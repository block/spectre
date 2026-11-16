# syntax=docker/dockerfile:1

# Demo image: the spectre binary, the sample services and their comparison
# config, for the containers of demo/mirror-pod.yaml. See demo/README.md.

FROM golang:1.27.3-alpine3.24 AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY internal/typescript ./internal/typescript
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
# Dockerfile.dockerignore excludes everything the build does not need, so a new
# package reaches the build without a change here.
COPY . .

ARG VERSION=demo
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
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
