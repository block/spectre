# Using SPECTRE

SPECTRE validates a service migration against real traffic. It runs the existing
service, the reference, and its replacement, the candidate, side by side and
compares what they do.

- The **ingress proxy** receives client requests. It sends each one to both
  services, returns the reference's response without waiting for the candidate,
  and compares the two responses.
- The **egress proxy** sits between the services and their dependencies. It
  forwards the reference's calls to the real dependency and records the
  responses. It answers each matching candidate call with the recorded response,
  so the candidate never reaches a dependency or makes a real write.
- A **mirror** is one running pair of proxies with their reference and
  candidate. `spectre proxy` runs one.
- The **SPECTRE server** collects the state of every mirror and the divergences
  they find. `spectre status` reads it.

When the candidate behaves differently from the reference, the mirror
**quarantines** it: it stops sending the candidate traffic until the mirror
restarts, and reports why to the server.

## Commands

| Command | Purpose |
| --- | --- |
| `spectre init` | Set a service checkout up for a migration from a template. |
| `spectre proxy` | Run the proxies in front of services that run elsewhere. |
| `spectre contain` | Block a candidate container's IP traffic, and redirect connections to local ports. |
| `spectre serve` | Run the SPECTRE server. |
| `spectre status` | List mirrors and divergences, or clear divergences. |
| `spectre state` | Describe and validate a migration state file. |
| `spectre next` | List the nodes to migrate next. |
| `spectre done` | Mark nodes migrated in the migration state. |
| `spectre gen` | Generate TypeScript schema declarations and the script API declaration. |

Run `spectre <command> --help` for every flag of a command, with its default.

## Installing

Releases are published for Linux AMD64 and ARM64 as container images:

- `ghcr.io/block/spectre`
- `docker.io/blockossreleases/spectre`

The image contains the single `spectre` binary, with the proxies embedded, and
its entry point is `spectre`, so the same image runs any command. Commands
listen on localhost by default, so pass `--listen` with `0.0.0.0` inside a
container.

`spectre --version` prints the version.

## Configuration

### Flags, environment variables, and files

Every flag can also be set from an environment variable named after it with a
`SPECTRE_` prefix, such as `SPECTRE_SERVER_URL` for `--server-url`. The only
exception is `--version`.

`--config=FILE` loads flag values from a TOML file. Keys are flag names without
the leading dashes, and command-line flags take precedence. Unknown keys are
rejected:

```toml
scripts-dir = "spectre/scripts"
schema-dir = "spectre/schema"
descriptors-dir = "spectre/descriptors"

[destination]
"forecasts.example" = "http://127.0.0.1:50053"
```

### Addresses

Flags that listen take `host:port`, or `unix:<path>` for a unix socket. On Linux,
`unix:@<name>` is an abstract socket.

Flags that name a service take a URL with one of these schemes:

- `http` and `https`.
- `h2c`, for unencrypted HTTP/2.
- `http+unix:<socket>` and `h2c+unix:<socket>`, for a unix socket.

SPECTRE creates unix socket files readable and writable by their group.

### Logging

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

SPECTRE's own lines carry `spectre.subsystem` set to `mirror`, `ingress`,
`egress` or `health`.

## Setting a service up

`spectre init --template=NAME [DIR]` renders a template into the `spectre`
directory of a service checkout, which defaults to the current directory. A
template repository holds one template per top-level directory. It is a local
directory or a git source, such as `git+ssh://github.com/org/repo`, passed with
`--template-repo`. A custom binary can set a default repository.

- `--var=NAME=VALUE` sets a template variable. Write a list comma-separated.
  `spectre_version` defaults to the version of the running binary.
- `--force` renders over an existing `spectre` directory, overwriting the files
  the template writes.

A template may check its variables before anything is written, and stop with an
error.

## Running a mirror with `spectre proxy`

