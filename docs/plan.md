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

## Implementation order

1. Define missing-value arguments and how to pair unordered array elements.
2. Build descriptor loading, decoding, and TypeScript declaration generation.
3. Build esbuild/Sobek loading, registration validation, and bounded execution.
4. Implement recursive dispatch, handled-field tracking, and structural comparison.
5. Integrate gRPC forwarding, quarantine, and sanitised capture. Track each
   invocation separately from its trace ID. Bound candidate work without waiting
   for it before returning the reference response. Compare RPC status separately
   from bodies. Quarantine must not depend on capture delivery succeeding.

Test equivalent binary/JSON decoding, presence and precision, callback ordering,
per-occurrence coverage, immutable inputs, runtime isolation, and failure handling.
Integration tests must cover forwarding fidelity, cancellation, capacity limits,
quarantine races, and reference delivery during candidate or comparator failure.

## Open decisions

- Missing-value arguments and unordered-array pairing before child comparisons.
  Positional child failures cannot later be erased by an unordered parent rule.
- Unary-first scope, exact JSON transport support, and stream-message pairing.
- Policy for unable-to-compare outcomes, quarantine recovery, application metadata,
  and capture delivery failures.
