package comparison_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/comparison/javascript/javascripttest"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// declarations mirror the ProtoJSON shape of the test.v1 protobuf package, with optional
// members so tests send only what they compare, plus a raw module of null members.
const declarations = `
declare module "test.v1" {
  export interface Request {}
  export interface Filter { minDays?: number; }
  export interface Query {
    location?: string;
    days?: number;
    tags?: string[];
    filter?: import("test.v1").Filter;
    trace?: string;
  }
  export interface User { name: string; }
  export interface Response {
    stable?: string;
    ignored?: string;
    roles?: string[];
    users?: import("test.v1").User[];
    count?: string;
    owner?: import("test.v1").User | null;
    labels?: Record<string, string>;
  }
  export interface Service {
    Get(request: import("test.v1").Request): import("test.v1").Response;
  }
}

declare module "raw" {
  interface Response {
    stable: string;
    ignored?: string;
    count?: number;
    enabled?: boolean;
    roles?: string[];
    metadata?: Record<string, string>;
  }
  interface Fee {
    plan: string | null;
    note?: string | null;
    tier: "A" | "B" | null;
    holder?: Holder | null;
  }
  interface Holder {
    id: string;
  }
}
`

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*comparison.Config){
		"ScriptsDir": func(config *comparison.Config) { config.ScriptsDir = "" },
		"SchemaDir":  func(config *comparison.Config) { config.Schema.SchemaDir = "" },
		"Timeout":    func(config *comparison.Config) { config.ComparisonTimeout = 0 },
	}
	for name, update := range tests {
		t.Run(name, func(t *testing.T) {
			config := newConfig(t, map[string]string{"test.ts": module("")})
			update(&config)
			scripts, err := comparison.New(t.Context(), config, bindings())
			assert.Error(t, err)
			assert.Equal(t, (*comparison.Scripts)(nil), scripts)
		})
	}
}

func TestRejectsInvalidScripts(t *testing.T) {
	for name, test := range map[string]struct {
		scripts map[string]string
		message string
	}{
		"MissingSpectreImport": {
			scripts: map[string]string{"test.ts": `
				import type * as v1 from "test.v1";
				spectre.field<v1.Response, "ignored">(() => undefined);
			`},
			message: "Cannot find name 'spectre'",
		},
		"UnsupportedModuleImport": {
			scripts: map[string]string{"test.ts": `import "unsupported";`},
			message: "compile comparison scripts",
		},
		"DuplicateTargetAcrossScripts": {
			scripts: map[string]string{
				"a.ts": module(`spectre.message<v1.User>((user) => user);`),
				"b.ts": module(`spectre.message<v1.User>((user) => user);`),
			},
			message: `duplicate normaliser target "test.v1.User"`,
		},
		// The cast skips type checking, so the plan's own check rejects the path.
		"UnknownNormaliserField": {
			scripts: map[string]string{"test.ts": module(`(spectre.field as any)("test.v1.Response", "unknown", () => true);`)},
			message: `prepare comparison plan`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			scripts, err := comparison.New(t.Context(), newConfig(t, test.scripts), bindings())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
			assert.Equal(t, (*comparison.Scripts)(nil), scripts)
		})
	}
}

func TestRejectsEndpointsAbsentFromSchema(t *testing.T) {
	for name, test := range map[string]struct {
		typeName string
		pattern  string
	}{
		"Missing":   {typeName: "test.v1.Missing", pattern: "GET weather.example/v1/forecast"},
		"Operation": {typeName: "test.v1.Service.Get", pattern: "GET weather.example/v1/forecast"},
		"NotAName":  {typeName: "not a name", pattern: "GET weather.example/v1/forecast"},
	} {
		t.Run(name, func(t *testing.T) {
			// The cast skips type checking, so the type reaches the schema check.
			config := newConfig(t, map[string]string{
				"weather.ts": module(`(spectre.egress.fake("` + test.pattern + `").match as any)("` + test.typeName + `");`),
			})
			_, err := comparison.New(t.Context(), config, bindings())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `resolve endpoint "`+test.pattern+`"`)
			assert.Contains(t, err.Error(), `type "`+test.typeName+`" is not declared`)
		})
	}
}

