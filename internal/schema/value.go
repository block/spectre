package schema

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
)

// Value is an immutable JSON value and its type. New functions build unknown values,
// As and Decode check JSON against a type, and only Without may drop a required field.
//
//sumtype:decl
type Value interface {
	json.Marshaler
	// Type returns the type the value was checked against.
	Type() Type
	// JSONType returns the value's JSON shape.
	JSONType() model.JSONType
	// Equal reports whether two values hold the same JSON, whatever their types.
	Equal(other Value) bool
	isValue()
}

// NullValue is JSON null.
type NullValue struct {
	declared Type
}

// StringValue is a JSON string.
type StringValue struct {
	declared Type
	text     string
}

// NumberValue is a finite JSON number.
type NumberValue struct {
	declared Type
	number   float64
}

// BooleanValue is a JSON boolean.
type BooleanValue struct {
	declared Type
	boolean  bool
}

// ListValue is a JSON array. Its elements are never nil, and never shared with callers.
type ListValue struct {
	declared Type
	elements []Value
}

// ObjectValue is a JSON object. Its members are never nil, and never shared with callers.
type ObjectValue struct {
	declared Type
	members  map[string]Value
}

// NewNull returns null of type unknown.
func NewNull() Value {
	return NullValue{declared: UnknownType{}}
}

// NewString returns a string of type unknown.
func NewString(text string) Value {
	return StringValue{declared: UnknownType{}, text: text}
}

// NewBoolean returns a boolean of type unknown.
func NewBoolean(boolean bool) Value {
	return BooleanValue{declared: UnknownType{}, boolean: boolean}
}

// NewNumber returns a number of type unknown. JSON has no non-finite numbers.
func NewNumber(number float64) (Value, error) {
	if math.IsInf(number, 0) || math.IsNaN(number) {
		return nil, errors.New("number must be finite")
	}
	return NumberValue{declared: UnknownType{}, number: number}, nil
}

// NewList returns an array of type unknown holding a copy of elements.
func NewList(elements []Value) (Value, error) {
	if slices.Contains(elements, nil) {
		return nil, errors.New("list element is missing")
	}
	return ListValue{declared: UnknownType{}, elements: append([]Value{}, elements...)}, nil
}

// NewObject returns an object of type unknown holding a copy of members.
func NewObject(members map[string]Value) (Value, error) {
	for _, member := range members {
		if member == nil {
			return nil, errors.New("object member is missing")
		}
	}
	copied := make(map[string]Value, len(members))
	maps.Copy(copied, members)
	return ObjectValue{declared: UnknownType{}, members: copied}, nil
}

// Decode decodes JSON and checks it against declared. Errors never quote the JSON.
func Decode(declared Type, data []byte) (Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	parsed, err := decodeJSON(decoder)
	if syntax, ok := errors.AsType[*json.SyntaxError](err); ok {
		return nil, errors.Errorf("invalid JSON at offset %d", syntax.Offset)
	} else if err != nil {
		return nil, errors.New("invalid JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid JSON")
	}
	return As(declared, parsed)
}

// decodeJSON reads one JSON value. A repeated object key keeps its last value, as
// encoding/json does.
func decodeJSON(decoder *json.Decoder) (Value, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	switch token := token.(type) {
	case nil:
		return NewNull(), nil
	case bool:
		return NewBoolean(token), nil
	case string:
		return NewString(token), nil
	case json.Number:
		number, err := token.Float64()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		return NewNumber(number)
	case json.Delim:
		if token == '[' {
			elements := []Value{}
			for decoder.More() {
				element, err := decodeJSON(decoder)
				if err != nil {
					return nil, err
				}
				elements = append(elements, element)
			}
			return ListValue{declared: UnknownType{}, elements: elements}, closeJSON(decoder)
		}
		members := map[string]Value{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, errors.WithStack(err)
			}
			member, err := decodeJSON(decoder)
			if err != nil {
				return nil, err
			}
			members[key.(string)] = member //nolint:forcetypeassert // The decoder only yields string keys.
		}
		return ObjectValue{declared: UnknownType{}, members: members}, closeJSON(decoder)
	}
	return nil, errors.New("unexpected JSON token")
}

