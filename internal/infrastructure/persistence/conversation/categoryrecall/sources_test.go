package categoryrecall

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	_ "modernc.org/sqlite"
)

type l1SourceStub struct {
	items []l1sqlite.L1KnowledgeItem
}

func (s l1SourceStub) SearchKnowledgeItemsFTS(_ context.Context, _ string, _ string, _ int) ([]l1sqlite.L1KnowledgeItem, error) {
	return append([]l1sqlite.L1KnowledgeItem(nil), s.items...), nil
}

func TestL1KnowledgeSourceProjectsValidatedCategories(t *testing.T) {
	now := time.Now().UTC()
	source := NewL1KnowledgeSource(l1SourceStub{items: []l1sqlite.L1KnowledgeItem{{
		ID: "l1-movie", Domain: "movie", Title: "Catalog fact", SummaryDraft: "A validated fact",
		SourceURL: "https://example.test/l1-movie", UpdatedAt: now,
	}}})
	result, err := source.Search(context.Background(), conversation.CategoryRecallQuery{Category: "movie", Message: "映画", Limit: 3, Time: now})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Records) != 1 || result.Records[0].Category != "movie" || result.Records[0].State != conversation.CategoryRecordStateValidated {
		t.Fatalf("unexpected L1 records: %#v", result.Records)
	}
	if len(result.Records[0].ProvenanceURLs) != 1 {
		t.Fatalf("L1 provenance missing: %#v", result.Records[0])
	}
}

func TestL1KnowledgeSourceAddsExplicitFreshnessForNewsAndInvestment(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	for _, category := range []string{"news", "investment"} {
		source := NewL1KnowledgeSource(l1SourceStub{items: []l1sqlite.L1KnowledgeItem{{
			ID: "l1-" + category, Domain: category, Title: "Validated " + category, SummaryDraft: "Validated summary",
			SourceURL: "https://example.test/" + category, UpdatedAt: now,
		}}})
		result, err := source.Search(context.Background(), conversation.CategoryRecallQuery{Category: category, Message: category, Limit: 3, Time: now})
		if err != nil {
			t.Fatalf("%s Search failed: %v", category, err)
		}
		if len(result.Records) != 1 || !result.Records[0].FreshUntil.Equal(now.Add(DefaultL1CategoryRecallFreshness)) {
			t.Fatalf("%s freshness=%#v, want explicit TTL", category, result.Records)
		}
	}
}

func TestL1KnowledgeSourceDoesNotInventLifecycleTime(t *testing.T) {
	source := NewL1KnowledgeSource(l1SourceStub{items: []l1sqlite.L1KnowledgeItem{{
		ID: "l1-zero", Domain: "movie", Title: "Movie", SummaryDraft: "Summary", SourceURL: "https://example.test/movie",
	}}})
	result, err := source.Search(context.Background(), conversation.CategoryRecallQuery{Category: "movie", Message: "movie", Limit: 3})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Records) != 1 || !result.Records[0].RetrievedAt.IsZero() || !result.Records[0].ValidatedAt.IsZero() {
		t.Fatalf("zero UpdatedAt should remain zero for registry validation: %#v", result.Records)
	}
}

