package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

// TestRecentEventsIsDeterministicWithIdenticalTimestamps は同一タイムスタンプの
// レコードでも取得順序が挿入順に従うことを確認する
//
// ORDER BY が時刻カラムのみで一意な tiebreak を持たない場合、同一
// タイムスタンプのレコードの順序は SQLite の内部都合で決まり不定になる。
// クロック粒度が粗い環境（Windowsでは100ns〜1ms程度）では、連続して
// 記録したイベントが同一タイムスタンプになるため実際に発生する。
//
// このテストはクロック粒度に依存せず、タイムスタンプを明示的に揃えて
// 再現する。
func TestRecentEventsIsDeterministicWithIdenticalTimestamps(t *testing.T) {
	ctx := context.Background()
	store := newOrderTestStore(t)

	const namespace = "conv:order-stability"
	types := []string{"first.event", "second.event", "third.event", "fourth.event"}
	for _, eventType := range types {
		if _, err := store.AppendEvent(ctx, eventType, namespace, "session-order", 1, map[string]interface{}{"n": eventType}, "test"); err != nil {
			t.Fatalf("AppendEvent(%s) failed: %v", eventType, err)
		}
	}

	// 全レコードのタイムスタンプを揃えて、時刻では順序が決まらない状態にする
	fixed := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_event_log SET created_at = ? WHERE namespace = ?`, fixed, namespace); err != nil {
		t.Fatalf("normalize created_at failed: %v", err)
	}

	events, err := store.RecentEvents(ctx, namespace, 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != len(types) {
		t.Fatalf("expected %d events, got %d", len(types), len(events))
	}

	// RecentEvents は新しい順。挿入順の逆になっていること
	for i, event := range events {
		want := types[len(types)-1-i]
		if event.EventType != want {
			t.Fatalf("events[%d].EventType = %q, want %q (order is not deterministic)", i, event.EventType, want)
		}
	}
}

// TestListUserMemoriesIsDeterministicWithIdenticalTimestamps は user memory の
// 取得順序が同一タイムスタンプでも安定することを確認する
func TestListUserMemoriesIsDeterministicWithIdenticalTimestamps(t *testing.T) {
	ctx := context.Background()
	store := newOrderTestStore(t)

	const userID = "order-stability-user"
	statements := []string{"first statement", "second statement", "third statement"}
	for _, statement := range statements {
		if _, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
			UserID:    userID,
			Type:      domainmemory.UserMemoryTypePreference,
			Statement: statement,
			Source:    "test",
		}); err != nil {
			t.Fatalf("CreateUserMemory(%s) failed: %v", statement, err)
		}
	}

	fixed := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET created_at = ?`, fixed); err != nil {
		t.Fatalf("normalize created_at failed: %v", err)
	}

	items, err := store.ListUserMemories(ctx, userID, "", true, 10)
	if err != nil {
		t.Fatalf("ListUserMemories failed: %v", err)
	}
	if len(items) != len(statements) {
		t.Fatalf("expected %d items, got %d", len(statements), len(items))
	}
	for i, item := range items {
		want := statements[len(statements)-1-i]
		if item.Statement != want {
			t.Fatalf("items[%d].Statement = %q, want %q (order is not deterministic)", i, item.Statement, want)
		}
	}
}

func newOrderTestStore(t *testing.T) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
