# IO providers

**Status:** Implemented, and recorded as [ADR 009](009-protocol-providers.md), which supersedes the matcher, rule-declaration and `Content.Rule` parts of [ADR 003](003-protocol-engine.md).

## Problem

SPECTRE proxies two protocols, HTTP (with gRPC and Connect) in `internal/httpserver` and MySQL in `internal/mysqlserver`. Much of what each protocol needs lives outside those packages:

- The script loader defines the protocols' script API.
- The schema package declares their payload types and reads RPC services.
- The engine runs HTTP response rules.
- The mirror wires each protocol by hand.
- The migration state has a struct field per protocol.

Adding a protocol today means editing about a dozen shared packages. Custom binaries cannot add one at all. This design puts everything specific to one protocol behind one interface, implemented once per protocol. Adding a protocol then means adding one package and registering it in `Main`. A later release lets custom binaries register one with `spectre.WithProtocol`.

## Goals

- Shared code sees only the provider interface. It works with `schema.Value`, `schema.Type` and `internal/model` types, never a protocol's own types.
- Every method maps to a responsibility a protocol has today.
- Each protocol owns its config, script API, script declarations, payload types, schema forms and migration-state settings.
- Shape the internal interface so that a later public contract can stay small.
- Behaviour, flag names and the script API stay as they are, unless this document says otherwise.

## Non-goals

- New protocols, or new features in the existing ones.
- Changing how the engine pairs, compares or quarantines.
- Runtime plugins.
- The public extension point itself, which follows in a later release.

## Decisions from review

| Question | Decision |
| --- | --- |
| Public extension point | Yes, in a later release, once the internal contracts have settled. Custom binaries will add protocols with `spectre.WithProtocol`. |
| Engine package name | `internal/protocol` becomes `internal/engine`, so it is not confused with `internal/protocols`. |
| MySQL node type | Registering MySQL adds `egress[mysql]`. |
| Descriptors and `gen proto` | They stay a shared protobuf schema source, outside the providers. |
| `schema.Operation` | RPC operations move out of the schema and behind the HTTP binding. |
| Package names | `internal/protocols` holds the interface. The protocols move to `internal/protocols/http` and `internal/protocols/mysql`. SPECTRE's own HTTP plumbing moves to `internal/httpservice`. |
| Egress health | `--egress-health-listen` reports every egress provider, including MySQL. |
| ADR | Record the accepted design as an ADR that supersedes the affected parts of ADR 003. |

## Where protocols leak today

Every item below is outside the two protocol packages and knows about a specific protocol.

### Listening, forwarding and serving

- `internal/proxies/mirror.go`:
  - `Mirror` holds `*httpserver.Ingress` and `*httpserver.Egress` fields.
  - `startEgress`, `startMySQL` and `startIngress` build each server by hand.
  - `Listeners` has five fixed HTTP listeners and two optional MySQL ones.
  - `exits` has a capacity of 4, one per hard-coded server.
- MySQL borrows HTTP egress's engine and candidates through `Egress.Engine()` and `Egress.Candidates()`. It therefore has to start after HTTP egress.
- `startHealth` aggregates only the HTTP servers' health. MySQL reports no readiness.
- The SPECTRE server, the health listener and the sample services import `httpserver/middleware` and `httpserver.Serve`. That is SPECTRE's own HTTP plumbing, not the mirrored protocol, but it lives in the HTTP protocol package.

### Engine ownership

- Each HTTP server builds its own `protocol.Engine`, its own `proxy.Candidates` and its own route matcher. The result is two engines that each load the scripts.
- HTTP ingress wraps its candidates in a `nodeQuarantiner` that adds the migration node, and it filters on `Protocol == ProtocolHTTP`.
- HTTP ingress owns the `CallCounter`, and the reporter reads `m.ingress.Calls`.

### Matching and decoding

- `protocol.DeclarationRule` carries a `javascript.Rule`, which is an HTTP rule.
- The engine passes every script declaration to a `protocol.Matcher` keyed by protocol. Only HTTP registers one.
- `Engine.SelectRule` takes `javascript.Request` and `javascript.Response`, which hold an HTTP method, path, headers and status.

### Normalising and comparing

