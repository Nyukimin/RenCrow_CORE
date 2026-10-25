package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const preparedChatRecallMarker = "prepared-chat-recall-marker"

type preparedChatEngineStub struct {
	*mockConversationEngine
	prepared       *conversation.PreparedChatTurn
	prepareErr     error
	prepareCalls   int
	prepareTurnIDs []string
	prepareSession []string
}

func (e *preparedChatEngineStub) PrepareChatTurn(_ context.Context, turnID, sessionID, userMessage string) (*conversation.PreparedChatTurn, error) {
	e.prepareCalls++
	e.prepareTurnIDs = append(e.prepareTurnIDs, turnID)
	e.prepareSession = append(e.prepareSession, sessionID)
	if e.prepareErr != nil {
		return nil, e.prepareErr
	}
	return e.prepared, nil
}

type preparedChatGuardStub struct {
	validateCalls int
	validateErr   error
	releaseCalls  int
}

func (*preparedChatGuardStub) OwnerID() string       { return "owner" }
func (*preparedChatGuardStub) SourceVersion() string { return "source-v1" }
func (*preparedChatGuardStub) BindingGeneration() conversation.BindingGeneration {
	return conversation.BindingGenerationUnavailable
}
func (*preparedChatGuardStub) MatchesPreparation(preparation *conversation.ConversationTurnPreparation) bool {
	return preparation != nil
}
func (g *preparedChatGuardStub) Validate() error {
	g.validateCalls++
	return g.validateErr
}
func (*preparedChatGuardStub) Use(enqueue func() error) error { return enqueue() }
func (*preparedChatGuardStub) RetainPublication() (conversation.PreparedChatTurnPublicationGuard, error) {
	return preparedChatPublicationGuardStub{}, nil
}
func (g *preparedChatGuardStub) Release() error {
	g.releaseCalls++
	return nil
}

type preparedChatPublicationGuardStub struct{}

func (preparedChatPublicationGuardStub) Use(claim func() error) error { return claim() }
func (preparedChatPublicationGuardStub) Retain() conversation.PreparedChatTurnPublicationGuard {
	return preparedChatPublicationGuardStub{}
}
func (preparedChatPublicationGuardStub) Release() {}

type preparedChatClassifierStub struct {
	classifications []MioTurnClassification
	requests        []llm.GenerateRequest
	classifyCaps    []*llm.GenerateRequestAdmission
	routeOnly       routing.Decision
}

func (c *preparedChatClassifierStub) Classify(ctx context.Context, _ conversation.TurnInput) (routing.Decision, error) {
	c.classifyCaps = append(c.classifyCaps, MioTurnGenerateRequestAdmissionFromContext(ctx))
	return c.routeOnly, nil
}
func (*preparedChatClassifierStub) ClassifyWithChatReply(context.Context, llm.GenerateRequest) (RouteReplyClassification, error) {
	return RouteReplyClassification{}, errors.New("legacy combined classification must not run")
}

