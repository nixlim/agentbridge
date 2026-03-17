# Adversarial Review Constitution

These principles govern the adversarial review process. The reviewer MUST
evaluate the spec against every applicable principle. When a principle is
violated, it becomes a finding.

## Core Axioms

1. **The spec is wrong until proven right.** Do not extend the benefit of
   the doubt. If something is unclear, it is a defect.
2. **Silence is a bug.** If the spec does not address a concern, that concern
   is unaddressed — not implicitly handled.
3. **Every requirement must be testable.** If you cannot write a test for a
   requirement, the requirement is defective.
4. **Every test must trace to a requirement.** Orphan tests indicate scope
   creep or missing requirements.
5. **Failure is the default.** Assume every external call fails, every input
   is malformed, every user is confused, and every attacker is motivated.

## Lens 1 Principles: Ambiguity

| ID | Principle | Anti-Pattern |
|----|-----------|-------------|
| AMB-01 | Every domain term must be defined exactly once | Using "workload", "service", and "application" interchangeably |
| AMB-02 | Requirements must use RFC 2119 language (MUST/SHOULD/MAY) | "The system will try to..." or "The system handles..." |
| AMB-03 | Numeric thresholds must have explicit units and bounds | "Response time should be fast" without ms/s and percentile |
| AMB-04 | Conditional logic must cover all branches | "If the user is authenticated, show the dashboard" (what about unauthenticated?) |
| AMB-05 | Error messages must specify exact content or format | "Display an appropriate error message" |
| AMB-06 | Time references must be absolute or relative with a defined anchor | "Recently created", "old records", "stale data" |
| AMB-07 | Quantities must be explicit | "Multiple retries", "a few seconds", "several items" |

## Lens 2 Principles: Incompleteness

| ID | Principle | Anti-Pattern |
|----|-----------|-------------|
| INC-01 | Every external dependency must have a failure mode scenario | Assuming the database/API/queue is always available |
| INC-02 | Every user input must have validation rules specified | Accepting user input without defining valid range/format |
| INC-03 | Every state machine must show all transitions, including error states | Happy path only state diagrams |
| INC-04 | Data lifecycle must be complete: create, read, update, delete, archive | Specifying creation but not cleanup/deletion |
| INC-05 | Concurrency model must be specified for shared resources | Assuming single-threaded access to shared state |
| INC-06 | Idempotency requirements must be stated for retryable operations | POST/PUT without duplicate detection |
| INC-07 | Timeout values must be specified for every blocking operation | "Wait for response" without timeout or fallback |
| INC-08 | Pagination must be specified for any list/query operation | Returning unbounded result sets |
| INC-09 | Rate limiting must be specified for any public-facing endpoint | No throttling on API endpoints |
| INC-10 | Migration strategy must be specified for schema/data changes | Adding new fields without specifying existing data handling |

## Lens 3 Principles: Inconsistency

| ID | Principle | Anti-Pattern |
|----|-----------|-------------|
| CON-01 | The same concept must use the same name everywhere | "user ID" in stories, "userId" in BDD, "user_id" in datasets |
| CON-02 | Traceability must be bidirectional with no orphans | Requirements without scenarios, scenarios without tests |
| CON-03 | Priority ordering must be consistent across dependencies | P0 feature depending on P3 prerequisite |
| CON-04 | Data types must be consistent across all references | String in one place, integer in another for the same field |
| CON-05 | Error codes/messages must be consistent across scenarios | Different error messages for the same failure condition |
| CON-06 | Acceptance criteria must not contradict each other | "MUST allow special characters" and "input MUST be alphanumeric" |

## Lens 4 Principles: Infeasibility

| ID | Principle | Anti-Pattern |
|----|-----------|-------------|
| FEA-01 | Requirements must be achievable with the stated tech stack | Requiring real-time guarantees on eventually-consistent systems |
| FEA-02 | Performance targets must be realistic for the architecture | Sub-millisecond response with multiple service hops |
| FEA-03 | Test scenarios must be reproducible in CI/CD | Tests requiring manual setup, specific network conditions, or time-of-day |
| FEA-04 | Success criteria must be measurable with available tooling | Metrics requiring instrumentation that doesn't exist |
| FEA-05 | Ordering guarantees must be achievable in distributed systems | Assuming global ordering without coordination mechanism |

## Lens 5 Principles: Insecurity (STRIDE)

