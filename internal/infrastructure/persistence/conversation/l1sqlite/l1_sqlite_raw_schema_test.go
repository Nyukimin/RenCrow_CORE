package l1sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommonRawSchemaMigrationIsIdempotentAndVerifiedOnReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conversation-l1.db")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("initial NewL1SQLiteStore: %v", err)
	}
	ctx := context.Background()
	var version int
	if err := store.db.QueryRowContext(ctx, `SELECT version FROM l1_schema_migrations WHERE migration_name = ?`, commonRawSchemaMigrationName).Scan(&version); err != nil {
		t.Fatalf("migration marker: %v", err)
	}
	if version != commonRawSchemaMigrationVersion {
		t.Fatalf("migration version=%d, want %d", version, commonRawSchemaMigrationVersion)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen NewL1SQLiteStore: %v", err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_raw_source_manifest`).Scan(&count); err != nil {
		t.Fatalf("reopened raw table: %v", err)
	}
	if count != 0 {
		t.Fatalf("reopened raw manifest count=%d, want 0", count)
	}
}

func TestCommonRawSchemaMarkerDoesNotHideMissingObjects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "broken-conversation-l1.db")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("initial NewL1SQLiteStore: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE l1_raw_record`); err != nil {
		t.Fatalf("drop raw record for broken marker fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close broken fixture: %v", err)
	}
	if reopened, err := NewL1SQLiteStore(dbPath); err == nil {
		_ = reopened.Close()
		t.Fatal("marker-only broken schema must not reopen successfully")
	} else if err == sql.ErrNoRows {
		t.Fatalf("unexpected marker lookup result: %v", err)
	}
}

func TestCommonRawSchemaRejectsMarkerlessTableWithMissingRequiredColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stale-conversation-l1.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open stale sqlite: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE l1_raw_projection_receipt (
		projection_receipt_id TEXT PRIMARY KEY,
		projection_type TEXT NOT NULL,
		output_store TEXT NOT NULL,
		output_record_id TEXT NOT NULL,
		revision TEXT NOT NULL,
		input_sha256 TEXT NOT NULL,
		output_sha256 TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		failure_reason TEXT NOT NULL
	)`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create stale projection table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close stale sqlite: %v", err)
	}
	if store, err := NewL1SQLiteStore(dbPath); err == nil {
		_ = store.Close()
		t.Fatal("markerless table with missing raw_record_ids_json must fail closed")
	}
}

func TestCommonRawSchemaHasAppendOnlyTriggersAndPluralProjectionRefs(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, name := range []string{
		"trg_l1_raw_manifest_immutable_update", "trg_l1_raw_manifest_immutable_delete",
		"trg_l1_raw_record_immutable_update", "trg_l1_raw_record_immutable_delete",
		"trg_l1_raw_state_immutable_update", "trg_l1_raw_state_immutable_delete",
		"trg_l1_raw_projection_immutable_update", "trg_l1_raw_projection_immutable_delete",
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("trigger %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("trigger %s count=%d", name, count)
		}
	}
	var projectionSQL string
	if err := store.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'l1_raw_projection_receipt'`).Scan(&projectionSQL); err != nil {
		t.Fatalf("projection schema: %v", err)
	}
	if !containsAll(projectionSQL, "raw_record_ids_json", "failure_reason") {
		t.Fatalf("projection schema missing plural raw IDs or failure reason: %s", projectionSQL)
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
