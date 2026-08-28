package agent

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestMioDecideGameTurnRequestsNonStreamingJSONObject(t *testing.T) {
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
	if captured.ResponseFormat != llm.ResponseFormatJSONObject {
		t.Fatalf("ResponseFormat = %q, want %q", captured.ResponseFormat, llm.ResponseFormatJSONObject)
	}
	if captured.OnToken != nil {
		t.Fatal("game turn structured output must be non-streaming")
	}
	if captured.MaxTokens != gameTurnMaxTokens {
		t.Fatalf("MaxTokens = %d, want bounded game budget %d", captured.MaxTokens, gameTurnMaxTokens)
	}
}