func TestLoadsEveryScriptAsOneSet(t *testing.T) {
	scripts := newScriptsFromFiles(t, map[string]string{
		"users/user.ts": module(`
			import {lower} from "../helpers/strings";
			spectre.message<v1.User>((user) => user === undefined ? undefined : {name: lower(user.name)});
			spectre.egress.fake("GET weather.example/v1/forecast").match<v1.Query>();
		`),
		"weather.ts": module(`
			import {lower} from "./helpers/strings";
			spectre.field<v1.Response, "stable">((stable) => stable === undefined ? undefined : lower(stable));
		`),
		"helpers/strings.ts": `export const lower = (value: string) => value.toLowerCase();`,
		"README.md":          "Not a script.",
	})
	endpoints := []string{}
	for _, endpoint := range scripts.Endpoints() {
		endpoints = append(endpoints, fmt.Sprintf("%s %s %q %q", endpoint.Direction(), endpoint.Protocol(), endpoint.Pattern(), endpoint.Type()))
	}
	assert.Equal(t, []string{`egress fake "GET weather.example/v1/forecast" "test.v1.Query"`}, endpoints)

	differences, truncated, err := scripts.Compare(t.Context(),
		instance(t, scripts, response, `{"stable":"SAME","owner":{"name":"ALICE"}}`),
		instance(t, scripts, response, `{"stable":"same","owner":{"name":"alice"}}`))
	assert.NoError(t, err)
	assert.Equal(t, []string{}, model.DifferenceLines(differences, truncated))
}

