package personrelatedcatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEligiblePeopleUsesOnlyExplicitPositiveAssessment(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	people, err := EligiblePeople(ctx, movieDB, 1000)
	if err != nil {
		t.Fatalf("EligiblePeople: %v", err)
	}
	if got, want := len(people), 2; got != want {
		t.Fatalf("eligible people = %d, want %d: %#v", got, want, people)
	}
	if people[0].MovieCatalogPersonID != "p-known" || people[1].MovieCatalogPersonID != "p-like" {
		t.Fatalf("eligible people = %#v, want p-known and p-like", people)
	}
	if _, err := EligiblePeople(ctx, movieDB, 1001); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("EligiblePeople over limit error = %v, want ErrInvalidLimit", err)
	}
	person, found, err := EligiblePersonByID(ctx, movieDB, "p-known")
	if err != nil || !found || person.MovieCatalogPersonID != "p-known" {
		t.Fatalf("EligiblePersonByID positive = %#v found=%t err=%v", person, found, err)
	}
	if _, found, err := EligiblePersonByID(ctx, movieDB, "p-unknown"); err != nil || found {
		t.Fatalf("EligiblePersonByID negative found=%t err=%v", found, err)
	}
}

func TestImportAndLookupIsIdempotent(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	artifact := validArtifact(t)
	hash := sha256.Sum256(artifact)
	sha := hex.EncodeToString(hash[:])
	first, err := Import(ctx, hobbyDB, artifact, sha, int64(len(artifact)))
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	second, err := Import(ctx, hobbyDB, artifact, sha, int64(len(artifact)))
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if first.ItemCount != 1 || first.RelationCount != 1 || second.ItemCount != 1 || second.RelationCount != 1 {
		t.Fatalf("import results = %#v / %#v", first, second)
	}
	hobbyDB.SetMaxOpenConns(1)

	rows, err := Lookup(ctx, hobbyDB, "p-known", "drama", 50)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got, want := len(rows), 1; got != want {
		t.Fatalf("lookup rows = %d, want %d: %#v", got, want, rows)
	}
	if rows[0].DisplayName != "日本語作品" || rows[0].NameState != "source_ja" {
		t.Fatalf("lookup row = %#v", rows[0])
	}
	if rows[0].SummaryJA != "" || rows[0].SummaryState != "unavailable" || rows[0].SummarySourceURL != "" {
		t.Fatalf("summary must not leak when artifact has no description: %#v", rows[0])
	}
	lookup, err := LookupWithCoverage(ctx, hobbyDB, "p-known", "drama", 50)
	if err != nil {
		t.Fatalf("LookupWithCoverage: %v", err)
	}
	if lookup.SummaryCoverage != (SummaryCoverage{Ready: 0, Unavailable: 1, Total: 1}) {
		t.Fatalf("summary coverage = %#v", lookup.SummaryCoverage)
	}
	assertCount(t, hobbyDB, "hobby_related_items", 1)
	assertCount(t, hobbyDB, "hobby_person_relations", 1)
	assertCount(t, hobbyDB, "hobby_collection_receipts", 1)
}

func TestImportAndLookupProjectsJapaneseSummaryAndCoverage(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	artifact := []byte(strings.Replace(
		string(validArtifact(t)),
		`"description_translation_state":"not_attempted"`,
		`"description_original":"日本語の概要","description_language":"ja","description_ja":"日本語の概要","description_translation_state":"not_required"`,
		1,
	))
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); err != nil {
		t.Fatalf("Import: %v", err)
	}
	result, err := LookupWithCoverage(ctx, hobbyDB, "p-known", "drama", 50)
	if err != nil {
		t.Fatalf("LookupWithCoverage: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %#v", result.Items)
	}
	item := result.Items[0]
	if item.SummaryJA != "日本語の概要" || item.SummaryState != "source_summary" || item.SummarySourceURL != "https://example.test/title/1" {
		t.Fatalf("summary projection = %#v", item)
	}
	if result.SummaryCoverage != (SummaryCoverage{Ready: 1, Unavailable: 0, Total: 1}) {
		t.Fatalf("summary coverage = %#v", result.SummaryCoverage)
	}
}

func TestArtifactSummaryValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
		valid  bool
	}{
		{
			name: "foreign translated",
			mutate: func(value string) string {
				return strings.Replace(value, `"description_translation_state":"not_attempted"`, `"description_original":"An English summary","description_language":"en","description_ja":"英語概要","description_translation_state":"ready"`, 1)
			},
			valid: true,
		},
		{
			name: "unknown state",
			mutate: func(value string) string {
				return strings.Replace(value, `"description_translation_state":"not_attempted"`, `"description_original":"概要","description_language":"ja","description_ja":"概要","description_translation_state":"unknown"`, 1)
			},
		},
		{
			name: "Japanese mismatch",
			mutate: func(value string) string {
				return strings.Replace(value, `"description_translation_state":"not_attempted"`, `"description_original":"概要","description_language":"ja","description_ja":"別概要","description_translation_state":"not_required"`, 1)
			},
		},
		{
			name: "ready without Japanese",
			mutate: func(value string) string {
				return strings.Replace(value, `"description_translation_state":"not_attempted"`, `"description_original":"An English summary","description_language":"en","description_translation_state":"ready"`, 1)
			},
		},
		{
			name: "Japanese without original",
			mutate: func(value string) string {
				return strings.Replace(value, `"description_translation_state":"not_attempted"`, `"description_language":"ja","description_ja":"概要","description_translation_state":"not_required"`, 1)
			},
		},
		{
			name: "foreign failed does not expose Japanese",
			mutate: func(value string) string {
				return strings.Replace(value, `"description_translation_state":"not_attempted"`, `"description_original":"An English summary","description_language":"en","description_translation_state":"failed"`, 1)
			},
			valid: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			movieDB := openTestDB(t)
			hobbyDB := openTestDB(t)
			setupAssessmentFixture(t, movieDB)
			if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
				t.Fatal(err)
			}
			artifact := []byte(tt.mutate(string(validArtifact(t))))
			hash := sha256.Sum256(artifact)
			_, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact)))
			if tt.valid && err != nil {
				t.Fatalf("valid summary artifact rejected: %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("invalid summary artifact error = %v, want ErrInvalidArtifact", err)
			}
		})
	}
}

func TestImportRejectsHashBytesCountsAndGeneratedName(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	artifact := validArtifact(t)
	hash := sha256.Sum256(artifact)
	sha := hex.EncodeToString(hash[:])
	if _, err := Import(ctx, hobbyDB, artifact, "00"+sha[2:], int64(len(artifact))); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("hash mismatch error = %v, want ErrIntegrity", err)
	}
	if _, err := Import(ctx, hobbyDB, artifact, sha, int64(len(artifact)+1)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("byte mismatch error = %v, want ErrIntegrity", err)
	}
	badCount := []byte(strings.Replace(string(artifact), `"item_count":1`, `"item_count":2`, 1))
	badHash := sha256.Sum256(badCount)
	if _, err := Import(ctx, hobbyDB, badCount, hex.EncodeToString(badHash[:]), int64(len(badCount))); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("count mismatch error = %v, want ErrInvalidArtifact", err)
	}
	badName := []byte(strings.Replace(string(artifact), `"name_state":"source_ja"`, `"name_state":"translated_ja"`, 1))
	badNameHash := sha256.Sum256(badName)
	if _, err := Import(ctx, hobbyDB, badName, hex.EncodeToString(badNameHash[:]), int64(len(badName))); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("generated name error = %v, want ErrInvalidArtifact", err)
	}
	assertCount(t, hobbyDB, "hobby_collection_receipts", 1)
}

func TestLookupRequiresSchemaAndBoundedLimit(t *testing.T) {
	ctx := context.Background()
	hobbyDB := openTestDB(t)
	if _, err := Lookup(ctx, hobbyDB, "p-known", "drama", 50); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Lookup without schema error = %v, want ErrUnavailable", err)
	}
	if _, err := Lookup(ctx, hobbyDB, "p-known", "movie", 51); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("Lookup over limit error = %v, want ErrInvalidLimit", err)
	}
}

func TestIndexedQueryPlansDoNotScanCatalogTables(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	artifact := validArtifact(t)
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); err != nil {
		t.Fatalf("Import: %v", err)
	}
	assertSearchPlan(t, movieDB, `SELECT target_id FROM movie_catalog_assessments INDEXED BY idx_movie_catalog_assessments_eligible_target WHERE kind='person' AND target_id=? AND (familiarity='known' OR sentiment='like') LIMIT 1`, "p-known", "idx_movie_catalog_assessments_eligible_target")
	assertSearchPlan(t, hobbyDB, `SELECT person_ref_id FROM hobby_person_references INDEXED BY idx_hobby_person_references_movie_catalog_person_id WHERE movie_catalog_person_id=? LIMIT 1`, "p-known", "idx_hobby_person_references_movie_catalog_person_id")
	assertSearchPlan(t, hobbyDB, `SELECT target_item_id FROM hobby_person_relations INDEXED BY idx_hobby_person_relations_person_category_relation WHERE person_ref_id=? AND category=? LIMIT 50`, "ref-1", "drama", "idx_hobby_person_relations_person_category_relation")
}

func assertSearchPlan(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args[:len(args)-1]...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	wantIndex, _ := args[len(args)-1].(string)
	if !strings.Contains(plan, "SEARCH") || !strings.Contains(plan, wantIndex) || strings.Contains(plan, "SCAN") {
		t.Fatalf("query plan must use SEARCH %s without SCAN:\n%s", wantIndex, plan)
	}
}

func TestEnsureSchemaDoesNotCreateAssessmentTable(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	if _, err := movieDB.Exec(`CREATE TABLE people (person_id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL)`); err != nil {
		t.Fatalf("create people table: %v", err)
	}
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if _, err := EligiblePeople(ctx, movieDB, 100); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("EligiblePeople without assessment table error = %v, want ErrUnavailable", err)
	}
}

