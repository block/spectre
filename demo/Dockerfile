# syntax=docker/dockerfile:1

# Demo image: the spectre binary, the sample services and their comparison
# config, for the containers of demo/mirror-pod.yaml. See demo/README.md.

# Empty unless the build passes a named build context of the same name. CI
# passes the Hermit and Go build caches it has already restored and warmed.
FROM scratch AS host-hermit-cache
FROM scratch AS host-go-build-cache

FROM alpine:3.24.3 AS build

# Hermit's bootstrap needs bash, curl and openssl, and it fetches package
# manifests with git.
# hadolint ignore=DL3018
RUN apk add --no-cache bash ca-certificates curl git openssl

WORKDIR /src
COPY bin ./bin
# Hermit's lock fails between BuildKit steps, whose processes can share a PID,
# so unpack Go once under BuildKit's lock before any concurrent build needs it.
RUN --mount=type=bind,from=host-hermit-cache,target=/host/hermit \
    --mount=type=cache,target=/root/.cache/hermit,sharing=locked \
    if [ -z "$(ls -A /host/hermit)" ]; then bin/go version; fi
# Dockerfile.dockerignore excludes everything the build does not need, so a new
# package reaches the build without a change here.
COPY . .

ARG VERSION=demo
# Hermit's Go matches the host's exactly, so a host build cache is reusable.
# Each cache falls back to a BuildKit cache mount when the host passes none.
RUN --mount=type=bind,from=host-hermit-cache,target=/host/hermit,rw \
    --mount=type=bind,from=host-go-build-cache,target=/host/go-build,rw \
    --mount=type=cache,target=/root/.cache/hermit \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    if [ -n "$(ls -A /host/hermit)" ]; then export HERMIT_STATE_DIR=/host/hermit; fi && \
    if [ -n "$(ls -A /host/go-build)" ]; then export GOCACHE=/host/go-build; fi && \
    CGO_ENABLED=0 bin/go build -trimpath \
      -ldflags="-s -w -buildid= -X github.com/block/spectre/internal.Version=${VERSION}" \
      -o /out/ \
      ./cmd/spectre ./cmd/spectre-sample ./cmd/spectre-sample-forecasts

FROM alpine:3.24.3

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates
COPY --from=build /out/* /usr/local/bin/

WORKDIR /spectre
COPY internal/sample/scripts ./scripts
COPY internal/sample/schema ./schema
COPY internal/sample/testdata ./testdata
COPY dist/descriptors ./descriptors
