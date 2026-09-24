# Contributing

The `spectre-ingress` command validates local protobuf descriptor sets and mirrors
HTTP requests to reference and candidate services. It returns the reference response
without waiting for the candidate response.

Logs default to info-level, colorized text on stderr. Use `--log-level=debug|info|warn|error`
to change the minimum level and `--log-json` for JSON output.

## Sample gRPC service

From the repository root with Hermit activated, build and run the sample:

```sh
bit sample
./scripts/spectre-ingress check-schema dist/sample.pb
./scripts/spectre-sample
```

Run a second sample on port 50052, then start the ingress proxy:

```sh
./scripts/spectre-ingress serve \
  --reference=http://127.0.0.1:50051 \
  --candidate=http://127.0.0.1:50052
```

The proxy listens on `127.0.0.1:50050` by default. It accepts HTTP/1 and unencrypted
HTTP/2 so the sample can be called through the proxy with `grpcurl`.

The sample serves plaintext gRPC on `127.0.0.1:50051` with reflection enabled.
Use `--listen=127.0.0.1:50052` to run a second instance and `--data=path/to/users.json`
to load a different ProtoJSON `ListUsersResponse` at startup. With `grpcurl` installed:

```sh
grpcurl -plaintext -d '{}' localhost:50051 spectre.sample.v1.UserService/ListUsers
grpcurl -plaintext -d '{"id":"user-1"}' localhost:50051 spectre.sample.v1.UserService/GetUser
grpcurl -plaintext -d '{"role":"ROLE_READER"}' localhost:50051 spectre.sample.v1.UserService/ListUsers
```

The [sample data](internal/sample/testdata/users.json) includes nested messages,
repeated fields, maps, enums, oneofs, bytes, timestamps, and 64-bit integers beyond
JavaScript's safe integer range. Optional fields cover absent and explicitly empty
or false values. Responses preserve fixture order and timestamps for repeatable
comparisons. `GetUser` returns `InvalidArgument` for an empty ID and `NotFound` for
an unknown ID. `ListUsers` filters by IDs and an optional role.

Edit the [protobuf definitions](internal/sample/proto/service.proto) and run
`bit sample` to regenerate the Go bindings and descriptor set. `bit test` and
`bit lint` also generate them before running their checks.
