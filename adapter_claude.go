package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type ClaudeAdapter struct {
	config        AgentConfig
	workspacePath string
}

func NewClaudeAdapter(config AgentConfig, workspacePath string) *ClaudeAdapter {
	return &ClaudeAdapter{config: config, workspacePath: workspacePath}
}

func (a *ClaudeAdapter) Name() string { return "claude" }

func (a *ClaudeAdapter) IsAvailable() bool {
	return lookPath(a.config.Command)
}

func (a *ClaudeAdapter) Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(a.config.TimeoutSeconds)*time.Second)
	defer cancel()

	args := append([]string{}, a.config.Args...)
	args = ensureArg(args, "--dangerously-skip-permissions")
	args = append([]string{"-p", prompt}, args...)
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

func (a *ClaudeAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
	type payload struct {
		Result     string  `json:"result"`
		CostUSD    float64 `json:"cost_usd"`
		DurationMS int64   `json:"duration_ms"`
		NumTurns   int     `json:"num_turns"`
		IsError    bool    `json:"is_error"`
		Error      string  `json:"error"`
	}

	var data payload
	if err := json.Unmarshal(raw, &data); err != nil {
		type event struct {
			Type       string `json:"type"`
			Subtype    string `json:"subtype"`
			Result     string `json:"result"`
			IsError    bool   `json:"is_error"`
			DurationMS int64  `json:"duration_ms"`
		}

		var events []event
		if err := json.Unmarshal(raw, &events); err == nil {
			for i := len(events) - 1; i >= 0; i-- {
				if events[i].Type != "result" {
					continue
				}
				if events[i].IsError {
					return nil, errors.New(firstNonEmpty(events[i].Result, "claude execution failed"))
				}
				return &AgentResult{
					RawOutput:  strings.TrimSpace(string(raw)),
					Summary:    strings.TrimSpace(events[i].Result),
					DurationMs: events[i].DurationMS,
				}, nil
			}
		}
		return parseGenericOutput(raw)
	}
	if data.IsError {
		return nil, errors.New(firstNonEmpty(data.Error, "claude execution failed"))
	}
	return &AgentResult{
		RawOutput:  strings.TrimSpace(string(raw)),
		Summary:    strings.TrimSpace(data.Result),
		DurationMs: data.DurationMS,
	}, nil
}
