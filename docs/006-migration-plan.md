# Migration plan

**Status:** Accepted

## Problem

A migration moves a service one piece at a time, but nothing records what the pieces are, what each depends on, or how far each has got. Without that, neither a person nor an LLM can tell which module to migrate next.

The kinds of IO a module performs also decide how SPECTRE verifies it. Those kinds are defined by what the ingress and egress proxies support, but today the script loader is the only place that knows them.

## Goals

- A `spectre/state.toml` file that lists a service's modules as nodes, their types, and the nodes they depend on.
- Node types are `library` or an IO kind written `<direction>[<protocol>]`, such as `ingress[http]` or `egress[http]`. A node can have several, each with its own settings.
- The IO kinds are not hard-coded in the plan. They come from the same abstraction the proxies register their protocols with, so a protocol a custom binary adds becomes a valid node type too.
- Ingress mirrors only the routes of migrated nodes, so a partly migrated candidate is compared only on what it implements, and quarantines the candidate on a route the state does not list.
- Commands that validate the plan, list the nodes ready for their next step, and describe each valid node type well enough for an agent to write the plan.

## Non-goals

- Extracting modules from source code automatically. That stays with the person or LLM that writes the plan.
- Updating node status from live mirror traffic.
- Recording which source files make up each node, in either the reference or the candidate.
- Changing how the engine pairs or compares traffic.
- Removing migrated code from the reference, or cutting traffic over to the candidate.

## Decision

### IO kinds

An IO kind is a direction, ingress or egress, paired with a protocol, such as HTTP. It is written `<direction>[<protocol>]`, for example `ingress[http]`. The protocol names are the ones scripts already pass to `ingress.match` and `egress.match`, so a plan and its scripts describe the same traffic in the same words.

Directions, protocols, and kinds move out of the script loader into their own low-level package, which the script loader, the protocol engine, and the plan all import.

Each protocol server reports the kinds it supports. For each kind it gives a description of the code that has that kind and how to recognise it, and the settings a node of that kind takes, such as the routes an HTTP ingress node handles. The HTTP server supports both `ingress[http]` and `egress[http]`. A future protocol that proxies only one direction, such as a queue consumer, reports only that one. The protocol server knows its traffic best, so it owns the description and the settings, and a custom protocol brings its own.

At startup, the binary collects the kinds of every protocol server it contains into one set. That set is the single source for:

- **Scripts**: an endpoint fails at load when its protocol is not supported in its direction. Today the engine checks only the protocol, so it cannot tell directions apart.
- **The script API declaration**: the protocol parameter of `ingress.match` and `egress.match` lists the protocols supported in that direction, instead of only HTTP.
- **The plan**: a node type is `library` or a supported kind.

When custom binaries can add protocols, which ADR 004 lists as a planned extension point, registering a protocol server also adds its kinds, their descriptions, and their settings. The plan then accepts them with no other change.

### Plan file

SPECTRE does not build the plan itself. An external agent writes it from the service's source code, using the node types and descriptions that `spectre state types` prints.

`spectre/state.toml` sits beside `spectre/migration.toml`. The migration config describes where the mirror runs and changes rarely. The plan changes at every migration step and is often written by an LLM, so it is a separate file.

Each node is a table under `nodes`, keyed by a kebab-case ID, so TOML itself rejects duplicate IDs. Each IO kind the node has is a table inside it, keyed by direction and then protocol, that holds the kind's settings. A kind with no settings is an empty table.

This example assumes a binary that also has a Kafka protocol server:

```toml
[nodes.money]
status = "migrated"

[nodes.ledger-client]
depends_on = ["money"]
status = "migrated"

[nodes.ledger-client.egress.http]

[nodes.invoices-api]
depends_on = ["money", "ledger-client"]
status = "migrated"

[nodes.invoices-api.ingress.http]
routes = ["GET /v1/invoices/{id}", "POST /example.invoices.v1.InvoiceService/CreateInvoice"]

[nodes.invoices-api.ingress.kafka]
topics = ["invoice-requested"]

[nodes.invoices-api.egress.http]

[nodes.reports-api]
depends_on = ["money"]
status = "pending"

[nodes.reports-api.ingress.http]
routes = ["GET /v1/reports/{id}"]
```

| Field | Meaning |
| --- | --- |
| `depends_on` | IDs of nodes whose code this node calls or imports. |
| `status` | `pending` or `migrated`. Defaults to `pending`. |
| `<direction>.<protocol>` | An IO kind the node has, holding the settings its protocol server defines. |

Loading the state rejects a kind the binary does not support and a setting its protocol server does not define, so mistakes in an agent's output fail at once. Each protocol server also checks its settings across nodes, for example that a route belongs to only one node.

A node's IO kinds describe the IO its own code performs, not IO it reaches through its dependencies. `invoices-api` above serves HTTP requests and Kafka messages, and calls a tax service directly, so it has all three kinds. It reaches the ledger only through `ledger-client`, which carries that egress.

