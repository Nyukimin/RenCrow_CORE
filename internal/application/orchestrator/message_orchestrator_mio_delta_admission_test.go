package orchestrator

import (
	"context"
	"errors"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type mioDeltaAdmissionRepository struct {
	useCalls int
	used     domainconversation.SessionBinding
	err      error
}

type mioDeltaPublicationQualification struct{}

func (mioDeltaPublicationQualification) Use(claim func() error) error { return claim() }
func (q mioDeltaPublicationQualification) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	return q
}
func (mioDeltaPublicationQualification) Release() {}

type mioDeltaReleaseQualification struct {
	guardActive        *bool
	releases           int
	releasedUnderGuard bool
}

func (q *mioDeltaReleaseQualification) Use(claim func() error) error { return claim() }
func (q *mioDeltaReleaseQualification) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	return q
}
func (q *mioDeltaReleaseQualification) Release() {
	q.releases++
	if q.guardActive != nil && *q.guardActive {
		q.releasedUnderGuard = true
	}
}

func (*mioDeltaAdmissionRepository) Save(context.Context, *session.Session) error { return nil }
func (*mioDeltaAdmissionRepository) Load(context.Context, string) (*session.Session, error) {
	return nil, nil
}
func (*mioDeltaAdmissionRepository) Exists(context.Context, string) (bool, error) { return false, nil }
func (*mioDeltaAdmissionRepository) Delete(context.Context, string) error         { return nil }
func (r *mioDeltaAdmissionRepository) UseBinding(_ context.Context, binding domainconversation.SessionBinding, enqueue func() error) error {
	r.useCalls++
	r.used = binding
	if r.err != nil {
		return r.err
	}
	return enqueue()
}

func TestPrivateMioDeltaAdmissionRequiresExactAuthenticatedPrivateBinding(t *testing.T) {
	ownerID := "ren"
	sessionID := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 7, "incarnation-v1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authenticatedOwnerContextForDeltaTest(t, ownerID)
	ctx = domainconversation.WithSessionBinding(ctx, binding)
	repository := &mioDeltaAdmissionRepository{}
	var admitted MioAnswerDelta
	var qualification domainconversation.PreparedChatTurnPublicationGuard
	callbackCalls := 0
	req := ProcessMessageRequest{
		TurnID: string(modulecore.NewTurnID()), TraceID: string(modulecore.NewTraceID()), AgentMessageID: string(modulecore.NewMessageID()), SessionID: string(sessionID),
		Channel: "viewer", ChatID: "cmd:" + ownerID, To: "mio", OperationSource: "RenCrow_CMD",
	}
	req = WithMioAnswerDeltaAdmission(req, func(delta MioAnswerDelta, retained domainconversation.PreparedChatTurnPublicationGuard) (func(bool), error) {
		callbackCalls++
		admitted = delta
		qualification = retained
		return func(bool) {}, nil
	}, nil)
	delta := mioAnswerDelta{
		TurnID: req.TurnID, TraceID: req.TraceID, TaskID: string(modulecore.NewTaskID()), RunID: string(modulecore.NewRunID()),
		SessionID: req.SessionID, MessageID: req.AgentMessageID, Text: "partial",
	}
	orch := &MessageOrchestrator{sessions: newMessageSessionLifecycle(repository)}
	guardUse := domainagent.MioTurnStreamGuardUse{
		OwnerID: ownerID, SourceVersion: "source-v1", BindingGeneration: 7,
		Use: func(enqueue func() error) error { return enqueue() },
		Retain: func() (domainconversation.PreparedChatTurnPublicationGuard, error) {
			return mioDeltaPublicationQualification{}, nil
		},
	}
	finish, err := orch.admitPrivateMioAnswerDelta(ctx, req, delta, guardUse)
	if err != nil {
		t.Fatalf("admitPrivateMioAnswerDelta: %v", err)
	}
	if finish == nil {
		t.Fatal("successful Session binding admission returned no completion ticket")
	}
	finish(true)
	if qualification == nil {
		t.Fatal("successful admission returned no retained final qualification")
	}
	defer qualification.Release()
	publicationCalled := false
	if err := qualification.Use(func() error { publicationCalled = true; return nil }); err != nil {
		t.Fatalf("final revalidation: %v", err)
	}
	if repository.useCalls != 2 || callbackCalls != 1 || !publicationCalled || repository.used != binding || admitted.Text != delta.Text || admitted.SessionID != req.SessionID {
		t.Fatalf("UseBinding calls=%d callback calls=%d publication=%t used=%+v admitted=%+v", repository.useCalls, callbackCalls, publicationCalled, repository.used, admitted)
	}
}

