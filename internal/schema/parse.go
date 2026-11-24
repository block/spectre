package schema

import (
	"cmp"
	"context"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	ts "github.com/microsoft/TypeScript/tsc/shim/typescript"
)

// programRoot is where schema files live in a TypeScript program. TypeName relies
// on it to tell schema declarations from script and library declarations.
const programRoot = "/schema/"

// ScriptRoot is where comparison scripts live in a TypeScript program. A type declared
// at the top level of a script is named by its own name.
const ScriptRoot = "/scripts/"

// reservedModule is the script API, which a schema cannot redeclare. Payload modules are
// under reservedPrefix, but schemas may declare others there, like "spectre.sample.v1".
const (
	reservedModule = "spectre"
	reservedPrefix = reservedModule + "."
)

// payloadRoot is where binding payload declarations live in a program. It is outside
// programRoot, so no schema file can take their names.
const payloadRoot = "/spectre/payloads/"

// ReadSources reads every .ts file in declarations, keyed by slash-separated name.
// Symlinks are rejected because fs.FS has no portable root-confined open operation.
func ReadSources(declarations fs.FS) (map[string]string, error) {
	sources := map[string]string{}
	err := fs.WalkDir(declarations, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.Wrap(err, "read schema directory")
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.Errorf("schema path %q is a symbolic link", name)
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

func payloadFile(index int) string {
	return payloadRoot + strconv.Itoa(index) + ".d.ts"
}

// ProgramFiles places sources and the payload declarations where FromProgram
// and TypeName expect them.
func ProgramFiles(sources map[string]string, payloads []string) map[string]string {
	files := make(map[string]string, len(sources)+len(payloads))
	for name, source := range sources {
		files[programRoot+name] = source
	}
	for index, payload := range payloads {
		files[payloadFile(index)] = payload
	}
	return files
}

// Parse type-checks every .ts file in declarations and reads the schema they declare.
// The schema also includes the types the payload declarations declare.
func Parse(ctx context.Context, declarations fs.FS, payloads ...string) (*Schema, error) {
	sources, err := ReadSources(declarations)
	if err != nil {
		return nil, err
	}
	return ParseSources(ctx, sources, payloads...)
}

// ParseSources type-checks named sources and payload declarations on their own and
// reads the schema they declare.
func ParseSources(ctx context.Context, sources map[string]string, payloads ...string) (*Schema, error) {
	program, err := ts.NewProgram(ctx, ProgramFiles(sources, payloads))
	if err != nil {
		return nil, errors.Wrap(err, "check schema declarations")
	}
	return FromProgram(program, sources, payloads, nil)
}

// FromProgram reads the types that sources, payloads and references declare in a program
// built from ProgramFiles. Payload modules are under "spectre." and sources cannot declare them.
func FromProgram(program *ts.Program, sources map[string]string, payloads []string, references []*ts.Node) (*Schema, error) {
	payloadModules := map[string]bool{}
	for index := range payloads {
		file := program.SourceFile(payloadFile(index))
		if file == nil {
			return nil, errors.Errorf("payload declarations %d are not in the program", index)
		}
		declared, err := declaredModules(file)
		if err != nil {
			return nil, err
		}
		for _, module := range slices.Sorted(maps.Keys(declared)) {
			if !strings.HasPrefix(module, reservedPrefix) {
				return nil, errors.Errorf("payload module %q must be named under %q", module, reservedPrefix)
			}
			if payloadModules[module] {
				return nil, errors.Errorf("payload module %q is declared more than once", module)
			}
			payloadModules[module] = true
		}
	}
	modules := maps.Clone(payloadModules)
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		file := program.SourceFile(programRoot + name)
		if file == nil {
			return nil, errors.Errorf("schema file %q is not in the program", name)
		}
		declared, err := declaredModules(file)
		if err != nil {
			return nil, err
		}
		for _, statement := range file.Statements.Nodes {
			if module := statement.Name().Text(); module == reservedModule || payloadModules[module] {
				return nil, errors.Errorf("%s: module %q is reserved by SPECTRE", ts.Location(statement), module)
			}
		}
		maps.Copy(modules, declared)
	}
	return newReader(program.Checker()).read(modules, references)
}

// declaredModules returns the names of the modules a schema file declares.
func declaredModules(file *ts.SourceFile) (map[string]bool, error) {
	modules := map[string]bool{}
	for _, statement := range file.Statements.Nodes {
		if statement.Kind != ts.KindModuleDeclaration || statement.Name().Kind != ts.KindStringLiteral {
			return nil, errors.Errorf("%s: schema files may only contain declare module \"name\" { ... } blocks", ts.Location(statement))
		}
		modules[statement.Name().Text()] = true
	}
	return modules, nil
}

// TypeName returns the schema name of a named object type, which is how scripts and
// the host refer to it. Types declared outside a schema module or script have no name.
func TypeName(t *ts.Type) (TypeRef, error) {
	if alias := t.Alias(); alias != nil && len(alias.TypeArguments()) > 0 {
		symbol := t.Symbol()
		if symbol == nil || symbol.Flags()&ts.SymbolFlagsTypeLiteral != 0 {
			return "", errors.Errorf("generic type %q is not supported", alias.Symbol().Name())
		}
	}
	symbol := namedSymbol(t)
	if symbol == nil || symbol.Flags()&(ts.SymbolFlagsInterface|ts.SymbolFlagsTypeAlias) == 0 {
		return "", errors.New("object types must be declared by name in a schema module or at the top level of a script")
	}
	return qualifiedName(symbol)
}

// qualifiedName walks a symbol's parents up to its ambient module, which must be
// declared in a schema file rather than augmented by a script.
func qualifiedName(symbol *ts.Symbol) (TypeRef, error) {
	if name, ok := scriptName(symbol); ok {
		return name, nil
	}
	names := []string{}
	for current := symbol; current != nil; current = current.Parent() {
		if len(current.Declarations()) > 0 {
			declaration := current.Declarations()[0]
			if declaration.Kind == ts.KindModuleDeclaration && declaration.Name().Kind == ts.KindStringLiteral {
				module := declaration.Name().Text()
				if !slices.ContainsFunc(current.Declarations(), inSchemaFile) {
					return "", errors.Errorf("%s: %s is declared in module %q, but only schema files may declare modules",
						ts.Location(symbol.Declarations()[0]), symbol.Name(), module)
				}
				if err := checkDeclaredInSchema(symbol); err != nil {
					return "", err
				}
				slices.Reverse(names)
				return TypeRef(module + "." + strings.Join(names, ".")), nil
			}
		}
		names = append(names, current.Name())
	}
	return "", errors.Errorf("type %q is not exported from a schema module or declared at the top level of a script", symbol.Name())
}

// scriptName names a type declared at the top level of a script by its own name.
func scriptName(symbol *ts.Symbol) (name TypeRef, ok bool) {
	if len(symbol.Declarations()) == 0 {
		return "", false
	}
	declaration := symbol.Declarations()[0]
	file := ts.SourceFileOf(declaration).FileName().AsString()
	if declaration.Parent.Kind != ts.KindSourceFile || !strings.HasPrefix(file, ScriptRoot) {
		return "", false
	}
	return TypeRef(symbol.Name()), true
}

func inSchemaFile(node *ts.Node) bool {
	name := ts.SourceFileOf(node).FileName().AsString()
	return strings.HasPrefix(name, programRoot) || strings.HasPrefix(name, payloadRoot)
}

// checkDeclaredInSchema rejects script augmentations of schema modules, which
// would change the payload types the host validates.
func checkDeclaredInSchema(symbol *ts.Symbol) error {
	for _, declaration := range symbol.Declarations() {
		if !inSchemaFile(declaration) {
			return errors.Errorf("%s: %s augments a schema module; only schema files may declare its members", ts.Location(declaration), symbol.Name())
		}
	}
	return nil
}

type pendingType struct {
	declared *ObjectType
	t        *ts.Type
}

// reader converts checked declarations to the schema model. It creates each named
// object type once and fills in its fields later, so recursive references terminate.
type reader struct {
	checker *ts.Checker
	pending []pendingType
	objects map[TypeRef]*ObjectType
	symbols map[TypeRef]*ts.Symbol
	methods []Method
}

func newReader(checker *ts.Checker) *reader {
	return &reader{checker: checker, objects: map[TypeRef]*ObjectType{}, symbols: map[TypeRef]*ts.Symbol{}}
}

func (r *reader) read(modules map[string]bool, references []*ts.Node) (*Schema, error) {
	for _, module := range slices.Sorted(maps.Keys(modules)) {
		if err := r.members(r.checker.TryFindAmbientModule(module)); err != nil {
			return nil, err
		}
	}
	for _, reference := range references {
		if _, err := r.objectReference(r.checker.GetTypeFromTypeNode(reference), reference); err != nil {
			return nil, err
		}
	}
	types := []*ObjectType{}
	for len(r.pending) > 0 {
		next := r.pending[0]
		r.pending = r.pending[1:]
		if err := r.objectType(next.declared, next.t); err != nil {
			return nil, err
		}
		types = append(types, next.declared)
	}
	return New(types, r.methods)
}

// members reads every export of a module or namespace, so unreferenced types are
// still validated and available to scripts.
func (r *reader) members(container *ts.Symbol) error {
	exports := r.checker.GetExportsOfModule(container)
	slices.SortFunc(exports, func(left, right *ts.Symbol) int { return cmp.Compare(left.Name(), right.Name()) })
	for _, symbol := range exports {
		if err := checkDeclaredInSchema(symbol); err != nil {
			return err
		}
		declaration := symbol.Declarations()[0]
		if symbol.Flags()&(ts.SymbolFlagsValue&^ts.SymbolFlagsModule|ts.SymbolFlagsAlias) != 0 {
			return errors.Errorf("%s: schema modules may only declare interfaces, type aliases, and namespaces", ts.Location(declaration))
		}
		if len(r.checker.GetLocalTypeParametersOfClassOrInterfaceOrTypeAlias(symbol)) > 0 {
			return errors.Errorf("%s: generic declarations are not supported", ts.Location(declaration))
		}
		if symbol.Flags()&ts.SymbolFlagsInterface != 0 {
			if err := r.interfaceDeclaration(symbol); err != nil {
				return err
			}
		}
		if symbol.Flags()&ts.SymbolFlagsTypeAlias != 0 {
			// Aliases of non-object types are inlined where they are used, but must be valid anyway.
			if _, err := r.typeOf(r.checker.GetDeclaredTypeOfSymbol(symbol), declaration); err != nil {
				return err
			}
		}
		if symbol.Flags()&ts.SymbolFlagsModule != 0 {
			if err := r.members(symbol); err != nil {
				return err
			}
		}
	}
	return nil
}

// interfaceDeclaration reads an interface of methods, each with one object
// parameter and an object result.
func (r *reader) interfaceDeclaration(symbol *ts.Symbol) error {
	declared := r.checker.GetDeclaredTypeOfSymbol(symbol)
	properties := r.checker.GetPropertiesOfType(declared)
	if len(properties) == 0 || !slices.ContainsFunc(properties, isMethod) {
		_, err := r.typeOf(declared, symbol.Declarations()[0])
		return err
	}
	declaring, err := qualifiedName(symbol)
	if err != nil {
		return errors.Errorf("%s: %v", ts.Location(symbol.Declarations()[0]), err)
	}
	for _, method := range properties {
		if err := checkDeclaredInSchema(method); err != nil {
			return err
		}
		read, err := r.method(declaring, method)
		if err != nil {
			return err
		}
		r.methods = append(r.methods, read)
	}
	return nil
}

func isMethod(symbol *ts.Symbol) bool {
	return symbol.Flags()&ts.SymbolFlagsMethod != 0
}

func (r *reader) method(declaring TypeRef, method *ts.Symbol) (Method, error) {
	declaration := method.Declarations()[0]
	form := errors.Errorf("%s: service methods must have the form %s(request: Request): Response", ts.Location(declaration), method.Name())
	if !isMethod(method) {
		return Method{}, errors.Errorf("%s: service interfaces may only declare methods", ts.Location(declaration))
	}
	signatures := r.checker.GetSignaturesOfType(r.checker.GetTypeOfSymbol(method), ts.SignatureKindCall)
	if method.Flags()&ts.SymbolFlagsOptional != 0 || len(signatures) != 1 {
		return Method{}, form
	}
	signature := signatures[0]
	if len(signature.TypeParameters()) > 0 || signature.ThisParameter() != nil || signature.HasRestParameter() ||
		len(signature.Parameters()) != 1 || signature.MinArgumentCount() != 1 {
		return Method{}, form
	}
	parameter, err := r.objectReference(r.checker.GetTypeOfSymbol(signature.Parameters()[0]), declaration)
	if err != nil {
		return Method{}, err
	}
	result, err := r.objectReference(r.checker.GetReturnTypeOfSignature(signature), declaration)
	if err != nil {
		return Method{}, err
	}
	return Method{Interface: declaring, Name: method.Name(), Parameter: parameter, Result: result}, nil
}

func (r *reader) objectReference(t *ts.Type, at *ts.Node) (TypeRef, error) {
	read, err := r.typeOf(t, at)
	if err != nil {
		return "", err
	}
	object, ok := read.(*ObjectType)
	if !ok {
		return "", errors.Errorf("%s: expected an object type, found %s", ts.Location(at), read)
	}
	return object.Name, nil
}

// typeOf maps a checked type to the schema type. at locates errors.
func (r *reader) typeOf(t *ts.Type, at *ts.Node) (Type, error) {
	t, nullable := r.withoutNull(t)
	read, err := r.nonNullType(t, at)
	if err != nil || !nullable {
		return read, err
	}
	return NullableType{Type: read}, nil
}

func (r *reader) nonNullType(t *ts.Type, at *ts.Node) (Type, error) {
	flags := t.Flags()
	switch {
	case flags&ts.TypeFlagsUnknown != 0:
		return UnknownType{}, nil
	case flags&ts.TypeFlagsEnumLiteral != 0:
	case flags&ts.TypeFlagsBoolean != 0:
		return BooleanType{}, nil
	case flags&ts.TypeFlagsString != 0:
		return StringType{}, nil
	case flags&ts.TypeFlagsNumber != 0:
		return NumberType{}, nil
	case flags&(ts.TypeFlagsStringLiteral|ts.TypeFlagsUnion) != 0:
		return r.union(t, at)
	case flags&ts.TypeFlagsObject != 0:
		return r.object(t, at)
	}
	return nil, r.unsupported(t, at)
}

func (r *reader) unsupported(t *ts.Type, at *ts.Node) error {
	return errors.Errorf("%s: type %s is not supported in schemas", ts.Location(at), r.checker.TypeToString(t))
}

// union reads string literals as an enum, or as a number's literals when the
// union also contains number.
func (r *reader) union(t *ts.Type, at *ts.Node) (Type, error) {
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
			return nil, r.unsupported(t, at)
		}
		literals = append(literals, literal)
	}
	// The checker orders union members itself, so declaration order is lost.
	slices.Sort(literals)
	name := None[TypeRef]()
	if alias := t.Alias(); alias != nil {
		// The alias name only renders the union, so one declared outside a module is dropped.
		if qualified, err := qualifiedName(alias.Symbol()); err == nil {
			name = Some(qualified)
		}
	}
	if number {
		return NumberType{Name: name, Literals: literals}, nil
	}
	return EnumType{Name: name, Literals: literals}, nil
}

