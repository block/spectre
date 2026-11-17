# syntax=docker/dockerfile:1

# The image copies in a spectre binary compiled on the host, where it reuses
# the host's Go build cache. BUILD.bit passes the binaries as a build context
# with one directory per platform, so each platform gets its own build.
FROM alpine:3.24.3

LABEL org.opencontainers.image.source="https://github.com/block/spectre"

# Keep CA roots current within the pinned Alpine release.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates
ARG TARGETOS
ARG TARGETARCH
# hadolint ignore=DL3022
COPY --from=binaries ${TARGETOS}-${TARGETARCH}/spectre /usr/local/bin/

# Run as Alpine's nobody user without requiring name resolution.
USER 65534:65534
# The default ports of "spectre supervise" and "spectre serve".
EXPOSE 50050 50070
# Run any spectre command, such as "spectre supervise" for a mirror or
# "spectre serve" for the SPECTRE server.
ENTRYPOINT ["spectre"]
