# TypeScript schema implementation plan

Replace protobuf descriptor sets as the host's schema with TypeScript declarations.
Protobuf was the schema because gRPC was the first protocol, so other protocols
needed synthetic `.proto` files such as the former weather service definition.
With TypeScript as the schema, each protocol produces declarations in the same
language the normaliser scripts are written in, and the host checks the scripts
against them. This supersedes the TypeScript items in [the ingress plan](plan.md) and
supplies the schema model for the forthcoming protocol abstraction.

## Decisions

- TypeScript declaration files are the schema. They are generated from protobuf
  descriptors, written by hand for ad hoc REST, and later generated from DDL for
  MySQL. There is one source of truth per protocol and the host reads it directly.
- The host type-checks the schema and scripts in one program with the TypeScript 7
  checker from `github.com/microsoft/TypeScript/tsc`. Its packages are `internal`,
  so a shim module under that module path re-exports the compiler, checker, and
  transpiler. Pseudo-versions only.
- The host reads the schema from the checker's types, so the compiler resolves
  every reference. Anything outside the subset fails at load.
- A protocol-neutral schema model in Go replaces `protoreflect` throughout
  `internal/comparison`. Protocols hand the engine a `schema.Type` and a JSON
  value, which is the boundary intended by the protocol abstraction.
  The model drives target resolution, traversal, HTTP parameter binding,
  presence, and hashing.
- The HTTP protocol keeps descriptors for gRPC and Connect wire decoding only. At
  configure time it checks that the descriptors and the generated declarations
  agree by name. Reflection still verifies that reference and candidate serve the
  same schema. Reflection ownership moves in the protocol abstraction follow-up.
- Scripts name types with type arguments, such as `field<User, "roles">`. The
  host asks the checker which schema type each argument resolves to, then passes
  the qualified name to the runtime as a leading string argument.
- The `spectre` module declaration is static. Mapped types derive field paths and
  normaliser argument types from the type arguments, so nothing is generated
  from the schema.
- The pinned TypeScript Go module checks and transpiles each script at load. Sobek
  links the transpiled modules and provides the virtual `spectre` module, so no
  bundler or separate `tsc` step is needed. `tsconfig.json` mirrors the host's
  compiler options for editors.

## Schema subset

- Files contain only `declare module "name" { ... }` blocks, which may span files.
  A type's name is its module name followed by its namespace path. Modules share
  no scope, so separate services cannot collide; one may import another's types.
- `interface`, `type` aliases to object literals, and namespaces for nesting.
- Members: `name: T` and `name?: T`. Optional means absent is allowed and arrives
  as `undefined`. Required request members may be supplied by path or query binding.
- Types: `string`, `number`, `boolean`, `T[]`, `Record<string, T>`, unions of string
  literals, and references to other declared types.
- Services: an interface whose members are method signatures
  `Method(request: Req): Res`. They map gRPC paths to request and response types.
- Rejected: generics, intersections, conditional and mapped types, index
  signatures other than `Record`, `any`, `unknown`, functions, and classes.

## Protobuf mapping

The generator emits one `.d.ts` per proto file inside a `declare module` named
after the proto package, so `spectre.sample.v1.User` keeps its name. Files without
a package are rejected. References use `import("package").Type`, which nested
names cannot shadow.

| Protobuf | TypeScript |
| --- | --- |
| `string`, `bool` | `string`, `boolean` |
| 32-bit integers | `number` |
| `float`, `double` | `number \| "-Infinity" \| "Infinity" \| "NaN"` |
| 64-bit integers | `string` |
| `bytes` | `string` (base64) |
| `enum` | union of value-name literals, plus `number` for open enums |
| `optional`, message fields | `name?: T` |
| `repeated T` | `T[]` |
| `map<K, V>` | `Record<string, V>` |
| `google.protobuf.Timestamp`, `Duration` | `string` |
| `service` | interface of method signatures |

`oneof` members are optional siblings, matching ProtoJSON. `google.protobuf.Any`
and the arbitrary JSON well-known types (`Struct`, `Value`, `ListValue`) are rejected.
Generated declarations are checked in alongside generated Go bindings.

## Script API

Scripts are TypeScript only. The first argument of `ingress` and `egress` is the
protocol key from the protocol abstraction.

