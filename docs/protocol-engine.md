# Protocol engine

Spectre only understands HTTP and gRPC. To support other protocols, such as MySQL, Redis, and Kafka, protocol servers report traffic to a protocol-agnostic engine that decides what happens to each request and owns pairing, comparison, and quarantine.

## Packages

- `internal/protocol`: the engine. No protocol-specific code.
- `internal/comparison`: scripts, normalisation, diffing, and canonical hashing. HTTP decoding moves out.
- `internal/httpserver`: one HTTP proxy implementation shared by ingress and egress, with subpackages:
  - `message`: decodes captured HTTP requests and responses into engine messages (Connect, gRPC, protobuf, JSON, wildcard and query binding, status handling).
  - `codec`, `route`, `middleware`: moved from `internal/httpcodec`, `internal/route`, and `internal/middleware`. `route` implements `protocol.Matcher`.
- `internal/proxy`: keeps only the protocol-agnostic `Candidates` (admission and quarantine) and `Budget`.

## Engine API

```go
type Route string // canonical; equal routes have identical text

// RequestID and Exchange are TypeIDs (go.jetify.com/typeid) with prefixes "request" and "exchange".
type RequestID struct{ typeid.TypeID[requestPrefix] }
type Exchange struct{ typeid.TypeID[exchangePrefix] }

type Declaration interface{ declaration() }
type DeclarationRequest struct{ Type TypeRef }  // egress.match
type DeclarationResponse struct{ Type TypeRef } // ingress.match
type DeclarationIgnore struct{}                 // ingress.ignore

type Matcher interface {
	Declare(pattern string, declaration Declaration) error
}

// Message without a Type is compared raw, without normalisers.
type Message struct {
	Type  Option[TypeRef]
	Value any
}

// Response compares Status, then Message. Raw is opaque to the engine and replayed on intercept.
type Response struct {
	Status  string
	Message Option[Message]
	Raw     any
	Latency time.Duration
}

type Decision interface{ decision() }
type DecisionForward struct {
	RequestID RequestID
	Exchange  Exchange
}
type DecisionIntercept struct{ Raw any }

func NewEngine(ctx context.Context, config comparison.Config, matchers map[Protocol]Matcher,
	quarantine Quarantiner, log *slog.Logger) (*Engine, error)

func (e *Engine) IngressReference(protocol Protocol) *Acceptor
func (e *Engine) IngressCandidate(protocol Protocol) *Acceptor
func (e *Engine) EgressReference(protocol Protocol) *Acceptor
func (e *Engine) EgressCandidate(protocol Protocol) *Acceptor

func (a *Acceptor) ProcessRequest(ctx context.Context, requestID Option[RequestID], route Route,
	declaration Option[Declaration], request func(context.Context) (Message, error)) (Decision, error)
func (a *Acceptor) ProcessResponse(ctx context.Context, exchange Exchange, response Response)
func (a *Acceptor) Unable(ctx context.Context, exchange Exchange, reason error)
```

- `NewEngine` passes each script declaration to its protocol's matcher. A script naming an unregistered protocol fails startup.
- Servers match routes themselves, because only they understand their pattern syntax and wildcard binding.
- The request payload is lazy, so the engine never delays a request it forwards.
- Every exchange ends with exactly one `ProcessResponse` or `Unable`.

## Points

The engine creates one acceptor per point, so servers never know whether they carry reference or candidate traffic.

| Point | Request | Response |
| --- | --- | --- |
| Ingress reference | Assigns a RequestID; forwards. Never reads the payload. | Stored without blocking the reference. |
| Ingress candidate | Requires the reference's RequestID, passed along by the server; forwards. | Paired by RequestID, normalised, and diffed. Ignored routes are not compared. |
| Egress reference | Forwards immediately, then hashes route and normalised payload in the background. | Stored for candidates with the same hash. |
| Egress candidate | Hashes, claims the oldest unused reference with the same hash, waits for its response, and intercepts with its `Raw`. | Never called. |

Until RequestIDs are [propagated](context-propagation.md), egress pairs on hashes alone.

## Pairing windows

- An unused egress reference expires after `MatchWindow` (default 5s), releasing its `Raw`.
- An egress candidate waits up to `MatchWindow` for a matching reference.
- An ingress reference response waits until its candidate leg ends, which the server bounds with `CandidateTimeout`. If the candidate never responds, the engine logs that comparison did not finish.
- The engine also expires abandoned exchanges, as a guard against server bugs.

`MatchWindow` moves into `comparison.Config`.

## Quarantine

Divergence, `Unable`, an unidentifiable request, and an unmatched egress candidate are all reported to the `Quarantiner`. Servers pass the same `proxy.Candidates` they use for candidate admission, cancellation, and timeouts.

`Raw` values that expire unused are released through an optional `Release()` method. After an intercept, the server releases the `Raw` once replay finishes.

## HTTP server

Ingress and egress share one point handler: match the route, call `ProcessRequest` with a lazy payload from a bounded request capture, then forward while recording the response or replay a recording, and finally report the decoded response. One recording serves both comparison and replay.

Only assembly differs:

- Ingress: one listener sends each request to two points. The reference streams to the client, and the candidate runs in the background with a mirrored body and its own timeout. Descriptors come from reflection.
- Egress: two listeners, one point each. The reference selects its upstream by host. The candidate has no upstream, so even a wrong `Forward` cannot reach a dependency.

## Stages

1. Move HTTP decoding into `httpserver/message` and the HTTP packages under `httpserver`, without behaviour changes.
2. Add the engine and the HTTP matcher, tested with a fake protocol.
3. Build the shared HTTP point handler and move ingress and egress onto it, deleting `Comparator` and `RequestHasher`.
4. Delete the `docs/protocol.go` sketch.
