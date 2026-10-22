# {{ .service_name }} SPECTRE mirror

This directory builds a SPECTRE mirror for {{ .service_name }}. One container runs
the `spectre` supervisor, which starts the ingress and egress proxies, the
existing service as the reference, and the new implementation as the candidate.
The reference answers clients; the candidate is compared off the response path
and runs sandboxed, so it never makes real writes.

`spectre init --template={{ .Template }}` wrote these files. Run `spectre init`
again to bring them up to date with the template; `init.lock.toml` records what
it manages.

## Layout

- `migration.toml`: where the mirror runs, read by `spectre status`.
- `state.toml`: the service's modules, what each depends on, and which are
  migrated. Ingress mirrors only the routes of migrated modules.
- `scripts/`: comparison scripts for both proxies. They type the payloads of each
  route and dependency call, and normalise differences that do not matter.
- `schema/`: TypeScript declarations of the payloads the scripts refer to.
- `descriptors/`: protobuf descriptor sets for the payloads.
- `bin/entrypoint`: the container entrypoint, which runs the supervisor.
- `bin/reference` and `bin/candidate`: start each leg.

## Wiring

The supervisor exports `$SPECTRE_LISTEN`, the address a leg serves on, and
`$SPECTRE_EGRESS`, where it sends dependency calls through the egress proxy. Egress
forwards the reference's calls, records the responses, and replays them to the
candidate when its calls match.
