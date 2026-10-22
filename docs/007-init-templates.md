# Init templates

**Status:** Accepted

## Problem

Setting a service up for SPECTRE means writing the same set of files every time: the migration config, an empty migration state, the entrypoint and launch scripts for both legs, the mirror's container build, the normaliser scripts, and the instructions agents follow to drive the migration. Today they are copied by hand from an earlier service and edited, so each service drifts from the last, and improvements to the shared files never reach services set up before them.

The files depend on the stack being migrated from and to, and on the deploy platform. Most of those stacks and platforms are private to the organisation running the migration, so the open-source binary cannot ship them.

## Goals

- `spectre init` writes the files a migration needs into a service checkout from a named template.
- A template declares the values it needs, such as the service name and deploy platform. A person is asked for them in a terminal; an agent passes them as flags and can list them beforehand.
- Running `spectre init` again on a set-up service brings its files up to date with the template, with the answers given last time, and removes files the template no longer produces.
- A template marks files that a person or agent is expected to edit, such as the migration config and state, so a re-run never overwrites or deletes them.
- The binary ships a generic template with placeholders for the stack-specific parts. A custom binary adds its own templates, which can build on another template.

## Non-goals

- Editing files the template does not own, such as the service's CI or deployment config. A template prints the steps for those instead.
- Generating per-service content, such as the migration state's nodes, schema declarations, or descriptor sets. Templates ship agent instructions for producing them.
- Merging a template's changes into an edited customisable file.
- Fetching templates from outside the binary.

## Decision

### Templates

A template is a directory of files rendered with Go's template language using the [scaffolder](https://github.com/block/scaffolder) library. File and directory names are templates too, so a name that renders empty is left out.

Each template has a manifest at its root, which is not rendered. It holds:

- A description.
- An optional parent template it builds on.
- Its variables, in the order they are asked for. Each has help text, a type of string, boolean, or list, an optional default, and optional choices or pattern that values must match. A default is itself a template, so it can derive from the target directory's name or from earlier variables.
- Which files are customisable, and which are executable, since compiled-in files carry no permissions.
- Symbolic links to create, since compiled-in files cannot hold them.
- Files from the parent template to leave out.
- Next steps to print after the files are written.

A child template is rendered on top of its parent. Its variables are added to the parent's, replacing any with the same name. Each file takes its customisable and executable flags from the template that produced it, so a child that replaces a parent's placeholder with a real file can manage it, even where the placeholder was customisable.

The binary ships a generic `base` template. Custom binaries register templates of their own as compiled-in file trees.

### Variables

Values come, highest precedence first, from flags, a values file, the answers recorded at the last run, and then a prompt when standard input is a terminal or the default when it is not. A variable with no value from any of those fails the run with each missing variable's help text, so an agent can retry with the values. `spectre templates` lists the templates, and describes one template's variables as text or JSON.

### Re-running

`spectre init` records, in a lock file beside the migration config, the template, the answers, and every file it wrote with whether each is customisable. A re-run without a template uses the recorded one.

Each run renders the whole template into a staging area first, then reconciles the checkout with it:

- A rendered file that is not customisable overwrites what is there, whether or not an earlier run wrote it.
- A customisable file is written only when it does not exist.
- A file the last run wrote that the template no longer renders is deleted, unless it was customisable. Directories left empty are removed.
- Files no run has written are never touched, so the service's own code is safe.

A dry run reports the same actions without writing anything. Every run can report its actions as JSON for agents.

## Consequences

- Shared files change in one place, and a service picks the changes up by re-running `spectre init` after bumping SPECTRE.
- Edits to a managed file are lost on the next run. Files meant to be edited must be marked customisable, and template authors decide which.
- Every template file is a Go template, so a literal `{{` in a script or instruction must be escaped.
- The public API gains one extension point, registering a template, in the style of ADR 004.
- scaffolder is experimental and reads only from disk, so templates are copied to a temporary directory before rendering.

## Open questions

None.

## References

- [ADR 004: Compile-time extensions](004-compile-time-extensions.md)
- [ADR 006: Migration plan](006-migration-plan.md)
- [scaffolder](https://github.com/block/scaffolder)
