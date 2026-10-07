package ingress

import (
	"context"
	"net"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/proxy"
)

// Serve accepts ingress traffic until the context is cancelled or the server fails.
// Readiness remains false until the comparison schema is configured.
func (h *Handler) Serve(ctx context.Context, listener net.Listener) error {
	bindings := []proxy.Binding{proxy.NewBinding(listener, h)}
	lifecycle := proxy.NewLifecycle(h.configureSchema, h.markUnready, h.Shutdown)
	return errors.Wrap(proxy.Serve(ctx, h.config.Config, h.log, bindings, lifecycle), "serve ingress")
}

func (h *Handler) markUnready() {
	h.health.SetReady(false)
}

// configureSchema makes readiness contingent on one schema shared by both backends.
// Static descriptors are identical for both, so only reflected ones are compared.
func (h *Handler) configureSchema(ctx context.Context) {
	reflectionContext, cancel := context.WithTimeout(ctx, h.config.ReflectionTimeout)
	defer cancel()
	set := h.static
	if h.config.Reflection {
		reference, candidate, err := h.loadDescriptors(reflectionContext)
		if err != nil {
			h.log.ErrorContext(ctx, "Backend descriptor loading failed", "error", err)
			return
		}
		if !proto.Equal(reference, candidate) {
			h.log.ErrorContext(ctx, "Backend descriptors differ")
			return
		}
		set, err = descriptors.Merge(reference, h.static)
		if err != nil {
			h.log.ErrorContext(ctx, "Backend descriptors conflict with static descriptors", "error", err)
			return
		}
	}
	if err := h.comparator.Configure(reflectionContext, set); err != nil {
		h.log.ErrorContext(ctx, "Response comparison setup failed", "error", err)
		return
	}
	h.health.SetReady(true)
}

func (h *Handler) loadDescriptors(ctx context.Context) (*descriptorpb.FileDescriptorSet, *descriptorpb.FileDescriptorSet, error) {
	type descriptorResult struct {
		name string
		set  *descriptorpb.FileDescriptorSet
		err  error
	}
	loader, ok := h.descriptors.Get()
	if !ok {
		return nil, nil, errors.New("descriptor loader is required")
	}
	results := make(chan descriptorResult, 2)
	// Both loads share one deadline and each goroutine publishes exactly one result.
	for name, endpoint := range map[string]string{"reference": h.config.Reference, "candidate": h.config.Candidate} {
		go func() {
			set, err := loader.Load(ctx, endpoint)
			results <- descriptorResult{name: name, set: set, err: err}
		}()
	}
	sets := map[string]*descriptorpb.FileDescriptorSet{}
	for range 2 {
		loaded := <-results
		if loaded.err != nil {
			return nil, nil, errors.Wrapf(loaded.err, "load %s descriptors", loaded.name)
		}
		if loaded.set == nil {
			return nil, nil, errors.Errorf("load %s descriptors: descriptor set is nil", loaded.name)
		}
		sets[loaded.name] = loaded.set
	}
	return sets["reference"], sets["candidate"], nil
}
