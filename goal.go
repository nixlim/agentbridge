package main

import "time"

type GoalStatus string

const (
	GoalPlanning  GoalStatus = "planning"
	GoalActive    GoalStatus = "active"
	GoalCompleted GoalStatus = "completed"
	GoalFailed    GoalStatus = "failed"
)

type Goal struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      GoalStatus `json:"status"`
	PlanID      string     `json:"plan_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Summary     string     `json:"summary,omitempty"`
}

type CreateGoalRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}
