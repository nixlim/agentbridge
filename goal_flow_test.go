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
var discussionFilePattern = regexp.MustCompile(`You must write your round/phase discussion record to this workspace file: ([^\n]+)`)

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

var specWritePathPattern = regexp.MustCompile(`(?:Write the (?:specification|consolidated specification) to this (?:workspace|new version) path|Write the consolidated specification to this new version path): (\S+)`)

type hangingFileAgent struct {
	name            string
	filePath        string
	content         string
	discussionBody  string
	writeDiscussion bool
	available       bool
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
	// Determine the file path to write: prefer extracting from prompt, fall back to configured.
	writePath := a.filePath
	if matches := specWritePathPattern.FindStringSubmatch(prompt); len(matches) == 2 {
		writePath = matches[1]
	}
	fullPath := filepath.Join(workDir, filepath.FromSlash(writePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(fullPath, []byte(a.content), 0o644); err != nil {
		return nil, err
	}
	if a.writeDiscussion {
		matches := discussionFilePattern.FindStringSubmatch(prompt)
		if len(matches) == 2 {
			discussionPath := filepath.Join(workDir, filepath.FromSlash(strings.TrimSpace(matches[1])))
			if err := os.MkdirAll(filepath.Dir(discussionPath), 0o755); err != nil {
				return nil, err
			}
			body := a.discussionBody
			if body == "" {
				body = "discussion record"
			}
			if err := os.WriteFile(discussionPath, []byte(body), 0o644); err != nil {
				return nil, err
			}
		}
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

func TestSpecWorkflowSupportsCrossCritiqueRecipe(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-1",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Creates technical specifications",
		},
		{
			Name:        "review-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications for implementation readiness",
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
		"spec-1":        &scriptedAgent{name: "spec-1", responses: []string{"initial spec", "final consolidation summary"}, delay: 20 * time.Millisecond, available: true},
		"review-codex":  &scriptedAgent{name: "review-codex", responses: []string{"VERDICT: PASS\n\nThe spec has no unaddressed ambiguities or unanswered questions for an AI coding agent.", "VERDICT: PASS\n\nThe peer review is rigorous enough for the spec creator to rely on."}, delay: 20 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{"VERDICT: PASS\n\nThe spec has no unaddressed ambiguities or unanswered questions for an AI coding agent.", "VERDICT: PASS\n\nThe peer review is rigorous enough for the spec creator to rely on."}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:          "B3 spec",
		Description:    "Create and review a technical spec document",
		WorkflowRecipe: workflowRecipeSpecCrossCritiqueLoop,
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 3*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalCompleted
	})

	current, plan, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalCompleted {
		t.Fatalf("expected goal completed, got %s with snapshot %#v", current.Status, coordinator.Snapshot())
	}
	if current.WorkflowRecipe != workflowRecipeSpecCrossCritiqueLoop {
		t.Fatalf("expected workflow recipe %q, got %q", workflowRecipeSpecCrossCritiqueLoop, current.WorkflowRecipe)
	}
	if len(tasks) != 6 {
		t.Fatalf("expected prepare, dual review, dual critique, and consolidation tasks, got %d", len(tasks))
	}
	if plan == nil || len(plan.Phases) != 4 {
		t.Fatalf("expected prepare, review, critique, and consolidation phases, got %#v", plan)
	}
	if plan.Phases[2].Title != "Cross Critique Round 1" {
		t.Fatalf("expected cross critique phase, got %q", plan.Phases[2].Title)
	}
}

func TestCrossCritiqueVerdictsDoNotBlockConsolidationOutcome(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-1",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Creates technical specifications",
		},
		{
			Name:        "review-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications for implementation readiness",
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
		"spec-1": &scriptedAgent{name: "spec-1", responses: []string{
			"initial spec",
			"consolidation summary",
		}, delay: 20 * time.Millisecond, available: true},
		"review-codex": &scriptedAgent{name: "review-codex", responses: []string{
			"VERDICT: PASS\n\nThe spec has no unaddressed ambiguities or unanswered questions for an AI coding agent.",
			"VERDICT: FAIL\n\nMISSED ISSUES\n- reviewer 1 missed one weakness",
		}, delay: 20 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{
			"VERDICT: PASS\n\nThe spec has no unaddressed ambiguities or unanswered questions for an AI coding agent.",
			"VERDICT: FAIL\n\nWEAK OBJECTIONS\n- reviewer 2 over-emphasized a minor issue",
		}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:          "B4 spec",
		Description:    "Create and review a technical spec document",
		WorkflowRecipe: workflowRecipeSpecCrossCritiqueLoop,
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 3*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalCompleted
	})

	current, _, _, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalCompleted {
		t.Fatalf("expected completed goal despite critique FAIL verdicts, got %s", current.Status)
	}
}

func TestCrossCritiqueRecipeStartsWithPreparationOnly(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-1",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Creates technical specifications",
		},
		{
			Name:        "review-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications for implementation readiness",
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
		"spec-1":        &scriptedAgent{name: "spec-1", responses: []string{"created spec"}, delay: 250 * time.Millisecond, available: true},
		"review-codex":  &scriptedAgent{name: "review-codex", responses: []string{"VERDICT: PASS\n\nLooks good."}, delay: 20 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{"VERDICT: PASS\n\nLooks good."}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:          "B4 spec",
		Description:    "Create and review a technical spec document",
		WorkflowRecipe: workflowRecipeSpecCrossCritiqueLoop,
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	current, plan, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalActive {
		t.Fatalf("expected active goal immediately after submit, got %s", current.Status)
	}
	if plan == nil || len(plan.Phases) != 4 {
		t.Fatalf("expected four planned phases, got %#v", plan)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected only preparation task created initially, got %d", len(tasks))
	}
	if tasks[0].AssignedTo != "spec-1" {
		t.Fatalf("expected preparation task assigned to spec creator, got %q", tasks[0].AssignedTo)
	}
	if !strings.Contains(tasks[0].DiscussionFile, "spec-1__spec-creator__round-00__prepare-spec") {
		t.Fatalf("unexpected preparation discussion file %q", tasks[0].DiscussionFile)
	}
}

func TestReviewPromptIncludesDiscussionReferences(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-1",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Creates technical specifications",
		},
		{
			Name:        "review-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications for implementation readiness",
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
		"spec-1":        &scriptedAgent{name: "spec-1", responses: []string{"created spec"}, delay: 20 * time.Millisecond, available: true},
		"review-codex":  &scriptedAgent{name: "review-codex", responses: []string{"VERDICT: PASS\n\nLooks good."}, delay: 250 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{"VERDICT: PASS\n\nLooks good."}, delay: 250 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:          "B5 spec",
		Description:    "Create and review a technical spec document",
		WorkflowRecipe: workflowRecipeSpecCrossCritiqueLoop,
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		_, _, tasks, err := coordinator.GetGoal(goal.ID)
		if err != nil {
			return false
		}
		return len(tasks) >= 3
	})

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	var prepTask *Task
	var reviewTask *Task
	for _, task := range coordinator.tasks {
		if task.GoalID != goal.ID {
			continue
		}
		if strings.Contains(task.Title, "Prepare") {
			prepTask = task
		}
		if strings.Contains(task.Title, "Adversarial Review") && task.AssignedTo == "review-codex" {
			reviewTask = task
		}
	}
	if prepTask == nil || reviewTask == nil {
		t.Fatalf("expected prep and review tasks, got prep=%v review=%v", prepTask != nil, reviewTask != nil)
	}
	prompt := coordinator.buildAgentPromptLocked(reviewTask, coordinator.agentState[reviewTask.AssignedTo], nil)
	if !strings.Contains(prompt, prepTask.DiscussionFile) {
		t.Fatalf("expected prompt to reference dependency discussion file %q, got %q", prepTask.DiscussionFile, prompt)
	}
	if !strings.Contains(prompt, "specs/b5-spec-spec-v0.md") {
		t.Fatalf("expected prompt to reference spec file, got %q", prompt)
	}
	if !strings.Contains(prompt, reviewTask.DiscussionFile) {
		t.Fatalf("expected prompt to reference current discussion file %q, got %q", reviewTask.DiscussionFile, prompt)
	}
}

func TestBlockedGoalDoesNotBlockNextGoalOrLeakPlanState(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())
	cfg.Team = []TeamMemberConfig{
		{
			Name:        "spec-1",
			Provider:    "claude",
			Role:        "spec_creator",
			Count:       1,
			Description: "Creates technical specifications",
		},
		{
			Name:        "review-codex",
			Provider:    "codex",
			Role:        "reviewer",
			Count:       1,
			Description: "Reviews specifications for implementation readiness",
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
		"spec-1": &scriptedAgent{name: "spec-1", responses: []string{
			"first spec draft",
			"first consolidation",
			"second spec draft",
		}, delay: 20 * time.Millisecond, available: true},
		"review-codex": &scriptedAgent{name: "review-codex", responses: []string{
			"VERDICT: FAIL\n\nissue from codex",
			"VERDICT: PASS\n\nThe peer review is rigorous enough for the spec creator to rely on.",
		}, delay: 20 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{
			"VERDICT: FAIL\n\nissue from claude",
			"VERDICT: PASS\n\nThe peer review is rigorous enough for the spec creator to rely on.",
		}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	firstGoal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:           "Blocked spec",
		Description:     "Create and review a technical spec document",
		WorkflowRecipe:  workflowRecipeSpecCrossCritiqueLoop,
		MaxReviewRounds: 1,
	})
	if err != nil {
		t.Fatalf("SubmitGoal(first) error = %v", err)
	}

	waitFor(t, 3*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(firstGoal.ID)
		return err == nil && current.Status == GoalBlocked
	})

	secondGoal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:          "Fresh spec",
		Description:    "Create and review a technical spec document",
		WorkflowRecipe: workflowRecipeSpecCrossCritiqueLoop,
	})
	if err != nil {
		t.Fatalf("SubmitGoal(second) error = %v", err)
	}

	current, plan, tasks, err := coordinator.GetGoal(secondGoal.ID)
	if err != nil {
		t.Fatalf("GetGoal(second) error = %v", err)
	}
	if current.Status != GoalActive {
		t.Fatalf("expected second goal active, got %s", current.Status)
	}
	if plan == nil || len(plan.Phases) != 4 {
		t.Fatalf("expected second goal plan with four phases, got %#v", plan)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected only preparation task created for second goal, got %d", len(tasks))
	}
	if tasks[0].AssignedTo != "spec-1" {
		t.Fatalf("expected second goal prep assigned to spec creator, got %q", tasks[0].AssignedTo)
	}
}

