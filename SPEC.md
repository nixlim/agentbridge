# AgentBridge — Multi-Agent Coordinator

## Specification for Implementation by a Coding Agent

**Version:** 1.0  
**Target Language:** Go (backend), HTML/JS (frontend)  
**Purpose:** A coordinator process that orchestrates communication between Claude Code and Codex CLI sessions, with a real-time web dashboard for human observation.

---

## 1. System Overview

AgentBridge is a long-running Go process that acts as a central orchestrator between multiple AI coding agents (initially Claude Code and OpenAI Codex). It receives high-level tasks, decomposes them into sub-tasks, dispatches work to agents via their CLIs, captures all input/output, routes messages between agents, and exposes a real-time web dashboard over WebSocket so a human operator can observe (and optionally intervene in) all communications.

### Core Principles

- The coordinator owns the event loop — agents never need to poll or discover messages.
- All agent interactions are captured, timestamped, and logged.
- The web UI is observe-first, intervene-second — the human watches by default but can inject messages or override routing.
- The system must be resilient to agent failures (timeouts, crashes, malformed output).

### Architecture Diagram (ASCII)

```
┌─────────────────────────────────────────────────────┐
│                   Web Browser                        │
│              (Dashboard + Controls)                  │
└──────────────────────┬──────────────────────────────┘
                       │ WebSocket (bidirectional)
                       │
┌──────────────────────▼──────────────────────────────┐
│                  AgentBridge (Go)                     │
│                                                      │
│  ┌────────────┐  ┌────────────┐  ┌───────────────┐  │
│  │ Task Planner│  │  Router    │  │  Message Log  │  │
│  └─────┬──────┘  └─────┬──────┘  └───────┬───────┘  │
│        │               │                 │           │
│  ┌─────▼───────────────▼─────────────────▼───────┐  │
│  │              Agent Manager                     │  │
│  │  ┌─────────────┐       ┌─────────────────┐    │  │
│  │  │ Claude Code  │       │     Codex       │    │  │
│  │  │  Adapter     │       │    Adapter      │    │  │
│  │  └──────┬──────┘       └───────┬─────────┘    │  │
│  └─────────┼──────────────────────┼──────────────┘  │
└────────────┼──────────────────────┼──────────────────┘
             │ stdin/stdout/stderr  │ stdin/stdout/stderr
             ▼                      ▼
      ┌──────────┐           ┌──────────┐
      │ claude    │           │ codex    │
      │ CLI       │           │ CLI      │
      └──────────┘           └──────────┘
             │                      │
             └──────┬───────────────┘
                    ▼
            ┌──────────────┐
            │ Shared       │
            │ Workspace    │
            │ (git repo)   │
            └──────────────┘
```

---

## 2. Project Structure

```
agentbridge/
├── main.go                  # Entry point, CLI flags, server startup
├── go.mod
├── go.sum
├── config.go                # Configuration loading and defaults
├── coordinator.go           # Core orchestration loop
├── task.go                  # Task and sub-task types, planner
├── agent.go                 # Agent interface and registry
├── adapters/
│   ├── claude.go            # Claude Code CLI adapter
│   └── codex.go             # Codex CLI adapter
├── router.go                # Message routing logic
├── message.go               # Message types and log
├── workspace.go             # Shared workspace management
├── server.go                # HTTP server + WebSocket hub
├── hub.go                   # WebSocket connection management
├── static/
│   ├── index.html           # Single-page dashboard
│   ├── app.js               # Frontend logic
│   └── style.css            # Dashboard styles
└── README.md
```

---

## 3. Configuration

AgentBridge reads configuration from a YAML file (`agentbridge.yaml`) with CLI flag overrides.

### Config File Schema

