package knowledgememory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

const (
	KnowledgeMemoryCoverageIndexing = "indexing"
	KnowledgeMemoryCoverageReady    = "ready"
	KnowledgeMemoryIntegrityReady   = "ready"
	KnowledgeMemoryIntegrityFailed  = "failed"
	maxProjectionBackfillBatch      = 200
)

// CoverageReceipt is a bounded, non-sensitive promotion receipt. It contains
// counts and states only; it never carries source payloads.
type CoverageReceipt struct {
	EligibleCount int    `json:"eligible_count"`
	IndexedCount  int    `json:"indexed_count"`
	State         string `json:"state"`
	CursorState   string `json:"cursor_state"`
}

type ImportManifest struct {
	SourceCount   int             `json:"source_count"`
	ImportedCount int             `json:"imported_count"`
	SourceHash    string          `json:"source_hash"`
	ImportedHash  string          `json:"imported_hash"`
	Coverage      CoverageReceipt `json:"coverage"`
}

type ImportReport struct {
	Manifest      ImportManifest  `json:"manifest"`
	SourceCount   int             `json:"source_count"`
	ImportedCount int             `json:"imported_count"`
	Coverage      CoverageReceipt `json:"coverage"`
}

type StoreReadiness struct {
	DatabaseAvailable bool            `json:"database_available"`
	SchemaReady       bool            `json:"schema_ready"`
	IndexReady        bool            `json:"index_ready"`
	Coverage          CoverageReceipt `json:"coverage"`
	IntegrityState    string          `json:"integrity_state"`
}

// OpenSQLiteStore opens an already deployed database without creating,
// migrating, or promoting it. Runtime capability wiring must use this entry
// point so a missing or partially deployed database remains fail-closed.
func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	return openSQLiteStore(path, true)
}

// OpenSQLiteStoreWritable opens an existing SQLite database for the domain
// Viewer/worker writers without creating or migrating it. Tool search must use
// OpenSQLiteStore instead, which is explicitly read-only.
func OpenSQLiteStoreWritable(path string) (*SQLiteStore, error) {
	return openSQLiteStore(path, false)
}

func openSQLiteStore(path string, readOnly bool) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("knowledge memory sqlite path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("knowledge memory sqlite path is a directory")
	}
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=" + mode + "&_time_format=sqlite"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Readiness(ctx context.Context) (StoreReadiness, error) {
	readiness := StoreReadiness{IntegrityState: KnowledgeMemoryIntegrityFailed}
	if s == nil || s.db == nil {
		return readiness, nil
	}
	if err := s.db.PingContext(ctx); err != nil {
		return readiness, err
	}
	readiness.DatabaseAvailable = true
	objects, err := sqliteSchemaObjects(ctx, s.db)
	if err != nil {
		return readiness, err
	}
	readiness.SchemaReady = allSchemaObjectsPresent(objects, requiredKnowledgeMemoryTables)
	if readiness.SchemaReady {
		readiness.SchemaReady, err = hasRequiredKnowledgeMemoryColumns(ctx, s.db)
	}
	readiness.IndexReady = allSchemaObjectsPresent(objects, requiredKnowledgeMemoryIndexes)
	if !readiness.SchemaReady {
		return readiness, nil
	}
	coverage, integrity, err := s.readPromotionReceipt(ctx)
	if err != nil {
		return readiness, err
	}
	readiness.Coverage = coverage
	readiness.IntegrityState = integrity
	return readiness, nil
}

var requiredKnowledgeMemoryTables = []string{
	"personal_archive",
	"creative_knowledge",
	"news_knowledge",
	"daily_intake_rule",
	"temporal_memory_marker",
	"dream_consolidation_run",
	"knowledge_memory_search_documents",
	"knowledge_memory_search_terms",
	"knowledge_memory_index_cursor",
	"knowledge_memory_import_manifest",
}

var requiredKnowledgeMemoryIndexes = []string{
	"idx_knowledge_memory_search_documents_lookup",
	"idx_knowledge_memory_search_terms_lookup",
}

