# Protocol engine

**Status:** Implemented

## Problem

Spectre only understands HTTP and gRPC. It needs to support other protocols, such as MySQL, Redis, and Kafka.

## Decision

Protocol servers report traffic to a protocol-agnostic engine that decides what happens to each request and owns pairing, comparison, and quarantine.

### Engine API

```go
type Route string // canonical; equal routes have identical text

// RequestID and Exchange are TypeIDs (go.jetify.com/typeid) with prefixes "request" and "exchange".
type RequestID struct{ typeid.TypeID[requestPrefix] }
type Exchange struct{ typeid.TypeID[exchangePrefix] }

type Declaration interface{ declaration() }
type DeclarationRequest struct{ Type schema.TypeRef }  // egress.match
type DeclarationResponse struct{ Type schema.TypeRef } // ingress.match
type DeclarationIgnore struct{ Reason string } // ingress.ignore, or unsupported traffic

type Matcher interface {
	Declare(pattern string, declaration Declaration) error
}

// Message without a Type is compared raw, without normalisers.
type Message struct {
	Type  Option[schema.TypeRef]
	Value any
}

// Raw is a protocol's recorded response, opaque to the engine and replayed on intercept.
type Raw interface{ Release() } // idempotent

// Response is decoded lazily, only to compare, on the candidate's time.
type Response struct {
	Raw     Raw
	Latency time.Duration
	Decode  func() Content
}

// Content compares Status when both sides know it, then Failure, then Message.
type Content struct {
	Status  Option[string] // absent only with a Failure
	Message Option[Message]
	Failure Option[error]
}

type Decision interface{ decision() }
type DecisionForward struct {
	RequestID Option[RequestID] // set only at the ingress reference
	Exchange  Exchange
}
type DecisionIntercept struct{ Raw Raw }

type Config struct {
	Comparison  comparison.Config `embed:""`
	MatchWindow time.Duration
}

func NewEngine(ctx context.Context, config Config, matchers map[javascript.Protocol]Matcher,
	quarantine Quarantiner) (*Engine, error)

func (e *Engine) IngressReference(protocol javascript.Protocol) Acceptor
func (e *Engine) IngressCandidate(protocol javascript.Protocol) Acceptor
func (e *Engine) EgressReference(protocol javascript.Protocol) Acceptor
func (e *Engine) EgressCandidate(protocol javascript.Protocol) Acceptor

type Acceptor interface {
	ProcessRequest(ctx context.Context, requestID Option[RequestID], route Route,
		ignored Option[string], request func(context.Context) (Message, error)) (Decision, error)
	ProcessResponse(ctx context.Context, exchange Exchange, response Response)
	Unable(ctx context.Context, exchange Exchange, reason error)
}
```

- `NewEngine` passes each script declaration to its protocol's matcher. A script naming an unregistered protocol fails startup.
- Servers match routes themselves, because only they understand their pattern syntax and wildcard binding.
- The request payload is lazy, so the engine never delays a request it forwards.
- `ignored` is the reason ingress never compares a request's responses. Other points ignore it.
- Response decoding is lazy too, so ignored routes are never decoded and the reference handler never decodes.
- Every exchange ends with exactly one `ProcessResponse` or `Unable`. The engine owns the `Raw` from then on.
- Declarations and sum types carry `//sumtype:decl`, so type switches over them are checked for exhaustiveness.

### Points

The engine creates one acceptor per point, so servers never know whether they carry reference or candidate traffic.

| Point | Request | Response |
| --- | --- | --- |
| Ingress reference | Assigns a RequestID; forwards. Never reads the payload. | Stored without blocking the reference. |
| Ingress candidate | Requires the reference's RequestID, passed along by the server; forwards. | Paired by RequestID, normalised, and diffed. Ignored routes are not compared. |
| Egress reference | Forwards immediately, then hashes route and normalised payload in the background. | Stored for candidates with the same hash. |
| Egress candidate | Hashes, claims the earliest unused reference with the same hash, waits for its response, and intercepts with its `Raw`. | Never called. |

