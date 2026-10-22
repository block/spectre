package scaffold_test

import (
	"testing"
	"testing/fstest"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/scaffold"
)

func TestParseManifestErrors(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		error string
	}{
		{
			name:  "CustomisableOutsideManagedDir",
			files: fstest.MapFS{"scaffold.toml": {Data: []byte(`files.customisable = ["config.toml"]`)}},
			error: `customisable pattern "config.toml" must be in spectre/`,
		},
		{
			name: "CustomisableTemplateName",
			files: fstest.MapFS{
				"scaffold.toml":                  {Data: []byte(`files.customisable = ["spectre/custom/**"]`)},
				"spectre/custom/{{ .name }}.txt": {Data: []byte("")},
			},
			error: `customisable file "spectre/custom/{{ .name }}.txt" must not have a template as its name`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := scaffold.ParseManifest(test.files)
			assert.EqualError(t, err, test.error)
		})
	}
}