func closeJSON(decoder *json.Decoder) error {
	_, err := decoder.Token()
	return errors.WithStack(err)
}

// As checks value's JSON against declared and returns it with that type. It rejects
// unknown keys, and nulls outside nullable types. Errors never quote payload keys.
func As(declared Type, value Value) (Value, error) {
	if value == nil {
		return nil, errors.New("value is missing")
	}
	return assign(declared, value, "$")
}

// assign gives value the type slot. A member of a nullable or unknown type has
// that type, so reference and candidate values of one slot have equal types.
func assign(slot Type, value Value, path string) (Value, error) {
	inner := slot
	if nullable, ok := slot.(NullableType); ok {
		if _, null := value.(NullValue); null {
			return NullValue{declared: slot}, nil
		}
		inner = nullable.Type
	}
	switch inner := inner.(type) {
	case nil:
		return nil, errors.Errorf("%s: type is missing", path)
	case UnknownType:
		return unknown(slot, value), nil
	case StringType:
		if text, ok := value.(StringValue); ok {
			return StringValue{declared: slot, text: text.text}, nil
		}
	case NumberType:
		if number, ok := value.(NumberValue); ok {
			return NumberValue{declared: slot, number: number.number}, nil
		}
		if text, ok := value.(StringValue); ok && slices.Contains(inner.Literals, text.text) {
			return StringValue{declared: slot, text: text.text}, nil
		}
	case BooleanType:
		if boolean, ok := value.(BooleanValue); ok {
			return BooleanValue{declared: slot, boolean: boolean.boolean}, nil
		}
	case EnumType:
		if text, ok := value.(StringValue); ok {
			if slices.Contains(inner.Literals, text.text) {
				return StringValue{declared: slot, text: text.text}, nil
			}
			return nil, errors.Errorf("%s: value is not one of %s", path, slot)
		}
	case ListType:
		if list, ok := value.(ListValue); ok {
			elements := make([]Value, len(list.elements))
			for index, element := range list.elements {
				assigned, err := assign(inner.Element, element, path+"["+strconv.Itoa(index)+"]")
				if err != nil {
					return nil, err
				}
				elements[index] = assigned
			}
			return ListValue{declared: slot, elements: elements}, nil
		}
	case MapType:
		if object, ok := value.(ObjectValue); ok {
			members := make(map[string]Value, len(object.members))
			for _, key := range slices.Sorted(maps.Keys(object.members)) {
				// A map key is payload data, so the path holds a wildcard instead.
				assigned, err := assign(inner.Element, object.members[key], path+"[*]")
				if err != nil {
					return nil, err
				}
				members[key] = assigned
			}
			return ObjectValue{declared: slot, members: members}, nil
		}
	case *ObjectType:
		if object, ok := value.(ObjectValue); ok {
			return assignObject(slot, inner, object.members, path)
		}
	case NullableType:
	}
	return nil, errors.Errorf("%s: expected %s, found %s", path, slot, describeJSONType(value.JSONType()))
}

func assignObject(slot Type, declared *ObjectType, object map[string]Value, path string) (Value, error) {
	for _, key := range slices.Sorted(maps.Keys(object)) {
		if _, ok := declared.Field(key); !ok {
			// An undeclared key is payload data, so the error does not name it.
			return nil, errors.Errorf("%s: type %q has a field it does not declare", path, declared.Name)
		}
	}
	members := make(map[string]Value, len(object))
	for _, field := range declared.Fields {
		member, present := object[field.Name]
		if !present {
			if field.Optional {
				continue
			}
			return nil, errors.Errorf("%s: required field %q of type %q is missing", path, field.Name, declared.Name)
		}
		assigned, err := assign(field.Type, member, path+"."+field.Name)
		if err != nil {
			return nil, err
		}
		members[field.Name] = assigned
	}
	return ObjectValue{declared: slot, members: members}, nil
}

