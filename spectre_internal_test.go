package spectre

import (
	"context"
	"log/slog"
	"testing"

	"github.com/alecthomas/assert/v2"
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
