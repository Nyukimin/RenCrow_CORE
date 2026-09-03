package orchestrator

import (
	"context"
	"testing"
	"time"

	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMessageSessionLifecycleResolvesCanonicalIdentityBeforeProcessing(t *testing.T) {
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	lifecycle := newMessageSessionLifecycle(repo)
	now := time.Date(2026, 9, 3, 23, 59, 59, 0, time.UTC)
	req := ProcessMessageRequest{Channel: "line", ChatID: "U123"}

	first, resolved, err := lifecycle.ResolveForRequest(context.Background(), req, now)
	if err != nil {
		t.Fatalf("ResolveForRequest: %v", err)
	}
	if resolved.SessionID != first.ID() {
		t.Fatalf("resolved SessionID = %q, want %q", resolved.SessionID, first.ID())
	}
	if err := modulecore.SessionID(resolved.SessionID).Validate(); err != nil {
		t.Fatalf("resolved SessionID: %v", err)
	}

	second, resolvedAgain, err := lifecycle.ResolveForRequest(context.Background(), req, now.Add(time.Second-time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID() != first.ID() || resolvedAgain.SessionID != first.ID() {
		t.Fatal("same logical date and ChannelAddress did not reuse the session")
	}

	next, _, err := lifecycle.ResolveForRequest(context.Background(), req, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() == first.ID() || next.LogicalDate() != "2026-09-04" {
		t.Fatalf("date boundary session = id:%q date:%q", next.ID(), next.LogicalDate())
	}
}