// unknown gives value the type slot, and its members type unknown.
func unknown(slot Type, value Value) Value {
	switch value := value.(type) {
	case NullValue:
		return NullValue{declared: slot}
	case StringValue:
		return StringValue{declared: slot, text: value.text}
	case NumberValue:
		return NumberValue{declared: slot, number: value.number}
	case BooleanValue:
		return BooleanValue{declared: slot, boolean: value.boolean}
	case ListValue:
		elements := make([]Value, len(value.elements))
		for index, element := range value.elements {
			elements[index] = unknown(UnknownType{}, element)
		}
		return ListValue{declared: slot, elements: elements}
	case ObjectValue:
		members := make(map[string]Value, len(value.members))
		for key, member := range value.members {
			members[key] = unknown(UnknownType{}, member)
		}
		return ObjectValue{declared: slot, members: members}
	}
	return value
}

func describeJSONType(jsonType model.JSONType) string {
	switch jsonType {
	case model.JSONNull:
		return "null"
	case model.JSONList:
		return "an array"
	case model.JSONObject:
		return "an object"
	case model.JSONBoolean, model.JSONNumber, model.JSONString:
	}
	return "a " + string(jsonType)
}

func (NullValue) isValue()    {}
func (StringValue) isValue()  {}
func (NumberValue) isValue()  {}
func (BooleanValue) isValue() {}
func (ListValue) isValue()    {}
func (ObjectValue) isValue()  {}

// Type returns the type the value was checked against.
func (v NullValue) Type() Type { return v.declared }

// JSONType returns null.
func (NullValue) JSONType() model.JSONType { return model.JSONNull }

// Equal reports whether other is also null.
func (NullValue) Equal(other Value) bool {
	_, ok := other.(NullValue)
	return ok
}

// MarshalJSON encodes null.
func (NullValue) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// Type returns the type the value was checked against.
func (v StringValue) Type() Type { return v.declared }

// JSONType returns string.
func (StringValue) JSONType() model.JSONType { return model.JSONString }

// Text returns the string.
func (v StringValue) Text() string { return v.text }

// Equal reports whether other is the same string.
func (v StringValue) Equal(other Value) bool {
	text, ok := other.(StringValue)
	return ok && v.text == text.text
}

// MarshalJSON encodes the string.
func (v StringValue) MarshalJSON() ([]byte, error) {
	return errors.WithStack2(json.Marshal(v.text))
}

// Type returns the type the value was checked against.
func (v NumberValue) Type() Type { return v.declared }

// JSONType returns number.
func (NumberValue) JSONType() model.JSONType { return model.JSONNumber }

// Float returns the number.
func (v NumberValue) Float() float64 { return v.number }

// Equal reports whether other is the same number.
func (v NumberValue) Equal(other Value) bool {
	number, ok := other.(NumberValue)
	return ok && v.number == number.number
}

// MarshalJSON encodes the number.
func (v NumberValue) MarshalJSON() ([]byte, error) {
	return errors.WithStack2(json.Marshal(v.number))
}

// Type returns the type the value was checked against.
func (v BooleanValue) Type() Type { return v.declared }

// JSONType returns boolean.
func (BooleanValue) JSONType() model.JSONType { return model.JSONBoolean }

// Bool returns the boolean.
func (v BooleanValue) Bool() bool { return v.boolean }

// Equal reports whether other is the same boolean.
func (v BooleanValue) Equal(other Value) bool {
	boolean, ok := other.(BooleanValue)
	return ok && v.boolean == boolean.boolean
}

// MarshalJSON encodes the boolean.
func (v BooleanValue) MarshalJSON() ([]byte, error) {
	return errors.WithStack2(json.Marshal(v.boolean))
}

// Type returns the type the value was checked against.
func (v ListValue) Type() Type { return v.declared }

// JSONType returns list.
func (ListValue) JSONType() model.JSONType { return model.JSONList }

// Len returns the number of elements.
func (v ListValue) Len() int { return len(v.elements) }

// Element returns the element at index, if there is one.
func (v ListValue) Element(index int) (element Value, ok bool) {
	if index < 0 || index >= len(v.elements) {
		return nil, false
	}
	return v.elements[index], true
}

