//go:build integration

package it

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/grpcreflect/v2"
	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver"
	"github.com/block/spectre/internal/proxies"
	spectreserver "github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/state"
)

const (
	// startTimeout bounds how long Start waits for the mirror to be ready.
	startTimeout = 60 * time.Second
	// waitTimeout bounds how long a test waits for a condition.
	waitTimeout = 10 * time.Second
	// requestTimeout bounds each request a test sends.
	requestTimeout = 5 * time.Second
	// Polls start at minPollInterval and back off to maxPollInterval.
	minPollInterval = 5 * time.Millisecond
	maxPollInterval = 500 * time.Millisecond
	// keepaliveInterval is how soon the proxy reports changed call counts.
	keepaliveInterval = 20 * time.Millisecond
	// defaultMatchWindow keeps tests that wait out the match window fast.
	defaultMatchWindow = 500 * time.Millisecond
)

// Egress is where a service sends its dependency calls, with the transport to
// send them on.
type Egress struct {
	URL       *url.URL
	Transport http.RoundTripper
}

// Backend is what a service registers its handlers on. Start serves the gRPC
// services, with reflection, next to the HTTP routes.
type Backend struct {
	Egress Egress
	GRPC   *connect.Server
	HTTP   *http.ServeMux
	// MySQL reaches the mirror's database through the proxy, when the mirror has one.
	MySQL Option[*sql.DB]
}

// Service registers the handlers of a reference or candidate service.
type Service func(t *testing.T, backend Backend)

// DependencyHandler builds the handler of a dependency.
type DependencyHandler func(t *testing.T) http.Handler

// Calls are the compared calls the proxy reports, by route.
type Calls map[model.Route]uint64

// Setting configures the mirror Start runs.
type Setting func(setup *setup)

// setup is what Start runs. Directories are relative to the test's
// working directory.
type setup struct {
	reference      Option[Service]
	candidate      Option[Service]
	dependencies   map[string]DependencyHandler
	scripts        []string
	scriptsDir     string
	schemas        []string
	schemaDir      string
	descriptorsDir string
	matchWindow    time.Duration
	// mysql holds the statements that set up the mirror's database, if it has one.
	mysql Option[[]string]
	// mysqlSavedRepliesSize overrides the bytes of reference replies the MySQL proxy saves.
	mysqlSavedRepliesSize Option[int]
}

func newSetup() *setup {
	return &setup{
		dependencies:   map[string]DependencyHandler{},
		schemaDir:      "../sample/schema",
		descriptorsDir: "../../dist/descriptors",
		matchWindow:    defaultMatchWindow,
	}
}

// Reference sets the reference service.
func Reference(service Service) Setting {
	return func(setup *setup) { setup.reference = Some(service) }
}

// Candidate sets the candidate service.
func Candidate(service Service) Setting {
	return func(setup *setup) { setup.candidate = Some(service) }
}

// Dependency serves a dependency that the services reach through egress as host.
func Dependency(host string, handler DependencyHandler) Setting {
	return func(setup *setup) { setup.dependencies[host] = handler }
}

// Scripts adds a comparison script, without the indentation its lines share.
func Scripts(source string) Setting {
	return func(setup *setup) { setup.scripts = append(setup.scripts, source) }
}

// Schema declares TypeScript types for the scripts, next to the sample's schema.
func Schema(source string) Setting {
	return func(setup *setup) { setup.schemas = append(setup.schemas, source) }
}

// MySQL gives the services a database of their own on the shared MySQL server, set up
// by statements, which they reach through the proxy.
func MySQL(statements ...string) Setting {
	return func(setup *setup) { setup.mysql = Some(statements) }
}

// MySQLSavedRepliesBytes sets how many bytes of reference replies the MySQL proxy saves.
func MySQLSavedRepliesBytes(size int) Setting {
	return func(setup *setup) { setup.mysqlSavedRepliesSize = Some(size) }
}

// ScriptsDir loads the comparison scripts in dir instead.
func ScriptsDir(dir string) Setting {
	return func(setup *setup) { setup.scriptsDir = dir }
}

// services returns the reference and candidate services, which every mirror needs.
func (s *setup) services(t *testing.T) (reference, candidate Service) {
	t.Helper()
	reference, hasReference := s.reference.Get()
	candidate, hasCandidate := s.candidate.Get()
	if !hasReference || !hasCandidate {
		t.Fatal("a mirror needs a reference and a candidate")
	}
	return reference, candidate
}

// serveDependencies serves each dependency until the test ends, and returns the
// requests each receives and the proxy's destinations, by host.
func (s *setup) serveDependencies(t *testing.T) (received map[string]*Counter, destinations map[string]string) {
	t.Helper()
	received, destinations = map[string]*Counter{}, map[string]string{}
	for host, handler := range s.dependencies {
		listener := listen(t)
		counter := newCounter(t)
		serve(t, listener, counter.wrap(handler(t)))
		received[host], destinations[host] = counter, baseURL(listener)
	}
	return received, destinations
}

