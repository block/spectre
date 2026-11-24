// Package schema models shared payloads as two trees: Type, a TypeScript-declared
// type, and Value, a JSON value that carries its Type.
package schema

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// Type is one TypeScript type expression in the supported subset.
//
//sumtype:decl
type Type interface {
	// String renders the type as TypeScript, naming referenced types as they are stored.
	String() string
	// Format renders the type as TypeScript, naming each referenced type with reference.
	Format(reference func(name TypeRef) string) string
	// Equal reports whether two types accept the same JSON. Literal union alias
	// names are ignored, but object types must have the same name.
	Equal(other Type) bool
	isType()
}

// StringType is a JSON string.
type StringType struct{}

// NumberType is a JSON number, or one of the strings in Literals, as in ProtoJSON's
// non-finite floats and open enums.
type NumberType struct {
	// Name is the alias the union was declared with, if any.
	Name Option[TypeRef]
	// Literals are the strings the number also accepts. Their order is not significant.
	Literals []string
}

// BooleanType is a JSON boolean.
type BooleanType struct{}

// EnumType is a union of string literals.
type EnumType struct {
	// Name is the alias the union was declared with, if any.
	Name Option[TypeRef]
	// Literals are the permitted strings. Their order is not significant.
	Literals []string
}

// UnknownType is any JSON value, written unknown.
type UnknownType struct{}

// NullableType is Type or null, written T | null. Type is never itself nullable.
type NullableType struct {
	Type Type
}

// ListType is an array, written T[].
type ListType struct {
	Element Type
}

// MapType is an object with arbitrary string keys, written Record<string, T>.
type MapType struct {
	Element Type
}

// TypeRef names an object type, e.g. "weather.service.GetWeatherRequest".
type TypeRef string

// ObjectType is a named object type. Fields point to other object types, so recursive
// types form cycles; compare them by name, and never mutate one a Schema holds.
type ObjectType struct {
	// Name is the declaring module's name followed by the type's namespace path.
	Name TypeRef
	// Fields are in declaration order.
	Fields []Field
}

// Field is one member of an object type.
type Field struct {
	// Name is the JSON key.
	Name string
	// Optional means the key may be absent.
	Optional bool
	// Type is the member's type.
	Type Type
}

func (StringType) isType()            {}
func (NumberType) isType()            {}
func (BooleanType) isType()           {}
func (EnumType) isType()              {}
func (UnknownType) isType()           {}
func (NullableType) isType()          {}
func (ListType) isType()              {}
func (MapType) isType()               {}
func (*ObjectType) isType()           {}
func (t StringType) String() string   { return t.Format(storedName) }
func (t NumberType) String() string   { return t.Format(storedName) }
func (t BooleanType) String() string  { return t.Format(storedName) }
func (t EnumType) String() string     { return t.Format(storedName) }
func (t UnknownType) String() string  { return t.Format(storedName) }
func (t NullableType) String() string { return t.Format(storedName) }
func (t ListType) String() string     { return t.Format(storedName) }
func (t MapType) String() string      { return t.Format(storedName) }
func (t *ObjectType) String() string  { return t.Format(storedName) }

// Format renders the type as TypeScript.
func (StringType) Format(func(name TypeRef) string) string { return "string" }

// Format renders the type as TypeScript, naming it by its alias if it has one.
func (t NumberType) Format(reference func(name TypeRef) string) string {
	if name, ok := t.Name.Get(); ok {
		return reference(name)
	}
	return strings.Join(append([]string{"number"}, quoted(t.Literals)...), " | ")
}

// Format renders the type as TypeScript.
func (BooleanType) Format(func(name TypeRef) string) string { return "boolean" }

// Format renders the type as TypeScript, naming it by its alias if it has one.
func (t EnumType) Format(reference func(name TypeRef) string) string {
	if name, ok := t.Name.Get(); ok {
		return reference(name)
	}
	return strings.Join(quoted(t.Literals), " | ")
}

// Format renders the type as TypeScript.
func (UnknownType) Format(func(name TypeRef) string) string { return "unknown" }

