package comparison

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"time"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/route"
)

// RequestHasher identifies egress requests by their normalised input, using the
// egress script endpoints.
type RequestHasher struct {
	scripts      *scriptSet
	timeout      time.Duration
	maxBodyBytes int
}

// NewRequestHasher constructs a request hasher from its parsed configuration.
func NewRequestHasher(ctx context.Context, config Config, log *slog.Logger) (*RequestHasher, error) {
	scripts, err := newScriptSet(ctx, config, javascript.Egress, log)
	if err != nil {
		return nil, err
	}
	return &RequestHasher{
		scripts:      scripts,
		timeout:      config.ComparisonTimeout,
		maxBodyBytes: config.ComparisonMaxBodyBytes,
	}, nil
}

// Configure activates the hasher against a descriptor set. It fails if an endpoint
// path wildcard does not name a field of the method's input.
func (h *RequestHasher) Configure(ctx context.Context, set *descriptorpb.FileDescriptorSet) error {
	configured, err := h.scripts.prepare(ctx, set)
	if err != nil {
		return err
	}
	for _, endpoint := range h.scripts.endpoints() {
		method, err := endpointMethod(configured.Schema(), protoreflect.FullName(endpoint.Method()))
		if err != nil {
			return errors.Wrapf(err, "resolve endpoint %q", endpoint.Pattern())
		}
		for _, name := range route.Wildcards(endpoint.Pattern()) {
			if _, err := resolveBinding(method.Input(), name, false); err != nil {
				return errors.Wrapf(err, "bind endpoint %q wildcard %q", endpoint.Pattern(), name)
			}
		}
	}
	h.scripts.activate(configured)
	return nil
}

// Hash returns SHA-256 over the method name and the canonical JSON of the
// normalised input. An error means the request cannot be identified.
func (h *RequestHasher) Hash(ctx context.Context, request Request) (RequestHash, error) {
	configured := h.scripts.active()
	if configured == nil {
		return RequestHash{}, errors.New("comparison schema is not ready")
	}
	if request.Overflow {
		return RequestHash{}, errors.New("request exceeds the comparison size limit")
	}
	loaded := configured.Schema()
	method, selected, wildcards, result := h.scripts.resolve(
		loaded,
		request.Method,
		request.Host,
		request.Path,
		request.Header.Get("Content-Type"),
	)
	if result.Outcome() != "" {
		return RequestHash{}, errors.Errorf("resolve request method: %s", result.Reason())
	}
	payload, err := decodeRequest(loaded, method.Input(), selected, request, wildcards, h.maxBodyBytes)
	if err != nil {
		return RequestHash{}, errors.Wrapf(err, "decode request as %s", method.Input().FullName())
	}
	normaliseContext, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	normalised, err := h.scripts.normalise(normaliseContext, configured, method.Input(), true, "request", payload)
	if err != nil {
		return RequestHash{}, errors.Wrap(err, "normalise request")
	}
	canonical, err := normalised.CanonicalJSON()
	if err != nil {
		return RequestHash{}, errors.WithStack(err)
	}
	// Full names never contain a zero byte, so it separates the name from the JSON.
	hashed := make([]byte, 0, len(method.FullName())+1+len(canonical))
	hashed = append(hashed, method.FullName()...)
	hashed = append(hashed, 0)
	hashed = append(hashed, canonical...)
	return sha256.Sum256(hashed), nil
}
