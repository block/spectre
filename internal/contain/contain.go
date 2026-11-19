package contain

import (
	"context"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/logger"
)

// tableName names the packet filter table that holds every rule contain installs.
const tableName = "spectre"

// Apply rejects the configured user's IP traffic and redirects the configured
// connections, replacing any rules a previous run installed.
func Apply(ctx context.Context, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if err := install(config); err != nil {
		return errors.WithStack(err)
	}
	redirects := make([]string, 0, len(config.Redirects))
	for _, redirect := range config.Redirects {
		redirects = append(redirects, redirect.String())
	}
	logger.FromContext(ctx).InfoContext(ctx, "Rejecting IP traffic", "candidate_uid", config.CandidateUID, "redirects", redirects, "table", tableName)
	return nil
}
