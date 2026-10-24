package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ownerAwareMessageSessionRepository struct {
	*mockSessionRepository
	ownerResolution      session.CanonicalSessionOwnerResolution
	newOwnerResolution   session.CanonicalSessionOwnerResolution
	canonicalSession     *session.Session
	ownerLoadCalls       int
	ownerCreateCalls     int
	publicCanonicalCalls int
	loadedSessionCalls   int
	requestedSessionID   string
	requestedOwnerID     string
	requestedAddress     domainconversation.ChannelAddress
	forceCanonicalResult bool
}

type bindingGenerationMessageSessionRepository struct {
	*ownerAwareMessageSessionRepository
	generation        domainconversation.BindingGeneration
	observedBinding   domainconversation.SessionBinding
	observedContext   any
	resolverWasProven bool
}

type bindingGenerationContextKey struct{}

func (r *bindingGenerationMessageSessionRepository) BindingGeneration(ctx context.Context, binding domainconversation.SessionBinding) (domainconversation.BindingGeneration, error) {
	r.observedBinding = binding
	r.observedContext = ctx.Value(bindingGenerationContextKey{})
	r.resolverWasProven = r.ownerLoadCalls+r.ownerCreateCalls > 0 || r.publicCanonicalCalls > 0
	return r.generation, nil
}

func newOwnerAwareMessageSessionRepository() *ownerAwareMessageSessionRepository {
	return &ownerAwareMessageSessionRepository{mockSessionRepository: newMockSessionRepository()}
}

func (r *ownerAwareMessageSessionRepository) Load(_ context.Context, id string) (*session.Session, error) {
	r.loadedSessionCalls++
	return r.mockSessionRepository.Load(context.Background(), id)
}

func (r *ownerAwareMessageSessionRepository) LoadCanonicalForOwner(_ context.Context, id, ownerID string, address domainconversation.ChannelAddress) (session.CanonicalSessionOwnerResolution, error) {
	r.ownerLoadCalls++
	r.requestedSessionID = id
	r.requestedOwnerID = ownerID
	r.requestedAddress = address
	return r.ownerResolution, nil
}

func (r *ownerAwareMessageSessionRepository) LoadOrCreateCanonicalForOwner(_ context.Context, _ string, address domainconversation.ChannelAddress, ownerID string, _ time.Time) (session.CanonicalSessionOwnerResolution, error) {
	r.ownerCreateCalls++
	r.requestedOwnerID = ownerID
	r.requestedAddress = address
	return r.newOwnerResolution, nil
}

func (r *ownerAwareMessageSessionRepository) LoadOrCreateCanonical(_ context.Context, _ string, address domainconversation.ChannelAddress, _ time.Time) (*session.Session, bool, error) {
	r.publicCanonicalCalls++
	if r.canonicalSession != nil && (r.forceCanonicalResult || r.canonicalSession.ChannelAddress() == address) {
		return r.canonicalSession, false, nil
	}
	return nil, false, errors.New("unexpected public canonical session lookup")
}

func ownerBoundMessageSession(t *testing.T, ownerID, channel, chatID string) (*session.Session, domainconversation.ChannelAddress) {
	t.Helper()
	address, err := domainconversation.NewChannelAddress(channel, chatID)
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	sess, err := session.NewCanonicalSessionWithOwner(
		modulecore.NewSessionID(), ownerID, "2026-10-24", address,
		time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewCanonicalSessionWithOwner: %v", err)
	}
	return sess, address
}

