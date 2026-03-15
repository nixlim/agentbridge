# AgentBridge v2 — Team Coordination Addendum

## Specification Addendum: LLM-Brained Coordinator with Configurable Teams

**Extends:** SPEC.md v1.0  
**Version:** 2.0

---

## 1. Motivation

In v1, the coordinator is a deterministic dispatcher — it runs tasks in dependency order and routes messages mechanically. The human must decompose every task, decide assignments, and judge results.

In v2, the coordinator itself is powered by an LLM (the "brain"). It receives a high-level goal, decomposes it into a plan, assigns work to a team of role-specific agents, reviews results, handles conflicts, re-plans when things go wrong, and only escalates to the human when it genuinely needs input.

The human shifts from micromanager to executive: set the goal, watch the team work, intervene when needed.

---

## 2. Conceptual Model

```
Human sets goal
       │
       ▼
┌──────────────────────────────────────────┐
│         Coordinator Brain (LLM)          │
│                                          │
│  "I have a team of 4 agents. Let me      │
│   break this goal into tasks, assign     │
│   them based on each agent's role,       │
│   review output, and iterate."           │
│                                          │
│  Internal state:                         │
│   - The plan (task graph)                │
│   - Team roster + roles                  │
│   - Conversation history with each agent │
│   - What's done, what's blocked, what    │
│     needs re-work                        │
└────┬─────┬──────────┬──────────┬─────────┘
     │     │          │          │
     ▼     ▼          ▼          ▼
  Agent1  Agent2    Agent3    Agent4
  (arch)  (impl)   (impl)   (test)
```

The brain is not an agent in the team. It is the coordinator. It never writes code itself — it thinks, plans, delegates, reviews, and decides.

---

## 3. Configuration Changes

### 3.1 Updated Config Schema

```yaml
# agentbridge.yaml v2

server:
  host: "127.0.0.1"
  port: 8080

workspace:
  path: "./workspace"
  init_git: true

# NEW: The coordinator's own LLM backend
brain:
  provider: "claude"              # "claude" or "codex"
  command: "claude"
  args: ["-p", "--output-format", "json", "--verbose"]
  timeout_seconds: 120
  model_hint: ""                  # Optional: pass to CLI if supported
  system_prompt_file: ""          # Optional: path to custom system prompt
  max_context_messages: 50        # Max messages to include in brain context
  planning_style: "upfront"       # "upfront" | "rolling" — see Section 6

# NEW: Team composition
team:
  - name: "architect"
    provider: "claude"
    role: "architect"
    count: 1
    description: "Designs system architecture, defines interfaces, reviews structural decisions"
  - name: "impl-1"
    provider: "claude"
    role: "implementer"
    count: 1
    description: "Writes production code"
  - name: "impl-2"
    provider: "codex"
    role: "implementer"
    count: 1
    description: "Writes production code"
  - name: "tester"
    provider: "claude"
    role: "tester"
    count: 1
    description: "Writes and runs tests, reports failures"

# Agent provider templates (referenced by team[].provider)
providers:
  claude:
    command: "claude"
    args: ["-p", "--output-format", "json", "--verbose"]
    timeout_seconds: 300
    max_retries: 2
    env:
      CLAUDE_CODE_MAX_TURNS: "50"
  codex:
    command: "codex"
    args: ["--json", "--approval-mode", "full-auto"]
    timeout_seconds: 300
    max_retries: 2
    env: {}

log:
  file: "./agentbridge.log"
  level: "info"
```

### 3.2 New Config Structs

```go
type BrainConfig struct {
    Provider           string   `yaml:"provider"`
    Command            string   `yaml:"command"`
    Args               []string `yaml:"args"`
    TimeoutSeconds     int      `yaml:"timeout_seconds"`
    ModelHint          string   `yaml:"model_hint"`
    SystemPromptFile   string   `yaml:"system_prompt_file"`
    MaxContextMessages int      `yaml:"max_context_messages"`
    PlanningStyle      string   `yaml:"planning_style"`
}

type TeamMemberConfig struct {
    Name        string `yaml:"name"`
    Provider    string `yaml:"provider"`
    Role        string `yaml:"role"`
    Count       int    `yaml:"count"`
    Description string `yaml:"description"`
}

type ProviderConfig struct {
    Command        string            `yaml:"command"`
    Args           []string          `yaml:"args"`
    TimeoutSeconds int               `yaml:"timeout_seconds"`
    MaxRetries     int               `yaml:"max_retries"`
    Env            map[string]string `yaml:"env"`
}
```

---

## 4. Roles

Roles are semantic labels that tell the brain what an agent is good at. The brain uses roles to decide task assignment. Roles are not hardcoded — they are strings in config — but the brain's system prompt knows about common role archetypes.

### 4.1 Built-in Role Archetypes

