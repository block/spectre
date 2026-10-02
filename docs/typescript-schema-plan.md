# TypeScript schema implementation plan

Replace protobuf descriptor sets as the host's schema with TypeScript declarations.
Protobuf was the schema because gRPC was the first protocol, so every other protocol
currently needs a synthetic `.proto` such as `internal/sample/proto/weather.proto`.
With TypeScript as the schema, each protocol produces declarations in the same
language the normaliser scripts are written in, and `tsc` checks the scripts against
them. This supersedes the TypeScript items in [the ingress plan](plan.md).

## Decisions

- TypeScript declaration files are the schema. They are generated from protobuf
  descriptors, written by hand for ad hoc REST, and later generated from DDL for
  MySQL. There is one source of truth per protocol and the host reads it directly.
- The host parses declarations with the TypeScript 7 parser from
  `github.com/microsoft/TypeScript/tsc`. Its packages are `internal`, so shim
  modules under that module path re-export the parser. Pseudo-versions only.
- Parsing is syntax only. The host accepts a defined subset and resolves type
  references itself. Anything outside the subset fails at load. `tsc` checks
  everything else at build time.
- A protocol-neutral schema model in Go replaces `protoreflect` throughout
  `internal/comparison`. Protocol adapters produce decoded JSON; the model drives
  target resolution, traversal, HTTP parameter binding, presence, and hashing.
- The gRPC adapter keeps descriptors for wire decoding only. At load it checks that
  the descriptors and the generated declarations agree by name. Reflection still
  verifies that reference and candidate serve the same schema.
- Scripts refer to types by fully qualified name. The host generates a registry
  declaration from the parsed schema so `tsc` checks names, paths, and normaliser
  argument types.
- esbuild bundles the scripts directory at load and provides the virtual `spectre`
  module. It only transpiles. `tsc --noEmit` runs through `bit`.

## Schema subset

- `interface`, `type` aliases to object literals, and `declare namespace` for
  qualified names. Names must be unique across the schema set.
- Members: `name: T` and `name?: T`. Optional means absent is allowed and arrives
  as `undefined`. Every singular scalar an egress request binds must be optional.
- Types: `string`, `number`, `boolean`, `T[]`, `Record<string, T>`, unions of string
  literals, and references to other declared types.
- Services: an interface whose members are method signatures
  `Method(request: Req): Res`. They map gRPC paths to request and response types.
- Rejected: generics, intersections, conditional and mapped types, index
  signatures other than `Record`, `any`, `unknown`, functions, and classes.

## Protobuf mapping

The generator emits one `.d.ts` per proto file inside a `declare namespace` that
mirrors the proto package, so `spectre.sample.v1.User` keeps its name.

| Protobuf | TypeScript |
| --- | --- |
| `string`, `bool` | `string`, `boolean` |
| 32-bit integers, `float`, `double` | `number` |
| 64-bit integers | `string` |
| `bytes` | `string` (base64) |
| `enum` | union of value-name literals |
| `optional`, message fields | `name?: T` |
| `repeated T` | `T[]` |
| `map<K, V>` | `Record<string, V>` |
| `google.protobuf.Timestamp`, `Duration` | `string` |
| `service` | interface of method signatures |

`oneof` and `google.protobuf.Any` are open decisions below.

## Script API

Targets stay strings. The registry types them.

```ts
import { egress, field, ingress } from "spectre";

ingress("GET /v2/forecast", "GetForecastV2Response");
egress("GET forecasts.example/v1/forecasts/{location}", "FetchForecastRequest");

field("GetForecastV2Response", "alerts", (alerts) => alerts?.sort(byID));
message("spectre.sample.v1.User", (user) => ({ ...user, roles: [...user.roles].sort() }));
```

`ingress` names the response type and `egress` the request type. gRPC and Connect
requests resolve through the service interfaces by path. The normaliser argument is
typed from the registry entry and the path, and is `undefined` when absent.

## Implementation checklist

### 1. Schema parser

- [ ] Add shim modules under `github.com/microsoft/TypeScript/tsc/shim/...` with
  `replace` directives, exporting only the parser and AST packages the host uses.
- [ ] Parse every `.ts` and `.d.ts` file in a schema directory into the schema model,
  enforcing the subset and resolving references across files.
- [ ] Reject duplicate names, unresolved references, and declarations outside the
  subset at load so the proxy never becomes ready.
- [ ] Test each accepted construct, each rejected construct, cross-file references,
  and the diagnostics from malformed files.

### 2. Schema model and comparison

- [ ] Define the model in `internal/schema`: named types, fields, scalar kinds,
  optional, list, map, literal unions, and operations with request and response types.
- [ ] Rewrite target resolution, traversal, HTTP parameter binding, presence checks,
  unknown field rejection, and hashing against the model.
- [ ] Remove `protoreflect` from `internal/comparison` and from the ingress and
  egress configuration surfaces.
- [ ] Port the existing comparison tests to the model and confirm hashes change only
  where the model intentionally differs.

### 3. Protocol adapters

- [ ] gRPC and Connect: decode with descriptors into JSON, then validate at load
  that every method and message in the descriptors has a matching declaration.
- [ ] Raw HTTP JSON: decode and bind directly from the model with no descriptors.
- [ ] Test adapter agreement on presence, 64-bit integers, bytes, enums, and nested
  collections across binary, ProtoJSON, and raw JSON inputs.

### 4. Protobuf generator

- [ ] Add `cmd/spectre-gen-proto` with a `scripts` symlink. It reads descriptor sets
  and writes the `.d.ts` files following the mapping table.
- [ ] Guarantee the output stays inside the subset and round-trips through the
  parser.
- [ ] Add a `bit` target that regenerates the sample declarations from
  `dist/descriptors/sample.pb`.
- [ ] Test generation against the sample protos with golden files.

### 5. Script loading

- [ ] Embed esbuild. Bundle the scripts directory with a plugin that resolves the
  virtual `spectre` module and rejects imports outside the directory.
- [ ] Generate the registry declaration and the `spectre` module declaration from
  the parsed schema into a build directory for `tsc`.
- [ ] Replace method-based `ingress` and `egress` with type-based registration and
  validate names and paths against the model.
- [ ] Add `tsc --noEmit` and the generators to `bit` test and lint targets.
- [ ] Test bundling, type errors surfaced by `tsc`, invalid names, and that
  registrations are identical across fresh runtimes.

### 6. Sample migration and cleanup

- [ ] Replace `weather.proto` and `forecasts.proto` with hand-written declarations
  and convert the sample scripts to TypeScript.
- [ ] Remove `--descriptors-dir` from egress once raw HTTP needs no descriptors, and
  keep it on ingress only for gRPC decoding.
- [ ] Update `CONTRIBUTING.md` for the generator and `tsc` workflow. Update
  `README.md` when asked.

## Open decisions

- `oneof`: optional sibling fields, matching ProtoJSON, or a discriminated union.
- `google.protobuf.Any`: reject, or model as `{ "@type": string }` with unknown body.
- Whether generated declarations are checked in or produced by `bit` on demand.
- Whether a later rewrite step lets scripts pass type arguments instead of names.
