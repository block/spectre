package scaffold

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/terminal"
)

// Prompt asks a person for variable values in a terminal.
type Prompt struct {
	in  *bufio.Reader
	out io.Writer
}

// NewPrompt returns a prompt that reads answers from in and writes questions to out.
func NewPrompt(in io.Reader, out io.Writer) Prompt {
	return Prompt{in: bufio.NewReader(in), out: out}
}

// TerminalPrompt returns a prompt over in and out when in is a terminal, and
// none otherwise, so agents and scripts never block on a question.
func TerminalPrompt(in *os.File, out io.Writer) Option[Prompt] {
	if !terminal.Interactive(in) {
		return None[Prompt]()
	}
	return Some(NewPrompt(in, out))
}

// Ask asks for a variable until the answer is valid. An empty answer takes the
// default, when there is one.
func (p Prompt) Ask(variable Variable, fallback Option[string]) (any, error) {
	hint := variable.Help
	switch {
	case len(variable.Choices) > 0:
		hint += "; one of " + strings.Join(variable.Choices, ", ")
	case variable.Type == List:
		hint += "; comma-separated"
	case variable.Type == Bool:
		hint += "; true or false"
	case variable.Type == String:
	}
	for {
		question := fmt.Sprintf("%s (%s)", variable.Name, hint)
		if fallback, ok := fallback.Get(); ok {
			question += fmt.Sprintf(" [%s]", fallback)
		}
		if _, err := fmt.Fprint(p.out, question+": "); err != nil {
			return nil, errors.WithStack(err)
		}
		line, err := p.in.ReadString('\n')
		if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
			return nil, errors.Wrapf(err, "read %s", variable.Name)
		}
		answer := strings.TrimSpace(line)
		if answer == "" {
			fallback, ok := fallback.Get()
			if !ok {
				continue
			}
			answer = fallback
		}
		value, err := variable.parse(answer)
		if err == nil {
			err = variable.check(value)
		}
		if err == nil {
			return value, nil
		}
		if _, err := fmt.Fprintln(p.out, err); err != nil {
			return nil, errors.WithStack(err)
		}
	}
}
