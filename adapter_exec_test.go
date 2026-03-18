package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecuteAdapterCommandCancelsProcessGroup(t *testing.T) {
	t.Parallel()

	cfg := AgentConfig{
		Command:        "/bin/sh",
		Args:           []string{},
		TimeoutSeconds: 30,
		MaxRetries:     0,
		Env:            map[string]string{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := executeAdapterCommand(ctx, cfg, t.TempDir(), "", []string{"-c", "trap '' TERM; sleep 60"}, parseGenericOutput)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancellation took too long: %s", time.Since(start))
	}
}

func TestExecuteAdapterCommandPreventsParentRepoDiscovery(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	parent := t.TempDir()
	if output, err := exec.Command("git", "init", parent).CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, output)
	}

	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	cfg := AgentConfig{
		Command:        "/bin/sh",
		TimeoutSeconds: 10,
		Env:            map[string]string{},
	}

	_, err := executeAdapterCommand(context.Background(), cfg, workspace, "", []string{"-c", "git rev-parse --show-toplevel"}, parseGenericOutput)
	if err == nil {
		t.Fatalf("expected git discovery outside workspace to fail")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("expected git guard failure, got %v", err)
	}
}

func TestExecuteAdapterCommandBlocksGitPush(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	workspace := t.TempDir()
	if output, err := exec.Command("git", "init", workspace).CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, output)
	}

	cfg := AgentConfig{
		Command:        "/bin/sh",
		TimeoutSeconds: 10,
		Env:            map[string]string{},
	}

	_, err := executeAdapterCommand(context.Background(), cfg, workspace, "", []string{"-c", "git push"}, parseGenericOutput)
	if err == nil {
		t.Fatalf("expected git push to be blocked")
	}
	if !strings.Contains(err.Error(), "git push is disabled") {
		t.Fatalf("expected git push guard failure, got %v", err)
	}
}
