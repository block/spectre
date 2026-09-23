# Architecture

- Put each executable entry point in `cmd/<command>`.
- Keep all initialisation and bootstrap code in the main entry point.
- Do not put application logic in main entry points.
- Do not use global state outside main entry points.
- Read environment variables only in main entry points, then pass configuration explicitly.
- Put all other code in the top-level `internal` directory unless it is explicitly part of a public API.

# Go conventions

- Use `log/slog` for logging.
- Wrap errors with `github.com/alecthomas/errors/v2`.
- Define and use constructor functions for all types.
- Keep every comment to at most two lines.
- Document every public symbol.
