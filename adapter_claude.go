package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	args := append([]string{}, a.config.Args...)
	args = ensureArg(args, "--dangerously-skip-permissions")
	args = append([]string{"-p", prompt}, args...)
	return executeAdapterCommand(ctx, a.config, a.workspacePath, workDir, args, a.ParseOutput)
}

func (a *ClaudeAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
	type payload struct {
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		CostUSD          float64         `json:"cost_usd"`
		DurationMS       int64           `json:"duration_ms"`
		NumTurns         int             `json:"num_turns"`
		IsError          bool            `json:"is_error"`
		Error            string          `json:"error"`
	}

	var data payload
	if err := json.Unmarshal(raw, &data); err != nil {
		type event struct {
			Type             string          `json:"type"`
			Subtype          string          `json:"subtype"`
			Result           string          `json:"result"`
			StructuredOutput json.RawMessage `json:"structured_output"`
			IsError          bool            `json:"is_error"`
			DurationMS       int64           `json:"duration_ms"`
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
					Summary:    claudeSummary(events[i].StructuredOutput, events[i].Result),
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
		Summary:    claudeSummary(data.StructuredOutput, data.Result),
		DurationMs: data.DurationMS,
	}, nil
}

func claudeSummary(structuredOutput json.RawMessage, result string) string {
	if trimmed := strings.TrimSpace(string(structuredOutput)); trimmed != "" && trimmed != "null" {
		return trimmed
	}
	return strings.TrimSpace(result)
}
