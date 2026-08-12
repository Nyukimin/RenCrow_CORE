package personrelatedcatalog

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SchemaVersion is the immutable JSONL artifact contract for this catalog.
const SchemaVersion = "rencrow.person-related-catalog.v1"

const (
	CategoryMovie = "movie"
	CategoryDrama = "drama"
	CategoryAward = "award"
	CategoryMusic = "music"
	CategoryAnime = "anime"
	CategoryNovel = "novel"
	CategoryManga = "manga"
)

var validCategories = map[string]struct{}{
	CategoryMovie: {}, CategoryDrama: {}, CategoryAward: {}, CategoryMusic: {},
	CategoryAnime: {}, CategoryNovel: {}, CategoryManga: {},
}

var validHobbyCategories = map[string]struct{}{
	CategoryDrama: {}, CategoryAward: {}, CategoryMusic: {}, CategoryAnime: {},
	CategoryNovel: {}, CategoryManga: {},
}

var contractFreeSourcesByCategory = map[string]map[string]struct{}{
	CategoryDrama: {"eiga.com": {}, "jpsearch": {}, "wikidata": {}, "official_public": {}},
	CategoryAward: {"mediaarts_db": {}, "japan_academy_prize": {}, "wikidata": {}, "official_public": {}},
	CategoryMusic: {"musicbrainz": {}, "jpsearch": {}, "wikidata": {}, "official_public": {}},
	CategoryAnime: {"mediaarts_db": {}, "jpsearch": {}, "wikidata": {}, "official_public": {}},
	CategoryNovel: {"ndl_bibliography": {}, "jpsearch": {}, "wikidata": {}, "official_public": {}},
	CategoryManga: {"mediaarts_db": {}, "ndl_bibliography": {}, "jpsearch": {}, "wikidata": {}, "official_public": {}},
}

var (
	ErrUnavailable     = errors.New("person related catalog unavailable")
	ErrInvalidLimit    = errors.New("person related catalog limit is invalid")
	ErrInvalidArtifact = errors.New("person related catalog artifact is invalid")
	ErrIntegrity       = errors.New("person related catalog artifact integrity check failed")
	ErrConflict        = errors.New("person related catalog record conflicts with existing data")
)

// EligiblePerson is an explicitly assessed movie-catalog person eligible for
// collection. Legacy favorite signals are intentionally not represented.
type EligiblePerson struct {
	MovieCatalogPersonID string `json:"movie_catalog_person_id"`
	Name                 string `json:"name"`
	URL                  string `json:"url"`
	Familiarity          string `json:"familiarity"`
	Sentiment            string `json:"sentiment"`
}

// ImportResult is the receipt projection returned after one category import.
type ImportResult struct {
	RunID                string `json:"run_id"`
	PersonRefID          string `json:"person_ref_id"`
	MovieCatalogPersonID string `json:"movie_catalog_person_id"`
	Category             string `json:"category"`
	Source               string `json:"source"`
	RetrievedAt          string `json:"retrieved_at,omitempty"`
	Status               string `json:"status"`
	ItemCount            int    `json:"item_count"`
	RelationCount        int    `json:"relation_count"`
	ArtifactSHA256       string `json:"artifact_sha256"`
	ArtifactBytes        int64  `json:"artifact_bytes"`
	Error                string `json:"error,omitempty"`
}

// CatalogImportResult is kept as a descriptive alias for callers that use
// the movie-catalog naming convention.
type CatalogImportResult = ImportResult

// RelatedCatalogItem is one indexed person-to-item relation returned by
// Lookup. It deliberately contains only columns covered by named indexes and
// fixed joins; it never exposes arbitrary JSON payload search.
type RelatedCatalogItem struct {
	RelationID           string `json:"relation_id"`
	PersonRefID          string `json:"person_ref_id"`
	MovieCatalogPersonID string `json:"movie_catalog_person_id"`
	Category             string `json:"category"`
	RelationType         string `json:"relation_type"`
	Source               string `json:"source"`
	EvidenceURL          string `json:"evidence_url"`
	ValidationState      string `json:"validation_state"`
	ItemID               string `json:"item_id"`
	ItemType             string `json:"item_type"`
	DisplayName          string `json:"display_name"`
	NameOriginal         string `json:"name_original"`
	NameJA               string `json:"name_ja,omitempty"`
	NameState            string `json:"name_state"`
	NameJASourceURL      string `json:"name_ja_source_url,omitempty"`
	SourceRecordID       string `json:"source_record_id"`
	CanonicalURL         string `json:"canonical_url"`
	SummaryJA            string `json:"summary_ja,omitempty"`
	SummaryState         string `json:"summary_state"`
	SummarySourceURL     string `json:"summary_source_url,omitempty"`
}

// SummaryCoverage is the bounded category-level projection of WorkSummary
// availability. It intentionally counts only the returned relation rows.
type SummaryCoverage struct {
	Ready       int `json:"ready"`
	Unavailable int `json:"unavailable"`
	Total       int `json:"total"`
}

// LookupResult is the category response exposed at the runtime/Tool boundary.
// Lookup remains available for callers that need only the indexed rows.
type LookupResult struct {
	Items           []RelatedCatalogItem `json:"items"`
	SummaryCoverage SummaryCoverage      `json:"summary_coverage"`
}

type artifactManifest struct {
	SchemaVersion        string `json:"schema_version"`
	RecordType           string `json:"record_type"`
	RunID                string `json:"run_id"`
	PersonRefID          string `json:"person_ref_id"`
	MovieCatalogPersonID string `json:"movie_catalog_person_id"`
	Category             string `json:"category"`
	Source               string `json:"source"`
	RetrievedAt          string `json:"retrieved_at"`
	ItemCount            int    `json:"item_count"`
	RelationCount        int    `json:"relation_count"`
}

type artifactIdentity struct {
	SchemaVersion        string            `json:"schema_version"`
	RecordType           string            `json:"record_type"`
	PersonRefID          string            `json:"person_ref_id"`
	MovieCatalogPersonID string            `json:"movie_catalog_person_id"`
	IdentityState        string            `json:"identity_state"`
	ExternalIDs          map[string]string `json:"external_ids"`
	EvidenceURL          string            `json:"evidence_url"`
}