func TestMovieCatalogSourceReadsOnlyPublicCatalogTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "movie.sqlite")
	db := openTestDB(t, dbPath)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE movies(movie_id TEXT PRIMARY KEY, title TEXT, title_lookup_key TEXT NOT NULL, url TEXT, synopsis TEXT, fetched_at TEXT)`)
	mustExec(t, db, `CREATE INDEX idx_movies_title_lookup_key ON movies(title_lookup_key)`)
	mustExec(t, db, `CREATE TABLE people(person_id TEXT PRIMARY KEY, name TEXT, name_lookup_key TEXT NOT NULL, url TEXT, biography TEXT, fetched_at TEXT)`)
	mustExec(t, db, `CREATE INDEX idx_people_name_lookup_key ON people(name_lookup_key)`)
	mustExec(t, db, `CREATE TABLE movie_watch_events(event_id TEXT, movie_id TEXT, note TEXT)`)
	mustExec(t, db, `CREATE TABLE movie_preference_signals(signal_id TEXT, target_id TEXT, evidence_json TEXT)`)
	mustExec(t, db, `INSERT INTO movies VALUES ('m1', 'マトリックス', 'マトリックス', 'https://example.test/m1', '公開カタログ概要', '2026-08-08 00:00:00')`)
	mustExec(t, db, `INSERT INTO people VALUES ('p1', 'キアヌ', 'キアヌ', 'https://example.test/p1', '公開人物概要', '2026-08-08T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO movie_watch_events VALUES ('w1', 'm1', 'PRIVATE WATCH NOTE')`)
	mustExec(t, db, `INSERT INTO movie_preference_signals VALUES ('s1', 'p1', 'PRIVATE PREFERENCE')`)

	source := NewMovieCatalogSource(dbPath)
	movie, err := source.Search(context.Background(), conversation.CategoryRecallQuery{Category: "movie", Message: "マトリックス", Limit: 3})
	if err != nil {
		t.Fatalf("movie Search failed: %v", err)
	}
	if len(movie.Records) != 1 || strings.Contains(movie.Records[0].ToPromptText(), "PRIVATE") {
		t.Fatalf("unexpected movie record: %#v", movie.Records)
	}
	person, err := source.Search(context.Background(), conversation.CategoryRecallQuery{Category: "person", Message: "キアヌ", Limit: 3})
	if err != nil {
		t.Fatalf("person Search failed: %v", err)
	}
	if len(person.Records) != 1 || strings.Contains(person.Records[0].ToPromptText(), "PRIVATE") {
		t.Fatalf("unexpected person record: %#v", person.Records)
	}
	if person.Records[0].RetrievedAt.IsZero() || person.Records[0].ValidatedAt.IsZero() {
		t.Fatalf("movie person fetched_at should provide lifecycle timestamps: %#v", person.Records[0])
	}
}

func TestDramaCategoryIsNotClaimedByMovieCatalog(t *testing.T) {
	for _, category := range NewMovieCatalogSource("unused").Categories() {
		if category == "drama" {
			t.Fatal("movie catalog must not classify every movie row as drama")
		}
	}
}

func TestDramaCategoryComesFromValidatedL1OrHobbyGraph(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	l1 := NewL1KnowledgeSource(l1SourceStub{items: []l1sqlite.L1KnowledgeItem{{
		ID: "l1-drama", Domain: "drama", Title: "Validated drama", SummaryDraft: "Validated drama summary",
		SourceURL: "https://example.test/drama", UpdatedAt: now,
	}}})
	l1Result, err := l1.Search(context.Background(), conversation.CategoryRecallQuery{Category: "drama", Message: "ドラマ", Time: now, Limit: 3})
	if err != nil || len(l1Result.Records) != 1 || l1Result.Records[0].Category != "drama" {
		t.Fatalf("validated L1 drama result=%#v err=%v", l1Result, err)
	}

	dbPath := filepath.Join(t.TempDir(), "hobby-drama.sqlite")
	db := openTestDB(t, dbPath)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE hobby_items(item_id TEXT PRIMARY KEY, category TEXT, item_type TEXT, title TEXT, normalized_title TEXT, canonical_source TEXT, canonical_url TEXT, metadata_json TEXT, updated_at TEXT)`)
	mustExec(t, db, `INSERT INTO hobby_items VALUES ('h-drama', 'drama', 'series', '夜ドラマ', '夜ドラマ', 'public-catalog', 'https://example.test/h-drama', '{}', '2026-08-08 00:00:00')`)
	hobbyResult, err := NewHobbyGraphSource(dbPath).Search(context.Background(), conversation.CategoryRecallQuery{Category: "drama", Message: "ドラマ", Limit: 3})
	if err != nil || len(hobbyResult.Records) != 1 || hobbyResult.Records[0].Category != "drama" {
		t.Fatalf("hobby graph drama result=%#v err=%v", hobbyResult, err)
	}
}

func TestMovieCatalogSourceMissingFetchedAtPreservesInvalidLifecycleTrace(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "movie-no-fetch.sqlite")
	db := openTestDB(t, dbPath)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE movies(movie_id TEXT PRIMARY KEY, title TEXT, title_lookup_key TEXT NOT NULL, url TEXT, synopsis TEXT)`)
	mustExec(t, db, `CREATE INDEX idx_movies_title_lookup_key ON movies(title_lookup_key)`)
	mustExec(t, db, `INSERT INTO movies VALUES ('m1', 'マトリックス', 'マトリックス', 'https://example.test/m1', '公開カタログ概要')`)

	result, err := NewMovieCatalogSource(dbPath).Search(context.Background(), conversation.CategoryRecallQuery{Category: "movie", Message: "マトリックス", Limit: 3})
	if err != nil {
		t.Fatalf("movie Search failed: %v", err)
	}
	if len(result.Records) != 1 || !result.Records[0].RetrievedAt.IsZero() || !result.Records[0].ValidatedAt.IsZero() {
		t.Fatalf("missing fetched_at should remain zero for registry validation: %#v", result.Records)
	}
}

