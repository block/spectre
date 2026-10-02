package egress_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/egress"
)

const (
	dependencyHost = "weather.example"
	forecastPath   = "/spectre.sample.v1.WeatherService/GetForecast"
	london         = `{"location":"london"}`
)

type responseView struct {
	Status  int
	Header  http.Header
	Body    string
	Trailer http.Header
}

func TestReplaysReferenceResponseToCandidate(t *testing.T) {
	var calls atomic.Int32
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Trailer", "X-Checksum")
		writer.Header().Set("X-Upstream", "reference")
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, `{"forecast":{}}`)
		writer.Header().Set("X-Checksum", "abc")
	})
	harness := newHarness(t, upstream)
	harness.Start(t, nil)

	reference := send(t, harness.Reference, dependencyHost, london)
	candidate := send(t, harness.Candidate, dependencyHost, london)

	assert.Equal(t, http.StatusCreated, reference.Status)
	assert.Equal(t, http.Header{"X-Checksum": []string{"abc"}}, reference.Trailer)
	assert.Equal(t, reference, candidate)
	assert.Equal(t, int32(1), calls.Load())
	assert.Contains(t, harness.Logs.String(), `"level":"DEBUG","msg":"Candidate request matched a reference request","host":"`+dependencyHost+`","path":"/spectre.sample.v1.WeatherService/GetForecast"`)
}

func TestReplaysRawJSONWithoutDescriptors(t *testing.T) {
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "reference")
	})
	harness := newHarness(t, upstream)
	harness.Config.Descriptors.DescriptorsDir = ""
	harness.Start(t, nil)

	reference := send(t, harness.Reference, dependencyHost, london)
	candidate := send(t, harness.Candidate, dependencyHost, london)

	assert.Equal(t, reference, candidate)
	assert.False(t, strings.Contains(harness.Logs.String(), "Candidate quarantined"))
}

func TestCandidateWaitsForReference(t *testing.T) {
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "reference")
	})
	harness := newHarness(t, upstream)
	hashed := make(chan struct{}, 2)
	harness.Start(t, hashed)

	candidate := make(chan responseView, 1)
	go func() { candidate <- send(t, harness.Candidate, dependencyHost, london) }()
	<-hashed
	send(t, harness.Reference, dependencyHost, london)

	assert.Equal(t, "reference", (<-candidate).Body)
	assert.False(t, strings.Contains(harness.Logs.String(), "Candidate quarantined"))
}

func TestForwardsReferenceTrafficItCannotIdentify(t *testing.T) {
	var calls atomic.Int32
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, "forwarded")
	})
	for name, test := range map[string]struct {
		host   string
		body   string
		reason string
	}{
		"UnconfiguredHost":   {host: strings.TrimPrefix(upstream, "http://"), body: london, reason: "reference request to unconfigured host"},
		"UndecodablePayload": {host: dependencyHost, body: `{"unknown":1}`, reason: "identify reference request"},
	} {
		t.Run(name, func(t *testing.T) {
			calls.Store(0)
			harness := newHarness(t, upstream)
			harness.Start(t, nil)

			response := send(t, harness.Reference, test.host, test.body)

			assert.Equal(t, "forwarded", response.Body)
			assert.Equal(t, int32(1), calls.Load())
			assert.Contains(t, harness.QuarantineReason(t), test.reason)
			assert.Equal(t, http.StatusServiceUnavailable, send(t, harness.Candidate, dependencyHost, london).Status)
		})
	}
}

