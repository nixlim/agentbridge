# Adversarial Review: Password Reset

**Spec reviewed**: password-reset-spec.md
**Review date**: 2026-02-17
**Verdict**: REVISE

## Executive Summary

The password reset specification covers the core happy path adequately but has
significant gaps in failure handling, security hardening, and design complexity.
3 major findings require revision before implementation. No critical
production-incident-level defects were found.

| Severity | Count |
|----------|-------|
| CRITICAL | 0 |
| MAJOR | 3 |
| MINOR | 4 |
| OBSERVATION | 3 |
| **Total** | **10** |

---

## Findings

### MAJOR Findings

#### [MAJ-001] Reset token brute-force not mitigated

- **Lens**: Insecurity
- **Affected section**: User Story 1, Acceptance Scenario 3; FR-004
- **Description**: The spec states "System MUST generate a unique, time-limited
  reset token" (FR-004) but does not specify rate limiting on the reset token
  verification endpoint. An attacker could brute-force short tokens by
  submitting many verification attempts.
- **Impact**: If tokens are 6-digit numeric codes, an attacker can enumerate
  all 1M possibilities in minutes. Account takeover becomes trivial.
- **Recommendation**: Add FR-011: "System MUST rate-limit reset token
  verification to a maximum of 5 attempts per token per 15-minute window.
  After 5 failed attempts, the token MUST be invalidated and the user MUST
  request a new one." Add a corresponding BDD Error Path scenario and test
  dataset row.

---

#### [MAJ-002] No specification for concurrent reset requests

- **Lens**: Incompleteness
- **Affected section**: User Story 1 — no concurrency scenario exists
- **Description**: The spec does not address what happens when a user requests
  multiple password resets before completing any of them. Questions unanswered:
  Does a new request invalidate previous tokens? Can multiple tokens be active
  simultaneously? What happens if the user completes an older token after
  requesting a newer one?
- **Impact**: If multiple tokens remain active, the attack surface multiplies.
  If only the latest is valid, users who receive emails out-of-order will see
  confusing errors.
- **Recommendation**: Add to Edge Cases: "When a user requests a new reset
  while a previous token is still active, the previous token MUST be
  invalidated immediately. Only the most recently issued token is valid."
  Add BDD scenario: "Scenario: Using an invalidated token after requesting a
  new reset — Category: Error Path."

---

#### [MAJ-003] Overengineered token generation architecture

- **Lens**: Overcomplexity
- **Affected section**: User Story 1, FR-004, TDD Plan — Test Hierarchy
- **Description**: The spec introduces a `TokenGeneratorInterface` with a
  `TokenGeneratorFactory` to "allow pluggable token generation strategies."
  The TDD plan includes 4 unit tests for the factory pattern and 2 for a
  `TokenGeneratorRegistry`. The only implementation is `CryptoRandomTokenGenerator`.
  This is a single-implementation interface with a factory — textbook premature
  abstraction (CPX-01). The factory and registry add 3 new types and ~6 tests
  for machinery that produces no user-facing value.
- **Impact**: Implementing this spec as-is produces ~120 lines of boilerplate
  (interface, factory, registry, tests for each) wrapping what could be a
  single `generateResetToken()` function. Future developers must understand
  the indirection to make any change to token generation. The abstraction
  creates maintenance burden without enabling any stated requirement.
- **Recommendation**: Remove `TokenGeneratorInterface`, `TokenGeneratorFactory`,
  and `TokenGeneratorRegistry` from the spec. Replace with FR-004: "System
  MUST generate a cryptographically random, URL-safe reset token of at least
  128 bits of entropy." The TDD plan should have one unit test for token
  generation (correct length, character set, uniqueness over N iterations) —
  not six tests for abstraction plumbing. If a second token strategy is ever
  needed, the interface can be extracted at that point in under 30 minutes.

---

### MINOR Findings

#### [MIN-001] Ambiguous "appropriate error message" in Scenario 5

- **Lens**: Ambiguity
- **Affected section**: BDD Scenario 5 — "Then an appropriate error is displayed"
- **Description**: The Then step uses subjective language. "Appropriate" is not
  defined. One engineer might show "Invalid token", another might show
  "This link has expired, please request a new one."
- **Recommendation**: Replace with: "Then the system displays the message
  'This reset link has expired. Please request a new password reset.'"

---

#### [MIN-002] Test dataset missing Unicode email addresses

- **Lens**: Incompleteness
- **Affected section**: Test Dataset 1 — Email Inputs
- **Description**: The email input dataset tests ASCII-only addresses. RFC 6531
  permits internationalized email addresses (e.g., `user@例え.jp`). The spec
  does not state whether these are supported or rejected.
- **Recommendation**: Add a decision to the spec: either add test rows for
  internationalized emails (if supported) or add FR-012: "System MAY reject
  non-ASCII email addresses with error 'Please enter an email address using
  standard characters.'"

---

#### [MIN-003] Missing `Traces to:` on Scenario Outline Examples

- **Lens**: Inconsistency
- **Affected section**: BDD Scenario Outline — "Password strength validation"
- **Description**: The Scenario Outline header has a `Traces to:` reference
  but individual rows in the Examples table do not indicate which acceptance
  criteria they exercise. Row 3 ("password" — weak) and Row 7 ("P@ss1" — too
  short) test different rules but both trace to the same scenario.
- **Recommendation**: Add a `Validates rule` column to the Examples table to
  clarify which password policy rule each row exercises (minimum length,
  complexity, common password blocklist).

---

#### [MIN-004] Success criteria SC-003 is not measurable

