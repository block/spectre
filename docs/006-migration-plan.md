# Migration plan

**Status:** Proposed

## Problem

A migration moves a service one piece at a time, but nothing records what the pieces are, what each depends on, or how far each has got. Without that, neither a person nor an LLM can tell which module to migrate next, or which reference code is safe to remove.

The kinds of IO a module performs also decide how SPECTRE verifies it. Those kinds are defined by what the ingress and egress proxies support, but today the script loader is the only place that knows them.

## Goals

- A `spectre/plan.toml` file that lists a service's modules as nodes, their types, and the nodes they depend on.
- Node types are `library` or an IO kind written `<direction>[<protocol>]`, such as `ingress[http]` or `egress[http]`. A node can have several.
- The IO kinds are not hard-coded in the plan. They come from the same abstraction the proxies register their protocols with, so a protocol a custom binary adds becomes a valid node type too.
- Commands that validate the plan, list the nodes ready for their next step, and describe each valid node type well enough for an agent to write the plan.

## Non-goals

- Extracting modules from source code automatically. That stays with the person or LLM that writes the plan.
- Updating node status from live mirror traffic.
- Changing how the engine pairs or compares traffic.

## Decision

### IO kinds

An IO kind is a direction, ingress or egress, paired with a protocol, such as HTTP. It is written `<direction>[<protocol>]`, for example `ingress[http]`. The protocol names are the ones scripts already pass to `ingress.match` and `egress.match`, so a plan and its scripts describe the same traffic in the same words.

Directions, protocols, and kinds move out of the script loader into their own low-level package, which the script loader, the protocol engine, and the plan all import.

Each protocol server reports the kinds it supports, each with a description of the code that has that kind and how to recognise it. The HTTP server supports both `ingress[http]` and `egress[http]`. A future protocol that proxies only one direction, such as a queue consumer, reports only that one. The protocol server knows its traffic best, so it owns the description, and a custom protocol brings its own.

At startup, the binary collects the kinds of every protocol server it contains into one set. That set is the single source for:

- **Scripts**: an endpoint fails at load when its protocol is not supported in its direction. Today the engine checks only the protocol, so it cannot tell directions apart.
- **The script API declaration**: the protocol parameter of `ingress.match` and `egress.match` lists the protocols supported in that direction, instead of only HTTP.
- **The plan**: a node type is `library` or a supported kind.

When custom binaries can add protocols, which ADR 004 lists as a planned extension point, registering a protocol server also adds its kinds and their descriptions. The plan then accepts them with no other change.

### Plan file

`spectre/plan.toml` sits beside `spectre/migration.toml`. The migration config describes where the mirror runs and changes rarely. The plan changes at every migration step and is often written by an LLM, so it is a separate file.

Each node is a table under `nodes`, keyed by a kebab-case ID, so TOML itself rejects duplicate IDs.

```toml
[nodes.money]
types = ["library"]
reference_sources = ["src/main/java/com/example/money/**"]
candidate_sources = ["candidate/internal/money/**"]
status = "migrated"

[nodes.ledger-client]
types = ["egress[http]"]
reference_sources = ["src/main/java/com/example/ledger/**"]
candidate_sources = ["candidate/internal/ledger/**"]
depends_on = ["money"]
status = "migrated"

[nodes.invoices-api]
types = ["ingress[http]", "egress[http]"]
reference_sources = ["src/main/java/com/example/invoices/api/**"]
candidate_sources = ["candidate/internal/invoices/**"]
depends_on = ["money", "ledger-client"]
status = "done"

[nodes.reports-api]
types = ["ingress[http]"]
reference_sources = ["src/main/java/com/example/reports/**"]
depends_on = ["money"]
status = "pending"
```

| Field | Meaning |
| --- | --- |
| `types` | Required. `library`, or one or more IO kinds. |
| `reference_sources` | Globs of the reference implementation's files that make up the node. |
| `candidate_sources` | Globs of the candidate implementation's files that make up the node, added when the node is migrated. |
| `depends_on` | IDs of nodes whose code this node calls or imports. |
| `status` | `pending`, `migrated`, or `done`. Defaults to `pending`. |

