package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type mioContinuationClassifier struct {
	responses   []MioTurnClassification
	requests    []llm.GenerateRequest
	fixedRoutes []*routing.Route
}

func (c *mioContinuationClassifier) Classify(context.Context, conversation.TurnInput) (routing.Decision, error) {
	return routing.NewDecision(routing.RouteCHAT, 0.95, "chat"), nil
}

func (c *mioContinuationClassifier) ClassifyWithMioTurnOutcome(_ context.Context, req llm.GenerateRequest, fixedRoute *routing.Route) (MioTurnClassification, error) {
	c.requests = append(c.requests, req)
	if fixedRoute == nil {
		c.fixedRoutes = append(c.fixedRoutes, nil)
	} else {
		routeCopy := *fixedRoute
		c.fixedRoutes = append(c.fixedRoutes, &routeCopy)
	}
	index := len(c.requests) - 1
	return c.responses[index], nil
}

func TestMioAgentContinueChatDraftRetainsPromptAndPreparedTurn(t *testing.T) {
	beginTurnCalls := 0
	legacyPrepareCalls := 0
	input := newAgentTurnInput(t, "何時に出発するんだっけ？", "viewer", "user").WithSessionID(string(modulecore.NewSessionID()))
	baseEngine := &mockConversationEngine{
		beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
			beginTurnCalls++
			return &conversation.RecallPack{RollingSummary: "known conversation context"}, nil
		},
		prepareTurnFunc: func(_ context.Context, turnID, sessionID string) (*conversation.ConversationTurnPreparation, error) {
			legacyPrepareCalls++
			return &conversation.ConversationTurnPreparation{TurnID: modulecore.TurnID(turnID), SessionID: sessionID, OwnerID: "test-owner"}, nil
		},
	}
	lastUserAt := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	lastAgentAt := lastUserAt.Add(time.Minute)
	preparation := &conversation.ConversationTurnPreparation{
		TurnID: input.TurnID(), SessionID: input.SessionID(), OwnerID: "owner", Revision: 4,
		ActiveThread: &conversation.Thread{
			SessionID: input.SessionID(), Domain: "general", Status: conversation.ThreadActive,
			StartTime: lastUserAt,
			Turns: []conversation.Message{
				{Speaker: conversation.SpeakerUser, Msg: "相談があります", Timestamp: lastUserAt},
				{Speaker: conversation.SpeakerMio, Msg: "どうしたの？", Timestamp: lastAgentAt},
			},
		},
	}
	guard := &preparedChatGuardStub{}
	engine := &preparedChatEngineStub{
		mockConversationEngine: baseEngine,
		prepared: &conversation.PreparedChatTurn{
			RecallPack:  &conversation.RecallPack{RollingSummary: "known conversation context"},
			Preparation: preparation,
			Guard:       guard,
		},
	}
	classifier := &mioContinuationClassifier{responses: []MioTurnClassification{
		{
			Decision: routing.NewDecision(routing.RouteCHAT, 0.95, "needs detail"),
			Boundary: conversation.ThreadBoundaryNew,
			Outcome: MioTurnOutcome{Kind: MioTurnOutcomeRetrieve, Requests: []MioRetrievalRequest{{
				SourceKind: MioRetrievalSourceUserMemory, Query: "出発時刻", InformationNeeded: "確定した時刻",
			}}},
			Generation: llm.GenerateResponse{Content: "initial control outcome"},
		},
		{
			Decision:   routing.NewDecision(routing.RouteCHAT, 0.95, "answer from evidence"),
			Boundary:   conversation.ThreadBoundaryKeep,
			Outcome:    MioTurnOutcome{Kind: MioTurnOutcomeAnswer, Text: "10時に出発する予定だよ。"},
			Generation: llm.GenerateResponse{Content: "final control outcome"},
		},
	}}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil {
		t.Fatalf("DecideActionWithChatDraft = route %s draft=%v err=%v", decision.Route, draft != nil, err)
	}
	if draft.boundary != conversation.ThreadBoundaryNew {
		t.Fatalf("initial boundary proposal = %q, want fixed new proposal", draft.boundary)
	}
	if len(classifier.requests) != 1 {
		t.Fatalf("initial provider calls = %d, want one combined generation", len(classifier.requests))
	}
	initialPrompt := classifier.requests[0].Messages
	joinedInitialPrompt := make([]string, 0, len(initialPrompt))
	for _, message := range initialPrompt {
		joinedInitialPrompt = append(joinedInitialPrompt, message.Content)
	}
	initialPromptText := strings.Join(joinedInitialPrompt, "\n")
	for _, expected := range []string{
		"current_thread_exists: true",
		"last_activity_at_utc: 2026-10-25T03:01:00Z",
		"last_chat_speakers: user,mio",
		"continuity_state: prior_agent_reply_available",
	} {
		if !strings.Contains(initialPromptText, expected) {
			t.Fatalf("initial combined prompt misses prepared thread hint %q: %s", expected, initialPromptText)
		}
	}
	if beginTurnCalls != 0 || legacyPrepareCalls != 0 || engine.prepareCalls != 1 {
		t.Fatalf("initial preparation calls BeginTurn=%d legacy Prepare=%d ready Prepare=%d, want ready path once", beginTurnCalls, legacyPrepareCalls, engine.prepareCalls)
	}
	initialOutcome, ok := draft.Outcome()
	if !ok || initialOutcome.Kind != MioTurnOutcomeRetrieve {
		t.Fatalf("initial draft outcome = %#v, %v; want retrieve", initialOutcome, ok)
	}

	outcome, err := mio.ContinueChatDraft(context.Background(), input, draft, []MioRetrievalResult{
		validMioUserMemoryResultForTest(input, "owner", "source-v1", "確定した出発時刻は10時。"),
	})
	if err != nil || outcome.Kind != MioTurnOutcomeAnswer || outcome.Text != "10時に出発する予定だよ。" {
		t.Fatalf("ContinueChatDraft = %#v, %v; want final answer", outcome, err)
	}
	if draft.boundary != conversation.ThreadBoundaryNew {
		t.Fatalf("continuation overwrote initial boundary proposal with %q", draft.boundary)
	}
	if len(classifier.requests) != 2 {
		t.Fatalf("classifier calls = %d, want initial plus one continuation", len(classifier.requests))
	}
	if len(classifier.fixedRoutes) != 2 || classifier.fixedRoutes[0] != nil || classifier.fixedRoutes[1] == nil || *classifier.fixedRoutes[1] != routing.RouteCHAT {
		t.Fatalf("fixed routes = %#v, want initial unpinned then fixed CHAT", classifier.fixedRoutes)
	}
	continuationMessages := classifier.requests[1].Messages
	joined := make([]string, 0, len(continuationMessages))
	for _, message := range continuationMessages {
		joined = append(joined, message.Content)
	}
	continuationPrompt := strings.Join(joined, "\n")
	for _, expected := range []string{"何時に出発するんだっけ？", `"kind":"retrieve"`, "出発時刻", "確定した出発時刻は10時"} {
		if !strings.Contains(continuationPrompt, expected) {
			t.Fatalf("continuation prompt does not retain %q: %#v", expected, continuationMessages)
		}
	}
	if beginTurnCalls != 0 || legacyPrepareCalls != 0 || engine.prepareCalls != 1 {
		t.Fatalf("continuation changed turn setup BeginTurn=%d legacy Prepare=%d ready Prepare=%d, want unchanged ready turn", beginTurnCalls, legacyPrepareCalls, engine.prepareCalls)
	}
	if len(engine.commitRequests) != 0 || draft.preparation == nil {
		t.Fatalf("continuation committed=%d and preparation=%#v; want uncommitted retained preparation", len(engine.commitRequests), draft.preparation)
	}

	answer, err := mio.ChatWithDraft(context.Background(), input, draft)
	if err != nil || answer != "10時に出発する予定だよ。" {
		t.Fatalf("ChatWithDraft = %q, %v; want one final adoption", answer, err)
	}
	if len(engine.commitRequests) != 1 || engine.commitRequests[0].TurnID != input.TurnID() || engine.commitRequests[0].SessionID != input.SessionID() {
		t.Fatalf("commit requests = %#v, want one commit for original turn/session", engine.commitRequests)
	}
}