- `protocol.Content.Rule` is an HTTP rule. `normaliseResponse` in `protocol/ingress.go` parses `Content.Status` as an HTTP status code and fails with "needs an HTTP status".
- `comparison.Scripts` has HTTP methods: `SelectRule`, `selectsStatus`, `selectsHeaders`, `NormaliseResponse` and `normalisedResponse`, which checks a status in the range 100 to 599.
- `comparison.Scripts` also has MySQL methods: `StatementReplacements`, `CombineStatements`, `SplitStatement` and `statementEvaluator`.

### Script bindings

All of these are in `internal/comparison/javascript`:

- `newCallbackRegistry` in `module.go` hard-wires `ingress.http`, `egress.http` and `egress.mysql.statement(s)`, along with their builders, validators and pattern syntax (`hasForm`).
- `rule.go` is entirely HTTP: rules, status, header and request selectors, `Request` and `Response`.
- `statements.go` and half of `evaluator.go` are MySQL: `StatementReplacement`, `Row`, `Claim`, `Offer`, and the SQL text coercion.
- `Endpoint`, `Rule` and `Replacement` hard-code `model.ProtocolHTTP`.
- `Program` and `NewEvaluator` list every protocol's registrations by name.
- `compile.go` has a MySQL carve-out (`mysqlOwner`) and an HTTP one (`IngressHTTPRule.match` may take `Text` or `unknown`).

### Script declarations, payload types and schema forms

- `comparison/javascript/spectre.d.ts` declares the HTTP and MySQL script APIs next to the shared ones.
- `schema/builtin.d.ts` declares the `spectre.connect` and `spectre.mysql` modules.
- `schema/parse.go` exports the constants `ConnectError` and `MySQLPairing`.
- `schema` reads an interface of methods as an RPC service. `schema.Operation` is "one unary RPC method", named `Service.Method`, and the schema enforces the form `method(request: Request): Response`.
- Only HTTP uses operations. The users are `httpserver/message/decoder.go` (`conventionalMethod`) and the method checks in `descriptors/agreement.go`.

### Config

- `proxies.ProxyConfig` holds HTTP flags: the listen addresses and the reference and candidate URLs.
- `proxies.Config` holds `Reflection`, `Destinations`, `Descriptors` and `CandidateStartupTimeout`, which are all HTTP. It embeds `mysqlserver.Config` with the prefix `mysql-`, and calls its `Validate` by name.
- `Mirror.ingressConfig` and `Mirror.egressConfig` copy these fields into `httpserver` configs one by one.

### Migration state

- `state.Ingress` and `state.Egress` have an `HTTP` field each, and `Node.kinds` lists HTTP kinds by hand.
- `state.NodeTypes` is a literal list that includes `ingress[http]` and `egress[http]`.
- `checkNode` and `checkRoutes` check HTTP routes by name.
- MySQL has no node type.

### Model, persistence and reporting

- `model.ProtocolHTTP` is defined in `internal/model`, while `mysqlserver.Protocol` is defined in its own package.
- Persistence, the wire protocol and CLI status output already treat the protocol as an open string. They need no change, apart from tests that use `model.ProtocolHTTP` as a fixture.

### Tests

- The integration harness has HTTP listener fields and MySQL fields in `Backend` and `setup`, plus `it.RemoveMySQLServer` in `TestMain`.
- `comparison/javascript` and `comparison` tests pin the HTTP and MySQL script APIs.

### Not protocol leaks

These stay as they are:

- `internal/contain` redirects TCP ports. Only its help text mentions MySQL.
- `internal/proxy` (`Candidates`, `Budget`, `CallCounter`) is already shared by both protocols.
- `internal/descriptors` and `gen proto` are a protobuf schema source, which ADR 004 plans as its own extension point. Only their RPC method checks move, as described under schema forms.

## Design

### Shape

There are three parts:

- **`protocols.Provider`** is the one interface each protocol implements. A **`protocols.Registry`** holds a binary's providers, because together they define what SPECTRE supports.
- **`javascript.Binding`** is how a provider contributes its part of the script module and gives meaning to its schema forms. It is built with `javascript.NewBinding`.
- **`state.Kind`** is how a provider describes each direction it proxies and the migration-state settings it takes. The registry rejects a kind whose protocol is not its provider's.

The engine package `internal/protocol` is renamed `internal/engine`. The rest of this document uses the new name.

The mirror owns everything shared:

- the engine
- candidate admission for each direction
- the call counter
- the listeners
- health
- the reporter
- the shutdown order

A provider receives those through a `Runtime`, and builds and serves only its own proxies.

The new package `internal/protocols` owns the interface. It is the lowest package that can name all of its parts. It imports these packages, and none of them import it:

