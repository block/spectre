# Service output logging

**Status:** Implemented

## Problem

The supervisor hands the candidate and reference services its own stdout and stderr, so their logs pass straight through. SPECTRE poses as the service it fronts, so an operator reading one log stream cannot tell which lines came from the proxies and which from either service. Internal log pipelines also expect field names that differ from `log/slog`'s defaults, such as `message` and `timestamp` instead of `msg` and `time`.

## Decision

### Wrap service logs

The supervisor gives each service a pipe for stdout and logs every complete line as one record through its own logger. Each record carries the existing `subsystem` attribute, set to `candidate` or `reference`. Stderr still passes straight through, because the goal is to normalise the services' logs, not every byte they write.

A plain text line is the record's message, logged at info level.

A line that is a JSON object is passed through structured, so a log pipeline can index the service's own fields:

- Its members become top-level attributes of the record.
- A string `msg` or `message` member becomes the record's message. A string `level` or `severity` member sets the record's level; `trace`, `warning`, `fatal`, `panic` and `critical` map onto the nearest slog level, and unrecognised names fall back to info.
- A `time`, `timestamp` or `ts` member is dropped in favour of SPECTRE's own timestamp, which avoids two time fields after remapping.
- The `subsystem` label overrides a same-named member, so a line stays attributable to the service that wrote it.

Anything else, including JSON arrays and malformed objects, is logged as plain text.

### Remap JSON fields

`--log-remap OLD=NEW` renames top-level JSON fields, including the built-in `time`, `level` and `msg` keys, following cachew's logging configuration. Nested attributes keep their names. Text output is unchanged, because it is for humans rather than log pipelines. Wrapped service lines flow through the same handler, so they are remapped too.

## Consequences

- Services must log to stdout to be labelled; anything on stderr stays unlabelled.
- Service logs are subject to SPECTRE's `--log-level`, so a service's debug lines are dropped at the default level.
- A trailing line without a newline is logged once the service exits.
- The supervisor waits for a service's stdout pipe to close as well as for the process to exit. A grandchild that inherits the pipe and outlives the service holds it open until `--shutdown-timeout`.
- A service's own timestamp is lost; SPECTRE's is recorded instead.

## Open questions

None.

## References

- [cachew logging configuration](https://github.com/block/cachew/blob/main/internal/logging/logging.go)
