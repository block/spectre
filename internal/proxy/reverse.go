package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/block/spectre/internal/netaddr"
)

// NewReverseProxy forwards requests to target with trusted forwarding headers.
// A discarding proxy logs failures as candidate failures and writes no response.
func NewReverseProxy(target *netaddr.Endpoint, transport http.RoundTripper, log *slog.Logger, discard bool) *httputil.ReverseProxy {
	if configured, ok := transport.(*Transport); ok {
		transport = configured.ForBackend(target)
	}
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target.URL())
			for name := range request.Out.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Forwarded-") {
					request.Out.Header.Del(name)
				}
			}
			request.Out.Header.Del("X-Real-IP")
			request.SetXForwarded()
			// The inbound Host is untrusted and must not become backend routing input.
			request.Out.Header.Del("X-Forwarded-Host")
		},
		Transport: transport,
		ErrorLog:  slog.NewLogLogger(log.Handler(), slog.LevelError),
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			if discard {
				log.WarnContext(request.Context(), "Candidate request failed", "error", err)
				return
			}
			log.ErrorContext(request.Context(), "Reference request failed", "error", err)
			writer.WriteHeader(http.StatusBadGateway)
		},
	}
}