func TestPrivateMioDeltaAdmissionFailsClosedOnOwnerOrBindingMismatch(t *testing.T) {
	ownerID := "ren"
	sessionID := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatal(err)
	}
	privateBinding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 7, "incarnation-v1")
	if err != nil {
		t.Fatal(err)
	}
	wrongAddress, err := domainconversation.NewChannelAddress("viewer", "cmd:other")
	if err != nil {
		t.Fatal(err)
	}
	publicBinding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePublic, "", 7, "incarnation-v1")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		binding domainconversation.SessionBinding
		ownerID string
		session string
		channel string
		to      string
		source  string
	}{
		{name: "public binding", binding: publicBinding, ownerID: ownerID, session: string(sessionID), channel: "viewer", to: "mio", source: "RenCrow_CMD"},
		{name: "different private owner", binding: bindingForDeltaTest(t, sessionID, address, "someone-else"), ownerID: ownerID, session: string(sessionID), channel: "viewer", to: "mio", source: "RenCrow_CMD"},
		{name: "different session", binding: privateBinding, ownerID: ownerID, session: string(modulecore.NewSessionID()), channel: "viewer", to: "mio", source: "RenCrow_CMD"},
		{name: "different address", binding: bindingForDeltaTest(t, sessionID, wrongAddress, ownerID), ownerID: ownerID, session: string(sessionID), channel: "viewer", to: "mio", source: "RenCrow_CMD"},
		{name: "different recipient", binding: privateBinding, ownerID: ownerID, session: string(sessionID), channel: "viewer", to: "shiro", source: "RenCrow_CMD"},
		{name: "different source", binding: privateBinding, ownerID: ownerID, session: string(sessionID), channel: "viewer", to: "mio", source: "RenCrow_CORE_VIEWER"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &mioDeltaAdmissionRepository{}
			callbackCalls := 0
			req := ProcessMessageRequest{
				TurnID: string(modulecore.NewTurnID()), TraceID: string(modulecore.NewTraceID()), AgentMessageID: string(modulecore.NewMessageID()), SessionID: test.session,
				Channel: test.channel, ChatID: "cmd:" + test.ownerID, To: test.to, OperationSource: test.source,
			}
			req = WithMioAnswerDeltaAdmission(req, func(MioAnswerDelta, domainconversation.PreparedChatTurnPublicationGuard) (func(bool), error) {
				callbackCalls++
				return nil, nil
			}, nil)
			ctx := domainconversation.WithSessionBinding(authenticatedOwnerContextForDeltaTest(t, test.ownerID), test.binding)
			delta := mioAnswerDelta{
				TurnID: req.TurnID, TraceID: req.TraceID, TaskID: string(modulecore.NewTaskID()), RunID: string(modulecore.NewRunID()),
				SessionID: req.SessionID, MessageID: req.AgentMessageID, Text: "private",
			}
			orch := &MessageOrchestrator{sessions: newMessageSessionLifecycle(repository)}
			guardUse := domainagent.MioTurnStreamGuardUse{OwnerID: test.ownerID, SourceVersion: "source-v1", BindingGeneration: 7, Use: func(enqueue func() error) error { return enqueue() }}
			if _, err := orch.admitPrivateMioAnswerDelta(ctx, req, delta, guardUse); err == nil {
				t.Fatal("mismatched private binding was accepted")
			}
			if repository.useCalls != 0 || callbackCalls != 0 {
				t.Fatalf("mismatch reached UseBinding/callback: %d/%d", repository.useCalls, callbackCalls)
			}
		})
	}
}

