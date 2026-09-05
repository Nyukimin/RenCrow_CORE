package transport

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNewMessage(t *testing.T) {
	taskID := modulecore.NewTaskID()
	msg := NewMessage("mio", "shiro", "session-1", taskID, "hello")

	if msg.From != "mio" {
		t.Errorf("Expected From 'Mio', got '%s'", msg.From)
	}
	if msg.To != "shiro" {
		t.Errorf("Expected To 'Shiro', got '%s'", msg.To)
	}
	if msg.SessionID != "session-1" {
		t.Errorf("Expected SessionID 'session-1', got '%s'", msg.SessionID)
	}
	if msg.TaskID != taskID {
		t.Errorf("Expected TaskID %q, got %q", taskID, msg.TaskID)
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
	msg := NewErrorMessage("Router", "mio", "s1", modulecore.NewTaskID(), "agent not found")

	if msg.Type != MessageTypeError {
		t.Errorf("Expected Type 'error', got '%s'", msg.Type)
	}
	if msg.Content != "agent not found" {
		t.Errorf("Expected error content, got '%s'", msg.Content)
	}
}

func TestMessage_Validate(t *testing.T) {
	validTaskID := modulecore.NewTaskID()
	tests := []struct {
		name    string
		msg     Message
		wantErr bool
	}{
		{
			name:    "Valid message",
			msg:     NewMessage("mio", "shiro", "s1", validTaskID, "hello"),
			wantErr: false,
		},
		{
			name: "Missing TaskID",
			msg: Message{
				From:      "mio",
				To:        "shiro",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
			wantErr: true,
		},
		{
			name: "Malformed TaskID",
			msg: Message{
				From:      "mio",
				To:        "shiro",
				TaskID:    modulecore.TaskID("not-a-task-id"),
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
			wantErr: true,
		},
		{
			name: "Missing From",
			msg: Message{
				To:        "shiro",
				TaskID:    validTaskID,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
			wantErr: true,
		},
		{
			name: "Missing To",
			msg: Message{
				From:      "mio",
				TaskID:    validTaskID,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
			wantErr: true,
		},
		{
			name: "Missing Timestamp",
			msg: Message{
				From:   "mio",
				To:     "shiro",
				TaskID: validTaskID,
			},
			wantErr: true,
		},
		{
			name: "Invalid Timestamp format",
			msg: Message{
				From:      "mio",
				To:        "shiro",
				TaskID:    validTaskID,
				Timestamp: "2026-03-03 12:00:00",
			},
			wantErr: true,
		},
		{
			name: "Valid with Proposal",
			msg: Message{
				From:      "coder3",
				To:        "Worker",
				TaskID:    validTaskID,
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
	msg := NewMessage("Worker", "mio", "s1", modulecore.NewTaskID(), "done")
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

func TestTurnInputMessageJSONRoundTripPreservesCanonicalProjection(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	turnID := modulecore.NewTurnID()
	traceID := modulecore.NewTraceID()
	userMessageID := modulecore.NewMessageID()
	agentMessageID := modulecore.NewMessageID()
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.ReconstructTurnInput(
		rootTaskID,
		turnID,
		traceID,
		userMessageID,
		agentMessageID,
		"hello",
		address,
	)
	if err != nil {
		t.Fatalf("ReconstructTurnInput() error = %v", err)
	}
	input = input.
		WithSessionID("session-1").
		WithAttachments([]attachment.Attachment{{
			ID:       "att-1",
			Kind:     attachment.KindImage,
			Filename: "photo.png",
			Data:     []byte("must not be persisted"),
		}}).
		WithViewerRecipient("mio").
		WithForcedRoute(routing.RoutePLAN).
		WithRoute(routing.RoutePLAN)

	executionTaskID := modulecore.NewTaskID()
	message, err := NewTurnInputMessage("mio", "shiro", executionTaskID, input)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("Message.Validate() error = %v", err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(encoded) == 0 || bytes.Contains(encoded, []byte(`"data"`)) {
		t.Fatalf("attachment data leaked into transport JSON: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"task_id"`)) {
		t.Fatalf("transport JSON does not use the canonical task field: %s", encoded)
	}

	var decoded Message
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	got, err := decoded.ReconstructTurnInput()
	if err != nil {
		t.Fatalf("ReconstructTurnInput() error = %v", err)
	}
	if got.RootTaskID() != rootTaskID || got.TurnID() != turnID || got.TraceID() != traceID || got.UserMessageID() != userMessageID || got.AgentMessageID() != agentMessageID {
		t.Fatalf("canonical identities changed: root=%q turn=%q trace=%q user=%q agent=%q", got.RootTaskID(), got.TurnID(), got.TraceID(), got.UserMessageID(), got.AgentMessageID())
	}
	if decoded.TaskID != executionTaskID {
		t.Fatalf("execution TaskID changed: got %q want %q", decoded.TaskID, executionTaskID)
	}
	if decoded.SessionID != "session-1" || decoded.Content != "hello" || got.SessionID() != input.SessionID() || got.MessageText() != input.MessageText() {
		t.Fatalf("outer message/input fields changed: message=%#v input=%#v", decoded, got)
	}
	gotAddress := got.ChannelAddress()
	if gotAddress.ChannelType() != "line" || gotAddress.ExternalConversationID() != "U123" {
		t.Fatalf("address changed: %#v", gotAddress)
	}
	if got.ViewerRecipient() != "mio" || got.ForcedRoute() != routing.RoutePLAN || got.Route() != routing.RoutePLAN || !got.HasForcedRoute() {
		t.Fatalf("route/recipient changed: recipient=%q forced=%q route=%q", got.ViewerRecipient(), got.ForcedRoute(), got.Route())
	}
	attachments := got.Attachments()
	if len(attachments) != 1 || attachments[0].ID != "att-1" || attachments[0].Filename != "photo.png" || attachments[0].Data != nil {
		t.Fatalf("attachment projection changed: %#v", attachments)
	}
}

func TestTurnInputMessageRejectsMissingOrMalformedProjection(t *testing.T) {
	legacy := NewMessage("mio", "shiro", "session-1", modulecore.NewTaskID(), "hello")
	if _, err := legacy.ReconstructTurnInput(); err == nil {
		t.Fatal("expected missing turn_input projection to fail")
	}

	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "hello", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	message, err := NewTurnInputMessage("mio", "shiro", modulecore.NewTaskID(), input)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}

	cloneWithProjection := func(source Message) Message {
		clone := source
		projection := *source.TurnInput
		clone.TurnInput = &projection
		return clone
	}

	malformedID := cloneWithProjection(message)
	malformedID.TurnInput.TurnID = modulecore.TurnID("not-a-turn-id")
	if _, err := malformedID.ReconstructTurnInput(); err == nil {
		t.Fatal("expected malformed turn ID to fail closed")
	}
	if err := malformedID.Validate(); err == nil {
		t.Fatal("Validate() must reject malformed turn projection")
	}

	sameMessageIDs := cloneWithProjection(message)
	sameMessageIDs.TurnInput.AgentMessageID = sameMessageIDs.TurnInput.UserMessageID
	if _, err := sameMessageIDs.ReconstructTurnInput(); err == nil {
		t.Fatal("expected identical user/agent message IDs to fail closed")
	}

	badAddress := cloneWithProjection(message)
	badAddress.TurnInput.ChannelType = "LINE"
	if _, err := badAddress.ReconstructTurnInput(); err == nil {
		t.Fatal("expected non-normalized channel address to fail closed")
	}
}

func TestNewTurnInputMessageRejectsMalformedExecutionTaskID(t *testing.T) {
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "hello", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}

	for _, taskID := range []modulecore.TaskID{"", "not-a-task-id"} {
		if _, err := NewTurnInputMessage("mio", "shiro", taskID, input); err == nil {
			t.Fatalf("NewTurnInputMessage() accepted invalid execution TaskID %q", taskID)
		}
	}
}