```ts
import { egress, field, ingress, message } from "spectre";
import type { User } from "spectre.sample.v1";
import type { GetForecastV2Response } from "weather";
import type { FetchForecastRequest } from "forecasts";

ingress<GetForecastV2Response>("http", "GET /v2/forecast");
egress<FetchForecastRequest>("http", "GET forecasts.example/v1/forecasts/{location}");

field<GetForecastV2Response, "alerts">((alerts) => alerts?.sort(byID));
message<User>((user) =>
  user === undefined ? undefined : { ...user, roles: [...user.roles].sort() },
);
```

`ingress` names the response type and `egress` the request type. gRPC and Connect
requests resolve through the service interfaces by path. The normaliser argument is
typed from the type and the path, and is `undefined` when absent. Before
transpiling, the host inserts the resolved names, so the runtime receives
`ingress("weather.GetForecastV2Response", "http", "GET /v2/forecast")`.

## Implementation checklist

The implementation is complete; the protocol engine and reflection ownership move
remain part of the separate protocol abstraction change.

### 1. Schema parser

- [x] Add shim modules under `github.com/microsoft/TypeScript/tsc/shim/...` with
  `replace` directives, exporting only the parser, AST, and transpiler the host uses.
- [x] Read every `.ts` and `.d.ts` file in a schema directory into the schema model
  through the checker, enforcing the subset.
- [x] Reject type errors, unresolved references, and declarations outside the
  subset at load so the proxy never becomes ready.
- [x] Test each accepted construct, each rejected construct, cross-file references,
  and the diagnostics from malformed files.

### 2. Schema model and comparison

- [x] Define the model in `internal/schema`: named types, fields, scalar kinds,
  optional, list, map, literal unions, and operations with request and response
  types. `schema.Type` is the value protocols hand to the engine.
- [x] Rewrite target resolution, traversal, HTTP parameter binding, presence checks,
  unknown field rejection, and hashing against the model.
- [x] Remove `protoreflect` from `internal/comparison` and from the ingress and
  egress configuration surfaces.
- [x] Port the existing comparison tests to the model and confirm hashes change only
  where the model intentionally differs.

### 3. Protocol adapters

- [x] gRPC and Connect: load wire descriptors, by reflection for
  ingress, decodes with them into JSON, and validates at configure time that every
  method and message has a matching declaration.
- [x] Raw HTTP JSON: decode and bind directly from the model with no descriptors.
- [x] Test adapter agreement on presence, 64-bit integers, bytes, enums, and nested
  collections across binary, ProtoJSON, and raw JSON inputs.

### 4. Protobuf generator

- [x] Add `cmd/spectre-gen proto` with a `scripts` symlink. It reads descriptor sets
  and writes the `.d.ts` files following the mapping table.
- [x] Guarantee the output stays inside the subset and round-trips through the
  parser.
- [x] Add a `bit` target that regenerates the sample declarations from
  `dist/descriptors/sample.pb`.
- [x] Test generation against the sample protos with golden files.

### 5. Script loading

- [x] Transpile every `.ts` file in the scripts directory with the shimmed
  transpiler, and load the output through the existing Sobek module loader, which
  resolves the virtual `spectre` module and rejects imports outside the directory.
- [x] Type-check scripts against the schema at startup, and pass each spectre
  call's resolved type arguments to the runtime.
- [x] Change the `type` argument of `ingress` and `egress` from an RPC method to a
  type name, keeping the protocol key, and validate names and paths against the
  model.
- [x] Add the generators to `bit` targets; the integration test checks the sample.
- [x] Test module loading, type errors surfaced by the checker, invalid names, and that
  registrations are identical across fresh runtimes.

### 6. Sample migration and cleanup

- [x] Replace `weather.proto` and `forecasts.proto` with hand-written declarations
  and convert the sample scripts to TypeScript.
- [x] Point core `schema.Config` at declarations and keep descriptor configuration
  separate, optional for raw HTTP ingress and egress.
- [x] Update `CONTRIBUTING.md` for the generator and editor workflow.
  Leave `README.md` unchanged until requested.

## Sequencing with the protocol abstraction

This change is implemented first. Protobuf wire decoding is isolated in
`internal/httpcodec`; comparison traversal and binding use `schema.Type`.
Rebase the protocol abstraction onto this work, then move HTTP routing and status
handling behind its engine boundary. Descriptor configuration remains separate
from core schema configuration.

## Open decisions

None.
