package descriptors

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/schema"
)

// scalarMessageValue models well-known messages whose ProtoJSON is a scalar.
func scalarMessageValue(name protoreflect.FullName) (value schema.Value, scalar bool) {
	switch name {
	case "google.protobuf.Timestamp", "google.protobuf.Duration", "google.protobuf.FieldMask",
		"google.protobuf.StringValue", "google.protobuf.BytesValue", "google.protobuf.Int64Value", "google.protobuf.UInt64Value":
		return schema.Value{Kind: schema.KindString}, true
	case "google.protobuf.DoubleValue", "google.protobuf.FloatValue":
		return floatValue(), true
	case "google.protobuf.Int32Value", "google.protobuf.UInt32Value":
		return schema.Value{Kind: schema.KindNumber}, true
	case "google.protobuf.BoolValue":
		return schema.Value{Kind: schema.KindBoolean}, true
	default:
		return schema.Value{}, false
	}
}

func isScalarMessage(name protoreflect.FullName) (scalar bool) {
	_, scalar = scalarMessageValue(name)
	return scalar
}

// floatValue models a float or double, which ProtoJSON writes as a string when
// it is not finite.
func floatValue() schema.Value {
	return schema.Value{Kind: schema.KindNumber, Literals: []string{"-Infinity", "Infinity", "NaN"}}
}

// isUntypedMessage identifies ProtoJSON shapes outside the schema subset.
func isUntypedMessage(name protoreflect.FullName) (unsupported bool) {
	switch name {
	case "google.protobuf.Any", "google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.ListValue", "google.protobuf.NullValue":
		return true
	default:
		return false
	}
}

// schemaFiles returns the paths of files a payload can reach through field,
// method, or enum references, leaving out files imported only for options.
func schemaFiles(registry *Registry) map[string]bool {
	imported := map[string]bool{}
	hasContent := map[string]bool{}
	hasService := map[string]bool{}
	edges := map[string][]string{}
	registry.Files().RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Messages().Len() > 0 || file.Enums().Len() > 0 || file.Services().Len() > 0 {
			hasContent[file.Path()] = true
		}
		hasService[file.Path()] = file.Services().Len() > 0
		for index := range file.Imports().Len() {
			imported[file.Imports().Get(index).Path()] = true
		}
		collectSchemaEdges(file, edges)
		return true
	})

	reached := map[string]bool{}
	var queue []string
	visit := func(path string) {
		if hasContent[path] && !reached[path] {
			reached[path] = true
			queue = append(queue, path)
		}
	}
	// Roots are the request and response surfaces: top-level files and services.
	for path := range hasContent {
		if !imported[path] || hasService[path] {
			visit(path)
		}
	}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		for _, target := range edges[path] {
			visit(target)
		}
	}
	return reached
}

func collectSchemaEdges(file protoreflect.FileDescriptor, edges map[string][]string) {
	collectMessageEdges(file.Path(), file.Messages(), edges)
	services := file.Services()
	for index := range services.Len() {
		methods := services.Get(index).Methods()
		for index := range methods.Len() {
			method := methods.Get(index)
			addSchemaEdge(edges, file.Path(), method.Input().ParentFile().Path())
			addSchemaEdge(edges, file.Path(), method.Output().ParentFile().Path())
		}
	}
}

func collectMessageEdges(from string, messages protoreflect.MessageDescriptors, edges map[string][]string) {
	for index := range messages.Len() {
		message := messages.Get(index)
		for index := range message.Fields().Len() {
			field := message.Fields().Get(index)
			value := field
			if field.IsMap() {
				value = field.MapValue()
			}
			if value.Message() != nil {
				addSchemaEdge(edges, from, value.Message().ParentFile().Path())
			}
			if value.Enum() != nil {
				addSchemaEdge(edges, from, value.Enum().ParentFile().Path())
			}
		}
		collectMessageEdges(from, message.Messages(), edges)
	}
}

func addSchemaEdge(edges map[string][]string, from, to string) {
	if from != to {
		edges[from] = append(edges[from], to)
	}
}