func TestCompareAppliesNormalisers(t *testing.T) {
	for name, test := range map[string]struct {
		script    string
		typeName  schema.TypeRef
		reference string
		candidate string
		expected  []string
	}{
		"IgnoreAndSortFields": {
			typeName: response,
			script: `
				spectre.field<v1.Response, "ignored">(() => undefined);
				spectre.field<v1.Response, "roles">((roles) => roles?.sort());
			`,
			reference: `{"stable":"same","ignored":"first","roles":["reader","writer"]}`,
			candidate: `{"roles":["writer","reader"],"ignored":"second","stable":"same"}`,
		},
		"Int64IsJavaScriptString": {
			typeName:  response,
			script:    `spectre.field<v1.Response, "count">((count) => typeof count);`,
			reference: `{"count":"9007199254740993"}`,
			candidate: `{"count":"9007199254740994"}`,
		},
		"UndefinedForMissingField": {
			typeName:  response,
			script:    `spectre.field<v1.Response, "ignored">((value) => value === undefined ? "present" : value);`,
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same","ignored":"present"}`,
		},
		"DifferencesRemainAfterNormalisation": {
			typeName:  response,
			script:    `spectre.field<v1.Response, "ignored">((value) => value?.toLowerCase());`,
			reference: `{"stable":"same","ignored":"First"}`,
			candidate: `{"stable":"same","ignored":"second"}`,
			expected:  []string{"~ $.ignored"},
		},
		"ParentReceivesNormalisedChildren": {
			typeName: response,
			script: `
				spectre.field<v1.Response, "ignored">((value) => value?.toLowerCase());
				// Normalisers may return any JSON, so the result need not be a Response.
				spectre.message<v1.Response>((response): any => ({ignored: response?.ignored}));
			`,
			reference: `{"stable":"first","ignored":"SAME"}`,
			candidate: `{"stable":"second","ignored":"same"}`,
		},
		"MessageNormaliserCanRemoveRoot": {
			typeName:  response,
			script:    `spectre.message<v1.Response>(() => undefined);`,
			reference: `{"stable":"first"}`,
			candidate: `{"stable":"second"}`,
		},
		"FieldNormaliserReplacesMessageNormaliserAtSameLocation": {
			typeName: response,
			script: `
				spectre.message<v1.User>(() => { throw new Error("message normaliser ran"); });
				spectre.field<v1.Response, "owner">((owner) => owner && { name: owner.name.toLowerCase() });
			`,
			reference: `{"owner":{"name":"ALICE"}}`,
			candidate: `{"owner":{"name":"alice"}}`,
		},
		"MessageNormaliserReceivesUndefinedForMissingMessage": {
			typeName:  response,
			script:    `spectre.message<v1.User>(() => ({ name: "ignored" }));`,
			reference: `{"stable":"same","owner":{"name":"alice"}}`,
			candidate: `{"stable":"same"}`,
		},
		"FieldNormaliserAppliesToRepeatedMessageElements": {
			typeName:  response,
			script:    `spectre.field<v1.Response, "users[].name">((name) => name?.toLowerCase());`,
			reference: `{"stable":"same","users":[{"name":"ALICE"},{"name":"BOB"}]}`,
			candidate: `{"stable":"same","users":[{"name":"alice"},{"name":"bob"}]}`,
		},
		"NormalisedTypeLetsFieldNormaliserRemoveRequiredField": {
			typeName: response,
			script: `
				interface NormalisedUser { name?: string }
				spectre.message<v1.User, NormalisedUser>();
				spectre.field<NormalisedUser, "name">(() => undefined);
			`,
			reference: `{"owner":{"name":"ALICE"},"users":[{"name":"ALICE"},{"name":"BOB"}]}`,
			candidate: `{"owner":{"name":"alice"},"users":[{"name":"alice"},{"name":"bob"}]}`,
		},
		"MessageNormaliserReceivesNormalisedType": {
			typeName: response,
			script: `
				interface NormalisedUser { name?: string }
				spectre.field<NormalisedUser, "name">(() => undefined);
				spectre.message<v1.User, NormalisedUser>((user) => user);
			`,
			reference: `{"owner":{"name":"ALICE"}}`,
			candidate: `{"owner":{"name":"alice"}}`,
		},
		"MessageNormaliserCanRemoveRepeatedElement": {
			typeName: response,
			script: `spectre.message<v1.User>((user) =>
				user === undefined || user.name === "ignored" ? undefined : {name: user.name.toLowerCase()});`,
			reference: `{"users":[{"name":"ALICE"},{"name":"ignored"},{"name":"BOB"}]}`,
			candidate: `{"users":[{"name":"alice"},{"name":"bob"}]}`,
		},
		"WithoutNormalisers": {
			typeName:  response,
			reference: `{"stable":"first"}`,
			candidate: `{"stable":"second"}`,
			expected:  []string{"~ $.stable"},
		},
		"UntypedSkipsNormalisers": {
			script:    `spectre.field<v1.Response, "ignored">(() => undefined);`,
			typeName:  unknownType,
			reference: `{"stable":"same","ignored":"first"}`,
			candidate: `{"stable":"same","ignored":"second"}`,
			// Untyped keys are payload data, so no path quotes them.
			expected: []string{"~ $[*]"},
		},
		"MapKeysAreNotReported": {
			typeName:  response,
			reference: `{"stable":"same","labels":{"alice@example.com":"a"}}`,
			candidate: `{"stable":"same","labels":{"alice@example.com":"b","bob@example.com":"c"}}`,
			expected:  []string{"~ $.labels[*]", "+ $.labels[*]"},
		},
		"DeclaredFieldsUnderListsAreReported": {
			typeName:  response,
			reference: `{"stable":"same","users":[{"name":"alice"}]}`,
			candidate: `{"stable":"same","users":[{"name":"bob"}]}`,
			expected:  []string{"~ $.users[0].name"},
		},
		"MissingField": {
			typeName:  response,
			reference: `{"stable":"same","ignored":"first"}`,
			candidate: `{"stable":"same"}`,
			expected:  []string{"- $.ignored"},
		},
		"RetypedField": {
			typeName:  response,
			reference: `{"stable":"same","owner":{"name":"alice"}}`,
			candidate: `{"stable":"same","owner":null}`,
			expected:  []string{"! $.owner object → null"},
		},
		"NullMembersAreEqual": {
			typeName:  "raw.Fee",
			reference: `{"plan":null,"note":null,"tier":null}`,
			candidate: `{"plan":null,"note":null,"tier":null}`,
		},
		"NullAgainstStringIsRetyped": {
			typeName:  "raw.Fee",
			reference: `{"plan":null,"note":"n","tier":"A"}`,
			candidate: `{"plan":"p","note":null,"tier":null}`,
			expected:  []string{"! $.note string → null", "! $.plan null → string", "! $.tier string → null"},
		},
		"NullMemberIsMissingWhenAbsent": {
			typeName:  "raw.Fee",
			reference: `{"plan":null,"note":null,"tier":null}`,
			candidate: `{"plan":null,"tier":null}`,
			expected:  []string{"- $.note"},
		},
		"NormaliserReceivesNullMember": {
			typeName: "raw.Fee",
			script: `
				spectre.field<raw.Fee, "plan">((plan) => plan === null ? "none" : plan);
				spectre.field<raw.Fee, "tier">((tier) => tier === null ? "A" : tier);
			`,
			reference: `{"plan":null,"tier":null}`,
			candidate: `{"plan":"none","tier":"A"}`,
		},
		"NormalisersSkipNullObjectMember": {
			typeName: "raw.Fee",
			script: `
				spectre.field<raw.Holder, "id">((id) => id.trim());
				spectre.field<raw.Fee, "holder.id">((): any => "constant");
				spectre.message<raw.Holder>((holder): any => {
					if (holder === null) throw new Error("normalised a null holder");
					return holder;
				});
			`,
			reference: `{"plan":null,"tier":null,"holder":null}`,
			candidate: `{"plan":null,"tier":null,"holder":null}`,
		},
		"NullObjectMemberAgainstObjectIsRetyped": {
			typeName:  "raw.Fee",
			script:    `spectre.field<raw.Holder, "id">((id) => id.trim());`,
			reference: `{"plan":null,"tier":null,"holder":null}`,
			candidate: `{"plan":null,"tier":null,"holder":{"id":" h "}}`,
			expected:  []string{"! $.holder null → object"},
		},
		"SameFieldInEveryElementIsMerged": {
			typeName:  response,
			reference: `{"stable":"same","users":[{"name":"alice"},{"name":"bob"}]}`,
			candidate: `{"stable":"same","users":[{"name":"carol"},{"name":"dave"}]}`,
			expected:  []string{"~ $.users[*].name ×2"},
		},
		"ExtraElementsAreCounted": {
			typeName:  response,
			reference: `{"stable":"same","users":[{"name":"alice"},{"name":"bob"},{"name":"carol"}]}`,
			candidate: `{"stable":"same","users":[{"name":"alice"}]}`,
			expected:  []string{"- $.users[*] ×2"},
		},
		"TooManyDifferencesAreTruncated": {
			typeName:  response,
			reference: `{"stable":"same","users":[` + strings.Repeat(`{"name":"alice"},`, 100) + `{"name":"alice"}]}`,
			candidate: `{"stable":"same","users":[` + strings.Repeat(`{"name":"bob"},`, 100) + `{"name":"bob"}]}`,
			expected:  []string{"~ $.users[*].name ×100", "… more"},
		},
		"ExplicitDefaultsRemainPresent": {
			typeName:  "raw.Response",
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same","ignored":"","count":0,"enabled":false,"roles":[],"metadata":{}}`,
			expected:  []string{"+ $.count", "+ $.enabled", "+ $.ignored", "+ $.metadata", "+ $.roles"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			scripts := newScripts(t, test.script)
			expected := test.expected
			if expected == nil {
				expected = []string{}
			}

			differences, truncated, err := scripts.Compare(t.Context(),
				instance(t, scripts, test.typeName, test.reference), instance(t, scripts, test.typeName, test.candidate))

			assert.NoError(t, err)
			assert.Equal(t, expected, model.DifferenceLines(differences, truncated))
		})
	}
}

