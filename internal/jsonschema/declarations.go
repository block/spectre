package jsonschema

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	compiled "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/block/spectre/internal/schema"
)

// Declarations renders the types the configured schemas describe as one
// TypeScript declaration file for the configured module.
func Declarations(ctx context.Context, config Config) (string, error) {
	if err := config.Validate(); err != nil {
		return "", err
	}
	compiler := compiled.NewCompiler()
	compiler.DefaultDraft(compiled.Draft2020)
	documents := make([]any, 0, len(config.Schemas))
	for _, path := range config.Schemas {
		document, err := readDocument(path)
		if err != nil {
			return "", err
		}
		// Adding every input before compiling lets inputs refer to each other.
		if err := compiler.AddResource(path, document); err != nil {
			return "", errors.Wrapf(err, "add %q", path)
		}
		documents = append(documents, document)
	}
	converter := newConverter(config.Module)
	for index, path := range config.Schemas {
		if err := converter.declareDocument(compiler, path, documents[index]); err != nil {
			return "", err
		}
	}
	declarations, err := converter.convert()
	if err != nil {
		return "", err
	}
	rendered := newWriter(config.Module).file(config.Schemas, declarations)
	// Valid JSON Schema names can be TypeScript keywords, so parse the output to catch them.
	if _, err := schema.ParseSources(ctx, map[string]string{"generated.d.ts": rendered}); err != nil {
		return "", errors.Wrap(err, "parse generated declarations")
	}
	return rendered, nil
}

// WriteDeclarations renders the declarations for config into the file at output.
func WriteDeclarations(ctx context.Context, config Config, output string) error {
	declarations, err := Declarations(ctx, config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		return errors.Wrap(err, "create declarations directory")
	}
	return errors.Wrapf(os.WriteFile(output, []byte(declarations), 0o600), "write %q", output)
}

func readDocument(path string) (any, error) {
	file, err := os.Open(path) //nolint:gosec // The path is a schema the user asked to read.
	if err != nil {
		return nil, errors.Wrap(err, "open schema")
	}
	defer file.Close() //nolint:errcheck // Closing a read-only file cannot lose data.
	document, err := compiled.UnmarshalJSON(file)
	return document, errors.Wrapf(err, "read %q", path)
}

// declaration is one exported type. Inline object schemas are hoisted into nested
// declarations, which render in a namespace merged with their parent.
type declaration struct {
	source *compiled.Schema
	// name is the module name followed by the namespace path.
	name   string
	local  string
	object bool
	fields []schema.Field
	alias  schema.Value
	nested []*declaration
}

// converter maps compiled schemas to declarations. The compiler shares one
// *compiled.Schema per location, so pointer identity identifies a schema.
type converter struct {
	module       string
	top          []*declaration
	declarations map[*compiled.Schema]*declaration
	// values holds the finished value of each declaration, so references reuse it.
	values map[*compiled.Schema]schema.Value
	// visiting detects schemas that contain themselves without an object between.
	visiting map[*compiled.Schema]bool
}

func newConverter(module string) *converter {
	return &converter{
		module:       module,
		declarations: map[*compiled.Schema]*declaration{},
		values:       map[*compiled.Schema]schema.Value{},
		visiting:     map[*compiled.Schema]bool{},
	}
}

// declareDocument names a document's root schema after its title, if any, and
// each definition after its key.
func (c *converter) declareDocument(compiler *compiled.Compiler, path string, document any) error {
	root, err := compiler.Compile(path)
	if err != nil {
		return errors.Wrapf(err, "compile %q", path)
	}
	if root.Title != "" {
		if err := c.declare(root, nil, root.Title); err != nil {
			return err
		}
	}
	keyword := "$defs"
	if root.DraftVersion < 2019 {
		keyword = "definitions"
	}
	object, _ := document.(map[string]any)
	definitions, _ := object[keyword].(map[string]any)
	for _, key := range slices.Sorted(maps.Keys(definitions)) {
		definition, err := compiler.Compile(path + "#/" + keyword + "/" + pointerToken(key))
		if err != nil {
			return errors.Wrapf(err, "compile %q", path)
		}
		if err := c.declare(definition, nil, key); err != nil {
			return err
		}
	}
	return nil
}

// convert fills in every declaration and returns the top-level ones.
func (c *converter) convert() ([]*declaration, error) {
	for _, declared := range c.top {
		if _, err := c.namedValue(declared.source); err != nil {
			return nil, err
		}
	}
	return c.top, nil
}

// pointerToken escapes a key as a JSON pointer token inside a URL fragment.
func pointerToken(key string) string {
	return url.PathEscape(strings.NewReplacer("~", "~0", "/", "~1").Replace(key))
}

