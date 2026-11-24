package script

import (
	. "github.com/alecthomas/types/optional"

	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/schema"
)

// Declarations are what scripts declare through the event binding.
type Declarations struct {
	// Starts are the reference event types that start work, in declaration order.
	Starts []Start
	// Produced are the reference event types that work produces, in declaration order.
	Produced []Produced
}

// Start is a reference event type that starts work, and what the candidate receives for it.
type Start struct {
	Type      string
	Candidate string
	// Payload maps the reference's payload to the candidate's. Without it, the payload passes unchanged.
	Payload Option[Mapping]
}

// Produced is a reference event type that work produces, and how the candidate's events pair with it.
type Produced struct {
	Type string
	// Candidate is the candidate's event type, which is also the route both sides pair under.
	Candidate string
	// Compared is the type both sides' payloads are compared as.
	Compared schema.TypeRef
	// Payload maps the reference's payload to a Compared value. Without it, the payload passes unchanged.
	Payload Option[Mapping]
	// Result maps the reference's result to the candidate's. Without it, the result passes unchanged.
	Result Option[Mapping]
}

// Mapping is a script function from a value of type From to a value of type To.
type Mapping struct {
	// index finds the function among one runtime's callbacks.
	index int
	From  schema.TypeRef
	To    schema.TypeRef
}

func newMapping(index int, from, to schema.TypeRef) Mapping {
	return Mapping{index: index, From: from, To: to}
}

// function finds the mapping's function among one runtime's callbacks.
func (m Mapping) function(functions []sobek.Callable) (function sobek.Callable, ok bool) {
	if m.index < 0 || m.index >= len(functions) {
		return nil, false
	}
	return functions[m.index], true
}
