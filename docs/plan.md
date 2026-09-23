# Ingress proxy implementation plan

Implement the ingress proxy from [the design](design.md). Mirror requests to the
reference and candidate, return only the reference response, and compare responses
asynchronously. Quarantine the candidate on divergence and capture structural
differences without payload values. Candidate isolation and egress control remain
separate prerequisites for safe mirroring.

## Host

- Go loads local protobuf descriptor sets and resolves gRPC methods and types.
- Go decodes binary protobuf and ProtoJSON into consistent JS objects. Preserve
  protobuf presence, JSON field names, string-encoded 64-bit integers, and base64
  bytes. Unsupported or unknown data must not silently disappear.
- Generate TypeScript declarations matching those objects. Scripts never handle
  descriptors.
- Embed [esbuild](https://esbuild.github.io/api/#go) to compile TypeScript at load
  time. Run the JavaScript in [Sobek](https://github.com/grafana/sobek). Use TS7 for
  type-checking during development and CI; esbuild only transpiles.
- Validate registrations before activation. Use synchronous callbacks, a fresh runtime
  per comparison, bounded workers, payload limits, and execution deadlines.
- Keep bootstrap in `cmd/<command>` and application logic under `internal`, with
  explicit dependencies and no global state.

## Comparators

One TypeScript file defines comparison behaviour, with optional helper imports.
The host supplies a `spectre` module with three registration functions:

| Function | Example target | Comparator receives |
| --- | --- | --- |
| `field(target, comparator)` | `example.users.v1.ListUsersResponse.users[].name` | Two field values |
| `message(target, comparator)` | `example.users.v1.User` | Two message values |
| `rpc(target, comparator)` | `example.users.v1.UserService.ListUsers` | Two response bodies |

Register ordinary functions during module evaluation; the host validates targets
against its schema. No exports or filename conventions are required. The same
function can be registered for multiple targets. Registration order does not affect
execution order.

Reject unresolved targets and duplicate registrations at startup. A field comparator
takes precedence over a message comparator at the same location. When both a
response-message comparator and an RPC comparator apply, run the message comparator
first and the RPC comparator last. Preserve failures from both.

Comparators take two directly typed parameters, reference and candidate, and return
booleans. There are no comparator objects, argument wrappers, normalisation hooks,
or delegation callbacks. Functions can normalise temporary copies internally.

The host runs custom comparators from leaves upward, with the optional RPC function
last. Payloads remain intact and protected from mutation. Go tracks handled fields
and subtrees by concrete path, then structurally compares everything unhandled.
Default comparison waits until custom dispatch finishes.

Parents receive complete objects. A parent returning true cannot override a child
failure. Type and RPC comparators cover their scopes, including anything descendants
have not checked. Without an RPC comparator, structural comparison handles the
remainder. Equivalence requires every callback to return true and all unhandled
structure to match. Errors and non-boolean returns mean unable to compare.

For example, field comparators can compare user roles without regard to order and
explicitly ignore a response-generation timestamp. Default comparison still checks
user IDs, names, and user ordering. Neither comparator deletes fields.

## Implementation checklist

### 1. Resolve comparison and transport decisions

- [ ] Define missing-value arguments, including how missing values differ from
  present default values.
- [ ] Define unordered-array pairing before child comparisons so positional
  failures cannot later be erased by a parent comparator.
- [ ] Decide unary-first scope and exact JSON transport support. Define
  stream-message pairing if streaming is included in the initial scope.
- [ ] Define the policy for unable-to-compare outcomes, quarantine recovery,
  application metadata, and capture delivery failures.

### 2. Load schemas and prepare typed payloads

- [x] Load local descriptor sets and resolve imported files, messages, and RPC
  methods without relying on process-global registrations.
- [x] Reject malformed or empty descriptor sets, missing imports, duplicate
  definitions, and unresolved types; test loading and lookup failures.
- [ ] Decode binary protobuf and ProtoJSON into consistent JS objects, preserving
  presence, JSON field names, string-encoded 64-bit integers, and base64 bytes.
- [ ] Handle unsupported and unknown data explicitly without silently dropping it.
- [ ] Generate TypeScript declarations matching the decoded objects and directly
  typed comparator arguments, without exposing descriptors to scripts.
- [ ] Test the host's binary/JSON decoding equivalence, presence, precision,
  nested collections, and unsupported or unknown data handling.

### 3. Load and execute comparator scripts

- [ ] Embed esbuild to compile a TypeScript entry file and optional helper imports
  at load time.
- [ ] Run the compiled JavaScript in Sobek and provide the `spectre` module's
  `field`, `message`, and `rpc` registration functions during module evaluation.
- [ ] Validate targets against the schema and reject unresolved targets and
  duplicate registrations before activation.
- [ ] Support registering the same ordinary function for multiple targets without
  requiring exports or filename conventions.
- [ ] Enforce synchronous callbacks with two direct arguments and boolean results.
  Treat errors and non-boolean results as unable to compare.
- [ ] Use a fresh runtime for each comparison and protect payloads from mutation.
- [ ] Bound comparison workers and payload sizes, and enforce execution deadlines.
- [ ] Add TS7 type-checking to development and CI through `bit`; keep esbuild
  responsible only for transpilation.
- [ ] Test script loading, invalid registrations, runtime isolation, immutable
  inputs, callback failures, deadlines, and capacity limits.

### 4. Dispatch comparators and compare remaining structure

- [ ] Traverse payloads from leaves upward, using the agreed missing-value and
  array-pairing rules independently of registration order.
- [ ] Give field comparators precedence over message comparators at the same
  location, and run response-message comparators before the final RPC comparator.
- [ ] Pass complete objects to parents and preserve every child failure regardless
  of parent or RPC results.
- [ ] Track handled fields and subtrees by concrete occurrence paths in Go.
  Message and RPC comparators cover their full scopes.
- [ ] Structurally compare all unhandled data after custom dispatch finishes.
- [ ] Report equivalence only when every callback returns true and all unhandled
  structure matches; keep divergence distinct from unable-to-compare outcomes.
- [ ] Test precedence, callback ordering, repeated-message occurrences, parent
  coverage, preserved failures, and structural comparison of the remainder.
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

- Missing-value arguments and unordered-array pairing before child comparisons.
  Positional child failures cannot later be erased by an unordered parent rule.
- Unary-first scope, exact JSON transport support, and stream-message pairing.
- Policy for unable-to-compare outcomes, quarantine recovery, application metadata,
  and capture delivery failures.