// Declarations renders one TypeScript declaration file per protobuf file, keyed
// by the proto file name with a .d.ts extension. Empty declarations are omitted.
func Declarations(ctx context.Context, set *descriptorpb.FileDescriptorSet) (map[string]string, error) {
	if set == nil {
		return nil, errors.New("descriptor set is nil")
	}
	registry, err := NewRegistry(set)
	if err != nil {
		return nil, errors.Wrap(err, "resolve descriptor set")
	}
	declarations := map[string]string{}
	reachable := schemaFiles(registry)
	var renderErr error
	registry.Files().RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if !reachable[file.Path()] {
			return true
		}
		if renderErr = checkDeclarationPath(file.Path()); renderErr != nil {
			return false
		}
		var rendered string
		rendered, renderErr = renderFile(registry, file)
		if renderErr != nil {
			renderErr = errors.Wrapf(renderErr, "render %q", file.Path())
			return false
		}
		if rendered != "" {
			name := strings.TrimSuffix(file.Path(), ".proto") + ".d.ts"
			if _, duplicate := declarations[name]; duplicate {
				renderErr = errors.Errorf("multiple proto files write declaration %q", name)
				return false
			}
			declarations[name] = rendered
		}
		return true
	})
	if renderErr != nil {
		return nil, renderErr
	}
	// Valid protobuf names can be TypeScript keywords, so parse the output to catch them.
	loaded, err := schema.ParseSources(ctx, declarations)
	if err != nil {
		return nil, errors.Wrap(err, "parse generated declarations")
	}
	if err := CheckAgreement(registry, loaded); err != nil {
		return nil, errors.Wrap(err, "check generated declarations")
	}
	return declarations, nil
}

func checkDeclarationPath(name string) error {
	if !fs.ValidPath(name) || !filepath.IsLocal(filepath.FromSlash(name)) || strings.ContainsAny(name, "\\:\r\n") {
		return errors.Errorf("proto file path %q is not a safe relative path", name)
	}
	return nil
}

// WriteDeclarations renders the declarations for set into dir, replacing any files
// of the same name. Other files are left alone; symlinks cannot escape dir.
func WriteDeclarations(ctx context.Context, set *descriptorpb.FileDescriptorSet, dir string) error {
	declarations, err := Declarations(ctx, set)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return errors.Wrap(err, "create declarations directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.Wrap(err, "open declarations directory")
	}
	defer root.Close() //nolint:errcheck // Closing the directory cannot affect completed writes.
	for name, contents := range declarations {
		target := filepath.FromSlash(name)
		if err := root.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return errors.Wrapf(err, "create directory for %q", name)
		}
		if err := root.WriteFile(target, []byte(contents), 0o600); err != nil {
			return errors.Wrapf(err, "write %q", name)
		}
	}
	return nil
}

// declarationWriter indents nested namespaces and remembers whether anything
// was declared, so files without declarations can be dropped.
type declarationWriter struct {
	registry *Registry
	builder  strings.Builder
	depth    int
	declared bool
}

// separate starts each declaration after the first with a blank line.
func (w *declarationWriter) separate() {
	if w.declared {
		w.builder.WriteString("\n")
	}
}

func (w *declarationWriter) linef(format string, args ...any) {
	w.builder.WriteString(strings.Repeat("  ", w.depth))
	fmt.Fprintf(&w.builder, format, args...)
	w.builder.WriteString("\n")
}

func newDeclarationWriter(registry *Registry, depth int) *declarationWriter {
	return &declarationWriter{registry: registry, depth: depth}
}

func (w *declarationWriter) contents() string {
	return w.builder.String()
}

func (w *declarationWriter) hasDeclarations() bool {
	return w.declared
}

func renderFile(registry *Registry, file protoreflect.FileDescriptor) (declaration string, err error) {
	return newDeclarationWriter(registry, 0).renderFile(file)
}