- `engine`
- `comparison`
- `comparison/javascript`
- `schema`
- `state`
- `proxy`
- `model`
- `httpservice/health`

The built-in protocols live in its subpackages `internal/protocols/http` and `internal/protocols/mysql`. The root package, `internal/proxies` and `internal/cli` import `internal/protocols`. Only the root package imports the subpackages.

### The provider interface

```go
package protocols

// Config is a provider's Kong-tagged configuration.
type Config interface{ Validate() error }

// Provider is everything SPECTRE knows about one protocol, configured by C.
// Register parses C's flags with Kong and passes the result to Listens and New.
type Provider[C Config] interface {
	// Protocol names the protocol in scripts, the migration state and quarantine reasons.
	Protocol() model.Protocol
	// Kinds describes each direction the protocol proxies and its migration-state settings.
	Kinds() []state.Kind
	// Scripts is the protocol's part of the spectre script module.
	Scripts() javascript.Binding
	// Listens names the listeners the server serves, with their addresses from config.
	Listens(config C) []Listen
	// New builds the protocol's proxies. An error fails startup before anything serves.
	New(ctx context.Context, config C, runtime Runtime) (Server, error)
}

// Listen is one listener a provider serves. The mirror opens them all before
// any provider starts, so a bad address fails startup.
type Listen struct {
	// Name is unique within the provider, such as "ingress" or "reference".
	Name    string
	Address string
}

// Server serves one provider's listeners.
type Server interface {
	// Serve blocks until ctx is done and the provider's in-flight work has ended.
	// The mirror stops candidate work before it cancels ctx.
	Serve(ctx context.Context, listeners map[string]net.Listener) error
}

// Runtime is what the mirror shares with a provider. It holds only the
// provider's own protocol, so a provider cannot reach another's traffic.
type Runtime struct {
	Schema  *schema.Schema
	Scripts *comparison.Scripts
	// Ingress and Egress are present for each direction that the provider's Kinds name.
	Ingress Option[Side]
	Egress  Option[Side]
}

// Side is one direction's shared machinery: the engine's points for this
// protocol, the candidate admission every protocol shares, and readiness.
type Side struct {
	Reference  engine.Acceptor
	Candidate  engine.Acceptor
	Candidates *proxy.Candidates
	// Health is this provider's own readiness. The mirror combines all providers' handlers.
	Health *health.Handler
	// Nodes are the migration-state nodes with this kind, when the mirror has a state.
	Nodes Option[[]state.KindNode]
	// SourceResponse is the type that replacements read an endpoint's responses as.
	// It is None for every ingress route.
	SourceResponse func(route model.Route) Option[schema.TypeRef]
}
```

`Runtime` gives the provider narrow views instead of the whole engine and migration state. That keeps a provider to its own protocol. It also keeps a later public contract smaller.

Each method maps to a responsibility found above:

| Method | Responsibility |
| --- | --- |
| `Protocol` | The name used in scripts, state, routes and quarantine reasons. It replaces `model.ProtocolHTTP` and `mysqlserver.Protocol`. |
| `Kinds` | Migration-state node types and settings. It also says which directions get engine points and script members. |
| `Scripts` | The script API, its declarations, payload types and schema forms, and the protocol's script evaluation. |
| `Config.Validate` | Checking the parsed config before anything is built from it. |
| `Listens` | The listener addresses, so the mirror can open every listener first and tests can pass their own by name. |
| `New` | Building the protocol's proxies, decoders, budgets and matchers from config and shared machinery. |
| `Server.Serve` | Listening, forwarding, recording, replay and decoding. |

Each package owns its Kong config, which implements `Config`, and passes it to the provider's methods. As `CLAUDE.md` requires, `Listens` and `New` read the whole parsed config.

### Registry

```go
// Registry is the set of protocols a binary supports.
func NewRegistry() *Registry
func Register[C Config](registry *Registry, provider Provider[C], config C) error
func (r *Registry) Protocols() []model.Protocol
func (r *Registry) Listens(protocol model.Protocol) []Listen
func (r *Registry) New(ctx context.Context, protocol model.Protocol, runtime Runtime) (Server, error)
func (r *Registry) Kinds() state.Kinds
func (r *Registry) Bindings() []javascript.Binding
func (r *Registry) Plugins() kong.Plugins
func (r *Registry) Validate() error
```

