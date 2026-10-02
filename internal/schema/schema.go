// Package schema models the payload types that normalisers and protocols share.
// Types are declared in TypeScript, so scripts and the host use one definition.
package schema

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
)

// Kind is the JSON shape of a value.
type Kind string

// Kinds of the TypeScript type expressions in the supported subset.
const (
	// KindString is a JSON string.
	KindString Kind = "string"
	// KindNumber is a JSON number, or one of its literals.
	KindNumber Kind = "number"
	// KindBoolean is a JSON boolean.
	KindBoolean Kind = "boolean"
	// KindEnum is a union of string literals.
	KindEnum Kind = "enum"
	// KindObject is a reference to a named object type.
	KindObject Kind = "object"
	// KindList is an array, written T[].
	KindList Kind = "list"
	// KindMap is an object with arbitrary string keys, written Record<string, T>.
	KindMap Kind = "map"
)

// Value is one TypeScript type expression in the supported subset.
type Value struct {
	// Kind is the value's JSON shape.
	Kind Kind
	// Literals are an enum's permitted strings, or strings a number also accepts, as
	// in ProtoJSON's non-finite floats and open enums. Their order is not significant.
	Literals []string
	// Element is the element type of a list or the value type of a map.
	Element *Value
	// Type names an object's type, or the alias a literal union was declared with, if any.
	Type string
}

// String renders the value as TypeScript, naming referenced types as they are stored.
func (v Value) String() string {
	return v.Format(func(name string) string { return name })
}

// Format renders the value as TypeScript, naming each referenced type with reference.
func (v Value) Format(reference func(name string) string) string {
	switch v.Kind {
	case KindEnum:
		if v.Type != "" {
			return reference(v.Type)
		}
		return strings.Join(quoted(v.Literals), " | ")
	case KindNumber:
		if v.Type != "" {
			return reference(v.Type)
		}
		return strings.Join(append([]string{string(KindNumber)}, quoted(v.Literals)...), " | ")
	case KindObject:
		return reference(v.Type)
	case KindList:
		element := v.Element.Format(reference)
		if v.Element.isUnion() {
			element = "(" + element + ")"
		}
		return element + "[]"
	case KindMap:
		return "Record<string, " + v.Element.Format(reference) + ">"
	case KindString, KindBoolean:
	}
	return string(v.Kind)
}

