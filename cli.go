package spectre

import (
	"context"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/jsonschema"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/supervisor"
)

// cli is the built-in command line. Extensions add to it, so custom binaries
// never copy it.
type cli struct {
	Log     logger.Config    `embed:""`
	Config  kong.ConfigFlag  `placeholder:"FILE" help:"Load flag values from a TOML file. Command-line flags take precedence."`
	Version kong.VersionFlag `help:"Print the version and exit."`

	Run runCommand `cmd:"" help:"Run the supervisor with the embedded proxies, splitting resources between a candidate and a reference."`
	Gen genCommand `cmd:"" help:"Generate TypeScript schema declarations and the script API."`
}

// runCommand supervises the candidate and reference behind the embedded proxies.
type runCommand struct {
	Supervisor supervisor.Config `embed:""`
}

func (c *runCommand) Run(ctx context.Context) error {
	super, err := supervisor.New(ctx, c.Supervisor)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(super.Run(ctx))
}

// genCommand groups the schema declaration generators.
type genCommand struct {
	Proto      protoCommand      `cmd:"" help:"Generate TypeScript schema declarations from protobuf descriptor sets."`
	JSONSchema jsonSchemaCommand `cmd:"" name:"json-schema" help:"Generate a TypeScript schema declaration from JSON Schema files."`
	Module     moduleCommand     `cmd:"" help:"Write the spectre script API declaration for editors."`
}

type protoCommand struct {
	Descriptors descriptors.Config `embed:""`
	Output      string             `required:"" type:"path" placeholder:"DIR" help:"Directory to write one .d.ts file per protobuf file into."`
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

type jsonSchemaCommand struct {
	JSONSchema jsonschema.Config `embed:""`
	Output     string            `required:"" type:"path" placeholder:"FILE" help:"Path to write the module's .d.ts file to."`
}

func (c *jsonSchemaCommand) Run(ctx context.Context) error {
	return errors.Wrap(jsonschema.WriteDeclarations(ctx, c.JSONSchema, c.Output), "write declarations")
}

type moduleCommand struct {
	Output string `required:"" type:"path" placeholder:"FILE" help:"Path to write the spectre.d.ts module declaration to."`
}

func (c *moduleCommand) Run() error {
	return errors.Wrap(javascript.WriteModuleDeclaration(c.Output), "write spectre module")
}
