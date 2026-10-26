#!/bin/sh
# Start the sample dependency, then run the supervisor with a sample reference
# and candidate. The candidate's egress is sandboxed and host resources are
# split between the two services.
set -eu

sockets=/spectre/sockets
mkdir -p "$sockets"

# The forecast provider is the services' dependency; the supervisor drains it
# into the supervisor cgroup when it takes over resource control.
spectre-sample-forecasts \
  --listen="unix:$sockets/forecasts.sock" \
  --weather=/spectre/testdata/weather.json &

# Mirrors report their quarantines to the SPECTRE server, so run one alongside.
spectre serve --listen=127.0.0.1:50070 &

service="spectre-sample --listen=\$SPECTRE_LISTEN --forecasts=\$SPECTRE_EGRESS --data=/spectre/testdata/users.json --weather=/spectre/testdata/weather.json"

exec spectre supervise \
  --listen=0.0.0.0:50050 \
  --server-url=http://127.0.0.1:50070 \
  --service=sample \
  --environment=demo \
  --revision=demo \
  --socket-dir="$sockets" \
  --reference="$service" \
  --candidate="$service" \
  --destination=forecasts.example="http+unix:$sockets/forecasts.sock" \
  --scripts-dir=/spectre/scripts \
  --schema-dir=/spectre/schema \
  --descriptors-dir=/spectre/descriptors \
  "$@"
