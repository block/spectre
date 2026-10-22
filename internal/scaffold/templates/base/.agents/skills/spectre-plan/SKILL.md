---
name: spectre-plan
description: Write or update spectre/state.toml, the SPECTRE migration state of {{ .service_name }}, listing its modules as nodes with their IO and dependencies. Use when planning a migration or when ingress quarantines the candidate on a route no node lists.
---

# Plan the migration

`spectre/state.toml` describes the modules of {{ .service_name }} independently of
its implementation, and drives the migration from one stack to the other.

1. Run `spectre state types` to learn the node types and the settings each takes.
2. Read the service's source code and list ALL its nodes and their dependencies.
3. Write each node as `[nodes.<kebab-id>]` with `status = "pending"`, its
   `depends_on`, and a table for each IO kind it performs.
4. Run `spectre state validate` and fix every error. Look into each warning about
   nodes no ingress node reaches: they are dead code or run outside requests.

Follow these rules:

- Every ingress and egress route of the service belongs to exactly one node,
  except health checks.
- A node depends on another when it needs the other to work correctly.
- Give a shared module its own node only when several other nodes use it and it
  is not purely technical. Code only one node uses is part of that node.
- A node's IO kinds are the IO its own code performs, not IO it reaches through
  its dependencies.

When updating an existing state, keep the IDs and statuses of existing nodes.