// declare names source after text, inside parent's namespace or at the top level.
func (c *converter) declare(source *compiled.Schema, parent *declaration, text string) error {
	local, err := typeName(text)
	if err != nil {
		return errors.Wrap(err, source.Location)
	}
	name, siblings := c.module+"."+local, &c.top
	if parent != nil {
		name, siblings = parent.name+"."+local, &parent.nested
	}
	if existing, declared := c.declarations[source]; declared {
		return errors.Errorf("%s: schema is declared as both %q and %q", source.Location, existing.name, name)
	}
	if slices.ContainsFunc(*siblings, func(sibling *declaration) bool { return sibling.local == local }) {
		return errors.Errorf("%s: type %q is declared more than once", source.Location, name)
	}
	declared := &declaration{source: source, name: name, local: local}
	*siblings = append(*siblings, declared)
	c.declarations[source] = declared
	return nil
}

// typeName converts text such as "daily_forecast" or "Daily forecast" to a
// PascalCase identifier.
func typeName(text string) (string, error) {
	var name strings.Builder
	upper := true
	for _, char := range text {
		alphanumeric := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if !alphanumeric {
			upper = true
			continue
		}
		if upper && char >= 'a' && char <= 'z' {
			char -= 'a' - 'A'
		}
		upper = false
		name.WriteRune(char)
	}
	if name.Len() == 0 || name.String()[0] <= '9' {
		return "", errors.Errorf("cannot derive a type name from %q", text)
	}
	return name.String(), nil
}

// value maps a schema to the value of a member or element, hoisting inline
// object schemas into declarations named suggested inside scope.
func (c *converter) value(source *compiled.Schema, scope *declaration, suggested string) (schema.Value, error) {
	source, err := resolve(source)
	if err != nil {
		return schema.Value{}, err
	}
	if _, named := c.declarations[source]; named {
		return c.namedValue(source)
	}
	shape, err := shapeOf(source)
	if err != nil {
		return schema.Value{}, err
	}
	if shape.kind == schema.KindObject {
		if err := c.declare(source, scope, suggested); err != nil {
			return schema.Value{}, err
		}
		return c.namedValue(source)
	}
	if c.visiting[source] {
		return schema.Value{}, errors.Errorf("%s: schema contains itself without an object in between", source.Location)
	}
	c.visiting[source] = true
	defer delete(c.visiting, source)
	return c.inlineValue(shape, scope, suggested)
}