var requiredKnowledgeMemoryColumns = map[string][]string{
	"personal_archive":                  {"entry_id", "user_id", "created_at", "payload"},
	"creative_knowledge":                {"item_id", "created_at", "payload"},
	"news_knowledge":                    {"item_id", "created_at", "payload"},
	"daily_intake_rule":                 {"rule_id", "user_id", "created_at", "payload"},
	"temporal_memory_marker":            {"marker_id", "user_id", "created_at", "payload"},
	"dream_consolidation_run":           {"run_id", "created_at", "payload"},
	"knowledge_memory_search_documents": {"record_type", "record_id", "scope", "user_id", "title", "summary", "visibility", "source_updated_at", "indexed_at", "content_sha256"},
	"knowledge_memory_search_terms":     {"scope", "user_id", "token", "record_type", "record_id"},
	"knowledge_memory_index_cursor":     {"record_type", "last_record_id", "eligible_count", "indexed_count", "state", "updated_at"},
	"knowledge_memory_import_manifest":  {"manifest_id", "source_count", "imported_count", "source_hash", "imported_hash", "coverage_state", "eligible_count", "indexed_count", "updated_at"},
}

func hasRequiredKnowledgeMemoryColumns(ctx context.Context, db *sql.DB) (bool, error) {
	for table, required := range requiredKnowledgeMemoryColumns {
		rows, err := db.QueryContext(ctx, `PRAGMA table_info("`+table+`")`)
		if err != nil {
			return false, err
		}
		seen := map[string]struct{}{}
		for rows.Next() {
			var cid int
			var name, columnType string
			var notNull, primaryKey int
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return false, err
			}
			seen[name] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return false, err
		}
		_ = rows.Close()
		for _, column := range required {
			if _, ok := seen[column]; !ok {
				return false, nil
			}
		}
	}
	return true, nil
}

func sqliteSchemaObjects(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name FROM sqlite_master WHERE type IN ('table', 'index')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := map[string]struct{}{}
	for rows.Next() {
		var objectType, name string
		if err := rows.Scan(&objectType, &name); err != nil {
			return nil, err
		}
		objects[objectType+":"+name] = struct{}{}
	}
	return objects, rows.Err()
}

func allSchemaObjectsPresent(objects map[string]struct{}, names []string) bool {
	objectType := "table:"
	if len(names) > 0 && strings.HasPrefix(names[0], "idx_") {
		objectType = "index:"
	}
	for _, name := range names {
		if _, ok := objects[objectType+name]; !ok {
			return false
		}
	}
	return true
}

