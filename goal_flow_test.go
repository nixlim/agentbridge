package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var taskIDPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
var completedTaskPattern = regexp.MustCompile(`task_completed:.*\(([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)`)
var triggerPattern = regexp.MustCompile(`## Trigger\s+([a-z_]+):`)
var triggerContextPattern = regexp.MustCompile(`## Trigger\s+[a-z_]+:\s*([\s\S]*?)\n\n## Conversation History`)

type scriptedAgent struct {
	name      string
	responses []string
	delay     time.Duration
	available bool
	err       error
	mu        sync.Mutex
	calls     int
}

func (a *scriptedAgent) Name() string { return a.name }

func (a *scriptedAgent) Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error) {
	a.mu.Lock()
	index := a.calls
	a.calls++
	response := ""
	if index < len(a.responses) {
		response = a.responses[index]
	}
	a.mu.Unlock()

	if !a.available {
		return nil, fmt.Errorf("agent unavailable")
	}
	if a.err != nil {
		return nil, a.err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(a.delay):
	}
	return &AgentResult{
		RawOutput:  response,
		Summary:    response,
		DurationMs: a.delay.Milliseconds(),
	}, nil
}

func (a *scriptedAgent) ParseOutput(raw []byte) (*AgentResult, error) {
	return &AgentResult{RawOutput: string(raw), Summary: string(raw)}, nil
}

func (a *scriptedAgent) IsAvailable() bool { return a.available }

type hangingFileAgent struct {
	name      string
	filePath  string
	content   string
	available bool
}

func (a *hangingFileAgent) Name() string { return a.name }

func (a *hangingFileAgent) Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error) {
	if !a.available {
		return nil, fmt.Errorf("agent unavailable")
	}
	if observer := commandTelemetryObserverFromContext(ctx); observer != nil {
		observer.ProcessStarted(os.Getpid())
		observer.WaitStarted()
	}
	fullPath := filepath.Join(workDir, filepath.FromSlash(a.filePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(fullPath, []byte(a.content), 0o644); err != nil {
		return nil, err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (a *hangingFileAgent) ParseOutput(raw []byte) (*AgentResult, error) {
	return &AgentResult{RawOutput: string(raw), Summary: string(raw)}, nil
}

func (a *hangingFileAgent) IsAvailable() bool { return a.available }

type scriptedBrain struct {
	mode      string
	available bool
	delay     time.Duration
}

func (b *scriptedBrain) Name() string { return "brain" }

func (b *scriptedBrain) Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error) {
	if !b.available {
		return nil, fmt.Errorf("brain unavailable")
	}
	if b.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(b.delay):
		}
	}
	var raw string
	switch b.mode {
	case "goal-complete":
		raw = b.goalCompleteResponse(prompt)
	case "goal-revise":
		raw = b.goalReviseResponse(prompt)
	case "goal-rolling":
		raw = b.goalRollingResponse(prompt)
	default:
		return nil, fmt.Errorf("unknown brain mode %q", b.mode)
	}
	return &AgentResult{RawOutput: raw, Summary: raw}, nil
}

func (b *scriptedBrain) ParseOutput(raw []byte) (*AgentResult, error) {
	return &AgentResult{RawOutput: string(raw), Summary: string(raw)}, nil
}

func (b *scriptedBrain) IsAvailable() bool { return b.available }

type envelopedBrain struct {
	available bool
}

func (b *envelopedBrain) Name() string { return "brain" }

func (b *envelopedBrain) Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error) {
	if !b.available {
		return nil, fmt.Errorf("brain unavailable")
	}
	switch currentTrigger(prompt) {
	case "goal_submitted":
		summary := `{"thinking":"Plan one implementation task from the result payload.","decisions":[{"action":"update_plan","reason":"Initial plan","plan":{"phases":[{"number":1,"title":"Implementation","description":"Do the work","tasks":[{"temp_id":"impl-1","title":"Implement feature","description":"Implement the requested feature and summarize the work.","assign_to":"implementer","depends_on":[],"priority":1}]}]}}]}`
		raw := fmt.Sprintf(`[{"type":"result","is_error":false,"duration_ms":17,"result":%q}]`, summary)
		return &AgentResult{RawOutput: raw, Summary: summary, DurationMs: 17}, nil
	case "task_completed":
		taskID := firstTaskID(prompt)
		summary := fmt.Sprintf(`{"thinking":"Accept the completed task from the result payload.","decisions":[{"action":"accept_task","task_id":"%s"}]}`, taskID)
		raw := fmt.Sprintf(`[{"type":"result","is_error":false,"duration_ms":11,"result":%q}]`, summary)
		return &AgentResult{RawOutput: raw, Summary: summary, DurationMs: 11}, nil
	case "all_tasks_complete":
		summary := `{"thinking":"Goal is complete from the result payload.","decisions":[{"action":"complete_goal","summary":"Implemented the feature and accepted the task."}]}`
		raw := fmt.Sprintf(`[{"type":"result","is_error":false,"duration_ms":9,"result":%q}]`, summary)
		return &AgentResult{RawOutput: raw, Summary: summary, DurationMs: 9}, nil
	default:
		summary := `{"thinking":"No-op.","decisions":[]}`
		raw := fmt.Sprintf(`[{"type":"result","is_error":false,"duration_ms":5,"result":%q}]`, summary)
		return &AgentResult{RawOutput: raw, Summary: summary, DurationMs: 5}, nil
	}
}

