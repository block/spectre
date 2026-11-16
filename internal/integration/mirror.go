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
)

const (
	// startTimeout bounds how long Start waits for the mirror to be ready.
	startTimeout = 60 * time.Second
	// waitTimeout bounds how long a test waits for a record or a request.
	waitTimeout = 10 * time.Second
	// requestTimeout bounds each request a test sends.
	requestTimeout = 5 * time.Second
	pollInterval   = 50 * time.Millisecond
)

// proxyListeners name the proxy's listeners in its "Proxy listening" records.
const (
	ingressListener         = "ingress"
	referenceEgressListener = "reference-egress"
	candidateEgressListener = "candidate-egress"
	egressHealthListener    = "egress-health"
	healthListener          = "health"
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
	ServerURL          string
	Reference          *Recorder
	Candidate          *Recorder
	// Ingress, Egress and Server select what the proxies and the server log.
	Ingress      Log
	Egress       Log
	Server       Log
	dependencies map[string]*Recorder
	client       *http.Client
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
// the proxy in front of them, and waits until the proxy is ready.
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

	server := watch.start(t, "server", "serve", "--listen=127.0.0.1:0")
	serverURL := "http://" + address(t, newLog(server, Fields{}, watch), Fields{"msg": "SPECTRE server listening"})
	args := []string{
		"proxy", "--listen=127.0.0.1:0",
		"--reference-egress-listen=127.0.0.1:0", "--candidate-egress-listen=127.0.0.1:0",
		"--egress-health-listen=127.0.0.1:0", "--health-listen=127.0.0.1:0",
		"--reference-url=h2c://" + reference.Addr().String(), "--candidate-url=h2c://" + candidate.Addr().String(),
		"--server-url=" + serverURL, "--service=integration", "--environment=integration", "--revision=candidate",
	}
	for host, listener := range dependencies {
		args = append(args, "--destination="+host+"=http://"+listener.Addr().String())
	}
	args = append(append(args, directoryFlags(t, mirror)...), mirror.ProxyFlags...)
	proxy := watch.start(t, "proxy", args...)
	listening := newLog(proxy, Fields{"msg": "Proxy listening"}, watch)
	urls := map[string]string{}
	for _, name := range []string{ingressListener, referenceEgressListener, candidateEgressListener, egressHealthListener, healthListener} {
		urls[name] = "http://" + address(t, listening, Fields{"listener": name})
	}

	running := &Running{
		IngressURL:         urls[ingressListener],
		CandidateEgressURL: urls[candidateEgressListener],
		ServerURL:          serverURL,
		Reference:          newRecorder("the reference", mirror.Reference(egress(t, urls[referenceEgressListener])), watch),
		Candidate:          newRecorder("the candidate", mirror.Candidate(egress(t, urls[candidateEgressListener])), watch),
		Ingress:            newLog(proxy, Fields{"spectre.subsystem": "ingress"}, watch),
		Egress:             newLog(proxy, Fields{"spectre.subsystem": "egress"}, watch),
		Server:             newLog(server, Fields{}, watch),
		dependencies:       map[string]*Recorder{},
		client:             newClient(t),
	}
	serve(t, reference, running.Reference)
	serve(t, candidate, running.Candidate)
	for host, listener := range dependencies {
		running.dependencies[host] = newRecorder(host, mirror.Dependencies[host], watch)
		serve(t, listener, running.dependencies[host])
	}
	watch.await(t, startTimeout, "the proxy to be ready", func() bool {
		return running.ready(t, urls[healthListener]+"/readyz")
	})
	return running
}

// Dependency returns the recorder of the dependency serving host.
func (r *Running) Dependency(t *testing.T, host string) *Recorder {
	t.Helper()
	recorder, ok := r.dependencies[host]
	assert.True(t, ok, "no dependency serves %s", host)
	return recorder
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

func (w *watch) start(t *testing.T, name string, args ...string) *process {
	t.Helper()
	p := startProcess(t, name, args...)
	w.processes = append(w.processes, p)
	return p
}

// await polls done until it holds, failing the test on timeout or when a
// process exits.
func (w *watch) await(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		for _, p := range w.processes {
			if err := p.failure(); err != nil {
				t.Fatalf("%v while waiting for %s", err, what)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(pollInterval)
	}
}

// address waits for a record in log with fields, and returns its address.
func address(t *testing.T, log Log, fields Fields) string {
	t.Helper()
	value, ok := log.WaitFor(t, fields)["address"].(string)
	assert.True(t, ok, "%s record has no address", log.describe(log.selection(fields)))
	return value
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
