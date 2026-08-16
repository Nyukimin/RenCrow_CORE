package l1sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestL1SQLiteStoreOwnerParquetExportValidatesScopeRootAndDelegatesOnce(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fake := &parquetArchiveStoreStub{}
	store.WithArchiveStore(fake)
	if err := store.SetParquetExportRoot(filepath.Join(t.TempDir(), "exports")); err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope("request-1", domaintool.ActorKindUser, "ren", "ren", []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.OwnerExportConversationArchiveParquet(domaintool.WithToolExecutionScope(ctx, scope), "request-1", "ren", "ren")
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	if fake.exportCalls != 1 || fake.lastRoot == "" || result.Receipt.Operation != domainmemory.UserMemoryOwnerOperationParquetExport {
		t.Fatalf("calls=%d root=%q result=%+v", fake.exportCalls, fake.lastRoot, result)
	}

	if _, err := store.OwnerExportConversationArchiveParquet(ctx, "request-2", "ren", "ren"); !errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) {
		t.Fatalf("missing scope = %v", err)
	}
	unsafeStore, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "unsafe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer unsafeStore.Close()
	if err := unsafeStore.SetParquetExportRoot("relative-root"); err == nil {
		t.Fatal("relative root should be rejected")
	}
	unsafeStore.WithArchiveStore(fake)
	if _, err := unsafeStore.OwnerExportConversationArchiveParquet(domaintool.WithToolExecutionScope(ctx, scope), "request-1", "ren", "ren"); !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("unsafe root = %v", err)
	}
	store.WithArchiveStore(nil)
	if _, err := store.OwnerExportConversationArchiveParquet(domaintool.WithToolExecutionScope(ctx, scope), "request-1", "ren", "ren"); !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("missing archive store = %v", err)
	}
}

func TestL1SQLiteStoreOwnerParquetVerifyRequiresTargetAndTrustedScope(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.WithArchiveStore(&parquetArchiveStoreStub{})
	if err := store.SetParquetExportRoot(filepath.Join(t.TempDir(), "exports")); err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope("verify-request", domaintool.ActorKindUser, "ren", "ren", []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OwnerVerifyConversationArchiveParquet(domaintool.WithToolExecutionScope(context.Background(), scope), "verify-request", "ren", "ren", "target-request"); err != nil {
		t.Fatalf("verify delegation failed: %v", err)
	}
}

type parquetArchiveStoreStub struct {
	exportCalls int
	verifyCalls int
	lastRoot    string
}

func (s *parquetArchiveStoreStub) ArchiveL1MemoryEvents(context.Context, []L1MemoryEvent) error {
	return nil
}

func (s *parquetArchiveStoreStub) ArchiveL1NewsItems(context.Context, []L1NewsItem) error {
	return nil
}

func (s *parquetArchiveStoreStub) ArchiveL1KnowledgeItems(context.Context, []L1KnowledgeItem) error {
	return nil
}

func (s *parquetArchiveStoreStub) ArchiveL1StagingItems(context.Context, []L1StagingItem) error {
	return nil
}

func (s *parquetArchiveStoreStub) ExportConversationArchiveParquet(_ context.Context, req OwnerParquetExportRequest, root string) (domainmemory.ConversationArchiveParquetExportResult, error) {
	s.exportCalls++
	s.lastRoot = root
	return domainmemory.ConversationArchiveParquetExportResult{ExportID: req.RequestID, Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: req.RequestID, Operation: domainmemory.UserMemoryOwnerOperationParquetExport}}, nil
}

func (s *parquetArchiveStoreStub) VerifyConversationArchiveParquet(_ context.Context, req OwnerParquetVerifyRequest, root string) (domainmemory.ConversationArchiveParquetVerifyResult, error) {
	s.verifyCalls++
	s.lastRoot = root
	return domainmemory.ConversationArchiveParquetVerifyResult{ExportID: req.TargetExportRequestID, Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: req.RequestID, Operation: domainmemory.UserMemoryOwnerOperationParquetVerify}}, nil
}
