package http_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	httpprotocol "github.com/block/spectre/internal/protocols/http"
)

func TestProviderListensOnIngressAndBothEgressListeners(t *testing.T) {
	config := httpprotocol.NewConfig()
	config.Listen = "127.0.0.1:1"
	config.ReferenceEgressListen = "127.0.0.1:2"
	config.CandidateEgressListen = "127.0.0.1:3"
	assert.Equal(t, []protocols.Listen{
		{Name: "ingress", Address: "127.0.0.1:1", Transport: protocols.Stream},
		{Name: "reference-egress", Address: "127.0.0.1:2", Transport: protocols.Stream},
		{Name: "candidate-egress", Address: "127.0.0.1:3", Transport: protocols.Stream},
	}, httpprotocol.NewProvider().Listens(config))
}

func TestProviderDescribesIngressAndEgress(t *testing.T) {
	provider := httpprotocol.NewProvider()
	assert.Equal(t, model.Protocol("http"), provider.Protocol())
	directions := []model.Direction{}
	for _, kind := range provider.Kinds() {
		directions = append(directions, kind.Direction)
	}
	assert.Equal(t, []model.Direction{model.Ingress, model.Egress}, directions)
}

func TestProviderNeedsBothSides(t *testing.T) {
	_, err := httpprotocol.NewProvider().New(t.Context(), httpprotocol.NewConfig(), protocols.Runtime{Ingress: None[protocols.Side]()})
	assert.EqualError(t, err, "HTTP needs an ingress side and an egress side")
}
