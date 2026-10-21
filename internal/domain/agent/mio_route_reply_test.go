package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
)

func TestMioAgent_CanUseCombinedChatDraftIsBoundedToOrdinaryMioText(t *testing.T) {
	engine := &mockConversationEngine{}
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, nil, nil, engine)
	base := newAgentTurnInput(t, "こんにちは。少し話そう", "viewer", "user")
	if !mio.canUseCombinedChatDraft(context.Background(), base) {
		t.Fatal("ordinary Mio text with typed conversation commit should be eligible")
	}

	tests := []struct {
		name  string
		input conversation.TurnInput
		setup func(*MioAgent) context.Context
	}{
		{name: "attachment", input: base.WithAttachments([]attachment.Attachment{{Filename: "image.png", Kind: attachment.KindImage}})},
		{name: "selected other recipient", input: base.WithViewerRecipient("shiro")},
		{name: "forced route", input: base.WithForcedRoute(routing.RouteOPS)},
		{name: "slash command", input: newAgentTurnInput(t, "/chat hello", "viewer", "user")},
		{name: "catalog lookup", input: newAgentTurnInput(t, "映画「PERFECT DAYS」の出演者を教えて", "viewer", "user")},
		{name: "data tool lookup", input: newAgentTurnInput(t, "RenCrowにはどんなDBがある？", "viewer", "user")},
		{name: "web search", input: newAgentTurnInput(t, "最新情報をWebで検索して", "viewer", "user")},
		{
			name:  "persona edit",
			input: newAgentTurnInput(t, "ペルソナの口調を変えて", "viewer", "user"),
			setup: func(m *MioAgent) context.Context {
				m.personaEditor = &mockPersonaEditor{}
				return context.Background()
			},
		},
		{
			name:  "SpeechPlan mode",
			input: base,
			setup: func(*MioAgent) context.Context {
				return ContextWithSpeechPlanCallback(context.Background(), func(speech.Result) {})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, nil, nil, &mockConversationEngine{})
			ctx := context.Background()
			if tt.setup != nil {
				ctx = tt.setup(agent)
			}
			if agent.canUseCombinedChatDraft(ctx, tt.input) {
				t.Fatal("special-path input must stay on the existing route/chat flow")
			}
		})
	}

	legacyEngine := &legacyOnlyConversationEngine{delegate: &mockConversationEngine{}}
	legacyMio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, nil, nil, legacyEngine)
	if legacyMio.canUseCombinedChatDraft(context.Background(), base) {
		t.Fatal("conversation engine without typed commit must stay on the existing path")
	}
}

type routeReplyClassifierStub struct {
	classifyCalls int
	combinedCalls int
	decision      routing.Decision
	combinedErr   error
}

func (c *routeReplyClassifierStub) Classify(context.Context, conversation.TurnInput) (routing.Decision, error) {
	c.classifyCalls++
	return c.decision, nil
}

func (c *routeReplyClassifierStub) ClassifyWithChatReply(context.Context, llm.GenerateRequest) (RouteReplyClassification, error) {
	c.combinedCalls++
	return RouteReplyClassification{Decision: c.decision, Reply: "unused", ReplyValid: true}, c.combinedErr
}

func TestMioAgent_DecideActionWithChatDraftKeepsDeterministicPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		wantRoute routing.Route
		rule      bool
	}{
		{name: "explicit command", message: "/code2 implement it", wantRoute: routing.RouteCODE2},
		{name: "rule dictionary", message: "ファイルを作成", wantRoute: routing.RouteCODE, rule: true},
		{name: "indexed person catalog", message: "東野圭吾の小説を教えて", wantRoute: routing.RouteCHAT},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classifier := &routeReplyClassifierStub{decision: routing.NewDecision(routing.RouteWILD, 0.99, "should not run")}
			rules := &mockRuleDictionary{}
			if tt.rule {
				rules.matchFunc = func(conversation.TurnInput) (routing.Route, float64, bool) {
					return routing.RouteCODE, 0.95, true
				}
			}
			mio := NewMioAgent(&mockLLMProvider{}, classifier, rules, nil, nil, &mockConversationEngine{})
			decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), newAgentTurnInput(t, tt.message, "viewer", "user"), true)
			if err != nil {
				t.Fatalf("DecideActionWithChatDraft() error = %v", err)
			}
			if decision.Route != tt.wantRoute || draft != nil {
				t.Fatalf("decision=%+v draft=%#v, want route %s and no draft", decision, draft, tt.wantRoute)
			}
			if classifier.classifyCalls != 0 || classifier.combinedCalls != 0 {
				t.Fatalf("classifier calls = legacy:%d combined:%d, want 0", classifier.classifyCalls, classifier.combinedCalls)
			}
		})
	}
}

