package schema

import (
	"cmp"
	"context"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	ts "github.com/microsoft/TypeScript/tsc/shim/typescript"
)

// programRoot is where schema files live in a TypeScript program. TypeName relies
// on it to tell schema declarations from script and library declarations.
const programRoot = "/schema/"

// reservedModule is the script API, which a schema cannot redeclare.
const reservedModule = "spectre"

// ReadSources reads every .ts file in declarations, keyed by slash-separated name.
func ReadSources(declarations fs.FS) (map[string]string, error) {
	sources := map[string]string{}
	err := fs.WalkDir(declarations, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.Wrap(err, "read schema directory")
		}
		if entry.IsDir() || path.Ext(name) != ".ts" {
			return nil
		}
		source, err := fs.ReadFile(declarations, name)
		if err != nil {
			return errors.Wrapf(err, "read schema file %q", name)
		}
		sources[name] = string(source)
		return nil
	})
	return sources, errors.WithStack(err)
}

// ProgramFiles places sources where FromProgram and TypeName expect them.
func ProgramFiles(sources map[string]string) map[string]string {
	files := make(map[string]string, len(sources))
	for name, source := range sources {
		files[programRoot+name] = source
	}
	return files
}

// Parse type-checks every .ts file in declarations and reads the schema they declare.
func Parse(ctx context.Context, declarations fs.FS) (*Schema, error) {
	sources, err := ReadSources(declarations)
	if err != nil {
		return nil, err
	}
	return ParseSources(ctx, sources)
}

// ParseSources type-checks named sources on their own and reads the schema they declare.
func ParseSources(ctx context.Context, sources map[string]string) (*Schema, error) {
	program, err := ts.NewProgram(ctx, ProgramFiles(sources))
	if err != nil {
		return nil, errors.Wrap(err, "check schema declarations")
	}
	return FromProgram(program, sources)
}

// FromProgram reads the schema sources declare in a checked program built from
// ProgramFiles(sources). A type's name is its module name and namespace path.
func FromProgram(program *ts.Program, sources map[string]string) (*Schema, error) {
	modules := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		file := program.SourceFile(programRoot + name)
		if file == nil {
			return nil, errors.Errorf("schema file %q is not in the program", name)
		}
		for _, statement := range file.Statements.Nodes {
			if statement.Kind != ts.KindModuleDeclaration || statement.Name().Kind != ts.KindStringLiteral {
				return nil, errors.Errorf("%s: schema files may only contain declare module \"name\" { ... } blocks", ts.Location(statement))
			}
			if statement.Name().Text() == reservedModule {
				return nil, errors.Errorf("%s: module %q is reserved for the script API", ts.Location(statement), reservedModule)
			}
			modules[statement.Name().Text()] = true
		}
	}
	return newReader(program.Checker()).read(modules)
}

// TypeName returns the schema name of a named object type, which is how scripts and
// the host refer to it. Types declared outside a schema module have no name.
func TypeName(t *ts.Type) (string, error) {
	symbol := t.Symbol()
	if alias := t.Alias(); alias != nil && (symbol == nil || symbol.Flags&ts.SymbolFlagsTypeLiteral != 0) {
		if len(alias.TypeArguments()) > 0 {
			return "", errors.Errorf("generic type %q is not supported", alias.Symbol().Name)
		}
		symbol = alias.Symbol()
	}
	if symbol == nil || symbol.Flags&(ts.SymbolFlagsInterface|ts.SymbolFlagsTypeAlias) == 0 {
		return "", errors.New("object types must be declared by name in a schema module")
	}
	return qualifiedName(symbol)
}

// qualifiedName walks a symbol's parents up to its ambient module, which must be
// declared in a schema file rather than augmented by a script.
func qualifiedName(symbol *ts.Symbol) (string, error) {
	names := []string{}
	for current := symbol; current != nil; current = current.Parent {
		if len(current.Declarations) > 0 {
			declaration := current.Declarations[0]
			if declaration.Kind == ts.KindModuleDeclaration && declaration.Name().Kind == ts.KindStringLiteral {
				if !inSchemaFile(declaration) {
					break
				}
				slices.Reverse(names)
				return declaration.Name().Text() + "." + strings.Join(names, "."), nil
			}
		}
		names = append(names, current.Name)
	}
	return "", errors.Errorf("type %q is not exported from a schema module", symbol.Name)
}

func inSchemaFile(node *ts.Node) bool {
	return strings.HasPrefix(ts.SourceFileOf(node).FileName(), programRoot)
}

// checkDeclaredInSchema rejects script augmentations of schema modules, which
// would change the payload types the host validates.
func checkDeclaredInSchema(symbol *ts.Symbol) error {
	for _, declaration := range symbol.Declarations {
		if !inSchemaFile(declaration) {
			return errors.Errorf("%s: %s augments a schema module; only schema files may declare its members", ts.Location(declaration), symbol.Name)
		}
	}
	return nil
}

type pendingType struct {
	name string
	t    *ts.Type
}

