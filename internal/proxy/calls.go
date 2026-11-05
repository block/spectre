package proxy

import (
	"maps"
	"sync"

	"github.com/block/spectre/internal/model"
)

// maxCountedRoutes bounds the routes a counter tracks, because routes without a
// migration state hold raw paths.
const maxCountedRoutes = 100

// CallCounter counts the requests a proxy receives on each route. It is safe
// for concurrent use.
type CallCounter struct {
	mu    sync.Mutex
	calls model.Calls
}

// NewCallCounter returns a counter that has counted no requests.
func NewCallCounter() *CallCounter {
	return &CallCounter{calls: model.Calls{Routes: map[model.Route]uint64{}}}
}

// Count counts one request on route. Once the counter tracks its most routes,
// requests to new routes count as other.
func (c *CallCounter) Count(route model.Route) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.calls.Routes[route]; ok || len(c.calls.Routes) < maxCountedRoutes {
		c.calls.Routes[route]++
		return
	}
	c.calls.Other++
}

// Calls returns the requests counted so far.
func (c *CallCounter) Calls() model.Calls {
	c.mu.Lock()
	defer c.mu.Unlock()
	calls := model.Calls{Other: c.calls.Other}
	if len(c.calls.Routes) > 0 {
		calls.Routes = maps.Clone(c.calls.Routes)
	}
	return calls
}
