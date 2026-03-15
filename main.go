package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	opts := ParseFlags(os.Args[1:])
	cfg, err := LoadConfig(opts)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	workspace := NewWorkspace(cfg.Workspace)
	if err := workspace.Init(); err != nil {
		log.Fatalf("init workspace: %v", err)
	}

	logStore, err := NewMessageStore(cfg.Log.File)
	if err != nil {
		log.Fatalf("open message log: %v", err)
	}
	defer func() { _ = logStore.Close() }()

	hub := NewWebSocketHub()
	go hub.Run()

	agents := map[string]Agent{
		"claude": NewClaudeAdapter(cfg.Agents["claude"], workspace.Path()),
		"codex":  NewCodexAdapter(cfg.Agents["codex"], workspace.Path()),
	}

	coordinator := NewCoordinator(cfg, agents, workspace, logStore, hub)
	if err := coordinator.RecoverFromLog(); err != nil {
		log.Fatalf("recover from log: %v", err)
	}
	coordinator.Start()

	startMsg := NewMessage(MsgSystemEvent, "coordinator", "human", "", fmt.Sprintf("AgentBridge started on %s:%d", cfg.Server.Host, cfg.Server.Port))
	coordinator.mu.Lock()
	coordinator.appendMessageLocked(startMsg)
	coordinator.mu.Unlock()

	server := NewServer(coordinator)
	httpServer := runHTTPServer(cfg, server)
	uiURL := interfaceURL(cfg)

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	log.Printf("AgentBridge listening on %s", httpServer.Addr)
	log.Printf("Dashboard available at %s", uiURL)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-signals:
		log.Printf("received signal %s, shutting down", sig)
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	if err := coordinator.Stop(shutdownCtx); err != nil {
		log.Printf("coordinator shutdown: %v", err)
	}
	hub.Shutdown()
}

func interfaceURL(cfg Config) string {
	host := cfg.Server.Host
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/", host, cfg.Server.Port)
}
