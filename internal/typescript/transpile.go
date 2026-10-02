package typescript

import (
	"context"

	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/transpile"
)

// Transpile strips types from one TypeScript module and emits an ES2020 module.
// It neither checks types nor resolves imports, so specifiers are kept as written.
func Transpile(ctx context.Context, fileName string, source string) (code string, diagnostics []string) {
	output := transpile.TranspileModule(ctx, source, transpile.Options{
		FileName: fileName,
		CompilerOptions: &core.CompilerOptions{
			Target: core.ScriptTargetES2020,
			Module: core.ModuleKindESNext,
		},
		ReportDiagnostics: true,
	})
	if output == nil {
		return "", []string{"transpilation produced no output"}
	}
	for _, diagnostic := range output.Diagnostics {
		diagnostics = append(diagnostics, diagnostic.String())
	}
	return output.OutputText, diagnostics
}
