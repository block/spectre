# Contributing

The `spectre-ingress` command mirrors HTTP requests to reference and candidate
services. It returns the reference response without waiting for the candidate response.

Logs default to info-level, colorized text on stderr. Use `--log-level=debug|info|warn|error`
to change the minimum level and `--log-json` for JSON output.

## Container image

Build the local Alpine-based image with `bit container`. Its entry point is the
`spectre-ingress` command, and it runs as a non-root user. Start the server with
backends in the same network namespace:

```sh
docker run --rm --network=host spectre-ingress:dev \
	-v "$PWD/internal/sample/comparison.js:/comparison.js:ro" \
  --listen=0.0.0.0:50050 \
  --reference=h2c://127.0.0.1:50051 \
  --candidate=h2c://127.0.0.1:50052 \
  --comparison-script=/comparison.js
```

Releases are published for Linux AMD64 and ARM64 as `ghcr.io/block/spectre`
and `docker.io/blockossreleases/spectre`.

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
  --comparison-script=internal/sample/comparison.js
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

Edit the [protobuf definitions](internal/sample/proto/service.proto) and run
`bit sample` to regenerate the Go and Connect bindings and descriptor set with
Buf. `bit test` and `bit lint` also generate them before running their checks.
