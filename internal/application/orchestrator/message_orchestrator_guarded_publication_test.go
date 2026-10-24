package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type guardedMioPublicationTestListener struct {
	event             OrchestratorEvent
	ownerID           string
	admissions        int
	err               error
	postAdmissionFail bool
}

func (l *guardedMioPublicationTestListener) OnEvent(OrchestratorEvent) error { return nil }

func (l *guardedMioPublicationTestListener) PublishGuardedCanonicalEvent(
	ev OrchestratorEvent,
	ownerID string,
	admit func(func() error) error,
	_ domainconversation.PreparedChatTurnPublicationGuard,
) (OrchestratorEvent, error) {
	l.event = ev
	l.ownerID = ownerID
	admissionErr := l.err
	if l.postAdmissionFail {
		admissionErr = nil
	}
	if err := admit(func() error {
		l.admissions++
		return admissionErr
	}); err != nil {
		return OrchestratorEvent{}, err
	}
	if l.postAdmissionFail {
		ev.EventSeq = 7
		return ev, &GuardedCanonicalLiveDeliveryError{AcceptedEvent: ev, Cause: l.err}
	}
	if l.err != nil {
		return OrchestratorEvent{}, l.err
	}
	ev.EventSeq = 1
	return ev, nil
}

type guardedMioPublicationTestRepository struct {
	useCalls int
	binding  domainconversation.SessionBinding
	revoked  bool
	err      error
}

func (*guardedMioPublicationTestRepository) Save(context.Context, *session.Session) error { return nil }
func (*guardedMioPublicationTestRepository) Load(context.Context, string) (*session.Session, error) {
	return nil, nil
}
func (*guardedMioPublicationTestRepository) Exists(context.Context, string) (bool, error) {
	return false, nil
}
func (*guardedMioPublicationTestRepository) Delete(context.Context, string) error { return nil }
func (r *guardedMioPublicationTestRepository) UseBinding(_ context.Context, binding domainconversation.SessionBinding, use func() error) error {
	r.useCalls++
	r.binding = binding
	if r.revoked {
		return errors.New("Session binding revoked")
	}
	if r.err != nil {
		return r.err
	}
	return use()
}

type guardedMioPublicationTestGuard struct {
	mu         sync.Mutex
	ownerID    string
	version    string
	generation domainconversation.BindingGeneration
	active     bool
	revoked    bool
	refs       int
}

func (g *guardedMioPublicationTestGuard) OwnerID() string       { return g.ownerID }
func (g *guardedMioPublicationTestGuard) SourceVersion() string { return g.version }
func (g *guardedMioPublicationTestGuard) BindingGeneration() domainconversation.BindingGeneration {
	return g.generation
}
func (*guardedMioPublicationTestGuard) MatchesPreparation(*domainconversation.ConversationTurnPreparation) bool {
	return true
}
func (g *guardedMioPublicationTestGuard) Validate() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.revoked {
		return errors.New("prepared source revoked")
	}
	return nil
}
func (g *guardedMioPublicationTestGuard) Use(use func() error) error {
	g.mu.Lock()
	if g.revoked {
		g.mu.Unlock()
		return errors.New("prepared source revoked")
	}
	g.active = true
	g.mu.Unlock()
	err := use()
	g.mu.Lock()
	g.active = false
	g.mu.Unlock()
	return err
}
func (g *guardedMioPublicationTestGuard) RetainPublication() (domainconversation.PreparedChatTurnPublicationGuard, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active || g.revoked {
		return nil, errors.New("RetainPublication must run inside a live Guard.Use")
	}
	g.refs++
	return &guardedMioPublicationTestQualification{state: g}, nil
}
func (g *guardedMioPublicationTestGuard) Release() error { return nil }

type guardedMioPublicationTestQualification struct {
	mu       sync.Mutex
	state    *guardedMioPublicationTestGuard
	released bool
}

func (q *guardedMioPublicationTestQualification) Use(claim func() error) error {
	if q == nil || q.state == nil || claim == nil {
		return errors.New("qualification unavailable")
	}
	q.state.mu.Lock()
	revoked := q.state.revoked
	q.state.mu.Unlock()
	if revoked {
		return errors.New("prepared source revoked")
	}
	return claim()
}
func (q *guardedMioPublicationTestQualification) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	if q == nil || q.state == nil {
		return nil
	}
	q.mu.Lock()
	released := q.released
	q.mu.Unlock()
	if released {
		return nil
	}
	q.state.mu.Lock()
	if q.state.revoked {
		q.state.mu.Unlock()
		return nil
	}
	q.state.refs++
	q.state.mu.Unlock()
	return &guardedMioPublicationTestQualification{state: q.state}
}
func (q *guardedMioPublicationTestQualification) Release() {
	if q == nil || q.state == nil {
		return
	}
	q.mu.Lock()
	if q.released {
		q.mu.Unlock()
		return
	}
	q.released = true
	q.mu.Unlock()
	q.state.mu.Lock()
	if q.state.refs > 0 {
		q.state.refs--
	}
	q.state.mu.Unlock()
}

