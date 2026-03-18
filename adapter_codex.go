package main

import (
	"context"
	"fmt"
)

type CodexAdapter struct {
	config        AgentConfig
	workspacePath string
}

func NewCodexAdapter(config AgentConfig, workspacePath string) *CodexAdapter {
	return &CodexAdapter{config: config, workspacePath: workspacePath}
}

func (a *CodexAdapter) Name() string { return "codex" }

func (a *CodexAdapter) IsAvailable() bool {
	return lookPath(a.config.Command)
}

func (a *CodexAdapter) Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error) {
	args := append([]string{}, a.config.Args...)
	// Pass the prompt via stdin using "-" as the positional argument.
	// This avoids OS command-line length limits when prompts are large
	// (workspace file lists, dependency context, etc. can exceed ARG_MAX).
	args = append(args, "-")
	return executeAdapterCommandWithStdin(ctx, a.config, a.workspacePath, workDir, args, prompt, a.ParseOutput)
}

func (a *CodexAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
	return parseGenericOutput(raw)
}

func chooseWorkDir(candidate, configured, fallback string) string {
	switch {
	case candidate != "":
		return candidate
	case configured != "":
		return configured
	default:
		return fallback
	}
}

func flattenEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, fmt.Sprintf("%s=%s", key, value))
	}
	return out
}
