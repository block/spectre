package message

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/schema"
)

// bindHTTPParameters returns the body members with path and query values bound. Each field
// binds once, so values cannot overwrite a body value or one another. Callers validate it.
func bindHTTPParameters(root *schema.ObjectType, members map[string]schema.Value, request Request, wildcards map[string]string) (schema.Value, error) {
	if request.Method == http.MethodGet && len(request.Body) > 0 {
		return nil, errors.New("GET request has a body")
	}
	for _, name := range slices.Sorted(maps.Keys(wildcards)) {
		if err := bindField(root, members, name, []string{wildcards[name]}, false); err != nil {
			return nil, errors.Wrapf(err, "bind path wildcard %q", name)
		}
	}
	query, err := url.ParseQuery(request.RawQuery)
	if err != nil {
		// The escape error quotes the query, which may carry values.
		return nil, errors.New("query is not valid URL encoding")
	}
	for _, name := range slices.Sorted(maps.Keys(query)) {
		if err := bindField(root, members, name, query[name], true); err != nil {
			return nil, errors.Wrapf(err, "bind query parameter %q", name)
		}
	}
	return errors.WithStack2(schema.NewObject(members))
}

func bindField(root *schema.ObjectType, members map[string]schema.Value, name string, values []string, repeatable bool) error {
	path, err := resolveBinding(root, name, repeatable)
	if err != nil {
		return err
	}
	declared := schema.NonNull(path[len(path)-1].Type)
	list, repeated := declared.(schema.ListType)
	if !repeated {
		if len(values) != 1 {
			return errors.Errorf("field %q is not repeated", name)
		}
		value, err := parseScalar(declared, values[0])
		if err != nil {
			return errors.Wrapf(err, "parse field %q", name)
		}
		return bindValue(members, path, name, value)
	}
	elements := make([]schema.Value, 0, len(values))
	for _, raw := range values {
		element, err := parseScalar(schema.NonNull(list.Element), raw)
		if err != nil {
			return errors.Wrapf(err, "parse field %q", name)
		}
		elements = append(elements, element)
	}
	value, err := schema.NewList(elements)
	if err != nil {
		return errors.WithStack(err)
	}
	return bindValue(members, path, name, value)
}

// bindValue sets value at path in members, adding the objects that path passes through.
func bindValue(members map[string]schema.Value, path []schema.Field, name string, value schema.Value) error {
	field := path[0]
	member, present := members[field.Name]
	if len(path) == 1 {
		if present {
			return errors.Errorf("field %q is already bound", name)
		}
		members[field.Name] = value
		return nil
	}
	nested := map[string]schema.Value{}
	if present {
		object, ok := member.(schema.ObjectValue)
		if !ok {
			return errors.Errorf("field %q is not an object", field.Name)
		}
		nested = object.Members()
	}
	if err := bindValue(nested, path[1:], name, value); err != nil {
		return err
	}
	rebuilt, err := schema.NewObject(nested)
	if err != nil {
		return errors.WithStack(err)
	}
	members[field.Name] = rebuilt
	return nil
}

// resolveBinding resolves JSON field names through singular objects to scalars.
// Query fields may be lists of scalars; path wildcards may not.
func resolveBinding(root *schema.ObjectType, name string, repeatable bool) ([]schema.Field, error) {
	parts := strings.Split(name, ".")
	path := make([]schema.Field, 0, len(parts))
	current := root
	for index, part := range parts {
		field, found := current.Field(part)
		if !found {
			return nil, errors.Errorf("type %q has no field %q", current.Name, part)
		}
		path = append(path, field)
		if index == len(parts)-1 {
			break
		}
		object, isObject := schema.NonNull(field.Type).(*schema.ObjectType)
		if !isObject {
			return nil, errors.Errorf("field %q is not a singular object", part)
		}
		current = object
	}
	declared := schema.NonNull(path[len(path)-1].Type)
	if list, repeated := declared.(schema.ListType); repeated {
		if !repeatable {
			return nil, errors.Errorf("field %q is repeated", name)
		}
		declared = schema.NonNull(list.Element)
	}
	switch declared.(type) {
	case schema.StringType, schema.NumberType, schema.BooleanType, schema.EnumType:
		return path, nil
	case *schema.ObjectType, schema.ListType, schema.MapType, schema.UnknownType, schema.NullableType:
		return nil, errors.Errorf("field %q is not a scalar", name)
	}
	return nil, errors.Errorf("field %q is not a scalar", name)
}

func parseScalar(declared schema.Type, raw string) (schema.Value, error) {
	switch declared := declared.(type) {
	case schema.StringType:
		return schema.NewString(raw), nil
	case schema.NumberType:
		if slices.Contains(declared.Literals, raw) {
			return schema.NewString(raw), nil
		}
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, errors.Wrap(numberError(err), "parse number")
		}
		return errors.WithStack2(schema.NewNumber(number))
	case schema.BooleanType:
		boolean, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.Wrap(numberError(err), "parse boolean")
		}
		return schema.NewBoolean(boolean), nil
	case schema.EnumType:
		if !slices.Contains(declared.Literals, raw) {
			return nil, errors.New("value is not a declared enum literal")
		}
		return schema.NewString(raw), nil
	case *schema.ObjectType, schema.ListType, schema.MapType, schema.UnknownType, schema.NullableType:
		return nil, errors.Errorf("type %s cannot be bound from text", declared)
	}
	return nil, errors.Errorf("type %s cannot be bound from text", declared)
}
