package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

type MessageType string

const (
	MsgHumanToCoordinator MessageType = "human→coordinator"
	MsgCoordinatorToAgent MessageType = "coordinator→agent"
	MsgAgentToCoordinator MessageType = "agent→coordinator"
	MsgAgentToAgent       MessageType = "agent→agent"
	MsgCoordinatorToHuman MessageType = "coordinator→human"
	MsgSystemEvent        MessageType = "system"
)

type Message struct {
	ID        string      `json:"id"`
	Timestamp time.Time   `json:"timestamp"`
	Type      MessageType `json:"type"`
	From      string      `json:"from"`
	To        string      `json:"to"`
	TaskID    string      `json:"task_id,omitempty"`
	Content   string      `json:"content"`
	Metadata  Metadata    `json:"metadata,omitempty"`
}

type Metadata struct {
	TokensIn     int         `json:"tokens_in,omitempty"`
	TokensOut    int         `json:"tokens_out,omitempty"`
	DurationMs   int64       `json:"duration_ms,omitempty"`
	ExitCode     int         `json:"exit_code,omitempty"`
	Error        string      `json:"error,omitempty"`
	FilesChanged []string    `json:"files_changed,omitempty"`
	CommitHash   string      `json:"commit_hash,omitempty"`
	Task         *Task       `json:"task,omitempty"`
	Agent        *AgentState `json:"agent,omitempty"`
	Goal         *Goal       `json:"goal,omitempty"`
	Plan         *Plan       `json:"plan,omitempty"`
	RawOutput    string      `json:"raw_output,omitempty"`
}

type MessageStore struct {
	path string
	file *os.File
	mu   sync.Mutex
}

func NewMessageStore(path string) (*MessageStore, error) {
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return &MessageStore{path: path, file: file}, nil
}

func (s *MessageStore) Append(msg *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	if _, err := s.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write message log: %w", err)
	}
	return s.file.Sync()
}

func (s *MessageStore) RecoverMessages() ([]*Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek log file: %w", err)
	}

	reader := bufio.NewReader(s.file)
	messages := make([]*Message, 0)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("read log file: %w", err)
		}
		line = trimTrailingNewline(line)
		if len(line) == 0 {
			if err == io.EOF {
				break
			}
			continue
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil, fmt.Errorf("unmarshal log entry: %w", err)
		}
		messages = append(messages, &msg)
		if err == io.EOF {
			break
		}
	}

	return messages, nil
}

func (s *MessageStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

func (s *MessageStore) Rewrite(messages []*Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate log file: %w", err)
	}
	if _, err := s.file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek rewritten log file: %w", err)
	}

	writer := bufio.NewWriter(s.file)
	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal message: %w", err)
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("rewrite message log: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush rewritten log: %w", err)
	}
	return s.file.Sync()
}

func NewMessage(msgType MessageType, from, to, taskID, content string) *Message {
	return &Message{
		ID:        uuid.NewString(),
		Timestamp: time.Now().UTC(),
		Type:      msgType,
		From:      from,
		To:        to,
		TaskID:    taskID,
		Content:   content,
	}
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}

func trimTrailingNewline(line []byte) []byte {
	if len(line) == 0 {
		return line
	}
	if line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}
