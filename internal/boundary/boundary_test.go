package boundary_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/boundary"
)

func TestKindString(t *testing.T) {
	assert.Equal(t, "ingress[http]", boundary.Kind{Direction: boundary.Ingress, Protocol: boundary.ProtocolHTTP}.String())
}

func TestCatalogGroupsByProtocolWithIngressFirst(t *testing.T) {
	kafkaEgress := support(boundary.Egress, "kafka")
	httpEgress := support(boundary.Egress, boundary.ProtocolHTTP)
	httpIngress := support(boundary.Ingress, boundary.ProtocolHTTP)

	catalog, err := boundary.NewCatalog(kafkaEgress, httpEgress, httpIngress)

	assert.NoError(t, err)
	assert.Equal(t, []boundary.Support{httpIngress, httpEgress, kafkaEgress}, catalog.Supports())
}

func TestCatalogRejectsKindSupportedTwice(t *testing.T) {
	_, err := boundary.NewCatalog(support(boundary.Ingress, boundary.ProtocolHTTP), support(boundary.Ingress, boundary.ProtocolHTTP))

	assert.EqualError(t, err, "ingress[http] is supported twice")
}

func support(direction boundary.Direction, protocol boundary.Protocol) boundary.Support {
	kind := boundary.Kind{Direction: direction, Protocol: protocol}
	return boundary.Support{Kind: kind, Description: "Code that does " + kind.String() + "."}
}