func TestPrivateMioDeltaAdmissionRequiresPreparedGuardGenerationMatch(t *testing.T) {
	ownerID := "ren"
	sessionID := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 7, "incarnation-v1")
	if err != nil {
		t.Fatal(err)
	}
	repository := &mioDeltaAdmissionRepository{}
	callbackCalls := 0
	req := ProcessMessageRequest{
		TurnID: string(modulecore.NewTurnID()), TraceID: string(modulecore.NewTraceID()), AgentMessageID: string(modulecore.NewMessageID()), SessionID: string(sessionID),
		Channel: "viewer", ChatID: "cmd:" + ownerID, To: "mio", OperationSource: "RenCrow_CMD",
	}
	req = WithMioAnswerDeltaAdmission(req, func(MioAnswerDelta, domainconversation.PreparedChatTurnPublicationGuard) (func(bool), error) {
		callbackCalls++
		return nil, nil
	}, nil)
	ctx := domainconversation.WithSessionBinding(authenticatedOwnerContextForDeltaTest(t, ownerID), binding)
	delta := mioAnswerDelta{
		TurnID: req.TurnID, TraceID: req.TraceID, TaskID: string(modulecore.NewTaskID()), RunID: string(modulecore.NewRunID()),
		SessionID: req.SessionID, MessageID: req.AgentMessageID, Text: "private",
	}
	wrongGeneration := domainagent.MioTurnStreamGuardUse{OwnerID: ownerID, SourceVersion: "source-v1", BindingGeneration: 8, Use: func(enqueue func() error) error { return enqueue() }}
	orch := &MessageOrchestrator{sessions: newMessageSessionLifecycle(repository)}
	if _, err := orch.admitPrivateMioAnswerDelta(ctx, req, delta, wrongGeneration); err == nil {
		t.Fatal("prepared guard from a different binding generation was accepted")
	}
	if repository.useCalls != 0 || callbackCalls != 0 {
		t.Fatalf("generation mismatch reached SessionRepo.UseBinding/callback: %d/%d", repository.useCalls, callbackCalls)
	}
}

func TestPrivateMioDeltaAdmissionReleasesRejectedQualificationAfterGuard(t *testing.T) {
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
	ctx := domainconversation.WithSessionBinding(authenticatedOwnerContextForDeltaTest(t, ownerID), binding)
	request := ProcessMessageRequest{
		TurnID: string(modulecore.NewTurnID()), TraceID: string(modulecore.NewTraceID()),
		AgentMessageID: string(modulecore.NewMessageID()), SessionID: string(sessionID),
		Channel: "viewer", ChatID: "cmd:" + ownerID, To: "mio", OperationSource: "RenCrow_CMD",
	}
	delta := mioAnswerDelta{
		TurnID: request.TurnID, TraceID: request.TraceID, TaskID: string(modulecore.NewTaskID()), RunID: string(modulecore.NewRunID()),
		SessionID: request.SessionID, MessageID: request.AgentMessageID, Text: "pending",
	}
	wantErr := errors.New("bounded delta queue is full")
	request = WithMioAnswerDeltaAdmission(request, func(_ MioAnswerDelta, _ domainconversation.PreparedChatTurnPublicationGuard) (func(bool), error) {
		return nil, wantErr
	}, nil)
	repository := &mioDeltaAdmissionRepository{}
	guardActive := false
	retained := &mioDeltaReleaseQualification{guardActive: &guardActive}
	guard := domainagent.MioTurnStreamGuardUse{
		OwnerID: ownerID, SourceVersion: "source-v1", BindingGeneration: 7,
		Use: func(enqueue func() error) error {
			guardActive = true
			err := enqueue()
			guardActive = false
			return err
		},
		Retain: func() (domainconversation.PreparedChatTurnPublicationGuard, error) { return retained, nil },
	}
	orch := &MessageOrchestrator{sessions: newMessageSessionLifecycle(repository)}
	var finish func(bool)
	var callbackErr error
	outerErr := guard.Use(func() error {
		finish, callbackErr = orch.admitPrivateMioAnswerDelta(ctx, request, delta, guard)
		return callbackErr
	})
	if !errors.Is(outerErr, wantErr) || !errors.Is(callbackErr, wantErr) || finish == nil {
		t.Fatalf("outer guard err=%v callback err=%v completion=%t, want bounded queue error and deferred cleanup ticket", outerErr, callbackErr, finish != nil)
	}
	if retained.releases != 0 {
		t.Fatalf("qualification released %d times before Guard.Use returned", retained.releases)
	}
	finish(false)
	if retained.releases != 1 || retained.releasedUnderGuard {
		t.Fatalf("qualification release count=%d releasedUnderGuard=%t, want one release after Guard.Use", retained.releases, retained.releasedUnderGuard)
	}
}

