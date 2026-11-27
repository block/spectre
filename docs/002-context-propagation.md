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
- A candidate operation without Spectre context quarantines the candidate, as with any other divergence.
- A reference operation without Spectre context belongs to no mirrored request, such as one for a request ingress did not mirror or for work no request starts. It is forwarded but never paired, so no candidate operation is expected for it. MySQL statements are the exception, as below.

### MySQL

Carry the request ID in a SQL comment on each statement, rather than in query attributes or connection-level state.

- Services copy the ID from each request's baggage into a comment on every statement they send for it. The proxy finds the ID by its own prefix in any of the statement's comments, so a bare ID, a sqlcommenter tag or a tag that an existing comment-tagging library writes all carry it.
- The proxy relays reference statements unchanged, and the server ignores the comment, so the proxy never rewrites packets and needs no protocol extension. Query attributes would have needed a server library that reads them, rewritten reference packets, and forks of the Go and Ruby clients.
- Comments are read outside strings, quoted names, optimiser hints and executable comments, with MySQL's default quoting rules. A session that turns off backslash escapes, or uses a character set such as GBK, could have its strings misread.
- A statement without an ID inside a transaction belongs to the transaction's request, so a commit or rollback that a driver sends itself still pairs.
- `SET` statements and database selections only change session state. They are never paired and need no ID.
- Unlike operations of other protocols, every other reference statement without a valid ID quarantines the candidate, because no candidate statement could pair with it and it would go uncompared.
- Prepared statements are not supported, because a comment cannot carry fresh context on each execution of a reused server-side prepared statement. The candidate cannot send them, and a reference prepared statement quarantines the candidate. Client-side prepared statements, which Connector/J uses by default, reach the server as text statements and work.

| Client | Approach |
| --- | --- |
| Java: MySQL Connector/J | Add the comment through the ORM's statement hooks, which Hibernate and jOOQ both offer, or a datasource proxy. Keep client-side prepared statements. Statements that the driver or pool sends itself carry no ID, so only their `SET` statements are accepted. |
| Go: `go-sql-driver/mysql` | Add the comment to each statement. No fork is needed. |
| Ruby: `mysql2`, Trilogy | Add the ID to the comment tags that services already write on each statement. |

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

MySQL needs no client forks: services in every language add the ID to statements they already send, and retain that propagation in the JVM client integrations. Redis still requires the proxy extension.

## Open questions

- How should the Redis envelope handle transactions, cluster routing, and special connection modes? Define this before implementation.

## References

- [MySQL comments](https://dev.mysql.com/doc/refman/8.4/en/comments.html)
- [sqlcommenter specification](https://google.github.io/sqlcommenter/spec/)
- [Redis protocol](https://redis.io/docs/latest/reference/protocol-spec/)