func TestCompareRejectsNonJSONNormaliserResult(t *testing.T) {
	scripts := newScripts(t, `spectre.message<v1.Response>((): any => () => true);`)

	differences, _, err := scripts.Compare(t.Context(), instance(t, scripts, response, `{"stable":"same"}`), instance(t, scripts, response, `{"stable":"same"}`))

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "normalise reference")
	assert.Equal(t, []model.Difference(nil), differences)
}

// A normaliser returns the type it receives, so a result of another type is an error.
func TestCompareRejectsNormaliserResultOfAnotherType(t *testing.T) {
	for name, test := range map[string]struct {
		script string
		error  string
	}{
		"Field": {
			script: `spectre.field<raw.Response, "stable">((): any => ({replacement: true}));`,
			error:  "normalised value is not string: $: expected string, found an object",
		},
		"Message": {
			script: `spectre.message<raw.Response>((): any => [true]);`,
			error:  "normalised value is not raw.Response: $: expected raw.Response, found an array",
		},
		"NullMessage": {
			script: `spectre.message<raw.Response>((): any => null);`,
			error:  "normalised value is not raw.Response: $: expected raw.Response, found null",
		},
	} {
		t.Run(name, func(t *testing.T) {
			scripts := newScripts(t, test.script)

			_, _, err := scripts.Compare(t.Context(), instance(t, scripts, "raw.Response", `{"stable":"first"}`),
				instance(t, scripts, "raw.Response", `{"stable":"second"}`))

			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.error)
		})
	}
}

