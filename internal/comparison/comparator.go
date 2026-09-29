package comparison

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	comparisoninternal "github.com/block/spectre/internal/comparison/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/route"
	"github.com/block/spectre/internal/schema"
)

// Comparator shares one immutable script program across response comparisons.
// Only schema-plan replacement is synchronized; each payload owns its evaluator.
type Comparator struct {
	log              *slog.Logger
	program          *javascript.Program
	routes           *route.Map[protoreflect.FullName]
	timeout          time.Duration
	maxResponseBytes int

	mu   sync.RWMutex
	plan *comparisoninternal.Plan
}

// New constructs a response comparator from its parsed configuration.
func New(ctx context.Context, config Config, log *slog.Logger) (*Comparator, error) {
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
	for _, endpoint := range program.Endpoints(javascript.Ingress) {
		method := protoreflect.FullName(endpoint.Method())
		if !method.IsValid() {
			return nil, errors.Errorf("endpoint %q has an invalid method name %q", endpoint.Pattern(), endpoint.Method())
		}
		if err := routes.Add(endpoint.Pattern(), method); err != nil {
			return nil, errors.Wrapf(err, "route endpoint %q", endpoint.Pattern())
		}
	}
	return &Comparator{
		log:              log,
		program:          program,
		routes:           routes,
		timeout:          config.ComparisonTimeout,
		maxResponseBytes: config.ComparisonMaxResponseBytes,
	}, nil
}

// MaxResponseBytes returns the per-backend response capture limit.
func (c *Comparator) MaxResponseBytes() int {
	return c.maxResponseBytes
}

// Configure activates the comparator against a descriptor set.
func (c *Comparator) Configure(ctx context.Context, set *descriptorpb.FileDescriptorSet) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, "configure comparator")
	}
	loaded, err := schema.NewFromFileDescriptorSet(set)
	if err != nil {
		return errors.Wrap(err, "load comparison schema")
	}
	for _, endpoint := range c.program.Endpoints(javascript.Ingress) {
		if _, err := endpointMethod(loaded, protoreflect.FullName(endpoint.Method())); err != nil {
			return errors.Wrapf(err, "resolve endpoint %q", endpoint.Pattern())
		}
	}
	configured, err := comparisoninternal.NewPlan(loaded, c.program.Fields(), c.program.Messages())
	if err != nil {
		return errors.Wrap(err, "prepare comparison plan")
	}
	c.mu.Lock()
	c.plan = configured
	c.mu.Unlock()
	return nil
}

// Compare compares one response pair without exposing response values in its result.
// requestPath must be escaped, as endpoints match it the way ServeMux does.
func (c *Comparator) Compare(
	ctx context.Context,
	requestMethod string,
	requestPath string,
	requestContentType string,
	reference Response,
	candidate Response,
) (result Result) {
	defer func() {
		attributes := []any{"path", requestPath, "outcome", result.Outcome()}
		differences := result.Differences()
		if len(differences) > 0 {
			attributes = append(attributes, "differences", differences)
		}
		if result.Reason() != "" {
			attributes = append(attributes, "reason", result.Reason())
		}
		c.log.DebugContext(ctx, "Response comparison completed", attributes...)
	}()
	if excludedRequestPath(requestPath) {
		return Resultf(Skipped, "gRPC namespace is excluded from response comparison")
	}
	c.mu.RLock()
	configured := c.plan
	c.mu.RUnlock()
	if configured == nil {
		return Resultf(Skipped, "comparison schema is not ready")
	}
	if reference.Overflow || candidate.Overflow {
		return Resultf(Unable, "response exceeds the comparison size limit")
	}
	if requestContentType == "" && hasJSONMediaType(reference.Header) {
		requestContentType = jsonMediaType
	}
	method, protocol, result := c.resolve(configured.Schema(), requestMethod, requestPath, requestContentType)
	if result.Outcome() != "" {
		return result
	}
	referenceJSON, candidateJSON, result := normaliseResponses(
		configured.Schema(),
		method.Output(),
		protocol,
		reference,
		candidate,
		c.maxResponseBytes,
	)
	if result.Outcome() != "" {
		return result
	}
	if len(referenceJSON) > c.maxResponseBytes || len(candidateJSON) > c.maxResponseBytes {
		return Resultf(Unable, "normalised response exceeds the comparison size limit")
	}
	// Connect errors are protocol envelopes, not instances of the RPC output message.
	runNormalisers := protocol != protocolConnectJSON || reference.StatusCode == http.StatusOK
	comparisonContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	normalisedReference, err := c.normalise(comparisonContext, configured, method, runNormalisers, "reference", referenceJSON)
	if err != nil {
		return Resultf(Unable, "normalise reference response: %v", err)
	}
	normalisedCandidate, err := c.normalise(comparisonContext, configured, method, runNormalisers, "candidate", candidateJSON)
	if err != nil {
		return Resultf(Unable, "normalise candidate response: %v", err)
	}
	differences := comparisoninternal.Diff(normalisedReference, normalisedCandidate)
	if len(differences) > 0 {
		return NewDifferenceResult(differences...)
	}
	return Resultf(Equivalent, "")
}

// resolve prefers declared endpoints, then falls back to the gRPC and Connect
// convention of naming the method in the path.
func (c *Comparator) resolve(
	loaded *schema.Schema,
	requestMethod, requestPath, requestContentType string,
) (protoreflect.MethodDescriptor, protocol, Result) {
	name, declared := c.routes.Match(requestMethod, requestPath)
	selected, result := requestProtocol(requestContentType)
	if declared && selected != protocolGRPC {
		// Declared endpoints serve raw HTTP JSON unless the request is gRPC.
		selected, result = protocolHTTPJSON, newEmptyResult()
	}
	if result.Outcome() != "" {
		return nil, 0, result
	}
	if !declared {
		method, conventional := conventionalMethod(loaded, requestPath)
		return method, selected, conventional
	}
	// Configure resolved every endpoint against this schema.
	method, err := endpointMethod(loaded, name)
	if err != nil {
		return nil, 0, Resultf(Unable, "endpoint method is unusable: %v", err)
	}
	return method, selected, newEmptyResult()
}

// normalise gives each payload a fresh evaluator, so script state cannot carry
// between payloads and a payload's normalised form depends only on its content.
func (c *Comparator) normalise(
	ctx context.Context,
	configured *comparisoninternal.Plan,
	method protoreflect.MethodDescriptor,
	runNormalisers bool,
	side string,
	payloadJSON []byte,
) (*comparisoninternal.Normalised, error) {
	evaluator, err := c.program.NewEvaluator(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	normalised, err := configured.Normalise(ctx, c.log, evaluator, method.Output(), runNormalisers, side, payloadJSON)
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