func TestMovieCatalogSourceFiltersBeforeHardLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "movie-large.sqlite")
	db := openTestDB(t, dbPath)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE movies(movie_id TEXT PRIMARY KEY, title TEXT, title_lookup_key TEXT NOT NULL, url TEXT, synopsis TEXT, fetched_at TEXT)`)
	mustExec(t, db, `CREATE INDEX idx_movies_title_lookup_key ON movies(title_lookup_key)`)
	for i := 0; i < 40; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO movies VALUES ('m%02d', 'Common title %02d', 'common title %02d', 'https://example.test/m%02d', 'summary', '2026-08-08T00:00:00Z')", i, i, i, i))
	}
	mustExec(t, db, `INSERT INTO movies VALUES ('target', 'ZZZ target movie', 'zzz target movie', 'https://example.test/target', 'target summary', '2026-08-08T00:00:00Z')`)

	result, err := NewMovieCatalogSource(dbPath).Search(context.Background(), conversation.CategoryRecallQuery{Category: "movie", Message: "ZZZ target movie", Limit: 1})
	if err != nil {
		t.Fatalf("movie Search failed: %v", err)
	}
	if len(result.Records) != 1 || result.Records[0].RecordID != "target" {
		t.Fatalf("lexical predicate should find late target before hard limit: %#v", result.Records)
	}
}

func TestHobbyGraphSourceDoesNotInjectPersonalTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby.sqlite")
	db := openTestDB(t, dbPath)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE hobby_items(item_id TEXT PRIMARY KEY, category TEXT, item_type TEXT, title TEXT, normalized_title TEXT, canonical_source TEXT, canonical_url TEXT, metadata_json TEXT, updated_at TEXT)`)
	mustExec(t, db, `CREATE TABLE hobby_relations(relation_id TEXT, from_item_id TEXT, to_item_id TEXT, relation_type TEXT, source TEXT)`)
	mustExec(t, db, `CREATE TABLE hobby_interactions(interaction_id TEXT, item_id TEXT, note TEXT)`)
	mustExec(t, db, `CREATE TABLE hobby_preference_signals(signal_id TEXT, target_item_id TEXT, evidence_json TEXT)`)
	mustExec(t, db, `INSERT INTO hobby_items VALUES ('h1', 'hobby', 'craft', '陶芸', '陶芸', 'public-catalog', 'https://example.test/h1', '{}', '2026-08-08T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO hobby_interactions VALUES ('i1', 'h1', 'PRIVATE NOTE')`)
	mustExec(t, db, `INSERT INTO hobby_preference_signals VALUES ('p1', 'h1', 'PRIVATE PREFERENCE')`)

	result, err := NewHobbyGraphSource(dbPath).Search(context.Background(), conversation.CategoryRecallQuery{Category: "hobby", Message: "趣味の陶芸", Limit: 3})
	if err != nil {
		t.Fatalf("hobby Search failed: %v", err)
	}
	if len(result.Records) != 1 || strings.Contains(result.Records[0].ToPromptText(), "PRIVATE") {
		t.Fatalf("unexpected hobby record: %#v", result.Records)
	}
}