Source globs are relative to the root of the repository that holds the plan. Recording both sides lets an agent find the reference code to migrate, the candidate code it became, and the reference code to remove once the node is done.

A node's types describe the IO its own code performs, not IO it reaches through its dependencies. `invoices-api` above calls a tax service directly, so it is `egress[http]` as well as `ingress[http]`. It reaches the ledger only through `ledger-client`, which carries that egress.

`library` means code that more than one other node uses and that performs no IO, so it cannot be combined with an IO kind. Code that only one node uses is part of that node.

A node's status says where its code lives:

| Status | Meaning |
| --- | --- |
| `pending` | Only in the reference. |
| `migrated` | In the candidate as well as the reference. |
| `done` | Removed from the reference, so only in the candidate. |

### Order

Migration runs in topological order, dependencies first, so libraries and egress clients usually go before the ingress nodes that use them.

Removal from the reference runs the other way, dependents first, so ingress nodes usually go before the libraries they use.

- A node is ready to migrate when it is `pending` and none of its dependencies are.
- A node is ready to remove when it is `migrated` and every node that depends on it is `done`.

`validate` also warns about nodes that no ingress node reaches. SPECTRE only sees code that ingress traffic reaches, so such a node is either dead code or runs from something SPECTRE does not mirror, such as a scheduled job.

### Commands

```
spectre plan types      # library, then each supported IO kind, with descriptions
spectre plan validate   # validate the plan; non-zero exit on any error
spectre plan next       # nodes ready to migrate, then nodes ready to remove
```

`next` sorts nodes by ID, so its output is stable, and can print JSON for agents. The plan's path is a flag that defaults to `spectre/plan.toml`.

`spectre plan types` is how module extraction discovers the vocabulary. An agent that writes the plan takes its output as input instead of a fixed list, so the prompt stays correct for custom binaries. The output is Markdown, and JSON on request:

```markdown
## library

Code shared by more than one other node that performs no IO, such as shared domain types, validation, calculations, and helpers. Code that only one node uses belongs to that node, not to a library. Shared code that does any IO is not a library either; it is a node with the IO kinds it performs.

## ingress[http]

Code that receives HTTP requests to the service, including gRPC and Connect: route registrations, controllers, handlers, and service implementations. The service's routes and RPC methods are the entry points SPECTRE compares, so every request path starts at one of these nodes.

## egress[http]

Code that sends HTTP requests to another service, including gRPC and Connect: API clients, generated stubs and the code that configures them, and code that builds requests or reads their responses. SPECTRE pairs these requests and replays the reference's responses to the candidate.
```

Each description says what the node type covers, how to recognise it in source code, and what SPECTRE does with it. `library` is described by the plan itself, since no protocol server owns it.

### Status

`spectre status` reports the plan each environment actually runs, not the one in the working tree. For each environment it already finds the commit the mirror runs. It then reads the plan file at that commit from the local git repository, fetching the commit from the repository's remote when it is missing, validates it, and prints a summary under the commit:

```
invoices
  production-us-west-2: spectre mirror deployed
    commit: 3f9c2a1
    image: ...
    plan: 4 done, 7 migrated, 12 pending (23 nodes); 3 ready to migrate, 2 ready to remove
    candidate: healthy
```

The plan path is relative to the repository root, because it is read from git rather than from disk.

The plan line reports why instead of a summary when:

- the commit is unknown, because the platform could not resolve the image;
- the commit cannot be fetched, with the fetch error;
- the commit has no plan file;
- the plan at that commit fails validation, with the first error.

None of these fail the report, like the other per-environment errors.

## Open questions

- Should `plan validate` take a checkout of the reference and fail when a source file matches no node, or more than one?
- Should a node also list the ingress routes and egress patterns it owns, so a later status between `migrated` and `done` could record that SPECTRE found them equivalent?