// Format renders the type as TypeScript.
func (t NullableType) Format(reference func(name TypeRef) string) string {
	return t.Type.Format(reference) + " | null"
}

// Format renders the type as TypeScript, parenthesising a union element.
func (t ListType) Format(reference func(name TypeRef) string) string {
	formatted := t.Element.Format(reference)
	if isUnion(t.Element) {
		formatted = "(" + formatted + ")"
	}
	return formatted + "[]"
}

// Format renders the type as TypeScript.
func (t MapType) Format(reference func(name TypeRef) string) string {
	return "Record<string, " + t.Element.Format(reference) + ">"
}

// Format renders the type as TypeScript, by name.
func (t *ObjectType) Format(reference func(name TypeRef) string) string {
	return reference(t.Name)
}

// Equal reports whether other is also a string.
func (StringType) Equal(other Type) bool {
	_, ok := other.(StringType)
	return ok
}

// Equal reports whether other is a number accepting the same literals.
func (t NumberType) Equal(other Type) bool {
	number, ok := other.(NumberType)
	return ok && sameLiterals(t.Literals, number.Literals)
}

// Equal reports whether other is also a boolean.
func (BooleanType) Equal(other Type) bool {
	_, ok := other.(BooleanType)
	return ok
}

// Equal reports whether other is an enum of the same literals.
func (t EnumType) Equal(other Type) bool {
	enum, ok := other.(EnumType)
	return ok && sameLiterals(t.Literals, enum.Literals)
}

// Equal reports whether other is also unknown.
func (UnknownType) Equal(other Type) bool {
	_, ok := other.(UnknownType)
	return ok
}

// Equal reports whether other is the same type or null.
func (t NullableType) Equal(other Type) bool {
	nullable, ok := other.(NullableType)
	return ok && t.Type.Equal(nullable.Type)
}

// Equal reports whether other is a list of the same elements.
func (t ListType) Equal(other Type) bool {
	list, ok := other.(ListType)
	return ok && t.Element.Equal(list.Element)
}

// Equal reports whether other is a map of the same values.
func (t MapType) Equal(other Type) bool {
	mapped, ok := other.(MapType)
	return ok && t.Element.Equal(mapped.Element)
}

// Equal reports whether other is the object type of the same name.
func (t *ObjectType) Equal(other Type) bool {
	object, ok := other.(*ObjectType)
	return ok && t.Name == object.Name
}

// Field returns the member with the given JSON key.
func (t *ObjectType) Field(name string) (field Field, found bool) {
	for _, field := range t.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return Field{}, false
}

// NonNull returns a nullable type's non-null type, and any other type as it is.
func NonNull(t Type) Type {
	if nullable, ok := t.(NullableType); ok {
		return nullable.Type
	}
	return t
}

func storedName(name TypeRef) string {
	return string(name)
}

// isUnion reports whether the type renders as an unnamed union.
func isUnion(t Type) bool {
	switch t := t.(type) {
	case NullableType:
		return true
	case EnumType:
		return !t.Name.Ok() && len(t.Literals) > 1
	case NumberType:
		return !t.Name.Ok() && len(t.Literals) > 0
	case StringType, BooleanType, UnknownType, ListType, MapType, *ObjectType:
	}
	return false
}

