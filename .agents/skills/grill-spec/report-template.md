# Adversarial Review Report Template

Use this template when assembling the findings report in Phase 4.

---

```markdown
# Adversarial Review: [Spec Name]

**Spec reviewed**: [path/to/spec-file.md]
**Review date**: [YYYY-MM-DD]
**Verdict**: [BLOCK | REVISE | PASS]

## Executive Summary

[2-3 sentences. State the total findings by severity and the overall verdict.
Be direct — no softening language.]

| Severity | Count |
|----------|-------|
| CRITICAL | N |
| MAJOR | N |
| MINOR | N |
| OBSERVATION | N |
| **Total** | **N** |

---

## Findings

### CRITICAL Findings

#### [CRIT-001] [Short title]

- **Lens**: [Ambiguity | Incompleteness | Inconsistency | Infeasibility | Insecurity | Inoperability | Incorrectness]
- **Affected section**: [Specific section, requirement ID, scenario name, or quote from spec]
- **Description**: [What is wrong. Be specific. Reference the exact text.]
- **Impact**: [What happens if this ships as-is. Concrete scenario, not abstract risk.]
- **Recommendation**: [Exactly what to change. Provide rewritten text if possible.]

---

#### [CRIT-002] [Short title]

[Same structure as above]

---

### MAJOR Findings

#### [MAJ-001] [Short title]

- **Lens**: [Lens name]
- **Affected section**: [Specific reference]
- **Description**: [What is wrong]
- **Impact**: [Concrete consequence]
- **Recommendation**: [Specific fix]

---

### MINOR Findings

#### [MIN-001] [Short title]

- **Lens**: [Lens name]
- **Affected section**: [Specific reference]
- **Description**: [What is wrong]
- **Recommendation**: [Specific fix]

---

### Observations

#### [OBS-001] [Short title]

- **Lens**: [Lens name]
- **Affected section**: [Specific reference]
- **Suggestion**: [Improvement idea]

---

## Structural Integrity

<!-- Choose ONE of the following variants based on the detected input mode. -->

### Variant A: Plan-Spec Format

<!-- Use this variant when reviewing a spec produced by /plan-spec.
     This is the 9-item pass/fail checklist. -->

| Check | Result | Notes |
|-------|--------|-------|
| Every user story has acceptance scenarios | PASS/FAIL | [Details if FAIL] |
| Every acceptance scenario has BDD scenarios | PASS/FAIL | [Details if FAIL] |
| Every BDD scenario has `Traces to:` reference | PASS/FAIL | [Details if FAIL] |
| Every BDD scenario has a test in TDD plan | PASS/FAIL | [Details if FAIL] |
| Every FR appears in traceability matrix | PASS/FAIL | [Details if FAIL] |
| Every BDD scenario in traceability matrix | PASS/FAIL | [Details if FAIL] |
| Test datasets cover boundaries/edges/errors | PASS/FAIL | [Details if FAIL] |
| Regression impact addressed | PASS/FAIL | [Details if FAIL] |
| Success criteria are measurable | PASS/FAIL | [Details if FAIL] |

### Variant B: Structured Spec

<!-- Use this variant when the spec has formal structure (numbered requirements,
     acceptance criteria) but is not in plan-spec format. Adapt rows to match
     the document's own structural elements. -->

| Check | Result | Notes |
|-------|--------|-------|
| Every goal/objective has acceptance criteria | PASS/FAIL | [Details if FAIL] |
| Cross-references are consistent | PASS/FAIL | [Details if FAIL] |
| Scope boundaries are explicit | PASS/FAIL | [Details if FAIL] |
| Success criteria are measurable | PASS/FAIL | [Details if FAIL] |
| Error/failure scenarios addressed | PASS/FAIL | [Details if FAIL] |
| Dependencies between requirements identified | PASS/FAIL | [Details if FAIL] |

### Variant C: Generic Markdown (Document Completeness Assessment)

<!-- Use this variant when the document is a prose design doc, RFC, ADR,
     or informal spec with no formal structure. Write a narrative assessment
     instead of a pass/fail table. -->

**Scope clarity**: [Assessment — is it clear what the document covers?]

**Actors identified**: [Assessment — are all users, systems, and services named?]

**Success criteria**: [Assessment — how will completion be measured?]

**Failure modes**: [Assessment — are failure scenarios addressed?]

**Implementation detail**: [Assessment — is there enough for an engineer to start?]

**Assumptions & constraints**: [Assessment — are these explicit or implied?]

---

## Test Coverage Assessment

### Missing Test Categories

| Category | Gap Description | Affected Scenarios |
|----------|----------------|-------------------|
| [e.g., Concurrency] | [What's missing] | [Which scenarios need it] |

### Dataset Gaps

| Dataset | Missing Boundary Type | Recommendation |
|---------|----------------------|----------------|
| [e.g., Email Inputs] | [e.g., Unicode local-part] | [Specific test case to add] |

---

## STRIDE Threat Summary

| Component | S | T | R | I | D | E | Notes |
|-----------|---|---|---|---|---|---|-------|
| [Component 1] | [risk/ok] | [risk/ok] | [risk/ok] | [risk/ok] | [risk/ok] | [risk/ok] | [Key concern] |
| [Component 2] | [risk/ok] | [risk/ok] | [risk/ok] | [risk/ok] | [risk/ok] | [risk/ok] | [Key concern] |

**Legend**: risk = identified threat not mitigated in spec, ok = adequately addressed or not applicable

---

## Unasked Questions

These are questions the spec should have answered but did not. The spec author
should address each one before proceeding to implementation.

1. [Question about missing requirement or undecided design choice]
2. [Question about unclear failure handling]
3. [Question about missing integration concern]
4. [...]

---

## Verdict Rationale

[1-2 paragraphs explaining the verdict. Reference the most impactful findings
by ID. State clearly what must change before implementation can proceed.]

### Recommended Next Actions

- [ ] [Specific action item — reference finding ID]
- [ ] [Specific action item — reference finding ID]
- [ ] [Specific action item — reference finding ID]
```
