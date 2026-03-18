package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStructuredFindings_JSONBlock(t *testing.T) {
	input := `Here is my review:

` + "```json" + `
{
  "findings": [
    {
      "severity": "CRITICAL",
      "affected_section": "Authentication",
      "description": "No rate limiting on login endpoint",
      "recommendation": "Add rate limiting middleware"
    },
    {
      "severity": "MINOR",
      "affected_section": "Logging",
      "description": "Missing structured logging in auth flow",
      "recommendation": ""
    }
  ]
}
` + "```" + `

That covers my concerns.`

	findings := ParseStructuredFindings(input, "reviewer-1", 1)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}

	f0 := findings[0]
	if f0.Severity != SeverityCritical {
		t.Errorf("expected CRITICAL, got %s", f0.Severity)
	}
	if f0.AffectedSection != "Authentication" {
		t.Errorf("expected Authentication, got %s", f0.AffectedSection)
	}
	if f0.Status != FindingRaised {
		t.Errorf("expected raised, got %s", f0.Status)
	}
	if f0.Round != 1 {
		t.Errorf("expected round 1, got %d", f0.Round)
	}
	if len(f0.Source) != 1 || f0.Source[0] != "reviewer-1" {
		t.Errorf("expected source [reviewer-1], got %v", f0.Source)
	}

	f1 := findings[1]
	if f1.Severity != SeverityMinor {
		t.Errorf("expected MINOR, got %s", f1.Severity)
	}
}

func TestParseStructuredFindings_FreeTextFallback(t *testing.T) {
	input := `## BLOCKERS
- No authentication on the admin endpoint
- Missing input validation on user creation

## AMBIGUITIES
- Unclear whether soft-delete is required
`

	findings := ParseStructuredFindings(input, "reviewer-2", 2)
	if len(findings) != 3 {
		t.Fatalf("expected 3 findings, got %d", len(findings))
	}

	// BLOCKERS should be CRITICAL
	for _, f := range findings[:2] {
		if f.Severity != SeverityCritical {
			t.Errorf("BLOCKERS item should be CRITICAL, got %s for %q", f.Severity, f.Description)
		}
		if f.AffectedSection != "BLOCKERS" {
			t.Errorf("expected section BLOCKERS, got %s", f.AffectedSection)
		}
	}

	// AMBIGUITIES should be MAJOR
	if findings[2].Severity != SeverityMajor {
		t.Errorf("AMBIGUITIES item should be MAJOR, got %s", findings[2].Severity)
	}
	if findings[2].Round != 2 {
		t.Errorf("expected round 2, got %d", findings[2].Round)
	}
}

func TestMergeFindings_Deduplication(t *testing.T) {
	existing := []*Finding{
		{
			Severity:        SeverityMajor,
			AffectedSection: "Authentication",
			Description:     "No rate limiting",
			Source:          []string{"reviewer-1"},
			Round:           1,
			Status:          FindingRaised,
		},
	}
	newFindings := []*Finding{
		{
			Severity:        SeverityMajor,
			AffectedSection: "Authentication",
			Description:     "No rate limiting on login endpoint", // contains "No rate limiting"
			Source:          []string{"reviewer-2"},
			Round:           1,
			Status:          FindingRaised,
		},
	}

	merged := MergeFindings(existing, newFindings)
	if len(merged) != 1 {
		t.Fatalf("expected 1 finding after dedup, got %d", len(merged))
	}
}

func TestMergeFindings_KeepsHigherSeverity(t *testing.T) {
	existing := []*Finding{
		{
			Severity:        SeverityMinor,
			AffectedSection: "Auth",
			Description:     "Missing rate limit",
			Source:          []string{"reviewer-1"},
			Status:          FindingRaised,
		},
	}
	newFindings := []*Finding{
		{
			Severity:        SeverityCritical,
			AffectedSection: "Auth",
			Description:     "Missing rate limit",
			Source:          []string{"reviewer-2"},
			Status:          FindingRaised,
		},
	}

	merged := MergeFindings(existing, newFindings)
	if len(merged) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(merged))
	}
	if merged[0].Severity != SeverityCritical {
		t.Errorf("expected CRITICAL (higher severity kept), got %s", merged[0].Severity)
	}
}

