# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.24 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd/spectre-ingress ./cmd/spectre-ingress
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -buildid= -X github.com/block/spectre/internal.Version=${VERSION}" \
      -o /out/spectre-ingress ./cmd/spectre-ingress

FROM alpine:3.24.2

LABEL org.opencontainers.image.source="https://github.com/block/spectre"

# Keep CA roots current within the pinned Alpine release.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates
COPY --from=build /out/spectre-ingress /usr/local/bin/spectre-ingress

# Run as Alpine's nobody user without requiring name resolution.
USER 65534:65534
EXPOSE 50050
ENTRYPOINT ["spectre-ingress"]
CMD ["serve", "--listen=0.0.0.0:50050"]
