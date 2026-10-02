package comparison

import (
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/schema"
)

// bindHTTPParameters binds each field once, so path and query values cannot
// overwrite a body value or one another. Required fields are validated afterwards.
func bindHTTPParameters(loaded *schema.Schema, root *schema.Type, object map[string]any, request Request, wildcards map[string]string) error {
	if request.Method == http.MethodGet && len(request.Body) > 0 {
		return errors.New("GET request has a body")
	}
	for _, name := range slices.Sorted(maps.Keys(wildcards)) {
		if err := bindField(loaded, root, object, name, []string{wildcards[name]}, false); err != nil {
			return errors.Wrapf(err, "bind path wildcard %q", name)
		}
	}
	query, err := url.ParseQuery(request.RawQuery)
	if err != nil {
		return errors.Wrap(err, "parse query")
	}
	for _, name := range slices.Sorted(maps.Keys(query)) {
		if err := bindField(loaded, root, object, name, query[name], true); err != nil {
			return errors.Wrapf(err, "bind query parameter %q", name)
		}
	}
	return nil
}

func bindField(loaded *schema.Schema, root *schema.Type, object map[string]any, name string, values []string, repeatable bool) error {
	path, err := resolveBinding(loaded, root, name, repeatable)
	if err != nil {
		return err
	}
	for _, parent := range path[:len(path)-1] {
		value, present := object[parent.Name]
		if !present {
			value = map[string]any{}
			object[parent.Name] = value
		}
		var ok bool
		object, ok = value.(map[string]any)
		if !ok {
			return errors.Errorf("field %q is not an object", parent.Name)
		}
	}
	field := path[len(path)-1]
	if _, present := object[field.Name]; present {
		return errors.Errorf("field %q is already bound", name)
	}
	if field.Value.Kind != schema.KindList {
		if len(values) != 1 {
			return errors.Errorf("field %q is not repeated", name)
		}
		value, err := parseScalar(field.Value, values[0])
		if err != nil {
			return errors.Wrapf(err, "parse field %q", name)
		}
		object[field.Name] = value
		return nil
	}
	list := make([]any, 0, len(values))
	for _, raw := range values {
		value, err := parseScalar(*field.Value.Element, raw)
		if err != nil {
			return errors.Wrapf(err, "parse field %q", name)
		}
		list = append(list, value)
	}
	object[field.Name] = list
	return nil
}

// resolveBinding resolves JSON field names through singular objects to scalars.
// Query fields may be lists of scalars; path wildcards may not.
func resolveBinding(loaded *schema.Schema, root *schema.Type, name string, repeatable bool) ([]schema.Field, error) {
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
		if field.Value.Kind != schema.KindObject {
			return nil, errors.Errorf("field %q is not a singular object", part)
		}
		var err error
		current, err = loaded.Type(field.Value.Type)
		if err != nil {
			return nil, errors.WithStack(err)
		}
	}
	value := path[len(path)-1].Value
	if value.Kind == schema.KindList {
		if !repeatable {
			return nil, errors.Errorf("field %q is repeated", name)
		}
		value = *value.Element
	}
	switch value.Kind {
	case schema.KindString, schema.KindNumber, schema.KindBoolean, schema.KindEnum:
		return path, nil
	case schema.KindObject, schema.KindList, schema.KindMap:
		return nil, errors.Errorf("field %q is not a scalar", name)
	default:
		return nil, errors.Errorf("field %q is not a scalar", name)
	}
}

func parseScalar(value schema.Value, raw string) (any, error) {
	switch value.Kind {
	case schema.KindString:
		return raw, nil
	case schema.KindNumber:
		if slices.Contains(value.Literals, raw) {
			return raw, nil
		}
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, errors.Wrap(err, "parse number")
		}
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, errors.New("number must be finite")
		}
		return number, nil
	case schema.KindBoolean:
		boolean, err := strconv.ParseBool(raw)
		return boolean, errors.Wrap(err, "parse boolean")
	case schema.KindEnum:
		if !slices.Contains(value.Literals, raw) {
			return nil, errors.New("value is not a declared enum literal")
		}
		return raw, nil
	case schema.KindObject, schema.KindList, schema.KindMap:
		return nil, errors.Errorf("type %s cannot be bound from text", value)
	default:
		return nil, errors.Errorf("type %s cannot be bound from text", value)
	}
}
