# Contributing

See [USAGE.md](USAGE.md) for how to run and configure SPECTRE.

## Container images

Build the local Alpine-based image with `bit container`.

Build the [demo](demo/README.md) image with `bit demo`. It carries the sample
services and their comparison config for the containers of
[`demo/mirror-pod.yaml`](demo/mirror-pod.yaml).

`bit test-mirror-pod` runs that pod in a local kind cluster with Docker and
checks that it mirrors, replays egress, and refuses the candidate's direct IP
traffic.

## Sample Connect service

From the repository root with Hermit activated, build and run the sample:

```sh
bit sample
spectre-sample
```

The sample serves Connect, gRPC, and gRPC-Web on `127.0.0.1:50051` with
reflection enabled.
Use `--listen=127.0.0.1:50052` to run a second instance and `--data=path/to/users.json`
to load a different ProtoJSON `ListUsersResponse` at startup. With `grpcurl` installed,
call it directly:

```sh
grpcurl -plaintext -d '{}' localhost:50051 spectre.sample.v1.UserService/ListUsers
grpcurl -plaintext -d '{"id":"user-1"}' localhost:50051 spectre.sample.v1.UserService/GetUser
grpcurl -plaintext -d '{"role":"ROLE_READER"}' localhost:50051 spectre.sample.v1.UserService/ListUsers
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
curl 'localhost:50051/v2/forecast?location=london'
curl 'localhost:50051/api/v1/forecast?location=sydney'
curl localhost:50051/_status
```

With `--forecasts=URL`, the weather endpoints fetch forecasts from a forecast
provider instead of their local data, sending `--forecasts-host`
(`forecasts.example` by default) as the host. Alerts still come from `--weather`.
Run the provider with `spectre-sample-forecasts`, which listens on
`127.0.0.1:50053` and serves `GET /v1/forecasts/{location}` from the same weather
data. The [forecasts script](internal/sample/scripts/forecasts.ts) declares this
endpoint for egress.

To run a whole mirror on unix sockets under `dist/sockets/`, with reloads on
change, run `proctor`. Like the containers of a mirror pod, the
[`Procfile`](Procfile) runs each of these as a process of its own: the forecast
provider, a SPECTRE server, reference and candidate samples in dependency mode,
and `spectre proxy` in front of them:

```sh
curl --unix-socket dist/sockets/ingress.sock 'http://ingress/v2/forecast?location=london'
grpcurl -unix -plaintext -d '{}' "$(pwd)/dist/sockets/ingress.sock" spectre.sample.v1.UserService/ListUsers
```

Edit the [protobuf definitions](internal/sample/proto/service.proto) and run
`bit sample` to regenerate the Go and Connect bindings, descriptors, and TypeScript
declarations. Generated declarations are checked in alongside the Go bindings.

## Integration tests

The Go tests in [`internal/integration`](internal/integration) each run
[`Procfile.integration`](Procfile.integration) with proctor, from a directory of
their own under `/tmp`, so they run in parallel without sharing sockets. Each test
drives the mirror over its sockets and checks what the processes log. The tests
are skipped with `-short`, which `bit test-go` passes. Run them with
`bit test-integration`, or with `go test ./internal/integration` once Hermit is
active. A failing test logs proctor's whole output.

## Editor configuration

`tsconfig.json` gives editors the host's compiler options, so they report the errors
the host would. Keep it in step with `internal/typescript/program.go`.
