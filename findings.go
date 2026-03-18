package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// FindingSeverity indicates how serious a review finding is.
type FindingSeverity string

const (
	SeverityCritical    FindingSeverity = "CRITICAL"
	SeverityMajor       FindingSeverity = "MAJOR"
	SeverityMinor       FindingSeverity = "MINOR"
	SeverityObservation FindingSeverity = "OBSERVATION"
)

// FindingStatus tracks the lifecycle of a finding.
type FindingStatus string

const (
	FindingRaised    FindingStatus = "raised"
	FindingAddressed FindingStatus = "addressed"
	FindingClosed    FindingStatus = "closed"
	FindingDismissed FindingStatus = "dismissed"
)

// DismissalReason constrains why a finding may be dismissed.
type DismissalReason string

const (
	DismissalDuplicate                DismissalReason = "DUPLICATE"
	DismissalOutOfScope               DismissalReason = "OUT_OF_SCOPE"
	DismissalContradictedByRequirement DismissalReason = "CONTRADICTED_BY_REQUIREMENT"
	DismissalReviewerError            DismissalReason = "REVIEWER_ERROR"
)

var validDismissalReasons = map[string]bool{
	string(DismissalDuplicate):                true,
	string(DismissalOutOfScope):               true,
	string(DismissalContradictedByRequirement): true,
	string(DismissalReviewerError):            true,
}

// Finding represents a single review finding with audit trail.
type Finding struct {
	ID              string          `json:"id"`
	Severity        FindingSeverity `json:"severity"`
	Status          FindingStatus   `json:"status"`
	AffectedSection string          `json:"affected_section"`
	Description     string          `json:"description"`
	Recommendation  string          `json:"recommendation,omitempty"`
	Source          []string        `json:"source"`
	Round           int             `json:"round"`
	StatusHistory   []StatusChange  `json:"status_history,omitempty"`
}

// StatusChange records a transition in a finding's lifecycle.
type StatusChange struct {
	From      FindingStatus `json:"from"`
	To        FindingStatus `json:"to"`
	Reason    string        `json:"reason,omitempty"`
	Round     int           `json:"round"`
	Timestamp time.Time     `json:"timestamp"`
}

// FindingsLedger holds all findings for a goal at a particular round.
type FindingsLedger struct {
	GoalID   string     `json:"goal_id"`
	Round    int        `json:"round"`
	Findings []*Finding `json:"findings"`
}

// SeverityRank returns a numeric rank for severity comparison.
// Lower values are more severe: CRITICAL=0, MAJOR=1, MINOR=2, OBSERVATION=3.
func SeverityRank(s FindingSeverity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityMajor:
		return 1
	case SeverityMinor:
		return 2
	case SeverityObservation:
		return 3
	default:
		return 4
	}
}

// severityPrefix returns the ID prefix for a given severity.
func severityPrefix(s FindingSeverity) string {
	switch s {
	case SeverityCritical:
		return "CRIT"
	case SeverityMajor:
		return "MAJ"
	case SeverityMinor:
		return "MIN"
	case SeverityObservation:
		return "OBS"
	default:
		return "UNK"
	}
}

// ValidateDismissalReason checks whether a dismissal reason is in the allowed set.
func ValidateDismissalReason(reason string) bool {
	return validDismissalReasons[reason]
}

// validTransitions defines which status transitions are allowed.
var validTransitions = map[FindingStatus]map[FindingStatus]bool{
	FindingRaised: {
		FindingAddressed: true,
		FindingDismissed: true,
		FindingClosed:    true,
	},
	FindingAddressed: {
		FindingClosed: true,
		FindingRaised: true, // reopen
	},
	FindingDismissed: {
		FindingRaised: true, // reopen
	},
	// FindingClosed is terminal.
}

// TransitionFinding validates and applies a status transition with an audit trail entry.
func TransitionFinding(f *Finding, to FindingStatus, reason string, round int) error {
	if f.Status == to {
		return fmt.Errorf("finding %s is already in status %q", f.ID, to)
	}
	allowed, ok := validTransitions[f.Status]
	if !ok || !allowed[to] {
		return fmt.Errorf("invalid transition for finding %s: %s -> %s", f.ID, f.Status, to)
	}
	if to == FindingDismissed && !ValidateDismissalReason(reason) {
		return fmt.Errorf("dismissal of finding %s requires a valid reason, got %q", f.ID, reason)
	}
	change := StatusChange{
		From:      f.Status,
		To:        to,
		Reason:    reason,
		Round:     round,
		Timestamp: time.Now(),
	}
	f.StatusHistory = append(f.StatusHistory, change)
	f.Status = to
	return nil
}

// jsonFindingsEnvelope is the expected shape of structured JSON findings from reviewers.
type jsonFindingsEnvelope struct {
	Findings []jsonFinding `json:"findings"`
}

type jsonFinding struct {
	Severity        string `json:"severity"`
	AffectedSection string `json:"affected_section"`
	Description     string `json:"description"`
	Recommendation  string `json:"recommendation"`
}