func TestCompareRejectsRemovingRequiredField(t *testing.T) {
	// The optional owner lets TypeScript accept undefined, but User requires its name.
	scripts := newScripts(t, `spectre.field<v1.Response, "owner.name">(() => undefined);`)

	_, _, err := scripts.Compare(t.Context(), instance(t, scripts, response, `{"owner":{"name":"alice"}}`),
		instance(t, scripts, response, `{"owner":{"name":"alice"}}`))

	assert.Error(t, err)
	assert.Contains(t, err.Error(), `remove normalised value: type "test.v1.User" requires field "name"`)
}

func TestRejectsInvalidNormalisedTypes(t *testing.T) {
	normalisedUser := `interface NormalisedUser { name?: string } spectre.message<v1.User, NormalisedUser>();`
	for name, test := range map[string]struct {
		script string
		error  string
	}{
		"MissingField": {
			script: `interface Empty {} spectre.message<v1.User, Empty>();`,
			error:  `"Empty" must declare field "name" of "test.v1.User"`,
		},
		"ExtraField": {
			script: `interface Extra { name: string; extra?: string } spectre.message<v1.User, Extra>();`,
			error:  `"Extra" declares field "extra", which "test.v1.User" does not have`,
		},
		"RequiredField": {
			script: `interface Required { minDays: number } spectre.message<v1.Filter, Required>();`,
			error:  `field "minDays" of "Required" must be optional, as it is in "test.v1.Filter"`,
		},
		"OtherType": {
			script: `interface Numbered { name?: number } spectre.message<v1.User, Numbered>();`,
			error:  `field "name" of "Numbered" must have type string`,
		},
		"NestedTypeNotNormalised": {
			script: normalisedUser + `
				interface NormalisedResponse {
					stable?: string; ignored?: string; roles?: string[]; users?: v1.User[]; count?: string;
					owner?: NormalisedUser | null; labels?: Record<string, string>;
				}
				spectre.message<v1.Response, NormalisedResponse>();
			`,
			error: `field "users" of "NormalisedResponse" must have type NormalisedUser[]`,
		},
		"FieldNamesIncomingType": {
			script: normalisedUser + `spectre.field<v1.User, "name">((name) => name);`,
			error:  `"test.v1.User" normalises to "NormalisedUser", which field normalisers must name instead`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := comparison.New(t.Context(), newConfig(t, map[string]string{"test.ts": module(test.script)}), bindings())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.error)
		})
	}
}

func TestInterruptsRunawayNormaliser(t *testing.T) {
	config := newConfig(t, map[string]string{
		"test.ts": module(`spectre.message<v1.Response>(() => { while (true) {} });`),
	})
	config.ComparisonTimeout = 10 * time.Millisecond
	scripts, err := comparison.New(t.Context(), config, bindings())
	assert.NoError(t, err)

	payload := instance(t, scripts, response, `{"stable":"same"}`)
	_, _, err = scripts.Compare(t.Context(), payload, payload)
	assert.IsError(t, err, context.DeadlineExceeded)

	_, err = scripts.Hash(t.Context(), "identity", "reference", payload)
	assert.IsError(t, err, context.DeadlineExceeded)
}

