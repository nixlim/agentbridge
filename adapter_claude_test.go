package main

import "testing"

func TestClaudeParseOutputUsesStructuredOutputFromResultEvent(t *testing.T) {
	adapter := NewClaudeAdapter(AgentConfig{Command: "claude"}, t.TempDir())
	raw := []byte(`[{"type":"system","subtype":"init"},{"type":"result","subtype":"success","is_error":false,"duration_ms":123,"result":"","structured_output":{"thinking":"ok","decisions":[]}}]`)

	result, err := adapter.ParseOutput(raw)
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if result.Summary != `{"thinking":"ok","decisions":[]}` {
		t.Fatalf("expected structured output summary, got %q", result.Summary)
	}
}
