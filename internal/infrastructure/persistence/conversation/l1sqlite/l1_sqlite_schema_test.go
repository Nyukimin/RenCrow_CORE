package l1sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureL1UserMemoryViewerProjectionSkipsSourceWhenProjectionIsPopulated(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	createViewerProjectionFixture(t, db)
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO l1_user_memory_viewer_projection (
	id, namespace, user_id, memory_type, memory_state, active, statement, evidence_text,
	confidence, sensitivity, scope, lifecycle_status, decay_score, superseded_by, created_at, updated_at
) VALUES ('memory-1', 'user:1', '1', 'preference', 'confirmed', 1, 'tea', '[]', 0.9, 'normal', 'all_personas', '', 0, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
`); err != nil {
		t.Fatalf("insert projection fixture: %v", err)
	}

	store := &L1SQLiteStore{db: db}
	if err := store.ensureL1UserMemoryViewerProjection(context.Background()); err != nil {
		t.Fatalf("populated projection must not require l1_memory_event: %v", err)
	}
}

func TestEnsureL1UserMemoryViewerProjectionBackfillsOnlyWhenProjectionIsEmpty(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	createViewerProjectionFixture(t, db)
	store := &L1SQLiteStore{db: db}
	if err := store.ensureL1UserMemoryViewerProjection(context.Background()); err == nil {
		t.Fatal("empty projection must attempt backfill and fail when l1_memory_event is absent")
	} else if !strings.Contains(err.Error(), "l1_memory_event") {
		t.Fatalf("empty projection error = %v, want l1_memory_event boundary", err)
	}
}

func TestEnsureL1UserMemoryViewerProjectionBackfillsMatchingMemoryEvents(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	createViewerProjectionFixture(t, db)
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE l1_memory_event (
	id TEXT PRIMARY KEY,
	namespace TEXT NOT NULL,
	session_id TEXT NOT NULL,
	thread_id INTEGER NOT NULL,
	speaker TEXT NOT NULL,
	message TEXT NOT NULL,
	meta_json TEXT NOT NULL DEFAULT '{}',
	memory_state TEXT NOT NULL,
	layer TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (
	'event-1', 'user:1', 'session-1', 1, 'memory', 'tea',
	'{"user_id":"1","type":"preference","statement":"tea"}',
	'confirmed', 'L1', 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
);
`); err != nil {
		t.Fatalf("create memory event fixture: %v", err)
	}

	store := &L1SQLiteStore{db: db}
	if err := store.ensureL1UserMemoryViewerProjection(context.Background()); err != nil {
		t.Fatalf("empty projection backfill: %v", err)
	}
	var statement string
	if err := db.QueryRowContext(context.Background(), `SELECT statement FROM l1_user_memory_viewer_projection WHERE id = 'event-1'`).Scan(&statement); err != nil {
		t.Fatalf("backfilled projection: %v", err)
	}
	if statement != "tea" {
		t.Fatalf("backfilled statement=%q, want tea", statement)
	}
	var ftsRows int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM l1_user_memory_viewer_fts`).Scan(&ftsRows); err != nil {
		t.Fatalf("backfilled FTS projection: %v", err)
	}
	if ftsRows != 1 {
		t.Fatalf("backfilled FTS rows=%d, want 1", ftsRows)
	}
}

func createViewerProjectionFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE l1_user_memory_viewer_projection (
	id TEXT PRIMARY KEY,
	namespace TEXT NOT NULL,
	user_id TEXT NOT NULL,
	memory_type TEXT NOT NULL,
	memory_state TEXT NOT NULL,
	active INTEGER NOT NULL,
	statement TEXT NOT NULL,
	evidence_text TEXT NOT NULL,
	confidence REAL NOT NULL,
	sensitivity TEXT NOT NULL,
	scope TEXT NOT NULL,
	lifecycle_status TEXT NOT NULL,
	decay_score REAL NOT NULL,
	superseded_by TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE VIRTUAL TABLE l1_user_memory_viewer_fts
	USING fts5(id UNINDEXED, statement, evidence_text, tokenize='trigram');
`); err != nil {
		t.Fatalf("create projection fixture: %v", err)
	}
}
