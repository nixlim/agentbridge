package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// Naming note: The coordinator was originally built around an LLM-driven "brain"
// that made planning decisions. The architecture has since shifted to a deterministic
// workflow model where the coordinator follows fixed recipes (spec→review loops, etc.).
//
// Legacy "brain" names that map to current concepts:
//   brainState        → workflow/plan state (BrainState holds the current plan and conversation history)
//   brainAdapter      → LLM adapter used only for non-deterministic planning styles (upfront/rolling)
//   brainSystemPrompt → system prompt for the LLM adapter (non-deterministic paths only)
//   brainQueue        → follow-up action queue for the workflow tick loop
//   brainFollowUp     → a queued follow-up action (trigger + context)
//   brainCancel       → cancel func for an in-flight LLM brain call (non-deterministic paths)
//   brainLoop         → the main workflow tick loop that drives plan execution
//
// These names are preserved to avoid a risky rename across 3000+ lines.
// TODO: Consider a phased rename once the non-deterministic planning paths are removed.

const (
	defaultSpecOutputDir = "specs"
)

var (
	workflowTickInterval       = 5 * time.Second
	fileArtifactQuietThreshold = 90 * time.Second
)

type dispatchResult struct {
	TaskID     string
	AgentName  string
	Result     *AgentResult
	Err        error
	RetryCount int
}

type brainFollowUp struct {
	Trigger string
	Context string
}

type Coordinator struct {
	config            Config
	agents            map[string]Agent
	brainAdapter      Agent
	brainSystemPrompt string
	agentState        map[string]*AgentState
	tasks             map[string]*Task
	taskOrder         []string
	messages          []*Message
	pendingInbox      map[string][]string
	goals             map[string]*Goal
	goalOrder         []string
	currentGoalID     string
	brainState        BrainState
	planExecutor      *PlanExecutor
	logStore          *MessageStore
	workspace         *Workspace
	hub               *WebSocketHub
	results           chan dispatchResult
	dispatchWake      chan struct{}
	brainQueue        chan brainFollowUp
	stop              chan struct{}
	wg                sync.WaitGroup
	mu                sync.RWMutex
	activeCancels     map[string]context.CancelFunc
	brainCancel       context.CancelFunc
	workspaceLocks    *WorkspaceLockManager
	shuttingDown      bool
}

func NewCoordinator(cfg Config, agents map[string]Agent, brainAdapter Agent, workspace *Workspace, logStore *MessageStore, hub *WebSocketHub) *Coordinator {
	return &Coordinator{
		config:            cfg,
		agents:            agents,
		brainAdapter:      brainAdapter,
		brainSystemPrompt: loadBrainSystemPrompt(cfg.Brain),
		agentState:        newInitialAgentStates(cfg, agents),
		tasks:             map[string]*Task{},
		taskOrder:         []string{},
		messages:          []*Message{},
		pendingInbox:      map[string][]string{},
		goals:             map[string]*Goal{},
		goalOrder:         []string{},
		brainState: BrainState{
			ConversationHistory: []BrainMessage{},
			ActiveProvider:      cfg.Brain.Provider,
		},
		logStore:       logStore,
		workspace:      workspace,
		hub:            hub,
		results:        make(chan dispatchResult, 128),
		dispatchWake:   make(chan struct{}, 1),
		brainQueue:     make(chan brainFollowUp, 64),
		stop:           make(chan struct{}),
		activeCancels:  map[string]context.CancelFunc{},
		workspaceLocks: NewWorkspaceLockManager(),
	}
}

func (c *Coordinator) Start() {
	c.wg.Add(1)
	go c.eventLoop()
}

func (c *Coordinator) Stop(ctx context.Context) error {
	c.mu.Lock()
	c.shuttingDown = true
	for _, cancel := range c.activeCancels {
		cancel()
	}
	c.mu.Unlock()

	close(c.stop)

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (c *Coordinator) RecoverFromLog() error {
	messages, err := c.logStore.RecoverMessages()
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, msg := range messages {
		c.messages = append(c.messages, msg)
		if msg.Metadata.Task != nil {
			task := msg.Metadata.Task.Clone()
			c.tasks[task.ID] = task
			if !containsString(c.taskOrder, task.ID) {
				c.taskOrder = append(c.taskOrder, task.ID)
			}
		}
		if msg.Metadata.Agent != nil {
			agent := *msg.Metadata.Agent
			c.agentState[agent.Name] = &agent
		}
		if msg.Metadata.Goal != nil {
			goal := *msg.Metadata.Goal
			c.goals[goal.ID] = &goal
			if !containsString(c.goalOrder, goal.ID) {
				c.goalOrder = append(c.goalOrder, goal.ID)
			}
		}
		if msg.Metadata.Plan != nil {
			plan := *msg.Metadata.Plan
			c.brainState.CurrentPlan = &plan
			c.planExecutor = NewPlanExecutor(&plan)
		}
	}
	if c.brainState.CurrentPlan != nil && c.planExecutor != nil {
		c.mergeExistingPlanStateLocked(c.brainState.CurrentPlan, c.planExecutor)
	}
	for i := len(c.goalOrder) - 1; i >= 0; i-- {
		goal := c.goals[c.goalOrder[i]]
		if goal == nil {
			continue
		}
		if goal.Status == GoalPlanning || goal.Status == GoalActive || goal.Status == GoalGated {
			c.currentGoalID = goal.ID
			break
		}
		if (goal.Status == GoalStopped || goal.Status == GoalBlocked || goal.Status == GoalFailed) && c.goalHasIncompletePlanLocked(goal.ID) {
			c.currentGoalID = goal.ID
			break
		}
	}
	for _, taskID := range c.taskOrder {
		task := c.tasks[taskID]
		if task == nil || task.Status != TaskRunning {
			continue
		}
		if c.recoveredTaskStillActiveLocked(task) {
			continue
		}
		task.Status = TaskPending
		task.StartedAt = nil
		task.Result = firstNonEmpty(task.Result, "recovered task was re-queued because the previous worker process was not active")
	}
	for name, state := range c.agentState {
		if state == nil || state.Status != AgentBusy {
			continue
		}
		task := c.tasks[state.CurrentTask]
		if task != nil && task.Status == TaskRunning && c.recoveredTaskStillActiveLocked(task) {
			continue
		}
		state.Status = AgentIdle
		state.CurrentTask = ""
		state.LastActivity = time.Now().UTC()
		c.agentState[name] = state
	}
	return nil
}

func (c *Coordinator) Snapshot() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshotLocked()
}

func (c *Coordinator) ListTasks() []*Task {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cloneTasksLocked()
}

func (c *Coordinator) ListGoals() []*Goal {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Goal, 0, len(c.goalOrder))
	for _, id := range c.goalOrder {
		if goal := c.goals[id]; goal != nil {
			clone := *goal
			out = append(out, &clone)
		}
	}
	return out
}

func (c *Coordinator) GetGoal(goalID string) (*Goal, *Plan, []*Task, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	goal, ok := c.goals[goalID]
	if !ok {
		return nil, nil, nil, errors.New("goal not found")
	}
	var plan *Plan
	if c.brainState.CurrentPlan != nil && c.brainState.CurrentPlan.GoalID == goalID {
		clone := *c.brainState.CurrentPlan
		plan = &clone
	}
	tasks := make([]*Task, 0)
	for _, taskID := range c.taskOrder {
		if task := c.tasks[taskID]; task != nil && task.GoalID == goalID {
			tasks = append(tasks, task.Clone())
		}
	}
	goalClone := *goal
	return &goalClone, plan, tasks, nil
}

func (c *Coordinator) CurrentPlan() *Plan {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.brainState.CurrentPlan == nil {
		return nil
	}
	clone := *c.brainState.CurrentPlan
	return &clone
}

func (c *Coordinator) goalHasIncompletePlanLocked(goalID string) bool {
	if goalID == "" || c.brainState.CurrentPlan == nil || c.planExecutor == nil {
		return false
	}
	if c.brainState.CurrentPlan.GoalID != goalID {
		return false
	}
	return !c.allPlannedTasksCompletedLocked()
}

func (c *Coordinator) BrainHistory() []BrainMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]BrainMessage(nil), c.brainState.ConversationHistory...)
}

func (c *Coordinator) GetTask(taskID string) (*Task, []*Message, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	task, ok := c.tasks[taskID]
	if !ok {
		return nil, nil, errors.New("task not found")
	}
	related := make([]*Message, 0)
	for _, msg := range c.messages {
		if msg.TaskID == taskID {
			related = append(related, msg)
		}
	}
	return task.Clone(), related, nil
}

func (c *Coordinator) ListMessages(limit, offset int, agent string) []*Message {
	c.mu.RLock()
	defer c.mu.RUnlock()

	filtered := make([]*Message, 0, len(c.messages))
	for _, msg := range c.messages {
		if agent != "" && msg.From != agent && msg.To != agent {
			continue
		}
		filtered = append(filtered, msg)
	}
	if offset >= len(filtered) {
		return []*Message{}
	}
	end := len(filtered)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return append([]*Message(nil), filtered[offset:end]...)
}

func (c *Coordinator) CreateTask(req CreateTaskRequest) (*Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.shuttingDown {
		return nil, errors.New("coordinator is shutting down")
	}
	task, err := c.createTaskLocked(req)
	if err != nil {
		return nil, err
	}
	c.recordTaskLocked(task, "task created")
	c.signalDispatchLocked()
	return task.Clone(), nil
}

func (c *Coordinator) SubmitGoal(req CreateGoalRequest) (*Goal, error) {
	if strings.TrimSpace(req.Title) == "" && strings.TrimSpace(req.Description) == "" {
		return nil, errors.New("goal title or description is required")
	}

	c.mu.Lock()
	if c.shuttingDown {
		c.mu.Unlock()
		return nil, errors.New("coordinator is shutting down")
	}
	if active := c.currentGoalLocked(); active != nil && (active.Status == GoalPlanning || active.Status == GoalActive || active.Status == GoalGated) {
		c.mu.Unlock()
		return nil, errors.New("an active goal already exists")
	}

	goal := &Goal{
		ID:              uuid.NewString(),
		Title:           strings.TrimSpace(req.Title),
		Description:     strings.TrimSpace(req.Description),
		WorkflowRecipe:  c.resolveGoalWorkflowRecipe(req.WorkflowRecipe),
		MaxReviewRounds: c.resolveGoalReviewRounds(req.MaxReviewRounds),
		Status:          GoalPlanning,
		CreatedAt:       time.Now().UTC(),
	}
	c.goals[goal.ID] = goal
	c.goalOrder = append(c.goalOrder, goal.ID)
	c.currentGoalID = goal.ID
	c.brainState.GoalDescription = strings.TrimSpace(strings.Join([]string{goal.Title, goal.Description}, "\n\n"))
	c.recordGoalLocked(goal, "goal submitted")
	plan, err := c.buildDeterministicPlanLocked(goal)
	if err != nil {
		c.failGoalLocked(goal, fmt.Sprintf("workflow planning failed: %v", err), "goal failed")
		c.mu.Unlock()
		return nil, err
	}
	if err := c.updatePlanLocked(plan, "coordinator", "deterministic workflow plan created"); err != nil {
		c.failGoalLocked(goal, fmt.Sprintf("workflow planning failed: %v", err), "goal failed")
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()

	c.mu.RLock()
	defer c.mu.RUnlock()
	clone := *c.goals[goal.ID]
	return &clone, nil
}

func (c *Coordinator) StartGoal(goalID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resumeGoalLocked(goalID, "goal started")
}

func (c *Coordinator) SendHumanMessage(to, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	task, err := c.createTaskLocked(CreateTaskRequest{
		Title:       humanMessageTaskTitle(to, content),
		Description: fmt.Sprintf("Respond directly to the human's message below.\n\n%s", strings.TrimSpace(content)),
		AssignedTo:  to,
	})
	if err != nil {
		return err
	}
	task.IsHumanMessage = true

	humanMsg := NewMessage(MsgHumanToCoordinator, "human", "coordinator", task.ID, content)
	c.appendMessageLocked(humanMsg)
	c.recordTaskLocked(task, "message queued")
	c.signalDispatchLocked()
	return nil
}

func (c *Coordinator) SendBrainMessage(content string) error {
	c.mu.Lock()
	active := c.currentGoalLocked()
	if active == nil {
		c.mu.Unlock()
		return errors.New("no active goal")
	}
	c.brainState.PendingHumanInput = nil
	c.mu.Unlock()
	c.enqueueBrainCycle("human_message", strings.TrimSpace(content))
	return nil
}

func (c *Coordinator) ForceReplan(guidance string) error {
	c.mu.RLock()
	active := c.currentGoalLocked()
	c.mu.RUnlock()
	if active == nil {
		return errors.New("no active goal")
	}
	c.enqueueBrainCycle("force_replan", strings.TrimSpace(guidance))
	return nil
}

func (c *Coordinator) OverridePlan(plan *Plan, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updatePlanLocked(plan, "coordinator", firstNonEmpty(reason, "manual plan override"))
}

func (c *Coordinator) SwitchBrainProvider(provider string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.config.Providers[provider]; !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	next := c.config
	next.Brain.Provider = provider
	next.Brain.Command = next.configuredBrainCommand(provider)
	next.Brain.Args = append([]string(nil), next.configuredBrainArgs(provider)...)
	next.Brain.TimeoutSeconds = next.configuredBrainTimeout(provider)
	adapter, err := instantiateBrainAdapter(next, c.workspace.Path())
	if err != nil {
		return err
	}
	c.config = next
	c.brainAdapter = adapter
	c.brainState.ActiveProvider = provider
	msg := NewMessage(MsgSystemEvent, "brain", "human", "", fmt.Sprintf("brain provider switched to %s", provider))
	c.appendMessageLocked(msg)
	return nil
}

func (c *Coordinator) PauseAgent(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.agentState[name]
	if !ok {
		return errors.New("agent not found")
	}
	if state.Status == AgentBusy {
		return errors.New("cannot pause a busy agent")
	}
	state.Status = AgentPaused
	state.LastActivity = time.Now().UTC()
	c.recordAgentLocked(state, "agent paused")
	return nil
}

func (c *Coordinator) ResumeAgent(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.agentState[name]
	if !ok {
		return errors.New("agent not found")
	}
	if agent, exists := c.agents[name]; !exists || !agent.IsAvailable() {
		state.Status = AgentOffline
	} else {
		state.Status = AgentIdle
	}
	state.LastActivity = time.Now().UTC()
	c.recordAgentLocked(state, "agent resumed")
	c.signalDispatchLocked()
	return nil
}

func (c *Coordinator) ResumeGoal(goalID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resumeGoalLocked(goalID, "goal resumed")
}

func (c *Coordinator) resumeGoalLocked(goalID string, record string) error {
	goal, ok := c.goals[goalID]
	if !ok {
		return errors.New("goal not found")
	}
	switch goal.Status {
	case GoalStopped, GoalBlocked, GoalGated:
	case GoalFailed:
		if strings.TrimSpace(goal.Summary) != "killed by human" && !c.goalHasIncompletePlanLocked(goalID) {
			return errors.New("goal cannot be resumed from failed state")
		}
	default:
		return fmt.Errorf("goal is not resumable from state %q", goal.Status)
	}
	if !c.goalHasIncompletePlanLocked(goalID) {
		return errors.New("goal has no incomplete plan to resume")
	}

	goal.Status = GoalActive
	goal.Summary = ""
	goal.CompletedAt = nil
	goal.ActiveGate = nil
	c.currentGoalID = goalID
	c.brainState.PendingHumanInput = nil
	c.recordGoalLocked(goal, record)

	incompletePhaseIndex := c.currentIncompletePhaseIndexLocked()
	nextPhaseIndex := -1
	if c.planExecutor != nil && c.brainState.CurrentPlan != nil && c.brainState.CurrentPlan.GoalID == goalID {
		nextPhaseIndex = c.nextUnexecutedPhaseLocked()
	}
	var resumePhaseNumber int
	if incompletePhaseIndex >= 0 && c.brainState.CurrentPlan != nil && incompletePhaseIndex < len(c.brainState.CurrentPlan.Phases) {
		resumePhaseNumber = c.brainState.CurrentPlan.Phases[incompletePhaseIndex].Number
	}

	if resumePhaseNumber != 0 {
		for _, task := range c.tasks {
			if task.GoalID != goalID || task.PlanPhase != resumePhaseNumber {
				continue
			}
			switch task.Status {
			case TaskFailed, TaskCancelled:
				if err := task.Retry(); err == nil {
					task.ErrorOutput = ""
					task.Result = ""
					task.ReviewReason = ""
					c.recordTaskLocked(task, "task re-queued by goal resume")
				}
			case TaskBlocked:
				if err := task.MarkPending(); err == nil {
					task.Result = ""
					c.recordTaskLocked(task, "task unblocked by goal resume")
				}
			}
			if state, ok := c.agentState[task.AssignedTo]; ok {
				if agent, exists := c.agents[task.AssignedTo]; !exists || !agent.IsAvailable() {
					state.Status = AgentOffline
				} else if state.Status != AgentBusy {
					state.Status = AgentIdle
				}
				if state.CurrentTask == task.ID && state.Status != AgentBusy {
					state.CurrentTask = ""
				}
				state.LastActivity = time.Now().UTC()
				c.recordAgentLocked(state, "agent reset by goal resume")
			}
		}
	}

	if c.planExecutor != nil && c.brainState.CurrentPlan != nil && c.brainState.CurrentPlan.GoalID == goalID {
		if nextPhaseIndex >= 0 {
			if err := c.planExecutor.ExecutePhase(c, nextPhaseIndex); err != nil {
				return err
			}
			c.recordPlanLocked(c.brainState.CurrentPlan, "coordinator", "plan resumed")
		}
	}
	c.signalDispatchLocked()
	return nil
}

func (c *Coordinator) StopGoal(goalID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	goal, ok := c.goals[goalID]
	if !ok {
		return errors.New("goal not found")
	}
	if goal.Status == GoalCompleted {
		return errors.New("completed goal cannot be stopped")
	}
	if goal.Status == GoalStopped {
		return errors.New("goal is already stopped")
	}
	goal.Status = GoalStopped
	goal.Summary = "stopped by human"
	goal.CompletedAt = nil
	if c.currentGoalID == goalID {
		c.currentGoalID = ""
		c.brainState.PendingHumanInput = nil
	}
	for _, task := range c.tasks {
		if task.GoalID != goalID {
			continue
		}
		if task.Status == TaskCompleted || task.Status == TaskFailed || task.Status == TaskCancelled {
			continue
		}
		_ = task.Cancel("stopped with goal")
		c.workspaceLocks.Unlock(task.ID)
		if cancel := c.activeCancels[task.ID]; cancel != nil {
			cancel()
			delete(c.activeCancels, task.ID)
		}
		if state, ok := c.agentState[task.AssignedTo]; ok {
			if agent, exists := c.agents[task.AssignedTo]; !exists || !agent.IsAvailable() {
				state.Status = AgentOffline
			} else {
				state.Status = AgentIdle
			}
			if state.CurrentTask == task.ID {
				state.CurrentTask = ""
			}
			state.LastActivity = time.Now().UTC()
			c.recordAgentLocked(state, "agent idle after goal stop")
		}
		c.recordTaskLocked(task, "task cancelled with goal stop")
	}
	c.recordGoalLocked(goal, "goal stopped")
	return nil
}

func (c *Coordinator) DeleteGoal(goalID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	goal, ok := c.goals[goalID]
	if !ok {
		return errors.New("goal not found")
	}
	for _, task := range c.tasks {
		if task.GoalID != goalID {
			continue
		}
		c.workspaceLocks.Unlock(task.ID)
		if cancel := c.activeCancels[task.ID]; cancel != nil {
			cancel()
			delete(c.activeCancels, task.ID)
		}
		if state, ok := c.agentState[task.AssignedTo]; ok && state.CurrentTask == task.ID {
			if agent, exists := c.agents[task.AssignedTo]; !exists || !agent.IsAvailable() {
				state.Status = AgentOffline
			} else {
				state.Status = AgentIdle
			}
			state.CurrentTask = ""
			state.LastActivity = time.Now().UTC()
		}
		delete(c.tasks, task.ID)
	}
	filteredTaskOrder := make([]string, 0, len(c.taskOrder))
	for _, taskID := range c.taskOrder {
		if task := c.tasks[taskID]; task != nil {
			filteredTaskOrder = append(filteredTaskOrder, taskID)
		}
	}
	c.taskOrder = filteredTaskOrder

	delete(c.goals, goalID)
	filteredGoalOrder := make([]string, 0, len(c.goalOrder))
	for _, id := range c.goalOrder {
		if id != goalID {
			filteredGoalOrder = append(filteredGoalOrder, id)
		}
	}
	c.goalOrder = filteredGoalOrder

	if c.currentGoalID == goalID {
		c.currentGoalID = ""
		c.brainState.PendingHumanInput = nil
	}
	if c.brainState.CurrentPlan != nil && c.brainState.CurrentPlan.GoalID == goalID {
		c.brainState.CurrentPlan = nil
		c.planExecutor = nil
	}

	filteredMessages := make([]*Message, 0, len(c.messages))
	for _, msg := range c.messages {
		if msg == nil {
			continue
		}
		if msg.TaskID != "" {
			if task := c.tasks[msg.TaskID]; task == nil {
				continue
			}
		}
		if msg.Metadata.Goal != nil && msg.Metadata.Goal.ID == goalID {
			continue
		}
		if msg.Metadata.Plan != nil && msg.Metadata.Plan.GoalID == goalID {
			continue
		}
		if msg.Metadata.Task != nil && msg.Metadata.Task.GoalID == goalID {
			continue
		}
		filteredMessages = append(filteredMessages, msg)
	}
	c.messages = filteredMessages
	if err := c.logStore.Rewrite(c.messages); err != nil {
		return err
	}
	c.hub.Broadcast("snapshot", c.snapshotLocked())
	_ = goal
	return nil
}

func (c *Coordinator) ClearMessages() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.messages = []*Message{}
	if err := c.logStore.Rewrite(c.messages); err != nil {
		return err
	}
	c.hub.Broadcast("snapshot", c.snapshotLocked())
	return nil
}

