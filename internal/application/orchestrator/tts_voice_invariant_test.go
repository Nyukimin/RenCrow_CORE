package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

// 不変条件: 読み上げる発話の声は、その発話を生成した Agent（agent.response の from。
// 委譲先の結果を mio が同一本文で中継する後続の agent.response は別発話ではない）の声である。
// ProcessMessage を通した実経路で、TTS セッションの voice_id / character_id、
// 読み上げ(PushText)の emotion voice profile、最初の agent.response.from が一致することを固定する。

type voicedTurn struct {
	bridge    *mockTTSBridge
	listener  *recordingEventListener
	responder string // 最初の agent.response の from
}

func profileOfAgent(t *testing.T, agent string) string {
	t.Helper()
	_, profile, ok := moduletts.VoiceForAgent(agent)
	if !ok {
		t.Fatalf("test agent %q has no voice", agent)
	}
	return profile
}

func firstAgentResponseFrom(events []OrchestratorEvent) string {
	for _, ev := range events {
		if ev.Type == "agent.response" {
			return ev.From
		}
	}
	return ""
}

func assertVoicedByAgent(t *testing.T, turn voicedTurn, want string) {
	t.Helper()
	if turn.responder != want {
		t.Fatalf("first agent.response from = %q, want %q", turn.responder, want)
	}
	if len(turn.bridge.startReqs) != 1 {
		t.Fatalf("TTS start requests = %d, want 1", len(turn.bridge.startReqs))
	}
	start := turn.bridge.startReqs[0]
	if start.CharacterID != want || start.VoiceID != want {
		t.Fatalf("TTS session identity = character=%q voice=%q, want both %q", start.CharacterID, start.VoiceID, want)
	}
	wantProfile := profileOfAgent(t, want)
	if start.VoiceProfile != wantProfile {
		t.Fatalf("TTS session voice profile = %q, want %q", start.VoiceProfile, wantProfile)
	}
	if len(turn.bridge.pushes) == 0 {
		t.Fatal("nothing was voiced")
	}
	for i, emotion := range turn.bridge.emotions {
		if emotion == nil || emotion.ReasonTrace.VoiceProfile != wantProfile {
			t.Fatalf("pushed utterance %d emotion voice profile = %+v, want %q", i, emotion, wantProfile)
		}
	}
	if len(turn.bridge.ended) != 1 {
		t.Fatalf("TTS sessions ended = %d, want 1", len(turn.bridge.ended))
	}
}

func newVoiceInvariantOrchestrator(t *testing.T, decision routing.Decision) (*MessageOrchestrator, *mockTTSBridge, *recordingEventListener) {
	t.Helper()
	repo := newMockSessionRepository()
	mio := newStreamingChatAgent("mioの応答です。")
	mio.decision = decision
	shiro := &mockShiroAgent{response: "shiroの実行結果です。"}
	orch := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetShiroChatAgent(newStreamingChatAgent("shiroの応答です。"))
	orch.SetWildAgent(&streamingGenerator{response: "midoriの応答です。"})
	orch.SetHeavyAgent(&streamingGenerator{response: "kuroの応答です。"})
	bridge := &mockTTSBridge{}
	orch.SetTTSBridge(bridge)
	listener := &recordingEventListener{}
	orch.SetEventListener(listener)
	return orch, bridge, listener
}

func runVoicedTurn(t *testing.T, decision routing.Decision, recipient, message string) voicedTurn {
	t.Helper()
	orch, bridge, listener := newVoiceInvariantOrchestrator(t, decision)
	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "voice-invariant",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: message,
		To:          recipient,
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	return voicedTurn{bridge: bridge, listener: listener, responder: firstAgentResponseFrom(listener.events)}
}

func TestProcessMessageVoicesDirectRecipientWithItsOwnVoice(t *testing.T) {
	for _, recipient := range []string{"mio", "shiro", "midori", "kuro"} {
		t.Run(recipient, func(t *testing.T) {
			turn := runVoicedTurn(t, routing.NewDecision(routing.RouteCHAT, 1, "chat"), recipient, "こんにちは")
			assertVoicedByAgent(t, turn, recipient)
		})
	}
}