func TestEvaluatesInFreshEvaluator(t *testing.T) {
	scripts := newScripts(t, `spectre.message<v1.Response>(() => ({ stable: "changed" }));`)
	payload := instance(t, scripts, response, `{"stable":"same"}`)
	normalise := func() (Option[schema.Value], error) {
		var normalised Option[schema.Value]
		err := scripts.Evaluate(t.Context(), func(evaluator *javascript.Evaluator) error {
			var err error
			normalised, err = evaluator.NormaliseMessage(response, Some(payload))
			return err
		})
		return normalised, err
	}

	first, err := normalise()
	assert.NoError(t, err)
	assert.Equal(t, `{"stable":"changed"}`, jsonOf(t, first))
	second, err := normalise()
	assert.NoError(t, err)
	assert.Equal(t, first, second)

	failure := errors.New("failed")
	assert.IsError(t, scripts.Evaluate(t.Context(), func(*javascript.Evaluator) error { return failure }), failure)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.Error(t, scripts.Evaluate(ctx, func(*javascript.Evaluator) error { return nil }))
}

func jsonOf(t *testing.T, value Option[schema.Value]) string {
	t.Helper()
	present, ok := value.Get()
	assert.True(t, ok)
	encoded, err := json.Marshal(present)
	assert.NoError(t, err)
	return string(encoded)
}

func TestLogsIndividualNormaliserResults(t *testing.T) {
	var output bytes.Buffer
	ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	config := newConfig(t, map[string]string{"test.ts": module(`
		spectre.field<v1.Response, "ignored">(() => undefined);
		spectre.field<v1.Response, "stable">((value) => value);
	`)})
	scripts, err := comparison.New(ctx, config, bindings())
	assert.NoError(t, err)

	differences, truncated, err := scripts.Compare(ctx,
		instance(t, scripts, response, `{"stable":"first","ignored":"first"}`),
		instance(t, scripts, response, `{"stable":"second","ignored":"second"}`))

	assert.NoError(t, err)
	assert.Equal(t, []model.Difference{{Path: "$.stable", Change: model.ChangeChanged, Count: 1}}, differences)
	assert.False(t, truncated)
	logs := output.String()
	assert.Contains(t, logs, `"msg":"Payload normaliser completed","kind":"field","target":"test.v1.Response.ignored","side":"reference","payload_path":"$.ignored"`)
	assert.Contains(t, logs, `"msg":"Payload normaliser completed","kind":"field","target":"test.v1.Response.stable","side":"candidate","payload_path":"$.stable"`)
	assert.Contains(t, logs, `"msg":"Payload normalisation completed","message":"test.v1.Response","side":"reference","normalisers":2`)
}

func TestLogsHashNormalisationWithoutNormalisers(t *testing.T) {
	var output bytes.Buffer
	ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	scripts, err := comparison.New(ctx, newConfig(t, map[string]string{"test.ts": module("")}), bindings())
	assert.NoError(t, err)

	_, err = scripts.Hash(ctx, "identity", "candidate", instance(t, scripts, query, `{"location":"london"}`))

	assert.NoError(t, err)
	assert.Contains(t, output.String(), `"level":"DEBUG","msg":"Payload normalisation completed","message":"test.v1.Query","side":"candidate","normalisers":0`)
}

const queryScript = `
	spectre.field<v1.Query, "trace">(() => undefined);
	spectre.field<v1.Query, "tags">((tags) => tags === undefined ? undefined : tags.sort());
`

func TestHashesIdentityAndCanonicalJSON(t *testing.T) {
	scripts := newScripts(t, queryScript)
	identity := "fake POST weather.example/v1/search"

	hash, err := scripts.Hash(t.Context(), identity, "reference", instance(t, scripts, query, `{"trace":"ignored","location":"london","days":3}`))

	assert.NoError(t, err)
	expected := sha256.Sum256([]byte(identity + "\x00" + `{"days":3,"location":"london"}`))
	assert.Equal(t, comparison.Hash(expected), hash)
}

