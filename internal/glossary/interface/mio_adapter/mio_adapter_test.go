package mio_adapter

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/repository"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
)

type countingGlossaryRepository struct {
	repository.GlossaryRepository
	recentReads int
}

func (r *countingGlossaryRepository) SetGlossaryMutationHooks(before func(), after func(context.Context)) {
	if owner, ok := r.GlossaryRepository.(interface {
		SetGlossaryMutationHooks(func(), func(context.Context))
	}); ok {
		owner.SetGlossaryMutationHooks(before, after)
	}
}

func (r *countingGlossaryRepository) FindRecent(ctx context.Context, limit int) ([]*entity.GlossaryItem, error) {
	r.recentReads++
	return r.GlossaryRepository.FindRecent(ctx, limit)
}

func TestMioGlossaryAdapterContextAndTopics(t *testing.T) {
	repo, err := persistence.NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	countedRepo := &countingGlossaryRepository{GlossaryRepository: repo}
	svc := service.NewGlossaryService(countedRepo)
	adapter := NewMioGlossaryAdapter(svc)
	ctx := context.Background()
	added, err := svc.AddGlossaryItem(ctx, "Mio", "chat agent", "manual", "agent")
	if err != nil {
		t.Fatalf("AddGlossaryItem failed: %v", err)
	}
	readsAfterMutation := countedRepo.recentReads

	termContext, err := adapter.GetContextForTerm(ctx, "Mio")
	if err != nil {
		t.Fatalf("GetContextForTerm failed: %v", err)
	}
	if !strings.Contains(termContext, "chat agent") || !strings.Contains(termContext, "manual") {
		t.Fatalf("unexpected term context: %q", termContext)
	}

	topics, err := adapter.GetRecentTopics(ctx, 5)
	if err != nil {
		t.Fatalf("GetRecentTopics failed: %v", err)
	}
	if len(topics) != 1 || !strings.Contains(topics[0], "Mio: chat agent") {
		t.Fatalf("unexpected topics: %#v", topics)
	}

	recentContext, err := adapter.GetRecentContext(ctx, 5)
	if err != nil {
		t.Fatalf("GetRecentContext failed: %v", err)
	}
	if !strings.Contains(recentContext, "最近語彙メモ") || !strings.Contains(recentContext, "Mio: chat agent") {
		t.Fatalf("unexpected recent context: %q", recentContext)
	}
	if countedRepo.recentReads != readsAfterMutation {
		t.Fatalf("Mio recent prompt reads hit repository: before=%d after=%d", readsAfterMutation, countedRepo.recentReads)
	}
	if err := repo.Delete(ctx, added.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if got, err := adapter.GetRecentContext(ctx, 5); err != nil || got != "" {
		t.Fatalf("recent context after delete=%q err=%v, want empty", got, err)
	}
}

func TestMioGlossaryAdapterRecentPromptIsEmptyUntilSnapshotReady(t *testing.T) {
	repo, err := persistence.NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	countedRepo := &countingGlossaryRepository{GlossaryRepository: repo}
	adapter := NewMioGlossaryAdapter(service.NewGlossaryService(countedRepo))
	if got, err := adapter.GetRecentContext(context.Background(), 5); err != nil || got != "" {
		t.Fatalf("not-ready RecentContext=%q err=%v, want empty", got, err)
	}
	if got, err := adapter.GetRecentTopics(context.Background(), 5); err != nil || len(got) != 0 {
		t.Fatalf("not-ready RecentTopics=%v err=%v, want empty", got, err)
	}
	if countedRepo.recentReads != 0 {
		t.Fatalf("not-ready prompt caused %d repository reads, want 0", countedRepo.recentReads)
	}
}

func TestMioGlossaryAdapterSnapshotTracksStorageHostMutations(t *testing.T) {
	repo, err := persistence.NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	adapter := NewMioGlossaryAdapter(service.NewGlossaryService(repo))
	if err := adapter.RefreshRecentPromptSnapshot(context.Background()); err != nil {
		t.Fatalf("initial snapshot warm: %v", err)
	}
	item := entity.NewGlossaryItem("StorageHost", "owner mutation", "test", "runtime")
	identity := persistence.GlossaryOperationIdentity{OpID: "glossary-save-test", Operation: "save", PayloadHash: strings.Repeat("a", 64)}
	if err := repo.SaveForStorageHostOperation(context.Background(), identity, item); err != nil {
		t.Fatalf("SaveForStorageHostOperation: %v", err)
	}
	if got, err := adapter.GetRecentContext(context.Background(), 5); err != nil || !strings.Contains(got, "StorageHost: owner mutation") {
		t.Fatalf("recent context after storage-host save=%q err=%v", got, err)
	}

	deleteIdentity := persistence.GlossaryOperationIdentity{OpID: "glossary-delete-test", Operation: "delete", PayloadHash: strings.Repeat("b", 64)}
	if err := repo.DeleteForStorageHostOperation(context.Background(), deleteIdentity, item.ID); err != nil {
		t.Fatalf("DeleteForStorageHostOperation: %v", err)
	}
	if got, err := adapter.GetRecentContext(context.Background(), 5); err != nil || got != "" {
		t.Fatalf("recent context after storage-host delete=%q err=%v, want empty", got, err)
	}
}

func TestMioGlossaryAdapterEmptyContext(t *testing.T) {
	repo, err := persistence.NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	adapter := NewMioGlossaryAdapter(service.NewGlossaryService(repo))
	ctx := context.Background()

	if got, err := adapter.GetRecentContext(ctx, 5); err != nil || got != "" {
		t.Fatalf("empty recent context = %q, %v", got, err)
	}
	if _, err := adapter.GetContextForTerm(ctx, "missing"); err == nil {
		t.Fatal("expected missing term lookup to return repository error")
	}
}