func TestMioExactSharedRecallClassifierReceivesPreparedRequestAdmission(t *testing.T) {
	const token = "RC_CTX_20260803_1328_L1S4"
	input := newAgentTurnInput(t, "合言葉を英数字だけでそのまま教えて", "viewer", "user").WithSessionID(string(modulecore.NewSessionID()))
	preparation := &conversation.ConversationTurnPreparation{TurnID: input.TurnID(), SessionID: input.SessionID(), OwnerID: "owner"}
	guard := &preparedChatGuardStub{}
	engine := &preparedChatEngineStub{
		mockConversationEngine: &mockConversationEngine{},
		prepared: &conversation.PreparedChatTurn{
			RecallPack:  &conversation.RecallPack{ShortContext: []conversation.Message{{Speaker: conversation.SpeakerMidori, Msg: token}}},
			Preparation: preparation, Guard: guard,
		},
	}
	classifier := &preparedChatClassifierStub{routeOnly: routing.NewDecision(routing.RouteCHAT, 0.95, "exact shared recall")}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	capability := llm.NewGenerateRequestAdmission(func() error { return nil })
	ctx := ContextWithMioTurnGenerateRequestAdmissionFactory(context.Background(), func(_ context.Context, actual conversation.PreparedChatTurnGuard) (*llm.GenerateRequestAdmission, error) {
		if actual != guard {
			t.Fatalf("admission factory guard=%v, want the prepared turn guard", actual)
		}
		return capability, nil
	})
	decision, draft, err := mio.DecideActionWithChatDraft(ctx, input, true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil {
		t.Fatalf("DecideActionWithChatDraft: route=%s draft=%v err=%v", decision.Route, draft != nil, err)
	}
	if len(classifier.classifyCaps) != 1 || classifier.classifyCaps[0] != capability {
		t.Fatalf("route classifier admission capabilities=%#v, want the exact prepared request capability", classifier.classifyCaps)
	}
	if len(classifier.requests) != 0 {
		t.Fatalf("exact shared recall issued %d combined generations, want route classification only", len(classifier.requests))
	}
	if draft.boundary != conversation.ThreadBoundaryUnknown {
		t.Fatalf("exact shared recall boundary=%q, want unknown", draft.boundary)
	}
	mio.ReleaseChatDraft(ctx, draft)
}
func (c *preparedChatClassifierStub) ClassifyWithMioTurnOutcome(_ context.Context, request llm.GenerateRequest, _ *routing.Route) (MioTurnClassification, error) {
	c.requests = append(c.requests, request)
	index := len(c.requests) - 1
	if index >= len(c.classifications) {
		return MioTurnClassification{}, errors.New("unexpected classifier call")
	}
	return c.classifications[index], nil
}

func TestMioCombinedChatPreparesOneReadyTurnAndReusesItsRecallForContinuation(t *testing.T) {
	guard := &preparedChatGuardStub{}
	preparation := &conversation.ConversationTurnPreparation{
		TurnID: modulecore.TurnID("prepared-turn-1"), SessionID: "prepared-session", OwnerID: "owner", Revision: 4,
	}
	pack := &conversation.RecallPack{
		RollingSummary: preparedChatRecallMarker,
		ShortContext:   []conversation.Message{{Speaker: conversation.SpeakerMio, Msg: "以前決めた時刻は10時。"}},
	}
	legacyBeginCalls := 0
	legacyPrepareCalls := 0
	legacyReleaseCalls := 0
	engine := &preparedChatEngineStub{
		mockConversationEngine: &mockConversationEngine{
			beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
				legacyBeginCalls++
				return nil, errors.New("legacy recall must not run")
			},
			prepareTurnFunc: func(context.Context, string, string) (*conversation.ConversationTurnPreparation, error) {
				legacyPrepareCalls++
				return nil, errors.New("legacy head preparation must not run")
			},
			releaseTurnFunc: func(context.Context, *conversation.ConversationTurnPreparation) error {
				legacyReleaseCalls++
				return nil
			},
		},
		prepared: &conversation.PreparedChatTurn{RecallPack: pack, Preparation: preparation, Guard: guard},
	}
	classifier := &preparedChatClassifierStub{
		routeOnly: routing.NewDecision(routing.RouteCHAT, 0.95, "chat"),
		classifications: []MioTurnClassification{
			{Decision: routing.NewDecision(routing.RouteCHAT, 0.95, "needs recall"), Outcome: MioTurnOutcome{Kind: MioTurnOutcomeRetrieve, Requests: []MioRetrievalRequest{{
				SourceKind: MioRetrievalSourceUserMemory, Query: "出発時刻", InformationNeeded: "確定した時刻",
			}}}},
			{Decision: routing.NewDecision(routing.RouteCHAT, 0.95, "answer from evidence"), Outcome: MioTurnOutcome{Kind: MioTurnOutcomeAnswer, Text: "10時に出発するよ。"}},
		},
	}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	input := newAgentTurnInput(t, "何時に出発するんだっけ？", "viewer", "user").WithSessionID("prepared-session")

	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil {
		t.Fatalf("DecideActionWithChatDraft() = route %s draft=%v err=%v", decision.Route, draft != nil, err)
	}
	if engine.prepareCalls != 1 || legacyBeginCalls != 0 || legacyPrepareCalls != 0 {
		t.Fatalf("turn setup calls ready=%d legacy BeginTurn=%d legacy Prepare=%d; want ready once and no legacy calls", engine.prepareCalls, legacyBeginCalls, legacyPrepareCalls)
	}
	if engine.prepareTurnIDs[0] != string(input.TurnID()) || engine.prepareSession[0] != input.SessionID() {
		t.Fatalf("PrepareChatTurn identity = turn %q session %q, want input turn/session", engine.prepareTurnIDs[0], engine.prepareSession[0])
	}
	if guard.validateCalls == 0 {
		t.Fatal("prepared guard was not validated before the initial prompt")
	}
	if len(classifier.requests) != 1 || !generateRequestContains(classifier.requests[0], preparedChatRecallMarker) {
		t.Fatalf("initial combined request did not use prepared RecallPack: requests=%d", len(classifier.requests))
	}

	// The result carries the same owner/source proof as the prepared guard.
	if _, err := mio.ContinueChatDraft(context.Background(), input, draft, []MioRetrievalResult{
		validMioUserMemoryResultForTest(input, "owner", "source-v1", "確定した出発時刻は10時。"),
	}); err != nil {
		t.Fatalf("ContinueChatDraft() error=%v", err)
	}
	if len(classifier.requests) != 2 || !generateRequestContains(classifier.requests[1], preparedChatRecallMarker) || !generateRequestContains(classifier.requests[1], "確定した出発時刻は10時") {
		t.Fatalf("continuation did not retain the ready RecallPack and owner result: requests=%d", len(classifier.requests))
	}
	if engine.prepareCalls != 1 || legacyBeginCalls != 0 || legacyPrepareCalls != 0 {
		t.Fatalf("continuation changed turn setup calls ready=%d legacy BeginTurn=%d legacy Prepare=%d", engine.prepareCalls, legacyBeginCalls, legacyPrepareCalls)
	}
	if guard.validateCalls < 4 {
		t.Fatalf("prepared guard validations=%d, want checks before initial prompt and retrieval adoption", guard.validateCalls)
	}
	if _, err := mio.ChatWithDraft(context.Background(), input, draft); err != nil {
		t.Fatalf("ChatWithDraft() error=%v", err)
	}
	if len(engine.commitRequests) != 1 || engine.commitRequests[0].Preparation != preparation {
		t.Fatalf("commit requests=%d preparation=%p, want the exact ready preparation %p", len(engine.commitRequests), engine.commitRequests[0].Preparation, preparation)
	}
	if guard.releaseCalls != 0 || legacyReleaseCalls != 0 {
		t.Fatalf("ChatWithDraft released the ready guard/preparation before final publication: guard=%d legacy=%d", guard.releaseCalls, legacyReleaseCalls)
	}
	if transferred := draft.TransferPreparedChatTurnGuard(); transferred != guard {
		t.Fatalf("TransferPreparedChatTurnGuard() = %v, want the ready guard", transferred)
	}
	if transferred := draft.TransferPreparedChatTurnGuard(); transferred != nil {
		t.Fatal("prepared chat guard transferred more than once")
	}
	mio.ReleaseChatDraft(context.Background(), draft)
	if guard.releaseCalls != 0 {
		t.Fatalf("draft released a transferred guard %d times, want dispatcher ownership", guard.releaseCalls)
	}
	if err := guard.Release(); err != nil || guard.releaseCalls != 1 {
		t.Fatalf("dispatcher guard release calls=%d err=%v, want one successful release", guard.releaseCalls, err)
	}
}

