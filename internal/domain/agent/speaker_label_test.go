package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// Failure: 応答の頭に `[midori] ` や `[midori] [midori] ` のような Agent 名ラベルが付く。
// 原因は、履歴の Agent 発言を `[speaker] msg` の形でプロンプトへ入れる
// （conversation.RecallPack.ToPromptMessages）ため、モデルがその形を真似ること。
// Invariant: どの Agent の応答も、先頭に Agent 名ラベルを持たない。応答本文（非stream）、
// stream の delta、保存する履歴、読み上げ文は同じ無ラベルの本文から作られる。
// 以下は Mio（Shiro CHAT を含む）・Midori・Kuro・Shiro の全経路で、その不変条件を検証する。

type labelCase struct {
	name      string
	recipient string // Mio.Chat の宛先（空は mio）。空以外はその宛先でだけ実行する
	tokens    []string
	content   string
	wantBody  string
}

func labelCases() []labelCase {
	return []labelCase{
		{
			name:     "self label split across deltas",
			tokens:   []string{"[mid", "ori", "]", " ", "こん", "にちは"},
			content:  "[midori] こんにちは",
			wantBody: "こんにちは",
		},
		{
			name:      "doubled label of another agent (shiro after midori)",
			recipient: "shiro",
			tokens:    []string{"[mid", "ori] [mid", "ori] ", "報告", "です"},
			content:   "[midori] [midori] 報告です",
			wantBody:  "報告です",
		},
		{
			name:     "self label with case and colon",
			tokens:   []string{" [MI", "O]：", "こんにちは"},
			content:  " [MIO]：こんにちは ",
			wantBody: "こんにちは",
		},
		{
			name:     "other agent label is removed",
			tokens:   []string{"[shiro]", "からの報告です"},
			content:  "[shiro]からの報告です",
			wantBody: "からの報告です",
		},
		{
			name:     "full width space",
			tokens:   []string{"　[mio]", "　こんにちは"},
			content:  "　[mio]　こんにちは",
			wantBody: "こんにちは",
		},
		{
			name:     "embedded label stays",
			tokens:   []string{"引用: ", "[mio] ", "こんにちは"},
			content:  "引用: [mio] こんにちは",
			wantBody: "引用: [mio] こんにちは",
		},
		{
			name:     "unknown bracket stays",
			tokens:   []string{"[注", "] 補足です"},
			content:  "[注] 補足です",
			wantBody: "[注] 補足です",
		},
		{
			name:     "label only",
			tokens:   []string{"[mid", "ori]"},
			content:  "[midori]",
			wantBody: "",
		},
		{
			name:     "empty response",
			tokens:   nil,
			content:  "",
			wantBody: "",
		},
		{
			name:     "no label",
			tokens:   []string{"こんにちは", "。"},
			content:  "こんにちは。",
			wantBody: "こんにちは。",
		},
	}
}

func labelProvider(tokens []string, content string) *mockLLMProvider {
	return &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		if req.OnToken != nil {
			for _, token := range tokens {
				req.OnToken(token)
			}
		}
		return llm.GenerateResponse{Content: content}, nil
	}}
}

type labelObservation struct {
	streamed string
	stored   []string
	speakers []conversation.Speaker
}

func (o *labelObservation) engine() *mockConversationEngine {
	return &mockConversationEngine{
		endTurnAsFunc: func(_ context.Context, _, _, response string, speaker conversation.Speaker) error {
			o.stored = append(o.stored, response)
			o.speakers = append(o.speakers, speaker)
			return nil
		},
	}
}

func (o *labelObservation) context() context.Context {
	return llm.ContextWithStreamCallback(context.Background(), func(token string) { o.streamed += token })
}

func assertLabelFree(t *testing.T, obs *labelObservation, response string, tc labelCase, wantSpeaker conversation.Speaker, checkStream bool) {
	t.Helper()
	if response != tc.wantBody {
		t.Errorf("response = %q, want %q", response, tc.wantBody)
	}
	if len(obs.stored) != 1 || obs.stored[0] != tc.wantBody {
		t.Errorf("stored history = %q, want [%q]", obs.stored, tc.wantBody)
	}
	if len(obs.speakers) == 1 && obs.speakers[0] != wantSpeaker {
		t.Errorf("stored speaker = %q, want %q", obs.speakers[0], wantSpeaker)
	}
	if checkStream {
		if got := strings.TrimSpace(obs.streamed); got != tc.wantBody {
			t.Errorf("streamed text = %q, want %q", got, tc.wantBody)
		}
	}
}

