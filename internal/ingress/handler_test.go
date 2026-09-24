package ingress_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/ingress"
)

type requestView struct {
	Method string
	Path   string
	Query  string
	Header string
	Body   string
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

func TestStreamsRequestBodyToCandidate(t *testing.T) {
	candidateRead := make(chan string, 1)
	candidateReceived := make(chan struct{})
	referenceRead := make(chan string, 1)
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		data := make([]byte, len("request chunk"))
		n, err := request.Body.Read(data)
		assert.NoError(t, err)
		switch request.URL.Host {
		case "candidate.example":
			candidateRead <- string(data[:n])
			close(candidateReceived)
		case "reference.example":
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
	referenceURL, err := url.Parse("http://reference.example")
	assert.NoError(t, err)
	candidateURL, err := url.Parse("http://candidate.example")
	assert.NoError(t, err)
	handler := ingress.New(referenceURL, candidateURL, time.Second, transport, slog.New(slog.DiscardHandler))
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

func newTestHandler(t *testing.T, reference, candidate string) *ingress.Handler {
	t.Helper()
	referenceURL, err := url.Parse(reference)
	assert.NoError(t, err)
	candidateURL, err := url.Parse(candidate)
	assert.NoError(t, err)
	return ingress.New(referenceURL, candidateURL, time.Second, http.DefaultTransport, slog.New(slog.DiscardHandler))
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

type blockingBody struct {
	data   []byte
	closed chan struct{}
	once   sync.Once
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