func TestProcessMessageVoicesRespondingAgentOfEveryRoute(t *testing.T) {
	cases := []struct {
		route routing.Route
		want  string
	}{
		{routing.RoutePLAN, "mio"},
		{routing.RouteRESEARCH, "mio"},
		{routing.RouteWILD, "midori"},
		{routing.RouteANALYZE, "kuro"},
		{routing.RouteOPS, "shiro"},
	}
	for _, tc := range cases {
		t.Run(string(tc.route), func(t *testing.T) {
			turn := runVoicedTurn(t, routing.NewDecision(tc.route, 0.9, "route"), "", "お願いします")
			assertVoicedByAgent(t, turn, tc.want)
		})
	}
}

// 朝刊は Mio 所有の Chat intent で、宛先に関わらず mio（失敗時のみ shiro）が応答する。
// 声は宛先ではなく、実際に agent.response を返した Agent のものになる。
func TestProcessMessageVoicesDailyNewsWithRespondingAgentNotRecipient(t *testing.T) {
	for _, recipient := range []string{"mio", "shiro", "kuro"} {
		t.Run(recipient, func(t *testing.T) {
			orch, bridge, listener := newVoiceInvariantOrchestrator(t, routing.NewDecision(routing.RouteCHAT, 1, "chat"))
			orch.SetDailyNewsBriefReader(domainnews.ReaderFunc(func(ctx context.Context, now time.Time) (domainnews.DailyNewsBrief, error) {
				return domainnews.DailyNewsBrief{
					Date:             domainnews.ExpectedMorningDate(now),
					Source:           domainnews.SourceScheduled,
					Status:           domainnews.StatusReady,
					EnrichmentStatus: domainnews.EnrichmentReady,
					Items:            []domainnews.Item{{ID: "news-1", Title: "朝刊記事", Source: "公式RSS"}},
				}, nil
			}))
			_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
				SessionID:   "voice-news",
				Channel:     "viewer",
				ChatID:      "viewer-user",
				UserMessage: "今朝のニュースを教えて",
				To:          recipient,
			})
			if err != nil {
				t.Fatalf("ProcessMessage: %v", err)
			}
			assertVoicedByAgent(t, voicedTurn{bridge: bridge, listener: listener, responder: firstAgentResponseFrom(listener.events)}, "mio")
		})
	}
}

func TestProcessMessageVoicesShiroWhenMioFailsDailyNews(t *testing.T) {
	orch, bridge, listener := newVoiceInvariantOrchestrator(t, routing.NewDecision(routing.RouteCHAT, 1, "chat"))
	orch.mio = &mockMioAgent{chatFunc: func(context.Context, conversation.TurnInput) (string, error) {
		return "", context.DeadlineExceeded
	}}
	orch.SetDailyNewsBriefReader(domainnews.ReaderFunc(func(ctx context.Context, now time.Time) (domainnews.DailyNewsBrief, error) {
		return domainnews.DailyNewsBrief{
			Date:             domainnews.ExpectedMorningDate(now),
			Source:           domainnews.SourceScheduled,
			Status:           domainnews.StatusReady,
			EnrichmentStatus: domainnews.EnrichmentReady,
			Items:            []domainnews.Item{{ID: "news-1", Title: "朝刊記事", Source: "公式RSS"}},
		}, nil
	}))
	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "voice-news-fallback",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "今朝のニュースを教えて",
		To:          "mio",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	assertVoicedByAgent(t, voicedTurn{bridge: bridge, listener: listener, responder: firstAgentResponseFrom(listener.events)}, "shiro")
}