// isUnion reports whether the value renders as an unnamed union.
func (v Value) isUnion() bool {
	switch v.Kind {
	case KindEnum:
		return v.Type == "" && len(v.Literals) > 1
	case KindNumber:
		return v.Type == "" && len(v.Literals) > 0
	case KindString, KindBoolean, KindObject, KindList, KindMap:
	}
	return false
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

// Equal reports whether two values accept the same JSON. Enum alias names are
// ignored, but object types must have the same name.
func (v Value) Equal(other Value) bool {
	if v.Kind != other.Kind {
		return false
	}
	switch v.Kind {
	case KindEnum, KindNumber:
		left, right := slices.Clone(v.Literals), slices.Clone(other.Literals)
		slices.Sort(left)
		slices.Sort(right)
		return slices.Equal(left, right)
	case KindObject:
		return v.Type == other.Type
	case KindList, KindMap:
		return v.Element.Equal(*other.Element)
	case KindString, KindBoolean:
	}
	return true
}

// Field is one member of an object type.
type Field struct {
	// Name is the JSON key.
	Name string
	// Optional means the key may be absent. A present key never holds null.
	Optional bool
	// Value is the member's type.
	Value Value
}

// Type is a named object type, declared as an interface or an object type alias.
type Type struct {
	// Name is the declaring module's name followed by the type's namespace path.
	Name string
	// Fields are in declaration order.
	Fields []Field
}

// Field returns the member with the given JSON key.
func (t *Type) Field(name string) (field Field, found bool) {
	for _, field := range t.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return Field{}, false
}

// Operation is one unary RPC method declared on a service interface.
type Operation struct {
	// Name is the service's type name followed by the method name.
	Name string
	// Request names the request object type.
	Request string
	// Response names the response object type.
	Response string
}

// Schema indexes types and operations whose references all resolve. Treat types
// and their fields as immutable after construction, as comparison plans share them.
type Schema struct {
	types      map[string]*Type
	operations map[string]Operation
}

// New takes ownership of types and validates their references. It rejects
// duplicate names and references to undeclared types.
func New(types []*Type, operations []Operation) (*Schema, error) {
	loaded := &Schema{types: map[string]*Type{}, operations: map[string]Operation{}}
	for _, declared := range types {
		if _, duplicate := loaded.types[declared.Name]; duplicate {
			return nil, errors.Errorf("type %q is declared more than once", declared.Name)
		}
		loaded.types[declared.Name] = declared
	}
	for _, operation := range operations {
		if _, duplicate := loaded.operations[operation.Name]; duplicate {
			return nil, errors.Errorf("operation %q is declared more than once", operation.Name)
		}
		loaded.operations[operation.Name] = operation
	}
	for _, declared := range types {
		names := map[string]bool{}
		for _, field := range declared.Fields {
			if names[field.Name] {
				return nil, errors.Errorf("type %q declares field %q more than once", declared.Name, field.Name)
			}
			names[field.Name] = true
			if err := loaded.checkValue(field.Value); err != nil {
				return nil, errors.Wrapf(err, "field %q of type %q", field.Name, declared.Name)
			}
		}
	}
	for _, operation := range operations {
		for _, name := range []string{operation.Request, operation.Response} {
			if _, ok := loaded.types[name]; !ok {
				return nil, errors.Errorf("operation %q refers to undeclared type %q", operation.Name, name)
			}
		}
	}
	return loaded, nil
}

func (s *Schema) checkValue(value Value) error {
	switch value.Kind {
	case KindObject:
		if _, ok := s.types[value.Type]; !ok {
			return errors.Errorf("type %q is not declared", value.Type)
		}
	case KindList, KindMap:
		if value.Element == nil {
			return errors.Errorf("%s has no element type", value.Kind)
		}
		return s.checkValue(*value.Element)
	case KindEnum:
		if len(value.Literals) == 0 {
			return errors.New("enum has no literals")
		}
	case KindString, KindNumber, KindBoolean:
	default:
		return errors.Errorf("unknown value kind %q", value.Kind)
	}
	return nil
}

// Type returns the named object type.
func (s *Schema) Type(name string) (*Type, error) {
	declared, ok := s.types[name]
	if !ok {
		return nil, errors.Errorf("type %q is not declared", name)
	}
	return declared, nil
}

// Operation returns the named RPC method.
func (s *Schema) Operation(name string) (Operation, error) {
	operation, ok := s.operations[name]
	if !ok {
		return Operation{}, errors.Errorf("operation %q is not declared", name)
	}
	return operation, nil
}

// Types returns every object type, sorted by name.
func (s *Schema) Types() []*Type {
	names := slices.Sorted(maps.Keys(s.types))
	types := make([]*Type, 0, len(names))
	for _, name := range names {
		types = append(types, s.types[name])
	}
	return types
}

// Operations returns every operation, sorted by name.
func (s *Schema) Operations() []Operation {
	names := slices.Sorted(maps.Keys(s.operations))
	operations := make([]Operation, 0, len(names))
	for _, name := range names {
		operations = append(operations, s.operations[name])
	}
	return operations
}

// Validate checks that a decoded JSON value is an instance of the named type. It
// rejects unknown keys and nulls, so a valid payload holds only declared data.
func (s *Schema) Validate(name string, value any) error {
	return s.validate(Value{Kind: KindObject, Type: name}, value, "$")
}

func (s *Schema) validate(expected Value, value any, path string) error {
	switch expected.Kind {
	case KindString:
		if _, ok := value.(string); ok {
			return nil
		}
	case KindNumber:
		if _, ok := value.(float64); ok {
			return nil
		}
		if literal, ok := value.(string); ok && slices.Contains(expected.Literals, literal) {
			return nil
		}
	case KindBoolean:
		if _, ok := value.(bool); ok {
			return nil
		}
	case KindEnum:
		if literal, ok := value.(string); ok {
			if slices.Contains(expected.Literals, literal) {
				return nil
			}
			return errors.Errorf("%s: value is not one of %s", path, expected)
		}
	case KindList:
		if items, ok := value.([]any); ok {
			for index, item := range items {
				if err := s.validate(*expected.Element, item, path+"["+strconv.Itoa(index)+"]"); err != nil {
					return err
				}
			}
			return nil
		}
	case KindMap:
		if entries, ok := value.(map[string]any); ok {
			for _, key := range slices.Sorted(maps.Keys(entries)) {
				if err := s.validate(*expected.Element, entries[key], path+"["+strconv.Quote(key)+"]"); err != nil {
					return err
				}
			}
			return nil
		}
	case KindObject:
		if object, ok := value.(map[string]any); ok {
			return s.validateObject(expected.Type, object, path)
		}
	}
	return errors.Errorf("%s: expected %s, found %s", path, expected, jsonKind(value))
}

func (s *Schema) validateObject(name string, object map[string]any, path string) error {
	declared, err := s.Type(name)
	if err != nil {
		return errors.Wrap(err, path)
	}
	for _, key := range slices.Sorted(maps.Keys(object)) {
		if _, ok := declared.Field(key); !ok {
			return errors.Errorf("%s: type %q has no field %q", path, name, key)
		}
	}
	for _, field := range declared.Fields {
		value, present := object[field.Name]
		if !present {
			if field.Optional {
				continue
			}
			return errors.Errorf("%s: required field %q of type %q is missing", path, field.Name, name)
		}
		if err := s.validate(field.Value, value, path+"."+field.Name); err != nil {
			return err
		}
	}
	return nil
}

func jsonKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return "an unsupported value"
	}
}
