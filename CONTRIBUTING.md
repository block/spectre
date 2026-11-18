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

The [`internal/integration`](internal/integration) tests run a whole mirror
each, in the test process: the services, the SPECTRE server and the proxy, on
free ports, so tests run in parallel without port conflicts. The
[`it`](internal/integration/it) package is their DSL. A test starts its mirror
with `it.Start`, passing the reference and candidate services, the dependencies,
and its comparison scripts as TypeScript inline in the Go file:

```go
m := it.Start(t,
	it.Scripts(`
		import { egress, ingress } from "spectre";
		import type { FetchForecastRequest } from "forecasts";
		import type { GetForecastV2Response } from "weather";

		ingress.http("GET /v2/forecast").match<GetForecastV2Response>();
		egress.http("GET forecasts.example/v1/forecasts/{location}").optional().match<FetchForecastRequest>();
	`),
	it.Reference(sampleService()),
	it.Candidate(sampleService(ownForecasts())),
	it.Dependency(forecastsHost, forecastProvider),
)
m.Ingress.Get("/v2/forecast?location=london").ExpectStatus(http.StatusOK)
m.ExpectCalls(it.Calls{"GET /v2/forecast": 1})
m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/london", 1)
```

The tests build their services from the sample's, with variations such as
`ownForecasts`. Tests send requests through ingress or either egress listener,
and check the responses, the requests each service and dependency received, and
the calls and quarantines the proxy reports to the server. The sample's schema
and descriptors and a 500ms match window are the defaults.

A test whose services read MySQL passes `it.MySQL` with the statements that
create its database. The services get a handle to it through the proxy in
`Backend.MySQL`, and `m.MySQL()` checks the statements that reached the server:

```go
m := it.Start(t,
	it.MySQL("CREATE TABLE orders (id BIGINT PRIMARY KEY, label VARCHAR(32) NOT NULL)"),
	it.Reference(ordersService(selectEachOrdersItems)),
	it.Candidate(ordersService(joinOrdersToItems)),
)
m.MySQL().ExpectSelects(map[string]uint64{"SELECT `id` , `label` FROM `orders`": 1})
```

The first such test starts a `mysql:8.4` container with `docker`, later ones
reuse it, and each test creates a database of its own in it. `TestMain`
removes the container once all tests have run. It also stops itself after 15
minutes, in case the process dies first. Without a running Docker daemon only
the MySQL tests fail.

Every file in both packages carries the `integration` build tag, so `bit test`
leaves them out and CI runs them in a separate job. Run them with
`bit test-integration`, or with
`go test -tags=integration ./internal/integration` once Hermit is active.

## Editor configuration

`tsconfig.json` gives editors the host's compiler options, so they report the errors
the host would. Keep it in step with `internal/typescript/program.go`.