`spectre proxy` runs the ingress and egress proxies and reports the mirror to the
SPECTRE server. It never starts the services, so the candidate and reference run
on their own, such as in other containers of the same pod. Pass where ingress reaches each service with
`--reference-url` and `--candidate-url`, and give each service the matching
egress address, `--reference-egress-listen` or `--candidate-egress-listen`, as
its dependency proxy. Any address may be a unix socket in a shared volume:

```sh
spectre proxy \
  --listen=0.0.0.0:8080 \
  --reference-url=h2c+unix:/sockets/reference.sock \
  --candidate-url=h2c+unix:/sockets/candidate.sock \
  --reference-egress-listen=unix:/sockets/egress-reference.sock \
  --candidate-egress-listen=unix:/sockets/egress-candidate.sock \
  --health-listen=0.0.0.0:8081 \
  --server-url=... --service=... --environment=... --revision=...
```

The candidate's URL and egress address must be a loopback IP or a unix socket.

Ingress listens on `--listen`, `127.0.0.1:50050` by default, and accepts HTTP/1
and unencrypted HTTP/2.

`--health-listen` serves `/livez`, and `/readyz` once both proxies are ready,
over TCP so a kubelet's HTTP probes can reach them. The egress proxy also serves
its own `/livez` and `/readyz` on `--egress-health-listen`.

Containers running as different users connect to the shared sockets through
the pod's shared `fsGroup`. A candidate that creates its own listening socket
must make it group-writable too.
[`demo/mirror-pod.yaml`](demo/mirror-pod.yaml) lays out such a pod.

### Containing a candidate container

A candidate in its own container must reach dependencies only through its
egress socket. Run it as a user of its own, and run `spectre contain
--candidate-uid=<candidate uid>` in an init container with root and
`NET_ADMIN`. It installs Linux packet filter rules that reject that user's
IPv4 and IPv6 traffic, including loopback and DNS, since the pod's containers
share one network. Replies on connections other containers open to the
candidate still pass, and unix sockets are unaffected. Running it again
replaces the rules.

Some clients cannot use a unix socket, such as Java services' MySQL
connections, which always reach the database's host on port 3306. Add
`--redirect=UID:PORT=LOCAL_PORT` to send a user's TCP connections to `PORT`,
on any address, to `LOCAL_PORT` on loopback instead. Repeat it for each user
and port. To send the reference's and candidate's MySQL connections to the
MySQL proxy's default listeners:

```
spectre contain --candidate-uid=1002 \
  --redirect=1002:3306=50071 \
  --redirect=1000:3306=50070
```

The contained user reaches a local port only through its own redirects, so
the candidate cannot reach the reference's listener. Redirects run before any
other address rewriting in the pod, such as a service mesh's, so a redirected
connection never reaches it. Run the proxy as a user that no redirect names,
or its own connections to the database are redirected back to it. Redirecting
uid 0 is refused.

## What a mirror does

### Ingress

Ingress is ready once the reference and candidate both accept connections and
its comparison schema is set up. With `--reflection`, the default, it loads both
services' protobuf descriptors with gRPC reflection and requires them to match.
Disable it with `--no-reflection` to compare using only `--descriptors-dir`.

Until it is ready, ingress answers every request with 503, so no request
reaches the reference without being mirrored. A candidate that accepts nothing
within `--candidate-startup-timeout` (5m by default) is quarantined, as is one
whose schema cannot be set up, for example because its descriptors differ from
the reference's. Ingress then stays unready.

