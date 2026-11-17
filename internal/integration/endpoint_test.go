//go:build integration

package integration_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
)

// sender sends a test's requests to one of the mirror's listeners.
type sender struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newSender(t *testing.T, client *http.Client, base string) sender {
	return sender{t: t, client: client, base: base}
}

// get fetches path, with host in the Host header unless it is empty.
func (s sender) get(host, path string) *response {
	s.t.Helper()
	request, err := http.NewRequestWithContext(s.t.Context(), http.MethodGet, s.base+path, nil)
	assert.NoError(s.t, err)
	if host != "" {
		request.Host = host
	}
	response, err := s.client.Do(request)
	assert.NoError(s.t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	assert.NoError(s.t, err)
	return newResponse(s.t, response.StatusCode, string(body))
}

// ingressEndpoint is where clients reach the mirrored service.
type ingressEndpoint struct {
	sender
}

// get fetches path through ingress.
func (i ingressEndpoint) get(path string) *response {
	i.t.Helper()
	return i.sender.get("", path)
}

// egressEndpoint is where a service reaches its dependencies.
type egressEndpoint struct {
	sender
}

// get fetches path from the dependency at host through egress.
func (e egressEndpoint) get(host, path string) *response {
	e.t.Helper()
	return e.sender.get(host, path)
}

// response is the answer to a test's request.
type response struct {
	t      *testing.T
	status int
	body   string
}

func newResponse(t *testing.T, status int, body string) *response {
	return &response{t: t, status: status, body: body}
}

// expectStatus checks the response's status.
func (r *response) expectStatus(expected int) *response {
	r.t.Helper()
	assert.Equal(r.t, expected, r.status, "status of a response with body %q", r.body)
	return r
}

// expectBodyContains checks that the response's body contains text.
func (r *response) expectBodyContains(text string) *response {
	r.t.Helper()
	assert.Contains(r.t, r.body, text)
	return r
}
