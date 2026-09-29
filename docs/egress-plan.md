# Egress proxy implementation plan

Implement the egress proxy from [the design](design.md). Reference requests pass
through to the dependency. Candidate requests never reach it. Instead, each
candidate request is matched with an equivalent reference request from the last
few seconds, and it receives that reference response. A candidate request with
no match is quarantined.

Requests are matched by the hash of their normalised form, not by trace ID.
Trace propagation is deferred.

The first target is a Ruby service with a raw HTTP JSON
API. Its API is modelled as a synthetic protobuf service, so JSON Schema support
is deferred.

## Script contract

Comparator functions become normalisers. Hashing a request requires the
normalised form of a single payload, and pairwise boolean comparators can't
produce one.

- `field` and `message` register a function that takes one value and
  returns its normalised form. A missing value is passed as `undefined`.
- A normaliser can sort values, drop parts of a value, or return a constant to
  ignore a node. Returning `undefined` removes the node.
- The host normalises each payload independently, starting at the leaves. The
  existing precedence rules still apply. Parents receive children that are already
  normalised.
- Two payloads are equivalent when their normalised documents are structurally
  equal. Differences are reported as paths without values.
- A thrown error, or a result that can't be represented as JSON, means the host
  is unable to compare.
- The hash is SHA-256 over the method name and the canonical JSON of the normalised
  document. Canonical JSON uses sorted keys.
- `deepEqual` is removed, because equality is now the host's job.

## Schemas

All schemas are protobuf. A raw HTTP JSON API is modelled as a synthetic protobuf
service, so decoding, target resolution, and traversal work unchanged. JSON
Schema is deferred until an API can't be modelled this way.

Schemas come from either or both of these sources:

- gRPC reflection from a backend, which is the current ingress default.
- Protobuf descriptor set files.

Each proxy takes a `--scripts-dir` and loads every script in it as one set.
Normalisers are keyed by protobuf type, not by direction, so ingress applies them
to responses and egress applies them to requests.

Scripts type raw HTTP requests by mapping them to RPC methods with
`spectre.endpoint(pattern, method)`. The pattern uses `net/http.ServeMux` syntax,
so matching is reused rather than reinvented. A method supplies both sides:
ingress decodes responses as its output, and egress decodes requests as its input.

- Ingress patterns omit the host, for example
  `spectre.endpoint("GET /v2/forecast", "spectre.sample.v1.WeatherService.GetForecastV2")`.
- Egress patterns include the destination host.
- gRPC and Connect requests name their method in the path, so they need no
  endpoint.
- Startup fails if endpoint patterns are invalid or conflict. An endpoint whose
  method is missing or streaming fails when the schema loads, so the proxy never
  becomes ready.
- A request matches only patterns for its own method. Unlike `net/http.ServeMux`,
  a `GET` pattern doesn't match `HEAD`.
- The method name is part of the hash.

Raw HTTP endpoints use a plain JSON protocol, unless the request is gRPC. The body
is decoded as ProtoJSON for the method's output message, and unknown fields are
rejected. Two empty bodies, such as `HEAD` or `204` responses, are equal. The status is
compared as an HTTP status rather than a Connect error. Ingress never decodes
requests, so synthetic methods can take `google.protobuf.Empty`.

Egress decodes raw HTTP requests as the method's input message. It uses the
`google.api.http` binding rules, but takes them from the endpoint rather than from
annotations.

- ServeMux path wildcards bind to fields of the same name.
- `GET` has no body, and other methods bind the body to the whole message.
  Binding the body to one field is deferred until an API needs it.
- Query parameters bind to the remaining fields by name. Nested fields use dotted
  names, and repeated keys fill repeated fields. Values are parsed for the
  field's type, so enums accept names.
- A query parameter with no matching field fails decoding, like an unknown JSON
  field.

Synthetic services follow these rules:

- Fields are proto3 `optional`, so an explicit zero differs from a missing key.
- A method has one output message. Error body fields that don't overlap the success
  fields go in the same message.

Known limitations:

- ProtoJSON treats `null` like a missing field, so switching between the two goes
  unnoticed.
- Bodies that aren't JSON, such as Rails HTML error pages, can't be decoded.

An egress request that no endpoint or method covers, or that fails to decode,
causes a quarantine. Ingress keeps its current policy. It skips unsupported content
types, but a JSON request that resolves to no endpoint or method quarantines the
candidate. Every raw HTTP path served through ingress therefore needs an endpoint.

## Egress behaviour

- One egress process serves every dependency. It has separate listeners for the
  reference and the candidate. The candidate listener must be a loopback IP address
  or a unix socket.
