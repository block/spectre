package spectre

import (
	"context"
	"os"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/jsonschema"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/scaffold"
	"github.com/block/spectre/internal/state"
	"github.com/block/spectre/internal/supervisor"
	"github.com/block/spectre/internal/terminal"
	"github.com/block/spectre/status"
)

// cli is the built-in command line. Extensions add to it, so custom binaries
// never copy it.
type cli struct {
	Log     logger.Config    `embed:"" prefix:"log-"`
	Config  kong.ConfigFlag  `placeholder:"FILE" help:"Load flag values from a TOML file. Command-line flags take precedence."`
	Version kong.VersionFlag `help:"Print the version and exit."`

	Run    runCommand    `cmd:"" help:"Run the supervisor with the embedded proxies, splitting resources between a candidate and a reference."`
	Gen    genCommand    `cmd:"" help:"Generate TypeScript schema declarations and the script API."`
	Status statusCommand `cmd:"" help:"Report the SPECTRE migration status and mirror deployment for a service."`
	State  stateCommand  `cmd:"" help:"Describe a service's migration state."`

	Init initCommand `cmd:"" help:"Write the files a migration needs into a service checkout from a template, or bring them up to date."`
}

// initCommand renders a template into a service checkout.
type initCommand struct {
	Init scaffold.Config `embed:""`
}

func (c *initCommand) Run(registry *scaffold.Registry) error {
	return errors.WithStack(scaffold.Init(os.Stdout, c.Init, registry))
}

// stateCommand groups the commands that read a service's migration state.
type stateCommand struct {
	Types    stateTypesCommand    `cmd:"" help:"Describe each node type the migration state can use."`
	Validate stateValidateCommand `cmd:"" help:"Check the migration state for errors, and warn about nodes ingress never reaches."`
}

// stateTypesCommand describes the node types for an agent that writes the migration state.
type stateTypesCommand struct {
	Types state.TypesConfig `embed:""`
}

func (c *stateTypesCommand) Run(catalog *boundary.Catalog) error {
	return errors.WithStack(state.WriteTypes(os.Stdout, c.Types, terminal.NewPalette(terminal.ColorEnabled(os.Stdout)), catalog))
}

// stateValidateCommand checks a migration state file.
type stateValidateCommand struct {
	State state.Config `embed:""`
}

func (c *stateValidateCommand) Run(catalog *boundary.Catalog) error {
	return errors.WithStack(state.Validate(os.Stdout, c.State, terminal.NewPalette(terminal.ColorEnabled(os.Stdout)), catalog))
}

// statusCommand reports the SPECTRE migration status for a service.
type statusCommand struct {
	Config status.Config `embed:""`
}

func (c *statusCommand) Run(ctx context.Context, registry *status.Registry) error {
	return errors.WithStack(status.Report(ctx, c.Config, registry))
}

// runCommand supervises the candidate and reference behind the embedded proxies.
type runCommand struct {
	Supervisor supervisor.Config `embed:""`
}

func (c *runCommand) Run(ctx context.Context, catalog *boundary.Catalog) error {
	super, err := supervisor.New(ctx, c.Supervisor, catalog)
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
