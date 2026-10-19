package status_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/status"
)

func TestLoadPlan(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		want    status.Plan
		wantErr string
	}{
		{
			name: "Valid",
			toml: `
app          = "feeplans-fe"
platform     = "ski"
environments = ["staging-us-west-2"]
`,
			want: status.Plan{
				App:          "feeplans-fe",
				Platform:     "ski",
				Environments: []string{"staging-us-west-2"},
			},
		},
		{
			name:    "MissingApp",
			toml:    `platform = "ski"`,
			wantErr: "app is required",
		},
		{
			name:    "MissingPlatform",
			toml:    `app = "feeplans-fe"`,
			wantErr: "platform is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "migration.toml")
			assert.NoError(t, os.WriteFile(path, []byte(test.toml), 0o600))

			plan, err := status.LoadPlan(path)
			if test.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.want, plan)
		})
	}
}

func TestLoadPlanMissingFile(t *testing.T) {
	_, err := status.LoadPlan(filepath.Join(t.TempDir(), "missing.toml"))
	assert.Error(t, err)
}
