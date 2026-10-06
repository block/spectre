package jsonschema_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/jsonschema"
	"github.com/block/spectre/internal/schema"
)

func weatherConfig() jsonschema.Config {
	return jsonschema.Config{Module: "weather", Schemas: []string{
		filepath.Join("testdata", "weather.schema.json"),
		filepath.Join("testdata", "alerts.schema.json"),
	}}
}

func TestDeclarationsGolden(t *testing.T) {
	generated, err := jsonschema.Declarations(t.Context(), weatherConfig())
	assert.NoError(t, err)
	golden, err := os.ReadFile(filepath.Join("testdata", "weather.d.ts"))
	assert.NoError(t, err)
	assert.Equal(t, string(golden), generated)
}

func TestDeclarationsSchemaModel(t *testing.T) {
	generated, err := jsonschema.Declarations(t.Context(), weatherConfig())
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), map[string]string{"weather.d.ts": generated})
	assert.NoError(t, err)
	text := schema.Value{Kind: schema.KindString}
	number := schema.Value{Kind: schema.KindNumber}
	object := func(name string) schema.Value { return schema.Value{Kind: schema.KindObject, Type: name} }
	assert.Equal(t, []*schema.Type{
		{Name: "weather.Alert", Fields: []schema.Field{
			{Name: "id", Value: text},
			{Name: "severity", Optional: true, Value: schema.Value{Kind: schema.KindEnum, Literals: []string{"major", "minor"}, Type: "weather.Severity"}},
		}},
		{Name: "weather.Forecast", Fields: []schema.Field{
			{Name: "days", Value: schema.Value{Kind: schema.KindList, Element: new(object("weather.Forecast.Days"))}},
			{Name: "location", Value: text},
			{Name: "next", Optional: true, Value: object("weather.Forecast")},
		}},
		{Name: "weather.Forecast.Days", Fields: []schema.Field{
			{Name: "date", Value: text},
			{Name: "high", Optional: true, Value: object("weather.Temperature")},
			{Name: "precipitation_percent", Optional: true, Value: number},
		}},
		{Name: "weather.GetForecastResponse", Fields: []schema.Field{
			{Name: "alerts", Optional: true, Value: schema.Value{Kind: schema.KindList, Element: new(object("weather.Alert"))}},
			{Name: "content-type", Optional: true, Value: schema.Value{Kind: schema.KindEnum, Literals: []string{"application/json"}}},
			{Name: "forecast", Value: object("weather.Forecast")},
			{Name: "labels", Optional: true, Value: schema.Value{Kind: schema.KindMap, Element: &text}},
			{Name: "success", Optional: true, Value: schema.Value{Kind: schema.KindBoolean}},
		}},
		{Name: "weather.Temperature", Fields: []schema.Field{
			{Name: "degrees", Optional: true, Value: number},
			{Name: "unit", Optional: true, Value: schema.Value{Kind: schema.KindEnum, Literals: []string{"celsius", "fahrenheit"}, Type: "weather.Unit"}},
		}},
		{Name: "weather.Wind", Fields: []schema.Field{
			{Name: "speed_kph", Optional: true, Value: number},
		}},
	}, loaded.Types())
}

func TestDeclarationsResolveInputsByID(t *testing.T) {
	dir := t.TempDir()
	for name, document := range map[string]string{
		"main.json":   `{"$id": "https://example.test/main.json", "title": "Main", "properties": {"common": {"$ref": "common.json"}}}`,
		"common.json": `{"$id": "https://example.test/common.json", "title": "Common", "properties": {"id": {"type": "string"}}}`,
	} {
		assert.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(document), 0o600))
	}
	config := jsonschema.Config{Module: "m", Schemas: []string{filepath.Join(dir, "main.json"), filepath.Join(dir, "common.json")}}
	generated, err := jsonschema.Declarations(t.Context(), config)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), map[string]string{"m.d.ts": generated})
	assert.NoError(t, err)
	assert.Equal(t, []*schema.Type{
		{Name: "m.Common", Fields: []schema.Field{{Name: "id", Optional: true, Value: schema.Value{Kind: schema.KindString}}}},
		{Name: "m.Main", Fields: []schema.Field{{Name: "common", Optional: true, Value: schema.Value{Kind: schema.KindObject, Type: "m.Common"}}}},
	}, loaded.Types())
}

