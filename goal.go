package main

import "time"

type GoalStatus string

const (
	GoalPlanning  GoalStatus = "planning"
	GoalActive    GoalStatus = "active"
	GoalStopped   GoalStatus = "stopped"
	GoalBlocked   GoalStatus = "blocked"
	GoalGated     GoalStatus = "gated"
	GoalCompleted GoalStatus = "completed"
	GoalFailed    GoalStatus = "failed"
)

// HumanGate represents a pause point where the workflow waits for human input.
type HumanGate struct {
	Type      string    `json:"type"`                // "discovery_review" or "escalation"
	Message   string    `json:"message"`             // What the human needs to decide
	Round     int       `json:"round,omitempty"`     // Review round (for escalation gates)
	CreatedAt time.Time `json:"created_at"`
}

type Goal struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	WorkflowRecipe  string     `json:"workflow_recipe,omitempty"`
	MaxReviewRounds int        `json:"max_review_rounds,omitempty"`
	Status          GoalStatus `json:"status"`
	PlanID          string     `json:"plan_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	Summary         string     `json:"summary,omitempty"`
	ActiveGate      *HumanGate `json:"active_gate,omitempty"`
}

type CreateGoalRequest struct {
	Title           string `json:"title"`
	Description     string `json:"description"`
	WorkflowRecipe  string `json:"workflow_recipe,omitempty"`
	MaxReviewRounds int    `json:"max_review_rounds,omitempty"`
}