func (w *declarationWriter) renderFile(file protoreflect.FileDescriptor) (declaration string, err error) {
	if file.Package() == "" {
		return "", errors.New("proto files must declare a package, which names their schema module")
	}
	w.linef("declare module %q {", file.Package())
	w.depth++
	if err := w.renderScope(file.Messages(), file.Enums()); err != nil {
		return "", err
	}
	services := file.Services()
	for index := range services.Len() {
		if err := w.renderService(services.Get(index)); err != nil {
			return "", err
		}
	}
	if !w.declared {
		return "", nil
	}
	w.depth--
	w.linef("}")
	header := "// Code generated by spectre-gen proto. DO NOT EDIT.\n// Source: " + file.Path() + "\n\n"
	return header + w.builder.String(), nil
}

func (w *declarationWriter) renderScope(messages protoreflect.MessageDescriptors, enums protoreflect.EnumDescriptors) error {
	for index := range enums.Len() {
		enum := enums.Get(index)
		if isUntypedMessage(enum.FullName()) {
			continue
		}
		w.separate()
		w.linef("export type %s = %s;", enum.Name(), enumLiterals(enum))
		w.declared = true
	}
	for index := range messages.Len() {
		message := messages.Get(index)
		if message.IsMapEntry() || isUntypedMessage(message.FullName()) || isScalarMessage(message.FullName()) {
			continue
		}
		if err := w.renderMessage(message); err != nil {
			return err
		}
	}
	return nil
}

func (w *declarationWriter) renderMessage(message protoreflect.MessageDescriptor) error {
	fields := messageFields(w.registry, message)
	declared, err := messageType(message.FullName(), fields)
	if err != nil {
		return err
	}
	w.separate()
	w.linef("export interface %s {", message.Name())
	w.depth++
	for index, field := range declared.Fields {
		optional := ""
		if field.Optional {
			optional = "?"
		}
		referenced := referencedType(fields[index])
		value := field.Value.Format(func(_ string) string { return typeReference(referenced) })
		w.linef("%s%s: %s;", schema.PropertyName(field.Name), optional, value)
	}
	w.depth--
	w.linef("}")
	w.declared = true
	nested := message.Messages()
	if nested.Len() == 0 && message.Enums().Len() == 0 {
		return nil
	}
	// Merging a namespace with the interface keeps nested names fully qualified.
	inner := newDeclarationWriter(w.registry, w.depth+1)
	if err := inner.renderScope(nested, message.Enums()); err != nil {
		return err
	}
	if inner.hasDeclarations() {
		w.separate()
		w.linef("export namespace %s {", message.Name())
		w.builder.WriteString(inner.contents())
		w.linef("}")
	}
	return nil
}

func (w *declarationWriter) renderService(service protoreflect.ServiceDescriptor) error {
	methods := service.Methods()
	signatures := []string{}
	for index := range methods.Len() {
		method := methods.Get(index)
		// Streaming methods have no single request and response to compare.
		if method.IsStreamingClient() || method.IsStreamingServer() {
			continue
		}
		if err := checkMethodTypes(method); err != nil {
			return err
		}
		signatures = append(signatures, fmt.Sprintf("%s(request: %s): %s;", schema.PropertyName(string(method.Name())), typeReference(method.Input()), typeReference(method.Output())))
	}
	if len(signatures) == 0 {
		return nil
	}
	w.separate()
	w.linef("export interface %s {", service.Name())
	w.depth++
	for _, signature := range signatures {
		w.linef("%s", signature)
	}
	w.depth--
	w.linef("}")
	w.declared = true
	return nil
}

func checkMethodTypes(method protoreflect.MethodDescriptor) error {
	for _, message := range []protoreflect.MessageDescriptor{method.Input(), method.Output()} {
		if isUntypedMessage(message.FullName()) || isScalarMessage(message.FullName()) {
			return errors.Errorf("method %q uses unsupported request or response type %q", method.FullName(), message.FullName())
		}
	}
	return nil
}

