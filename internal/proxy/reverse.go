package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/netaddr"
)

// NewReverseProxy forwards requests to a backend target with trusted forwarding headers.
// A discarding proxy logs failures as candidate failures and writes no response.
func NewReverseProxy(target *netaddr.Endpoint, transport http.RoundTripper, log *slog.Logger, discard bool) (*httputil.ReverseProxy, error) {
	targetURL, isBackend := target.URL().Get()
	if !isBackend {
		return nil, errors.New("reverse proxy target is not a backend")
	}
	if configured, ok := transport.(*Transport); ok {
		transport = configured.ForBackend(target)
	}
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(targetURL)
			// A unix-socket backend is a mesh socket that routes by authority over
			// a fixed connection, so the inbound Host selects the upstream.
			if target.IsUnix() {
				request.Out.Host = request.In.Host
			}
			for name := range request.Out.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Forwarded-") {
					request.Out.Header.Del(name)
				}
			}
			request.Out.Header.Del("X-Real-IP")
			request.SetXForwarded()
			// Drop X-Forwarded-Host so the inbound host is never forwarded as a hint.
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
	}, nil
}