type artifactItem struct {
	SchemaVersion          string  `json:"schema_version"`
	RecordType             string  `json:"record_type"`
	ItemID                 string  `json:"item_id"`
	Category               string  `json:"category"`
	ItemType               string  `json:"item_type"`
	DisplayName            string  `json:"display_name"`
	NameOriginal           string  `json:"name_original"`
	NameJA                 *string `json:"name_ja"`
	NameState              string  `json:"name_state"`
	NameJASourceURL        string  `json:"name_ja_source_url"`
	SourceRecordID         string  `json:"source_record_id"`
	CanonicalURL           string  `json:"canonical_url"`
	Source                 string  `json:"source"`
	DescriptionOriginal    string  `json:"description_original"`
	DescriptionLanguage    string  `json:"description_language"`
	DescriptionJA          string  `json:"description_ja"`
	DescriptionTranslation string  `json:"description_translation_state"`
}

type artifactRelation struct {
	SchemaVersion   string `json:"schema_version"`
	RecordType      string `json:"record_type"`
	RelationID      string `json:"relation_id"`
	PersonRefID     string `json:"person_ref_id"`
	Category        string `json:"category"`
	TargetItemID    string `json:"target_item_id"`
	RelationType    string `json:"relation_type"`
	Source          string `json:"source"`
	EvidenceURL     string `json:"evidence_url"`
	ValidationState string `json:"validation_state"`
}

type parsedArtifact struct {
	Manifest  artifactManifest
	Identity  artifactIdentity
	Items     []artifactItem
	Relations []artifactRelation
}

type receiptContext struct {
	RunID                string
	PersonRefID          string
	MovieCatalogPersonID string
	Category             string
	Source               string
	RetrievedAt          string
}

const (
	personReferenceTable = "hobby_person_references"
	relatedItemsTable    = "hobby_related_items"
	relationsTable       = "hobby_person_relations"
	receiptsTable        = "hobby_collection_receipts"
)

var ownTables = []string{personReferenceTable, relatedItemsTable, relationsTable, receiptsTable}

var ownIndexes = []string{
	"idx_hobby_person_references_movie_catalog_person_id",
	"idx_hobby_related_items_category_item_id",
	"idx_hobby_person_relations_person_category_relation",
	"idx_hobby_collection_receipts_run_category",
}

// EnsureSchema creates only this feature's tables and named indexes. The
// existing assessment table is never created here; only its collection index
// is added when that table already exists.
func EnsureSchema(ctx context.Context, movieDB, hobbyDB *sql.DB) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if movieDB == nil || hobbyDB == nil {
		return fmt.Errorf("%w: database is nil", ErrUnavailable)
	}
	tx, err := hobbyDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin person related catalog schema: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS hobby_person_references (
  person_ref_id TEXT PRIMARY KEY,
  movie_catalog_person_id TEXT NOT NULL,
  identity_state TEXT NOT NULL,
  external_ids_json TEXT NOT NULL,
  evidence_url TEXT NOT NULL,
  run_id TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
		`CREATE TABLE IF NOT EXISTS hobby_related_items (
  item_id TEXT NOT NULL,
  category TEXT NOT NULL,
  item_type TEXT NOT NULL,
  display_name TEXT NOT NULL,
  name_original TEXT NOT NULL,
  name_ja TEXT,
  name_state TEXT NOT NULL,
  name_ja_source_url TEXT,
  source_record_id TEXT NOT NULL,
  canonical_url TEXT NOT NULL,
  source TEXT NOT NULL,
  description_original TEXT,
  description_language TEXT,
  description_ja TEXT,
  description_translation_state TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(category, item_id)
)`,
		`CREATE TABLE IF NOT EXISTS hobby_person_relations (
  relation_id TEXT PRIMARY KEY,
  person_ref_id TEXT NOT NULL,
  category TEXT NOT NULL,
  target_item_id TEXT NOT NULL,
  relation_type TEXT NOT NULL,
  source TEXT NOT NULL,
  evidence_url TEXT NOT NULL,
  validation_state TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(person_ref_id, category, target_item_id, relation_type, source)
)`,
		`CREATE TABLE IF NOT EXISTS hobby_collection_receipts (
  run_id TEXT NOT NULL,
  category TEXT NOT NULL,
  source TEXT NOT NULL,
  person_ref_id TEXT NOT NULL,
  movie_catalog_person_id TEXT NOT NULL,
  retrieved_at TEXT NOT NULL,
  status TEXT NOT NULL,
  item_count INTEGER NOT NULL,
  relation_count INTEGER NOT NULL,
  artifact_sha256 TEXT NOT NULL,
  artifact_bytes INTEGER NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(run_id, category, source)
)`,
		`CREATE INDEX IF NOT EXISTS idx_hobby_person_references_movie_catalog_person_id
  ON hobby_person_references(movie_catalog_person_id, person_ref_id)`,
		`CREATE INDEX IF NOT EXISTS idx_hobby_related_items_category_item_id
  ON hobby_related_items(category, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_hobby_person_relations_person_category_relation
  ON hobby_person_relations(person_ref_id, category, relation_type, target_item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_hobby_collection_receipts_run_category
  ON hobby_collection_receipts(run_id, category, source)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create person related catalog schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit person related catalog schema: %w", err)
	}
	rollback = false

	if exists, err := sqliteObjectExists(ctx, movieDB, "table", "movie_catalog_assessments"); err != nil {
		return fmt.Errorf("check movie catalog assessment schema: %w", err)
	} else if exists {
		if _, err := movieDB.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_movie_catalog_assessments_collection_familiarity
  ON movie_catalog_assessments(kind, familiarity, target_id);
CREATE INDEX IF NOT EXISTS idx_movie_catalog_assessments_collection_sentiment
  ON movie_catalog_assessments(kind, sentiment, target_id);
CREATE INDEX IF NOT EXISTS idx_movie_catalog_assessments_eligible_target
  ON movie_catalog_assessments(kind, target_id, familiarity, sentiment)`); err != nil {
			return fmt.Errorf("create movie catalog collection index: %w", err)
		}
	}
	return nil
}

