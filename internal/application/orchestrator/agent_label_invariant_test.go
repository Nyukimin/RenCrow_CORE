package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// agent.thinking / agent.start / agent.response の from は、その turn で実際に
// 応答した Agent と一致しなければならない（docs/06_Public_API仕様.md）。
// 以下は route ごとに同じ turn のイベント列を記録し、この不変条件を検証する。

type labelEvent struct {
	Type string
	From string
	To   string
}

type labelRecorder struct {
	events []labelEvent
}

func (r *labelRecorder) emit(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
	r.events = append(r.events, labelEvent{Type: eventType, From: from, To: to})
}

func streamTokens(ctx context.Context, tokens ...string) {
	if callback := llm.StreamCallbackFromContext(ctx); callback != nil {
		for _, token := range tokens {
			callback(token)
		}
	}
}

// streamingGenerator は WildAgent / HeavyAgent として LLM のストリーミングを模す。
type streamingGenerator struct {
	response string
}

func (g *streamingGenerator) Generate(ctx context.Context, _ conversation.TurnInput) (string, error) {
	streamTokens(ctx, "ト", "ークン")
	return g.response, nil
}

func newStreamingChatAgent(response string) *mockMioAgent {
	return &mockMioAgent{chatFunc: func(ctx context.Context, _ conversation.TurnInput) (string, error) {
		streamTokens(ctx, "ト", "ークン")
		return response, nil
	}}
}

// startResponder は agent.start から応答者を導く。宛先が利用者なら from、
// 委譲先 Agent なら to が応答者である。
func startResponder(ev labelEvent) string {
	if ev.To == "" || ev.To == "user" {
		return ev.From
	}
	return ev.To
}

func assertAgentLabelInvariant(t *testing.T, events []labelEvent, want string) {
	t.Helper()
	var thinking, starts, responses int
	firstResponseFrom := ""
	for _, ev := range events {
		switch ev.Type {
		case "agent.thinking":
			thinking++
			if ev.From != want {
				t.Errorf("agent.thinking from = %q, want %q (events=%v)", ev.From, want, events)
			}
		case "agent.start":
			starts++
			if got := startResponder(ev); got != want {
				t.Errorf("agent.start responder = %q, want %q (events=%v)", got, want, events)
			}
		case "agent.response":
			responses++
			if firstResponseFrom == "" {
				firstResponseFrom = ev.From
			}
		}
	}
	if thinking == 0 || starts == 0 || responses == 0 {
		t.Fatalf("incomplete turn: thinking=%d start=%d response=%d events=%v", thinking, starts, responses, events)
	}
	if firstResponseFrom != want {
		t.Errorf("first agent.response from = %q, want %q (events=%v)", firstResponseFrom, want, events)
	}
}

func newLabelMessageDispatcher(rec *labelRecorder) *messageRouteDispatcher {
	lifecycle := newMessageTTSLifecycle(nil, nil, rec.emit)
	dispatcher := newMessageRouteDispatcher(
		newStreamingChatAgent("mio-answer"),
		&mockShiroAgent{},
		nil,
		rec.emit,
		lifecycle.WithStreamHooks,
		func(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {},
	)
	dispatcher.SetShiroChatAgent(newStreamingChatAgent("shiro-answer"))
	dispatcher.SetWildAgent(&streamingGenerator{response: "midori-answer"})
	dispatcher.SetHeavyAgent(&streamingGenerator{response: "kuro-answer"})
	return dispatcher
}

func TestMessageRouteDispatcherAgentLabelMatchesResponder(t *testing.T) {
	cases := []struct {
		name      string
		route     routing.Route
		recipient string
		want      string
	}{
		{"chat mio", routing.RouteCHAT, "mio", "mio"},
		{"chat shiro", routing.RouteCHAT, "shiro", "shiro"},
		{"chat midori", routing.RouteCHAT, "midori", "midori"},
		{"chat kuro", routing.RouteCHAT, "kuro", "kuro"},
		{"chat default recipient", routing.RouteCHAT, "", "mio"},
		{"plan", routing.RoutePLAN, "", "mio"},
		{"research", routing.RouteRESEARCH, "", "mio"},
		{"wild", routing.RouteWILD, "", "midori"},
		{"analyze", routing.RouteANALYZE, "", "kuro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &labelRecorder{}
			dispatcher := newLabelMessageDispatcher(rec)
			input := newOrchestratorTestTurnInput(t, "こんにちは", "line", "U123").WithSessionID("sess-1")
			if tc.recipient != "" {
				input = input.WithViewerRecipient(tc.recipient)
			}
			taskID := modulecore.NewTaskID()
			runID := modulecore.NewRunID()
			var err error
			if tc.route == routing.RouteCHAT {
				// CHAT は自律 coordinator を通らず ExecuteTurnInput が直接実行する。
				_, err = dispatcher.ExecuteTurnInput(context.Background(), input, tc.route, taskID, runID, "")
			} else {
				_, err = dispatcher.ExecuteDirect(context.Background(), input, tc.route, taskID, runID, "")
			}
			if err != nil {
				t.Fatalf("execute %s: %v", tc.route, err)
			}
			assertAgentLabelInvariant(t, rec.events, tc.want)
		})
	}
}

