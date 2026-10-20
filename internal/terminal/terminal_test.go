package terminal_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/terminal"
)

func TestWrap(t *testing.T) {
	for name, test := range map[string]struct {
		text     string
		expected string
	}{
		"Short":    {text: "one two", expected: "  one two\n"},
		"Breaks":   {text: "one two three four", expected: "  one two\n  three\n  four\n"},
		"LongWord": {text: "one abcdefghijkl two", expected: "  one\n  abcdefghijkl\n  two\n"},
		"Spaces":   {text: "  one   two  ", expected: "  one two\n"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.expected, terminal.Wrap(test.text, "  ", 11))
		})
	}
}

func TestPalette(t *testing.T) {
	assert.Equal(t, "text", terminal.NewPalette(false).Bold("text"))
	assert.Equal(t, "\033[1mtext\033[0m", terminal.NewPalette(true).Bold("text"))
}
