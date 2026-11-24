package script_test

import (
	"testing"
	"testing/fstest"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/http/script"
)

func TestMembersCheckScriptTypes(t *testing.T) {
	declarations := fstest.MapFS{"types.d.ts": {Data: []byte(`
declare module "test" {
  export interface Root { count: number; }
}
`)}}
	for name, test := range map[string]struct {
		body  string
		valid bool
	}{
		"Valid": {
			body: `
				ingress.http("GET /root").match<Root>();
				ingress.http((request) => request.path === "/root").status((status) => status >= 500).match<Text>(({ status }) => ({ status }));
				ingress.http("/").status(500, 503).match<Root>((response) => ({ ...response, body: response.body && { ...response.body } }));
				ingress.http("/").ignore();
				ingress.http((request) => request.headers["x-synthetic"] === "true").requestHeader("Accept", (value) => value === undefined).ignore();
				ingress.http("/").requestHeader("X-Synthetic", "true").ignore();
				ingress.http("/").status(503).responseHeader("Retry-After", (value) => value !== undefined).responseHeader("X-Served-By", "rails").ignore();
				egress.http("GET example/root").match<Root>();
			`,
			valid: true,
		},
		"EgressFunction":         {body: `egress.http(() => true);`},
		"EgressStatus":           {body: `egress.http("GET example/root").status(500);`},
		"EgressNormaliser":       {body: `egress.http("GET example/root").match<Root>((request) => request);`},
		"TextBodyNotString":      {body: `ingress.http("/").match<Text>(() => ({ body: 1 }));`},
		"StatusNotNumber":        {body: `ingress.http("/").status("500");`},
		"RequestNotFunction":     {body: `ingress.http(1);`},
		"RequestHeadersReadOnly": {body: `ingress.http((request) => { request.headers["a"] = "b"; return true; });`},
		"RequestHeaderNotString": {body: `ingress.http("/").requestHeader("Accept", 1);`},
		"HeaderNotString":        {body: `ingress.http("/").responseHeader("Accept", 1);`},
		"HeaderFunctionValue":    {body: `ingress.http("/").responseHeader("Accept", (value: string) => value === "a");`},
	} {
		t.Run(name, func(t *testing.T) {
			source := `import { ingress, egress, type Text } from "spectre";
import type { Root } from "test";
` + test.body
			binding, _ := script.New()
			_, err := javascript.NewProgram(t.Context(), time.Second, declarations, fstest.MapFS{"test.ts": {Data: []byte(source)}}, nil, []javascript.Binding{binding})
			if test.valid {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "error TS")
		})
	}
}
