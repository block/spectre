// Package protocol pairs the reference and candidate traffic that protocol
// servers report, compares ingress responses, and replays egress responses.
package protocol

import (
	"context"
	"time"

	. "github.com/alecthomas/types/optional"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/quarantine"
	"github.com/block/spectre/internal/schema"
)

type requestPrefix struct{}

func (requestPrefix) Prefix() string { return "request" }

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

// Declaration is what a script declares for a protocol pattern.
//
//sumtype:decl
type Declaration interface{ declaration() }

// DeclarationRequest types the requests that egress forwards to a dependency.
type DeclarationRequest struct{ Type schema.TypeRef }

// DeclarationResponse types the responses that ingress compares.
type DeclarationResponse struct{ Type schema.TypeRef }

// DeclarationIgnore skips comparison of ingress responses.
type DeclarationIgnore struct{ Reason string }

func (DeclarationRequest) declaration()  {}
func (DeclarationResponse) declaration() {}
func (DeclarationIgnore) declaration()   {}

// Matcher owns a protocol's pattern table. The engine passes it every script
// declaration for its protocol at startup, so invalid patterns fail startup.
type Matcher interface {
	Declare(pattern string, declaration Declaration) error
}

// Quarantiner stops all candidate traffic.
type Quarantiner interface {
	Quarantine(ctx context.Context, reason quarantine.Reason)
}

// Raw is a server's own record of a response, which an intercept replays.
// Release frees it, and must be safe to call more than once.
type Raw interface {
	Release()
}

// Message is a decoded payload. One without a Type is compared without normalisers.
type Message struct {
	Type  Option[schema.TypeRef]
	Value any
}

// Response is what a server received for a forwarded request.
type Response struct {
	Raw Raw
	// Latency is the time to the first response byte, or zero if unmeasured.
	Latency time.Duration
	// Decode is called only to compare, on the candidate's time rather than the
	// reference's, and while the engine still holds Raw.
	Decode func() Content
}

// Content is a response's comparable content. Comparison checks Status when both
// sides know it, then Failure, then Message.
type Content struct {
	// Status is absent only when Failure explains why.
	Status  Option[string]
	Message Option[Message]
	Failure Option[error]
}

// Decision tells a server what to do with a request.
//
//sumtype:decl
type Decision interface{ decision() }

// DecisionForward sends the request on. The server reports the response under
// Exchange, and passes RequestID to a candidate when one is assigned.
type DecisionForward struct {
	RequestID Option[RequestID]
	Exchange  Exchange
}

// DecisionIntercept answers the request with Raw instead of sending it. The
// server releases Raw once replay finishes.
type DecisionIntercept struct{ Raw Raw }

func (DecisionForward) decision()   {}
func (DecisionIntercept) decision() {}
