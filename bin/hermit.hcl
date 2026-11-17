env = {
  // Every Go build matches the release binaries' settings, so the image
  // binaries reuse the build cache that tests and other builds fill.
  "CGO_ENABLED": "0",
  "GOFLAGS": "-trimpath",
  "PATH": "${HERMIT_ENV}/scripts:${PATH}",
}
