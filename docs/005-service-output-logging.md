# Service log output

**Status:** Implemented

## Problem

SPECTRE runs in place of the service it fronts and shares its log stream. Two things make that stream hard to use:

- The candidate's and reference's logs pass straight through, so an operator cannot tell which lines came from which service, or from SPECTRE.
- SPECTRE's own JSON logs use slog's shape, while the log ingester expects the service's: its field names, level names, timestamp format and nesting.

## Goals

- Every line can be attributed to the candidate, the reference or a part of SPECTRE.
- SPECTRE's own lines suit the same ingester as the service's.
- The services' lines reach the ingester as the services wrote them, apart from the label.

## Non-goals

- Converting the services' logs into SPECTRE's format, or SPECTRE's into theirs.
- Filtering the services' logs by SPECTRE's log level.
- Labelling the services' stderr.

## Decision

### Two separate paths

SPECTRE's own logs are rendered in the service's format, and the services' logs are passed through with a label. Neither is converted into the other.

We considered parsing the services' JSON lines into SPECTRE's records and rendering both the same way. That loses whatever the parser does not understand, and it needs every service field and level name mapped before a line can be read correctly.

### Service logs: label and pass through

Each service's stdout is read line by line:

- A line that is a JSON object gains a `spectre.subsystem` member, set to `candidate` or `reference`, as its first member. Every other byte is kept, including the service's own level and timestamp.
- Any other non-blank line is prefixed with the role and a colon. The service already wrote it as text, so leaving it as text is still faithful.
- Blank lines pass through unchanged.

The label key is namespaced so a clash with a service's own field is very unlikely; SPECTRE does not check for one. Stderr passes straight through.

Output with no newline is passed through in 64 KiB pieces, so a service that never writes a newline cannot exhaust the supervisor's memory.

### SPECTRE's logs: a format template

`--log-json` takes a JSON object that is a template for each line, replacing a flag that only switched to slog's JSON shape. `--log-json=DEFAULT` is shorthand for a template matching that shape. A string value in angle brackets is a placeholder:

- `<msg>` is the message.
- `<level>` is the level, using slog's names. `<level:LEVEL=NAME,...>` maps slog levels to the service's names, such as `warn=WARNING` or `error+4=FATAL`. A level between two names uses the lower one. When every name is an integer, the level is written as a number, as some loggers do.
- `<time>` is the time in RFC 3339 form. `<time:FORMAT>` takes a Go time layout or `unix`, `unixms` or `unixns`, which are written as numbers.
- `<attrs>` is an object holding the remaining attributes. Without it, they go at the top level, which is common.

Other values, including nested objects and arrays, are written as given. Template keys take precedence over top-level attributes of the same name. Unknown placeholders are rejected at startup.

SPECTRE's own lines carry the same `spectre.subsystem` key, set to `supervisor`, `ingress` or `egress`, so one key identifies every component across both paths.

Text output is unchanged, because it is for people rather than the ingester.

## Consequences

- The ingester receives the services' lines as written, plus the label, and SPECTRE's lines in the services' shape.
- SPECTRE's `--log-level` does not apply to the services' lines.
- SPECTRE's lines go to stderr and the services' logs to stdout, as before.
- Rendering the template needs a JSON writer of SPECTRE's own, because slog's JSON handler can rename keys but cannot nest them.
- The supervisor waits for a service's stdout pipe to close as well as for the process to exit. A grandchild that inherits the pipe and outlives the service holds it open until `--shutdown-timeout`.

## Open questions

None.

## References

- [cachew logging configuration](https://github.com/block/cachew/blob/main/internal/logging/logging.go)
