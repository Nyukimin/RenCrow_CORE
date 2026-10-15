package rencrowllm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// SystemPrompt 欄は convertMessages が index 0 の system に変換する固定の指示である。
// 型を申告しないと Gateway は instruction_without_block で fallback し、時刻などの
// Variable が先頭 system へ畳み込まれて先頭 sha が要求ごとに変わる。
func TestPromptContextBlockMetadataTypesSystemPromptAsLeadingStable(t *testing.T) {
	request := llm.GenerateRequest{
		SystemPrompt: "固定の指示",
		Messages: []llm.Message{
			{Role: "system", Content: "現在時刻", Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "time"}},
			{Role: "user", Content: "本文", Type: llm.PromptContextUser},
		},
	}

	blocks := promptContextBlockMetadata(request)

	want := []struct {
		index int
		typ   llm.PromptContextType
	}{
		{0, llm.PromptContextStable},
		{1, llm.PromptContextVariable},
		{2, llm.PromptContextUser},
	}
	if len(blocks) != len(want) {
		t.Fatalf("blocks = %#v, want %d entries", blocks, len(want))
	}
	for position, expected := range want {
		if blocks[position]["message_index"] != expected.index || blocks[position]["type"] != string(expected.typ) {
			t.Fatalf("blocks[%d] = %#v, want message_index=%d type=%q", position, blocks[position], expected.index, expected.typ)
		}
	}
}

func TestPromptContextBlockMetadataWithoutSystemPromptAddsNoLeadingBlock(t *testing.T) {
	blocks := promptContextBlockMetadata(llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "本文"}}})
	if len(blocks) != 0 {
		t.Fatalf("blocks = %#v, want none for an untyped user-only request", blocks)
	}
}

// Generate が送る body で、index 0 の system と、その index に対応するブロックが揃う。
func TestGatewayProviderGenerateDeclaresSystemPromptBlockAtMessageIndexZero(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second).
		WithRenCrowExecution("shiro", "worker", "worker")

	_, err := provider.Generate(context.Background(), llm.GenerateRequest{
		SystemPrompt: "固定の指示",
		Messages:     []llm.Message{{Role: "user", Content: "本文"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	messages, _ := payload["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want system and user", messages)
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "固定の指示" {
		t.Fatalf("messages[0] = %#v, want the SystemPrompt as the leading system message", first)
	}
	metadata, _ := payload["rencrow"].(map[string]any)
	blocks, _ := metadata["prompt_context_blocks"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("prompt_context_blocks = %#v, want exactly the leading system block", blocks)
	}
	block, _ := blocks[0].(map[string]any)
	if block["message_index"] != float64(0) || block["type"] != string(llm.PromptContextStable) {
		t.Fatalf("prompt_context_blocks[0] = %#v, want message_index=0 type=%q", block, llm.PromptContextStable)
	}
}
