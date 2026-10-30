package javascript_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison/javascript"
)

func TestWriteModuleDeclaration(t *testing.T) {
	output := filepath.Join(t.TempDir(), "dist", "spectre.d.ts")
	assert.NoError(t, javascript.WriteModuleDeclaration(output))
	contents, err := os.ReadFile(output)
	assert.NoError(t, err)
	assert.Equal(t, javascript.ModuleDeclaration(), string(contents))
}

func TestModuleDeclarationChecksScriptTypes(t *testing.T) {
	declarations := fstest.MapFS{"types.d.ts": {Data: []byte(`
declare module "test" {
  export interface Root {
    count: number;
    state: "on" | "off";
    address?: Address;
    addresses: Address[];
    preferences: Record<string, Preference>;
    parent?: Root;
    "literal.key": string;
    "literal[]": string;
    "": string;
  }
  export interface Address { postalCode: string; }
  export interface Preference { enabled?: boolean; }
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
				ingress.http("/").status(503).header("Retry-After", (value) => value !== undefined).header("X-Served-By", "rails").ignore();
				egress.http("GET example/root").match<Root>();
				field<Root, "count">((value: number) => value + 1);
				field<Root, "address.postalCode">((value: string | undefined) => value?.trim());
				field<Root, "addresses[].postalCode">((value: string) => value.trim());
				field<Root, "state">((value: "on" | "off") => value);
				field<Root, "preferences">((value) => ({ ...value }));
				field<Root, "parent">((value) => value);
				message<Root>((value) => value);
				field<Address, "postalCode">(() => undefined);
			`,
			valid: true,
		},
		"InferredTypes":         {body: `field<Root, "addresses[].postalCode">((value) => value.trim()); field<Root, "address">((value) => value && { ...value });`, valid: true},
		"EgressFunction":        {body: `egress.http(() => true);`},
		"EgressStatus":          {body: `egress.http("GET example/root").status(500);`},
		"EgressNormaliser":      {body: `egress.http("GET example/root").match<Root>((request) => request);`},
		"NotObject":             {body: `ingress.http("GET /root").match<string>();`},
		"TextBodyNotString":     {body: `ingress.http("/").match<Text>(() => ({ body: 1 }));`},
		"StatusNotNumber":       {body: `ingress.http("/").status("500");`},
		"RequestNotFunction":    {body: `ingress.http(1);`},
		"HeaderNotString":       {body: `ingress.http("/").header("Accept", 1);`},
		"HeaderFunctionValue":   {body: `ingress.http("/").header("Accept", (value: string) => value === "a");`},
		"UnknownPath":           {body: `field<Root, "missing">((value) => value);`},
		"OtherTypePath":         {body: `field<Root, "enabled">((value) => value);`},
		"LiteralDotKey":         {body: `field<Root, "literal.key">((value) => value);`},
		"LiteralBracketKey":     {body: `field<Root, "literal[]">((value) => value);`},
		"EmptyKey":              {body: `field<Root, "">((value) => value);`},
		"MissingListExpansion":  {body: `field<Root, "addresses.postalCode">((value) => value);`},
		"MapExpansion":          {body: `field<Root, "preferences.enabled">((value) => value);`},
		"CycleExpansion":        {body: `field<Root, "parent.count">((value) => value);`},
		"WrongInput":            {body: `field<Root, "count">((value: string) => value);`},
		"WrongOutput":           {body: `field<Root, "count">(() => "changed");`},
		"OptionalAncestor":      {body: `field<Root, "address.postalCode">((value: string) => value);`},
		"OptionalLeaf":          {body: `field<Preference, "enabled">((value: boolean) => value);`},
		"WrongEnum":             {body: `field<Root, "state">(() => "other");`},
		"MessageMissingAllowed": {body: `message<Root>((value: object) => undefined);`},
		"WrongMessageOutput":    {body: `message<Root>(() => ({ count: "changed" }));`},
	} {
		t.Run(name, func(t *testing.T) {
			source := `import { ingress, egress, field, message, type Text } from "spectre";
import type { Address, Preference, Root } from "test";
` + test.body
			_, err := javascript.NewProgram(t.Context(), time.Second, declarations, fstest.MapFS{"test.ts": {Data: []byte(source)}})
			if test.valid {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "error TS")
		})
	}
}