```yaml
# agentbridge.yaml

server:
  host: "127.0.0.1"
  port: 8080

workspace:
  path: "./workspace"          # Shared directory both agents operate in
  init_git: true               # Initialize as git repo if not already

agents:
  claude:
    command: "claude"
    args: ["-p", "--output-format", "json", "--verbose"]
    timeout_seconds: 300       # Max time per invocation
    max_retries: 2
    working_dir: ""            # Defaults to workspace.path
    env:                       # Additional environment variables
      CLAUDE_CODE_MAX_TURNS: "50"
  codex:
    command: "codex"
    args: ["--json"]
    timeout_seconds: 300
    max_retries: 2
    working_dir: ""
    env: {}

log:
  file: "./agentbridge.log"    # Structured JSON log of all messages
  level: "info"                # debug | info | warn | error
```

### CLI Flags

```
agentbridge [flags]

  --config string       Path to config file (default "agentbridge.yaml")
  --port int            Override server port (default 8080)
  --workspace string    Override workspace path
  --log-level string    Override log level
```

### Config Loading Order

1. Load defaults (hardcoded in `config.go`).
2. Overlay with YAML file values.
3. Overlay with CLI flag values (highest priority).

### Config Struct

```go
type Config struct {
    Server    ServerConfig           `yaml:"server"`
    Workspace WorkspaceConfig        `yaml:"workspace"`
    Agents    map[string]AgentConfig `yaml:"agents"`
    Log       LogConfig              `yaml:"log"`
}

type ServerConfig struct {
    Host string `yaml:"host"`
    Port int    `yaml:"port"`
}

type WorkspaceConfig struct {
    Path    string `yaml:"path"`
    InitGit bool   `yaml:"init_git"`
}

type AgentConfig struct {
    Command        string            `yaml:"command"`
    Args           []string          `yaml:"args"`
    TimeoutSeconds int               `yaml:"timeout_seconds"`
    MaxRetries     int               `yaml:"max_retries"`
    WorkingDir     string            `yaml:"working_dir"`
    Env            map[string]string `yaml:"env"`
}

type LogConfig struct {
    File  string `yaml:"file"`
    Level string `yaml:"level"`
}
```

---

## 4. Core Data Types

### 4.1 Message

Every piece of communication flows through a unified `Message` type. This is the central data structure of the system.

```go
type MessageType string

const (
    MsgHumanToCoordinator   MessageType = "human→coordinator"
    MsgCoordinatorToAgent   MessageType = "coordinator→agent"
    MsgAgentToCoordinator   MessageType = "agent→coordinator"
    MsgAgentToAgent         MessageType = "agent→agent"       // Routed via coordinator
    MsgCoordinatorToHuman   MessageType = "coordinator→human"
    MsgSystemEvent          MessageType = "system"
)

type Message struct {
    ID        string      `json:"id"`         // UUIDv4
    Timestamp time.Time   `json:"timestamp"`
    Type      MessageType `json:"type"`
    From      string      `json:"from"`       // "human", "coordinator", "claude", "codex"
    To        string      `json:"to"`         // Target recipient
    TaskID    string      `json:"task_id"`    // Associated task, empty for ad-hoc
    Content   string      `json:"content"`    // The actual message body
    Metadata  Metadata    `json:"metadata"`   // Structured metadata
}

type Metadata struct {
    TokensIn     int           `json:"tokens_in,omitempty"`
    TokensOut    int           `json:"tokens_out,omitempty"`
    DurationMs   int64         `json:"duration_ms,omitempty"`
    ExitCode     int           `json:"exit_code,omitempty"`
    Error        string        `json:"error,omitempty"`
    FilesChanged []string      `json:"files_changed,omitempty"`
}
```

### 4.2 Task

