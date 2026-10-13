package protocol

import (
	"context"

	. "github.com/alecthomas/types/optional"
)

// Acceptor receives the requests and responses at one ingress or egress point,
// so servers never know whether they carry reference or candidate traffic.
type Acceptor interface {
	// ProcessRequest decides what a server does with a request. ignored explains why
	// ingress never compares its responses. An error means the request must not be sent.
	ProcessRequest(ctx context.Context, requestID Option[RequestID], route Route, ignored Option[string],
		request func(ctx context.Context) (Message, error)) (Decision, error)
	// ProcessResponse reports the response to a forwarded request. The engine owns
	// response.Raw from here on.
	ProcessResponse(ctx context.Context, exchange Exchange, response Response)
	// Unable ends a forwarded request whose response cannot be compared or replayed.
	// A cancelled ctx explains the failure, so it never quarantines the candidate.
	Unable(ctx context.Context, exchange Exchange, reason error)
}
