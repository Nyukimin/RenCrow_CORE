package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type sessionProtocolVersionProjector interface {
	SessionRuntimeVersion(context.Context, modulecore.SessionID, string) (string, bool, error)
}

func requireSessionProtocolVersionProjector(t *testing.T, store *SQLiteStore) sessionProtocolVersionProjector {
	t.Helper()
	projector, ok := any(store).(sessionProtocolVersionProjector)
	if !ok {
		t.Fatal("EventStore does not expose its hydrated session protocol projection")
	}
	return projector
}

func TestSessionRuntimeVersionProjectionUsesWarmStateWithoutSQLiteRead(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	legacySessionID := modulecore.NewSessionID()
	appendLegacyUserEvent(t, store, legacySessionID, "user-1", "existing legacy session")
	v2SessionID := modulecore.NewSessionID()
	acceptLiveTurn(t, store, v2SessionID, "projection-v2", true, 1)
	projector := requireSessionProtocolVersionProjector(t, store)

	if store.sessionStateDB == nil {
		t.Fatal("expected an independent EventStore read pool")
	}
	store.sessionStateDB.SetMaxOpenConns(1)
	readTx, err := store.sessionStateDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("pin EventStore read pool: %v", err)
	}
	defer readTx.Rollback()

	lookupCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	version, found, err := projector.SessionRuntimeVersion(lookupCtx, legacySessionID, "user-1")
	if err != nil || found || version != "" {
		t.Fatalf("legacy session projection = (%q, %t, %v), want verified absence", version, found, err)
	}
	version, found, err = projector.SessionRuntimeVersion(lookupCtx, v2SessionID, "user-1")
	if err != nil || !found || version != "v2" {
		t.Fatalf("v2 session projection = (%q, %t, %v), want v2 pin", version, found, err)
	}
	if _, _, err := projector.SessionRuntimeVersion(lookupCtx, v2SessionID, "other-owner"); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("wrong-owner projection error = %v, want owner conflict", err)
	}
	if err := lookupCtx.Err(); err != nil {
		t.Fatalf("projection lookup consumed the blocked SQLite read pool: %v", err)
	}
}

func TestSessionRuntimeVersionProjectionUpdatesOnCutoverAndFirstV2Acceptance(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	projector := requireSessionProtocolVersionProjector(t, store)

	legacySessionID := modulecore.NewSessionID()
	appendLegacyUserEvent(t, store, legacySessionID, "user-1", "cutover candidate")
	assertSessionRuntimeVersion(t, projector, legacySessionID, "user-1", "", false)
	boundary, err := store.ReadLegacySessionBoundary(ctx, legacySessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadLegacySessionBoundary(): %v", err)
	}
	token, err := NewVerifiedIdleBoundaryToken(boundary)
	if err != nil {
		t.Fatalf("NewVerifiedIdleBoundaryToken(): %v", err)
	}
	if _, err := store.CutoverSession(ctx, CutoverSessionInput{Boundary: token, RuntimeVersion: "v2", GenerationEpoch: 1}); err != nil {
		t.Fatalf("CutoverSession(): %v", err)
	}
	assertSessionRuntimeVersion(t, projector, legacySessionID, "user-1", "v2", true)

	newSessionID := modulecore.NewSessionID()
	assertSessionRuntimeVersion(t, projector, newSessionID, "user-1", "", false)
	acceptLiveTurn(t, store, newSessionID, "projection-first-accept", true, 1)
	assertSessionRuntimeVersion(t, projector, newSessionID, "user-1", "v2", true)
}

func TestSessionRuntimeVersionProjectionRebuildsFromDurablePins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-projection.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	legacySessionID := modulecore.NewSessionID()
	appendLegacyUserEvent(t, store, legacySessionID, "user-1", "un-pinned legacy session")
	v2SessionID := modulecore.NewSessionID()
	acceptLiveTurn(t, store, v2SessionID, "projection-restart", true, 1)
	if _, err := store.db.ExecContext(context.Background(), `UPDATE live_conversation_session SET runtime_version = 'future-v9' WHERE session_id = ?`, string(v2SessionID)); err != nil {
		t.Fatalf("seed unsupported durable pin: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	defer store.Close()
	projector := requireSessionProtocolVersionProjector(t, store)
	assertSessionRuntimeVersion(t, projector, legacySessionID, "user-1", "", false)
	assertSessionRuntimeVersion(t, projector, v2SessionID, "user-1", "future-v9", true)
	if _, _, err := projector.SessionRuntimeVersion(context.Background(), v2SessionID, "other-owner"); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("reloaded wrong-owner projection error = %v, want owner conflict", err)
	}
}

func assertSessionRuntimeVersion(t *testing.T, projector sessionProtocolVersionProjector, sessionID modulecore.SessionID, ownerID, wantVersion string, wantFound bool) {
	t.Helper()
	version, found, err := projector.SessionRuntimeVersion(context.Background(), sessionID, ownerID)
	if err != nil || version != wantVersion || found != wantFound {
		t.Fatalf("SessionRuntimeVersion(%q, %q) = (%q, %t, %v), want (%q, %t, nil)", sessionID, ownerID, version, found, err, wantVersion, wantFound)
	}
}