func (b *envelopedBrain) ParseOutput(raw []byte) (*AgentResult, error) {
	return &AgentResult{RawOutput: string(raw), Summary: string(raw)}, nil
}

func (b *envelopedBrain) IsAvailable() bool { return b.available }

func (b *scriptedBrain) goalCompleteResponse(prompt string) string {
	switch currentTrigger(prompt) {
	case "goal_submitted":
		return `{"thinking":"Plan one implementation task.","decisions":[{"action":"update_plan","reason":"Initial plan","plan":{"phases":[{"number":1,"title":"Implementation","description":"Do the work","tasks":[{"temp_id":"impl-1","title":"Implement feature","description":"Implement the requested feature and summarize the work.","assign_to":"implementer","depends_on":[],"priority":1}]}]}}]}`
	case "task_completed":
		return fmt.Sprintf(`{"thinking":"Accept the completed task.","decisions":[{"action":"accept_task","task_id":"%s"}]}`, firstTaskID(prompt))
	case "all_tasks_complete":
		return `{"thinking":"Goal is complete.","decisions":[{"action":"complete_goal","summary":"Implemented the feature and accepted the task."}]}`
	default:
		return `{"thinking":"No-op.","decisions":[]}`
	}
}

func (b *scriptedBrain) goalReviseResponse(prompt string) string {
	switch currentTrigger(prompt) {
	case "goal_submitted":
		return `{"thinking":"Plan one implementation task.","decisions":[{"action":"update_plan","reason":"Initial plan","plan":{"phases":[{"number":1,"title":"Implementation","description":"Do the work","tasks":[{"temp_id":"impl-1","title":"Implement feature","description":"Implement the requested feature and summarize the work.","assign_to":"implementer","depends_on":[],"priority":1}]}]}}]}`
	case "task_completed":
		if strings.Contains(currentTriggerContext(prompt), "draft implementation") {
			return fmt.Sprintf(`{"thinking":"The first attempt is incomplete.","decisions":[{"action":"revise_task","task_id":"%s","feedback":"Add the missing edge-case handling and rerun the task."}]}`, firstTaskID(prompt))
		}
		return fmt.Sprintf(`{"thinking":"The revision is acceptable.","decisions":[{"action":"accept_task","task_id":"%s"}]}`, firstTaskID(prompt))
	case "all_tasks_complete":
		return `{"thinking":"Goal is complete after revision.","decisions":[{"action":"complete_goal","summary":"Implemented the feature after one revision."}]}`
	default:
		return `{"thinking":"No-op.","decisions":[]}`
	}
}

