# syntax=docker/dockerfile:1

# Empty unless the build passes a named build context of the same name. CI
# passes the Hermit and Go build caches it has already restored and warmed.
FROM scratch AS host-hermit-cache
FROM scratch AS host-go-build-cache

FROM --platform=$BUILDPLATFORM alpine:3.24.3 AS build

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
# .dockerignore excludes everything the build does not need, so a new package
# reaches the build without a change here.
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# Hermit's Go matches the host's exactly, so a host build cache is reusable.
# Each cache falls back to a BuildKit cache mount when the host passes none.
RUN --mount=type=bind,from=host-hermit-cache,target=/host/hermit,rw \
    --mount=type=bind,from=host-go-build-cache,target=/host/go-build,rw \
    --mount=type=cache,target=/root/.cache/hermit \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    if [ -n "$(ls -A /host/hermit)" ]; then export HERMIT_STATE_DIR=/host/hermit; fi && \
    if [ -n "$(ls -A /host/go-build)" ]; then export GOCACHE=/host/go-build; fi && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH bin/go build -trimpath \
      -ldflags="-s -w -buildid= -X github.com/block/spectre/internal.Version=${VERSION}" \
      -o /out/ ./cmd/spectre

FROM alpine:3.24.3

LABEL org.opencontainers.image.source="https://github.com/block/spectre"

# Keep CA roots current within the pinned Alpine release.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates
COPY --from=build /out/spectre /usr/local/bin/

# Run as Alpine's nobody user without requiring name resolution.
USER 65534:65534
# The default ports of "spectre supervise" and "spectre serve".
EXPOSE 50050 50070
# Run any spectre command, such as "spectre supervise" for a mirror or
# "spectre serve" for the SPECTRE server.
ENTRYPOINT ["spectre"]
