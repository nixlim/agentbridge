package main

import (
	"context"
	"errors"
	"time"
)

type MockAdapter struct {
	name      string
	response  string
	delay     time.Duration
	err       error
	available bool
}

func (m *MockAdapter) Name() string {
	return m.name
}

func (m *MockAdapter) Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error) {
	if !m.available {
		return nil, errors.New("agent unavailable")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(m.delay):
	}
	if m.err != nil {
		return nil, m.err
	}
	return &AgentResult{
		RawOutput:  m.response,
		Summary:    m.response,
		DurationMs: m.delay.Milliseconds(),
	}, nil
}

func (m *MockAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
	return &AgentResult{
		RawOutput: string(raw),
		Summary:   string(raw),
	}, nil
}

func (m *MockAdapter) IsAvailable() bool {
	return m.available
}
