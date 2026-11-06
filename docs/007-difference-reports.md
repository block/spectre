# Difference reports

**Status:** Implemented

## Problem

When a candidate's response diverges, the mirror reports only the paths where the responses differ, such as `$status` or `$.fees[0].rate`. Production payloads can hold personal data, so these reports never include values, and they reach the SPECTRE server, logs and `spectre status`.

A path alone doesn't say what went wrong. Someone reading `$.fees[0].rate` can't tell whether the candidate dropped the field, added one, sent the wrong value, or sent a different kind of value. A renamed field shows up as two unrelated paths. And when the same field differs in every element of a list, one cause turns into one line per element until the report hits its cap.

## Goals

- Say how each path differs, not only where.
- Keep values out of every report, as before.
- Keep reports short when one cause affects many list elements or map entries.

## Non-goals

- Reporting values, or hashes of them, even for fields that look harmless.
- Linking a report to the request that caused it, or to the candidate's logs.
- Reporting how often a route diverges across many comparisons.

## Decision

### How a path differs

Each reported difference has a path, how the candidate differs there, and a count. The change is one of:

| Mark | Change | Meaning |
|---|---|---|
| `-` | missing | The reference has a value at the path and the candidate does not |
| `+` | added | The candidate has a value at the path and the reference does not |
| `~` | changed | Both have values of the same JSON type that differ |
| `!` | retyped | Both have values of different JSON types |

A retyped difference also names both JSON types: null, boolean, number, string, object or list. A type doesn't reveal the value. JSON null is a type of its own, so a value replaced by null is retyped, not missing.

A response with a body on one side only is missing or added at `$`. A different status is changed at `$status`. Status codes are not reported.

### Paths

Paths still name only fields the schema declares. Object keys that are payload data, such as map keys and the keys of untyped bodies, still appear as `[*]`.

List indexes are kept when a single element differs, because an index isn't a value. When differences at several indexes would otherwise be identical, they are merged into one entry whose indexes are `[*]`. Differences at identical paths are also merged, for example several map entries that changed. A merged entry reports how many differences it stands for. Extra or missing list elements merge the same way, so a candidate list that is two elements short reports `- $.fees[*] ×2`.

The cap of 100 differences still bounds both the work and the report. A report that reached the cap says so.

### Presentation

Logs list each difference on one line, joined by commas. `spectre status` lists them under the quarantined route, one per line, in the order the comparison found them:

```
feeplans-fe cc009bd staging-green-us-west-2 spectre v0.52.0, up 4h25m33s
  ingress http GET /v2/processing-fees: response differences (node processing-fees-v2)
    ! $.currency string → null
    ~ $.fees[0].rate_bps
    + $.fees[*].roundingMode ×4
    - $.fees[*].rounding_mode ×4
    - $.fees[*] ×2
    ~ $.metadata[*] ×3
  quarantined 2026-11-06T01:28:53Z
```

Object keys are compared in sorted order, so a renamed field usually appears next to its old name.

## Consequences

- What a report reveals stays small and fixed: declared field names, list indexes, JSON types, how many entries a path stands for, and whether the report was truncated.
- A changed boolean reveals that the candidate holds the opposite value. We accept this, because the reference's value is never reported and a boolean on its own is rarely personal data.
- A count reveals how many map entries or list elements differ, which bounds the size of a map or list. It doesn't reveal any key or element.
- The service protocol and the server's stored divergences change shape. Mirrors and the server must be upgraded together, and divergences recorded before the upgrade must be cleared, because the server rejects stored records it can't decode.

## Open questions

- Should status codes be reported? They come from the protocol rather than the payload, and would say much more about a `$status` divergence.

## References

- [Service log output](005-service-output-logging.md)
