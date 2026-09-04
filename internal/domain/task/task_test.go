package task

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNewTask(t *testing.T) {
	jobID := NewJobID()
	task := NewTask(jobID, "Hello", "line", "U123")

	if task.JobID() != jobID {
		t.Errorf("Expected JobID %s, got %s", jobID.String(), task.JobID().String())
	}

	if task.UserMessage() != "Hello" {
		t.Errorf("Expected UserMessage 'Hello', got '%s'", task.UserMessage())
	}

	if task.Channel() != "line" {
		t.Errorf("Expected Channel 'line', got '%s'", task.Channel())
	}

	if task.ChatID() != "U123" {
		t.Errorf("Expected ChatID 'U123', got '%s'", task.ChatID())
	}

	if task.HasForcedRoute() {
		t.Error("New task should not have forced route")
	}
}

func TestNewTaskGeneratesIndependentConversationIdentity(t *testing.T) {
	task := NewTask(NewJobID(), "Hello", "line", "U123")
	identities := []struct {
		name     string
		raw      string
		validate func() error
	}{
		{name: "TurnID", raw: string(task.TurnID()), validate: task.TurnID().Validate},
		{name: "TraceID", raw: string(task.TraceID()), validate: task.TraceID().Validate},
		{name: "RootTaskID", raw: string(task.RootTaskID()), validate: task.RootTaskID().Validate},
		{name: "UserMessageID", raw: string(task.UserMessageID()), validate: task.UserMessageID().Validate},
		{name: "AgentMessageID", raw: string(task.AgentMessageID()), validate: task.AgentMessageID().Validate},
	}
	seen := make(map[string]string, len(identities))
	for _, identity := range identities {
		if err := identity.validate(); err != nil {
			t.Errorf("%s=%q is not canonical: %v", identity.name, identity.raw, err)
		}
		if previous, duplicate := seen[identity.raw]; duplicate {
			t.Errorf("%s aliases %s with %q", identity.name, previous, identity.raw)
		}
		seen[identity.raw] = identity.name
	}
}

func TestTaskWithConversationIdentityIsImmutableAndExact(t *testing.T) {
	original := NewTask(NewJobID(), "hello", "viewer", "viewer-user")
	originalIdentity := []string{
		string(original.TurnID()),
		string(original.TraceID()),
		string(original.RootTaskID()),
		string(original.UserMessageID()),
		string(original.AgentMessageID()),
	}
	turnID := modulecore.NewTurnID()
	traceID := modulecore.NewTraceID()
	rootTaskID := modulecore.NewTaskID()
	userMessageID := modulecore.NewMessageID()
	agentMessageID := modulecore.NewMessageID()
	updated := original.WithConversationIdentity(turnID, traceID, rootTaskID, userMessageID, agentMessageID)

	if got := []string{
		string(original.TurnID()),
		string(original.TraceID()),
		string(original.RootTaskID()),
		string(original.UserMessageID()),
		string(original.AgentMessageID()),
	}; !equalStrings(got, originalIdentity) {
		t.Fatalf("original identity mutated: got=%v want=%v", got, originalIdentity)
	}
	if updated.TurnID() != turnID || updated.TraceID() != traceID || updated.RootTaskID() != rootTaskID || updated.UserMessageID() != userMessageID || updated.AgentMessageID() != agentMessageID {
		t.Fatalf("updated identity did not preserve exact overrides: %#v", updated)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestTaskWithSessionIDPreservesCanonicalIdentitySeparatelyFromChatID(t *testing.T) {
	original := NewTask(NewJobID(), "hello", "viewer", "viewer-user")
	sessionID := string(modulecore.NewSessionID())
	updated := original.WithSessionID(sessionID)

	if original.SessionID() != "" {
		t.Fatalf("original session ID mutated: %q", original.SessionID())
	}
	if updated.SessionID() != sessionID || updated.ChatID() != "viewer-user" {
		t.Fatalf("updated identity = session=%q chat=%q", updated.SessionID(), updated.ChatID())
	}
}

func TestTaskWithForcedRoute(t *testing.T) {
	jobID := NewJobID()
	task := NewTask(jobID, "Test", "line", "U123")

	taskWithRoute := task.WithForcedRoute(routing.RouteCODE3)

	if !taskWithRoute.HasForcedRoute() {
		t.Error("Task should have forced route after WithForcedRoute")
	}

	if taskWithRoute.ForcedRoute() != routing.RouteCODE3 {
		t.Errorf("Expected forced route CODE3, got %s", taskWithRoute.ForcedRoute())
	}

	// 元のtaskは変更されない（イミュータブル）
	if task.HasForcedRoute() {
		t.Error("Original task should not be modified")
	}
}

func TestTaskWithRoute(t *testing.T) {
	jobID := NewJobID()
	task := NewTask(jobID, "Test", "line", "U123")

	taskWithRoute := task.WithRoute(routing.RouteCHAT)

	if taskWithRoute.Route() != routing.RouteCHAT {
		t.Errorf("Expected route CHAT, got %s", taskWithRoute.Route())
	}

	// 元のtaskは変更されない
	if task.Route() != "" {
		t.Error("Original task should not be modified")
	}
}

func TestTaskWithUserMessageAndAttachmentsAreImmutable(t *testing.T) {
	jobID := NewJobID()
	task := NewTask(jobID, "old", "viewer", "chat-1")
	attachments := []attachment.Attachment{{ID: "att-1", Filename: "memo.txt"}}

	updated := task.WithUserMessage("new").WithAttachments(attachments)
	attachments[0].Filename = "changed.txt"

	if task.UserMessage() != "old" || len(task.Attachments()) != 0 {
		t.Fatalf("original task mutated: message=%q attachments=%v", task.UserMessage(), task.Attachments())
	}
	if updated.UserMessage() != "new" {
		t.Fatalf("updated message=%q, want new", updated.UserMessage())
	}
	got := updated.Attachments()
	if len(got) != 1 || got[0].Filename != "memo.txt" {
		t.Fatalf("attachments=%v, want copied memo.txt", got)
	}
	got[0].Filename = "mutated.txt"
	gotAgain := updated.Attachments()
	if gotAgain[0].Filename != "memo.txt" {
		t.Fatalf("Attachments returned mutable backing slice: %v", gotAgain)
	}
}

func TestTaskWithViewerRecipientIsImmutable(t *testing.T) {
	original := NewTask(NewJobID(), "hello", "viewer", "viewer-user")
	updated := original.WithViewerRecipient("kuro")

	if original.ViewerRecipient() != "" {
		t.Fatalf("original recipient mutated: %q", original.ViewerRecipient())
	}
	if updated.ViewerRecipient() != "kuro" {
		t.Fatalf("updated recipient = %q, want kuro", updated.ViewerRecipient())
	}
}