func (c *Coordinator) ClearFinishedTasks() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := map[string]bool{}
	filteredOrder := make([]string, 0, len(c.taskOrder))
	for _, taskID := range c.taskOrder {
		task := c.tasks[taskID]
		if task == nil {
			continue
		}
		switch task.Status {
		case TaskCompleted, TaskFailed, TaskCancelled:
			delete(c.tasks, taskID)
			removed[taskID] = true
		default:
			filteredOrder = append(filteredOrder, taskID)
		}
	}
	c.taskOrder = filteredOrder

	if len(removed) > 0 {
		filteredMessages := make([]*Message, 0, len(c.messages))
		for _, msg := range c.messages {
			if msg.TaskID != "" && removed[msg.TaskID] {
				continue
			}
			if msg.Metadata.Task != nil && removed[msg.Metadata.Task.ID] {
				continue
			}
			filteredMessages = append(filteredMessages, msg)
		}
		c.messages = filteredMessages
	}

	if err := c.logStore.Rewrite(c.messages); err != nil {
		return err
	}
	c.hub.Broadcast("snapshot", c.snapshotLocked())
	return nil
}

func (c *Coordinator) CancelTask(taskID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	task, ok := c.tasks[taskID]
	if !ok {
		return errors.New("task not found")
	}
	if cancel, ok := c.activeCancels[taskID]; ok {
		cancel()
	}
	c.workspaceLocks.Unlock(taskID)
	if err := task.Cancel("cancelled by human"); err != nil {
		return err
	}
	c.recordTaskLocked(task, "task cancelled")
	if state, ok := c.agentState[task.AssignedTo]; ok && state.CurrentTask == task.ID {
		state.Status = AgentIdle
		state.CurrentTask = ""
		state.LastActivity = time.Now().UTC()
		c.recordAgentLocked(state, "agent reset after cancellation")
	}
	return nil
}

func (c *Coordinator) RetryTask(taskID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	task, ok := c.tasks[taskID]
	if !ok {
		return errors.New("task not found")
	}
	if err := task.Retry(); err != nil {
		return err
	}
	task.ErrorOutput = ""
	if goal := c.goals[task.GoalID]; goal != nil && goal.Status == GoalBlocked {
		goal.Status = GoalActive
		goal.Summary = ""
		goal.CompletedAt = nil
		c.recordGoalLocked(goal, "goal resumed")
	}
	if state, ok := c.agentState[task.AssignedTo]; ok {
		if agent, exists := c.agents[task.AssignedTo]; !exists || !agent.IsAvailable() {
			state.Status = AgentOffline
		} else {
			state.Status = AgentIdle
		}
		state.CurrentTask = ""
		state.LastActivity = time.Now().UTC()
		c.recordAgentLocked(state, "agent reset for retry")
	}
	c.recordTaskLocked(task, "task retry requested")
	c.signalDispatchLocked()
	return nil
}

func (c *Coordinator) ApproveTask(taskID string) error {
	c.mu.Lock()
	followUp, err := c.acceptTaskLocked(taskID)
	if err == nil {
		c.signalDispatchLocked()
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if followUp != nil {
		c.enqueueBrainCycle(followUp.Trigger, followUp.Context)
	}
	return nil
}

func (c *Coordinator) RejectTask(taskID, reason string) error {
	c.mu.Lock()
	task, err := c.reviseTaskLocked(taskID, reason)
	if err == nil && task != nil {
		c.signalDispatchLocked()
	}
	c.mu.Unlock()
	return err
}

func (c *Coordinator) ResetAgent(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	state, ok := c.agentState[name]
	if !ok {
		return errors.New("agent not found")
	}
	if agent, exists := c.agents[name]; !exists || !agent.IsAvailable() {
		state.Status = AgentOffline
	} else {
		state.Status = AgentIdle
	}
	state.CurrentTask = ""
	state.LastActivity = time.Now().UTC()
	c.recordAgentLocked(state, "agent reset")
	c.signalDispatchLocked()
	return nil
}

func (c *Coordinator) ReassignTask(taskID, newAgent, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reassignTaskLocked(taskID, newAgent, reason)
}

func (c *Coordinator) eventLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(workflowTickInterval)
	defer ticker.Stop()

	for {
		select {
		case result := <-c.results:
			c.handleDispatchResult(result)
		case <-ticker.C:
			c.reconcileTasks()
			c.completeQuiescentFileTasks()
			c.requeueRecoveredStaleRunningTasks()
		case <-c.dispatchWake:
			c.dispatchReadyTasks()
		case <-c.stop:
			return
		}
	}
}

func (c *Coordinator) brainLoop() {
	defer c.wg.Done()

	for {
		select {
		case followUp := <-c.brainQueue:
			c.runBrainCycle(followUp.Trigger, followUp.Context)
		case <-c.stop:
			return
		}
	}
}

func (c *Coordinator) enqueueBrainCycle(trigger, brainContext string) {
	if trigger == "" {
		return
	}
	select {
	case <-c.stop:
		return
	case c.brainQueue <- brainFollowUp{Trigger: trigger, Context: brainContext}:
	}
}

func (c *Coordinator) reconcileTasks() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, task := range c.tasks {
		if task.Status == TaskBlocked && c.dependenciesMetLocked(task) {
			_ = task.MarkPending()
			c.recordTaskLocked(task, "dependencies satisfied")
		}
	}
	c.signalDispatchLocked()
}

func (c *Coordinator) dispatchReadyTasks() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.shuttingDown {
		return
	}

	tasks := c.cloneTasksLocked()
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority < tasks[j].Priority
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})

	for _, candidate := range tasks {
		task := c.tasks[candidate.ID]
		if task == nil || (task.Status != TaskPending && task.Status != TaskBlocked) {
			continue
		}
		agent, exists := c.agents[task.AssignedTo]
		state, ok := c.agentState[task.AssignedTo]
		if !exists || agent == nil {
			if !ok {
				continue
			}
			state.Status = AgentOffline
			state.CurrentTask = ""
			state.LastActivity = time.Now().UTC()
			if task.Status != TaskBlocked {
				_ = task.SetBlocked()
				task.Result = "assigned agent is unavailable in the current runtime"
				c.recordTaskLocked(task, "waiting for assigned agent to become available")
			}
			c.recordAgentLocked(state, "agent offline")
			continue
		}
		if !ok || state.Status == AgentOffline || state.Status == AgentError || state.Status == AgentBusy || state.Status == AgentPaused {
			continue
		}
		if !c.dependenciesMetLocked(task) {
			if task.Status != TaskBlocked {
				_ = task.SetBlocked()
				c.recordTaskLocked(task, "waiting for dependencies")
			}
			continue
		}
		if len(task.FilesTouched) > 0 {
			if ok, holder := c.workspaceLocks.TryLock(task.FilesTouched, task.ID); !ok {
				if task.Status != TaskBlocked {
					_ = task.SetBlocked()
				}
				task.Result = fmt.Sprintf("waiting for file lock held by %s", holder)
				c.recordTaskLocked(task, "waiting for advisory file lock")
				continue
			}
		}
		_ = task.MarkPending()
		if err := task.Start(); err != nil {
			c.workspaceLocks.Unlock(task.ID)
			continue
		}
		state.Status = AgentBusy
		state.CurrentTask = task.ID
		state.LastActivity = time.Now().UTC()
		c.recordTaskLocked(task, "task started")
		c.recordAgentLocked(state, "agent busy")
		c.runTaskLocked(task.Clone())
	}
}

func (c *Coordinator) runTaskLocked(task *Task) {
	agent := c.agents[task.AssignedTo]
	state := c.agentState[task.AssignedTo]
	if agent == nil {
		if state != nil {
			state.Status = AgentOffline
			state.CurrentTask = ""
			state.LastActivity = time.Now().UTC()
			c.recordAgentLocked(state, "agent offline")
		}
		_ = task.Fail("assigned agent is unavailable in the current runtime")
		c.recordTaskLocked(task, "task failed")
		if task.GoalID != "" {
			if goal := c.goals[task.GoalID]; goal != nil {
				c.blockGoalLocked(goal, fmt.Sprintf("task %q cannot start because assigned agent %q is unavailable", task.Title, task.AssignedTo), "goal blocked")
			}
		}
		return
	}
	forwarded := append([]string(nil), c.pendingInbox[task.AssignedTo]...)
	c.pendingInbox[task.AssignedTo] = nil
	prompt := c.buildAgentPromptLocked(task, state, forwarded)

	msg := NewMessage(MsgCoordinatorToAgent, "coordinator", task.AssignedTo, task.ID, prompt)
	c.appendMessageLocked(msg)

	ctx, cancel := context.WithCancel(context.Background())
	c.activeCancels[task.ID] = cancel
	workConfig := c.config.Agents[task.AssignedTo]
	providerName := ""
	if state != nil {
		providerName = state.Provider
	}
	if workConfig.Command == "" && providerName != "" {
		if providerCfg, ok := c.config.Providers[providerName]; ok {
			workConfig = providerToAgentConfig(providerCfg)
		}
	}
	observer := newCommandTelemetryObserver(ctx, "agent", providerName, workConfig, c.workspace.Path(), workConfig.Args, func(telemetry CommandTelemetry) {
		c.updateAgentInvocation(task.AssignedTo, telemetry)
	})
	c.syncAgentInvocationLocked(task.AssignedTo, observer.cloneLocked())

	c.wg.Add(1)
	go func(agentName string, prompt string, task *Task) {
		defer c.wg.Done()
		defer cancel()

		result, err := agent.Execute(observer.Context(), prompt, c.workspace.Path())
		c.results <- dispatchResult{
			TaskID:     task.ID,
			AgentName:  agentName,
			Result:     result,
			Err:        err,
			RetryCount: task.RetryCount,
		}
	}(task.AssignedTo, prompt, task)
}

