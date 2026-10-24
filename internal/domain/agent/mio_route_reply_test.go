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
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMioAgent_CanUseCombinedChatDraftIsBoundedToOrdinaryMioText(t *testing.T) {
	engine := &mockConversationEngine{}
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, nil, nil, engine)
	base := newAgentTurnInput(t, "こんにちは。少し話そう", "viewer", "user")
	if !mio.canUseCombinedChatDraft(context.Background(), base) {
		t.Fatal("ordinary Mio text with typed conversation commit should be eligible")
	}
	unsupportedPreparation := &preparationCapabilityConversationEngine{mockConversationEngine: &mockConversationEngine{}, supported: false}
	unsupportedMio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, nil, nil, unsupportedPreparation)
	if unsupportedMio.canUseCombinedChatDraft(context.Background(), base) {
		t.Fatal("engine whose manager cannot prepare a conversation head must stay on the existing route/chat flow")
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

type preparationCapabilityConversationEngine struct {
	*mockConversationEngine
	supported bool
}

func (e *preparationCapabilityConversationEngine) SupportsConversationTurnPreparation() bool {
	return e.supported
}

type routeReplyClassifierStub struct {
	classifyCalls   int
	combinedCalls   int
	outcomeCalls    int
	decision        routing.Decision
	combinedErr     error
	outcomeErr      error
	outcome         MioTurnOutcome
	fixedRouteCalls []*routing.Route
	onCombined      func()
}

func (c *routeReplyClassifierStub) Classify(context.Context, conversation.TurnInput) (routing.Decision, error) {
	c.classifyCalls++
	return c.decision, nil
}

func (c *routeReplyClassifierStub) ClassifyWithChatReply(context.Context, llm.GenerateRequest) (RouteReplyClassification, error) {
	c.combinedCalls++
	if c.onCombined != nil {
		c.onCombined()
	}
	return RouteReplyClassification{Decision: c.decision, Reply: "unused", ReplyValid: true}, c.combinedErr
}

func (c *routeReplyClassifierStub) ClassifyWithMioTurnOutcome(_ context.Context, _ llm.GenerateRequest, fixedRoute *routing.Route) (MioTurnClassification, error) {
	c.outcomeCalls++
	c.fixedRouteCalls = append(c.fixedRouteCalls, fixedRoute)
	if c.onCombined != nil {
		c.onCombined()
	}
	outcome := c.outcome
	if outcome.Kind == "" {
		outcome = MioTurnOutcome{Kind: MioTurnOutcomeAnswer, Text: "unused"}
	}
	return MioTurnClassification{Decision: c.decision, Outcome: outcome, Generation: llm.GenerateResponse{Content: "typed outcome"}}, c.outcomeErr
}

func TestMioAgentCombinedDraftUsesMioTurnOutcomeClassifier(t *testing.T) {
	engine := &mockConversationEngine{}
	classifier := &routeReplyClassifierStub{
		decision: routing.NewDecision(routing.RouteCHAT, 0.95, "chat"),
		outcome:  MioTurnOutcome{Kind: MioTurnOutcomeAnswer, Text: "typed direct answer"},
	}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	input := newAgentTurnInput(t, "ordinary chat", "viewer", "user")

	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil {
		t.Fatalf("DecideActionWithChatDraft() error = %v", err)
	}
	if classifier.outcomeCalls != 1 || classifier.combinedCalls != 0 || classifier.classifyCalls != 0 {
		t.Fatalf("classifier calls = v2:%d v1-combined:%d route-only:%d, want v2 only", classifier.outcomeCalls, classifier.combinedCalls, classifier.classifyCalls)
	}
	if len(classifier.fixedRouteCalls) != 1 || classifier.fixedRouteCalls[0] != nil {
		t.Fatalf("fixed route calls = %#v, want one initial classification with no fixed route", classifier.fixedRouteCalls)
	}
	if decision.Route != routing.RouteCHAT || draft == nil {
		t.Fatalf("decision=%+v draft=%#v, want CHAT with an adoptable draft", decision, draft)
	}
	answer, err := mio.ChatWithDraft(context.Background(), input, draft)
	if err != nil || answer != "typed direct answer" {
		t.Fatalf("ChatWithDraft() = %q, %v; want typed answer", answer, err)
	}
}

func TestMioAgentCombinedDraftReturnsRetrieveOnlyForOrchestrator(t *testing.T) {
	engine := &mockConversationEngine{}
	classifier := &routeReplyClassifierStub{
		decision: routing.NewDecision(routing.RouteCHAT, 0.95, "chat needs recall"),
		outcome: MioTurnOutcome{Kind: MioTurnOutcomeRetrieve, Requests: []MioRetrievalRequest{{
			SourceKind:        MioRetrievalSourceUserMemory,
			Query:             "出発時刻",
			References:        []string{"memory-17"},
			InformationNeeded: "確定した時刻",
		}}},
	}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	input := newAgentTurnInput(t, "何時に出発するんだっけ", "viewer", "user")

	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil || draft.Failed() {
		t.Fatalf("decision=%+v draft=%#v err=%v, want valid CHAT retrieve draft", decision, draft, err)
	}
	outcome, ok := draft.Outcome()
	if !ok || outcome.Kind != MioTurnOutcomeRetrieve || len(outcome.Requests) != 1 {
		t.Fatalf("Outcome() = %#v, %v; want one retrieve request", outcome, ok)
	}
	if classifier.outcomeCalls != 1 || classifier.combinedCalls != 0 || classifier.classifyCalls != 0 {
		t.Fatalf("classifier calls = v2:%d v1-combined:%d route-only:%d, want one v2 generation", classifier.outcomeCalls, classifier.combinedCalls, classifier.classifyCalls)
	}

	// The accessor returns an independent value so the orchestrator cannot
	// mutate the turn-local draft before handing the request to its owner.
	outcome.Requests[0].References[0] = "mutated"
	secondRead, ok := draft.Outcome()
	if !ok || secondRead.Requests[0].References[0] != "memory-17" {
		t.Fatalf("second Outcome() = %#v, %v; want immutable draft request", secondRead, ok)
	}

	if answer, err := mio.ChatWithDraft(context.Background(), input, draft); err == nil || answer != "" {
		t.Fatalf("ChatWithDraft() = %q, %v; retrieve must not become published or committed text", answer, err)
	}
	if len(engine.commitRequests) != 0 {
		t.Fatalf("conversation commits=%d, want zero for retrieve", len(engine.commitRequests))
	}
}

func TestMioAgentCombinedNonChatHandoffReturnsNoChatDraft(t *testing.T) {
	releases := 0
	engine := &mockConversationEngine{releaseTurnFunc: func(context.Context, *conversation.ConversationTurnPreparation) error {
		releases++
		return nil
	}}
	classifier := &routeReplyClassifierStub{
		decision: routing.NewDecision(routing.RoutePLAN, 0.95, "plan"),
		outcome:  MioTurnOutcome{Kind: MioTurnOutcomeHandoff},
	}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)

	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), newAgentTurnInput(t, "作業計画を立てて", "viewer", "user"), true)
	if err != nil || decision.Route != routing.RoutePLAN || draft != nil {
		t.Fatalf("decision=%+v draft=%#v err=%v, want non-CHAT handoff without draft", decision, draft, err)
	}
	if classifier.outcomeCalls != 1 || classifier.combinedCalls != 0 || classifier.classifyCalls != 0 {
		t.Fatalf("classifier calls = v2:%d v1-combined:%d route-only:%d, want one v2 generation", classifier.outcomeCalls, classifier.combinedCalls, classifier.classifyCalls)
	}
	if releases != 1 || len(engine.commitRequests) != 0 {
		t.Fatalf("preparation releases=%d commits=%d, want release once and no chat commit", releases, len(engine.commitRequests))
	}
}