func (r *reader) object(t *ts.Type, at *ts.Node) (Type, error) {
	if ts.IsTupleType(t) || r.checker.TypeHasCallOrConstructSignatures(t) {
		return nil, r.unsupported(t, at)
	}
	if r.checker.IsArrayType(t) {
		element, err := r.typeOf(r.checker.GetTypeArguments(t)[0], at)
		if err != nil {
			return nil, err
		}
		return ListType{Element: element}, nil
	}
	if indexes := r.checker.GetIndexInfosOfType(t); len(indexes) > 0 {
		if len(indexes) != 1 || indexes[0].KeyType().Flags()&ts.TypeFlagsString == 0 || len(r.checker.GetPropertiesOfType(t)) > 0 {
			return nil, errors.Errorf("%s: maps must have the form { [key: string]: T } or Record<string, T>", ts.Location(at))
		}
		element, err := r.typeOf(indexes[0].ValueType(), at)
		if err != nil {
			return nil, err
		}
		return MapType{Element: element}, nil
	}
	name, err := TypeName(t)
	if err != nil {
		return nil, errors.Errorf("%s: %s: %v", ts.Location(at), r.checker.TypeToString(t), err)
	}
	symbol := namedSymbol(t)
	declared, queued := r.objects[name]
	if !queued {
		declared = &ObjectType{Name: name}
		r.objects[name] = declared
		r.symbols[name] = symbol
		r.pending = append(r.pending, pendingType{declared: declared, t: t})
	}
	if other := r.symbols[name]; other != symbol {
		return nil, errors.Errorf("%s: types declared at %s and %s are both named %q", ts.Location(at),
			ts.Location(other.Declarations()[0]), ts.Location(symbol.Declarations()[0]), name)
	}
	return declared, nil
}

