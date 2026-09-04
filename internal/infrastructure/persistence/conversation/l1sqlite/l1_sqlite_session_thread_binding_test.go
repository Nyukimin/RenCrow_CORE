package l1sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type l1SessionThreadNoopResult struct{}

func (l1SessionThreadNoopResult) LastInsertId() (int64, error) { return 1, nil }
func (l1SessionThreadNoopResult) RowsAffected() (int64, error) { return 1, nil }

type l1SessionThreadCountingExecer struct {
	calls int
}

func (e *l1SessionThreadCountingExecer) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	e.calls++
	return l1SessionThreadNoopResult{}, nil
}

// l1TestSessionID keeps ordinary fixtures on the canonical SessionID path while
// preserving the human-readable seed used by each behavior test.
func l1TestSessionID(seed string) string {
	if seed == "" {
		return ""
	}
	if id := modulecore.SessionID(seed); id.Validate() == nil {
		return seed
	}
	raw, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "l1sqlite_test", "session_id", seed)
	if err != nil {
		panic(err)
	}
	return raw
}

func l1SessionThreadTestID(t *testing.T, seed string) string {
	t.Helper()
	raw, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "l1sqlite_session_thread_binding_test", "session_id", seed)
	if err != nil {
		t.Fatalf("NewMigrationID(session) failed: %v", err)
	}
	return raw
}

func l1SessionThreadTestTuple(t *testing.T, seed string) (modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind) {
	t.Helper()
	raw, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "l1sqlite_session_thread_binding_test", "thread_id", seed)
	if err != nil {
		t.Fatalf("NewMigrationID(thread) failed: %v", err)
	}
	return modulecore.ThreadID(raw), 1, modulecore.ThreadKindUserConversation
}

func TestValidateL1SessionThreadTupleBindsCanonicalParent(t *testing.T) {
	sessionID := l1SessionThreadTestID(t, "bound")
	threadID, threadSeq, threadKind := l1SessionThreadTestTuple(t, "bound")

	tests := []struct {
		name       string
		sessionID  string
		threadID   modulecore.ThreadID
		threadSeq  modulecore.ThreadSeq
		threadKind modulecore.ThreadKind
		wantErr    bool
	}{
		{name: "canonical bound", sessionID: sessionID, threadID: threadID, threadSeq: threadSeq, threadKind: threadKind},
		{name: "canonical optional empty", sessionID: sessionID},
		{name: "empty optional", wantErr: false},
		{name: "legacy bound", sessionID: "legacy-session", threadID: threadID, threadSeq: threadSeq, threadKind: threadKind, wantErr: true},
		{name: "legacy optional", sessionID: "legacy-session", wantErr: true},
		{name: "bound empty session", threadID: threadID, threadSeq: threadSeq, threadKind: threadKind, wantErr: true},
		{name: "partial tuple", sessionID: sessionID, threadID: threadID, wantErr: true},
		{name: "whitespace session", sessionID: " ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateL1SessionThreadTuple(tt.sessionID, tt.threadID, tt.threadSeq, tt.threadKind)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateL1SessionThreadTuple() error = %v, wantErr=%t", err, tt.wantErr)
			}
		})
	}
}

func TestL1SessionThreadValidationRejectsBeforeEventDBAccess(t *testing.T) {
	threadID, threadSeq, threadKind := l1SessionThreadTestTuple(t, "event")
	execer := &l1SessionThreadCountingExecer{}

	if _, err := appendL1EventLog(context.Background(), execer, "test.event", "conv:lifecycle", "legacy-session", threadID, threadSeq, threadKind, nil, "test"); err == nil {
		t.Fatal("appendL1EventLog accepted legacy session")
	}
	if execer.calls != 0 {
		t.Fatalf("appendL1EventLog called DB exec %d times after identity rejection", execer.calls)
	}

	if _, err := appendL1EventLog(context.Background(), execer, "test.event", "conv:lifecycle", "", threadID, threadSeq, threadKind, nil, "test"); err == nil {
		t.Fatal("appendL1EventLog accepted a bound tuple without a session")
	}
	if execer.calls != 0 {
		t.Fatalf("appendL1EventLog called DB exec %d times after bound-session rejection", execer.calls)
	}

	if _, err := appendL1EventLog(context.Background(), execer, "test.event", "conv:lifecycle", l1SessionThreadTestID(t, "event"), threadID, 0, "", nil, "test"); err == nil {
		t.Fatal("appendL1EventLog accepted a partial thread tuple")
	}
	if execer.calls != 0 {
		t.Fatalf("appendL1EventLog called DB exec %d times after partial-tuple rejection", execer.calls)
	}

	if _, err := appendL1EventLog(context.Background(), execer, "test.event", "conv:lifecycle", "", "", 0, "", nil, "test"); err != nil {
		t.Fatalf("appendL1EventLog rejected exact empty optional identity: %v", err)
	}
	if execer.calls != 1 {
		t.Fatalf("appendL1EventLog calls = %d, want 1 for valid optional event", execer.calls)
	}
}