var jsonBlockRe = regexp.MustCompile("(?s)```json\\s*\\n(.*?)\\n\\s*```")

// ParseStructuredFindings extracts findings from reviewer output.
// It first looks for a ```json block containing a "findings" array.
// If none is found, it falls back to parsing free-text sections.
func ParseStructuredFindings(reviewOutput string, source string, round int) []*Finding {
	if findings := parseJSONFindings(reviewOutput, source, round); len(findings) > 0 {
		return findings
	}
	return parseFreeTextFindings(reviewOutput, source, round)
}

func parseJSONFindings(reviewOutput string, source string, round int) []*Finding {
	matches := jsonBlockRe.FindAllStringSubmatch(reviewOutput, -1)
	for _, m := range matches {
		var envelope jsonFindingsEnvelope
		if err := json.Unmarshal([]byte(m[1]), &envelope); err != nil {
			continue
		}
		if len(envelope.Findings) == 0 {
			continue
		}
		var findings []*Finding
		for _, jf := range envelope.Findings {
			sev := normalizeSeverity(jf.Severity)
			findings = append(findings, &Finding{
				Severity:        sev,
				Status:          FindingRaised,
				AffectedSection: jf.AffectedSection,
				Description:     jf.Description,
				Recommendation:  jf.Recommendation,
				Source:          []string{source},
				Round:           round,
			})
		}
		return findings
	}
	return nil
}

func normalizeSeverity(s string) FindingSeverity {
	upper := strings.ToUpper(strings.TrimSpace(s))
	switch upper {
	case "CRITICAL":
		return SeverityCritical
	case "MAJOR":
		return SeverityMajor
	case "MINOR":
		return SeverityMinor
	case "OBSERVATION":
		return SeverityObservation
	default:
		return SeverityMinor
	}
}

// freeTextSectionSeverity maps known section headings to severities.
var freeTextSectionSeverity = map[string]FindingSeverity{
	"BLOCKERS":       SeverityCritical,
	"BLOCKER":        SeverityCritical,
	"AMBIGUITIES":    SeverityMajor,
	"AMBIGUITY":      SeverityMajor,
	"SUGGESTIONS":    SeverityMinor,
	"SUGGESTION":     SeverityMinor,
	"OBSERVATIONS":   SeverityObservation,
	"OBSERVATION":    SeverityObservation,
	"NOTES":          SeverityObservation,
	"CONCERNS":       SeverityMajor,
	"ISSUES":         SeverityMajor,
	"RECOMMENDATIONS": SeverityMinor,
}

// sectionHeaderRe matches lines like "## BLOCKERS" or "**BLOCKERS**" or "BLOCKERS:" at start of line.
var sectionHeaderRe = regexp.MustCompile(`(?m)^(?:#{1,4}\s+|\*{2})?(BLOCKERS?|AMBIGUIT(?:Y|IES)|SUGGESTIONS?|OBSERVATIONS?|NOTES|CONCERNS|ISSUES|RECOMMENDATIONS)\b[*:]*\s*$`)

// bulletRe matches lines starting with - or * (list items).
var bulletRe = regexp.MustCompile(`(?m)^\s*[-*]\s+(.+)`)

func parseFreeTextFindings(reviewOutput string, source string, round int) []*Finding {
	var findings []*Finding

	// Find all section headers and their positions.
	headerMatches := sectionHeaderRe.FindAllStringSubmatchIndex(reviewOutput, -1)
	if len(headerMatches) == 0 {
		return nil
	}

	for i, hm := range headerMatches {
		sectionName := strings.ToUpper(strings.Trim(reviewOutput[hm[2]:hm[3]], "*#: "))
		severity, ok := freeTextSectionSeverity[sectionName]
		if !ok {
			severity = SeverityMinor
		}

		// Determine the section body: from end of header line to start of next header or end of text.
		bodyStart := hm[1]
		var bodyEnd int
		if i+1 < len(headerMatches) {
			bodyEnd = headerMatches[i+1][0]
		} else {
			bodyEnd = len(reviewOutput)
		}
		body := reviewOutput[bodyStart:bodyEnd]

		// Extract bullet items from the section body.
		bullets := bulletRe.FindAllStringSubmatch(body, -1)
		for _, b := range bullets {
			text := strings.TrimSpace(b[1])
			if text == "" {
				continue
			}
			findings = append(findings, &Finding{
				Severity:        severity,
				Status:          FindingRaised,
				AffectedSection: sectionName,
				Description:     text,
				Source:          []string{source},
				Round:           round,
			})
		}
	}

	return findings
}

