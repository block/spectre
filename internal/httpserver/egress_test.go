package httpserver_test

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
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/protocol"
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
	harness.Start(t)

	reference := send(t, harness.Reference, dependencyHost, london)
	candidate := send(t, harness.Candidate, dependencyHost, london)

	assert.Equal(t, http.StatusCreated, reference.Status)
	assert.Equal(t, http.Header{"X-Checksum": []string{"abc"}}, reference.Trailer)
	assert.Equal(t, reference, candidate)
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, []matchRecord{{Event: "correlation", Protocol: "http", Route: "POST " + dependencyHost + forecastPath}},
		logRecords[matchRecord](t, harness.Logs, "Candidate request matched a reference request"))
}

func TestReplaysRawJSONWithoutDescriptors(t *testing.T) {
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "reference")
	})
	harness := newHarness(t, upstream)
	harness.Config.Descriptors.DescriptorsDir = ""
	harness.Start(t)

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
	harness.Start(t)

	candidate := make(chan responseView, 1)
	go func() { candidate <- send(t, harness.Candidate, dependencyHost, london) }()
	harness.WaitForCandidateHash(t)
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
			harness.Start(t)

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
		update    func(harness *harness)
		reference bool
		host      string
		reason    string
	}{
		"WindowExpiry": {
			update: func(harness *harness) { harness.Engine.MatchWindow = 50 * time.Millisecond },
			host:   dependencyHost,
			reason: "no matching reference request within 50ms",
		},
		"BudgetOverflow": {
			update:    func(harness *harness) { harness.Config.RecordingBufferBytes = 4 },
			reference: true,
			host:      dependencyHost,
			reason:    "reference response cannot be replayed: response exceeds the 4 byte recording budget",
		},
		"UnconfiguredHost": {
			update: func(*harness) {},
			host:   "other.example",
			reason: `candidate request to unconfigured host "other.example"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls.Store(0)
			harness := newHarness(t, upstream)
			test.update(harness)
			harness.Start(t)
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
	harness.Engine.MatchWindow = time.Second
	harness.Start(t)

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

// Requests that differ only in a path wildcard share the declared endpoint's name.
func TestPairsRequestsByDeclaredEndpointPattern(t *testing.T) {
	const pattern = "GET weather.example/v1/forecasts/{location}"
	upstream := newUpstream(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "forecast for "+path.Base(request.URL.Path))
	})
	harness := newHarness(t, upstream)
	harness.declareEndpoints(t, `spectre.egress.match<GetForecastRequest>("http", "`+pattern+`");`)
	harness.Start(t)

	for _, location := range []string{"london", "paris"} {
		getPath(t, harness.Reference, "/v1/forecasts/"+location)
	}
	candidates := []string{
		getPath(t, harness.Candidate, "/v1/forecasts/paris").Body,
		getPath(t, harness.Candidate, "/v1/forecasts/london").Body,
	}

	assert.Equal(t, []string{"forecast for paris", "forecast for london"}, candidates)
	assert.Equal(t, []matchRecord{
		{Event: "correlation", Protocol: "http", Route: pattern},
		{Event: "correlation", Protocol: "http", Route: pattern},
	}, waitForRecords[matchRecord](t, harness.Logs, "Candidate request matched a reference request", 2))
}

func TestServeShutsDownWhileCandidateWaits(t *testing.T) {
	upstream := newUpstream(t, func(http.ResponseWriter, *http.Request) {})
	harness := newHarness(t, upstream)
	harness.Engine.MatchWindow = time.Minute
	stop := harness.Start(t)

	candidate := make(chan responseView, 1)
	go func() { candidate <- send(t, harness.Candidate, dependencyHost, london) }()
	harness.WaitForCandidateHash(t)

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
	harness.Start(t)

	for _, path := range []string{"/livez", "/readyz"} {
		assert.Equal(t, "dependency", getPath(t, harness.Reference, path).Body)
	}

	assert.Equal(t, int32(2), calls.Load())
	assert.NotEqual(t, http.StatusNoContent, getPath(t, harness.Candidate, "/readyz").Status)
	assert.Equal(t, http.StatusNotFound, getPath(t, harness.Health, forecastPath).Status)
}

func TestEgressRejectsUnsafeConfiguration(t *testing.T) {
	for name, test := range map[string]struct {
		update  func(config *httpserver.EgressConfig, engineConfig *protocol.Config)
		message string
	}{
		"RemoteCandidateListener": {
			update:  func(config *httpserver.EgressConfig, _ *protocol.Config) { config.CandidateListen = "0.0.0.0:50061" },
			message: "candidate listener must use a loopback IP address or a unix socket",
		},
		"DestinationPort": {
			update: func(config *httpserver.EgressConfig, _ *protocol.Config) {
				config.Destinations = map[string]string{"weather.example:80": "http://127.0.0.1:9"}
			},
			message: `destination host "weather.example:80" must be a host name without a port`,
		},
		"DestinationLoop": {
			update: func(config *httpserver.EgressConfig, _ *protocol.Config) {
				config.Destinations = map[string]string{dependencyHost: "http://" + config.CandidateListen}
			},
			message: `destination "weather.example" must not target an egress listener`,
		},
		"InvalidDestination": {
			update: func(config *httpserver.EgressConfig, _ *protocol.Config) {
				config.Destinations = map[string]string{dependencyHost: "ftp://weather.example"}
			},
			message: `parse destination "weather.example"`,
		},
		"MatchWindow": {
			update:  func(_ *httpserver.EgressConfig, engineConfig *protocol.Config) { engineConfig.MatchWindow = 0 },
			message: "match window must be positive",
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := httpserver.NewEgressConfig()
			_, config.Descriptors.DescriptorsDir = writeSchemaAndDescriptors(t)
			engineConfig := newEgressEngineConfig(t)
			test.update(&config, &engineConfig)
			_, err := newEgress(t, config, engineConfig, slog.New(slog.DiscardHandler))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

// A declared endpoint that cannot be decoded is a startup error, not a proxy that never becomes ready.
func TestEgressRejectsUndecodableEndpoints(t *testing.T) {
	for name, test := range map[string]struct {
		declarations string
		descriptors  Option[*descriptorpb.FileDescriptorSet]
		message      string
	}{
		"UnknownWildcardField": {
			declarations: `spectre.egress.match<GetForecastRequest>("http", "GET weather.example/v1/{place}");`,
			message:      `bind endpoint "GET weather.example/v1/{place}" wildcard "place"`,
		},
		"DescriptorsDisagreeWithSchema": {
			// The schema declares spectre.sample.v1.GetForecastRequest with a location field.
			descriptors: Some(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
				Name: new("weather.proto"), Syntax: new("proto3"), Package: new("spectre.sample.v1"),
				MessageType: []*descriptorpb.DescriptorProto{{Name: new("GetForecastRequest")}},
			}}}),
			message: "configure request decoding: configure wire decoding",
		},
	} {
		t.Run(name, func(t *testing.T) {
			harness := newHarness(t, "http://127.0.0.1:9")
			harness.declareEndpoints(t, test.declarations)
			if set, ok := test.descriptors.Get(); ok {
				harness.Config.Descriptors.DescriptorsDir = writeDescriptorsDir(t, set)
			}

			_, err := newEgress(t, harness.Config, harness.Engine, slog.New(slog.DiscardHandler))

			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

// harness runs one egress proxy on loopback listeners in front of an upstream.
type harness struct {
	Config    httpserver.EgressConfig
	Engine    protocol.Config
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
	config := httpserver.NewEgressConfig()
	config.ReferenceListen = reference.Addr().String()
	config.CandidateListen = candidate.Addr().String()
	config.HealthListen = health.Addr().String()
	config.Destinations = map[string]string{dependencyHost: upstream}
	_, config.Descriptors.DescriptorsDir = writeSchemaAndDescriptors(t)
	return &harness{
		Config:    config,
		Engine:    newEgressEngineConfig(t),
		Reference: reference,
		Candidate: candidate,
		Health:    health,
		Logs:      &syncBuffer{},
	}
}

// Start serves until the returned function stops it.
func (h *harness) Start(t *testing.T) (stop func() error) {
	t.Helper()
	egressProxy, err := newEgress(t, h.Config, h.Engine, newLogger(h.Logs))
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

// declareEndpoints adds a script that declares more egress endpoints before the egress is built.
func (h *harness) declareEndpoints(t *testing.T, declarations string) {
	t.Helper()
	script := `
import * as spectre from "spectre";
import type { GetForecastRequest } from "spectre.sample.v1";
` + declarations
	assert.NoError(t, os.WriteFile(filepath.Join(h.Engine.Comparison.ScriptsDir, "endpoints.ts"), []byte(script), 0o600))
}

// QuarantineReason waits for the quarantine log, which is written asynchronously.
func (h *harness) QuarantineReason(t *testing.T) string {
	t.Helper()
	return waitForRecords[quarantineRecord](t, h.Logs, "Candidate quarantined", 1)[0].Reason
}

// WaitForCandidateHash waits until a candidate request is normalised, which
// happens just before it waits for a matching reference request.
func (h *harness) WaitForCandidateHash(t *testing.T) {
	t.Helper()
	type normalisationRecord struct {
		Side string `json:"side"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(logRecords[normalisationRecord](t, h.Logs, "Payload normalisation completed"), normalisationRecord{Side: "candidate"}) {
		if time.Now().After(deadline) {
			t.Fatal("candidate request was not hashed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newEgress builds an egress as the spectre-egress command does.
func newEgress(t *testing.T, config httpserver.EgressConfig, engineConfig protocol.Config, log *slog.Logger) (*httpserver.Egress, error) {
	t.Helper()
	return errors.WithStack2(httpserver.NewEgress(t.Context(), config, engineConfig, http.DefaultTransport, log))
}

// quarantineRecord is a "Candidate quarantined" log record.
type quarantineRecord struct {
	Event  string `json:"event"`
	Reason string `json:"reason"`
}

// matchRecord is a "Candidate request matched a reference request" log record.
type matchRecord struct {
	Event    string `json:"event"`
	Protocol string `json:"protocol"`
	Route    string `json:"route"`
}

// logRecords decodes every JSON log record with message as T.
func logRecords[T any](t *testing.T, logs *syncBuffer, message string) []T {
	t.Helper()
	records := []T{}
	for line := range strings.Lines(logs.String()) {
		var header struct {
			Message string `json:"msg"`
		}
		assert.NoError(t, json.Unmarshal([]byte(line), &header))
		if header.Message != message {
			continue
		}
		var record T
		assert.NoError(t, json.Unmarshal([]byte(line), &record))
		records = append(records, record)
	}
	return records
}

// waitForRecords polls until count records with message are logged, because
// quarantine and comparison log asynchronously.
func waitForRecords[T any](t *testing.T, logs *syncBuffer, message string, count int) []T {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		records := logRecords[T](t, logs, message)
		if len(records) >= count {
			return records
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q was logged %d times, not %d", message, len(records), count)
		}
		time.Sleep(10 * time.Millisecond)
	}
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

func newEgressEngineConfig(t *testing.T) protocol.Config {
	t.Helper()
	scripts := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(scripts, "egress.ts"), []byte(`
import * as spectre from "spectre";
import type { GetForecastRequest } from "spectre.sample.v1";
spectre.egress.match<GetForecastRequest>("http", "POST weather.example/spectre.sample.v1.WeatherService/GetForecast");
`), 0o600))
	config := protocol.NewConfig()
	config.Comparison.ScriptsDir = scripts
	config.Comparison.Schema.SchemaDir, _ = writeSchemaAndDescriptors(t)
	return config
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

// getPath makes a GET request for the dependency through one egress listener.
func getPath(t *testing.T, listener net.Listener, requestPath string) responseView {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+requestPath, nil)
	assert.NoError(t, err)
	return do(t, request, dependencyHost)
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
