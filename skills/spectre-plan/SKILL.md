---
name: spectre-plan
description: Write or update a `spectre/state.toml` file for a SPECTRE enabled migration of a service.
---

You need to be in a root directory of the service being migrated, and have `spectre` CLI tool available.

# Plan the migration

`spectre/state.toml` describes the modules of a service independently of its implementation, and drives the migration from one stack to the other.

1. Run `spectre state types` to learn the node types and the settings each takes.
2. Read the service's source code and list ALL its nodes and their dependencies.
3. Write each node as `[nodes.<kebab-id>]` with `status = "pending"`, its `depends_on`, and a table for each IO kind it performs.
4. Run `spectre state validate` and fix every error. Look into each warning about nodes no ingress node reaches: they are dead code or run outside requests.

Follow these rules:

- Every ingress and egress route of the service belongs to exactly one node,
  except health checks.
- A node depends on another when it needs the other to work correctly.
- Give a shared module its own node only when several other nodes use it and it
  is not purely technical. Code only one node uses is part of that node.
- A node's IO kinds are the IO its own code performs, not IO it reaches through
  its dependencies.

When updating an existing state, keep the IDs and statuses of existing nodes.