- Reference requests are always forwarded. Candidate requests are held and are
  never forwarded under any condition.
- The request host selects the destination. A configured destination list maps
  each host to its upstream URL, including whether it uses TLS. A request from
  either side for an unconfigured host quarantines the candidate. A reference
  request for an unconfigured host is still forwarded as received.
- Services must send plaintext HTTP to egress so that egress can read requests.
  Egress adds TLS on the way to the upstream.
- Reference requests:
  - The request is forwarded immediately, even when decoding or hashing fails.
  - It is normalised and hashed in parallel, so hashing never delays it.
  - The response is recorded under the hash for a configurable window, 5 seconds by
    default. The recording covers status, headers, body, and trailers.
- Candidate requests:
  - The request is normalised and hashed.
  - An unused reference entry with the same hash is a match. The host waits for
    that reference response, replays it, and marks the entry used.
  - A request still unmatched when the window closes gets an error and quarantines
    the candidate.
- Each reference entry is consumed at most once. Identical requests therefore pair
  one-to-one.
- Recorded responses share a memory budget. A candidate whose match exceeded the
  budget is quarantined.
- Quarantine is local to the egress process and lasts until it restarts. The
  candidate's responses then diverge, so ingress quarantines too.

## Reuse

- Move these from ingress into a shared proxy package:
  - the transport, taking explicit limits instead of `ingress.Config`
  - reverse proxy setup and forwarding-header cleanup
  - backend identity and listener loop checks
  - candidate admission, quarantine, and shutdown tracking
  - the serve lifecycle, extended to more than one listener
  - the buffer budget
- Record responses at the transport layer. Replay then goes through the standard
  reverse proxy, which already handles trailers.
- Use `netaddr`, the health and logging middleware, and the logger unchanged.

## Implementation checklist

### 1. Change the script contract to normalisers

- [x] Replace boolean comparator invocation with normaliser invocation. Validate
  results through a JSON round trip.
- [x] Normalise each document independently, starting at the leaves. Keep the
  existing precedence and descending array-index order.
- [x] Compare normalised documents structurally and report difference paths.
- [x] Expose normalisation separately from comparison so egress can hash one
  payload.
- [x] Remove `deepEqual`. Update `spectre.js`, the sample `comparison.js`, and the
  contract in [the ingress plan](plan.md).
- [x] Update the comparison, JavaScript, and integration tests for the new
  contract.

### 2. Load static schemas and route raw HTTP

- [x] Load descriptor set files as well as reflection. Keep reflection as the
  ingress default.
- [x] Replace the comparison script with a scripts directory, and let scripts
  declare raw HTTP endpoints typed by RPC methods.
- [x] Add the plain JSON protocol for raw HTTP endpoints.
- [x] Add a sample raw HTTP JSON service with a synthetic protobuf service.
- [ ] Write the first target's synthetic service.

### 3. Extract shared proxy code

- [ ] Move the shared pieces out of ingress without changing behaviour. The
  existing ingress tests cover this.

### 4. Normalise and hash requests

- [ ] Decode request payloads as the method's input message. Bind raw
  HTTP path wildcards, query parameters, and body by the rules above.
- [ ] Hash the canonical normalised document together with the method name.
- [ ] Test hash stability across field order, ignored fields, and sorted
  collections.

### 5. Build the egress proxy

- [ ] Add the egress package and `cmd/spectre-egress`, with a symlink in `scripts`.
- [ ] Implement recording, time-window matching, single consumption, replay, the
  memory budget, and quarantine.
- [ ] Test the following:
  - Candidate traffic never reaches the dependency.
  - Reference traffic is forwarded for unconfigured hosts and undecodable payloads.
  - Replay preserves the response, including trailers.
  - A candidate that arrives first waits for the reference.
  - Window expiry and budget overflow quarantine the candidate.
  - Duplicate requests pair one-to-one.
  - Shutdown completes cleanly.

### 6. Exercise it end to end

- [ ] Add an optional raw HTTP dependency mode to `spectre-sample`.
- [ ] Extend the Procfiles and integration test. Cover replay, exactly one
  dependency call per ingress request, and quarantine on divergence.
- [ ] Add build, release, and container entries for `spectre-egress`. Update
  `CONTRIBUTING.md`.

## Open decisions

- Whether a quarantine on a miss also logs differences against the closest unused
  reference request for the same method.
- Which protocols the first target uses for its dependencies. Anything other than
  HTTP, such as MySQL or Redis, needs its own egress support.
- How services address egress: a proxy setting such as `HTTP_PROXY`, or
  rewriting dependency URLs to point at egress with the original host preserved.
