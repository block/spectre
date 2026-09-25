package ingress_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/ingress"
)

type requestView struct {
	Method string
	Path   string
	Query  string
	Header string
	Body   string
}

type forwardingView struct {
	Host           string
	Target         string
	Forwarded      string
	ForwardedFor   string
	ForwardedHost  string
	ForwardedPort  string
	ForwardedProto string
	RealIP         string
}

func TestForwardsRequestToBothBackends(t *testing.T) {
	referenceRequests := make(chan requestView, 1)
	candidateRequests := make(chan requestView, 1)
	candidateReceived := make(chan struct{})
	candidate := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		candidateRequests <- readRequest(t, request)
		close(candidateReceived)
		writer.Header().Set("X-Backend", "candidate")
		writer.WriteHeader(http.StatusTeapot)
		_, err := writer.Write([]byte("candidate response"))
		assert.NoError(t, err)
	}))
	t.Cleanup(candidate.Close)
	reference := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		referenceRequests <- readRequest(t, request)
		<-candidateReceived
		writer.Header().Set("X-Backend", "reference")
		writer.WriteHeader(http.StatusCreated)
		_, err := writer.Write([]byte("reference response"))
		assert.NoError(t, err)
	}))
	t.Cleanup(reference.Close)

	handler := newTestHandler(t, reference.URL, candidate.URL)
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)
	request, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/items?mode=full", strings.NewReader("request body"))
	assert.NoError(t, err)
	request.Header.Set("X-Test", "mirror-me")
	response, err := http.DefaultClient.Do(request)
	assert.NoError(t, err)
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	assert.NoError(t, err)

	expectedRequest := requestView{
		Method: http.MethodPost,
		Path:   "/v1/items",
		Query:  "mode=full",
		Header: "mirror-me",
		Body:   "request body",
	}
	assert.Equal(t, expectedRequest, <-referenceRequests)
	assert.Equal(t, expectedRequest, <-candidateRequests)
	assert.Equal(t, http.StatusCreated, response.StatusCode)
	assert.Equal(t, "reference", response.Header.Get("X-Backend"))
	assert.Equal(t, "reference response", string(responseBody))
}

func TestUsesConfiguredH2CProtocol(t *testing.T) {
	referenceProtocol := make(chan int, 1)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	referenceServer := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			referenceProtocol <- request.ProtoMajor
			writer.WriteHeader(http.StatusNoContent)
		}),
		Protocols: protocols,
	}
	go func() {
		if err := referenceServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve h2c backend: %v", err)
		}
	}()
	t.Cleanup(func() { assert.NoError(t, referenceServer.Close()) })
	candidate := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(candidate.Close)
	config := newTestConfig("h2c://"+listener.Addr().String(), candidate.URL)
	transport := ingress.NewTransport(config)
	t.Cleanup(transport.CloseIdleConnections)
	handler, err := ingress.New(config, transport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)

	response, err := http.Get(proxy.URL)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
	assert.NoError(t, response.Body.Close())
	select {
	case protocol := <-referenceProtocol:
		assert.Equal(t, 2, protocol)
	case <-time.After(time.Second):
		t.Fatal("h2c backend did not receive request")
	}
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestCandidateDoesNotDelayReferenceResponse(t *testing.T) {
	candidateStarted := make(chan struct{})
	releaseCandidate := make(chan struct{})
	candidate := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(candidateStarted)
		<-releaseCandidate
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(candidate.Close)
	reference := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(reference.Close)
	handler := newTestHandler(t, reference.URL, candidate.URL)
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)

	result := make(chan error, 1)
	go func() {
		response, err := http.Get(proxy.URL)
		if err == nil {
			response.Body.Close()
		}
		result <- err
	}()
	select {
	case <-candidateStarted:
	case <-time.After(time.Second):
		t.Fatal("candidate request did not start")
	}
	select {
	case err := <-result:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("reference response waited for candidate")
	}
	close(releaseCandidate)
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestComparisonDoesNotDelayReferenceAndDivergenceQuarantines(t *testing.T) {
	comparisonStarted := make(chan struct{})
	releaseComparison := make(chan struct{})
	comparator := newResponseComparator(
		func(
			ctx context.Context,
			requestPath string,
			requestContentType string,
			reference comparison.Response,
			candidate comparison.Response,
		) comparison.Result {
			_, _, _, _, _ = ctx, requestPath, requestContentType, reference, candidate
			close(comparisonStarted)
			<-releaseComparison
			return comparison.NewDifferenceResult("$.name")
		},
	)
	var callsMu sync.Mutex
	candidateCalls := 0
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "127.0.0.1:50052" {
			callsMu.Lock()
			candidateCalls++
			callsMu.Unlock()
		}
		return noContentResponse(request), nil
	})
	messages := make(chan string, 8)
	config := newTestConfig("http://127.0.0.1:50051", "http://127.0.0.1:50052")
	handler, err := ingress.New(config, transport, matchingDescriptorLoader(), comparator, slog.New(newObservedLogHandler(messages)))
	assert.NoError(t, err)
	firstDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://proxy.example/first", nil))
		close(firstDone)
	}()

	select {
	case <-comparisonStarted:
	case <-time.After(time.Second):
		t.Fatal("response comparison did not start")
	}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("reference response waited for comparison")
	}
	close(releaseComparison)

