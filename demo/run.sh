#!/bin/sh
# Build and run the SPECTRE supervisor demo in one command. Extra arguments are
# passed through to "spectre supervise", e.g. --candidate-cpu-weight=200.
set -eu

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

bit demo

echo "Starting the demo. Ingress is published on http://127.0.0.1:50050." >&2
echo "In another terminal try:" >&2
echo "  grpcurl -plaintext -d '{}' 127.0.0.1:50050 spectre.sample.v1.UserService/ListUsers" >&2
echo "  curl -s 127.0.0.1:50050/v2/forecast?location=london" >&2

# --privileged lets the supervisor write cgroups and install the candidate's
# seccomp filter inside the container.
exec docker run --rm -it --privileged -p 50050:50050 spectre-demo "$@"