func authenticatedOwnerMessageContext(t *testing.T, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		"request-owner-binding", domaintool.ActorKindUser, ownerID, ownerID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func TestMessageSessionLifecycleUsesOwnerAwareExplicitSessionResolution(t *testing.T) {
	const ownerID = "authenticated-ren"
	sess, address := ownerBoundMessageSession(t, ownerID, "viewer", "chat-47")
	repo := newOwnerAwareMessageSessionRepository()
	repo.ownerResolution = session.CanonicalSessionOwnerResolution{
		Session: sess, BoundOwnerID: ownerID, Status: session.CanonicalSessionOwnerMatched,
	}
	lifecycle := newMessageSessionLifecycle(repo)
	req := ProcessMessageRequest{SessionID: sess.ID(), Channel: "viewer", ChatID: "chat-47"}

	resolved, normalized, boundOwnerID, err := lifecycle.ResolveForRequestWithOwner(authenticatedOwnerMessageContext(t, ownerID), req, time.Now())
	if err != nil {
		t.Fatalf("ResolveForRequest: %v", err)
	}
	if resolved != sess || normalized.SessionID != sess.ID() || boundOwnerID != ownerID {
		t.Fatalf("resolved session=(%p,%q), bound owner=%q; want (%p,%q), %q", resolved, normalized.SessionID, boundOwnerID, sess, sess.ID(), ownerID)
	}
	if repo.ownerLoadCalls != 1 || repo.loadedSessionCalls != 0 {
		t.Fatalf("owner-aware load calls=%d legacy Load calls=%d, want 1 and 0", repo.ownerLoadCalls, repo.loadedSessionCalls)
	}
	if repo.requestedSessionID != sess.ID() || repo.requestedOwnerID != ownerID || repo.requestedAddress != address {
		t.Fatalf("owner lookup=(%q,%q,%+v), want (%q,%q,%+v)", repo.requestedSessionID, repo.requestedOwnerID, repo.requestedAddress, sess.ID(), ownerID, address)
	}
}

func TestMessageSessionLifecycleUsesOwnerAwareCanonicalCreation(t *testing.T) {
	const ownerID = "authenticated-ren"
	sess, address := ownerBoundMessageSession(t, ownerID, "viewer", "chat-48")
	repo := newOwnerAwareMessageSessionRepository()
	repo.newOwnerResolution = session.CanonicalSessionOwnerResolution{
		Session: sess, BoundOwnerID: ownerID, Status: session.CanonicalSessionOwnerCreated,
	}
	lifecycle := newMessageSessionLifecycle(repo)
	now := time.Date(2026, 10, 24, 12, 5, 0, 0, time.UTC)

	resolved, req, boundOwnerID, err := lifecycle.ResolveForRequestWithOwner(authenticatedOwnerMessageContext(t, ownerID), ProcessMessageRequest{
		Channel: "viewer", ChatID: "chat-48",
	}, now)
	if err != nil {
		t.Fatalf("ResolveForRequest: %v", err)
	}
	if resolved != sess || req.SessionID != sess.ID() || boundOwnerID != ownerID {
		t.Fatalf("resolved session=(%p,%q), bound owner=%q; want (%p,%q), %q", resolved, req.SessionID, boundOwnerID, sess, sess.ID(), ownerID)
	}
	if repo.ownerCreateCalls != 1 || repo.publicCanonicalCalls != 0 {
		t.Fatalf("owner create calls=%d public canonical calls=%d, want 1 and 0", repo.ownerCreateCalls, repo.publicCanonicalCalls)
	}
	if repo.requestedOwnerID != ownerID || repo.requestedAddress != address {
		t.Fatalf("owner lookup=(%q,%+v), want (%q,%+v)", repo.requestedOwnerID, repo.requestedAddress, ownerID, address)
	}
}

func TestMessageSessionLifecycleAcceptsDerivedAgentWithVerifiedHTTPUserProof(t *testing.T) {
	const ownerID = "authenticated-ren"
	sess, address := ownerBoundMessageSession(t, ownerID, "viewer", "chat-derived-agent")
	repo := newOwnerAwareMessageSessionRepository()
	repo.newOwnerResolution = session.CanonicalSessionOwnerResolution{
		Session: sess, BoundOwnerID: ownerID, Status: session.CanonicalSessionOwnerCreated,
	}
	lifecycle := newMessageSessionLifecycle(repo)

	ctx, err := domaintool.DeriveAgentToolExecutionScope(
		authenticatedOwnerMessageContext(t, ownerID), "derived-request", "mio", "chat", "", false,
	)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
	}
	resolved, req, boundOwnerID, err := lifecycle.ResolveForRequestWithOwner(ctx, ProcessMessageRequest{
		Channel: "viewer", ChatID: "chat-derived-agent",
	}, time.Now())
	if err != nil {
		t.Fatalf("ResolveForRequestWithOwner: %v", err)
	}
	if resolved != sess || req.SessionID != sess.ID() || boundOwnerID != ownerID {
		t.Fatalf("resolved session=(%p,%q), bound owner=%q; want (%p,%q), %q", resolved, req.SessionID, boundOwnerID, sess, sess.ID(), ownerID)
	}
	if repo.ownerCreateCalls != 1 || repo.publicCanonicalCalls != 0 || repo.requestedOwnerID != ownerID || repo.requestedAddress != address {
		t.Fatalf("owner create calls=%d public calls=%d owner=%q address=%+v; want one owner-aware create for %q at %+v", repo.ownerCreateCalls, repo.publicCanonicalCalls, repo.requestedOwnerID, repo.requestedAddress, ownerID, address)
	}
}

