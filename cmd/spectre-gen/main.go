package main

import (
	"context"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/descriptors"
)

type cli struct {
	Version kong.VersionFlag `help:"Print the version and exit."`
	Proto   protoCommand     `cmd:"" help:"Generate TypeScript schema declarations from protobuf descriptor sets."`
	Module  moduleCommand    `cmd:"" help:"Write the spectre script API declaration for editors."`
}

type protoCommand struct {
	Descriptors descriptors.Config `embed:""`
	Output      string             `required:"" type:"path" placeholder:"DIR" help:"Directory to write one .d.ts file per protobuf file into."`
}

type moduleCommand struct {
	Output string `required:"" type:"path" placeholder:"FILE" help:"Path to write the spectre.d.ts module declaration to."`
}

func main() {
	command := &cli{}
	kctx := kong.Parse(command, kong.Vars{"version": internal.Version})
	kctx.BindTo(context.Background(), (*context.Context)(nil))
	kctx.FatalIfErrorf(kctx.Run())
}

func (c *protoCommand) Run(ctx context.Context) error {
	if c.Descriptors.DescriptorsDir == "" {
		return errors.New("a descriptors directory is required")
	}
	set, err := descriptors.LoadDescriptorSets(c.Descriptors)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.Wrap(descriptors.WriteDeclarations(ctx, set, c.Output), "write declarations")
}

func (c *moduleCommand) Run() error {
	return errors.Wrap(javascript.WriteModuleDeclaration(c.Output), "write spectre module")
}
