package scripted

import (
	"context"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
)

// runner runs the statement replacements' callbacks, each in a fresh evaluator.
type runner struct {
	scripts *comparison.Scripts
	handle  Handle
}

func newRunner(scripts *comparison.Scripts, handle Handle) runner {
	return runner{scripts: scripts, handle: handle}
}

// replacements returns the statement replacements that the scripts declared.
func (r runner) replacements() []Replacement {
	return r.handle.Declared(r.scripts.Program()).Replacements
}

// run calls function with the callbacks of a fresh evaluator.
func (r runner) run(ctx context.Context, function func(callbacks Callbacks) error) error {
	return errors.WithStack(r.scripts.Evaluate(ctx, func(evaluator *javascript.Evaluator) error {
		return function(r.handle.Instance(evaluator))
	}))
}