// reader converts checked declarations to the schema model, queueing each named
// object type once so recursive references terminate.
type reader struct {
	checker    *ts.Checker
	pending    []pendingType
	queued     map[string]bool
	operations []Operation
}

func newReader(checker *ts.Checker) *reader {
	return &reader{checker: checker, queued: map[string]bool{}}
}

func (r *reader) read(modules map[string]bool) (*Schema, error) {
	for _, module := range slices.Sorted(maps.Keys(modules)) {
		if err := r.members(r.checker.TryFindAmbientModule(module)); err != nil {
			return nil, err
		}
	}
	types := []*Type{}
	for len(r.pending) > 0 {
		next := r.pending[0]
		r.pending = r.pending[1:]
		declared, err := r.objectType(next.name, next.t)
		if err != nil {
			return nil, err
		}
		types = append(types, declared)
	}
	return New(types, r.operations)
}

// members reads every export of a module or namespace, so unreferenced types are
// still validated and available to scripts.
func (r *reader) members(container *ts.Symbol) error {
	exports := r.checker.GetExportsOfModule(container)
	slices.SortFunc(exports, func(left, right *ts.Symbol) int { return cmp.Compare(left.Name, right.Name) })
	for _, symbol := range exports {
		if err := checkDeclaredInSchema(symbol); err != nil {
			return err
		}
		declaration := symbol.Declarations[0]
		if symbol.Flags&(ts.SymbolFlagsValue&^ts.SymbolFlagsModule|ts.SymbolFlagsAlias) != 0 {
			return errors.Errorf("%s: schema modules may only declare interfaces, type aliases, and namespaces", ts.Location(declaration))
		}
		if len(r.checker.GetLocalTypeParametersOfClassOrInterfaceOrTypeAlias(symbol)) > 0 {
			return errors.Errorf("%s: generic declarations are not supported", ts.Location(declaration))
		}
		if symbol.Flags&ts.SymbolFlagsInterface != 0 {
			if err := r.interfaceDeclaration(symbol); err != nil {
				return err
			}
		}
		if symbol.Flags&ts.SymbolFlagsTypeAlias != 0 {
			// Aliases of non-object types are inlined where they are used, but must be valid anyway.
			if _, err := r.value(r.checker.GetDeclaredTypeOfSymbol(symbol), declaration); err != nil {
				return err
			}
		}
		if symbol.Flags&ts.SymbolFlagsModule != 0 {
			if err := r.members(symbol); err != nil {
				return err
			}
		}
	}
	return nil
}

// interfaceDeclaration treats an interface of methods as a service, whose methods
// are the RPC paths that name their request and response types.
func (r *reader) interfaceDeclaration(symbol *ts.Symbol) error {
	declared := r.checker.GetDeclaredTypeOfSymbol(symbol)
	properties := r.checker.GetPropertiesOfType(declared)
	if len(properties) == 0 || !slices.ContainsFunc(properties, isMethod) {
		_, err := r.value(declared, symbol.Declarations[0])
		return err
	}
	service, err := qualifiedName(symbol)
	if err != nil {
		return errors.Errorf("%s: %v", ts.Location(symbol.Declarations[0]), err)
	}
	for _, method := range properties {
		if err := checkDeclaredInSchema(method); err != nil {
			return err
		}
		operation, err := r.operation(service, method)
		if err != nil {
			return err
		}
		r.operations = append(r.operations, operation)
	}
	return nil
}

func isMethod(symbol *ts.Symbol) bool {
	return symbol.Flags&ts.SymbolFlagsMethod != 0
}

func (r *reader) operation(service string, method *ts.Symbol) (Operation, error) {
	declaration := method.Declarations[0]
	form := errors.Errorf("%s: service methods must have the form %s(request: Request): Response", ts.Location(declaration), method.Name)
	if !isMethod(method) {
		return Operation{}, errors.Errorf("%s: service interfaces may only declare methods", ts.Location(declaration))
	}
	signatures := r.checker.GetSignaturesOfType(r.checker.GetTypeOfSymbol(method), ts.SignatureKindCall)
	if method.Flags&ts.SymbolFlagsOptional != 0 || len(signatures) != 1 {
		return Operation{}, form
	}
	signature := signatures[0]
	if len(signature.TypeParameters()) > 0 || signature.ThisParameter() != nil || signature.HasRestParameter() ||
		len(signature.Parameters()) != 1 || signature.MinArgumentCount() != 1 {
		return Operation{}, form
	}
	request, err := r.objectReference(r.checker.GetTypeOfSymbol(signature.Parameters()[0]), declaration)
	if err != nil {
		return Operation{}, err
	}
	response, err := r.objectReference(r.checker.GetReturnTypeOfSignature(signature), declaration)
	if err != nil {
		return Operation{}, err
	}
	return Operation{Name: service + "." + method.Name, Request: request, Response: response}, nil
}

func (r *reader) objectReference(t *ts.Type, at *ts.Node) (string, error) {
	value, err := r.value(t, at)
	if err != nil {
		return "", err
	}
	if value.Kind != KindObject {
		return "", errors.Errorf("%s: expected an object type, found %s", ts.Location(at), value)
	}
	return value.Type, nil
}