func TestResumeBlockedGoalWithIncompletePlan(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())

	worker := &scriptedAgent{name: "impl-1", responses: []string{"implemented after resume"}, delay: 20 * time.Millisecond, available: true, err: fmt.Errorf("temporary failure")}

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
		"impl-1": worker,
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "Resume me",
		Description: "Complete the workflow after a blocked start",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalBlocked
	})

	worker.err = nil
	if err := coordinator.ResumeGoal(goal.ID); err != nil {
		t.Fatalf("ResumeGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalCompleted
	})

	current, _, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalCompleted {
		t.Fatalf("expected completed goal after resume, got %s", current.Status)
	}
	if len(tasks) != 1 || tasks[0].Status != TaskCompleted {
		t.Fatalf("expected completed draft task after resume, got %#v", tasks)
	}
}

func TestStopAndStartGoalWithIncompletePlan(t *testing.T) {
	cfg := newGoalTestConfig(t.TempDir())

	worker := &scriptedAgent{name: "impl-1", responses: []string{"implemented after restart"}, delay: 250 * time.Millisecond, available: true}

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
		"impl-1": worker,
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "Stop me",
		Description: "Pause and restart the workflow",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, tasks, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalActive && len(tasks) == 1 && tasks[0].Status == TaskRunning
	})

	if err := coordinator.StopGoal(goal.ID); err != nil {
		t.Fatalf("StopGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, tasks, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalStopped && len(tasks) == 1 && tasks[0].Status == TaskCancelled
	})

	if err := coordinator.StartGoal(goal.ID); err != nil {
		t.Fatalf("StartGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, _, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalCompleted
	})
}