func (s *SQLiteStore) readPromotionReceipt(ctx context.Context) (CoverageReceipt, string, error) {
	coverage := CoverageReceipt{State: KnowledgeMemoryCoverageIndexing, CursorState: KnowledgeMemoryCoverageIndexing}
	rows, err := s.db.QueryContext(ctx, `SELECT record_type, eligible_count, indexed_count, state
		FROM knowledge_memory_index_cursor WHERE record_type IN (?, ?)`, creativeKnowledgeRecordType, newsKnowledgeRecordType)
	if err != nil {
		return coverage, KnowledgeMemoryIntegrityFailed, err
	}
	cursors := map[string]struct {
		eligible int
		indexed  int
		state    string
	}{}
	for rows.Next() {
		var recordType, state string
		var eligible, indexed int
		if err := rows.Scan(&recordType, &eligible, &indexed, &state); err != nil {
			_ = rows.Close()
			return coverage, KnowledgeMemoryIntegrityFailed, err
		}
		cursors[recordType] = struct {
			eligible int
			indexed  int
			state    string
		}{eligible: eligible, indexed: indexed, state: state}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return coverage, KnowledgeMemoryIntegrityFailed, err
	}
	_ = rows.Close()
	if len(cursors) == len(searchableKnowledgeRecordTypes) {
		allReady := true
		for _, recordType := range searchableKnowledgeRecordTypes {
			cursor, ok := cursors[recordType]
			if !ok || cursor.state != KnowledgeMemoryCoverageReady {
				allReady = false
				continue
			}
			coverage.EligibleCount += cursor.eligible
			coverage.IndexedCount += cursor.indexed
		}
		if allReady {
			coverage.State = KnowledgeMemoryCoverageReady
			coverage.CursorState = KnowledgeMemoryCoverageReady
		}
	}
	if coverage.State != KnowledgeMemoryCoverageReady {
		return coverage, KnowledgeMemoryIntegrityFailed, nil
	}

	var manifest ImportManifest
	var coverageState string
	var updatedAt string
	err = s.db.QueryRowContext(ctx, `SELECT source_count, imported_count, source_hash, imported_hash,
		coverage_state, eligible_count, indexed_count, updated_at
		FROM knowledge_memory_import_manifest WHERE manifest_id = 'active'`).Scan(
		&manifest.SourceCount, &manifest.ImportedCount, &manifest.SourceHash, &manifest.ImportedHash,
		&coverageState, &manifest.Coverage.EligibleCount, &manifest.Coverage.IndexedCount, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return coverage, KnowledgeMemoryIntegrityFailed, nil
	}
	if err != nil {
		return coverage, KnowledgeMemoryIntegrityFailed, err
	}
	manifest.Coverage.State = coverageState
	manifest.Coverage.CursorState = coverage.CursorState
	if manifest.Coverage.State != KnowledgeMemoryCoverageReady || manifest.Coverage.EligibleCount != coverage.EligibleCount || manifest.Coverage.IndexedCount != coverage.IndexedCount {
		return coverage, KnowledgeMemoryIntegrityFailed, nil
	}
	currentCount, currentHash, err := s.domainManifest(ctx)
	if err != nil {
		return coverage, KnowledgeMemoryIntegrityFailed, err
	}
	if manifest.SourceCount != manifest.ImportedCount || manifest.ImportedCount != currentCount || manifest.SourceHash != manifest.ImportedHash || manifest.ImportedHash != currentHash {
		return coverage, KnowledgeMemoryIntegrityFailed, nil
	}
	for _, recordType := range searchableKnowledgeRecordTypes {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_memory_search_documents WHERE record_type = ?`, recordType).Scan(&count); err != nil {
			return coverage, KnowledgeMemoryIntegrityFailed, err
		}
		cursor := cursors[recordType]
		if cursor.eligible != count || cursor.indexed != count {
			return coverage, KnowledgeMemoryIntegrityFailed, nil
		}
	}
	projectionOK, err := s.verifySearchProjectionIntegrity(ctx)
	if err != nil {
		return coverage, KnowledgeMemoryIntegrityFailed, err
	}
	if !projectionOK {
		return coverage, KnowledgeMemoryIntegrityFailed, nil
	}
	return coverage, KnowledgeMemoryIntegrityReady, nil
}

func (s *SQLiteStore) verifySearchProjectionIntegrity(ctx context.Context) (bool, error) {
	for _, recordType := range searchableKnowledgeRecordTypes {
		rows, err := s.db.QueryContext(ctx, `SELECT item_id, payload FROM `+recordType+` ORDER BY item_id`)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var id, payload string
			if err := rows.Scan(&id, &payload); err != nil {
				_ = rows.Close()
				return false, err
			}
			projection, err := projectionFromRecord(promotionRecord{recordType: recordType, recordID: id, payload: []byte(payload)})
			if err != nil {
				_ = rows.Close()
				return false, err
			}
			var doc safeSearchProjection
			err = s.db.QueryRowContext(ctx, `SELECT scope, user_id, title, summary, visibility, source_updated_at, content_sha256
				FROM knowledge_memory_search_documents WHERE record_type = ? AND record_id = ?`, recordType, id).Scan(
				&doc.scope, &doc.userID, &doc.title, &doc.summary, &doc.visibility, &doc.sourceUpdatedAt, &doc.contentSHA256)
			if projection == nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					_ = rows.Close()
					return false, err
				}
				_ = rows.Close()
				return false, nil
			}
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					_ = rows.Close()
					return false, nil
				}
				_ = rows.Close()
				return false, err
			}
			if doc.scope != projection.scope || doc.userID != projection.userID || doc.title != projection.title || doc.summary != projection.summary || doc.visibility != projection.visibility || doc.sourceUpdatedAt != projection.sourceUpdatedAt || doc.contentSHA256 != projection.contentSHA256 {
				_ = rows.Close()
				return false, nil
			}
			termRows, err := s.db.QueryContext(ctx, `SELECT token FROM knowledge_memory_search_terms WHERE record_type = ? AND record_id = ? ORDER BY token`, recordType, id)
			if err != nil {
				_ = rows.Close()
				return false, err
			}
			actualTokens := []string{}
			for termRows.Next() {
				var token string
				if err := termRows.Scan(&token); err != nil {
					_ = termRows.Close()
					_ = rows.Close()
					return false, err
				}
				actualTokens = append(actualTokens, token)
			}
			termErr := termRows.Err()
			_ = termRows.Close()
			if termErr != nil {
				_ = rows.Close()
				return false, termErr
			}
			expectedTokens := append([]string(nil), projection.tokens...)
			sort.Strings(expectedTokens)
			if !slicesEqual(actualTokens, expectedTokens) {
				_ = rows.Close()
				return false, nil
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return false, err
		}
		_ = rows.Close()
		var orphanTerms int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_memory_search_terms AS terms
			LEFT JOIN knowledge_memory_search_documents AS documents
			ON documents.record_type = terms.record_type AND documents.record_id = terms.record_id
			WHERE terms.record_type = ? AND documents.record_id IS NULL`, recordType).Scan(&orphanTerms); err != nil {
			return false, err
		}
		if orphanTerms != 0 {
			return false, nil
		}
	}
	return true, nil
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

