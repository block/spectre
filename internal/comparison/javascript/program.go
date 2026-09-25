// Package javascript evaluates Spectre response comparator scripts.
package javascript

import (
	"context"
	"slices"

	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"
)

const spectreModuleName = "spectre"

// Program is an immutable script shared by all response comparisons.
// It retains no runtime-local callback state.
type Program struct {
	entry    *sobek.SourceTextModuleRecord
	spectre  *spectreModule
	fields   []string
	messages []string
	rpcs     []string
}

// NewProgram parses and links a script, then evaluates it once to validate registrations.
func NewProgram(ctx context.Context, name, source string) (*Program, error) {
	spectre := &spectreModule{}
	resolve := func(_ any, specifier string) (sobek.ModuleRecord, error) {
		if specifier == spectreModuleName {
			return spectre, nil
		}
		return nil, errors.Errorf("unsupported comparison module import %q", specifier)
	}
	entry, err := sobek.ParseModule(name, source, resolve)
	if err != nil {
		return nil, errors.Wrap(err, "parse comparison module")
	}
	if err := entry.Link(); err != nil {
		return nil, errors.Wrap(err, "link comparison module")
	}
	evaluator, fields, messages, rpcs, err := newEvaluator(ctx, entry, spectre)
	if err != nil {
		return nil, errors.Wrap(err, "evaluate comparison module")
	}
	defer evaluator.Close()
	return &Program{
		entry:    entry,
		spectre:  spectre,
		fields:   fields,
		messages: messages,
		rpcs:     rpcs,
	}, nil
}

// Fields returns the protobuf fields declared by the script.
func (p *Program) Fields() []string {
	return append([]string(nil), p.fields...)
}

// Messages returns the protobuf messages declared by the script.
func (p *Program) Messages() []string {
	return append([]string(nil), p.messages...)
}

// RPCs returns the RPC methods declared by the script.
func (p *Program) RPCs() []string {
	return append([]string(nil), p.rpcs...)
}

// NewEvaluator creates an isolated evaluator for one response comparison.
// Re-evaluation must reproduce the registrations used to build the schema plan.
func (p *Program) NewEvaluator(ctx context.Context) (*Evaluator, error) {
	evaluator, fields, messages, rpcs, err := newEvaluator(ctx, p.entry, p.spectre)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(fields, p.fields) || !slices.Equal(messages, p.messages) || !slices.Equal(rpcs, p.rpcs) {
		evaluator.Close()
		return nil, errors.New("comparison script registrations changed after startup")
	}
	return evaluator, nil
}