// message / distributed の両 lifecycle が、同じ route・宛先に同じ Agent の声でセッションを開く。
func TestBothLifecyclesStartSessionWithRespondingAgentVoice(t *testing.T) {
	cases := []struct {
		route     routing.Route
		recipient string
		want      string
	}{
		{routing.RouteCHAT, "mio", "mio"},
		{routing.RouteCHAT, "shiro", "shiro"},
		{routing.RouteCHAT, "midori", "midori"},
		{routing.RouteCHAT, "kuro", "kuro"},
		{routing.RoutePLAN, "", "mio"},
		{routing.RouteRESEARCH, "", "mio"},
		{routing.RouteWILD, "", "midori"},
		{routing.RouteANALYZE, "", "kuro"},
		{routing.RouteOPS, "", "shiro"},
		{routing.RouteCODE, "", "shiro"},
		{routing.RouteCODE1, "", "shiro"},
		{routing.RouteCODE2, "", "shiro"},
		{routing.RouteCODE3, "", "shiro"},
		{routing.RouteCODE4, "", "shiro"},
	}
	for _, tc := range cases {
		t.Run(string(tc.route)+"/"+tc.recipient, func(t *testing.T) {
			req := ProcessMessageRequest{SessionID: "viewer-chat", Channel: "viewer", ChatID: "viewer-user", To: tc.recipient}
			decision := routing.NewDecision(tc.route, 1, "t")

			messageBridge := &mockTTSBridge{}
			if !newMessageTTSLifecycle(messageBridge, nil, func(string, string, string, string, string, string, string, string, string) {}).
				StartSessionForRoute(context.Background(), req, modulecore.NewTaskID(), decision, "tts-1") {
				t.Fatal("message lifecycle did not start a session")
			}
			distributedBridge := &mockTTSBridge{}
			if id := newDistributedTTSLifecycle(distributedBridge, nil, func(string, string, string, string, string, string, string, string, string) {}).
				StartSessionForRoute(context.Background(), req, modulecore.NewTaskID(), decision); id == "" {
				t.Fatal("distributed lifecycle did not start a session")
			}
			for name, bridge := range map[string]*mockTTSBridge{"message": messageBridge, "distributed": distributedBridge} {
				if len(bridge.startReqs) != 1 {
					t.Fatalf("%s: start requests = %d, want 1", name, len(bridge.startReqs))
				}
				got := bridge.startReqs[0]
				if got.CharacterID != tc.want || got.VoiceID != tc.want {
					t.Fatalf("%s: session identity = character=%q voice=%q, want %q", name, got.CharacterID, got.VoiceID, tc.want)
				}
			}
		})
	}
}

// 話者が解決できない場合は別 Agent の声で読まず、セッションを開始しない。
func TestLifecyclesRefuseToStartSessionForUnresolvedSpeaker(t *testing.T) {
	req := ProcessMessageRequest{SessionID: "viewer-chat", Channel: "viewer", ChatID: "viewer-user"}
	unknownRoute := routing.NewDecision(routing.Route("UNKNOWN"), 1, "t")

	messageBridge := &mockTTSBridge{}
	if newMessageTTSLifecycle(messageBridge, nil, func(string, string, string, string, string, string, string, string, string) {}).
		StartSessionForRoute(context.Background(), req, modulecore.NewTaskID(), unknownRoute, "tts-1") {
		t.Fatal("message lifecycle started a session for an unresolved speaker")
	}
	distributedBridge := &mockTTSBridge{}
	if id := newDistributedTTSLifecycle(distributedBridge, nil, func(string, string, string, string, string, string, string, string, string) {}).
		StartSessionForRoute(context.Background(), req, modulecore.NewTaskID(), unknownRoute); id != "" {
		t.Fatalf("distributed lifecycle started session %q for an unresolved speaker", id)
	}
	if len(messageBridge.startReqs) != 0 || len(distributedBridge.startReqs) != 0 {
		t.Fatal("a TTS start request was sent for an unresolved speaker")
	}
}

// 話者が解決できない発話は読み上げず、mio の声へも落とさない。
func TestUnresolvedSpeakerIsNotVoiced(t *testing.T) {
	bridge := &mockTTSBridge{}
	emit := func(string, string, string, string, string, string, string, string, string) {}
	newMessageTTSLifecycle(bridge, nil, emit).Push(context.Background(), "tts-1", routing.RouteCHAT, "wild", "agent.response", "読み上げ対象です。")
	newDistributedTTSLifecycle(bridge, nil, emit).Push(context.Background(), "tts-1", routing.RouteCHAT, "", "agent.response", "読み上げ対象です。")
	if forwarder := newTTSStreamForwarder(bridge, "tts-1", routing.RouteCHAT, "male_01", "agent.response", "tts:"); forwarder != nil {
		t.Fatal("stream forwarder was created for a legacy alias speaker")
	}
	if len(bridge.pushes) != 0 {
		t.Fatalf("unresolved speakers were voiced: %v", bridge.pushes)
	}
}

// VTuber の発話者も、読み上げる Agent と同じ話者になる（route からの再導出をしない）。
func TestVTuberRequestFollowsSpeakingAgent(t *testing.T) {
	for _, agent := range []string{"mio", "shiro", "midori", "kuro"} {
		req, ok := buildVTuberRequest("agent.response", routing.RouteCHAT, agent, "sess", "ありがとう、完了しました。", moduletts.EmotionContext{}, profileOfAgent(t, agent))
		if !ok || req.CharacterID != agent {
			t.Fatalf("VTuber character for %q = %q (ok=%v)", agent, req.CharacterID, ok)
		}
	}
}
