package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

// 不変条件: どの Agent の応答も、先頭に Agent 名ラベル（`[midori] `、重なった
// `[midori] [midori] `、自分以外の Agent 名）を持たない。agent.thinking の delta、
// agent.response の本文、読み上げ文（TTS push）は同じ無ラベルの本文から作られる。
// モデルが履歴の `[speaker] msg` を真似てラベルを出力する状況を、実Agentと
// ProcessMessage を通した実経路で再現して固定する。

const labelFreeBody = "夕焼けがきれいです。"

// labeledStreamProvider は、ラベルが delta に分割されて流れる LLM を模す。
type labeledStreamProvider struct {
	tokens  []string
	content string
}

func (p labeledStreamProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if req.OnToken != nil {
		for _, token := range p.tokens {
			req.OnToken(token)
		}
	}
	return llm.GenerateResponse{Content: p.content}, nil
}

func (labeledStreamProvider) Name() string { return "labeled-stream" }

func productionLabeledProvider() labeledStreamProvider {
	return labeledStreamProvider{
		tokens:  []string{"[mid", "ori", "] [mid", "ori] ", "夕焼けが", "きれい", "です。"},
		content: "[midori] [midori] " + labelFreeBody,
	}
}

func assertNoSpeakerLabel(t *testing.T, where, text string) {
	t.Helper()
	lower := strings.ToLower(text)
	if strings.ContainsAny(text, "[]") || strings.Contains(lower, "midori") {
		t.Errorf("%s contains an agent label: %q", where, text)
	}
}

func assertLabelFreeTurn(t *testing.T, listener *recordingEventListener, bridge *mockTTSBridge, wantStreamed bool) {
	t.Helper()
	var thinking, responses []string
	for _, ev := range listener.events {
		switch ev.Type {
		case "agent.thinking":
			thinking = append(thinking, ev.Content)
		case "agent.response":
			responses = append(responses, ev.Content)
		}
	}
	if len(responses) == 0 {
		t.Fatal("no agent.response event")
	}
	for _, content := range responses {
		if strings.TrimSpace(content) != labelFreeBody {
			t.Errorf("agent.response content = %q, want %q", content, labelFreeBody)
		}
	}
	streamed := strings.Join(thinking, "")
	if wantStreamed {
		if strings.TrimSpace(streamed) != labelFreeBody {
			t.Errorf("agent.thinking deltas = %q, want %q", streamed, labelFreeBody)
		}
	}
	assertNoSpeakerLabel(t, "agent.thinking deltas", streamed)
	if len(bridge.pushes) == 0 {
		t.Fatal("nothing was voiced")
	}
	spoken := strings.Join(bridge.pushes, "")
	assertNoSpeakerLabel(t, "TTS speech text", spoken)
	if !strings.Contains(spoken, "夕焼けがきれいです") {
		t.Errorf("TTS speech text lost the body: %q", spoken)
	}
	for _, push := range bridge.pushes {
		assertNoSpeakerLabel(t, "TTS push", push)
	}
}

func TestProcessMessageRemovesAgentLabelsFromEventsAndSpeech(t *testing.T) {
	for _, recipient := range []string{"mio", "shiro", "midori", "kuro"} {
		t.Run(recipient, func(t *testing.T) {
			provider := productionLabeledProvider()
			newMio := func() *mockMioAgent {
				real := agent.NewMioAgent(provider, nil, nil, nil, nil, nil)
				return &mockMioAgent{
					decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
					chatFunc: real.Chat,
				}
			}
			orch := NewMessageOrchestrator(newMockSessionRepository(), newMio(), &mockShiroAgent{response: "unused"}, nil, nil, nil, nil, nil)
			attachCanonicalTestTaskOwner(t, orch)
			orch.SetShiroChatAgent(newMio())
			orch.SetWildAgent(agent.NewWildAgent(provider, "creative system"))
			orch.SetHeavyAgent(agent.NewHeavyAgent(provider, "heavy system"))
			bridge := &mockTTSBridge{}
			orch.SetTTSBridge(bridge)
			listener := &recordingEventListener{}
			orch.SetEventListener(listener)

			_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
				SessionID:   "label-invariant",
				Channel:     "viewer",
				ChatID:      "viewer-user",
				UserMessage: "こんにちは",
				To:          recipient,
			})
			if err != nil {
				t.Fatalf("ProcessMessage: %v", err)
			}
			// Kuro の LLM 呼び出しは stream しない。本文と読み上げ文だけを検証する。
			assertLabelFreeTurn(t, listener, bridge, recipient != "kuro")
		})
	}
}

func TestProcessMessageOPSRemovesAgentLabelsFromSpeech(t *testing.T) {
	provider := productionLabeledProvider()
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteOPS, 0.9, "ops")}
	shiro := agent.NewShiroAgent(provider, nil, nil, "shiro system", nil)
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, shiro, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	bridge := &mockTTSBridge{}
	orch.SetTTSBridge(bridge)
	listener := &recordingEventListener{}
	orch.SetEventListener(listener)

	if _, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "label-invariant-ops",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "状態を確認して",
	}); err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	assertLabelFreeTurn(t, listener, bridge, false)
}
