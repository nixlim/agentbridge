package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type dispatchResult struct {
	TaskID     string
	AgentName  string
	Result     *AgentResult
	Err        error
	RetryCount int
}

type Coordinator struct {
	config        Config
	agents        map[string]Agent
	agentState    map[string]*AgentState
	tasks         map[string]*Task
	taskOrder     []string
	messages      []*Message
	pendingInbox  map[string][]string
	logStore      *MessageStore
	workspace     *Workspace
	hub           *WebSocketHub
	results       chan dispatchResult
	dispatchWake  chan struct{}
	stop          chan struct{}
	wg            sync.WaitGroup
	mu            sync.RWMutex
	activeCancels map[string]context.CancelFunc
	shuttingDown  bool
}

func NewCoordinator(cfg Config, agents map[string]Agent, workspace *Workspace, logStore *MessageStore, hub *WebSocketHub) *Coordinator {
	return &Coordinator{
		config:        cfg,
		agents:        agents,
		agentState:    newInitialAgentStates(cfg, agents),
		tasks:         map[string]*Task{},
		taskOrder:     []string{},
		messages:      []*Message{},
		pendingInbox:  map[string][]string{},
		logStore:      logStore,
		workspace:     workspace,
		hub:           hub,
		results:       make(chan dispatchResult, 128),
		dispatchWake:  make(chan struct{}, 1),
		stop:          make(chan struct{}),
		activeCancels: map[string]context.CancelFunc{},
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
	defer c.mu.Unlock()

	task, ok := c.tasks[taskID]
	if !ok {
		return errors.New("task not found")
	}
	if err := task.ApproveReview(); err != nil {
		return err
	}
	c.recordTaskLocked(task, "review approved")
	return nil
}

func (c *Coordinator) RejectTask(taskID, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	task, ok := c.tasks[taskID]
	if !ok {
		return errors.New("task not found")
	}
	if err := task.RejectReview(reason); err != nil {
		return err
	}
	c.recordTaskLocked(task, "review rejected")
	return nil
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
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})

	for _, candidate := range tasks {
		task := c.tasks[candidate.ID]
		if task == nil || (task.Status != TaskPending && task.Status != TaskBlocked) {
			continue
		}
		state, ok := c.agentState[task.AssignedTo]
		if !ok || state.Status == AgentOffline || state.Status == AgentError || state.Status == AgentBusy {
			continue
		}
		if !c.dependenciesMetLocked(task) {
			if task.Status != TaskBlocked {
				_ = task.SetBlocked()
				c.recordTaskLocked(task, "waiting for dependencies")
			}
			continue
		}
		_ = task.MarkPending()
		if err := task.Start(); err != nil {
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
	forwarded := append([]string(nil), c.pendingInbox[task.AssignedTo]...)
	c.pendingInbox[task.AssignedTo] = nil
	deps := c.completedDependencySummariesLocked(task)
	prompt := buildWrappedPrompt(c.workspace.Path(), forwarded, deps, task)

	msg := NewMessage(MsgCoordinatorToAgent, "coordinator", task.AssignedTo, task.ID, prompt)
	c.appendMessageLocked(msg)

	ctx, cancel := context.WithCancel(context.Background())
	c.activeCancels[task.ID] = cancel

	c.wg.Add(1)
	go func(agentName string, prompt string, task *Task) {
		defer c.wg.Done()
		defer cancel()

		result, err := agent.Execute(ctx, prompt, c.workspace.Path())
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
	c.mu.Lock()
	defer c.mu.Unlock()

	task, ok := c.tasks[dr.TaskID]
	if !ok {
		return
	}
	state := c.agentState[dr.AgentName]
	delete(c.activeCancels, dr.TaskID)

	if task.Status == TaskCancelled {
		if state != nil {
			state.Status = AgentIdle
			state.CurrentTask = ""
			state.LastActivity = time.Now().UTC()
			c.recordAgentLocked(state, "agent idle")
		}
		return
	}

	if dr.Err != nil {
		task.RetryCount++
		errMsg := dr.Err.Error()
		agentMsg := NewMessage(MsgAgentToCoordinator, dr.AgentName, "coordinator", task.ID, errMsg)
		agentMsg.Metadata.Error = errMsg
		c.appendMessageLocked(agentMsg)

		if task.RetryCount <= task.MaxRetries {
			task.Status = TaskPending
			task.Result = fmt.Sprintf("retry %d/%d: %s", task.RetryCount, task.MaxRetries, errMsg)
			if state != nil {
				state.Status = AgentIdle
				state.CurrentTask = ""
				state.LastActivity = time.Now().UTC()
				c.recordAgentLocked(state, "agent idle after retry")
			}
			c.recordTaskLocked(task, "task retry scheduled")
			c.signalDispatchLocked()
			return
		}

		_ = task.Fail(errMsg)
		if state != nil {
			state.Status = AgentError
			state.CurrentTask = ""
			state.LastActivity = time.Now().UTC()
			c.recordAgentLocked(state, "agent entered error state")
		}
		c.recordTaskLocked(task, "task failed")
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
	}
	c.appendMessageLocked(agentMsg)

	if task.IsReviewTask {
		_ = task.Complete(summary, filesChanged, commitHash)
		if parent, ok := c.tasks[task.ParentID]; ok {
			parent.ReviewResult = summary
			c.recordTaskLocked(parent, "review result received")
			if !parent.IsHumanMessage {
				followUp := BuildReviewActionTask(parent, task, c.config.Agents[parent.AssignedTo].MaxRetries)
				parent.ReviewActionTaskID = followUp.ID
				c.tasks[followUp.ID] = followUp
				c.taskOrder = append(c.taskOrder, followUp.ID)
				c.recordTaskLocked(parent, "review action task created")
				c.recordTaskLocked(followUp, "task created")
				c.signalDispatchLocked()
			}
		}
		c.recordTaskLocked(task, "task completed")
	} else if task.ReviewBy != "" {
		_ = task.MoveToReview(summary, filesChanged, commitHash)
		reviewTask := BuildReviewTask(task, task.ReviewBy, c.config.Agents[task.ReviewBy].MaxRetries)
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

func (c *Coordinator) completedDependencySummariesLocked(task *Task) []string {
	summaries := make([]string, 0, len(task.DependsOn))
	for _, depID := range task.DependsOn {
		if dep, ok := c.tasks[depID]; ok && dep.Result != "" {
			summaries = append(summaries, fmt.Sprintf("%s: %s", dep.Title, dep.Result))
		}
	}
	return summaries
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
}

func (c *Coordinator) recordAgentLocked(agent *AgentState, content string) {
	clone := *agent
	msg := NewMessage(MsgSystemEvent, "coordinator", "human", "", content)
	msg.Metadata.Agent = &clone
	c.appendMessageLocked(msg)
	c.hub.Broadcast("agent_status", &clone)
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
	return map[string]interface{}{
		"agents":   c.cloneAgentStatesLocked(),
		"tasks":    c.cloneTasksLocked(),
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
	if _, ok := c.agents[req.AssignedTo]; !ok {
		return nil, fmt.Errorf("unknown agent %q", req.AssignedTo)
	}
	for _, depID := range req.DependsOn {
		if _, ok := c.tasks[depID]; !ok {
			return nil, fmt.Errorf("dependency %q not found", depID)
		}
	}
	task := NewTask(req, c.config.Agents[req.AssignedTo].MaxRetries)
	c.tasks[task.ID] = task
	c.taskOrder = append(c.taskOrder, task.ID)
	return task, nil
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
