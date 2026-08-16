package archivesqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestL1SQLiteStoreOwnerArchiveCopiesConfirmedMemoryAndKeepsSourceImmutable(t *testing.T) {
	ctx := context.Background()
	l1, archive := newOwnerArchiveStores(t)
	item, err := l1.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "exact archive source",
		State: domainmemory.MemoryStateConfirmed, EvidenceEventIDs: []string{"evidence-1"},
		Confidence: 0.91, Sensitivity: "normal", Scope: "all_personas", Source: "user_explicit",
	})
	if err != nil {
		t.Fatalf("CreateUserMemory failed: %v", err)
	}
	original, found, err := l1.FindUserMemoryEventByID(ctx, "ren", item.ID)
	if err != nil || !found {
		t.Fatalf("read original source found=%v err=%v", found, err)
	}

	requestID := "archive-owner-request-1"
	result, err := l1.OwnerArchiveUserMemory(ownerArchiveContext(t, requestID, "ren"), requestID, "ren", "ren", item.ID, "retain exact copy")
	if err != nil {
		t.Fatalf("OwnerArchiveUserMemory failed: %v", err)
	}
	if result.Item.ID != item.ID || result.Item.State != domainmemory.MemoryStateConfirmed || result.Receipt.Operation != domainmemory.UserMemoryOwnerOperationArchive ||
		result.Receipt.OwnerRoute != "conversation_archive/user_memory/archive" || result.Receipt.AuditReference != item.ID || result.Receipt.InputCount != 1 || result.Receipt.OutputCount != 1 || result.Receipt.IdempotentReplay {
		t.Fatalf("archive result=%+v", result)
	}
	archived, found, err := archive.FindUserMemoryArchive(ctx, "ren", item.ID)
	if err != nil || !found || !archiveL1MemoryEventEqual(archived, original) {
		t.Fatalf("archived event=%+v found=%v err=%v original=%+v", archived, found, err, original)
	}
	unchanged, found, err := l1.FindUserMemoryEventByID(ctx, "ren", item.ID)
	if err != nil || !found || !archiveL1MemoryEventEqual(unchanged, original) {
		t.Fatalf("source changed after archive: got=%+v found=%v err=%v original=%+v", unchanged, found, err, original)
	}
	var archiveRows, receiptRows int
	if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event_archive WHERE id = ?`, item.ID).Scan(&archiveRows); err != nil {
		t.Fatalf("archive row count: %v", err)
	}
	if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_archive_request_receipt WHERE request_id = ?`, requestID).Scan(&receiptRows); err != nil {
		t.Fatalf("receipt row count: %v", err)
	}
	if archiveRows != 1 || receiptRows != 1 {
		t.Fatalf("archive rows=%d receipts=%d", archiveRows, receiptRows)
	}

	replay, err := l1.OwnerArchiveUserMemory(ownerArchiveContext(t, requestID, "ren"), requestID, "ren", "ren", item.ID, "retain exact copy")
	if err != nil || !replay.Receipt.IdempotentReplay || !replay.Receipt.CompletedAt.Equal(result.Receipt.CompletedAt) {
		t.Fatalf("same request replay=%+v err=%v", replay, err)
	}
	if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event_archive WHERE id = ?`, item.ID).Scan(&archiveRows); err != nil {
		t.Fatalf("replay archive row count: %v", err)
	}
	if archiveRows != 1 {
		t.Fatalf("replay replaced archive row count=%d", archiveRows)
	}

	_, err = l1.OwnerArchiveUserMemory(ownerArchiveContext(t, requestID, "ren"), requestID, "ren", "ren", item.ID, "different reason")
	if !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		t.Fatalf("same request different payload err=%v, want conflict", err)
	}
}

func TestL1SQLiteStoreOwnerArchiveRejectsCandidateCrossOwnerAndUnavailable(t *testing.T) {
	ctx := context.Background()
	t.Run("candidate conflict", func(t *testing.T) {
		l1, archive := newOwnerArchiveStores(t)
		item, err := l1.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
			UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "candidate only", Source: "viewer",
		})
		if err != nil {
			t.Fatalf("CreateUserMemory candidate failed: %v", err)
		}
		_, err = l1.OwnerArchiveUserMemory(ownerArchiveContext(t, "candidate-request", "ren"), "candidate-request", "ren", "ren", item.ID, "retain")
		if !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
			t.Fatalf("candidate err=%v, want conflict", err)
		}
		var count int
		if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event_archive`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("candidate archived rows=%d", count)
		}
	})

	t.Run("cross owner not found", func(t *testing.T) {
		l1, archive := newOwnerArchiveStores(t)
		item, err := l1.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
			UserID: "other", Type: domainmemory.UserMemoryTypePreference, Statement: "other owner", State: domainmemory.MemoryStateConfirmed,
			EvidenceEventIDs: []string{"evidence-other"}, Source: "user_explicit",
		})
		if err != nil {
			t.Fatalf("CreateUserMemory cross-owner failed: %v", err)
		}
		_, err = l1.OwnerArchiveUserMemory(ownerArchiveContext(t, "cross-owner-request", "ren"), "cross-owner-request", "ren", "ren", item.ID, "retain")
		if !errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound) {
			t.Fatalf("cross owner err=%v, want not found", err)
		}
		var count int
		if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event_archive`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("cross-owner archived rows=%d", count)
		}
	})

	t.Run("archive unavailable", func(t *testing.T) {
		l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer l1.Close()
		_, err = l1.OwnerArchiveUserMemory(ownerArchiveContext(t, "unavailable-request", "ren"), "unavailable-request", "ren", "ren", "memory-1", "retain")
		if !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
			t.Fatalf("unavailable err=%v, want unavailable", err)
		}
	})
}

func TestL1SQLiteStoreOwnerArchiveRequiresTrustedUserScope(t *testing.T) {
	ctx := context.Background()
	l1, archive := newOwnerArchiveStores(t)
	item, err := l1.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "scope must be trusted",
		State: domainmemory.MemoryStateConfirmed, EvidenceEventIDs: []string{"evidence-scope"}, Source: "user_explicit",
	})
	if err != nil {
		t.Fatalf("CreateUserMemory failed: %v", err)
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing scope", ctx: context.Background()},
		{name: "invalid scope", ctx: domaintool.WithToolExecutionScope(ctx, domaintool.ToolExecutionScope{RequestID: "scope-request", ActorKind: domaintool.ActorKindUser, ActorID: "ren", AuthenticatedUserID: "ren"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := l1.OwnerArchiveUserMemory(tc.ctx, "scope-request", "ren", "ren", item.ID, "retain")
			if !errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) {
				t.Fatalf("err=%v, want forbidden", err)
			}
			var archiveRows, receiptRows int
			if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event_archive`).Scan(&archiveRows); err != nil {
				t.Fatalf("archive row count: %v", err)
			}
			if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_archive_request_receipt`).Scan(&receiptRows); err != nil {
				t.Fatalf("receipt row count: %v", err)
			}
			if archiveRows != 0 || receiptRows != 0 {
				t.Fatalf("untrusted scope created archive rows=%d receipts=%d", archiveRows, receiptRows)
			}
		})
	}
}

func newOwnerArchiveStores(t *testing.T) (*l1sqlite.L1SQLiteStore, *ArchiveSQLiteStore) {
	t.Helper()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	archive, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		l1.Close()
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	l1.WithArchiveStore(archive)
	t.Cleanup(func() {
		_ = archive.Close()
		_ = l1.Close()
	})
	return l1, archive
}

func ownerArchiveContext(t *testing.T, requestID, userID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, userID, userID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatalf("NewToolExecutionScope failed: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
