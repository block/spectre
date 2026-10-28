# Contributing

The `spectre` binary supervises the whole comparison. `spectre supervise` starts a
candidate and a reference service behind two embedded proxies. The ingress proxy
mirrors HTTP requests to both services and returns the reference response without
waiting for the candidate. The egress proxy sits between the services and their
dependencies, forwarding reference calls and replaying the recorded responses to
matching candidate calls. `spectre gen` generates the TypeScript schema
declarations used for comparison.

Logs default to info-level, colorized text on stderr. Use `--log-level=debug|info|warn|error`
to change the minimum level.

`--log-json` writes JSON logs shaped by a JSON template, so SPECTRE's own logs can look like
the service it stands in for. `--log-json=DEFAULT` uses
`{"time": "<time>", "level": "<level>", "msg": "<msg>"}`. Placeholders are `<msg>`,
`<level>`, `<time>` and `<attrs>`; other values are written as given. Without `<attrs>`,
attributes go at the top level:

```sh
--log-json='{"ts": "<time:unixms>", "severity": "<level:debug=DEBUG,info=INFO,warn=WARNING,error=ERROR>", "message": "<msg>"}'
```

- `<level:LEVEL=NAME,...>` maps slog levels to the service's names. A level between two
  names uses the lower one, and names that are all integers are written as numbers.
- `<time:FORMAT>` takes a Go time layout or `rfc3339`, `rfc3339nano` (the default), `unix`,
  `unixms` or `unixns`.

The candidate's and reference's stdout passes through with a label added, and stderr
passes through unchanged. A JSON object line gains a leading
`"spectre.subsystem": "candidate"` or `"reference"` member and is otherwise left as
written; any other line is prefixed with `candidate: ` or `reference: `. SPECTRE's own
lines carry `spectre.subsystem` set to `supervisor`, `ingress` or `egress`.

`spectre supervise` accepts `--config=FILE` to load flag values from a TOML file. Keys are
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
`spectre` binary, with the proxies embedded, and its entry point is `spectre`, so
the same image runs any command. Run `spectre supervise` with your candidate and
reference commands to start the whole stack, or `spectre serve` to run the
SPECTRE server. Both listen on localhost by default, so pass `--listen` with
`0.0.0.0` inside a container.

Build the [demo](demo/README.md) image with `bit demo`. It wraps a complete
supervised example around the sample service, including the cgroup split and
egress sandbox.

Releases are published for Linux AMD64 and ARM64 as `ghcr.io/block/spectre`
and `docker.io/blockossreleases/spectre`.

## SPECTRE server

`spectre serve` collects the quarantines that mirrors report. It listens on
`127.0.0.1:50070` by default and keeps them in memory, so a restart loses them.
To keep them in Redis instead, pass `--storage=redis` and `--redis-url`, such as
`redis://localhost:6379/0`.

Every mirror reports to a server, so `spectre supervise` requires its URL,
`--server-url`, and the mirror's identity, `--service`, `--environment`, and
`--revision`. Set them in a deployment with the matching `SPECTRE_SERVER_URL`,
`SPECTRE_SERVICE`, `SPECTRE_ENVIRONMENT`, and `SPECTRE_REVISION` variables.
Every flag can be set from a `SPECTRE_` variable named after it.

List what the server holds with `spectre divergences --server-url=URL`. Filter
with `--service`, `--environment`, `--revision`, or `--node`, and print JSON
with `--json`. A divergence stays listed until someone clears it with
`spectre divergences clear --service=SERVICE --environment=ENVIRONMENT`, which
removes both directions of that mirror.

## Egress proxy

The embedded egress proxy forwards each reference service's dependency calls to
the real upstream and records the responses. `spectre supervise` wires it to the
services automatically; map each dependency with a `--destination=HOST=URL` flag,
which sends requests for `HOST` to `URL`:

```sh
spectre supervise ... --destination=forecasts.example=http://127.0.0.1:50053
```

A `HOST` may be a glob, such as `--destination="*.example=http://127.0.0.1:50053"`,
where `*` also matches dots. A host takes the first destination, in pattern order,
that matches it.

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
`spectre supervise`, which starts both services itself. It reports to a SPECTRE
server, so start one first:

```sh
spectre serve &
spectre supervise \
  --server-url=http://127.0.0.1:50070 \
  --service=sample \
  --environment=local \
  --revision=local \
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

`--state-file` passes the migration state described in
[ADR 006](docs/006-migration-plan.md) to ingress. Ingress then mirrors only the routes
of migrated nodes and sends routes of pending nodes to the reference only. A route
that no node lists also goes to the reference only, and quarantines the candidate
unless scripts ignore it or it is gRPC reflection. Check the file with
`spectre state validate` first; an invalid state stops `spectre supervise` at startup.

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
