package httpserver

import (
	"github.com/block/spectre/internal/boundary"
)

// Supports returns the kinds of IO the HTTP server proxies, described for an
// agent that sorts a service's code into them.
func Supports() []boundary.Support {
	return []boundary.Support{
		{
			Kind: boundary.Kind{Direction: boundary.Ingress, Protocol: boundary.ProtocolHTTP},
			Description: "Code that receives HTTP requests to the service, including gRPC and Connect: " +
				"route registrations, controllers, handlers, and service implementations. " +
				"The service's routes and RPC methods are the entry points SPECTRE compares, " +
				"so every request path starts at one of these nodes.",
			Settings: []boundary.Setting{{
				Name:     "routes",
				Required: true,
				Description: "Each route the node handles, as <METHOD> /<path>. " +
					"A gRPC or Connect method is POST /<package>.<Service>/<Method>. " +
					"List every route the service serves: a request to a route no node lists quarantines the candidate.",
			}},
		},
		{
			Kind: boundary.Kind{Direction: boundary.Egress, Protocol: boundary.ProtocolHTTP},
			Description: "Code that sends HTTP requests to another service, including gRPC and Connect: " +
				"API clients, generated stubs and the code that configures them, " +
				"and code that builds requests or reads their responses. " +
				"SPECTRE pairs these requests and replays the reference's responses to the candidate.",
		},
	}
}