```go
type TaskStatus string

const (
    TaskPending    TaskStatus = "pending"
    TaskRunning    TaskStatus = "running"
    TaskBlocked    TaskStatus = "blocked"    // Waiting on another task
    TaskReview     TaskStatus = "review"     // Waiting for human/agent review
    TaskCompleted  TaskStatus = "completed"
    TaskFailed     TaskStatus = "failed"
)

type Task struct {
    ID           string     `json:"id"`             // UUIDv4
    ParentID     string     `json:"parent_id"`      // Empty for root tasks
    Title        string     `json:"title"`
    Description  string     `json:"description"`    // Full prompt/context
    AssignedTo   string     `json:"assigned_to"`    // Agent name
    Status       TaskStatus `json:"status"`
    DependsOn    []string   `json:"depends_on"`     // Task IDs that must complete first
    Result       string     `json:"result"`         // Agent's output summary
    CreatedAt    time.Time  `json:"created_at"`
    StartedAt    *time.Time `json:"started_at"`
    CompletedAt  *time.Time `json:"completed_at"`
    MessageIDs   []string   `json:"message_ids"`    // All messages related to this task
}
```

### 4.3 Agent State

```go
type AgentStatus string

const (
    AgentIdle    AgentStatus = "idle"
    AgentBusy    AgentStatus = "busy"
    AgentError   AgentStatus = "error"
    AgentOffline AgentStatus = "offline"
)

type AgentState struct {
    Name       string      `json:"name"`
    Status     AgentStatus `json:"status"`
    CurrentTask string     `json:"current_task"`  // Task ID or empty
    TasksCompleted int     `json:"tasks_completed"`
    TotalTokensIn  int     `json:"total_tokens_in"`
    TotalTokensOut int     `json:"total_tokens_out"`
    LastActivity   time.Time `json:"last_activity"`
}
```

---

## 5. Agent Adapters

### 5.1 Agent Interface

All agent adapters implement this interface:

```go
type Agent interface {
    Name() string
    Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error)
    ParseOutput(raw []byte) (*AgentResult, error)
    IsAvailable() bool    // Check if CLI is installed and accessible
}

type AgentResult struct {
    RawOutput    string   `json:"raw_output"`
    Summary      string   `json:"summary"`       // Extracted or generated summary
    FilesChanged []string `json:"files_changed"`
    TokensIn     int      `json:"tokens_in"`
    TokensOut    int      `json:"tokens_out"`
    ExitCode     int      `json:"exit_code"`
}
```

### 5.2 Claude Code Adapter

**CLI invocation pattern:**

```bash
claude -p "<prompt>" \
  --output-format json \
  --verbose \
  --max-turns 50
```

The `--output-format json` flag causes Claude Code to emit structured JSON to stdout. The adapter must:

1. Build the command with the prompt injected via the `-p` flag.
2. Set `working_dir` to the shared workspace.
3. Set a context deadline based on `timeout_seconds`.
4. Capture stdout, stderr, and the exit code.
5. Parse the JSON output. The expected shape is:

```json
{
  "result": "text of Claude's final response",
  "cost_usd": 0.05,
  "duration_ms": 12345,
  "num_turns": 3,
  "is_error": false
}
```

6. If `is_error` is true or exit code is non-zero, return an error with stderr content.
7. Extract `files_changed` by running `git diff --name-only HEAD` in the workspace after execution (if git is initialized).

**Prompt wrapping:** The adapter prepends workspace context to every prompt:

```
You are working in the directory: {workspace_path}
This is a shared workspace with another AI agent (Codex).
Do not delete or overwrite files without being asked to.

Context from coordinator:
{any prior messages or results being forwarded}

Your task:
{the actual prompt}
```

### 5.3 Codex Adapter

**CLI invocation pattern:**

Codex CLI invocation should be treated as configurable since the CLI interface may differ across versions. The default pattern:

```bash
codex --prompt "<prompt>" \
  --json \
  --approval-mode full-auto
```

The adapter follows the same lifecycle as Claude's adapter: build command, set working dir, capture output, parse JSON, extract file changes.

**Important:** If the Codex CLI does not support JSON output, the adapter should capture raw stdout and treat the entire output as the `summary` field. The adapter must handle both structured and unstructured output gracefully.

