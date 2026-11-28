package middleware

import (
	"context"
	"testing"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestThinkingPolicyPreservesToolCalling(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(domainllm.LLMProvider) domainllm.LLMProvider
		want domainllm.ReasoningEffort
	}{
		{"disabled", NewNoThinkingProvider, domainllm.ReasoningEffortUnspecified},
		{"low", NewLowThinkingProvider, domainllm.ReasoningEffortLow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &dateTimeCaptureProvider{}
			provider, ok := tc.wrap(inner).(domainllm.ToolCallingProvider)
			if !ok {
				t.Fatal("thinking middleware removed ToolCallingProvider")
			}
			request := domainllm.ChatRequest{
				Messages:  []domainllm.ChatMessage{{Role: "user", Content: "fetch the official page"}},
				Tools:     []domainllm.ToolDefinition{{Type: "function", Function: domainllm.ToolFunctionDef{Name: "web_gather.fetch"}}},
				MaxTokens: 2048, ReasoningEffort: domainllm.ReasoningEffortLow,
			}
			if _, err := provider.Chat(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			got := inner.chatRequest
			if got.ReasoningEffort != tc.want || got.MaxTokens != request.MaxTokens || len(got.Tools) != 1 || got.Tools[0].Function.Name != "web_gather.fetch" || len(got.Messages) != 1 || got.Messages[0].Content != request.Messages[0].Content {
				t.Fatalf("tool request contract changed: %#v", got)
			}
			if request.ReasoningEffort != domainllm.ReasoningEffortLow {
				t.Fatal("caller request mutated")
			}
		})
	}
}