quarantine:
	for {
		select {
		case message := <-messages:
			if message == "Candidate quarantined" {
				break quarantine
			}
		case <-time.After(time.Second):
			t.Fatal("divergent comparison did not quarantine the candidate")
		}
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://proxy.example/second", nil))
	callsMu.Lock()
	assert.Equal(t, 1, candidateCalls)
	callsMu.Unlock()
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestServeStopsAfterContextCancellation(t *testing.T) {
	reference := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(reference.Close)
	candidate := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(candidate.Close)
	handler := newTestHandler(t, reference.URL, candidate.URL)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	t.Cleanup(func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close listener: %v", err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() { serveDone <- handler.Serve(ctx, listener) }()

	response, err := http.Get("http://" + listener.Addr().String())
	assert.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
	assert.NoError(t, response.Body.Close())
	cancel()
	select {
	case err := <-serveDone:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("ingress server did not stop after cancellation")
	}
}

func TestHealthEndpointsAreLocalAndTrackReadiness(t *testing.T) {
	backendRequests := make(chan struct{}, 1)
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		backendRequests <- struct{}{}
		return noContentResponse(request), nil
	})
	var logs bytes.Buffer
	config := newTestConfig("http://127.0.0.1:50051", "http://127.0.0.1:50052")
	handler, err := ingress.New(config, transport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.NewJSONHandler(&logs, nil)))
	assert.NoError(t, err)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/livez", nil))
	assert.Equal(t, http.StatusNoContent, response.Code)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() { serveDone <- handler.Serve(ctx, listener) }()
	healthResponse, err := http.Get("http://" + listener.Addr().String() + "/livez")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, healthResponse.StatusCode)
	assert.NoError(t, healthResponse.Body.Close())
	deadline := time.Now().Add(time.Second)
	for {
		healthResponse, err = http.Get("http://" + listener.Addr().String() + "/readyz")
		assert.NoError(t, err)
		assert.NoError(t, healthResponse.Body.Close())
		if healthResponse.StatusCode == http.StatusNoContent {
			break
		}
		assert.Equal(t, http.StatusServiceUnavailable, healthResponse.StatusCode)
		if time.Now().After(deadline) {
			t.Fatal("ingress did not become ready after descriptor comparison")
		}
	}
	select {
	case <-backendRequests:
		t.Fatal("health request reached a backend")
	default:
	}

	cancel()
	select {
	case err := <-serveDone:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("ingress server did not stop after cancellation")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.False(t, strings.Contains(logs.String(), `"msg":"HTTP request"`))
}

func TestDescriptorMismatchKeepsServerUnreadyAndLogs(t *testing.T) {
	config := newTestConfig("http://127.0.0.1:50051", "http://127.0.0.1:50052")
	descriptors := descriptorLoaderFunc(func(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error) {
		_ = ctx
		return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{Name: &endpoint}}}, nil
	})
	messages := make(chan string, 2)
	handler, err := ingress.New(config, http.DefaultTransport, descriptors, newTestComparator(t), slog.New(newObservedLogHandler(messages)))
	assert.NoError(t, err)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() { serveDone <- handler.Serve(ctx, listener) }()