Clients always receive the reference's response. Each pair of responses is
compared as the [comparison scripts](#comparison-scripts) describe. Without a
script rule, a response is decoded by its content type. A request no
rule covers, whose content type is not JSON, protobuf, or gRPC, quarantines the
candidate unless it is a Connect GET.

SPECTRE cannot yet compare streaming RPCs. The first streaming RPC request that
no script rule ignores quarantines the candidate as `unable`, and only the
reference receives it.

### Egress

The egress proxy forwards each reference service's dependency calls to
the real upstream and records the responses. A service sends each call to its
egress address with the dependency's host name in the `Host` header. Map each
dependency host to its real URL with a `--destination=HOST=URL` flag:

```sh
spectre proxy ... --destination=forecasts.example=http://127.0.0.1:50053
```

A `HOST` may be a glob, such as `--destination="*.example=http://127.0.0.1:50053"`,
where `*` also matches dots. A host takes the first destination, in pattern order,
that matches it. A reference call to a host no destination matches gets a `502`.

Egress pairs a candidate call only with a reference call made for the same
mirrored request. Ingress gives each mirrored request an ID, and adds it to the
copies the reference and candidate receive as a `spectre_request_id` member of
the W3C `baggage` header. Both services must pass `baggage` on from each request
to the dependency calls they make for it, as OpenTelemetry's `baggage`
propagator does. Egress removes the member before a call reaches a dependency,
and ingress replaces any that a client sends.

A candidate call without a request ID gets a `502` and quarantines the candidate
as `pairing`, as does one whose ID is malformed. A reference call without one
belongs to no mirrored request, such as a call for a request ingress did not
mirror or for work no request started. It is forwarded but never paired, so it
never quarantines.

Scripts declare dependency endpoints with `egress.http(pattern).match<Type>()`.
Types come from `--schema-dir`; descriptors are only needed for protobuf traffic. A candidate call waits up to `--match-window` (5s by default) for an
equivalent reference call and receives its recorded response. A candidate call
without a match gets a `502` and stops all later candidate calls until the process
restarts. Ingress then quarantines the candidate when its responses diverge.

A candidate call that gives up before it is answered, for example because its
own client timed out, also quarantines the candidate, whether or not it matched a
reference call. Giving up before a match is `unmatched`, and giving up later, such
as while waiting for the reference's response, is `unable`. Calls SPECTRE cancels
itself, while shutting down or after a quarantine, never quarantine.

A reference call for a mirrored request that no candidate call matches within
`--match-window` also quarantines the candidate, because the candidate left it
out. Reference calls left unmatched during shutdown never quarantine.

When the candidate legitimately makes fewer calls to an endpoint than the
reference, for example because it reads data the reference fetched again from a
cache or a session, mark the endpoint `optional()`. A reference call to it that
no candidate call matches within `--match-window` then expires without
quarantining, and the mirror logs `Candidate left out an optional reference
request` with its route. Candidate calls to the endpoint still pair with
reference calls as usual, and one with no match still quarantines. `optional()`
comes before `match` or `replaces`:

```ts
egress.http("POST users.example/users.UserService/ListUsers").optional().match<ListUsersRequest>();
```

When the candidate asks a dependency for data the reference read from another of
its endpoints, declare that the candidate's call `replaces` the reference's call
to that source endpoint. The candidate is then answered from the reference's
response. The source endpoint must be declared with `match`. `using` takes two
functions: `request` maps the candidate's request to the source request it pairs
with, and `response` builds the answer from the paired reference response.
Replacing never claims the source reference, so a candidate call to the source
still pairs with it. A source reference that a replacing call matched is not
left out:

```ts
import { egress } from "spectre";
import type { GetUserRequest, GetUserResponse, ListUsersRequest, ListUsersResponse } from "users";

egress.http("POST users.example/users.UserService/ListUsers").match<ListUsersRequest>();
egress
  .http("POST users.example/users.UserService/GetUser")
  .replaces<ListUsersRequest, ListUsersResponse>("POST users.example/users.UserService/ListUsers")
  .using<GetUserRequest, GetUserResponse>(
    (request) => ({ ids: [request.id] }),
    (request, reference) => ({ user: reference.users.find((user) => user.id === request.id) }),
  );
```

Every candidate call to a replacing endpoint is answered this way, as bare
protobuf or HTTP JSON in the request's format. One with no matching source
reference within `--match-window` gets a `502` and quarantines the candidate, as
does one whose source reference failed or did not succeed. Replace only
read-only endpoints, because the candidate's call is never sent.

### MySQL

Set `--mysql-upstream=HOST:PORT` to proxy a MySQL server as well, with the
`--mysql-user` and `--mysql-password` that the services use. The reference
connects to `--mysql-reference-listen` and the candidate to
`--mysql-candidate-listen`, both without TLS and with `mysql_native_password`.

Reference commands, including prepared statements, reach the server and their
replies come back unchanged. Candidate commands never reach it. Each candidate
text statement is answered with the reply that the reference got for the same
statement text in the same default database: its rows, its affected rows and
insert ID, or its error. A candidate that repeats the reference's writes and
transaction statements is therefore answered without writing anything.

The candidate can send only text statements and select a database. A prepared
statement, a column listing, or any other command gets an error and quarantines
the candidate as `unable`. Reference prepared statements are only forwarded.

Statements carry no request ID, so statements pair across all requests by text
alone, within `--match-window`. Each reference statement answers one candidate
statement. A candidate statement that no reference statement matches gets an
error, and quarantines the candidate as `unmatched`, as does a reference
statement that no candidate statement claims. Every text statement the
reference sends through the proxy must therefore be one that the mirrored
candidate also sends.

Saved replies wait for the candidate in memory. When they exceed
`--mysql-saved-replies-bytes`, the reference is still answered in full, and the
candidate is quarantined as `capacity`.

MySQL quarantines are egress quarantines with the protocol `mysql`. Their route
is the statement's first keyword, such as `SELECT` or `COMMIT`, or `statement`
when it starts with none, or `command` for a command that is not a statement.
They never include a statement or its values.

### Quarantine

A mirror quarantines its candidate for one of these reasons:

| Kind | Reason |
| --- | --- |
| `divergent` | The candidate's response differed from the reference's. |
| `unmatched` | The candidate or reference made a dependency call the other did not. |
| `unlisted` | A request's route is in no node of the [migration state](#migration-state). |
| `pairing` | The mirror could not pair the candidate's traffic with the reference's. |
| `unable` | The mirror could not compare or replay the traffic. |
| `capacity` | Candidate work exceeded a concurrency or buffer limit. |

A mirrored request that does not finish within `--candidate-timeout` quarantines
the candidate as `unable`. The reason says whether the candidate or the reference
was too slow, or whether the comparison did not finish.

A quarantined candidate gets no more traffic until the mirror restarts. The
reference and its clients are unaffected.

A cancelled reference request never quarantines the candidate. When a client
cancels a request, or the reference's response is aborted, ingress stops the
candidate's copy and compares neither response. A candidate dependency call that
pairs with a reference call the reference gave up on gets a `502` without
quarantine.

A divergence report lists the paths where the responses differ and how. It
never includes values, because production payloads can hold personal data:

| Mark | Meaning |
| --- | --- |
| `-` | The reference has a value at the path and the candidate does not. |
| `+` | The candidate has a value at the path and the reference does not. |
| `~` | Both have values of the same JSON type that differ. |
| `!` | Both have values of different JSON types, which the report names. |

A different status is reported at `$status` with both statuses, such as
`200 → 500`. Differences at several list indexes or map entries merge into one
entry with `[*]` and a count, such as `- $.fees[*] ×2`. A report holds at most
100 differences and says when it reached that cap.

## Comparison scripts

### Schemas

`--schema-dir` is required. Every `.ts` file in it, including subdirectories,
contains only `declare module "name" { ... }` blocks. A type's name is its module
name followed by its namespace path, such as `weather.GetForecastV2Response`, so
separate services cannot collide. Modules share nothing unless one imports another.
Use interfaces or object aliases, namespaces, optional members, scalar types, arrays,
`Record<string, T>`, and string-literal unions. Unsupported constructs fail at
startup. Raw JSON must match the declared shape; unknown keys, missing required
members, and nulls are rejected.

A member holds null only if its type is `T | null` or `null | T`, where `T` is any
supported type; `note?: T | null` also lets it be absent. Elsewhere, such as in array
elements or `Record` values, and as a bare `null` or any other union, null is
unsupported, as are nullable members of protobuf messages. A null is its own JSON
type in difference reports and reaches the member's field normalisers as `null`;
message normalisers and normalisers of fields beneath a null object skip it:

```ts
declare module "processing_fees" {
  export interface Fee {
    fee_plan_id: string | null;
    note?: string | null;
  }
}
```

Protobuf traffic also needs binary `FileDescriptorSet` files, each including its
imports, in `--descriptors-dir`. Every `.pb` file in it, including
subdirectories, is loaded.

### Generating schemas

For protobuf traffic, generate declarations with
`spectre gen proto --descriptors-dir=DIR --output=DIR`, which writes one `.d.ts`
file per protobuf file.
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

For editor support, write the `spectre` module declaration with
`spectre gen module --output=spectre.d.ts`.

### Scripts

`--scripts-dir` loads every `.ts` script, including subdirectories. Scripts can
import relative modules inside the directory, the virtual `spectre` module, and
types from schema modules. At startup the host type-checks the scripts against the
schema with the TypeScript 7 checker, and any error stops it.

```ts
import { field, ingress } from "spectre";
import type { GetForecastV2Response } from "weather";

ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
field<GetForecastV2Response, "alerts">((alerts) =>
  alerts?.sort((a, b) => (a.id ?? "").localeCompare(b.id ?? "")),
);
```

The `spectre` module provides:

| Function | Purpose |
| --- | --- |
| `ingress.http(select)` | Start a rule that decides how responses are compared. |
| `egress.http(pattern)` | Declare a dependency endpoint, as `<METHOD> <host>/<path>`. |
| `field<T, "path">(normalise)` | Normalise one field of every `T`. |
| `message<T>(normalise)` | Normalise every `T`. |
| `variable(name)` | Read a script variable. |

Type arguments are required, and they must name schema types. The host reads them
from the checker, so aliases and namespace imports work.

### Normalisers

`field<T, "path">(callback)` uses JSON field names. Paths descend through objects
with dots and through lists with `[]`, such as `users[].name`.
`message<T>(callback)` applies wherever that object type occurs. Missing optional
values arrive as `undefined`; returning `undefined` removes them.

### Ingress rules

`ingress.http(select)` starts a rule for requests matching a ServeMux pattern, such
as `GET /v2/forecast` or `/` for every request, or that a function accepts. The
function receives the method, path, and headers by lower-case name.
`.requestHeader(name, ...)` narrows it to requests whose header, with its values
joined by `, `, equals a string, or that a function accepts; the function receives
`undefined` for a missing header. `.status(...)` narrows it to responses with the
given codes, or whose status a function accepts, and `.responseHeader(name, ...)`
to responses by a header as `.requestHeader` does. A rule may name several
headers, and must match all of them. Rules apply in declaration order, and the
first that selects a response decides how it is compared. Both sides share the
request, but each selects a rule by its own status and headers, and responses no
rule selects are decoded by their content type. A rule with only a pattern and
request header values that ignores a request skips it before either response
arrives.

`.match<T>(normalise?)` decodes the body as `T` and compares the responses after the
normaliser, which returns a new `{ status, body }`; members it leaves out are not
compared. `T` may be `Text` to compare the body as exact text. `.ignore()` skips
comparison, so the responses never quarantine the candidate:

```ts
import { ingress, type Text } from "spectre";

ingress.http("GET /_status").ignore();
// Compare only the status of server errors, whose bodies differ between stacks.
ingress.http("/").status((status) => status >= 500).match<Text>(({ status }) => ({ status }));
ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
```

### Script variables

Values that differ between environments, such as dependency hosts, come from
script variables. Pass each with `--script-variable=NAME=VALUE`, and read it with
`variable(NAME)` from the `spectre` module. A script that reads an unset variable
fails to load:

```ts
import { egress, variable } from "spectre";
import type { FetchForecastRequest } from "forecasts";

egress.http(`GET forecasts.${variable("environment")}.example/v1/forecasts/{location}`).match<FetchForecastRequest>();
```

## Migration state

A migration state file records a service's modules as nodes, what each depends
on, and whether each is migrated yet. With `--state-file`, ingress compares only
what the candidate already implements:

- A request on a route of a `migrated` node is mirrored and compared.
- A request on a route of a `pending` node goes to the reference only.
- A request on a route that no node lists goes to the reference only, and
  quarantines the candidate as `unlisted`. Routes that scripts ignore and gRPC
  reflection need no node.

Without a state file, ingress mirrors every request.

Each node is a table under `nodes`, keyed by a kebab-case ID:

```toml
[nodes.money]
status = "migrated"

[nodes.ledger-client]
depends_on = ["money"]
status = "migrated"

[nodes.ledger-client.egress.http]

[nodes.invoices-api]
depends_on = ["money", "ledger-client"]
status = "migrated"

[nodes.invoices-api.ingress.http.routes]
"GET /v1/invoices/{id}" = {}
"POST /example.invoices.v1.InvoiceService/CreateInvoice" = {}

[nodes.reports-api]
depends_on = ["money"]
status = "pending"

[nodes.reports-api.ingress.http.routes]
"GET /v1/reports/{id}" = {}
```

| Field | Meaning |
| --- | --- |
| `depends_on` | IDs of nodes whose code this node calls or imports. |
| `status` | `pending`, only in the reference, or `migrated`, in the candidate as well. Defaults to `pending`. |
| `<direction>.<protocol>` | A kind of IO the node's own code performs, holding that kind's settings. |

A node with no IO is a library, shared by more than one other node. Each route
belongs to one node, so a divergence on a route also names the node to fix.
Ship the state file with the candidate from the same commit, so the mirrored
routes always match what the candidate implements.

`spectre state types` describes each node type and its settings, for a person or
agent writing the file. `--json` prints JSON.

`spectre state validate` checks the file, `spectre/state.toml` by default or
`--state-file`. It fails on errors and warns about nodes that no ingress node
reaches, which are dead code or run from something SPECTRE does not mirror. An
invalid state also stops `spectre proxy` at startup.

`spectre next` lists the nodes to migrate next, dependencies first, so that
ingress traffic can verify them as soon as they are migrated. Each step holds one
pending ingress node and the pending nodes it depends on. It picks the step with
the fewest nodes that have IO, then the fewest nodes, then the lowest ingress
node ID. `--json` prints each node as JSON, with the settings of each kind of
IO it has. It prints that every node is migrated when none is pending, and
fails when no ingress node reaches the pending nodes left.

`spectre done <node>...` marks nodes migrated after they are added to the
candidate. It rewrites the file in a canonical layout, with keys sorted and
comments dropped, so after the first rewrite each change is only status lines.
It writes nothing when a node is unknown or the state would be invalid, for
example because a node depends on one still pending, and nothing when every
node is already migrated. It does not commit.

## SPECTRE server

`spectre serve` collects the states that mirrors stream to it: each mirror's
candidate revision, SPECTRE version, uptime, and quarantines. The server also records each quarantine as a
divergence that outlives the mirror run. It listens on `127.0.0.1:50070` by
default and keeps everything in memory, so a restart loses it. To keep them in Redis instead, pass a Redis URL to `--storage`, such as
`--storage=redis://localhost:6379/0`. For a Redis Cluster, use the
`redis-cluster` scheme, or `rediss-cluster` for TLS, and list further seed
nodes with `addr`, such as
`--storage=redis-cluster://localhost:7000?addr=localhost:7001`.

Every mirror reports to a server, so `spectre proxy` requires its URL,
`--server-url`, and the mirror's identity, `--service`, `--environment`, and
`--revision`, the candidate's commit. Set them in a deployment with the
matching `SPECTRE_SERVER_URL`, `SPECTRE_SERVICE`, `SPECTRE_ENVIRONMENT`, and
`SPECTRE_REVISION` variables.

Where the server is only reachable through a service mesh's unix egress socket,
pass the socket as `--server-socket` and the server's mesh address as
`--server-url`. Every call dials the socket, and the mesh routes it by the URL's
host, such as `--server-url=http://spectre.mesh --server-socket=@egress.sock`.

A mirror sends everything to the server over one stream, which begins with its
whole state. It then sends each quarantine as it begins. Every
`--server-keepalive-interval` (30s by default) when nothing else is sent, it
sends the number of requests its ingress proxy has received on each route if
those changed, or else a keepalive. It counts at most 100 routes, and counts
requests to any further routes together. Keep the interval below the idle timeout of any proxy in front of the server;
Caddy, for one, ends a request stream after 60s without data. When the stream
fails, the mirror logs it and reconnects with backoff, from 1s up to 1m. It also
replaces the stream after `--server-stream-max-age` (10m by default, with
jitter), so streams spread across server replicas.

### Status

List the reporting mirrors and recorded divergences with
`spectre status --server-url=URL`. Repeat `--server-url` to list the mirrors of
several servers together, such as one per deployment; `status` fails if any of
them does, so no server's mirrors go missing. Filter with `--service`,
`--environment`, `--revision`, or `--node`, and print JSON with `--json`. Each
mirror lists the calls on each route whose responses it has compared, which lag
by up to the keepalive interval. Calls ingress does not mirror, or that arrive
after a quarantine, are not counted. A
mirror stays listed until it sends nothing for the server's `--state-ttl`
(90s by default), so keep that above the keepalive interval. A mirror stays
quarantined until it restarts.

```
feeplans-fe cc009bd staging-green-us-west-2 spectre v0.52.0, up 4h25m33s
  ingress http GET /v2/processing-fees: response differences (node processing-fees-v2)
    ! $.currency string → null
    ~ $.fees[0].rate_bps
    + $.fees[*].roundingMode ×4
    - $.fees[*].rounding_mode ×4
    - $.fees[*] ×2
  quarantined 2026-11-06T01:28:53Z
```

The server keeps the latest divergence of each direction of a service
environment, through redeploys and stopped mirrors, until someone clears it with
`spectre status clear --service=SERVICE --environment=ENVIRONMENT`, which
clears it on every server `status` calls. A
clear also ignores the quarantines that mirrors still running report, so a
cleared divergence only returns if the candidate quarantines again.

## Custom binaries

A team can build its own `spectre` binary that keeps every built-in command and
adds its own. Import `github.com/block/spectre` and call `spectre.Main` with
options:

```go
package main

import "github.com/block/spectre"

func main() {
	spectre.Main(
		spectre.WithCommand("image", "Build a Docker image.", &ImageCommand{}),
		spectre.WithTemplateRepo("git+ssh://github.com/example/spectre-templates"),
		spectre.WithServerURL("http://spectre.internal:50070"),
	)
}
```

- `WithCommand` adds a top-level command. Its flags work like built-in flags,
  including environment variables and the config file.
- `WithTemplateRepo` sets the default template repository for `spectre init`.
- `WithServerURL` sets the default `--server-url`.
- `WithCollectorServers` sets the collector servers `spectre status` calls by
  default, such as every deployment's server. Pass it after `WithServerURL`, which also sets
  them.
- `WithKongOptions` passes options to the command-line parser, such as
  `kong.Vars{"version": ...}` to set the `--version` string.
