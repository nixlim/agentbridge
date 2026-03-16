package main

import "testing"

func TestConstrainBrainProviderForClaudeDisablesToolsAndAddsSchema(t *testing.T) {
	cfg := ProviderConfig{
		Command:        "claude",
		Args:           []string{"--output-format", "json", "--verbose"},
		TimeoutSeconds: 120,
		Env:            map[string]string{},
	}

	constrained := constrainBrainProvider("claude", cfg)

	assertOptionPair := func(flag, value string) {
		t.Helper()
		for i := 0; i < len(constrained.Args)-1; i++ {
			if constrained.Args[i] == flag && constrained.Args[i+1] == value {
				return
			}
		}
		t.Fatalf("expected %s %q in args, got %#v", flag, value, constrained.Args)
	}

	assertOptionPair("--tools", "")
	assertOptionPair("--json-schema", claudeBrainDecisionSchema)
	if !containsString(constrained.Args, "--disable-slash-commands") {
		t.Fatalf("expected --disable-slash-commands in args, got %#v", constrained.Args)
	}
	if !containsString(constrained.Args, "--strict-mcp-config") {
		t.Fatalf("expected --strict-mcp-config in args, got %#v", constrained.Args)
	}
	if containsString(constrained.Args, "--verbose") {
		t.Fatalf("expected --verbose to be removed for brain invocations, got %#v", constrained.Args)
	}
	if constrained.Env["CLAUDE_CODE_MAX_TURNS"] != "1" {
		t.Fatalf("expected CLAUDE_CODE_MAX_TURNS=1, got %#v", constrained.Env)
	}
}
