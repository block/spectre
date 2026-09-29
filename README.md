# SPECTRE

SPECTRE (Service Pair Execution Comparator for Traffic Response Equivalence) is a system for validating service migrations against real traffic. It runs a reference implementation and a candidate implementation in parallel, then compares their externally observable behaviour.

The reference implementation remains authoritative for client responses and side effects. The candidate runs in a network sandbox: SPECTRE intercepts its outbound requests, compares them with the reference implementation's requests, and replays the reference results without allowing the candidate to make real writes.

Semantic differences are captured for analysis and can be fed back into an LLM-assisted migration workflow. A candidate is quarantined when its behaviour diverges from the reference implementation.

SPECTRE is primarily intended for language and framework migrations, but the same approach can help validate dependency upgrades, service consolidation, and other behaviour-sensitive changes.

See the [design document](docs/design.md) for the proposed architecture and safety model.

## Try it

The builtin sample service uses the normaliser scripts in `internal/sample/scripts/`. `bit sample` generates the sample's descriptor set, then `proctor` runs the Spectre ingress and two sample backends, all over unix domain sockets under `dist/sockets/`:

```
$ bit sample
$ proctor
            setup ● ready
        reference │ INF Sample Connect server listening address=.../dist/sockets/reference.sock
        candidate │ INF Sample Connect server listening address=.../dist/sockets/candidate.sock
  reference-ready ● ready
  candidate-ready ● ready
          ingress │ INF Ingress proxy listening address=.../dist/sockets/ingress.sock
    ingress-ready ● ready

```

In another terminal issue a gRPC request over the ingress socket. `grpcurl -unix` needs an absolute socket path:

```
$ grpcurl -unix -plaintext -d '{}' "$(pwd)/dist/sockets/ingress.sock" spectre.sample.v1.UserService/ListUsers
...
```

The first terminal should then output something like this:


```
candidate │ INF HTTP request method=POST path=/spectre.sample.v1.UserService/ListUsers status=200 duration=783.958µs
candidate │ INF HTTP request method=POST path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo status=200 duration=3.499958ms
reference │ INF HTTP request method=POST path=/spectre.sample.v1.UserService/ListUsers status=200 duration=723.833µs
reference │ INF HTTP request method=POST path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo status=200 duration=3.078125ms
  ingress │ INF HTTP request method=POST path=/spectre.sample.v1.UserService/ListUsers status=200 duration=1.035708ms
  ingress │ INF HTTP request method=POST path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo status=200 duration=4.005333ms
  ingress │ DBG Response comparison completed path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo outcome=skipped reason="gRPC namespace is excluded from response comparison"
  ingress │ DBG Response comparison skipped reason="gRPC namespace is excluded from response comparison"
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.User.roles side=reference response_path=$.users[0].roles
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.User.roles side=reference response_path=$.users[1].roles
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.User.roles side=reference response_path=$.users[2].roles
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.ListUsersResponse.generated_at side=reference response_path=$.generatedAt
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.User.roles side=candidate response_path=$.users[0].roles
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.User.roles side=candidate response_path=$.users[1].roles
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.User.roles side=candidate response_path=$.users[2].roles
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.ListUsersResponse.generated_at side=candidate response_path=$.generatedAt
  ingress │ DBG Response comparison completed path=/spectre.sample.v1.UserService/ListUsers outcome=equivalent
```

This shows the registered normalisers sorting roles and removing the generation timestamp in each backend's response, then structural comparison succeeding.

The sample also serves raw HTTP JSON endpoints modelled on a legacy weather service. They have no RPC paths, so `internal/sample/scripts/weather.js` maps each one to a method of the synthetic `WeatherService` in `internal/sample/proto/weather.proto`. Issue one with `curl`:

```
$ curl --unix-socket dist/sockets/ingress.sock 'http://localhost/v2/forecast?location=london'
{"forecast":{"location":"london", ...}, "alerts":[{"id":"alert-1", ...}, ...]}
```

The first terminal should then show the alerts being sorted before comparison:

```
reference │ INF HTTP request method=GET path=/v2/forecast status=200 duration=111.334µs
candidate │ INF HTTP request method=GET path=/v2/forecast status=200 duration=107.625µs
  ingress │ INF HTTP request method=GET path=/v2/forecast status=200 duration=425.125µs
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.GetForecastV2Response.alerts side=reference response_path=$.alerts
  ingress │ DBG Response normaliser completed kind=field target=spectre.sample.v1.GetForecastV2Response.alerts side=candidate response_path=$.alerts
  ingress │ DBG Response comparison completed path=/v2/forecast outcome=equivalent
```

`/api/v1/forecast?location=sydney` and `/_status` are also available.