func TestMioAgentCombinedInvalidOutcomeIsTerminalWithoutFallbackGeneration(t *testing.T) {
	engine := &mockConversationEngine{}
	classifier := &routeReplyClassifierStub{
		decision: routing.NewDecision(routing.RouteCHAT, 0.95, "chat"),
		outcome:  MioTurnOutcome{Kind: MioTurnOutcomeHandoff},
	}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	input := newAgentTurnInput(t, "ordinary chat", "viewer", "user")

	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil || !draft.Failed() {
		t.Fatalf("decision=%+v draft=%#v err=%v, want terminal failed draft", decision, draft, err)
	}
	if classifier.outcomeCalls != 1 || classifier.combinedCalls != 0 || classifier.classifyCalls != 0 {
		t.Fatalf("classifier calls = v2:%d v1-combined:%d route-only:%d, want one v2 attempt and no hidden fallback", classifier.outcomeCalls, classifier.combinedCalls, classifier.classifyCalls)
	}
	if answer, err := mio.ChatWithDraft(context.Background(), input, draft); err == nil || answer != "" {
		t.Fatalf("ChatWithDraft() = %q, %v; invalid outcome must fail closed", answer, err)
	}
	if len(engine.commitRequests) != 0 {
		t.Fatalf("conversation commits=%d, want zero after invalid outcome", len(engine.commitRequests))
	}
}