func TestMessageSessionLifecycleRejectsDerivedAgentWhenHTTPUserProofDoesNotMatch(t *testing.T) {
	const ownerID = "authenticated-ren"
	ctx, err := domaintool.DeriveAgentToolExecutionScope(
		authenticatedOwnerMessageContext(t, ownerID), "derived-request", "mio", "chat", "", false,
	)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
	}
	conflictingScope, err := domaintool.NewToolExecutionScope(
		"conflicting-derived-request", domaintool.ActorKindAgent, "mio", "other-user",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	ctx = domaintool.WithToolExecutionScope(ctx, conflictingScope)
	repo := newOwnerAwareMessageSessionRepository()
	lifecycle := newMessageSessionLifecycle(repo)

	_, _, _, err = lifecycle.ResolveForRequestWithOwner(ctx, ProcessMessageRequest{Channel: "viewer", ChatID: "chat-proof-mismatch"}, time.Now())
	if err == nil {
		t.Fatal("derived Agent scope with a different authenticated user was accepted")
	}
	if repo.ownerCreateCalls != 0 || repo.publicCanonicalCalls != 0 {
		t.Fatalf("owner create calls=%d public calls=%d before proof mismatch rejection", repo.ownerCreateCalls, repo.publicCanonicalCalls)
	}
}

func TestMessageSessionLifecycleRejectsUnavailableOwnerResolutionWithoutLegacyFallback(t *testing.T) {
	sess, _ := ownerBoundMessageSession(t, "ren", "viewer", "chat-49")
	for _, status := range []session.CanonicalSessionOwnerStatus{
		session.CanonicalSessionOwnerUnavailable,
		session.CanonicalSessionOwnerConflict,
		session.CanonicalSessionOwnerNotFound,
	} {
		t.Run(string(status), func(t *testing.T) {
			repo := newOwnerAwareMessageSessionRepository()
			repo.ownerResolution.Status = status
			lifecycle := newMessageSessionLifecycle(repo)
			_, _, _, err := lifecycle.ResolveForRequestWithOwner(authenticatedOwnerMessageContext(t, "ren"), ProcessMessageRequest{
				SessionID: sess.ID(), Channel: "viewer", ChatID: "chat-49",
			}, time.Now())
			if err == nil {
				t.Fatalf("ResolveForRequest accepted owner status %q", status)
			}
			if repo.ownerLoadCalls != 1 || repo.loadedSessionCalls != 0 {
				t.Fatalf("owner-aware calls=%d legacy Load calls=%d, want 1 and 0", repo.ownerLoadCalls, repo.loadedSessionCalls)
			}
		})
	}
}

func TestMessageSessionLifecycleRejectsOwnerResolutionWhoseSessionOwnerDiffers(t *testing.T) {
	const ownerID = "authenticated-ren"
	sess, _ := ownerBoundMessageSession(t, "different-owner", "viewer", "owner-session-owner-mismatch")
	repo := newOwnerAwareMessageSessionRepository()
	repo.ownerResolution = session.CanonicalSessionOwnerResolution{
		Session: sess, BoundOwnerID: ownerID, Status: session.CanonicalSessionOwnerMatched,
	}
	lifecycle := newMessageSessionLifecycle(repo)

	_, _, _, err := lifecycle.ResolveForRequestWithOwner(authenticatedOwnerMessageContext(t, ownerID), ProcessMessageRequest{
		SessionID: sess.ID(), Channel: "viewer", ChatID: "owner-session-owner-mismatch",
	}, time.Now())
	if err == nil {
		t.Fatal("owner resolution accepted a Session whose canonical owner differs from the verified HTTP User")
	}
}