// EligiblePeople returns only people with an explicit positive assessment.
// It intentionally does not consult movie_preference_signals or any legacy
// favorite state.
func EligiblePeople(ctx context.Context, movieDB *sql.DB, limit int) ([]EligiblePerson, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInvalidLimit
	}
	if err := requireMovieSelectionSchema(ctx, movieDB); err != nil {
		return nil, err
	}
	rows, err := movieDB.QueryContext(ctx, `
WITH eligible AS (
  SELECT target_id FROM movie_catalog_assessments INDEXED BY idx_movie_catalog_assessments_collection_familiarity
  WHERE kind='person' AND familiarity='known'
  UNION
  SELECT target_id FROM movie_catalog_assessments INDEXED BY idx_movie_catalog_assessments_collection_sentiment
  WHERE kind='person' AND sentiment='like'
)
SELECT p.person_id, p.name, p.url, a.familiarity, a.sentiment
FROM eligible e
JOIN movie_catalog_assessments AS a INDEXED BY idx_movie_catalog_assessments_eligible_target
  ON a.kind='person' AND a.target_id=e.target_id
JOIN people AS p ON p.person_id=a.target_id
ORDER BY p.name, p.person_id
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("select eligible people: %w", err)
	}
	defer rows.Close()
	people := []EligiblePerson{}
	for rows.Next() {
		var person EligiblePerson
		if err := rows.Scan(&person.MovieCatalogPersonID, &person.Name, &person.URL, &person.Familiarity, &person.Sentiment); err != nil {
			return nil, fmt.Errorf("scan eligible person: %w", err)
		}
		people = append(people, person)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read eligible people: %w", err)
	}
	return people, nil
}

// EligiblePersonByID verifies one explicit assessment without enumerating the
// eligible population. It never consults legacy favorite signals.
func EligiblePersonByID(ctx context.Context, movieDB *sql.DB, movieCatalogPersonID string) (EligiblePerson, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	movieCatalogPersonID = strings.TrimSpace(movieCatalogPersonID)
	if movieCatalogPersonID == "" {
		return EligiblePerson{}, false, fmt.Errorf("%w: movie catalog person id is required", ErrInvalidArtifact)
	}
	if err := requireMovieSelectionSchema(ctx, movieDB); err != nil {
		return EligiblePerson{}, false, err
	}
	var person EligiblePerson
	err := movieDB.QueryRowContext(ctx, `
SELECT p.person_id, p.name, p.url, a.familiarity, a.sentiment
FROM movie_catalog_assessments AS a INDEXED BY idx_movie_catalog_assessments_eligible_target
JOIN people AS p ON p.person_id = a.target_id
WHERE a.kind = 'person'
  AND a.target_id = ?
  AND (a.familiarity = 'known' OR a.sentiment = 'like')
LIMIT 1`, movieCatalogPersonID).Scan(
		&person.MovieCatalogPersonID, &person.Name, &person.URL,
		&person.Familiarity, &person.Sentiment,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return EligiblePerson{}, false, nil
	}
	if err != nil {
		return EligiblePerson{}, false, fmt.Errorf("select eligible person by id: %w", err)
	}
	return person, true, nil
}

// Import validates and imports one immutable category artifact in one hobby
// database transaction. A failed artifact is recorded as an error receipt
// when the dedicated schema is available.
func Import(ctx context.Context, hobbyDB *sql.DB, artifact []byte, expectedSHA256 string, expectedBytes int64) (ImportResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, hobbyDB); err != nil {
		return ImportResult{}, err
	}
	actualHash := sha256.Sum256(artifact)
	actualSHA256 := hex.EncodeToString(actualHash[:])
	contextHint := artifactReceiptContext(artifact)
	baseResult := ImportResult{
		RunID:                contextHint.RunID,
		PersonRefID:          contextHint.PersonRefID,
		MovieCatalogPersonID: contextHint.MovieCatalogPersonID,
		Category:             contextHint.Category,
		Source:               contextHint.Source,
		RetrievedAt:          contextHint.RetrievedAt,
		ArtifactSHA256:       actualSHA256,
		ArtifactBytes:        int64(len(artifact)),
	}
	if err := verifyArtifactIntegrity(artifact, expectedSHA256, expectedBytes); err != nil {
		baseResult.Status = "error"
		baseResult.Error = err.Error()
		_ = writeErrorReceipt(ctx, hobbyDB, baseResult)
		return baseResult, err
	}
	parsed, err := parseArtifact(artifact)
	if err != nil {
		baseResult.Status = "error"
		baseResult.Error = err.Error()
		if parsedHint := parsedReceiptContext(artifact); parsedHint.RunID != "" {
			baseResult.RunID = parsedHint.RunID
			baseResult.PersonRefID = parsedHint.PersonRefID
			baseResult.MovieCatalogPersonID = parsedHint.MovieCatalogPersonID
			baseResult.Category = parsedHint.Category
			baseResult.Source = parsedHint.Source
			baseResult.RetrievedAt = parsedHint.RetrievedAt
		}
		_ = writeErrorReceipt(ctx, hobbyDB, baseResult)
		return baseResult, err
	}
	baseResult.RunID = parsed.Manifest.RunID
	baseResult.PersonRefID = parsed.Manifest.PersonRefID
	baseResult.MovieCatalogPersonID = parsed.Manifest.MovieCatalogPersonID
	baseResult.Category = parsed.Manifest.Category
	baseResult.Source = parsed.Manifest.Source
	baseResult.RetrievedAt = parsed.Manifest.RetrievedAt
	baseResult.ItemCount = len(parsed.Items)
	baseResult.RelationCount = len(parsed.Relations)
	if err := importParsed(ctx, hobbyDB, parsed, actualSHA256, int64(len(artifact))); err != nil {
		baseResult.Status = "error"
		baseResult.Error = err.Error()
		_ = writeErrorReceipt(ctx, hobbyDB, baseResult)
		return baseResult, err
	}
	baseResult.Status = "success"
	return baseResult, nil
}

// Lookup returns bounded, indexed relation rows for one movie-catalog person
// and category. Missing named schema/indexes fail closed with ErrUnavailable.
func Lookup(ctx context.Context, hobbyDB *sql.DB, movieCatalogPersonID, category string, limit int) ([]RelatedCatalogItem, error) {
	result, err := LookupWithCoverage(ctx, hobbyDB, movieCatalogPersonID, category, limit)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

// LookupWithCoverage returns the same fixed, indexed relation rows as Lookup
// together with the public WorkSummary coverage for the bounded result.
func LookupWithCoverage(ctx context.Context, hobbyDB *sql.DB, movieCatalogPersonID, category string, limit int) (LookupResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit < 1 || limit > 50 {
		return LookupResult{}, ErrInvalidLimit
	}
	movieCatalogPersonID = strings.TrimSpace(movieCatalogPersonID)
	if movieCatalogPersonID == "" {
		return LookupResult{}, fmt.Errorf("%w: movie_catalog_person_id is required", ErrInvalidArtifact)
	}
	if !validHobbyCategory(category) {
		return LookupResult{}, fmt.Errorf("%w: category %q", ErrInvalidArtifact, category)
	}
	if err := requireHobbySchema(ctx, hobbyDB); err != nil {
		return LookupResult{}, err
	}
	rows, err := hobbyDB.QueryContext(ctx, `
SELECT r.relation_id, r.person_ref_id, p.movie_catalog_person_id,
       r.category, r.relation_type, r.source, r.evidence_url,
       r.validation_state,
       i.item_id, i.item_type, i.display_name, i.name_original,
       COALESCE(i.name_ja, ''), i.name_state,
       COALESCE(i.name_ja_source_url, ''), i.source_record_id, i.canonical_url,
       COALESCE(i.description_original, ''), COALESCE(i.description_language, ''),
       COALESCE(i.description_ja, ''), COALESCE(i.description_translation_state, '')
FROM hobby_person_references AS p INDEXED BY idx_hobby_person_references_movie_catalog_person_id
JOIN hobby_person_relations AS r INDEXED BY idx_hobby_person_relations_person_category_relation
  ON r.person_ref_id = p.person_ref_id
JOIN hobby_related_items AS i INDEXED BY idx_hobby_related_items_category_item_id
  ON i.category = r.category AND i.item_id = r.target_item_id
WHERE p.movie_catalog_person_id = ?
  AND r.category = ?
ORDER BY r.relation_type, r.relation_id
LIMIT ?`, movieCatalogPersonID, category, limit)
	if err != nil {
		return LookupResult{}, fmt.Errorf("lookup person related catalog: %w", err)
	}
	defer rows.Close()
	items := []RelatedCatalogItem{}
	for rows.Next() {
		var item RelatedCatalogItem
		var descriptionOriginal, descriptionLanguage, descriptionJA, descriptionTranslation string
		if err := rows.Scan(
			&item.RelationID, &item.PersonRefID, &item.MovieCatalogPersonID,
			&item.Category, &item.RelationType, &item.Source, &item.EvidenceURL,
			&item.ValidationState, &item.ItemID, &item.ItemType,
			&item.DisplayName, &item.NameOriginal, &item.NameJA,
			&item.NameState, &item.NameJASourceURL, &item.SourceRecordID,
			&item.CanonicalURL, &descriptionOriginal, &descriptionLanguage,
			&descriptionJA, &descriptionTranslation,
		); err != nil {
			return LookupResult{}, fmt.Errorf("scan person related catalog: %w", err)
		}
		item.SummaryJA, item.SummaryState, item.SummarySourceURL = projectWorkSummary(
			descriptionOriginal, descriptionLanguage, descriptionJA, descriptionTranslation, item.CanonicalURL,
		)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return LookupResult{}, fmt.Errorf("read person related catalog: %w", err)
	}
	return LookupResult{Items: items, SummaryCoverage: summarizeCoverage(items)}, nil
}

func projectWorkSummary(original, language, summaryJA, translationState, sourceURL string) (string, string, string) {
	original = strings.TrimSpace(original)
	language = strings.TrimSpace(language)
	summaryJA = strings.TrimSpace(summaryJA)
	translationState = strings.TrimSpace(translationState)
	if original == "" {
		return "", "unavailable", ""
	}
	if strings.EqualFold(language, "ja") && translationState == "not_required" && summaryJA == original {
		return summaryJA, "source_summary", strings.TrimSpace(sourceURL)
	}
	if !strings.EqualFold(language, "ja") && translationState == "ready" && summaryJA != "" {
		return summaryJA, "translated_summary", strings.TrimSpace(sourceURL)
	}
	return "", "unavailable", ""
}

func summarizeCoverage(items []RelatedCatalogItem) SummaryCoverage {
	coverage := SummaryCoverage{Total: len(items)}
	for _, item := range items {
		if item.SummaryState == "source_summary" || item.SummaryState == "translated_summary" {
			coverage.Ready++
		} else {
			coverage.Unavailable++
		}
	}
	return coverage
}

func importParsed(ctx context.Context, db *sql.DB, artifact parsedArtifact, artifactSHA256 string, artifactBytes int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin person related catalog import: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	externalIDs, err := json.Marshal(artifact.Identity.ExternalIDs)
	if err != nil {
		return fmt.Errorf("encode identity external ids: %w", err)
	}
	if err := checkExistingPersonReference(ctx, tx, artifact, string(externalIDs)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO hobby_person_references(person_ref_id,movie_catalog_person_id,identity_state,external_ids_json,evidence_url,run_id,created_at,updated_at)
VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(person_ref_id) DO UPDATE SET
  movie_catalog_person_id=excluded.movie_catalog_person_id,
  identity_state=excluded.identity_state,
  external_ids_json=excluded.external_ids_json,
  evidence_url=excluded.evidence_url,
  run_id=excluded.run_id,
  updated_at=CURRENT_TIMESTAMP`,
		artifact.Identity.PersonRefID, artifact.Identity.MovieCatalogPersonID,
		artifact.Identity.IdentityState, string(externalIDs), artifact.Identity.EvidenceURL,
		artifact.Manifest.RunID); err != nil {
		return fmt.Errorf("import person reference: %w", err)
	}

	for _, item := range artifact.Items {
		if err := checkExistingItem(ctx, tx, item, artifact.Manifest.Source); err != nil {
			return err
		}
		var nameJA any
		if item.NameJA != nil && strings.TrimSpace(*item.NameJA) != "" {
			nameJA = *item.NameJA
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO hobby_related_items(
  item_id,category,item_type,display_name,name_original,name_ja,name_state,
  name_ja_source_url,source_record_id,canonical_url,source,description_original,
  description_language,description_ja,description_translation_state,created_at,updated_at
)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(category,item_id) DO UPDATE SET
  item_type=excluded.item_type,
  display_name=excluded.display_name,
  name_original=excluded.name_original,
  name_ja=excluded.name_ja,
  name_state=excluded.name_state,
  name_ja_source_url=excluded.name_ja_source_url,
  source_record_id=excluded.source_record_id,
  canonical_url=excluded.canonical_url,
  source=excluded.source,
  description_original=excluded.description_original,
  description_language=excluded.description_language,
  description_ja=excluded.description_ja,
  description_translation_state=excluded.description_translation_state,
  updated_at=CURRENT_TIMESTAMP`,
			item.ItemID, item.Category, item.ItemType, item.DisplayName,
			item.NameOriginal, nameJA, item.NameState, nullableString(item.NameJASourceURL),
			item.SourceRecordID, item.CanonicalURL, effectiveItemSource(item.Source, artifact.Manifest.Source),
			nullableString(item.DescriptionOriginal), nullableString(item.DescriptionLanguage),
			nullableString(item.DescriptionJA), nullableString(item.DescriptionTranslation)); err != nil {
			return fmt.Errorf("import related item %q: %w", item.ItemID, err)
		}
	}

	for _, relation := range artifact.Relations {
		if err := checkExistingRelation(ctx, tx, relation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO hobby_person_relations(
  relation_id,person_ref_id,category,target_item_id,relation_type,source,evidence_url,validation_state,created_at
)
VALUES(?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(relation_id) DO UPDATE SET
  person_ref_id=excluded.person_ref_id,
  category=excluded.category,
  target_item_id=excluded.target_item_id,
  relation_type=excluded.relation_type,
  source=excluded.source,
  evidence_url=excluded.evidence_url,
  validation_state=excluded.validation_state`,
			relation.RelationID, relation.PersonRefID, relation.Category,
			relation.TargetItemID, relation.RelationType, relation.Source,
			relation.EvidenceURL, relation.ValidationState); err != nil {
			return fmt.Errorf("import relation %q: %w", relation.RelationID, err)
		}
	}

	if err := checkExistingReceipt(ctx, tx, artifact.Manifest, artifactSHA256); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO hobby_collection_receipts(
  run_id,category,source,person_ref_id,movie_catalog_person_id,retrieved_at,status,
  item_count,relation_count,artifact_sha256,artifact_bytes,error,created_at,updated_at
)
VALUES(?,?,?,?,?,?,?,?,?,?,?,'',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(run_id,category,source) DO UPDATE SET
  person_ref_id=excluded.person_ref_id,
  movie_catalog_person_id=excluded.movie_catalog_person_id,
  retrieved_at=excluded.retrieved_at,
  status=excluded.status,
  item_count=excluded.item_count,
  relation_count=excluded.relation_count,
  artifact_sha256=excluded.artifact_sha256,
  artifact_bytes=excluded.artifact_bytes,
  error='',
  updated_at=CURRENT_TIMESTAMP`,
		artifact.Manifest.RunID, artifact.Manifest.Category, artifact.Manifest.Source,
		artifact.Manifest.PersonRefID, artifact.Manifest.MovieCatalogPersonID,
		artifact.Manifest.RetrievedAt, "success", len(artifact.Items), len(artifact.Relations),
		artifactSHA256, artifactBytes); err != nil {
		return fmt.Errorf("write collection receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit person related catalog import: %w", err)
	}
	rollback = false
	return nil
}

func checkExistingPersonReference(ctx context.Context, tx *sql.Tx, artifact parsedArtifact, externalIDs string) error {
	var moviePersonID, identityState, storedExternalIDs, evidenceURL string
	err := tx.QueryRowContext(ctx, `SELECT movie_catalog_person_id,identity_state,external_ids_json,evidence_url FROM hobby_person_references WHERE person_ref_id=?`, artifact.Identity.PersonRefID).Scan(&moviePersonID, &identityState, &storedExternalIDs, &evidenceURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check person reference: %w", err)
	}
	if moviePersonID != artifact.Identity.MovieCatalogPersonID || identityState != artifact.Identity.IdentityState || storedExternalIDs != externalIDs || evidenceURL != artifact.Identity.EvidenceURL {
		return fmt.Errorf("%w: person_ref_id %q", ErrConflict, artifact.Identity.PersonRefID)
	}
	return nil
}

func checkExistingItem(ctx context.Context, tx *sql.Tx, item artifactItem, manifestSource string) error {
	var existing artifactItem
	var existingNameJA string
	err := tx.QueryRowContext(ctx, `
SELECT item_id,category,item_type,display_name,name_original,COALESCE(name_ja,''),name_state,
       COALESCE(name_ja_source_url,''),source_record_id,canonical_url,source,
       COALESCE(description_original,''),COALESCE(description_language,''),COALESCE(description_ja,''),COALESCE(description_translation_state,'')
FROM hobby_related_items WHERE category=? AND item_id=?`, item.Category, item.ItemID).Scan(
		&existing.ItemID, &existing.Category, &existing.ItemType, &existing.DisplayName,
		&existing.NameOriginal, &existingNameJA, &existing.NameState, &existing.NameJASourceURL,
		&existing.SourceRecordID, &existing.CanonicalURL, &existing.Source,
		&existing.DescriptionOriginal, &existing.DescriptionLanguage, &existing.DescriptionJA,
		&existing.DescriptionTranslation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check related item: %w", err)
	}
	nameJA := ""
	if item.NameJA != nil {
		nameJA = *item.NameJA
	}
	if existing.ItemType != item.ItemType || existing.DisplayName != item.DisplayName || existing.NameOriginal != item.NameOriginal || existingNameJA != nameJA || existing.NameState != item.NameState || existing.NameJASourceURL != item.NameJASourceURL || existing.SourceRecordID != item.SourceRecordID || existing.CanonicalURL != item.CanonicalURL || existing.Source != effectiveItemSource(item.Source, manifestSource) || existing.DescriptionOriginal != item.DescriptionOriginal || existing.DescriptionLanguage != item.DescriptionLanguage || existing.DescriptionJA != item.DescriptionJA || existing.DescriptionTranslation != item.DescriptionTranslation {
		return fmt.Errorf("%w: item %q", ErrConflict, item.ItemID)
	}
	return nil
}

func checkExistingRelation(ctx context.Context, tx *sql.Tx, relation artifactRelation) error {
	var existing artifactRelation
	err := tx.QueryRowContext(ctx, `
SELECT relation_id,person_ref_id,category,target_item_id,relation_type,source,evidence_url,validation_state
FROM hobby_person_relations WHERE relation_id=?`, relation.RelationID).Scan(
		&existing.RelationID, &existing.PersonRefID, &existing.Category, &existing.TargetItemID,
		&existing.RelationType, &existing.Source, &existing.EvidenceURL, &existing.ValidationState)
	if errors.Is(err, sql.ErrNoRows) {
		var otherID string
		err = tx.QueryRowContext(ctx, `SELECT relation_id FROM hobby_person_relations WHERE person_ref_id=? AND category=? AND target_item_id=? AND relation_type=? AND source=?`, relation.PersonRefID, relation.Category, relation.TargetItemID, relation.RelationType, relation.Source).Scan(&otherID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("check logical relation: %w", err)
		}
		return fmt.Errorf("%w: relation tuple already uses %q", ErrConflict, otherID)
	}
	if err != nil {
		return fmt.Errorf("check relation: %w", err)
	}
	if existing.PersonRefID != relation.PersonRefID || existing.Category != relation.Category || existing.TargetItemID != relation.TargetItemID || existing.RelationType != relation.RelationType || existing.Source != relation.Source || existing.EvidenceURL != relation.EvidenceURL || existing.ValidationState != relation.ValidationState {
		return fmt.Errorf("%w: relation %q", ErrConflict, relation.RelationID)
	}
	return nil
}

func checkExistingReceipt(ctx context.Context, tx *sql.Tx, manifest artifactManifest, artifactSHA256 string) error {
	var existingHash string
	err := tx.QueryRowContext(ctx, `SELECT artifact_sha256 FROM hobby_collection_receipts WHERE run_id=? AND category=? AND source=?`, manifest.RunID, manifest.Category, manifest.Source).Scan(&existingHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check collection receipt: %w", err)
	}
	if existingHash != artifactSHA256 {
		return fmt.Errorf("%w: collection receipt %q/%q/%q", ErrConflict, manifest.RunID, manifest.Category, manifest.Source)
	}
	return nil
}

func writeErrorReceipt(ctx context.Context, db *sql.DB, result ImportResult) error {
	if db == nil || !schemaAvailable(ctx, db, ownTables, ownIndexes) {
		return ErrUnavailable
	}
	runID := result.RunID
	if strings.TrimSpace(runID) == "" {
		runID = "unknown"
	}
	category := result.Category
	if !validCategory(category) {
		category = "unknown"
	}
	source := strings.TrimSpace(result.Source)
	if source == "" {
		source = "unknown"
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO hobby_collection_receipts(
  run_id,category,source,person_ref_id,movie_catalog_person_id,retrieved_at,status,
  item_count,relation_count,artifact_sha256,artifact_bytes,error,created_at,updated_at
)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(run_id,category,source) DO UPDATE SET
  status='error',
  artifact_sha256=excluded.artifact_sha256,
  artifact_bytes=excluded.artifact_bytes,
  error=excluded.error,
  updated_at=CURRENT_TIMESTAMP`,
		runID, category, source, result.PersonRefID, result.MovieCatalogPersonID, result.RetrievedAt,
		result.Status, result.ItemCount, result.RelationCount, result.ArtifactSHA256,
		result.ArtifactBytes, result.Error)
	return err
}