func TestDeleteGoalRemovesState(t *testing.T) {
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
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"implemented feature"}, delay: 500 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() { _ = coordinator.Stop(context.Background()) }()

	goal, err := coordinator.SubmitGoal(CreateGoalRequest{
		Title:       "Delete me",
		Description: "Remove this workflow from the system",
	})
	if err != nil {
		t.Fatalf("SubmitGoal() error = %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		current, _, tasks, err := coordinator.GetGoal(goal.ID)
		return err == nil && current.Status == GoalActive && len(tasks) == 1
	})

	if err := coordinator.StopGoal(goal.ID); err != nil {
		t.Fatalf("StopGoal() error = %v", err)
	}
	if err := coordinator.DeleteGoal(goal.ID); err != nil {
		t.Fatalf("DeleteGoal() error = %v", err)
	}

	if _, _, _, err := coordinator.GetGoal(goal.ID); err == nil {
		t.Fatalf("expected deleted goal lookup to fail")
	}
	if len(coordinator.ListGoals()) != 0 {
		t.Fatalf("expected deleted goal to be removed from goal list")
	}
	if len(coordinator.ListTasks()) != 0 {
		t.Fatalf("expected deleted goal tasks to be removed")
	}
	if current, ok := coordinator.Snapshot()["current_goal"].(*Goal); !ok || current != nil {
		t.Fatalf("expected no current goal after delete, got %#v", coordinator.Snapshot()["current_goal"])
	}
	if plan := coordinator.CurrentPlan(); plan != nil {
		t.Fatalf("expected no current plan after delete, got %#v", plan)
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
		"spec-1":        &hangingFileAgent{name: "spec-1", filePath: "specs/b2-spec-spec-v0.md", content: "# spec", discussionBody: "prepared spec", writeDiscussion: true, available: true},
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

func TestSpecWorkflowDoesNotAutoCompleteArtifactTaskWithoutDiscussionRecord(t *testing.T) {
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
		"spec-1":        &hangingFileAgent{name: "spec-1", filePath: "specs/b2-spec-spec.md", content: "# spec", writeDiscussion: false, available: true},
		"review-codex":  &scriptedAgent{name: "review-codex", responses: []string{"VERDICT: PASS"}, delay: 20 * time.Millisecond, available: true},
		"review-claude": &scriptedAgent{name: "review-claude", responses: []string{"VERDICT: PASS"}, delay: 20 * time.Millisecond, available: true},
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

	time.Sleep(300 * time.Millisecond)

	current, plan, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalActive {
		t.Fatalf("expected active goal while prepare task remains in progress, got %s", current.Status)
	}
	if plan == nil || len(plan.Phases) == 0 {
		t.Fatalf("expected plan to remain present, got %#v", plan)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected only prepare task before review starts, got %d", len(tasks))
	}
	if tasks[0].Status != TaskRunning {
		t.Fatalf("expected prepare task to stay running without discussion record, got %s", tasks[0].Status)
	}
}

func TestSnapshotUsesBlockedGoalWithIncompletePlanAsCurrentGoal(t *testing.T) {
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

	coordinator := NewCoordinator(cfg, map[string]Agent{}, nil, workspace, store, hub)
	goal := &Goal{
		ID:      "goal-blocked",
		Title:   "Blocked goal",
		Status:  GoalBlocked,
		Summary: "needs resume",
	}
	task := &Task{
		ID:         "task-pending",
		GoalID:     goal.ID,
		Title:      "Pending work",
		AssignedTo: "impl-1",
		Status:     TaskPending,
		CreatedAt:  time.Now().UTC(),
	}
	plan := &Plan{
		ID:     "plan-1",
		GoalID: goal.ID,
		Phases: []Phase{{
			Number: 1,
			Title:  "Phase 1",
			Tasks: []PlannedTask{{
				TempID:     "p1",
				Title:      task.Title,
				AssignTo:   task.AssignedTo,
				RealTaskID: task.ID,
			}},
		}},
	}

	coordinator.mu.Lock()
	coordinator.goals[goal.ID] = goal
	coordinator.goalOrder = append(coordinator.goalOrder, goal.ID)
	coordinator.tasks[task.ID] = task
	coordinator.taskOrder = append(coordinator.taskOrder, task.ID)
	coordinator.brainState.CurrentPlan = plan
	coordinator.planExecutor = NewPlanExecutor(plan)
	coordinator.mu.Unlock()

	snapshot := coordinator.Snapshot()
	currentGoal, ok := snapshot["current_goal"].(*Goal)
	if !ok || currentGoal == nil {
		t.Fatalf("expected blocked goal to surface as current_goal, got %#v", snapshot["current_goal"])
	}
	if currentGoal.ID != goal.ID {
		t.Fatalf("expected current_goal %q, got %q", goal.ID, currentGoal.ID)
	}

	workflow, ok := snapshot["workflow"].(WorkflowState)
	if !ok {
		t.Fatalf("expected workflow state, got %#v", snapshot["workflow"])
	}
	if workflow.Status != string(GoalBlocked) {
		t.Fatalf("expected blocked workflow state, got %q", workflow.Status)
	}
}

func TestSpecWorkflowHonorsGoalReviewRoundOverride(t *testing.T) {
	prevTick := workflowTickInterval
	prevQuiet := fileArtifactQuietThreshold
	workflowTickInterval = 25 * time.Millisecond
	fileArtifactQuietThreshold = 50 * time.Millisecond
	defer func() {
		workflowTickInterval = prevTick
		fileArtifactQuietThreshold = prevQuiet
	}()

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
		"spec-1":        &hangingFileAgent{name: "spec-1", filePath: "specs/b2-spec-spec.md", content: "# revised spec", discussionBody: "consolidated discussion", writeDiscussion: true, available: true},
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

func TestRecoverFromLogResumeBlockedGoalContinuesFromNextPhase(t *testing.T) {
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

	coordinator := NewCoordinator(cfg, map[string]Agent{}, nil, workspace, store, hub)

	goal := &Goal{
		ID:             "goal-blocked-recover",
		Title:          "Resume later phase",
		Description:    "Should continue from phase 2",
		WorkflowRecipe: workflowRecipeSpecReviewLoop,
		Status:         GoalBlocked,
		Summary:        "blocked awaiting resume",
		CreatedAt:      time.Now().UTC(),
	}
	phaseOneTask := &Task{
		ID:         "task-phase-1",
		GoalID:     goal.ID,
		Title:      "Prepare Spec",
		AssignedTo: "impl-1",
		Status:     TaskCompleted,
		CreatedAt:  time.Now().UTC(),
	}
	plan := &Plan{
		ID:     "plan-recover",
		GoalID: goal.ID,
		Phases: []Phase{
			{
				Number: 1,
				Title:  "Prepare Spec",
				Tasks: []PlannedTask{{
					TempID:     "prepare-spec-r1",
					Title:      phaseOneTask.Title,
					AssignTo:   phaseOneTask.AssignedTo,
					RealTaskID: phaseOneTask.ID,
				}},
			},
			{
				Number: 2,
				Title:  "Review Spec",
				Tasks: []PlannedTask{{
					TempID:     "review-spec-r1-1",
					Title:      "Review Spec",
					AssignTo:   "impl-1",
					DependsOn:  []string{"prepare-spec-r1"},
				}},
			},
		},
		CreatedAt: time.Now().UTC(),
		Version:   1,
	}

	coordinator.mu.Lock()
	coordinator.goals[goal.ID] = goal
	coordinator.goalOrder = append(coordinator.goalOrder, goal.ID)
	coordinator.tasks[phaseOneTask.ID] = phaseOneTask
	coordinator.taskOrder = append(coordinator.taskOrder, phaseOneTask.ID)
	coordinator.brainState.CurrentPlan = plan
	coordinator.planExecutor = NewPlanExecutor(plan)
	goal.PlanID = plan.ID
	coordinator.recordGoalLocked(goal, "goal blocked")
	coordinator.recordTaskLocked(phaseOneTask, "task completed")
	coordinator.recordPlanLocked(plan, "coordinator", "plan updated")
	coordinator.mu.Unlock()

	if err := store.Close(); err != nil {
		t.Fatalf("logStore.Close() error = %v", err)
	}
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
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"done"}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	if err := recovered.RecoverFromLog(); err != nil {
		t.Fatalf("RecoverFromLog() error = %v", err)
	}

	if recovered.planExecutor == nil {
		t.Fatalf("expected recovered plan executor")
	}
	if !recovered.planExecutor.executedPhases[0] {
		t.Fatalf("expected phase 1 to be marked executed after recovery")
	}
	if next := recovered.nextUnexecutedPhaseLocked(); next != 1 {
		t.Fatalf("nextUnexecutedPhaseLocked() = %d, want 1", next)
	}

	if err := recovered.ResumeGoal(goal.ID); err != nil {
		t.Fatalf("ResumeGoal() error = %v", err)
	}

	current, planAfter, tasks, err := recovered.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalActive {
		t.Fatalf("expected goal active after resume, got %s", current.Status)
	}
	if planAfter == nil || len(planAfter.Phases) != 2 {
		t.Fatalf("expected recovered plan with 2 phases, got %#v", planAfter)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected phase 2 task to be created on resume without duplicating phase 1, got %d tasks", len(tasks))
	}
	if tasks[0].ID != phaseOneTask.ID {
		t.Fatalf("unexpected task set after resume: %#v", tasks)
	}
	if tasks[1].Title != "Review Spec" || tasks[1].Status != TaskPending {
		t.Fatalf("expected resumed phase 2 task to be created pending, got %#v", tasks[1])
	}
}

func TestResumeGoalDoesNotRetryCancelledTasksFromEarlierPhases(t *testing.T) {
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
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"reviewed after resume"}, delay: 20 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)

	goal := &Goal{
		ID:         "goal-resume-phase-scope",
		Title:      "Resume phase scope",
		Status:     GoalBlocked,
		Summary:    "blocked before phase 2",
		CreatedAt:  time.Now().UTC(),
		PlanID:     "plan-phase-scope",
	}
	completedAt := time.Now().UTC()
	oldCancelled := &Task{
		ID:          "task-old-prepare",
		GoalID:      goal.ID,
		Title:       "Prepare Spec",
		AssignedTo:  "impl-1",
		Status:      TaskCancelled,
		Result:      "stopped with goal",
		CompletedAt: &completedAt,
		CreatedAt:   time.Now().UTC(),
		PlanPhase:   1,
	}
	phaseOneCompleted := &Task{
		ID:        "task-phase-1-complete",
		GoalID:    goal.ID,
		Title:     "Consolidate Review Round 3",
		AssignedTo:"impl-1",
		Status:    TaskCompleted,
		CreatedAt: time.Now().UTC(),
		PlanPhase: 10,
	}
	plan := &Plan{
		ID:     goal.PlanID,
		GoalID: goal.ID,
		Phases: []Phase{
			{
				Number: 10,
				Title:  "Consolidate Review Round 3",
				Tasks: []PlannedTask{{
					TempID:     "consolidate-spec-r3",
					Title:      phaseOneCompleted.Title,
					AssignTo:   "impl-1",
					RealTaskID: phaseOneCompleted.ID,
				}},
			},
			{
				Number: 11,
				Title:  "Adversarial Review Round 4",
				Tasks: []PlannedTask{{
					TempID:    "review-spec-r4-1",
					Title:     "Adversarial Review Round 4 Reviewer 1",
					AssignTo:  "impl-1",
					DependsOn: []string{"consolidate-spec-r3"},
				}},
			},
		},
		CreatedAt: time.Now().UTC(),
		Version:   1,
	}

	coordinator.mu.Lock()
	coordinator.goals[goal.ID] = goal
	coordinator.goalOrder = append(coordinator.goalOrder, goal.ID)
	coordinator.tasks[oldCancelled.ID] = oldCancelled
	coordinator.tasks[phaseOneCompleted.ID] = phaseOneCompleted
	coordinator.taskOrder = append(coordinator.taskOrder, oldCancelled.ID, phaseOneCompleted.ID)
	coordinator.brainState.CurrentPlan = plan
	coordinator.planExecutor = NewPlanExecutor(plan)
	coordinator.planExecutor.executedPhases[0] = true
	coordinator.planExecutor.currentPhase = 0
	coordinator.planExecutor.tempToReal["consolidate-spec-r3"] = phaseOneCompleted.ID
	coordinator.recordGoalLocked(goal, "goal blocked")
	coordinator.recordPlanLocked(plan, "coordinator", "plan updated")
	coordinator.mu.Unlock()

	if err := coordinator.ResumeGoal(goal.ID); err != nil {
		t.Fatalf("ResumeGoal() error = %v", err)
	}

	current, currentPlan, tasks, err := coordinator.GetGoal(goal.ID)
	if err != nil {
		t.Fatalf("GetGoal() error = %v", err)
	}
	if current.Status != GoalActive {
		t.Fatalf("expected active goal after resume, got %s", current.Status)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected old cancelled task plus one completed and one resumed phase task, got %d", len(tasks))
	}
	if tasks[0].ID != oldCancelled.ID || tasks[0].Status != TaskCancelled {
		t.Fatalf("expected old cancelled task to remain cancelled, got %#v", tasks[0])
	}
	if tasks[2].Title != "Adversarial Review Round 4 Reviewer 1" || tasks[2].Status != TaskPending {
		t.Fatalf("expected only phase-11 task to be created pending, got %#v", tasks[2])
	}
	if currentPlan == nil || currentPlan.Phases[1].Tasks[0].RealTaskID == "" {
		t.Fatalf("expected resumed phase task mapping to be written into plan, got %#v", currentPlan)
	}
}
