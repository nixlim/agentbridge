package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewTaskDispatchesWhenParentIsInReview(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(t.TempDir(), "agentbridge.log")
	cfg.Agents["claude"] = AgentConfig{Command: "mock", TimeoutSeconds: 5, MaxRetries: 1}
	cfg.Agents["codex"] = AgentConfig{Command: "mock", TimeoutSeconds: 5, MaxRetries: 1}

	workspace := NewWorkspace(cfg.Workspace)
	if err := workspace.Init(); err != nil {
		t.Fatalf("workspace.Init() error = %v", err)
	}

	store, err := NewMessageStore(cfg.Log.File)
	if err != nil {
		t.Fatalf("NewMessageStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	hub := NewWebSocketHub()
	go hub.Run()
	defer hub.Shutdown()

	coordinator := NewCoordinator(cfg, map[string]Agent{
		"claude": &MockAdapter{name: "claude", response: "implementation complete", delay: 20 * time.Millisecond, available: true},
		"codex":  &MockAdapter{name: "codex", response: "review complete", delay: 20 * time.Millisecond, available: true},
	}, workspace, store, hub)
	coordinator.Start()
	defer func() {
		_ = coordinator.Stop(context.Background())
	}()

	task, err := coordinator.CreateTask(CreateTaskRequest{
		Title:       "Build login form",
		Description: "Create login page",
		AssignedTo:  "claude",
		ReviewBy:    "codex",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		root, _, err := coordinator.GetTask(task.ID)
		if err != nil {
			return false
		}
		if root.ReviewTaskID == "" {
			return false
		}
		review, _, err := coordinator.GetTask(root.ReviewTaskID)
		return err == nil && review.Status == TaskCompleted
	})

	root, _, err := coordinator.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask(root) error = %v", err)
	}
	if root.Status != TaskReview {
		t.Fatalf("expected root task to remain in review, got %s", root.Status)
	}
	if root.ReviewResult != "review complete" {
		t.Fatalf("expected review result to be recorded, got %q", root.ReviewResult)
	}
	if root.ReviewActionTaskID == "" {
		t.Fatal("expected review action task to be created")
	}

	waitFor(t, 2*time.Second, func() bool {
		followUp, _, err := coordinator.GetTask(root.ReviewActionTaskID)
		return err == nil && followUp.Status == TaskCompleted
	})

	followUp, _, err := coordinator.GetTask(root.ReviewActionTaskID)
	if err != nil {
		t.Fatalf("GetTask(followUp) error = %v", err)
	}
	if followUp.AssignedTo != "claude" {
		t.Fatalf("expected follow-up assigned to claude, got %s", followUp.AssignedTo)
	}
	if followUp.Result != "implementation complete" {
		t.Fatalf("expected follow-up result %q, got %q", "implementation complete", followUp.Result)
	}
}
