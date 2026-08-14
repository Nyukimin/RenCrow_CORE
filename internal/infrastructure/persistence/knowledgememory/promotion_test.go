package knowledgememory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

func TestImportJSONLToSQLiteCreatesPromotionReceiptWithoutChangingSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := NewJSONLStore(root)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	if err := source.SaveCreativeKnowledgeItem(ctx, domainkm.CreativeKnowledgeItem{
		ItemID: "creative-1", Title: "公開作品", WorkType: "映画", Status: "reviewed", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := source.SaveNewsKnowledgeItem(ctx, domainkm.NewsKnowledgeItem{
		ItemID: "news-1", Source: "公的情報源", Topic: "未審査", Status: "candidate", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	sourceFiles := []string{
		filepath.Join(root, "creative_knowledge.jsonl"),
		filepath.Join(root, "news_knowledge.jsonl"),
	}
	before := readFilesForTest(t, sourceFiles)
	sqlitePath := filepath.Join(t.TempDir(), "knowledge_memory.db")

	report, err := ImportJSONLToSQLite(ctx, root, sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceCount != 2 || report.ImportedCount != 2 {
		t.Fatalf("manifest counts = %d/%d, want 2/2: %#v", report.SourceCount, report.ImportedCount, report)
	}
	if report.Coverage.State != KnowledgeMemoryCoverageReady || report.Coverage.EligibleCount != 1 || report.Coverage.IndexedCount != 1 {
		t.Fatalf("coverage = %#v", report.Coverage)
	}
	after := readFilesForTest(t, sourceFiles)
	for path, contents := range before {
		if string(after[path]) != string(contents) {
			t.Fatalf("source JSONL changed during import: %s", path)
		}
	}

	store, err := OpenSQLiteStore(sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	readiness, err := store.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.DatabaseAvailable || !readiness.SchemaReady || !readiness.IndexReady || readiness.Coverage.State != KnowledgeMemoryCoverageReady || readiness.IntegrityState != KnowledgeMemoryIntegrityReady {
		t.Fatalf("readiness = %#v", readiness)
	}
	results, err := store.Search(ctx, appkm.SearchRequest{Query: "公開作品", Scope: appkm.SearchScope{Scope: appkm.SearchScopePublic}, RecordType: "creative_knowledge", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].RecordID != "creative-1" || results[0].Summary == "" && results[0].Title == "" {
		t.Fatalf("search results = %#v", results)
	}
	if _, err := store.db.Exec(`UPDATE creative_knowledge SET payload = payload WHERE item_id = ?`, "creative-1"); err == nil {
		t.Fatal("runtime SQLite store unexpectedly permits writes")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	writable, err := OpenSQLiteStoreWritable(sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.db.Exec(`UPDATE creative_knowledge SET payload = payload WHERE item_id = ?`, "creative-1"); err != nil {
		t.Fatalf("Viewer writable handle rejected an existing database: %v", err)
	}
	_ = writable.Close()
}

func TestPrivateCandidateDoesNotInvalidatePromotedReadinessAfterRestart(t *testing.T) {
	ctx := context.Background()
	sourceRoot := t.TempDir()
	source := NewJSONLStore(sourceRoot)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	if err := source.SaveCreativeKnowledgeItem(ctx, domainkm.CreativeKnowledgeItem{
		ItemID: "creative-imported-1", Title: "Imported public work", Status: "reviewed", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	sqlitePath := filepath.Join(t.TempDir(), "knowledge_memory.db")
	if _, err := ImportJSONLToSQLite(ctx, sourceRoot, sqlitePath); err != nil {
		t.Fatal(err)
	}

	candidate := domainkm.CreativeKnowledgeItem{
		ItemID:     "candidate-owner-1",
		UserID:     "user-1",
		Title:      "Private owner candidate",
		Status:     "candidate",
		Visibility: "private",
		CreatedAt:  now.Add(time.Minute),
	}
	receipt := KnowledgeMemoryRequestReceipt{
		RequestID:   "request-owner-candidate-1",
		UserID:      candidate.UserID,
		ActorID:     "shiro",
		PayloadHash: "sha256:owner-candidate-1",
		ItemID:      candidate.ItemID,
		CreatedAt:   now.Add(2 * time.Minute),
	}
	writable, err := OpenSQLiteStoreWritable(sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := writable.SaveCreativeCandidateWithReceipt(ctx, candidate, receipt)
	if err != nil {
		_ = writable.Close()
		t.Fatal(err)
	}
	if replayed {
		_ = writable.Close()
		t.Fatal("first private candidate write was reported as a replay")
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLiteStore(sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	readiness, err := store.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.DatabaseAvailable || !readiness.SchemaReady || !readiness.IndexReady || readiness.Coverage.State != KnowledgeMemoryCoverageReady || readiness.IntegrityState != KnowledgeMemoryIntegrityReady {
		t.Fatalf("readiness after private candidate write = %#v", readiness)
	}

	got, found, err := store.FindCreativeCandidateByID(ctx, candidate.UserID, candidate.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.ItemID != candidate.ItemID || got.UserID != candidate.UserID || got.Title != candidate.Title {
		t.Fatalf("private candidate recall = %#v, found=%v", got, found)
	}
	results, err := store.Search(ctx, appkm.SearchRequest{
		Query:      candidate.Title,
		Scope:      appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: candidate.UserID},
		RecordType: creativeKnowledgeRecordType,
		Limit:      20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("private candidate unexpectedly searchable: %#v", results)
	}
	var projected int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_memory_search_documents WHERE record_type = ? AND record_id = ?`, creativeKnowledgeRecordType, candidate.ItemID).Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if projected != 0 {
		t.Fatalf("private candidate has a search projection: %d", projected)
	}
}

func TestOpenSQLiteStoreDoesNotPromoteAnUnreadyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unready.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	readiness, err := store.Readiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if readiness.DatabaseAvailable && readiness.SchemaReady {
		t.Fatalf("empty database was reported ready: %#v", readiness)
	}
}

func TestSQLiteReadinessClassifiesCoverageAndIntegrityGates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "partial.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCreativeKnowledgeItem(ctx, domainkm.CreativeKnowledgeItem{
		ItemID: "partial-1", Title: "部分投入", Status: "reviewed", CreatedAt: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := opened.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.DatabaseAvailable || !readiness.SchemaReady || !readiness.IndexReady || readiness.Coverage.State != KnowledgeMemoryCoverageIndexing {
		t.Fatalf("partial promotion readiness = %#v", readiness)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}

	sourceRoot := t.TempDir()
	source := NewJSONLStore(sourceRoot)
	if err := source.SaveCreativeKnowledgeItem(ctx, domainkm.CreativeKnowledgeItem{
		ItemID: "partial-1", Title: "部分投入", Status: "reviewed", CreatedAt: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportJSONLToSQLite(ctx, sourceRoot, path); err != nil {
		t.Fatal(err)
	}
	opened, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.db.Exec(`UPDATE creative_knowledge SET payload = ? WHERE item_id = ?`, `{"item_id":"partial-1","title":"改変","status":"reviewed","created_at":"2026-08-12T12:00:00Z"}`, "partial-1"); err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	readiness, err = opened.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.IntegrityState != KnowledgeMemoryIntegrityFailed || readiness.Coverage.State != KnowledgeMemoryCoverageReady {
		t.Fatalf("tampered promotion readiness = %#v", readiness)
	}
	_ = opened.Close()
}

func TestBackfillSearchProjectionResumesFromPersistedCursor(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i, id := range []string{"resume-a", "resume-b", "resume-c"} {
		if err := store.SaveCreativeKnowledgeItem(ctx, domainkm.CreativeKnowledgeItem{
			ItemID: id, Title: "再開作品", Status: "reviewed", CreatedAt: time.Date(2026, 8, 12, 12+i, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.BackfillSearchProjection(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != KnowledgeMemoryCoverageIndexing {
		t.Fatalf("first bounded pass = %#v", first)
	}
	var firstCursor string
	if err := store.db.QueryRow(`SELECT last_record_id FROM knowledge_memory_index_cursor WHERE record_type = ?`, creativeKnowledgeRecordType).Scan(&firstCursor); err != nil {
		t.Fatal(err)
	}
	if firstCursor != "resume-a" {
		t.Fatalf("first cursor = %q, want resume-a", firstCursor)
	}
	second, err := store.BackfillSearchProjection(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != KnowledgeMemoryCoverageIndexing {
		t.Fatalf("second bounded pass = %#v", second)
	}
	var secondCursor string
	if err := store.db.QueryRow(`SELECT last_record_id FROM knowledge_memory_index_cursor WHERE record_type = ?`, creativeKnowledgeRecordType).Scan(&secondCursor); err != nil {
		t.Fatal(err)
	}
	if secondCursor != "resume-b" {
		t.Fatalf("second cursor = %q, want resume-b", secondCursor)
	}
	for i := 0; i < 8; i++ {
		coverage, err := store.BackfillSearchProjection(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if coverage.State == KnowledgeMemoryCoverageReady {
			return
		}
	}
	t.Fatal("bounded backfill did not reach ready state")
}

func TestOpenSQLiteStoresConfigureSingleConnectionAndBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge_memory.db")
	seed, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	for name, open := range map[string]func(string) (*SQLiteStore, error){
		"read-only": OpenSQLiteStore,
		"writable":  OpenSQLiteStoreWritable,
	} {
		store, err := open(path)
		if err != nil {
			t.Fatalf("%s open: %v", name, err)
		}
		if got := store.db.Stats().MaxOpenConnections; got != 1 {
			_ = store.Close()
			t.Fatalf("%s max open connections=%d want=1", name, got)
		}
		var busyTimeout int
		if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			_ = store.Close()
			t.Fatalf("%s busy timeout query: %v", name, err)
		}
		if busyTimeout != 5000 {
			_ = store.Close()
			t.Fatalf("%s busy timeout=%d want=5000", name, busyTimeout)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("%s close: %v", name, err)
		}
	}
}

func readFilesForTest(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out[path] = contents
	}
	return out
}