func TestMergeFindings_MergesSources(t *testing.T) {
	existing := []*Finding{
		{
			Severity:        SeverityMajor,
			AffectedSection: "API",
			Description:     "No pagination",
			Source:          []string{"reviewer-1"},
			Status:          FindingRaised,
		},
	}
	newFindings := []*Finding{
		{
			Severity:        SeverityMajor,
			AffectedSection: "API",
			Description:     "No pagination on list endpoints",
			Source:          []string{"reviewer-2", "reviewer-3"},
			Status:          FindingRaised,
		},
	}

	merged := MergeFindings(existing, newFindings)
	if len(merged) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(merged))
	}
	sources := merged[0].Source
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources, got %d: %v", len(sources), sources)
	}
	expected := map[string]bool{"reviewer-1": true, "reviewer-2": true, "reviewer-3": true}
	for _, s := range sources {
		if !expected[s] {
			t.Errorf("unexpected source %q", s)
		}
	}
}

func TestAssignFindingIDs(t *testing.T) {
	findings := []*Finding{
		{Severity: SeverityCritical},
		{Severity: SeverityCritical},
		{Severity: SeverityMajor},
		{Severity: SeverityMinor},
		{Severity: SeverityObservation},
	}

	AssignFindingIDs(findings)

	expected := []string{"CRIT-001", "CRIT-002", "MAJ-001", "MIN-001", "OBS-001"}
	for i, f := range findings {
		if f.ID != expected[i] {
			t.Errorf("findings[%d]: expected ID %q, got %q", i, expected[i], f.ID)
		}
	}
}

func TestTransitionFinding_ValidTransitions(t *testing.T) {
	tests := []struct {
		from   FindingStatus
		to     FindingStatus
		reason string
	}{
		{FindingRaised, FindingAddressed, "fix applied"},
		{FindingRaised, FindingDismissed, "DUPLICATE"},
		{FindingRaised, FindingClosed, "resolved"},
		{FindingAddressed, FindingClosed, "verified"},
		{FindingAddressed, FindingRaised, "reopened"},
		{FindingDismissed, FindingRaised, "reconsidered"},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"->"+string(tt.to), func(t *testing.T) {
			f := &Finding{ID: "TEST-001", Status: tt.from}
			err := TransitionFinding(f, tt.to, tt.reason, 1)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if f.Status != tt.to {
				t.Errorf("expected status %s, got %s", tt.to, f.Status)
			}
			if len(f.StatusHistory) != 1 {
				t.Fatalf("expected 1 history entry, got %d", len(f.StatusHistory))
			}
			if f.StatusHistory[0].From != tt.from || f.StatusHistory[0].To != tt.to {
				t.Errorf("history mismatch: %+v", f.StatusHistory[0])
			}
		})
	}
}