func TestMessageSessionLifecycleFailsClosedWhenOwnerAwareRepositoryIsUnavailable(t *testing.T) {
	legacy := newMockSessionRepository()
	legacy.loadErr = errors.New("legacy Load must not authorize an owner session")
	lifecycle := newMessageSessionLifecycle(legacy)

	_, _, _, err := lifecycle.ResolveForRequestWithOwner(authenticatedOwnerMessageContext(t, "ren"), ProcessMessageRequest{
		SessionID: string(modulecore.NewSessionID()), Channel: "viewer", ChatID: "chat-50",
	}, time.Now())
	if err == nil {
		t.Fatal("owner-scoped explicit session resolved through a repository without owner-aware APIs")
	}
	if errors.Is(err, legacy.loadErr) {
		t.Fatalf("owner-scoped request fell back to legacy Load: %v", err)
	}
}

func TestMessageSessionLifecycleRejectsOwnerResolutionIdentityDrift(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ownerID string
		channel string
		chatID  string
		status  session.CanonicalSessionOwnerStatus
	}{
		{name: "owner", ownerID: "other-owner", channel: "viewer", chatID: "chat-53", status: session.CanonicalSessionOwnerMatched},
		{name: "address", ownerID: "ren", channel: "viewer", chatID: "other-chat", status: session.CanonicalSessionOwnerMatched},
		{name: "explicit id", ownerID: "ren", channel: "viewer", chatID: "chat-53", status: session.CanonicalSessionOwnerMatched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionChannel, sessionChat := tc.channel, tc.chatID
			resolutionOwner := tc.ownerID
			if tc.name == "owner" {
				resolutionOwner = "other-owner"
			} else if tc.name == "address" {
				sessionChat = "different-chat"
			} else {
				sessionChat = "chat-53"
			}
			sess, _ := ownerBoundMessageSession(t, resolutionOwner, sessionChannel, sessionChat)
			repo := newOwnerAwareMessageSessionRepository()
			repo.ownerResolution = session.CanonicalSessionOwnerResolution{
				Session: sess, BoundOwnerID: resolutionOwner, Status: tc.status,
			}
			lifecycle := newMessageSessionLifecycle(repo)
			req := ProcessMessageRequest{SessionID: sess.ID(), Channel: tc.channel, ChatID: tc.chatID}
			if tc.name == "explicit id" {
				req.SessionID = string(modulecore.NewSessionID())
			}
			_, _, _, err := lifecycle.ResolveForRequestWithOwner(authenticatedOwnerMessageContext(t, "ren"), req, time.Now())
			if err == nil {
				t.Fatalf("ResolveForRequest accepted %s identity drift", tc.name)
			}
		})
	}
}

