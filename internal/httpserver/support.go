package httpserver

import (
	"github.com/block/spectre/internal/boundary"
)

// Supports returns the kinds of IO the HTTP server proxies, described for an
// agent that sorts a service's code into them.
func Supports() []boundary.Support {
	return []boundary.Support{
		{
			Kind:        boundary.Kind{Direction: boundary.Ingress, Protocol: boundary.ProtocolHTTP},
			Description: "Module that receives HTTP requests to the service, including gRPC and Connect.",
			Settings: []boundary.Setting{{
				Name:        "routes",
				Required:    true,
				Description: "Each route the node handles.",
				Key:         "<METHOD> /<path>",
			}},
		},
		{
			Kind:        boundary.Kind{Direction: boundary.Egress, Protocol: boundary.ProtocolHTTP},
			Description: "Module that sends HTTP requests to another service, including gRPC and Connect.",
		},
	}
}
