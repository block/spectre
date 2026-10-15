# Compile-time extensions

## Problem

Teams need to build their own `spectre` binary that adds to the open-source one. Examples include loading protobuf descriptors from a schema registry, supporting internal RPC protocols, and adding commands. Today nothing is importable, because every package is under `internal/`.

## Goals

- A custom binary gets every built-in command and flag, including commands added in later releases, without copying any command-line code.
- Only the extension points we deliberately approve are public. Everything else stays in `internal/`.
- Extensions are passed in explicitly. There is no global registry.

## Non-goals

- Runtime plugins, using either Go's `plugin` package or separate processes.
- Making `internal/` packages public.
- Backwards compatibility while the version is 0.x.

## Design

The module root, `github.com/block/spectre`, is the only public package. It owns the command line and startup, and offers one option per extension point.

`cmd/spectre/main.go` becomes:

```go
func main() { spectre.Main() }
```

A custom binary in another repository looks like this:

```go
func main() {
	spectre.Main(
		spectre.WithCommand("image", "Build a Docker image.", &image.Command{}),
	)
}
```

Commands are the first extension point. Descriptor sources are planned next, as described below.

### Contracts

Every public contract is a type defined in the root package. The root package never exposes an internal type, not even through an alias. Aliases show internal import paths in documentation and editors, and expose every exported field and method of the internal type.

A contract that internal code uses is also defined in the lowest internal package that owns the concept. The public type mirrors it:

- **Interfaces** mirror with the same method set. Go interfaces match by methods, so a value of the public interface is passed to internal code as is. That assignment also fails to compile if the two drift apart.
- **Data types** mirror with only the fields that are meant to be public. Each has one conversion function, placed beside it in the root package and covered by tests.

A contract that only the root package uses has no internal counterpart.

```go
// github.com/block/spectre

// Command is a top-level command. Kong parses its tagged fields before Run.
type Command interface {
	Run(ctx context.Context, log *slog.Logger) error
}
```

A public contract's signatures may mention only:
- the standard library
- third-party types such as `descriptorpb`
- other public contracts

Each new extension point adds one public contract and one `With...` option. Adding one is a deliberate change to the public API.

### Commands

- `WithCommand(name, help, command)` adds a top-level command with `kong.DynamicCommand`. `command` must be a pointer to a struct, because Kong parses flags into its fields.
- `Main` binds the context and logger, so a command's `Run` receives them.
- Extension flags work everywhere built-in flags do: environment variables, the TOML config file and `--help`.

### Planned: descriptor sources

A descriptor source supplies protobuf descriptors alongside the descriptors directory, for example from a schema registry.

- `WithDescriptorSource[C](build func(ctx, C) (DescriptorSource, error))` would register a constructor and its Kong-tagged config type `C`.
- For each command that loads descriptors (`run` and `gen proto`), the root package would create a fresh `C`, add it to the command's `kong.Plugins` field, and call `build` with the parsed config.
- The supervisor would load descriptors once and pass the loaded set to ingress and egress, so both proxies decode with the same descriptors.
- `DescriptorSource.Load` needs an argument that says what to load, such as a path or subject. Its shape is still open.

### Validation

`Main` adds no checks of its own. Kong currently allows these, and they should be fixed in Kong rather than here:

- an extension command with the same name as a built-in command
- a command flag with the same name as a root flag
- a command that is not a pointer to a struct, which panics

### Internal changes

The command-line types and startup code move from `cmd/spectre/main.go` into the root package. This includes the egress-sandbox relaunch check, which must run before flags are parsed.

## Enforcement

- A new `publicapi` tsk linter reports any exported declaration in the root package whose type mentions a type from `internal/`, including through an alias.
- The `layout` linter's `allow` list gains the root package.
- `CLAUDE.md` changes:
  - The root package is the only public API.
  - Extension contracts used by internal code are defined internally and mirrored at the root.
  - A public mirror in the root package is an exception to the rule against duplicating types.
  - Startup code lives in the root package, and `main` only calls `spectre.Main`.

## Open questions

- Should a custom binary be able to set its own `--version` string?
- Should a golden file of exported symbols supplement the linter, so API changes show up in review?
- What does `DescriptorSource.Load` take to identify the descriptors to load?
- Which extension points follow descriptor sources, for example HTTP RPC formats?
