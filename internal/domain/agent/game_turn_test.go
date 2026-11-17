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

func TestEveryAgentKeepsTheNetHackTokenGameTurnRequestContract(t *testing.T) {
	for _, persona := range []string{"mio", "shiro", "kuro", "midori"} {
		t.Run(persona, func(t *testing.T) {
			var captured llm.GenerateRequest
			provider := &mockLLMProvider{
				generateFunc: func(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
					captured = request
					return llm.GenerateResponse{Content: "search"}, nil
				},
			}
			var decide func(context.Context, string) (string, error)
			switch persona {
			case "mio", "shiro":
				viewerPrompts := map[string]string(nil)
				recipient := "mio"
				if persona == "shiro" {
					viewerPrompts = map[string]string{"shiro": "shiro prompt"}
					recipient = "shiro"
				}
				mio := &MioAgent{llmProvider: provider, viewerPrompts: viewerPrompts}
				decide = func(ctx context.Context, prompt string) (string, error) {
					return mio.DecideGameTurn(ctx, prompt, recipient)
				}
			case "kuro":
				decide = NewHeavyAgent(provider, "kuro prompt").DecideGameTurn
			case "midori":
				decide = NewWildAgent(provider, "midori prompt").DecideGameTurn
			}
			if _, err := decide(t.Context(), "search"); err != nil {
				t.Fatalf("DecideGameTurn() error = %v", err)
			}
			if captured.ResponseFormat != llm.ResponseFormatText || captured.OnToken != nil || captured.MaxTokens != gameTurnMaxTokens {
				t.Fatalf("token request format/stream/budget = %q/%t/%d", captured.ResponseFormat, captured.OnToken != nil, captured.MaxTokens)
			}
			stop, ok := captured.ProviderOptions["stop"].([]string)
			if !ok || len(stop) != 4 || stop[0] != "," || stop[1] != "\n" || stop[2] != " " || stop[3] != "\"" {
				t.Fatalf("NetHack stop sequence contract changed: %#v", captured.ProviderOptions)
			}
		})
	}
}

func TestMioDecideStructuredGameTurnRequestsBoundedNonStreamingJSONWithoutTokenStops(t *testing.T) {
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = request
			return llm.GenerateResponse{Content: "{\"action\":\"move\",\"target\":\"hex-1\"}"}, nil
		},
	}
	mio := &MioAgent{
		llmProvider:   provider,
		viewerPrompts: map[string]string{"shiro": "shiro prompt"},
		generation: MioGenerationOptions{
			Stream:      true,
			MaxTokens:   16,
			Temperature: 0.3,
		},
	}

	if _, err := mio.DecideStructuredGameTurn(t.Context(), "choose one structured action", "shiro"); err != nil {
		t.Fatalf("DecideStructuredGameTurn() error = %v", err)
	}
	if captured.ResponseFormat != llm.ResponseFormatJSONObject || captured.OnToken != nil {
		t.Fatalf("format/stream = %q/%t, want json_object/non-stream", captured.ResponseFormat, captured.OnToken != nil)
	}
	if captured.MaxTokens != structuredGameTurnMaxTokens {
		t.Fatalf("MaxTokens = %d, want bounded structured budget %d", captured.MaxTokens, structuredGameTurnMaxTokens)
	}
	if _, ok := captured.ProviderOptions["stop"]; ok {
		t.Fatalf("structured game turn must not use token stop sequences: %#v", captured.ProviderOptions)
	}
	if len(captured.Messages) == 0 || captured.Messages[0].Content != "shiro prompt" {
		t.Fatalf("structured call did not use the requested persona identity: %#v", captured.Messages)
	}
}

func TestHeavyAndWildDecideStructuredGameTurnRequestBoundedNonStreamingJSON(t *testing.T) {
	for _, persona := range []string{"kuro", "midori"} {
		t.Run(persona, func(t *testing.T) {
			var captured llm.GenerateRequest
			provider := &mockLLMProvider{
				generateFunc: func(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
					captured = request
					return llm.GenerateResponse{Content: "{\"action\":\"wait\"}"}, nil
				},
			}
			var decide func(context.Context, string) (string, error)
			if persona == "kuro" {
				decide = NewHeavyAgent(provider, "kuro prompt").DecideStructuredGameTurn
			} else {
				decide = NewWildAgent(provider, "midori prompt").DecideStructuredGameTurn
			}
			if _, err := decide(t.Context(), "one complete action"); err != nil {
				t.Fatalf("DecideStructuredGameTurn() error = %v", err)
			}
			if captured.ResponseFormat != llm.ResponseFormatJSONObject || captured.OnToken != nil {
				t.Fatalf("format/stream = %q/%t, want json_object/non-stream", captured.ResponseFormat, captured.OnToken != nil)
			}
			if captured.MaxTokens != structuredGameTurnMaxTokens {
				t.Fatalf("MaxTokens = %d, want %d", captured.MaxTokens, structuredGameTurnMaxTokens)
			}
			if _, ok := captured.ProviderOptions["stop"]; ok {
				t.Fatalf("structured game turn must not use token stop sequences: %#v", captured.ProviderOptions)
			}
		})
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