// messageFields lists a message's fields followed by the extensions the registry
// declares for it, which ProtoJSON writes as "[full.name]" keys.
func messageFields(registry *Registry, message protoreflect.MessageDescriptor) []protoreflect.FieldDescriptor {
	fields := message.Fields()
	all := make([]protoreflect.FieldDescriptor, 0, fields.Len())
	for index := range fields.Len() {
		all = append(all, fields.Get(index))
	}
	return append(all, registry.Extensions(message.FullName())...)
}

// messageType models the ProtoJSON object a message encodes to when default values
// are emitted: only fields with presence and extensions, written when set, may be absent.
func messageType(name protoreflect.FullName, fields []protoreflect.FieldDescriptor) (*schema.Type, error) {
	declared := &schema.Type{Name: string(name), Fields: make([]schema.Field, 0, len(fields))}
	for _, field := range fields {
		value, err := fieldValue(field)
		if err != nil {
			return nil, errors.Wrapf(err, "field %q", field.FullName())
		}
		optional := field.HasPresence() || field.IsExtension()
		declared.Fields = append(declared.Fields, schema.Field{Name: field.JSONName(), Optional: optional, Value: value})
	}
	return declared, nil
}

func fieldValue(field protoreflect.FieldDescriptor) (schema.Value, error) {
	if field.IsMap() {
		element, err := singularValue(field.MapValue())
		return schema.Value{Kind: schema.KindMap, Element: Some(&element)}, err
	}
	element, err := singularValue(field)
	if err != nil || !field.IsList() {
		return element, err
	}
	return schema.Value{Kind: schema.KindList, Element: Some(&element)}, nil
}

func singularValue(field protoreflect.FieldDescriptor) (schema.Value, error) {
	switch field.Kind() {
	case protoreflect.StringKind, protoreflect.BytesKind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// ProtoJSON encodes bytes as base64 and 64-bit integers as strings.
		return schema.Value{Kind: schema.KindString}, nil
	case protoreflect.BoolKind:
		return schema.Value{Kind: schema.KindBoolean}, nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return schema.Value{Kind: schema.KindNumber}, nil
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return floatValue(), nil
	case protoreflect.EnumKind:
		if isUntypedMessage(field.Enum().FullName()) {
			return schema.Value{}, errors.Errorf("%s is not supported", field.Enum().FullName())
		}
		return enumValue(field.Enum()), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		name := field.Message().FullName()
		if value, scalar := scalarMessageValue(name); scalar {
			return value, nil
		}
		if isUntypedMessage(name) {
			return schema.Value{}, errors.Errorf("%s is not supported", name)
		}
		return schema.Value{Kind: schema.KindObject, Type: string(name)}, nil
	}
	return schema.Value{}, errors.Errorf("field kind %s is not supported", field.Kind())
}

// referencedType returns the message or enum a field's value names, if any.
func referencedType(field protoreflect.FieldDescriptor) protoreflect.Descriptor {
	if field.IsMap() {
		field = field.MapValue()
	}
	if field.Enum() != nil {
		return field.Enum()
	}
	return field.Message()
}

// typeReference names a type through its package's module, which no nested
// declaration can shadow.
func typeReference(descriptor protoreflect.Descriptor) (reference string) {
	pkg := descriptor.ParentFile().Package()
	path := strings.TrimPrefix(string(descriptor.FullName()), string(pkg)+".")
	return fmt.Sprintf("import(%q).%s", pkg, path)
}

func enumValue(enum protoreflect.EnumDescriptor) schema.Value {
	values := enum.Values()
	literals := make([]string, 0, values.Len())
	for index := range values.Len() {
		literals = append(literals, string(values.Get(index).Name()))
	}
	value := schema.Value{Kind: schema.KindEnum, Literals: literals, Type: string(enum.FullName())}
	// Open enums keep unknown numbers, which ProtoJSON writes as JSON numbers.
	if !enum.IsClosed() {
		value.Kind = schema.KindNumber
	}
	return value
}

func enumLiterals(enum protoreflect.EnumDescriptor) (literals string) {
	value := enumValue(enum)
	value.Type = ""
	return value.String()
}