func (c *Coordinator) handleDispatchResult(dr dispatchResult) {
	var followUp *brainFollowUp

	c.mu.Lock()
	task, ok := c.tasks[dr.TaskID]
	if !ok {
		c.mu.Unlock()
		return
	}
	state := c.agentState[dr.AgentName]
	delete(c.activeCancels, dr.TaskID)
	c.workspaceLocks.Unlock(dr.TaskID)

	if task.Status != TaskRunning {
		if state != nil {
			if state.CurrentTask == dr.TaskID {
				state.CurrentTask = ""
				if state.Status == AgentBusy {
					state.Status = AgentIdle
				}
				state.LastActivity = time.Now().UTC()
				c.recordAgentLocked(state, "agent idle")
			}
		}
		c.mu.Unlock()
		return
	}

	if dr.Err != nil {
		task.RetryCount++
		errMsg := dr.Err.Error()
		errorOutput := extractErrorOutput(dr.Err)
		task.ErrorOutput = errorOutput
		agentMsg := NewMessage(MsgAgentToCoordinator, dr.AgentName, "coordinator", task.ID, errMsg)
		agentMsg.Metadata.Error = errMsg
		agentMsg.Metadata.RawOutput = errorOutput
		c.appendMessageLocked(agentMsg)

		if state != nil {
			state.CurrentTask = ""
			state.LastActivity = time.Now().UTC()
		}

		if task.RetryCount <= task.MaxRetries {
			task.Status = TaskPending
			task.Result = fmt.Sprintf("retry %d/%d: %s", task.RetryCount, task.MaxRetries, errMsg)
			if state != nil {
				state.Status = AgentIdle
				c.recordAgentLocked(state, "agent idle after retry")
			}
			c.recordTaskLocked(task, "task retry scheduled")
			c.signalDispatchLocked()
			c.mu.Unlock()
			return
		}

		_ = task.Fail(errMsg)
		if state != nil {
			state.Status = AgentError
			state.TasksFailed++
			c.recordAgentLocked(state, "agent entered error state")
		}
		c.recordTaskLocked(task, "task failed")
		if task.GoalID != "" {
			if goal := c.goals[task.GoalID]; goal != nil {
				c.blockGoalLocked(goal, fmt.Sprintf("task %q failed: %s", task.Title, errMsg), "goal blocked")
			}
		}
		c.mu.Unlock()
		return
	}

	summary := ""
	filesChanged := []string(nil)
	commitHash := ""
	if dr.Result != nil {
		summary = dr.Result.Summary
		filesChanged = dr.Result.FilesChanged
	}

	if hash, committedFiles, err := c.workspace.CommitTask(dr.AgentName, task.Title, task.ID, taskCommitPaths(task)); err == nil {
		commitHash = hash
		if len(committedFiles) > 0 {
			filesChanged = committedFiles
		}
	}

	agentMsg := NewMessage(MsgAgentToCoordinator, dr.AgentName, "coordinator", task.ID, summary)
	if dr.Result != nil {
		agentMsg.Metadata.TokensIn = dr.Result.TokensIn
		agentMsg.Metadata.TokensOut = dr.Result.TokensOut
		agentMsg.Metadata.DurationMs = dr.Result.DurationMs
		agentMsg.Metadata.ExitCode = dr.Result.ExitCode
		agentMsg.Metadata.RawOutput = dr.Result.RawOutput
		agentMsg.Metadata.FilesChanged = append([]string(nil), filesChanged...)
		agentMsg.Metadata.CommitHash = commitHash
	}
	c.appendMessageLocked(agentMsg)

	if task.IsReviewTask {
		_ = task.Complete(summary, filesChanged, commitHash)
		c.recordTaskLocked(task, "task completed")
		if parent, ok := c.tasks[task.ParentID]; ok {
			parent.ReviewResult = summary
			c.recordTaskLocked(parent, "review result received")
			if !parent.IsHumanMessage {
				if parent.GoalID == "" {
					followUpTask := BuildReviewActionTask(parent, task, c.maxRetriesForAgentLocked(parent.AssignedTo))
					followUpTask.GoalID = parent.GoalID
					followUpTask.Priority = parent.Priority
					followUpTask.PlanPhase = parent.PlanPhase
					parent.ReviewActionTaskID = followUpTask.ID
					c.tasks[followUpTask.ID] = followUpTask
					c.taskOrder = append(c.taskOrder, followUpTask.ID)
					c.recordTaskLocked(parent, "review action task created")
					c.recordTaskLocked(followUpTask, "task created")
					c.signalDispatchLocked()
				} else {
					if reviewApproves(summary) {
						if _, err := c.acceptTaskLocked(parent.ID); err == nil {
							c.recordTaskLocked(parent, "task approved by reviewer")
						}
					} else {
						followUpTask, err := c.reviseTaskLocked(parent.ID, summary)
						if err == nil && followUpTask != nil {
							c.signalDispatchLocked()
						}
					}
				}
			}
		}
	} else if task.ReviewBy != "" && !c.shouldUseBrainForTaskLocked(task) {
		_ = task.MoveToReview(summary, filesChanged, commitHash)
		reviewBy := task.ReviewBy
		reviewTask := BuildReviewTask(task, reviewBy, c.maxRetriesForAgentLocked(reviewBy))
		reviewTask.GoalID = task.GoalID
		reviewTask.Priority = task.Priority
		reviewTask.PlanPhase = task.PlanPhase
		task.ReviewTaskID = reviewTask.ID
		c.tasks[reviewTask.ID] = reviewTask
		c.taskOrder = append(c.taskOrder, reviewTask.ID)
		reviewMsg := NewMessage(MsgAgentToAgent, dr.AgentName, task.ReviewBy, task.ID, summary)
		c.appendMessageLocked(reviewMsg)
		c.recordTaskLocked(task, "task awaiting review")
		c.recordTaskLocked(reviewTask, "review task created")
		c.signalDispatchLocked()
	} else {
		_ = task.Complete(summary, filesChanged, commitHash)
		c.recordTaskLocked(task, "task completed")
		if task.GoalID != "" {
			if conflictContext := c.detectIntegrationConflictLocked(task, filesChanged); conflictContext != "" {
				if goal := c.goals[task.GoalID]; goal != nil {
					c.blockGoalLocked(goal, conflictContext, "goal blocked")
				}
			} else if err := c.advanceGoalWorkflowLocked(); err != nil {
				if goal := c.goals[task.GoalID]; goal != nil {
					c.failGoalLocked(goal, fmt.Sprintf("workflow progression failed: %v", err), "goal failed")
				}
			}
		}
	}

	if task.IsHumanMessage {
		reply := NewMessage(MsgCoordinatorToHuman, "coordinator", "human", task.ID, summary)
		if dr.Result != nil {
			reply.Metadata.TokensIn = dr.Result.TokensIn
			reply.Metadata.TokensOut = dr.Result.TokensOut
			reply.Metadata.DurationMs = dr.Result.DurationMs
		}
		c.appendMessageLocked(reply)
	}

	if state != nil {
		state.Status = AgentIdle
		state.CurrentTask = ""
		state.TasksCompleted++
		if dr.Result != nil {
			state.TotalTokensIn += dr.Result.TokensIn
			state.TotalTokensOut += dr.Result.TokensOut
		}
		state.LastActivity = time.Now().UTC()
		c.recordAgentLocked(state, "agent idle")
	}
	c.signalDispatchLocked()
	c.mu.Unlock()

	if followUp != nil {
		c.enqueueBrainCycle(followUp.Trigger, followUp.Context)
	}
}

func (c *Coordinator) completeQuiescentFileTasks() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, task := range c.tasks {
		if task == nil || task.Status != TaskRunning || len(task.FilesTouched) == 0 {
			continue
		}
		state := c.agentState[task.AssignedTo]
		if state == nil || state.LastInvocation == nil {
			continue
		}
		invocation := state.LastInvocation
		if invocation.Status != "running" && invocation.Status != "waiting_for_exit" {
			continue
		}
		if !c.outputArtifactsReadyLocked(task, invocation) {
			continue
		}
		if cancel := c.activeCancels[task.ID]; cancel != nil {
			cancel()
			delete(c.activeCancels, task.ID)
		}
		c.workspaceLocks.Unlock(task.ID)
		commitHash, filesChanged, _ := c.workspace.CommitTask(task.AssignedTo, task.Title, task.ID, taskCommitPaths(task))
		if err := task.Complete(c.syntheticArtifactSummary(task), filesChanged, commitHash); err != nil {
			continue
		}
		if state.CurrentTask == task.ID {
			state.CurrentTask = ""
		}
		state.Status = AgentIdle
		state.TasksCompleted++
		state.LastActivity = time.Now().UTC()
		c.recordTaskLocked(task, "task auto-completed from workspace artifact")
		c.recordAgentLocked(state, "agent idle after artifact completion")
		if task.GoalID != "" {
			if goal := c.goals[task.GoalID]; goal != nil {
				if err := c.advanceGoalWorkflowLocked(); err != nil {
					c.failGoalLocked(goal, fmt.Sprintf("workflow progression failed: %v", err), "goal failed")
				}
			}
		}
	}
}

func (c *Coordinator) requeueRecoveredStaleRunningTasks() {
	c.mu.Lock()
	defer c.mu.Unlock()

	requeued := false
	for _, task := range c.tasks {
		if task == nil || task.Status != TaskRunning {
			continue
		}
		if _, launchedHere := c.activeCancels[task.ID]; launchedHere {
			continue
		}
		if c.recoveredTaskStillActiveLocked(task) {
			continue
		}
		task.Status = TaskPending
		task.StartedAt = nil
		task.Result = "recovered task was re-queued after stale process detection"
		c.recordTaskLocked(task, "recovered task re-queued after stale process detection")
		if state := c.agentState[task.AssignedTo]; state != nil {
			state.Status = AgentIdle
			state.CurrentTask = ""
			state.LastActivity = time.Now().UTC()
			c.recordAgentLocked(state, "agent reset after stale recovery")
		}
		requeued = true
	}
	if requeued {
		c.signalDispatchLocked()
	}
}

func (c *Coordinator) recoveredTaskStillActiveLocked(task *Task) bool {
	if task == nil {
		return false
	}
	state := c.agentState[task.AssignedTo]
	if state == nil || state.LastInvocation == nil {
		return false
	}
	invocation := state.LastInvocation
	switch invocation.Status {
	case "succeeded", "failed", "timed_out", "cancelled", "start_failed":
		return false
	}
	if invocation.PID <= 0 || !processExists(invocation.PID) {
		return false
	}
	startedAt := invocation.StartedAt
	if startedAt.IsZero() && task.StartedAt != nil {
		startedAt = *task.StartedAt
	}
	if startedAt.IsZero() {
		startedAt = task.CreatedAt
	}
	timeout := time.Duration(invocation.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	return time.Since(startedAt) <= timeout+30*time.Second
}

func (c *Coordinator) outputArtifactsReadyLocked(task *Task, invocation *CommandTelemetry) bool {
	if task == nil || invocation == nil || len(task.FilesTouched) == 0 {
		return false
	}
	lastEvent := invocation.LastEventAt
	if lastEvent.IsZero() {
		lastEvent = invocation.StartedAt
	}
	if lastEvent.IsZero() || time.Since(lastEvent) < fileArtifactQuietThreshold {
		return false
	}
	taskStart := task.CreatedAt
	if task.StartedAt != nil {
		taskStart = *task.StartedAt
	}
	hasFreshEvidence := false
	if task.DiscussionFile != "" {
		info, err := os.Stat(filepath.Join(c.workspace.Path(), filepath.FromSlash(task.DiscussionFile)))
		if err != nil || info.IsDir() {
			return false
		}
		if info.ModTime().Before(taskStart.Add(-time.Second)) {
			return false
		}
		hasFreshEvidence = true
	}
	for _, relPath := range task.FilesTouched {
		info, err := os.Stat(filepath.Join(c.workspace.Path(), filepath.FromSlash(relPath)))
		if err != nil || info.IsDir() {
			return false
		}
		if info.ModTime().Before(taskStart.Add(-time.Second)) {
			return false
		}
		hasFreshEvidence = true
	}
	return hasFreshEvidence
}

func (c *Coordinator) syntheticArtifactSummary(task *Task) string {
	if task == nil || len(task.FilesTouched) == 0 {
		return "Expected workspace artifacts were produced; coordinator advanced after the worker stopped reporting progress."
	}
	details := append([]string(nil), task.FilesTouched...)
	if task.DiscussionFile != "" {
		details = append(details, task.DiscussionFile)
	}
	return fmt.Sprintf(
		"Expected workspace artifacts were produced (%s); coordinator advanced after the worker stopped reporting progress.",
		strings.Join(details, ", "),
	)
}

func (c *Coordinator) shouldUseBrainForTaskLocked(task *Task) bool {
	return false
}

func (c *Coordinator) runBrainCycle(trigger, brainContext string) {
	if c.brainAdapter == nil || !c.brainAdapter.IsAvailable() {
		c.recordBrainError(trigger, errors.New("brain adapter is unavailable"))
		return
	}
	decision, err := c.invokeBrain(trigger, brainContext)
	if err != nil {
		c.recordBrainError(trigger, err)
		return
	}
	if _, err := c.executeDecisions(decision, trigger); err != nil {
		c.recordBrainError(trigger, err)
	}
}

func (c *Coordinator) invokeBrain(trigger string, brainContext string) (*BrainDecision, error) {
	c.mu.Lock()
	prompt := c.buildBrainPromptLocked(trigger, brainContext)
	workDir := c.workspace.Path()
	providerCfg := constrainBrainProvider(c.brainState.ActiveProvider, ProviderConfig{
		Command:        c.config.Brain.Command,
		Args:           append([]string(nil), c.config.Brain.Args...),
		TimeoutSeconds: c.config.Brain.TimeoutSeconds,
	})
	workConfig := providerToAgentConfig(providerCfg)
	ctx, cancel := context.WithCancel(context.Background())
	observer := newCommandTelemetryObserver(ctx, "brain", c.brainState.ActiveProvider, workConfig, workDir, workConfig.Args, c.updateBrainInvocation)
	c.brainState.InvocationInFlight = true
	c.brainState.LastTrigger = trigger
	c.brainState.LastThinking = fmt.Sprintf("Brain invocation in progress for %s...", trigger)
	initialTelemetry := observer.cloneLocked()
	c.syncBrainInvocationLocked(initialTelemetry)
	c.brainState.ConversationHistory = append(c.brainState.ConversationHistory, BrainMessage{
		Role:      "user",
		Content:   fmt.Sprintf("%s: %s", trigger, brainContext),
		Timestamp: time.Now().UTC(),
	})
	c.pruneBrainHistoryLocked()
	c.hub.Broadcast("brain_thinking", map[string]interface{}{
		"thinking":             c.brainState.LastThinking,
		"trigger":              trigger,
		"invocation_in_flight": true,
	})
	c.hub.Broadcast("brain_status", initialTelemetry)
	c.brainCancel = cancel
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.brainCancel = nil
		c.mu.Unlock()
	}()

	result, err := c.brainAdapter.Execute(observer.Context(), prompt, workDir)
	if err != nil {
		c.mu.Lock()
		c.brainState.InvocationInFlight = false
		c.mu.Unlock()
		return nil, err
	}

	brainOutput := strings.TrimSpace(result.Summary)
	if brainOutput == "" {
		brainOutput = strings.TrimSpace(result.RawOutput)
	}

	decision, err := parseBrainDecision(brainOutput)
	if err != nil && strings.TrimSpace(result.RawOutput) != "" && strings.TrimSpace(result.RawOutput) != brainOutput {
		decision, err = parseBrainDecision(result.RawOutput)
	}
	if err != nil {
		c.mu.Lock()
		c.brainState.InvocationInFlight = false
		c.mu.Unlock()
		return nil, err
	}

	c.mu.Lock()
	c.brainState.InvocationInFlight = false
	c.brainState.InvocationCount++
	c.brainState.TotalTokensIn += result.TokensIn
	c.brainState.TotalTokensOut += result.TokensOut
	c.brainState.LastThinking = decision.Thinking
	c.brainState.LastTrigger = trigger
	c.brainState.ConversationHistory = append(c.brainState.ConversationHistory, BrainMessage{
		Role:      "assistant",
		Content:   brainOutput,
		Timestamp: time.Now().UTC(),
	})
	c.pruneBrainHistoryLocked()
	c.hub.Broadcast("brain_status", c.brainState.LastInvocation)
	c.mu.Unlock()

	return decision, nil
}

func (c *Coordinator) executeDecisions(decision *BrainDecision, trigger string) (*brainFollowUp, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.recordBrainThinkingLocked(decision.Thinking, trigger)
	c.hub.Broadcast("brain_decisions", map[string]interface{}{"decisions": decision.Decisions, "trigger": trigger})

	for _, entry := range decision.Decisions {
		switch entry.Action {
		case "create_task":
			req := CreateTaskRequest{
				Title:        entry.Title,
				Description:  entry.Description,
				AssignedTo:   entry.AssignTo,
				DependsOn:    append([]string(nil), entry.DependsOn...),
				ReviewBy:     entry.ReviewBy,
				FilesTouched: append([]string(nil), entry.FilesTouched...),
				Priority:     entry.Priority,
			}
			if req.GoalID == "" {
				req.GoalID = c.currentGoalID
			}
			task, err := c.createTaskLocked(req)
			if err != nil {
				return nil, err
			}
			c.recordTaskLocked(task, "task created by brain")
			if goal := c.currentGoalLocked(); goal != nil && goal.Status == GoalPlanning {
				goal.Status = GoalActive
				c.recordGoalLocked(goal, "goal activated")
			}
		case "send_message":
			c.sendMessageDecisionLocked(entry.To, entry.Content)
		case "revise_task":
			if _, err := c.reviseTaskLocked(entry.TaskID, entry.Feedback); err != nil {
				return nil, err
			}
		case "reassign_task":
			if err := c.reassignTaskLocked(entry.TaskID, entry.NewAgent, entry.Reason); err != nil {
				return nil, err
			}
		case "accept_task":
			if _, err := c.acceptTaskLocked(entry.TaskID); err != nil {
				return nil, err
			}
		case "complete_goal":
			c.completeCurrentGoalLocked(entry.Summary)
		case "request_human_input":
			c.requestHumanInputLocked(entry.Question, entry.Context)
		case "update_plan":
			if entry.Plan == nil {
				return nil, errors.New("update_plan requires a plan")
			}
			if err := c.updatePlanLocked(entry.Plan, "brain", entry.Reason); err != nil {
				return nil, err
			}
		case "":
			return nil, errors.New("brain returned a decision with empty action")
		default:
			return nil, fmt.Errorf("unknown brain action %q", entry.Action)
		}
	}

	c.signalDispatchLocked()
	return c.postDecisionFollowUpLocked(), nil
}

func (c *Coordinator) updatePlanLocked(plan *Plan, actor string, reason string) error {
	goal := c.currentGoalLocked()
	if goal == nil {
		return errors.New("cannot update plan without an active goal")
	}
	normalized := normalizePlan(goal.ID, plan, c.brainState.CurrentPlan)
	executor := NewPlanExecutor(normalized)
	c.mergeExistingPlanStateLocked(normalized, executor)
	goal.PlanID = normalized.ID
	if goal.Status == GoalPlanning {
		goal.Status = GoalActive
		c.recordGoalLocked(goal, "goal activated")
	}
	c.brainState.CurrentPlan = normalized
	c.planExecutor = executor
	nextPhase := c.nextUnexecutedPhaseLocked()
	if nextPhase >= 0 {
		if err := c.planExecutor.ExecutePhase(c, nextPhase); err != nil {
			return err
		}
		c.signalDispatchLocked()
	}
	c.recordPlanLocked(normalized, actor, firstNonEmpty(reason, "plan updated"))
	return nil
}