type promotionRecord struct {
	recordType string
	recordID   string
	payload    []byte
}

func (s *SQLiteStore) domainManifest(ctx context.Context) (int, string, error) {
	accumulator := newManifestAccumulator()
	for _, table := range []struct {
		recordType string
		table      string
		idColumn   string
	}{
		{recordType: "personal_archive", table: "personal_archive", idColumn: "entry_id"},
		{recordType: creativeKnowledgeRecordType, table: "creative_knowledge", idColumn: "item_id"},
		{recordType: newsKnowledgeRecordType, table: "news_knowledge", idColumn: "item_id"},
		{recordType: "daily_intake_rule", table: "daily_intake_rule", idColumn: "rule_id"},
		{recordType: "temporal_memory_marker", table: "temporal_memory_marker", idColumn: "marker_id"},
		{recordType: "dream_consolidation_run", table: "dream_consolidation_run", idColumn: "run_id"},
	} {
		rows, err := s.db.QueryContext(ctx, `SELECT `+table.idColumn+`, payload FROM `+table.table+` ORDER BY `+table.idColumn)
		if err != nil {
			return 0, "", err
		}
		for rows.Next() {
			var id, payload string
			if err := rows.Scan(&id, &payload); err != nil {
				_ = rows.Close()
				return 0, "", err
			}
			accumulator.Add(table.recordType, id, []byte(payload))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return 0, "", err
		}
		_ = rows.Close()
	}
	return accumulator.Count, hex.EncodeToString(accumulator.Hash.Sum(nil)), nil
}

type manifestAccumulator struct {
	Count int
	Hash  hash.Hash
}

func newManifestAccumulator() *manifestAccumulator {
	return &manifestAccumulator{Hash: sha256.New()}
}

func (a *manifestAccumulator) Add(recordType, recordID string, payload []byte) {
	a.Count++
	_, _ = a.Hash.Write([]byte(recordType))
	_, _ = a.Hash.Write([]byte{0})
	_, _ = a.Hash.Write([]byte(recordID))
	_, _ = a.Hash.Write([]byte{0})
	_, _ = a.Hash.Write(payload)
	_, _ = a.Hash.Write([]byte{'\n'})
}

type sourceSnapshot struct {
	personal []domainkm.PersonalArchiveEntry
	creative []domainkm.CreativeKnowledgeItem
	news     []domainkm.NewsKnowledgeItem
	intake   []domainkm.DailyIntakeRule
	temporal []domainkm.TemporalMemoryMarker
	dream    []domainkm.DreamConsolidationRun
	count    int
	hash     string
}

