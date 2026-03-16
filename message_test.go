package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMessageStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := NewMessageStore(filepath.Join(t.TempDir(), "agentbridge.log"))
	if err != nil {
		t.Fatalf("NewMessageStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	message := NewMessage(MsgCoordinatorToAgent, "coordinator", "claude", "task-1", "implement auth")
	message.Metadata.TokensIn = 12

	if err := store.Append(message); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	messages, err := store.RecoverMessages()
	if err != nil {
		t.Fatalf("RecoverMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Content != "implement auth" {
		t.Fatalf("unexpected message content %q", messages[0].Content)
	}
	if messages[0].Metadata.TokensIn != 12 {
		t.Fatalf("expected tokens_in 12, got %d", messages[0].Metadata.TokensIn)
	}
}

func TestMessageStoreRecoverLargeLogLine(t *testing.T) {
	t.Parallel()

	store, err := NewMessageStore(filepath.Join(t.TempDir(), "agentbridge.log"))
	if err != nil {
		t.Fatalf("NewMessageStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	message := NewMessage(MsgAgentToCoordinator, "claude", "coordinator", "task-large", "large output")
	message.Metadata.RawOutput = strings.Repeat("x", 256*1024)

	if err := store.Append(message); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	messages, err := store.RecoverMessages()
	if err != nil {
		t.Fatalf("RecoverMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if got := len(messages[0].Metadata.RawOutput); got != 256*1024 {
		t.Fatalf("expected raw_output length %d, got %d", 256*1024, got)
	}
}