func (c *Coordinator) acceptTaskLocked(taskID string) (*brainFollowUp, error) {
	task, ok := c.tasks[taskID]
	if !ok {
		return nil, errors.New("task not found")
	}
	switch task.Status {
	case TaskReview:
		if err := task.ApproveReview(); err != nil {
			return nil, err
		}
	case TaskCompleted:
	default:
		return nil, fmt.Errorf("task %s is not awaiting acceptance", taskID)
	}
	c.recordTaskLocked(task, "task accepted")
	return c.postDecisionFollowUpLocked(), nil
}

func (c *Coordinator) reviseTaskLocked(taskID, feedback string) (*Task, error) {
	task, ok := c.tasks[taskID]
	if !ok {
		return nil, errors.New("task not found")
	}
	if task.Status != TaskReview && task.Status != TaskCompleted {
		return nil, fmt.Errorf("task %s is not eligible for revision", taskID)
	}
	_ = task.Fail(firstNonEmpty(feedback, "revision requested"))
	task.ReviewReason = feedback
	task.RevisionCount++
	c.recordTaskLocked(task, "task sent for revision")

	revision, err := c.createTaskLocked(CreateTaskRequest{
		Title:        task.Title,
		Description:  task.Description,
		AssignedTo:   task.AssignedTo,
		DependsOn:    append([]string(nil), task.DependsOn...),
		ReviewBy:     task.ReviewBy,
		GoalID:       task.GoalID,
		FilesTouched: append([]string(nil), task.FilesTouched...),
		Priority:     task.Priority,
	})
	if err != nil {
		return nil, err
	}
	revision.ParentID = task.ParentID
	revision.RevisionOf = task.ID
	revision.RevisionFeedback = feedback
	revision.RevisionCount = task.RevisionCount
	revision.PlanPhase = task.PlanPhase
	c.replacePlanTaskMappingLocked(task.ID, revision.ID)
	c.recordTaskLocked(revision, "revision task created")
	return revision, nil
}

func (c *Coordinator) reassignTaskLocked(taskID, newAgent, reason string) error {
	task, ok := c.tasks[taskID]
	if !ok {
		return errors.New("task not found")
	}
	if task.Status == TaskRunning {
		return errors.New("cannot reassign a running task")
	}
	assignedTo, err := c.resolveAgentNameLocked(newAgent)
	if err != nil {
		return err
	}
	task.AssignedTo = assignedTo
	task.DiscussionFile = c.discussionFilePathLocked(task)
	task.Result = firstNonEmpty(reason, task.Result)
	c.recordTaskLocked(task, "task reassigned")
	return nil
}

func (c *Coordinator) requestHumanInputLocked(question, context string) {
	c.brainState.PendingHumanInput = &HumanInputRequest{
		Question: question,
		Context:  context,
	}
	msg := NewMessage(MsgSystemEvent, "brain", "human", "", strings.TrimSpace(strings.Join([]string{question, context}, "\n\n")))
	c.appendMessageLocked(msg)
	c.hub.Broadcast("human_input_requested", c.brainState.PendingHumanInput)
}

func (c *Coordinator) completeCurrentGoalLocked(summary string) {
	goal := c.currentGoalLocked()
	if goal == nil {
		return
	}
	now := time.Now().UTC()
	goal.Status = GoalCompleted
	goal.Summary = summary
	goal.CompletedAt = &now
	c.currentGoalID = ""
	c.brainState.PendingHumanInput = nil
	c.recordGoalLocked(goal, "goal completed")
}

func (c *Coordinator) postDecisionFollowUpLocked() *brainFollowUp {
	if err := c.advanceGoalWorkflowLocked(); err != nil {
		if goal := c.currentGoalLocked(); goal != nil {
			c.failGoalLocked(goal, fmt.Sprintf("workflow progression failed: %v", err), "goal failed")
		}
	}
	return nil
}

func (c *Coordinator) dependenciesMetLocked(task *Task) bool {
	for _, depID := range task.DependsOn {
		dep, ok := c.tasks[depID]
		if !ok {
			return false
		}
		if dep.Status == TaskCompleted {
			continue
		}
		if task.IsReviewTask && dep.ID == task.ParentID && dep.Status == TaskReview {
			continue
		}
		return false
	}
	return true
}

func (c *Coordinator) buildAgentPromptLocked(task *Task, agent *AgentState, forwarded []string) string {
	var builder strings.Builder
	if agent != nil {
		builder.WriteString(fmt.Sprintf("You are %s, a %s on a software engineering team.\n", agent.Name, agent.Role))
		builder.WriteString(fmt.Sprintf("Role description: %s\n\n", agent.Description))
	}
	builder.WriteString(fmt.Sprintf("Working directory: %s\n", c.workspace.Path()))
	builder.WriteString("Git restrictions:\n")
	builder.WriteString("- You may only run git status/add/diff/commit against files inside the shared workspace above.\n")
	builder.WriteString("- Do not create commits that include files outside the shared workspace.\n")
	builder.WriteString("- Never run git push.\n\n")
	builder.WriteString("Files in workspace:\n")
	builder.WriteString(c.workspaceTreeForTaskLocked(task))
	builder.WriteString("\n\n")
	if task != nil && task.DiscussionFile != "" {
		builder.WriteString(fmt.Sprintf("You must write your round/phase discussion record to this workspace file: %s\n", task.DiscussionFile))
	}
	if task != nil && len(task.FilesTouched) > 0 {
		builder.WriteString(fmt.Sprintf("Required workspace outputs for this task: %s\n", strings.Join(task.FilesTouched, ", ")))
	}
	builder.WriteString("\n")

	for _, depID := range task.DependsOn {
		if dep := c.tasks[depID]; dep != nil {
			builder.WriteString(fmt.Sprintf("--- Prior work by %s: %s ---\n", dep.AssignedTo, dep.Title))
			if dep.DiscussionFile != "" {
				builder.WriteString(fmt.Sprintf("Discussion file: %s\n", dep.DiscussionFile))
			}
			referencedFiles := combineReferencedFiles(dep.DiscussionFile, dep.FilesTouched, dep.FilesChanged)
			if len(referencedFiles) > 0 {
				builder.WriteString(fmt.Sprintf("Referenced workspace files: %s\n", strings.Join(referencedFiles, ", ")))
			}
			builder.WriteString(dep.Result)
			builder.WriteString("\n\n")
		}
	}

	if len(forwarded) > 0 {
		for _, msg := range forwarded {
			builder.WriteString(fmt.Sprintf("Note from coordinator: %s\n\n", msg))
		}
	}

	if task.RevisionOf != "" {
		if prev := c.tasks[task.RevisionOf]; prev != nil {
			builder.WriteString("--- REVISION NEEDED ---\n")
			builder.WriteString(fmt.Sprintf("Your previous attempt was rejected.\nFeedback: %s\n\n", task.RevisionFeedback))
			builder.WriteString(fmt.Sprintf("Your previous output:\n%s\n\n", prev.Result))
		}
	}

	builder.WriteString("--- YOUR TASK ---\n")
	builder.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	builder.WriteString("Description:\n")
	builder.WriteString(task.Description)
	return builder.String()
}

func (c *Coordinator) buildBrainPromptLocked(trigger string, context string) string {
	var builder strings.Builder
	roster := c.teamRosterLocked(true)
	systemPrompt := strings.ReplaceAll(c.brainSystemPrompt, "{{TEAM_ROSTER}}", roster)
	builder.WriteString(systemPrompt)
	builder.WriteString("\n\n## Current State\n\n")
	if goal := c.currentGoalLocked(); goal != nil {
		builder.WriteString(fmt.Sprintf("Goal: %s\n\n", strings.TrimSpace(strings.Join([]string{goal.Title, goal.Description}, " - "))))
	} else {
		builder.WriteString("Goal: No active goal.\n\n")
	}
	builder.WriteString("Team Status:\n")
	builder.WriteString(roster)
	builder.WriteString("\n\n### Current Plan\n")
	if c.brainState.CurrentPlan != nil {
		data, _ := json.MarshalIndent(c.brainState.CurrentPlan, "", "  ")
		builder.Write(data)
		builder.WriteString("\n")
	} else {
		builder.WriteString("No plan yet.\n")
	}
	builder.WriteString("\n### Active Tasks\n")
	builder.WriteString(c.activeTasksSummaryLocked())
	builder.WriteString("\n\n### Recently Completed\n")
	builder.WriteString(c.completedTasksSummaryLocked(5))
	builder.WriteString("\n\n## Trigger\n\n")
	builder.WriteString(fmt.Sprintf("%s: %s\n", trigger, context))
	builder.WriteString("\n## Conversation History\n")
	builder.WriteString(c.brainHistorySummaryLocked())
	builder.WriteString("\n\n## Instructions\n\nAnalyze only the state and trigger event provided above. Do not read files, browse, research, or use tools. If the goal mentions skills, commands, or files, those are for delegated agents, not for you. Respond immediately with one valid JSON object only.")
	return builder.String()
}

func (c *Coordinator) appendMessageLocked(msg *Message) {
	c.messages = append(c.messages, msg)
	if err := c.logStore.Append(msg); err == nil {
		c.hub.Broadcast("message", msg)
	}
}

func (c *Coordinator) recordTaskLocked(task *Task, content string) {
	msg := NewMessage(MsgSystemEvent, "coordinator", "human", task.ID, content)
	msg.Metadata.Task = task.Clone()
	c.appendMessageLocked(msg)
	c.hub.Broadcast("task_update", task.Clone())
	c.hub.Broadcast("workflow_update", c.workflowStateLocked())
}

func (c *Coordinator) recordAgentLocked(agent *AgentState, content string) {
	clone := *agent
	if agent.LastInvocation != nil {
		last := *agent.LastInvocation
		last.Args = append([]string(nil), last.Args...)
		last.Events = append([]TelemetryEvent(nil), last.Events...)
		clone.LastInvocation = &last
	}
	if len(agent.RecentInvocations) > 0 {
		clone.RecentInvocations = append([]CommandTelemetry(nil), agent.RecentInvocations...)
	}
	msg := NewMessage(MsgSystemEvent, "coordinator", "human", "", content)
	msg.Metadata.Agent = &clone
	c.appendMessageLocked(msg)
	c.hub.Broadcast("agent_status", &clone)
}

func (c *Coordinator) recordGoalLocked(goal *Goal, content string) {
	clone := *goal
	msg := NewMessage(MsgSystemEvent, "coordinator", "human", "", content)
	msg.Metadata.Goal = &clone
	c.appendMessageLocked(msg)
	c.hub.Broadcast("goal_update", &clone)
	c.hub.Broadcast("workflow_update", c.workflowStateLocked())
}

func (c *Coordinator) recordPlanLocked(plan *Plan, actor string, content string) {
	clone := *plan
	msg := NewMessage(MsgSystemEvent, firstNonEmpty(actor, "coordinator"), "human", "", content)
	msg.Metadata.Plan = &clone
	c.appendMessageLocked(msg)
	c.hub.Broadcast("plan_update", &clone)
	c.hub.Broadcast("workflow_update", c.workflowStateLocked())
}

func (c *Coordinator) recordBrainThinkingLocked(thinking, trigger string) {
	c.brainState.LastThinking = thinking
	c.brainState.LastTrigger = trigger
	msg := NewMessage(MsgSystemEvent, "brain", "human", "", thinking)
	c.appendMessageLocked(msg)
	c.hub.Broadcast("brain_thinking", map[string]interface{}{
		"thinking":             thinking,
		"trigger":              trigger,
		"invocation_in_flight": c.brainState.InvocationInFlight,
	})
}

func (c *Coordinator) updateBrainInvocation(telemetry CommandTelemetry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncBrainInvocationLocked(telemetry)
	c.hub.Broadcast("brain_status", telemetry)
}

func (c *Coordinator) updateAgentInvocation(agentName string, telemetry CommandTelemetry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	clone := c.syncAgentInvocationLocked(agentName, telemetry)
	if clone != nil {
		c.hub.Broadcast("agent_status", clone)
	}
}

func (c *Coordinator) syncBrainInvocationLocked(telemetry CommandTelemetry) {
	clone := telemetry
	clone.Args = append([]string(nil), telemetry.Args...)
	clone.Events = append([]TelemetryEvent(nil), telemetry.Events...)
	c.brainState.LastInvocation = &clone

	replaced := false
	for i := range c.brainState.RecentInvocations {
		if c.brainState.RecentInvocations[i].ID == clone.ID {
			c.brainState.RecentInvocations[i] = clone
			replaced = true
			break
		}
	}
	if !replaced {
		c.brainState.RecentInvocations = append(c.brainState.RecentInvocations, clone)
	}
	if len(c.brainState.RecentInvocations) > 10 {
		c.brainState.RecentInvocations = append([]CommandTelemetry(nil), c.brainState.RecentInvocations[len(c.brainState.RecentInvocations)-10:]...)
	}
}

func (c *Coordinator) syncAgentInvocationLocked(agentName string, telemetry CommandTelemetry) *AgentState {
	state := c.agentState[agentName]
	if state == nil {
		return nil
	}

	clone := telemetry
	clone.Args = append([]string(nil), telemetry.Args...)
	clone.Events = append([]TelemetryEvent(nil), telemetry.Events...)
	state.LastInvocation = &clone

	replaced := false
	for i := range state.RecentInvocations {
		if state.RecentInvocations[i].ID == clone.ID {
			state.RecentInvocations[i] = clone
			replaced = true
			break
		}
	}
	if !replaced {
		state.RecentInvocations = append(state.RecentInvocations, clone)
	}
	if len(state.RecentInvocations) > 10 {
		state.RecentInvocations = append([]CommandTelemetry(nil), state.RecentInvocations[len(state.RecentInvocations)-10:]...)
	}

	out := *state
	if state.LastInvocation != nil {
		last := *state.LastInvocation
		last.Args = append([]string(nil), last.Args...)
		last.Events = append([]TelemetryEvent(nil), last.Events...)
		out.LastInvocation = &last
	}
	out.RecentInvocations = append([]CommandTelemetry(nil), state.RecentInvocations...)
	return &out
}

func (c *Coordinator) recordBrainError(trigger string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	content := fmt.Sprintf("brain error during %s: %v", trigger, err)
	c.brainState.InvocationInFlight = false
	c.brainState.LastThinking = content
	c.brainState.LastTrigger = trigger
	msg := NewMessage(MsgSystemEvent, "brain", "human", "", content)
	c.appendMessageLocked(msg)
	c.hub.Broadcast("brain_thinking", map[string]interface{}{
		"thinking":             content,
		"trigger":              trigger,
		"invocation_in_flight": false,
	})

	if goal := c.currentGoalLocked(); goal != nil {
		now := time.Now().UTC()
		switch goal.Status {
		case GoalPlanning:
			goal.Status = GoalFailed
			goal.Summary = content
			goal.CompletedAt = &now
			c.recordGoalLocked(goal, "goal failed")
		}
	}
}

func (c *Coordinator) signalDispatchLocked() {
	select {
	case c.dispatchWake <- struct{}{}:
	default:
	}
}

func (c *Coordinator) cloneTasksLocked() []*Task {
	out := make([]*Task, 0, len(c.taskOrder))
	for _, taskID := range c.taskOrder {
		if task, ok := c.tasks[taskID]; ok {
			out = append(out, task.Clone())
		}
	}
	return out
}

func (c *Coordinator) cloneAgentStatesLocked() map[string]*AgentState {
	out := make(map[string]*AgentState, len(c.agentState))
	for name, state := range c.agentState {
		clone := *state
		if state.LastInvocation != nil {
			last := *state.LastInvocation
			last.Args = append([]string(nil), last.Args...)
			last.Events = append([]TelemetryEvent(nil), last.Events...)
			clone.LastInvocation = &last
		}
		if len(state.RecentInvocations) > 0 {
			clone.RecentInvocations = append([]CommandTelemetry(nil), state.RecentInvocations...)
		}
		out[name] = &clone
	}
	return out
}

func (c *Coordinator) recentMessagesLocked(limit int) []*Message {
	if limit <= 0 || limit >= len(c.messages) {
		return append([]*Message(nil), c.messages...)
	}
	return append([]*Message(nil), c.messages[len(c.messages)-limit:]...)
}

