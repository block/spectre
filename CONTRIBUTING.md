# Contributing

The `spectre-ingress` command mirrors HTTP requests to reference and candidate
services. It returns the reference response without waiting for the candidate response.
The `spectre-egress` command sits between those services and their dependencies. It
forwards reference calls and replays the recorded responses to matching candidate calls.

Logs default to info-level, colorized text on stderr. Use `--log-level=debug|info|warn|error`
to change the minimum level and `--log-json` for JSON output.

Both commands accept `--config=FILE` to load flag values from a TOML file. Keys are
flag names without the leading dashes, and command-line flags take precedence:

```toml
scripts-dir = "internal/sample/scripts"
descriptors-dir = "dist/descriptors"

[destination]
"forecasts.example" = "http://127.0.0.1:50053"
```

Unknown keys are rejected.

## Container image

Build the local Alpine-based image with `bit container`. It contains both commands,
its entry point is `spectre-ingress`, and it runs as a non-root user. Start the
server with backends in the same network namespace:

```sh
docker run --rm --network=host \
  -v "$PWD/internal/sample/scripts:/scripts:ro" \
  -v "$PWD/dist/descriptors:/descriptors:ro" \
  spectre:dev \
  --listen=0.0.0.0:50050 \
  --reference=h2c://127.0.0.1:50051 \
  --candidate=h2c://127.0.0.1:50052 \
  --scripts-dir=/scripts \
  --descriptors-dir=/descriptors
```

Run the egress proxy from the same image with `--entrypoint spectre-egress`.

Releases are published for Linux AMD64 and ARM64 as `ghcr.io/block/spectre`
and `docker.io/blockossreleases/spectre`.

## Egress proxy

`spectre-egress` has three listeners. Reference services send dependency calls to
`127.0.0.1:50060`, candidates send them to `127.0.0.1:50061`, and `/livez` and
`/readyz` are served on `127.0.0.1:50062`. The candidate listener must use a
loopback IP or a unix socket. Each `--destination=HOST=URL` flag sends requests
for `HOST` to `URL`:

```sh
spectre-egress \
  --destination=forecasts.example=http://127.0.0.1:50053 \
  --scripts-dir=internal/sample/scripts \
  --descriptors-dir=dist/descriptors
```

Scripts declare dependency endpoints with `spectre.egress()`, and their types come
only from `--descriptors-dir`. A candidate call waits up to `--match-window` for an
equivalent reference call and receives its recorded response. A candidate call
without a match gets a `502` and stops all later candidate calls until the process
restarts. Ingress then quarantines the candidate when its responses diverge.

## Sample Connect service

From the repository root with Hermit activated, build and run the sample:

```sh
bit sample
spectre-sample
```

Run a second sample on port 50052, then start the ingress proxy:

```sh
spectre-ingress \
  --reference=h2c://127.0.0.1:50051 \
  --candidate=h2c://127.0.0.1:50052 \
  --scripts-dir=internal/sample/scripts \
  --descriptors-dir=dist/descriptors
```

The proxy listens on `127.0.0.1:50050` by default. It accepts HTTP/1 and unencrypted
HTTP/2 so the sample can be called through the proxy with `grpcurl`. Use `h2c://`
for a plaintext HTTP/2 backend. Candidate backends must use a literal loopback IP.
If candidate buffering or concurrency reaches its limit, the proxy cancels all
candidate requests and stops mirroring until the process restarts.

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
They are not exposed through reflection, so the proxy types them with the synthetic
`WeatherService` in [weather.proto](internal/sample/proto/weather.proto), loaded
from `dist/descriptors/`. Use `--weather=path/to/weather.json` to load different
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
data. The [forecasts script](internal/sample/scripts/forecasts.js) declares this
endpoint for egress.

To run the whole stack on unix sockets under `dist/sockets/`, with reloads on
change, run `proctor`. It starts the forecast provider, egress, reference and
candidate samples in dependency mode, and ingress:

```sh
curl --unix-socket dist/sockets/ingress.sock 'http://ingress/v2/forecast?location=london'
```

Edit the [protobuf definitions](internal/sample/proto/service.proto) and run
`bit sample` to regenerate the Go and Connect bindings and descriptor set with
Buf. `bit test` and `bit lint` also generate them before running their checks.
