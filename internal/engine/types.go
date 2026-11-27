// Package engine pairs the reference and candidate traffic that protocol
// servers report, compares ingress responses, and replays egress responses.
package engine

import (
	"context"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

type requestPrefix struct{}

func (requestPrefix) Prefix() string { return "srid" }

// RequestIDKey names the W3C baggage member that carries a mirrored request's ID on its
// dependency calls.
const RequestIDKey = "spectre_request_id"

// RequestID links a mirrored inbound request to the operations it causes.
type RequestID struct {
	typeid.TypeID[requestPrefix]
}

type exchangePrefix struct{}

func (exchangePrefix) Prefix() string { return "exchange" }

// Exchange links a forwarded request to its response. It is unique within one
// engine, so it never leaves the process that forwarded the request.
type Exchange struct {
	typeid.TypeID[exchangePrefix]
}

// ErrCandidateStopped is the cancellation cause of candidate work the proxy
// stops, as opposed to work the candidate abandons.
var ErrCandidateStopped = errors.New("candidate work stopped by the proxy")

// Quarantiner stops all candidate traffic.
type Quarantiner interface {
	Quarantine(ctx context.Context, reason model.QuarantineReason)
}

// CallCounter counts the ingress requests whose comparison completed, by route.
type CallCounter interface {
	Count(route model.Route)
}

// Raw is a server's own record of a response, which an intercept replays.
// Release frees it, and must be safe to call more than once.
type Raw interface {
	Release()
}

// Response is what a server received for a forwarded request.
type Response struct {
	Raw Raw
	// Latency is the time to the first response byte, or zero if unmeasured.
	Latency time.Duration
	// Decode is called to compare, on the candidate's time rather than the reference's,
	// or when a replacement's source response finishes. The engine still holds Raw.
	Decode func(ctx context.Context) Content
}

// Normaliser is a protocol's normalisation of one response, which the engine applies
// at the point it compares, on the candidate's time. A None status is then not compared.
type Normaliser func(ctx context.Context) (status Option[string], message Option[schema.Value], err error)

// Content is a response's comparable content. Comparison skips it when Ignored,
// then checks Status when both sides know it, then Failure, then Message.
type Content struct {
	// Status is a fixed code such as "200", never payload, because differences report
	// it. It is absent only when Failure explains why.
	Status  Option[string]
	Message Option[schema.Value]
	Failure Option[error]
	// Ignored explains why the response is never compared.
	Ignored Option[string]
	// Normalise applies the protocol's normalisation to this response before comparison.
	Normalise Option[Normaliser]
	// Hold is the memory reserved for Message while the engine keeps it after Raw
	// is released. The engine releases it once it stops offering Message.
	Hold Option[Raw]
}

// Decision tells a server what to do with a request.
//
//sumtype:decl
type Decision interface{ decision() }

// DecisionForward sends the request on, carrying RequestID when one is assigned, so the
// service passes it on to its dependency requests. The server reports the response under Exchange.
type DecisionForward struct {
	RequestID Option[RequestID]
	Exchange  Exchange
}

// DecisionIntercept answers the request with Raw instead of sending it. The
// server releases Raw once replay finishes.
type DecisionIntercept struct{ Raw Raw }

// DecisionAnswer answers the request with Message, which the server encodes as
// a successful response, instead of sending it.
type DecisionAnswer struct{ Message schema.Value }

func (DecisionForward) decision()   {}
func (DecisionIntercept) decision() {}
func (DecisionAnswer) decision()    {}