// namedValue fills in a declaration and returns how references to it render.
// Objects and enums are referenced by name; other aliases are inlined.
func (c *converter) namedValue(source *compiled.Schema) (schema.Value, error) {
	if value, done := c.values[source]; done {
		return value, nil
	}
	if c.visiting[source] {
		return schema.Value{}, errors.Errorf("%s: schema contains itself without an object in between", source.Location)
	}
	declared := c.declarations[source]
	if source.Ref != nil {
		if err := checkRef(source); err != nil {
			return schema.Value{}, err
		}
		return c.aliasValue(declared, func() (schema.Value, error) { return c.value(source.Ref, declared, "Target") })
	}
	shape, err := shapeOf(source)
	if err != nil {
		return schema.Value{}, err
	}
	if shape.kind != schema.KindObject {
		return c.aliasValue(declared, func() (schema.Value, error) { return c.inlineValue(shape, declared, "") })
	}
	value := schema.Value{Kind: schema.KindObject, Type: declared.name}
	// Recording the reference first lets the object's members refer to it.
	c.values[source] = value
	declared.object = true
	required := map[string]bool{}
	for _, name := range source.Required {
		required[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(source.Properties)) {
		member, err := c.value(source.Properties[name], declared, name)
		if err != nil {
			return schema.Value{}, err
		}
		declared.fields = append(declared.fields, schema.Field{Name: name, Optional: !required[name], Value: member})
	}
	return value, nil
}

func (c *converter) aliasValue(declared *declaration, body func() (schema.Value, error)) (schema.Value, error) {
	c.visiting[declared.source] = true
	defer delete(c.visiting, declared.source)
	value, err := body()
	if err != nil {
		return schema.Value{}, err
	}
	declared.alias = value
	if value.Kind == schema.KindEnum && value.Type == "" {
		value.Type = declared.name
	}
	c.values[declared.source] = value
	return value, nil
}

func (c *converter) inlineValue(shape shape, scope *declaration, suggested string) (schema.Value, error) {
	switch shape.kind {
	case schema.KindList, schema.KindMap:
		if suggested == "" && shape.kind == schema.KindList {
			suggested = "Item"
		} else if suggested == "" {
			suggested = "Value"
		}
		element, err := c.value(shape.element, scope, suggested)
		if err != nil {
			return schema.Value{}, err
		}
		return schema.Value{Kind: shape.kind, Element: &element}, nil
	case schema.KindEnum:
		return schema.Value{Kind: schema.KindEnum, Literals: shape.literals}, nil
	case schema.KindString, schema.KindNumber, schema.KindBoolean, schema.KindObject:
	}
	return schema.Value{Kind: shape.kind}, nil
}

// resolve follows references to the schema that describes the value.
func resolve(source *compiled.Schema) (*compiled.Schema, error) {
	for source.Ref != nil {
		if err := checkRef(source); err != nil {
			return nil, err
		}
		source = source.Ref
	}
	return source, nil
}

// checkRef rejects keywords beside $ref that would change the referenced shape.
func checkRef(source *compiled.Schema) error {
	if keyword := unsupportedKeyword(source); keyword != "" {
		return errors.Errorf("%s: %s is not supported", source.Location, keyword)
	}
	if source.Types != nil || source.Enum != nil || source.Const != nil || source.Properties != nil ||
		source.AdditionalProperties != nil || source.Items != nil || source.Items2020 != nil {
		return errors.Errorf("%s: $ref cannot be combined with other type keywords", source.Location)
	}
	return nil
}

// shape is the TypeScript form of a schema. KindObject is an interface, and
// element is the schema of a list's items or a map's values.
type shape struct {
	kind     schema.Kind
	literals []string
	element  *compiled.Schema
}

// shapeOf checks that a resolved schema is inside the TypeScript schema subset.
// Keywords that only narrow the accepted values, such as pattern, are ignored.
func shapeOf(source *compiled.Schema) (shape, error) {
	// The compiler also represents {} as the boolean schema true.
	if source.Bool != nil && *source.Bool {
		return shape{}, errors.Errorf("%s: schema accepts any JSON value", source.Location)
	}
	if source.Bool != nil {
		return shape{}, errors.Errorf("%s: schema accepts no value", source.Location)
	}
	if keyword := unsupportedKeyword(source); keyword != "" {
		return shape{}, errors.Errorf("%s: %s is not supported", source.Location, keyword)
	}
	kind, err := jsonType(source)
	if err != nil {
		return shape{}, errors.Wrap(err, source.Location)
	}
	switch kind {
	case "string":
		return stringShape(source)
	case "number":
		return shape{kind: schema.KindNumber}, nil
	case "boolean":
		return shape{kind: schema.KindBoolean}, nil
	case "array":
		return arrayShape(source)
	case "object":
		return objectShape(source)
	}
	return shape{}, errors.Errorf("%s: type %q is not supported", source.Location, kind)
}

func unsupportedKeyword(source *compiled.Schema) (keyword string) {
	_, tuple := source.Items.([]*compiled.Schema)
	dependentSchema := false
	for _, dependency := range source.Dependencies {
		_, isSchema := dependency.(*compiled.Schema)
		dependentSchema = dependentSchema || isSchema
	}
	for _, check := range []struct {
		keyword string
		present bool
	}{
		{"allOf", len(source.AllOf) > 0},
		{"anyOf", len(source.AnyOf) > 0},
		{"oneOf", len(source.OneOf) > 0},
		{"if", source.If != nil || source.Then != nil || source.Else != nil},
		{"patternProperties", len(source.PatternProperties) > 0},
		{"dependentSchemas", len(source.DependentSchemas) > 0 || dependentSchema},
		{"unevaluatedProperties", source.UnevaluatedProperties != nil},
		{"unevaluatedItems", source.UnevaluatedItems != nil},
		{"prefixItems", len(source.PrefixItems) > 0 || tuple},
		{"$dynamicRef", source.DynamicRef != nil || source.RecursiveRef != nil},
	} {
		if check.present {
			return check.keyword
		}
	}
	return ""
}

// jsonType returns the one JSON type a schema accepts, treating integer as number
// and inferring the type from other keywords when "type" is absent.
func jsonType(source *compiled.Schema) (kind string, err error) {
	types := map[string]bool{}
	switch {
	case source.Types != nil:
		for _, name := range source.Types.ToStrings() {
			types[name] = true
		}
	case source.Const != nil || source.Enum != nil:
		for _, literal := range constants(source) {
			types[literalType(literal)] = true
		}
	case source.Properties != nil || source.AdditionalProperties != nil:
		types["object"] = true
	case source.Items != nil || source.Items2020 != nil:
		types["array"] = true
	default:
		return "", errors.New("schema does not restrict its type, so it accepts any JSON value")
	}
	if types["null"] {
		return "", errors.New("null is not supported; leave the property out of required instead")
	}
	if types["integer"] {
		delete(types, "integer")
		types["number"] = true
	}
	if len(types) != 1 {
		return "", errors.Errorf("unions of %s are not supported", strings.Join(slices.Sorted(maps.Keys(types)), ", "))
	}
	for name := range types {
		kind = name
	}
	return kind, nil
}

func constants(source *compiled.Schema) []any {
	if source.Const != nil {
		return []any{*source.Const}
	}
	if source.Enum != nil {
		return source.Enum.Values
	}
	return nil
}

func literalType(literal any) string {
	switch literal.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "number"
	}
}

