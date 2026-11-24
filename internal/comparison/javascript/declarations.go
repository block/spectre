package javascript

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/errors"
)

//go:embed spectre.d.ts
var moduleDeclaration string

// ModuleDeclaration returns the TypeScript declaration of the spectre module: its shared
// declarations, then each binding's members and payloads.
func ModuleDeclaration(bindings []Binding) string {
	var declaration strings.Builder
	declaration.WriteString(moduleDeclaration)
	for _, binding := range bindings {
		declaration.WriteString("\n")
		declaration.WriteString(binding.Members())
		declaration.WriteString("\n")
		declaration.WriteString(binding.Payloads())
	}
	return declaration.String()
}

// WriteModuleDeclaration writes the module declaration to output, creating its parent directory.
func WriteModuleDeclaration(output string, bindings []Binding) error {
	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		return errors.Wrap(err, "create spectre declaration directory")
	}
	return errors.Wrap(os.WriteFile(output, []byte(ModuleDeclaration(bindings)), 0o600), "write spectre declaration")
}