The brain's system prompt should include guidance for these common roles. Users can define custom roles and the brain will interpret them from the `description` field.

| Role | Typical Responsibilities |
|------|------------------------|
| `architect` | System design, interface definitions, directory structure, dependency decisions, structural code review |
| `implementer` | Write production code, fix bugs, refactor, follow the architect's plan |
| `tester` | Write unit/integration/e2e tests, run test suites, report coverage, verify fixes |
| `reviewer` | Code review, security audit, style enforcement, documentation quality |
| `devops` | CI/CD config, Dockerfiles, deployment scripts, infrastructure-as-code |
| `docs` | README, API docs, user guides, inline documentation |
| `researcher` | Investigate libraries, read docs, prototype approaches, report findings |

### 4.2 Role in the Agent State

Extend `AgentState` from v1:

```go
type AgentState struct {
    Name           string      `json:"name"`
    Provider       string      `json:"provider"`       // NEW
    Role           string      `json:"role"`            // NEW
    Description    string      `json:"description"`     // NEW
    Status         AgentStatus `json:"status"`
    CurrentTask    string      `json:"current_task"`
    TasksCompleted int         `json:"tasks_completed"`
    TasksFailed    int         `json:"tasks_failed"`    // NEW
    TotalTokensIn  int         `json:"total_tokens_in"`
    TotalTokensOut int         `json:"total_tokens_out"`
    LastActivity   time.Time   `json:"last_activity"`
}
```

---

## 5. The Brain

### 5.1 What the Brain Is

The brain is an LLM invoked via the same CLI adapter pattern as team agents. However, it is never assigned tasks from the queue. Instead, the coordinator calls the brain at specific decision points (see Section 5.3).

The brain maintains a running context: a conversation history of its own prior decisions and their outcomes. Each brain invocation receives the current state and is asked to make a decision.

### 5.2 Brain State

```go
type BrainState struct {
    ConversationHistory []BrainMessage `json:"conversation_history"`
    CurrentPlan         *Plan          `json:"current_plan"`
    GoalDescription     string         `json:"goal_description"`
}

type BrainMessage struct {
    Role    string `json:"role"`    // "user" (coordinator feeding info) or "assistant" (brain's response)
    Content string `json:"content"`
    Timestamp time.Time `json:"timestamp"`
}
```

The conversation history is pruned to `max_context_messages` using a sliding window that always preserves: (a) the original goal, (b) the current plan, and (c) the most recent N messages.

### 5.3 Brain Decision Points

The coordinator invokes the brain at these moments:

| Trigger | Brain is Asked To |
|---------|-------------------|
| New goal submitted by human | Decompose into a task graph with assignments |
| Agent completes a task | Review the result, decide next step (accept, revise, create follow-up) |
| Agent fails a task | Diagnose, decide retry/reassign/re-plan |
| Multiple agents complete concurrent tasks | Integrate results, resolve conflicts |
| Agent produces output that references another agent's work | Decide if coordination message is needed |
| Human sends a message | Interpret intent, adjust plan if needed |
| All tasks in plan complete | Evaluate overall goal completion, decide if done or needs more work |
| Stall detected (no progress for N minutes) | Diagnose and unblock |

### 5.4 Brain Invocation

Each invocation is a single LLM call (not a multi-turn session). The coordinator constructs a prompt containing all necessary context, sends it, and parses the structured response.

```go
func (c *Coordinator) invokeBrain(trigger string, context string) (*BrainDecision, error) {
    prompt := c.buildBrainPrompt(trigger, context)
    result, err := c.brainAdapter.Execute(ctx, prompt, c.config.Workspace.Path)
    if err != nil {
        return nil, err
    }
    decision, err := parseBrainDecision(result.RawOutput)
    return decision, err
}
```

### 5.5 Brain System Prompt

The brain's system prompt is critical. It defines the brain's personality, capabilities, and output format. Store it in a file (`brain_system_prompt.md`) and load it at startup. Default content:

```markdown
You are the lead coordinator of a software engineering team. You do not write code yourself.
Your job is to plan, delegate, review, and coordinate.

## Your Team

{{TEAM_ROSTER}}

## Your Capabilities

You make decisions by responding with structured JSON. Every response must be valid JSON
matching one of the schemas below. Do not include any text outside the JSON.

## Response Format

Always respond with a JSON object containing a "decisions" array. Each decision is an action
you want the coordinator to execute.

{
  "thinking": "Your internal reasoning about the situation (visible to human observer but not to agents)",
  "decisions": [
    { "action": "create_task", ... },
    { "action": "send_message", ... },
    { "action": "complete_goal", ... },
    ...
  ]
}

## Available Actions

### create_task
Create a new task and assign it to a team member.
{
  "action": "create_task",
  "title": "Short task title",
  "description": "Detailed instructions for the agent. Be specific. Include file paths, function signatures, constraints.",
  "assign_to": "agent-name",
  "depends_on": ["task-id-1"],    // optional
  "review_by": "agent-name",      // optional
  "priority": 1                   // 1=highest, 5=lowest
}

### send_message
Send a message to an agent (will be prepended to their next task prompt) or to the human.
{
  "action": "send_message",
  "to": "agent-name | human",
  "content": "Your message"
}

### revise_task
Reject an agent's output and send them back to redo it with feedback.
{
  "action": "revise_task",
  "task_id": "the-task-id",
  "feedback": "What was wrong and what to fix"
}

### reassign_task
Take a task from one agent and give it to another.
{
  "action": "reassign_task",
  "task_id": "the-task-id",
  "new_agent": "agent-name",
  "reason": "Why"
}

### accept_task
Mark a completed task as accepted (its output becomes available to dependent tasks).
{
  "action": "accept_task",
  "task_id": "the-task-id"
}

### complete_goal
Signal that the overall goal is achieved.
{
  "action": "complete_goal",
  "summary": "What was accomplished"
}

### request_human_input
Escalate to the human when you are stuck or need a decision you cannot make.
{
  "action": "request_human_input",
  "question": "What you need from the human",
  "context": "Relevant background for the human"
}

### update_plan
Replace the current plan with a revised version (use after re-planning).
{
  "action": "update_plan",
  "plan": {
    "phases": [ ... ]  // see Plan schema
  },
  "reason": "Why the plan changed"
}

## Rules

1. Never assign work to yourself. You are the coordinator, not a coder.
2. Match tasks to agent roles. Don't ask a tester to do architecture.
3. Be specific in task descriptions. Agents have no memory of prior tasks unless you include context.
4. When an agent's work is done, always explicitly accept_task or revise_task. Never leave tasks in limbo.
5. If two agents need to modify the same file, sequence their tasks (dependency), don't run them in parallel.
6. When you detect a conflict or integration issue, create a reconciliation task.
7. Keep your "thinking" honest — the human is watching. Flag risks and uncertainties.
8. Prefer small, focused tasks over large vague ones. Each task should take an agent 1-5 minutes.
```

The `{{TEAM_ROSTER}}` placeholder is replaced at startup with the actual team config:

```
- architect (claude): Designs system architecture, defines interfaces. [idle]
- impl-1 (claude): Writes production code. [idle]
- impl-2 (codex): Writes production code. [idle]
- tester (claude): Writes and runs tests, reports failures. [idle]
```

The roster is updated with current status on each brain invocation.

### 5.6 Brain Prompt Template

Each brain invocation assembles a prompt from these sections:

```
## Current State

Goal: {{GOAL_DESCRIPTION}}

Team Status:
{{TEAM_ROSTER_WITH_STATUS}}

### Current Plan
{{PLAN_JSON or "No plan yet."}}

### Active Tasks
{{ACTIVE_TASKS_SUMMARY}}

### Recently Completed
{{LAST_N_COMPLETED_TASKS_WITH_RESULTS}}

## Trigger

{{TRIGGER_TYPE}}: {{TRIGGER_DETAILS}}

For example:
- "task_completed: Task 'Implement user model' (assigned to impl-1) finished. Output: Created user.go with User struct and CRUD methods. Files changed: models/user.go, models/user_test.go"
- "task_failed: Task 'Run integration tests' (assigned to tester) failed. Error: exit code 1. Stderr: FAIL TestUserCreate - expected 200 got 500"
- "human_message: 'Actually, use PostgreSQL instead of SQLite'"

## Conversation History
{{RECENT_BRAIN_MESSAGES}}

## Instructions

Analyze the current state and the trigger event. Decide what to do next. Respond with JSON only.
```

---

## 6. Planning

### 6.1 Plan Schema

```go
type Plan struct {
    ID          string   `json:"id"`
    GoalID      string   `json:"goal_id"`
    Phases      []Phase  `json:"phases"`
    CreatedAt   time.Time `json:"created_at"`
    Version     int      `json:"version"`        // Incremented on re-plan
}

type Phase struct {
    Number      int           `json:"number"`
    Title       string        `json:"title"`
    Description string        `json:"description"`
    Tasks       []PlannedTask `json:"tasks"`
}

type PlannedTask struct {
    TempID      string   `json:"temp_id"`        // Placeholder ID used in depends_on before real IDs exist
    Title       string   `json:"title"`
    Description string   `json:"description"`
    AssignTo    string   `json:"assign_to"`       // Role name or specific agent name
    DependsOn   []string `json:"depends_on"`      // TempIDs of other planned tasks
    ReviewBy    string   `json:"review_by"`
    Priority    int      `json:"priority"`
    RealTaskID  string   `json:"real_task_id"`    // Filled in when actually created
}
```