// value maps a checked type to the JSON shape it accepts. at locates errors.
func (r *reader) value(t *ts.Type, at *ts.Node) (Value, error) {
	flags := t.Flags()
	switch {
	case flags&ts.TypeFlagsEnumLiteral != 0:
	case flags&ts.TypeFlagsBoolean != 0:
		return Value{Kind: KindBoolean}, nil
	case flags&ts.TypeFlagsString != 0:
		return Value{Kind: KindString}, nil
	case flags&ts.TypeFlagsNumber != 0:
		return Value{Kind: KindNumber}, nil
	case flags&(ts.TypeFlagsStringLiteral|ts.TypeFlagsUnion) != 0:
		return r.union(t, at)
	case flags&ts.TypeFlagsObject != 0:
		return r.object(t, at)
	}
	return Value{}, r.unsupported(t, at)
}

func (r *reader) unsupported(t *ts.Type, at *ts.Node) error {
	return errors.Errorf("%s: type %s is not supported in schemas", ts.Location(at), r.checker.TypeToString(t))
}

// union reads string literals as an enum, or as a number's literals when the
// union also contains number.
func (r *reader) union(t *ts.Type, at *ts.Node) (Value, error) {
	members := []*ts.Type{t}
	if t.Flags()&ts.TypeFlagsUnion != 0 {
		members = t.Types()
	}
	literals := make([]string, 0, len(members))
	number := false
	for _, member := range members {
		if member.Flags()&ts.TypeFlagsNumber != 0 {
			number = true
			continue
		}
		literal, ok := ts.StringLiteral(member)
		if !ok || member.Flags()&ts.TypeFlagsEnumLiteral != 0 {
			return Value{}, r.unsupported(t, at)
		}
		literals = append(literals, literal)
	}
	// The checker orders union members itself, so declaration order is lost.
	slices.Sort(literals)
	if number {
		return Value{Kind: KindNumber, Literals: literals}, nil
	}
	value := Value{Kind: KindEnum, Literals: literals}
	if alias := t.Alias(); alias != nil {
		// The alias name only renders the enum, so one declared outside a module is dropped.
		if name, err := qualifiedName(alias.Symbol()); err == nil {
			value.Type = name
		}
	}
	return value, nil
}

func (r *reader) object(t *ts.Type, at *ts.Node) (Value, error) {
	if ts.IsTupleType(t) || r.checker.TypeHasCallOrConstructSignatures(t) {
		return Value{}, r.unsupported(t, at)
	}
	if r.checker.IsArrayType(t) {
		element, err := r.value(r.checker.GetTypeArguments(t)[0], at)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindList, Element: &element}, nil
	}
	if indexes := r.checker.GetIndexInfosOfType(t); len(indexes) > 0 {
		if len(indexes) != 1 || indexes[0].KeyType().Flags()&ts.TypeFlagsString == 0 || len(r.checker.GetPropertiesOfType(t)) > 0 {
			return Value{}, errors.Errorf("%s: maps must have the form { [key: string]: T } or Record<string, T>", ts.Location(at))
		}
		element, err := r.value(indexes[0].ValueType(), at)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindMap, Element: &element}, nil
	}
	name, err := TypeName(t)
	if err != nil {
		return Value{}, errors.Errorf("%s: %s: %v", ts.Location(at), r.checker.TypeToString(t), err)
	}
	if !r.queued[name] {
		r.queued[name] = true
		r.pending = append(r.pending, pendingType{name: name, t: t})
	}
	return Value{Kind: KindObject, Type: name}, nil
}

func (r *reader) objectType(name string, t *ts.Type) (*Type, error) {
	symbol := t.Symbol()
	if alias := t.Alias(); alias != nil {
		symbol = alias.Symbol()
	}
	declaration := symbol.Declarations[0]
	if symbol.Flags&ts.SymbolFlagsClass != 0 {
		return nil, errors.Errorf("%s: classes are not supported in schemas", ts.Location(declaration))
	}
	if len(r.checker.GetLocalTypeParametersOfClassOrInterfaceOrTypeAlias(symbol)) > 0 {
		return nil, errors.Errorf("%s: generic declarations are not supported", ts.Location(declaration))
	}
	properties := r.checker.GetPropertiesOfType(t)
	declared := &Type{Name: name, Fields: make([]Field, 0, len(properties))}
	for _, property := range properties {
		if err := checkDeclaredInSchema(property); err != nil {
			return nil, err
		}
		at := property.Declarations[0]
		if isMethod(property) {
			return nil, errors.Errorf("%s: object types may only declare properties; %s mixes them with methods", ts.Location(at), name)
		}
		propertyType := r.checker.GetTypeOfSymbol(property)
		optional := property.Flags&ts.SymbolFlagsOptional != 0
		if optional {
			propertyType = r.checker.RemoveMissingOrUndefinedType(propertyType)
		}
		value, err := r.value(propertyType, at)
		if err != nil {
			return nil, err
		}
		declared.Fields = append(declared.Fields, Field{Name: property.Name, Optional: optional, Value: value})
	}
	return declared, nil
}
