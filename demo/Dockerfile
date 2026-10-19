# syntax=docker/dockerfile:1

# Demo image: the SPECTRE supervisor running a sample reference and candidate
# behind both proxies, with the candidate's egress sandboxed and host resources
# split between the services. See demo/README.md.

FROM golang:1.27.2-alpine3.24 AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY internal/typescript ./internal/typescript
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=demo
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -buildid= -X github.com/block/spectre/internal.Version=${VERSION}" \
      -o /out/ \
      ./cmd/spectre ./cmd/spectre-sample ./cmd/spectre-sample-forecasts

FROM alpine:3.24.2

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates
COPY --from=build /out/* /usr/local/bin/

WORKDIR /spectre
COPY internal/sample/scripts ./scripts
COPY internal/sample/schema ./schema
COPY internal/sample/testdata ./testdata
COPY dist/descriptors ./descriptors
COPY demo/entrypoint.sh /usr/local/bin/spectre-demo

EXPOSE 50050
ENTRYPOINT ["spectre-demo"]
