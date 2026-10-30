package comparison

import (
	"context"
	"crypto/sha256"
	"math"
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

// New loads every script in the configured directory and checks that each
// declared endpoint type exists.
func New(ctx context.Context, config Config) (*Scripts, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(config.ScriptsDir)
	if err != nil {
		return nil, errors.Wrap(err, "open scripts directory")
	}
	defer root.Close() //nolint:errcheck // Module source is fully loaded before closing.
	program, err := javascript.NewProgram(ctx, config.ComparisonTimeout, os.DirFS(config.Schema.SchemaDir), root.FS())
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
	for _, rule := range scripts.Rules() {
		if match, ok := rule.Action().(javascript.ActionMatch); ok {
			if name, typed := match.Type.Get(); typed {
				if _, err := loaded.Type(name); err != nil {
					return nil, errors.Wrapf(err, "resolve rule %s", rule)
				}
			}
		}
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

// Endpoints returns every declared endpoint, ingress first, in declaration order.
func (s *Scripts) Endpoints() []javascript.Endpoint {
	return append(s.program.Endpoints(model.Ingress), s.program.Endpoints(model.Egress)...)
}

// Rules returns the ingress rules in declaration order.
func (s *Scripts) Rules() []javascript.Rule {
	return s.program.Rules()
}

// SelectRule returns the first rule that selects a response. rules must be in
// declaration order, and the request must match each rule's pattern, if it has one.
func (s *Scripts) SelectRule(ctx context.Context, rules []javascript.Rule, request javascript.Request, response javascript.Response) (Option[javascript.Rule], error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	evaluator := None[*javascript.Evaluator]()
	defer func() {
		if created, ok := evaluator.Get(); ok {
			created.Close()
		}
	}()
	// Functions share one evaluator, created only once a rule needs it.
	script := func() (*javascript.Evaluator, error) {
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
	for _, rule := range rules {
		selected, err := selects(rule, request, response, script)
		if err != nil {
			return None[javascript.Rule](), err
		}
		if selected {
			return Some(rule), nil
		}
	}
	return None[javascript.Rule](), nil
}

func selects(rule javascript.Rule, request javascript.Request, response javascript.Response, script func() (*javascript.Evaluator, error)) (bool, error) {
	if _, scripted := rule.Request().(javascript.RequestScript); scripted {
		evaluator, err := script()
		if err != nil {
			return false, err
		}
		if selected, err := evaluator.SelectsRequest(rule, request); err != nil || !selected {
			return false, errors.WithStack(err)
		}
	}
	if selected, err := selectsStatus(rule, response.Status, script); err != nil || !selected {
		return false, err
	}
	for _, header := range rule.Headers() {
		if selected, err := selectsHeader(rule, header, response, script); err != nil || !selected {
			return false, err
		}
	}
	return true, nil
}

func selectsStatus(rule javascript.Rule, status int, script func() (*javascript.Evaluator, error)) (bool, error) {
	switch selector := rule.Status().(type) {
	case javascript.StatusAny:
		return true, nil
	case javascript.StatusCodes:
		return slices.Contains(selector.Codes, status), nil
	case javascript.StatusScript:
		evaluator, err := script()
		if err != nil {
			return false, err
		}
		return errors.WithStack2(evaluator.SelectsStatus(rule, status))
	}
	return false, errors.Errorf("rule %s has an unknown status selector", rule)
}

func selectsHeader(rule javascript.Rule, header javascript.HeaderSelector, response javascript.Response, script func() (*javascript.Evaluator, error)) (bool, error) {
	switch selector := header.(type) {
	case javascript.HeaderValue:
		value, present := response.HeaderValue(selector.Name).Get()
		return present && value == selector.Value, nil
	case javascript.HeaderScript:
		evaluator, err := script()
		if err != nil {
			return false, err
		}
		return errors.WithStack2(evaluator.SelectsHeader(rule, selector.Name, response.HeaderValue(selector.Name)))
	}
	return false, errors.Errorf("rule %s has an unknown header selector", rule)
}

// NormaliseResponse applies a rule's normaliser to one response. A None status
// or body is not compared, and a returned body must still be the rule's type.
func (s *Scripts) NormaliseResponse(ctx context.Context, rule javascript.Rule, status int, body Option[any]) (Option[int], Option[any], error) {
	match, ok := rule.Action().(javascript.ActionMatch)
	if !ok || !match.Normalises {
		return Some(status), body, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	evaluator, err := s.program.NewEvaluator(ctx)
	if err != nil {
		return None[int](), None[any](), errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	argument := map[string]any{"status": status}
	if value, present := body.Get(); present {
		argument["body"] = value
	}
	result, err := evaluator.NormaliseResponse(rule, argument)
	if err != nil {
		return None[int](), None[any](), errors.WithStack(err)
	}
	return s.normalisedResponse(match, result)
}

// normalisedResponse checks a normaliser's result: an object with at most a
// status and a body of the rule's type.
func (s *Scripts) normalisedResponse(match javascript.ActionMatch, result Option[any]) (Option[int], Option[any], error) {
	value, _ := result.Get()
	response, ok := value.(map[string]any)
	if !ok {
		return None[int](), None[any](), errors.New("response normaliser must return an object")
	}
	status := None[int]()
	body := None[any]()
	for key, member := range response {
		switch key {
		case "status":
			code, number := member.(float64)
			if !number || code != math.Trunc(code) || code < 100 || code > 599 {
				return None[int](), None[any](), errors.New("normalised status must be an integer from 100 to 599")
			}
			status = Some(int(code))
		case "body":
			body = Some(member)
		default:
			return None[int](), None[any](), errors.Errorf("normalised response has an unknown member %q", key)
		}
	}
	value, present := body.Get()
	if !present {
		return status, body, nil
	}
	name, typed := match.Type.Get()
	if !typed {
		if _, text := value.(string); !text {
			return None[int](), None[any](), errors.New("normalised Text body must be a string")
		}
		return status, body, nil
	}
	if err := s.plan.Schema().Validate(name, value); err != nil {
		return None[int](), None[any](), errors.Wrap(err, "validate normalised body")
	}
	return status, body, nil
}

// Compare normalises both payloads as typeName and returns the paths at which
// they differ. Untyped payloads are compared without normalisers.
func (s *Scripts) Compare(ctx context.Context, typeName Option[schema.TypeRef], reference, candidate any) (differences []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	normalisedReference, err := s.normalise(ctx, typeName, "reference", reference)
	if err != nil {
		return nil, errors.Wrap(err, "normalise reference")
	}
	normalisedCandidate, err := s.normalise(ctx, typeName, "candidate", candidate)
	if err != nil {
		return nil, errors.Wrap(err, "normalise candidate")
	}
	return comparisoninternal.Diff(normalisedReference, normalisedCandidate), nil
}

// Hash returns SHA-256 over identity and the canonical JSON of the normalised
// payload. side only labels log records.
func (s *Scripts) Hash(ctx context.Context, identity string, typeName Option[schema.TypeRef], side string, payload any) (Hash, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	normalised, err := s.normalise(ctx, typeName, side, payload)
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

// normalise gives each payload a fresh evaluator, so script state cannot carry
// between payloads and a payload's normalised form depends only on its content.
func (s *Scripts) normalise(ctx context.Context, typeName Option[schema.TypeRef], side string, payload any) (*comparisoninternal.Normalised, error) {
	name, typed := typeName.Get()
	if !typed {
		return comparisoninternal.Raw(payload), nil
	}
	root, err := s.plan.Schema().Type(name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	evaluator, err := s.program.NewEvaluator(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	return errors.WithStack2(s.plan.Normalise(ctx, evaluator, root, side, payload))
}