// snapshotLocked returns the current state for WebSocket broadcast and initial connect.
// Note: workspace file listing is intentionally excluded here. ListFiles is an IO operation
// (directory walk) that must not run under the coordinator mutex. Clients that need the
// file list should call GET /api/workspace/files separately.
func (c *Coordinator) snapshotLocked() map[string]interface{} {
	goals := make([]*Goal, 0, len(c.goalOrder))
	for _, id := range c.goalOrder {
		if goal := c.goals[id]; goal != nil {
			clone := *goal
			goals = append(goals, &clone)
		}
	}
	var currentGoal *Goal
	if goal := c.currentGoalLocked(); goal != nil {
		clone := *goal
		currentGoal = &clone
	}
	return map[string]interface{}{
		"agents":       c.cloneAgentStatesLocked(),
		"tasks":        c.cloneTasksLocked(),
		"goals":        goals,
		"current_goal": currentGoal,
		"plan":         c.brainState.CurrentPlan,
		"workflow":     c.workflowStateLocked(),
		"workspace": map[string]interface{}{
			"path": c.workspace.Path(),
		},
		"messages": c.recentMessagesLocked(200),
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (c *Coordinator) createTaskLocked(req CreateTaskRequest) (*Task, error) {
	if strings.TrimSpace(req.AssignedTo) == "" {
		return nil, errors.New("assigned_to is required")
	}
	assignedTo, err := c.resolveAgentNameLocked(req.AssignedTo)
	if err != nil {
		return nil, err
	}
	req.AssignedTo = assignedTo
	if req.ReviewBy != "" {
		reviewBy, err := c.resolveAgentNameLocked(req.ReviewBy)
		if err != nil {
			return nil, err
		}
		req.ReviewBy = reviewBy
	}
	for _, depID := range req.DependsOn {
		if _, ok := c.tasks[depID]; !ok {
			return nil, fmt.Errorf("dependency %q not found", depID)
		}
	}
	task := NewTask(req, c.maxRetriesForAgentLocked(req.AssignedTo))
	task.DiscussionFile = c.discussionFilePathLocked(task)
	c.tasks[task.ID] = task
	c.taskOrder = append(c.taskOrder, task.ID)
	return task, nil
}

func (c *Coordinator) resolveAgentNameLocked(nameOrRole string) (string, error) {
	if _, ok := c.agents[nameOrRole]; ok {
		return nameOrRole, nil
	}
	type candidate struct {
		name string
		load int
	}
	candidates := make([]candidate, 0)
	for name, state := range c.agentState {
		if state.Role != nameOrRole {
			continue
		}
		candidates = append(candidates, candidate{name: name, load: c.agentLoadLocked(name)})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("unknown agent or role %q", nameOrRole)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].load != candidates[j].load {
			return candidates[i].load < candidates[j].load
		}
		return candidates[i].name < candidates[j].name
	})
	return candidates[0].name, nil
}

func (c *Coordinator) agentLoadLocked(name string) int {
	load := 0
	for _, task := range c.tasks {
		if task.AssignedTo != name {
			continue
		}
		switch task.Status {
		case TaskPending, TaskBlocked, TaskRunning, TaskReview:
			load++
		}
	}
	return load
}

func (c *Coordinator) maxRetriesForAgentLocked(agentName string) int {
	if cfg, ok := c.config.Agents[agentName]; ok {
		return cfg.MaxRetries
	}
	for _, member := range c.config.Team {
		if member.Name == agentName {
			if providerCfg, ok := c.config.Providers[member.Provider]; ok {
				return providerCfg.MaxRetries
			}
		}
	}
	return 0
}

func (c *Coordinator) currentGoalLocked() *Goal {
	if c.currentGoalID != "" {
		return c.goals[c.currentGoalID]
	}
	if c.brainState.CurrentPlan != nil {
		if goal := c.goals[c.brainState.CurrentPlan.GoalID]; goal != nil && c.goalHasIncompletePlanLocked(goal.ID) {
			return goal
		}
	}
	return nil
}

