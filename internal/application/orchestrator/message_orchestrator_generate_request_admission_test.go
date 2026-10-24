package orchestrator

import (
	"context"
	"errors"
	"testing"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type generateAdmissionGuard struct {
	ownerID           string
	sourceVersion     string
	bindingGeneration domainconversation.BindingGeneration
	active            bool
	useCalls          int
	validateErr       error
}

func (g *generateAdmissionGuard) OwnerID() string       { return g.ownerID }
func (g *generateAdmissionGuard) SourceVersion() string { return g.sourceVersion }
func (g *generateAdmissionGuard) BindingGeneration() domainconversation.BindingGeneration {
	return g.bindingGeneration
}
func (*generateAdmissionGuard) MatchesPreparation(*domainconversation.ConversationTurnPreparation) bool {
	return true
}
func (g *generateAdmissionGuard) Validate() error { return g.validateErr }
func (g *generateAdmissionGuard) Use(enqueue func() error) error {
	g.useCalls++
	if g.validateErr != nil {
		return g.validateErr
	}
	g.active = true
	defer func() { g.active = false }()
	return enqueue()
}
func (*generateAdmissionGuard) RetainPublication() (domainconversation.PreparedChatTurnPublicationGuard, error) {
	return nil, errors.New("not used by request admission")
}
func (*generateAdmissionGuard) Release() error { return nil }

type generateAdmissionSessionRepository struct {
	mioDeltaAdmissionRepository
	guard           *generateAdmissionGuard
	expectedBinding domainconversation.SessionBinding
	useBindingCalls int
	callbackCalls   int
	observedBinding domainconversation.SessionBinding
	err             error
}

func (r *generateAdmissionSessionRepository) UseBinding(_ context.Context, binding domainconversation.SessionBinding, claim func() error) error {
	r.useBindingCalls++
	r.observedBinding = binding
	if !r.guard.active {
		return errors.New("Session binding admitted outside prepared-turn guard")
	}
	if binding.SessionID() != r.expectedBinding.SessionID() || binding.OwnerID() != r.expectedBinding.OwnerID() ||
		binding.ChannelAddress() != r.expectedBinding.ChannelAddress() || binding.BindingGeneration() != r.expectedBinding.BindingGeneration() ||
		binding.PublicationIncarnation() != r.expectedBinding.PublicationIncarnation() {
		return errors.New("UseBinding received a different canonical Session binding")
	}
	if r.err != nil {
		return r.err
	}
	r.callbackCalls++
	return claim()
}

func TestPrivateMioGenerateRequestAdmissionComposesPreparedAndCanonicalSessionGuards(t *testing.T) {
	const ownerID = "ren"
	sessionID := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 7, "incarnation-v1")
	if err != nil {
		t.Fatal(err)
	}
	userCtx := authenticatedOwnerMessageContext(t, ownerID)
	ctx, err := domaintool.DeriveAgentToolExecutionScope(userCtx, "mio-generate-admission", "mio", "chat", "chat", false)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
	}
	ctx = domainconversation.WithSessionBinding(ctx, binding)
	guard := &generateAdmissionGuard{ownerID: ownerID, sourceVersion: "source-v1", bindingGeneration: 7}
	repository := &generateAdmissionSessionRepository{guard: guard, expectedBinding: binding}
	request := ProcessMessageRequest{
		TurnID: string(modulecore.NewTurnID()), TraceID: string(modulecore.NewTraceID()),
		RootTaskID: string(modulecore.NewTaskID()), AgentMessageID: string(modulecore.NewMessageID()),
		SessionID: string(sessionID), Channel: "viewer", ChatID: "cmd:" + ownerID,
		To: "mio", OperationSource: "RenCrow_CMD",
	}
	orch := &MessageOrchestrator{sessions: newMessageSessionLifecycle(repository)}
	capability, err := orch.newPrivateMioGenerateRequestAdmission(ctx, request, guard)
	if err != nil {
		t.Fatalf("newPrivateMioGenerateRequestAdmission: %v", err)
	}
	if err := capability.Admit(); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if guard.useCalls != 1 || guard.active || repository.useBindingCalls != 1 || repository.callbackCalls != 1 {
		t.Fatalf("guard uses=%d active=%t UseBinding=%d claim=%d; want one nested short admission with both guards released", guard.useCalls, guard.active, repository.useBindingCalls, repository.callbackCalls)
	}
	if repository.observedBinding.BindingGeneration() != 7 || repository.observedBinding.PublicationIncarnation() != "incarnation-v1" {
		t.Fatalf("UseBinding binding=%+v; want exact prepared Session binding", repository.observedBinding)
	}
	if err := capability.Admit(); !errors.Is(err, domainllm.ErrGenerateRequestAdmissionConsumed) {
		t.Fatalf("second Admit error=%v, want consumed capability", err)
	}
	if repository.useBindingCalls != 1 {
		t.Fatalf("second Admit reached UseBinding %d times, want no replay", repository.useBindingCalls)
	}
}

func TestPrivateMioGenerateRequestAdmissionBlocksHTTPDispatchAfterEitherOwnerRevokes(t *testing.T) {
	tests := []struct {
		name      string
		guardErr  error
		repoErr   error
		wantGuard int
		wantRepo  int
	}{
		{name: "prepared source revoked", guardErr: errors.New("prepared source revoked"), wantGuard: 1},
		{name: "canonical Session binding revoked", repoErr: errors.New("Session binding revoked"), wantGuard: 1, wantRepo: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const ownerID = "ren"
			sessionID := modulecore.NewSessionID()
			address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 7, "incarnation-v1")
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := domaintool.DeriveAgentToolExecutionScope(authenticatedOwnerMessageContext(t, ownerID), "mio-generate-admission", "mio", "chat", "chat", false)
			if err != nil {
				t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
			}
			ctx = domainconversation.WithSessionBinding(ctx, binding)
			guard := &generateAdmissionGuard{ownerID: ownerID, sourceVersion: "source-v1", bindingGeneration: 7, validateErr: test.guardErr}
			repository := &generateAdmissionSessionRepository{guard: guard, expectedBinding: binding, err: test.repoErr}
			request := ProcessMessageRequest{
				TurnID: string(modulecore.NewTurnID()), TraceID: string(modulecore.NewTraceID()),
				RootTaskID: string(modulecore.NewTaskID()), AgentMessageID: string(modulecore.NewMessageID()),
				SessionID: string(sessionID), Channel: "viewer", ChatID: "cmd:" + ownerID,
				To: "mio", OperationSource: "RenCrow_CMD",
			}
			orch := &MessageOrchestrator{sessions: newMessageSessionLifecycle(repository)}
			capability, err := orch.newPrivateMioGenerateRequestAdmission(ctx, request, guard)
			if err != nil {
				t.Fatalf("newPrivateMioGenerateRequestAdmission: %v", err)
			}
			if err := capability.Admit(); err == nil {
				t.Fatal("Admit accepted a revoked owner capability")
			}
			if guard.useCalls != test.wantGuard || repository.useBindingCalls != test.wantRepo || repository.callbackCalls != 0 {
				t.Fatalf("guard uses=%d Session bindings=%d claims=%d; want %d/%d/0", guard.useCalls, repository.useBindingCalls, repository.callbackCalls, test.wantGuard, test.wantRepo)
			}
		})
	}
}
