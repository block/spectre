package javascript

import (
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"
)

// Members are a binding's members in one runtime. D is what scripts declared
// through them, which must be equal in every runtime; I holds their callbacks.
type Members[D, I any] struct {
	// Ingress and Egress are installed as ingress.<protocol> and egress.<protocol>.
	Ingress Option[sobek.Value]
	Egress  Option[sobek.Value]
	// Declared returns what scripts have declared so far. D must hold no runtime values.
	Declared func() D
	Instance I
}