comparison:
	for {
		select {
		case message := <-messages:
			if message == "Backend descriptors differ" {
				break comparison
			}
		case <-time.After(time.Second):
			t.Fatal("descriptor mismatch was not logged")
		}
	}
	response, err := http.Get("http://" + listener.Addr().String() + "/readyz")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.NoError(t, response.Body.Close())
	cancel()
	select {
	case err := <-serveDone:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("ingress server did not stop after cancellation")
	}
}

func TestServeDrainsReferenceAfterContextCancellation(t *testing.T) {
	referenceContext := make(chan context.Context, 1)
	releaseReference := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReference) }) }
	defer release()
	reference := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		referenceContext <- request.Context()
		<-releaseReference
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(reference.Close)
	candidate := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(candidate.Close)
	config := newTestConfig(reference.URL, candidate.URL)
	config.ShutdownTimeout = time.Second
	handler, err := ingress.New(config, http.DefaultTransport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	observedListener := newCloseObservedListener(listener)
	ctx, cancel := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() { serveDone <- handler.Serve(ctx, observedListener) }()
	clientDone := make(chan struct {
		status int
		err    error
	}, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		status := 0
		if err == nil {
			status = response.StatusCode
			err = response.Body.Close()
		}
		clientDone <- struct {
			status int
			err    error
		}{status: status, err: err}
	}()
	var requestContext context.Context
	select {
	case requestContext = <-referenceContext:
	case <-time.After(time.Second):
		t.Fatal("reference request did not start")
	}
	cancel()
	select {
	case <-observedListener.closedSignal():
	case <-time.After(time.Second):
		t.Fatal("ingress listener did not close during shutdown")
	}
	assert.NoError(t, requestContext.Err())
	release()
	select {
	case result := <-clientDone:
		assert.NoError(t, result.err)
		assert.Equal(t, http.StatusNoContent, result.status)
	case <-time.After(time.Second):
		t.Fatal("reference response did not finish during graceful shutdown")
	}
	select {
	case err := <-serveDone:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("ingress server did not finish graceful shutdown")
	}
}