// createDatabase creates the mirror's MySQL database, if it has one, and returns how
// the proxy reaches it. It opens the proxy's MySQL listeners in listeners.
func (s *setup) createDatabase(t *testing.T, w *watch, listeners *proxies.Listeners) (mysqlserver.Config, Option[*Database]) {
	t.Helper()
	statements, ok := s.mysql.Get()
	if !ok {
		return mysqlserver.NewConfig(), None[*Database]()
	}
	database := newDatabase(t, w, statements)
	reference, candidate := listen(t), listen(t)
	listeners.MySQLReference, listeners.MySQLCandidate = Some(reference), Some(candidate)
	config := database.connect(reference, candidate)
	config.SavedRepliesBytes = s.mysqlSavedRepliesSize.Default(config.SavedRepliesBytes)
	return config, Some(database)
}

// writeScripts returns the directory of the comparison scripts, writing those
// given inline to a temporary one.
func (s *setup) writeScripts(t *testing.T) string {
	t.Helper()
	switch {
	case len(s.scripts) == 0 && s.scriptsDir == "":
		t.Fatal("a mirror needs comparison scripts")
	case len(s.scripts) == 0:
		return s.scriptsDir
	case s.scriptsDir != "":
		t.Fatal("give comparison scripts inline or as a directory, not both")
	}
	dir := t.TempDir()
	for i, source := range s.scripts {
		assert.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("script%d.ts", i)), []byte(dedent(source)), 0o600))
	}
	return dir
}

// writeSchema returns the directory of the schema. Types given inline are written to a
// temporary one, next to a copy of the schema the descriptors need.
func (s *setup) writeSchema(t *testing.T) string {
	t.Helper()
	if len(s.schemas) == 0 {
		return s.schemaDir
	}
	dir := t.TempDir()
	assert.NoError(t, os.CopyFS(dir, os.DirFS(s.schemaDir)))
	for i, source := range s.schemas {
		assert.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("inline%d.d.ts", i)), []byte(dedent(source)), 0o600))
	}
	return dir
}

// proxyConfig configures the proxy in front of the services on listeners.
func (s *setup) proxyConfig(listeners proxies.Listeners, reference, candidate net.Listener,
	destinations map[string]string, scriptsDir, schemaDir, serverURL string,
) proxies.ProxyConfig {
	config := proxies.ProxyConfig{
		Listen:                listeners.Ingress.Addr().String(),
		ReferenceURL:          "h2c://" + reference.Addr().String(),
		CandidateURL:          "h2c://" + candidate.Addr().String(),
		ReferenceEgressListen: listeners.ReferenceEgress.Addr().String(),
		CandidateEgressListen: listeners.CandidateEgress.Addr().String(),
		EgressHealthListen:    listeners.EgressHealth.Addr().String(),
		HealthListen:          listeners.Health.Addr().String(),
		Mirror:                proxies.NewConfig(),
	}
	config.Mirror.Destinations = destinations
	config.Mirror.ScriptsDir = scriptsDir
	config.Mirror.SchemaDir = schemaDir
	config.Mirror.Descriptors.DescriptorsDir = s.descriptorsDir
	config.Mirror.MatchWindow = s.matchWindow
	config.Mirror.Server.Client.URL = serverURL
	config.Mirror.Server.KeepaliveInterval = keepaliveInterval
	config.Mirror.Server.Service, config.Mirror.Server.Environment, config.Mirror.Server.Revision = "integration", "integration", "candidate"
	return config
}

// Mirror is a running mirror, which stops when its test ends.
type Mirror struct {
	// Ingress is where clients reach the mirrored service.
	Ingress IngressEndpoint
	// ReferenceEgress and CandidateEgress are where each service reaches its dependencies.
	ReferenceEgress EgressEndpoint
	CandidateEgress EgressEndpoint
	// Reference and Candidate count the requests that reach each service.
	Reference    *Counter
	Candidate    *Counter
	t            *testing.T
	mysql        Option[*Database]
	dependencies map[string]*Counter
	client       *http.Client
	server       *client.Client
	watch        *watch
}

