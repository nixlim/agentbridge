package main

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskBlocked   TaskStatus = "blocked"
	TaskReview    TaskStatus = "review"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

type Task struct {
	ID                 string     `json:"id"`
	ParentID           string     `json:"parent_id,omitempty"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	AssignedTo         string     `json:"assigned_to"`
	Status             TaskStatus `json:"status"`
	DependsOn          []string   `json:"depends_on,omitempty"`
	ReviewBy           string     `json:"review_by,omitempty"`
	Result             string     `json:"result,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	MessageIDs         []string   `json:"message_ids,omitempty"`
	RetryCount         int        `json:"retry_count"`
	MaxRetries         int        `json:"max_retries"`
	FilesChanged       []string   `json:"files_changed,omitempty"`
	CommitHash         string     `json:"commit_hash,omitempty"`
	ReviewTaskID       string     `json:"review_task_id,omitempty"`
	ReviewResult       string     `json:"review_result,omitempty"`
	ReviewReason       string     `json:"review_reason,omitempty"`
	ReviewActionTaskID string     `json:"review_action_task_id,omitempty"`
	IsReviewTask       bool       `json:"is_review_task,omitempty"`
	IsHumanMessage     bool       `json:"is_human_message,omitempty"`
}

type CreateTaskRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	AssignedTo  string   `json:"assigned_to"`
	DependsOn   []string `json:"depends_on"`
	ReviewBy    string   `json:"review_by"`
}

func NewTask(req CreateTaskRequest, maxRetries int) *Task {
	return &Task{
		ID:          uuid.NewString(),
		Title:       req.Title,
		Description: req.Description,
		AssignedTo:  req.AssignedTo,
		Status:      TaskPending,
		DependsOn:   append([]string(nil), req.DependsOn...),
		ReviewBy:    req.ReviewBy,
		CreatedAt:   time.Now().UTC(),
		MaxRetries:  maxRetries,
		MessageIDs:  []string{},
	}
}

func (t *Task) Start() error {
	if t.Status != TaskPending && t.Status != TaskBlocked {
		return errors.New("task cannot start from current state")
	}
	now := time.Now().UTC()
	t.Status = TaskRunning
	t.StartedAt = &now
	return nil
}

func (t *Task) SetBlocked() error {
	if t.Status != TaskPending && t.Status != TaskBlocked {
		return errors.New("task cannot be blocked from current state")
	}
	t.Status = TaskBlocked
	return nil
}

func (t *Task) MarkPending() error {
	if t.Status != TaskBlocked && t.Status != TaskPending {
		return errors.New("task cannot return to pending from current state")
	}
	t.Status = TaskPending
	return nil
}

func (t *Task) Complete(result string, filesChanged []string, commitHash string) error {
	if t.Status != TaskRunning && t.Status != TaskReview {
		return errors.New("task cannot complete from current state")
	}
	now := time.Now().UTC()
	t.Status = TaskCompleted
	t.Result = result
	t.CompletedAt = &now
	t.FilesChanged = append([]string(nil), filesChanged...)
	t.CommitHash = commitHash
	return nil
}

func (t *Task) MoveToReview(result string, filesChanged []string, commitHash string) error {
	if t.Status != TaskRunning {
		return errors.New("task cannot enter review from current state")
	}
	now := time.Now().UTC()
	t.Status = TaskReview
	t.Result = result
	t.CompletedAt = &now
	t.FilesChanged = append([]string(nil), filesChanged...)
	t.CommitHash = commitHash
	return nil
}

func (t *Task) Fail(reason string) error {
	if t.Status != TaskRunning && t.Status != TaskReview && t.Status != TaskPending && t.Status != TaskBlocked {
		return errors.New("task cannot fail from current state")
	}
	now := time.Now().UTC()
	t.Status = TaskFailed
	t.Result = reason
	t.CompletedAt = &now
	return nil
}

func (t *Task) Cancel(reason string) error {
	if t.Status == TaskCompleted || t.Status == TaskFailed || t.Status == TaskCancelled {
		return errors.New("task cannot be cancelled from terminal state")
	}
	now := time.Now().UTC()
	t.Status = TaskCancelled
	t.Result = reason
	t.CompletedAt = &now
	return nil
}

func (t *Task) ApproveReview() error {
	if t.Status != TaskReview {
		return errors.New("task is not awaiting review")
	}
	now := time.Now().UTC()
	t.Status = TaskCompleted
	t.CompletedAt = &now
	return nil
}

func (t *Task) RejectReview(reason string) error {
	if t.Status != TaskReview {
		return errors.New("task is not awaiting review")
	}
	now := time.Now().UTC()
	t.Status = TaskFailed
	t.ReviewReason = reason
	t.Result = reason
	t.CompletedAt = &now
	return nil
}

func (t *Task) Clone() *Task {
	if t == nil {
		return nil
	}
	clone := *t
	clone.DependsOn = append([]string(nil), t.DependsOn...)
	clone.MessageIDs = append([]string(nil), t.MessageIDs...)
	clone.FilesChanged = append([]string(nil), t.FilesChanged...)
	return &clone
}
