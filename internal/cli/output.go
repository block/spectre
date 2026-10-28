package cli

import (
	"io"
	"os"

	"github.com/block/spectre/internal/cli/terminal"
)

// Output is where a command writes, with the palette that suits it.
type Output struct {
	Writer  io.Writer
	Palette terminal.Palette
}

// NewOutput writes to file, styling text only when file is a colour terminal.
func NewOutput(file *os.File) Output {
	return Output{Writer: file, Palette: terminal.NewPalette(terminal.ColorEnabled(file))}
}
