package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

const testSpeechPlanJSON = `{"schema_version":"rencrow.speech/1.0","utterances":[{"segments":[{"text":"hello"}],"delivery":{"tone":"warm","attitude":"gentle","energy":"medium","pace":"normal","mode":"dialogue"},"pause_after_ms":0}]}`

func TestExactSharedRecallSpeechPlanForDeterministicShortcuts(t *testing.T) {
	const token = "RC_CTX_20260803_1328_L1S4"
	for _, tc := range []struct {
		name  string
		input string
		build func(llm.LLMProvider, *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error)
	}{
		{
			name:  "mio",
			input: "/chat 合言葉を英数字だけでそのまま教えて。",
			build: func(provider llm.LLMProvider, engine *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error) {
				return NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine).Chat
			},
		},
		{
			name:  "heavy",
			input: "/heavy 合言葉を英数字だけでそのまま教えて。",
			build: func(provider llm.LLMProvider, engine *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error) {
				return NewHeavyAgent(provider, "heavy").WithConversationEngine(engine).Generate
			},
		},
		{
			name:  "wild",
			input: "/wild 合言葉を英数字だけでそのまま教えて。",
			build: func(provider llm.LLMProvider, engine *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error) {
				return NewWildAgent(provider, "wild").WithConversationEngine(engine).Generate
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providerCalls := 0
			provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
				providerCalls++
				return llm.GenerateResponse{Content: "must not rewrite exact recall"}, nil
			}}
			engine := &mockConversationEngine{beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
				return &conversation.RecallPack{ShortContext: []conversation.Message{{
					Speaker: conversation.SpeakerUser,
					Msg:     "この会話固有の合言葉は " + token + " です",
				}}}, nil
			}}
			generate := tc.build(provider, engine)
			var result speech.Result
			callbackCalls := 0
			var streamed []string
			ctx := llm.ContextWithStreamCallback(context.Background(), func(value string) { streamed = append(streamed, value) })
			ctx = ContextWithSpeechPlanCallback(ctx, func(got speech.Result) {
				callbackCalls++
				result = got
			})

			got, err := generate(ctx, newAgentTurnInput(t, tc.input, "viewer", "viewer-user"))
			if err != nil {
				t.Fatalf("generate exact recall: %v", err)
			}
			if got != token {
				t.Fatalf("exact recall = %q, want %q", got, token)
			}
			if providerCalls != 0 {
				t.Fatalf("LLM calls = %d, want 0 for deterministic recall", providerCalls)
			}
			if callbackCalls != 1 {
				t.Fatalf("SpeechPlan callback calls = %d, want 1", callbackCalls)
			}
			if len(streamed) != 0 {
				t.Fatalf("legacy token stream = %#v, want none in SpeechPlan mode", streamed)
			}
			if !result.Valid || result.DisplayText != token || result.FallbackReason != "" {
				t.Fatalf("neutral SpeechPlan result = %#v", result)
			}
			if len(result.Plan.Utterances) != 1 || len(result.Plan.Utterances[0].Segments) != 1 || result.Plan.Utterances[0].Segments[0].Text != token {
				t.Fatalf("neutral SpeechPlan body = %#v", result.Plan)
			}
			if result.Plan.Utterances[0].Delivery != (speech.Delivery{Tone: "neutral", Attitude: "neutral", Energy: "medium", Pace: "normal", Mode: "dialogue"}) {
				t.Fatalf("deterministic SpeechPlan delivery = %#v, want neutral", result.Plan.Utterances[0].Delivery)
			}
		})
	}
}