func (c *Coordinator) teamRosterLocked(withStatus bool) string {
	lines := make([]string, 0, len(c.agentState))
	names := make([]string, 0, len(c.agentState))
	for name := range c.agentState {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		state := c.agentState[name]
		line := fmt.Sprintf("- %s (%s): %s", state.Name, state.Provider, state.Description)
		if withStatus {
			line = fmt.Sprintf("%s [%s]", line, state.Status)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "- No team members configured."
	}
	return strings.Join(lines, "\n")
}

func (c *Coordinator) activeTasksSummaryLocked() string {
	lines := make([]string, 0)
	for _, taskID := range c.taskOrder {
		task := c.tasks[taskID]
		if task == nil {
			continue
		}
		switch task.Status {
		case TaskPending, TaskBlocked, TaskRunning, TaskReview:
			lines = append(lines, fmt.Sprintf("- [%s] %s assigned to %s (%s)", task.Status, task.Title, task.AssignedTo, task.ID))
		}
	}
	if len(lines) == 0 {
		return "No active tasks."
	}
	return strings.Join(lines, "\n")
}

func (c *Coordinator) completedTasksSummaryLocked(limit int) string {
	lines := make([]string, 0)
	for i := len(c.taskOrder) - 1; i >= 0 && len(lines) < limit; i-- {
		task := c.tasks[c.taskOrder[i]]
		if task == nil || task.Status != TaskCompleted {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s (%s): %s", task.Title, task.AssignedTo, task.Result))
	}
	if len(lines) == 0 {
		return "No recently completed tasks."
	}
	return strings.Join(lines, "\n")
}

func (c *Coordinator) brainHistorySummaryLocked() string {
	if len(c.brainState.ConversationHistory) == 0 {
		return "No history."
	}
	var lines []string
	for _, msg := range c.brainState.ConversationHistory {
		lines = append(lines, fmt.Sprintf("- %s: %s", msg.Role, msg.Content))
	}
	return strings.Join(lines, "\n")
}

func (c *Coordinator) pruneBrainHistoryLocked() {
	limit := c.config.Brain.MaxContextMessages
	if limit <= 0 || len(c.brainState.ConversationHistory) <= limit {
		return
	}
	keep := append([]BrainMessage(nil), c.brainState.ConversationHistory[len(c.brainState.ConversationHistory)-limit:]...)
	c.brainState.ConversationHistory = keep
}

// workspaceTreeForTaskLocked returns a workspace file listing scoped to the
// current task. Discussion files from prior rounds are summarized (count per
// round directory) rather than listed individually, which keeps prompts
// compact as the workspace grows across many review rounds.
func (c *Coordinator) workspaceTreeForTaskLocked(task *Task) string {
	files, err := c.workspace.ListFiles()
	if err != nil || len(files) == 0 {
		return "(workspace is empty)"
	}

	// Determine which discussion round directories are relevant to this task.
	relevantRoundDirs := map[string]bool{}
	if task != nil {
		if task.DiscussionFile != "" {
			relevantRoundDirs[discussionRoundDir(task.DiscussionFile)] = true
		}
		for _, depID := range task.DependsOn {
			if dep := c.tasks[depID]; dep != nil && dep.DiscussionFile != "" {
				relevantRoundDirs[discussionRoundDir(dep.DiscussionFile)] = true
			}
		}
	}

	var listed []string
	summarized := map[string]int{} // round dir → count
	for _, f := range files {
		roundDir := discussionRoundDir(f)
		if roundDir == "" || relevantRoundDirs[roundDir] {
			listed = append(listed, f)
		} else {
			summarized[roundDir]++
		}
	}

	if len(summarized) > 0 {
		dirs := make([]string, 0, len(summarized))
		for dir := range summarized {
			dirs = append(dirs, dir)
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			listed = append(listed, fmt.Sprintf("%s/ (%d files from earlier rounds)", dir, summarized[dir]))
		}
	}

	if len(listed) > 100 {
		listed = listed[:100]
		listed = append(listed, "... (truncated)")
	}
	return strings.Join(listed, "\n")
}

// workspaceTreeLocked returns the full workspace file listing (legacy helper).
func (c *Coordinator) workspaceTreeLocked() string {
	return c.workspaceTreeForTaskLocked(nil)
}

// discussionRoundDir extracts the round directory prefix from a discussion
// file path, e.g. "discussions/foo/round-02/adversarial-review-round-2"
// returns "discussions/foo/round-02". Returns "" for non-discussion paths.
func discussionRoundDir(path string) string {
	if !strings.HasPrefix(path, "discussions/") {
		return ""
	}
	parts := strings.Split(path, "/")
	// discussions/<goal-slug>/round-NN/...
	if len(parts) < 4 {
		return ""
	}
	if !strings.HasPrefix(parts[2], "round-") && !strings.HasPrefix(parts[2], "round_") {
		return ""
	}
	return strings.Join(parts[:3], "/")
}

func (c *Coordinator) sendMessageDecisionLocked(to, content string) {
	if to == "human" {
		msg := NewMessage(MsgCoordinatorToHuman, "brain", "human", "", content)
		c.appendMessageLocked(msg)
		return
	}
	msg := NewMessage(MsgCoordinatorToAgent, "brain", to, "", content)
	c.appendMessageLocked(msg)
	c.pendingInbox[to] = append(c.pendingInbox[to], content)
}

func (c *Coordinator) replacePlanTaskMappingLocked(oldTaskID, newTaskID string) {
	if c.brainState.CurrentPlan == nil {
		return
	}
	for i := range c.brainState.CurrentPlan.Phases {
		for j := range c.brainState.CurrentPlan.Phases[i].Tasks {
			if c.brainState.CurrentPlan.Phases[i].Tasks[j].RealTaskID == oldTaskID {
				c.brainState.CurrentPlan.Phases[i].Tasks[j].RealTaskID = newTaskID
			}
		}
	}
	if c.planExecutor != nil {
		for tempID, realTaskID := range c.planExecutor.tempToReal {
			if realTaskID == oldTaskID {
				c.planExecutor.tempToReal[tempID] = newTaskID
			}
		}
	}
}

func (c *Coordinator) mergeExistingPlanStateLocked(plan *Plan, executor *PlanExecutor) {
	if plan == nil || executor == nil || c.brainState.CurrentPlan == nil {
		return
	}
	if c.brainState.CurrentPlan.GoalID != "" && plan.GoalID != "" && c.brainState.CurrentPlan.GoalID != plan.GoalID {
		return
	}
	known := map[string]string{}
	for _, phase := range c.brainState.CurrentPlan.Phases {
		for _, task := range phase.Tasks {
			if task.TempID == "" || task.RealTaskID == "" {
				continue
			}
			if _, ok := c.tasks[task.RealTaskID]; ok {
				known[task.TempID] = task.RealTaskID
			}
		}
	}
	for i := range plan.Phases {
		if len(plan.Phases[i].Tasks) == 0 {
			continue
		}
		allCreated := true
		for j := range plan.Phases[i].Tasks {
			if realTaskID := known[plan.Phases[i].Tasks[j].TempID]; realTaskID != "" {
				plan.Phases[i].Tasks[j].RealTaskID = realTaskID
				executor.tempToReal[plan.Phases[i].Tasks[j].TempID] = realTaskID
			} else {
				allCreated = false
			}
		}
		if allCreated {
			executor.executedPhases[i] = true
			executor.currentPhase = i
		}
	}
}

func (c *Coordinator) nextUnexecutedPhaseLocked() int {
	if c.planExecutor == nil || c.brainState.CurrentPlan == nil {
		return -1
	}
	for i := range c.brainState.CurrentPlan.Phases {
		if !c.planExecutor.executedPhases[i] {
			return i
		}
	}
	return -1
}

func (c *Coordinator) currentIncompletePhaseIndexLocked() int {
	if c.planExecutor == nil || c.brainState.CurrentPlan == nil {
		return -1
	}
	for i := range c.brainState.CurrentPlan.Phases {
		if !c.planExecutor.executedPhases[i] {
			continue
		}
		if !c.phaseTasksCompleteLocked(i) {
			return i
		}
	}
	return -1
}

func (c *Coordinator) advanceGoalWorkflowLocked() error {
	goal := c.currentGoalLocked()
	if goal == nil || c.planExecutor == nil || c.brainState.CurrentPlan == nil {
		return nil
	}
	if goal.Status == GoalBlocked || goal.Status == GoalFailed || goal.Status == GoalCompleted || goal.Status == GoalGated {
		return nil
	}
	if reason, tripped := c.checkCircuitBreakersLocked(goal); tripped {
		c.blockGoalLocked(goal, reason, reason)
		return nil
	}

	phaseIndex := c.planExecutor.currentPhase
	if phaseIndex >= 0 && phaseIndex < len(c.brainState.CurrentPlan.Phases) {
		if !c.phaseTasksCompleteLocked(phaseIndex) {
			return nil
		}
		if handled, err := c.handleCompletedPhaseLocked(goal, phaseIndex); handled || err != nil {
			return err
		}
	}

	nextPhase := c.nextUnexecutedPhaseLocked()
	if nextPhase >= 0 {
		if err := c.planExecutor.ExecutePhase(c, nextPhase); err != nil {
			return err
		}
		c.signalDispatchLocked()
		return nil
	}

	if c.allPlannedTasksCompletedLocked() {
		c.completeCurrentGoalLocked(fmt.Sprintf("Completed workflow for %q.", goal.Title))
	}
	return nil
}

func (c *Coordinator) workflowStateLocked() WorkflowState {
	state := WorkflowState{
		Mode:   "deterministic",
		Recipe: c.resolveGoalWorkflowRecipe(""),
		Status: "idle",
	}

	goal := c.currentGoalLocked()
	if goal == nil {
		return state
	}
	state.Recipe = c.goalWorkflowRecipeLocked(goal)

	state.Status = string(goal.Status)
	if c.brainState.CurrentPlan == nil || len(c.brainState.CurrentPlan.Phases) == 0 {
		return state
	}

	state.TotalPhases = len(c.brainState.CurrentPlan.Phases)
	phaseIndex := 0
	if c.planExecutor != nil && c.planExecutor.currentPhase >= 0 && c.planExecutor.currentPhase < len(c.brainState.CurrentPlan.Phases) {
		phaseIndex = c.planExecutor.currentPhase
	}
	phase := c.brainState.CurrentPlan.Phases[phaseIndex]
	state.CurrentPhaseNumber = phase.Number
	state.CurrentPhaseTitle = phase.Title
	state.Stage = c.workflowStageForPhase(phase)
	state.StageDetail = phase.Description
	state.ReviewRound = c.activeReviewRoundLocked(phaseIndex)
	state.CompletedReviewRounds = c.completedReviewRoundsLocked()
	state.MaxReviewRounds = c.goalReviewRoundsLocked(goal)
	state.SpecVersion = state.CompletedReviewRounds
	state.StageTaskCompleted, state.StageTaskTotal = c.phaseTaskProgressLocked(phase)
	if goal.Summary != "" && (goal.Status == GoalBlocked || goal.Status == GoalFailed || goal.Status == GoalGated) {
		state.LastError = goal.Summary
	}

	if goal.Status == GoalActive {
		if c.phaseTasksCompleteLocked(phaseIndex) {
			state.Status = "transitioning"
		} else {
			state.Status = "executing"
		}
	}

	return state
}

func (c *Coordinator) activeReviewRoundLocked(phaseIndex int) int {
	if c.brainState.CurrentPlan == nil || phaseIndex < 0 || phaseIndex >= len(c.brainState.CurrentPlan.Phases) {
		return 0
	}
	phase := c.brainState.CurrentPlan.Phases[phaseIndex]
	if round := c.extractReviewRound(phase.Title); round > 0 {
		return round
	}
	round := 0
	for i := 0; i <= phaseIndex && i < len(c.brainState.CurrentPlan.Phases); i++ {
		if next := c.extractReviewRound(c.brainState.CurrentPlan.Phases[i].Title); next > round {
			round = next
		}
	}
	return round
}

func (c *Coordinator) completedReviewRoundsLocked() int {
	if c.brainState.CurrentPlan == nil {
		return 0
	}
	completed := 0
	for idx, phase := range c.brainState.CurrentPlan.Phases {
		if c.workflowStageForPhase(phase) != "adversarial_review" {
			continue
		}
		if !c.phaseTasksCompleteLocked(idx) {
			continue
		}
		if round := c.extractReviewRound(phase.Title); round > completed {
			completed = round
		}
	}
	return completed
}

func (c *Coordinator) phaseTaskProgressLocked(phase Phase) (int, int) {
	total := len(phase.Tasks)
	if total == 0 {
		return 0, 0
	}
	completed := 0
	for _, planned := range phase.Tasks {
		if planned.RealTaskID == "" {
			continue
		}
		task := c.tasks[planned.RealTaskID]
		if task != nil && task.Status == TaskCompleted {
			completed++
		}
	}
	return completed, total
}

func (c *Coordinator) phaseTasksCompleteLocked(phaseIndex int) bool {
	if c.planExecutor == nil || c.brainState.CurrentPlan == nil {
		return false
	}
	if phaseIndex < 0 || phaseIndex >= len(c.brainState.CurrentPlan.Phases) {
		return false
	}
	for _, planned := range c.brainState.CurrentPlan.Phases[phaseIndex].Tasks {
		task := c.tasks[planned.RealTaskID]
		if task == nil || task.Status != TaskCompleted {
			return false
		}
	}
	return true
}

func (c *Coordinator) handleCompletedPhaseLocked(goal *Goal, phaseIndex int) (bool, error) {
	if goal == nil || c.brainState.CurrentPlan == nil || phaseIndex < 0 || phaseIndex >= len(c.brainState.CurrentPlan.Phases) {
		return false, nil
	}
	phase := c.brainState.CurrentPlan.Phases[phaseIndex]

	// Post-discovery gate: pause after the discovery phase so a human can review
	// the extracted requirements before spec drafting begins.
	if c.config.Workflow.EnableHumanGates && c.config.Workflow.EnableDiscovery {
		stage := c.workflowStageForPhase(phase)
		if stage == "executing" && strings.Contains(strings.ToLower(phase.Title), "discover") {
			c.gateGoalLocked(goal, "discovery_review", "Review the extracted requirements before spec drafting begins.", 0)
			return true, nil
		}
	}

	switch c.goalWorkflowRecipeLocked(goal) {
	case workflowRecipeSpecCrossCritiqueLoop:
		return c.handleCompletedCrossCritiquePhaseLocked(goal, phase)
	default:
		return c.handleCompletedParallelReviewPhaseLocked(goal, phase)
	}
}

func (c *Coordinator) handleCompletedParallelReviewPhaseLocked(goal *Goal, phase Phase) (bool, error) {
	if c.workflowStageForPhase(phase) != "adversarial_review" {
		return false, nil
	}
	reviewTasks := c.tasksForPhaseLocked(phase)
	if len(reviewTasks) == 0 {
		return false, fmt.Errorf("review phase %q completed without a task", phase.Title)
	}
	allPassed, failedSummaries, reviewTaskIDs := evaluateTaskVerdicts(reviewTasks)
	round := max(1, c.extractReviewRound(phase.Title))

	// Parse, merge, and persist structured findings from this review round.
	c.mergeReviewFindingsLocked(goal, reviewTasks, round)

	// Check for stale findings that persist unchanged across rounds.
	if reason, stale := c.checkFindingStalenessLocked(goal, round); stale {
		c.blockGoalLocked(goal, reason, reason)
		return true, nil
	}

	if allPassed {
		c.completeCurrentGoalLocked(fmt.Sprintf("Specification %q passed adversarial review after %d round(s).", goal.Title, round))
		return true, nil
	}
	if round >= c.goalReviewRoundsLocked(goal) {
		msg := fmt.Sprintf("specification review did not pass after %d rounds: %s", round, strings.TrimSpace(strings.Join(failedSummaries, "\n\n---\n\n")))
		if c.config.Workflow.EnableHumanGates {
			c.gateGoalLocked(goal, "escalation", msg, round)
		} else {
			c.blockGoalLocked(goal, msg, "goal blocked")
		}
		return true, nil
	}
	if reason, tripped := c.checkCircuitBreakersLocked(goal); tripped {
		c.blockGoalLocked(goal, reason, reason)
		return true, nil
	}
	if err := c.appendSpecRevisionRoundLocked(goal, round+1, reviewTaskIDs); err != nil {
		return true, err
	}
	nextPhase := c.nextUnexecutedPhaseLocked()
	if nextPhase >= 0 {
		if err := c.planExecutor.ExecutePhase(c, nextPhase); err != nil {
			return true, err
		}
		c.signalDispatchLocked()
	}
	return true, nil
}

func (c *Coordinator) handleCompletedCrossCritiquePhaseLocked(goal *Goal, phase Phase) (bool, error) {
	if c.workflowStageForPhase(phase) != "consolidating_review" {
		return false, nil
	}
	round := max(1, c.extractReviewRound(phase.Title))
	reviewTasks := c.tasksForReviewRoundLocked(round, "adversarial_review")
	if len(reviewTasks) == 0 {
		return false, fmt.Errorf("review round %d completed without review tasks", round)
	}
	critiqueTasks := c.tasksForReviewRoundLocked(round, "cross_critique")
	if len(critiqueTasks) == 0 {
		return false, fmt.Errorf("review round %d completed without cross-critique tasks", round)
	}
	allPassed, failedSummaries, _ := evaluateTaskVerdicts(reviewTasks)

	// Parse, merge, and persist structured findings from this review round.
	c.mergeReviewFindingsLocked(goal, reviewTasks, round)

	// Check for stale findings that persist unchanged across rounds.
	if reason, stale := c.checkFindingStalenessLocked(goal, round); stale {
		c.blockGoalLocked(goal, reason, reason)
		return true, nil
	}

	if allPassed {
		c.completeCurrentGoalLocked(fmt.Sprintf("Specification %q passed adversarial review after %d round(s).", goal.Title, round))
		return true, nil
	}
	if round >= c.goalReviewRoundsLocked(goal) {
		msg := fmt.Sprintf("specification review did not pass after %d rounds: %s", round, strings.TrimSpace(strings.Join(failedSummaries, "\n\n---\n\n")))
		if c.config.Workflow.EnableHumanGates {
			c.gateGoalLocked(goal, "escalation", msg, round)
		} else {
			c.blockGoalLocked(goal, msg, "goal blocked")
		}
		return true, nil
	}
	if reason, tripped := c.checkCircuitBreakersLocked(goal); tripped {
		c.blockGoalLocked(goal, reason, reason)
		return true, nil
	}
	if err := c.appendSpecCrossCritiqueRoundLocked(goal, round+1, phase); err != nil {
		return true, err
	}
	nextPhase := c.nextUnexecutedPhaseLocked()
	if nextPhase >= 0 {
		if err := c.planExecutor.ExecutePhase(c, nextPhase); err != nil {
			return true, err
		}
		c.signalDispatchLocked()
	}
	return true, nil
}

func (c *Coordinator) appendSpecRevisionRoundLocked(goal *Goal, round int, reviewTaskIDs []string) error {
	if goal == nil || c.brainState.CurrentPlan == nil {
		return errors.New("cannot append revision round without an active plan")
	}
	primary := c.choosePrimaryRoleLocked(goal)
	reviewers := c.chooseReviewerAgentsLocked(primary, 2)
	if primary == "" || len(reviewers) < 2 {
		return errors.New("spec workflow requires one spec_creator and two reviewers")
	}
	specPath := c.specOutputPath(goal)
	slug := c.goalSlug(goal)
	consolidationRound := round - 1
	consolidationOutputPath := specVersionPath(slug, consolidationRound)
	baseTitle := c.normalizedGoalTitle(goal)
	consolidateTempID := fmt.Sprintf("consolidate-spec-r%d", round-1)
	currentPhaseCount := len(c.brainState.CurrentPlan.Phases)
	dependsOn := make([]string, 0, len(reviewTaskIDs))
	for _, reviewTaskID := range reviewTaskIDs {
		if tempID := c.findPhaseTempIDForTaskLocked(reviewTaskID); tempID != "" {
			dependsOn = append(dependsOn, tempID)
		}
	}
	reviewTasks := make([]PlannedTask, 0, len(reviewers))
	for i, reviewer := range reviewers {
		reviewTasks = append(reviewTasks, PlannedTask{
			TempID:      fmt.Sprintf("review-spec-r%d-%d", round, i+1),
			Title:       fmt.Sprintf("Adversarial Review %s Round %d Reviewer %d", baseTitle, round, i+1),
			Description: c.buildSpecReviewTaskDescription(goal, specPath, round, i+1, len(reviewers)),
			AssignTo:    reviewer,
			DependsOn:   []string{consolidateTempID},
			Priority:    1,
		})
	}
	c.brainState.CurrentPlan.Phases = append(c.brainState.CurrentPlan.Phases,
		Phase{
			Number:      currentPhaseCount + 1,
			Title:       fmt.Sprintf("Consolidate Review Round %d", round-1),
			Description: "Assess both reviewer outputs, decide what to accept, and amend the spec.",
			Tasks: []PlannedTask{{
				TempID:       consolidateTempID,
				Title:        fmt.Sprintf("Consolidate %s Review Round %d", baseTitle, round-1),
				Description:  c.buildSpecConsolidationTaskDescription(goal, specPath, round-1, len(reviewers)),
				AssignTo:     primary,
				DependsOn:    dependsOn,
				Priority:     1,
				FilesTouched: []string{consolidationOutputPath},
			}},
		},
		Phase{
			Number:      currentPhaseCount + 2,
			Title:       fmt.Sprintf("Adversarial Review Round %d", round),
			Description: "Run two parallel adversarial reviews over the consolidated specification.",
			Tasks:       reviewTasks,
		},
	)
	c.recordPlanLocked(c.brainState.CurrentPlan, "coordinator", fmt.Sprintf("appended review round %d", round))
	return nil
}

func (c *Coordinator) appendSpecCrossCritiqueRoundLocked(goal *Goal, round int, completedConsolidation Phase) error {
	if goal == nil || c.brainState.CurrentPlan == nil {
		return errors.New("cannot append review round without an active plan")
	}
	primary := c.choosePrimaryRoleLocked(goal)
	reviewers := c.chooseReviewerAgentsLocked(primary, 2)
	if primary == "" || len(reviewers) < 2 {
		return errors.New("spec workflow requires one spec_creator and two reviewers")
	}
	specPath := c.specOutputPath(goal)
	baseTitle := c.normalizedGoalTitle(goal)
	currentPhaseCount := len(c.brainState.CurrentPlan.Phases)
	reviewDependsOn := make([]string, 0, len(completedConsolidation.Tasks))
	for _, planned := range completedConsolidation.Tasks {
		if planned.TempID != "" {
			reviewDependsOn = append(reviewDependsOn, planned.TempID)
		}
	}
	reviewPhase, critiquePhase, consolidatePhase := c.buildCrossCritiqueRoundPhasesLocked(goal, primary, reviewers, specPath, baseTitle, round, reviewDependsOn)
	reviewPhase.Number = currentPhaseCount + 1
	critiquePhase.Number = currentPhaseCount + 2
	consolidatePhase.Number = currentPhaseCount + 3
	c.brainState.CurrentPlan.Phases = append(c.brainState.CurrentPlan.Phases, reviewPhase, critiquePhase, consolidatePhase)
	c.recordPlanLocked(c.brainState.CurrentPlan, "coordinator", fmt.Sprintf("appended cross-critique review round %d", round))
	return nil
}

func (c *Coordinator) findPhaseTempIDForTaskLocked(taskID string) string {
	if c.brainState.CurrentPlan == nil {
		return ""
	}
	for _, phase := range c.brainState.CurrentPlan.Phases {
		for _, planned := range phase.Tasks {
			if planned.RealTaskID == taskID {
				return planned.TempID
			}
		}
	}
	return ""
}

func (c *Coordinator) firstTaskForPhaseLocked(phase Phase) *Task {
	for _, planned := range phase.Tasks {
		if planned.RealTaskID == "" {
			continue
		}
		if task := c.tasks[planned.RealTaskID]; task != nil {
			return task
		}
	}
	return nil
}

func (c *Coordinator) tasksForPhaseLocked(phase Phase) []*Task {
	tasks := make([]*Task, 0, len(phase.Tasks))
	for _, planned := range phase.Tasks {
		if planned.RealTaskID == "" {
			continue
		}
		if task := c.tasks[planned.RealTaskID]; task != nil {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

func (c *Coordinator) tasksForReviewRoundLocked(round int, stage string) []*Task {
	if c.brainState.CurrentPlan == nil || round <= 0 {
		return nil
	}
	tasks := make([]*Task, 0)
	for _, phase := range c.brainState.CurrentPlan.Phases {
		if c.workflowStageForPhase(phase) != stage {
			continue
		}
		if c.extractReviewRound(phase.Title) != round {
			continue
		}
		tasks = append(tasks, c.tasksForPhaseLocked(phase)...)
	}
	return tasks
}

func (c *Coordinator) workflowStageForPhase(phase Phase) string {
	title := strings.ToLower(phase.Title)
	switch {
	case strings.Contains(title, "discover requirements"):
		return "discovering_requirements"
	case strings.Contains(title, "prepare spec"):
		return "preparing_spec"
	case strings.Contains(title, "cross critique"):
		return "cross_critique"
	case strings.Contains(title, "consolidate review"):
		return "consolidating_review"
	case strings.Contains(title, "adversarial review"):
		return "adversarial_review"
	case strings.Contains(title, "amend spec"):
		return "amending_spec"
	case strings.Contains(title, "review"):
		return "reviewing"
	case strings.Contains(title, "draft"):
		return "drafting"
	default:
		return "executing"
	}
}

func (c *Coordinator) extractReviewRound(title string) int {
	lower := strings.ToLower(title)
	idx := strings.LastIndex(lower, "round ")
	if idx < 0 {
		return 0
	}
	value := strings.TrimSpace(lower[idx+len("round "):])
	for i, r := range value {
		if r < '0' || r > '9' {
			value = value[:i]
			break
		}
	}
	if value == "" {
		return 0
	}
	round := 0
	for _, r := range value {
		round = round*10 + int(r-'0')
	}
	return round
}

func (c *Coordinator) allPlannedTasksCompletedLocked() bool {
	if c.brainState.CurrentPlan == nil {
		return false
	}
	for _, phase := range c.brainState.CurrentPlan.Phases {
		for _, planned := range phase.Tasks {
			task := c.tasks[planned.RealTaskID]
			if task == nil || task.Status != TaskCompleted {
				return false
			}
		}
	}
	return len(c.brainState.CurrentPlan.Phases) > 0
}

func (c *Coordinator) buildDeterministicPlanLocked(goal *Goal) (*Plan, error) {
	if goal == nil {
		return nil, errors.New("cannot build plan without a goal")
	}
	primary := c.choosePrimaryRoleLocked(goal)
	if primary == "" {
		return nil, errors.New("no suitable primary agent or role found")
	}
	reviewers := c.chooseReviewerAgentsLocked(primary, 2)
	if primary == "spec_creator" && len(reviewers) < 2 {
		return nil, errors.New("spec workflow requires two reviewer agents")
	}
	if len(reviewers) >= 2 && primary == "spec_creator" {
		switch c.goalWorkflowRecipeLocked(goal) {
		case workflowRecipeSpecCrossCritiqueLoop:
			return c.buildSpecCrossCritiqueWorkflowPlanLocked(goal, primary, reviewers), nil
		default:
			return c.buildSpecWorkflowPlanLocked(goal, primary, reviewers), nil
		}
	}

	baseTitle := c.normalizedGoalTitle(goal)
	return &Plan{
		GoalID: goal.ID,
		Phases: []Phase{
			{
				Number:      1,
				Title:       "Draft",
				Description: "Create the primary deliverable for the goal.",
				Tasks: []PlannedTask{{
					TempID:      "draft",
					Title:       fmt.Sprintf("Draft %s", baseTitle),
					Description: c.buildDraftTaskDescription(goal),
					AssignTo:    primary,
					Priority:    1,
				}},
			},
		},
	}, nil
}

func (c *Coordinator) choosePrimaryRoleLocked(goal *Goal) string {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{goal.Title, goal.Description}, "\n")))
	if c.roleExistsLocked("spec_creator") && containsAny(text, "spec", "design", "plan", "document", "doc", "proposal") {
		return "spec_creator"
	}
	if c.roleExistsLocked("implementer") {
		return "implementer"
	}
	for _, member := range c.config.Team {
		if member.Role != "" && member.Role != "reviewer" {
			return member.Role
		}
	}
	for _, member := range c.config.Team {
		if member.Name != "" {
			return member.Name
		}
	}
	return ""
}

func (c *Coordinator) chooseReviewerAgentsLocked(primary string, count int) []string {
	if count <= 0 {
		return nil
	}
	type candidate struct {
		name     string
		provider string
		load     int
	}
	candidates := make([]candidate, 0)
	for name, state := range c.agentState {
		if state.Role != "reviewer" || name == primary {
			continue
		}
		candidates = append(candidates, candidate{name: name, provider: state.Provider, load: c.agentLoadLocked(name)})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].load != candidates[j].load {
			return candidates[i].load < candidates[j].load
		}
		if candidates[i].provider != candidates[j].provider {
			return candidates[i].provider < candidates[j].provider
		}
		return candidates[i].name < candidates[j].name
	})

	selected := make([]string, 0, min(count, len(candidates)))
	usedProviders := map[string]bool{}
	for _, candidate := range candidates {
		if len(selected) >= count {
			break
		}
		if candidate.provider != "" && usedProviders[candidate.provider] {
			continue
		}
		selected = append(selected, candidate.name)
		if candidate.provider != "" {
			usedProviders[candidate.provider] = true
		}
	}
	for _, candidate := range candidates {
		if len(selected) >= count {
			break
		}
		if containsString(selected, candidate.name) {
			continue
		}
		selected = append(selected, candidate.name)
	}
	return selected
}

func (c *Coordinator) buildSpecWorkflowPlanLocked(goal *Goal, primary string, reviewers []string) *Plan {
	baseTitle := c.normalizedGoalTitle(goal)
	specPath := c.specOutputPath(goal)
	slug := c.goalSlug(goal)
	v0Path := specVersionPath(slug, 0)

	var phases []Phase
	phaseNum := 1
	prepDependsOn := []string(nil)

	if c.config.Workflow.EnableDiscovery {
		reqPath := filepath.ToSlash(filepath.Join("discussions", slug, "requirements.md"))
		phases = append(phases, Phase{
			Number:      phaseNum,
			Title:       "Discover Requirements",
			Description: "Extract structured requirements from source documents before spec drafting.",
			Tasks: []PlannedTask{{
				TempID:       "discover-requirements",
				Title:        fmt.Sprintf("Discover Requirements for %s", baseTitle),
				Description:  c.buildDiscoveryTaskDescription(goal),
				AssignTo:     primary,
				Priority:     1,
				FilesTouched: []string{reqPath},
			}},
		})
		prepDependsOn = []string{"discover-requirements"}
		phaseNum++
	}

	reviewTasks := make([]PlannedTask, 0, len(reviewers))
	for i, reviewer := range reviewers {
		reviewTasks = append(reviewTasks, PlannedTask{
			TempID:      fmt.Sprintf("review-spec-r1-%d", i+1),
			Title:       fmt.Sprintf("Adversarial Review %s Round 1 Reviewer %d", baseTitle, i+1),
			Description: c.buildSpecReviewTaskDescription(goal, specPath, 1, i+1, len(reviewers)),
			AssignTo:    reviewer,
			DependsOn:   []string{"prepare-spec-r1"},
			Priority:    1,
		})
	}

	phases = append(phases, Phase{
		Number:      phaseNum,
		Title:       "Prepare Spec",
		Description: "Create the implementation-ready specification from the provided technical documents.",
		Tasks: []PlannedTask{{
			TempID:       "prepare-spec-r1",
			Title:        fmt.Sprintf("Prepare %s Specification", baseTitle),
			Description:  c.buildSpecPreparationTaskDescription(goal, specPath),
			AssignTo:     primary,
			DependsOn:    prepDependsOn,
			Priority:     1,
			FilesTouched: []string{v0Path},
		}},
	})
	phaseNum++

	phases = append(phases, Phase{
		Number:      phaseNum,
		Title:       "Adversarial Review Round 1",
		Description: "Run two parallel adversarial reviews over the specification.",
		Tasks:       reviewTasks,
	})

	return &Plan{
		GoalID: goal.ID,
		Phases: phases,
	}
}

