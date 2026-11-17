# syntax=docker/dockerfile:1

# Demo image: the spectre binary, the sample services and their comparison
# config, for the containers of demo/mirror-pod.yaml. See demo/README.md.
# Like the main image, it copies in binaries compiled on the host.

FROM alpine:3.24.3

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates
ARG TARGETOS
ARG TARGETARCH
# hadolint ignore=DL3022
COPY --from=binaries ${TARGETOS}-${TARGETARCH}/ /usr/local/bin/

WORKDIR /spectre
COPY internal/sample/scripts ./scripts
COPY internal/sample/schema ./schema
COPY internal/sample/testdata ./testdata
COPY dist/descriptors ./descriptors
