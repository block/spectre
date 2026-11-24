package event

import (
	"context"
	"net"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/httpservice"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/event/script"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
	"github.com/block/spectre/spectrepb/eventpb/eventpbconnect"
)

// Server serves events on the reference's and the candidate's listeners. Start events never
// reach the engine: their run IDs are minted here, because egress pairs by any ID.
type Server struct {
	reference *referenceService
	candidate *candidateService
	healths   []*health.Handler
}

// New builds the event server from what scripts declared through the handle.
func New(config Config, handle script.Handle, runtime protocols.Runtime, ingress, egress protocols.Side) (*Server, error) {
	if runtime.Scripts == nil || runtime.Schema == nil {
		return nil, errors.New("events need scripts and a schema")
	}
	for _, side := range []protocols.Side{ingress, egress} {
		if side.Candidates == nil || side.Quarantiner == nil || side.Health == nil {
			return nil, errors.New("events need candidates, a quarantiner and health on both sides")
		}
	}
	declared := handle.Declared(runtime.Scripts.Program())
	if err := checkMappings(runtime.Schema, declared); err != nil {
		return nil, err
	}
	mapper := newMapper(runtime.Scripts, handle, runtime.Schema)
	declaredRules := newRules(declared)
	queue := newTriggers(config.TriggerQueue)
	return &Server{
		reference: newReferenceService(mapper, declaredRules, stateTypes(ingress.Nodes), ingress.Quarantiner,
			egress.Reference, []*proxy.Candidates{ingress.Candidates, egress.Candidates}, queue),
		candidate: newCandidateService(mapper, declaredRules, ingress.Candidates, ingress.Quarantiner, egress, queue),
		healths:   []*health.Handler{ingress.Health, egress.Health},
	}, nil
}

// checkMappings fails startup for a mapping whose types the schema lacks.
func checkMappings(loaded *schema.Schema, declared script.Declarations) error {
	mappings := []Option[script.Mapping]{}
	for _, start := range declared.Starts {
		mappings = append(mappings, start.Payload)
	}
	for _, produced := range declared.Produced {
		mappings = append(mappings, produced.Payload, produced.Result)
	}
	for _, declaredMapping := range mappings {
		mapping, ok := declaredMapping.Get()
		if !ok {
			continue
		}
		for _, typeName := range []schema.TypeRef{mapping.From, mapping.To} {
			if _, err := loaded.Type(typeName); err != nil {
				return errors.Wrap(err, "resolve event mapping")
			}
		}
	}
	return nil
}

// Serve serves both listeners until the context is cancelled. Both directions are ready once
// they serve. The mirror stops candidate work first, which ends the candidate's Receive streams.
func (s *Server) Serve(ctx context.Context, listeners map[string]net.Listener) error {
	reference, candidate := listeners[ReferenceListener], listeners[CandidateListener]
	if reference == nil || candidate == nil {
		return errors.New("event listeners are not open")
	}
	bindings := []httpservice.Binding{
		httpservice.NewBinding(reference, eventHandler(s.reference)),
		httpservice.NewBinding(candidate, eventHandler(s.candidate)),
	}
	lifecycle := httpservice.NewLifecycle(func(context.Context) {
		for _, handler := range s.healths {
			handler.SetReady(true)
		}
	}, func() {}, func(context.Context) error { return nil })
	return errors.Wrap(httpservice.Serve(ctx, httpservice.NewServeConfig(), bindings, lifecycle), "serve events")
}

func eventHandler(service eventpbconnect.EventServiceHandler) http.Handler {
	mux := http.NewServeMux()
	server := connect.NewServer()
	eventpbconnect.RegisterEventServiceHandler(server, service)
	connecthttp.Mount(mux, server)
	return mux
}

// admit starts candidate work that the mirror can stop, returning the work's context and the
// function that ends it, unless candidate work is refused.
func admit(ctx context.Context, candidates *proxy.Candidates) (context.Context, func(), bool) {
	ctx, cancel := context.WithCancelCause(ctx)
	run, ok := candidates.Start(ctx, func() { cancel(engine.ErrCandidateStopped) }).Get()
	if !ok {
		cancel(nil)
		return nil, nil, false
	}
	return ctx, func() {
		candidates.Finish(run)
		cancel(nil)
	}, true
}

// quarantined reports whether any direction has quarantined the candidate.
func quarantined(directions ...*proxy.Candidates) bool {
	for _, candidates := range directions {
		if candidates.QuarantineStatus().Ok() {
			return true
		}
	}
	return false
}

func reason(kind model.QuarantineKind, direction model.Direction, eventType string, detail string) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:    kind,
		Traffic: Some(model.Traffic{Direction: direction, Protocol: script.Protocol, Route: model.Route(eventType)}),
		Detail:  detail,
	}
}

func invalidArgument(message string) error {
	return connect.NewError(connect.CodeInvalidArgument, message)
}