func TestMioAgentPreparesConversationHeadBeforeCombinedGenerationAndPassesLeaseToCommit(t *testing.T) {
	var order []string
	engine := &mockConversationEngine{
		prepareTurnFunc: func(_ context.Context, turnID, sessionID string) (*conversation.ConversationTurnPreparation, error) {
			order = append(order, "prepare")
			return &conversation.ConversationTurnPreparation{
				TurnID: modulecore.TurnID(turnID), SessionID: sessionID, OwnerID: "owner", Revision: 7,
			}, nil
		},
	}
	classifier := &routeReplyClassifierStub{
		decision:   routing.NewDecision(routing.RouteCHAT, 0.95, "chat"),
		onCombined: func() { order = append(order, "generate") },
	}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	input := newAgentTurnInput(t, "ordinary chat", "viewer", "user").WithSessionID("session-one")
	recorder := turntiming.New(time.Now())
	ctx := turntiming.WithRecorder(context.Background(), recorder)

	decision, draft, err := mio.DecideActionWithChatDraft(ctx, input, true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil || draft.preparation == nil {
		t.Fatalf("decision=%+v draft=%#v err=%v, want prepared CHAT draft", decision, draft, err)
	}
	if len(order) != 2 || order[0] != "prepare" || order[1] != "generate" {
		t.Fatalf("order=%v, want preparation before provider generation", order)
	}
	if _, err := mio.ChatWithDraft(ctx, input, draft); err != nil {
		t.Fatalf("ChatWithDraft() error=%v", err)
	}
	if len(engine.commitRequests) != 1 || engine.commitRequests[0].Preparation == nil || engine.commitRequests[0].Preparation.Revision != 7 {
		t.Fatalf("commit preparation=%+v requests=%d, want the prepared head lease", engine.commitRequests, len(engine.commitRequests))
	}
	points := recorder.Snapshot().PointsMS
	for _, point := range []turntiming.Point{turntiming.PointConversationHeadPreparationStarted, turntiming.PointConversationHeadPreparationEnded} {
		if _, ok := points[string(point)]; !ok {
			t.Errorf("missing timing point %q: %#v", point, points)
		}
	}
}

func TestMioAgentDoesNotGenerateWhenConversationHeadPreparationFails(t *testing.T) {
	classifier := &routeReplyClassifierStub{decision: routing.NewDecision(routing.RouteCHAT, 0.95, "chat")}
	engine := &mockConversationEngine{prepareTurnFunc: func(context.Context, string, string) (*conversation.ConversationTurnPreparation, error) {
		return nil, conversation.ErrConversationTurnUnavailable
	}}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), newAgentTurnInput(t, "ordinary chat", "viewer", "user"), true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil || !draft.Failed() {
		t.Fatalf("decision=%+v draft=%#v err=%v, want failed closed CHAT draft", decision, draft, err)
	}
	if classifier.outcomeCalls != 0 {
		t.Fatalf("provider calls=%d, want zero after head preparation failure", classifier.outcomeCalls)
	}
}

