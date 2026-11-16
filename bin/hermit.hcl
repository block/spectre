env = {
  // Match the image builds, so they can reuse the host's Go build cache.
  "CGO_ENABLED": "0",
  "PATH": "${HERMIT_ENV}/scripts:${PATH}",
}