func readSourceSnapshot(root string) (sourceSnapshot, error) {
	jsonl := NewJSONLStore(root)
	snapshot := sourceSnapshot{}
	var err error
	if snapshot.personal, err = readLatestJSONL(jsonl.personalPath, func(item domainkm.PersonalArchiveEntry) string { return item.EntryID }, domainkm.ValidatePersonalArchiveEntry); err != nil {
		return snapshot, err
	}
	if snapshot.creative, err = readLatestJSONL(jsonl.creativePath, func(item domainkm.CreativeKnowledgeItem) string { return item.ItemID }, domainkm.ValidateCreativeKnowledgeItem); err != nil {
		return snapshot, err
	}
	if snapshot.news, err = readLatestJSONL(jsonl.newsPath, func(item domainkm.NewsKnowledgeItem) string { return item.ItemID }, domainkm.ValidateNewsKnowledgeItem); err != nil {
		return snapshot, err
	}
	if snapshot.intake, err = readLatestJSONL(jsonl.intakePath, func(item domainkm.DailyIntakeRule) string { return item.RuleID }, domainkm.ValidateDailyIntakeRule); err != nil {
		return snapshot, err
	}
	if snapshot.temporal, err = readLatestJSONL(jsonl.temporalPath, func(item domainkm.TemporalMemoryMarker) string { return item.MarkerID }, domainkm.ValidateTemporalMemoryMarker); err != nil {
		return snapshot, err
	}
	if snapshot.dream, err = readLatestJSONL(jsonl.dreamPath, func(item domainkm.DreamConsolidationRun) string { return item.RunID }, domainkm.ValidateDreamConsolidationRun); err != nil {
		return snapshot, err
	}
	accumulator := newManifestAccumulator()
	for _, item := range snapshot.personal {
		payload, _ := json.Marshal(item)
		accumulator.Add("personal_archive", item.EntryID, payload)
	}
	for _, item := range snapshot.creative {
		payload, _ := json.Marshal(item)
		accumulator.Add(creativeKnowledgeRecordType, item.ItemID, payload)
	}
	for _, item := range snapshot.news {
		payload, _ := json.Marshal(item)
		accumulator.Add(newsKnowledgeRecordType, item.ItemID, payload)
	}
	for _, item := range snapshot.intake {
		payload, _ := json.Marshal(item)
		accumulator.Add("daily_intake_rule", item.RuleID, payload)
	}
	for _, item := range snapshot.temporal {
		payload, _ := json.Marshal(item)
		accumulator.Add("temporal_memory_marker", item.MarkerID, payload)
	}
	for _, item := range snapshot.dream {
		payload, _ := json.Marshal(item)
		accumulator.Add("dream_consolidation_run", item.RunID, payload)
	}
	snapshot.count = accumulator.Count
	snapshot.hash = hex.EncodeToString(accumulator.Hash.Sum(nil))
	return snapshot, nil
}

func readLatestJSONL[T any](path string, idOf func(T) string, validate func(T) error) ([]T, error) {
	latest := map[string]T{}
	if err := readJSONL(path, func(line []byte) error {
		var item T
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if err := validate(item); err != nil {
			return err
		}
		latest[idOf(item)] = item
		return nil
	}); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]T, 0, len(keys))
	for _, key := range keys {
		items = append(items, latest[key])
	}
	return items, nil
}

