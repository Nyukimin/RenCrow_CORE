package glossary

import (
	"context"
	"database/sql"
	_ "modernc.org/sqlite"
	"strings"
	"testing"
)

func glossaryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE glossary_items(id TEXT PRIMARY KEY,term TEXT NOT NULL,explanation TEXT NOT NULL,source TEXT NOT NULL,category TEXT NOT NULL,created_at TEXT,updated_at TEXT); CREATE INDEX idx_term ON glossary_items(term); CREATE INDEX idx_category ON glossary_items(category); INSERT INTO glossary_items VALUES('1','Go','language','test','tech','','2026'),('2','go','board game','test','game','','2026'),('3','Tool','definition','test','tech','','2026');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func TestLookupExactTermAndCategory(t *testing.T) {
	db := glossaryDB(t)
	got, err := Lookup(context.Background(), db, LookupRequest{Operation: "define_term", Term: "Go"})
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != "1" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	lower, err := Lookup(context.Background(), db, LookupRequest{Operation: "define_term", Term: "GO"})
	if err != nil || len(lower.Items) != 0 {
		t.Fatalf("exact case contract failed: %#v err=%v", lower, err)
	}
	cat, err := Lookup(context.Background(), db, LookupRequest{Operation: "list_category", Category: "tech", Limit: 1})
	if err != nil || len(cat.Items) != 1 {
		t.Fatalf("cat=%#v err=%v", cat, err)
	}
}
func TestLookupValidationAndMissingIndex(t *testing.T) {
	db := glossaryDB(t)
	for _, req := range []LookupRequest{{Operation: "define_term"}, {Operation: "define_term", Term: "Go", Category: "tech"}, {Operation: "list_category", Category: "tech", Limit: 21}, {Operation: "all"}} {
		if _, err := Lookup(context.Background(), db, req); err == nil {
			t.Fatalf("expected error: %#v", req)
		}
	}
	db.Exec(`DROP INDEX idx_term`)
	if _, err := Lookup(context.Background(), db, LookupRequest{Operation: "define_term", Term: "Go"}); err == nil {
		t.Fatal("missing index accepted")
	}
}
func TestLookupPlansUseNamedIndexes(t *testing.T) {
	db := glossaryDB(t)
	for _, tc := range []struct {
		idx, table, q string
		args          []any
	}{{"idx_term", "glossary_items", `SELECT id FROM glossary_items INDEXED BY idx_term WHERE term=? ORDER BY id LIMIT ?`, []any{"Go", 10}}, {"idx_category", "glossary_items", `SELECT id FROM glossary_items INDEXED BY idx_category WHERE category=? ORDER BY id LIMIT ?`, []any{"tech", 10}}} {
		rows, err := db.Query("EXPLAIN QUERY PLAN "+tc.q, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		plan := ""
		for rows.Next() {
			var a, b, c int
			var d string
			rows.Scan(&a, &b, &c, &d)
			plan += d
		}
		rows.Close()
		if !strings.Contains(plan, "SEARCH "+tc.table) || !strings.Contains(plan, tc.idx) || strings.Contains(plan, "SCAN "+tc.table) {
			t.Fatalf("bad plan: %s", plan)
		}
	}
}
