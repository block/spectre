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
	"strconv"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/client"
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
	// ProxyFlags are added to the proxy's command line.
	ProxyFlags []string
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

// Main builds the spectre binary and the given bit targets once, unless -short
// skips the tests, then runs them. Call it from TestMain.
func Main(m *testing.M, targets ...string) int {
	flag.Parse()
	if !testing.Short() {
		if err := build(targets); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return m.Run()
}

// Start serves the mirror's services on free ports, runs the SPECTRE server and
// the proxy in front of them on free ports of their own, and waits until the proxy is ready.
func Start(t *testing.T, mirror Mirror) *Running {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a whole mirror")
	}
	watch := newWatch()
	reference, candidate := listen(t), listen(t)
	dependencies := map[string]net.Listener{}
	for host := range mirror.Dependencies {
		dependencies[host] = listen(t)
	}
	server := listen(t)
	serverURL := baseURL(server)
	watch.start(t, "server", []net.Listener{server}, "serve", "--listen=fd:3")

	// Each listener flag names the descriptor its listener is inherited as.
	listenerFlags := []string{"listen", "reference-egress-listen", "candidate-egress-listen", "egress-health-listen", "health-listen"}
	proxyListeners := make([]net.Listener, len(listenerFlags))
	args := []string{
		"proxy",
		"--reference-url=h2c://" + reference.Addr().String(), "--candidate-url=h2c://" + candidate.Addr().String(),
		"--server-url=" + serverURL, "--server-keepalive-interval=" + keepaliveInterval.String(),
		"--service=integration", "--environment=integration", "--revision=candidate",
	}
	for i, name := range listenerFlags {
		proxyListeners[i] = listen(t)
		args = append(args, "--"+name+"=fd:"+strconv.Itoa(3+i))
	}
	ingress, referenceEgress, candidateEgress, health := proxyListeners[0], proxyListeners[1], proxyListeners[2], proxyListeners[4]
	for host, listener := range dependencies {
		args = append(args, "--destination="+host+"="+baseURL(listener))
	}
	args = append(append(args, directoryFlags(t, mirror)...), mirror.ProxyFlags...)
	running := &Running{
		IngressURL:         baseURL(ingress),
		CandidateEgressURL: baseURL(candidateEgress),
		client:             newClient(t),
		server:             client.New(client.Config{URL: serverURL}),
		watch:              watch,
	}
	healthURL := baseURL(health) + "/readyz"
	serve(t, reference, mirror.Reference(egress(t, baseURL(referenceEgress))))
	serve(t, candidate, mirror.Candidate(egress(t, running.CandidateEgressURL)))
	for host, listener := range dependencies {
		serve(t, listener, mirror.Dependencies[host])
	}
	watch.start(t, "proxy", proxyListeners, args...)
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

// watch tracks a mirror's processes, so that waiting fails as soon as one exits.
type watch struct {
	processes []*process
}

func newWatch() *watch {
	return &watch{}
}

func (w *watch) start(t *testing.T, name string, listeners []net.Listener, args ...string) {
	t.Helper()
	w.processes = append(w.processes, startProcess(t, name, listeners, args...))
}

// await polls done until it holds, failing the test on timeout or when a
// process exits. The interval between polls doubles up to maxPollInterval.
func (w *watch) await(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	interval := minPollInterval
	for !done() {
		for _, p := range w.processes {
			if err := p.failure(); err != nil {
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

// directoryFlags passes the mirror's directories to the proxy as absolute paths.
func directoryFlags(t *testing.T, mirror Mirror) []string {
	t.Helper()
	flags := []string{}
	for flag, dir := range map[string]string{"scripts-dir": mirror.ScriptsDir, "schema-dir": mirror.SchemaDir, "descriptors-dir": mirror.DescriptorsDir} {
		if dir == "" {
			continue
		}
		absolute, err := filepath.Abs(dir)
		assert.NoError(t, err)
		flags = append(flags, "--"+flag+"="+absolute)
	}
	return flags
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

// build runs bit in the repository root for the spectre binary and targets.
func build(targets []string) error {
	root := repositoryRoot()
	command := exec.CommandContext(context.Background(), filepath.Join(root, "bin", "bit"), append([]string{"-q", "spectre-bin"}, targets...)...)
	command.Dir = root
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return errors.Wrap(command.Run(), "build spectre")
}

// repositoryRoot finds the root from this file's path, two directories down.
func repositoryRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}