func TestHashIsStable(t *testing.T) {
	type hashed struct {
		identity string
		typeName schema.TypeRef
		payload  string
	}
	queried := func(payload string) hashed {
		return hashed{identity: "fake POST weather.example/v1/search", typeName: query, payload: payload}
	}
	scripts := newScripts(t, queryScript)
	for name, test := range map[string]struct {
		left  hashed
		right hashed
		equal bool
	}{
		"FieldOrder": {
			left:  queried(`{"location":"london","days":3}`),
			right: queried(`{"days":3,"location":"london"}`),
			equal: true,
		},
		"IgnoredField": {
			left:  queried(`{"location":"london","trace":"first"}`),
			right: queried(`{"location":"london","trace":"second"}`),
			equal: true,
		},
		"SortedCollection": {
			left:  queried(`{"tags":["wind","rain"]}`),
			right: queried(`{"tags":["rain","wind"]}`),
			equal: true,
		},
		"Value": {
			left:  queried(`{"location":"london","days":3}`),
			right: queried(`{"location":"london","days":4}`),
		},
		"ExplicitDefault": {
			left:  queried(`{"location":"london"}`),
			right: queried(`{"location":"london","days":0}`),
		},
		"Identity": {
			left:  queried(`{}`),
			right: hashed{identity: "fake GET weather.example/v1/search", typeName: query, payload: `{}`},
		},
		"UnknownSkipsNormalisers": {
			left:  hashed{identity: "fake POST weather.example/v1/search", typeName: unknownType, payload: `{"trace":"first"}`},
			right: hashed{identity: "fake POST weather.example/v1/search", typeName: unknownType, payload: `{"trace":"second"}`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			left, err := scripts.Hash(t.Context(), test.left.identity, "reference", instance(t, scripts, test.left.typeName, test.left.payload))
			assert.NoError(t, err)
			right, err := scripts.Hash(t.Context(), test.right.identity, "candidate", instance(t, scripts, test.right.typeName, test.right.payload))
			assert.NoError(t, err)
			assert.Equal(t, test.equal, left == right)
		})
	}
}

// bindings are the script bindings of the fake protocol, which declares endpoints and replacements.
func bindings() []javascript.Binding {
	return []javascript.Binding{javascripttest.Binding()}
}

func newConfig(t *testing.T, scripts map[string]string) comparison.Config {
	t.Helper()
	config := comparison.NewConfig()
	config.Schema.SchemaDir = writeFiles(t, map[string]string{"test.d.ts": declarations})
	config.ScriptsDir = writeFiles(t, scripts)
	return config
}

func newScriptsFromFiles(t *testing.T, files map[string]string) *comparison.Scripts {
	t.Helper()
	scripts, err := comparison.New(t.Context(), newConfig(t, files), bindings())
	assert.NoError(t, err)
	return scripts
}

func newScripts(t *testing.T, script string) *comparison.Scripts {
	t.Helper()
	return newScriptsFromFiles(t, map[string]string{"test.ts": module(script)})
}

// module imports the spectre API and the test schema modules, so body can name their types.
func module(body string) string {
	return `import * as spectre from "spectre"; import type * as v1 from "test.v1"; import type * as raw from "raw";` + body
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, source := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		assert.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	}
	return dir
}

// Types of the test payloads. The empty reference stands for unknown.
const (
	response    schema.TypeRef = "test.v1.Response"
	query       schema.TypeRef = "test.v1.Query"
	unknownType schema.TypeRef = ""
)

func objectType(t *testing.T, scripts *comparison.Scripts, name schema.TypeRef) *schema.ObjectType {
	t.Helper()
	declared, err := scripts.Schema().Type(name)
	assert.NoError(t, err)
	return declared
}

// instance decodes a payload as the named object type, or as unknown for unknownType.
func instance(t *testing.T, scripts *comparison.Scripts, declared schema.TypeRef, payload string) schema.Value {
	t.Helper()
	if declared == unknownType {
		return typed(t, schema.UnknownType{}, payload)
	}
	return typed(t, objectType(t, scripts, declared), payload)
}

func typed(t *testing.T, declared schema.Type, payload string) schema.Value {
	t.Helper()
	decoded, err := schema.Decode(declared, []byte(payload))
	assert.NoError(t, err)
	return decoded
}
