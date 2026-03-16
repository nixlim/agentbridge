package main

import "testing"

func TestParseBrainDecisionFromClaudeResultEnvelope(t *testing.T) {
	raw := `[{"type":"system","subtype":"init"},{"type":"assistant","message":{"content":[{"type":"text","text":"{\"thinking\":\"intermediate\",\"decisions\":[]}"}]}},{"type":"result","subtype":"success","is_error":false,"result":"{\"thinking\":\"ok\",\"decisions\":[{\"action\":\"complete_goal\",\"summary\":\"done\"}]}"}]`

	decision, err := parseBrainDecision(raw)
	if err != nil {
		t.Fatalf("parseBrainDecision() error = %v", err)
	}
	if decision.Thinking != "ok" {
		t.Fatalf("expected thinking from result payload, got %q", decision.Thinking)
	}
	if len(decision.Decisions) != 1 || decision.Decisions[0].Action != "complete_goal" {
		t.Fatalf("expected complete_goal decision, got %#v", decision.Decisions)
	}
}

func TestParseBrainDecisionFromMarkdownFence(t *testing.T) {
	raw := "Here is the plan:\n```json\n{\"thinking\":\"ok\",\"decisions\":[]}\n```"

	decision, err := parseBrainDecision(raw)
	if err != nil {
		t.Fatalf("parseBrainDecision() error = %v", err)
	}
	if decision.Thinking != "ok" {
		t.Fatalf("expected thinking ok, got %q", decision.Thinking)
	}
	if len(decision.Decisions) != 0 {
		t.Fatalf("expected no decisions, got %#v", decision.Decisions)
	}
}

func TestParseBrainDecisionRejectsWrapperWithoutDecisionShape(t *testing.T) {
	raw := `{"type":"result","result":"not a decision"}`

	if _, err := parseBrainDecision(raw); err == nil {
		t.Fatal("expected parseBrainDecision to reject non-decision wrapper JSON")
	}
}

func TestParseBrainDecisionIgnoresWrapperTextWithBraces(t *testing.T) {
	raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"status update: {not-json}"}]}}`

	if _, err := parseBrainDecision(raw); err == nil {
		t.Fatal("expected parseBrainDecision to reject wrapper text with brace noise")
	}
}
