package event

import (
	"maps"
	"slices"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/event/script"
	"github.com/block/spectre/internal/schema"
	"github.com/block/spectre/internal/state"
)

// rules index what the scripts declared through the event binding by event type.
type rules struct {
	// starts are the reference types that start work.
	starts map[string]script.Start
	// produced are the reference types that work produces, which the scripts declared.
	produced map[string]script.Produced
	// compared types each route's payloads, for the produced types the scripts declared.
	compared map[model.Route]schema.TypeRef
	// starting holds both sides' types of every start, which the candidate never reports.
	starting map[string]bool
}

func newRules(declared script.Declarations) rules {
	r := rules{
		starts:   map[string]script.Start{},
		produced: map[string]script.Produced{},
		compared: map[model.Route]schema.TypeRef{},
		starting: map[string]bool{},
	}
	for _, start := range declared.Starts {
		r.starts[start.Type] = start
		r.starting[start.Type] = true
		r.starting[start.Candidate] = true
	}
	for _, produced := range declared.Produced {
		r.produced[produced.Type] = produced
		r.compared[model.Route(produced.Candidate)] = produced.Compared
	}
	return r
}

// start is the start the scripts declared for a reference type.
func (r rules) start(eventType string) (start script.Start, ok bool) {
	start, ok = r.starts[eventType]
	return start, ok
}

// producedBy is the produced event the scripts declared for a reference type.
func (r rules) producedBy(eventType string) (produced script.Produced, ok bool) {
	produced, ok = r.produced[eventType]
	return produced, ok
}

// startsWork reports whether either side's events of eventType start work.
func (r rules) startsWork(eventType string) bool {
	return r.starting[eventType]
}

// route is the route the reference's events of eventType pair under: the candidate's type.
func (r rules) route(eventType string) model.Route {
	if produced, ok := r.produced[eventType]; ok {
		return model.Route(produced.Candidate)
	}
	return model.Route(eventType)
}

// comparedType is the type a route's payloads are compared as, if the scripts declared one.
func (r rules) comparedType(route model.Route) Option[schema.TypeRef] {
	typeName, ok := r.compared[route]
	if !ok {
		return None[schema.TypeRef]()
	}
	return Some(typeName)
}

// stateType is an ingress[event] type that a migration state node lists.
type stateType struct {
	node     string
	migrated bool
}

// stateTypes maps the ingress[event] types of each node to the node.
func stateTypes(migration Option[[]state.KindNode]) Option[map[string]stateType] {
	nodes, ok := migration.Get()
	if !ok {
		return None[map[string]stateType]()
	}
	types := map[string]stateType{}
	for _, node := range nodes {
		for _, eventType := range slices.Sorted(maps.Keys(node.Settings["types"])) {
			types[eventType] = stateType{node: node.ID, migrated: node.Migrated}
		}
	}
	return Some(types)
}
