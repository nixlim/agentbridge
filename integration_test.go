package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHTTPAndWebSocketIntegration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Workspace.InitGit = false
	cfg.Log.File = filepath.Join(t.TempDir(), "agentbridge.log")
	cfg.Agents["claude"] = AgentConfig{
		Command:        "mock",
		TimeoutSeconds: 5,
		MaxRetries:     1,
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
		"claude": &MockAdapter{name: "claude", response: "integration complete", delay: 25 * time.Millisecond, available: true},
	}, nil, workspace, store, hub)
	coordinator.Start()
	defer func() {
		_ = coordinator.Stop(context.Background())
	}()

	server := httptest.NewServer(NewServer(coordinator).Handler())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial error = %v", err)
	}
	defer conn.Close()

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("initial websocket read error = %v", err)
	}

	resp, err := http.Post(server.URL+"/api/tasks", "application/json", strings.NewReader(`{"title":"Implement auth","description":"Add login flow","assigned_to":"claude"}`))
	if err != nil {
		t.Fatalf("http post error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, resp.StatusCode)
	}

	waitFor(t, 2*time.Second, func() bool {
		taskResp, err := http.Get(server.URL + "/api/tasks")
		if err != nil {
			return false
		}
		defer taskResp.Body.Close()
		var tasks []*Task
		if err := json.NewDecoder(taskResp.Body).Decode(&tasks); err != nil {
			return false
		}
		return len(tasks) == 1 && tasks[0].Status == TaskCompleted
	})

	deadline := time.Now().Add(2 * time.Second)
	sawTaskUpdate := false
	for time.Now().Before(deadline) && !sawTaskUpdate {
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, data, err := conn.ReadMessage()
		if err != nil {
			continue
		}
		var event struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("unmarshal event error = %v", err)
		}
		if event.Event == "task_update" {
			var task Task
			if err := json.Unmarshal(event.Data, &task); err != nil {
				t.Fatalf("unmarshal task update error = %v", err)
			}
			if task.Status == TaskCompleted {
				sawTaskUpdate = true
			}
		}
	}
	if !sawTaskUpdate {
		t.Fatal("expected completed task_update over websocket")
	}

	logData, err := os.ReadFile(cfg.Log.File)
	if err != nil {
		t.Fatalf("read log error = %v", err)
	}
	if !strings.Contains(string(logData), "Implement auth") {
		t.Fatal("expected task title in NDJSON log")
	}

	stateResp, err := http.Get(server.URL + "/api/state")
	if err != nil {
		t.Fatalf("state request error = %v", err)
	}
	defer stateResp.Body.Close()
	if stateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected state status %d, got %d", http.StatusOK, stateResp.StatusCode)
	}

	workspaceURL := server.URL + "/api/workspace/files"
	if _, err := url.Parse(workspaceURL); err != nil {
		t.Fatalf("unexpected workspace url parse error = %v", err)
	}
}

func TestHTTPGoalSubmissionIntegration(t *testing.T) {
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
		"impl-1": &scriptedAgent{name: "impl-1", responses: []string{"implemented over HTTP"}, delay: 20 * time.Millisecond, available: true},
	}, &scriptedBrain{mode: "goal-complete", available: true}, workspace, store, hub)
	coordinator.Start()
	defer func() {
		_ = coordinator.Stop(context.Background())
	}()

	server := httptest.NewServer(NewServer(coordinator).Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/goals", "application/json", strings.NewReader(`{"title":"HTTP goal","description":"Drive the new goal endpoint"}`))
	if err != nil {
		t.Fatalf("http post error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, resp.StatusCode)
	}

	waitFor(t, 2*time.Second, func() bool {
		stateResp, err := http.Get(server.URL + "/api/goals")
		if err != nil {
			return false
		}
		defer stateResp.Body.Close()
		var goals []*Goal
		if err := json.NewDecoder(stateResp.Body).Decode(&goals); err != nil {
			return false
		}
		return len(goals) == 1 && goals[0].Status == GoalCompleted
	})

	planResp, err := http.Get(server.URL + "/api/plan")
	if err != nil {
		t.Fatalf("plan request error = %v", err)
	}
	defer planResp.Body.Close()
	var plan Plan
	if err := json.NewDecoder(planResp.Body).Decode(&plan); err != nil {
		t.Fatalf("decode plan error = %v", err)
	}
	if len(plan.Phases) != 1 {
		t.Fatalf("expected one plan phase, got %d", len(plan.Phases))
	}
}
