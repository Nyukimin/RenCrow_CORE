package task

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/attachment"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
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