func (c *Coordinator) buildSpecCrossCritiqueWorkflowPlanLocked(goal *Goal, primary string, reviewers []string) *Plan {
	baseTitle := c.normalizedGoalTitle(goal)
	specPath := c.specOutputPath(goal)
	slug := c.goalSlug(goal)
	v0Path := specVersionPath(slug, 0)

	var phases []Phase
	phaseNum := 1
	prepDependsOn := []string(nil)

	if c.config.Workflow.EnableDiscovery {
		reqPath := filepath.ToSlash(filepath.Join("discussions", slug, "requirements.md"))
		phases = append(phases, Phase{
			Number:      phaseNum,
			Title:       "Discover Requirements",
			Description: "Extract structured requirements from source documents before spec drafting.",
			Tasks: []PlannedTask{{
				TempID:       "discover-requirements",
				Title:        fmt.Sprintf("Discover Requirements for %s", baseTitle),
				Description:  c.buildDiscoveryTaskDescription(goal),
				AssignTo:     primary,
				Priority:     1,
				FilesTouched: []string{reqPath},
			}},
		})
		prepDependsOn = []string{"discover-requirements"}
		phaseNum++
	}

	phases = append(phases, Phase{
		Number:      phaseNum,
		Title:       "Prepare Spec",
		Description: "Create the implementation-ready specification from the provided technical documents.",
		Tasks: []PlannedTask{{
			TempID:       "prepare-spec-r1",
			Title:        fmt.Sprintf("Prepare %s Specification", baseTitle),
			Description:  c.buildSpecPreparationTaskDescription(goal, specPath),
			AssignTo:     primary,
			DependsOn:    prepDependsOn,
			Priority:     1,
			FilesTouched: []string{v0Path},
		}},
	})
	phaseNum++

	reviewPhases, critiquePhase, consolidatePhase := c.buildCrossCritiqueRoundPhasesLocked(goal, primary, reviewers, specPath, baseTitle, 1, []string{"prepare-spec-r1"})
	reviewPhases.Number = phaseNum
	critiquePhase.Number = phaseNum + 1
	consolidatePhase.Number = phaseNum + 2

	phases = append(phases, reviewPhases, critiquePhase, consolidatePhase)

	return &Plan{
		GoalID: goal.ID,
		Phases: phases,
	}
}

func (c *Coordinator) buildCrossCritiqueRoundPhasesLocked(goal *Goal, primary string, reviewers []string, specPath string, baseTitle string, round int, reviewDependsOn []string) (Phase, Phase, Phase) {
	slug := c.goalSlug(goal)
	consolidationOutputPath := specVersionPath(slug, round)

	reviewTasks := make([]PlannedTask, 0, len(reviewers))
	reviewTempIDs := make([]string, 0, len(reviewers))
	for i, reviewer := range reviewers {
		tempID := fmt.Sprintf("review-spec-r%d-%d", round, i+1)
		reviewTempIDs = append(reviewTempIDs, tempID)
		reviewTasks = append(reviewTasks, PlannedTask{
			TempID:      tempID,
			Title:       fmt.Sprintf("Adversarial Review %s Round %d Reviewer %d", baseTitle, round, i+1),
			Description: c.buildSpecReviewTaskDescription(goal, specPath, round, i+1, len(reviewers)),
			AssignTo:    reviewer,
			DependsOn:   append([]string(nil), reviewDependsOn...),
			Priority:    1,
		})
	}

	critiqueTasks := make([]PlannedTask, 0, len(reviewers))
	critiqueTempIDs := make([]string, 0, len(reviewers))
	for i, reviewer := range reviewers {
		tempID := fmt.Sprintf("cross-critique-r%d-%d", round, i+1)
		critiqueTempIDs = append(critiqueTempIDs, tempID)
		peerIndex := ((i + 1) % len(reviewers)) + 1
		critiqueTasks = append(critiqueTasks, PlannedTask{
			TempID:      tempID,
			Title:       fmt.Sprintf("Cross Critique %s Round %d Reviewer %d", baseTitle, round, i+1),
			Description: c.buildSpecCritiqueTaskDescription(goal, specPath, round, i+1, peerIndex, len(reviewers)),
			AssignTo:    reviewer,
			DependsOn:   append([]string(nil), reviewTempIDs...),
			Priority:    1,
		})
	}

	consolidateDependsOn := append(append([]string(nil), reviewTempIDs...), critiqueTempIDs...)
	consolidateTempID := fmt.Sprintf("consolidate-spec-r%d", round)

	return Phase{
			Number:      0,
			Title:       fmt.Sprintf("Adversarial Review Round %d", round),
			Description: "Run two parallel adversarial reviews over the specification.",
			Tasks:       reviewTasks,
		}, Phase{
			Number:      0,
			Title:       fmt.Sprintf("Cross Critique Round %d", round),
			Description: "Each reviewer critiques the other reviewer's findings before consolidation.",
			Tasks:       critiqueTasks,
		}, Phase{
			Number:      0,
			Title:       fmt.Sprintf("Consolidate Review Round %d", round),
			Description: "Assess both reviews and both critiques, then amend the spec.",
			Tasks: []PlannedTask{{
				TempID:       consolidateTempID,
				Title:        fmt.Sprintf("Consolidate %s Review Round %d", baseTitle, round),
				Description:  c.buildSpecCrossCritiqueConsolidationTaskDescription(goal, specPath, round, len(reviewers)),
				AssignTo:     primary,
				DependsOn:    consolidateDependsOn,
				Priority:     1,
				FilesTouched: []string{consolidationOutputPath},
			}},
		}
}

func (c *Coordinator) roleExistsLocked(role string) bool {
	for _, state := range c.agentState {
		if state.Role == role {
			return true
		}
	}
	return false
}

func (c *Coordinator) normalizedGoalTitle(goal *Goal) string {
	baseTitle := strings.TrimSpace(goal.Title)
	if baseTitle == "" {
		baseTitle = "goal"
	}
	return baseTitle
}

func (c *Coordinator) resolveGoalReviewRounds(requested int) int {
	if requested > 0 {
		return requested
	}
	if c.config.Workflow.DefaultReviewRounds > 0 {
		return c.config.Workflow.DefaultReviewRounds
	}
	return 6
}

func (c *Coordinator) resolveGoalWorkflowRecipe(requested string) string {
	recipe := strings.TrimSpace(requested)
	if recipe == "" {
		recipe = strings.TrimSpace(c.config.Workflow.DefaultRecipe)
	}
	switch recipe {
	case "", workflowRecipeSpecReviewLoop:
		return workflowRecipeSpecReviewLoop
	case workflowRecipeSpecCrossCritiqueLoop:
		return workflowRecipeSpecCrossCritiqueLoop
	default:
		return workflowRecipeSpecReviewLoop
	}
}

func (c *Coordinator) goalWorkflowRecipeLocked(goal *Goal) string {
	if goal != nil && strings.TrimSpace(goal.WorkflowRecipe) != "" {
		return c.resolveGoalWorkflowRecipe(goal.WorkflowRecipe)
	}
	return c.resolveGoalWorkflowRecipe("")
}

func (c *Coordinator) goalReviewRoundsLocked(goal *Goal) int {
	if goal != nil && goal.MaxReviewRounds > 0 {
		return goal.MaxReviewRounds
	}
	return c.resolveGoalReviewRounds(0)
}

func (c *Coordinator) buildDiscoveryTaskDescription(goal *Goal) string {
	slug := c.goalSlug(goal)
	reqPath := filepath.ToSlash(filepath.Join("discussions", slug, "requirements.md"))
	return strings.TrimSpace(strings.Join([]string{
		"Analyze the source documents in the workspace and extract structured requirements.",
		"Produce a requirements summary covering: actors, scope, constraints, priorities, assumptions, and open questions.",
		fmt.Sprintf("Write the requirements to: %s", reqPath),
		"Format as markdown with clear section headings for each category.",
		"Flag any ambiguities or conflicts between source documents.",
		fmt.Sprintf("Goal title: %s", goal.Title),
		"Goal details:",
		goal.Description,
	}, "\n\n"))
}

func (c *Coordinator) buildDraftTaskDescription(goal *Goal) string {
	return strings.TrimSpace(strings.Join([]string{
		"Produce the initial deliverable for this goal.",
		fmt.Sprintf("Goal title: %s", goal.Title),
		"Goal details:",
		goal.Description,
		"Work in the shared workspace and leave clear output for the next phase.",
	}, "\n\n"))
}

func (c *Coordinator) buildReviewTaskDescription(goal *Goal) string {
	return strings.TrimSpace(strings.Join([]string{
		"Review the previous deliverable for correctness, completeness, and missing edge cases.",
		fmt.Sprintf("Goal title: %s", goal.Title),
		"Use the dependency context from the previous task as the review input.",
		"Return concise review feedback that the original author can act on.",
	}, "\n\n"))
}

func (c *Coordinator) goalSlug(goal *Goal) string {
	slug := strings.ToLower(strings.TrimSpace(goal.Title))
	if slug == "" {
		slug = "spec"
	}
	slug = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '-' || r == '_':
			return '-'
		default:
			return -1
		}
	}, slug)
	slug = strings.Trim(strings.Join(strings.FieldsFunc(slug, func(r rune) bool { return r == '-' }), "-"), "-")
	if slug == "" {
		slug = "spec"
	}
	return slug
}

func (c *Coordinator) specOutputPath(goal *Goal) string {
	return filepath.ToSlash(filepath.Join(defaultSpecOutputDir, c.goalSlug(goal)+"-spec.md"))
}

func specVersionPath(goalSlug string, version int) string {
	return filepath.ToSlash(filepath.Join(defaultSpecOutputDir, fmt.Sprintf("%s-spec-v%d.md", goalSlug, version)))
}

func (c *Coordinator) buildSpecPreparationTaskDescription(goal *Goal, specPath string) string {
	slug := c.goalSlug(goal)
	v0Path := specVersionPath(slug, 0)
	lines := []string{
		"Prepare an implementation-ready specification from the supplied technical materials.",
		fmt.Sprintf("Use the plan-spec skill located at: %s", specPrepSkillPath()),
		fmt.Sprintf("Write the specification to this workspace path: %s", v0Path),
		"Create the directory if needed. The file must be markdown.",
	}
	if c.config.Workflow.EnableDiscovery {
		reqPath := filepath.ToSlash(filepath.Join("discussions", slug, "requirements.md"))
		lines = append(lines, fmt.Sprintf("Use the requirements extracted in the discovery phase as your primary input. The requirements file is at: %s", reqPath))
	}
	lines = append(lines,
		"Use the technical documents and constraints in the goal details as source inputs.",
		"Your output must leave the spec ready for adversarial review by another agent.",
		"At the end, summarize the output path, major assumptions addressed, and any remaining open questions.",
		fmt.Sprintf("Goal title: %s", goal.Title),
		"Goal details:",
		goal.Description,
	)
	return strings.TrimSpace(strings.Join(lines, "\n\n"))
}

func (c *Coordinator) buildSpecReviewTaskDescription(goal *Goal, specPath string, round int, reviewerIndex int, reviewerCount int) string {
	// Determine the latest spec version: for round 1, reviewers read v0 (the initial preparation).
	// For subsequent rounds, the previous consolidation wrote v{round-1}.
	slug := c.goalSlug(goal)
	latestVersion := round - 1
	reviewSpecPath := specVersionPath(slug, latestVersion)

	// Reviewer focus specialization: assign different review lenses to each reviewer.
	var lensLine string
	switch reviewerIndex {
	case 1:
		lensLine = "Your assigned review lenses for this round: CLARITY, CORRECTNESS, SECURITY, COMPLETENESS."
	case 2:
		lensLine = "Your assigned review lenses for this round: CONSISTENCY, FEASIBILITY, OPERABILITY, COMPLEXITY."
	default:
		lensLine = "Your assigned review lenses for this round: CLARITY, CORRECTNESS, SECURITY, COMPLETENESS."
	}

	return strings.TrimSpace(strings.Join([]string{
		fmt.Sprintf("Perform adversarial review round %d on the prepared specification.", round),
		fmt.Sprintf("You are reviewer %d of %d parallel reviewers for this round.", reviewerIndex, reviewerCount),
		fmt.Sprintf("Use the grill-spec skill located at: %s", specReviewSkillPath()),
		fmt.Sprintf("Review this workspace file only: %s", reviewSpecPath),
		lensLine,
		"While you should note any issue you find, prioritize findings in your assigned lenses.",
		"Assume the spec will be handed to an AI coding agent. Your job is to find every ambiguity, hidden assumption, missing edge case, missing acceptance criterion, and any unanswered engineering question.",
		"Return a strict review report starting with exactly one of these first lines:",
		"VERDICT: PASS",
		"VERDICT: FAIL",
		"If the verdict is FAIL, include sections named BLOCKERS, AMBIGUITIES, ASSUMPTIONS, QUESTIONS, and OPERABILITY GAPS.",
		"If the verdict is PASS, explicitly state that the spec has no unaddressed ambiguities or unanswered questions for an AI coding agent.",
		"",
		"After your verdict and sections, include a structured findings block for automated tracking.",
		"Wrap it in a JSON code fence exactly like this:",
		"```json",
		`{"findings":[{"severity":"CRITICAL|MAJOR|MINOR|OBSERVATION","affected_section":"section name","description":"what is wrong","recommendation":"how to fix"}]}`,
		"```",
		"Every issue mentioned in your BLOCKERS/AMBIGUITIES/ASSUMPTIONS sections must appear as a finding.",
		fmt.Sprintf("Goal title: %s", goal.Title),
	}, "\n\n"))
}

func (c *Coordinator) buildSpecCritiqueTaskDescription(goal *Goal, specPath string, round int, reviewerIndex int, peerIndex int, reviewerCount int) string {
	slug := c.goalSlug(goal)
	latestVersion := round - 1
	critiqueSpecPath := specVersionPath(slug, latestVersion)
	return strings.TrimSpace(strings.Join([]string{
		fmt.Sprintf("Critique the peer review from adversarial review round %d.", round),
		fmt.Sprintf("You are reviewer %d of %d and you are critiquing reviewer %d's report.", reviewerIndex, reviewerCount, peerIndex),
		fmt.Sprintf("The specification under review is still: %s", critiqueSpecPath),
		"Use the dependency context from both adversarial review outputs, but focus on the other reviewer's reasoning, completeness, and whether they missed issues or raised weak objections.",
		"Return a strict critique report. This is not a pass/fail step.",
		"Use these exact section headings in this order:",
		"STRENGTHS",
		"MISSED ISSUES",
		"WEAK OBJECTIONS",
		"DISAGREEMENTS",
		"EXTRA QUESTIONS",
		"In STRENGTHS, note what the peer review covered well and what is worth preserving.",
		"In MISSED ISSUES, identify important defects, ambiguities, assumptions, or risks the peer review missed.",
		"In WEAK OBJECTIONS, call out findings that are unsupported, overstated, or low-value.",
		"In DISAGREEMENTS, explicitly state where you disagree with the peer review and why.",
		"In EXTRA QUESTIONS, list follow-up questions the spec creator should consider during consolidation.",
		"End with a short overall assessment of how useful and reliable the peer review is as an input to consolidation.",
		fmt.Sprintf("Goal title: %s", goal.Title),
	}, "\n\n"))
}

func (c *Coordinator) buildSpecConsolidationTaskDescription(goal *Goal, specPath string, round int, reviewerCount int) string {
	slug := c.goalSlug(goal)
	inputVersion := round - 1
	outputVersion := round
	inputPath := specVersionPath(slug, inputVersion)
	outputPath := specVersionPath(slug, outputVersion)
	findingsPath := fmt.Sprintf("discussions/%s/round-%02d/merged-findings-round-%02d.json", slug, round, round)
	return strings.TrimSpace(strings.Join([]string{
		fmt.Sprintf("Consolidate and amend the specification after adversarial review round %d.", round),
		fmt.Sprintf("Use the plan-spec skill located at: %s", specPrepSkillPath()),
		fmt.Sprintf("Read the current specification from: %s", inputPath),
		fmt.Sprintf("Write the consolidated specification to this new version path: %s", outputPath),
		"Create the directory if needed. The file must be markdown.",
		fmt.Sprintf("Use the dependency context from %d adversarial reviewer outputs as the authoritative review input.", reviewerCount),
		fmt.Sprintf("A merged findings ledger with deduplicated, ID-tagged findings is available at: %s", findingsPath),
		"Reference findings by their ID (e.g. CRIT-001, MAJ-002) in your consolidation summary.",
		"For each finding, state whether you: INCORPORATED it, DISMISSED it (with reason: DUPLICATE, OUT_OF_SCOPE, CONTRADICTED_BY_REQUIREMENT, or REVIEWER_ERROR), or DEFERRED it.",
		"Incorporate every accepted change into the specification.",
		"If you disagree with a reviewer point, record the disagreement and rationale clearly in your summary so the next round can inspect it.",
		"If any item cannot be fully resolved from the source materials, document it explicitly in the spec and in your summary.",
		"At the end, provide a concise issue-by-issue consolidation summary covering agree, disagree, and incorporate decisions.",
		fmt.Sprintf("Goal title: %s", goal.Title),
		"Goal details:",
		goal.Description,
	}, "\n\n"))
}

