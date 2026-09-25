package comparison

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/types/descriptorpb"

	comparisoninternal "github.com/block/spectre/internal/comparison/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// Comparator shares one immutable script program across response comparisons.
// Only schema-plan replacement is synchronized; each comparison owns its evaluator.
type Comparator struct {
	log              *slog.Logger
	program          *javascript.Program
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
	source, err := os.ReadFile(config.ComparisonScript)
	if err != nil {
		return nil, errors.Wrap(err, "read comparison script")
	}
	programContext, cancel := context.WithTimeout(ctx, config.ComparisonTimeout)
	defer cancel()
	program, err := javascript.NewProgram(programContext, config.ComparisonScript, string(source))
	if err != nil {
		return nil, errors.Wrap(err, "compile comparison script")
	}
	return &Comparator{
		log:              log,
		program:          program,
		timeout:          config.ComparisonTimeout,
		maxResponseBytes: config.ComparisonMaxResponseBytes,
	}, nil
}

// MaxResponseBytes returns the per-backend response capture limit.
func (c *Comparator) MaxResponseBytes() int {
	return c.maxResponseBytes
}

// Configure activates the comparator against a reflected descriptor set.
func (c *Comparator) Configure(ctx context.Context, set *descriptorpb.FileDescriptorSet) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, "configure comparator")
	}
	loaded, err := schema.NewFromFileDescriptorSet(set)
	if err != nil {
		return errors.Wrap(err, "load comparison schema")
	}
	configured, err := comparisoninternal.NewPlan(loaded, c.program.Fields(), c.program.Messages(), c.program.RPCs())
	if err != nil {
		return errors.Wrap(err, "prepare comparison plan")
	}
	c.mu.Lock()
	c.plan = configured
	c.mu.Unlock()
	return nil
}

// Compare compares one response pair without exposing response values in its result.
func (c *Comparator) Compare(
	ctx context.Context,
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
	if requestContentType == "" && hasMediaType(reference.Header, "application/json") {
		requestContentType = "application/json"
	}
	method, protocol, result := resolveMethod(configured.Schema(), requestPath, requestContentType)
	if result.Outcome() != "" {
		return result
	}
	referenceJSON, candidateJSON, result := normaliseResponses(
		configured.Schema(),
		method,
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
	runComparators := protocol != protocolConnectJSON || reference.StatusCode == http.StatusOK
	comparisonContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	evaluator, err := c.program.NewEvaluator(comparisonContext)
	if err != nil {
		return Resultf(Unable, "initialise JavaScript evaluator: %v", err)
	}
	defer evaluator.Close()
	differences, err := configured.Compare(
		comparisonContext,
		c.log,
		evaluator,
		method,
		runComparators,
		referenceJSON,
		candidateJSON,
	)
	if err != nil {
		return Resultf(Unable, "compare responses: %v", err)
	}
	if len(differences) > 0 {
		return NewDifferenceResult(differences...)
	}
	return Resultf(Equivalent, "")
}