`Register` is a function because Go methods cannot take type parameters. It pairs the provider with its config in an unexported type that holds the config in a field tagged `embed:""`. That is the Kong-plugin pattern ADR 004 planned for descriptor sources, and it hides the config's type, so the registry can hold providers of different configs. `Plugins` hands those to the `proxy` command, so only that command parses provider config. Each package has one constructor, `NewProvider()`, and `Register` takes the zero config when Kong applies the defaults.

`spectre.Main` builds the registry in `spectre.go`. That is bootstrap code, so it is allowed there, and there is no global registry:

```go
registry := protocols.NewRegistry()
err := errors.Join(
	protocols.Register(registry, http.NewProvider(), http.Config{}),
	protocols.Register(registry, mysql.NewProvider(), mysql.Config{}),
)
```

The rules for the registry:

- `NewProvider` creates the provider's script binding and keeps its handle in the provider value, so no package state is needed.
- `Register` fails when a protocol is registered twice, or two providers define the same kind.
- `Kinds` returns a `state.Kinds`, which owns the supported kinds. Its methods are `NodeTypes`, `Load`, `Parse` and `MarkMigrated`, so callers pass no maps.
- `Protocols`, `Listens` and `New` are how the mirror reaches a provider, by protocol and in registration order. `New` names the protocol when the provider fails.
- `Validate` checks every provider's parsed config, naming the protocol of the first that fails. The mirror calls it before it builds anything.
- `Main` binds the registry, its kinds, their node types and its bindings. Each command takes only what it reads:

| Command | What it uses |
| --- | --- |
| `proxy` | `Plugins` for its flags, then the registry itself. |
| `state types` | `Kinds().NodeTypes()`. |
| `state validate`, `next`, `done` | `Kinds`. |
| `gen module` | `Bindings`, which return `Scripts().Members()` and `Scripts().Payloads()`. |

A new `architecture` linter rule allows only the root package, and tests, to import a subpackage of `internal/protocols`.

### Script bindings

The `spectre` module keeps its shared members: `field`, `message`, `variable`, `Text`, `FieldPath` and the `ingress` and `egress` objects. A protocol adds its members to those objects.

**Declarations.** The shared `spectre.d.ts` declares `ingress` and `egress` through empty interfaces. TypeScript merges ambient module declarations and the interfaces inside them, so each protocol adds its members in its own file:

```ts
// shared spectre.d.ts
declare module "spectre" {
  export interface Ingress {}
  export interface Egress {}
  export const ingress: Ingress;
  export const egress: Egress;
}

// the HTTP binding's members
declare module "spectre" {
  interface Ingress { http(select: string | ((request: HTTPRequest) => boolean)): IngressHTTPRule; }
  interface Egress { http(pattern: string): EgressHTTPRule; }
  // HTTPRequest, IngressHTTPRule, Response<T>, ... as today
}
```

A script using a protocol the binary lacks, or a direction the protocol does not proxy, fails its type check. ADR 006 asked for this direction check.

**Payload modules.** A binding also declares the payload modules the protocol creates, which every schema includes. HTTP declares `spectre.connect` and MySQL declares `spectre.mysql`.

- `schema.FromProgram` takes these sources instead of the embedded `builtin.d.ts`.
- It reserves the modules the bindings declare, which must be named under `spectre.`. It does not reserve the whole namespace, because SPECTRE's own sample protobuf package is `spectre.sample.v1`.
- Two bindings that declare the same module fail startup.
- The constants `ConnectError` and `MySQLPairing` move to the protocol packages.

**Runtime members.** `javascript` keeps the module record, the evaluator, the JSON conversions and timeouts. A binding defines its members in each runtime through a `Scope`:

```go
package javascript

// Binding is one protocol's part of the spectre script module. Build it with NewBinding.
type Binding struct{ /* unexported */ }

func (b Binding) Protocol() model.Protocol
// Members is TypeScript adding the protocol's members to the spectre module's
// Ingress and Egress interfaces.
func (b Binding) Members() string
// Payloads is TypeScript declaring the spectre.<name> modules of the payloads
// the protocol creates.
func (b Binding) Payloads() string

// TypeArguments says how compile reads a generic method's type arguments.
type TypeArguments int

const (
	// TypeArgumentsSchema name schema types, which compile passes at run time. It is the default.
	TypeArgumentsSchema TypeArguments = iota
	// TypeArgumentsBody are TypeArgumentsSchema, except that the first may also be Text or unknown.
	TypeArgumentsBody
	// TypeArgumentsCheckOnly only check scripts, and are not passed at run time.
	TypeArgumentsCheckOnly
)

// Members are a binding's members in one runtime. D is what scripts declared
// through them, which must be equal in every runtime; I holds their callbacks.
type Members[D, I any] struct {
	Ingress  Option[sobek.Value]
	Egress   Option[sobek.Value]
	Declared func() D
	Instance I
}

// NewBinding binds a protocol. typeArguments is keyed by owner and method, such
// as "IngressHTTPRule.match", and lists only methods that differ from the default.
func NewBinding[D, I any](
	protocol model.Protocol,
	members, payloads string,
	typeArguments map[string]TypeArguments,
	define func(scope *Scope) (Members[D, I], error),
) (Binding, Handle[D, I])

// Handle reads a binding's declarations and per-runtime instances. Only the
// protocol that built the binding holds it.
type Handle[D, I any] struct{ /* unexported */ }

func (h Handle[D, I]) Declared(scripts *comparison.Scripts) D
func (h Handle[D, I]) Instance(evaluator *Evaluator) I

// Scope is what a binding builds its members with in one runtime.
type Scope struct{ /* unexported */ }

func (s *Scope) Runtime() *sobek.Runtime
func (s *Scope) Schema() *schema.Schema
// DeclareEndpoint and DeclareReplacement record what the engine itself pairs by.
func (s *Scope) DeclareEndpoint(endpoint Endpoint)
func (s *Scope) DeclareReplacement(replacement Replacement, request, response sobek.Callable)
// Call runs a script callback under the evaluator's timeout, converting values as JSON.
func (s *Scope) Call(callback sobek.Callable, arguments ...schema.Value) (Option[schema.Value], error)
```

Some notes on this API:

- `Handle.Declared` is how a protocol reads its own registrations at startup. HTTP reads its rules there instead of receiving them through the engine.
- `Handle.Instance` is how a protocol calls its own callbacks inside an evaluator it got from `comparison.Scripts`. An example is MySQL's `using` functions.
- `Endpoint` and `Replacement` stay in `javascript` because the engine pairs by them. The binding sets their protocol instead of `javascript` hard-coding HTTP.
- `NewEvaluator` keeps checking that re-evaluation reproduces the registrations. It now does so for each binding's `D`.
- `comparison.Scripts` loses its protocol methods. It gains `Evaluate(ctx, func(*javascript.Evaluator) error) error`, with the timeout and stopwatch that `statementEvaluator` has today.
- `gen module` writes the shared declarations followed by each binding's `Members` and `Payloads`.

### Schema forms: operations move to HTTP

Only `schema` has the TypeScript checker, so it keeps reading declarations. It stops giving interfaces of methods an RPC meaning:

```go
package schema

// Method is one method declared on a schema interface, with the object type of
// its single parameter and of its result. Protocols decide what methods mean.
type Method struct {
	Interface TypeRef
	Name      string
	Parameter TypeRef
	Result    TypeRef
}

// Methods returns every declared method. It replaces Operation and Operations.
func (s *Schema) Methods() []Method
```

The schema still checks the TypeScript form, a single object parameter and an object result, because it reports the source location. Everything that makes a method an RPC moves into the HTTP binding's package:

- the `Operation` type
- naming an operation `Service.Method`
- the lookup by RPC path in `conventionalMethod`
- the protobuf method checks from `descriptors/agreement.go`, namely that a method's types match its descriptor and that it is not streaming

`descriptors.CheckAgreement` keeps its message checks. As a result, `gen proto` no longer checks the service interfaces it generates against their own descriptors. HTTP still checks them against the descriptors when the proxy starts.

### Engine changes

The engine becomes protocol-free and is built once, by the mirror:

```go
// Normaliser applies the script rule that selected a response, on the
// candidate's time. A None Status is then not compared.
type Normaliser func(ctx context.Context) (status Option[string], message Option[schema.Value], err error)

type Content struct {
	Status    Option[string]
	Message   Option[schema.Value]
	Failure   Option[error]
	Ignored   Option[string]
	Normalise Option[Normaliser] // replaces Rule Option[javascript.Rule]
	Hold      Option[Raw]
}

// NewEngine takes a quarantiner per direction, because each direction has its own quarantine.
func NewEngine(ctx context.Context, config Config, bindings []javascript.Binding,
	ingress, egress Quarantiner, calls CallCounter) (*Engine, error)
```

