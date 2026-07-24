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

func TestWithCurrentJSTTimeAddsTimeToCanonicalSystemPrompt(t *testing.T) {
	now := time.Date(2026, time.July, 24, 13, 45, 12, 0, time.UTC)
	originalMessages := []Message{
		{Role: "system", Content: "message system"},
		{Role: "user", Content: "hello"},
	}

	t.Run("SystemPrompt field takes precedence", func(t *testing.T) {
		req := GenerateRequest{
			SystemPrompt: "field system",
			Messages:     originalMessages,
		}

		got := WithCurrentJSTTime(req, now)

		if !strings.HasSuffix(got.SystemPrompt, "現在時刻（JST）: 2026-07-24 22:45:12 JST") {
			t.Fatalf("SystemPrompt = %q", got.SystemPrompt)
		}
		if got.Messages[0].Content != "message system" {
			t.Fatalf("message system prompt should remain unchanged, got %q", got.Messages[0].Content)
		}
	})

	t.Run("first system message is used when field is empty", func(t *testing.T) {
		req := GenerateRequest{Messages: originalMessages}

		got := WithCurrentJSTTime(req, now)

		if !strings.HasSuffix(got.Messages[0].Content, "現在時刻（JST）: 2026-07-24 22:45:12 JST") {
			t.Fatalf("system message = %q", got.Messages[0].Content)
		}
		if originalMessages[0].Content != "message system" {
			t.Fatalf("input messages mutated: %q", originalMessages[0].Content)
		}
	})
}
