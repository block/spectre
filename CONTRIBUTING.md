# Contributing

See [USAGE.md](USAGE.md) for how to run and configure SPECTRE.

## Container images

Build the local Alpine-based image with `bit container`.

Build the [demo](demo/README.md) image with `bit demo`. It wraps a complete
supervised example around the sample service, including the cgroup split and
egress sandbox.

`bit test-mirror-pod` runs [`demo/mirror-pod.yaml`](demo/mirror-pod.yaml) in a
local kind cluster with Docker and checks that it mirrors, replays egress, and
refuses the candidate's direct IP traffic.

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

The sample serves Connect, gRPC, and gRPC-Web on `127.0.0.1:50051` with
reflection enabled.
Use `--listen=127.0.0.1:50052` to run a second instance and `--data=path/to/users.json`
to load a different ProtoJSON `ListUsersResponse` at startup. With `grpcurl` installed,
call it through ingress:

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

## Editor configuration

`tsconfig.json` gives editors the host's compiler options, so they report the errors
the host would. Keep it in step with `internal/typescript/program.go`.
