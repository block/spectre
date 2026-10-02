package typescript

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing/fstest"

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
			return nil, errors.New("program file " + name + " is not absolute")
		}
		tree[strings.TrimPrefix(name, "/")] = &fstest.MapFile{Data: []byte(text)}
	}
	fileSystem := bundled.WrapFS(iovfs.From(tree, true))
	host := compiler.NewCompilerHost("/", fileSystem, bundled.LibPath(), nil, nil, nil)
	program := compiler.NewProgram(compiler.ProgramOptions{
		ProgramConfig: compiler.ProgramConfig{
			Config: &tsoptions.ParsedCommandLine{ParsedConfig: &tsoptions.ParsedOptions{
				FileNames: slices.Sorted(maps.Keys(files)),
				CompilerOptions: &core.CompilerOptions{
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
			}},
			// One checker keeps diagnostics and type identities in a single checker.
			SingleThreaded: core.TSTrue,
		},
		ProgramHosts: compiler.ProgramHosts{Host: host},
	})
	diagnostics := compiler.GetDiagnosticsOfAnyProgram(ctx, program, nil, false, program.GetBindDiagnostics, program.GetSemanticDiagnostics)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(diagnostics) > 0 {
		var report strings.Builder
		diagnosticwriter.WriteFormatDiagnostics(&report, diagnosticwriter.FromASTDiagnostics(compiler.SortAndDeduplicateDiagnostics(diagnostics)),
			&diagnosticwriter.FormattingOptions{
				Locale:              locale.Default,
				ComparePathsOptions: tspath.ComparePathsOptions{UseCaseSensitiveFileNames: true, CurrentDirectory: "/"},
				NewLine:             "\n",
			})
		return nil, errors.New(strings.TrimSpace(report.String()))
	}
	checker, _ := program.GetTypeChecker(ctx)
	return &Program{program: program, checker: checker}, nil
}

// SourceFile returns the named root file, or nil if it is not in the program.
func (p *Program) SourceFile(name string) *SourceFile {
	return p.program.GetSourceFile(name)
}

// Checker returns the program's type checker.
func (p *Program) Checker() *Checker {
	return p.checker
}
