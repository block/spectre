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
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/state"
	"github.com/block/spectre/internal/supervisor"
	"github.com/block/spectre/internal/terminal"
)

// cli is the built-in command line. Extensions add to it, so custom binaries
// never copy it.
type cli struct {
	Log    logger.Config   `embed:"" prefix:"log-"`
	Config kong.ConfigFlag `placeholder:"FILE" help:"Load flag values from a TOML file. Command-line flags take precedence."`
	// A deployment's own SPECTRE_VERSION must not make every command exit.
	Version kong.VersionFlag `env:"-" help:"Print the version and exit."`

	Supervise   superviseCommand   `cmd:"" help:"Supervise a candidate and a reference behind the embedded proxies, splitting resources between them."`
	Serve       serveCommand       `cmd:"" help:"Run the SPECTRE server, which mirrors report their quarantines to."`
	Gen         genCommand         `cmd:"" help:"Generate TypeScript schema declarations and the script API."`
	Divergences divergencesCommand `cmd:"" help:"List or clear the divergences that mirrors reported to the SPECTRE server."`
	State       stateCommand       `cmd:"" help:"Describe a service's migration state."`

	Init initCommand `cmd:"" help:"Set a service checkout up for a SPECTRE migration from a template."`
}

// initCommand renders a template into a service checkout.
type initCommand struct {
	Init scaffold.Config `embed:""`
}

func (c *initCommand) Run(ctx context.Context) error {
	return errors.WithStack(scaffold.Init(ctx, c.Init))
}

// stateCommand groups the commands that read a service's migration state.
type stateCommand struct { //nolint:singleusestruct // A Kong command group; folding it would flatten its subcommands.
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

// divergencesCommand groups the commands for the divergences mirrors reported.
type divergencesCommand struct {
	List  divergencesListCommand  `cmd:"" default:"withargs" help:"List the divergences that mirrors reported. This is the default."`
	Clear divergencesClearCommand `cmd:"" help:"Clear the divergences of a service environment once they are dealt with."`
}

// divergencesListCommand lists the divergences mirrors reported.
type divergencesListCommand struct {
	Divergences server.DivergencesConfig `embed:""`
}

func (c *divergencesListCommand) Run(ctx context.Context) error {
	return errors.WithStack(server.WriteDivergences(ctx, os.Stdout, c.Divergences, terminal.NewPalette(terminal.ColorEnabled(os.Stdout))))
}

// divergencesClearCommand clears the divergences of a service environment.
type divergencesClearCommand struct {
	Clear server.ClearConfig `embed:""`
}

func (c *divergencesClearCommand) Run(ctx context.Context) error {
	return errors.WithStack(server.ClearDivergences(ctx, c.Clear))
}

// serveCommand runs the SPECTRE server.
type serveCommand struct {
	Serve server.ServeConfig `embed:""`
}

func (c *serveCommand) Run(ctx context.Context) error {
	return errors.WithStack(server.Serve(ctx, c.Serve))
}

// superviseCommand supervises the candidate and reference behind the embedded proxies.
type superviseCommand struct {
	Supervisor supervisor.Config `embed:""`
}

func (c *superviseCommand) Run(ctx context.Context, catalog *boundary.Catalog) error {
	super, err := supervisor.New(ctx, c.Supervisor, catalog)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(super.Run(ctx))
}

// genCommand groups the schema declaration generators.
type genCommand struct { //nolint:singleusestruct // A Kong command group; folding it would flatten its subcommands.
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
