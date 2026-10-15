package subagent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/toolloop"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func doneProvider() *mockProvider {
	return &mockProvider{responses: []llm.ChatResponse{{
		Message:      llm.ChatMessage{Role: "assistant", Content: "done"},
		FinishReason: "stop",
	}}}
}

// Failure knowledge: docs/調査/*ShiroChatのPrefix_Cache不成立とStrata_prefill.md.
// A flattened SystemPrompt string loses the block types the Gateway needs to
// keep the leading system message byte-stable, so a typed PromptContext must
// reach the provider unchanged.
func TestRunSyncSendsTheTypedPromptContextToTheProvider(t *testing.T) {
	provider := doneProvider()
	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 3})
	promptContext := []llm.Message{
		{Role: "system", Content: "character", Type: llm.PromptContextCharacter},
		{Role: "system", Content: "stable", Type: llm.PromptContextStable, Metadata: map[string]string{"runtime_context_kind": "agent_contract"}},
		{Role: "system", Content: "recall", Type: llm.PromptContextRecall},
		{Role: "system", Content: llm.CurrentJSTTimePrefix + "2026-10-15 10:00:00 JST", Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "time"}},
		{Role: "user", Content: "do it", Type: llm.PromptContextUser},
	}

	if _, err := mgr.RunSync(context.Background(), agent.SubagentTask{AgentName: "shiro", Instruction: "do it", PromptContext: promptContext}); err != nil {
		t.Fatalf("RunSync() error = %v", err)
	}

	got := provider.lastReq.Messages
	if len(got) != len(promptContext) {
		t.Fatalf("provider received %d messages, want %d: %#v", len(got), len(promptContext), got)
	}
	for i, want := range promptContext {
		if got[i].Role != want.Role || got[i].Content != want.Content || got[i].Type != want.Type || !reflect.DeepEqual(got[i].Metadata, want.Metadata) {
			t.Fatalf("message %d = %#v, want %#v", i, got[i], want)
		}
	}
	for _, message := range got {
		if strings.Contains(message.Content, "You are a helpful assistant") {
			t.Fatalf("the default system prompt must not be added to a typed prompt context: %#v", got)
		}
	}
}

func TestRunSyncAppendsTheInstructionWhenThePromptContextHasNoUserMessage(t *testing.T) {
	provider := doneProvider()
	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 3})
	promptContext := []llm.Message{{Role: "system", Content: "character", Type: llm.PromptContextCharacter}}

	if _, err := mgr.RunSync(context.Background(), agent.SubagentTask{AgentName: "shiro", Instruction: "do it", PromptContext: promptContext}); err != nil {
		t.Fatalf("RunSync() error = %v", err)
	}
	got := provider.lastReq.Messages
	if len(got) != 2 || got[1].Role != "user" || got[1].Content != "do it" || got[1].Type != llm.PromptContextUser {
		t.Fatalf("messages = %#v, want the character block followed by the typed user instruction", got)
	}
}

func TestRunSyncWithoutPromptContextKeepsTheLegacySystemPrompt(t *testing.T) {
	provider := doneProvider()
	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 3})

	if _, err := mgr.RunSync(context.Background(), agent.SubagentTask{AgentName: "worker", Instruction: "do it", SystemPrompt: "legacy system"}); err != nil {
		t.Fatalf("RunSync() error = %v", err)
	}
	got := provider.lastReq.Messages
	if len(got) != 2 || got[0].Role != "system" || got[0].Content != "legacy system" || got[1].Content != "do it" {
		t.Fatalf("messages = %#v, want the legacy system + user pair", got)
	}
}