### 5.4 Adapter Registration

Adapters are registered at startup in a map:

```go
var agents = map[string]Agent{
    "claude": &ClaudeAdapter{},
    "codex":  &CodexAdapter{},
}
```

New adapters can be added by implementing the `Agent` interface and registering them in this map. The config file's `agents` keys must match these registration keys.

---

## 6. Coordinator Logic

The coordinator is the central orchestration engine. It runs as a goroutine and processes events from multiple sources.

### 6.1 Event Loop

```go
type Coordinator struct {
    config     Config
    agents     map[string]Agent
    agentState map[string]*AgentState
    tasks      map[string]*Task
    messages   []*Message              // Append-only log
    taskQueue  chan *Task
    msgChan    chan *Message            // Incoming messages from all sources
    hub        *WebSocketHub           // Broadcasts to web clients
    mu         sync.RWMutex
}
```

The coordinator runs a `select` loop over:

1. **`msgChan`** — Messages from the web UI (human operator), agent completions, or internal routing.
2. **`taskQueue`** — Tasks ready to be dispatched.
3. **A periodic ticker (every 5 seconds)** — Checks for timed-out agents, re-evaluates blocked tasks whose dependencies may have completed.

### 6.2 Task Lifecycle

```
                ┌─────────┐
                │ pending  │
                └────┬────┘
                     │ dependencies met
                ┌────▼────┐
                │ running  │◄─── agent invoked
                └────┬────┘
                     │
              ┌──────┼──────┐
              │      │      │
         ┌────▼──┐ ┌─▼───┐ ┌▼───────┐
         │failed │ │done  │ │review  │
         └───────┘ └──┬──┘ └───┬────┘
                      │        │ human approves
                 ┌────▼────┐   │
                 │completed│◄──┘
                 └─────────┘
```

### 6.3 Dispatch Algorithm

When a task enters the queue:

1. Check `DependsOn` — if any dependency is not `completed`, set status to `blocked` and skip.
2. Look up `AssignedTo` agent. If agent status is `busy`, re-enqueue with a short delay.
3. Set agent status to `busy`, task status to `running`.
4. Build the prompt:
   - Include the task description.
   - Include summaries of completed dependency tasks (so the agent has context on prior work).
   - Include any forwarded messages from other agents.
5. Call `agent.Execute(ctx, prompt, workDir)`.
6. On success: record the result, set task to `completed`, set agent to `idle`, broadcast update via WebSocket, check if any blocked tasks can now proceed.
7. On failure: retry up to `max_retries`. If exhausted, set task to `failed`, broadcast error, set agent to `error` (until next successful task or manual reset).

### 6.4 Inter-Agent Messaging

When agent A's output needs to be reviewed or consumed by agent B:

1. The coordinator creates an `agent→agent` message (routed through the coordinator — agents never talk directly).
2. The coordinator creates a follow-up task assigned to agent B whose description includes agent A's output.
3. This follow-up task enters the task queue and is dispatched normally.

The coordinator determines when inter-agent messaging is needed based on task metadata. Specifically, tasks can have a `ReviewBy` field:

```go
type Task struct {
    // ... existing fields ...
    ReviewBy string `json:"review_by"` // Agent name that should review output
}
```

If `ReviewBy` is set and the task completes, the coordinator automatically creates a review task.

---

## 7. Message Log

All messages are stored in an append-only in-memory slice and simultaneously written to the log file as newline-delimited JSON (NDJSON).

```go
func (c *Coordinator) recordMessage(msg *Message) {
    c.mu.Lock()
    c.messages = append(c.messages, msg)
    c.mu.Unlock()

    // Write to NDJSON log file
    c.writeToLog(msg)

    // Broadcast to all WebSocket clients
    c.hub.Broadcast(msg)
}
```

### Log File Format

Each line is a complete JSON object:

```json
{"id":"abc-123","timestamp":"2025-03-14T10:30:00Z","type":"coordinator→agent","from":"coordinator","to":"claude","task_id":"task-1","content":"Implement the user auth module...","metadata":{}}
```

This format is chosen because it is easy to tail, grep, pipe, and parse with standard tools.

---

## 8. WebSocket Hub

### 8.1 Connection Management

```go
type WebSocketHub struct {
    clients    map[*websocket.Conn]bool
    broadcast  chan []byte
    register   chan *websocket.Conn
    unregister chan *websocket.Conn
    mu         sync.Mutex
}
```

The hub runs its own goroutine. On `broadcast`, it writes the JSON payload to every connected client. Dead connections are cleaned up on write error.

### 8.2 Protocol

All WebSocket communication is JSON. Messages from server to client:

```json
{
  "event": "message",
  "data": { /* Message struct */ }
}
```

```json
{
  "event": "task_update",
  "data": { /* Task struct */ }
}
```

```json
{
  "event": "agent_status",
  "data": { /* AgentState struct */ }
}
```

```json
{
  "event": "snapshot",
  "data": {
    "agents": { /* map of AgentState */ },
    "tasks": [ /* all Task structs */ ],
    "messages": [ /* last 200 messages */ ]
  }
}
```

The `snapshot` event is sent once when a new WebSocket client connects, giving it the full current state.

Messages from client to server:

```json
{
  "action": "send_task",
  "data": {
    "title": "Implement auth",
    "description": "Build JWT-based authentication...",
    "assigned_to": "claude"
  }
}
```

```json
{
  "action": "send_message",
  "data": {
    "to": "claude",
    "content": "Stop what you're doing and focus on the bug in auth.go"
  }
}
```

```json
{
  "action": "approve_review",
  "data": {
    "task_id": "task-xyz"
  }
}
```

```json
{
  "action": "reject_review",
  "data": {
    "task_id": "task-xyz",
    "reason": "The tests don't cover edge cases"
  }
}
```

```json
{
  "action": "cancel_task",
  "data": {
    "task_id": "task-xyz"
  }
}
```

```json
{
  "action": "reset_agent",
  "data": {
    "agent": "codex"
  }
}
```

---

## 9. HTTP API

