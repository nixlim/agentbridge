package main

import (
	"context"
	"os"
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
	coordinator := NewCoordinator(cfg, agents, nil, workspace, store, hub)
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
	coordinator := NewCoordinator(cfg, agents, nil, workspace, store, hub)
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

func TestDispatchReadyTasksDoesNotPanicWhenAssignedAgentMissing(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(t.TempDir(), "agentbridge.log")
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "ghost",
			Provider:    "claude",
			Role:        "implementer",
			Count:       1,
			Description: "Missing runtime agent",
		},
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

	coordinator := NewCoordinator(cfg, map[string]Agent{}, nil, workspace, store, hub)
	task := NewTask(CreateTaskRequest{
		Title:       "Missing agent task",
		Description: "Should not panic",
		AssignedTo:  "ghost",
	}, 1)

	coordinator.mu.Lock()
	coordinator.tasks[task.ID] = task
	coordinator.taskOrder = append(coordinator.taskOrder, task.ID)
	coordinator.agentState["ghost"] = &AgentState{
		Name:         "ghost",
		Provider:     "claude",
		Role:         "implementer",
		Status:       AgentIdle,
		LastActivity: time.Now().UTC(),
	}
	coordinator.mu.Unlock()

	coordinator.dispatchReadyTasks()

	got, _, err := coordinator.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got.Status != TaskBlocked {
		t.Fatalf("expected blocked task, got %s", got.Status)
	}
	if got.Result == "" {
		t.Fatal("expected missing-agent explanation on blocked task")
	}
}

func TestRecoverFromLogRequeuesStaleRunningTask(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(t.TempDir(), "agentbridge.log")
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "claude",
			Provider:    "claude",
			Role:        "implementer",
			Count:       1,
			Description: "Implementation agent",
		},
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

	task := NewTask(CreateTaskRequest{
		Title:       "Recovered task",
		Description: "Should be requeued",
		AssignedTo:  "claude",
	}, 1)
	if err := task.Start(); err != nil {
		t.Fatalf("task.Start() error = %v", err)
	}
	agentState := &AgentState{
		Name:         "claude",
		Provider:     "claude",
		Role:         "implementer",
		Status:       AgentBusy,
		CurrentTask:  task.ID,
		LastActivity: time.Now().UTC(),
		LastInvocation: &CommandTelemetry{
			Command:        "claude",
			Status:         "running",
			PID:            999999,
			TimeoutSeconds: 900,
			StartedAt:      time.Now().Add(-time.Minute),
			LastEventAt:    time.Now().Add(-time.Minute),
		},
	}
	if err := store.Append(&Message{Metadata: Metadata{Task: task.Clone()}}); err != nil {
		t.Fatalf("append task message: %v", err)
	}
	if err := store.Append(&Message{Metadata: Metadata{Agent: agentState}}); err != nil {
		t.Fatalf("append agent message: %v", err)
	}

	hub := NewWebSocketHub()
	go hub.Run()
	defer hub.Shutdown()

	coordinator := NewCoordinator(cfg, map[string]Agent{
		"claude": &MockAdapter{name: "claude", response: "done", delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	if err := coordinator.RecoverFromLog(); err != nil {
		t.Fatalf("RecoverFromLog() error = %v", err)
	}

	recovered, _, err := coordinator.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if recovered.Status != TaskPending {
		t.Fatalf("expected pending recovered task, got %s", recovered.Status)
	}
	if coordinator.agentState["claude"].Status != AgentIdle {
		t.Fatalf("expected idle recovered agent, got %s", coordinator.agentState["claude"].Status)
	}
}

func TestDetectIntegrationConflictIgnoresCrossCritiqueTasks(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(t.TempDir(), "agentbridge.log")

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

	coordinator := NewCoordinator(cfg, map[string]Agent{}, nil, workspace, store, hub)
	goal := &Goal{
		ID:        "goal-cross-critique",
		Title:     "Cross critique goal",
		Status:    GoalActive,
		CreatedAt: time.Now().UTC(),
	}
	plan := &Plan{
		ID:     "plan-cross-critique",
		GoalID: goal.ID,
		Phases: []Phase{
			{Number: 11, Title: "Adversarial Review Round 4"},
			{Number: 12, Title: "Cross Critique Round 4"},
			{Number: 13, Title: "Consolidate Review Round 4"},
		},
		CreatedAt: time.Now().UTC(),
		Version:   1,
	}
	other := &Task{
		ID:          "other-task",
		GoalID:      goal.ID,
		Title:       "Cross Critique Reviewer 1",
		Status:      TaskCompleted,
		PlanPhase:   12,
		FilesChanged: []string{"specs/b4-spec-spec.md"},
		CreatedAt:   time.Now().UTC(),
	}
	current := &Task{
		ID:        "current-task",
		GoalID:    goal.ID,
		Title:     "Cross Critique Reviewer 2",
		Status:    TaskCompleted,
		PlanPhase: 12,
		CreatedAt: time.Now().UTC(),
	}

	coordinator.mu.Lock()
	coordinator.goals[goal.ID] = goal
	coordinator.goalOrder = append(coordinator.goalOrder, goal.ID)
	coordinator.tasks[other.ID] = other
	coordinator.tasks[current.ID] = current
	coordinator.taskOrder = append(coordinator.taskOrder, other.ID, current.ID)
	coordinator.brainState.CurrentPlan = plan
	coordinator.mu.Unlock()

	if got := coordinator.detectIntegrationConflictLocked(current, []string{"specs/b4-spec-spec.md"}); got != "" {
		t.Fatalf("expected no integration conflict for cross-critique task, got %q", got)
	}
}

func TestRecoverFromLogKeepsLiveRunningTask(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(t.TempDir(), "agentbridge.log")
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "claude",
			Provider:    "claude",
			Role:        "implementer",
			Count:       1,
			Description: "Implementation agent",
		},
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

	task := NewTask(CreateTaskRequest{
		Title:       "Recovered task",
		Description: "Should stay running",
		AssignedTo:  "claude",
	}, 1)
	if err := task.Start(); err != nil {
		t.Fatalf("task.Start() error = %v", err)
	}
	agentState := &AgentState{
		Name:         "claude",
		Provider:     "claude",
		Role:         "implementer",
		Status:       AgentBusy,
		CurrentTask:  task.ID,
		LastActivity: time.Now().UTC(),
		LastInvocation: &CommandTelemetry{
			Command:        "claude",
			Status:         "running",
			PID:            os.Getpid(),
			TimeoutSeconds: 900,
			StartedAt:      time.Now().Add(-time.Second),
			LastEventAt:    time.Now().Add(-time.Second),
		},
	}
	if err := store.Append(&Message{Metadata: Metadata{Task: task.Clone()}}); err != nil {
		t.Fatalf("append task message: %v", err)
	}
	if err := store.Append(&Message{Metadata: Metadata{Agent: agentState}}); err != nil {
		t.Fatalf("append agent message: %v", err)
	}

	hub := NewWebSocketHub()
	go hub.Run()
	defer hub.Shutdown()

	coordinator := NewCoordinator(cfg, map[string]Agent{
		"claude": &MockAdapter{name: "claude", response: "done", delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	if err := coordinator.RecoverFromLog(); err != nil {
		t.Fatalf("RecoverFromLog() error = %v", err)
	}

	recovered, _, err := coordinator.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if recovered.Status != TaskRunning {
		t.Fatalf("expected running recovered task, got %s", recovered.Status)
	}
	if coordinator.agentState["claude"].Status != AgentBusy {
		t.Fatalf("expected busy recovered agent, got %s", coordinator.agentState["claude"].Status)
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
