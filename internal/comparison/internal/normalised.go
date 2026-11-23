package comparisoninternal

import (
	"maps"
	"slices"
	"strings"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// normalisedTypes holds the type each object type's values have once normalised. A type
// without a declared one keeps its own, but is derived under its name where a field's type changes.
type normalisedTypes struct {
	loaded *schema.Schema
	// byIncoming holds only types that normalise to another type.
	byIncoming map[schema.TypeRef]*schema.ObjectType
	// declaredFor maps each declared normalised type to the type it normalises.
	declaredFor map[schema.TypeRef]schema.TypeRef
}

func newNormalisedTypes(loaded *schema.Schema, messages []javascript.MessageTarget) (*normalisedTypes, error) {
	types := &normalisedTypes{
		loaded:      loaded,
		byIncoming:  map[schema.TypeRef]*schema.ObjectType{},
		declaredFor: map[schema.TypeRef]schema.TypeRef{},
	}
	for _, message := range messages {
		if err := types.declare(message); err != nil {
			return nil, errors.Wrapf(err, "validate normalised type of %q", message.Type())
		}
	}
	for normalised, incoming := range types.declaredFor {
		if _, normalises := types.byIncoming[normalised]; normalises {
			return nil, errors.Errorf("normalised type %q of %q declares its own normalised type", normalised, incoming)
		}
	}
	declared := maps.Clone(types.byIncoming)
	types.derive()
	for _, incoming := range slices.Sorted(maps.Keys(declared)) {
		source, err := loaded.Type(incoming)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if err := types.checkDeclared(source, declared[incoming]); err != nil {
			return nil, errors.Wrapf(err, "validate normalised type of %q", incoming)
		}
	}
	return types, nil
}

func (n *normalisedTypes) declare(message javascript.MessageTarget) error {
	incoming, err := n.loaded.Type(message.Type())
	if err != nil {
		return errors.WithStack(err)
	}
	normalised, err := n.loaded.Type(message.Normalised())
	if err != nil {
		return errors.WithStack(err)
	}
	if normalised == incoming {
		return nil
	}
	if other, taken := n.declaredFor[normalised.Name]; taken {
		return errors.Errorf("%q is already the normalised type of %q", normalised.Name, other)
	}
	n.byIncoming[incoming.Name] = normalised
	n.declaredFor[normalised.Name] = incoming.Name
	return nil
}

// derive gives each type that reaches a declared normalised type, through its fields,
// a copy whose fields hold normalised types. Loaded types are never mutated.
func (n *normalisedTypes) derive() {
	derived := []*schema.ObjectType{}
	for changed := true; changed; {
		changed = false
		for _, incoming := range n.loaded.Types() {
			_, done := n.byIncoming[incoming.Name]
			// A declared normalised type is checked as written, never derived.
			_, isNormalised := n.declaredFor[incoming.Name]
			if done || isNormalised {
				continue
			}
			if slices.ContainsFunc(incoming.Fields, func(field schema.Field) bool { return n.changes(field.Type) }) {
				n.byIncoming[incoming.Name] = &schema.ObjectType{Name: incoming.Name}
				derived = append(derived, incoming)
				changed = true
			}
		}
	}
	for _, incoming := range derived {
		normalised := n.byIncoming[incoming.Name]
		normalised.Fields = make([]schema.Field, 0, len(incoming.Fields))
		for _, field := range incoming.Fields {
			normalised.Fields = append(normalised.Fields, schema.Field{Name: field.Name, Optional: field.Optional, Type: n.slot(field.Type)})
		}
	}
}

// changes reports whether declared holds an object type that normalises to another type.
func (n *normalisedTypes) changes(declared schema.Type) bool {
	switch declared := declared.(type) {
	case *schema.ObjectType:
		_, normalises := n.byIncoming[declared.Name]
		return normalises
	case schema.NullableType:
		return n.changes(declared.Type)
	case schema.ListType:
		return n.changes(declared.Element)
	case schema.MapType:
		return n.changes(declared.Element)
	case schema.StringType, schema.NumberType, schema.BooleanType, schema.EnumType, schema.UnknownType:
	}
	return false
}

// of returns the type values of declared have once normalised.
func (n *normalisedTypes) of(declared *schema.ObjectType) *schema.ObjectType {
	if normalised, normalises := n.byIncoming[declared.Name]; normalises {
		return normalised
	}
	return declared
}

// slot returns declared with each object type replaced by its normalised type.
func (n *normalisedTypes) slot(declared schema.Type) schema.Type {
	switch declared := declared.(type) {
	case *schema.ObjectType:
		return n.of(declared)
	case schema.NullableType:
		return schema.NullableType{Type: n.slot(declared.Type)}
	case schema.ListType:
		return schema.ListType{Element: n.slot(declared.Element)}
	case schema.MapType:
		return schema.MapType{Element: n.slot(declared.Element)}
	case schema.StringType, schema.NumberType, schema.BooleanType, schema.EnumType, schema.UnknownType:
	}
	return declared
}

// checkDeclared requires normalised to be incoming with only fields made optional,
// and with each object type replaced by its normalised type.
func (n *normalisedTypes) checkDeclared(incoming, normalised *schema.ObjectType) error {
	for _, field := range normalised.Fields {
		if _, found := incoming.Field(field.Name); !found {
			return errors.Errorf("%q declares field %q, which %q does not have", normalised.Name, field.Name, incoming.Name)
		}
	}
	for _, field := range incoming.Fields {
		relaxed, found := normalised.Field(field.Name)
		if !found {
			return errors.Errorf("%q must declare field %q of %q", normalised.Name, field.Name, incoming.Name)
		}
		if field.Optional && !relaxed.Optional {
			return errors.Errorf("field %q of %q must be optional, as it is in %q", field.Name, normalised.Name, incoming.Name)
		}
		expected := n.slot(field.Type)
		if !sameType(expected, relaxed.Type) {
			return errors.Errorf("field %q of %q must have type %s%s", field.Name, normalised.Name, expected, n.derivedHint(expected))
		}
	}
	return nil
}

// derivedHint asks for a declared normalised type where expected holds a derived one,
// which scripts cannot name.
func (n *normalisedTypes) derivedHint(expected schema.Type) string {
	derived := []string{}
	var visit func(declared schema.Type)
	visit = func(declared schema.Type) {
		switch declared := declared.(type) {
		case *schema.ObjectType:
			if _, isDeclared := n.declaredFor[declared.Name]; !isDeclared && n.changes(declared) {
				derived = append(derived, string(declared.Name))
			}
		case schema.NullableType:
			visit(declared.Type)
		case schema.ListType:
			visit(declared.Element)
		case schema.MapType:
			visit(declared.Element)
		case schema.StringType, schema.NumberType, schema.BooleanType, schema.EnumType, schema.UnknownType:
		}
	}
	visit(expected)
	if len(derived) == 0 {
		return ""
	}
	return ", so declare a normalised type for " + strings.Join(derived, " and ") + " with spectre.message"
}

// fieldRoot returns the normalised type a field normaliser naming name applies to.
func (n *normalisedTypes) fieldRoot(name schema.TypeRef) (*schema.ObjectType, error) {
	declared, err := n.loaded.Type(name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if _, isDeclared := n.declaredFor[name]; isDeclared {
		return declared, nil
	}
	normalised := n.of(declared)
	if _, isDeclared := n.declaredFor[normalised.Name]; isDeclared {
		return nil, errors.Errorf("%q normalises to %q, which field normalisers must name instead", name, normalised.Name)
	}
	return normalised, nil
}

// sameType reports whether two types are identical, with object types compared by
// identity because a derived normalised type shares its incoming type's name.
func sameType(left, right schema.Type) bool {
	switch left := left.(type) {
	case *schema.ObjectType:
		object, ok := right.(*schema.ObjectType)
		return ok && left == object
	case schema.NullableType:
		nullable, ok := right.(schema.NullableType)
		return ok && sameType(left.Type, nullable.Type)
	case schema.ListType:
		list, ok := right.(schema.ListType)
		return ok && sameType(left.Element, list.Element)
	case schema.MapType:
		mapped, ok := right.(schema.MapType)
		return ok && sameType(left.Element, mapped.Element)
	case schema.StringType, schema.NumberType, schema.BooleanType, schema.EnumType, schema.UnknownType:
		return left.Equal(right)
	}
	return false
}
