package l1sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestLoadActiveConversationThreadProjectionUsesReadSnapshotWhileWriterConnectionIsBusy(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "active-thread-read-snapshot.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	request := conversationTurnStoreTestRequest("active-read-snapshot-seed", "active-read-snapshot-session")
	if _, err := store.CommitConversationTurn(context.Background(), request); err != nil {
		t.Fatalf("seed active thread: %v", err)
	}

	writer, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin writer transaction: %v", err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(context.Background(), `UPDATE conversation_active_thread SET updated_at = updated_at WHERE session_id = ?`, request.SessionID); err != nil {
		t.Fatalf("hold writer connection: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	events, err := store.LoadActiveConversationThreadProjection(ctx, request.SessionID)
	if err != nil {
		t.Fatalf("LoadActiveConversationThreadProjection while writer is busy: %v", err)
	}
	if len(events) != 2 || events[0].Message != request.UserMessage || events[1].Message != request.AgentMessage {
		t.Fatalf("active projection=%+v, want the two messages from the committed snapshot", events)
	}
}

func TestLoadActiveConversationThreadProjectionFailsClosedWhenReadPoolUnavailable(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "active-thread-read-unavailable.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	store.readDB.Close()
	store.readDB = nil

	if _, err := store.LoadActiveConversationThreadProjection(context.Background(), "session-read-unavailable"); !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("LoadActiveConversationThreadProjection() error=%v, want unavailable without writer-pool fallback", err)
	}
}
