package proxy_test

import (
	"fmt"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/proxy"
)

func TestCallCounterCountsBeyondItsRoutesAsOther(t *testing.T) {
	counter := proxy.NewCallCounter()
	assert.Equal(t, model.Calls{}, counter.Calls())
	expected := model.Calls{Routes: map[model.Route]uint64{}, Other: 2}
	for i := range 100 {
		route := model.Route(fmt.Sprintf("GET /v1/users/%d", i))
		counter.Count(route)
		expected.Routes[route] = 1
	}
	counter.Count("GET /v1/users/0")
	expected.Routes["GET /v1/users/0"] = 2
	counter.Count("GET /v1/users/100")
	counter.Count("GET /v1/users/101")
	assert.Equal(t, expected, counter.Calls())
}