func TestMioCombinedChatReadyFailureDoesNotUseLegacyRecallOrGenerate(t *testing.T) {
	readyErr := errors.New("prepared recall unavailable")
	legacyBeginCalls := 0
	legacyPrepareCalls := 0
	engine := &preparedChatEngineStub{
		mockConversationEngine: &mockConversationEngine{
			beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
				legacyBeginCalls++
				return &conversation.RecallPack{}, nil
			},
			prepareTurnFunc: func(context.Context, string, string) (*conversation.ConversationTurnPreparation, error) {
				legacyPrepareCalls++
				return &conversation.ConversationTurnPreparation{}, nil
			},
		},
		prepareErr: readyErr,
	}
	classifier := &preparedChatClassifierStub{routeOnly: routing.NewDecision(routing.RouteCHAT, 0.95, "chat")}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), newAgentTurnInput(t, "普通の会話", "viewer", "user"), true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil || !draft.Failed() {
		t.Fatalf("DecideActionWithChatDraft() = route %s draft=%#v err=%v; want failed closed CHAT draft", decision.Route, draft, err)
	}
	if engine.prepareCalls != 1 || legacyBeginCalls != 0 || legacyPrepareCalls != 0 || len(classifier.requests) != 0 {
		t.Fatalf("ready failure fallback calls: ready=%d BeginTurn=%d Prepare=%d LLM=%d; want 1/0/0/0", engine.prepareCalls, legacyBeginCalls, legacyPrepareCalls, len(classifier.requests))
	}
}