| ID | Principle | Anti-Pattern |
|----|-----------|-------------|
| SEC-01 | Every entry point must specify authentication mechanism | Endpoints without auth requirements |
| SEC-02 | Every operation must specify authorization rules | "Authenticated users can..." without role/permission checks |
| SEC-03 | Every sensitive operation must produce an audit log entry | State changes without audit trail |
| SEC-04 | Error responses must not leak internal details | Stack traces, internal IPs, or database schemas in error messages |
| SEC-05 | All inputs must be validated at the system boundary | Trusting data from external sources |
| SEC-06 | Secrets must never appear in logs, URLs, or error messages | API keys in query parameters, tokens in log output |
| SEC-07 | Data at rest and in transit must specify encryption requirements | Storing PII without encryption specification |
| SEC-08 | Session/token management must specify expiry and revocation | Tokens without TTL or invalidation mechanism |
| SEC-09 | Resource limits must be specified to prevent exhaustion | Unbounded file uploads, unbounded query results, no connection limits |

## Lens 6 Principles: Inoperability

| ID | Principle | Anti-Pattern |
|----|-----------|-------------|
| OPS-01 | Every new component must specify health check endpoints | Services without liveness/readiness probes |
| OPS-02 | Every failure mode must specify an observable indicator | Failures that are silent or only visible in logs |
| OPS-03 | Rollback procedure must be specified or feature-flagged | Big-bang deployments with no rollback plan |
| OPS-04 | Structured logging must include correlation IDs | Log messages without traceId, requestId, or workload identifiers |
| OPS-05 | Alerting thresholds must be specified for key metrics | Monitoring without actionable alerts |
| OPS-06 | Graceful degradation behaviour must be specified | Feature fails completely instead of degrading |
| OPS-07 | Configuration must be externalized, not hardcoded | Magic numbers, embedded URLs, inline credentials |
| OPS-08 | Startup and shutdown behaviour must be specified | No graceful shutdown, no dependency readiness checks |

## Lens 7 Principles: Incorrectness

| ID | Principle | Anti-Pattern |
|----|-----------|-------------|
| COR-01 | Business rules must match source-of-truth documentation | Spec contradicts Jira ticket, Confluence doc, or existing code |
| COR-02 | Boundary values in test data must be mathematically correct | Off-by-one errors in min/max calculations |
| COR-03 | Given preconditions must be achievable from a clean state | BDD scenarios assuming state that no scenario creates |
| COR-04 | Time zone and locale assumptions must be explicit | Assuming UTC, assuming English, assuming Gregorian calendar |
| COR-05 | Existing code behaviour assumed by the spec must be verified | Spec assumes an API returns X but it actually returns Y |
| COR-06 | Race conditions between concurrent operations must be identified | Two users modifying the same resource simultaneously |

## Lens 8 Principles: Overcomplexity

| ID | Principle | Anti-Pattern |
|----|-----------|-------------|
| CPX-01 | Every abstraction must have at least two concrete implementations or a stated reason to exist | Interface with one implementation "for testability" when a concrete type and simple test double would suffice |
| CPX-02 | Configuration options must correspond to values that will realistically change | Externalizing a retry count that has been 3 for five years and nobody has ever changed |
| CPX-03 | The number of architectural layers must be justified by the problem's complexity | Request → Controller → Service → Repository → DAO → Database for a single-table CRUD operation |
| CPX-04 | Requirements must solve the current problem, not hypothetical future ones | "MAY support pluggable storage backends" when the only backend is Azure Blob Storage |
| CPX-05 | Error handling complexity must match error likelihood and impact | Circuit breakers and exponential backoff for an internal synchronous call that fails once a year |
| CPX-06 | Test infrastructure must not exceed the complexity of the code under test | Test factories, builders, and fixtures more complex than the production code they test |
| CPX-07 | The simplest solution that satisfies all stated requirements is the correct one | Introducing an event-driven architecture when a direct function call achieves the same result |
| CPX-08 | Feature flags, toggles, and gradual rollout mechanisms must justify their maintenance cost | Feature flag for a feature that will never be toggled off after initial release |
| CPX-09 | New concepts (types, services, tables, queues) must each solve a distinct stated problem | Creating a dedicated microservice for logic that belongs in an existing module |
| CPX-10 | Performance optimizations must target measured bottlenecks, not theoretical ones | Adding caching, connection pooling, or async processing without evidence of a performance problem |

## Review Completeness Check

Before finalising the review, verify:

- [ ] Every lens has been applied (or explicitly marked as not applicable with justification)
- [ ] Every finding has a specific section reference from the spec
- [ ] Every finding has a concrete, actionable recommendation
- [ ] Findings are classified by severity (CRITICAL, MAJOR, MINOR, OBSERVATION)
- [ ] No false reassurance language appears in the report
- [ ] The STRIDE analysis covers every component/data flow in the spec
- [ ] The unasked questions section identifies genuine gaps, not rhetorical questions