func TestPublishPrivateMioFinalResponseUsesExactIdentityAndRetainsSessionQualifiedTTS(t *testing.T) {
	const ownerID = "ren"
	sessionID := modulecore.NewSessionID()
	turnID, traceID := modulecore.NewTurnID(), modulecore.NewTraceID()
	taskID, runID, messageID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewMessageID()
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 9, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authenticatedOwnerContextForDeltaTest(t, ownerID)
	ctx = domainconversation.WithSessionBinding(ctx, binding)
	repository := &guardedMioPublicationTestRepository{}
	listener := &guardedMioPublicationTestListener{}
	events := newMessageEventPort(listener)
	if err := events.BindConversationSessionBinding(taskID.String(), turnID, traceID, binding); err != nil {
		t.Fatalf("BindConversationSessionBinding: %v", err)
	}
	if err := events.BindExecutionIdentity(taskID, runID, canonicalExecutionActorKind, "mio"); err != nil {
		t.Fatalf("BindExecutionIdentity: %v", err)
	}
	events.BindResponseMessageID(taskID.String(), messageID)
	guard := &guardedMioPublicationTestGuard{ownerID: ownerID, version: "source-v9", generation: 9}
	request := ProcessMessageRequest{
		TurnID: string(turnID), TraceID: string(traceID), AgentMessageID: string(messageID), SessionID: string(sessionID),
		Channel: "viewer", ChatID: "cmd:" + ownerID, To: "mio", OperationSource: "RenCrow_CMD",
	}
	orch := &MessageOrchestrator{events: events, sessions: newMessageSessionLifecycle(repository)}
	qualification, err := orch.publishPrivateMioFinalResponse(ctx, request, "final answer", taskID, runID, guard)
	if err != nil {
		t.Fatalf("publishPrivateMioFinalResponse: %v", err)
	}
	if listener.admissions != 1 || listener.ownerID != ownerID || listener.event.Type != "agent.response" ||
		listener.event.From != "mio" || listener.event.To != "user" || listener.event.Route != "CHAT" ||
		listener.event.Visibility != "private" || listener.event.EventSeq != 0 || listener.event.Content != "final answer" ||
		listener.event.TurnID != turnID || listener.event.TraceID != traceID || listener.event.SessionID != sessionID ||
		listener.event.TaskID != taskID || listener.event.RunID != runID || listener.event.MessageID != messageID ||
		listener.event.ChannelAddress != "cmd:"+ownerID || listener.event.PublicationIncarnation != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("guarded final event or admission identity mismatch: admissions=%d owner=%q event=%+v", listener.admissions, listener.ownerID, listener.event)
	}
	if repository.useCalls != 2 || repository.binding != binding {
		t.Fatalf("final admission UseBinding calls=%d binding=%+v, want two uses of exact binding %+v", repository.useCalls, repository.binding, binding)
	}
	if err := qualification.Use(func() error { return nil }); err != nil {
		t.Fatalf("retained TTS qualification before revocation: %v", err)
	}
	if repository.useCalls != 3 {
		t.Fatalf("TTS claim did not revalidate exact Session binding: UseBinding calls=%d", repository.useCalls)
	}
	repository.revoked = true
	if err := qualification.Use(func() error { t.Fatal("revoked TTS claim reached bridge admission"); return nil }); err == nil {
		t.Fatal("revoked Session binding authorized a pending TTS handoff")
	}
	repository.revoked = false
	guard.mu.Lock()
	guard.revoked = true
	guard.mu.Unlock()
	if err := qualification.Use(func() error { t.Fatal("reset source claim reached bridge admission"); return nil }); err == nil {
		t.Fatal("revoked prepared-turn source authorized a pending TTS handoff after reset")
	}
	if retained := qualification.Retain(); retained != nil {
		retained.Release()
		t.Fatal("revoked source produced a new retained writer ticket")
	}
	qualification.Release()
}

