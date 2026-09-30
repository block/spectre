package route_test

import (
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/route"
)

func TestMatchesRequestsToValues(t *testing.T) {
	routes := route.New[string]()
	assert.NoError(t, routes.Add("GET /v2/forecast", "v2"))
	assert.NoError(t, routes.Add("POST /v1/locations/{location}/alerts", "update"))
	assert.NoError(t, routes.Add("POST /test.v1.Weather/Get", "rpc"))
	assert.NoError(t, routes.Add("HEAD /v1/status", "status"))
	assert.NoError(t, routes.Add("GET weather.example/v2/forecast", "host"))
	assert.NoError(t, routes.Add("GET weather.example/v1/files/{path...}", "files"))
	for name, test := range map[string]struct {
		method    string
		host      string
		path      string
		expected  string
		wildcards map[string]string
		matched   bool
	}{
		"MethodAndPath":    {method: http.MethodGet, path: "/v2/forecast", expected: "v2", wildcards: map[string]string{}, matched: true},
		"Head":             {method: http.MethodHead, path: "/v1/status", expected: "status", wildcards: map[string]string{}, matched: true},
		"GetIsNotHead":     {method: http.MethodHead, path: "/v2/forecast"},
		"Wildcard":         {method: http.MethodPost, path: "/v1/locations/london/alerts", expected: "update", wildcards: map[string]string{"location": "london"}, matched: true},
		"EscapedSlash":     {method: http.MethodPost, path: "/v1/locations/a%2Fb/alerts", expected: "update", wildcards: map[string]string{"location": "a/b"}, matched: true},
		"InvalidEscape":    {method: http.MethodGet, path: "/v2/forecast%zz"},
		"RPCPath":          {method: http.MethodPost, path: "/test.v1.Weather/Get", expected: "rpc", wildcards: map[string]string{}, matched: true},
		"WrongMethod":      {method: http.MethodPost, path: "/v2/forecast"},
		"UnknownPath":      {method: http.MethodGet, path: "/v3/forecast"},
		"TrailingSlash":    {method: http.MethodGet, path: "/v2/forecast/"},
		"UncleanPath":      {method: http.MethodGet, path: "/v2/../v2/forecast"},
		"Host":             {method: http.MethodGet, host: "weather.example", path: "/v2/forecast", expected: "host", wildcards: map[string]string{}, matched: true},
		"HostWithPort":     {method: http.MethodGet, host: "weather.example:8080", path: "/v2/forecast", expected: "host", wildcards: map[string]string{}, matched: true},
		"OtherHost":        {method: http.MethodGet, host: "other.example", path: "/v2/forecast", expected: "v2", wildcards: map[string]string{}, matched: true},
		"HostOnly":         {method: http.MethodGet, path: "/v1/files/a/b"},
		"MultipleSegments": {method: http.MethodGet, host: "weather.example", path: "/v1/files/a/b", expected: "files", wildcards: map[string]string{"path": "a/b"}, matched: true},
	} {
		t.Run(name, func(t *testing.T) {
			value, wildcards, matched := routes.Match(test.method, test.host, test.path)
			assert.Equal(t, test.expected, value)
			assert.Equal(t, test.wildcards, wildcards)
			assert.Equal(t, test.matched, matched)
		})
	}
}

func TestRejectsInvalidPatterns(t *testing.T) {
	for name, test := range map[string]struct {
		patterns []string
		message  string
	}{
		"NoMethod":       {patterns: []string{"/forecast"}, message: `route pattern "/forecast" must have the form "<METHOD> [<host>]/<path>"`},
		"Empty":          {patterns: []string{" "}, message: `must have the form "<METHOD> [<host>]/<path>"`},
		"InvalidPattern": {patterns: []string{"GET /forecast/{"}, message: "invalid route pattern"},
		"Duplicate":      {patterns: []string{"GET /forecast", "GET /forecast"}, message: "invalid route pattern"},
		"Conflict":       {patterns: []string{"GET /{location}/alerts", "GET /locations/{alert}"}, message: "invalid route pattern"},
	} {
		t.Run(name, func(t *testing.T) {
			routes := route.New[int]()
			var err error
			for index, pattern := range test.patterns {
				if err = routes.Add(pattern, index); err != nil {
					break
				}
			}
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestEmptyMapMatchesNothing(t *testing.T) {
	value, wildcards, matched := route.New[int]().Match(http.MethodGet, "", "/v2/forecast")
	assert.Equal(t, 0, value)
	assert.Equal(t, nil, wildcards)
	assert.False(t, matched)
}

func TestListsWildcards(t *testing.T) {
	for pattern, expected := range map[string][]string{
		"GET /v1/forecast":                        {},
		"GET /v1/locations/{location}/days/{day}": {"location", "day"},
		"GET weather.example/v1/files/{path...}":  {"path"},
		"GET /v1/locations/{$}":                   {},
		"GET weather.example/v1/{location}/{$}":   {"location"},
	} {
		t.Run(pattern, func(t *testing.T) {
			assert.Equal(t, expected, route.Wildcards(pattern))
		})
	}
}
