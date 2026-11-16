package integration

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/model"
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
)

// Egress is where a service sends its dependency calls, with the transport to
// send them on.
type Egress struct {
	URL       *url.URL
	Transport http.RoundTripper
}

// Service builds the handler of a reference or candidate service.
type Service func(egress Egress) http.Handler

// Mirror defines the services a mirror runs and how the proxy compares them.
// Directories are relative to the test's working directory.
type Mirror struct {
	Reference Service
	Candidate Service
	// Dependencies serve the egress proxy's destinations, keyed by host.
	Dependencies   map[string]http.Handler
	ScriptsDir     string
	SchemaDir      string
	DescriptorsDir string
	// MatchWindow replaces the proxy's default when it is not zero.
	MatchWindow time.Duration
}

// Running is a started mirror, which stops when its test ends. URLs are the
// base URLs of its listeners.
type Running struct {
	IngressURL         string
	CandidateEgressURL string
	client             *http.Client
	server             *client.Client
	watch              *watch
}

// Main builds the given bit targets once, unless -short skips the tests, then
// runs them. Call it from TestMain.
func Main(m *testing.M, targets ...string) int {
	flag.Parse()
	if !testing.Short() && len(targets) > 0 {
		if err := build(targets); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return m.Run()
}

// Start serves the services, the SPECTRE server and the proxy on free ports in
// the test process, and waits until the proxy is ready. They stop with the test.
func Start(t *testing.T, mirror Mirror) *Running {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a whole mirror")
	}
	ctx := t.Context()
	watch := newWatch()
	server := listen(t)
	serverURL := baseURL(server)
	watch.run(ctx, t, "server", func(ctx context.Context) error {
		return errors.WithStack(spectreserver.ServeListener(ctx, spectreserver.NewServeConfig(), server))
	})

	reference, candidate := listen(t), listen(t)
	listeners := proxies.Listeners{Ingress: listen(t), ReferenceEgress: listen(t), CandidateEgress: listen(t), EgressHealth: listen(t), Health: listen(t)}
	running := &Running{
		IngressURL:         baseURL(listeners.Ingress),
		CandidateEgressURL: baseURL(listeners.CandidateEgress),
		client:             newClient(t),
		server:             client.New(client.Config{URL: serverURL}),
		watch:              watch,
	}
	serve(t, reference, mirror.Reference(egress(t, baseURL(listeners.ReferenceEgress))))
	serve(t, candidate, mirror.Candidate(egress(t, running.CandidateEgressURL)))
	destinations := map[string]string{}
	for host, handler := range mirror.Dependencies {
		dependency := listen(t)
		serve(t, dependency, handler)
		destinations[host] = baseURL(dependency)
	}

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
	config.Mirror.ScriptsDir = mirror.ScriptsDir
	config.Mirror.SchemaDir = mirror.SchemaDir
	config.Mirror.Descriptors.DescriptorsDir = mirror.DescriptorsDir
	if mirror.MatchWindow != 0 {
		config.Mirror.MatchWindow = mirror.MatchWindow
	}
	config.Mirror.Server.Client.URL = serverURL
	config.Mirror.Server.KeepaliveInterval = keepaliveInterval
	config.Mirror.Server.Service, config.Mirror.Server.Environment, config.Mirror.Server.Revision = "integration", "integration", "candidate"
	catalog, err := state.NewCatalog(httpserver.Supports()...)
	assert.NoError(t, err)
	proxy, err := proxies.New(ctx, config, catalog)
	assert.NoError(t, err)
	assert.NoError(t, proxy.StartListeners(ctx, listeners))
	watch.run(ctx, t, "proxy", func(ctx context.Context) error { return proxy.Wait(ctx) })

	healthURL := baseURL(listeners.Health) + "/readyz"
	watch.await(t, startTimeout, "the proxy to be ready", func() bool {
		return running.ready(t, healthURL)
	})
	return running
}

// Eventually waits until condition holds, polling it with backoff. It fails the
// test after a timeout, or as soon as the proxy or the server exits.
func (r *Running) Eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	r.watch.await(t, waitTimeout, what, condition)
}

// WaitForState waits until the state the proxy reported to the SPECTRE server
// satisfies condition, and returns it.
func (r *Running) WaitForState(t *testing.T, what string, condition func(state model.State) bool) model.State {
	t.Helper()
	var matched model.State
	r.Eventually(t, what, func() bool {
		states, _, err := r.server.ListDivergences(t.Context(), model.DivergenceFilter{})
		assert.NoError(t, err)
		for _, state := range states {
			if condition(state) {
				matched = state
				return true
			}
		}
		return false
	})
	return matched
}

// WaitForCalls waits until the proxy reports at least the expected compared
// calls on each route, or a quarantine, and returns its state.
func (r *Running) WaitForCalls(t *testing.T, expected map[model.Route]uint64) model.State {
	t.Helper()
	return r.WaitForState(t, fmt.Sprintf("compared calls %v", expected), func(state model.State) bool {
		if len(state.Quarantines) > 0 {
			return true
		}
		for route, calls := range expected {
			if state.Calls.Routes[route] < calls {
				return false
			}
		}
		return true
	})
}

// WaitForQuarantine waits until the proxy reports a quarantine in direction,
// and returns its reason.
func (r *Running) WaitForQuarantine(t *testing.T, direction model.Direction) model.QuarantineReason {
	t.Helper()
	state := r.WaitForState(t, fmt.Sprintf("an %s quarantine", direction), func(state model.State) bool {
		_, ok := state.Quarantines[direction]
		return ok
	})
	return state.Quarantines[direction].Reason
}

// Client returns a client that speaks HTTP/2 without TLS, as gRPC needs.
func (r *Running) Client() *http.Client {
	return r.client
}

// Get fetches url, returning the response's status and body.
func (r *Running) Get(t *testing.T, url string) (status int, body string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	assert.NoError(t, err)
	return r.Do(t, request)
}

// Do sends request, returning the response's status and body.
func (r *Running) Do(t *testing.T, request *http.Request) (status int, body string) {
	t.Helper()
	response, err := r.client.Do(request)
	assert.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	assert.NoError(t, err)
	return response.StatusCode, string(data)
}

func (r *Running) ready(t *testing.T, url string) bool {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	assert.NoError(t, err)
	response, err := r.client.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusNoContent
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

// egress returns where a service reaches the egress listener at base.
func egress(t *testing.T, base string) Egress {
	t.Helper()
	target, err := url.Parse(base)
	assert.NoError(t, err)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return Egress{URL: target, Transport: transport}
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: requestTimeout}
}

// build runs bit in the repository root for targets.
func build(targets []string) error {
	root := repositoryRoot()
	command := exec.CommandContext(context.Background(), filepath.Join(root, "bin", "bit"), append([]string{"-q"}, targets...)...)
	command.Dir = root
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return errors.Wrap(command.Run(), "build the test's dependencies")
}

// repositoryRoot finds the root from this file's path, two directories down.
func repositoryRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}