func (b *scriptedBrain) goalRollingResponse(prompt string) string {
	switch currentTrigger(prompt) {
	case "goal_submitted":
		return `{"thinking":"Plan phase one only.","decisions":[{"action":"update_plan","reason":"Rolling phase 1","plan":{"phases":[{"number":1,"title":"Design","description":"Initial phase","tasks":[{"temp_id":"phase-1","title":"Design the feature","description":"Create the initial design deliverable.","assign_to":"implementer","depends_on":[],"priority":1}]}]}}]}`
	case "task_completed":
		return fmt.Sprintf(`{"thinking":"Accept the finished phase task.","decisions":[{"action":"accept_task","task_id":"%s"}]}`, firstTaskID(prompt))
	case "all_phase_tasks_done":
		if strings.Contains(currentTriggerContext(prompt), "Phase \"Design\"") || strings.Contains(currentTriggerContext(prompt), "Phase \"Design") {
			return `{"thinking":"Plan the second rolling phase.","decisions":[{"action":"update_plan","reason":"Rolling phase 2","plan":{"phases":[{"number":1,"title":"Design","description":"Initial phase","tasks":[{"temp_id":"phase-1","title":"Design the feature","description":"Create the initial design deliverable.","assign_to":"implementer","depends_on":[],"priority":1}]},{"number":2,"title":"Implementation","description":"Second phase","tasks":[{"temp_id":"phase-2","title":"Implement the feature","description":"Implement the planned feature.","assign_to":"implementer","depends_on":["phase-1"],"priority":1}]}]}}]}`
		}
		return `{"thinking":"Rolling goal is complete.","decisions":[{"action":"complete_goal","summary":"Completed both rolling phases."}]}`
	case "all_tasks_complete":
		return `{"thinking":"Rolling goal is complete.","decisions":[{"action":"complete_goal","summary":"Completed both rolling phases."}]}`
	default:
		return `{"thinking":"No-op.","decisions":[]}`
	}
}

func firstTaskID(prompt string) string {
	if matches := completedTaskPattern.FindStringSubmatch(prompt); len(matches) == 2 {
		return matches[1]
	}
	return taskIDPattern.FindString(prompt)
}

func currentTrigger(prompt string) string {
	if matches := triggerPattern.FindStringSubmatch(prompt); len(matches) == 2 {
		return matches[1]
	}
	return ""
}

func currentTriggerContext(prompt string) string {
	if matches := triggerContextPattern.FindStringSubmatch(prompt); len(matches) == 2 {
		return matches[1]
	}
	return ""
}

func newGoalTestConfig(tempDir string) Config {
	cfg := DefaultConfig()
	cfg.Workspace.Path = tempDir
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(tempDir, "agentbridge.log")
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "impl-1",
			Provider:    "claude",
			Role:        "implementer",
			Count:       1,
			Description: "Writes production code",
		},
	}
	cfg.Agents = map[string]AgentConfig{
		"impl-1": {
			Command:        "mock",
			TimeoutSeconds: 5,
			MaxRetries:     1,
		},
	}
	return cfg
}

func TestSubmitGoalReturnsWithDeterministicWorkflow(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())

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
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"implemented feature"}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	start := time.Now()
	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "Ship feature async",
		Description: "Return immediately while the brain plans",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("SubmitGoal() took %s, want < 150ms", elapsed)
	}

	current, _, _, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalActive {
		t.Fatalf("goal status = %s, want %s immediately after submission", current.Status, GoalActive)
	}

	snapshot := coordinator.Snapshot()
	workflow, ok := snapshot["workflow"].(WorkflowState)
	if !ok {
		t.Fatalf("snapshot workflow type = %T, want WorkflowState", snapshot["workflow"])
	}
	if workflow.Mode != "deterministic" {
		t.Fatalf("workflow mode = %q, want deterministic", workflow.Mode)
	}
	if workflow.Status != "executing" {
		t.Fatalf("workflow status = %q, want executing", workflow.Status)
	}
}

func TestSubmitGoalPlansExecutesAndCompletes(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())

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
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"implemented feature"}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "Ship feature",
		Description: "Implement the requested feature",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		if err == nil && current.Status == GoalCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	current, plan, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalCompleted {
		t.Fatalf("expected goal completed, got %s with snapshot %#v", current.Status, coordinator.Snapshot())
	}
	if plan == nil || len(plan.Phases) != 1 || len(tasks) != 1 {
		t.Fatalf("expected one-phase plan with one task, got plan=%v tasks=%d", plan != nil, len(tasks))
	}
	if tasks[0].Status != TaskCompleted {
		t.Fatalf("expected planned task completed, got %s", tasks[0].Status)
	}
}