func TestMioCombinedChatReleasesReadyGuardWhenRouteHandsOff(t *testing.T) {
	guard := &preparedChatGuardStub{}
	engine := &preparedChatEngineStub{
		mockConversationEngine: &mockConversationEngine{},
		prepared: &conversation.PreparedChatTurn{
			RecallPack:  &conversation.RecallPack{},
			Preparation: &conversation.ConversationTurnPreparation{TurnID: modulecore.TurnID("prepared-turn-2"), SessionID: "prepared-session", OwnerID: "owner"},
			Guard:       guard,
		},
	}
	classifier := &preparedChatClassifierStub{
		routeOnly:       routing.NewDecision(routing.RouteCHAT, 0.95, "chat"),
		classifications: []MioTurnClassification{{Decision: routing.NewDecision(routing.RoutePLAN, 0.95, "plan"), Outcome: MioTurnOutcome{Kind: MioTurnOutcomeHandoff}}},
	}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), newAgentTurnInput(t, "計画を作って", "viewer", "user").WithSessionID("prepared-session"), true)
	if err != nil || decision.Route != routing.RoutePLAN || draft != nil {
		t.Fatalf("DecideActionWithChatDraft() = route %s draft=%#v err=%v; want non-CHAT handoff", decision.Route, draft, err)
	}
	if guard.releaseCalls != 1 {
		t.Fatalf("ready guard release calls=%d, want exactly one for non-CHAT handoff", guard.releaseCalls)
	}
}

func TestMioCombinedChatExactRecallUsesReadyPack(t *testing.T) {
	const exactToken = "RC_READY_20261024_A9"
	guard := &preparedChatGuardStub{}
	engine := &preparedChatEngineStub{
		mockConversationEngine: &mockConversationEngine{
			beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
				return nil, errors.New("legacy recall must not run")
			},
		},
		prepared: &conversation.PreparedChatTurn{
			RecallPack:  &conversation.RecallPack{ShortContext: []conversation.Message{{Speaker: conversation.SpeakerMidori, Msg: exactToken}}},
			Preparation: &conversation.ConversationTurnPreparation{TurnID: modulecore.TurnID("prepared-turn-exact"), SessionID: "prepared-session", OwnerID: "owner"},
			Guard:       guard,
		},
	}
	classifier := &preparedChatClassifierStub{routeOnly: routing.NewDecision(routing.RouteCHAT, 0.95, "chat")}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	input := newAgentTurnInput(t, "合言葉を英数字だけでそのまま教えて", "viewer", "user").WithSessionID("prepared-session")
	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil {
		t.Fatalf("DecideActionWithChatDraft() = route %s draft=%v err=%v", decision.Route, draft != nil, err)
	}
	outcome, ok := draft.Outcome()
	if !ok || outcome.Kind != MioTurnOutcomeAnswer || outcome.Text != exactToken {
		t.Fatalf("ready RecallPack exact outcome = %#v, %v; want %q", outcome, ok, exactToken)
	}
	if draft.boundary != conversation.ThreadBoundaryUnknown {
		t.Fatalf("ready exact recall boundary=%q, want unknown", draft.boundary)
	}
	if engine.prepareCalls != 1 || len(classifier.classifyCaps) != 1 || len(classifier.requests) != 0 {
		t.Fatalf("ready calls=%d route-only classifier=%d combined generations=%d, want one preparation and route-only exact recall classification", engine.prepareCalls, len(classifier.classifyCaps), len(classifier.requests))
	}
	mio.ReleaseChatDraft(context.Background(), draft)
	if guard.releaseCalls != 1 {
		t.Fatalf("exact recall draft guard release calls=%d, want one", guard.releaseCalls)
	}
}

func TestMioCombinedChatGuardValidationFailureStopsInitialGeneration(t *testing.T) {
	guard := &preparedChatGuardStub{validateErr: errors.New("prepared source is stale")}
	engine := &preparedChatEngineStub{
		mockConversationEngine: &mockConversationEngine{},
		prepared: &conversation.PreparedChatTurn{
			RecallPack:  &conversation.RecallPack{RollingSummary: preparedChatRecallMarker},
			Preparation: &conversation.ConversationTurnPreparation{TurnID: modulecore.TurnID("prepared-turn-stale"), SessionID: "prepared-session", OwnerID: "owner"},
			Guard:       guard,
		},
	}
	classifier := &preparedChatClassifierStub{routeOnly: routing.NewDecision(routing.RouteCHAT, 0.95, "chat")}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), newAgentTurnInput(t, "ordinary chat", "viewer", "user").WithSessionID("prepared-session"), true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil || !draft.Failed() {
		t.Fatalf("DecideActionWithChatDraft() = route %s draft=%#v err=%v; want failed closed draft", decision.Route, draft, err)
	}
	if guard.validateCalls != 1 || guard.releaseCalls != 1 || len(classifier.requests) != 0 {
		t.Fatalf("stale guard calls: validate=%d release=%d generation=%d; want 1/1/0", guard.validateCalls, guard.releaseCalls, len(classifier.requests))
	}
}

func generateRequestContains(request llm.GenerateRequest, expected string) bool {
	for _, message := range request.Messages {
		if strings.Contains(message.Content, expected) {
			return true
		}
	}
	return false
}
