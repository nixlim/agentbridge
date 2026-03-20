# AgentBridge

> **R&D Note:** This system has been a valuable R&D effort in multi-agent coordination for specification development. A new, production-focused system is being developed at [github.com/nixlim/spec_system](https://github.com/nixlim/spec_system).

AgentBridge is a local Go application for coordinating multiple coding agents against a shared workspace.

The current system is deterministic. It is optimized for one primary use case:

1. prepare a specification from technical source documents
2. run adversarial review with two reviewers
3. amend the specification
4. repeat review and amendment until the spec passes or the configured review budget is exhausted

The application serves a browser dashboard, stores an NDJSON event log, and manages a git-backed workspace for generated artifacts.

## What It Does

- Runs a team of CLI agents against a shared workspace
- Tracks goals, plans, phases, tasks, messages, and discussion artifacts
- Supports deterministic spec workflows with explicit state transitions
- Shows live status in a browser UI over WebSocket
- Uploads source files into the workspace from the UI
- Commits task outputs into the workspace git repo
- Guards agent git usage so they can only commit workspace files and can never `git push`
- Surfaces agent questions to the human and pauses the workflow for answers
- Provides human decision gates at configurable workflow stages

## Current Workflow Model

AgentBridge no longer depends on an autonomous coordinator brain for normal operation.

The coordinator is a deterministic workflow engine. Goals are turned into explicit phases and tasks. The UI label still uses some legacy `brain` naming in a few places, but current execution is state-machine driven.

Supported workflow recipes:

- `spec-review-loop`
  - prepare spec
  - parallel adversarial review by two reviewers
  - spec creator consolidates and amends
  - repeat
- `spec-cross-critique-loop`
  - prepare spec
  - parallel adversarial review by two reviewers
  - each reviewer critiques the other reviewer's output
  - spec creator consolidates both reviews and both critiques, then amends
  - repeat

For the cross-critique recipe:

- adversarial review verdicts determine whether the spec passes a round
- cross-critiques are advisory inputs for consolidation
- cross-critique `VERDICT: FAIL` does not block progression by itself

## Team Roles

The intended team shape is:

- one `spec_creator`
- two `reviewer` agents, ideally from different providers

Example:

```yaml
team:
  - name: "spec-creator-claude"
    provider: "claude"
    role: "spec_creator"
    count: 1
    description: "Creates and refines technical specifications."

  - name: "reviewer-codex"
    provider: "codex"
    role: "reviewer"
    count: 1
    description: "Reviews specifications for implementation readiness."

  - name: "reviewer-claude"
    provider: "claude"
    role: "reviewer"
    count: 1
    description: "Performs a second adversarial review."
```

If you want multiple instances of the same role/provider, set `count > 1`. AgentBridge expands them into numbered agents.

## Skills

The deterministic spec workflows expect local skills at repo root:

- `.agents/skills/plan-spec`
- `.agents/skills/grill-spec`

The spec creator uses `plan-spec`.
Reviewers use `grill-spec`.

## Run

Build and start:

```bash
go build -o agentbridge .
./agentbridge --config ./agentbridge.yaml
```

Common overrides:

```bash
./agentbridge --config ./agentbridge.yaml --workspace /tmp/agentbridge-workspace
./agentbridge --config ./agentbridge.yaml --port 9090
./agentbridge --config ./agentbridge.yaml --log-level debug
```

The dashboard is served at `http://127.0.0.1:9939/` for the default repo config.

## UI Overview

The dashboard has a sidebar navigation with these tabs:

- `Overview`
  - current goal summary
  - workflow stage and transition history
  - agent cards with worker telemetry
- `Plan`
  - current deterministic plan and phase/task mapping
  - spec version diff viewer
  - plan override editor
- `Tasks`
  - live queue grouped by status (running, pending, review, completed, failed, cancelled)
  - cancel, retry, reassign actions
  - surfaced task error output and verdict badges
- `Messages`
  - event timeline with type filters and search
  - human input request banner (when agents have questions)
  - reply compose box for sending messages to agents
- `Workspace`
  - collapsible file tree with file viewer
  - file uploads into the shared workspace
- `Launch`
  - submit goal with recipe and review round selection
  - create ad hoc tasks or messages to agents

The header displays:

- goal title and status
- workflow stepper showing all plan phases with completion state
- progress bar and task counts
- workflow recipe and metadata

## Human Input and Questions

Agents can surface questions to the human during workflow execution:

1. **Questions file convention**: Agents are instructed to write questions or ambiguities that need human input to `questions/pending.md` in the workspace as a numbered markdown list.

2. **Automatic gating**: When any goal task completes and `questions/pending.md` exists in the workspace, the coordinator gates the goal (`questions_pending` type). The workflow pauses until the human responds.

3. **UI presentation**: The gate panel appears at the top of the dashboard showing the questions with a scrollable panel. The human types answers in the feedback textarea and clicks Approve to continue or Reject to stop.

4. **Answer persistence**: Human answers are saved to `questions/<goal-slug>-answers.md` in the workspace. The `questions/pending.md` file is deleted after resolution to prevent re-triggering.

5. **Answer incorporation**: Subsequent consolidation tasks receive an instruction to read and incorporate the human answers file, with answers taking priority over assumptions.

The Messages tab also provides:

- a persistent reply compose box for sending messages to any agent at any time
- a notification badge when human input is pending

## Human Decision Gates

Gates pause the workflow at specific points for human approval:

- **Discovery review gate**: After the discovery phase extracts requirements (requires `enable_discovery: true` and `enable_human_gates: true`)
- **Escalation gate**: When the spec fails review after all configured rounds
- **Questions gate**: When an agent writes questions requiring human answers

Gates are resolved via the gate panel in the UI with Approve (continue) or Reject (stop/fail).

## Goal Lifecycle

Goals support:

- `Start`
- `Stop`
- `Resume`
- `Delete`

Behavior:

- `Stop` is resumable
- `Resume` continues an incomplete blocked/stopped/failed/gated goal plan
- `Delete` is permanent

Blocked goals with incomplete plans remain resumable. Deleted goals are removed from active system state.

## Workspace Model

The workspace is a real directory on disk, usually `./workspace` or the path passed with `--workspace`.

AgentBridge uses it for:

- uploaded source documents
- generated specs (versioned: `specs/<slug>-spec-v0.md`, `v1`, etc.)
- review outputs
- round/phase discussion records
- agent questions and human answers

Discussion records are written into `discussions/...` paths. File names include:

- agent name
- role
- round
- phase

This lets the coordinator pass the correct references from one round to the next.

## Git Behavior

If `workspace.init_git: true`, AgentBridge initializes a git repo in the workspace and commits successful task outputs.

Important safety constraints:

- agents may only commit files inside the workspace
- AgentBridge stages and commits only task-scoped files
- agent subprocesses are guarded against parent-repo discovery
- `git push` is blocked for agents
- agents must not commit files outside the workspace

This is enforced both by prompt instructions and by runtime git guardrails.

## Uploading Source Files

Use the `Workspace` tab:

1. optionally enter a target folder
2. choose one or more files
3. click `Upload`

Files are written into the shared workspace and immediately become available to the next workflow tasks.

## Configuration

Repo-local config lives in [agentbridge.yaml](./agentbridge.yaml).

Important sections:

```yaml
server:
  host: "127.0.0.1"
  port: 9939

workspace:
  path: "./workspace"
  init_git: true

workflow:
  default_review_rounds: 6
  default_recipe: "spec-review-loop"
  enable_discovery: false     # add requirements discovery phase
  enable_human_gates: false   # add human approval gates

providers:
  claude:
    command: "claude"
    args: ["--dangerously-skip-permissions", "--output-format", "json", "--verbose"]
    timeout_seconds: 1800
    max_retries: 2

  codex:
    command: "codex"
    args: ["exec", "--full-auto"]
    timeout_seconds: 1800
    max_retries: 2
```

Notes:

- default timeouts are 1800 seconds (30 minutes) per agent invocation
- `workflow.default_review_rounds` sets the default review/amend budget
- the submit form can override review rounds per goal
- the submit form can choose the workflow recipe per goal

## The `brain` Config Section

The config still contains a `brain` section for compatibility with older versions and existing config files.

Current behavior:

- `planning_style` should be treated as deterministic
- the coordinator does not rely on a free-running planner brain for normal workflow execution
- the active orchestration logic is in the deterministic coordinator

## Logging and Recovery

AgentBridge writes an NDJSON log to the configured log file, by default `./agentbridge.log`.

On startup it recovers:

- goals
- tasks
- plan state
- messages

Recovery preserves genuinely running assignments when possible and requeues stale ones.

## HTTP Surface

Main endpoints:

- `GET /api/state`
- `GET,POST /api/goals`
- `POST /api/goals/{id}/start`
- `POST /api/goals/{id}/stop`
- `POST /api/goals/{id}/resume`
- `POST /api/goals/{id}/delete`
- `POST /api/goals/{id}/gate` — resolve human gate (approve/reject with feedback)
- `GET,POST /api/tasks`
- `POST /api/tasks/{id}/cancel`
- `POST /api/tasks/{id}/retry`
- `POST /api/tasks/{id}/approve`
- `POST /api/tasks/{id}/reject`
- `POST /api/tasks/{id}/reassign`
- `GET,POST /api/messages`
- `POST /api/messages/clear`
- `POST /api/tasks/clear`
- `GET,POST /api/workspace/files`
- `GET /api/workspace/files/{path}`
- `GET /api/workspace/diff`
- `GET,POST /api/plan`
- `POST /api/agents/{name}/reset`
- `POST /api/agents/{name}/pause`
- `POST /api/agents/{name}/resume`
- `GET /ws`
- `GET /healthz`

## Requirements

- Go 1.25.x or compatible
- `claude` CLI on `PATH` for Claude-backed agents
- `codex` CLI on `PATH` for Codex-backed agents

Agents whose provider CLI is unavailable remain offline.

## Development

Run tests:

```bash
go test ./...
```

Build:

```bash
go build -o agentbridge .
```

---

## Operations Manual

### Starting a Workflow

1. **Start the server**: `./agentbridge --config ./agentbridge.yaml`
2. **Open the dashboard**: Navigate to `http://127.0.0.1:9939/`
3. **Upload source documents**: Go to the Workspace tab, select files, click Upload
4. **Submit a goal**: Go to the Launch tab, enter a title and description, choose a recipe, set review rounds, click Submit Goal

### Monitoring Execution

- **Overview tab**: Watch agent status cards, workflow transitions, and coordinator telemetry
- **Tasks tab**: See running/pending/completed tasks with results and verdicts
- **Header stepper**: Shows all plan phases with live progress — each step is colored completed (green), active (teal), or planned (grey)
- **Messages tab**: Full event timeline with type filters

### Responding to Agent Questions

When an agent encounters ambiguities or needs clarification:

1. The agent writes questions to `questions/pending.md` in the workspace
2. The workflow automatically pauses (goal becomes "gated")
3. The gate panel appears: "Agent Has Questions — Workflow Paused"
4. Read the questions in the scrollable panel
5. Type your answers in the textarea
6. Click **Approve** to resume the workflow with your answers incorporated
7. Click **Reject** to stop the workflow

Your answers are saved to the workspace and referenced by the next consolidation task.

### Responding to Human Gates

Discovery and escalation gates work similarly:

1. The gate panel appears with context about what's being gated
2. Provide optional feedback in the textarea
3. **Approve** to continue, **Reject** to stop

### Sending Ad Hoc Messages to Agents

Two ways to message an agent outside the workflow:

1. **Messages tab**: Use the "Reply to Agent" compose box at the bottom
2. **Launch tab**: Under Overrides, select "Message Agent", pick the agent, type content

Messages create a task for the agent to respond to. The agent's reply appears in the Messages timeline.

### Managing Running Workflows

- **Stop**: Pauses execution. All running tasks are cancelled. Resumable.
- **Resume**: Continues from where the workflow stopped. Requeues incomplete tasks.
- **Delete**: Permanently removes the goal and its plan.
- **Cancel task**: Cancels a specific running task without stopping the goal.
- **Retry task**: Requeues a failed/cancelled task.
- **Reassign task**: Moves a task to a different agent.

### Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Agent shows "offline" | Provider CLI not on PATH | Install `claude` or `codex` CLI |
| Task fails with timeout | Agent invocation exceeded `timeout_seconds` | Increase timeout in config, restart server |
| Goal stuck in "blocked" | All review rounds exhausted with FAIL verdicts | Resume to retry, or delete and resubmit with more rounds |
| Goal stuck in "gated" | Human gate waiting for response | Check the gate panel at top of dashboard, approve or reject |
| Workspace files not showing | UI cache | Click Refresh in the Workspace tab |
| Agent produced no output | Agent CLI error | Check task error output in Tasks tab, check `agentbridge.log` |

### File Layout

```
agentbridge/
  agentbridge.yaml          # Configuration
  agentbridge.log           # NDJSON event log (auto-created)
  .agents/skills/           # Skills used by agents
    plan-spec/              # Spec preparation skill
    grill-spec/             # Spec review skill
  static/                   # Dashboard UI (HTML, CSS, JS)
  workspace/                # Git-backed workspace (configurable)
    specs/                  # Versioned spec files
    discussions/            # Round/phase discussion records
    questions/              # Agent questions and human answers
```

### Practical Notes

- If the workspace already contains a git repo, AgentBridge reuses it.
- If you change embedded frontend files under `static/`, restart the server to serve the updated UI.
- Long-running spec jobs are expected; the default timeout is 30 minutes per agent invocation.
- A blocked goal with an incomplete plan can be resumed from the UI.
- The `.workspace/` directory is gitignored and should not be committed to the repo.
- The workspace path in the config is relative to the working directory where the server is started.