// namedSymbol returns the symbol TypeName names t by: its own, or the alias of a type literal.
func namedSymbol(t *ts.Type) *ts.Symbol {
	symbol := t.Symbol()
	if alias := t.Alias(); alias != nil && (symbol == nil || symbol.Flags()&ts.SymbolFlagsTypeLiteral != 0) {
		return alias.Symbol()
	}
	return symbol
}

func (r *reader) objectType(declared *ObjectType, t *ts.Type) error {
	symbol := t.Symbol()
	if alias := t.Alias(); alias != nil {
		symbol = alias.Symbol()
	}
	declaration := symbol.Declarations()[0]
	if symbol.Flags()&ts.SymbolFlagsClass != 0 {
		return errors.Errorf("%s: classes are not supported in schemas", ts.Location(declaration))
	}
	if len(r.checker.GetLocalTypeParametersOfClassOrInterfaceOrTypeAlias(symbol)) > 0 {
		return errors.Errorf("%s: generic declarations are not supported", ts.Location(declaration))
	}
	properties := r.checker.GetPropertiesOfType(t)
	declared.Fields = make([]Field, 0, len(properties))
	for _, property := range properties {
		if inSchemaFile(declaration) {
			if err := checkDeclaredInSchema(property); err != nil {
				return err
			}
		}
		at := property.Declarations()[0]
		if isMethod(property) {
			return errors.Errorf("%s: object types may only declare properties; %s mixes them with methods", ts.Location(at), declared.Name)
		}
		propertyType := r.checker.GetTypeOfSymbol(property)
		optional := property.Flags()&ts.SymbolFlagsOptional != 0
		if optional {
			propertyType = r.checker.RemoveMissingOrUndefinedType(propertyType)
		}
		read, err := r.typeOf(propertyType, at)
		if err != nil {
			return err
		}
		declared.Fields = append(declared.Fields, Field{Name: property.Name(), Optional: optional, Type: read})
	}
	return nil
}

// withoutNull removes null from a union, reporting whether it was a member.
func (r *reader) withoutNull(t *ts.Type) (rest *ts.Type, nullable bool) {
	if t.Flags()&ts.TypeFlagsUnion == 0 {
		return t, false
	}
	members := slices.DeleteFunc(slices.Clone(t.Types()), func(member *ts.Type) bool {
		return member.Flags()&ts.TypeFlagsNull != 0
	})
	if len(members) == len(t.Types()) {
		return t, false
	}
	return r.checker.GetUnionType(members), true
}
