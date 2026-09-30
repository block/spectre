package comparison

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	comparisoninternal "github.com/block/spectre/internal/comparison/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/route"
	"github.com/block/spectre/internal/schema"
)

// scriptSet binds one direction's endpoints and every normaliser to a schema.
// It shares one immutable program; only plan replacement is synchronized.
type scriptSet struct {
	log       *slog.Logger
	program   *javascript.Program
	direction javascript.Direction
	routes    *route.Map[protoreflect.FullName]

	mu   sync.RWMutex
	plan *comparisoninternal.Plan
}

func newScriptSet(ctx context.Context, config Config, direction javascript.Direction, log *slog.Logger) (*scriptSet, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		return nil, errors.New("logger is required")
	}
	programContext, cancel := context.WithTimeout(ctx, config.ComparisonTimeout)
	defer cancel()
	program, err := javascript.NewProgram(programContext, os.DirFS(config.ScriptsDir))
	if err != nil {
		return nil, errors.Wrap(err, "compile comparison scripts")
	}
	routes := route.New[protoreflect.FullName]()
	for _, endpoint := range program.Endpoints(direction) {
		method := protoreflect.FullName(endpoint.Method())
		if !method.IsValid() {
			return nil, errors.Errorf("endpoint %q has an invalid method name %q", endpoint.Pattern(), endpoint.Method())
		}
		if err := routes.Add(endpoint.Pattern(), method); err != nil {
			return nil, errors.Wrapf(err, "route endpoint %q", endpoint.Pattern())
		}
	}
	return &scriptSet{log: log, program: program, direction: direction, routes: routes}, nil
}

func (s *scriptSet) endpoints() []javascript.Endpoint {
	return s.program.Endpoints(s.direction)
}

// prepare resolves every endpoint and normaliser against a descriptor set, so a
// caller can validate the plan further before activating it.
func (s *scriptSet) prepare(ctx context.Context, set *descriptorpb.FileDescriptorSet) (*comparisoninternal.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(err, "configure comparison scripts")
	}
	loaded, err := schema.NewFromFileDescriptorSet(set)
	if err != nil {
		return nil, errors.Wrap(err, "load comparison schema")
	}
	for _, endpoint := range s.endpoints() {
		if _, err := endpointMethod(loaded, protoreflect.FullName(endpoint.Method())); err != nil {
			return nil, errors.Wrapf(err, "resolve endpoint %q", endpoint.Pattern())
		}
	}
	configured, err := comparisoninternal.NewPlan(loaded, s.program.Fields(), s.program.Messages())
	if err != nil {
		return nil, errors.Wrap(err, "prepare comparison plan")
	}
	return configured, nil
}

func (s *scriptSet) activate(configured *comparisoninternal.Plan) {
	s.mu.Lock()
	s.plan = configured
	s.mu.Unlock()
}

// active returns the current plan, or nil before the schema is ready.
func (s *scriptSet) active() *comparisoninternal.Plan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.plan
}

// resolve prefers declared endpoints, then falls back to the gRPC and Connect
// convention of naming the method in the path.
func (s *scriptSet) resolve(
	loaded *schema.Schema,
	requestMethod, requestHost, requestPath, requestContentType string,
) (method protoreflect.MethodDescriptor, selected protocol, wildcards map[string]string, result Result) {
	name, wildcards, declared := s.routes.Match(requestMethod, requestHost, requestPath)
	selected, result = requestProtocol(requestContentType)
	if declared && selected != protocolGRPC {
		// Declared endpoints serve raw HTTP JSON unless the request is gRPC.
		selected, result = protocolHTTPJSON, newEmptyResult()
	}
	if result.Outcome() != "" {
		return nil, 0, nil, result
	}
	if !declared {
		method, result = conventionalMethod(loaded, requestPath)
		return method, selected, nil, result
	}
	// prepare resolved every endpoint against this schema.
	method, err := endpointMethod(loaded, name)
	if err != nil {
		return nil, 0, nil, Resultf(Unable, "endpoint method is unusable: %v", err)
	}
	return method, selected, wildcards, newEmptyResult()
}

// normalise gives each payload a fresh evaluator, so script state cannot carry
// between payloads and a payload's normalised form depends only on its content.
func (s *scriptSet) normalise(
	ctx context.Context,
	configured *comparisoninternal.Plan,
	root protoreflect.MessageDescriptor,
	runNormalisers bool,
	side string,
	payload any,
) (*comparisoninternal.Normalised, error) {
	evaluator, err := s.program.NewEvaluator(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	normalised, err := configured.Normalise(ctx, s.log, evaluator, root, runNormalisers, side, payload)
	return normalised, errors.Wrap(err, "normalise payload")
}

func endpointMethod(loaded *schema.Schema, name protoreflect.FullName) (protoreflect.MethodDescriptor, error) {
	method, err := loaded.Method(name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if method.IsStreamingClient() || method.IsStreamingServer() {
		return nil, errors.Errorf("endpoint method %q must be unary", name)
	}
	return method, nil
}
