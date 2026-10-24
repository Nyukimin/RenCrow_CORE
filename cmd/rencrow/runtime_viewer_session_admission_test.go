package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestViewerSessionProtocolAdmissionUsesVerifiedRequestOwner(t *testing.T) {
	store := openViewerBridgeAdmissionStore(t)
	legacySessionID := seedViewerProtocolSession(t, store, "ren", "v2")
	futureSessionID := seedViewerProtocolSession(t, store, "ren", "future-v9")
	unpinnedSessionID := seedViewerProtocolLegacySession(t, store, "ren")
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	admission := newViewerSessionProtocolAdmission(
		&config.Config{LocalAgentOps: config.LocalAgentOpsConfig{UserID: "ren"}},
		&Dependencies{canonicalEventStore: store},
		repo,
	)

	renContext := viewerProtocolUserContext(t, "ren")
	legacyRelease, err := admission.AdmitLegacy(renContext, unpinnedSessionID)
	if err != nil {
		t.Fatalf("AdmitLegacy(unpinned legacy session): %v", err)
	}
	legacyRelease()

	release, err := admission.AdmitV2(renContext, legacySessionID)
	if err != nil {
		t.Fatalf("AdmitV2(authenticated ren): %v", err)
	}
	release()

	otherUserContext := viewerProtocolUserContext(t, "other-user")
	if release, err := admission.AdmitV2(otherUserContext, legacySessionID); release != nil || !errors.Is(err, viewer.ErrSessionProtocolUnavailable) {
		t.Fatalf("AdmitV2(wrong authenticated owner) = (%v, %v), want unavailable", release != nil, err)
	}
	if release, err := admission.AdmitLegacy(renContext, legacySessionID); release != nil || !errors.Is(err, viewer.ErrSessionProtocolConflict) {
		t.Fatalf("AdmitLegacy(v2-pinned session) = (%v, %v), want protocol conflict", release != nil, err)
	}
	if release, err := admission.AdmitV2(renContext, futureSessionID); release != nil || !errors.Is(err, viewer.ErrSessionProtocolUnavailable) {
		t.Fatalf("AdmitV2(unknown persisted version) = (%v, %v), want unavailable", release != nil, err)
	}
}

func seedViewerProtocolSession(t *testing.T, store *eventstore.SQLiteStore, ownerID, runtimeVersion string) modulecore.SessionID {
	t.Helper()
	ctx := context.Background()
	sessionID := seedViewerProtocolLegacySession(t, store, ownerID)
	boundary, err := store.ReadLegacySessionBoundary(ctx, sessionID, ownerID)
	if err != nil {
		t.Fatalf("ReadLegacySessionBoundary(protocol fixture): %v", err)
	}
	token, err := eventstore.NewVerifiedIdleBoundaryToken(boundary)
	if err != nil {
		t.Fatalf("NewVerifiedIdleBoundaryToken(protocol fixture): %v", err)
	}
	if _, err := store.CutoverSession(ctx, eventstore.CutoverSessionInput{
		Boundary: token, RuntimeVersion: runtimeVersion, GenerationEpoch: 1,
	}); err != nil {
		t.Fatalf("CutoverSession(protocol fixture): %v", err)
	}
	return sessionID
}

func seedViewerProtocolLegacySession(t *testing.T, store *eventstore.SQLiteStore, ownerID string) modulecore.SessionID {
	t.Helper()
	sessionID := modulecore.NewSessionID()
	event := modulecore.NewRootEventEnvelope("orchestrator", "conversation.user_message", time.Now().UTC(), map[string]any{
		"from": "user", "source_actor_id": ownerID, "to": "mio", "content": "protocol fixture", "text": "protocol fixture",
	})
	event.SessionID = sessionID
	event.TurnID = modulecore.NewTurnID()
	event.MessageID = modulecore.NewMessageID()
	event.ActorKind = "user"
	event.ActorID = ownerID
	if _, err := store.AppendSequenced(context.Background(), event); err != nil {
		t.Fatalf("AppendSequenced(protocol fixture): %v", err)
	}
	return sessionID
}

func viewerProtocolUserContext(t *testing.T, userID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		"viewer-protocol-test", domaintool.ActorKindUser, userID, userID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(%q): %v", userID, err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