func TestSpecGoalCreatesDraftReviewAndReviseWorkflow(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-1",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Writes specifications",
		},
		{
			Name:        "review-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications",
		},
		{
			Name:        "review-claude",
			Provider:    "claude",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications from a second perspective",
		},
	}
	cfg.Agents = map[string]AgentConfig{
		"spec-1": {
			Command:        "mock",
			TimeoutSeconds: 5,
			MaxRetries:     1,
		},
		"review-codex": {
			Command:        "mock",
			TimeoutSeconds: 5,
			MaxRetries:     1,
		},
		"review-claude": {
			Command:        "mock",
			TimeoutSeconds: 5,
			MaxRetries:     1,
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

	coordinator := NewCoordinator(cfg, map[string]Agent{
		"spec-1":        &scriptedAgent{name: "spec-1", responses: []string{"initial spec", "revised spec"}, delay: 20 * time.Millisecond, available: true},
		"review-codex":  &scriptedAgent{name: "review-codex", responses: []string{"VERDICT: FAIL\n\nBLOCKERS\n- needs a clearer acceptance criteria section", "VERDICT: PASS\n\nThe spec has no unaddressed ambiguities or unanswered questions for an AI coding agent."}, delay: 20 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{"VERDICT: PASS\n\nThe spec is acceptable for round 1.", "VERDICT: PASS\n\nThe spec has no unaddressed ambiguities or unanswered questions for an AI coding agent."}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "B2 spec",
		Description: "Create and review a technical spec document",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		if err == nil && current.Status == GoalCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	current, plan, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalCompleted {
		t.Fatalf("expected goal completed, got %s with snapshot %#v", current.Status, coordinator.Snapshot())
	}
	if len(tasks) != 6 {
		t.Fatalf("expected prepare, dual review, consolidate, and final dual review tasks, got %d", len(tasks))
	}
	if plan == nil || len(plan.Phases) != 4 {
		t.Fatalf("expected four workflow phases, got %#v", plan)
	}
	statusByTitle := make(map[string]TaskStatus, len(tasks))
	for _, task := range tasks {
		statusByTitle[task.Title] = task.Status
	}
	for _, title := range []string{
		"Prepare B2 spec Specification",
		"Adversarial Review B2 spec Round 1 Reviewer 1",
		"Adversarial Review B2 spec Round 1 Reviewer 2",
		"Consolidate B2 spec Review Round 1",
		"Adversarial Review B2 spec Round 2 Reviewer 1",
		"Adversarial Review B2 spec Round 2 Reviewer 2",
	} {
		if statusByTitle[title] != TaskCompleted {
			t.Fatalf("expected %q completed, got %s", title, statusByTitle[title])
		}
	}
}

func TestGoalFailureBlocksWorkflowWithoutBrain(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())

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
		"impl-1": &scriptedAgent{name: "impl-1", responses: nil, delay: 20 * time.Millisecond, available: true, err: fmt.Errorf("execution failed")},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "Unavailable worker",
		Description: "This should fail the workflow without invoking the brain",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalBlocked
	})

	current, _, _, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalBlocked {
		t.Fatalf("expected blocked goal, got %s", current.Status)
	}
	if !strings.Contains(current.Summary, "task") {
		t.Fatalf("expected task failure summary, got %q", current.Summary)
	}
}

func TestSpecWorkflowAutoCompletesQuiescentArtifactTask(t *testing.T) {
	prevTick := workflowTickInterval
	prevQuiet := fileArtifactQuietThreshold
	workflowTickInterval = 25 * time.Millisecond
	fileArtifactQuietThreshold = 50 * time.Millisecond
	defer func() {
		workflowTickInterval = prevTick
		fileArtifactQuietThreshold = prevQuiet
	}()

	cfg := newGoalTestConfig(t.TempDir())
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-1",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Creates specifications",
		},
		{
			Name:        "review-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications",
		},
		{
			Name:        "review-claude",
			Provider:    "claude",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications from a second perspective",
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

	coordinator := NewCoordinator(cfg, map[string]Agent{
		"spec-1":        &hangingFileAgent{name: "spec-1", filePath: "specs/b2-spec-spec.md", content: "# spec", available: true},
		"review-codex":  &scriptedAgent{name: "review-codex", responses: []string{"VERDICT: PASS\n\nThe spec has no unaddressed ambiguities or unanswered questions for an AI coding agent."}, delay: 20 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{"VERDICT: PASS\n\nThe spec has no unaddressed ambiguities or unanswered questions for an AI coding agent."}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "B2 spec",
		Description: "Create and review a technical spec document",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalCompleted
	})

	current, plan, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalCompleted {
		t.Fatalf("expected completed goal, got %s", current.Status)
	}
	if plan == nil || len(plan.Phases) < 2 {
		t.Fatalf("expected review phase after artifact completion, got %#v", plan)
	}
	if len(tasks) < 3 {
		t.Fatalf("expected prepare and dual review tasks, got %d", len(tasks))
	}
	if tasks[0].Status != TaskCompleted {
		t.Fatalf("expected prepare task completed, got %s", tasks[0].Status)
	}
	if !strings.Contains(tasks[0].Result, "Expected workspace artifacts were produced") {
		t.Fatalf("expected synthetic completion summary, got %q", tasks[0].Result)
	}
}

func TestSpecWorkflowHonorsGoalReviewRoundOverride(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())
	cfg.Workflow.DefaultReviewRounds = 6
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-1",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Creates specifications",
		},
		{
			Name:        "review-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications",
		},
		{
			Name:        "review-claude",
			Provider:    "claude",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications from a second perspective",
		},
	}
	cfg.Agents = map[string]AgentConfig{
		"spec-1": {
			Command:        "mock",
			TimeoutSeconds: 5,
			MaxRetries:     1,
		},
		"review-codex": {
			Command:        "mock",
			TimeoutSeconds: 5,
			MaxRetries:     1,
		},
		"review-claude": {
			Command:        "mock",
			TimeoutSeconds: 5,
			MaxRetries:     1,
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

	coordinator := NewCoordinator(cfg, map[string]Agent{
		"spec-1":        &scriptedAgent{name: "spec-1", responses: []string{"initial spec", "revised spec"}, delay: 20 * time.Millisecond, available: true},
		"review-codex":  &scriptedAgent{name: "review-codex", responses: []string{"VERDICT: FAIL\n\nBLOCKERS\n- still ambiguous", "VERDICT: FAIL\n\nBLOCKERS\n- still ambiguous"}, delay: 20 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{"VERDICT: FAIL\n\nBLOCKERS\n- still ambiguous", "VERDICT: FAIL\n\nBLOCKERS\n- still ambiguous"}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:           "B2 spec",
		Description:     "Create and review a technical spec document",
		MaxReviewRounds: 2,
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalBlocked
	})

	current, plan, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalBlocked {
		t.Fatalf("expected blocked goal, got %s", current.Status)
	}
	if current.MaxReviewRounds != 2 {
		t.Fatalf("expected goal review rounds 2, got %d", current.MaxReviewRounds)
	}
	if plan == nil || len(plan.Phases) != 4 {
		t.Fatalf("expected prepare, dual review, consolidate, dual review phases, got %#v", plan)
	}
	if len(tasks) != 6 {
		t.Fatalf("expected 6 tasks before block, got %d", len(tasks))
	}
}

func TestSpecWorkflowReportsReviewCycleProgress(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())
	cfg.Workflow.DefaultReviewRounds = 6
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-creator-claude",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Creates specifications",
		},
		{
			Name:        "reviewer-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications",
		},
		{
			Name:        "reviewer-claude",
			Provider:    "claude",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications",
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

	coordinator := NewCoordinator(cfg, map[string]Agent{
		"spec-creator-claude": &scriptedAgent{name: "spec-creator-claude", responses: []string{"created spec"}, delay: 20 * time.Millisecond, available: true},
		"reviewer-codex":      &scriptedAgent{name: "reviewer-codex", responses: []string{"VERDICT: PASS\n\nLooks good."}, delay: 200 * time.Millisecond, available: true},
		"reviewer-claude":     &scriptedAgent{name: "reviewer-claude", responses: []string{"VERDICT: PASS\n\nLooks good."}, delay: 200 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	if _, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "B3 spec",
		Description: "Prepare and review a technical specification",
	}); err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		snapshot := coordinator.Snapshot()
		workflow, ok := snapshot["workflow"].(WorkflowState)
		return ok && workflow.Stage == "adversarial_review" && workflow.StageTaskTotal == 2
	})

	snapshot := coordinator.Snapshot()
	workflow, ok := snapshot["workflow"].(WorkflowState)
	if !ok {
		t.Fatalf("snapshot workflow type = %T, want WorkflowState", snapshot["workflow"])
	}
	if workflow.ReviewRound != 1 {
		t.Fatalf("workflow review round = %d, want 1", workflow.ReviewRound)
	}
	if workflow.CompletedReviewRounds != 0 {
		t.Fatalf("completed review rounds = %d, want 0", workflow.CompletedReviewRounds)
	}
	if workflow.StageTaskTotal != 2 {
		t.Fatalf("stage task total = %d, want 2", workflow.StageTaskTotal)
	}
}

