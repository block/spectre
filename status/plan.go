package status

import (
	"os"

	"github.com/alecthomas/errors"
	"github.com/pelletier/go-toml"
)

// Plan describes a SPECTRE migration for one application: the deploy platform it
// runs on and the environments that run a SPECTRE mirror.
type Plan struct {
	// App is the service name the plan migrates.
	App string `toml:"app"`
	// Platform names the deploy platform the service runs on, e.g. ski.
	Platform string `toml:"platform"`
	// Environments lists the <env>-<region> targets to check, e.g. staging-us-west-2.
	Environments []string `toml:"environments"`
}

// LoadPlan reads and validates the migration plan TOML file at path.
func LoadPlan(path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, errors.Wrapf(err, "read migration plan %q", path)
	}

	var plan Plan
	if err := toml.Unmarshal(data, &plan); err != nil {
		return Plan{}, errors.Wrapf(err, "parse migration plan %q", path)
	}

	if plan.App == "" {
		return Plan{}, errors.Errorf("migration plan %q: app is required", path)
	}
	if plan.Platform == "" {
		return Plan{}, errors.Errorf("migration plan %q: platform is required", path)
	}
	return plan, nil
}