func newLabelDistributedDispatcher(rec *labelRecorder) *distributedRouteDispatcher {
	lifecycle := newDistributedTTSLifecycle(nil, nil, rec.emit)
	dispatcher := newDistributedRouteDispatcher(
		newStreamingChatAgent("mio-answer"),
		session.NewCentralMemory(),
		rec.emit,
		func(from, to, content, route, taskID, sessionID, channel, chatID string) {},
		lifecycle.WithStreamHooks,
		func(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {},
		func(ctx context.Context, in conversation.TurnInput, route routing.Route, taskID modulecore.TaskID) (string, error) {
			return "", nil
		},
		func(route routing.Route) string { return "" },
		func(in conversation.TurnInput, targetAgent string) conversation.TurnInput { return in },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			return domaintransport.Message{}, nil
		},
	)
	dispatcher.SetShiroChatAgent(newStreamingChatAgent("shiro-answer"))
	dispatcher.SetWildAgent(&streamingGenerator{response: "midori-answer"})
	dispatcher.SetHeavyAgent(&streamingGenerator{response: "kuro-answer"})
	return dispatcher
}

func TestDistributedRouteDispatcherAgentLabelMatchesResponder(t *testing.T) {
	cases := []struct {
		name      string
		route     routing.Route
		recipient string
		want      string
	}{
		{"chat mio", routing.RouteCHAT, "mio", "mio"},
		{"chat shiro", routing.RouteCHAT, "shiro", "shiro"},
		{"chat midori", routing.RouteCHAT, "midori", "midori"},
		{"chat kuro", routing.RouteCHAT, "kuro", "kuro"},
		{"chat default recipient", routing.RouteCHAT, "", "mio"},
		{"wild", routing.RouteWILD, "", "midori"},
		{"analyze", routing.RouteANALYZE, "", "kuro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &labelRecorder{}
			dispatcher := newLabelDistributedDispatcher(rec)
			input := newOrchestratorTestTurnInput(t, "こんにちは", "line", "U123").WithSessionID("sess-1")
			if tc.recipient != "" {
				input = input.WithViewerRecipient(tc.recipient)
			}
			if _, err := dispatcher.ExecuteDirect(context.Background(), input, tc.route, modulecore.NewTaskID(), modulecore.NewRunID(), ""); err != nil {
				t.Fatalf("ExecuteDirect: %v", err)
			}
			assertAgentLabelInvariant(t, rec.events, tc.want)
		})
	}
}

// WithStreamHooks は呼び出し側が渡した speaker をそのまま thinking の from にする。
// 固定値や route からの推測へ戻っていないことを両 lifecycle で確認する。
func TestStreamHooksEmitThinkingFromGivenSpeaker(t *testing.T) {
	for _, speaker := range []string{"mio", "shiro", "midori", "kuro"} {
		t.Run(speaker, func(t *testing.T) {
			var got []labelEvent
			emit := func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
				got = append(got, labelEvent{Type: eventType, From: from, To: to})
			}
			hooks := map[string]messageStreamHook{
				"message":     newMessageTTSLifecycle(nil, nil, emit).WithStreamHooks,
				"distributed": newDistributedTTSLifecycle(nil, nil, emit).WithStreamHooks,
			}
			for name, hook := range hooks {
				got = nil
				streamCtx, _ := hook(context.Background(), routing.RouteCHAT, speaker, "tsk_00000000-0000-5000-8000-000000000031", "sess-1", "line", "U123", "")
				llm.StreamCallbackFromContext(streamCtx)("tok")
				if len(got) != 1 || got[0].Type != "agent.thinking" || got[0].From != speaker || got[0].To != "user" {
					t.Fatalf("%s: thinking event = %#v, want from=%s to=user", name, got, speaker)
				}
			}
		})
	}
}
