package comparison

import (
	"context"
	"crypto/sha256"
	"os"
	"slices"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	comparisoninternal "github.com/block/spectre/internal/comparison/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// Scripts is one immutable set of normaliser scripts and the schema they were
// checked against. It is safe for concurrent use.
type Scripts struct {
	program *javascript.Program
	plan    *comparisoninternal.Plan
	timeout time.Duration
}

// New loads every script in the configured directory, type-checked against the bindings'
// script API, and checks that each declared endpoint type exists.
func New(ctx context.Context, config Config, bindings []javascript.Binding) (*Scripts, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(config.ScriptsDir)
	if err != nil {
		return nil, errors.Wrap(err, "open scripts directory")
	}
	defer root.Close() //nolint:errcheck // Module source is fully loaded before closing.
	program, err := javascript.NewProgram(ctx, config.ComparisonTimeout, os.DirFS(config.Schema.SchemaDir), root.FS(), config.Variables, bindings)
	if err != nil {
		return nil, errors.Wrap(err, "compile comparison scripts")
	}
	loaded := program.Schema()
	scripts := &Scripts{program: program, timeout: config.ComparisonTimeout}
	for _, endpoint := range scripts.Endpoints() {
		if _, err := loaded.Type(endpoint.Type()); err != nil {
			return nil, errors.Wrapf(err, "resolve endpoint %q", endpoint.Pattern())
		}
	}
	if err := checkReplacements(scripts.program.Replacements(), scripts.Endpoints()); err != nil {
		return nil, err
	}
	scripts.plan, err = comparisoninternal.NewPlan(loaded, program.Fields(), program.Messages())
	if err != nil {
		return nil, errors.Wrap(err, "prepare comparison plan")
	}
	return scripts, nil
}

// Schema returns the payload types the scripts were checked against.
func (s *Scripts) Schema() *schema.Schema {
	return s.program.Schema()
}

// Program returns what the scripts declared, which a binding's Handle reads its declarations from.
func (s *Scripts) Program() *javascript.Program {
	return s.program
}

// Evaluate runs function in a fresh evaluator. The comparison timeout limits the evaluator's start,
// and the time that bindings' callbacks run for, so function may wait without being interrupted.
func (s *Scripts) Evaluate(ctx context.Context, function func(evaluator *javascript.Evaluator) error) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, "initialise JavaScript evaluator")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limit := time.AfterFunc(s.timeout, cancel)
	evaluator, err := s.program.NewEvaluator(ctx)
	if !limit.Stop() && err == nil {
		evaluator.Close()
		err = context.DeadlineExceeded
	}
	if err != nil {
		return errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	return function(evaluator)
}

// Endpoints returns every declared endpoint, ingress first, in declaration order.
func (s *Scripts) Endpoints() []javascript.Endpoint {
	return append(s.program.Endpoints(model.Ingress), s.program.Endpoints(model.Egress)...)
}

// Replacements returns the egress replacements in declaration order.
func (s *Scripts) Replacements() []javascript.Replacement {
	return s.program.Replacements()
}

// checkReplacements requires each source to be an endpoint that pairs its own
// requests, and the replacements of one source to decode its responses alike.
func checkReplacements(replacements []javascript.Replacement, endpoints []javascript.Endpoint) error {
	responses := map[string]schema.TypeRef{}
	for _, replacement := range replacements {
		index := slices.IndexFunc(endpoints, func(endpoint javascript.Endpoint) bool {
			return endpoint.Direction() == model.Egress && endpoint.Pattern() == replacement.Source()
		})
		if index < 0 {
			return errors.Errorf("%s: source endpoint is not declared", replacement)
		}
		if slices.ContainsFunc(replacements, func(other javascript.Replacement) bool { return other.Pattern() == replacement.Source() }) {
			return errors.Errorf("%s: source endpoint itself replaces another", replacement)
		}
		if declared := endpoints[index].Type(); declared != replacement.SourceRequest() {
			return errors.Errorf("%s: source requests are %s, not %s", replacement, declared, replacement.SourceRequest())
		}
		if response, seen := responses[replacement.Source()]; seen && response != replacement.SourceResponse() {
			return errors.Errorf("%s: source responses are decoded as both %s and %s", replacement, response, replacement.SourceResponse())
		}
		responses[replacement.Source()] = replacement.SourceResponse()
	}
	return nil
}

