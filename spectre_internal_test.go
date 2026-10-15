package spectre

import (
	"context"
	"log/slog"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
)

func TestWithCommandAddsCommand(t *testing.T) {
	extension := &imageCommand{}
	command := &cli{}
	parser, err := newParser(command, WithCommand("image", "Build an image.", extension))
	assert.NoError(t, err)

	kctx, err := parser.Parse([]string{"image", "--tag=spectre:dev"})
	assert.NoError(t, err)
	kctx.BindTo(t.Context(), (*context.Context)(nil))
	log := slog.New(slog.DiscardHandler)
	kctx.Bind(log)
	assert.NoError(t, kctx.Run(command.Log))

	assert.Equal(t, &imageCommand{Tag: "spectre:dev", log: log}, extension)
}

func TestWithCommandKeepsBuiltInCommands(t *testing.T) {
	parser, err := newParser(&cli{}, WithCommand("image", "Build an image.", &imageCommand{}))
	assert.NoError(t, err)

	kctx, err := parser.Parse([]string{"gen", "module", "--output=spectre.d.ts"})
	assert.NoError(t, err)

	assert.Equal(t, "gen module", kctx.Command())
}

func TestWithKongOptionsAddsVars(t *testing.T) {
	extension := &pushCommand{}
	parser, err := newParser(&cli{},
		WithCommand("push", "Push an image.", extension),
		WithKongOptions(kong.Vars{"registry": "registry.example.com"}),
	)
	assert.NoError(t, err)

	_, err = parser.Parse([]string{"push"})
	assert.NoError(t, err)

	assert.Equal(t, &pushCommand{Image: "registry.example.com/spectre:" + internal.Version}, extension)
}

// pushCommand's default needs both a variable from WithKongOptions and the
// built-in version variable.
type pushCommand struct {
	Image string `help:"Image to push." default:"${registry}/spectre:${version}"`
}

func (c *pushCommand) Run(_ context.Context, _ *slog.Logger) error { return nil }

// imageCommand records the logger it ran with. Kong fails to call Run unless
// both arguments are bound.
type imageCommand struct {
	Tag string `help:"Image tag."`

	log *slog.Logger
}

func (c *imageCommand) Run(_ context.Context, log *slog.Logger) error {
	c.log = log
	return nil
}
