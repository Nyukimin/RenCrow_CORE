package personrelatedcatalog

import (
	"context"
	"database/sql"
	"testing"
)

func TestEnsureHobbySchemaPreservesCurrentSummaryIndexPages(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatalf("initial schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO hobby_summary_jobs(category,item_id,state,next_attempt_at) VALUES('drama','item-1','pending','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed summary job: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO hobby_item_summaries(category,item_id,source,source_status,translation_status,source_record_id,canonical_url,retrieved_at,expires_at) VALUES('drama','item-1','source-1','ready','ready','','','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed item summary: %v", err)
	}
	before := hobbySchemaIndexSnapshot(t, db)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatalf("repeat schema: %v", err)
	}
	after := hobbySchemaIndexSnapshot(t, db)
	if before.schemaVersion != after.schemaVersion {
		t.Errorf("schema_version changed on unchanged schema: %d -> %d", before.schemaVersion, after.schemaVersion)
	}
	for name, beforeRoot := range before.rootpages {
		if after.rootpages[name] != beforeRoot {
			t.Errorf("index %s rootpage changed on unchanged schema: %d -> %d", name, beforeRoot, after.rootpages[name])
		}
	}
	assertCount(t, db, "hobby_summary_jobs", 1)
	assertCount(t, db, "hobby_item_summaries", 1)
	var dueJobs, expiringSummaries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hobby_summary_jobs INDEXED BY idx_hobby_summary_jobs_due WHERE state='pending' AND next_attempt_at<='2026-01-01T00:00:00Z'`).Scan(&dueJobs); err != nil {
		t.Fatalf("query due jobs through preserved index: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM hobby_item_summaries INDEXED BY idx_hobby_item_summaries_expiry WHERE source_status='ready' AND expires_at>='2027-01-01T00:00:00Z'`).Scan(&expiringSummaries); err != nil {
		t.Fatalf("query expiry summaries through preserved index: %v", err)
	}
	if dueJobs != 1 || expiringSummaries != 1 {
		t.Fatalf("indexed query results changed: due_jobs=%d summaries=%d", dueJobs, expiringSummaries)
	}
}

