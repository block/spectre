// Command spectre runs the supervisor with its embedded proxies and generates
// the TypeScript schema declarations used for comparison.
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
	kongtoml "github.com/alecthomas/kong-toml"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/jsonschema"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/supervisor"
)

type cli struct {
	Log     logger.Config    `embed:""`
	Config  kong.ConfigFlag  `placeholder:"FILE" help:"Load flag values from a TOML file. Command-line flags take precedence."`
	Version kong.VersionFlag `help:"Print the version and exit."`

	Run runCommand `cmd:"" help:"Run the supervisor with the embedded proxies, splitting resources between a candidate and a reference."`
	Gen genCommand `cmd:"" help:"Generate TypeScript schema declarations and the script API."`

	// The proxies run as children of the supervisor, re-exec'd through these
	// hidden commands rather than as separate binaries.
	Ingress ingressCommand `cmd:"" name:"__ingress" hidden:"" help:"Run the embedded ingress proxy. Used internally by the supervisor."`
	Egress  egressCommand  `cmd:"" name:"__egress" hidden:"" help:"Run the embedded egress proxy. Used internally by the supervisor."`
}

// runCommand supervises the candidate and reference behind the embedded proxies.
type runCommand struct {
	Supervisor supervisor.Config `embed:""`
}

// ingressCommand runs the embedded ingress proxy.
type ingressCommand struct {
	Ingress httpserver.IngressConfig `embed:""`
	Engine  protocol.Config          `embed:""`
}

// egressCommand runs the embedded egress proxy.
type egressCommand struct {
	Egress httpserver.EgressConfig `embed:""`
	Engine protocol.Config         `embed:""`
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

type jsonSchemaCommand struct {
	JSONSchema jsonschema.Config `embed:""`
	Output     string            `required:"" type:"path" placeholder:"FILE" help:"Path to write the module's .d.ts file to."`
}

type moduleCommand struct {
	Output string `required:"" type:"path" placeholder:"FILE" help:"Path to write the spectre.d.ts module declaration to."`
}

// commandContext carries the runtime wiring shared by the commands.
type commandContext struct {
	ctx context.Context
	log *slog.Logger
}

func main() {
	// A re-exec through the egress sandbox installs the seccomp filter then execs
	// the real command, so it must run before any flag parsing.
	if len(os.Args) > 1 && os.Args[1] == supervisor.SandboxArg {
		if err := supervisor.ExecSandbox(os.Args[2:]); err != nil {
			logger.New(logger.NewConfig(), os.Stderr).Error("Egress sandbox failed", "error", err)
			os.Exit(1)
		}
		return
	}

	command := &cli{}
	kctx := kong.Parse(command, kong.Vars{"version": internal.Version}, kong.Configuration(kongtoml.Loader))
	log := logger.Tagged(logger.New(command.Log, os.Stderr))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)
	kctx.BindTo(ctx, (*context.Context)(nil))
	kctx.FatalIfErrorf(kctx.Run(&commandContext{ctx: ctx, log: log}, command.Log))
}

func (c *runCommand) Run(runtime *commandContext, logConfig logger.Config) error {
	super, err := supervisor.New(c.Supervisor, logConfig, runtime.log, os.Stdout)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(super.Run(runtime.ctx))
}

func (c *ingressCommand) Run(runtime *commandContext) error {
	transport := httpserver.NewTransport(c.Ingress.Config)
	defer transport.CloseIdleConnections()
	loader := Some[httpserver.DescriptorLoader](descriptors.NewReflectionLoader())
	handler, err := httpserver.NewIngress(runtime.ctx, c.Ingress, c.Engine, transport, loader, runtime.log)
	if err != nil {
		return errors.Wrap(err, "configure ingress")
	}
	listener, err := netaddr.ParseListen(c.Ingress.Listen).Listen(runtime.ctx)
	if err != nil {
		return errors.Wrap(err, "listen for HTTP requests")
	}
	return errors.Wrap(handler.Serve(runtime.ctx, listener), "run ingress")
}

func (c *egressCommand) Run(runtime *commandContext) error {
	transport := httpserver.NewTransport(c.Egress.Config)
	defer transport.CloseIdleConnections()
	handler, err := httpserver.NewEgress(runtime.ctx, c.Egress, c.Engine, transport, runtime.log)
	if err != nil {
		return errors.Wrap(err, "configure egress")
	}
	addresses := []string{c.Egress.ReferenceListen, c.Egress.CandidateListen, c.Egress.HealthListen}
	listeners := make([]net.Listener, 0, len(addresses))
	for _, address := range addresses {
		listener, err := netaddr.ParseListen(address).Listen(runtime.ctx)
		if err != nil {
			return errors.Join(errors.Wrapf(err, "listen on %s", address), closeListeners(listeners))
		}
		listeners = append(listeners, listener)
	}
	return errors.Wrap(handler.Serve(runtime.ctx, listeners[0], listeners[1], listeners[2]), "run egress")
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

func (c *jsonSchemaCommand) Run(ctx context.Context) error {
	return errors.Wrap(jsonschema.WriteDeclarations(ctx, c.JSONSchema, c.Output), "write declarations")
}

func (c *moduleCommand) Run() error {
	return errors.Wrap(javascript.WriteModuleDeclaration(c.Output), "write spectre module")
}

func closeListeners(listeners []net.Listener) error { //nolint:mainlogic releases listeners opened during startup
	closeErrors := make([]error, 0, len(listeners))
	for _, listener := range listeners {
		closeErrors = append(closeErrors, listener.Close())
	}
	return errors.Join(closeErrors...)
}
