# AgentBridge

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
  - each reviewer critiques the other reviewer’s output
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

The dashboard has these main tabs:

- `Overview`
  - current goal summary
  - workflow stage and transition history
  - agent cards with worker telemetry
- `Plan`
  - current deterministic plan and phase/task mapping
  - plan override editor
- `Tasks`
  - live queue
  - cancel, retry, reassign
  - surfaced task error output
- `Messages`
  - event timeline and agent/coordinator traffic
- `Workspace`
  - file tree
  - file uploads into the shared workspace
- `Controls`
  - submit goal
  - choose workflow recipe
  - override review rounds
  - create ad hoc tasks or messages

## Goal Lifecycle

Goals now support:

- `Start`
- `Stop`
- `Resume`
- `Delete`

Behavior:

- `Stop` is resumable
- `Resume` continues an incomplete blocked/stopped/failed goal plan
- `Delete` is permanent

Blocked goals with incomplete plans remain resumable. Deleted goals are removed from active system state.

## Workspace Model

The workspace is a real directory on disk, usually `./workspace` or the path passed with `--workspace`.

AgentBridge uses it for:

- uploaded source documents
- generated specs
- review outputs
- round/phase discussion records

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

providers:
  claude:
    command: "claude"
    args: ["--dangerously-skip-permissions", "--output-format", "json", "--verbose"]
    timeout_seconds: 900
    max_retries: 2

  codex:
    command: "codex"
    args: ["exec", "--full-auto"]
    timeout_seconds: 900
    max_retries: 2
```

Notes:

- current default timeouts are 900 seconds
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
- `GET,POST /api/tasks`
- `POST /api/tasks/{id}/cancel`
- `POST /api/tasks/{id}/retry`
- `POST /api/tasks/{id}/reassign`
- `GET,POST /api/workspace/files`
- `GET /api/workspace/files/{path}`
- `GET /api/workspace/diff`
- `GET /ws`

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

## Practical Notes

- If the workspace already contains a git repo, AgentBridge reuses it.
- If you change embedded frontend files under `static/`, restart the server to serve the rebuilt UI.
- Long-running spec jobs are expected; the default timeout is 15 minutes per agent invocation.
- A blocked goal with an incomplete plan can be resumed from the UI.