### 6.2 Planning Styles

**Upfront planning** (`planning_style: "upfront"`):

When a goal is submitted, the brain is asked to produce a complete plan covering all phases. All tasks from phase 1 are created immediately. Tasks from later phases are created as their dependencies complete.

Good for well-understood projects. Gives the human a clear roadmap upfront.

**Rolling planning** (`planning_style: "rolling"`):

When a goal is submitted, the brain plans only the first phase. As each phase completes, the brain is re-invoked to plan the next phase based on what was learned.

Good for exploratory or ambiguous projects. More adaptive but less predictable.

### 6.3 Plan Execution

The coordinator maintains a `PlanExecutor` that:

1. Receives a `Plan` from the brain.
2. Creates real `Task` objects for all tasks in the current phase.
3. Maps `TempID → RealTaskID` so dependency references resolve correctly.
4. Enqueues tasks that have no unmet dependencies.
5. As tasks complete, checks if the phase is done, then:
   - Upfront: creates tasks for the next phase.
   - Rolling: invokes the brain to plan the next phase.

```go
type PlanExecutor struct {
    plan        *Plan
    tempToReal  map[string]string   // TempID → real Task ID
    coordinator *Coordinator
}

func (pe *PlanExecutor) ExecutePhase(phaseNum int) error {
    phase := pe.plan.Phases[phaseNum]
    for _, pt := range phase.Tasks {
        realDeps := []string{}
        for _, dep := range pt.DependsOn {
            realDeps = append(realDeps, pe.tempToReal[dep])
        }
        task := &Task{
            ID:          uuid.New().String(),
            Title:       pt.Title,
            Description: pt.Description,
            AssignedTo:  pe.resolveAgent(pt.AssignTo),
            DependsOn:   realDeps,
            ReviewBy:    pt.ReviewBy,
            Status:      TaskPending,
            Priority:    pt.Priority,
        }
        pe.tempToReal[pt.TempID] = task.ID
        pe.coordinator.addTask(task)
    }
    return nil
}

// resolveAgent maps a role name to a specific idle agent of that role,
// or returns the name directly if it's already a specific agent.
func (pe *PlanExecutor) resolveAgent(nameOrRole string) string {
    // Check if nameOrRole matches a specific agent name first.
    // If not, find an available agent with that role.
    // If multiple agents share the role, pick the one with fewer active tasks.
}
```

---

## 7. Revised Coordinator Event Loop

The v1 coordinator was a simple dispatch loop. The v2 coordinator is driven by the brain.

### 7.1 Goal Submission

Replace the v1 concept of a human manually creating individual tasks. In v2, the human submits a **goal**, and the brain handles decomposition.

```go
type Goal struct {
    ID          string     `json:"id"`
    Title       string     `json:"title"`
    Description string     `json:"description"`
    Status      GoalStatus `json:"status"`
    PlanID      string     `json:"plan_id"`
    CreatedAt   time.Time  `json:"created_at"`
    CompletedAt *time.Time `json:"completed_at"`
}

type GoalStatus string

const (
    GoalPlanning  GoalStatus = "planning"
    GoalActive    GoalStatus = "active"
    GoalCompleted GoalStatus = "completed"
    GoalFailed    GoalStatus = "failed"
)
```

### 7.2 Event Processing

The coordinator loop now works as follows:

```
for {
    select {
    case event := <-eventChan:
        switch event.Type {

        case "goal_submitted":
            // Invoke brain: "New goal. Create a plan."
            // Brain responds with create_task actions or update_plan
            // Coordinator executes the decisions

        case "task_completed":
            // Invoke brain: "Task X finished. Here's the output. What next?"
            // Brain responds with accept_task, revise_task, create_task, etc.
            // Coordinator executes the decisions

        case "task_failed":
            // Invoke brain: "Task X failed. Here's the error. What next?"
            // Brain responds with reassign, retry instructions, or re-plan

        case "human_message":
            // Invoke brain: "The human says: '...'. Adjust accordingly."
            // Brain responds with plan changes, new tasks, or messages

        case "stall_detected":
            // Invoke brain: "Nothing has progressed in N minutes. Diagnose."

        case "all_phase_tasks_done":
            // Upfront: start next phase
            // Rolling: invoke brain for next phase plan

        case "brain_requested_human_input":
            // Forward question to web dashboard, wait for response

        }

    case <-ticker.C:
        // Check for stalls
        // Check for timed-out agents
        // Re-evaluate blocked tasks
    }
}
```

### 7.3 Decision Executor

The brain's JSON response is parsed into a list of decisions. The coordinator executes them sequentially:

```go
type BrainDecision struct {
    Thinking  string     `json:"thinking"`
    Decisions []Decision `json:"decisions"`
}

type Decision struct {
    Action string          `json:"action"`
    Params json.RawMessage `json:"params"` // Decoded based on Action
}

func (c *Coordinator) executeDecisions(bd *BrainDecision) error {
    // Record thinking as a system message (visible in dashboard)
    c.recordMessage(&Message{
        Type:    MsgSystemEvent,
        From:    "brain",
        Content: bd.Thinking,
    })

    for _, d := range bd.Decisions {
        switch d.Action {
        case "create_task":
            var p CreateTaskParams
            json.Unmarshal(d.Params, &p)
            c.createTaskFromBrain(p)

        case "accept_task":
            var p AcceptTaskParams
            json.Unmarshal(d.Params, &p)
            c.acceptTask(p.TaskID)

        case "revise_task":
            var p ReviseTaskParams
            json.Unmarshal(d.Params, &p)
            c.reviseTask(p.TaskID, p.Feedback)

        case "reassign_task":
            var p ReassignTaskParams
            json.Unmarshal(d.Params, &p)
            c.reassignTask(p.TaskID, p.NewAgent, p.Reason)

        case "send_message":
            var p SendMessageParams
            json.Unmarshal(d.Params, &p)
            c.sendAgentMessage(p.To, p.Content)

        case "complete_goal":
            var p CompleteGoalParams
            json.Unmarshal(d.Params, &p)
            c.completeGoal(p.Summary)

        case "request_human_input":
            var p RequestHumanInputParams
            json.Unmarshal(d.Params, &p)
            c.requestHumanInput(p.Question, p.Context)

        case "update_plan":
            var p UpdatePlanParams
            json.Unmarshal(d.Params, &p)
            c.updatePlan(p.Plan, p.Reason)
        }
    }
    return nil
}
```

---

## 8. Concurrent Execution

v2 supports multiple agents working in parallel. This requires changes to the dispatch logic.

### 8.1 Dispatch Rules

- Multiple agents can be `busy` simultaneously.
- Each agent can only run one task at a time.
- The brain decides task parallelism through the dependency graph: tasks with no unmet dependencies can run concurrently.
- If multiple agents share the same role, the brain's `assign_to` can specify a role name instead of a specific agent. The `PlanExecutor.resolveAgent` function picks the least-loaded idle agent with that role.

### 8.2 Conflict Prevention

The brain's system prompt instructs it to avoid assigning concurrent tasks that modify the same files. However, as a safety net:

**File-level locking:**

```go
type WorkspaceLockManager struct {
    locks map[string]string  // filepath → task ID that holds the lock
    mu    sync.Mutex
}

func (wl *WorkspaceLockManager) TryLock(files []string, taskID string) (bool, string) {
    wl.mu.Lock()
    defer wl.mu.Unlock()
    for _, f := range files {
        if holder, ok := wl.locks[f]; ok && holder != taskID {
            return false, holder
        }
    }
    for _, f := range files {
        wl.locks[f] = taskID
    }
    return true, ""
}
```

When the brain creates a task, it can optionally specify `files_touched` (an advisory list). Before dispatch, the coordinator checks for lock conflicts. If a conflict is found, the task is held in `blocked` status and the brain is notified.

In practice, this is advisory — agents may touch unexpected files. If `git diff` after two concurrent agents shows merge conflicts, the coordinator detects this (see Section 8.3) and invokes the brain.

### 8.3 Merge Conflict Detection

After each agent completes a task, the coordinator runs:

```bash
git add -A && git diff --cached --name-only
```

