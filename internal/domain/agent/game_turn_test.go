package agent

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestMioDecideGameTurnRequestsBoundedNonStreamingText(t *testing.T) {
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = request
			return llm.GenerateResponse{Content: `{"intent":"search"}`}, nil
		},
	}
	mio := &MioAgent{
		llmProvider: provider,
		generation: MioGenerationOptions{
			Stream:      true,
			MaxTokens:   256,
			Temperature: 0.3,
		},
	}

	if _, err := mio.DecideGameTurn(t.Context(), "choose one action", "mio"); err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}
	if captured.ResponseFormat != llm.ResponseFormatText {
		t.Fatalf("ResponseFormat = %q, want text", captured.ResponseFormat)
	}
	if captured.OnToken != nil {
		t.Fatal("game turn structured output must be non-streaming")
	}
	if captured.MaxTokens != gameTurnMaxTokens {
		t.Fatalf("MaxTokens = %d, want bounded game budget %d", captured.MaxTokens, gameTurnMaxTokens)
	}
	stop, ok := captured.ProviderOptions["stop"].([]string)
	if !ok || len(stop) != 4 || stop[0] != "," {
		t.Fatalf("game turn stop contract missing: %#v", captured.ProviderOptions)
	}
}

// ゲームの判断も型付きで渡す。型の無い system があると、宛先が先頭固定の配置
// （single_leading_stable）でも Gateway は fallback し、時刻の Variable が先頭 system へ
// 畳み込まれて先頭が要求ごとに変わる。
func TestMioDecideGameTurnTypesEverySystemMessage(t *testing.T) {
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = request
			return llm.GenerateResponse{Content: "search"}, nil
		},
	}
	mio := &MioAgent{llmProvider: provider, systemPrompt: "persona prompt"}

	if _, err := mio.DecideGameTurn(t.Context(), "choose one action", "mio"); err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}

	for index, message := range captured.Messages {
		if message.Role == "system" && message.Type == "" {
			t.Fatalf("message %d is a system message without a Type: %q", index, message.Content)
		}
	}
	first := captured.Messages[0]
	if first.Role != "system" || first.Content != "persona prompt" || first.Type != llm.PromptContextCharacter {
		t.Fatalf("leading message = %#v, want the persona prompt as a Character system message", first)
	}
	var boundary *llm.Message
	for index := range captured.Messages {
		if captured.Messages[index].Content == gameTurnBoundaryPrompt {
			boundary = &captured.Messages[index]
		}
	}
	if boundary == nil || boundary.Type != llm.PromptContextStable {
		t.Fatalf("game turn boundary = %#v, want a Stable system message", boundary)
	}
	last := captured.Messages[len(captured.Messages)-1]
	if last.Role != "user" || last.Type != llm.PromptContextUser {
		t.Fatalf("last message = %#v, want the observation as a User message", last)
	}
}
