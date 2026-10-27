---
name: spectre-schema
description: Manage the SPECTRE comparison scripts, schema declarations, and descriptors for a service. Use when adding a route or dependency, or when a comparison fails on a difference that does not matter.
---

You need to be in a root directory of the service being migrated, and have `spectre` CLI tool available.

# Write the comparison scripts

Both proxies load every script in `spectre/scripts/`.
A script imports the `spectre` module; run `spectre gen module --output=spectre/spectre.d.ts` to get its declaration for an editor.
Keep it out of `spectre/schema/`, which cannot redeclare the module.

- Type each route's responses with `ingress.match<Response>("http", "<METHOD> /<path>")`.
- Skip a route that cannot be compared with `ingress.ignore("http", "<METHOD> /<path>")`.
  Ignored routes never quarantine the candidate. Ask the user whether to ignore
  the health check, such as `GET /_status`.
- Type each dependency call with `egress.match<Request>("http", "<METHOD> <host>/<path>")`,
  so egress can pair the candidate's call with the reference's. Keep one script
  per dependency.
- Normalise fields that differ legitimately, such as timestamps or ordering,
  rather than ignoring a whole route.

The types come from declarations in `spectre/schema/`:

- For protobuf payloads, put descriptor sets in `spectre/descriptors/` and run
  `spectre gen proto --descriptors-dir=spectre/descriptors --output=spectre/schema`.
- For JSON Schema, run `spectre gen json-schema --output=spectre/schema/<name>.d.ts`.
- Otherwise write the declaration by hand from the reference's responses.

Every route a migrated node lists in `spectre/state.toml` needs a match or an ignore.
