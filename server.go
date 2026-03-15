package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

//go:embed static/*
var staticFiles embed.FS

type Server struct {
	coordinator *Coordinator
	upgrader    websocket.Upgrader
	router      *mux.Router
}

func NewServer(coordinator *Coordinator) *Server {
	server := &Server{
		coordinator: coordinator,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		router: mux.NewRouter(),
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) routes() {
	staticFS, _ := fs.Sub(staticFiles, "static")

	s.router.HandleFunc("/", s.handleIndex).Methods(http.MethodGet)
	s.router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))).Methods(http.MethodGet)
	s.router.HandleFunc("/ws", s.handleWebSocket).Methods(http.MethodGet)
	s.router.HandleFunc("/api/state", s.handleState).Methods(http.MethodGet)
	s.router.HandleFunc("/api/messages", s.handleMessages).Methods(http.MethodGet, http.MethodPost)
	s.router.HandleFunc("/api/messages/clear", s.handleMessagesClear).Methods(http.MethodPost)
	s.router.HandleFunc("/api/tasks", s.handleTasks).Methods(http.MethodGet, http.MethodPost)
	s.router.HandleFunc("/api/tasks/clear", s.handleTasksClear).Methods(http.MethodPost)
	s.router.HandleFunc("/api/tasks/{id}", s.handleTaskDetail).Methods(http.MethodGet)
	s.router.HandleFunc("/api/tasks/{id}/cancel", s.handleTaskCancel).Methods(http.MethodPost)
	s.router.HandleFunc("/api/tasks/{id}/approve", s.handleTaskApprove).Methods(http.MethodPost)
	s.router.HandleFunc("/api/tasks/{id}/reject", s.handleTaskReject).Methods(http.MethodPost)
	s.router.HandleFunc("/api/tasks/{id}/reassign", s.handleTaskReassign).Methods(http.MethodPost)
	s.router.HandleFunc("/api/agents/{name}/reset", s.handleAgentReset).Methods(http.MethodPost)
	s.router.HandleFunc("/api/agents/{name}/pause", s.handleAgentPause).Methods(http.MethodPost)
	s.router.HandleFunc("/api/agents/{name}/resume", s.handleAgentResume).Methods(http.MethodPost)
	s.router.HandleFunc("/api/goals", s.handleGoals).Methods(http.MethodGet, http.MethodPost)
	s.router.HandleFunc("/api/goals/{id}", s.handleGoalDetail).Methods(http.MethodGet)
	s.router.HandleFunc("/api/goals/{id}/kill", s.handleGoalKill).Methods(http.MethodPost)
	s.router.HandleFunc("/api/plan", s.handlePlan).Methods(http.MethodGet, http.MethodPost)
	s.router.HandleFunc("/api/brain/replan", s.handleBrainReplan).Methods(http.MethodPost)
	s.router.HandleFunc("/api/brain/respond", s.handleBrainRespond).Methods(http.MethodPost)
	s.router.HandleFunc("/api/brain/switch", s.handleBrainSwitch).Methods(http.MethodPost)
	s.router.HandleFunc("/api/brain/history", s.handleBrainHistory).Methods(http.MethodGet)
	s.router.HandleFunc("/api/workspace/files", s.handleWorkspaceFiles).Methods(http.MethodGet)
	s.router.HandleFunc("/api/workspace/files/{path:.*}", s.handleWorkspaceFile).Methods(http.MethodGet)
	s.router.HandleFunc("/api/workspace/diff", s.handleWorkspaceDiff).Methods(http.MethodGet)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.coordinator.hub.register <- conn
	if err := conn.WriteJSON(HubEvent{Event: "snapshot", Data: s.coordinator.Snapshot()}); err != nil {
		s.coordinator.hub.unregister <- conn
		return
	}

	go func() {
		defer func() {
			s.coordinator.hub.unregister <- conn
		}()
		for {
			var payload map[string]json.RawMessage
			if err := conn.ReadJSON(&payload); err != nil {
				return
			}
			action := ""
			_ = json.Unmarshal(payload["action"], &action)
			switch action {
			case "send_task":
				var req CreateTaskRequest
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_, _ = s.coordinator.CreateTask(req)
				}
			case "send_message":
				var req struct {
					To      string `json:"to"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.SendHumanMessage(req.To, req.Content)
				}
			case "approve_review":
				var req struct {
					TaskID string `json:"task_id"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.ApproveTask(req.TaskID)
				}
			case "reject_review":
				var req struct {
					TaskID string `json:"task_id"`
					Reason string `json:"reason"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.RejectTask(req.TaskID, req.Reason)
				}
			case "cancel_task":
				var req struct {
					TaskID string `json:"task_id"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.CancelTask(req.TaskID)
				}
			case "reset_agent":
				var req struct {
					Agent string `json:"agent"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.ResetAgent(req.Agent)
				}
			case "submit_goal":
				var req CreateGoalRequest
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_, _ = s.coordinator.SubmitGoal(req)
				}
			case "respond_to_brain":
				var req struct {
					Answer string `json:"answer"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.SendBrainMessage(req.Answer)
				}
			case "override_assignment":
				var req struct {
					TaskID   string `json:"task_id"`
					NewAgent string `json:"new_agent"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.ReassignTask(req.TaskID, req.NewAgent, "manual override")
				}
			case "pause_agent":
				var req struct {
					Agent string `json:"agent"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.PauseAgent(req.Agent)
				}
			case "resume_agent":
				var req struct {
					Agent string `json:"agent"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.ResumeAgent(req.Agent)
				}
			case "switch_brain":
				var req struct {
					Provider string `json:"provider"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.SwitchBrainProvider(req.Provider)
				}
			case "force_replan":
				var req struct {
					Guidance string `json:"guidance"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.ForceReplan(req.Guidance)
				}
			case "kill_goal":
				var req struct {
					GoalID string `json:"goal_id"`
				}
				if err := json.Unmarshal(payload["data"], &req); err == nil {
					_ = s.coordinator.KillGoal(req.GoalID)
				}
			}
		}
	}()
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.coordinator.Snapshot())
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			To      string `json:"to"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.coordinator.SendHumanMessage(req.To, req.Content); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	writeJSON(w, http.StatusOK, s.coordinator.ListMessages(limit, offset, agent))
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.coordinator.ListTasks())
		return
	}

	var req CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	task, err := s.coordinator.CreateTask(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handleTasksClear(w http.ResponseWriter, r *http.Request) {
	if err := s.coordinator.ClearFinishedTasks(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (s *Server) handleMessagesClear(w http.ResponseWriter, r *http.Request) {
	if err := s.coordinator.ClearMessages(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (s *Server) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["id"]
	task, messages, err := s.coordinator.GetTask(taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"task":     task,
		"messages": messages,
	})
}

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["id"]
	if err := s.coordinator.CancelTask(taskID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) handleTaskApprove(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["id"]
	if err := s.coordinator.ApproveTask(taskID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

func (s *Server) handleTaskReject(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["id"]
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.coordinator.RejectTask(taskID, req.Reason); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

func (s *Server) handleTaskReassign(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["id"]
	var req struct {
		NewAgent string `json:"new_agent"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.coordinator.ReassignTask(taskID, req.NewAgent, req.Reason); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reassigned"})
}

func (s *Server) handleAgentReset(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if err := s.coordinator.ResetAgent(name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func (s *Server) handleAgentPause(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if err := s.coordinator.PauseAgent(name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleAgentResume(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if err := s.coordinator.ResumeAgent(name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

func (s *Server) handleGoals(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.coordinator.ListGoals())
		return
	}

	var req CreateGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	goal, err := s.coordinator.SubmitGoal(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, goal)
}

func (s *Server) handleGoalDetail(w http.ResponseWriter, r *http.Request) {
	goalID := mux.Vars(r)["id"]
	goal, plan, tasks, err := s.coordinator.GetGoal(goalID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"goal":  goal,
		"plan":  plan,
		"tasks": tasks,
	})
}

func (s *Server) handleGoalKill(w http.ResponseWriter, r *http.Request) {
	goalID := mux.Vars(r)["id"]
	if err := s.coordinator.KillGoal(goalID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "killed"})
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Plan   Plan   `json:"plan"`
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.coordinator.OverridePlan(&req.Plan, req.Reason); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "updated"})
		return
	}
	writeJSON(w, http.StatusOK, s.coordinator.CurrentPlan())
}

func (s *Server) handleBrainReplan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Guidance string `json:"guidance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.coordinator.ForceReplan(req.Guidance); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *Server) handleBrainRespond(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.coordinator.SendBrainMessage(req.Answer); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *Server) handleBrainSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.coordinator.SwitchBrainProvider(req.Provider); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "switched"})
}

func (s *Server) handleBrainHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.coordinator.BrainHistory())
}

func (s *Server) handleWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.coordinator.workspace.ListFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	path := mux.Vars(r)["path"]
	content, err := s.coordinator.workspace.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"path":    path,
		"content": string(content),
	})
}

func (s *Server) handleWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	diff, err := s.coordinator.workspace.Diff()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"diff": diff})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func runHTTPServer(cfg Config, server *Server) *http.Server {
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler: server.Handler(),
	}
	return httpServer
}