func TestStreamsRequestBodyToCandidate(t *testing.T) {
	candidateRead := make(chan string, 1)
	candidateReceived := make(chan struct{})
	referenceRead := make(chan string, 1)
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		data := make([]byte, len("request chunk"))
		n, err := request.Body.Read(data)
		assert.NoError(t, err)
		switch request.URL.Host {
		case "127.0.0.1:50052":
			candidateRead <- string(data[:n])
			close(candidateReceived)
		case "127.0.0.1:50051":
			referenceRead <- string(data[:n])
			select {
			case <-candidateReceived:
			case <-time.After(time.Second):
				return nil, errors.New("candidate did not receive the streaming request body")
			}
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})
	handler := newTestHandlerWithTransport(t, "http://127.0.0.1:50051", "http://127.0.0.1:50052", transport)
	body := newBlockingBody([]byte("request chunk"))
	t.Cleanup(func() { _ = body.Close() })
	request := httptest.NewRequest(http.MethodPost, "http://proxy.example/stream", body)
	request.ContentLength = -1
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reference response waited for the request body to end")
	}

	assert.Equal(t, "request chunk", <-referenceRead)
	assert.Equal(t, "request chunk", <-candidateRead)
	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestCandidateBufferOverflowQuarantinesCandidate(t *testing.T) {
	candidateStarted := make(chan struct{})
	candidateCancelled := make(chan struct{})
	referenceBody := make(chan string, 2)
	var candidateCalls int
	var callsMu sync.Mutex
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "127.0.0.1:50052" {
			callsMu.Lock()
			candidateCalls++
			callsMu.Unlock()
			close(candidateStarted)
			<-request.Context().Done()
			close(candidateCancelled)
			return nil, errors.Wrap(request.Context().Err(), "candidate request")
		}
		<-candidateStarted
		data, err := io.ReadAll(request.Body)
		assert.NoError(t, err)
		referenceBody <- string(data)
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})
	config := ingress.NewConfig()
	config.Reference = "http://127.0.0.1:50051"
	config.Candidate = "http://127.0.0.1:50052"
	config.CandidateBufferBytes = 4
	handler, err := ingress.New(config, transport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "http://proxy.example/overflow", strings.NewReader("12345"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, "12345", <-referenceBody)
	assert.Equal(t, http.StatusNoContent, response.Code)
	select {
	case <-candidateCancelled:
	case <-time.After(time.Second):
		t.Fatal("candidate was not cancelled after exceeding the shared buffer")
	}
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, httptest.NewRequest(http.MethodPost, "http://proxy.example/quarantined", strings.NewReader("later")))
	assert.Equal(t, "later", <-referenceBody)
	assert.Equal(t, http.StatusNoContent, secondResponse.Code)
	callsMu.Lock()
	assert.Equal(t, 1, candidateCalls)
	callsMu.Unlock()
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestDefaultBufferSupportsCandidateConcurrencyLimit(t *testing.T) {
	candidateStarted := make(chan struct{})
	releaseCandidates := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCandidates) }) }
	defer release()
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "127.0.0.1:50052" {
			candidateStarted <- struct{}{}
			select {
			case <-releaseCandidates:
				return noContentResponse(request), nil
			case <-request.Context().Done():
				return nil, errors.Wrap(request.Context().Err(), "candidate request")
			}
		}
		_, err := io.ReadAll(request.Body)
		assert.NoError(t, err)
		return noContentResponse(request), nil
	})
	config := newTestConfig("http://127.0.0.1:50051", "http://127.0.0.1:50052")
	config.CandidateTimeout = 10 * time.Second
	handler, err := ingress.New(config, transport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.DiscardHandler))
	assert.NoError(t, err)

	for range config.CandidateMaxInFlight {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://proxy.example/body", strings.NewReader("x")))
		select {
		case <-candidateStarted:
		case <-time.After(time.Second):
			t.Fatal("default buffer quarantined the candidate before reaching its concurrency limit")
		}
	}
	release()
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestReferenceEarlyCloseAbortsCandidateWithoutQuarantine(t *testing.T) {
	candidateError := make(chan error, 1)
	laterCandidate := make(chan struct{}, 1)
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "127.0.0.1:50052" {
			var err error
			if request.Body != nil {
				_, err = io.ReadAll(request.Body)
			}
			if request.URL.Path == "/early" {
				candidateError <- err
				return nil, errors.Wrap(err, "read candidate request")
			}
			laterCandidate <- struct{}{}
			return noContentResponse(request), nil
		}
		if request.URL.Path == "/early" {
			assert.NoError(t, request.Body.Close())
		}
		return noContentResponse(request), nil
	})
	handler := newTestHandlerWithTransport(t, "http://127.0.0.1:50051", "http://127.0.0.1:50052", transport)
	earlyRequest := httptest.NewRequest(http.MethodPost, "http://proxy.example/early", strings.NewReader("unread body"))
	earlyRequest.ContentLength = -1
	handler.ServeHTTP(httptest.NewRecorder(), earlyRequest)
	select {
	case err := <-candidateError:
		assert.IsError(t, err, io.ErrUnexpectedEOF)
	case <-time.After(time.Second):
		t.Fatal("candidate request was not aborted after the reference closed its body")
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://proxy.example/later", nil))
	select {
	case <-laterCandidate:
	case <-time.After(time.Second):
		t.Fatal("reference body close quarantined the candidate")
	}
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestRewritesUntrustedForwardingHeaders(t *testing.T) {
	referenceRequest := make(chan forwardingView, 1)
	candidateRequest := make(chan forwardingView, 1)
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		view := forwardingView{
			Host:           request.Host,
			Target:         request.URL.Host,
			Forwarded:      request.Header.Get("Forwarded"),
			ForwardedFor:   request.Header.Get("X-Forwarded-For"),
			ForwardedHost:  request.Header.Get("X-Forwarded-Host"),
			ForwardedPort:  request.Header.Get("X-Forwarded-Port"),
			ForwardedProto: request.Header.Get("X-Forwarded-Proto"),
			RealIP:         request.Header.Get("X-Real-IP"),
		}
		if request.URL.Host == "127.0.0.1:50052" {
			candidateRequest <- view
		} else {
			referenceRequest <- view
		}
		return noContentResponse(request), nil
	})
	handler := newTestHandlerWithTransport(t, "http://127.0.0.1:50051", "http://127.0.0.1:50052", transport)
	request := httptest.NewRequest(http.MethodGet, "http://attacker.example/test", nil)
	request.Header.Set("Forwarded", "for=spoofed;host=spoofed;proto=https")
	request.Header.Set("X-Forwarded-For", "spoofed")
	request.Header.Set("X-Forwarded-Host", "spoofed")
	request.Header.Set("X-Forwarded-Port", "443")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Real-IP", "spoofed")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, forwardingView{
		Target:         "127.0.0.1:50051",
		ForwardedFor:   "192.0.2.1",
		ForwardedProto: "http",
	}, <-referenceRequest)
	assert.Equal(t, forwardingView{
		Target:         "127.0.0.1:50052",
		ForwardedFor:   "192.0.2.1",
		ForwardedProto: "http",
	}, <-candidateRequest)
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestCandidateConcurrencyLimitQuarantinesAllRequests(t *testing.T) {
	candidateStarted := make(chan struct{}, 2)
	candidateCancelled := make(chan struct{}, 2)
	var candidateCalls int
	var callsMu sync.Mutex
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "127.0.0.1:50052" {
			callsMu.Lock()
			candidateCalls++
			callsMu.Unlock()
			candidateStarted <- struct{}{}
			<-request.Context().Done()
			candidateCancelled <- struct{}{}
		}
		return noContentResponse(request), nil
	})
	config := newTestConfig("http://127.0.0.1:50051", "http://127.0.0.1:50052")
	config.CandidateMaxInFlight = 2
	handler, err := ingress.New(config, transport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.DiscardHandler))
	assert.NoError(t, err)

	for _, path := range []string{"first", "second"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://proxy.example/"+path, nil))
		select {
		case <-candidateStarted:
		case <-time.After(time.Second):
			t.Fatal("candidate request did not start")
		}
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://proxy.example/limit", nil))
	for range 2 {
		select {
		case <-candidateCancelled:
		case <-time.After(time.Second):
			t.Fatal("quarantine did not cancel every candidate request")
		}
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://proxy.example/quarantined", nil))
	callsMu.Lock()
	assert.Equal(t, 2, candidateCalls)
	callsMu.Unlock()
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestRejectsIngressRequestsOverCapacity(t *testing.T) {
	referenceStarted := make(chan struct{})
	releaseReference := make(chan struct{})
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "127.0.0.1:50051" {
			close(referenceStarted)
			<-releaseReference
		}
		return noContentResponse(request), nil
	})
	config := newTestConfig("http://127.0.0.1:50051", "http://127.0.0.1:50052")
	config.MaxInFlightRequests = 1
	handler, err := ingress.New(config, transport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	firstDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://proxy.example/first", nil))
		close(firstDone)
	}()
	select {
	case <-referenceStarted:
	case <-time.After(time.Second):
		t.Fatal("reference request did not start")
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://proxy.example/second", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	close(releaseReference)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
	assert.NoError(t, handler.Shutdown(t.Context()))
}

func TestRejectsUnsafeConfiguration(t *testing.T) {
	tests := map[string]struct {
		update  func(*ingress.Config)
		message string
	}{
		"CandidateMustBeLoopback": {
			update:  func(config *ingress.Config) { config.Candidate = "http://candidate.example" },
			message: "literal loopback IP",
		},
		"BackendsMustDiffer": {
			update:  func(config *ingress.Config) { config.Candidate = config.Reference },
			message: "must be different",
		},
		"BackendUserInformation": {
			update:  func(config *ingress.Config) { config.Reference = "http://user@127.0.0.1:50051" },
			message: "cannot contain user information",
		},
		"BackendPath": {
			update:  func(config *ingress.Config) { config.Reference = "http://127.0.0.1:50051/base" },
			message: "cannot contain a path",
		},
		"CandidateTimeout": {
			update:  func(config *ingress.Config) { config.CandidateTimeout = 0 },
			message: "candidate timeout must be positive",
		},
		"ReflectionTimeout": {
			update:  func(config *ingress.Config) { config.ReflectionTimeout = 0 },
			message: "reflection timeout must be positive",
		},
		"CandidateTargetsIngress": {
			update: func(config *ingress.Config) {
				config.Listen = "127.0.0.1:50050"
				config.Candidate = "http://127.0.0.1:50050"
			},
			message: "must not target the ingress listener",
		},
		"ReferenceTargetsIngress": {
			update: func(config *ingress.Config) {
				config.Listen = "127.0.0.1:50050"
				config.Reference = "http://localhost:50050"
			},
			message: "must not target the ingress listener",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			config := newTestConfig("http://127.0.0.1:50051", "http://127.0.0.1:50052")
			test.update(&config)
			_, err := ingress.New(config, http.DefaultTransport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.DiscardHandler))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func newTestHandler(t *testing.T, reference, candidate string) *ingress.Handler {
	t.Helper()
	return newTestHandlerWithTransport(t, reference, candidate, http.DefaultTransport)
}

func newTestHandlerWithTransport(t *testing.T, reference, candidate string, transport http.RoundTripper) *ingress.Handler {
	t.Helper()
	config := newTestConfig(reference, candidate)
	handler, err := ingress.New(config, transport, matchingDescriptorLoader(), newTestComparator(t), slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	return handler
}

func newTestConfig(reference, candidate string) ingress.Config {
	config := ingress.NewConfig()
	config.Listen = "127.0.0.1:0"
	config.Reference = reference
	config.Candidate = candidate
	config.CandidateTimeout = time.Second
	return config
}

func newTestComparator(t *testing.T) *comparison.Comparator {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comparison.js")
	assert.NoError(t, os.WriteFile(path, []byte(`import * as spectre from "spectre";`), 0o600))
	config := comparison.NewConfig()
	config.ComparisonScript = path
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	return comparator
}

func noContentResponse(request *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    request,
	}
}

func readRequest(t *testing.T, request *http.Request) requestView {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	assert.NoError(t, err)
	return requestView{
		Method: request.Method,
		Path:   request.URL.Path,
		Query:  request.URL.RawQuery,
		Header: request.Header.Get("X-Test"),
		Body:   string(body),
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type descriptorLoaderFunc func(context.Context, string) (*descriptorpb.FileDescriptorSet, error)

func (f descriptorLoaderFunc) Load(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error) {
	return f(ctx, endpoint)
}

type responseComparator struct {
	compare func(
		context.Context,
		string,
		string,
		comparison.Response,
		comparison.Response,
	) comparison.Result
}

func newResponseComparator(compare func(
	context.Context,
	string,
	string,
	comparison.Response,
	comparison.Response,
) comparison.Result) *responseComparator {
	return &responseComparator{compare: compare}
}

func (c *responseComparator) MaxResponseBytes() int {
	return 1024
}

func (c *responseComparator) Configure(ctx context.Context, set *descriptorpb.FileDescriptorSet) error {
	_, _ = ctx, set
	return nil
}

func (c *responseComparator) Compare(
	ctx context.Context,
	requestPath string,
	requestContentType string,
	reference comparison.Response,
	candidate comparison.Response,
) comparison.Result {
	return c.compare(ctx, requestPath, requestContentType, reference, candidate)
}

func matchingDescriptorLoader() descriptorLoaderFunc {
	return func(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error) {
		_, _ = ctx, endpoint
		return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
			Name:   new("test.proto"),
			Syntax: new("proto3"),
		}}}, nil
	}
}

type observedLogHandler struct {
	messages chan<- string
}

func newObservedLogHandler(messages chan<- string) *observedLogHandler {
	return &observedLogHandler{messages: messages}
}

func (h *observedLogHandler) Enabled(ctx context.Context, level slog.Level) (enabled bool) {
	_, _ = ctx, level
	return true
}

func (h *observedLogHandler) Handle(ctx context.Context, record slog.Record) error {
	_ = ctx
	h.messages <- record.Message
	return nil
}

func (h *observedLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	_ = attrs
	return h
}

func (h *observedLogHandler) WithGroup(name string) slog.Handler {
	_ = name
	return h
}

type blockingBody struct {
	data   []byte
	closed chan struct{}
	once   sync.Once
}

type closeObservedListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func newCloseObservedListener(listener net.Listener) *closeObservedListener {
	return &closeObservedListener{Listener: listener, closed: make(chan struct{})}
}

func (l *closeObservedListener) closedSignal() <-chan struct{} {
	return l.closed
}

func (l *closeObservedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

func newBlockingBody(data []byte) *blockingBody {
	return &blockingBody{data: data, closed: make(chan struct{})}
}

func (b *blockingBody) Read(data []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(data, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	<-b.closed
	return 0, io.EOF
}

func (b *blockingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}
