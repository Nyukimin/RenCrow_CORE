package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

func TestChatGPTConversationIdentityIsDeterministicAndSeparate(t *testing.T) {
	firstSession, firstThread, err := chatGPTConversationIdentity("conversation-1")
	if err != nil {
		t.Fatalf("chatGPTConversationIdentity() error = %v", err)
	}
	secondSession, secondThread, err := chatGPTConversationIdentity("conversation-1")
	if err != nil {
		t.Fatalf("second chatGPTConversationIdentity() error = %v", err)
	}
	if firstSession != secondSession || firstThread != secondThread {
		t.Fatalf("identity is not deterministic: first=%s/%s second=%s/%s", firstSession, firstThread, secondSession, secondThread)
	}
	if err := firstSession.Validate(); err != nil {
		t.Fatalf("session ID is not canonical: %v", err)
	}
	if err := firstThread.Validate(); err != nil {
		t.Fatalf("thread ID is not canonical: %v", err)
	}
	if string(firstSession) == string(firstThread) {
		t.Fatalf("session and thread IDs must differ: %q", firstSession)
	}
}

func TestChatGPTConversationIdentityPropagatesConstructorErrors(t *testing.T) {
	_, _, err := chatGPTConversationIdentity("")
	if err == nil || !strings.Contains(err.Error(), "session ID") {
		t.Fatalf("chatGPTConversationIdentity(empty) error = %v, want contextual session ID error", err)
	}
}

func TestArchiveThreadTupleValidationRejectsLegacyAndMalformedRows(t *testing.T) {
	validRaw, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "archive_test", "thread_id", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	valid := archiveThreadIdentity{
		threadID:   modulecore.ThreadID(validRaw),
		threadSeq:  1,
		threadKind: modulecore.ThreadKindUserConversation,
	}
	if err := validateArchiveThreadIdentity(valid); err != nil {
		t.Fatalf("valid archive tuple rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*archiveThreadIdentity)
		want   string
	}{
		{name: "numeric legacy id", mutate: func(identity *archiveThreadIdentity) { identity.threadID = "123" }, want: "thread_id"},
		{name: "missing sequence", mutate: func(identity *archiveThreadIdentity) { identity.threadSeq = 0 }, want: "thread_seq"},
		{name: "wrong kind", mutate: func(identity *archiveThreadIdentity) { identity.threadKind = "wrong" }, want: "thread_kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identity := valid
			tc.mutate(&identity)
			if err := validateArchiveThreadIdentity(identity); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateArchiveThreadIdentity() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBackfillArchiveThreadsReadsAndValidatesCanonicalTuple(t *testing.T) {
	threadRaw, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "archive_test", "thread_id", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "archive.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE session_thread (
  thread_id TEXT NOT NULL,
  thread_seq INTEGER NOT NULL,
  thread_kind TEXT NOT NULL,
  session_id TEXT NOT NULL,
  ts_start TIMESTAMP NOT NULL,
  ts_end TIMESTAMP NOT NULL,
  domain TEXT NOT NULL,
  summary TEXT NOT NULL,
  keywords TEXT NOT NULL,
  is_novel BOOLEAN NOT NULL
)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, err = db.Exec(`INSERT INTO session_thread (thread_id, thread_seq, thread_kind, session_id, ts_start, ts_end, domain, summary, keywords, is_novel) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		threadRaw, 3, modulecore.ThreadKindDocument, "ses_archive", now, now.Add(time.Minute), "docs", "archive summary", "[]", false)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	count, err := backfillArchiveThreads(context.Background(), dbPath, nil, nil, true, 0)
	if err != nil || count != 1 {
		t.Fatalf("valid archive backfill count=%d err=%v, want 1", count, err)
	}

	db, err = sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE session_thread SET thread_id = ?`, "123")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if count, err := backfillArchiveThreads(context.Background(), dbPath, nil, nil, true, 0); err == nil || count != 0 || !strings.Contains(err.Error(), "thread_id") {
		t.Fatalf("legacy archive backfill count=%d err=%v, want thread_id validation failure", count, err)
	}
}
