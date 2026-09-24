#!/bin/sh
set -eu

image="$1"
expected_version="$2"

version="$(docker run --rm "$image" --version)"
test "$version" = "$expected_version"

source="$(docker image inspect "$image" --format '{{ index .Config.Labels "org.opencontainers.image.source" }}')"
test "$source" = "https://github.com/block/spectre"

docker run --rm --entrypoint /bin/sh "$image" -c \
  'test "$(id -u)" -ne 0 && test -s /etc/ssl/certs/ca-certificates.crt'
