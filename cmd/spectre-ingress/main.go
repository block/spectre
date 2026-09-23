package main

import (
	"context"
	"os"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/schema"
)

func main() {
	var cli struct {
		Log         logger.Config    `embed:""`
		Version     kong.VersionFlag `help:"Print the version and exit."`
		CheckSchema struct {
			DescriptorSet string `arg:"" type:"existingfile" help:"Binary protobuf descriptor set, including imports."`
		} `cmd:"" help:"Validate a local protobuf descriptor set."`
	}
	cli.Log = logger.NewConfig()
	kctx := kong.Parse(&cli, kong.Vars{"version": internal.Version})
	log := logger.New(cli.Log, os.Stderr)
	ctx := logger.WithLogger(context.Background(), log)
	data, err := os.ReadFile(cli.CheckSchema.DescriptorSet)
	kctx.FatalIfErrorf(errors.Wrap(err, "read descriptor set"))
	_, err = schema.New(data)
	kctx.FatalIfErrorf(err)
	log.InfoContext(ctx, "Descriptor set is valid", "path", cli.CheckSchema.DescriptorSet)
}
