package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const defaultBrainSystemPrompt = `You are the lead coordinator of a software engineering team. You do not write code yourself.
Your job is to plan, delegate, review, and coordinate.

## Your Team

{{TEAM_ROSTER}}

## Your Capabilities

You make decisions by responding with structured JSON. Every response must be valid JSON.

{
  "thinking": "Visible reasoning for the human operator",
  "decisions": [
    { "action": "create_task", "title": "...", "description": "...", "assign_to": "...", "depends_on": [], "review_by": "", "priority": 3, "files_touched": [] },
    { "action": "accept_task", "task_id": "..." },
    { "action": "revise_task", "task_id": "...", "feedback": "..." },
    { "action": "reassign_task", "task_id": "...", "new_agent": "...", "reason": "..." },
    { "action": "send_message", "to": "agent-name|human", "content": "..." },
    { "action": "complete_goal", "summary": "..." },
    { "action": "request_human_input", "question": "...", "context": "..." },
    { "action": "update_plan", "plan": { "phases": [] }, "reason": "..." }
  ]
}

Rules:
1. Never assign work to yourself.
2. Match tasks to agent roles.
3. Be specific in task descriptions.
4. Always accept or revise finished work.
5. Sequence tasks when file overlap is likely.
6. Prefer small, focused tasks.
7. Do not read files, browse, research, or ask tools to investigate before responding.
8. Base decisions only on the state and trigger provided in the prompt.
9. For goal_submitted, produce the smallest viable initial plan and delegate immediately.
10. If critical information is missing, emit request_human_input instead of investigating.
11. If the goal mentions skills, commands, or files, treat them as inputs for delegated agents, not actions for you to perform yourself.`

type BrainState struct {
	ConversationHistory []BrainMessage     `json:"conversation_history"`
	CurrentPlan         *Plan              `json:"current_plan,omitempty"`
	GoalDescription     string             `json:"goal_description,omitempty"`
	InvocationCount     int                `json:"invocation_count"`
	InvocationInFlight  bool               `json:"invocation_in_flight"`
	TotalTokensIn       int                `json:"total_tokens_in"`
	TotalTokensOut      int                `json:"total_tokens_out"`
	LastThinking        string             `json:"last_thinking,omitempty"`
	LastTrigger         string             `json:"last_trigger,omitempty"`
	ActiveProvider      string             `json:"active_provider"`
	PendingHumanInput   *HumanInputRequest `json:"pending_human_input,omitempty"`
	LastInvocation      *CommandTelemetry  `json:"last_invocation,omitempty"`
	RecentInvocations   []CommandTelemetry `json:"recent_invocations,omitempty"`
}

type BrainMessage struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type BrainDecision struct {
	Thinking  string          `json:"thinking"`
	Decisions []DecisionEntry `json:"decisions"`
}

type DecisionEntry struct {
	Action       string   `json:"action"`
	Title        string   `json:"title,omitempty"`
	Description  string   `json:"description,omitempty"`
	AssignTo     string   `json:"assign_to,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	ReviewBy     string   `json:"review_by,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	FilesTouched []string `json:"files_touched,omitempty"`
	To           string   `json:"to,omitempty"`
	Content      string   `json:"content,omitempty"`
	TaskID       string   `json:"task_id,omitempty"`
	Feedback     string   `json:"feedback,omitempty"`
	NewAgent     string   `json:"new_agent,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Question     string   `json:"question,omitempty"`
	Context      string   `json:"context,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Plan         *Plan    `json:"plan,omitempty"`
}

type HumanInputRequest struct {
	Question      string `json:"question"`
	Context       string `json:"context"`
	QuestionsFile string `json:"questions_file,omitempty"`
}

func loadBrainSystemPrompt(cfg BrainConfig) string {
	if cfg.SystemPromptFile != "" {
		if data, err := os.ReadFile(cfg.SystemPromptFile); err == nil && strings.TrimSpace(string(data)) != "" {
			return string(data)
		}
	}
	return defaultBrainSystemPrompt
}

func parseBrainDecision(raw string) (*BrainDecision, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("brain returned empty output")
	}

	if decision := parseBrainDecisionValue(trimmed); decision != nil {
		return decision, nil
	}
	return nil, fmt.Errorf("brain output was not valid JSON")
}

var jsonFencePattern = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

func parseBrainDecisionValue(raw string) *BrainDecision {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	if decision := parseBrainObjectString(trimmed); decision != nil {
		return decision
	}

	var decoded interface{}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		if decision := parseBrainDecisionAny(decoded, 0); decision != nil {
			return decision
		}
	}

	if matches := jsonFencePattern.FindStringSubmatch(trimmed); len(matches) == 2 {
		fenced := strings.TrimSpace(matches[1])
		if decision := parseBrainObjectString(fenced); decision != nil {
			return decision
		}
	}

	if candidate := extractJSONObject(trimmed); candidate != "" && candidate != trimmed {
		if decision := parseBrainObjectString(candidate); decision != nil {
			return decision
		}
	}

	return nil
}

func parseBrainDecisionAny(value interface{}, depth int) *BrainDecision {
	if depth > 6 {
		return nil
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		if decision := brainDecisionFromMap(typed); decision != nil {
			return decision
		}
		for _, key := range []string{"result", "summary", "output", "message", "content", "text"} {
			if nested, ok := typed[key]; ok {
				if decision := parseBrainDecisionAny(nested, depth+1); decision != nil {
					return decision
				}
			}
		}
	case []interface{}:
		for i := len(typed) - 1; i >= 0; i-- {
			if decision := parseBrainDecisionAny(typed[i], depth+1); decision != nil {
				return decision
			}
		}
	case string:
		if decision := parseBrainObjectString(typed); decision != nil {
			return decision
		}
		if matches := jsonFencePattern.FindStringSubmatch(typed); len(matches) == 2 {
			if decision := parseBrainObjectString(matches[1]); decision != nil {
				return decision
			}
		}
		if candidate := extractJSONObject(typed); candidate != "" && candidate != strings.TrimSpace(typed) {
			return parseBrainObjectString(candidate)
		}
	}
	return nil
}

func parseBrainObjectString(raw string) *BrainDecision {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return nil
	}

	var decision BrainDecision
	if err := json.Unmarshal([]byte(trimmed), &decision); err != nil || !looksLikeBrainDecision(&decision) {
		return nil
	}
	return normalizeBrainDecision(&decision)
}

func extractJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(raw[start : end+1])
}

func brainDecisionFromMap(values map[string]interface{}) *BrainDecision {
	if values == nil {
		return nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	var decision BrainDecision
	if err := json.Unmarshal(data, &decision); err != nil || !looksLikeBrainDecision(&decision) {
		return nil
	}
	return normalizeBrainDecision(&decision)
}

func looksLikeBrainDecision(decision *BrainDecision) bool {
	if decision == nil {
		return false
	}
	if strings.TrimSpace(decision.Thinking) != "" {
		return true
	}
	return decision.Decisions != nil
}

func normalizeBrainDecision(decision *BrainDecision) *BrainDecision {
	if decision == nil {
		return nil
	}
	if decision.Decisions == nil {
		decision.Decisions = []DecisionEntry{}
	}
	return decision
}
