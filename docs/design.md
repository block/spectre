# SPECTRE: Safe, LLM-Driven Service Migration

# What?

SPECTRE (Service Pair Execution Comparator for Traffic Response Equivalence) is a system for safely migrating services from one language/framework to another using LLMs. It works by running an instance of both the reference and candidate implementations of the service in parallel, semantically validating the candidate implementation's external behaviour against the reference. The reference implementation is authoritative for all responses and side-effects (eg. database writes, queued data). The candidate implementation is completely side-effect free, only its behaviour is validated.

Any behavioural violations are delivered to an external [capture service](#capture) for later assessment by the migration system. An LLM performing the migration utilises this captured feedback to modify the behaviour of the code, gradually improving it until there are no violations.

![Scaffold the service container once. The LLM migrates or revises an endpoint, then the candidate is built, checked and deployed with OLD in a production Mirror pod. One-time onboarding of OLD supplies routing, tracing, adapters and the baseline. Mirror verifies live traffic with only OLD writing or replying to clients, while NEW stays network sandboxed. Record differences, coverage and exact revisions inside the protected production boundary. If acceptance criteria are not met, prepare safe feedback and return it to the LLM for another revision; the feedback mechanism remains unresolved and raw production evidence stays inside production. When the endpoint is verified, repeat for the next endpoint. Once all endpoints are complete, plan production cutover as a separate promotion decision.][image1]

# Why?

Large service estates often span many frameworks and languages. Maintaining this variety becomes increasingly expensive because each stack needs its own updates, security fixes, tooling, and expertise. Some services may also depend on versions that are already at or approaching end of life. Consolidating on a smaller set of supported runtimes provides a more consistent path for deploying security and version updates, improving frameworks, and fixing shared issues.

However, migrating services from one language to another with LLM's is risky. Unit and integration tests themselves give some degree of confidence, but as they are themselves migrated by the LLM, alone they are not a guarantee. Acceptance tests give us more assurance. Especially regressions on non functional requirements, like latencies, transaction boundaries, timeouts, and throughput are not covered by traditional tests. Achieving high confidence over a wide range of inputs is an intractable problem. 

Beyond service migration, this approach could also be used to validate dependency upgrades, consolidate on a single language or framework within an existing runtime ecosystem, reduce service sprawl with modulith migrations, and so on. There's also potential even for normal deployments, for comparing reference and candidate behaviour.

The goal of SPECTRE is to give us very high confidence that a migration is correct, with zero risk.

# How?

As described above, SPECTRE consists of two proxies: one that forks inbound traffic and another that joins outbound traffic, for the reference and candidate service implementations. Apart from traffic interception, the reference service will be deployed as normal. The candidate implementation however, will be completely isolated from the network by host-based firewall rules, allowing only specific egress and only through SPECTRE. This guarantees that the candidate implementation cannot have unintended side-effects.

A migration will begin its life in staging for initial iteration, but eventually run in production as staging doesn't provide sufficient behavioural fidelity.

## Ingress

Requests to the pod are intercepted by SPECTRE's ingress proxy, trace IDs registered, then the request is mirrored to both the reference and candidate implementations. Responses from the candidate implementation are then semantically cross-[validated](#validation) against the reference implementation's responses, while only the reference implementation's responses are returned to clients. If divergences occur they are logged to an external capture service for later analysis, and the candidate implementation [quarantined](#quarantine).

## Egress

Outbound requests to dependencies from both reference and candidate service implementations are intercepted by SPECTRE's egress proxy, including writes to databases, queues, and other services. Each request from the *reference implementation* is passed through to the dependency. When the request from the candidate implementation arrives it is compared to the reference request for semantic equivalence. Requests are matched by their trace ID, registered during ingress. If equivalent, the response to the reference implementation's request is also returned to the candidate implementation. Otherwise, the candidate implementation is quarantined and its divergence logged to the capture service.

## Validation {#validation}

Validation will need tuning and improvement over time. It will be per-protocol, and likely customisable per service. For example, for gRPC we'll need to be able to decode the raw protobuf, compare fields, ignore non-deterministic data such as timestamps and ID's, handle organisation-specific annotations, handle PII, and so on. For MySQL we'll need to be able to normalise and compare queries across different ORMs, though a better solution here might be custom libraries that mimic the previous system's queries.

## Quarantine {#quarantine}

As soon as a validation failure occurs, the candidate service will be quarantined and no further traffic will be sent to it. This is done because we will be unable to guarantee that the service is in a valid state, and we want to prevent further spiralling.

## Capture {#capture}

When divergence is detected, a *representation* of the relevant data structures (e.g. inbound request+response, MySQL query, etc.) will be sent to the capture service for subsequent feedback into the migration LLM. The raw data structures may contain PII, so great care must be taken to ensure this process is secure. There are several approaches we can take here, with increasing degrees of both risk and migration efficacy:

1. We will start with a simple structural field difference description: `user.age differed` (for example). This may be sufficient for LLM's to debug and fix the code, with no further detail required.  
2. If the previous approach isn't powerful enough, we could use a self-hosted LLM to describe the differences, with instructions not to leak values, then deterministically enforce that instruction by redacting all values in the data.

## Complications

### Service behaviour

Obviously this is a very simplified view of how services operate in practice and in reality they perform a wide variety of actions that don't cleanly fit into this model. Asynchronous tasks, caches, ORM differences, batching, etc. will all need to be accounted for. The good news is that because we can fully control both the candidate and reference code, and the frameworks, we can gradually collect these patterns and solve them. For example, asynchronous tasks might need to be moved to an external service that triggers via request. This would then fit SPECTRE's approach. For caches we might need to make them more deterministic, or even share caches somehow. Every challenge will need its own solution, but again these can be largely LLM-driven.

### Associating ingress and egress

The high level approach is to rely on OpenTelemetry or another distributed tracing system to propagate trace IDs and match inbound and outbound requests. This does get complicated under some circumstances such as MySQL prepared statements, so we'll need specific approaches for different protocols.

## Life of a Request

![Six lifelines show Client, Mirror Ingress, OLD, NEW, Mirror Egress and Dependency. Egress forwards the OLD write to the dependency. The sandboxed NEW write attempt stops at Egress and receives a replayed result, with no upstream forwarding. Only OLD has a response path through Ingress to Client. NEW responses stop at Ingress for asynchronous comparison, with an explicit barrier before Client.][image2]

## Deployment

SPECTRE is intended to be deployed into Kubernetes. We plan to use a separate deployment, with a single pod and multiple containers. The reason for this approach is to minimise the chances of network failures between the SPECTRE proxies and the reference/candidate services.

![A Kubernetes Service selects ordinary pods and a dedicated Mirror pod. Separate Deployments own ReplicaSets and pods, shown by dashed arrows. Only OLD responses return through Istio and the Service to the client. Within the Mirror pod, ingress compares NEW responses and never returns them. OLD retains normal dependency access through its egress forwarder. NEW is fully network sandboxed, with access only to local Mirror interfaces and a replay branch ending at a no real writes barrier. The Mirror Deployment has one replica with additional resources.][image3]

# References

More details are available in the LLM-driven document [Mirror: Service Migration Through Paired Execution](https://docs.google.com/document/u/0/d/18gvgPck4X1IeqHizDlXOKhWHpkuD9h_PStC22S3RwN8/edit)

[image1]: images/spectre-migration-loop.png

[image2]: images/spectre-request-flow.png

[image3]: images/spectre-deployment.png
