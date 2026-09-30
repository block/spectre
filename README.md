# SPECTRE

SPECTRE (Service Pair Execution Comparator for Traffic Response Equivalence) is a system for validating service migrations against real traffic. It runs a reference implementation and a candidate implementation in parallel, then compares their externally observable behaviour.

The reference implementation remains authoritative for client responses and side effects. The candidate runs in a network sandbox: SPECTRE intercepts its outbound requests, compares them with the reference implementation's requests, and replays the reference results without allowing the candidate to make real writes.

Semantic differences are captured for analysis and can be fed back into an LLM-assisted migration workflow. A candidate is quarantined when its behaviour diverges from the reference implementation.

SPECTRE is primarily intended for language and framework migrations, but the same approach can help validate dependency upgrades, service consolidation, and other behaviour-sensitive changes.

See the [design document](docs/design.md) for the proposed architecture and safety model.

## Try it

The builtin sample service uses the normaliser scripts in `internal/sample/scripts/`. `bit sample` generates the sample's descriptor set. Then `proctor` runs the whole exemplar over unix domain sockets under `dist/sockets/`:

- `forecasts`, a forecast provider that stands in for a downstream dependency.
- `egress`, the Spectre egress proxy between the samples and the forecast provider.
- `reference` and `candidate`, two sample backends that fetch forecasts through egress.
- `ingress`, the Spectre ingress proxy that mirrors client requests to both backends.

```
$ bit sample
$ proctor
    setup ● exit 0
forecasts │ INF Sample forecast provider listening address=.../dist/sockets/forecasts.sock
forecasts ● ready
   egress │ INF Proxy listening address=.../dist/sockets/egress-reference.sock
   egress │ INF Proxy listening address=.../dist/sockets/egress-candidate.sock
   egress │ INF Proxy listening address=.../dist/sockets/egress-health.sock
   egress ● ready
reference │ INF Sample Connect server listening address=.../dist/sockets/reference.sock
candidate │ INF Sample Connect server listening address=.../dist/sockets/candidate.sock
reference ● ready
candidate ● ready
  ingress │ INF Proxy listening address=.../dist/sockets/ingress.sock
  ingress ● ready
```

In another terminal issue a gRPC request over the ingress socket. `grpcurl -unix` needs an absolute socket path:

```
$ grpcurl -unix -plaintext -d '{}' "$(pwd)/dist/sockets/ingress.sock" spectre.sample.v1.UserService/ListUsers
...
```

The first terminal should then output something like this:


```
reference │ INF HTTP request method=POST path=/spectre.sample.v1.UserService/ListUsers status=200 duration=941.25µs
reference │ INF HTTP request method=POST path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo status=200 duration=3.49ms
candidate │ INF HTTP request method=POST path=/spectre.sample.v1.UserService/ListUsers status=200 duration=1.01725ms
candidate │ INF HTTP request method=POST path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo status=200 duration=3.748417ms
  ingress │ INF HTTP request method=POST path=/spectre.sample.v1.UserService/ListUsers status=200 duration=1.13775ms
  ingress │ INF HTTP request method=POST path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo status=200 duration=4.2135ms
  ingress │ DBG Response comparison completed path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo outcome=skipped reason="gRPC namespace is excluded from response comparison"
  ingress │ DBG Response comparison skipped reason="gRPC namespace is excluded from response comparison"
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.User.roles side=reference payload_path=$.users[0].roles
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.User.roles side=reference payload_path=$.users[1].roles
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.User.roles side=reference payload_path=$.users[2].roles
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.ListUsersResponse.generated_at side=reference payload_path=$.generatedAt
  ingress │ DBG Payload normalisation completed message=spectre.sample.v1.ListUsersResponse side=reference normalisers=4
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.User.roles side=candidate payload_path=$.users[0].roles
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.User.roles side=candidate payload_path=$.users[1].roles
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.User.roles side=candidate payload_path=$.users[2].roles
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.ListUsersResponse.generated_at side=candidate payload_path=$.generatedAt
  ingress │ DBG Payload normalisation completed message=spectre.sample.v1.ListUsersResponse side=candidate normalisers=4
  ingress │ DBG Response comparison completed path=/spectre.sample.v1.UserService/ListUsers outcome=equivalent
```

This shows the registered normalisers sorting roles and removing the generation timestamp in each backend's response, then structural comparison succeeding.

The sample also serves raw HTTP JSON endpoints modelled on a legacy weather service. They have no RPC paths, so `internal/sample/scripts/weather.js` maps each one to a method of the synthetic `WeatherService` in `internal/sample/proto/weather.proto`. Issue one with `curl`:

```
$ curl --unix-socket dist/sockets/ingress.sock 'http://localhost/v2/forecast?location=london'
{"forecast":{"location":"london", ...}, "alerts":[{"id":"alert-1", ...}, ...]}
```

Each backend fetches the forecast from the forecast provider through egress. `internal/sample/scripts/forecasts.js` types that dependency call so egress can match the candidate's call with the reference's. Egress forwards only the reference's call to the provider, then replays the recorded response to the candidate. The first terminal should show one provider request, two egress requests with their normalisation, the candidate's request matching the reference's, and the alerts being sorted before comparison. No normalisers are registered for the forecast request, so egress hashes it unchanged:

```
forecasts │ INF HTTP request method=GET path=/v1/forecasts/london status=200 duration=850.416µs
   egress │ INF HTTP request method=GET path=/v1/forecasts/london status=200 duration=4.520709ms
   egress │ DBG Payload normalisation completed message=spectre.sample.v1.FetchForecastRequest side=reference normalisers=0
   egress │ DBG Payload normalisation completed message=spectre.sample.v1.FetchForecastRequest side=candidate normalisers=0
   egress │ DBG Candidate request matched a reference request host=forecasts.example path=/v1/forecasts/london
   egress │ INF HTTP request method=GET path=/v1/forecasts/london status=200 duration=5.972042ms
reference │ INF HTTP request method=GET path=/v2/forecast status=200 duration=2.718583ms
candidate │ INF HTTP request method=GET path=/v2/forecast status=200 duration=2.736917ms
  ingress │ INF HTTP request method=GET path=/v2/forecast status=200 duration=3.01225ms
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.GetForecastV2Response.alerts side=reference payload_path=$.alerts
  ingress │ DBG Payload normalisation completed message=spectre.sample.v1.GetForecastV2Response side=reference normalisers=1
  ingress │ DBG Payload normaliser completed kind=field target=spectre.sample.v1.GetForecastV2Response.alerts side=candidate payload_path=$.alerts
  ingress │ DBG Payload normalisation completed message=spectre.sample.v1.GetForecastV2Response side=candidate normalisers=1
  ingress │ DBG Response comparison completed path=/v2/forecast outcome=equivalent
```

If the candidate makes a dependency call that the reference did not, egress answers it with a `502` and stops serving the candidate. The candidate's responses then diverge, and ingress quarantines it.

`/api/v1/forecast?location=sydney` and `/_status` are also available.