func TestValidateL1MemoryEventAndSaveMessageRequireCanonicalSessionForThread(t *testing.T) {
	threadID, threadSeq, threadKind := l1SessionThreadTestTuple(t, "memory")
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	base := L1MemoryEvent{
		ID:          "memory-event",
		Namespace:   "conv:" + string(threadID),
		SessionID:   l1SessionThreadTestID(t, "memory"),
		ThreadID:    threadID,
		ThreadSeq:   threadSeq,
		ThreadKind:  threadKind,
		Speaker:     domconv.SpeakerUser,
		Message:     "message",
		Meta:        map[string]interface{}{},
		MemoryState: MemoryStateObserved,
		Layer:       MemoryLayerL1,
		Source:      "test",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := validateL1MemoryEvent(base); err != nil {
		t.Fatalf("canonical memory event rejected: %v", err)
	}
	base.SessionID = "legacy-session"
	if err := validateL1MemoryEvent(base); err == nil {
		t.Fatal("legacy memory event session was accepted")
	}

	msg := domconv.NewMessage(domconv.SpeakerUser, "message", nil)
	if err := validateL1MessageSaveInput(l1SessionThreadTestID(t, "save"), threadID, threadSeq, threadKind, msg); err != nil {
		t.Fatalf("canonical SaveMessage identity rejected: %v", err)
	}
	if err := validateL1MessageSaveInput("legacy-session", threadID, threadSeq, threadKind, msg); err == nil {
		t.Fatal("legacy SaveMessage session was accepted")
	}
	if err := validateL1MessageSaveInput(l1SessionThreadTestID(t, "save"), "", 0, "", msg); err == nil {
		t.Fatal("SaveMessage accepted an empty thread tuple")
	}
}

func TestL1SQLiteStoreSaveRecallTraceRejectsLegacyWithoutWrites(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	trace := domconv.RecallTrace{
		ResponseID: "response-legacy",
		SessionID:  "legacy-session",
		Role:       "mio",
		CreatedAt:  time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
		Items: []domconv.RecallTraceItem{{
			Layer: "L3", Kind: "user_memory", Summary: "should not persist", Decision: "included", Status: domconv.TraceStatusInjected,
		}},
	}
	if err := store.SaveRecallTrace(context.Background(), trace); err == nil {
		t.Fatal("SaveRecallTrace accepted legacy session")
	}
	for _, table := range []string{"recall_trace", "recall_trace_item", "prompt_injection_event", "l1_event_log"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("legacy SaveRecallTrace wrote %d rows to %s", count, table)
		}
	}
}

func TestL1SQLiteStoreRecentRecallTracesRejectsLegacyFilterBeforeQuery(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatalf("close DB for pre-query guard: %v", err)
	}
	defer store.readDB.Close()
	defer store.progressDB.Close()

	_, err = store.RecentRecallTraces(context.Background(), "legacy-session", 10)
	if err == nil || !strings.Contains(err.Error(), "invalid recall trace session filter") {
		t.Fatalf("RecentRecallTraces() error = %v, want canonical filter rejection", err)
	}
}

func TestL1SQLiteStoreRecentEventsRejectsStoredSessionThreadMismatch(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	threadID, threadSeq, threadKind := l1SessionThreadTestTuple(t, "stored-event")
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`
INSERT INTO l1_event_log (
	id, event_type, namespace, session_id, thread_id, thread_seq, thread_kind, payload_json, source, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"stored-mismatch", "test.event", "conv:lifecycle", "legacy-session", string(threadID), threadSeq, string(threadKind), `{}`, "test", now); err != nil {
		t.Fatalf("insert stored mismatch event: %v", err)
	}
	if _, err := store.RecentEvents(context.Background(), "conv:lifecycle", 10); err == nil {
		t.Fatal("RecentEvents accepted stored legacy session bound to a thread")
	}
}

func TestProfilePromotionRejectsLegacyBatchBeforeStoreMutation(t *testing.T) {
	threadID, threadSeq, threadKind := l1SessionThreadTestTuple(t, "promotion")
	batch := domainmemory.ProfilePromotionBatch{
		LeaseToken: "lease",
		SessionID:  "legacy-session",
		ThreadID:   threadID,
		ThreadSeq:  threadSeq,
		ThreadKind: threadKind,
		Messages: []domainmemory.ProfilePromotionMessage{{
			EventID: "evidence", SessionID: "legacy-session", ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind, Text: "evidence",
		}},
	}
	store := &L1SQLiteStore{}
	validCandidates := []domainmemory.ProfileCandidate{{
		Type: domainmemory.UserMemoryTypeProfile, Statement: "profile", Confidence: 0.8, Sensitivity: "normal", Scope: "all_personas",
	}}
	if _, err := store.CompleteProfilePromotionBatch(context.Background(), batch, validCandidates, "owner", time.Now().UTC()); err == nil {
		t.Fatal("CompleteProfilePromotionBatch accepted legacy batch")
	}
	if err := store.FailProfilePromotionBatch(context.Background(), batch, 5, time.Now().UTC(), "failure"); err == nil {
		t.Fatal("FailProfilePromotionBatch accepted legacy batch")
	}
	if err := store.DeferProfilePromotionBatch(context.Background(), batch, time.Now().UTC()); err == nil {
		t.Fatal("DeferProfilePromotionBatch accepted legacy batch")
	}
}

func TestL1SessionThreadHelperErrorsRemainSpecific(t *testing.T) {
	threadID, _, _ := l1SessionThreadTestTuple(t, "error")
	if err := validateL1SessionThreadTuple("", threadID, 0, ""); err == nil || !strings.Contains(err.Error(), "thread sequence") {
		t.Fatalf("bound tuple error = %v, want tuple validation before parent binding", err)
	}
	if err := validateL1SessionThreadTuple("", threadID, 1, domconv.ThreadKindUserConversation); err == nil || !strings.Contains(err.Error(), "session_id") {
		t.Fatalf("bound empty-session error = %v, want session_id requirement", err)
	}
	if err := validateL1SessionThreadTuple("legacy-session", "", 0, ""); err == nil {
		t.Fatal("legacy optional identity was accepted")
	}
}
