package eventstore

import (
	"context"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestReadPendingAcceptedV2SessionPageUsesOwnerCheckedKeyset(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	firstSession := modulecore.NewSessionID()
	secondSession := modulecore.NewSessionID()
	acceptLiveTurn(t, store, firstSession, "first", true, 1)
	acceptLiveTurn(t, store, secondSession, "second", true, 1)

	var got []modulecore.SessionID
	cursor := modulecore.SessionID("")
	for page := 0; page < 3; page++ {
		sessions, hasMore, err := store.ReadPendingAcceptedV2SessionPage(ctx, "user-1", cursor, 1)
		if err != nil {
			t.Fatalf("ReadPendingAcceptedV2SessionPage(cursor=%q): %v", cursor, err)
		}
		got = append(got, sessions...)
		if !hasMore {
			break
		}
		if len(sessions) != 1 {
			t.Fatalf("page with hasMore=true returned %d sessions, want a full one-session page", len(sessions))
		}
		cursor = sessions[len(sessions)-1]
	}
	if len(got) != 2 || got[0] == got[1] || (got[0] != firstSession && got[0] != secondSession) || (got[1] != firstSession && got[1] != secondSession) {
		t.Fatalf("pending v2 session pages = %v, want both accepted sessions exactly once", got)
	}

	wrongOwner, hasMore, err := store.ReadPendingAcceptedV2SessionPage(ctx, "user-2", "", 10)
	if err != nil || hasMore || len(wrongOwner) != 0 {
		t.Fatalf("wrong-owner pending sessions=%v hasMore=%t err=%v, want an empty page", wrongOwner, hasMore, err)
	}
	if _, _, err := store.ReadPendingAcceptedV2SessionPage(ctx, "user-1", "", 0); err == nil {
		t.Fatal("ReadPendingAcceptedV2SessionPage(limit=0) error = nil, want invalid limit")
	}
}

func TestReadPendingAcceptedV2SessionPageExcludesCompletedAndLegacySessions(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	completedSession := modulecore.NewSessionID()
	completed := acceptLiveTurn(t, store, completedSession, "completed", true, 1)
	commitCompletedTurn(t, store, completed)

	legacySession := modulecore.NewSessionID()
	appendLegacyUserEvent(t, store, legacySession, "user-1", "legacy input")

	sessions, hasMore, err := store.ReadPendingAcceptedV2SessionPage(ctx, "user-1", "", 10)
	if err != nil {
		t.Fatalf("ReadPendingAcceptedV2SessionPage(): %v", err)
	}
	if hasMore || len(sessions) != 0 {
		t.Fatalf("pending v2 sessions=%v hasMore=%t, want none for completed and legacy data", sessions, hasMore)
	}
}