func TestPrivateMioDeltaRevalidationDropsAfterSessionBindingRevocation(t *testing.T) {
	ownerID := "ren"
	sessionID := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 7, "incarnation-v1")
	if err != nil {
		t.Fatal(err)
	}
	repository := &mioDeltaAdmissionRepository{}
	var qualification domainconversation.PreparedChatTurnPublicationGuard
	req := ProcessMessageRequest{
		TurnID: string(modulecore.NewTurnID()), TraceID: string(modulecore.NewTraceID()), AgentMessageID: string(modulecore.NewMessageID()), SessionID: string(sessionID),
		Channel: "viewer", ChatID: "cmd:" + ownerID, To: "mio", OperationSource: "RenCrow_CMD",
	}
	req = WithMioAnswerDeltaAdmission(req, func(_ MioAnswerDelta, retained domainconversation.PreparedChatTurnPublicationGuard) (func(bool), error) {
		qualification = retained
		return func(bool) {}, nil
	}, nil)
	ctx := domainconversation.WithSessionBinding(authenticatedOwnerContextForDeltaTest(t, ownerID), binding)
	delta := mioAnswerDelta{
		TurnID: req.TurnID, TraceID: req.TraceID, TaskID: string(modulecore.NewTaskID()), RunID: string(modulecore.NewRunID()),
		SessionID: req.SessionID, MessageID: req.AgentMessageID, Text: "pending private delta",
	}
	guardUseCalls := 0
	guard := domainagent.MioTurnStreamGuardUse{
		OwnerID: ownerID, SourceVersion: "source-v1", BindingGeneration: 7,
		Use: func(enqueue func() error) error { guardUseCalls++; return enqueue() },
		Retain: func() (domainconversation.PreparedChatTurnPublicationGuard, error) {
			return mioDeltaPublicationQualification{}, nil
		},
	}
	orch := &MessageOrchestrator{sessions: newMessageSessionLifecycle(repository)}
	finish, err := orch.admitPrivateMioAnswerDelta(ctx, req, delta, guard)
	if err != nil || finish == nil || qualification == nil {
		t.Fatalf("initial guarded admission: finish=%t qualification=%t err=%v", finish != nil, qualification != nil, err)
	}
	finish(true)
	revokedErr := errors.New("Session binding was revoked")
	repository.err = revokedErr
	published := false
	if err := qualification.Use(func() error { published = true; return nil }); !errors.Is(err, revokedErr) {
		t.Fatalf("fanout revalidation error=%v, want Session binding revocation", err)
	}
	qualification.Release()
	if published || repository.useCalls != 2 || guardUseCalls != 0 {
		t.Fatalf("revoked fanout published=%t UseBinding calls=%d prepared Guard.Use calls=%d", published, repository.useCalls, guardUseCalls)
	}
}

func bindingForDeltaTest(t *testing.T, sessionID modulecore.SessionID, address domainconversation.ChannelAddress, ownerID string) domainconversation.SessionBinding {
	t.Helper()
	binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 7, "incarnation-v1")
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func authenticatedOwnerContextForDeltaTest(t *testing.T, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		"request-mio-delta", domaintool.ActorKindUser, ownerID, ownerID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
