# Architecture

- Put each executable entry point in `cmd/<command>`.
- For each binary, add a symlink named after the binary in `./scripts` pointing to `spectre-ingress`.
- Keep all initialisation and bootstrap code in the main entry point.
- Do not put application logic in main entry points.
- Do not use global state outside main entry points.
- Read environment variables only in main entry points, then pass configuration explicitly.
- Put all other code in the top-level `internal` directory unless it is explicitly part of a public API.

# Automation

- Use `bit` and define tasks in `BUILD.bit` for all automation that would normally go in a Makefile, Justfile, or other build file.
- Run tests, linting, and formatting through `bit` targets instead of invoking the underlying commands manually.
- All tools used in build files or documentation must be installed in the repository's Hermit environment.

# Documentation

- Do not modify `README.md` unless the user explicitly requests it.
- Put all developer documentation in `CONTRIBUTING.md`.

# Commits and pull requests

- Always use Conventional Commits for commit messages and pull request titles.
- Keep commit messages and pull request titles and descriptions succinct and in plain English. Avoid jargon.
- Explain why the change is needed, not what changed.

# Go conventions

- Use `log/slog` for logging.
- Wrap errors with `github.com/alecthomas/errors/v2`.
- Define and use constructor functions for all public types.
- Keep every comment to at most two lines.
- Document every public symbol.