func TestImportRejectsDuplicateAndMissingProvenanceRecords(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	artifact := validArtifact(t)
	duplicate := append(append([]byte{}, artifact...), []byte(strings.Split(string(artifact), "\n")[2]+"\n")...)
	duplicateHash := sha256.Sum256(duplicate)
	if _, err := Import(ctx, hobbyDB, duplicate, hex.EncodeToString(duplicateHash[:]), int64(len(duplicate))); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("duplicate item error = %v, want ErrInvalidArtifact", err)
	}
	missingEvidence := []byte(strings.Replace(string(artifact), `"evidence_url":"https://example.test/credit/1"`, `"evidence_url":""`, 1))
	missingEvidenceHash := sha256.Sum256(missingEvidence)
	if _, err := Import(ctx, hobbyDB, missingEvidence, hex.EncodeToString(missingEvidenceHash[:]), int64(len(missingEvidence))); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("missing provenance error = %v, want ErrInvalidArtifact", err)
	}
}

func TestImportRejectsSourceThatRequiresCredentialOrApplication(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	artifact := []byte(strings.ReplaceAll(string(validArtifact(t)), "wikidata", "tmdb"))
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("credentialed source error=%v, want ErrInvalidArtifact", err)
	}
}

func TestContractFreeSourceAllowlistUsesOfficialOpenProviders(t *testing.T) {
	allowed := map[string][]string{
		CategoryDrama: {"jpsearch", "wikidata"},
		CategoryAward: {"mediaarts_db", "japan_academy_prize"},
		CategoryMusic: {"musicbrainz", "jpsearch"},
		CategoryAnime: {"mediaarts_db", "jpsearch"},
		CategoryNovel: {"ndl_bibliography", "jpsearch"},
		CategoryManga: {"mediaarts_db", "ndl_bibliography", "jpsearch"},
	}
	for category, sources := range allowed {
		for _, source := range sources {
			if !contractFreeSourceAllowed(category, source) {
				t.Fatalf("source %q must be allowed for %q", source, category)
			}
		}
	}
	for _, source := range []string{"jikan", "tmdb", "openbd"} {
		if contractFreeSourceAllowed(CategoryManga, source) || contractFreeSourceAllowed(CategoryAnime, source) {
			t.Fatalf("non-official or contract source %q must be rejected", source)
		}
	}
}

func validArtifact(t *testing.T) []byte {
	t.Helper()
	lines := []string{
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"manifest","run_id":"run-1","person_ref_id":"ref-1","movie_catalog_person_id":"p-known","category":"drama","source":"wikidata","retrieved_at":"2026-08-12T00:00:00Z","item_count":1,"relation_count":1}`,
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"identity","person_ref_id":"ref-1","movie_catalog_person_id":"p-known","identity_state":"confirmed","external_ids":{"wikidata":"Q123"},"evidence_url":"https://example.test/person/123"}`,
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"item","item_id":"drama-1","category":"drama","item_type":"series","display_name":"日本語作品","name_original":"Original Work","name_ja":"日本語作品","name_state":"source_ja","name_ja_source_url":"https://example.test/title/1","source_record_id":"wikidata:Q1","canonical_url":"https://example.test/title/1","description_translation_state":"not_attempted"}`,
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"relation","relation_id":"rel-1","person_ref_id":"ref-1","category":"drama","target_item_id":"drama-1","relation_type":"出演","source":"wikidata","evidence_url":"https://example.test/credit/1","validation_state":"validated"}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func setupAssessmentFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
CREATE TABLE people (person_id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL);
CREATE TABLE movie_catalog_assessments (
  kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  target_label TEXT NOT NULL,
  familiarity TEXT NOT NULL DEFAULT '',
  sentiment TEXT NOT NULL DEFAULT '',
  updated_by TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(kind, target_id)
);`)
	if err != nil {
		t.Fatalf("create assessment fixture: %v", err)
	}
	for _, person := range []struct {
		id, name, familiarity, sentiment string
	}{
		{"p-known", "Known Person", "known", ""},
		{"p-like", "Liked Person", "", "like"},
		{"p-unknown", "Unknown Person", "unknown", ""},
		{"p-dislike", "Disliked Person", "", "dislike"},
		{"p-empty", "Empty Person", "", ""},
	} {
		if _, err := db.Exec(`INSERT INTO people(person_id,name,url) VALUES(?,?,?)`, person.id, person.name, "https://example.test/person/"+person.id); err != nil {
			t.Fatalf("insert person %s: %v", person.id, err)
		}
		if _, err := db.Exec(`INSERT INTO movie_catalog_assessments(kind,target_id,target_label,familiarity,sentiment,updated_by) VALUES('person',?,?,?,?, 'test')`, person.id, person.name, person.familiarity, person.sentiment); err != nil {
			t.Fatalf("insert assessment %s: %v", person.id, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO people(person_id,name,url) VALUES('p-legacy','Legacy Favorite','https://example.test/person/p-legacy')`); err != nil {
		t.Fatalf("insert legacy person: %v", err)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("count %s = %d, want %d", table, got, want)
	}
}