func TestMioAgentReleasesPreparedHeadWhenCombinedRouteIsNotChatOrFails(t *testing.T) {
	for _, tc := range []struct {
		name       string
		decision   routing.Decision
		outcome    MioTurnOutcome
		outcomeErr error
	}{
		{name: "non-chat route", decision: routing.NewDecision(routing.RoutePLAN, 0.95, "plan"), outcome: MioTurnOutcome{Kind: MioTurnOutcomeHandoff}},
		{name: "generation failure", decision: routing.NewDecision(routing.RouteCHAT, 0.95, "chat"), outcomeErr: errors.New("provider unavailable")},
		{name: "generation cancellation", decision: routing.NewDecision(routing.RouteCHAT, 0.95, "chat"), outcomeErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			releases := 0
			engine := &mockConversationEngine{releaseTurnFunc: func(context.Context, *conversation.ConversationTurnPreparation) error {
				releases++
				return nil
			}}
			classifier := &routeReplyClassifierStub{decision: tc.decision, outcome: tc.outcome, outcomeErr: tc.outcomeErr}
			mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
			_, _, err := mio.DecideActionWithChatDraft(context.Background(), newAgentTurnInput(t, "ordinary chat", "viewer", "user"), true)
			if err != nil {
				t.Fatalf("DecideActionWithChatDraft() error=%v", err)
			}
			if releases != 1 {
				t.Fatalf("release calls=%d, want exactly one", releases)
			}
		})
	}
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
	outcome, ok := draft.Outcome()
	if !ok || outcome.Kind != MioTurnOutcomeAnswer || outcome.Text != token {
		t.Fatalf("Outcome() = %#v, %v; want deterministic exact-recall answer", outcome, ok)
	}
	if classifier.classifyCalls != 1 || classifier.combinedCalls != 0 || classifier.outcomeCalls != 0 {
		t.Fatalf("classifier calls = route-only:%d v1-combined:%d v2:%d, want exact-recall route-only once", classifier.classifyCalls, classifier.combinedCalls, classifier.outcomeCalls)
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
		name       string
		outcomeErr error
	}{
		{name: "success"},
		{name: "error", outcomeErr: errors.New("combined classifier unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			classifier := &routeReplyClassifierStub{
				decision:   routing.NewDecision(routing.RouteCHAT, 0.95, "chat"),
				outcomeErr: tc.outcomeErr,
			}
			mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, &mockConversationEngine{})
			recorder := turntiming.New(time.Now())
			ctx := turntiming.WithRecorder(context.Background(), recorder)

			decision, draft, err := mio.DecideActionWithChatDraft(ctx, newAgentTurnInput(t, "普通の会話です", "viewer", "user"), true)
			if err != nil {
				t.Fatalf("DecideActionWithChatDraft() error=%v", err)
			}
			if classifier.classifyCalls != 0 || classifier.combinedCalls != 0 || classifier.outcomeCalls != 1 {
				t.Fatalf("classifier calls = route-only:%d v1-combined:%d v2:%d, want v2 only", classifier.classifyCalls, classifier.combinedCalls, classifier.outcomeCalls)
			}
			if decision.Route != routing.RouteCHAT || draft == nil {
				t.Fatalf("decision=%+v draft=%#v, want CHAT decision and draft", decision, draft)
			}
			if tc.outcomeErr != nil && !draft.Failed() {
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
