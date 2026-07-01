package transport

import (
	"testing"
	"time"
)

func TestNewMessage(t *testing.T) {
	msg := NewMessage("mio", "shiro", "session-1", "job-1", "hello")

	if msg.From != "mio" {
		t.Errorf("Expected From 'Mio', got '%s'", msg.From)
	}
	if msg.To != "shiro" {
		t.Errorf("Expected To 'Shiro', got '%s'", msg.To)
	}
	if msg.SessionID != "session-1" {
		t.Errorf("Expected SessionID 'session-1', got '%s'", msg.SessionID)
	}
	if msg.JobID != "job-1" {
		t.Errorf("Expected JobID 'job-1', got '%s'", msg.JobID)
	}
	if msg.Content != "hello" {
		t.Errorf("Expected Content 'hello', got '%s'", msg.Content)
	}
	if msg.Type != MessageTypeTask {
		t.Errorf("Expected Type 'task', got '%s'", msg.Type)
	}
	if msg.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}

	// Timestamp はRFC3339形式
	if _, err := time.Parse(time.RFC3339, msg.Timestamp); err != nil {
		t.Errorf("Timestamp should be RFC3339 format: %v", err)
	}
}

func TestNewErrorMessage(t *testing.T) {
	msg := NewErrorMessage("Router", "mio", "s1", "j1", "agent not found")

	if msg.Type != MessageTypeError {
		t.Errorf("Expected Type 'error', got '%s'", msg.Type)
	}
	if msg.Content != "agent not found" {
		t.Errorf("Expected error content, got '%s'", msg.Content)
	}
}

func TestMessage_Validate(t *testing.T) {
	tests := []struct {
		name    string
		msg     Message
		wantErr bool
	}{
		{
			name:    "Valid message",
			msg:     NewMessage("mio", "shiro", "s1", "j1", "hello"),
			wantErr: false,
		},
		{
			name: "Missing From",
			msg: Message{
				To:        "shiro",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
			wantErr: true,
		},
		{
			name: "Missing To",
			msg: Message{
				From:      "mio",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
			wantErr: true,
		},
		{
			name: "Missing Timestamp",
			msg: Message{
				From: "mio",
				To:   "shiro",
			},
			wantErr: true,
		},
		{
			name: "Invalid Timestamp format",
			msg: Message{
				From:      "mio",
				To:        "shiro",
				Timestamp: "2026-03-03 12:00:00",
			},
			wantErr: true,
		},
		{
			name: "Valid with Proposal",
			msg: Message{
				From:      "coder3",
				To:        "Worker",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Proposal: &ProposalPayload{
					Plan:  "create file",
					Patch: "{}",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMessage_WithPayloads(t *testing.T) {
	msg := NewMessage("Worker", "mio", "s1", "j1", "done")
	msg.Type = MessageTypeResult
	msg.Result = &ResultPayload{
		Success:      true,
		Summary:      "3 commands executed",
		ExecutedCmds: 3,
		FailedCmds:   0,
		GitCommit:    "abc12345",
		Results: []CommandResultPayload{
			{Command: "create", Target: "main.go", Success: true, Output: "File created"},
		},
	}

	if err := msg.Validate(); err != nil {
		t.Errorf("Message with result payload should be valid: %v", err)
	}

	if msg.Result.ExecutedCmds != 3 {
		t.Errorf("Expected 3 executed cmds, got %d", msg.Result.ExecutedCmds)
	}

	if len(msg.Result.Results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(msg.Result.Results))
	}
}
