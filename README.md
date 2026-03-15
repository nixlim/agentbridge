# AgentBridge

AgentBridge is a Go application that coordinates Claude Code and Codex CLI runs against a shared workspace, logs all traffic as NDJSON, and exposes a real-time dashboard over WebSocket.

## Run

```bash
go build -o agentbridge .
./agentbridge
```

Options:

```bash
./agentbridge --config ./agentbridge.yaml
./agentbridge --port 9090 --workspace /tmp/agentbridge-workspace --log-level debug
```

The dashboard is served at `http://127.0.0.1:8080/` by default.

## Notes

- The default workspace is `./workspace`.
- When git is enabled, AgentBridge initializes the workspace repository and commits changes after successful task runs.
- Claude runs with `--dangerously-skip-permissions` by default.
- Codex runs in non-interactive mode via `codex exec --full-auto` by default.
- If `claude` or `codex` are not on `PATH`, those agents remain offline until the binaries are available.