The Go server exposes these HTTP endpoints. All responses are JSON.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/` | Serve the dashboard SPA (`static/index.html`) |
| `GET`  | `/static/*` | Serve static assets (JS, CSS) |
| `GET`  | `/ws` | WebSocket upgrade endpoint |
| `GET`  | `/api/state` | Full current state snapshot (agents, tasks, recent messages) |
| `GET`  | `/api/messages?limit=100&offset=0&agent=claude` | Paginated message log with optional filters |
| `GET`  | `/api/tasks` | All tasks with current status |
| `GET`  | `/api/tasks/{id}` | Single task detail including all related messages |
| `POST` | `/api/tasks` | Create a new task (body: `{title, description, assigned_to, depends_on?, review_by?}`) |
| `POST` | `/api/tasks/{id}/cancel` | Cancel a running or pending task |
| `POST` | `/api/tasks/{id}/approve` | Approve a task in review status |
| `POST` | `/api/tasks/{id}/reject` | Reject a task in review (body: `{reason}`) |
| `POST` | `/api/messages` | Inject a human message to an agent (body: `{to, content}`) |
| `POST` | `/api/agents/{name}/reset` | Reset an agent's error state to idle |
| `GET`  | `/api/workspace/files` | List files in the shared workspace |
| `GET`  | `/api/workspace/files/{path}` | Read a specific file's content |
| `GET`  | `/api/workspace/diff` | Get `git diff` of uncommitted changes |

Use `net/http` from the standard library. Use `github.com/gorilla/websocket` for WebSocket handling. Use `github.com/gorilla/mux` or the standard `http.ServeMux` (Go 1.22+ pattern matching) for routing.

---

## 10. Web Dashboard

The dashboard is a single HTML page with embedded JS (no build step, no framework). It connects to the WebSocket and renders state updates in real-time.

### 10.1 Layout

```
┌─────────────────────────────────────────────────────────┐
│  AgentBridge Dashboard                        [status]  │
├────────────────┬────────────────────────┬───────────────┤
│                │                        │               │
│  Agent Panel   │   Message Timeline     │  Task Panel   │
│                │                        │               │
│  ┌──────────┐  │  ┌──────────────────┐  │  ┌─────────┐ │
│  │ Claude   │  │  │ 10:30 coord→cl   │  │  │ Task 1  │ │
│  │ ● idle   │  │  │ "Implement..."   │  │  │ running │ │
│  │ tasks: 3 │  │  ├──────────────────┤  │  ├─────────┤ │
│  └──────────┘  │  │ 10:31 cl→coord   │  │  │ Task 2  │ │
│  ┌──────────┐  │  │ "Done. Created.."│  │  │ pending │ │
│  │ Codex    │  │  ├──────────────────┤  │  ├─────────┤ │
│  │ ● busy   │  │  │ 10:32 coord→cdx  │  │  │ Task 3  │ │
│  │ tasks: 1 │  │  │ "Review this..." │  │  │ blocked │ │
│  └──────────┘  │  └──────────────────┘  │  └─────────┘ │
│                │                        │               │
├────────────────┴────────────────────────┴───────────────┤
│  ┌───────────────────────────────────────┐ [To: ▼] [⏎] │
│  │ Type a message or task...             │              │
│  └───────────────────────────────────────┘              │
└─────────────────────────────────────────────────────────┘
```

### 10.2 Panel Specifications

**Agent Panel (left sidebar, ~200px wide)**

- One card per registered agent.
- Shows: agent name, status indicator (colored dot: green=idle, amber=busy, red=error, gray=offline), current task title (if busy), cumulative stats (tasks completed, total tokens).
- Click an agent card to filter the message timeline to only show messages involving that agent.
- "Reset" button appears when agent is in error state.

**Message Timeline (center, flexible width)**

- Chronological list of all messages, newest at bottom, auto-scrolls.
- Each message rendered as a card/row:
  - Timestamp (HH:MM:SS)
  - Direction arrow and participants, color-coded (e.g., blue for Claude, green for Codex, orange for coordinator, purple for human).
  - Message type badge.
  - Content (first 3 lines visible, expandable on click to show full content).
  - If associated with a task, show task title as a tag/link.
  - Metadata line (tokens, duration) shown on expansion.
- Filters at the top: checkboxes for each message type, agent name filter.
- Search bar to search message content.

**Task Panel (right sidebar, ~250px wide)**

- Grouped by status: Running, Pending/Blocked, Review, Completed (collapsed), Failed.
- Each task card shows: title, assigned agent, status badge, dependency links.
- Click a task to expand it inline showing description, result, related messages.
- Review tasks have "Approve" and "Reject" buttons directly on the card.
- "New Task" button at top opens an inline form.

**Input Bar (bottom, full width)**

- Text input field for composing messages or tasks.
- Dropdown selector: "Send to: Claude / Codex / New Task".
- When "New Task" is selected, additional fields appear: title (required), assign to (dropdown), review by (optional dropdown), depends on (multi-select of existing task IDs).
- Enter key or send button submits.

### 10.3 Color Scheme

Use a dark theme with the following semantic colors:

| Element | Color (hex) |
|---------|-------------|
| Background | `#0d1117` |
| Panel background | `#161b22` |
| Card background | `#21262d` |
| Border | `#30363d` |
| Text primary | `#e6edf3` |
| Text secondary | `#8b949e` |
| Claude messages | `#58a6ff` (blue) |
| Codex messages | `#3fb950` (green) |
| Coordinator messages | `#d29922` (amber) |
| Human messages | `#bc8cff` (purple) |
| Status: idle | `#3fb950` |
| Status: busy | `#d29922` |
| Status: error | `#f85149` |
| Status: offline | `#484f58` |

### 10.4 Frontend Implementation Notes

- Use vanilla JS. No React, no build tools.
- Single `index.html` with `<script>` and `<style>` tags, or load `app.js` and `style.css` from `/static/`.
- WebSocket connection with auto-reconnect (exponential backoff: 1s, 2s, 4s, 8s, max 30s).
- On connect, the server sends a `snapshot` event — render all existing state.
- Subsequent events are incrementally applied: new messages appended, task states updated, agent states updated.
- Use `document.createElement` or template literals for rendering — no innerHTML with user content (XSS).
- Message content should be rendered with whitespace preserved (CSS `white-space: pre-wrap`) and code blocks detected and syntax-highlighted with a lightweight highlighter (or just monospaced in a `<pre>` tag).
- The message timeline should efficiently handle 10,000+ messages. Use a virtual scroll or limit rendered DOM nodes to the most recent 500 and lazy-load on scroll-up.

---

## 11. Shared Workspace

The shared workspace is a directory on disk that both agents operate in. The coordinator initializes it at startup.

### Initialization

1. Create the directory if it doesn't exist.
2. If `init_git` is true and the directory is not already a git repo, run `git init`.
3. Create a `.agentbridge/` subdirectory for metadata files that agents should ignore.

### Git Integration

After each agent completes a task, the coordinator:

1. Runs `git add -A` in the workspace.
2. Runs `git diff --cached --name-only` to get the list of changed files.
3. Commits with message: `[agentbridge] {agent_name}: {task_title} (task:{task_id})`.
4. Stores the commit hash in the task's metadata.

This gives the human operator a full history of what each agent changed and when, and makes it easy to revert a specific agent's work.

### File Watching (Optional Enhancement)

If implemented, use `fsnotify` to watch the workspace directory. When files change outside of a coordinator-driven task (e.g., the human edits something), broadcast a `workspace_change` event over WebSocket.

---

## 12. Error Handling

### Agent Timeout

If an agent does not return within `timeout_seconds`:

1. Kill the child process (send SIGTERM, wait 5s, then SIGKILL).
2. Record a message with `metadata.error = "timeout after Xs"`.
3. Set task status to `failed`.
4. Set agent status to `error`.
5. Broadcast both updates.

### Agent Crash (Non-Zero Exit)

1. Capture stderr.
2. Record a message with `metadata.error` = stderr content and `metadata.exit_code`.
3. If retries remain, re-enqueue the task and log a retry message.
4. If retries exhausted, mark task as failed.

### Malformed Output

If `agent.ParseOutput()` fails:

1. Store the raw output in the message content.
2. Record a system event message noting the parse failure.
3. The coordinator should attempt to use the raw output as a plain text summary.

### WebSocket Disconnect

- Client disconnect: Remove from hub, no further action.
- Client reconnect: Send fresh `snapshot` event.

### Coordinator Crash Recovery

On startup, if the log file exists, the coordinator reads it back to reconstruct state. This means all `Task` and `Message` data can be recovered from the NDJSON log.

```go
func (c *Coordinator) recoverFromLog(logPath string) error {
    // Read each line, unmarshal, rebuild tasks and messages slices.
    // Tasks whose status is "running" should be reset to "pending" (the agent process is gone).
}
```

---

## 13. Startup Sequence

1. Parse CLI flags.
2. Load config (file → merge flags).
3. Validate config (check agent commands exist on PATH via `exec.LookPath`).
4. Initialize logger.
5. Initialize workspace (create dir, git init).
6. Initialize the message log (open file, run recovery if file exists).
7. Register agent adapters, run `IsAvailable()` check on each.
8. Start the WebSocket hub goroutine.
9. Start the HTTP server.
10. Start the coordinator event loop goroutine.
11. Log a system event: "AgentBridge started" with config summary.
12. Block on `os.Signal` (SIGINT, SIGTERM) for graceful shutdown.

### Graceful Shutdown

On signal:

1. Stop accepting new tasks.
2. Wait for any currently running agent invocations to complete (up to 30 seconds).
3. Close all WebSocket connections with a close frame.
4. Flush and close the log file.
5. Exit.

---

## 14. Dependencies

```
github.com/gorilla/websocket   v1.5+    WebSocket support
github.com/google/uuid          v1.6+    UUID generation
gopkg.in/yaml.v3                v3.0+    Config file parsing
```

No other external dependencies. Use the Go standard library for HTTP serving, JSON encoding, process execution, file I/O, and logging.

---

## 15. Build and Run

```bash
# Build
go build -o agentbridge .

# Run with defaults
./agentbridge

# Run with overrides
./agentbridge --port 9090 --workspace /tmp/my-project --log-level debug

# Run with custom config
./agentbridge --config ./my-config.yaml
```

---

## 16. Testing Strategy

### Unit Tests

- `message_test.go` — Message serialization/deserialization round-trip.
- `task_test.go` — Task state machine transitions (verify illegal transitions are rejected).
- `router_test.go` — Given a completed task with `ReviewBy` set, verify a review task is created.
- `coordinator_test.go` — Test the dispatch algorithm with mock agents.

### Mock Agent

Create a `MockAdapter` that implements `Agent`:

```go
type MockAdapter struct {
    name     string
    response string
    delay    time.Duration
    err      error
}
```

This allows testing the full coordinator flow without real CLI tools.

### Integration Tests

- Start the full server on a random port.
- Connect a WebSocket client.
- Submit a task via the HTTP API.
- Verify the WebSocket client receives the expected sequence of events.
- Verify the log file contains the expected NDJSON entries.

---

## 17. Future Extensions (Out of Scope for v1)

These are not to be implemented now but the architecture should not preclude them:

- **MCP server mode** — AgentBridge itself exposes an MCP interface so agents with native MCP support can connect to it as a tool server instead of being invoked via CLI.
- **Plugin agents** — Load agent adapters from shared libraries or external processes.
- **Task decomposition by LLM** — Instead of the human breaking tasks down, send the root task to an agent with a planning prompt and use its output to create sub-tasks.
- **Parallel execution** — Run independent tasks on different agents concurrently (the goroutine model supports this naturally, the dispatch algorithm just needs to allow multiple busy agents).
- **Cost tracking** — Aggregate `cost_usd` from Claude Code output and display in the dashboard.
- **Voice control** — WebSocket client that sends speech-to-text as human messages.

---

## 18. Implementation Order

A coding agent should implement the system in this order, as each step builds on the last and is independently testable:

1. **Config + main.go** — Parse YAML, CLI flags, merge. Print config and exit. Verify with `go run .`.
2. **Message + log** — Define Message type, implement NDJSON writer and reader. Write a test.
3. **WebSocket hub** — Implement hub, HTTP server, `/ws` endpoint. Serve a minimal `index.html` that connects and displays messages. Manually send test messages via a goroutine.
4. **Agent interface + mock adapter** — Define the interface, build MockAdapter. Test Execute flow.
5. **Claude adapter** — Implement real CLI invocation. Test with a simple prompt.
6. **Codex adapter** — Same as above.
7. **Task system** — Define Task type, implement state machine, implement dispatch.
8. **Coordinator event loop** — Wire everything together: tasks → dispatch → agent → result → broadcast.
9. **HTTP API** — Implement all REST endpoints.
10. **Dashboard** — Build the full web UI.
11. **Workspace + git** — Implement git auto-commit after each agent run.
12. **Recovery** — Implement log replay on startup.
13. **Integration tests** — End-to-end test with mock agents.
