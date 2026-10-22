---
name: spectre-migrate
description: Migrate the next module of {{ .service_name }} from the reference to the candidate, following spectre/state.toml. Use when asked to continue or advance the SPECTRE migration.
---

# Migrate the next node

1. Run `spectre state validate`. Plan the migration with the `spectre-plan` skill
   first if it fails or lists no nodes.
2. Pick a node that is `pending` and whose `depends_on` are all `migrated`.
   Prefer libraries and egress clients, which other nodes need first.
3. Implement the node in the candidate so it behaves exactly like the reference,
   including errors and the dependency calls it makes. Match behaviour, not code.
4. Add or update the comparison scripts for its routes and dependency calls with
   the `spectre-scripts` skill.
5. Set the node's `status` to `"migrated"` in `spectre/state.toml`, changing only
   that line, and run `spectre state validate`.
6. Commit the candidate change and the state change together, so the mirror only
   compares routes the candidate implements.

When the mirror reports a difference or quarantines the candidate, fix the
candidate, not the reference. Normalise a difference only when it does not matter
to clients.
