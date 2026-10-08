package comparison

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/descriptorpb"

	comparisoninternal "github.com/block/spectre/internal/comparison/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/logger"
)

// Comparator compares ingress response pairs with the ingress script endpoints.
type Comparator struct {
	log              *slog.Logger
	scripts          *scriptSet
	timeout          time.Duration
	maxResponseBytes int
}

// New constructs a response comparator from its parsed configuration.
func New(ctx context.Context, config Config, log *slog.Logger) (*Comparator, error) {
	scripts, err := newScriptSet(ctx, config, javascript.Ingress, log)
	if err != nil {
		return nil, err
	}
	return &Comparator{
		log:              log,
		scripts:          scripts,
		timeout:          config.ComparisonTimeout,
		maxResponseBytes: config.ComparisonMaxBodyBytes,
	}, nil
}

// MaxResponseBytes returns the per-backend response capture limit.
func (c *Comparator) MaxResponseBytes() int {
	return c.maxResponseBytes
}

// Configure activates the comparator against the wire descriptors.
func (c *Comparator) Configure(ctx context.Context, set *descriptorpb.FileDescriptorSet) error {
	configured, err := c.scripts.prepare(ctx, set)
	if err != nil {
		return err
	}
	c.scripts.activate(configured)
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
		attributes := []any{"event", logger.EventCorrelation, "method", requestMethod, "path", requestPath, "outcome", result.Outcome()}
		differences := result.Differences()
		if len(differences) > 0 {
			attributes = append(attributes, "differences", differences)
		}
		if result.Reason() != "" {
			attributes = append(attributes, "reason", result.Reason())
		}
		// dt is the candidate's latency minus the reference's: negative when the
		// candidate was faster, positive when it was slower.
		if reference.Latency > 0 && candidate.Latency > 0 {
			attributes = append(attributes, "dt", candidate.Latency-reference.Latency)
		}
		c.log.InfoContext(ctx, "Response comparison completed", attributes...)
	}()
	if excludedRequestPath(requestPath) {
		return Resultf(Skipped, "gRPC namespace is excluded from response comparison")
	}
	configured, ok := c.scripts.active().Get()
	if !ok {
		return Resultf(Skipped, "comparison schema is not ready")
	}
	if reference.Overflow || candidate.Overflow {
		return Resultf(Unable, "response exceeds the comparison size limit")
	}
	if requestContentType == "" && hasJSONMediaType(reference.Header) {
		requestContentType = jsonMediaType
	}
	// Ingress endpoints never name a host.
	root, _, protocol, _, result := c.scripts.resolve(configured, requestMethod, "", requestPath, requestContentType)
	if result.Outcome() != "" {
		return result
	}
	referencePayload, candidatePayload, result := normaliseResponses(
		configured.plan.Schema(),
		configured.codec,
		root,
		protocol,
		requestMethod,
		reference,
		candidate,
		c.maxResponseBytes,
	)
	if result.Outcome() != "" {
		return result
	}
	// Connect errors are protocol envelopes, not instances of the RPC output message.
	runNormalisers := protocol != protocolConnectJSON || reference.StatusCode == http.StatusOK
	comparisonContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	normalisedReference, err := c.scripts.normalise(comparisonContext, configured, root, runNormalisers, "reference", referencePayload)
	if err != nil {
		return Resultf(Unable, "normalise reference response: %v", err)
	}
	normalisedCandidate, err := c.scripts.normalise(comparisonContext, configured, root, runNormalisers, "candidate", candidatePayload)
	if err != nil {
		return Resultf(Unable, "normalise candidate response: %v", err)
	}
	differences := comparisoninternal.Diff(normalisedReference, normalisedCandidate)
	if len(differences) > 0 {
		return NewDifferenceResult(differences...)
	}
	return Resultf(Equivalent, "")
}