These parts are removed:

- `Matcher`
- `Declaration`, `DeclarationRequest` and `DeclarationRule`
- `Engine.SelectRule`
- the HTTP status parsing in `normaliseResponse`

The engine calls `Content.Normalise` at the same point it calls the rule normaliser today, so the order of comparison does not change. A script can no longer name an unregistered protocol, because the type check rejects it, so `declare` has nothing left to check.

### Migration state

The migration state stores settings generically. Each provider describes its settings:

```go
package state

// Kind is one direction a provider proxies, as the migration state describes it.
type Kind struct {
	Direction   model.Direction
	Protocol    model.Protocol
	Description string
	// Settings are what a node's table for the kind holds. A Unique setting's keys
	// are the routes the provider names traffic by.
	Settings []Setting
}

// Settings are one node's settings for one kind: each keyed setting's entries.
// Each entry is an empty table, because no kind defines entry settings yet.
type Settings map[string]map[string]struct{}

// KindNode is one node with a kind, as the provider of that kind reads it.
type KindNode struct {
	ID       string
	Migrated bool
	Settings Settings
}

// Node.Ingress and Node.Egress become map[model.Protocol]Settings.
// Kinds are the kinds of IO a binary supports.
func (k Kinds) Add(kind Kind) error
// Kind returns the supported kind of protocol's traffic in direction.
func (k Kinds) Kind(direction model.Direction, protocol model.Protocol) (kind Kind, supported bool)
func (k Kinds) Load(path string) (State, error)
func (k Kinds) NodeTypes() []NodeType
// Nodes returns the nodes that have kind.
func (s State) Nodes(kind Kind) []KindNode
// Owner is the node that lists traffic's route under a Unique setting of its kind.
func (s State) Owner(traffic model.Traffic) (node string, ok bool)
```

The shared checks replace the HTTP ones:

- `Load` rejects a kind the binary does not support, and a setting the kind does not define.
- It enforces `Required` settings.
- It enforces `Unique` keys across nodes.

The mirror wraps ingress candidates in a shared node quarantiner that calls `Owner`, so the HTTP filter goes away. Its invariant is that a provider names a route it mirrors by the state key that listed it, as HTTP ingress does today.

### The mirror

`internal/proxies` builds everything from the registry:

1. It loads the migration state with the registry's kinds.
2. It builds one `proxy.Candidates` per direction, one `CallCounter` and one engine, using every binding.
3. For each provider it creates the `Side`s its kinds name, then opens the listeners from `Listens`.
4. It calls each provider's `New`, then runs every `Serve`. It serves `--health-listen` from every handler and `--egress-health-listen` from every egress handler, and it starts the reporter.
5. On shutdown it does these steps in order:
   - It marks every handler unready.
   - It calls `engine.Close()` and `Candidates.Close()`.
   - It cancels the providers and waits for their `Serve` calls to return.
   - It calls `Candidates.Shutdown` and `engine.Shutdown`.

This is the same order HTTP egress's `stop` and `drain` use today.

The shared mirror config keeps these flags:

- scripts, schema, variables and the state file
- `MatchWindow`
- the candidate admission limits (`proxy.CandidatesConfig`, moved out of `httpserver.Config`)
- the two health listeners
- the reporter

The tests and `StartListeners` pass listeners as `map[model.Protocol]map[string]net.Listener`.

### Later: the public extension point

A later release lets custom binaries add protocols with `spectre.WithProtocol`. It will get its own design. This design only keeps that contract small.

The public contract should be designed for custom protocols. It should not mirror every internal type the built-in protocols use, because many of those exist only for what HTTP and MySQL do. Payloads stay `schema.Value`, which is what the schema is for, and the schema becomes public with the contract. Two more choices keep the contract small:

- **No custom script members at first.** A custom protocol declares only its payload modules. The shared `field` and `message` normalisers apply to those types. Script members such as `egress.mysql`, and with them `NewBinding`, `Scope`, `Handle`, rule normalisers, replacements and sobek, stay internal until a custom protocol needs them.
- **Quarantine through the side.** `Side` gains admit, finish and quarantine methods that take a quarantine kind, a route and a detail. Then `Candidates`, `QuarantineReason` and `Traffic` need no mirrors.

Besides the schema, the contract then needs these types:

