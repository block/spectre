# Spectre context propagation

**Status:** Accepted

## Problem

Spectre needs to associate outbound operations from the reference and candidate implementations with the same mirrored inbound request. OpenTelemetry is not required or currently available.

## Decision

We will propagate our own context.

### Approach

- Assign a shared `spectre_request_id` at ingress and pass it to both implementations.
- Egress identifies the reference and candidate by the listener an operation arrives on, never by propagated context, so a candidate cannot claim to be the reference.
- Propagate context through protocols with header support, such as HTTP, gRPC, and Kafka, as a `spectre_request_id` entry in the W3C `baggage` header, which services already propagate. Ingress adds the entry without disturbing other baggage, and egress removes only that entry before forwarding to a dependency. The sections below cover only protocols without header support.
- An operation without Spectre context quarantines the candidate, as with any other divergence.

### MySQL

Use named query attributes, such as `spectre_request_id`, rather than SQL comments or connection-level state.

- The proxy negotiates `CLIENT_QUERY_ATTRIBUTES` and extracts attributes from `COM_QUERY` and `COM_STMT_EXECUTE`.
- Inject fresh context on every execution, including reused prepared statements. Attributes travel alongside parameters without changing or repreparing SQL.
- Negotiate upstream capabilities separately; consume Spectre attributes and re-encode packets as required for the upstream connection. RDS does not need to emit traces or understand Spectre context.
- Because the proxy reads and rewrites packets, it must terminate TLS and take part in authentication between the client and the upstream server.
- Candidates are answered from reference recordings, either by replaying raw response packets or by decoding and re-encoding results. Raw replay requires both client connections to negotiate identical capabilities, such as `CLIENT_DEPRECATE_EOF`, which changes result set encoding. A candidate that negotiates different capabilities from the reference is quarantined.

| Client | Approach |
| --- | --- |
| Java: MySQL Connector/J | Query attributes supported since 8.0.26 through `JdbcStatement.setAttribute()`, including prepared statements. Standardize on a maintained compatible version and refresh/clear attributes on every execution. Native OTel support is irrelevant here. |
| Go: `go-sql-driver/mysql` | Temporarily fork to expose attributes and encode them for ordinary queries and prepared-statement executions. No per-query attribute API in the checked v1.10.1 release. |
| Ruby: `mysql2` | Temporarily fork the C extension to expose Oracle `libmysqlclient` attribute APIs. Use a compatible 8.4+ client library for both ordinary and prepared queries. |

Oracle's open-source C APIs are `mysql_bind_param()` for ordinary queries (8.0.23+) and `mysql_stmt_bind_named_param()` for prepared statements (introduced in 8.2.0; that release had shared-library export issues). MariaDB Connector/C is not an interchangeable choice for these APIs.

SQL comments cannot carry fresh context on each execution of a reused server-side prepared statement.

### Redis

Redis has no standard per-command trace-context carrier. Proposed proxy extension:

```text
SPECTRE.EXEC <context> <command> <arguments...>
```

The client serializes Spectre context into the envelope; the proxy extracts it and forwards the original command. Integrate at command encoding so context stays attached to each command when pipelining or multiplexing.

## Consequences

- IDs correlate operations with their inbound request. Matching reference and candidate operations still requires Spectre's comparison logic; execution order may differ.
- Identical operations issued concurrently within one request remain ambiguous, because each implementation may issue them in a different order. Spectre detects this and delegates resolution to the scripting layer.

### Migration scope

Maintain temporary Go/Ruby client forks while Spectre migrates services from the existing framework to the JVM. Retire them as those services migrate. Keep JVM MySQL drivers current and retain custom propagation in the JVM client integrations; Redis still requires the proxy extension.

## Open questions

- How should the Redis envelope handle transactions, cluster routing, and special connection modes? Define this before implementation.

## References

- [Connector/J query attributes](https://dev.mysql.com/doc/connector-j/en/connector-j-query-attributes.html)
- [C API: ordinary query attributes](https://dev.mysql.com/doc/c-api/8.4/en/mysql-bind-param.html)
- [C API: prepared-statement attributes](https://dev.mysql.com/doc/c-api/8.4/en/mysql-stmt-bind-named-param.html)
- [Redis protocol](https://redis.io/docs/latest/reference/protocol-spec/)
