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
	program, err := javascript.NewProgram(ctx, config.ComparisonTimeout, os.DirFS(config.Schema.SchemaDir), root.FS(), config.Variables)
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

// Endpoints returns every declared endpoint, ingress first, in declaration order.
func (s *Scripts) Endpoints() []javascript.Endpoint {
	return append(s.program.Endpoints(model.Ingress), s.program.Endpoints(model.Egress)...)
}

// Rules returns the ingress rules in declaration order.
func (s *Scripts) Rules() []javascript.Rule {
	return s.program.Rules()
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

// StatementReplacements returns the MySQL statement replacements in declaration order.
func (s *Scripts) StatementReplacements() []javascript.StatementReplacement {
	return s.program.StatementReplacements()
}

// CombineStatements answers a candidate statement from the reference statements it claims.
// Waiting in claims does not count against the comparison timeout.
func (s *Scripts) CombineStatements(ctx context.Context, replacement javascript.StatementReplacement, values []Option[string],
	claims []javascript.Claim,
) ([]javascript.Row, error) {
	evaluator, closer, err := s.statementEvaluator(ctx)
	if err != nil {
		return nil, err
	}
	defer closer()
	return errors.WithStack2(evaluator.CombineStatements(s.timeout, replacement, values, claims))
}

// SplitStatement offers the replies that a reference statement's reply splits into.
func (s *Scripts) SplitStatement(ctx context.Context, replacement javascript.StatementReplacement, values []Option[string],
	rows []javascript.Row, offer javascript.Offer,
) error {
	evaluator, closer, err := s.statementEvaluator(ctx)
	if err != nil {
		return err
	}
	defer closer()
	return errors.WithStack(evaluator.SplitStatement(s.timeout, replacement, values, rows, offer))
}

// statementEvaluator starts an evaluator within the comparison timeout, and returns it
// with what closes it. Only a slow start ends its context, so claims are never interrupted.
func (s *Scripts) statementEvaluator(ctx context.Context) (evaluator *javascript.Evaluator, closer func(), err error) {
	ctx, cancel := context.WithCancel(ctx)
	limit := time.AfterFunc(s.timeout, cancel)
	evaluator, err = s.program.NewEvaluator(ctx)
	if !limit.Stop() && err == nil {
		evaluator.Close()
		err = context.DeadlineExceeded
	}
	if err != nil {
		cancel()
		return nil, nil, errors.Wrap(err, "initialise JavaScript evaluator")
	}
	return evaluator, func() {
		evaluator.Close()
		cancel()
	}, nil
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
	requestHeader := func(evaluator *javascript.Evaluator, name string, value Option[string]) (bool, error) {
		return errors.WithStack2(evaluator.SelectsRequestHeader(rule, name, value))
	}
	if selected, err := selectsHeaders(rule, rule.RequestHeaders(), request.HeaderValue, requestHeader, script); err != nil || !selected {
		return false, err
	}
	if selected, err := selectsStatus(rule, response.Status, script); err != nil || !selected {
		return false, err
	}
	responseHeader := func(evaluator *javascript.Evaluator, name string, value Option[string]) (bool, error) {
		return errors.WithStack2(evaluator.SelectsResponseHeader(rule, name, value))
	}
	return selectsHeaders(rule, rule.ResponseHeaders(), response.HeaderValue, responseHeader, script)
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

// selectsHeaders reports whether a message satisfies every header selector.
// lookup reads the message's joined header values, and evaluate runs a function.
func selectsHeaders(
	rule javascript.Rule,
	headers []javascript.HeaderSelector,
	lookup func(name string) Option[string],
	evaluate func(evaluator *javascript.Evaluator, name string, value Option[string]) (bool, error),
	script func() (*javascript.Evaluator, error),
) (bool, error) {
	for _, header := range headers {
		switch selector := header.(type) {
		case javascript.HeaderValue:
			if !selector.Selects(lookup(selector.Name)) {
				return false, nil
			}
		case javascript.HeaderScript:
			evaluator, err := script()
			if err != nil {
				return false, err
			}
			if selected, err := evaluate(evaluator, selector.Name, lookup(selector.Name)); err != nil || !selected {
				return false, err
			}
		default:
			return false, errors.Errorf("rule %s has an unknown header selector", rule)
		}
	}
	return true, nil
}

// NormaliseResponse applies a rule's normaliser to one response. A None status
// or body is not compared, and a returned body must still be the rule's type.
func (s *Scripts) NormaliseResponse(ctx context.Context, rule javascript.Rule, status int, body Option[schema.Value]) (Option[int], Option[schema.Value], error) {
	match, ok := rule.Action().(javascript.ActionMatch)
	if !ok || !match.Normalises {
		return Some(status), body, nil
	}
	code, err := schema.NewNumber(float64(status))
	if err != nil {
		return None[int](), None[schema.Value](), errors.WithStack(err)
	}
	members := map[string]schema.Value{"status": code}
	if value, present := body.Get(); present {
		members["body"] = value
	}
	argument, err := schema.NewObject(members)
	if err != nil {
		return None[int](), None[schema.Value](), errors.WithStack(err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	evaluator, err := s.program.NewEvaluator(ctx)
	if err != nil {
		return None[int](), None[schema.Value](), errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	result, err := evaluator.NormaliseResponse(rule, argument)
	if err != nil {
		return None[int](), None[schema.Value](), errors.WithStack(err)
	}
	return normalisedResponse(match, result)
}

// normalisedResponse checks a normaliser's result: an object with at most a
// status and a body of the rule's type.
func normalisedResponse(match javascript.ActionMatch, result Option[schema.Value]) (Option[int], Option[schema.Value], error) {
	value, _ := result.Get()
	response, ok := value.(schema.ObjectValue)
	if !ok {
		return None[int](), None[schema.Value](), errors.New("response normaliser must return an object")
	}
	status := None[int]()
	body := None[schema.Value]()
	for _, key := range response.Keys() {
		member, _ := response.Member(key)
		switch key {
		case "status":
			number, isNumber := member.(schema.NumberValue)
			code := number.Float()
			if !isNumber || code != math.Trunc(code) || code < 100 || code > 599 {
				return None[int](), None[schema.Value](), errors.New("normalised status must be an integer from 100 to 599")
			}
			status = Some(int(code))
		case "body":
			body = Some(member)
		default:
			return None[int](), None[schema.Value](), errors.Errorf("normalised response has an unknown member %q", key)
		}
	}
	member, present := body.Get()
	if !present {
		return status, None[schema.Value](), nil
	}
	checked, err := schema.As(match.Type, member)
	if err != nil {
		return None[int](), None[schema.Value](), errors.Wrap(err, "validate normalised body")
	}
	return status, Some(checked), nil
}

// Compare normalises both payloads and returns where they differ, and whether they
// differ in more places. Both must be instances of the same type.
func (s *Scripts) Compare(ctx context.Context, reference, candidate schema.Value) (differences []model.Difference, truncated bool, err error) {
	if !reference.Type().Equal(candidate.Type()) {
		return nil, false, errors.Errorf("reference is %s but candidate is %s", reference.Type(), candidate.Type())
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	normalisedReference, err := s.normalise(ctx, "reference", reference)
	if err != nil {
		return nil, false, errors.Wrap(err, "normalise reference")
	}
	normalisedCandidate, err := s.normalise(ctx, "candidate", candidate)
	if err != nil {
		return nil, false, errors.Wrap(err, "normalise candidate")
	}
	differences, truncated = comparisoninternal.Diff(normalisedReference, normalisedCandidate)
	return differences, truncated, nil
}

// Hash returns SHA-256 over identity and the canonical JSON of the normalised
// payload. side only labels log records.
func (s *Scripts) Hash(ctx context.Context, identity string, side string, payload schema.Value) (Hash, error) {
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
func (s *Scripts) normalise(ctx context.Context, side string, payload schema.Value) (*comparisoninternal.Normalised, error) {
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