func TestMioAgentContinueChatDraftKeepsInitialUnknownBoundaryProposal(t *testing.T) {
	input := newAgentTurnInput(t, "今日の交通情報を教えて", "viewer", "user").WithSessionID(string(modulecore.NewSessionID()))
	preparation := &conversation.ConversationTurnPreparation{TurnID: input.TurnID(), SessionID: input.SessionID(), OwnerID: "owner"}
	guard := &preparedChatGuardStub{}
	engine := &preparedChatEngineStub{
		mockConversationEngine: &mockConversationEngine{},
		prepared: &conversation.PreparedChatTurn{
			RecallPack: &conversation.RecallPack{}, Preparation: preparation, Guard: guard,
		},
	}
	classifier := &mioContinuationClassifier{responses: []MioTurnClassification{
		{
			Decision: routing.NewDecision(routing.RouteCHAT, 0.95, "needs evidence"),
			Boundary: conversation.ThreadBoundaryUnknown,
			Outcome: MioTurnOutcome{Kind: MioTurnOutcomeRetrieve, Requests: []MioRetrievalRequest{{
				SourceKind: MioRetrievalSourceKnowledgeMemory, Query: "traffic", InformationNeeded: "current traffic information",
			}}},
		},
		{
			Decision: routing.NewDecision(routing.RouteCHAT, 0.95, "answer from evidence"),
			Boundary: conversation.ThreadBoundaryNew,
			Outcome:  MioTurnOutcome{Kind: MioTurnOutcomeAnswer, Text: "交通情報を確認したよ。"},
		},
	}}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	_, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || draft == nil {
		t.Fatalf("initial draft=%v err=%v, want prepared retrieve draft", draft != nil, err)
	}
	if draft.boundary != conversation.ThreadBoundaryUnknown {
		t.Fatalf("initial draft boundary=%q err=%v, want frozen unknown", draft.boundary, err)
	}
	_, err = mio.ContinueChatDraft(context.Background(), input, draft, []MioRetrievalResult{{
		SourceKind: MioRetrievalSourceKnowledgeMemory, Status: MioRetrievalStatusFound,
		Items: []MioRetrievalItem{{Text: "道路は通常どおり通行できます。"}},
	}})
	if err != nil {
		t.Fatalf("ContinueChatDraft: %v", err)
	}
	if draft.boundary != conversation.ThreadBoundaryUnknown {
		t.Fatalf("continuation changed initial unknown proposal to %q", draft.boundary)
	}
}