func TestDeclarationsRejectUnsupportedSchemas(t *testing.T) {
	for name, test := range map[string]struct {
		module   string
		schema   string
		expected string
	}{
		"Null": {
			schema:   `{"title": "T", "type": "object", "properties": {"a": {"type": ["string", "null"]}}}`,
			expected: "null is not supported",
		},
		"TypeUnion": {
			schema:   `{"title": "T", "type": "object", "properties": {"a": {"type": ["string", "number"]}}}`,
			expected: "unions of number, string are not supported",
		},
		"MixedEnum": {
			schema:   `{"title": "T", "enum": ["a", 1]}`,
			expected: "unions of number, string are not supported",
		},
		"AnyOf": {
			schema:   `{"title": "T", "anyOf": [{"type": "string"}, {"type": "number"}]}`,
			expected: "anyOf is not supported",
		},
		"Not": {
			schema:   `{"title": "T", "properties": {"id": {"type": "string"}}, "not": {"required": ["id"]}}`,
			expected: "not is not supported",
		},
		"Tuple": {
			schema:   `{"title": "T", "type": "array", "prefixItems": [{"type": "string"}]}`,
			expected: "prefixItems is not supported",
		},
		"ArrayWithoutItems": {
			schema:   `{"title": "T", "type": "array"}`,
			expected: "arrays must declare items",
		},
		"FreeFormObject": {
			schema:   `{"title": "T", "type": "object", "properties": {"a": {"type": "object"}}}`,
			expected: "objects must declare properties or an additionalProperties schema",
		},
		"EmptyPropertiesFreeFormObject": {
			schema:   `{"title": "T", "type": "object", "properties": {}}`,
			expected: "objects must declare properties or an additionalProperties schema",
		},
		"AnyValue": {
			schema:   `{"title": "T", "type": "object", "properties": {"a": {}}}`,
			expected: "accepts any JSON value",
		},
		"PropertiesAndAdditionalSchema": {
			schema:   `{"title": "T", "properties": {"a": {"type": "string"}}, "additionalProperties": {"type": "string"}}`,
			expected: "objects cannot declare both properties and an additionalProperties schema",
		},
		"RequiredUndeclared": {
			schema:   `{"title": "T", "properties": {}, "required": ["a"]}`,
			expected: `required property "a" is not declared`,
		},
		"RequiredMapKey": {
			schema:   `{"title": "T", "type": "object", "required": ["tenant"], "additionalProperties": {"type": "string"}}`,
			expected: `required property "tenant" is not declared`,
		},
		"RefWithRequired": {
			schema:   `{"title": "T", "properties": {"a": {"$ref": "#/$defs/A", "required": ["id"]}}, "$defs": {"A": {"properties": {"id": {"type": "string"}}}}}`,
			expected: "$ref cannot be combined with other type keywords",
		},
		"RefWithType": {
			schema:   `{"title": "T", "properties": {"a": {"$ref": "#/$defs/A", "type": "string"}}, "$defs": {"A": {"type": "string"}}}`,
			expected: "$ref cannot be combined with other type keywords",
		},
		"RefCycle": {
			schema:   `{"$defs": {"A": {"$ref": "#/$defs/A"}}}`,
			expected: "$ref chain refers back to itself",
		},
		"MutualRefCycle": {
			schema:   `{"$defs": {"A": {"$ref": "#/$defs/B"}, "B": {"$ref": "#/$defs/A"}}}`,
			expected: "$ref chain refers back to itself",
		},
		"DependentRequired": {
			schema:   `{"title": "T", "properties": {"card": {"type": "string"}, "billing": {"type": "string"}}, "dependentRequired": {"card": ["billing"]}}`,
			expected: "dependentRequired is not supported",
		},
		"Dependencies": {
			schema:   `{"$schema": "http://json-schema.org/draft-07/schema#", "title": "T", "properties": {"card": {"type": "string"}, "billing": {"type": "string"}}, "dependencies": {"card": ["billing"]}}`,
			expected: "dependencies is not supported",
		},
		"AliasCycle": {
			schema:   `{"$defs": {"A": {"type": "array", "items": {"$ref": "#/$defs/A"}}}}`,
			expected: "schema contains itself without an object in between",
		},
		"DuplicateName": {
			schema:   `{"$defs": {"foo": {"type": "string"}, "Foo": {"type": "string"}}}`,
			expected: `type "m.Foo" is declared more than once`,
		},
		"InvalidName": {
			schema:   `{"$defs": {"1st": {"type": "string"}}}`,
			expected: `cannot derive a type name from "1st"`,
		},
		"ReservedModule": {
			module:   "spectre",
			schema:   `{"title": "T", "properties": {"a": {"type": "string"}}}`,
			expected: `module "spectre" is reserved`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "schema.json")
			assert.NoError(t, os.WriteFile(path, []byte(test.schema), 0o600))
			module := test.module
			if module == "" {
				module = "m"
			}
			_, err := jsonschema.Declarations(t.Context(), jsonschema.Config{Module: module, Schemas: []string{path}})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.expected)
		})
	}
}
