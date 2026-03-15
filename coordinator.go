package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	c.wg.Add(2)
	go c.eventLoop()
	go c.brainLoop()
}

func (c *Coordinator) Stop(ctx context.Context) error {
	c.mu.Lock()
	c.shuttingDown = true
	for _, cancel := range c.activeCancels {
		cancel()
	}
	if c.brainCancel != nil {
		c.brainCancel()
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
			if task.Status == TaskRunning {
				task.Status = TaskPending
				task.StartedAt = nil
			}
			c.tasks[task.ID] = task
			if !containsString(c.taskOrder, task.ID) {
				c.taskOrder = append(c.taskOrder, task.ID)
			}
		}
		if msg.Metadata.Agent != nil {
			agent := *msg.Metadata.Agent
			if agent.Status == AgentBusy {
				agent.Status = AgentIdle
				agent.CurrentTask = ""
			}
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
	for i := len(c.goalOrder) - 1; i >= 0; i-- {
		goal := c.goals[c.goalOrder[i]]
		if goal == nil {
			continue
		}
		if goal.Status == GoalPlanning || goal.Status == GoalActive {
			c.currentGoalID = goal.ID
			break
		}
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
	if active := c.currentGoalLocked(); active != nil && (active.Status == GoalPlanning || active.Status == GoalActive) {
		c.mu.Unlock()
		return nil, errors.New("an active goal already exists")
	}

	goal := &Goal{
		ID:          uuid.NewString(),
		Title:       strings.TrimSpace(req.Title),
		Description: strings.TrimSpace(req.Description),
		Status:      GoalPlanning,
		CreatedAt:   time.Now().UTC(),
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

func (c *Coordinator) KillGoal(goalID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	goal, ok := c.goals[goalID]
	if !ok {
		return errors.New("goal not found")
	}
	now := time.Now().UTC()
	goal.Status = GoalFailed
	goal.Summary = "killed by human"
	goal.CompletedAt = &now
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
		_ = task.Cancel("killed with goal")
		c.workspaceLocks.Unlock(task.ID)
		if cancel := c.activeCancels[task.ID]; cancel != nil {
			cancel()
		}
		c.recordTaskLocked(task, "task cancelled with goal")
	}
	c.recordGoalLocked(goal, "goal killed")
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
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case result := <-c.results:
			c.handleDispatchResult(result)
		case <-ticker.C:
			c.reconcileTasks()
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
		state, ok := c.agentState[task.AssignedTo]
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

	if task.Status == TaskCancelled {
		if state != nil {
			state.Status = AgentIdle
			state.CurrentTask = ""
			state.LastActivity = time.Now().UTC()
			c.recordAgentLocked(state, "agent idle")
		}
		c.mu.Unlock()
		return
	}

	if dr.Err != nil {
		task.RetryCount++
		errMsg := dr.Err.Error()
		agentMsg := NewMessage(MsgAgentToCoordinator, dr.AgentName, "coordinator", task.ID, errMsg)
		agentMsg.Metadata.Error = errMsg
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
				c.failGoalLocked(goal, fmt.Sprintf("task %q failed: %s", task.Title, errMsg), "goal failed")
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

	if hash, committedFiles, err := c.workspace.CommitTask(dr.AgentName, task.Title, task.ID); err == nil {
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
					c.failGoalLocked(goal, conflictContext, "goal failed")
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
	builder.WriteString("Files in workspace:\n")
	builder.WriteString(c.workspaceTreeLocked())
	builder.WriteString("\n\n")

	for _, depID := range task.DependsOn {
		if dep := c.tasks[depID]; dep != nil {
			builder.WriteString(fmt.Sprintf("--- Prior work by %s: %s ---\n", dep.AssignedTo, dep.Title))
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
		"brain":        c.brainState,
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
	if c.currentGoalID == "" {
		return nil
	}
	return c.goals[c.currentGoalID]
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

func (c *Coordinator) workspaceTreeLocked() string {
	files, err := c.workspace.ListFiles()
	if err != nil || len(files) == 0 {
		return "(workspace is empty)"
	}
	if len(files) > 200 {
		files = files[:200]
		files = append(files, "... (truncated)")
	}
	return strings.Join(files, "\n")
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

func (c *Coordinator) advanceGoalWorkflowLocked() error {
	goal := c.currentGoalLocked()
	if goal == nil || c.planExecutor == nil || c.brainState.CurrentPlan == nil {
		return nil
	}

	phaseIndex := c.planExecutor.currentPhase
	if phaseIndex >= 0 && phaseIndex < len(c.brainState.CurrentPlan.Phases) {
		if !c.phaseTasksCompleteLocked(phaseIndex) {
			return nil
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
		Mode:        "deterministic",
		PlannerMode: c.workflowPlannerModeLocked(),
		Status:      "idle",
	}

	goal := c.currentGoalLocked()
	if goal == nil {
		return state
	}

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

	if goal.Status == GoalActive {
		if c.phaseTasksCompleteLocked(phaseIndex) {
			state.Status = "transitioning"
		} else {
			state.Status = "executing"
		}
	}

	return state
}

func (c *Coordinator) workflowPlannerModeLocked() string {
	switch c.config.Brain.PlanningStyle {
	case "manual":
		return "manual"
	case "boundary", "upfront", "rolling":
		return "boundary"
	default:
		return "disabled"
	}
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
	reviewer := c.chooseReviewerRoleLocked(primary)

	baseTitle := strings.TrimSpace(goal.Title)
	if baseTitle == "" {
		baseTitle = "goal"
	}

	phases := []Phase{
		{
			Number:      1,
			Title:       "Draft",
			Description: "Create the primary deliverable for the goal.",
			Tasks: []PlannedTask{
				{
					TempID:      "draft",
					Title:       fmt.Sprintf("Draft %s", baseTitle),
					Description: c.buildDraftTaskDescription(goal),
					AssignTo:    primary,
					Priority:    1,
				},
			},
		},
	}

	if reviewer != "" {
		phases = append(phases,
			Phase{
				Number:      2,
				Title:       "Review",
				Description: "Review the draft deliverable and identify concrete issues.",
				Tasks: []PlannedTask{
					{
						TempID:      "review",
						Title:       fmt.Sprintf("Review %s", baseTitle),
						Description: c.buildReviewTaskDescription(goal),
						AssignTo:    reviewer,
						DependsOn:   []string{"draft"},
						Priority:    1,
					},
				},
			},
			Phase{
				Number:      3,
				Title:       "Revise",
				Description: "Incorporate review feedback and finalize the deliverable.",
				Tasks: []PlannedTask{
					{
						TempID:      "revise",
						Title:       fmt.Sprintf("Revise %s", baseTitle),
						Description: c.buildReviseTaskDescription(goal),
						AssignTo:    primary,
						DependsOn:   []string{"review"},
						Priority:    1,
					},
				},
			},
		)
	}

	return &Plan{GoalID: goal.ID, Phases: phases}, nil
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

func (c *Coordinator) chooseReviewerRoleLocked(primary string) string {
	if !c.roleExistsLocked("reviewer") {
		return ""
	}
	if primary == "reviewer" {
		return ""
	}
	return "reviewer"
}

func (c *Coordinator) roleExistsLocked(role string) bool {
	for _, state := range c.agentState {
		if state.Role == role {
			return true
		}
	}
	return false
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

func (c *Coordinator) buildReviseTaskDescription(goal *Goal) string {
	return strings.TrimSpace(strings.Join([]string{
		"Revise the deliverable using the dependency context from the review task.",
		fmt.Sprintf("Goal title: %s", goal.Title),
		"Apply the review feedback, update the workspace artifacts, and provide a final summary.",
	}, "\n\n"))
}

func (c *Coordinator) failGoalLocked(goal *Goal, summary string, content string) {
	if goal == nil {
		return
	}
	now := time.Now().UTC()
	goal.Status = GoalFailed
	goal.Summary = summary
	goal.CompletedAt = &now
	if c.currentGoalID == goal.ID {
		c.currentGoalID = ""
	}
	c.brainState.PendingHumanInput = nil
	c.recordGoalLocked(goal, firstNonEmpty(content, "goal failed"))
}

func (c *Coordinator) detectIntegrationConflictLocked(task *Task, filesChanged []string) string {
	if task == nil || task.GoalID == "" || len(filesChanged) == 0 {
		return ""
	}
	parts := make([]string, 0)
	for _, other := range c.tasks {
		if other == nil || other.ID == task.ID || other.GoalID != task.GoalID {
			continue
		}
		if other.PlanPhase != 0 && task.PlanPhase != 0 && other.PlanPhase != task.PlanPhase && other.Status != TaskRunning {
			continue
		}
		if len(other.FilesChanged) == 0 {
			continue
		}
		matches := intersectStrings(filesChanged, other.FilesChanged)
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

func reviewApproves(summary string) bool {
	text := strings.ToLower(summary)
	return containsAny(text, "approve", "approved", "lgtm", "looks good", "ship it", "no issues")
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
