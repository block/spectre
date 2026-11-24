package proxies

import (
	"fmt"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript/javascripttest"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/proxy"
)

func TestNodeQuarantinerNamesNodeOfIngressRoute(t *testing.T) {
	kinds := fakeRegistry(t, newFakeProvider()).Kinds()
	const listed = "GET /compared/{name}"
	const listedWithHost = "GET api.example/compared/{name}"
	ingress := func(route model.Route) Option[model.Traffic] {
		return Some(model.Traffic{Direction: model.Ingress, Protocol: javascripttest.Protocol, Route: route})
	}
	for name, test := range map[string]struct {
		traffic Option[model.Traffic]
		node    Option[string]
	}{
		"Listed":         {traffic: ingress(listed), node: Some("forecasts")},
		"ListedWithHost": {traffic: ingress(listedWithHost), node: Some("forecasts")},
		"Unlisted":       {traffic: ingress("GET /unlisted"), node: None[string]()},
		"NoTraffic":      {traffic: None[model.Traffic](), node: None[string]()},
		"Egress": {
			traffic: Some(model.Traffic{Direction: model.Egress, Protocol: javascripttest.Protocol, Route: listed}),
			node:    None[string](),
		},
		"OtherProtocol": {
			traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: "other", Route: listed}),
			node:    None[string](),
		},
	} {
		t.Run(name, func(t *testing.T) {
			migration, err := kinds.Parse(fmt.Appendf(nil, `
[nodes.forecasts]
status = "migrated"
[nodes.forecasts.ingress.fake.routes]
%q = {}
%q = {}
`, listed, listedWithHost))
			assert.NoError(t, err)
			candidates := proxy.NewCandidates(proxy.NewCandidatesConfig())
			reason := model.QuarantineReason{Kind: model.QuarantineUnable, Traffic: test.traffic, Detail: "detail"}

			newNodeQuarantiner(candidates, Some(migration)).Quarantine(t.Context(), reason)

			reason.Node = test.node
			status, quarantined := candidates.QuarantineStatus().Get()
			assert.True(t, quarantined)
			assert.Equal(t, reason, status.Reason)
		})
	}
}