func TestMessageSessionLifecycleRejectsOwnerScopeFromNonUserActor(t *testing.T) {
	scope, err := domaintool.NewToolExecutionScope(
		"agent-owner-scope", domaintool.ActorKindAgent, "mio", "ren",
		[]string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	repo := newOwnerAwareMessageSessionRepository()
	lifecycle := newMessageSessionLifecycle(repo)
	ctx := domaintool.WithToolExecutionScope(context.Background(), scope)

	_, _, _, err = lifecycle.ResolveForRequestWithOwner(ctx, ProcessMessageRequest{
		Channel: "viewer", ChatID: "chat-54",
	}, time.Now())
	if err == nil {
		t.Fatal("agent actor with delegated user scope was accepted as the canonical session owner")
	}
	if repo.ownerCreateCalls != 0 || repo.publicCanonicalCalls != 0 {
		t.Fatalf("owner creates=%d public canonical calls=%d before actor rejection", repo.ownerCreateCalls, repo.publicCanonicalCalls)
	}
}

func TestMessageSessionLifecyclePublicRouteDoesNotClaimOwnerFromChatID(t *testing.T) {
	address, err := domainconversation.NewChannelAddress("viewer", "ren")
	if err != nil {
		t.Fatal(err)
	}
	publicSession, err := session.NewCanonicalSession(
		modulecore.NewSessionID(), "2026-10-24", address, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	repo := newOwnerAwareMessageSessionRepository()
	repo.canonicalSession = publicSession
	lifecycle := newMessageSessionLifecycle(repo)

	resolved, req, boundOwnerID, err := lifecycle.ResolveForRequestWithOwner(context.Background(), ProcessMessageRequest{
		Channel: "viewer", ChatID: "ren",
	}, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ResolveForRequest public route: %v", err)
	}
	if resolved != publicSession || req.SessionID != publicSession.ID() || boundOwnerID != "" {
		t.Fatalf("public resolution session=%p id=%q owner=%q, want %p id=%q owner empty", resolved, req.SessionID, boundOwnerID, publicSession, publicSession.ID())
	}
	if repo.publicCanonicalCalls != 1 || repo.ownerCreateCalls != 0 || repo.ownerLoadCalls != 0 {
		t.Fatalf("public canonical calls=%d owner creates=%d owner loads=%d", repo.publicCanonicalCalls, repo.ownerCreateCalls, repo.ownerLoadCalls)
	}
}

func TestMessageSessionLifecycleRejectsOwnerBoundSessionOnPublicResolution(t *testing.T) {
	t.Run("explicit session id", func(t *testing.T) {
		const ownerID = "authenticated-ren"
		sess, _ := ownerBoundMessageSession(t, ownerID, "viewer", "public-explicit")
		repo := newOwnerAwareMessageSessionRepository()
		repo.sessions[sess.ID()] = sess
		lifecycle := newMessageSessionLifecycle(repo)

		_, _, err := lifecycle.ResolveForRequest(context.Background(), ProcessMessageRequest{
			SessionID: sess.ID(), Channel: "viewer", ChatID: "public-explicit",
		}, time.Now())
		if err == nil {
			t.Fatal("public resolution accepted an owner-bound explicit Session")
		}
		if repo.loadedSessionCalls != 1 || repo.ownerLoadCalls != 0 {
			t.Fatalf("legacy loads=%d owner-aware loads=%d, want 1 and 0", repo.loadedSessionCalls, repo.ownerLoadCalls)
		}
	})

	t.Run("canonical address", func(t *testing.T) {
		const ownerID = "authenticated-ren"
		sess, address := ownerBoundMessageSession(t, ownerID, "viewer", "public-canonical")
		repo := newOwnerAwareMessageSessionRepository()
		repo.canonicalSession = sess
		lifecycle := newMessageSessionLifecycle(repo)

		_, _, err := lifecycle.ResolveForRequest(context.Background(), ProcessMessageRequest{
			Channel: "viewer", ChatID: "public-canonical",
		}, time.Now())
		if err == nil {
			t.Fatal("public resolution accepted an owner-bound canonical Session")
		}
		if repo.publicCanonicalCalls != 1 || repo.ownerCreateCalls != 0 || repo.ownerLoadCalls != 0 {
			t.Fatalf("public canonical calls=%d owner creates=%d owner loads=%d, want 1, 0, and 0", repo.publicCanonicalCalls, repo.ownerCreateCalls, repo.ownerLoadCalls)
		}
		if sess.ChannelAddress() != address {
			t.Fatalf("canonical Session address=%+v, want %+v", sess.ChannelAddress(), address)
		}
	})
}

func TestMessageSessionLifecycleRejectsPublicSessionIdentityMismatch(t *testing.T) {
	address, err := domainconversation.NewChannelAddress("viewer", "requested-public-address")
	if err != nil {
		t.Fatal(err)
	}
	otherAddress, err := domainconversation.NewChannelAddress("viewer", "other-public-address")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.NewCanonicalSession(modulecore.NewSessionID(), "2026-10-24", otherAddress, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("canonical address mismatch", func(t *testing.T) {
		repo := newOwnerAwareMessageSessionRepository()
		repo.canonicalSession = sess
		repo.forceCanonicalResult = true
		lifecycle := newMessageSessionLifecycle(repo)
		_, _, _, err := lifecycle.ResolveForRequestWithOwner(context.Background(), ProcessMessageRequest{
			Channel: "viewer", ChatID: address.ExternalConversationID(),
		}, time.Now())
		if err == nil {
			t.Fatal("public canonical resolution accepted a mismatched ChannelAddress")
		}
	})
	t.Run("explicit session id mismatch", func(t *testing.T) {
		repo := newOwnerAwareMessageSessionRepository()
		requestedID := modulecore.NewSessionID()
		repo.sessions[string(requestedID)] = sess
		lifecycle := newMessageSessionLifecycle(repo)
		_, _, _, err := lifecycle.ResolveForRequestWithOwner(context.Background(), ProcessMessageRequest{
			SessionID: string(requestedID), Channel: "viewer", ChatID: otherAddress.ExternalConversationID(),
		}, time.Now())
		if err == nil {
			t.Fatal("public explicit resolution accepted a Session whose ID differs from the requested ID")
		}
	})
}

func TestProcessMessageCarriesResolvedOwnerBindingBeforeMio(t *testing.T) {
	const ownerID = "authenticated-ren"
	sess, _ := ownerBoundMessageSession(t, ownerID, "viewer", "chat-51")
	repo := newOwnerAwareMessageSessionRepository()
	repo.ownerResolution = session.CanonicalSessionOwnerResolution{
		Session: sess, BoundOwnerID: ownerID, Status: session.CanonicalSessionOwnerMatched,
	}
	var observedOwner string
	var observedBinding domainconversation.SessionBinding
	var observedBindingFound bool
	var observedScope domaintool.ToolExecutionScope
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "test"),
		response: "answer",
		decideFunc: func(ctx context.Context, _ domainconversation.TurnInput) (routing.Decision, error) {
			observedOwner, _ = boundCanonicalSessionOwnerFromContext(ctx)
			observedBinding, observedBindingFound = domainconversation.SessionBindingFromContext(ctx)
			observedScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
			return routing.NewDecision(routing.RouteCHAT, 1, "test"), nil
		},
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)

	ctx, err := domaintool.DeriveAgentToolExecutionScope(
		authenticatedOwnerMessageContext(t, ownerID), "mio-direct-answer", "mio", "chat", "", false,
	)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
	}
	_, err = orch.ProcessMessage(ctx, ProcessMessageRequest{
		SessionID: sess.ID(), Channel: "viewer", ChatID: "chat-51", UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if observedOwner != ownerID {
		t.Fatalf("owner binding observed by Mio = %q, want canonical owner %q", observedOwner, ownerID)
	}
	if !observedBindingFound || observedBinding.SessionID() != modulecore.SessionID(sess.ID()) ||
		observedBinding.ChannelAddress() != sess.ChannelAddress() || observedBinding.Audience() != domainconversation.SessionAudiencePrivate ||
		observedBinding.OwnerID() != ownerID || observedBinding.BindingGeneration() != domainconversation.BindingGenerationUnavailable {
		t.Fatalf("SessionBinding observed by Mio=%+v found=%v; want private owner binding for %q with generation 0", observedBinding, observedBindingFound, ownerID)
	}
	if observedScope.ActorKind != domaintool.ActorKindAgent || observedScope.ActorID != "mio" ||
		observedScope.AuthenticatedUserID != ownerID || !observedScope.Allows(domaintool.DataScopeUser) {
		t.Fatalf("scope observed by direct answer Mio=%+v, want Mio Agent scope delegated from authenticated owner %q", observedScope, ownerID)
	}
}

func TestProcessMessageUsesCanonicalOwnerBindingGenerationCapability(t *testing.T) {
	const ownerID = "ren"
	const requestContext = "same-request-context"
	sess, _ := ownerBoundMessageSession(t, ownerID, "viewer", "chat-binding-generation")
	baseRepo := newOwnerAwareMessageSessionRepository()
	baseRepo.ownerResolution = session.CanonicalSessionOwnerResolution{
		Session: sess, BoundOwnerID: ownerID, Status: session.CanonicalSessionOwnerMatched,
	}
	repo := &bindingGenerationMessageSessionRepository{
		ownerAwareMessageSessionRepository: baseRepo,
		generation:                         domainconversation.BindingGeneration(42),
	}
	var observedBinding domainconversation.SessionBinding
	var observedBindingFound bool
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "test"),
		response: "answer",
		decideFunc: func(ctx context.Context, _ domainconversation.TurnInput) (routing.Decision, error) {
			observedBinding, observedBindingFound = domainconversation.SessionBindingFromContext(ctx)
			return routing.NewDecision(routing.RouteCHAT, 1, "test"), nil
		},
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	ctx, err := domaintool.DeriveAgentToolExecutionScope(
		authenticatedOwnerMessageContext(t, ownerID), "mio-direct-answer", "mio", "chat", "", false,
	)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
	}
	ctx = context.WithValue(ctx, bindingGenerationContextKey{}, requestContext)
	if _, err := orch.ProcessMessage(ctx, ProcessMessageRequest{
		SessionID: sess.ID(), Channel: "viewer", ChatID: "chat-binding-generation", UserMessage: "hello",
	}); err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if !repo.resolverWasProven {
		t.Fatal("binding generation capability ran before canonical owner resolution")
	}
	if repo.observedBinding.BindingGeneration() != domainconversation.BindingGenerationUnavailable ||
		repo.observedBinding.Audience() != domainconversation.SessionAudiencePrivate || repo.observedBinding.OwnerID() != ownerID {
		t.Fatalf("generation lookup binding=%+v, want unresolved private binding for authenticated owner %q", repo.observedBinding, ownerID)
	}
	if repo.observedContext != requestContext {
		t.Fatalf("generation lookup context value=%v, want propagated request value %q", repo.observedContext, requestContext)
	}
	if !observedBindingFound || observedBinding.BindingGeneration() != repo.generation ||
		observedBinding.Audience() != domainconversation.SessionAudiencePrivate || observedBinding.OwnerID() != ownerID {
		t.Fatalf("binding observed by Mio=%+v found=%v, want repository generation %d for authenticated User %q", observedBinding, observedBindingFound, repo.generation, ownerID)
	}
}