func TestTransitionFinding_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name   string
		from   FindingStatus
		to     FindingStatus
		reason string
	}{
		{"closed is terminal", FindingClosed, FindingRaised, "reopen"},
		{"same status", FindingRaised, FindingRaised, "no-op"},
		{"dismissed needs valid reason", FindingRaised, FindingDismissed, "bad-reason"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Finding{ID: "TEST-001", Status: tt.from}
			err := TransitionFinding(f, tt.to, tt.reason, 1)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestFormatFindingsForPrompt(t *testing.T) {
	findings := []*Finding{
		{
			ID:              "CRIT-001",
			Severity:        SeverityCritical,
			Status:          FindingRaised,
			AffectedSection: "Auth",
			Description:     "No rate limiting",
			Recommendation:  "Add middleware",
			Source:          []string{"reviewer-1"},
		},
		{
			ID:              "MAJ-001",
			Severity:        SeverityMajor,
			Status:          FindingAddressed,
			AffectedSection: "API",
			Description:     "Missing pagination",
			Source:          []string{"reviewer-1", "reviewer-2"},
		},
	}

	output := FormatFindingsForPrompt(findings)

	if !strings.Contains(output, "## Review Findings") {
		t.Error("missing header")
	}
	if !strings.Contains(output, "### CRITICAL") {
		t.Error("missing CRITICAL section")
	}
	if !strings.Contains(output, "### MAJOR") {
		t.Error("missing MAJOR section")
	}
	if !strings.Contains(output, "[CRIT-001]") {
		t.Error("missing finding ID CRIT-001")
	}
	if !strings.Contains(output, "Recommendation: Add middleware") {
		t.Error("missing recommendation")
	}
	if !strings.Contains(output, "reviewer-1, reviewer-2") {
		t.Error("missing merged sources")
	}
}

func TestFormatFindingsForPrompt_Empty(t *testing.T) {
	output := FormatFindingsForPrompt(nil)
	if output != "No findings recorded." {
		t.Errorf("expected 'No findings recorded.', got %q", output)
	}
}

func TestWriteAndReadFindingsLedger(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "findings-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ws := &Workspace{path: tmpDir}

	ledger := &FindingsLedger{
		GoalID: "goal-123",
		Round:  1,
		Findings: []*Finding{
			{
				ID:              "CRIT-001",
				Severity:        SeverityCritical,
				Status:          FindingRaised,
				AffectedSection: "Auth",
				Description:     "No rate limiting",
				Source:          []string{"reviewer-1"},
				Round:           1,
			},
		},
	}

	if err := WriteFindingsLedger(ws, "my-goal", 1, ledger); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Verify the file exists at the expected path.
	expectedPath := filepath.Join(tmpDir, "discussions", "my-goal", "round-01", "merged-findings-round-01.json")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected file at %s: %v", expectedPath, err)
	}

	// Read it back.
	got, err := ReadFindingsLedger(ws, "my-goal", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if got.GoalID != "goal-123" {
		t.Errorf("expected goal_id goal-123, got %s", got.GoalID)
	}
	if len(got.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(got.Findings))
	}
	if got.Findings[0].ID != "CRIT-001" {
		t.Errorf("expected ID CRIT-001, got %s", got.Findings[0].ID)
	}
}

func TestValidateDismissalReason(t *testing.T) {
	valid := []string{"DUPLICATE", "OUT_OF_SCOPE", "CONTRADICTED_BY_REQUIREMENT", "REVIEWER_ERROR"}
	for _, r := range valid {
		if !ValidateDismissalReason(r) {
			t.Errorf("expected %q to be valid", r)
		}
	}
	invalid := []string{"", "INVALID", "duplicate", "other"}
	for _, r := range invalid {
		if ValidateDismissalReason(r) {
			t.Errorf("expected %q to be invalid", r)
		}
	}
}

func TestSeverityRank(t *testing.T) {
	if SeverityRank(SeverityCritical) != 0 {
		t.Error("CRITICAL should be 0")
	}
	if SeverityRank(SeverityMajor) != 1 {
		t.Error("MAJOR should be 1")
	}
	if SeverityRank(SeverityMinor) != 2 {
		t.Error("MINOR should be 2")
	}
	if SeverityRank(SeverityObservation) != 3 {
		t.Error("OBSERVATION should be 3")
	}
	if SeverityRank(SeverityCritical) >= SeverityRank(SeverityMajor) {
		t.Error("CRITICAL should rank lower (more severe) than MAJOR")
	}
}

func TestParseStructuredFindings_InvalidJSON(t *testing.T) {
	// Invalid JSON should fall through to free-text parsing (which also finds nothing).
	input := "```json\n{broken json\n```\n\nSome text with no sections."
	findings := ParseStructuredFindings(input, "test", 1)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings from invalid input, got %d", len(findings))
	}
}

func TestFindingJSONRoundTrip(t *testing.T) {
	f := &Finding{
		ID:              "CRIT-001",
		Severity:        SeverityCritical,
		Status:          FindingRaised,
		AffectedSection: "Auth",
		Description:     "Test finding",
		Source:          []string{"r1"},
		Round:           1,
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var f2 Finding
	if err := json.Unmarshal(data, &f2); err != nil {
		t.Fatal(err)
	}
	if f2.ID != f.ID || f2.Severity != f.Severity || f2.Status != f.Status {
		t.Errorf("round-trip mismatch: %+v vs %+v", f, f2)
	}
}
