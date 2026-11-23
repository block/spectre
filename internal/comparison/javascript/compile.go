package javascript

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	ts "github.com/microsoft/TypeScript/tsc/shim/typescript"

	"github.com/block/spectre/internal/schema"
)

// Scripts and the module declaration sit beside the schema in one program, so
// error locations name the directory each file came from.
const (
	scriptsRoot           = "/scripts/"
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
	loaded, err := schema.FromProgram(program, declarations)
	if err != nil {
		return nil, errors.Wrap(err, "read schema")
	}
	result := &compiled{schema: loaded, modules: map[string]string{}}
	for _, name := range slices.Sorted(maps.Keys(scripts)) {
		if !isScript(name) {
			continue
		}
		source, err := passTypeArguments(program, loaded, program.SourceFile(scriptsRoot+name))
		if err != nil {
			return nil, err
		}
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

type insertion struct {
	offset int
	text   string
}

// passTypeArguments rewrites every call the checker resolved to a spectre function,
// including calls through aliases, so the runtime receives the type arguments.
func passTypeArguments(program *ts.Program, loaded *schema.Schema, file *ts.SourceFile) (string, error) {
	insertions := []insertion{}
	var rewriteErr error
	var visit func(node *ts.Node) bool
	visit = func(node *ts.Node) bool {
		if node.Kind == ts.KindCallExpression {
			found, err := typeArguments(program.Checker(), loaded, node)
			if err != nil {
				rewriteErr = err
				return true
			}
			if inserted, ok := found.Get(); ok {
				insertions = append(insertions, inserted)
			}
		}
		return node.ForEachChild(visit)
	}
	file.AsNode().ForEachChild(visit)
	if rewriteErr != nil {
		return "", rewriteErr
	}
	// Later offsets are applied first so earlier ones stay valid.
	slices.SortFunc(insertions, func(left, right insertion) int { return cmp.Compare(right.offset, left.offset) })
	source := file.Text()
	for _, inserted := range insertions {
		source = source[:inserted.offset] + inserted.text + source[inserted.offset:]
	}
	return source, nil
}

// typeArguments returns the arguments to insert after a spectre call's opening
// parenthesis, or None for any other call.
func typeArguments(checker *ts.Checker, loaded *schema.Schema, call *ts.Node) (Option[insertion], error) {
	signature := checker.GetResolvedSignature(call)
	if signature == nil || signature.Declaration() == nil {
		return None[insertion](), nil // Most calls are not registrations.
	}
	declaration := signature.Declaration()
	if ts.SourceFileOf(declaration).FileName() != moduleDeclarationFile || declaration.Name() == nil {
		return None[insertion](), nil // Only spectre functions take type arguments at runtime.
	}
	expected := len(declaration.TypeParameters())
	if expected == 0 {
		return None[insertion](), nil // Only generic spectre functions take type arguments at runtime.
	}
	function := declaration.Name().Text()
	owner := ts.MemberOwnerName(declaration)
	if strings.HasPrefix(owner, mysqlOwner) {
		return None[insertion](), nil // MySQL row types only check scripts.
	}
	// display names the call the way a script writes it, e.g. "IngressHTTPRule.match".
	display := function
	if owner != "" {
		display = owner + "." + function
	}
	types := call.TypeArguments()
	if len(types) != expected {
		return None[insertion](), errors.Errorf("%s: spectre.%s needs %d explicit type argument(s)", ts.Location(call), display, expected)
	}
	values := make([]string, 0, len(types))
	for index, node := range types {
		resolved := checker.GetTypeFromTypeNode(node)
		if function == string(targetField) && index == 1 {
			path, ok := ts.StringLiteral(resolved)
			if !ok {
				return None[insertion](), errors.Errorf("%s: spectre.field path must be one string literal type", ts.Location(node))
			}
			values = append(values, path)
			continue
		}
		name, err := bodyTypeName(resolved, index == 0 && display == "IngressHTTPRule."+matchMethod)
		if err == nil && name != textTypeName && name != unknownTypeName {
			_, err = loaded.Type(name)
		}
		if err != nil {
			return None[insertion](), errors.Errorf("%s: spectre.%s type argument %s: %v", ts.Location(node), display, checker.TypeToString(resolved), err)
		}
		values = append(values, string(name))
	}
	encoded := make([]string, 0, len(values)+1)
	for _, value := range values {
		// JSON strings are valid JavaScript string literals.
		literal, _ := json.Marshal(value) //nolint:errcheck // Strings always encode.
		encoded = append(encoded, string(literal))
	}
	if len(call.Arguments()) > 0 {
		encoded = append(encoded, "")
	}
	return Some(insertion{offset: call.AsCallExpression().Arguments.Loc.Pos(), text: strings.Join(encoded, ", ")}), nil
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
