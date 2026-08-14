package toolregistry

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
)

func newTestStore(t *testing.T) *SQLiteToolRegistryStore {
	t.Helper()
	store, err := NewSQLiteToolRegistryStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSQLiteToolRegistryConfiguresSerializedBusyTimeoutAndWAL(t *testing.T) {
	store, err := NewSQLiteToolRegistryStore(filepath.Join(t.TempDir(), "tool-registry.db"))
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout query failed: %v", err)
	}
	if busyTimeout != sqliteBusyTimeoutMilliseconds {
		t.Fatalf("busy_timeout = %d, want %d", busyTimeout, sqliteBusyTimeoutMilliseconds)
	}
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode query failed: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
}

func TestSQLiteToolRegistryConcurrentRegisterWrites(t *testing.T) {
	store := newTestStore(t)
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.Register(context.Background(), capability.ToolEntry{
				Name:        fmt.Sprintf("concurrent-tool-%d", i),
				Description: "concurrent owner write",
				SchemaJSON:  `{}`,
				Platforms:   []string{"linux"},
				Source:      capability.ToolSourceBuiltin,
				CreatedAt:   time.Now().UTC(),
				CreatedBy:   "test",
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Register failed: %v", err)
		}
	}
	items, err := store.ListForPlatform(context.Background(), "linux")
	if err != nil || len(items) != workers {
		t.Fatalf("concurrent tool count = %d, err=%v; want %d", len(items), err, workers)
	}
}

func TestRegisterAndGet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	entry := capability.ToolEntry{
		Name:        "web_search",
		Description: "Search the web",
		SchemaJSON:  `{"type":"function","function":{"name":"web_search"}}`,
		Platforms:   []string{"linux", "windows"},
		Source:      capability.ToolSourceBuiltin,
		CreatedAt:   time.Now(),
		CreatedBy:   "builtin",
	}

	if err := store.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := store.Get(ctx, "web_search")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "web_search" {
		t.Errorf("Name = %q, want %q", got.Name, "web_search")
	}
	if len(got.Platforms) != 2 {
		t.Errorf("Platforms len = %d, want 2", len(got.Platforms))
	}
	if got.Source != capability.ToolSourceBuiltin {
		t.Errorf("Source = %q, want %q", got.Source, capability.ToolSourceBuiltin)
	}
}

func TestRegister_Idempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	entry := capability.ToolEntry{
		Name:       "tool_a",
		SchemaJSON: `{}`,
		Platforms:  []string{"linux"},
		Source:     capability.ToolSourceBuiltin,
	}

	// 2回登録しても重複エラーにならない
	if err := store.Register(ctx, entry); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	entry.Description = "Updated description"
	if err := store.Register(ctx, entry); err != nil {
		t.Fatalf("second Register: %v", err)
	}

	got, err := store.Get(ctx, "tool_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Description != "Updated description" {
		t.Errorf("Description = %q, want %q", got.Description, "Updated description")
	}
}

func TestListForPlatform(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	tools := []capability.ToolEntry{
		{Name: "linux_only", SchemaJSON: "{}", Platforms: []string{"linux"}, Source: capability.ToolSourceBuiltin},
		{Name: "windows_only", SchemaJSON: "{}", Platforms: []string{"windows"}, Source: capability.ToolSourceBuiltin},
		{Name: "cross_platform", SchemaJSON: "{}", Platforms: []string{"linux", "windows"}, Source: capability.ToolSourceBuiltin},
	}
	for _, e := range tools {
		if err := store.Register(ctx, e); err != nil {
			t.Fatalf("Register %q: %v", e.Name, err)
		}
	}

	linuxTools, err := store.ListForPlatform(ctx, "linux")
	if err != nil {
		t.Fatalf("ListForPlatform(linux): %v", err)
	}
	if len(linuxTools) != 2 {
		t.Errorf("expected 2 linux tools, got %d: %v", len(linuxTools), linuxTools)
	}

	winTools, err := store.ListForPlatform(ctx, "windows")
	if err != nil {
		t.Fatalf("ListForPlatform(windows): %v", err)
	}
	if len(winTools) != 2 {
		t.Errorf("expected 2 windows tools, got %d", len(winTools))
	}

	darwinTools, err := store.ListForPlatform(ctx, "darwin")
	if err != nil {
		t.Fatalf("ListForPlatform(darwin): %v", err)
	}
	if len(darwinTools) != 0 {
		t.Errorf("expected 0 darwin tools, got %d", len(darwinTools))
	}
}