// Start runs a mirror for a parallel test: its services, the SPECTRE server and
// the proxy, on free ports in the test process.
func Start(t *testing.T, settings ...Setting) *Mirror {
	t.Helper()
	t.Parallel()
	setup := newSetup()
	for _, setting := range settings {
		setting(setup)
	}
	referenceService, candidateService := setup.services(t)
	scriptsDir, schemaDir := setup.writeScripts(t), setup.writeSchema(t)
	ctx := t.Context()
	server := listen(t)
	serverURL := baseURL(server)
	httpClient := newClient(t)
	m := &Mirror{
		t:      t,
		client: httpClient,
		server: client.New(client.Config{URL: serverURL}),
		watch:  newWatch(),
	}
	m.watch.run(ctx, t, "server", func(ctx context.Context) error {
		return errors.WithStack(spectreserver.ServeListener(ctx, spectreserver.NewServeConfig(), server))
	})

	listeners := proxies.Listeners{Ingress: listen(t), ReferenceEgress: listen(t), CandidateEgress: listen(t), EgressHealth: listen(t), Health: listen(t)}
	m.Ingress = newIngressEndpoint(t, httpClient, baseURL(listeners.Ingress))
	m.ReferenceEgress = newEgressEndpoint(t, httpClient, baseURL(listeners.ReferenceEgress))
	m.CandidateEgress = newEgressEndpoint(t, httpClient, baseURL(listeners.CandidateEgress))
	mysqlConfig, database := setup.createDatabase(t, m.watch, &listeners)
	m.mysql = database
	referenceListener, candidateListener := listen(t), listen(t)
	m.Reference = serveService(t, referenceListener, referenceService, m.ReferenceEgress.url(), m.mysqlHandle(false))
	m.Candidate = serveService(t, candidateListener, candidateService, m.CandidateEgress.url(), m.mysqlHandle(true))
	dependencies, destinations := setup.serveDependencies(t)
	m.dependencies = dependencies

	proxyConfig := setup.proxyConfig(listeners, referenceListener, candidateListener, destinations, scriptsDir, schemaDir, serverURL)
	proxyConfig.Mirror.MySQL = mysqlConfig
	catalog, err := state.NewCatalog(httpserver.Supports()...)
	assert.NoError(t, err)
	proxy, err := proxies.New(ctx, proxyConfig, catalog)
	assert.NoError(t, err)
	assert.NoError(t, proxy.StartListeners(ctx, listeners))
	m.watch.run(ctx, t, "proxy", func(ctx context.Context) error { return proxy.Wait(ctx) })

	healthURL := baseURL(listeners.Health) + "/readyz"
	m.watch.await(t, startTimeout, "the proxy to be ready", func() bool {
		return m.ready(healthURL)
	})
	return m
}

// Client returns the client the mirror sends requests with, which speaks
// HTTP/2 without TLS, as gRPC needs.
func (m *Mirror) Client() *http.Client {
	return m.client
}

// MySQL returns the mirror's database.
func (m *Mirror) MySQL() *Database {
	m.t.Helper()
	database, ok := m.mysql.Get()
	if !ok {
		m.t.Fatal("the mirror has no MySQL database")
	}
	return database
}

// mysqlHandle returns where the reference or the candidate reaches the mirror's database, if it has one.
func (m *Mirror) mysqlHandle(candidate bool) Option[*sql.DB] {
	database, ok := m.mysql.Get()
	switch {
	case !ok:
		return None[*sql.DB]()
	case candidate:
		return Some(database.Candidate)
	}
	return Some(database.Reference)
}

// Dependency returns the requests that reached the dependency at host.
func (m *Mirror) Dependency(host string) *Counter {
	m.t.Helper()
	received, ok := m.dependencies[host]
	if !ok {
		m.t.Fatalf("no dependency %s", host)
	}
	return received
}

// ExpectCalls waits until the proxy reports the expected compared calls, and
// checks that they are all it reports, with no quarantine.
func (m *Mirror) ExpectCalls(expected Calls) {
	m.t.Helper()
	state := m.waitForState(fmt.Sprintf("compared calls %v", expected), func(state model.State) bool {
		if len(state.Quarantines) > 0 {
			return true
		}
		for route, count := range expected {
			if state.Calls.Routes[route] < count {
				return false
			}
		}
		return true
	})
	assert.Equal(m.t, map[model.Direction]model.Quarantine(nil), state.Quarantines)
	assert.Equal(m.t, model.Calls{Routes: expected}, state.Calls)
}

// ExpectQuarantine waits until the proxy reports a quarantine in the direction
// of the expected reason's traffic, and checks its reason.
func (m *Mirror) ExpectQuarantine(expected model.QuarantineReason) {
	m.t.Helper()
	traffic, hasTraffic := expected.Traffic.Get()
	if !hasTraffic {
		m.t.Fatal("expect a quarantine reason with traffic")
	}
	direction := traffic.Direction
	state := m.waitForState(fmt.Sprintf("an %s quarantine", direction), func(state model.State) bool {
		_, ok := state.Quarantines[direction]
		return ok
	})
	assert.Equal(m.t, expected, state.Quarantines[direction].Reason)
}

