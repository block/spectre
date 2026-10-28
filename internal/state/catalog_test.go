package state_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

func TestCatalogGroupsByProtocolWithIngressFirst(t *testing.T) {
	kafkaEgress := support(model.Egress, "kafka")
	httpEgress := support(model.Egress, model.ProtocolHTTP)
	httpIngress := support(model.Ingress, model.ProtocolHTTP)

	catalog, err := state.NewCatalog(kafkaEgress, httpEgress, httpIngress)

	assert.NoError(t, err)
	assert.Equal(t, []state.Support{httpIngress, httpEgress, kafkaEgress}, catalog.Supports())
}

func TestCatalogRejectsKindSupportedTwice(t *testing.T) {
	_, err := state.NewCatalog(support(model.Ingress, model.ProtocolHTTP), support(model.Ingress, model.ProtocolHTTP))

	assert.EqualError(t, err, "ingress[http] is supported twice")
}

func TestCatalogSupport(t *testing.T) {
	httpIngress := support(model.Ingress, model.ProtocolHTTP)
	catalog, err := state.NewCatalog(httpIngress)
	assert.NoError(t, err)

	found, ok := catalog.Support(httpIngress.Kind)
	assert.True(t, ok)
	assert.Equal(t, httpIngress, found)

	_, ok = catalog.Support(model.IOKind{Direction: model.Egress, Protocol: model.ProtocolHTTP})
	assert.False(t, ok)
}

func support(direction model.Direction, protocol model.Protocol) state.Support {
	kind := model.IOKind{Direction: direction, Protocol: protocol}
	return state.Support{Kind: kind, Description: "Code that does " + kind.String() + "."}
}
