// Package status reports the SPECTRE migration status for a service: whether
// each environment in its migration config runs a SPECTRE mirror, the commit it
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
	// ConfigFile is the path to the SPECTRE migration config TOML file.
	ConfigFile File `default:"spectre/migration.toml" help:"Path to the SPECTRE migration config TOML file."`
}

// Report prints each target's SPECTRE mirror status and git commit for the
// config's service. Per-target errors are printed; they do not abort the report.
func Report(ctx context.Context, cfg Config, registry *Registry) error {
	migration, err := LoadMigrationConfig(cfg.ConfigFile)
	if err != nil {
		return err
	}

	platform, err := registry.New(migration.Platform, migration.App)
	if err != nil {
		return errors.WithStack(err)
	}

	useColor = colorEnabled(os.Stdout)
	fmt.Println(bold(migration.App))
	for _, target := range migration.Environments {
		reportTarget(ctx, platform, target)
	}
	return nil
}

// reportTarget prints the SPECTRE mirror status for one <env>-<region> target.
func reportTarget(ctx context.Context, platform Platform, target string) {
	mirror, err := platform.Mirror(ctx, target)
	if err != nil {
		fmt.Printf("  %s: %s\n", bold(target), red(fmt.Sprintf("unknown (%v)", err)))
		return
	}
	if !mirror.Enabled {
		fmt.Printf("  %s: %s\n", bold(target), dim("no spectre mirror configured"))
		return
	}
	fmt.Printf("  %s: %s\n", bold(target), green("spectre mirror deployed"))
	printCommit(ctx, platform, mirror)
	printStatus(ctx, platform, target)
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
		fmt.Printf("    commit: %s\n", green(string(commit)))
	}
	fmt.Printf("    image: %s\n", dim(string(mirror.Image)))
}
