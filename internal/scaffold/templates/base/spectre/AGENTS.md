This directory holds the SPECTRE migration of {{ .service_name }}, the service in
the parent directory. SPECTRE runs the existing service as the reference and the
new implementation as the candidate, mirrors traffic to both, and compares their
responses and dependency calls.

`spectre init` wrote these files and keeps them up to date. Do not edit a file it
manages: the next run overwrites it. `init.lock.toml` lists the managed files and
the customisable ones, which are yours to edit.

Use these skills:

- `spectre-plan` to write `state.toml`, the modules of the service and their dependencies.
- `spectre-scripts` to declare the routes and dependency calls SPECTRE compares, in `scripts/`.
- `spectre-migrate` to move the next module to the candidate.
