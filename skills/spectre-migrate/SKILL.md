---
name: spectre-migrate
description: Migrate the next module of a service from the reference to the candidate. Use when asked to continue or advance the SPECTRE migration.
---

You need to be in a root directory of the service being migrated, and have `spectre` CLI tool available.

# Migrate the next node

- If there is no state.toml yet, Plan the migration with the `spectre-plan` skill.
- Pick a node that is `pending` and whose `depends_on` are all `migrated`.
  Prefer libraries and egress clients, which other nodes need first.
- Implement the node in the candidate so it behaves exactly like the reference,
  including errors and the dependency calls it makes. Match behaviour, not code.
- Add or update the comparison scripts for its routes and dependency calls with
  the `spectre-schema` skill.
- Run `spectre done <node>` with the ID of the node you migrated. It marks it
  migrated in `spectre/state.toml` and checks the state is still valid.
- Commit the candidate change and the state change together, so the mirror only
  compares routes the candidate implements.

When the mirror reports a difference or quarantines the candidate, fix the candidate, not the reference.
Normalise a difference only when it does not matter to clients.

Use any environment specific `spectre-migration-<env>` skills if avialable.
