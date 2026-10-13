package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/proxy"
)

// errAborted explains a response that ReverseProxy could not finish copying.
var errAborted = errors.New("response was aborted")

// decodeFunc returns the comparable content of a recorded response.
type decodeFunc func(recorded *recording) protocol.Content

// noDecode decodes responses that are only replayed or ignored, never compared.
func noDecode(*recording) protocol.Content {
	return protocol.Content{Failure: Some(errors.New("response is not compared"))}
}

// unreadRequest is the payload of requests whose content the engine never needs.
func unreadRequest(context.Context) (protocol.Message, error) {
	return protocol.Message{}, errors.New("request payloads are not decoded at this point")
}

// point serves one ingress or egress point, so both share forwarding, recording,
// replay, and reporting. Only how requests reach it differs.
type point struct {
	acceptor protocol.Acceptor
	log      *slog.Logger
	// budget returns the budget for one recording.
	budget func() *proxy.Budget
	// abortCancels reports an aborted response as cancelled, because the client
	// may have caused it.
	abortCancels bool
	// replaysFailures reports a transport failure as a response, so a candidate
	// replaying it sees the same failure. Otherwise it is reported as unable.
	replaysFailures bool
}

func newPoint(acceptor protocol.Acceptor, log *slog.Logger, budget func() *proxy.Budget, abortCancels, replaysFailures bool) *point {
	return &point{acceptor: acceptor, log: log, budget: budget, abortCancels: abortCancels, replaysFailures: replaysFailures}
}

// process asks the engine what to do with a request.
func (p *point) process(ctx context.Context, requestID Option[protocol.RequestID], route protocol.Route,
	ignored Option[string], request func(ctx context.Context) (protocol.Message, error),
) (protocol.Decision, error) {
	return errors.WithStack2(p.acceptor.ProcessRequest(ctx, requestID, route, ignored, request))
}

// respond carries out the engine's decision. A forwarded response is recorded and
// reported; with no upstream, the request ends without being sent.
func (p *point) respond(writer http.ResponseWriter, request *http.Request, decision protocol.Decision,
	upstream Option[*httputil.ReverseProxy], decode decodeFunc,
) {
	switch decision := decision.(type) {
	case protocol.DecisionForward:
		p.forward(writer, request, decision.Exchange, upstream, decode)
	case protocol.DecisionIntercept:
		p.replay(writer, request, decision.Raw)
	}
}

func (p *point) forward(writer http.ResponseWriter, request *http.Request, exchange protocol.Exchange,
	upstream Option[*httputil.ReverseProxy], decode decodeFunc,
) {
	ctx := request.Context()
	destination, ok := upstream.Get()
	if !ok {
		p.acceptor.Unable(ctx, exchange, errors.New("request has no usable destination"))
		writer.WriteHeader(http.StatusBadGateway)
		return
	}
	recorded := newRecording(p.budget())
	reported := false
	// ReverseProxy panics to abort a response, so report before the panic continues.
	defer func() {
		if reported {
			return
		}
		recorded.finish(nil, errAborted)
		reportContext := ctx
		if p.abortCancels {
			var cancel context.CancelCauseFunc
			reportContext, cancel = context.WithCancelCause(ctx)
			cancel(errAborted)
		}
		p.report(reportContext, exchange, recorded, decode)
	}()
	recordingProxy(destination, recorded).ServeHTTP(writer, request)
	// The transport may never run, so the handler guarantees the recording completes.
	recorded.finish(nil, errors.New("request ended without a response"))
	reported = true
	p.report(ctx, exchange, recorded, decode)
}

// report ends the exchange with the response, or as unable. The response is
// decoded only if the engine compares it.
func (p *point) report(ctx context.Context, exchange protocol.Exchange, recorded *recording, decode decodeFunc) {
	err := recorded.complete()
	if err == nil && !p.replaysFailures {
		err = recorded.failure()
	}
	if err != nil {
		recorded.Release()
		p.acceptor.Unable(ctx, exchange, err)
		return
	}
	p.acceptor.ProcessResponse(ctx, exchange, protocol.Response{
		Raw:     recorded,
		Latency: recorded.latency(),
		Decode:  func() protocol.Content { return decode(recorded) },
	})
}

// replay answers a request from another point's recording, then releases it.
func (p *point) replay(writer http.ResponseWriter, request *http.Request, raw protocol.Raw) {
	defer raw.Release()
	recorded, ok := raw.(*recording)
	if !ok {
		p.log.ErrorContext(request.Context(), "Intercepted response is not an HTTP recording")
		writer.WriteHeader(http.StatusBadGateway)
		return
	}
	replayed := request.Clone(request.Context())
	replayed.Body = http.NoBody
	replayed.ContentLength = 0
	newReplayProxy(recorded, p.log).ServeHTTP(writer, replayed)
}

// recordingProxy copies a destination's reverse proxy so that one request's
// response is recorded. The copy shares the destination's forwarding setup.
func recordingProxy(destination *httputil.ReverseProxy, recorded *recording) *httputil.ReverseProxy {
	forward := *destination
	forward.Transport = newRecordingTransport(destination.Transport, recorded)
	return &forward
}

// newReplayProxy answers one request from its recording, reusing the reverse
// proxy's header and trailer handling.
func newReplayProxy(recorded *recording, log *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		// The recording answers without dialing, so the outbound URL is never used.
		Rewrite:   func(*httputil.ProxyRequest) {},
		Transport: recorded,
		ErrorLog:  slog.NewLogLogger(log.Handler(), slog.LevelError),
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			log.WarnContext(request.Context(), "Candidate replay failed", "error", err)
			writer.WriteHeader(http.StatusBadGateway)
		},
	}
}
