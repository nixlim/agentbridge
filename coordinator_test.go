package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCoordinatorDispatchesTaskWithMockAgent(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(t.TempDir(), "agentbridge.log")
	cfg.Agents["claude"] = AgentConfig{
		Command:        "mock",
		TimeoutSeconds: 5,
		MaxRetries:     1,
	}

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

	agents := map[string]Agent{
		"claude": &MockAdapter{name: "claude", response: "done", delay: 20 * time.Millisecond, available: true},
	}
	coordinator := NewCoordinator(cfg, agents, workspace, store, hub)
	coordinator.Start()
	defer func() {
		_ = coordinator.Stop(context.Background())
	}()

	task, err := coordinator.CreateTask(CreateTaskRequest{
		Title:       "Implement auth",
		Description: "Add login flow",
		AssignedTo:  "claude",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	waitFor(t, time.Second, func() bool {
		task, _, err := coordinator.GetTask(task.ID)
		return err == nil && task.Status == TaskCompleted
	})

	completed, _, err := coordinator.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if completed.Result != "done" {
		t.Fatalf("expected result %q, got %q", "done", completed.Result)
	}
}

func TestSendHumanMessageCreatesRunnableTask(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(t.TempDir(), "agentbridge.log")
	cfg.Agents["claude"] = AgentConfig{
		Command:        "mock",
		TimeoutSeconds: 5,
		MaxRetries:     1,
	}

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

	agents := map[string]Agent{
		"claude": &MockAdapter{name: "claude", response: "hello back", delay: 20 * time.Millisecond, available: true},
	}
	coordinator := NewCoordinator(cfg, agents, workspace, store, hub)
	coordinator.Start()
	defer func() {
		_ = coordinator.Stop(context.Background())
	}()

	if err := coordinator.SendHumanMessage("claude", "how are you today?"); err != nil {
		t.Fatalf("SendHumanMessage() error = %v", err)
	}

	waitFor(t, time.Second, func() bool {
		tasks := coordinator.ListTasks()
		return len(tasks) == 1 && tasks[0].Status == TaskCompleted
	})

	tasks := coordinator.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 ad-hoc task, got %d", len(tasks))
	}
	if tasks[0].Result != "hello back" {
		t.Fatalf("expected result %q, got %q", "hello back", tasks[0].Result)
	}

	messages := coordinator.ListMessages(0, 0, "")
	foundReply := false
	for _, msg := range messages {
		if msg.Type == MsgCoordinatorToHuman && msg.TaskID == tasks[0].ID && msg.Content == "hello back" {
			foundReply = true
			break
		}
	}
	if !foundReply {
		t.Fatal("expected coordinator-to-human reply message")
	}
}

func waitFor(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}