func stringShape(source *compiled.Schema) (shape, error) {
	values := constants(source)
	if values == nil {
		return shape{kind: schema.KindString}, nil
	}
	literals := make([]string, 0, len(values))
	for _, value := range values {
		literal, ok := value.(string)
		if !ok {
			return shape{}, errors.Errorf("%s: string enums may only list strings", source.Location)
		}
		literals = append(literals, literal)
	}
	return shape{kind: schema.KindEnum, literals: literals}, nil
}

func arrayShape(source *compiled.Schema) (shape, error) {
	if items, ok := source.Items.(*compiled.Schema); ok {
		return shape{kind: schema.KindList, element: items}, nil
	}
	if source.Items2020 != nil {
		return shape{kind: schema.KindList, element: source.Items2020}, nil
	}
	return shape{}, errors.Errorf("%s: arrays must declare items", source.Location)
}

// objectShape treats an object with properties as an interface and one with only
// an additionalProperties schema as a map. Undeclared keys fail payload validation.
func objectShape(source *compiled.Schema) (shape, error) {
	additional, isSchema := source.AdditionalProperties.(*compiled.Schema)
	if source.Properties == nil && source.AdditionalProperties != false {
		if !isSchema {
			return shape{}, errors.Errorf("%s: objects must declare properties or an additionalProperties schema", source.Location)
		}
		return shape{kind: schema.KindMap, element: additional}, nil
	}
	if isSchema {
		return shape{}, errors.Errorf("%s: objects cannot declare both properties and an additionalProperties schema", source.Location)
	}
	for _, name := range source.Required {
		if _, declared := source.Properties[name]; !declared {
			return shape{}, errors.Errorf("%s: required property %q is not declared", source.Location, name)
		}
	}
	return shape{kind: schema.KindObject}, nil
}

type writer struct {
	module  string
	builder strings.Builder
	depth   int
}

func newWriter(module string) *writer {
	return &writer{module: module}
}

// file renders the module declaring declarations, naming the schemas it came from.
func (w *writer) file(schemas []string, declarations []*declaration) string {
	sources := make([]string, 0, len(schemas))
	for _, path := range schemas {
		// Inputs are usually absolute, so only names keep checked-in output portable.
		sources = append(sources, filepath.Base(path))
	}
	w.builder.WriteString("// Code generated by spectre-gen json-schema. DO NOT EDIT.\n")
	w.builder.WriteString("// Source: " + strings.Join(sources, ", ") + "\n\n")
	w.linef("declare module %q {", w.module)
	w.depth++
	w.declarations(declarations)
	w.depth--
	w.linef("}")
	return w.builder.String()
}

func (w *writer) linef(format string, args ...any) {
	w.builder.WriteString(strings.Repeat("  ", w.depth))
	fmt.Fprintf(&w.builder, format, args...)
	w.builder.WriteString("\n")
}

func (w *writer) declarations(declarations []*declaration) {
	for index, declared := range declarations {
		if index > 0 {
			w.builder.WriteString("\n")
		}
		w.declaration(declared)
	}
}

func (w *writer) declaration(declared *declaration) {
	switch {
	case !declared.object:
		w.linef("export type %s = %s;", declared.local, declared.alias.Format(w.reference))
	case len(declared.fields) == 0:
		w.linef("export interface %s {}", declared.local)
	default:
		w.linef("export interface %s {", declared.local)
		w.depth++
		for _, field := range declared.fields {
			optional := ""
			if field.Optional {
				optional = "?"
			}
			w.linef("%s%s: %s;", schema.PropertyName(field.Name), optional, field.Value.Format(w.reference))
		}
		w.depth--
		w.linef("}")
	}
	if len(declared.nested) == 0 {
		return
	}
	w.builder.WriteString("\n")
	w.linef("export namespace %s {", declared.local)
	w.depth++
	w.declarations(declared.nested)
	w.depth--
	w.linef("}")
}

// reference names a type through its module, which no nested declaration can shadow.
func (w *writer) reference(name string) string {
	return fmt.Sprintf("import(%q).%s", w.module, strings.TrimPrefix(name, w.module+"."))
}
