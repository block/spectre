package status

import (
	"os"

	"github.com/alecthomas/errors"
	"github.com/pelletier/go-toml"
)

// File is a filesystem path to a file.
type File string

// MigrationConfig describes a SPECTRE migration for one application: the deploy
// platform it runs on and the environments that run a SPECTRE mirror.
type MigrationConfig struct {
	// App is the service name the config migrates.
	App string `toml:"app"`
	// Platform names the deploy platform the service runs on, e.g. ski.
	Platform string `toml:"platform"`
	// Environments lists the <env>-<region> targets to check, e.g. staging-us-west-2.
	Environments []string `toml:"environments"`
}

// LoadMigrationConfig reads and validates the migration config TOML file at path.
func LoadMigrationConfig(path File) (MigrationConfig, error) {
	data, err := os.ReadFile(string(path))
	if err != nil {
		return MigrationConfig{}, errors.Wrapf(err, "read migration config %q", path)
	}

	var migration MigrationConfig
	if err := toml.Unmarshal(data, &migration); err != nil {
		return MigrationConfig{}, errors.Wrapf(err, "parse migration config %q", path)
	}

	if migration.App == "" {
		return MigrationConfig{}, errors.Errorf("migration config %q: app is required", path)
	}
	if migration.Platform == "" {
		return MigrationConfig{}, errors.Errorf("migration config %q: platform is required", path)
	}
	return migration, nil
}