func parseArtifact(artifact []byte) (parsedArtifact, error) {
	var parsed parsedArtifact
	scanner := bufio.NewScanner(strings.NewReader(string(artifact)))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	seenItemIDs := map[string]struct{}{}
	seenRelationIDs := map[string]struct{}{}
	seenRelationTuples := map[string]struct{}{}
	manifestCount := 0
	identityCount := 0
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope struct {
			SchemaVersion string `json:"schema_version"`
			RecordType    string `json:"record_type"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			return parsed, invalidArtifact(lineNo, "invalid JSON", err)
		}
		if envelope.SchemaVersion != SchemaVersion {
			return parsed, invalidArtifact(lineNo, "schema_version must be exact", nil)
		}
		switch envelope.RecordType {
		case "manifest":
			manifestCount++
			if manifestCount > 1 {
				return parsed, invalidArtifact(lineNo, "duplicate manifest", nil)
			}
			if err := json.Unmarshal([]byte(line), &parsed.Manifest); err != nil {
				return parsed, invalidArtifact(lineNo, "decode manifest", err)
			}
		case "identity":
			identityCount++
			if identityCount > 1 {
				return parsed, invalidArtifact(lineNo, "duplicate identity", nil)
			}
			if err := json.Unmarshal([]byte(line), &parsed.Identity); err != nil {
				return parsed, invalidArtifact(lineNo, "decode identity", err)
			}
		case "item":
			var item artifactItem
			if err := json.Unmarshal([]byte(line), &item); err != nil {
				return parsed, invalidArtifact(lineNo, "decode item", err)
			}
			item.ItemID = strings.TrimSpace(item.ItemID)
			if item.ItemID == "" {
				return parsed, invalidArtifact(lineNo, "item_id is required", nil)
			}
			if _, exists := seenItemIDs[item.ItemID]; exists {
				return parsed, invalidArtifact(lineNo, "duplicate item_id", nil)
			}
			seenItemIDs[item.ItemID] = struct{}{}
			parsed.Items = append(parsed.Items, item)
		case "relation":
			var relation artifactRelation
			if err := json.Unmarshal([]byte(line), &relation); err != nil {
				return parsed, invalidArtifact(lineNo, "decode relation", err)
			}
			relation.RelationID = strings.TrimSpace(relation.RelationID)
			if relation.RelationID == "" {
				return parsed, invalidArtifact(lineNo, "relation_id is required", nil)
			}
			if _, exists := seenRelationIDs[relation.RelationID]; exists {
				return parsed, invalidArtifact(lineNo, "duplicate relation_id", nil)
			}
			seenRelationIDs[relation.RelationID] = struct{}{}
			parsed.Relations = append(parsed.Relations, relation)
		default:
			return parsed, invalidArtifact(lineNo, "record_type is unsupported", nil)
		}
	}
	if err := scanner.Err(); err != nil {
		return parsed, invalidArtifact(0, "read artifact", err)
	}
	if manifestCount != 1 {
		return parsed, invalidArtifact(0, "exactly one manifest is required", nil)
	}
	if identityCount != 1 {
		return parsed, invalidArtifact(0, "exactly one identity is required", nil)
	}
	if err := validateParsedArtifact(&parsed, seenRelationTuples); err != nil {
		return parsed, err
	}
	return parsed, nil
}

func validateParsedArtifact(parsed *parsedArtifact, seenRelationTuples map[string]struct{}) error {
	manifest := &parsed.Manifest
	if strings.TrimSpace(manifest.RunID) == "" || strings.TrimSpace(manifest.PersonRefID) == "" || strings.TrimSpace(manifest.MovieCatalogPersonID) == "" || strings.TrimSpace(manifest.Source) == "" || !validHobbyCategory(manifest.Category) || strings.TrimSpace(manifest.RetrievedAt) == "" {
		return fmt.Errorf("%w: manifest required field is missing", ErrInvalidArtifact)
	}
	if !contractFreeSourceAllowed(manifest.Category, manifest.Source) {
		return fmt.Errorf("%w: source %q is not an approved contract-free source for category %q", ErrInvalidArtifact, manifest.Source, manifest.Category)
	}
	if _, err := time.Parse(time.RFC3339, manifest.RetrievedAt); err != nil {
		return fmt.Errorf("%w: retrieved_at must be RFC3339", ErrInvalidArtifact)
	}
	if manifest.ItemCount < 0 || manifest.RelationCount < 0 || manifest.ItemCount != len(parsed.Items) || manifest.RelationCount != len(parsed.Relations) {
		return fmt.Errorf("%w: manifest counts do not match records", ErrInvalidArtifact)
	}
	identity := &parsed.Identity
	if identity.PersonRefID != manifest.PersonRefID || identity.MovieCatalogPersonID != manifest.MovieCatalogPersonID || identity.IdentityState != "confirmed" || strings.TrimSpace(identity.EvidenceURL) == "" || !validHTTPURL(identity.EvidenceURL) || len(identity.ExternalIDs) == 0 {
		return fmt.Errorf("%w: identity is not confirmed", ErrInvalidArtifact)
	}
	for key, value := range identity.ExternalIDs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: identity external_ids contains an empty value", ErrInvalidArtifact)
		}
	}
	itemIDs := map[string]struct{}{}
	for i := range parsed.Items {
		item := &parsed.Items[i]
		if !validCategory(item.Category) || item.Category != manifest.Category || strings.TrimSpace(item.ItemType) == "" || strings.TrimSpace(item.DisplayName) == "" || strings.TrimSpace(item.NameOriginal) == "" || strings.TrimSpace(item.SourceRecordID) == "" || !validHTTPURL(item.CanonicalURL) {
			return fmt.Errorf("%w: item %q required field is invalid", ErrInvalidArtifact, item.ItemID)
		}
		if _, exists := itemIDs[item.ItemID]; exists {
			return fmt.Errorf("%w: duplicate item %q", ErrInvalidArtifact, item.ItemID)
		}
		itemIDs[item.ItemID] = struct{}{}
		if item.Source == "" {
			item.Source = manifest.Source
		}
		if strings.ToLower(strings.TrimSpace(item.Source)) != strings.ToLower(strings.TrimSpace(manifest.Source)) {
			return fmt.Errorf("%w: item %q source differs from manifest", ErrInvalidArtifact, item.ItemID)
		}
		if err := validateWorkSummary(item); err != nil {
			return fmt.Errorf("%w: item %q summary: %v", ErrInvalidArtifact, item.ItemID, err)
		}
		switch item.NameState {
		case "official_ja", "source_ja":
			if item.NameJA == nil || strings.TrimSpace(*item.NameJA) == "" || item.DisplayName != *item.NameJA || !validHTTPURL(item.NameJASourceURL) {
				return fmt.Errorf("%w: item %q Japanese name lacks source evidence", ErrInvalidArtifact, item.ItemID)
			}
		case "original":
			if item.NameJA != nil && strings.TrimSpace(*item.NameJA) != "" || item.NameJASourceURL != "" || item.DisplayName != item.NameOriginal {
				return fmt.Errorf("%w: item %q original-name contract violated", ErrInvalidArtifact, item.ItemID)
			}
		default:
			return fmt.Errorf("%w: item %q name_state is invalid", ErrInvalidArtifact, item.ItemID)
		}
	}
	for i := range parsed.Relations {
		relation := &parsed.Relations[i]
		if relation.Category == "" {
			relation.Category = manifest.Category
		}
		if relation.PersonRefID != manifest.PersonRefID || relation.Category != manifest.Category || strings.TrimSpace(relation.TargetItemID) == "" || strings.TrimSpace(relation.RelationType) == "" || strings.ToLower(strings.TrimSpace(relation.Source)) != strings.ToLower(strings.TrimSpace(manifest.Source)) || relation.ValidationState != "validated" || !validHTTPURL(relation.EvidenceURL) {
			return fmt.Errorf("%w: relation %q is invalid", ErrInvalidArtifact, relation.RelationID)
		}
		if _, exists := itemIDs[relation.TargetItemID]; !exists {
			return fmt.Errorf("%w: relation %q targets an unknown item", ErrInvalidArtifact, relation.RelationID)
		}
		key := relation.PersonRefID + "\x00" + relation.Category + "\x00" + relation.TargetItemID + "\x00" + relation.RelationType + "\x00" + relation.Source
		if _, exists := seenRelationTuples[key]; exists {
			return fmt.Errorf("%w: duplicate relation tuple", ErrInvalidArtifact)
		}
		seenRelationTuples[key] = struct{}{}
	}
	return nil
}

func validateWorkSummary(item *artifactItem) error {
	original := strings.TrimSpace(item.DescriptionOriginal)
	language := strings.TrimSpace(item.DescriptionLanguage)
	summaryJA := strings.TrimSpace(item.DescriptionJA)
	translationState := strings.TrimSpace(item.DescriptionTranslation)
	if original == "" {
		if language != "" || summaryJA != "" || (translationState != "" && translationState != "not_attempted") {
			return errors.New("empty description_original requires empty language/ja and empty or not_attempted translation state")
		}
		return nil
	}
	if language == "" {
		return errors.New("description_language is required when description_original is present")
	}
	switch translationState {
	case "not_required":
		if !strings.EqualFold(language, "ja") || summaryJA == "" || summaryJA != original {
			return errors.New("not_required requires a Japanese description identical to the original")
		}
	case "ready":
		if strings.EqualFold(language, "ja") || summaryJA == "" {
			return errors.New("ready requires a non-Japanese original and nonblank Japanese description")
		}
	case "failed", "not_attempted":
		if summaryJA != "" {
			return errors.New("failed or not_attempted must not contain a public Japanese description")
		}
	default:
		return errors.New("description_translation_state is unknown")
	}
	return nil
}

func verifyArtifactIntegrity(artifact []byte, expectedSHA256 string, expectedBytes int64) error {
	if expectedBytes != int64(len(artifact)) {
		return fmt.Errorf("%w: expected bytes=%d actual bytes=%d", ErrIntegrity, expectedBytes, len(artifact))
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if len(expectedSHA256) != sha256.Size*2 {
		return fmt.Errorf("%w: expected SHA-256 is missing or malformed", ErrIntegrity)
	}
	if _, err := hex.DecodeString(expectedSHA256); err != nil {
		return fmt.Errorf("%w: expected SHA-256 is malformed", ErrIntegrity)
	}
	actual := sha256.Sum256(artifact)
	if hex.EncodeToString(actual[:]) != expectedSHA256 {
		return fmt.Errorf("%w: SHA-256 mismatch", ErrIntegrity)
	}
	return nil
}

func artifactReceiptContext(artifact []byte) receiptContext {
	return parsedReceiptContext(artifact)
}

func parsedReceiptContext(artifact []byte) receiptContext {
	scanner := bufio.NewScanner(strings.NewReader(string(artifact)))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record struct {
			RecordType           string `json:"record_type"`
			RunID                string `json:"run_id"`
			PersonRefID          string `json:"person_ref_id"`
			MovieCatalogPersonID string `json:"movie_catalog_person_id"`
			Category             string `json:"category"`
			Source               string `json:"source"`
			RetrievedAt          string `json:"retrieved_at"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.RecordType == "manifest" {
			return receiptContext{RunID: record.RunID, PersonRefID: record.PersonRefID, MovieCatalogPersonID: record.MovieCatalogPersonID, Category: record.Category, Source: record.Source, RetrievedAt: record.RetrievedAt}
		}
	}
	return receiptContext{}
}

