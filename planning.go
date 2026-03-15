package main

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Plan struct {
	ID        string    `json:"id"`
	GoalID    string    `json:"goal_id"`
	Phases    []Phase   `json:"phases"`
	CreatedAt time.Time `json:"created_at"`
	Version   int       `json:"version"`
}

type WorkflowState struct {
	Mode               string `json:"mode"`
	PlannerMode        string `json:"planner_mode"`
	Status             string `json:"status"`
	CurrentPhaseNumber int    `json:"current_phase_number,omitempty"`
	CurrentPhaseTitle  string `json:"current_phase_title,omitempty"`
	TotalPhases        int    `json:"total_phases,omitempty"`
}

type Phase struct {
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Tasks       []PlannedTask `json:"tasks"`
}

type PlannedTask struct {
	TempID       string   `json:"temp_id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	AssignTo     string   `json:"assign_to"`
	DependsOn    []string `json:"depends_on"`
	ReviewBy     string   `json:"review_by"`
	Priority     int      `json:"priority"`
	FilesTouched []string `json:"files_touched,omitempty"`
	RealTaskID   string   `json:"real_task_id,omitempty"`
}

type PlanExecutor struct {
	plan           *Plan
	tempToReal     map[string]string
	executedPhases map[int]bool
	currentPhase   int
}

func NewPlanExecutor(plan *Plan) *PlanExecutor {
	return &PlanExecutor{
		plan:           plan,
		tempToReal:     map[string]string{},
		executedPhases: map[int]bool{},
		currentPhase:   -1,
	}
}

func (pe *PlanExecutor) ExecutePhase(c *Coordinator, phaseIndex int) error {
	if pe == nil || pe.plan == nil {
		return nil
	}
	if phaseIndex < 0 || phaseIndex >= len(pe.plan.Phases) {
		return fmt.Errorf("phase %d out of range", phaseIndex)
	}
	if pe.executedPhases[phaseIndex] {
		pe.currentPhase = phaseIndex
		return nil
	}

	phase := &pe.plan.Phases[phaseIndex]
	for i := range phase.Tasks {
		pt := &phase.Tasks[i]
		assignedTo, err := c.resolveAgentNameLocked(pt.AssignTo)
		if err != nil {
			return err
		}
		reviewBy := ""
		if pt.ReviewBy != "" {
			reviewBy, err = c.resolveAgentNameLocked(pt.ReviewBy)
			if err != nil {
				return err
			}
		}
		task, err := c.createTaskLocked(CreateTaskRequest{
			Title:        pt.Title,
			Description:  pt.Description,
			AssignedTo:   assignedTo,
			ReviewBy:     reviewBy,
			GoalID:       pe.plan.GoalID,
			FilesTouched: pt.FilesTouched,
			Priority:     pt.Priority,
		})
		if err != nil {
			return err
		}
		task.PlanPhase = phase.Number
		pt.RealTaskID = task.ID
		pe.tempToReal[pt.TempID] = task.ID
	}
	for i := range phase.Tasks {
		pt := &phase.Tasks[i]
		task := c.tasks[pt.RealTaskID]
		if task == nil {
			continue
		}
		task.DependsOn = task.DependsOn[:0]
		for _, dep := range pt.DependsOn {
			realTaskID := pe.tempToReal[dep]
			if realTaskID == "" {
				return fmt.Errorf("planned dependency %q has not been created", dep)
			}
			task.DependsOn = append(task.DependsOn, realTaskID)
		}
		c.recordTaskLocked(task, "task created")
	}

	pe.executedPhases[phaseIndex] = true
	pe.currentPhase = phaseIndex
	return nil
}

func normalizePlan(goalID string, plan *Plan, previous *Plan) *Plan {
	if plan == nil {
		return nil
	}
	clone := *plan
	if clone.ID == "" {
		clone.ID = uuid.NewString()
	}
	if clone.GoalID == "" {
		clone.GoalID = goalID
	}
	if clone.CreatedAt.IsZero() {
		clone.CreatedAt = time.Now().UTC()
	}
	if clone.Version <= 0 {
		if previous != nil {
			clone.Version = previous.Version + 1
		} else {
			clone.Version = 1
		}
	}
	return &clone
}
