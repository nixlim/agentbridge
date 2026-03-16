package main

import (
	"context"
	"errors"
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