func TestChatSpeechPlanGenerationProjectsBeforeMemoryCommit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recipient string
		speaker   conversation.Speaker
		build     func(llm.LLMProvider, *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error)
	}{
		{
			name:      "mio chat",
			recipient: "mio",
			speaker:   conversation.SpeakerMio,
			build: func(provider llm.LLMProvider, engine *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error) {
				mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine)
				return mio.Chat
			},
		},
		{
			name:      "shiro chat",
			recipient: "shiro",
			speaker:   conversation.SpeakerShiro,
			build: func(provider llm.LLMProvider, engine *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error) {
				mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine)
				return mio.Chat
			},
		},
		{
			name:      "midori chat",
			recipient: "midori",
			speaker:   conversation.SpeakerMidori,
			build: func(provider llm.LLMProvider, engine *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error) {
				wild := NewWildAgent(provider, "wild system").WithConversationEngine(engine)
				return wild.Generate
			},
		},
		{
			name:      "kuro chat",
			recipient: "kuro",
			speaker:   conversation.SpeakerKuro,
			build: func(provider llm.LLMProvider, engine *mockConversationEngine) func(context.Context, conversation.TurnInput) (string, error) {
				heavy := NewHeavyAgent(provider, "heavy system").WithConversationEngine(engine)
				return heavy.Generate
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			var captured llm.GenerateRequest
			generateCalls := 0
			provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
				generateCalls++
				captured = req
				events = append(events, "generate")
				if req.OnToken != nil {
					req.OnToken(testSpeechPlanJSON)
				}
				time.Sleep(2 * time.Millisecond)
				return llm.GenerateResponse{Content: testSpeechPlanJSON, TokensUsed: 17, TokensPerSecond: 23.5}, nil
			}}
			engine := &mockConversationEngine{
				beginTurnFunc: func(_ context.Context, sessionID, userMessage string) (*conversation.RecallPack, error) {
					// A projected reply equal to the latest other-speaker message must
					// not trigger Mio's ordinary attribution repair generation.
					return &conversation.RecallPack{ShortContext: []conversation.Message{{Speaker: conversation.SpeakerUser, Msg: "hello"}}}, nil
				},
				commitTurnFunc: func(_ context.Context, request conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
					events = append(events, "commit")
					if request.AgentMessage != "hello" {
						t.Errorf("committed agent message = %q, want projected display text", request.AgentMessage)
					}
					if request.AgentSpeaker != tc.speaker {
						t.Errorf("committed speaker = %q, want %q", request.AgentSpeaker, tc.speaker)
					}
					return conversation.ConversationTurnResult{TurnID: request.TurnID, SessionID: request.SessionID, Status: conversation.ConversationTurnCompleted}, nil
				},
			}
			generate := tc.build(provider, engine)
			var result speech.Result
			var streamed []string
			var metrics []llm.GenerationMetrics
			ctx := ContextWithSpeechPlanCallback(context.Background(), func(got speech.Result) {
				events = append(events, "plan")
				result = got
			})
			ctx = llm.ContextWithStreamCallback(ctx, func(token string) { streamed = append(streamed, token) })
			ctx = llm.ContextWithGenerationMetricsCallback(ctx, func(got llm.GenerationMetrics) { metrics = append(metrics, got) })

			input := newAgentTurnInput(t, "say hello", "viewer", "chat-1").WithViewerRecipient(tc.recipient)
			response, err := generate(ctx, input)
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if response != "hello" {
				t.Fatalf("response = %q, want projected display text", response)
			}
			if generateCalls != 1 {
				t.Fatalf("LLM Generate calls = %d, want one generation", generateCalls)
			}
			if captured.ResponseFormat != llm.ResponseFormatJSONObject {
				t.Fatalf("ResponseFormat = %q, want json_object", captured.ResponseFormat)
			}
			if captured.OnToken != nil {
				t.Fatal("JSON SpeechPlan generation must not expose raw JSON token streaming")
			}
			var prompt strings.Builder
			for _, message := range captured.Messages {
				prompt.WriteString(message.Content)
			}
			for _, expected := range []string{"rencrow.speech/1.0", "segments", "delivery", "voice_id", "caption", "lora_adapter"} {
				if !strings.Contains(prompt.String(), expected) {
					t.Errorf("SpeechPlan prompt missing schema/restriction %q", expected)
				}
			}
			if len(streamed) != 0 {
				t.Fatalf("raw generation reached legacy stream callback: %#v", streamed)
			}
			if !result.Valid || result.DisplayText != "hello" || len(result.Plan.Utterances) != 1 {
				t.Fatalf("SpeechPlan callback result = %#v", result)
			}
			if result.LLMGenerationMS == nil || *result.LLMGenerationMS < 1 {
				t.Fatalf("LLM generation measurement = %v, want at least 1ms", result.LLMGenerationMS)
			}
			if result.PlanValidationMS == nil {
				t.Fatal("plan validation measurement is unavailable after parsing")
			}
			if strings.Join(events, ",") != "generate,plan,commit" {
				t.Fatalf("generation/plan/commit order = %s", strings.Join(events, ","))
			}
			if len(metrics) != 1 || metrics[0].CompletionTokens != 17 || metrics[0].TokensPerSecond != 23.5 {
				t.Fatalf("generation metrics = %#v", metrics)
			}
		})
	}
}

func TestMioSpeechPlanGenerationFailureReportsValidationAsUnavailable(t *testing.T) {
	providerFailure := errors.New("provider failure with private details")
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{}, providerFailure
	}}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, &mockConversationEngine{})
	var got speech.Result
	callbackCount := 0
	ctx := ContextWithSpeechPlanCallback(context.Background(), func(result speech.Result) {
		got = result
		callbackCount++
	})
	_, err := mio.Chat(ctx, newAgentTurnInput(t, "返事して", "viewer", "chat-provider-error"))
	if !errors.Is(err, providerFailure) {
		t.Fatalf("Chat() error = %v, want provider failure", err)
	}
	if callbackCount != 1 {
		t.Fatalf("SpeechPlan callback count = %d, want one failure observation", callbackCount)
	}
	if got.FallbackReason != speech.FallbackGenerationFailed {
		t.Fatalf("FallbackReason = %q, want bounded generation failure code", got.FallbackReason)
	}
	if got.LLMGenerationMS == nil {
		t.Fatal("provider failure omitted its measured Generate duration")
	}
	if got.PlanValidationMS != nil {
		t.Fatalf("plan validation measurement = %v, want unavailable", got.PlanValidationMS)
	}
	if strings.Contains(got.FallbackReason, "private details") {
		t.Fatal("provider error text leaked into fallback reason")
	}
}

func TestMioSpeechPlanKeepsRecoverableTextWhenPlanIsNotVoiceable(t *testing.T) {
	const invalidPlanJSON = `{"schema_version":"rencrow.speech/1.0","utterances":[{"segments":[{"text":"本文は表示する。"}],"delivery":{"tone":"neutral","attitude":"neutral","energy":"medium","pace":"normal","mode":"dialogue"},"pause_after_ms":0}],"voice_id":"forbidden"}`
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{Content: invalidPlanJSON}, nil
	}}
	engine := &mockConversationEngine{}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine)
	var got speech.Result
	ctx := ContextWithSpeechPlanCallback(context.Background(), func(result speech.Result) { got = result })

	response, err := mio.Chat(ctx, newAgentTurnInput(t, "返事して", "viewer", "chat-2"))
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if response != "本文は表示する。" {
		t.Fatalf("response = %q, want recoverable display text", response)
	}
	if got.Valid {
		t.Fatal("plan with a forbidden provider field must not be voiceable")
	}
	if len(engine.commitRequests) != 1 || engine.commitRequests[0].AgentMessage != response {
		t.Fatalf("committed conversation turns = %#v", engine.commitRequests)
	}
}