// ImportJSONLToSQLite performs an additive, non-destructive import. The
// source JSONL remains untouched, while the target is promoted only after the
// domain rows, bounded projection backfill, and manifest all agree.
func ImportJSONLToSQLite(ctx context.Context, sourceRoot, sqlitePath string) (ImportReport, error) {
	snapshot, err := readSourceSnapshot(sourceRoot)
	if err != nil {
		return ImportReport{}, err
	}
	store, err := NewSQLiteStore(sqlitePath)
	if err != nil {
		return ImportReport{}, err
	}
	defer store.Close()
	for _, item := range snapshot.personal {
		if err := store.SavePersonalArchiveEntry(ctx, item); err != nil {
			return ImportReport{}, err
		}
	}
	for _, item := range snapshot.creative {
		if err := store.SaveCreativeKnowledgeItem(ctx, item); err != nil {
			return ImportReport{}, err
		}
	}
	for _, item := range snapshot.news {
		if err := store.SaveNewsKnowledgeItem(ctx, item); err != nil {
			return ImportReport{}, err
		}
	}
	for _, item := range snapshot.intake {
		if err := store.SaveDailyIntakeRule(ctx, item); err != nil {
			return ImportReport{}, err
		}
	}
	for _, item := range snapshot.temporal {
		if err := store.SaveTemporalMemoryMarker(ctx, item); err != nil {
			return ImportReport{}, err
		}
	}
	for _, item := range snapshot.dream {
		if err := store.SaveDreamConsolidationRun(ctx, item); err != nil {
			return ImportReport{}, err
		}
	}
	var coverage CoverageReceipt
	for {
		coverage, err = store.BackfillSearchProjection(ctx, maxProjectionBackfillBatch)
		if err != nil {
			return ImportReport{}, err
		}
		if coverage.State == KnowledgeMemoryCoverageReady {
			break
		}
	}
	importedCount, importedHash, err := store.domainManifest(ctx)
	if err != nil {
		return ImportReport{}, err
	}
	manifest := ImportManifest{
		SourceCount:   snapshot.count,
		ImportedCount: importedCount,
		SourceHash:    snapshot.hash,
		ImportedHash:  importedHash,
		Coverage:      coverage,
	}
	if manifest.SourceCount != manifest.ImportedCount || manifest.SourceHash != manifest.ImportedHash {
		return ImportReport{}, fmt.Errorf("knowledge memory import manifest mismatch")
	}
	if err := store.writeImportManifest(ctx, manifest); err != nil {
		return ImportReport{}, err
	}
	return ImportReport{Manifest: manifest, SourceCount: snapshot.count, ImportedCount: importedCount, Coverage: coverage}, nil
}

func (s *SQLiteStore) writeImportManifest(ctx context.Context, manifest ImportManifest) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO knowledge_memory_import_manifest
		(manifest_id, source_count, imported_count, source_hash, imported_hash, coverage_state, eligible_count, indexed_count, updated_at)
		VALUES ('active', ?, ?, ?, ?, ?, ?, ?, ?)`,
		manifest.SourceCount, manifest.ImportedCount, manifest.SourceHash, manifest.ImportedHash,
		manifest.Coverage.State, manifest.Coverage.EligibleCount, manifest.Coverage.IndexedCount,
		nowUTCString())
	return err
}

func nowUTCString() string {
	return timeNow().UTC().Format(timeFormatRFC3339Nano)
}

var timeNow = func() time.Time { return time.Now() }

// BackfillSearchProjection rebuilds the reviewed public/user projection in
// bounded batches. A cursor row stays in indexing until both record types have
// completed, so runtime never mistakes a partial index for a ready capability.
func (s *SQLiteStore) BackfillSearchProjection(ctx context.Context, batchSize int) (CoverageReceipt, error) {
	if s == nil || s.db == nil {
		return CoverageReceipt{}, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	if batchSize <= 0 || batchSize > maxProjectionBackfillBatch {
		batchSize = maxProjectionBackfillBatch
	}
	remaining := batchSize
	for _, recordType := range searchableKnowledgeRecordTypes {
		done, processed, err := s.backfillRecordTypeBatch(ctx, recordType, remaining)
		if err != nil {
			return CoverageReceipt{State: KnowledgeMemoryCoverageIndexing, CursorState: KnowledgeMemoryCoverageIndexing}, err
		}
		remaining -= processed
		if !done && remaining <= 0 {
			return s.readBackfillCoverage(ctx)
		}
	}
	return s.readBackfillCoverage(ctx)
}

func (s *SQLiteStore) readBackfillCoverage(ctx context.Context) (CoverageReceipt, error) {
	var eligible, indexed int
	ready := true
	rows, err := s.db.QueryContext(ctx, `SELECT eligible_count, indexed_count FROM knowledge_memory_index_cursor WHERE record_type IN (?, ?)`, creativeKnowledgeRecordType, newsKnowledgeRecordType)
	if err != nil {
		return CoverageReceipt{}, err
	}
	seen := 0
	defer rows.Close()
	for rows.Next() {
		seen++
		var e, i int
		if err := rows.Scan(&e, &i); err != nil {
			return CoverageReceipt{}, err
		}
		eligible += e
		indexed += i
	}
	if err := rows.Err(); err != nil {
		return CoverageReceipt{}, err
	}
	if seen != len(searchableKnowledgeRecordTypes) {
		ready = false
	}
	if ready {
		var pending int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_memory_index_cursor WHERE record_type IN (?, ?) AND state != ?`, creativeKnowledgeRecordType, newsKnowledgeRecordType, KnowledgeMemoryCoverageReady).Scan(&pending); err != nil {
			return CoverageReceipt{}, err
		}
		ready = pending == 0
	}
	state := KnowledgeMemoryCoverageIndexing
	if ready {
		state = KnowledgeMemoryCoverageReady
	}
	return CoverageReceipt{EligibleCount: eligible, IndexedCount: indexed, State: state, CursorState: state}, nil
}