func TestPlannerErrorDoesNotFailActiveDeterministicGoal(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())

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
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"implemented feature"}, delay: 80 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "Ship feature with planner failure",
		Description: "Complete the workflow even if a manual planner request fails",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}
	if err := coordinator.ForceReplan("try planner anyway"); err != nil {
		t.Fatalf("ForceReplan() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalCompleted
	})

	current, _, _, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalCompleted {
		t.Fatalf("expected completed goal, got %s", current.Status)
	}
}

func TestRecoverFromLogRestoresActiveGoalAndWorkflow(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())

	workspace := NewWorkspace(cfg.Workspace)
	if err := workspace.Init(); err != nil {
		t.Fatalf("workspace.Init() error = %v", err)
	}

	store, err := NewMessageStore(cfg.Log.File)
	if err != nil {
		t.Fatalf("NewMessageStore() error = %v", err)
	}

	hub := NewWebSocketHub()
	go hub.Run()

	coordinator := NewCoordinator(cfg, map[string]Agent{
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"implemented feature"}, delay: 500 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "Recover active goal",
		Description: "Persist the active goal and workflow state",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, time.Second, func() bool {
		current, _, tasks, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalActive && len(tasks) == 1 && tasks[0].Status == TaskRunning
	})

	if err := coordinator.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	_ = store.Close()
	hub.Shutdown()

	store, err = NewMessageStore(cfg.Log.File)
	if err != nil {
		t.Fatalf("NewMessageStore() reopen error = %v", err)
	}
	defer func() { _ = store.Close() }()

	hub = NewWebSocketHub()
	go hub.Run()
	defer hub.Shutdown()

	recovered := NewCoordinator(cfg, map[string]Agent{
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"implemented feature"}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	if err := recovered.RecoverFromLog(); err != nil {
		t.Fatalf("RecoverFromLog() error = %v", err)
	}

	snapshot := recovered.Snapshot()
	currentGoal, ok := snapshot["current_goal"].(*Goal)
	if !ok || currentGoal == nil {
		t.Fatalf("snapshot current_goal = %#v, want active goal", snapshot["current_goal"])
	}
	if currentGoal.ID != goal.ID {
		t.Fatalf("recovered current goal = %q, want %q", currentGoal.ID, goal.ID)
	}

	workflow, ok := snapshot["workflow"].(WorkflowState)
	if !ok {
		t.Fatalf("snapshot workflow type = %T, want WorkflowState", snapshot["workflow"])
	}
	if workflow.Status == "idle" {
		t.Fatalf("workflow status = idle, want active workflow")
	}

	plan, ok := snapshot["plan"].(*Plan)
	if !ok || plan == nil {
		t.Fatalf("snapshot plan = %#v, want restored plan", snapshot["plan"])
	}
	if len(plan.Phases) != 1 || len(plan.Phases[0].Tasks) != 1 || plan.Phases[0].Tasks[0].RealTaskID == "" {
		t.Fatalf("restored plan missing real task mapping: %#v", plan)
	}
}
