package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
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
	ctx, cancel := context.WithTimeout(ctx, time.Duration(a.config.TimeoutSeconds)*time.Second)
	defer cancel()

	args := append([]string{}, a.config.Args...)
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, a.config.Command, args...)
	cmd.Dir = chooseWorkDir(workDir, a.config.WorkingDir, a.workspacePath)
	cmd.Env = append(os.Environ(), flattenEnv(a.config.Env)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("timeout after %ds", a.config.TimeoutSeconds)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	result, parseErr := a.ParseOutput(stdout.Bytes())
	if parseErr != nil {
		return &AgentResult{
			RawOutput:  stdout.String(),
			Summary:    strings.TrimSpace(stdout.String()),
			DurationMs: duration.Milliseconds(),
		}, nil
	}
	result.DurationMs = duration.Milliseconds()
	return result, nil
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