A node with no IO kinds is a `library`. A library is code that more than one other node uses, so code that only one node uses is part of that node.

A node's status says where its code lives:

| Status | Meaning |
| --- | --- |
| `pending` | Only in the reference. |
| `migrated` | In the candidate as well as the reference. |

### Mirroring

The SPECTRE image carries the plan from the same commit as the candidate, and ingress reads it at startup. Each ingress protocol server reads only the settings of its own kind. For HTTP:

- A request whose route is in the `ingress.http.routes` of a `migrated` node is mirrored to the candidate and compared, as today.
- A request whose route belongs to a `pending` node goes to the reference only.
- A request whose route no node lists also goes to the reference only, but quarantines the candidate with a reason that names the route. The state is incomplete, so it must be updated before comparison resumes. Routes that ingress never compares, such as those scripts ignore and gRPC reflection, need no node.
- Without a plan file, ingress mirrors every request, as today.

Another protocol mirrors by its own settings in the same way, such as a Kafka server mirroring the topics of migrated nodes.

Because the plan and the candidate come from one commit, the mirrored routes always match the code the candidate has. Marking a node migrated and deploying is enough to start comparing it, and the candidate is never quarantined for routes it does not implement yet. `ingress.ignore` remains for routes that are migrated but cannot be compared.

Each route belongs to one node, so a divergence on a route also names the node to fix.

### Order

Migration runs in topological order, dependencies first, so libraries and egress clients usually go before the ingress nodes that use them. A node is ready to migrate when it is `pending` and none of its dependencies are.

`validate` also warns about nodes that no ingress node reaches. SPECTRE only sees code that ingress traffic reaches, so such a node is either dead code or runs from something SPECTRE does not mirror, such as a scheduled job.

### Commands

```
spectre state types      # library, then each supported IO kind, with descriptions and settings
spectre state validate   # validate the plan; non-zero exit on any error
spectre state next       # nodes ready to migrate
spectre state migrated <node>  # mark a node migrated
```

`next` sorts nodes by ID, so its output is stable, and can print JSON for agents. The plan's path is a flag that defaults to `spectre/state.toml`.

`migrated` is for the agent doing the migration, which calls it after it adds a node to the candidate. It only updates the node's status in the plan file:

- It changes only that node's status line, so comments, ordering, and formatting stay as written and the diff is one line.
- The node must be ready to migrate, as defined above. Otherwise the command fails and names the dependencies still pending, so the agent knows what to do first.
- Marking a node that is already migrated succeeds without changing the file, so an agent can safely retry.
- It does not commit. The agent commits the plan change with the code change it records.

`spectre state types` is how module extraction discovers the vocabulary. An agent that writes the plan takes its output as input instead of a fixed list, so the prompt stays correct for custom binaries. The output is Markdown, and JSON on request:

```markdown
## library

Code shared by more than one other node that performs no IO, such as shared domain types, validation, calculations, and helpers. Code that only one node uses belongs to that node, not to a library. Shared code that does any IO is not a library either; it is a node with the IO kinds it performs. A library node has no IO kind tables.

## ingress[http]

Code that receives HTTP requests to the service, including gRPC and Connect: route registrations, controllers, handlers, and service implementations. The service's routes and RPC methods are the entry points SPECTRE compares, so every request path starts at one of these nodes.

Write it as the table `[nodes.<id>.ingress.http]`, with these settings:

- `routes` (required): Each route the node handles, as `<METHOD> /<path>`. A gRPC or Connect method is `POST /<package>.<Service>/<Method>`. List every route the service serves: a request to a route no node lists quarantines the candidate.

## egress[http]

Code that sends HTTP requests to another service, including gRPC and Connect: API clients, generated stubs and the code that configures them, and code that builds requests or reads their responses. SPECTRE pairs these requests and replays the reference's responses to the candidate.

Write it as the empty table `[nodes.<id>.egress.http]`.
```

Each description says what the node type covers, how to recognise it in source code, and what SPECTRE does with it, followed by the table a node of that type uses and the settings it takes. `library` is described by the plan itself, since no protocol server owns it. It has no table, because a library is a node without IO kinds.

### Status

`spectre status` reports the plan each environment actually runs, not the one in the working tree. For each environment it already finds the commit the mirror runs. It then reads the plan file at that commit from the local git repository, fetching the commit from the repository's remote when it is missing, validates it, and prints a summary under the commit:

```
invoices
  production-us-west-2: spectre mirror deployed
    commit: 3f9c2a1
    image: ...
    state: 7 migrated, 12 pending (19 nodes); 3 ready to migrate
    candidate: healthy
```

The plan path is relative to the repository root, because it is read from git rather than from disk.

The state line reports why instead of a summary when:

- the commit is unknown, because the platform could not resolve the image;
- the commit cannot be fetched, with the fetch error;
- the commit has no plan file;
- the plan at that commit fails validation, with the first error.

None of these fail the report, like the other per-environment errors.

## Open questions

None.
