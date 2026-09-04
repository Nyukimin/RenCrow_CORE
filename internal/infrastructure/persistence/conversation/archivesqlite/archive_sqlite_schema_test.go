package archivesqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestArchiveSQLiteStoreRejectsLegacyThreadSchemaAtOpen(t *testing.T) {
	tests := []struct {
		name       string
		threadType string
		withTuple  bool
		want       string
	}{
		{name: "numeric thread id and missing tuple", threadType: "BIGINT", want: "thread_id"},
		{name: "missing tuple columns", threadType: "TEXT", withTuple: false, want: "thread_seq"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "legacy.db")
			db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
			if err != nil {
				t.Fatalf("open legacy sqlite: %v", err)
			}
			legacySchema := legacyThreadSchema(tt.threadType, tt.withTuple)
			if _, err := db.ExecContext(context.Background(), legacySchema); err != nil {
				_ = db.Close()
				t.Fatalf("create legacy schema: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close legacy sqlite: %v", err)
			}

			store, err := NewArchiveSQLiteStore(dbPath)
			if err == nil {
				_ = store.Close()
				t.Fatal("legacy archive schema was accepted at open")
			}
			if !strings.Contains(err.Error(), "writer-stopped migration") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("legacy schema error = %v, want writer-stopped migration mentioning %q", err, tt.want)
			}
		})
	}
}

func TestArchiveSQLiteStoreSessionThreadSequenceIsUniquePerSession(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()

	first := &domconv.ThreadSummary{
		ThreadID:   archiveTestThreadID("unique-first"),
		ThreadSeq:  1,
		ThreadKind: domconv.ThreadKindUserConversation,
		SessionID:  archiveTestSessionID("same-session"),
		Summary:    "first summary",
		Keywords:   []string{"one", "two", "three"},
		Roles:      []string{"user"},
	}
	if err := store.SaveThreadSummaryWithReceipt(ctx, first, receiptForRoles(first.Roles)); err != nil {
		t.Fatalf("save first summary: %v", err)
	}
	second := &domconv.ThreadSummary{
		ThreadID:   archiveTestThreadID("unique-second"),
		ThreadSeq:  1,
		ThreadKind: domconv.ThreadKindAgentDiscussion,
		SessionID:  first.SessionID,
		Summary:    "second summary",
		Keywords:   []string{"one", "two", "three"},
		Roles:      []string{"user"},
	}
	if err := store.SaveThreadSummaryWithReceipt(ctx, second, receiptForRoles(second.Roles)); err == nil {
		t.Fatal("duplicate session/thread_seq was accepted")
	}
	got, err := store.GetSessionHistory(ctx, first.SessionID, 10)
	if err != nil {
		t.Fatalf("read session history after rejected duplicate: %v", err)
	}
	if len(got) != 1 || got[0].ThreadID != first.ThreadID || got[0].ThreadSeq != first.ThreadSeq {
		t.Fatalf("unexpected rows after rejected duplicate: %+v", got)
	}
}

func legacyThreadSchema(threadType string, withTuple bool) string {
	threadColumns := "thread_id " + threadType + " PRIMARY KEY NOT NULL,"
	if withTuple {
		threadColumns += " thread_seq INTEGER NOT NULL, thread_kind TEXT NOT NULL,"
	}
	return `
CREATE TABLE session_thread (
        ` + threadColumns + `
        session_id VARCHAR NOT NULL,
        ts_start TIMESTAMP NOT NULL,
        ts_end TIMESTAMP,
        domain VARCHAR,
        summary TEXT,
        keywords TEXT,
        embedding TEXT,
        is_novel BOOLEAN,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE conversation_thread_summary_receipt (
        thread_id ` + threadType + ` PRIMARY KEY NOT NULL,
        schema_version TEXT NOT NULL,
        generation_mode TEXT NOT NULL,
        provider TEXT NOT NULL,
        failure_code TEXT NOT NULL,
        evidence_sha256 TEXT NOT NULL,
        source_turn_count INTEGER NOT NULL,
        roles_json TEXT NOT NULL,
        created_at TIMESTAMP NOT NULL
);
CREATE TABLE l1_memory_event_archive (
        id VARCHAR PRIMARY KEY,
        namespace VARCHAR NOT NULL,
        session_id VARCHAR NOT NULL,
        thread_id ` + threadType + ` NOT NULL,
        speaker VARCHAR NOT NULL,
        message TEXT NOT NULL,
        meta_json TEXT NOT NULL,
        memory_state VARCHAR NOT NULL,
        layer VARCHAR NOT NULL,
        source VARCHAR NOT NULL,
        created_at TIMESTAMP NOT NULL,
        updated_at TIMESTAMP NOT NULL
);`
}
