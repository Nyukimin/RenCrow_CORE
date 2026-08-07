package llm

import (
	"strings"
	"testing"
	"time"
)

func TestAppendCurrentJSTTimeAddsConvertedTimeAtPromptEnd(t *testing.T) {
	now := time.Date(2026, time.July, 24, 13, 45, 12, 0, time.UTC)

	got := AppendCurrentJSTTime("system prompt\n", now)
	want := "system prompt\n\n現在時刻（JST）: 2026-07-24 22:45:12 JST"

	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestWithCurrentJSTTimeAddsVariableContextBeforeUserMessage(t *testing.T) {
	now := time.Date(2026, time.July, 24, 13, 45, 12, 0, time.UTC)
	originalMessages := []Message{
		{Role: "system", Content: "message system"},
		{Role: "user", Content: "hello"},
	}

	t.Run("SystemPrompt field remains stable", func(t *testing.T) {
		req := GenerateRequest{
			SystemPrompt: "field system",
			Messages:     originalMessages,
		}

		got := WithCurrentJSTTime(req, now)

		if got.SystemPrompt != "field system" {
			t.Fatalf("SystemPrompt = %q", got.SystemPrompt)
		}
		if len(got.Messages) != 3 || got.Messages[1].Type != PromptContextVariable || !strings.Contains(got.Messages[1].Content, "現在時刻（JST）: 2026-07-24 22:45:12 JST") {
			t.Fatalf("variable time context missing: %#v", got.Messages)
		}
	})

	t.Run("character system message remains unchanged", func(t *testing.T) {
		req := GenerateRequest{Messages: originalMessages}

		got := WithCurrentJSTTime(req, now)

		if got.Messages[0].Content != "message system" || got.Messages[1].Type != PromptContextVariable {
			t.Fatalf("prompt context order = %#v", got.Messages)
		}
		if originalMessages[0].Content != "message system" {
			t.Fatalf("input messages mutated: %q", originalMessages[0].Content)
		}
	})
}
