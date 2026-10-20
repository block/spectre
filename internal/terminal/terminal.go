// Package terminal styles command output for terminals.
package terminal

import (
	"os"
	"strings"
)

// ANSI SGR sequences.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

// Palette styles text with ANSI colors, or leaves it plain when disabled.
type Palette struct {
	enabled bool
}

// NewPalette returns a palette that styles text only when enabled.
func NewPalette(enabled bool) Palette {
	return Palette{enabled: enabled}
}

// ColorEnabled reports whether f is a terminal and NO_COLOR is unset, following
// the https://no-color.org convention.
func ColorEnabled(f *os.File) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok { //nolint:forbidigo // NO_COLOR is a cross-tool convention, not service config.
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Bold styles text as bold.
func (p Palette) Bold(text string) string { return p.paint(bold, text) }

// Dim styles text as dim.
func (p Palette) Dim(text string) string { return p.paint(dim, text) }

// Red colors text red.
func (p Palette) Red(text string) string { return p.paint(red, text) }

// Green colors text green.
func (p Palette) Green(text string) string { return p.paint(green, text) }

// Yellow colors text yellow.
func (p Palette) Yellow(text string) string { return p.paint(yellow, text) }

// Cyan colors text cyan.
func (p Palette) Cyan(text string) string { return p.paint(cyan, text) }

func (p Palette) paint(code, text string) string {
	if !p.enabled {
		return text
	}
	return code + text + reset
}

// Wrap breaks text into lines of at most width columns, each prefixed with
// indent. A word longer than the width gets a line of its own.
func Wrap(text string, indent string, width int) string {
	var wrapped strings.Builder
	line := indent
	for _, word := range strings.Fields(text) {
		if line != indent && len(line)+1+len(word) > width {
			wrapped.WriteString(line + "\n")
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += word
	}
	wrapped.WriteString(line + "\n")
	return wrapped.String()
}
