# Protocol providers

**Status:** Implemented

## Problem

SPECTRE proxies HTTP, including gRPC and Connect, and MySQL. Each protocol has its own server package, but much of what a protocol needs lives in shared code:

- The script loader defines each protocol's script API.
- The schema declares protocols' payload types and reads RPC services.
- The engine runs HTTP response rules and parses HTTP status codes.
- The proxy wiring builds each protocol's servers by hand.
- The migration state has a field per protocol.

Adding a protocol means editing about a dozen shared packages, and a custom binary cannot add one at all.

## Goals

- Everything specific to one protocol lives in that protocol's package, behind one interface that each protocol implements.
- Adding a protocol means adding one package and registering it, with no edits elsewhere.
- Shared code works only with typed schema values and model types, never with a protocol's own types.
- Behaviour, flag names and the script API stay as they are, except where this ADR says otherwise.

## Non-goals

- New protocols, or new features in the existing ones.
- Changing how the engine pairs, compares or quarantines.
- A public extension point for protocols. That follows later, as described under Consequences.

## Decision

### One interface per protocol

Each protocol implements one provider interface. A provider supplies:

- **Its name**, used in scripts, the migration state, routes and quarantine reasons.
- **Its kinds**: the directions it proxies, with a description and the migration-state settings each takes.
- **Its script binding**: its part of the script module, its TypeScript declarations, its payload types and its reading of schema forms.
- **Its listeners**, named, with the addresses its config gives them.
- **Its proxies**, built from its config and the shared machinery, and served on its listeners.

Each provider owns its command-line config, with its own flag prefix if it has one. The proxy command parses every provider's config, and the provider receives it whole.

The binary's entry point registers the built-in providers explicitly. There is no global registry. Registering two providers with the same name fails startup. Only the entry point may import a provider's package, and a linter enforces this.

### Shared machinery

The proxy wiring owns everything protocols share, and hands each provider only the parts for its own protocol:

- one engine, which loads the scripts once
- candidate admission and quarantine for each direction, which every protocol shares
- the ingress call counter
- the listeners, which it opens before any provider starts
- readiness: each provider reports its own, and the health endpoints combine them
- the reporter
- the shutdown order: it marks everything unready and stops candidate work before it stops the providers, then finishes the engine's background work

A provider cannot reach another protocol's engine points.

### Scripts

The shared script module keeps only what applies to every protocol: field and message normalisers, script variables, exact-text bodies, and the ingress and egress objects. Each provider adds its own members to those objects, such as `ingress.http` or `egress.mysql`, with their own TypeScript declarations.

- A script that names a protocol the binary lacks, or a direction a protocol does not proxy, fails its type check.
- Each provider declares the payload types it creates in its own modules under the `spectre.` namespace. HTTP declares `spectre.connect` and MySQL declares `spectre.mysql`. A schema cannot declare those modules, and two providers cannot declare the same one.
- Each provider runs its own script callbacks, such as HTTP's rule selectors and normalisers and MySQL's statement replacements. The script loader provides only the runtime, value conversion and time limits.
- The engine still pairs egress endpoints and replacements, so providers declare those through the shared loader.

### Engine

The engine no longer knows any protocol:

- It no longer receives script declarations for protocols to match. Each provider reads its own declarations.
- A response tells the engine how to apply the script rule that selected it. The engine applies it at the point it applied HTTP rules before, so the order of comparison does not change.
- It no longer parses statuses. A status stays an opaque code.

### Schema

The schema reads TypeScript declarations, including interfaces of methods, but gives methods no meaning. HTTP gives them the meaning of RPC operations, looks them up by RPC path, and checks them against protobuf descriptors.

Protobuf descriptors and the generator that writes TypeScript from them stay shared, as a schema source.

### Migration state

The migration state stores each node's settings for each kind generically, and checks them against the kinds the binary's providers describe:

- It rejects an unsupported kind or an undefined setting, requires required settings, and keeps unique keys to one node.
- The keys of a unique setting are the routes the provider names its traffic by. That lets a quarantine name the node that owns its route, for any protocol.
- Registering MySQL adds the node type `egress[mysql]`, which takes no settings.

## Consequences

- The engine package is renamed, so its name is not confused with the providers' package.
- The built-in protocols move under one parent package. SPECTRE's own HTTP plumbing, used by the SPECTRE server, health endpoints and samples, moves out of the HTTP protocol into its own package.
- HTTP's and MySQL's script code, declarations and tests move from the script loader into their protocol packages.
- The egress health endpoint reports every egress provider, so it now includes MySQL.
- `spectre state types` lists `egress[mysql]`.
- The generator of TypeScript from protobuf no longer checks the service interfaces it writes against their own descriptors. HTTP still checks them when the proxy starts.
- A provider's ingress and egress start together, instead of egress first. Ingress stays unready until both backends accept connections, so no mirrored traffic arrives early.
- This supersedes the parts of [ADR 003](003-protocol-engine.md) on matchers, rule declarations and HTTP rules in the engine.
- A later ADR adds a public extension point for protocols. It will keep payloads as typed schema values and make the schema public. At first, custom protocols will declare only payload types, without script members of their own.

## Open questions

None.

## References

- [IO providers design](008-io-providers.md)
- [ADR 003: Protocol engine](003-protocol-engine.md)
- [ADR 004: Compile-time extensions](004-compile-time-extensions.md)
- [ADR 006: Migration plan](006-migration-plan.md)