Egress pairs in arrival order. Each request is numbered when it arrives, before hashing, and both unused references and waiting candidates are kept in that order. A reference that is still being hashed is not waited for, so a candidate can claim a later reference of the same hash that finished hashing first.

### Pairing windows

- An unused egress reference expires after `MatchWindow` (default 5s), releasing its `Raw`.
- An egress candidate waits up to `MatchWindow` for a matching reference.
- The server must call the ingress candidate's `ProcessRequest` before forwarding the reference. A reference that ends with no candidate joined is dropped at once.
- An ingress candidate's response waits for the reference, bounded by the candidate's context, which the server limits with `CandidateTimeout`. If the reference never ends, or was cancelled, the engine logs that comparison did not finish.
- There is no separate expiry for abandoned ingress exchanges.

`MatchWindow` is part of `protocol.Config`, which both servers embed, so it is also an ingress flag.

### Quarantine

Divergence, `Unable`, an unidentifiable request, and an unmatched egress candidate are all reported to the `Quarantiner`. Servers pass the same `proxy.Candidates` they use for candidate admission, cancellation, and timeouts.

- `Unable` with a cancelled context never quarantines, because the caller gave up. Nor does a comparison the candidate's context cut short.
- Ignored routes never quarantine, even when the candidate fails. Their candidate responses are reported as skipped without waiting for the reference.
- Known statuses are compared first, so a status difference is divergent even if a body fails to decode. Otherwise a response one side cannot decode is reported as unable.

The engine releases `Raw` values that are abandoned or expire unused. After an intercept, the server releases the `Raw` once replay finishes.

Comparison and match logs name traffic by `protocol` and `route`. HTTP ingress routes are `METHOD /path`. Egress routes are the declared endpoint pattern, such as `GET forecasts.example/v1/forecasts/{location}`, or `METHOD host/path` for undeclared requests.

### HTTP server

Ingress and egress share one point handler: match the route, call `ProcessRequest` with a lazy payload from a bounded request capture, then forward while recording the response or replay a recording, and finally report the response with a lazy decoder. One recording serves both comparison and replay. At ingress, a transport failure is reported as `Unable`. At egress, it is recorded and replayed to the candidate, so the candidate sees what the reference saw.

Each server constructor builds its own candidates, route matcher, and engine from `protocol.Config`, so they cannot be wired inconsistently. The server therefore also shuts its engine down. `ComparisonMaxBodyBytes` is a server setting, because only servers capture bodies.

Only assembly differs:

- Ingress: one listener sends each request to two points. The candidate is admitted first; if it is refused, the reference is proxied without the engine. The reference streams to the client, and the candidate runs in the background with a mirrored body and its own timeout. Descriptors come from reflection.
- Ingress classifies each request when it arrives. Undeclared requests without a content type are compared only as Connect GET requests, and otherwise ignored.
- Egress: two listeners, one point each. The reference selects its upstream by host. The candidate has no upstream, so even a wrong `Forward` cannot reach a dependency. The request decoder is built at startup, so a schema that cannot decode the declared endpoints fails startup.

## Consequences

- Until RequestIDs are [propagated](002-context-propagation.md), egress pairs on hashes alone.

### Packages

- `internal/protocol`: the engine. No protocol-specific code.
- `internal/comparison`: scripts, normalisation, diffing, and canonical hashing. HTTP decoding moves out.
- `internal/httpserver`: one HTTP proxy implementation shared by ingress and egress, with subpackages:
  - `message`: decodes captured HTTP requests and responses into engine messages (Connect, gRPC, protobuf, JSON, wildcard and query binding, status handling).
  - `codec`, `route`, `middleware`: moved from `internal/httpcodec`, `internal/route`, and `internal/middleware`. `route` implements `protocol.Matcher`.
- `internal/proxy`: keeps only the protocol-agnostic `Candidates` (admission and quarantine) and `Budget`.

## Open questions

None.