func TestEnsureHobbySchemaRepairsMissingOrStaleSummaryIndex(t *testing.T) {
	tests := []struct {
		name  string
		index hobbyIndexDefinition
		ddl   string
	}{
		{name: "missing_due", index: hobbySummaryJobsDueIndex},
		{name: "missing_expiry", index: hobbyItemSummariesExpiryIndex},
		{name: "stale_due_descending", index: hobbySummaryJobsDueIndex, ddl: "CREATE INDEX idx_hobby_summary_jobs_due ON hobby_summary_jobs(state DESC, next_attempt_at, category, item_id)"},
		{name: "stale_due_unique", index: hobbySummaryJobsDueIndex, ddl: "CREATE UNIQUE INDEX idx_hobby_summary_jobs_due ON hobby_summary_jobs(state, next_attempt_at, category, item_id)"},
		{name: "stale_expiry_partial", index: hobbyItemSummariesExpiryIndex, ddl: "CREATE INDEX idx_hobby_item_summaries_expiry ON hobby_item_summaries(source_status, expires_at, category, item_id, source) WHERE source_status='ready'"},
		{name: "stale_expiry_collation", index: hobbyItemSummariesExpiryIndex, ddl: "CREATE INDEX idx_hobby_item_summaries_expiry ON hobby_item_summaries(source_status COLLATE NOCASE, expires_at, category, item_id, source)"},
		{name: "stale_expiry_expression", index: hobbyItemSummariesExpiryIndex, ddl: "CREATE INDEX idx_hobby_item_summaries_expiry ON hobby_item_summaries(lower(source_status), expires_at, category, item_id, source)"},
		{name: "stale_expiry_column_order", index: hobbyItemSummariesExpiryIndex, ddl: "CREATE INDEX idx_hobby_item_summaries_expiry ON hobby_item_summaries(expires_at, source_status, category, item_id, source)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t)
			if err := EnsureHobbySchema(ctx, db); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO hobby_summary_jobs(category,item_id,state,next_attempt_at) VALUES('drama','item-1','pending','2026-01-01T00:00:00Z')"); err != nil {
				t.Fatalf("seed summary job: %v", err)
			}
			if _, err := db.Exec("INSERT INTO hobby_item_summaries(category,item_id,source,source_status,translation_status,source_record_id,canonical_url,retrieved_at,expires_at) VALUES('drama','item-1','source-1','ready','ready','','','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z')"); err != nil {
				t.Fatalf("seed item summary: %v", err)
			}
			before := hobbySchemaIndexSnapshot(t, db)
			if _, err := db.Exec("DROP INDEX " + test.index.name); err != nil {
				t.Fatalf("drop current test index: %v", err)
			}
			if test.ddl != "" {
				if _, err := db.Exec(test.ddl); err != nil {
					t.Fatalf("install stale test index: %v", err)
				}
			}
			var brokenSchemaVersion int
			if err := db.QueryRow(`PRAGMA schema_version`).Scan(&brokenSchemaVersion); err != nil {
				t.Fatal(err)
			}
			if err := EnsureHobbySchema(ctx, db); err != nil {
				t.Fatalf("repair schema index: %v", err)
			}
			var schemaVersion int
			if err := db.QueryRow(`PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
				t.Fatal(err)
			}
			wantSchemaVersion := brokenSchemaVersion + 1
			if test.ddl != "" {
				wantSchemaVersion++
			}
			if schemaVersion != wantSchemaVersion {
				t.Fatalf("unexpected schema_version repair delta for %s: broken=%d after=%d want=%d", test.index.name, brokenSchemaVersion, schemaVersion, wantSchemaVersion)
			}
			var repairedSQL string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, test.index.name).Scan(&repairedSQL); err != nil {
				t.Fatalf("read repaired index SQL: %v", err)
			}
			if repairedSQL != before.indexSQL[test.index.name] {
				t.Fatalf("repaired index SQL differs from correct initial definition: got=%q want=%q", repairedSQL, before.indexSQL[test.index.name])
			}
			otherIndex := hobbySummaryJobsDueIndex
			if test.index.name == otherIndex.name {
				otherIndex = hobbyItemSummariesExpiryIndex
			}
			if got := indexRootpage(t, db, otherIndex.name); got != before.rootpages[otherIndex.name] {
				t.Errorf("unmodified sibling index %s rootpage changed: %d -> %d", otherIndex.name, before.rootpages[otherIndex.name], got)
			}
			var siblingSQL string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, otherIndex.name).Scan(&siblingSQL); err != nil {
				t.Fatalf("read sibling index SQL: %v", err)
			}
			if siblingSQL != before.indexSQL[otherIndex.name] {
				t.Errorf("unmodified sibling index %s SQL changed: %q -> %q", otherIndex.name, before.indexSQL[otherIndex.name], siblingSQL)
			}
			assertCount(t, db, "hobby_summary_jobs", 1)
			assertCount(t, db, "hobby_item_summaries", 1)
		})
	}
}

func TestEnsureHobbySchemaSummaryIndexFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE hobby_summary_jobs (category TEXT NOT NULL, state TEXT NOT NULL, next_attempt_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_hobby_summary_jobs_due ON hobby_summary_jobs(category)`); err != nil {
		t.Fatal(err)
	}
	beforeDueRootpage := indexRootpage(t, db, "idx_hobby_summary_jobs_due")
	if err := EnsureHobbySchema(ctx, db); err == nil {
		t.Fatal("EnsureHobbySchema succeeded despite due-index column missing from malformed table")
	}
	var afterFailureVersion int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&afterFailureVersion); err != nil {
		t.Fatal(err)
	}
	if afterFailureVersion != 2 {
		t.Errorf("failed migration did not roll back schema changes: schema_version=%d, want 2", afterFailureVersion)
	}
	var dueSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_hobby_summary_jobs_due'`).Scan(&dueSQL); err != nil {
		t.Fatal(err)
	}
	if dueSQL != "CREATE INDEX idx_hobby_summary_jobs_due ON hobby_summary_jobs(category)" {
		t.Errorf("failed migration did not restore old due index: %s", dueSQL)
	}
	var dueRootpage int
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE type='index' AND name='idx_hobby_summary_jobs_due'`).Scan(&dueRootpage); err != nil {
		t.Fatal(err)
	}
	if dueRootpage != beforeDueRootpage {
		t.Errorf("old due index rootpage was not restored: %d -> %d", beforeDueRootpage, dueRootpage)
	}
	var itemIDPresent int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('hobby_summary_jobs') WHERE name='item_id'`).Scan(&itemIDPresent); err != nil {
		t.Fatal(err)
	}
	if itemIDPresent != 0 {
		t.Errorf("failed migration retained item_id column addition: count=%d", itemIDPresent)
	}
	var migratedColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('hobby_summary_jobs') WHERE name IN ('source_cursor','last_reason')`).Scan(&migratedColumns); err != nil {
		t.Fatal(err)
	}
	if migratedColumns != 0 {
		t.Errorf("failed migration retained legacy column additions: count=%d", migratedColumns)
	}
	var otherTablePresent int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='hobby_item_summaries'`).Scan(&otherTablePresent); err != nil {
		t.Fatal(err)
	}
	if otherTablePresent != 0 {
		t.Errorf("failed migration retained earlier table creation: count=%d", otherTablePresent)
	}
}

func indexRootpage(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var rootpage int
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&rootpage); err != nil {
		t.Fatalf("read index rootpage %s: %v", name, err)
	}
	return rootpage
}

type hobbyIndexSnapshot struct {
	schemaVersion int
	rootpages     map[string]int
	indexSQL      map[string]string
}

func hobbySchemaIndexSnapshot(t *testing.T, db *sql.DB) hobbyIndexSnapshot {
	t.Helper()
	var snapshot hobbyIndexSnapshot
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&snapshot.schemaVersion); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	snapshot.rootpages = make(map[string]int, 2)
	snapshot.indexSQL = make(map[string]string, 2)
	rows, err := db.Query(`SELECT name,rootpage,sql FROM sqlite_master WHERE type='index' AND name IN (?,?)`,
		"idx_hobby_summary_jobs_due", "idx_hobby_item_summaries_expiry")
	if err != nil {
		t.Fatalf("read summary index rootpages: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var rootpage int
		var statement string
		if err := rows.Scan(&name, &rootpage, &statement); err != nil {
			t.Fatalf("scan summary index rootpage: %v", err)
		}
		snapshot.rootpages[name] = rootpage
		snapshot.indexSQL[name] = statement
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read summary index rootpages: %v", err)
	}
	if len(snapshot.rootpages) != 2 || len(snapshot.indexSQL) != 2 {
		t.Fatalf("found %d summary indexes, want 2: %#v", len(snapshot.rootpages), snapshot.rootpages)
	}
	return snapshot
}
