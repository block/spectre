# SPECTRE

SPECTRE (Service Pair Execution Comparator for Traffic Response Equivalence) is a system for validating service migrations against real traffic. It runs a reference implementation and a candidate implementation in parallel, then compares their externally observable behaviour.

The reference implementation remains authoritative for client responses and side effects. The candidate runs in a network sandbox: SPECTRE intercepts its outbound requests, compares them with the reference implementation's requests, and replays the reference results without allowing the candidate to make real writes.

Semantic differences are captured for analysis and can be fed back into an LLM-assisted migration workflow. A candidate is quarantined when its behaviour diverges from the reference implementation.

SPECTRE is primarily intended for language and framework migrations, but the same approach can help validate dependency upgrades, service consolidation, and other behaviour-sensitive changes.

See the [design document](docs/design.md) for the proposed architecture and safety model.

## Try it

The builtin sample service uses `internal/sample/comparison.js` as the comparator script. You can run the Spectre ingress service and two backends like so:

```
$ proctor
candidate ▶ starting
reference ▶ starting
reference │ INF Sample Connect server listening address=127.0.0.1:50051
candidate │ INF Sample Connect server listening address=127.0.0.1:50052
reference ● ready
candidate ● ready
  ingress ▶ starting
reference │ INF HTTP request method=POST path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo status=200 duration=1.139167ms
  ingress │ INF Ingress proxy listening address=127.0.0.1:50050
candidate │ INF HTTP request method=POST path=/grpc.reflection.v1.ServerReflection/ServerReflectionInfo status=200 duration=1.575958ms
  ingress ● ready

```

In another terminal issue a gRPC request:

```
$ grpcurl -plaintext -d '{}' localhost:50050 spectre.sample.v1.UserService/ListUsers
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
  ingress │ DBG Response comparator completed kind=field target=spectre.sample.v1.User.roles response_path=$.users[0].roles matched=true
  ingress │ DBG Response comparator completed kind=field target=spectre.sample.v1.User.roles response_path=$.users[1].roles matched=true
  ingress │ DBG Response comparator completed kind=field target=spectre.sample.v1.User.roles response_path=$.users[2].roles matched=true
  ingress │ DBG Response comparator completed kind=field target=spectre.sample.v1.ListUsersResponse.generated_at response_path=$.generatedAt matched=true
  ingress │ DBG Response comparison completed path=/spectre.sample.v1.UserService/ListUsers outcome=equivalent
```

This shows the various registered comparison functions applying to the responses from the two backends, then structural comparison succeeding.
