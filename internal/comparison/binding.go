package comparison

import (
	"encoding/base64"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// bindHTTPParameters binds path wildcards and query parameters over a decoded body by
// the google.api.http rules. Each field is bound once, so no source overrides another.
func bindHTTPParameters(message protoreflect.Message, request Request, wildcards map[string]string) error {
	if request.Method == http.MethodGet && len(request.Body) > 0 {
		return errors.New("GET request has a body")
	}
	for _, name := range slices.Sorted(maps.Keys(wildcards)) {
		if err := bindField(message, name, []string{wildcards[name]}, false); err != nil {
			return errors.Wrapf(err, "bind path wildcard %q", name)
		}
	}
	query, err := url.ParseQuery(request.RawQuery)
	if err != nil {
		return errors.Wrap(err, "parse query")
	}
	for _, name := range slices.Sorted(maps.Keys(query)) {
		if err := bindField(message, name, query[name], true); err != nil {
			return errors.Wrapf(err, "bind query parameter %q", name)
		}
	}
	return nil
}

// bindField parses values into the field a dotted name selects.
func bindField(message protoreflect.Message, name string, values []string, repeatable bool) error {
	path, err := resolveBinding(message.Descriptor(), name, repeatable)
	if err != nil {
		return err
	}
	for _, parent := range path[:len(path)-1] {
		// Mutable would silently clear a oneof member bound by another source.
		if otherOneofMemberSet(message, parent) {
			return errors.Errorf("field %q conflicts with a bound oneof member", parent.FullName())
		}
		message = message.Mutable(parent).Message()
	}
	field := path[len(path)-1]
	if message.Has(field) || otherOneofMemberSet(message, field) {
		return errors.Errorf("field %q is already bound", field.FullName())
	}
	if !field.IsList() {
		if len(values) != 1 {
			return errors.Errorf("field %q is not repeated", field.FullName())
		}
		value, err := parseFieldValue(field, values[0])
		if err != nil {
			return err
		}
		message.Set(field, value)
		return nil
	}
	list := message.Mutable(field).List()
	for _, raw := range values {
		value, err := parseFieldValue(field, raw)
		if err != nil {
			return err
		}
		list.Append(value)
	}
	return nil
}

func otherOneofMemberSet(message protoreflect.Message, field protoreflect.FieldDescriptor) bool {
	oneof := field.ContainingOneof()
	if oneof == nil {
		return false
	}
	set := message.WhichOneof(oneof)
	return set != nil && set.Number() != field.Number()
}

// requireExplicitPresence checks that every singular scalar in a raw HTTP input has
// presence, because ProtoJSON drops implicit defaults such as an explicit "?location=".
func requireExplicitPresence(descriptor protoreflect.MessageDescriptor) error {
	visited := map[protoreflect.FullName]bool{}
	var check func(message protoreflect.MessageDescriptor) error
	check = func(message protoreflect.MessageDescriptor) error {
		// Well-known types have their own JSON form rather than one key per field.
		if visited[message.FullName()] || strings.HasPrefix(string(message.FullName()), "google.protobuf.") {
			return nil
		}
		visited[message.FullName()] = true
		fields := message.Fields()
		for index := range fields.Len() {
			field := fields.Get(index)
			if field.IsMap() {
				field = field.MapValue()
			}
			switch {
			case field.Message() != nil:
				if err := check(field.Message()); err != nil {
					return err
				}
			case !field.IsList() && !field.HasPresence():
				return errors.Errorf("field %q must be optional so an explicit default differs from a missing value", field.FullName())
			}
		}
		return nil
	}
	return check(descriptor)
}

// resolveBinding resolves a dotted field name through singular messages to a
// scalar or enum field. Path wildcards are not repeatable.
func resolveBinding(descriptor protoreflect.MessageDescriptor, name string, repeatable bool) ([]protoreflect.FieldDescriptor, error) {
	parts := strings.Split(name, ".")
	path := make([]protoreflect.FieldDescriptor, 0, len(parts))
	current := descriptor
	for index, part := range parts {
		fields := current.Fields()
		field := fields.ByName(protoreflect.Name(part))
		if field == nil {
			field = fields.ByJSONName(part)
		}
		if field == nil {
			return nil, errors.Errorf("message %q has no field %q", current.FullName(), part)
		}
		path = append(path, field)
		if index == len(parts)-1 {
			break
		}
		if field.Message() == nil || field.IsList() || field.IsMap() {
			return nil, errors.Errorf("field %q is not a singular message", field.FullName())
		}
		current = field.Message()
	}
	field := path[len(path)-1]
	if field.IsMap() || field.Message() != nil {
		return nil, errors.Errorf("field %q is not a scalar or enum", field.FullName())
	}
	if field.IsList() && !repeatable {
		return nil, errors.Errorf("field %q is repeated", field.FullName())
	}
	return path, nil
}

// parseFieldValue parses one text value for a scalar or enum field. Enums accept
// value names and numbers, and bytes accept any base64 alphabet, as in ProtoJSON.
func parseFieldValue(field protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	value, err := parseScalar(field, raw)
	if err != nil {
		return protoreflect.Value{}, errors.Wrapf(err, "parse field %q", field.FullName())
	}
	return value, nil
}

func parseScalar(field protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	switch field.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BytesKind:
		encoding := base64.StdEncoding
		if strings.ContainsAny(raw, "-_") {
			encoding = base64.URLEncoding
		}
		if len(raw)%4 != 0 {
			encoding = encoding.WithPadding(base64.NoPadding)
		}
		value, err := encoding.DecodeString(raw)
		return protoreflect.ValueOfBytes(value), errors.WithStack(err)
	case protoreflect.BoolKind:
		value, err := strconv.ParseBool(raw)
		return protoreflect.ValueOfBool(value), errors.WithStack(err)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		value, err := strconv.ParseInt(raw, 10, 32)
		return protoreflect.ValueOfInt32(int32(value)), errors.WithStack(err)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		value, err := strconv.ParseInt(raw, 10, 64)
		return protoreflect.ValueOfInt64(value), errors.WithStack(err)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		value, err := strconv.ParseUint(raw, 10, 32)
		return protoreflect.ValueOfUint32(uint32(value)), errors.WithStack(err)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		value, err := strconv.ParseUint(raw, 10, 64)
		return protoreflect.ValueOfUint64(value), errors.WithStack(err)
	case protoreflect.FloatKind:
		value, err := strconv.ParseFloat(raw, 32)
		return protoreflect.ValueOfFloat32(float32(value)), errors.WithStack(err)
	case protoreflect.DoubleKind:
		value, err := strconv.ParseFloat(raw, 64)
		return protoreflect.ValueOfFloat64(value), errors.WithStack(err)
	case protoreflect.EnumKind:
		if value := field.Enum().Values().ByName(protoreflect.Name(raw)); value != nil {
			return protoreflect.ValueOfEnum(value.Number()), nil
		}
		number, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, errors.Errorf("enum %q has no value %q", field.Enum().FullName(), raw)
		}
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(number)), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
	}
	return protoreflect.Value{}, errors.Errorf("field kind %s cannot be bound", field.Kind())
}
