package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type mioBoundaryAttestationGuardTestStub struct {
	preparation *conversation.ConversationTurnPreparation
	ownerID     string
	validateErr error
}

func (g *mioBoundaryAttestationGuardTestStub) OwnerID() string     { return g.ownerID }
func (*mioBoundaryAttestationGuardTestStub) SourceVersion() string { return "source-v1" }
func (*mioBoundaryAttestationGuardTestStub) BindingGeneration() conversation.BindingGeneration {
	return conversation.BindingGenerationUnavailable
}
func (g *mioBoundaryAttestationGuardTestStub) MatchesPreparation(preparation *conversation.ConversationTurnPreparation) bool {
	return preparation != nil && g.preparation != nil && preparation.TurnID == g.preparation.TurnID &&
		preparation.SessionID == g.preparation.SessionID && preparation.OwnerID == g.preparation.OwnerID &&
		preparation.Revision == g.preparation.Revision
}
func (g *mioBoundaryAttestationGuardTestStub) Validate() error { return g.validateErr }
func (*mioBoundaryAttestationGuardTestStub) Use(enqueue func() error) error {
	if enqueue == nil {
		return nil
	}
	return enqueue()
}
func (*mioBoundaryAttestationGuardTestStub) RetainPublication() (conversation.PreparedChatTurnPublicationGuard, error) {
	return nil, errors.New("unused")
}
func (*mioBoundaryAttestationGuardTestStub) Release() error { return nil }

func TestChatWithDraftAttestsBoundaryProposalToPreparedTurn(t *testing.T) {
	input := newAgentTurnInput(t, "別件です。旅行の持ち物を教えて", "viewer", "user").WithSessionID(string(modulecore.NewSessionID()))
	preparation := &conversation.ConversationTurnPreparation{
		TurnID: input.TurnID(), SessionID: input.SessionID(), OwnerID: "owner", Revision: 7,
	}
	guard := &preparedChatGuardStub{}
	var gotProposal conversation.ThreadBoundaryProposal
	var gotAttestation bool
	baseEngine := &mockConversationEngine{
		commitTurnFunc: func(ctx context.Context, request conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
			gotProposal, gotAttestation, _ = MioThreadBoundaryProposalFromContext(ctx, request, "owner")
			return conversation.ConversationTurnResult{TurnID: request.TurnID, SessionID: request.SessionID, Status: conversation.ConversationTurnCompleted}, nil
		},
	}
	engine := &preparedChatEngineStub{
		mockConversationEngine: baseEngine,
		prepared: &conversation.PreparedChatTurn{
			RecallPack:  &conversation.RecallPack{},
			Preparation: preparation,
			Guard:       guard,
		},
	}
	classifier := &mioContinuationClassifier{responses: []MioTurnClassification{{
		Decision:   routing.NewDecision(routing.RouteCHAT, 0.95, "new topic"),
		Boundary:   conversation.ThreadBoundaryNew,
		Outcome:    MioTurnOutcome{Kind: MioTurnOutcomeAnswer, Text: "旅行にはこれを持っていくといいよ。"},
		Generation: llm.GenerateResponse{Content: "valid combined answer"},
	}}}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	_, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || draft == nil {
		t.Fatalf("DecideActionWithChatDraft() = draft %v, err %v", draft != nil, err)
	}
	if _, err := mio.ChatWithDraft(context.Background(), input, draft); err != nil {
		t.Fatalf("ChatWithDraft(): %v", err)
	}
	if !gotAttestation || gotProposal != conversation.ThreadBoundaryNew {
		t.Fatalf("commit attestation = (%q, %t), want (%q, true)", gotProposal, gotAttestation, conversation.ThreadBoundaryNew)
	}
}