func TestProcessMessageClearsCanonicalOwnerBindingForPublicRoute(t *testing.T) {
	address, err := domainconversation.NewChannelAddress("viewer", "public-chat")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.NewCanonicalSession(modulecore.NewSessionID(), "2026-10-24", address, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	repo := newOwnerAwareMessageSessionRepository()
	repo.canonicalSession = sess
	var observedOwner string
	var observedBinding domainconversation.SessionBinding
	var observedBindingFound bool
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "test"),
		response: "answer",
		decideFunc: func(ctx context.Context, _ domainconversation.TurnInput) (routing.Decision, error) {
			observedOwner, _ = boundCanonicalSessionOwnerFromContext(ctx)
			observedBinding, observedBindingFound = domainconversation.SessionBindingFromContext(ctx)
			return routing.NewDecision(routing.RouteCHAT, 1, "test"), nil
		},
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	staleAddress, err := domainconversation.NewChannelAddress("viewer", "stale-private-address")
	if err != nil {
		t.Fatal(err)
	}
	staleBinding, err := domainconversation.NewSessionBinding(modulecore.NewSessionID(), staleAddress, domainconversation.SessionAudiencePrivate, "stale-owner", 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withBoundCanonicalSessionOwner(context.Background(), "stale-owner")
	ctx = domainconversation.WithSessionBinding(ctx, staleBinding)

	_, err = orch.ProcessMessage(ctx, ProcessMessageRequest{
		Channel: "viewer", ChatID: "public-chat", UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if observedOwner != "" {
		t.Fatalf("public route propagated stale canonical owner binding %q", observedOwner)
	}
	if !observedBindingFound || observedBinding.SessionID() != modulecore.SessionID(sess.ID()) ||
		observedBinding.ChannelAddress() != sess.ChannelAddress() || observedBinding.Audience() != domainconversation.SessionAudiencePublic ||
		observedBinding.OwnerID() != "" || observedBinding.BindingGeneration() != domainconversation.BindingGenerationUnavailable {
		t.Fatalf("public route binding=%+v found=%v, want current public binding for %q", observedBinding, observedBindingFound, sess.ID())
	}
}

func TestLegacyCanonicalOwnerAccessorDoesNotTrustOwnerStringAlone(t *testing.T) {
	ctx := withBoundCanonicalSessionOwner(context.Background(), "unverified-owner")
	if ownerID, found := boundCanonicalSessionOwnerFromContext(ctx); found || ownerID != "" {
		t.Fatalf("legacy owner string was trusted without a validated SessionBinding: owner=%q found=%v", ownerID, found)
	}
}

func TestProcessMessageRejectsUnavailableOwnerSessionBeforeMio(t *testing.T) {
	sess, _ := ownerBoundMessageSession(t, "ren", "viewer", "chat-52")
	repo := newOwnerAwareMessageSessionRepository()
	repo.ownerResolution.Status = session.CanonicalSessionOwnerUnavailable
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCHAT, 1, "must not run"), response: "must not run"}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)

	_, err := orch.ProcessMessage(authenticatedOwnerMessageContext(t, "ren"), ProcessMessageRequest{
		SessionID: sess.ID(), Channel: "viewer", ChatID: "chat-52", UserMessage: "hello",
	})
	if err == nil {
		t.Fatal("ProcessMessage accepted unavailable owner resolution")
	}
	if mio.decideCalls != 0 {
		t.Fatalf("Mio.DecideAction calls=%d, want 0 before owner resolution succeeds", mio.decideCalls)
	}
}