func (s *SQLiteStore) backfillRecordTypeBatch(ctx context.Context, recordType string, batchSize int) (bool, int, error) {
	if batchSize <= 0 {
		return false, 0, nil
	}
	table := recordType
	var lastID string
	var cursorState string
	var indexedCount int
	err := s.db.QueryRowContext(ctx, `SELECT last_record_id, indexed_count, state FROM knowledge_memory_index_cursor WHERE record_type = ?`, recordType).Scan(&lastID, &indexedCount, &cursorState)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO knowledge_memory_index_cursor (record_type, last_record_id, eligible_count, indexed_count, state, updated_at) VALUES (?, '', 0, 0, ?, ?)`, recordType, KnowledgeMemoryCoverageIndexing, nowUTCString()); err != nil {
			return false, 0, err
		}
		lastID = ""
		indexedCount = 0
		cursorState = KnowledgeMemoryCoverageIndexing
	} else if err != nil {
		return false, 0, err
	}
	if cursorState == KnowledgeMemoryCoverageReady {
		return true, 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, payload FROM `+table+` WHERE item_id > ? ORDER BY item_id LIMIT ?`, lastID, batchSize)
	if err != nil {
		return false, 0, err
	}
	batch := []promotionRecord{}
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			_ = rows.Close()
			return false, 0, err
		}
		batch = append(batch, promotionRecord{recordType: recordType, recordID: id, payload: []byte(payload)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, 0, err
	}
	_ = rows.Close()
	if len(batch) == 0 {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_memory_search_documents WHERE record_type = ?`, recordType).Scan(&count); err != nil {
			return false, 0, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE knowledge_memory_index_cursor SET last_record_id = '', eligible_count = ?, indexed_count = ?, state = ?, updated_at = ? WHERE record_type = ?`, count, count, KnowledgeMemoryCoverageReady, nowUTCString(), recordType); err != nil {
			return false, 0, err
		}
		return true, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	projected := 0
	for _, row := range batch {
		projection, err := projectionFromRecord(row)
		if err != nil {
			_ = tx.Rollback()
			return false, 0, err
		}
		if err := deleteSearchProjectionTx(ctx, tx, row.recordType, row.recordID); err != nil {
			_ = tx.Rollback()
			return false, 0, err
		}
		if projection != nil {
			if err := insertSearchProjectionTx(ctx, tx, *projection); err != nil {
				_ = tx.Rollback()
				return false, 0, err
			}
			projected++
		}
	}
	lastID = batch[len(batch)-1].recordID
	indexedCount += projected
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_memory_index_cursor SET last_record_id = ?, indexed_count = ?, updated_at = ? WHERE record_type = ?`, lastID, indexedCount, nowUTCString(), recordType); err != nil {
		_ = tx.Rollback()
		return false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return false, len(batch), nil
}

func projectionFromRecord(row promotionRecord) (*safeSearchProjection, error) {
	switch row.recordType {
	case creativeKnowledgeRecordType:
		var item domainkm.CreativeKnowledgeItem
		if err := json.Unmarshal(row.payload, &item); err != nil {
			return nil, err
		}
		if err := domainkm.ValidateCreativeKnowledgeItem(item); err != nil {
			return nil, err
		}
		return creativeSearchProjection(item), nil
	case newsKnowledgeRecordType:
		var item domainkm.NewsKnowledgeItem
		if err := json.Unmarshal(row.payload, &item); err != nil {
			return nil, err
		}
		if err := domainkm.ValidateNewsKnowledgeItem(item); err != nil {
			return nil, err
		}
		return newsSearchProjection(item), nil
	default:
		return nil, fmt.Errorf("unsupported projection record type %q", row.recordType)
	}
}