- the provider: `Provider`, `ProtocolConfig`, `Kind`, `Setting`, `Listen`, `Server`, `Runtime`, `Side` and `Direction`
- the engine points: `Acceptor`, `RequestID`, `Exchange`, `Decision` with `DecisionForward` and `DecisionIntercept`, `Response`, `Content` and `Raw`
- the rest: `QuarantineKind` and `KindNode`

`Server`, `Raw` and readiness use only standard-library types, so they pass to internal code unchanged. `Provider` and `Acceptor` need adapters in the root package. The built-in protocols keep implementing the internal interface, because the root package imports them.

## Mapping the existing protocols

### HTTP (`internal/protocols/http`, from `internal/httpserver`)

| Part | Comes from |
| --- | --- |
| `Protocol()` = `"http"` | `model.ProtocolHTTP` |
| `Kinds()`: `ingress[http]` with `routes`, and `egress[http]` | `state.NodeTypes` |
| `Scripts()` | A new `script` subpackage. It holds `ingress.http` and `egress.http` from `module.go`, all of `rule.go`, the HTTP evaluator methods, `Scripts.SelectRule` and `NormaliseResponse`, the HTTP half of `spectre.d.ts`, and `spectre.connect`. It uses `{"IngressHTTPRule.match": TypeArgumentsBody}`. Egress endpoints and replacements go through `Scope.DeclareEndpoint` and `DeclareReplacement`. |
| Operations | `schema.Operation`, built from `schema.Methods()`, and the method checks from `descriptors/agreement.go`. |
| `Config` | One type that merges `IngressConfig`, `EgressConfig`, the HTTP fields of `ProxyConfig`, and `Reflection`, `Destinations`, `Descriptors` and `CandidateStartupTimeout` from `proxies.Config`. Flag names do not change, and it has no prefix. |
| `Listens` | `ingress`, `reference-egress` and `candidate-egress`. |
| `New` | `NewIngress` and `NewEgress`, which take the `Side`s instead of building engines and candidates. The route matcher is built from `Handle.Declared`. Ingress reads migrated routes from `Side.Nodes`. The decoder sets `Content.Normalise` from the selected rule. |
| `Serve` | Today's `Ingress.Serve` and `Egress.Serve`. |

These parts move out of the package:

- `middleware` and the `Serve`, `Binding` and `Lifecycle` serve loop move to `internal/httpservice`.
- `nodeQuarantiner` moves to the mirror.
- `Egress.Engine()`, `Egress.Candidates()` and `Ingress.Calls()` are removed.

### MySQL (`internal/protocols/mysql`, from `internal/mysqlserver`)

| Part | Comes from |
| --- | --- |
| `Protocol()` = `"mysql"` | `mysqlserver.Protocol` |
| `Kinds()`: `egress[mysql]`, with no settings | New. |
| `Scripts()` | The `scripted` subpackage. It holds `egress.mysql.statement(s)` from `module.go`, `statements.go`, the MySQL evaluator code, `Scripts.CombineStatements` and `SplitStatement`, the MySQL half of `spectre.d.ts`, and `spectre.mysql`. It uses `TypeArgumentsCheckOnly` for the `EgressMySQL*` methods, which replaces `mysqlOwner`. |
| `Config` | `mysqlserver.Config`, which wraps its fields with the `mysql-` prefix itself. |
| `Listens` | `reference` and `candidate` when `Upstream` is set, otherwise none. |
| `New` | `mysqlserver.New` plus `scripted.New(handle, exact.New())`, moved out of `startMySQL`. It uses `Runtime.Egress`. When MySQL is disabled it returns a server that only waits for ctx. |
| `Serve` | Today's `Serve`. It also marks its egress handler ready once it is serving, so egress health now includes MySQL. |

## What shared code stops knowing

- **`internal/proxies`**: no protocol imports, fields, listeners, config or start functions.
- **`internal/engine`**, formerly `internal/protocol`: no `javascript.Rule`, no HTTP status parsing, no matchers.
- **`internal/comparison`**: no HTTP or MySQL methods on `Scripts`.
- **`internal/comparison/javascript`**: no HTTP or MySQL members, types, declarations, carve-outs or tests.
- **`internal/schema`**: no `builtin.d.ts`, no protocol constants, no RPC operations.
- **`internal/descriptors`**: no RPC method checks.
- **`internal/state`**: no protocol fields, kinds or checks.
- **`internal/model`**: no `ProtocolHTTP`.
- **`internal/cli`**: no protocol knowledge, as today. It gains the registry.
- **`internal/server`, `internal/sample` and `cmd/spectre-sample`**: they import `internal/httpservice` instead of the HTTP protocol.
- **`internal/integration`**: the shared harness starts a mirror from a registry and named listeners. Protocol helpers stay in protocol-specific files, since they test the whole binary.