func TestPublishPrivateMioFinalResponseReturnsCanonicalAdmissionFailure(t *testing.T) {
	ownerID := "ren"
	sessionID := modulecore.NewSessionID()
	turnID, traceID := modulecore.NewTurnID(), modulecore.NewTraceID()
	taskID, runID, messageID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewMessageID()
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 9, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	ctx := domainconversation.WithSessionBinding(authenticatedOwnerContextForDeltaTest(t, ownerID), binding)
	wantErr := errors.New("bounded canonical RAM admission failed")
	listener := &guardedMioPublicationTestListener{err: wantErr}
	events := newMessageEventPort(listener)
	if err := events.BindConversationSessionBinding(taskID.String(), turnID, traceID, binding); err != nil {
		t.Fatalf("BindConversationSessionBinding: %v", err)
	}
	if err := events.BindExecutionIdentity(taskID, runID, canonicalExecutionActorKind, "mio"); err != nil {
		t.Fatalf("BindExecutionIdentity: %v", err)
	}
	events.BindResponseMessageID(taskID.String(), messageID)
	repository := &guardedMioPublicationTestRepository{}
	guard := &guardedMioPublicationTestGuard{ownerID: ownerID, version: "source-v9", generation: 9}
	request := ProcessMessageRequest{
		TurnID: string(turnID), TraceID: string(traceID), AgentMessageID: string(messageID), SessionID: string(sessionID),
		Channel: "viewer", ChatID: "cmd:" + ownerID, To: "mio", OperationSource: "RenCrow_CMD",
	}
	orch := &MessageOrchestrator{events: events, sessions: newMessageSessionLifecycle(repository)}
	if qualification, err := orch.publishPrivateMioFinalResponse(ctx, request, "final answer", taskID, runID, guard); !errors.Is(err, wantErr) || qualification != nil {
		if qualification != nil {
			qualification.Release()
		}
		t.Fatalf("publishPrivateMioFinalResponse qualification=%v err=%v, want nil and %v", qualification, err, wantErr)
	}
	if listener.admissions != 1 {
		t.Fatalf("canonical admission callback count=%d, want one", listener.admissions)
	}
}

func TestPublishPrivateMioFinalResponsePreservesAcceptedEventAfterLiveFailure(t *testing.T) {
	const ownerID = "ren"
	sessionID := modulecore.NewSessionID()
	turnID, traceID := modulecore.NewTurnID(), modulecore.NewTraceID()
	taskID, runID, messageID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewMessageID()
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, ownerID, 9, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	ctx := domainconversation.WithSessionBinding(authenticatedOwnerContextForDeltaTest(t, ownerID), binding)
	wantErr := errors.New("EventHub live sequence conflict")
	listener := &guardedMioPublicationTestListener{err: wantErr, postAdmissionFail: true}
	events := newMessageEventPort(listener)
	if err := events.BindConversationSessionBinding(taskID.String(), turnID, traceID, binding); err != nil {
		t.Fatalf("BindConversationSessionBinding: %v", err)
	}
	if err := events.BindExecutionIdentity(taskID, runID, canonicalExecutionActorKind, "mio"); err != nil {
		t.Fatalf("BindExecutionIdentity: %v", err)
	}
	events.BindResponseMessageID(taskID.String(), messageID)
	repository := &guardedMioPublicationTestRepository{}
	guard := &guardedMioPublicationTestGuard{ownerID: ownerID, version: "source-v9", generation: 9}
	request := ProcessMessageRequest{
		TurnID: string(turnID), TraceID: string(traceID), AgentMessageID: string(messageID), SessionID: string(sessionID),
		Channel: "viewer", ChatID: "cmd:" + ownerID, To: "mio", OperationSource: "RenCrow_CMD",
	}
	orch := &MessageOrchestrator{events: events, sessions: newMessageSessionLifecycle(repository)}
	if qualification, err := orch.publishPrivateMioFinalResponse(ctx, request, "final answer", taskID, runID, guard); qualification != nil || err == nil {
		if qualification != nil {
			qualification.Release()
		}
		t.Fatalf("qualification=%v err=%v, want no TTS qualification after post-admission live failure", qualification, err)
	} else {
		var liveErr *GuardedCanonicalLiveDeliveryError
		if !errors.As(err, &liveErr) || !errors.Is(err, ErrGuardedCanonicalLiveDeliveryFailed) ||
			liveErr.AcceptedEvent.EventSeq != 7 || liveErr.AcceptedEvent.EventID != listener.event.EventID {
			t.Fatalf("post-admission error=%+v accepted=%+v, want typed C success with original EventID and positive sequence", err, liveErr)
		}
	}
	if listener.admissions != 1 {
		t.Fatalf("canonical admission callback count=%d, want exactly one with no retry", listener.admissions)
	}
	if repository.useCalls != 2 {
		t.Fatalf("guarded admission UseBinding calls=%d, want exactly two bounded admission checks", repository.useCalls)
	}
	guard.mu.Lock()
	remainingRefs := guard.refs
	guard.mu.Unlock()
	if remainingRefs != 0 {
		t.Fatalf("retained final/TTS qualifications after W failure=%d, want all released", remainingRefs)
	}
}
