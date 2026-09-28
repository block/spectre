# Egress proxy implementation plan

Implement the egress proxy from [the design](design.md). Reference requests pass
through to the dependency. Candidate requests never reach it. Instead, each
candidate request is matched with an equivalent reference request from the last
few seconds, and it receives that reference response. A candidate request with
no match is quarantined.

Requests are matched by the hash of their normalised form, not by trace ID.
Trace propagation is deferred.

The first target is `squareup/feeplans-fe`, a Ruby service using raw HTTP. Schema
loading must therefore support more than gRPC.

## Script contract

Comparator functions become normalisers. Hashing a request requires the
normalised form of a single payload, and pairwise boolean comparators can't
produce one.

- `field`, `message`, and `rpc` register a function that takes one value and
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
- The hash is SHA-256 over the route and the canonical JSON of the normalised
  document. Canonical JSON uses sorted keys.
- `deepEqual` is removed, because equality is now the host's job.

## Schemas

Ingress and egress share the schema registry code: schema loading, route
matching, and decoding. Ingress and egress each have their own route map.
Entries use `net/http.ServeMux` pattern syntax, `<method> <host>/<path>`, so
matching is reused rather than reinvented.

- Ingress entries omit the host, for example `POST /v1/fees => fees.schema.json`.
- Egress uses a single map. Every entry includes the destination host, for
  example `POST fees.example/v1/fees => fees.schema.json`.

Supported sources:

- gRPC reflection from a backend, which is the current ingress behaviour.
- Protobuf descriptor set files.
- JSON Schema files for raw HTTP. A request's normalised document contains its
  method, path, query, and decoded JSON body. A response's contains its status and
  decoded JSON body.

An egress request that no schema covers, or that fails to decode, causes a
quarantine. Ingress keeps its current policy of skipping it.

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

### 2. Add the schema registry

- [ ] Define the registry, plus separate route maps for ingress and egress.
- [ ] Load schemas from gRPC reflection, descriptor set files, and JSON Schema
  files.
- [ ] Resolve script targets against JSON Schema definitions.
- [ ] Decode raw HTTP JSON requests and responses into normalised documents.
- [ ] Move ingress onto the registry. Keep reflection as its default.

### 3. Extract shared proxy code

- [ ] Move the shared pieces out of ingress without changing behaviour. The
  existing ingress tests cover this.

### 4. Normalise and hash requests

- [ ] Decode request payloads for gRPC and Connect using the method's input
  message, and for raw HTTP using its JSON Schema.
- [ ] Hash the canonical normalised document together with the route.
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

- How script targets name JSON Schema locations, since protobuf full names don't
  apply.
- Whether a quarantine on a miss also logs differences against the closest unused
  reference request for the same route.
- Which protocols `feeplans-fe` uses for its dependencies. Anything other than
  HTTP, such as MySQL or Redis, needs its own egress support.- How services address egress: a proxy setting such as `HTTP_PROXY`, or
  rewriting dependency URLs to point at egress with the original host preserved.