// MergeFindings deduplicates and merges new findings into an existing list.
// Duplicates are identified by normalized affected_section plus substring match on description.
// When duplicates are found, the higher severity is kept and source lists are merged.
// After merging, stable IDs are reassigned.
func MergeFindings(existing []*Finding, newFindings []*Finding) []*Finding {
	merged := make([]*Finding, len(existing))
	for i, f := range existing {
		cp := *f
		cp.Source = append([]string(nil), f.Source...)
		if f.StatusHistory != nil {
			cp.StatusHistory = append([]StatusChange(nil), f.StatusHistory...)
		}
		merged[i] = &cp
	}

	for _, nf := range newFindings {
		if idx := findDuplicate(merged, nf); idx >= 0 {
			ef := merged[idx]
			// Keep higher severity (lower rank).
			if SeverityRank(nf.Severity) < SeverityRank(ef.Severity) {
				ef.Severity = nf.Severity
			}
			// Merge sources.
			ef.Source = mergeSourceLists(ef.Source, nf.Source)
			// Keep the more detailed description if the new one is longer.
			if len(nf.Description) > len(ef.Description) {
				ef.Description = nf.Description
			}
		} else {
			cp := *nf
			cp.Source = append([]string(nil), nf.Source...)
			merged = append(merged, &cp)
		}
	}

	sortFindings(merged)
	AssignFindingIDs(merged)
	return merged
}

func findDuplicate(findings []*Finding, candidate *Finding) int {
	normSection := normalizeSection(candidate.AffectedSection)
	normDesc := strings.ToLower(strings.TrimSpace(candidate.Description))

	for i, f := range findings {
		if normalizeSection(f.AffectedSection) != normSection {
			continue
		}
		existingDesc := strings.ToLower(strings.TrimSpace(f.Description))
		// Substring match in either direction.
		if strings.Contains(existingDesc, normDesc) || strings.Contains(normDesc, existingDesc) {
			return i
		}
	}
	return -1
}

func normalizeSection(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func mergeSourceLists(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	for _, s := range a {
		seen[s] = true
	}
	merged := append([]string(nil), a...)
	for _, s := range b {
		if !seen[s] {
			merged = append(merged, s)
			seen[s] = true
		}
	}
	return merged
}

func sortFindings(findings []*Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := SeverityRank(findings[i].Severity), SeverityRank(findings[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(findings[i].AffectedSection) < strings.ToLower(findings[j].AffectedSection)
	})
}

// AssignFindingIDs assigns sequential IDs grouped by severity prefix.
// E.g. CRIT-001, CRIT-002, MAJ-001, MIN-001, OBS-001.
func AssignFindingIDs(findings []*Finding) {
	counters := map[FindingSeverity]int{}
	for _, f := range findings {
		counters[f.Severity]++
		f.ID = fmt.Sprintf("%s-%03d", severityPrefix(f.Severity), counters[f.Severity])
	}
}

// FormatFindingsForPrompt formats findings as human-readable text for injection into agent prompts.
func FormatFindingsForPrompt(findings []*Finding) string {
	if len(findings) == 0 {
		return "No findings recorded."
	}

	var buf strings.Builder
	buf.WriteString("## Review Findings\n\n")

	currentSeverity := FindingSeverity("")
	for _, f := range findings {
		if f.Severity != currentSeverity {
			currentSeverity = f.Severity
			buf.WriteString(fmt.Sprintf("### %s\n\n", currentSeverity))
		}
		buf.WriteString(fmt.Sprintf("- **[%s]** (%s) %s — %s", f.ID, f.Status, f.AffectedSection, f.Description))
		if f.Recommendation != "" {
			buf.WriteString(fmt.Sprintf("\n  Recommendation: %s", f.Recommendation))
		}
		if len(f.Source) > 0 {
			buf.WriteString(fmt.Sprintf("\n  Source: %s", strings.Join(f.Source, ", ")))
		}
		buf.WriteString("\n")
	}

	return buf.String()
}

// WriteFindingsLedger serializes a findings ledger to JSON in the workspace at
// discussions/{goalSlug}/round-{NN}/merged-findings-round-{NN}.json.
func WriteFindingsLedger(workspace *Workspace, goalSlug string, round int, ledger *FindingsLedger) error {
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal findings ledger: %w", err)
	}

	relPath := findingsLedgerPath(goalSlug, round)
	if _, err := workspace.WriteFile(relPath, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write findings ledger: %w", err)
	}
	return nil
}

// ReadFindingsLedger reads a previously written findings ledger from the workspace.
func ReadFindingsLedger(workspace *Workspace, goalSlug string, round int) (*FindingsLedger, error) {
	relPath := findingsLedgerPath(goalSlug, round)
	data, err := workspace.ReadFile(relPath)
	if err != nil {
		return nil, fmt.Errorf("read findings ledger: %w", err)
	}

	var ledger FindingsLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, fmt.Errorf("unmarshal findings ledger: %w", err)
	}
	return &ledger, nil
}

func findingsLedgerPath(goalSlug string, round int) string {
	roundDir := fmt.Sprintf("round-%02d", round)
	fileName := fmt.Sprintf("merged-findings-round-%02d.json", round)
	return filepath.Join("discussions", goalSlug, roundDir, fileName)
}
