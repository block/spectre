package javascript

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	ts "github.com/microsoft/TypeScript/tsc/shim/typescript"

	"github.com/block/spectre/internal/schema"
)

// Scripts and the module declaration sit beside the schema in one program, so
// error locations name the directory each file came from.
const (
	scriptsRoot           = schema.ScriptRoot
	moduleDeclarationFile = "/spectre.d.ts"
)

// compiled is the checked schema and the executable form of every script.
type compiled struct {
	schema  *schema.Schema
	modules map[string]string
}

// compile type-checks scripts against the schema, then transpiles them after
// inserting each spectre call's resolved type names, which transpilation erases.
func compile(ctx context.Context, declarations, scripts map[string]string) (*compiled, error) {
	files := schema.ProgramFiles(declarations)
	for name, source := range scripts {
		files[scriptsRoot+name] = source
	}
	files[moduleDeclarationFile] = moduleDeclaration
	program, err := ts.NewProgram(ctx, files)
	if err != nil {
		return nil, errors.Wrap(err, "check comparison scripts")
	}
	insertions := map[string][]insertion{}
	references := []*ts.Node{}
	for _, name := range slices.Sorted(maps.Keys(scripts)) {
		if !isScript(name) {
			continue
		}
		calls, err := spectreCalls(program.Checker(), program.SourceFile(scriptsRoot+name))
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			inserted, referenced, err := typeArguments(program.Checker(), call)
			if err != nil {
				return nil, err
			}
			insertions[name] = append(insertions[name], inserted)
			references = append(references, referenced...)
		}
	}
	// Scripts may name types they declare themselves, so the schema includes every type they name.
	loaded, err := schema.FromProgram(program, declarations, references)
	if err != nil {
		return nil, errors.Wrap(err, "read schema")
	}
	result := &compiled{schema: loaded, modules: map[string]string{}}
	for _, name := range slices.Sorted(maps.Keys(scripts)) {
		if !isScript(name) {
			continue
		}
		source := insertTypeArguments(program.SourceFile(scriptsRoot+name).Text(), insertions[name])
		code, diagnostics := ts.Transpile(ctx, scriptsRoot+name, source)
		if err := ctx.Err(); err != nil {
			return nil, errors.Wrapf(err, "transpile comparison module %q", name)
		}
		if len(diagnostics) != 0 {
			return nil, errors.Errorf("transpile comparison module %q: %s", name, strings.Join(diagnostics, "; "))
		}
		result.modules[name] = code
	}
	return result, nil
}

// spectreCall is a call the checker resolved to a generic spectre function, with
// the type arguments the script wrote.
type spectreCall struct {
	node     *ts.Node
	function string
	// display names the call the way a script writes it, e.g. "IngressHTTPRule.match".
	display string
	types   []*ts.Node
}

// spectreCalls returns every call in file to a generic spectre function, including
// calls through aliases.
func spectreCalls(checker *ts.Checker, file *ts.SourceFile) ([]spectreCall, error) {
	calls := []spectreCall{}
	var findErr error
	var visit func(node *ts.Node) bool
	visit = func(node *ts.Node) bool {
		if node.Kind == ts.KindCallExpression {
			found, err := resolveSpectreCall(checker, node)
			if err != nil {
				findErr = err
				return true
			}
			if call, ok := found.Get(); ok {
				calls = append(calls, call)
			}
		}
		return node.ForEachChild(visit)
	}
	file.AsNode().ForEachChild(visit)
	return calls, findErr
}