func (c *Coordinator) buildSpecCrossCritiqueConsolidationTaskDescription(goal *Goal, specPath string, round int, reviewerCount int) string {
	slug := c.goalSlug(goal)
	inputVersion := round - 1
	outputVersion := round
	inputPath := specVersionPath(slug, inputVersion)
	outputPath := specVersionPath(slug, outputVersion)
	findingsPath := fmt.Sprintf("discussions/%s/round-%02d/merged-findings-round-%02d.json", slug, round, round)
	return strings.TrimSpace(strings.Join([]string{
		fmt.Sprintf("Consolidate and amend the specification after adversarial review round %d and the reviewer cross-critiques.", round),
		fmt.Sprintf("Use the plan-spec skill located at: %s", specPrepSkillPath()),
		fmt.Sprintf("Read the current specification from: %s", inputPath),
		fmt.Sprintf("Write the consolidated specification to this new version path: %s", outputPath),
		"Create the directory if needed. The file must be markdown.",
		fmt.Sprintf("Use the dependency context from %d reviewer reports and %d reviewer-to-reviewer critiques as authoritative input.", reviewerCount, reviewerCount),
		fmt.Sprintf("A merged findings ledger with deduplicated, ID-tagged findings is available at: %s", findingsPath),
		"Reference findings by their ID (e.g. CRIT-001, MAJ-002) in your consolidation summary.",
		"For each finding, state whether you: INCORPORATED it, DISMISSED it (with reason: DUPLICATE, OUT_OF_SCOPE, CONTRADICTED_BY_REQUIREMENT, or REVIEWER_ERROR), or DEFERRED it.",
		"Incorporate every accepted change into the specification.",
		"If you disagree with a reviewer or critique point, record the disagreement and rationale clearly in your summary so the next round can inspect it.",
		"If any item cannot be fully resolved from the source materials, document it explicitly in the spec and in your summary.",
		"At the end, provide a concise issue-by-issue consolidation summary covering agree, disagree, and incorporate decisions.",
		fmt.Sprintf("Goal title: %s", goal.Title),
		"Goal details:",
		goal.Description,
	}, "\n\n"))
}

func (c *Coordinator) checkCircuitBreakersLocked(goal *Goal) (string, bool) {
	if goal == nil {
		return "", false
	}
	// Wall clock check.
	if limit := c.config.Workflow.MaxWallClockMinutes; limit > 0 {
		elapsed := time.Since(goal.CreatedAt)
		if elapsed > time.Duration(limit)*time.Minute {
			return fmt.Sprintf("circuit breaker: wall clock exceeded %d minutes (elapsed %s)", limit, elapsed.Truncate(time.Second)), true
		}
	}
	// Token cost check: sum all agent-state tokens for tasks belonging to this goal.
	if limit := c.config.Workflow.MaxCostTokens; limit > 0 {
		totalTokens := 0
		goalTaskAgents := map[string]bool{}
		for _, taskID := range c.taskOrder {
			task := c.tasks[taskID]
			if task != nil && task.GoalID == goal.ID && task.AssignedTo != "" {
				goalTaskAgents[task.AssignedTo] = true
			}
		}
		for agentName := range goalTaskAgents {
			if state := c.agentState[agentName]; state != nil {
				totalTokens += state.TotalTokensIn + state.TotalTokensOut
			}
		}
		if totalTokens > limit {
			return fmt.Sprintf("circuit breaker: total tokens %d exceeded limit %d", totalTokens, limit), true
		}
	}
	return "", false
}

// checkFindingStalenessLocked detects CRITICAL or MAJOR findings that persist
// unchanged (still "raised") across consecutive review rounds. This is a circuit
// breaker: if 2 or more such findings are stale, the goal should be blocked.
// Only meaningful when currentRound >= 3 (need at least 2 prior rounds).
func (c *Coordinator) checkFindingStalenessLocked(goal *Goal, currentRound int) (string, bool) {
	if goal == nil || currentRound < 3 {
		return "", false
	}
	slug := c.goalSlug(goal)

	currentLedger, err := ReadFindingsLedger(c.workspace, slug, currentRound)
	if err != nil || currentLedger == nil {
		return "", false
	}
	priorLedger, err := ReadFindingsLedger(c.workspace, slug, currentRound-1)
	if err != nil || priorLedger == nil {
		return "", false
	}

	// Index prior findings by ID for lookup.
	priorByID := make(map[string]*Finding, len(priorLedger.Findings))
	for _, f := range priorLedger.Findings {
		priorByID[f.ID] = f
	}

	const stalenessThreshold = 2
	staleCount := 0
	staleIDs := make([]string, 0)
	for _, f := range currentLedger.Findings {
		if f.Severity != SeverityCritical && f.Severity != SeverityMajor {
			continue
		}
		if f.Status != FindingRaised {
			continue
		}
		prior, ok := priorByID[f.ID]
		if !ok {
			continue
		}
		if prior.Status == FindingRaised {
			staleCount++
			staleIDs = append(staleIDs, f.ID)
		}
	}

	if staleCount >= stalenessThreshold {
		reason := fmt.Sprintf("circuit breaker: %d CRITICAL/MAJOR findings unchanged across rounds %d and %d (%s)",
			staleCount, currentRound-1, currentRound, strings.Join(staleIDs, ", "))
		return reason, true
	}
	return "", false
}

// mergeReviewFindingsLocked parses structured findings from reviewer task
// results, deduplicates them, assigns stable IDs, writes the merged ledger
// to the workspace, and returns the formatted text for consolidation prompts.
func (c *Coordinator) mergeReviewFindingsLocked(goal *Goal, reviewTasks []*Task, round int) string {
	slug := c.goalSlug(goal)

	// Load prior round ledger so we can merge across rounds.
	var existing []*Finding
	if round > 1 {
		if prev, err := ReadFindingsLedger(c.workspace, slug, round-1); err == nil && prev != nil {
			existing = prev.Findings
		}
	}

	// Parse structured findings from each reviewer's output.
	var roundFindings []*Finding
	for _, task := range reviewTasks {
		if task == nil || task.Result == "" {
			continue
		}
		source := task.AssignedTo
		parsed := ParseStructuredFindings(task.Result, source, round)
		roundFindings = append(roundFindings, parsed...)
	}

	// Merge with existing findings and assign stable IDs.
	merged := MergeFindings(existing, roundFindings)

	// Write the ledger to the workspace.
	ledger := &FindingsLedger{
		GoalID:   goal.ID,
		Round:    round,
		Findings: merged,
	}
	_ = WriteFindingsLedger(c.workspace, slug, round, ledger)

	if len(merged) == 0 {
		return ""
	}
	return FormatFindingsForPrompt(merged)
}

func (c *Coordinator) failGoalLocked(goal *Goal, summary string, content string) {
	if goal == nil {
		return
	}
	now := time.Now().UTC()
	goal.Status = GoalFailed
	goal.Summary = compactGoalSummary(summary)
	goal.CompletedAt = &now
	if c.currentGoalID == goal.ID {
		c.currentGoalID = ""
	}
	c.brainState.PendingHumanInput = nil
	c.recordGoalLocked(goal, firstNonEmpty(content, "goal failed"))
}

func (c *Coordinator) blockGoalLocked(goal *Goal, summary string, content string) {
	if goal == nil {
		return
	}
	goal.Status = GoalBlocked
	goal.Summary = compactGoalSummary(summary)
	goal.CompletedAt = nil
	c.recordGoalLocked(goal, firstNonEmpty(content, "goal blocked"))
}

func (c *Coordinator) gateGoalLocked(goal *Goal, gateType string, message string, round int) {
	if goal == nil {
		return
	}
	goal.Status = GoalGated
	goal.ActiveGate = &HumanGate{
		Type:      gateType,
		Message:   message,
		Round:     round,
		CreatedAt: time.Now().UTC(),
	}
	c.recordGoalLocked(goal, fmt.Sprintf("goal gated: %s", gateType))
}

func (c *Coordinator) ResolveGate(goalID string, approved bool, feedback string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	goal, ok := c.goals[goalID]
	if !ok {
		return errors.New("goal not found")
	}
	if goal.Status != GoalGated || goal.ActiveGate == nil {
		return errors.New("goal does not have an active gate")
	}

	gateType := goal.ActiveGate.Type
	goal.ActiveGate = nil

	if approved {
		goal.Status = GoalActive
		goal.Summary = ""
		c.recordGoalLocked(goal, fmt.Sprintf("gate %s approved by human", gateType))
		c.signalDispatchLocked()
		return nil
	}

	// Rejected
	switch gateType {
	case "escalation":
		goal.Status = GoalBlocked
		goal.Summary = compactGoalSummary(firstNonEmpty(feedback, "rejected by human at escalation gate"))
		c.recordGoalLocked(goal, "escalation gate rejected by human")
	default: // "discovery_review" and others
		now := time.Now().UTC()
		goal.Status = GoalFailed
		goal.Summary = compactGoalSummary(firstNonEmpty(feedback, "requirements rejected by human"))
		goal.CompletedAt = &now
		if c.currentGoalID == goal.ID {
			c.currentGoalID = ""
		}
		c.recordGoalLocked(goal, "discovery gate rejected by human")
	}
	return nil
}

func compactGoalSummary(summary string) string {
	summary = strings.Join(strings.Fields(strings.TrimSpace(summary)), " ")
	if len(summary) <= 320 {
		return summary
	}
	return strings.TrimSpace(summary[:319]) + "…"
}

func taskCommitPaths(task *Task) []string {
	if task == nil {
		return nil
	}
	paths := append([]string{}, task.FilesTouched...)
	if task.DiscussionFile != "" {
		paths = append(paths, task.DiscussionFile)
	}
	return normalizeWorkspacePaths(paths)
}

func (c *Coordinator) detectIntegrationConflictLocked(task *Task, filesChanged []string) string {
	if task == nil || task.GoalID == "" || len(filesChanged) == 0 {
		return ""
	}
	if !c.shouldDetectIntegrationConflictForTaskLocked(task) {
		return ""
	}
	relevantFiles := filterIntegrationConflictPaths(filesChanged)
	if len(relevantFiles) == 0 {
		return ""
	}
	parts := make([]string, 0)
	for _, other := range c.tasks {
		if other == nil || other.ID == task.ID || other.GoalID != task.GoalID {
			continue
		}
		if !c.shouldDetectIntegrationConflictForTaskLocked(other) {
			continue
		}
		if other.PlanPhase != 0 && task.PlanPhase != 0 && other.PlanPhase != task.PlanPhase && other.Status != TaskRunning {
			continue
		}
		otherFiles := filterIntegrationConflictPaths(other.FilesChanged)
		if len(otherFiles) == 0 {
			continue
		}
		matches := intersectStrings(relevantFiles, otherFiles)
		if len(matches) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s overlaps on %s", other.ID, strings.Join(matches, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return fmt.Sprintf("Potential integration conflict after task %q (%s). Overlaps detected: %s", task.Title, task.ID, strings.Join(parts, "; "))
}

func (c *Coordinator) shouldDetectIntegrationConflictForTaskLocked(task *Task) bool {
	if task == nil {
		return false
	}
	stage := c.workflowStageForTaskLocked(task)
	switch stage {
	case "preparing_spec", "consolidating_review", "amending_spec", "executing", "":
		return true
	default:
		return false
	}
}

func (c *Coordinator) workflowStageForTaskLocked(task *Task) string {
	if task == nil || c.brainState.CurrentPlan == nil || task.PlanPhase == 0 {
		return ""
	}
	for _, phase := range c.brainState.CurrentPlan.Phases {
		if phase.Number == task.PlanPhase {
			return c.workflowStageForPhase(phase)
		}
	}
	return ""
}

func filterIntegrationConflictPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(filepath.ToSlash(path))
		if trimmed == "" {
			continue
		}
		if trimmed == ".DS_Store" || strings.HasSuffix(trimmed, "/.DS_Store") {
			continue
		}
		if strings.HasPrefix(trimmed, "discussions/") {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	return filtered
}

func intersectStrings(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	lookup := make(map[string]bool, len(a))
	for _, value := range a {
		lookup[value] = true
	}
	out := make([]string, 0)
	for _, value := range b {
		if lookup[value] {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func repoSkillPath(name string) string {
	candidates := []string{
		filepath.Join(".agents", "skills", name),
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append([]string{filepath.Join(cwd, ".agents", "skills", name)}, candidates...)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".agents", "skills", name))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			if abs, err := filepath.Abs(candidate); err == nil {
				return filepath.ToSlash(abs)
			}
			return filepath.ToSlash(candidate)
		}
	}
	return filepath.ToSlash(candidates[0])
}

func specPrepSkillPath() string {
	return repoSkillPath("plan-spec")
}

func specReviewSkillPath() string {
	return repoSkillPath("grill-spec")
}

func combineReferencedFiles(discussionFile string, groups ...[]string) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	if discussionFile != "" {
		seen[discussionFile] = true
		out = append(out, discussionFile)
	}
	for _, group := range groups {
		for _, entry := range group {
			entry = strings.TrimSpace(entry)
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			out = append(out, entry)
		}
	}
	sort.Strings(out)
	return out
}

func (c *Coordinator) discussionFilePathLocked(task *Task) string {
	if task == nil {
		return ""
	}
	goalSlug := "manual"
	if goal := c.goals[task.GoalID]; goal != nil {
		goalSlug = slugifyPathFragment(goal.Title)
	}
	if goalSlug == "" {
		goalSlug = "manual"
	}
	roleSlug := "agent"
	if state := c.agentState[task.AssignedTo]; state != nil && state.Role != "" {
		roleSlug = slugifyPathFragment(state.Role)
	}
	agentSlug := slugifyPathFragment(task.AssignedTo)
	if agentSlug == "" {
		agentSlug = "agent"
	}
	phaseTitle := c.phaseTitleForTaskLocked(task)
	phaseSlug := slugifyPathFragment(phaseTitle)
	if phaseSlug == "" {
		phaseSlug = "phase"
	}
	round := c.extractReviewRound(phaseTitle)
	fileName := fmt.Sprintf("%s__%s__round-%02d__%s.md", agentSlug, roleSlug, max(0, round), phaseSlug)
	return filepath.ToSlash(filepath.Join("discussions", goalSlug, fmt.Sprintf("round-%02d", max(0, round)), phaseSlug, fileName))
}

func (c *Coordinator) phaseTitleForTaskLocked(task *Task) string {
	if task == nil {
		return ""
	}
	if c.brainState.CurrentPlan != nil && task.PlanPhase != 0 {
		for _, phase := range c.brainState.CurrentPlan.Phases {
			if phase.Number == task.PlanPhase {
				return phase.Title
			}
		}
	}
	return firstNonEmpty(task.Title, "manual")
}

func slugifyPathFragment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '-' || r == '_':
			return '-'
		default:
			return -1
		}
	}, value)
	value = strings.Trim(strings.Join(strings.FieldsFunc(value, func(r rune) bool { return r == '-' }), "-"), "-")
	return value
}

func reviewApproves(summary string) bool {
	text := strings.ToLower(summary)
	switch {
	case strings.Contains(text, "verdict: pass"):
		return true
	case strings.Contains(text, "verdict: fail"):
		return false
	default:
		return containsAny(text, "approved with no blockers", "no blocking issues", "no unaddressed ambiguities", "ready for implementation")
	}
}

func evaluateTaskVerdicts(tasks []*Task) (bool, []string, []string) {
	allPassed := true
	failedSummaries := make([]string, 0, len(tasks))
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task == nil {
			allPassed = false
			continue
		}
		taskIDs = append(taskIDs, task.ID)
		if reviewApproves(task.Result) {
			continue
		}
		allPassed = false
		if summary := strings.TrimSpace(task.Result); summary != "" {
			failedSummaries = append(failedSummaries, summary)
		}
	}
	return allPassed, failedSummaries, taskIDs
}

func extractErrorOutput(err error) string {
	if err == nil {
		return ""
	}
	var commandErr *CommandExecutionError
	if errors.As(err, &commandErr) {
		return strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(commandErr.Stdout),
			strings.TrimSpace(commandErr.Stderr),
		}, "\n"))
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func humanMessageTaskTitle(agentName, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return fmt.Sprintf("Human message to %s", agentName)
	}
	trimmed = strings.Join(strings.Fields(trimmed), " ")
	runes := []rune(trimmed)
	if len(runes) > 48 {
		trimmed = string(runes[:48]) + "..."
	}
	return fmt.Sprintf("Human message to %s: %s", agentName, trimmed)
}