func TestMioAgentExactRecallDraftTimesLegacyClassifier(t *testing.T) {
	const token = "RC_CTX_20260803_1328_L1S4"
	engine := &mockConversationEngine{
		beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
			return &conversation.RecallPack{ShortContext: []conversation.Message{{
				Speaker: conversation.SpeakerMidori,
				Msg:     token,
			}}}, nil
		},
	}
	classifier := &routeReplyClassifierStub{
		decision: routing.NewDecision(routing.RouteCHAT, 0.95, "chat"),
	}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	recorder := turntiming.New(time.Now())
	ctx := turntiming.WithRecorder(context.Background(), recorder)

	decision, draft, err := mio.DecideActionWithChatDraft(ctx, newAgentTurnInput(t, "合言葉を英数字だけでそのまま教えて", "viewer", "user"), true)
	if err != nil {
		t.Fatalf("DecideActionWithChatDraft() error = %v", err)
	}
	if decision.Route != routing.RouteCHAT || draft == nil || !draft.ready {
		t.Fatalf("decision=%+v draft=%#v, want ready exact-recall CHAT draft", decision, draft)
	}
	if classifier.classifyCalls != 1 || classifier.combinedCalls != 0 {
		t.Fatalf("classifier calls = legacy:%d combined:%d, want exactly one legacy call", classifier.classifyCalls, classifier.combinedCalls)
	}
	points := recorder.Snapshot().PointsMS
	for _, point := range []turntiming.Point{turntiming.PointClassificationStarted, turntiming.PointClassificationEnded} {
		if _, ok := points[string(point)]; !ok {
			t.Errorf("exact-recall classifier did not record %q: %#v", point, points)
		}
	}
}

func TestMioAgentCombinedClassifierRecordsClassificationBoundariesIncludingErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		combinedErr error
	}{
		{name: "success"},
		{name: "error", combinedErr: errors.New("combined classifier unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			classifier := &routeReplyClassifierStub{
				decision:    routing.NewDecision(routing.RouteCHAT, 0.95, "chat"),
				combinedErr: tc.combinedErr,
			}
			mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, &mockConversationEngine{})
			recorder := turntiming.New(time.Now())
			ctx := turntiming.WithRecorder(context.Background(), recorder)

			decision, draft, err := mio.DecideActionWithChatDraft(ctx, newAgentTurnInput(t, "普通の会話です", "viewer", "user"), true)
			if err != nil {
				t.Fatalf("DecideActionWithChatDraft() error=%v", err)
			}
			if classifier.classifyCalls != 0 || classifier.combinedCalls != 1 {
				t.Fatalf("classifier calls = legacy:%d combined:%d, want combined only", classifier.classifyCalls, classifier.combinedCalls)
			}
			if decision.Route != routing.RouteCHAT || draft == nil {
				t.Fatalf("decision=%+v draft=%#v, want CHAT decision and draft", decision, draft)
			}
			if tc.combinedErr != nil && !draft.Failed() {
				t.Fatal("failed combined classifier did not leave a failed draft")
			}

			points := recorder.Snapshot().PointsMS
			ordered := []turntiming.Point{
				turntiming.PointClassificationStarted,
				turntiming.PointGenerationProviderStart,
				turntiming.PointGenerationProviderEnd,
				turntiming.PointClassificationEnded,
			}
			var previous float64
			for index, point := range ordered {
				offset, ok := points[string(point)]
				if !ok {
					t.Fatalf("combined classifier did not record %q: %#v", point, points)
				}
				if index > 0 && offset < previous {
					t.Fatalf("point %q offset=%v precedes prior=%v: %#v", point, offset, previous, points)
				}
				previous = offset
			}
		})
	}
}

func TestMioAgent_ChatWithDraftRejectsDifferentTurnOrInput(t *testing.T) {
	classifier := &routeReplyClassifierStub{decision: routing.NewDecision(routing.RouteCHAT, 0.95, "chat")}
	engine := &mockConversationEngine{}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	input := newAgentTurnInput(t, "こんにちは", "viewer", "user").WithSessionID("session-one")
	_, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil {
		t.Fatalf("DecideActionWithChatDraft() error = %v", err)
	}
	if draft == nil {
		t.Fatal("DecideActionWithChatDraft() returned no CHAT draft")
	}

	tests := []struct {
		name  string
		input conversation.TurnInput
	}{
		{name: "different turn", input: newAgentTurnInput(t, "こんにちは", "viewer", "user").WithSessionID("session-one")},
		{name: "different session", input: input.WithSessionID("session-two")},
		{name: "changed message", input: input.WithMessageText("別のメッセージ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := mio.ChatWithDraft(context.Background(), tt.input, draft); err == nil {
				t.Fatal("ChatWithDraft() error = nil, want turn binding rejection")
			}
			if len(engine.commitRequests) != 0 {
				t.Fatalf("conversation commits = %d, want no commit for mismatched draft", len(engine.commitRequests))
			}
		})
	}
}
