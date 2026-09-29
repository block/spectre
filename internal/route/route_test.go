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
	for name, test := range map[string]struct {
		method   string
		path     string
		expected string
		matched  bool
	}{
		"MethodAndPath": {method: http.MethodGet, path: "/v2/forecast", expected: "v2", matched: true},
		"Head":          {method: http.MethodHead, path: "/v1/status", expected: "status", matched: true},
		"GetIsNotHead":  {method: http.MethodHead, path: "/v2/forecast"},
		"Wildcard":      {method: http.MethodPost, path: "/v1/locations/london/alerts", expected: "update", matched: true},
		"EscapedSlash":  {method: http.MethodPost, path: "/v1/locations/a%2Fb/alerts", expected: "update", matched: true},
		"InvalidEscape": {method: http.MethodGet, path: "/v2/forecast%zz"},
		"RPCPath":       {method: http.MethodPost, path: "/test.v1.Weather/Get", expected: "rpc", matched: true},
		"WrongMethod":   {method: http.MethodPost, path: "/v2/forecast"},
		"UnknownPath":   {method: http.MethodGet, path: "/v3/forecast"},
		"TrailingSlash": {method: http.MethodGet, path: "/v2/forecast/"},
		"UncleanPath":   {method: http.MethodGet, path: "/v2/../v2/forecast"},
	} {
		t.Run(name, func(t *testing.T) {
			value, matched := routes.Match(test.method, test.path)
			assert.Equal(t, test.expected, value)
			assert.Equal(t, test.matched, matched)
		})
	}
}

func TestRejectsInvalidPatterns(t *testing.T) {
	for name, test := range map[string]struct {
		patterns []string
		message  string
	}{
		"NoMethod":       {patterns: []string{"/forecast"}, message: `route pattern "/forecast" must have the form "<METHOD> /<path>"`},
		"Empty":          {patterns: []string{" "}, message: `must have the form "<METHOD> /<path>"`},
		"Host":           {patterns: []string{"GET weather.example/forecast"}, message: `route pattern "GET weather.example/forecast" must not include a host`},
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
	value, matched := route.New[int]().Match(http.MethodGet, "/v2/forecast")
	assert.Equal(t, 0, value)
	assert.False(t, matched)
}