- **Lens**: Infeasibility
- **Affected section**: SC-003 — "Password reset emails are delivered promptly"
- **Description**: "Promptly" is subjective. No threshold is defined.
- **Recommendation**: Replace with: "SC-003: 95% of password reset emails
  MUST be accepted by the outbound mail relay within 5 seconds of the reset
  request. Measured via structured log timestamp delta."

---

### Observations

#### [OBS-001] Consider adding a reset audit log

- **Lens**: Inoperability
- **Affected section**: General — no audit logging specified
- **Suggestion**: For compliance and incident investigation, consider adding
  FR-013: "System SHOULD log all password reset requests and completions to
  the audit log with userId, timestamp, IP address, and outcome (success/failure/
  expired)." This supports SOC investigations and abuse detection.

---

#### [OBS-002] No mention of email deliverability monitoring

- **Lens**: Inoperability
- **Affected section**: User Story 1 — email sending
- **Suggestion**: Password reset is a critical user-facing flow. Consider
  adding a success criterion for monitoring: "SC-007: Email delivery failures
  MUST trigger an alert to the operations team within 5 minutes." Without this,
  a misconfigured mail relay could silently block all resets.

---

#### [OBS-003] Test dataset could include timing-based edge cases

- **Lens**: Incompleteness
- **Affected section**: Test Dataset 3 — Reset Token Inputs
- **Suggestion**: Add rows for tokens submitted at exactly the expiry boundary
  (e.g., token expires at T+30m, submitted at T+29m59s and T+30m01s). This
  catches off-by-one errors in timestamp comparison. Also consider clock skew
  between services.

---

## Structural Integrity

| Check | Result | Notes |
|-------|--------|-------|
| Every user story has acceptance scenarios | PASS | |
| Every acceptance scenario has BDD scenarios | PASS | |
| Every BDD scenario has `Traces to:` reference | PASS | |
| Every BDD scenario has a test in TDD plan | PASS | |
| Every FR appears in traceability matrix | PASS | |
| Every BDD scenario in traceability matrix | PASS | |
| Test datasets cover boundaries/edges/errors | FAIL | Missing Unicode emails (MIN-002), timing boundaries (OBS-003) |
| Regression impact addressed | PASS | "No regression impact — new capability" stated |
| Success criteria are measurable | FAIL | SC-003 uses "promptly" without threshold (MIN-004) |

---

## Test Coverage Assessment

### Missing Test Categories

| Category | Gap Description | Affected Scenarios |
|----------|----------------|-------------------|
| Concurrency | No test for simultaneous reset requests | MAJ-002 |
| Rate limiting | No test for brute-force token attempts | MAJ-001 |
| Timing boundaries | No test for exact-expiry-moment tokens | OBS-003 |

### Dataset Gaps

| Dataset | Missing Boundary Type | Recommendation |
|---------|----------------------|----------------|
| Email Inputs | Unicode/internationalized addresses | Add RFC 6531 test cases or explicit rejection rule |
| Reset Tokens | Exact expiry boundary (T-1s, T+0, T+1s) | Add timing precision test rows |
| Password Inputs | Very long passwords (10,000+ chars) | Add DoS-prevention boundary test |

---

## STRIDE Threat Summary

| Component | S | T | R | I | D | E | Notes |
|-----------|---|---|---|---|---|---|-------|
| Reset request endpoint | ok | ok | risk | ok | risk | ok | No audit log (R), no rate limit on requests (D) |
| Token verification endpoint | ok | ok | ok | ok | risk | ok | No rate limit on attempts (D) — MAJ-001 |
| Email delivery | risk | ok | ok | risk | ok | ok | No SPF/DKIM mentioned (S), email content could leak reset URL to logs (I) |
| Password update endpoint | ok | ok | ok | ok | ok | ok | Adequately specified |

---

## Unasked Questions

1. What happens if the email service is temporarily unavailable? Does the user
   see an error immediately, or does the system queue and retry?
2. Should the reset token be single-use? (The spec says time-limited but does
   not address whether a token can be used twice within its validity window.)
3. Is there a cooldown between reset requests from the same email address?
   Without one, an attacker can spam a user's inbox with reset emails.
4. Are reset emails sent in the user's preferred language/locale, or always
   in English?
5. What is the token format? Numeric, alphanumeric, UUID? This affects
   brute-force resistance (MAJ-001).

---

## Verdict Rationale

The spec is structurally sound — traceability is complete and BDD scenarios
cover the core flows. However, three major findings must be addressed before
implementation: security (MAJ-001: token brute-force), completeness (MAJ-002:
concurrent resets), and overcomplexity (MAJ-003: unnecessary abstraction layers
in token generation). The first two are straightforward additions; the third
requires removing spec content — stripping out the factory/registry/interface
pattern in favour of a direct implementation.

The four minor findings are quality improvements that strengthen the spec but
do not block implementation. The three observations are suggested enhancements
for operational maturity.

### Recommended Next Actions

- [ ] Address MAJ-001: Add token verification rate limiting requirement, BDD scenario, and test data
- [ ] Address MAJ-002: Specify concurrent reset request behaviour, add BDD scenario
- [ ] Address MAJ-003: Remove TokenGeneratorInterface/Factory/Registry; simplify to a single function with one unit test
- [ ] Fix MIN-004: Replace "promptly" in SC-003 with measurable threshold
- [ ] Fix MIN-001: Replace "appropriate error" with exact error message text
- [ ] Consider MIN-002: Decide on internationalized email support
- [ ] Answer the 5 unasked questions and encode decisions into the spec
