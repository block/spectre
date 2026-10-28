package httpserver

import (
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

// routesSetting names the ingress[http] setting that lists a node's routes.
const routesSetting = "routes"

// Supports returns the kinds of IO the HTTP server proxies, described for an
// agent that sorts a service's code into them.
func Supports() []state.Support {
	return []state.Support{
		{
			Kind:        model.IOKind{Direction: model.Ingress, Protocol: model.ProtocolHTTP},
			Description: "Module that receives HTTP requests to the service, including gRPC and Connect.",
			Settings: []state.Setting{{
				Name:        routesSetting,
				Required:    true,
				Description: "Each route the node handles.",
				Key:         "<METHOD> /<path>",
				Unique:      true,
			}},
		},
		{
			Kind:        model.IOKind{Direction: model.Egress, Protocol: model.ProtocolHTTP},
			Description: "Module that sends HTTP requests to another service, including gRPC and Connect.",
		},
	}
}