// Elements returns a copy of the elements.
func (v ListValue) Elements() []Value { return slices.Clone(v.elements) }

// With returns the list with element at index, checked against the element type.
func (v ListValue) With(index int, element Value) (Value, error) {
	if index < 0 || index >= len(v.elements) {
		return nil, errors.New("list has no element at the index")
	}
	slot := Type(UnknownType{})
	if list, ok := NonNull(v.declared).(ListType); ok {
		slot = list.Element
	}
	assigned, err := As(slot, element)
	if err != nil {
		return nil, err
	}
	elements := slices.Clone(v.elements)
	elements[index] = assigned
	return typedList(v.declared, elements), nil
}

// Without returns the list without the element at index, if it has one.
func (v ListValue) Without(index int) Value {
	if index < 0 || index >= len(v.elements) {
		return v
	}
	return typedList(v.declared, slices.Delete(slices.Clone(v.elements), index, index+1))
}

// typedList returns a list of type declared, which must already type its elements.
func typedList(declared Type, elements []Value) ListValue {
	return ListValue{declared: declared, elements: elements}
}

// Equal reports whether other is a list of equal elements in the same order.
func (v ListValue) Equal(other Value) bool {
	list, ok := other.(ListValue)
	return ok && slices.EqualFunc(v.elements, list.elements, Value.Equal)
}

// MarshalJSON encodes the elements.
func (v ListValue) MarshalJSON() ([]byte, error) {
	return errors.WithStack2(json.Marshal(append([]Value{}, v.elements...)))
}

// Type returns the type the value was checked against.
func (v ObjectValue) Type() Type { return v.declared }

// JSONType returns object.
func (ObjectValue) JSONType() model.JSONType { return model.JSONObject }

// Len returns the number of members.
func (v ObjectValue) Len() int { return len(v.members) }

// Member returns the member with the given key, if there is one.
func (v ObjectValue) Member(key string) (member Value, ok bool) {
	member, ok = v.members[key]
	return member, ok
}

// Keys returns the members' keys in order.
func (v ObjectValue) Keys() []string { return slices.Sorted(maps.Keys(v.members)) }

// Members returns a copy of the members.
func (v ObjectValue) Members() map[string]Value { return maps.Clone(v.members) }

// With returns the object with member at key, checked against the key's declared type.
func (v ObjectValue) With(key string, member Value) (Value, error) {
	slot := Type(UnknownType{})
	switch declared := NonNull(v.declared).(type) {
	case *ObjectType:
		field, ok := declared.Field(key)
		if !ok {
			// An undeclared key may be payload data, so the error does not name it.
			return nil, errors.Errorf("type %q does not declare the field", declared.Name)
		}
		slot = field.Type
	case MapType:
		slot = declared.Element
	case StringType, NumberType, BooleanType, EnumType, UnknownType, NullableType, ListType:
	}
	assigned, err := As(slot, member)
	if err != nil {
		return nil, err
	}
	members := maps.Clone(v.members)
	if members == nil {
		members = map[string]Value{}
	}
	members[key] = assigned
	return typedObject(v.declared, members), nil
}

// Without returns the object without the member at key. It keeps the object's type
// even when the member is required, as normalisers remove values to skip comparing them.
func (v ObjectValue) Without(key string) Value {
	members := maps.Clone(v.members)
	delete(members, key)
	return typedObject(v.declared, members)
}

// typedObject returns an object of type declared, which must already type its members.
func typedObject(declared Type, members map[string]Value) ObjectValue {
	return ObjectValue{declared: declared, members: members}
}

// Equal reports whether other is an object of equal members.
func (v ObjectValue) Equal(other Value) bool {
	object, ok := other.(ObjectValue)
	return ok && maps.EqualFunc(v.members, object.members, Value.Equal)
}

// MarshalJSON encodes the members with sorted keys.
func (v ObjectValue) MarshalJSON() ([]byte, error) {
	members := v.members
	if members == nil {
		members = map[string]Value{}
	}
	return errors.WithStack2(json.Marshal(members))
}
