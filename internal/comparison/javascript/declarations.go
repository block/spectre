package javascript

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/alecthomas/errors"
)

//go:embed spectre.d.ts
var moduleDeclaration string

// ModuleDeclaration returns the TypeScript declaration of the spectre module.
func ModuleDeclaration() string {
	return moduleDeclaration
}

// WriteModuleDeclaration writes the module declaration to output, creating its parent directory.
func WriteModuleDeclaration(output string) error {
	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		return errors.Wrap(err, "create spectre declaration directory")
	}
	return errors.Wrap(os.WriteFile(output, []byte(moduleDeclaration), 0o600), "write spectre declaration")
}