// resolveSpectreCall returns the spectre call that call makes, or None for any other call.
func resolveSpectreCall(checker *ts.Checker, call *ts.Node) (Option[spectreCall], error) {
	signature := checker.GetResolvedSignature(call)
	if signature == nil || signature.Declaration() == nil {
		return None[spectreCall](), nil // Most calls are not registrations.
	}
	declaration := signature.Declaration()
	if ts.SourceFileOf(declaration).FileName() != moduleDeclarationFile || declaration.Name() == nil {
		return None[spectreCall](), nil // Only spectre functions take type arguments at runtime.
	}
	parameters := declaration.TypeParameters()
	if len(parameters) == 0 {
		return None[spectreCall](), nil // Only generic spectre functions take type arguments at runtime.
	}
	function := declaration.Name().Text()
	owner := ts.MemberOwnerName(declaration)
	if strings.HasPrefix(owner, mysqlOwner) {
		return None[spectreCall](), nil // MySQL row types only check scripts.
	}
	display := function
	if owner != "" {
		display = owner + "." + function
	}
	required := 0
	for _, parameter := range parameters {
		if parameter.AsTypeParameterDeclaration().DefaultType == nil {
			required++
		}
	}
	types := call.TypeArguments()
	if len(types) < required || len(types) > len(parameters) {
		count := strconv.Itoa(len(parameters))
		if required != len(parameters) {
			count = strconv.Itoa(required) + " or " + count
		}
		return None[spectreCall](), errors.Errorf("%s: spectre.%s needs %s explicit type argument(s)", ts.Location(call), display, count)
	}
	return Some(spectreCall{node: call, function: function, display: display, types: types}), nil
}

type insertion struct {
	offset int
	text   string
}

// insertTypeArguments applies insertions to source, so the runtime receives each spectre
// call's type arguments as its first arguments.
func insertTypeArguments(source string, insertions []insertion) string {
	insertions = slices.Clone(insertions)
	// Later offsets are applied first so earlier ones stay valid.
	slices.SortFunc(insertions, func(left, right insertion) int { return cmp.Compare(right.offset, left.offset) })
	for _, inserted := range insertions {
		source = source[:inserted.offset] + inserted.text + source[inserted.offset:]
	}
	return source
}

// typeArguments returns the arguments to insert after a spectre call's opening
// parenthesis, and the type arguments that name schema types.
func typeArguments(checker *ts.Checker, call spectreCall) (insertion, []*ts.Node, error) {
	values := make([]string, 0, len(call.types))
	references := []*ts.Node{}
	for index, node := range call.types {
		resolved := checker.GetTypeFromTypeNode(node)
		if call.function == string(targetField) && index == 1 {
			path, ok := ts.StringLiteral(resolved)
			if !ok {
				return insertion{}, nil, errors.Errorf("%s: spectre.field path must be one string literal type", ts.Location(node))
			}
			values = append(values, path)
			continue
		}
		name, err := bodyTypeName(resolved, index == 0 && call.display == "IngressHTTPRule."+matchMethod)
		if err != nil {
			return insertion{}, nil, errors.Errorf("%s: spectre.%s type argument %s: %v", ts.Location(node), call.display, checker.TypeToString(resolved), err)
		}
		if name != textTypeName && name != unknownTypeName {
			references = append(references, node)
		}
		values = append(values, string(name))
	}
	encoded := make([]string, 0, len(values)+1)
	for _, value := range values {
		// JSON strings are valid JavaScript string literals.
		literal, _ := json.Marshal(value) //nolint:errcheck // Strings always encode.
		encoded = append(encoded, string(literal))
	}
	if len(call.node.Arguments()) > 0 {
		encoded = append(encoded, "")
	}
	return insertion{offset: call.node.AsCallExpression().Arguments.Loc.Pos(), text: strings.Join(encoded, ", ")}, references, nil
}

// bodyTypeName names a schema type, or the spectre Text type or unknown where an ingress
// rule's body may have them.
func bodyTypeName(resolved *ts.Type, body bool) (schema.TypeRef, error) {
	if resolved.Flags()&ts.TypeFlagsUnknown != 0 {
		if !body {
			return "", errors.New("only ingress rules may compare bodies as unknown")
		}
		return unknownTypeName, nil
	}
	symbol := resolved.Symbol()
	if symbol == nil || len(symbol.Declarations()) == 0 || ts.SourceFileOf(symbol.Declarations()[0]).FileName() != moduleDeclarationFile {
		return errors.WithStack2(schema.TypeName(resolved))
	}
	if !body || symbol.Name() != "Text" {
		return "", errors.New("only ingress rules may compare bodies as Text")
	}
	return textTypeName, nil
}
