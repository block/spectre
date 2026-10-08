# Architecture

- Put each executable entry point in `cmd/<command>`.
- For each binary, add a symlink named after the binary in `./scripts` pointing to `spectre-ingress`.
- Keep all initialisation and bootstrap code in the main entry point.
- Do not put application logic in main entry points. The `mainlogic` linter
  allows only `main` and `Run` methods there. Keep a bootstrap helper with
  `//nolint:mainlogic <reason>`.
- Do not use global state outside main entry points.
- Read environment variables only through Kong `env:` tags, then pass
  configuration explicitly. `forbidigo` rejects the `os` and `syscall` environment functions.
- Each package that exposes command-line options owns its Kong-tagged `Config` type.
- Give every Kong flag, argument, and command a `help:"..."` tag. The `konghelp`
  linter enforces this.
- Embed package configs in CLI commands with `embed:""`; do not pre-initialise CLI defaults because Kong resets them.
  The `kongembed` linter enforces the tag.
- Pass the complete parsed config to the package constructor instead of unpacking individual fields.
- Use `kong.ApplyDefaults()` in config constructors so Kong tags remain the single source of default values.
  The `configdefaults` linter enforces this.
- Define config validation as a `Validate()` method on the package-owned `Config` type.
- Put all other code in the top-level `internal` directory unless it is explicitly part of a public API.
- The `layout` linter enforces where packages live and the `./scripts`
  symlinks. List public API packages in its `allow` option in `.tsk.toml`.

# Compatibility

- While the version is 0.x.y, make breaking changes freely. Do not add backwards-compatibility shims, deprecation paths, or migration code.

# Automation

- Use `bit` and define tasks in `BUILD.bit` for all automation that would normally go in a Makefile, Justfile, or other build file.
- Run `bit -s <provider>` before adding build configuration so the most specific provider and correct fields are used.
- Run tests, linting, and formatting through `bit` targets instead of invoking the underlying commands manually.
- All tools used in build files or documentation must be installed in the repository's Hermit environment.
- Invoke commands from `scripts` by name. Never prefix them with the repository-relative scripts path because Hermit adds that directory to `PATH`.

# Documentation

- Do not modify `README.md` unless the user explicitly requests it.
- Put developer workflow documentation, such as building, testing, and running locally, in `CONTRIBUTING.md`.
- Put design documents, implementation plans, and other documents that fit neither `README.md` nor `CONTRIBUTING.md` in `docs/`.

# Commits and pull requests

- Always use Conventional Commits for commit messages and pull request titles.
- Keep commit messages and pull request titles and descriptions succinct and in plain English. Avoid jargon.
- In commit messages, explain why the change is needed, not what changed.
- In pull request descriptions, explain why the change is needed and the high-level approach taken.
- Write pull request descriptions as plain paragraphs when the explanation is straightforward. Use bullet lists for more complex explanations.
- Add Mermaid diagrams to pull request descriptions for very complex changes.
- Do not use Markdown headers in pull request descriptions. Other Markdown, such as code blocks, is fine.
- To auto-merge a pull request, create it first, then run `gh pr merge <number> --auto --squash`.

# Go conventions

- Use `log/slog` for logging.
- Keep a type and all of its methods in the same file. Do not split a type's
  methods across multiple files. The `methodfile` linter enforces this.
- Wrap errors with `github.com/alecthomas/errors`.
- Name every input parameter in function, method, and interface signatures.
- Name result parameters when intrinsic types such as `bool`, `int`, or `string`
  do not convey their meaning. Do not use naked returns.
- Keep every comment to at most two lines. The `commentlength` linter enforces
  this.
- Document every public symbol.

# Linters

- `bit lint-tsk` runs the `tsk` linters; `tsk config` lists them. Keep an
  intentional finding with a `//nolint:<linter> <reason>` comment at the end of
  its line or on its own line above the statement.
- When the user corrects you, check whether the correction is a general rule
  that a `tsk` linter could enforce. Offer a linter only when all of these hold:
  - The rule applies across the codebase, not only to the current change.
  - The rule is likely to come up again.
  - A linter can detect violations from Go syntax and types with few false
    positives.
  - No existing linter in `.golangci.yml` or `tsk config` already enforces it.
- For example, "use `Option` instead of nil for absent values" justified the
  `optionalnil` linter. "Rename this variable" does not justify a linter.
- Do not interrupt the current task to offer a linter. Make the offer once in
  your final response for that task.
- Offer each rule at most once per conversation. If the user declines, do not
  raise it again.
- Write a linter only after the user agrees. Follow the `tsk` README at
  https://github.com/alecthomas/tsk:
  - Put the linter in `.tsk/<name>.ts` and its test data in
    `.tsk/testdata/<name>/`. `bit test-tsk` runs the tests.
  - Fix existing violations in the same change, or keep intentional ones with
    `//nolint:<name> <reason>`.
  - Add or update the matching rule in this file, and note that the linter
    enforces it.

# Optional values

- Use `github.com/alecthomas/types/optional.Option[T]` for any value that may be
  semantically absent, instead of a nil pointer, interface, func, channel,
  slice, or map, or a separate `present` flag.
- Use a plain slice or map when absent and empty mean the same thing.
- Dot-import it as `. "github.com/alecthomas/types/optional"` and write
  `Option[T]`, `Some(value)`, and `None[T]()`. It is the only allowed dot import.
  The `optionalstyle` linter enforces the dot import.
- Keep Go's `(value, ok)` return idiom for lookups instead of returning `Option`.
- Keep nil checks on required arguments; those values are not optional.
- Read values with `Get()` or `Default()` and handle the absent case. Never call
  `MustGet()` outside tests; the linter rejects it.
- Tag optional JSON fields with `omitzero`, not `omitempty`. The
  `optionalstyle` linter enforces this.
- `Option` decodes JSON with `json.Unmarshal`, ignoring decoder settings such as
  `DisallowUnknownFields`. A type held in an `Option` must enforce strict
  decoding in its own `UnmarshalJSON`.

# Go testing

- Use `github.com/alecthomas/assert/v2` for assertions. Remember that
  `assert.Equal()` performs a deep comparison.
- Compare whole objects rather than individual fields. Exclude dynamic values
  with `assert.Equal(t, expected, actual, assert.Exclude[T]())`.
- Use table-driven tests when cases can be parameterized on data. Otherwise,
  create distinct test functions.
- Name Go test functions and subtests in UpperCamelCase. Never use underscores
  in test function names.
- Update an existing Go test when appropriate instead of creating a new one.
- Run necessary tests with `go test -timeout 30s`. Do not generally use `-v`.