func TestRegisterWithReceiptReplayConflictAndSemanticDedupe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool-registry.db")
	store, err := NewSQLiteToolRegistryStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	ctx := context.Background()
	entry := capability.ToolEntry{
		Name: "existing_tool", Description: "same", SchemaJSON: `{"type":"object"}`,
		Platforms: []string{"windows", "linux"}, Source: capability.ToolSource("/workspace/tools/existing_tool.sh"), CreatedBy: "mio",
	}
	first, err := store.RegisterWithReceipt(ctx, entry, "request-1", "mio", "hash-1")
	if err != nil || first.RequestReplay || first.SemanticDedupe || first.Receipt.RequestID != "request-1" {
		t.Fatalf("first registration = %+v err=%v", first, err)
	}
	replay, err := store.RegisterWithReceipt(ctx, entry, "request-1", "mio", "hash-1")
	if err != nil || !replay.RequestReplay || replay.SemanticDedupe || !replay.Receipt.CreatedAt.Equal(first.Receipt.CreatedAt) {
		t.Fatalf("replay = %+v err=%v first=%+v", replay, err, first)
	}
	if _, err := store.RegisterWithReceipt(ctx, entry, "request-1", "mio", "different-hash"); !errors.Is(err, ErrToolRegistryRequestConflict) {
		t.Fatalf("payload conflict err=%v", err)
	}
	if _, err := store.RegisterWithReceipt(ctx, entry, "request-1", "other-agent", "hash-1"); !errors.Is(err, ErrToolRegistryRequestConflict) {
		t.Fatalf("actor conflict err=%v", err)
	}
	semantic, err := store.RegisterWithReceipt(ctx, entry, "request-2", "shiro", "hash-2")
	if err != nil || semantic.RequestReplay || !semantic.SemanticDedupe || semantic.Receipt.RequestID != "request-2" {
		t.Fatalf("semantic dedupe = %+v err=%v", semantic, err)
	}
	changed := entry
	changed.Description = "changed"
	if _, err := store.RegisterWithReceipt(ctx, changed, "request-3", "mio", "hash-3"); !errors.Is(err, ErrToolRegistryEntryConflict) {
		t.Fatalf("entry conflict err=%v", err)
	}
	if receipt, found, err := store.FindRequestReceipt(ctx, "request-3"); err != nil || found {
		t.Fatalf("conflicting request receipt = %+v found=%v err=%v", receipt, found, err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewSQLiteToolRegistryStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	reopenedReplay, err := reopened.RegisterWithReceipt(ctx, entry, "request-1", "mio", "hash-1")
	if err != nil || !reopenedReplay.RequestReplay {
		t.Fatalf("reopened replay = %+v err=%v", reopenedReplay, err)
	}
	got, found, err := reopened.FindRequestReceipt(ctx, "request-2")
	if err != nil || !found || got.ActorID != "shiro" || got.ToolName != entry.Name || got.PayloadHash != "hash-2" {
		t.Fatalf("semantic receipt = %+v found=%v err=%v", got, found, err)
	}
}

func TestLegacyToolRegistryRowsRemainReadableAfterReceiptMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-tool-registry.db")
	store, err := NewSQLiteToolRegistryStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	legacy := capability.ToolEntry{
		Name: "legacy_tool", Description: "legacy", SchemaJSON: `{}`, Platforms: []string{"linux"},
		Source: capability.ToolSourceBuiltin, CreatedBy: "builtin", CreatedAt: time.Now().UTC(),
	}
	if err := store.Register(context.Background(), legacy); err != nil {
		t.Fatalf("legacy Register: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewSQLiteToolRegistryStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Get(context.Background(), legacy.Name)
	if err != nil || got.Name != legacy.Name || got.Description != legacy.Description || got.Source != legacy.Source {
		t.Fatalf("legacy row = %+v err=%v", got, err)
	}
	if receipt, found, err := reopened.FindRequestReceipt(context.Background(), "missing"); err != nil || found || receipt.RequestID != "" {
		t.Fatalf("missing receipt = %+v found=%v err=%v", receipt, found, err)
	}
}
