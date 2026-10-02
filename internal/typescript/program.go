package typescript

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing/fstest"

	"github.com/alecthomas/errors"
	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/bundled"
	"github.com/microsoft/TypeScript/tsc/internal/compiler"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/diagnosticwriter"
	"github.com/microsoft/TypeScript/tsc/internal/locale"
	"github.com/microsoft/TypeScript/tsc/internal/tsoptions"
	"github.com/microsoft/TypeScript/tsc/internal/tspath"
	"github.com/microsoft/TypeScript/tsc/internal/vfs/iovfs"
)

// Program is a type-checked set of files. Its checker and every type and symbol
// it returns belong to this program and must not be mixed with another's.
type Program struct {
	program *compiler.Program
	checker *Checker
}

// NewProgram type-checks files as one strict ES2020 project with no DOM. Names are
// absolute and slash-separated, and every file is a root. Any diagnostic is an error.
func NewProgram(ctx context.Context, files map[string]string) (*Program, error) {
	tree := fstest.MapFS{}
	for name, text := range files {
		if !strings.HasPrefix(name, "/") {
			return nil, errors.Errorf("program file %s is not absolute", name)
		}
		tree[strings.TrimPrefix(name, "/")] = &fstest.MapFile{Data: []byte(text)}
	}
	fileSystem := bundled.WrapFS(iovfs.From(tree, true))
	host := compiler.NewCompilerHost("/", fileSystem, bundled.LibPath(), nil, nil, nil)
	program := compiler.NewProgram(compiler.ProgramOptions{
		Config: tsoptions.NewParsedCommandLine(
			&core.CompilerOptions{
				Target:                     core.ScriptTargetES2020,
				Module:                     core.ModuleKindESNext,
				ModuleResolution:           core.ModuleResolutionKindBundler,
				Lib:                        []string{"lib.es2020.d.ts"},
				Types:                      []string{},
				Strict:                     core.TSTrue,
				IsolatedModules:            core.TSTrue,
				NoEmit:                     core.TSTrue,
				AllowImportingTsExtensions: core.TSTrue,
				SkipDefaultLibCheck:        core.TSTrue,
			},
			slices.Sorted(maps.Keys(files)),
			nil,
			tspath.ComparePathsOptions{UseCaseSensitiveFileNames: true, CurrentDirectory: "/"},
		),
		// One checker keeps diagnostics and type identities in a single checker.
		SingleThreaded: core.TSTrue,
		Host:           host,
	})
	diagnostics, err := programDiagnostics(ctx, program)
	if err != nil {
		return nil, err
	}
	if len(diagnostics) > 0 {
		var report strings.Builder
		diagnosticwriter.WriteFormatDiagnostics(&report, diagnosticwriter.FromASTDiagnostics(compiler.SortAndDeduplicateDiagnostics(diagnostics)),
			&diagnosticwriter.FormattingOptions{
				Locale:                    locale.Default,
				UseCaseSensitiveFileNames: true,
				CurrentDirectory:          "/",
				NewLine:                   "\n",
			})
		return nil, errors.New(strings.TrimSpace(report.String()))
	}
	checker, _ := program.GetTypeChecker(ctx)
	return &Program{program: program, checker: checker}, nil
}

// programDiagnostics reports cancellation as an error. A cancelled checker panics
// when asked about its next file, so that panic is recovered once ctx is done.
func programDiagnostics(ctx context.Context, program *compiler.Program) (diagnostics []*ast.Diagnostic, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if ctx.Err() == nil {
				panic(recovered)
			}
			diagnostics, err = nil, errors.WithStack(ctx.Err())
		}
	}()
	diagnostics = compiler.GetDiagnosticsOfAnyProgram(ctx, program, nil, false, program.GetBindDiagnostics, program.GetSemanticDiagnostics)
	return diagnostics, errors.WithStack(ctx.Err())
}

// SourceFile returns the named root file, or nil if it is not in the program.
func (p *Program) SourceFile(name string) *SourceFile {
	return p.program.GetSourceFile(name)
}

// Checker returns the program's type checker.
func (p *Program) Checker() *Checker {
	return p.checker
}