## Risks

- **Script bindings expose sobek.** Protocol packages depend on sobek's API and version. Bindings must keep the evaluator's rules: each runtime has one caller, and re-evaluation must reproduce the same declarations. `NewBinding` is the most complex new API here.
- **Interface merging changes the script type errors.** For example, a missing protocol is reported as "Property 'kafka' does not exist on type 'Ingress'". `compile.go` must recognise generic calls declared in any binding's file, not only `/spectre.d.ts`.
- **Kong plugins.** Environment variables, the TOML config file, `--help` and duplicate-flag detection must behave for plugins as they do for embedded configs. ADR 004 planned plugins but has not used them yet. `kong.Plugins` is `[]any`, which `noany` may report at the call site.
- **One engine instead of two.** Scripts are evaluated once rather than twice. `engine.Close()` must still affect only egress.
- **Startup order.** The mirror starts egress before ingress today. With one `Serve` per provider, a provider's ingress and egress start together. Ingress stays unready until both backends accept connections, so this should be safe, but it needs checking.
- **Shutdown order.** The mirror now owns the stop and drain sequence that HTTP egress owns today. Getting it wrong would quarantine candidates during shutdown.
- **Generic state settings lose Go typing.** HTTP reads its routes as map keys. The invariant that a `Unique` setting's keys are routes must hold for every provider, including custom ones.
- **Package name.** `internal/protocols/http` shadows `net/http`. Its importers need an alias, and `revive` needs the same exclusion it has for `internal/httputil`.
- **Test churn.** About 2,000 lines of HTTP and MySQL script tests move from `comparison` and `comparison/javascript` into the protocol packages. The shared packages need a small test binding.

## Open questions

None.

## Migration order

Each step leaves the build and the tests green. None of them changes the public Go API.

1. **Rename the engine package** from `internal/protocol` to `internal/engine`.
2. **Move shared HTTP plumbing.** Move `httpserver/middleware` and the serve loop to `internal/httpservice`. Move `model.ProtocolHTTP` to `httpserver.Protocol`, and give shared tests their own protocol constant.
3. **Normaliser in content.** Replace `Content.Rule` with `Content.Normalise`. The HTTP decoder builds it with the existing `Scripts.NormaliseResponse`, and the engine drops its HTTP status parsing.
4. **Remove matchers.** HTTP's route matcher reads endpoints and rules from `Scripts()` directly. Then delete `Matcher`, `Declaration` and `Engine.SelectRule`.
5. **One engine, owned by the mirror.** The mirror builds the candidates, the call counter and the engine with a quarantiner per direction, plus the shared node quarantiner. HTTP and MySQL receive them, and the mirror takes over shutdown.
6. **Generic migration state.** Add `state.Kind`, generic `Settings`, `KindNode`, `Kinds`, `Nodes` and `Owner`. For now the mirror and the CLI use HTTP's kinds, still written in `httpserver`.
7. **Operations to HTTP.** Replace `schema.Operation` with `schema.Method`. Move `Operation`, the RPC lookup and the protobuf method checks into `httpserver`.
8. **Binding machinery.** Add `Binding`, `Scope`, `Handle`, `TypeArguments`, the `Ingress` and `Egress` interfaces in `spectre.d.ts`, and payload modules in `schema`. Adapt today's registry into one internal binding that holds both protocols.
9. **Move HTTP's script code** into its `script` subpackage, with its tests.
10. **Move MySQL's script code** into `mysqlserver/scripted`, with its tests, and delete `builtin.d.ts`.
11. **The provider interface.** Add `internal/protocols` with `Provider`, `Registry` and `Runtime`.
    - Move `httpserver` and `mysqlserver` to `internal/protocols/http` and `internal/protocols/mysql`, and convert them.
    - Move their flags into their configs.
    - Make the mirror and its listeners generic, with MySQL in egress health.
    - Register the providers in `Main` and pass the registry to the CLI.
    - Add the `architecture` rule for the subpackages.
    - Update `USAGE.md` for `egress[mysql]` and the egress health change.
    - Write the ADR that supersedes the affected parts of ADR 003.