If a `git commit` fails due to conflicts (it shouldn't since we add-all, but if merging branches):

1. Run `git diff --name-only --diff-filter=U` to find conflicting files.
2. Invoke the brain with a `merge_conflict` trigger listing the files and both sides.
3. The brain creates a reconciliation task assigned to one agent.

For the simpler case of sequential commits on the same branch (default): after agent B completes, if agent B modified a file that agent A also modified in a previous concurrent task, flag this to the brain as a potential integration issue even if git sees no conflict.

---

## 9. Agent Context Injection

Since agents have no memory between invocations, the coordinator must inject all relevant context into each prompt. This is critical for team coordination.

### 9.1 Context Assembly

When building a prompt for an agent, include:

```go
func (c *Coordinator) buildAgentPrompt(task *Task, agent *AgentState) string {
    var b strings.Builder

    // 1. Agent identity and role
    b.WriteString(fmt.Sprintf("You are %s, a %s on a software engineering team.\n", agent.Name, agent.Role))
    b.WriteString(fmt.Sprintf("Role description: %s\n\n", agent.Description))

    // 2. Workspace state
    b.WriteString(fmt.Sprintf("Working directory: %s\n", c.config.Workspace.Path))
    b.WriteString("Files in workspace:\n")
    b.WriteString(c.getWorkspaceTree())
    b.WriteString("\n\n")

    // 3. Relevant prior work (outputs from dependency tasks)
    for _, depID := range task.DependsOn {
        dep := c.tasks[depID]
        b.WriteString(fmt.Sprintf("--- Prior work by %s: %s ---\n", dep.AssignedTo, dep.Title))
        b.WriteString(dep.Result)
        b.WriteString("\n\n")
    }

    // 4. Messages from coordinator/brain to this agent
    for _, msgID := range task.MessageIDs {
        msg := c.getMessageByID(msgID)
        if msg.To == agent.Name {
            b.WriteString(fmt.Sprintf("Note from coordinator: %s\n\n", msg.Content))
        }
    }

    // 5. Revision feedback (if this is a revision of a prior attempt)
    if task.RevisionOf != "" {
        prev := c.tasks[task.RevisionOf]
        b.WriteString(fmt.Sprintf("--- REVISION NEEDED ---\nYour previous attempt was rejected.\nFeedback: %s\n\n", task.RevisionFeedback))
        b.WriteString(fmt.Sprintf("Your previous output:\n%s\n\n", prev.Result))
    }

    // 6. The actual task
    b.WriteString("--- YOUR TASK ---\n")
    b.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
    b.WriteString(fmt.Sprintf("Description:\n%s\n", task.Description))

    return b.String()
}
```

### 9.2 Task Extensions for Revision Tracking

```go
type Task struct {
    // ... existing fields ...
    RevisionOf       string `json:"revision_of"`        // Task ID this is a revision of
    RevisionFeedback string `json:"revision_feedback"`   // Why it was rejected
    RevisionCount    int    `json:"revision_count"`      // How many times revised
    FilesTouched     []string `json:"files_touched"`     // Advisory lock list
    Priority         int    `json:"priority"`            // 1-5, used for queue ordering
}
```

---

## 10. Stall Detection

A stall occurs when no progress is made for a configurable duration. The coordinator detects this and asks the brain for help.

```go
const StallThreshold = 5 * time.Minute

func (c *Coordinator) checkForStalls() {
    c.mu.RLock()
    defer c.mu.RUnlock()

    for _, agent := range c.agentState {
        if agent.Status == AgentBusy {
            if time.Since(agent.LastActivity) > StallThreshold {
                // Agent has been busy too long but hasn't timed out yet.
                // This is different from a timeout — the agent is still running
                // but might be stuck in a loop.
                c.eventChan <- Event{Type: "stall_detected", AgentName: agent.Name}
            }
        }
    }

    // Also check if all agents are idle but there are pending/blocked tasks
    allIdle := true
    for _, agent := range c.agentState {
        if agent.Status == AgentBusy { allIdle = false; break }
    }
    if allIdle {
        hasPending := false
        for _, task := range c.tasks {
            if task.Status == TaskPending || task.Status == TaskBlocked {
                hasPending = true; break
            }
        }
        if hasPending {
            c.eventChan <- Event{Type: "stall_detected", Detail: "all agents idle but tasks remain"}
        }
    }
}
```

---

## 11. Human Override Capabilities

The human can intervene at any point through the dashboard. v2 adds these override actions beyond v1:

| Action | Effect |
|--------|--------|
| Submit a goal | Brain plans and executes |
| Send message to brain | Brain re-evaluates, may replan |
| Send message to agent | Queued as context for agent's current or next task |
| Override assignment | Manually reassign a task to a different agent |
| Pause/resume agent | Take an agent out of rotation or bring it back |
| Force accept/reject task | Override brain's review decision |
| Edit plan | Directly modify the task graph (add/remove/reorder tasks) |
| Switch brain provider | Hot-swap from claude to codex or vice versa (takes effect on next brain invocation) |
| Kill current goal | Cancel all pending tasks, stop all running agents |

---

## 12. Dashboard Changes

### 12.1 New Dashboard Sections

**Brain Panel (top, collapsible):**

- Shows the brain's last "thinking" output in a styled callout box.
- Shows which brain provider is active with a swap button.
- Shows brain invocation count and total tokens used.
- A "Force Re-plan" button that triggers a brain invocation with a re-planning prompt.

**Plan View (new tab or panel):**

- Visual Kanban or Gantt view of the current plan.
- Phases as swim lanes or columns.
- Tasks as cards with drag-drop for manual reordering (sends override to coordinator).
- Color-coded by assigned agent.
- Dependency arrows between tasks.
- Completed tasks grayed out, failed tasks red, running tasks pulsing.

**Goal Bar (top of page):**

- Shows current goal title and status.
- Progress bar: `completed tasks / total tasks in plan`.
- "New Goal" button that opens a full-screen input for goal title + description.

### 12.2 Updated Message Timeline

Messages from the brain get a distinct treatment:

- Brain "thinking" messages: rendered in a muted italic style, collapsible, with a brain icon.
- Brain decisions: rendered as a structured card showing each action the brain took (task created, task accepted, etc.) rather than raw JSON.

### 12.3 Agent Cards Update

- Show role badge (e.g., "architect", "tester") under agent name.
- Show provider badge ("claude" / "codex").
- When agent is busy, show a live elapsed timer.
- Show revision count if the agent is working on a revised task.

---

## 13. New WebSocket Events

Add these events to the v1 protocol:

```json
{ "event": "goal_update",     "data": { /* Goal struct */ } }
{ "event": "plan_update",     "data": { /* Plan struct */ } }
{ "event": "brain_thinking",  "data": { "thinking": "...", "trigger": "..." } }
{ "event": "brain_decisions", "data": { "decisions": [ ... ] } }
{ "event": "human_input_requested", "data": { "question": "...", "context": "..." } }
```

New client-to-server actions:

```json
{ "action": "submit_goal",        "data": { "title": "...", "description": "..." } }
{ "action": "respond_to_brain",   "data": { "answer": "..." } }
{ "action": "override_assignment","data": { "task_id": "...", "new_agent": "..." } }
{ "action": "pause_agent",        "data": { "agent": "..." } }
{ "action": "resume_agent",       "data": { "agent": "..." } }
{ "action": "switch_brain",       "data": { "provider": "codex" } }
{ "action": "force_replan",       "data": { "guidance": "optional human guidance" } }
{ "action": "kill_goal",          "data": { "goal_id": "..." } }
```

---

## 14. New HTTP Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/goals` | Submit a new goal |
| `GET`  | `/api/goals` | List all goals |
| `GET`  | `/api/goals/{id}` | Goal detail with plan and tasks |
| `POST` | `/api/goals/{id}/kill` | Kill a running goal |
| `GET`  | `/api/plan` | Current active plan |
| `POST` | `/api/brain/replan` | Force brain re-plan (body: `{guidance?}`) |
| `POST` | `/api/brain/switch` | Switch brain provider (body: `{provider}`) |
| `GET`  | `/api/brain/history` | Brain conversation history |
| `POST` | `/api/agents/{name}/pause` | Pause an agent |
| `POST` | `/api/agents/{name}/resume` | Resume a paused agent |

---

## 15. Implementation Order

Extend the v1 implementation order. Begin from a working v1 system.

1. **Config v2** — Add `brain`, `team`, `providers` to config parsing. Validate that team entries reference valid providers. Print new config and exit.
2. **Roles and team instantiation** — At startup, create `AgentState` entries from the `team` config instead of the v1 `agents` map. Each team member gets its own adapter instance configured from its provider template.
3. **Brain adapter** — Implement the brain as a special adapter instance. Build `invokeBrain()` with the system prompt and prompt template. Test with a mock goal — verify the brain returns parseable JSON decisions.
4. **Decision executor** — Implement `executeDecisions()` for all action types. Test with hardcoded `BrainDecision` structs.
5. **Goal submission flow** — Implement `Goal` type, `POST /api/goals`, and the `goal_submitted` event handler that invokes the brain and executes its plan.
6. **Plan executor** — Implement `Plan`, `Phase`, `PlannedTask`, and the `PlanExecutor`. Test: submit a goal, brain creates a plan, tasks appear in the queue and dispatch.
7. **Concurrent dispatch** — Modify the v1 dispatch to allow multiple agents to be `busy`. Add `Priority` to task queue ordering.
8. **Task completion → brain review** — When a task completes, invoke the brain instead of auto-completing. Brain decides accept/revise/follow-up.
9. **Revision loop** — Implement `revise_task`: create a new task linked to the original, inject feedback. Test the cycle: task → complete → brain rejects → revision → complete → brain accepts.
10. **Stall detection** — Implement the ticker-based stall check. Test with a mock agent that never returns.
11. **Conflict detection** — Implement advisory file locking and post-task conflict check.
12. **Context assembly** — Implement `buildAgentPrompt()` with full context injection. Verify agents receive dependency outputs and revision feedback.
13. **Dashboard v2** — Add brain panel, plan view, goal bar, updated agent cards. Add new WebSocket events and client actions.
14. **Human overrides** — Implement all override actions (pause, resume, force-replan, kill goal, switch brain, override assignment).
15. **Rolling planning** — Implement the alternative planning style where the brain plans one phase at a time.
16. **Integration test** — Full end-to-end: submit a goal with mock agents, brain plans, agents execute, brain reviews, tasks complete, goal finishes.

---

## 16. Example Flow

To make the system concrete, here is a complete trace of what happens when a human submits a goal.

**Human submits:** "Build a REST API for a todo app with CRUD endpoints and tests"

**1. Brain invoked (trigger: goal_submitted)**

Brain receives the goal and team roster. Brain responds:

```json
{
  "thinking": "This is a standard REST API task. I'll have the architect define the structure first, then the implementers can work in parallel on handlers and models, and finally the tester writes tests.",
  "decisions": [
    {
      "action": "update_plan",
      "plan": {
        "phases": [
          {
            "number": 1,
            "title": "Architecture",
            "tasks": [
              {
                "temp_id": "arch-1",
                "title": "Define API structure and interfaces",
                "description": "Create the directory structure, define the Router interface, Handler interface, and data models for a Todo REST API. Write a DESIGN.md documenting the architecture. Use standard net/http. Endpoints: GET/POST /todos, GET/PUT/DELETE /todos/:id.",
                "assign_to": "architect",
                "depends_on": [],
                "priority": 1
              }
            ]
          },
          {
            "number": 2,
            "title": "Implementation",
            "tasks": [
              {
                "temp_id": "impl-models",
                "title": "Implement data models and storage",
                "description": "Implement the Todo model and an in-memory store following the interfaces in DESIGN.md.",
                "assign_to": "implementer",
                "depends_on": ["arch-1"],
                "priority": 2
              },
              {
                "temp_id": "impl-handlers",
                "title": "Implement HTTP handlers",
                "description": "Implement all CRUD handlers following the interfaces in DESIGN.md. Use the storage interface — don't import the concrete store directly.",
                "assign_to": "implementer",
                "depends_on": ["arch-1"],
                "priority": 2
              }
            ]
          },
          {
            "number": 3,
            "title": "Integration and Testing",
            "tasks": [
              {
                "temp_id": "integrate",
                "title": "Wire up main.go",
                "description": "Create main.go that initializes the store, creates handlers, sets up routes, and starts the server on :8080.",
                "assign_to": "implementer",
                "depends_on": ["impl-models", "impl-handlers"],
                "priority": 2
              },
              {
                "temp_id": "test",
                "title": "Write and run tests",
                "description": "Write table-driven tests for all CRUD endpoints using httptest. Verify status codes, response bodies, and error cases. Run them and report results.",
                "assign_to": "tester",
                "depends_on": ["integrate"],
                "review_by": "architect",
                "priority": 3
              }
            ]
          }
        ]
      }
    }
  ]
}
```

**2. Coordinator creates Plan, executes Phase 1**

- Creates task for "Define API structure" assigned to `architect`.
- Dispatches immediately (no dependencies).

**3. Architect completes**

- Brain invoked: "architect finished. Output: created DESIGN.md, models/todo.go (interfaces only), handlers/handlers.go (interfaces only)."
- Brain responds: `accept_task` for arch-1.
- Phase 1 complete → Phase 2 begins.

**4. Two implementers work in parallel**

- `impl-1` gets "Implement data models" (assigned by role resolution: picks idle implementer with fewest tasks).
- `impl-2` gets "Implement HTTP handlers".
- Both work concurrently. No file conflict because architect defined separate files.

**5. impl-2 finishes first, impl-1 still running**

- Brain invoked for impl-2's result. Brain responds: `accept_task`.
- impl-1 finishes. Brain invoked. Brain responds: `accept_task`.
- Both Phase 2 tasks done → Phase 3 begins.

**6. Integration task, then tests**

- `impl-1` (now idle) gets the integration task.
- After it completes, `tester` gets the test task.
- Tester runs tests. Two fail.

**7. Brain handles test failure**

- Brain sees tester's output including failure details.
- Brain responds:

```json
{
  "thinking": "Two tests failed — TestDeleteTodo gets 404 and TestUpdateTodo gets wrong status. Likely a routing issue in the handlers. I'll have impl-2 fix it since they wrote the handlers.",
  "decisions": [
    { "action": "accept_task", "task_id": "test-task-id" },
    {
      "action": "create_task",
      "title": "Fix DELETE and PUT handler routing",
      "description": "TestDeleteTodo returns 404 and TestUpdateTodo returns 405. Check route registration for /todos/:id DELETE and PUT methods. The tester's output:\n\n...",
      "assign_to": "impl-2"
    },
    {
      "action": "create_task",
      "title": "Re-run tests after fix",
      "description": "Run all tests again and report results.",
      "assign_to": "tester",
      "depends_on": ["fix-task-temp-id"]
    }
  ]
}
```

**8. Fix → retest → all pass → brain completes goal**

```json
{
  "thinking": "All tests pass. The API is implemented with full CRUD, tested, and clean. Goal is complete.",
  "decisions": [
    { "action": "accept_task", "task_id": "retest-task-id" },
    { "action": "complete_goal", "summary": "REST API for todo app implemented with 5 endpoints, in-memory storage, and full test coverage. All tests passing." }
  ]
}
```

The human watched this entire flow in the dashboard, seeing the brain's thinking, task assignments, agent outputs, and the revision cycle — all in real time.