func TestConfiguredCategorySourcesReportMissingDB(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	if _, err := NewMovieCatalogSource(missing).Search(context.Background(), conversation.CategoryRecallQuery{Category: "movie", Message: "映画"}); err == nil {
		t.Fatal("missing movie DB should be unavailable")
	}
	if _, err := NewHobbyGraphSource(missing).Search(context.Background(), conversation.CategoryRecallQuery{Category: "hobby", Message: "趣味"}); err == nil {
		t.Fatal("missing hobby DB should be unavailable")
	}
}

func TestMovieCatalogSourceOldSchemaOrMissingIndexIsUnavailable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "movie-old.sqlite")
	db := openTestDB(t, dbPath)
	mustExec(t, db, `CREATE TABLE movies(movie_id TEXT PRIMARY KEY,title TEXT,url TEXT,synopsis TEXT)`)
	db.Close()
	if _, err := NewMovieCatalogSource(dbPath).Search(context.Background(), conversation.CategoryRecallQuery{Category: "movie", Message: "Heat"}); err == nil || !strings.Contains(err.Error(), "title_lookup_key") {
		t.Fatalf("old schema should be unavailable: %v", err)
	}

	db = openTestDB(t, dbPath)
	mustExec(t, db, `ALTER TABLE movies ADD COLUMN title_lookup_key TEXT NOT NULL DEFAULT ''`)
	db.Close()
	if _, err := NewMovieCatalogSource(dbPath).Search(context.Background(), conversation.CategoryRecallQuery{Category: "movie", Message: "Heat"}); err == nil || !strings.Contains(err.Error(), "idx_movies_title_lookup_key") {
		t.Fatalf("missing index should be unavailable: %v", err)
	}
}

func TestMovieCatalogStartupEntityHintsNeverOpensDatabase(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	hints, err := NewMovieCatalogSource(missing).StartupEntityHints(context.Background())
	if err != nil || len(hints) != 0 {
		t.Fatalf("startup hints should be empty without DB access: hints=%#v err=%v", hints, err)
	}
}

func TestHobbyGraphSourceParsesSQLiteTimestamp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby-time.sqlite")
	db := openTestDB(t, dbPath)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE hobby_items(item_id TEXT PRIMARY KEY, category TEXT, item_type TEXT, title TEXT, normalized_title TEXT, canonical_source TEXT, canonical_url TEXT, metadata_json TEXT, updated_at TEXT)`)
	mustExec(t, db, `INSERT INTO hobby_items VALUES ('h1', 'hobby', 'craft', '陶芸', '陶芸', 'public-catalog', 'https://example.test/h1', '{}', '2026-08-08 00:00:00')`)

	result, err := NewHobbyGraphSource(dbPath).Search(context.Background(), conversation.CategoryRecallQuery{Category: "hobby", Message: "陶芸", Limit: 3})
	if err != nil {
		t.Fatalf("hobby Search failed: %v", err)
	}
	if len(result.Records) != 1 || result.Records[0].RetrievedAt.IsZero() || result.Records[0].RetrievedAt.UTC().Format("2006-01-02") != "2026-08-08" {
		t.Fatalf("sqlite updated_at should be parsed: %#v", result.Records)
	}
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
