# SPECTRE supervisor demo

This demo packages the `spectre` binary into a single container image. The
`spectre run` supervisor starts a reference and a candidate service behind both
SPECTRE proxies with one command: it runs the embedded ingress and egress
proxies, wires the services to them over unix sockets, sandboxes the candidate's
egress with seccomp, and splits host CPU and memory between the two services
with cgroups.

The services here are two copies of the builtin sample backend, standing in for
a reference and a candidate. Mirrored client traffic is compared and the
correlations are logged.

## Run it

```
$ demo/run.sh
```

This builds `demo/Dockerfile` and runs it, publishing the ingress on
`http://127.0.0.1:50050`. In another terminal:

```
$ curl -s '127.0.0.1:50050/v2/forecast?location=london'
$ grpcurl -plaintext -d '{}' 127.0.0.1:50050 spectre.sample.v1.UserService/ListUsers
```

The container logs each component's output, tagged by name, including the
correlation events:

```
egress   | INF Candidate request matched a reference request event=correlation method=GET host=forecasts.example path=/v1/forecasts/london
ingress  | INF Response comparison completed event=correlation method=GET path=/v2/forecast outcome=equivalent dt=-183µs
```

`dt` is the candidate's latency minus the reference's for the matched request.

The container runs `--privileged` so the supervisor can write cgroups and
install the candidate's seccomp filter. cgroups v2 and seccomp are Linux
features; on macOS and Windows the demo runs inside Docker's Linux VM.

### Tuning the split

Arguments to `demo/run.sh` are passed through to the supervisor. For example, to
give the candidate a quarter of the CPU under contention and run without the
egress sandbox:

```
$ demo/run.sh --candidate-cpu-weight=25 --reference-cpu-weight=100 --no-seccomp-egress
```

Run `docker run --rm spectre-demo --help` for the full flag list.

## Build your own image

The demo image is a template for running your own services under the supervisor.
Base your image on the same pattern as `demo/Dockerfile`:

1. Build or copy the `spectre` binary into the image. It embeds the ingress and
   egress proxies, so no other components are needed.
2. Add your reference and candidate binaries, your comparison scripts
   (`--scripts-dir`), TypeScript schema (`--schema-dir`), and protobuf
   descriptors (`--descriptors-dir`).
3. Run the supervisor with your two commands.

The supervisor runs each service command through `/bin/sh -c` and exports two
environment variables to it:

- `$SPECTRE_LISTEN` — the address the service must listen on. Pass it through to
  your service's listen flag.
- `$SPECTRE_EGRESS` — the base URL the service must send its dependency calls to,
  so egress can intercept them.

Declare each dependency with `--destination=HOST=URL`, mapping the request host
your services use to the real upstream URL. Egress forwards only the reference's
call to the upstream and replays the recorded response to the candidate.

A minimal invocation:

```
spectre run \
  --listen=0.0.0.0:50050 \
  --reference='my-service --listen=$SPECTRE_LISTEN --upstream=$SPECTRE_EGRESS' \
  --candidate='my-service-v2 --listen=$SPECTRE_LISTEN --upstream=$SPECTRE_EGRESS' \
  --destination=api.internal=https://api.internal \
  --scripts-dir=/app/scripts \
  --schema-dir=/app/schema \
  --descriptors-dir=/app/descriptors
```

Run the resulting image with `--privileged` for the cgroup split and seccomp
sandbox. To run without them — for example in an environment that forbids
privileged containers — pass `--no-limits` and `--no-seccomp-egress`.
