package state

import (
	"cmp"
	"maps"
	"slices"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
)

// Kinds are the kinds of IO a binary supports, which a migration state's nodes may have.
// A State shares the Kinds it was checked against, so add every kind before parsing.
type Kinds struct {
	kinds map[kindKey]Kind
}

// NewKinds returns Kinds that support none.
func NewKinds() Kinds {
	return Kinds{kinds: map[kindKey]Kind{}}
}

// Add supports kind. A kind defined twice is an error.
func (k Kinds) Add(kind Kind) error {
	key := kind.key()
	if _, duplicate := k.kinds[key]; duplicate {
		return errors.Errorf("%s is defined more than once", kind)
	}
	k.kinds[key] = kind
	return nil
}

// NodeTypes describes library, then each kind of IO a node can have, ingress
// before egress and otherwise by protocol.
func (k Kinds) NodeTypes() []NodeType {
	types := []NodeType{{
		Name: Library,
		Description: "Module shared by more than one other node that performs no IO, " +
			"such as shared domain types, validation, calculations, and helpers.",
	}}
	for _, kind := range k.sorted() {
		types = append(types, NodeType{
			Name:        kind.String(),
			Description: kind.Description,
			Table:       "nodes.<id>." + string(kind.Direction) + "." + string(kind.Protocol),
			Settings:    kind.Settings,
		})
	}
	return types
}

// Load reads and parses the migration state file at path, which may use only these kinds.
func (k Kinds) Load(path string) (State, error) {
	return load(path, k)
}

// Parse reads and checks a migration state against these kinds. An invalid state
// returns an *InvalidError.
func (k Kinds) Parse(data []byte) (State, error) {
	return parse(data, k)
}

// MarkMigrated sets the status of each node in ids to migrated and rewrites
// the state, dropping comments. The result must still be a valid state.
func (k Kinds) MarkMigrated(data []byte, ids []string) (updated []byte, marked []string, err error) {
	return markMigrated(data, k, ids)
}

// Kind returns the supported kind of protocol's traffic in direction.
func (k Kinds) Kind(direction model.Direction, protocol model.Protocol) (kind Kind, supported bool) {
	kind, supported = k.kinds[kindKey{Direction: direction, Protocol: protocol}]
	return kind, supported
}

// sorted returns the kinds ordered ingress first, then by protocol.
func (k Kinds) sorted() []Kind {
	return slices.SortedFunc(maps.Values(k.kinds), func(a, b Kind) int {
		if a.Direction != b.Direction {
			return -cmp.Compare(a.Direction, b.Direction)
		}
		return cmp.Compare(a.Protocol, b.Protocol)
	})
}
