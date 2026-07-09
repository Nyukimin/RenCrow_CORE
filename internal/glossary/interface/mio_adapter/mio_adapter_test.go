package mio_adapter

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
)

func TestMioGlossaryAdapterContextAndTopics(t *testing.T) {
	repo, err := persistence.NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer repo.Close()

	svc := service.NewGlossaryService(repo)
	adapter := NewMioGlossaryAdapter(svc)
	ctx := context.Background()
	if _, err := svc.AddGlossaryItem(ctx, "Mio", "chat agent", "manual", "agent"); err != nil {
		t.Fatalf("AddGlossaryItem failed: %v", err)
	}

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
