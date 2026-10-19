# Contributing

The `spectre` binary supervises the whole comparison. `spectre run` starts a
candidate and a reference service behind two embedded proxies. The ingress proxy
mirrors HTTP requests to both services and returns the reference response without
waiting for the candidate. The egress proxy sits between the services and their
dependencies, forwarding reference calls and replaying the recorded responses to
matching candidate calls. `spectre gen` generates the TypeScript schema
declarations used for comparison.

Logs default to info-level, colorized text on stderr. Use `--log-level=debug|info|warn|error`
to change the minimum level and `--log-json` for JSON output. `--log-remap OLD=NEW` renames a
top-level JSON field for pipelines that expect other names, for example
`--log-remap msg=message --log-remap time=timestamp`.

Each line the candidate and reference write to stdout is logged as a record tagged
`subsystem=candidate` or `subsystem=reference`, so their logs are distinguishable from
SPECTRE's own lines, which carry `subsystem=supervisor`, `ingress` or `egress`. A line
that is a JSON object is passed through with its fields as attributes and its `msg`,
`level` and `time` lifted into the record. If a service uses other field or level names,
map them with `--service-log-field` and `--service-log-level`, for example
`--service-log-field message=msg --service-log-field severity=level
--service-log-level warning=warn --service-log-level fatal=error`. Stderr passes through
unchanged.

`spectre run` accepts `--config=FILE` to load flag values from a TOML file. Keys are
flag names without the leading dashes, and command-line flags take precedence:

```toml
scripts-dir = "internal/sample/scripts"
schema-dir = "internal/sample/schema"
descriptors-dir = "dist/descriptors"

[destination]
"forecasts.example" = "http://127.0.0.1:50053"
```

Unknown keys are rejected.

## Container image

Build the local Alpine-based image with `bit container`. It contains the single
`spectre` binary, with the proxies embedded, and its entry point is `spectre`.
Run `spectre run` with your candidate and reference commands to start the whole
stack. The [demo](demo/README.md) wraps a complete supervised example around the
sample service, including the cgroup split and egress sandbox.

Releases are published for Linux AMD64 and ARM64 as `ghcr.io/block/spectre`
and `docker.io/blockossreleases/spectre`.

## Egress proxy

The embedded egress proxy forwards each reference service's dependency calls to
the real upstream and records the responses. `spectre run` wires it to the
services automatically; map each dependency with a `--destination=HOST=URL` flag,
which sends requests for `HOST` to `URL`:

```sh
spectre run ... --destination=forecasts.example=http://127.0.0.1:50053
```

Scripts declare dependency endpoints with `egress.match<Type>("http", pattern)`.
Types come from `--schema-dir`; descriptors are only needed for protobuf traffic. A candidate call waits up to `--match-window` for an
equivalent reference call and receives its recorded response. A candidate call
without a match gets a `502` and stops all later candidate calls until the process
restarts. Ingress then quarantines the candidate when its responses diverge.

## Sample Connect service

From the repository root with Hermit activated, build and run the sample:

```sh
bit sample
spectre-sample
```

Run two samples as the candidate and reference behind the proxies with
`spectre run`, which starts both services itself:

```sh
spectre run \
  --reference='spectre-sample --listen=$SPECTRE_LISTEN' \
  --candidate='spectre-sample --listen=$SPECTRE_LISTEN' \
  --scripts-dir=internal/sample/scripts \
  --schema-dir=internal/sample/schema \
  --descriptors-dir=dist/descriptors \
  --no-limits
```

Ingress listens on `127.0.0.1:50050` by default. It accepts HTTP/1 and unencrypted
HTTP/2 so the sample can be called through it with `grpcurl`. The cgroup split and
egress sandbox are Linux-only; pass `--no-limits` off Linux. If candidate buffering
or concurrency reaches its limit, the candidate stops being mirrored until the run
restarts.

The sample serves Connect, gRPC, and gRPC-Web on `127.0.0.1:50051` with
reflection enabled.
Use `--listen=127.0.0.1:50052` to run a second instance and `--data=path/to/users.json`
to load a different ProtoJSON `ListUsersResponse` at startup. With `grpcurl` installed:

```sh
grpcurl -plaintext -d '{}' localhost:50050 spectre.sample.v1.UserService/ListUsers
grpcurl -plaintext -d '{"id":"user-1"}' localhost:50050 spectre.sample.v1.UserService/GetUser
grpcurl -plaintext -d '{"role":"ROLE_READER"}' localhost:50050 spectre.sample.v1.UserService/ListUsers
```

The [sample data](internal/sample/testdata/users.json) includes nested messages,
repeated fields, maps, enums, oneofs, bytes, timestamps, and 64-bit integers beyond
JavaScript's safe integer range. Optional fields cover absent and explicitly empty
or false values. Responses preserve fixture order and user creation timestamps.
`ListUsers` generates its response timestamp for each request. `GetUser` returns
`InvalidArgument` for an empty ID and `NotFound` for an unknown ID. `ListUsers`
filters by IDs and an optional role.

