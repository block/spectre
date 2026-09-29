# Ingress proxy implementation plan

Implement the ingress proxy from [the design](design.md). Mirror requests to the
reference and candidate, return only the reference response, and compare responses
asynchronously. Quarantine the candidate on divergence and capture structural
differences without payload values. Candidate isolation and egress control remain
separate prerequisites for safe mirroring.

## Host

- Go loads local protobuf descriptor sets and resolves gRPC methods and types.
- Go decodes unary binary gRPC and Connect ProtoJSON into consistent JS objects. Preserve
  protobuf presence, JSON field names, string-encoded 64-bit integers, and base64
  bytes. Unsupported or unknown data must not silently disappear.
- Initially load one plain JavaScript module and resolve its `spectre` import in
  [Sobek](https://github.com/grafana/sobek). TypeScript declarations, other imports,
  and esbuild compilation are deferred. Scripts never handle descriptors.
- Validate registrations before activation. Use synchronous callbacks, a fresh runtime
  per comparison, bounded workers, payload limits, and execution deadlines.
- Keep bootstrap in `cmd/<command>` and application logic under `internal`, with
  explicit dependencies and no global state.

## Normalisers

A scripts directory defines comparison behaviour. The host loads every `.js` file
in it, including subdirectories, as one set. Scripts can import each other by
relative path to share helpers, and each script runs once. The host supplies a
`spectre` module with two registration functions:

| Function | Example target | Normaliser receives |
| --- | --- | --- |
| `field(target, normaliser)` | `example.users.v1.ListUsersResponse.users[].name` | One field value |
| `message(target, normaliser)` | `example.users.v1.User` | One message value |

Normalisers are global. Each applies wherever its target appears, and a response
message normaliser covers the whole response body. gRPC and Connect requests name
their method in the path. A raw HTTP request is typed by an RPC method declared with
`ingress(pattern, method)`, such as
`ingress("GET /v2/forecast", "spectre.sample.v1.WeatherService.GetForecastV2")`.

Register ordinary functions during module evaluation; the host validates targets
against its schema. No exports or filename conventions are required. The same
function can be registered for multiple targets. Registration order does not affect
execution order.

Reject invalid or conflicting endpoints and duplicate registrations at startup.
Unresolved targets and endpoint methods fail when the schema loads, so the proxy
never becomes ready. A field normaliser replaces a message normaliser at the same
location.

A normaliser takes one directly typed value and returns its normalised form. A
missing value arrives as `undefined`. Returning a value replaces the node, returning
a constant ignores it, and returning `undefined` removes it. Results must be
representable as JSON.

The host normalises each payload independently, from leaves upward. Each payload gets a fresh runtime, so its normalised
form depends only on its content. Arguments are protected from mutation. Parents
receive children that are already normalised.

Two payloads are equivalent when their normalised forms are structurally equal.
Differences are reported as paths without values. Errors and results that are not
representable as JSON mean unable to compare.

For example, field normalisers can sort user roles and remove a response-generation
timestamp. Default comparison still checks user IDs, names, and user ordering.

## Implementation checklist

### 1. Resolve comparison and transport decisions

- [x] Pass missing field values as JavaScript `undefined`, distinct from present
  protobuf default values.
- [x] Normalise each payload independently so unordered arrays need no pairing.
- [x] Start with unary binary gRPC and Connect JSON. Defer gRPC-Web and streaming.
- [ ] Define the policy for unable-to-compare outcomes, quarantine recovery,
  application metadata, and capture delivery failures.

### 2. Load schemas and prepare typed payloads

- [x] Load local descriptor sets and resolve imported files, messages, and RPC
  methods without relying on process-global registrations.
- [x] Reject malformed or empty descriptor sets, missing imports, duplicate
  definitions, and unresolved types; test loading and lookup failures.
- [x] Decode binary protobuf and ProtoJSON into consistent JS objects, preserving
  presence, JSON field names, string-encoded 64-bit integers, and base64 bytes.
- [x] Handle unsupported and unknown data explicitly without silently dropping it.
- [ ] Generate TypeScript declarations matching the decoded objects and directly
  typed normaliser arguments, without exposing descriptors to scripts.
- [ ] Test the host's binary/JSON decoding equivalence, presence, precision,
  nested collections, and unsupported or unknown data handling.

### 3. Load and execute normaliser scripts

- [ ] Embed esbuild to compile a TypeScript entry file and optional helper imports
  at load time.
- [x] Run plain JavaScript modules in Sobek and provide `field`, `message`, and
  `ingress` registration functions from the imported `spectre` module.
- [ ] Validate targets against the schema and reject unresolved targets and
  duplicate registrations before activation.
- [ ] Support registering the same ordinary function for multiple targets without
  requiring exports or filename conventions.
- [x] Enforce synchronous callbacks with one direct argument and JSON results.
  Treat errors and results that are not JSON as unable to compare.
- [x] Use a fresh runtime for each payload and protect payloads from mutation.
- [x] Bound comparison work and payload sizes, and enforce execution deadlines.
- [ ] Add TS7 type-checking to development and CI through `bit`; keep esbuild
  responsible only for transpilation.
- [ ] Test script loading, invalid registrations, runtime isolation, immutable
  inputs, callback failures, deadlines, and capacity limits.

### 4. Dispatch normalisers and compare normalised structure

- [x] Traverse payloads from leaves upward, using the agreed missing-value rules
  independently of registration order.
- [x] Let field normalisers replace message normalisers at the same location.
- [x] Pass normalised children to parents.
- [x] Replace or remove normalised fields and subtrees in temporary payload copies.
- [x] Structurally compare the normalised payloads.
- [ ] Report equivalence only when the normalised payloads match; keep divergence
  distinct from unable-to-compare outcomes.
- [ ] Test precedence, normaliser ordering, repeated-message occurrences, parent
  inputs, and structural comparison of normalised payloads.
- [ ] Test unordered role comparison and ignored timestamps while still checking
  user IDs, names, and user ordering.

### 5. Integrate forwarding, quarantine, and capture

- [ ] Add ingress startup and configuration under `cmd/spectre-ingress`, keeping
  application logic under `internal` with explicit dependencies and no global state.
- [ ] Implement forwarding for the agreed transports, preserving reference
  response fidelity, metadata, status, deadlines, and cancellation.
- [ ] Track each invocation separately from its trace ID and associate its
  reference and candidate results.
- [ ] Mirror requests to the candidate with bounded work and return the reference
  response without waiting for the candidate or comparison.
- [ ] Compare RPC status separately from response bodies and dispatch body
  comparisons asynchronously.
- [ ] Apply the agreed policy when candidate execution or comparison fails.
- [ ] Quarantine the candidate on divergence and stop further candidate traffic,
  including under concurrent requests; implement the agreed recovery policy.
- [ ] Capture structural difference paths without payload values and apply the
  agreed delivery-failure policy. Quarantine must not depend on capture delivery.
- [ ] Test forwarding fidelity, invocation correlation, cancellation, capacity
  limits, quarantine races, and capture failures.
- [ ] Test that candidate and comparator failures do not prevent or delay delivery
  of the reference response.
- [ ] Verify candidate isolation and egress control as separate prerequisites
  before enabling mirroring against services that can produce side effects.

## Open decisions

- Unordered-array pairing before child comparisons. Positional child failures
  cannot later be erased by an unordered parent rule.
- Stream-message pairing when streaming comparison is added.
- Quarantine recovery, application metadata,
  and capture delivery failures.
