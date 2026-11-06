package contain

import (
	"context"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/logger"
)

// tableName names the packet filter table that holds every rule contain installs.
const tableName = "spectre"

// Apply rejects the configured user's IP traffic, replacing any rules a previous
// run installed, so running it again gives the same result.
func Apply(ctx context.Context, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if err := install(config.UID); err != nil {
		return errors.WithStack(err)
	}
	logger.FromContext(ctx).InfoContext(ctx, "Rejecting IP traffic", "uid", config.UID, "table", tableName)
	return nil
}