// ReplaceRequest maps a request to a replacing endpoint to the source request it pairs with.
func (s *Scripts) ReplaceRequest(ctx context.Context, replacement javascript.Replacement, request schema.Value) (schema.Value, error) {
	return s.replace(ctx, replacement.SourceRequest(), func(evaluator *javascript.Evaluator) (schema.Value, error) {
		return errors.WithStack2(evaluator.ReplaceRequest(replacement, request))
	})
}

// ReplaceResponse builds the answer to a request to a replacing endpoint from a
// reference response to its source.
func (s *Scripts) ReplaceResponse(ctx context.Context, replacement javascript.Replacement, request, reference schema.Value) (schema.Value, error) {
	expected, err := s.Schema().Type(replacement.SourceResponse())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if !reference.Type().Equal(expected) {
		return nil, errors.Errorf("source reference response is %s, not %s", reference.Type(), expected)
	}
	return s.replace(ctx, replacement.Response(), func(evaluator *javascript.Evaluator) (schema.Value, error) {
		return errors.WithStack2(evaluator.ReplaceResponse(replacement, request, reference))
	})
}

// replace runs one replacement function in a fresh evaluator and validates its result as typeName.
func (s *Scripts) replace(ctx context.Context, typeName schema.TypeRef, function func(*javascript.Evaluator) (schema.Value, error)) (schema.Value, error) {
	declared, err := s.Schema().Type(typeName)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	evaluator, err := s.program.NewEvaluator(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	translated, err := function(evaluator)
	if err != nil {
		return nil, err
	}
	checked, err := schema.As(declared, translated)
	if err != nil {
		return nil, errors.Wrapf(err, "validate translated %s", typeName)
	}
	return checked, nil
}

// Compare normalises both payloads and returns where they differ, and whether they
// differ in more places. Both must be instances of the same type.
func (s *Scripts) Compare(ctx context.Context, reference, candidate schema.Value) (differences []model.Difference, truncated bool, err error) {
	if !reference.Type().Equal(candidate.Type()) {
		return nil, false, errors.Errorf("reference is %s but candidate is %s", reference.Type(), candidate.Type())
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	normalisedReference, err := s.normalise(ctx, model.Reference, reference)
	if err != nil {
		return nil, false, errors.Wrap(err, "normalise reference")
	}
	normalisedCandidate, err := s.normalise(ctx, model.Candidate, candidate)
	if err != nil {
		return nil, false, errors.Wrap(err, "normalise candidate")
	}
	differences, truncated = comparisoninternal.Diff(normalisedReference, normalisedCandidate)
	return differences, truncated, nil
}

// Hash returns SHA-256 over identity and the canonical JSON of the normalised
// payload. side only labels log records.
func (s *Scripts) Hash(ctx context.Context, identity string, side model.Side, payload schema.Value) (Hash, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	normalised, err := s.normalise(ctx, side, payload)
	if err != nil {
		return Hash{}, errors.Wrap(err, "normalise payload")
	}
	canonical, err := normalised.CanonicalJSON()
	if err != nil {
		return Hash{}, errors.WithStack(err)
	}
	// Identities contain no zero bytes, separating identity from JSON.
	hashed := make([]byte, 0, len(identity)+1+len(canonical))
	hashed = append(hashed, identity...)
	hashed = append(hashed, 0)
	hashed = append(hashed, canonical...)
	return sha256.Sum256(hashed), nil
}

// normalise gives each object payload a fresh evaluator, so script state cannot carry
// between payloads. Normalisers apply only to object types; others compare as they are.
func (s *Scripts) normalise(ctx context.Context, side model.Side, payload schema.Value) (*comparisoninternal.Normalised, error) {
	root, isObject := schema.NonNull(payload.Type()).(*schema.ObjectType)
	if !isObject {
		return comparisoninternal.Raw(payload), nil
	}
	evaluator := None[*javascript.Evaluator]()
	defer func() {
		if created, ok := evaluator.Get(); ok {
			created.Close()
		}
	}()
	// Most payloads match no normaliser, so the evaluator starts only once one does.
	start := func() (*javascript.Evaluator, error) {
		if created, ok := evaluator.Get(); ok {
			return created, nil
		}
		created, err := s.program.NewEvaluator(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "initialise JavaScript evaluator")
		}
		evaluator = Some(created)
		return created, nil
	}
	return errors.WithStack2(s.plan.Normalise(ctx, start, root, side, payload))
}