func TestQuarantinesUnmatchedCandidate(t *testing.T) {
	var calls atomic.Int32
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, "a response larger than the budget")
	})
	for name, test := range map[string]struct {
		update    func(config *egress.Config)
		reference bool
		host      string
		reason    string
	}{
		"WindowExpiry": {
			update: func(config *egress.Config) { config.MatchWindow = 50 * time.Millisecond },
			host:   dependencyHost,
			reason: "no matching reference request within 50ms",
		},
		"BudgetOverflow": {
			update:    func(config *egress.Config) { config.RecordingBufferBytes = 4 },
			reference: true,
			host:      dependencyHost,
			reason:    "reference response exceeds the recording budget",
		},
		"UnconfiguredHost": {
			update: func(*egress.Config) {},
			host:   "other.example",
			reason: `candidate request to unconfigured host "other.example"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls.Store(0)
			harness := newHarness(t, upstream)
			test.update(&harness.Config)
			harness.Start(t, nil)
			if test.reference {
				send(t, harness.Reference, dependencyHost, london)
			}

			response := send(t, harness.Candidate, test.host, london)

			assert.Equal(t, http.StatusBadGateway, response.Status)
			assert.Contains(t, harness.QuarantineReason(t), test.reason)
			assert.Equal(t, http.StatusServiceUnavailable, send(t, harness.Candidate, dependencyHost, london).Status)
			expectedCalls := int32(0)
			if test.reference {
				expectedCalls = 1
			}
			assert.Equal(t, expectedCalls, calls.Load())
		})
	}
}

func TestPairsDuplicateRequestsOneToOne(t *testing.T) {
	var calls atomic.Int32
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, []string{"first", "second"}[calls.Add(1)-1])
	})
	harness := newHarness(t, upstream)
	harness.Config.MatchWindow = time.Second
	harness.Start(t, nil)

	send(t, harness.Reference, dependencyHost, london)
	send(t, harness.Reference, dependencyHost, london)
	// Hashing runs beside forwarding, so the two references may register in either order.
	replayed := []string{
		send(t, harness.Candidate, dependencyHost, london).Body,
		send(t, harness.Candidate, dependencyHost, london).Body,
	}
	slices.Sort(replayed)
	unmatched := send(t, harness.Candidate, dependencyHost, london)

	assert.Equal(t, []string{"first", "second"}, replayed)
	assert.Equal(t, http.StatusBadGateway, unmatched.Status)
	assert.Contains(t, harness.QuarantineReason(t), "no matching reference request")
}

func TestServeShutsDownWhileCandidateWaits(t *testing.T) {
	upstream := newUpstream(t, func(http.ResponseWriter, *http.Request) {})
	harness := newHarness(t, upstream)
	harness.Config.MatchWindow = time.Minute
	hashed := make(chan struct{}, 1)
	stop := harness.Start(t, hashed)

	candidate := make(chan responseView, 1)
	go func() { candidate <- send(t, harness.Candidate, dependencyHost, london) }()
	<-hashed

	assert.NoError(t, stop())
	assert.Equal(t, http.StatusBadGateway, (<-candidate).Status)
	assert.False(t, strings.Contains(harness.Logs.String(), "Candidate quarantined"))
}

func TestForwardsDependencyHealthPaths(t *testing.T) {
	var calls atomic.Int32
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, "dependency")
	})
	harness := newHarness(t, upstream)
	harness.Start(t, nil)

	for _, path := range []string{"/livez", "/readyz"} {
		assert.Equal(t, "dependency", get(t, harness.Reference, dependencyHost, path).Body)
	}

	assert.Equal(t, int32(2), calls.Load())
	assert.NotEqual(t, http.StatusNoContent, get(t, harness.Candidate, dependencyHost, "/readyz").Status)
	assert.Equal(t, http.StatusNotFound, get(t, harness.Health, dependencyHost, forecastPath).Status)
}

func TestRejectsUnsafeConfiguration(t *testing.T) {
	for name, test := range map[string]struct {
		update  func(config *egress.Config)
		message string
	}{
		"RemoteCandidateListener": {
			update:  func(config *egress.Config) { config.CandidateListen = "0.0.0.0:50061" },
			message: "candidate listener must use a loopback IP address or a unix socket",
		},
		"DestinationPort": {
			update: func(config *egress.Config) {
				config.Destinations = map[string]string{"weather.example:80": "http://127.0.0.1:9"}
			},
			message: `destination host "weather.example:80" must be a host name without a port`,
		},
		"DestinationLoop": {
			update: func(config *egress.Config) {
				config.Destinations = map[string]string{dependencyHost: "http://" + config.CandidateListen}
			},
			message: `destination "weather.example" must not target an egress listener`,
		},
		"InvalidDestination": {
			update: func(config *egress.Config) {
				config.Destinations = map[string]string{dependencyHost: "ftp://weather.example"}
			},
			message: `parse destination "weather.example"`,
		},
		"MatchWindow": {
			update:  func(config *egress.Config) { config.MatchWindow = 0 },
			message: "match window must be positive",
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := egress.NewConfig()
			_, config.Descriptors.DescriptorsDir = writeSchemaAndDescriptors(t)
			test.update(&config)
			_, err := egress.New(config, http.DefaultTransport, newHasher(t), slog.New(slog.DiscardHandler))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

// harness runs one egress proxy on loopback listeners in front of an upstream.
type harness struct {
	Config    egress.Config
	Reference net.Listener
	Candidate net.Listener
	Health    net.Listener
	Logs      *syncBuffer
}

func newHarness(t *testing.T, upstream string) *harness {
	t.Helper()
	reference := listen(t)
	candidate := listen(t)
	health := listen(t)
	config := egress.NewConfig()
	config.ReferenceListen = reference.Addr().String()
	config.CandidateListen = candidate.Addr().String()
	config.HealthListen = health.Addr().String()
	config.Destinations = map[string]string{dependencyHost: upstream}
	_, config.Descriptors.DescriptorsDir = writeSchemaAndDescriptors(t)
	return &harness{Config: config, Reference: reference, Candidate: candidate, Health: health, Logs: &syncBuffer{}}
}

// Start serves until the returned function stops it. A non-nil hashed channel
// receives a value after each request hash.
func (h *harness) Start(t *testing.T, hashed chan<- struct{}) (stop func() error) {
	t.Helper()
	var hasher egress.RequestHasher = newHasher(t)
	if hashed != nil {
		hasher = newSignallingHasher(hasher, hashed)
	}
	log := slog.New(slog.NewJSONHandler(h.Logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	egressProxy, err := egress.New(h.Config, http.DefaultTransport, hasher, log)
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- egressProxy.Serve(ctx, h.Reference, h.Candidate, h.Health) }()
	stop = sync.OnceValue(func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("egress did not stop after cancellation")
			return nil
		}
	})
	t.Cleanup(func() { assert.NoError(t, stop()) })
	waitReady(t, h.Health)
	return stop
}

// QuarantineReason waits for the quarantine log, which is written asynchronously.
func (h *harness) QuarantineReason(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for line := range strings.Lines(h.Logs.String()) {
			var record struct {
				Message string `json:"msg"`
				Reason  string `json:"reason"`
			}
			assert.NoError(t, json.Unmarshal([]byte(line), &record))
			if record.Message == "Candidate quarantined" {
				return record.Reason
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("candidate was not quarantined")
	return ""
}

// signallingHasher reports each finished hash so tests can order requests.
type signallingHasher struct {
	egress.RequestHasher
	hashed chan<- struct{}
}

func newSignallingHasher(hasher egress.RequestHasher, hashed chan<- struct{}) *signallingHasher {
	return &signallingHasher{RequestHasher: hasher, hashed: hashed}
}

func (h *signallingHasher) Hash(ctx context.Context, request comparison.Request) (comparison.RequestHash, error) {
	hash, err := h.RequestHasher.Hash(ctx, request)
	h.hashed <- struct{}{}
	return hash, err
}

// syncBuffer collects log output written from request and quarantine goroutines.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func newUpstream(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

func newHasher(t *testing.T) *comparison.RequestHasher {
	t.Helper()
	scripts := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(scripts, "egress.ts"), []byte(`
import * as spectre from "spectre";
import type { GetForecastRequest } from "spectre.sample.v1";
spectre.egress<GetForecastRequest>("http", "POST weather.example/spectre.sample.v1.WeatherService/GetForecast");
`), 0o600))
	config := comparison.NewConfig()
	config.ScriptsDir = scripts
	config.Schema.SchemaDir, _ = writeSchemaAndDescriptors(t)
	hasher, err := comparison.NewRequestHasher(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	return hasher
}

func writeSchemaAndDescriptors(t *testing.T) (schemaDir string, descriptorsDir string) {
	t.Helper()
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: new("weather.proto"), Syntax: new("proto3"), Package: new("spectre.sample.v1"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("GetForecastRequest"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: new("location"), Number: new(int32(1)), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}},
		}},
	}}}
	declarations, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	schemaDir = t.TempDir()
	for name, declaration := range declarations {
		target := filepath.Join(schemaDir, filepath.FromSlash(name))
		assert.NoError(t, os.MkdirAll(filepath.Dir(target), 0o750))
		assert.NoError(t, os.WriteFile(target, []byte(declaration), 0o600))
	}
	data, err := proto.Marshal(set)
	assert.NoError(t, err)
	descriptorsDir = t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(descriptorsDir, "weather.pb"), data, 0o600))
	return schemaDir, descriptorsDir
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	return listener
}

func waitReady(t *testing.T, listener net.Listener) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + listener.Addr().String() + "/readyz")
		assert.NoError(t, err)
		assert.NoError(t, response.Body.Close())
		if response.StatusCode == http.StatusNoContent {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("egress did not become ready")
}

// send makes a raw JSON request for host through one egress listener.
func send(t *testing.T, listener net.Listener, host string, body string) responseView {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+forecastPath, strings.NewReader(body))
	assert.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	return do(t, request, host)
}

func get(t *testing.T, listener net.Listener, host string, path string) responseView {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+path, nil)
	assert.NoError(t, err)
	return do(t, request, host)
}

func do(t *testing.T, request *http.Request, host string) responseView {
	t.Helper()
	request.Host = host
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Do(request)
	assert.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	assert.NoError(t, err)
	return responseView{Status: response.StatusCode, Header: response.Header, Body: string(data), Trailer: response.Trailer}
}