func invalidArtifact(line int, message string, cause error) error {
	if line > 0 {
		message = fmt.Sprintf("line %d: %s", line, message)
	}
	if cause != nil {
		message += ": " + cause.Error()
	}
	return fmt.Errorf("%w: %s", ErrInvalidArtifact, message)
}

func requireMovieSelectionSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("%w: movie database is nil", ErrUnavailable)
	}
	if err := requireObjects(ctx, db, []string{"people", "movie_catalog_assessments"}, []string{"idx_movie_catalog_assessments_collection_familiarity", "idx_movie_catalog_assessments_collection_sentiment", "idx_movie_catalog_assessments_eligible_target"}); err != nil {
		return err
	}
	return nil
}

func requireHobbySchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("%w: hobby database is nil", ErrUnavailable)
	}
	if err := requireObjects(ctx, db, ownTables, ownIndexes); err != nil {
		return err
	}
	return nil
}

func schemaAvailable(ctx context.Context, db *sql.DB, tables, indexes []string) bool {
	return db != nil && requireObjects(ctx, db, tables, indexes) == nil
}

func requireObjects(ctx context.Context, db *sql.DB, tables, indexes []string) error {
	for _, table := range tables {
		exists, err := sqliteObjectExists(ctx, db, "table", table)
		if err != nil {
			return fmt.Errorf("%w: check table %s: %v", ErrUnavailable, table, err)
		}
		if !exists {
			return fmt.Errorf("%w: table %s is missing", ErrUnavailable, table)
		}
	}
	for _, index := range indexes {
		exists, err := sqliteObjectExists(ctx, db, "index", index)
		if err != nil {
			return fmt.Errorf("%w: check index %s: %v", ErrUnavailable, index, err)
		}
		if !exists {
			return fmt.Errorf("%w: index %s is missing", ErrUnavailable, index)
		}
	}
	return nil
}

func sqliteObjectExists(ctx context.Context, db *sql.DB, objectType, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, objectType, name).Scan(&count)
	return count > 0, err
}

func validCategory(category string) bool {
	_, ok := validCategories[category]
	return ok
}

func validHobbyCategory(category string) bool {
	_, ok := validHobbyCategories[category]
	return ok
}

func contractFreeSourceAllowed(category, source string) bool {
	sources := contractFreeSourcesByCategory[category]
	_, ok := sources[strings.ToLower(strings.TrimSpace(source))]
	return ok
}

func validHTTPURL(value string) bool {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func effectiveItemSource(itemSource, manifestSource string) string {
	if strings.TrimSpace(itemSource) != "" {
		return itemSource
	}
	return manifestSource
}
