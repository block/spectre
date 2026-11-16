---
name: spectre-migrate
description: Migrate the next module of a service from the reference to the candidate. Use when asked to continue or advance the SPECTRE migration.
---

You need to be in a root directory of the service being migrated, and have `spectre` CLI tool available.

# Migrate the next node

- If there is no state.toml yet, Plan the migration with the `spectre-plan` skill.
- Run `spectre next --json` for the nodes of the next step, dependencies first.
  Migrate exactly those nodes, so the step stays small and verifiable.
- Implement the nodes in the candidate so they behave exactly like the reference,
  including errors and the dependency calls they make. Match behaviour, not code.
- Add or update the comparison scripts for their routes and dependency calls with
  the `spectre-schema` skill.
- Run `spectre done <node>...` with the IDs of the nodes you migrated. It marks
  them migrated in `spectre/state.toml` and checks the state is still valid.
- Commit the candidate change and the state change together, so the mirror only
  compares routes the candidate implements.

When the mirror reports a difference or quarantines the candidate, fix the candidate, not the reference.
Normalise a difference only when it does not matter to clients.

Use any environment specific `spectre-migration-<env>` skills if avialable.