func TestMioChatRemovesLeadingAgentLabels(t *testing.T) {
	for _, tc := range labelCases() {
		for _, recipient := range []string{"", "shiro"} {
			if tc.recipient != "" && tc.recipient != recipient {
				continue
			}
			name := tc.name + "/recipient=" + recipient
			t.Run(name, func(t *testing.T) {
				obs := &labelObservation{}
				agent := NewMioAgent(labelProvider(tc.tokens, tc.content), &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, obs.engine())
				input := newAgentTurnInput(t, "hello", "viewer", "viewer-user").WithViewerRecipient(recipient)
				response, err := agent.Chat(obs.context(), input)
				if err != nil {
					t.Fatalf("Chat failed: %v", err)
				}
				want := conversation.SpeakerMio
				if recipient == "shiro" {
					want = conversation.SpeakerShiro
				}
				assertLabelFree(t, obs, response, tc, want, true)
			})
		}
	}
}

// Mio の attribution retry は2回目の生成を同じ ctx の stream へ流す。2回目にも
// 新しい filter が掛かり、無ラベルの本文だけが保存・返却される。
func TestMioChatRetryAlsoRemovesLeadingAgentLabels(t *testing.T) {
	obs := &labelObservation{}
	engine := obs.engine()
	engine.beginTurnFunc = func(context.Context, string, string) (*conversation.RecallPack, error) {
		return &conversation.RecallPack{ShortContext: []conversation.Message{
			{Speaker: conversation.SpeakerShiro, Msg: "世界の調律師という設定はどう？"},
		}}, nil
	}
	calls := 0
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		if calls == 1 {
			req.OnToken("[mi")
			req.OnToken("o] 世界の調律師という設定はどう？")
			return llm.GenerateResponse{Content: "[mio] 世界の調律師という設定はどう？"}, nil
		}
		req.OnToken("[shi")
		req.OnToken("ro] あなたの案")
		req.OnToken("いいね。")
		return llm.GenerateResponse{Content: "[shiro] あなたの案いいね。"}, nil
	}}
	agent := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine)

	response, err := agent.Chat(obs.context(), newAgentTurnInput(t, "続けて", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("generate calls = %d, want 2 (retry expected)", calls)
	}
	if response != "あなたの案いいね。" {
		t.Errorf("response = %q", response)
	}
	if len(obs.stored) != 1 || obs.stored[0] != "あなたの案いいね。" {
		t.Errorf("stored = %q", obs.stored)
	}
	if strings.Contains(obs.streamed, "[") || strings.Contains(obs.streamed, "]") {
		t.Errorf("streamed text contains a label: %q", obs.streamed)
	}
}

