// Package status reports the SPECTRE migration status for a service: whether
// each environment in its migration plan runs a SPECTRE mirror, the commit it
// runs, and its live candidate status. The deploy platform is pluggable: a
// custom binary registers one through spectre.WithStatusPlatform.
package status

import (
	"context"
	"fmt"
	"os"

	"github.com/alecthomas/errors"
)

// Config configures the status command.
type Config struct {
	// Plan is the path to the SPECTRE migration plan TOML file.
	Plan string `default:"spectre/migration.toml" help:"Path to the SPECTRE migration plan TOML file."`
	// Env, together with Region, overrides the plan's environments with a single target.
	Env string `help:"Environment to check, e.g. staging; overrides the plan's environments (requires Region)."`
	// Region, together with Env, overrides the plan's environments with a single target.
	Region string `help:"Region to check, e.g. us-west-2; overrides the plan's environments (requires Env)."`
	// Live additionally queries each mirror for its live candidate quarantine status.
	Live bool `help:"Query each SPECTRE mirror for live candidate quarantine status."`
}

// Report prints each target's SPECTRE mirror status and git commit for the
// plan's service. Per-target errors are printed; they do not abort the report.
func Report(ctx context.Context, cfg Config, registry *Registry) error {
	plan, err := LoadPlan(cfg.Plan)
	if err != nil {
		return err
	}

	platform, err := registry.New(plan.Platform, plan.App)
	if err != nil {
		return errors.WithStack(err)
	}

	useColor = colorEnabled(os.Stdout)
	fmt.Println(bold(plan.App))
	for _, target := range targets(cfg, plan) {
		reportTarget(ctx, platform, target, cfg.Live)
	}
	return nil
}

// targets returns the environments to check: the single cfg.Env/cfg.Region
// override when both are set, otherwise every environment in the plan.
func targets(cfg Config, plan Plan) []string {
	if cfg.Env != "" && cfg.Region != "" {
		return []string{cfg.Env + "-" + cfg.Region}
	}
	return plan.Environments
}

// reportTarget prints the SPECTRE mirror status for one <env>-<region> target.
func reportTarget(ctx context.Context, platform Platform, target string, live bool) {
	mirror := platform.Mirror(target)
	if !mirror.Enabled {
		fmt.Printf("  %s: %s\n", bold(target), dim("no spectre mirror configured"))
		return
	}
	fmt.Printf("  %s: %s\n", bold(target), green("spectre mirror deployed"))
	printCommit(ctx, platform, mirror)
	if live {
		printStatus(ctx, platform, target)
	}
}

// printStatus resolves and prints the live candidate quarantine status for one
// mirror, falling back to a notice when the mirror cannot be reached.
func printStatus(ctx context.Context, platform Platform, target string) {
	report, err := platform.ResolveStatus(ctx, target)
	switch {
	case err != nil:
		fmt.Printf("    candidate: %s\n", yellow(fmt.Sprintf("status unavailable (%v)", err)))
	case report.Quarantined:
		fmt.Printf("    candidate: %s\n", red("quarantined"))
		printQuarantineReasons(report)
	default:
		fmt.Printf("    candidate: %s\n", green("healthy"))
	}
}

// printQuarantineReasons prints each quarantined proxy's reason and start time.
func printQuarantineReasons(report QuarantineReport) {
	for _, source := range report.Sources {
		if !source.Quarantined {
			continue
		}
		detail := source.Reason
		if source.Since != "" {
			detail = fmt.Sprintf("%s (since %s)", detail, source.Since)
		}
		fmt.Printf("      %s: %s\n", source.Name, red(detail))
	}
}

// printCommit resolves and prints the git commit for one mirror, falling back
// to the image digest on a resolver error or an unresolved commit.
func printCommit(ctx context.Context, platform Platform, mirror MirrorState) {
	commit, err := platform.ResolveCommit(ctx, mirror.Image)
	switch {
	case err != nil:
		fmt.Printf("    commit: %s\n", red(fmt.Sprintf("unknown (resolve error: %v, digest %s)", err, mirror.Digest)))
	case commit == "":
		fmt.Printf("    commit: %s\n", yellow(fmt.Sprintf("unknown (digest %s)", mirror.Digest)))
	default:
		fmt.Printf("    commit: %s\n", green(commit))
	}
	fmt.Printf("    image: %s\n", dim(mirror.Image))
}