func sameLiterals(left, right []string) bool {
	left, right = slices.Clone(left), slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func quoted(literals []string) []string {
	encoded := make([]string, 0, len(literals))
	for _, literal := range literals {
		// JSON strings are valid TypeScript string literals.
		value, _ := json.Marshal(literal) //nolint:errcheck // Strings always encode.
		encoded = append(encoded, string(value))
	}
	return encoded
}

// PropertyName renders a JSON key as a TypeScript property name, quoting it
// unless it is an identifier.
func PropertyName(name string) (property string) {
	if isIdentifier(name) {
		return name
	}
	// JSON strings are valid TypeScript string literals.
	encoded, _ := json.Marshal(name) //nolint:errcheck // Strings always encode.
	return string(encoded)
}

func isIdentifier(name string) (valid bool) {
	if name == "" {
		return false
	}
	for index, char := range name {
		if char == '$' || char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

// Method is one method declared on a schema interface, with the object types of
// its single parameter and its result. Protocols decide what methods mean.
type Method struct {
	// Interface is the qualified name of the declaring interface.
	Interface TypeRef
	// Name is the method's name within its interface.
	Name string
	// Parameter names the object type of the single parameter.
	Parameter TypeRef
	// Result names the object type of the result.
	Result TypeRef
}

// Schema indexes object types and methods whose references all resolve.
type Schema struct {
	types   map[TypeRef]*ObjectType
	methods []Method
}

// New takes ownership of types and validates them. It rejects duplicate names, and
// fields of object types it was not given.
func New(types []*ObjectType, methods []Method) (*Schema, error) {
	loaded := &Schema{types: map[TypeRef]*ObjectType{}}
	for _, declared := range types {
		if _, duplicate := loaded.types[declared.Name]; duplicate {
			return nil, errors.Errorf("type %q is declared more than once", declared.Name)
		}
		loaded.types[declared.Name] = declared
	}
	declaredMethods := map[Method]bool{}
	for _, method := range methods {
		key := Method{Interface: method.Interface, Name: method.Name}
		if declaredMethods[key] {
			return nil, errors.Errorf("method %q is declared more than once", method.qualifiedName())
		}
		declaredMethods[key] = true
	}
	for _, declared := range types {
		names := map[string]bool{}
		for _, field := range declared.Fields {
			if names[field.Name] {
				return nil, errors.Errorf("type %q declares field %q more than once", declared.Name, field.Name)
			}
			names[field.Name] = true
			if err := loaded.check(field.Type); err != nil {
				return nil, errors.Wrapf(err, "field %q of type %q", field.Name, declared.Name)
			}
		}
	}
	for _, method := range methods {
		for _, name := range []TypeRef{method.Parameter, method.Result} {
			if _, ok := loaded.types[name]; !ok {
				return nil, errors.Errorf("method %q refers to undeclared type %q", method.qualifiedName(), name)
			}
		}
	}
	loaded.methods = slices.SortedFunc(slices.Values(methods), func(left, right Method) int {
		return cmp.Or(cmp.Compare(left.Interface, right.Interface), cmp.Compare(left.Name, right.Name))
	})
	return loaded, nil
}

// check rejects a type that refers to an object type the schema does not hold.
func (s *Schema) check(declared Type) error {
	switch declared := declared.(type) {
	case nil:
		return errors.New("type is missing")
	case *ObjectType:
		if s.types[declared.Name] != declared {
			return errors.Errorf("type %q is not declared", declared.Name)
		}
	case ListType:
		return s.check(declared.Element)
	case MapType:
		return s.check(declared.Element)
	case NullableType:
		if _, nested := declared.Type.(NullableType); nested {
			return errors.New("nullable type is nullable")
		}
		return s.check(declared.Type)
	case EnumType:
		if len(declared.Literals) == 0 {
			return errors.New("enum has no literals")
		}
	case StringType, NumberType, BooleanType, UnknownType:
	}
	return nil
}

// Type returns the named object type.
func (s *Schema) Type(name TypeRef) (*ObjectType, error) {
	declared, ok := s.types[name]
	if !ok {
		return nil, errors.Errorf("type %q is not declared", name)
	}
	return declared, nil
}

// Types returns every object type, sorted by name.
func (s *Schema) Types() []*ObjectType {
	names := slices.Sorted(maps.Keys(s.types))
	types := make([]*ObjectType, 0, len(names))
	for _, name := range names {
		types = append(types, s.types[name])
	}
	return types
}

// Methods returns every declared method, sorted by interface and name.
func (s *Schema) Methods() []Method {
	return slices.Clone(s.methods)
}

func (m Method) qualifiedName() string {
	return string(m.Interface) + "." + m.Name
}
