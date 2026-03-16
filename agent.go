package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type AgentStatus string

const (
	AgentIdle    AgentStatus = "idle"
	AgentBusy    AgentStatus = "busy"
	AgentError   AgentStatus = "error"
	AgentOffline AgentStatus = "offline"
	AgentPaused  AgentStatus = "paused"
)

type AgentState struct {
	Name              string             `json:"name"`
	Provider          string             `json:"provider"`
	Role              string             `json:"role"`
	Description       string             `json:"description"`
	Status            AgentStatus        `json:"status"`
	CurrentTask       string             `json:"current_task,omitempty"`
	TasksCompleted    int                `json:"tasks_completed"`
	TasksFailed       int                `json:"tasks_failed"`
	TotalTokensIn     int                `json:"total_tokens_in"`
	TotalTokensOut    int                `json:"total_tokens_out"`
	LastActivity      time.Time          `json:"last_activity"`
	LastInvocation    *CommandTelemetry  `json:"last_invocation,omitempty"`
	RecentInvocations []CommandTelemetry `json:"recent_invocations,omitempty"`
}

type Agent interface {
	Name() string
	Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error)
	ParseOutput(raw []byte) (*AgentResult, error)
	IsAvailable() bool
}

type AgentResult struct {
	RawOutput    string   `json:"raw_output"`
	Summary      string   `json:"summary"`
	FilesChanged []string `json:"files_changed,omitempty"`
	TokensIn     int      `json:"tokens_in"`
	TokensOut    int      `json:"tokens_out"`
	ExitCode     int      `json:"exit_code"`
	DurationMs   int64    `json:"duration_ms"`
}

func newInitialAgentStates(cfg Config, agents map[string]Agent) map[string]*AgentState {
	states := make(map[string]*AgentState, len(cfg.Team))
	for _, member := range cfg.Team {
		status := AgentOffline
		if agent, ok := agents[member.Name]; ok && agent.IsAvailable() {
			status = AgentIdle
		}
		states[member.Name] = &AgentState{
			Name:         member.Name,
			Provider:     member.Provider,
			Role:         member.Role,
			Description:  member.Description,
			Status:       status,
			LastActivity: time.Now().UTC(),
		}
	}
	return states
}

func lookPath(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func parseGenericOutput(raw []byte) (*AgentResult, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return &AgentResult{RawOutput: "", Summary: ""}, nil
	}

	type genericPayload struct {
		Result     string   `json:"result"`
		Summary    string   `json:"summary"`
		Output     string   `json:"output"`
		Files      []string `json:"files_changed"`
		TokensIn   int      `json:"tokens_in"`
		TokensOut  int      `json:"tokens_out"`
		ExitCode   int      `json:"exit_code"`
		DurationMs int64    `json:"duration_ms"`
		IsError    bool     `json:"is_error"`
		Error      string   `json:"error"`
	}

	var payload genericPayload
	if err := json.Unmarshal(raw, &payload); err == nil {
		if payload.IsError {
			if payload.Error == "" {
				payload.Error = "agent reported an error"
			}
			return nil, errors.New(payload.Error)
		}
		summary := firstNonEmpty(payload.Summary, payload.Result, payload.Output)
		return &AgentResult{
			RawOutput:    trimmed,
			Summary:      summary,
			FilesChanged: payload.Files,
			TokensIn:     payload.TokensIn,
			TokensOut:    payload.TokensOut,
			ExitCode:     payload.ExitCode,
			DurationMs:   payload.DurationMs,
		}, nil
	}

	return &AgentResult{
		RawOutput: trimmed,
		Summary:   trimmed,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func buildWrappedPrompt(workspacePath string, forwarded []string, dependencySummaries []string, task *Task) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("You are working in the directory: %s\n", workspacePath))
	builder.WriteString("This is a shared workspace with another AI agent.\n")
	builder.WriteString("Do not delete or overwrite files without being asked to.\n\n")

	if len(dependencySummaries) > 0 {
		builder.WriteString("Completed dependency context:\n")
		for _, summary := range dependencySummaries {
			builder.WriteString("- ")
			builder.WriteString(summary)
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
	}

	if len(forwarded) > 0 {
		builder.WriteString("Context from coordinator:\n")
		for _, msg := range forwarded {
			builder.WriteString("- ")
			builder.WriteString(msg)
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
	}

	builder.WriteString("Your task:\n")
	builder.WriteString(task.Description)
	return builder.String()
}