// stream が判定の途中（ラベルになり得る先頭部分）で終わっても、保留分は Generate の完了時に流れる。
func TestMioChatFlushesUndecidedStreamHead(t *testing.T) {
	obs := &labelObservation{}
	agent := NewMioAgent(labelProvider([]string{"[mid"}, "[mid"), &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, obs.engine())
	response, err := agent.Chat(obs.context(), newAgentTurnInput(t, "hello", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if response != "[mid" || obs.streamed != "[mid" {
		t.Fatalf("response = %q, streamed = %q, want both %q", response, obs.streamed, "[mid")
	}
}

// 生成が失敗したときは、判定中の保留分（ラベルになり得る先頭部分）を流さない。
func TestChatAndGenerateDoNotFlushUndecidedHeadOnError(t *testing.T) {
	failing := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		req.OnToken("[mid")
		return llm.GenerateResponse{}, context.DeadlineExceeded
	}}
	obs := &labelObservation{}
	mio := NewMioAgent(failing, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	if _, err := mio.Chat(obs.context(), newAgentTurnInput(t, "hello", "viewer", "viewer-user")); err == nil {
		t.Fatal("Mio.Chat must return the generation error")
	}
	if obs.streamed != "" {
		t.Errorf("Mio streamed %q after a failed generation", obs.streamed)
	}

	obs = &labelObservation{}
	wild := NewWildAgent(failing, "creative system")
	if _, err := wild.Generate(obs.context(), newAgentTurnInput(t, "/wild 夕焼け", "viewer", "viewer-user")); err == nil {
		t.Fatal("Wild.Generate must return the generation error")
	}
	if obs.streamed != "" {
		t.Errorf("Wild streamed %q after a failed generation", obs.streamed)
	}
}

// ctx に stream callback が無いときは、これまでどおり非streamのままにする。
func TestMioChatWithoutStreamCallbackKeepsNonStreamingRequest(t *testing.T) {
	var gotOnToken bool
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		gotOnToken = req.OnToken != nil
		return llm.GenerateResponse{Content: "[mio] こんにちは"}, nil
	}}
	agent := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	response, err := agent.Chat(context.Background(), newAgentTurnInput(t, "hello", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if response != "こんにちは" {
		t.Fatalf("response = %q", response)
	}
	if gotOnToken {
		t.Fatal("OnToken must stay nil when the caller did not ask for a stream")
	}
}

func TestWildGenerateRemovesLeadingAgentLabels(t *testing.T) {
	for _, tc := range labelCases() {
		if tc.recipient != "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			obs := &labelObservation{}
			wild := NewWildAgent(labelProvider(tc.tokens, tc.content), "creative system").WithConversationEngine(obs.engine())
			response, err := wild.Generate(obs.context(), newAgentTurnInput(t, "/wild 夕焼けの話", "viewer", "viewer-user"))
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}
			assertLabelFree(t, obs, response, tc, conversation.SpeakerMidori, true)
		})
	}
}

func TestWildGenerateStaysStreamingWithoutCallerCallback(t *testing.T) {
	var gotOnToken bool
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		gotOnToken = req.OnToken != nil
		if req.OnToken != nil {
			req.OnToken("[midori] ")
		}
		return llm.GenerateResponse{Content: "[midori] 夕焼けです"}, nil
	}}
	response, err := NewWildAgent(provider, "creative system").Generate(context.Background(), newAgentTurnInput(t, "/wild 夕焼け", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if !gotOnToken {
		t.Fatal("Wild must always select the streaming provider path")
	}
	if response != "夕焼けです" {
		t.Fatalf("response = %q", response)
	}
}

// Kuro は stream を使わない。応答本文と保存する履歴だけが対象。
func TestHeavyGenerateRemovesLeadingAgentLabels(t *testing.T) {
	for _, tc := range labelCases() {
		if tc.recipient != "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			var gotOnToken bool
			obs := &labelObservation{}
			provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
				gotOnToken = req.OnToken != nil
				return llm.GenerateResponse{Content: tc.content}, nil
			}}
			heavy := NewHeavyAgent(provider, "heavy system").WithConversationEngine(obs.engine())
			response, err := heavy.Generate(context.Background(), newAgentTurnInput(t, "/heavy 調べて", "viewer", "viewer-user"))
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}
			if gotOnToken {
				t.Fatal("Heavy must stay non-streaming")
			}
			assertLabelFree(t, obs, response, tc, conversation.SpeakerKuro, false)
		})
	}
}

// Shiro の Worker 実行（OPS）は stream を使わない。応答本文と保存する履歴が対象。
func TestShiroExecuteRemovesLeadingAgentLabels(t *testing.T) {
	for _, tc := range labelCases() {
		if tc.recipient != "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			obs := &labelObservation{}
			shiro := NewShiroAgent(labelProvider(nil, tc.content), &mockToolRunner{}, &mockMCPClient{}, "shiro system", nil).WithConversationEngine(obs.engine())
			response, err := shiro.Execute(context.Background(), newAgentTurnInput(t, "状態を確認して", "viewer", "viewer-user"))
			if err != nil {
				t.Fatalf("Execute failed: %v", err)
			}
			assertLabelFree(t, obs, response, tc, conversation.SpeakerShiro, false)
		})
	}
}