The sample also serves raw HTTP JSON endpoints modelled on a legacy weather service.
Their payloads are described directly in
[weather.d.ts](internal/sample/schema/weather.d.ts), with no protobuf definitions.
Use `--weather=path/to/weather.json` to load different
[weather](internal/sample/testdata/weather.json) and `--revision` to set the status revision:

```sh
curl 'localhost:50050/v2/forecast?location=london'
curl 'localhost:50050/api/v1/forecast?location=sydney'
curl localhost:50050/_status
```

With `--forecasts=URL`, the weather endpoints fetch forecasts from a forecast
provider instead of their local data, sending `--forecasts-host`
(`forecasts.example` by default) as the host. Alerts still come from `--weather`.
Run the provider with `spectre-sample-forecasts`, which listens on
`127.0.0.1:50053` and serves `GET /v1/forecasts/{location}` from the same weather
data. The [forecasts script](internal/sample/scripts/forecasts.ts) declares this
endpoint for egress.

To run the whole stack on unix sockets under `dist/sockets/`, with reloads on
change, run `proctor`. It starts the forecast provider, egress, reference and
candidate samples in dependency mode, and ingress:

```sh
curl --unix-socket dist/sockets/ingress.sock 'http://ingress/v2/forecast?location=london'
```

Edit the [protobuf definitions](internal/sample/proto/service.proto) and run
`bit sample` to regenerate the Go and Connect bindings, descriptors, and TypeScript
declarations. Generated declarations are checked in alongside the Go bindings.

## TypeScript schemas and normalisers

`--schema-dir` is required. Every `.ts` file in it, including subdirectories,
contains only `declare module "name" { ... }` blocks. A type's name is its module
name followed by its namespace path, such as `weather.GetForecastV2Response`, so
separate services cannot collide. Modules share nothing unless one imports another.
Use interfaces or object aliases, namespaces, optional members, scalar types, arrays,
`Record<string, T>`, and string-literal unions. Unsupported constructs fail at
startup. Raw JSON must match the declared shape; unknown keys, missing required
members, and nulls are rejected.

`--scripts-dir` loads every `.ts` script, including subdirectories. Scripts can
import relative modules inside the directory, the virtual `spectre` module, and
types from schema modules. At startup the host type-checks the scripts against the
schema with the TypeScript 7 checker, and any error stops it.

```ts
import { field, ingress } from "spectre";
import type { GetForecastV2Response } from "weather";

ingress.match<GetForecastV2Response>("http", "GET /v2/forecast");
field<GetForecastV2Response, "alerts">((alerts) =>
  alerts?.sort((a, b) => (a.id ?? "").localeCompare(b.id ?? "")),
);
```

Type arguments are required, and they must name schema types. The host reads them
from the checker, so aliases and namespace imports work. `field<T, "path">(callback)`
uses JSON field names. Paths descend through objects with dots and through lists
with `[]`, such as `users[].name`. `message<T>(callback)` applies wherever that
object type occurs. Missing optional values arrive as `undefined`; returning
`undefined` removes them.

`ingress.ignore("http", pattern)` skips comparison for matching requests, so they
are never quarantined. Use it for health and status endpoints, whose non-JSON error
during an unready upstream would otherwise fail decoding and quarantine the candidate:

```ts
import { ingress } from "spectre";

ingress.ignore("http", "GET /_status");
```

For protobuf traffic, generate declarations with
`spectre gen proto --descriptors-dir=dist/descriptors --output=internal/sample/schema`.
Wire descriptors must agree with those declarations. ProtoJSON defaults are emitted
so required scalar, array, and map members are present. Oneofs become optional sibling
members. Arbitrary JSON well-known types, including `google.protobuf.Any`, are unsupported.

For JSON APIs described by JSON Schema, generate one module with
`spectre gen json-schema --module=weather --output=schema/weather.d.ts weather.schema.json`.
Each titled root schema and each `$defs` or `definitions` entry becomes a type, and inline
objects become nested types named after their property. Properties outside `required` are
optional. Null, type unions, `anyOf`, `oneOf`, `allOf`, `not`, tuples, and free-form objects are
unsupported. Undeclared properties fail payload validation even when the schema allows them.
Keywords that only narrow values within a type, such as `pattern`, `minimum`, or a numeric
or boolean `enum`, are not enforced.

`tsconfig.json` gives editors the host's compiler options, so they report the errors
the host would. Keep it in step with `internal/typescript/program.go`. For scripts
outside this repository, write the `spectre` module declaration with
`spectre gen module --output=spectre.d.ts`.