func TestMioThreadBoundaryAttestationRejectsDifferentTurnBindings(t *testing.T) {
	input := newAgentTurnInput(t, "今日の交通情報を教えて", "viewer", "user").WithSessionID("session-one")
	preparation := &conversation.ConversationTurnPreparation{
		TurnID: input.TurnID(), SessionID: input.SessionID(), OwnerID: "owner", Revision: 7,
	}
	request := conversation.ConversationTurnRequest{
		TurnID: input.TurnID(), TraceID: input.TraceID(), RootTaskID: input.RootTaskID(),
		UserMessageID: input.UserMessageID(), SessionID: input.SessionID(), UserMessage: input.MessageText(),
		AgentSpeaker: conversation.SpeakerMio, Preparation: preparation,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*conversation.ConversationTurnRequest)
	}{
		{name: "turn", mutate: func(request *conversation.ConversationTurnRequest) { request.TurnID = modulecore.NewTurnID() }},
		{name: "trace", mutate: func(request *conversation.ConversationTurnRequest) { request.TraceID = modulecore.NewTraceID() }},
		{name: "root task", mutate: func(request *conversation.ConversationTurnRequest) { request.RootTaskID = modulecore.NewTaskID() }},
		{name: "message id", mutate: func(request *conversation.ConversationTurnRequest) { request.UserMessageID = modulecore.NewMessageID() }},
		{name: "session", mutate: func(request *conversation.ConversationTurnRequest) { request.SessionID = "session-two" }},
		{name: "message hash", mutate: func(request *conversation.ConversationTurnRequest) { request.UserMessage += " changed" }},
		{name: "speaker", mutate: func(request *conversation.ConversationTurnRequest) { request.AgentSpeaker = conversation.SpeakerShiro }},
		{name: "prepared revision", mutate: func(request *conversation.ConversationTurnRequest) {
			changed := *request.Preparation
			changed.Revision++
			request.Preparation = &changed
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := &mioBoundaryAttestationGuardTestStub{preparation: preparation, ownerID: "owner"}
			ctx := withMioThreadBoundaryAttestation(context.Background(), input, preparation, guard, conversation.ThreadBoundaryNew)
			changed := request
			tc.mutate(&changed)
			if proposal, found, err := MioThreadBoundaryProposalFromContext(ctx, changed, "owner"); err == nil || !found || proposal != conversation.ThreadBoundaryUnknown {
				t.Fatalf("changed binding resolved proposal=(%q,%t), err=%v; want rejected stale attestation", proposal, found, err)
			}
		})
	}
}

func TestMioThreadBoundaryAttestationRequiresLiveGuardAndCannotBeCallerSupplied(t *testing.T) {
	input := newAgentTurnInput(t, "今日の交通情報を教えて", "viewer", "user").WithSessionID("session-one")
	preparation := &conversation.ConversationTurnPreparation{
		TurnID: input.TurnID(), SessionID: input.SessionID(), OwnerID: "owner", Revision: 7,
	}
	request := conversation.ConversationTurnRequest{
		TurnID: input.TurnID(), TraceID: input.TraceID(), RootTaskID: input.RootTaskID(),
		UserMessageID: input.UserMessageID(), SessionID: input.SessionID(), UserMessage: input.MessageText(),
		AgentSpeaker: conversation.SpeakerMio, Preparation: preparation, Boundary: true, BoundaryReason: "caller supplied",
	}
	guard := &mioBoundaryAttestationGuardTestStub{preparation: preparation, ownerID: "owner", validateErr: conversation.ErrConversationTurnConflict}
	ctx := withMioThreadBoundaryAttestation(context.Background(), input, preparation, guard, conversation.ThreadBoundaryNew)
	if proposal, found, err := MioThreadBoundaryProposalFromContext(ctx, request, "owner"); err == nil || !found || proposal != conversation.ThreadBoundaryUnknown {
		t.Fatalf("stale guard resolved proposal=(%q,%t), err=%v; want rejection", proposal, found, err)
	}
	if proposal, found, err := MioThreadBoundaryProposalFromContext(context.Background(), request, "owner"); err != nil || found || proposal != conversation.ThreadBoundaryUnknown {
		t.Fatalf("caller-only boundary fields resolved proposal=(%q,%t), err=%v; want no attestation", proposal, found, err)
	}
	ownerGuard := &mioBoundaryAttestationGuardTestStub{preparation: preparation, ownerID: "owner"}
	ownerCtx := withMioThreadBoundaryAttestation(context.Background(), input, preparation, ownerGuard, conversation.ThreadBoundaryNew)
	if proposal, found, err := MioThreadBoundaryProposalFromContext(ownerCtx, request, "different-owner"); err == nil || !found || proposal != conversation.ThreadBoundaryUnknown {
		t.Fatalf("different resolved owner resolved proposal=(%q,%t), err=%v; want owner conflict", proposal, found, err)
	}
}
