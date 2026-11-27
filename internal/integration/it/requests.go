//go:build integration

package it

import (
	"io"
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/engine"
)

// IngressEndpoint is where clients reach the mirrored service.
type IngressEndpoint struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newIngressEndpoint(t *testing.T, client *http.Client, base string) IngressEndpoint {
	return IngressEndpoint{t: t, client: client, base: base}
}

// URL is the base URL of the ingress listener.
func (i IngressEndpoint) URL() string {
	return i.base
}

// Get fetches path through ingress.
func (i IngressEndpoint) Get(path string) *Response {
	i.t.Helper()
	return get(i.t, i.client, i.base+path, "", http.Header{})
}

// EgressEndpoint is where a service reaches its dependencies.
type EgressEndpoint struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newEgressEndpoint(t *testing.T, client *http.Client, base string) EgressEndpoint {
	return EgressEndpoint{t: t, client: client, base: base}
}

// TestRequestID is the one mirrored request that a test's own egress calls and statements
// belong to, as if ingress had mirrored it.
func TestRequestID() engine.RequestID {
	return typeid.Must(typeid.Parse[engine.RequestID]("srid_01jx0000000000000000000000"))
}

// Get fetches path from the dependency at host through egress, as a call of the one
// mirrored request the test stands in for.
func (e EgressEndpoint) Get(host, path string) *Response {
	e.t.Helper()
	return get(e.t, e.client, e.base+path, host, http.Header{"Baggage": []string{engine.RequestIDKey + "=" + TestRequestID().String()}})
}

// GetUnmirrored fetches path from the dependency at host through egress, as a call
// that belongs to no mirrored request.
func (e EgressEndpoint) GetUnmirrored(host, path string) *Response {
	e.t.Helper()
	return get(e.t, e.client, e.base+path, host, http.Header{})
}

func (e EgressEndpoint) url() string {
	return e.base
}

// Response is the answer to a test's request.
type Response struct {
	t      *testing.T
	status int
	body   string
}

func newResponse(t *testing.T, status int, body string) *Response {
	return &Response{t: t, status: status, body: body}
}

// ExpectStatus checks the response's status.
func (r *Response) ExpectStatus(expected int) *Response {
	r.t.Helper()
	assert.Equal(r.t, expected, r.status, "status of a response with body %q", r.body)
	return r
}

// ExpectBodyContains checks that the response's body contains text.
func (r *Response) ExpectBodyContains(text string) *Response {
	r.t.Helper()
	assert.Contains(r.t, r.body, text)
	return r
}

// get fetches url with header, and with host in the Host header unless it is empty.
func get(t *testing.T, client *http.Client, url, host string, header http.Header) *Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	assert.NoError(t, err)
	request.Header = header
	if host != "" {
		request.Host = host
	}
	response, err := client.Do(request)
	assert.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	assert.NoError(t, err)
	return newResponse(t, response.StatusCode, string(body))
}
