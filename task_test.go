package main

import "testing"

func TestTaskStateTransitions(t *testing.T) {
	t.Parallel()

	task := NewTask(CreateTaskRequest{
		Title:       "Implement auth",
		Description: "Add login flow",
		AssignedTo:  "claude",
	}, 2)

	if err := task.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if task.Status != TaskRunning {
		t.Fatalf("expected status %s, got %s", TaskRunning, task.Status)
	}

	if err := task.MoveToReview("done", []string{"auth.go"}, "abc123"); err != nil {
		t.Fatalf("MoveToReview() error = %v", err)
	}
	if task.Status != TaskReview {
		t.Fatalf("expected status %s, got %s", TaskReview, task.Status)
	}

	if err := task.ApproveReview(); err != nil {
		t.Fatalf("ApproveReview() error = %v", err)
	}
	if task.Status != TaskCompleted {
		t.Fatalf("expected status %s, got %s", TaskCompleted, task.Status)
	}

	if err := task.Cancel("too late"); err == nil {
		t.Fatal("expected cancelling a completed task to fail")
	}
}