// waitForState waits until the state the proxy reported to the SPECTRE server
// satisfies condition, and returns it.
func (m *Mirror) waitForState(what string, condition func(state model.State) bool) model.State {
	m.t.Helper()
	var matched model.State
	m.watch.await(m.t, waitTimeout, what, func() bool {
		status, err := m.server.ListDivergences(m.t.Context(), model.DivergenceFilter{})
		assert.NoError(m.t, err)
		for _, state := range status.States {
			if condition(state) {
				matched = state
				return true
			}
		}
		return false
	})
	return matched
}

func (m *Mirror) ready(url string) bool {
	m.t.Helper()
	request, err := http.NewRequestWithContext(m.t.Context(), http.MethodGet, url, nil)
	assert.NoError(m.t, err)
	response, err := m.client.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusNoContent
}

// UnmatchedEgress is the reason for an egress call on route that no call from
// the other service matched.
func UnmatchedEgress(route model.Route, detail string) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: route}),
		Detail:  detail,
	}
}

// UnpairedEgress is the reason for an egress call on route that could not be paired at all.
func UnpairedEgress(route model.Route, detail string) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:    model.QuarantinePairing,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: route}),
		Detail:  detail,
	}
}

// DivergentIngress is the reason for responses on route that differ as
// differences describe.
func DivergentIngress(route model.Route, differences ...model.Difference) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:        model.QuarantineDivergent,
		Traffic:     Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: route}),
		Differences: differences,
	}
}

// Counter counts the requests a service or dependency receives, by path.
type Counter struct {
	t     *testing.T
	mu    sync.Mutex
	paths map[string]int
}

func newCounter(t *testing.T) *Counter {
	return &Counter{t: t, paths: map[string]int{}}
}

// wrap counts each request to next as it arrives.
func (c *Counter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		c.mu.Lock()
		c.paths[request.URL.Path]++
		c.mu.Unlock()
		next.ServeHTTP(writer, request)
	})
}

// ExpectReceived checks how many requests for path have arrived so far.
func (c *Counter) ExpectReceived(path string, expected int) {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Equal(c.t, expected, c.paths[path], "requests for %s", path)
}

// watch tracks the server and proxy, so that waiting fails as soon as one stops.
type watch struct {
	components []*component
}

func newWatch() *watch {
	return &watch{}
}

// run runs a component until the test ends, and waits for it to stop.
func (w *watch) run(ctx context.Context, t *testing.T, name string, run func(ctx context.Context) error) {
	t.Helper()
	c := newComponent(name)
	go c.run(ctx, run)
	t.Cleanup(c.wait)
	w.components = append(w.components, c)
}

// await polls done until it holds, failing the test on timeout or when a
// component stops. The interval between polls doubles up to maxPollInterval.
func (w *watch) await(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	interval := minPollInterval
	for !done() {
		for _, c := range w.components {
			if err := c.failure(); err != nil {
				t.Fatalf("%v while waiting for %s", err, what)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(min(interval, time.Until(deadline)))
		interval = min(2*interval, maxPollInterval)
	}
}

// serveService serves service on listener, with its calls through the egress
// listener at egressURL, and counts the requests it receives.
func serveService(t *testing.T, listener net.Listener, service Service, egressURL string, mysql Option[*sql.DB]) *Counter {
	t.Helper()
	server := connect.NewServer()
	mux := http.NewServeMux()
	service(t, Backend{Egress: newEgress(t, egressURL), GRPC: server, HTTP: mux, MySQL: mysql})
	grpcreflect.Register(server)
	connecthttp.Mount(mux, server)
	received := newCounter(t)
	serve(t, listener, received.wrap(mux))
	return received
}

// listen opens a listener on a free loopback port, closed when the test ends.
func listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func baseURL(listener net.Listener) string {
	return "http://" + listener.Addr().String()
}

// serve serves handler on listener over HTTP/1 and HTTP/2 without TLS until the test ends.
func serve(t *testing.T, listener net.Listener, handler http.Handler) {
	t.Helper()
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{Handler: handler, Protocols: protocols, ReadHeaderTimeout: requestTimeout}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve %s: %v", listener.Addr(), err)
		}
	})
}

// newEgress returns where a service reaches the egress listener at base.
func newEgress(t *testing.T, base string) Egress {
	t.Helper()
	target, err := url.Parse(base)
	assert.NoError(t, err)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return Egress{URL: target, Transport: transport}
}

// newClient returns a client that speaks HTTP/2 without TLS, as gRPC needs.
func newClient(t *testing.T) *http.Client {
	t.Helper()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: requestTimeout}
}

// dedent removes the blank lines around source and the indentation its lines share.
func dedent(source string) string {
	lines := strings.Split(strings.Trim(strings.TrimRight(source, " \t"), "\n"), "\n")
	prefix, found := "", false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if !found {
			prefix, found = indent, true
		}
		for !strings.HasPrefix(indent, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, prefix)
	}
	return strings.Join(lines, "\n") + "\n"
}
