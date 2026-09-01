package dcimigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestManifestSchemaUsesLogicalSourceBindingV2(t *testing.T) {
	if ManifestSchemaVersion != "rencrow.identity.dci-migration/v2" {
		t.Fatalf("ManifestSchemaVersion = %q, want v2", ManifestSchemaVersion)
	}
}

func TestManifestV2UsesExactLogicalHashMapsAndRejectsV1(t *testing.T) {
	manifest := runTestSnapshot(t, makeTestSnapshot(t, "manifest-v2"), filepath.Join(t.TempDir(), "manifest.json"))
	if manifest.LogicalHashAlgorithm != LogicalHashAlgorithm {
		t.Fatalf("logical hash algorithm = %q, want %q", manifest.LogicalHashAlgorithm, LogicalHashAlgorithm)
	}
	assertExactHashKeys(t, manifest.SourceDatabaseLogicalSHA256, requiredDatabaseLogicalHashKeys)
	assertExactHashKeys(t, manifest.SourceSchemaSHA256, requiredSchemaHashKeys)
	assertExactHashKeys(t, manifest.SourceDCIClassificationSHA256, requiredClassificationHashKeys)
	assertExactHashKeys(t, manifest.SourceFileSHA256, requiredFileHashKeys)
	assertExactHashKeys(t, manifest.SourceNonDCILogicalSHA256, requiredNonDCILogicalHashKeys)
	if manifest.SourceDCIClassificationSHA256["source_dci_jsonl"] != manifest.SourceFileSHA256["source_dci_jsonl"] {
		t.Fatal("JSONL classification and file hash must bind the same byte content")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "source_logical_sha256") {
		t.Fatal("v2 manifest retained the ambiguous source_logical_sha256 field")
	}
	v1 := manifest
	v1.SchemaVersion = "rencrow.identity.dci-migration/v1"
	if err := validateManifest(v1); err == nil {
		t.Fatal("v1 receipt unexpectedly validated")
	}
}

func TestBlockedManifestV2ReceiptIsBoundedAndRedacted(t *testing.T) {
	snapshot := makeTestSnapshot(t, "blocked-v2-receipt")
	manifest, err := DryRun(context.Background(), Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl",
		SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive",
		Manifest: "blocked.json", Expected: ExpectedCounts{Searches: 99, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	})
	if err == nil || manifest.Status != StatusBlocked || manifest.SchemaVersion != ManifestSchemaVersion {
		t.Fatalf("blocked result = %#v, err=%v", manifest, err)
	}
	data, err := os.ReadFile(filepath.Join(snapshot, "blocked.json"))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > maxManifestBytes {
		t.Fatalf("blocked manifest exceeds bound: %d", len(data))
	}
	info, err := os.Stat(filepath.Join(snapshot, "blocked.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("blocked manifest permissions = %o, want 600", info.Mode().Perm())
	}
	for _, forbidden := range []string{"legacy-search-1", "legacy-evidence-1", "evidence text", "canonical migration test", "spec.md"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("blocked manifest leaked %q", forbidden)
		}
	}
}

func TestDryRunLogicalSourceHashesAreSnapshotRootIndependent(t *testing.T) {
	first := runTestSnapshot(t, makeTestSnapshot(t, filepath.Join("logical", "first")), filepath.Join(t.TempDir(), "first-manifest.json"))
	second := runTestSnapshot(t, makeTestSnapshot(t, filepath.Join("logical", "second")), filepath.Join(t.TempDir(), "second-manifest.json"))
	for _, item := range []struct {
		name   string
		first  map[string]string
		second map[string]string
	}{
		{name: "database", first: first.SourceDatabaseLogicalSHA256, second: second.SourceDatabaseLogicalSHA256},
		{name: "schema", first: first.SourceSchemaSHA256, second: second.SourceSchemaSHA256},
		{name: "classification", first: first.SourceDCIClassificationSHA256, second: second.SourceDCIClassificationSHA256},
		{name: "file", first: first.SourceFileSHA256, second: second.SourceFileSHA256},
		{name: "non_dci", first: first.SourceNonDCILogicalSHA256, second: second.SourceNonDCILogicalSHA256},
	} {
		if !reflect.DeepEqual(item.first, item.second) {
			t.Fatalf("root-dependent %s source hashes: first=%#v second=%#v", item.name, item.first, item.second)
		}
	}
}

func TestSQLiteLogicalHashIgnoresInsertionOrderPageSizeAndVacuum(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first.db")
	second := filepath.Join(t.TempDir(), "second.db")
	writeLogicalHashFixture(t, first, 4096, []int{1, 2, 3})
	writeLogicalHashFixture(t, second, 8192, []int{3, 1, 2})
	firstHash := readLogicalHash(t, first, nil)
	secondHash := readLogicalHash(t, second, nil)
	if firstHash != secondHash {
		t.Fatalf("logical hash depends on insertion order/page layout: first=%#v second=%#v", firstHash, secondHash)
	}
}

func TestSQLiteLogicalHashBindsTimestampStorageValue(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "timestamp-first.db")
	secondPath := filepath.Join(t.TempDir(), "timestamp-second.db")
	integerPath := filepath.Join(t.TempDir(), "timestamp-integer.db")
	writeTimestampLogicalHashFixture(t, firstPath, "2026-08-31T00:00:00+00:00")
	writeTimestampLogicalHashFixture(t, secondPath, "2026-08-30T20:00:00-04:00")
	writeTimestampLogicalHashFixture(t, integerPath, int64(1788134400))

	first := readLogicalHash(t, firstPath, nil)
	second := readLogicalHash(t, secondPath, nil)
	integer := readLogicalHash(t, integerPath, nil)
	if first.Full == second.Full {
		t.Fatalf("distinct TIMESTAMP TEXT encodings unexpectedly share full hash: first=%#v second=%#v", first, second)
	}
	if first.Schema != second.Schema || first.Schema != integer.Schema {
		t.Fatalf("value/storage-class mutation changed schema hash: first=%#v second=%#v integer=%#v", first, second, integer)
	}
	if first.Full == integer.Full {
		t.Fatalf("TIMESTAMP TEXT and INTEGER storage classes unexpectedly share full hash: text=%#v integer=%#v", first, integer)
	}
	if got := sqliteStorageClass(t, firstPath); got != "text" {
		t.Fatalf("first TIMESTAMP storage class = %q, want text", got)
	}
	if got := sqliteStorageClass(t, integerPath); got != "integer" {
		t.Fatalf("integer TIMESTAMP storage class = %q, want integer", got)
	}
	if got := sqliteValueType(t, firstPath, `SELECT happened FROM events`); got != "time.Time" {
		t.Fatalf("declared TIMESTAMP direct scan type = %q, want time.Time for regression fixture", got)
	}
	if got := sqliteValueType(t, firstPath, `SELECT +happened AS happened FROM events`); got != "string" {
		t.Fatalf("unary-plus TEXT scan type = %q, want string", got)
	}
	if got := sqliteValueType(t, integerPath, `SELECT +happened AS happened FROM events`); got != "int64" {
		t.Fatalf("unary-plus INTEGER scan type = %q, want int64", got)
	}
}

func TestSQLiteLogicalHashClosesTableListBeforeColumnQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "multiple-tables.db")
	db := openTestDB(t, path)
	for _, table := range []string{"first", "second", "third"} {
		mustExec(t, db, `CREATE TABLE `+table+` (id INTEGER PRIMARY KEY, value TEXT)`)
		mustExec(t, db, `INSERT INTO `+table+`(id,value) VALUES(1,?)`, table)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := openSQLiteReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()

	legacyContext, cancelLegacy := context.WithTimeout(context.Background(), 100*time.Millisecond)
	legacyErr := legacyLogicalTablesForTest(legacyContext, readOnly)
	cancelLegacy()
	if legacyErr == nil || !errors.Is(legacyErr, context.DeadlineExceeded) {
		t.Fatalf("old rows-held structure err = %v, want context deadline", legacyErr)
	}

	currentContext, cancelCurrent := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelCurrent()
	started := time.Now()
	if _, _, err := hashSQLiteSchema(currentContext, readOnly); err != nil {
		t.Fatalf("materialized table-list structure failed: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 300*time.Millisecond {
		t.Fatalf("materialized table-list structure took %s", elapsed)
	}
}

func TestSQLiteLogicalHashIncludesShadowAndProjectionContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow-projection.db")
	db := openTestDB(t, path)
	mustExec(t, db, `CREATE TABLE source(id INTEGER PRIMARY KEY, body TEXT)`)
	mustExec(t, db, `CREATE TABLE source_projection(source_id INTEGER PRIMARY KEY, normalized TEXT)`)
	mustExec(t, db, `CREATE VIRTUAL TABLE source_fts USING fts5(body, content='source', content_rowid='id')`)
	mustExec(t, db, `INSERT INTO source(id,body) VALUES(1,'before')`)
	mustExec(t, db, `INSERT INTO source_projection(source_id,normalized) VALUES(1,'before')`)
	mustExec(t, db, `INSERT INTO source_fts(rowid,body) VALUES(1,'before')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	base := readLogicalHash(t, path, nil)
	mutateLogicalDB(t, path, `UPDATE source_projection SET normalized='projection-after' WHERE source_id=1`)
	projection := readLogicalHash(t, path, nil)
	if projection.Full == base.Full {
		t.Fatal("projection mutation did not change full logical hash")
	}
	mutateLogicalDB(t, path, `UPDATE source_fts SET body='shadow-after' WHERE rowid=1`)
	shadow := readLogicalHash(t, path, nil)
	if shadow.Full == projection.Full {
		t.Fatal("shadow-table mutation did not change full logical hash")
	}
}

func TestSQLiteLogicalHashTracksContentSchemaAllocatorAndDuplicates(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "base.db")
	writeLogicalHashFixture(t, basePath, 4096, []int{1, 2, 3})
	base := readLogicalHash(t, basePath, nil)

	contentPath := filepath.Join(t.TempDir(), "content.db")
	copyFile(t, basePath, contentPath)
	mutateLogicalDB(t, contentPath, `UPDATE records SET label='changed' WHERE id=1`)
	content := readLogicalHash(t, contentPath, nil)
	if content.Full == base.Full || content.Schema != base.Schema {
		t.Fatalf("content mutation hashes = %#v, base=%#v", content, base)
	}

	duplicatePath := filepath.Join(t.TempDir(), "duplicate.db")
	copyFile(t, basePath, duplicatePath)
	mutateLogicalDB(t, duplicatePath, `INSERT INTO records(id,label,score,payload,happened) SELECT id,label,score,payload,happened FROM records WHERE id=1`)
	duplicate := readLogicalHash(t, duplicatePath, nil)
	if duplicate.Full == base.Full || duplicate.Schema != base.Schema {
		t.Fatalf("duplicate row did not change content hash: duplicate=%#v base=%#v", duplicate, base)
	}

	allocatorPath := filepath.Join(t.TempDir(), "allocator.db")
	copyFile(t, basePath, allocatorPath)
	mutateLogicalDB(t, allocatorPath, `UPDATE sqlite_sequence SET seq=99 WHERE name='allocator'`)
	allocator := readLogicalHash(t, allocatorPath, nil)
	if allocator.Full == base.Full || allocator.Schema != base.Schema {
		t.Fatalf("allocator mutation hashes = %#v, base=%#v", allocator, base)
	}

	schemaPath := filepath.Join(t.TempDir(), "schema.db")
	copyFile(t, basePath, schemaPath)
	mutateLogicalDB(t, schemaPath, `ALTER TABLE records ADD COLUMN extra TEXT`)
	schema := readLogicalHash(t, schemaPath, nil)
	if schema.Schema == base.Schema || schema.Full == base.Full {
		t.Fatalf("schema mutation hashes = %#v, base=%#v", schema, base)
	}
}

func TestL1LogicalHashExcludesOnlyClassifiedDCIRows(t *testing.T) {
	snapshot := makeTestSnapshot(t, "l1-logical-hash")
	path := filepath.Join(snapshot, "source-l1")
	baseData, baseHashes, err := loadL1Current(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseData.StagingIDs) != 1 || len(baseData.RegistryIDs) != 1 {
		t.Fatalf("classification sets = staging=%#v registry=%#v", baseData.StagingIDs, baseData.RegistryIDs)
	}

	mutateLogicalDB(t, path, `INSERT INTO l1_staging_item(id,kind,namespace,event_id,source_id,fetched_at,raw_text,raw_hash,summary_draft,keywords_json,license_note,validation_status,meta_json,created_at,updated_at) VALUES('news-1','news','kb:news','news-event','news-source','2026-08-31T00:00:01Z','news text','news-hash','','[]','','pending','{}','2026-08-31T00:00:01Z','2026-08-31T00:00:01Z')`)
	_, nonDCIHashes, err := loadL1Current(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if nonDCIHashes.DatabaseLogical == baseHashes.DatabaseLogical || nonDCIHashes.NonDCI == baseHashes.NonDCI {
		t.Fatalf("non-DCI mutation did not change full/non-DCI hash: before=%#v after=%#v", baseHashes, nonDCIHashes)
	}
	if nonDCIHashes.Classification != baseHashes.Classification {
		t.Fatalf("non-DCI mutation changed DCI classification hash: before=%q after=%q", baseHashes.Classification, nonDCIHashes.Classification)
	}

	mutateLogicalDB(t, path, `UPDATE l1_staging_item SET raw_text='changed DCI text', raw_hash='a7118355dfe4aa1a600c10aa3fb6e58c756c8b94e3d5d958eaf4d0d10d64df86' WHERE id='staging-1'`)
	_, dciHashes, err := loadL1Current(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if dciHashes.DatabaseLogical == nonDCIHashes.DatabaseLogical || dciHashes.Classification == nonDCIHashes.Classification {
		t.Fatalf("DCI mutation did not change full/classification hash: before=%#v after=%#v", nonDCIHashes, dciHashes)
	}
	if dciHashes.NonDCI != nonDCIHashes.NonDCI {
		t.Fatalf("DCI mutation changed non-DCI hash: before=%q after=%q", nonDCIHashes.NonDCI, dciHashes.NonDCI)
	}
}

func TestSQLiteLogicalHashRejectsCellRowAndContextBounds(t *testing.T) {
	cellPath := filepath.Join(t.TempDir(), "cell.db")
	writeLogicalHashFixture(t, cellPath, 4096, []int{1})
	mutateLogicalDB(t, cellPath, `INSERT INTO records(id,label,score,payload,happened) VALUES(99,?,0.0,X'',?)`, strings.Repeat("x", maxLogicalCellSize+1), time.Now().UTC().Format(time.RFC3339Nano))
	if err := readLogicalHashError(t, cellPath, nil); err == nil {
		t.Fatal("oversized cell unexpectedly hashed")
	}
	if _, err := typedRowDigest([]any{struct{}{}}); err == nil {
		t.Fatal("unknown SQLite value type unexpectedly hashed")
	}

	rowPath := filepath.Join(t.TempDir(), "rows.db")
	db := openTestDB(t, rowPath)
	mustExec(t, db, `CREATE TABLE rows(value INTEGER)`)
	mustExec(t, db, `INSERT INTO rows(value) VALUES(1),(2)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := openSQLiteReadOnly(context.Background(), rowPath)
	if err != nil {
		t.Fatal(err)
	}
	table := logicalTable{Name: "rows", Columns: []logicalColumn{{CID: 0, Name: "value"}}}
	if _, err := hashSQLiteTableRows(context.Background(), readOnly, table, nil, 1); err == nil {
		t.Fatal("oversized row count unexpectedly hashed")
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = openSQLiteReadOnly(context.Background(), cellPath)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = hashSQLiteLogical(canceled, db, nil)
	_ = db.Close()
	if err == nil {
		t.Fatal("canceled logical hash unexpectedly succeeded")
	}
}

func assertExactHashKeys(t *testing.T, values map[string]string, required []string) {
	t.Helper()
	if len(values) != len(required) {
		t.Fatalf("hash map keys = %#v, want %v", values, required)
	}
	for _, key := range required {
		value, ok := values[key]
		if !ok || !isLowerHexSHA256(value) {
			t.Fatalf("hash map missing/invalid %q: %#v", key, values)
		}
	}
}

func readLogicalHash(t *testing.T, path string, exclude logicalRowExcluder) logicalHashes {
	t.Helper()
	hashes, err := readLogicalHashResult(path, exclude)
	if err != nil {
		t.Fatalf("readLogicalHash(%s): %v", path, err)
	}
	return hashes
}

func readLogicalHashError(t *testing.T, path string, exclude logicalRowExcluder) error {
	t.Helper()
	_, err := readLogicalHashResult(path, exclude)
	return err
}

func readLogicalHashResult(path string, exclude logicalRowExcluder) (logicalHashes, error) {
	db, err := openSQLiteReadOnly(context.Background(), path)
	if err != nil {
		return logicalHashes{}, err
	}
	hashes, hashErr := hashSQLiteLogical(context.Background(), db, exclude)
	closeErr := db.Close()
	if hashErr != nil {
		return logicalHashes{}, hashErr
	}
	return hashes, closeErr
}

func writeLogicalHashFixture(t *testing.T, path string, pageSize int, order []int) {
	t.Helper()
	db := openTestDB(t, path)
	mustExec(t, db, fmt.Sprintf("PRAGMA page_size = %d", pageSize))
	mustExec(t, db, `PRAGMA application_id = 42`)
	mustExec(t, db, `CREATE TABLE records(id INTEGER, label TEXT, score REAL, payload BLOB, happened TEXT)`)
	mustExec(t, db, `CREATE TABLE allocator(id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT)`)
	rows := map[int][]any{
		1: {1, "one", 1.25, []byte{1, 2}, "2026-08-31T00:00:01Z"},
		2: {2, "two", 2.5, []byte{3, 4}, "2026-08-31T00:00:02Z"},
		3: {3, "three", 3.75, []byte{5, 6}, "2026-08-31T00:00:03Z"},
	}
	for _, id := range order {
		values := rows[id]
		mustExec(t, db, `INSERT INTO records(id,label,score,payload,happened) VALUES(?,?,?,?,?)`, values...)
	}
	for _, id := range order {
		mustExec(t, db, `INSERT INTO allocator(id,label) VALUES(?,?)`, id, fmt.Sprintf("allocator-%d", id))
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTimestampLogicalHashFixture(t *testing.T, path string, value any) {
	t.Helper()
	db := openTestDB(t, path)
	mustExec(t, db, `CREATE TABLE events(id INTEGER PRIMARY KEY, happened TIMESTAMP NOT NULL)`)
	mustExec(t, db, `INSERT INTO events(id,happened) VALUES(1,?)`, value)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func sqliteStorageClass(t *testing.T, path string) string {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	var class string
	if err := db.QueryRow(`SELECT typeof(happened) FROM events`).Scan(&class); err != nil {
		t.Fatal(err)
	}
	return class
}

func sqliteValueType(t *testing.T, path, query string) string {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	var value any
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%T", value)
}

func mutateLogicalDB(t *testing.T, path, query string, args ...any) {
	t.Helper()
	db := openTestDB(t, path)
	mustExec(t, db, query, args...)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// legacyLogicalTablesForTest preserves the pre-fix shape: the schema rows
// remain open while table_xinfo is queried on a one-connection database.
// It must hit the deadline, proving the regression test can distinguish the
// old deadlock from the fixed materialize-close-query sequence.
func legacyLogicalTablesForTest(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if _, err := logicalColumns(ctx, db, name); err != nil {
			return err
		}
	}
	return rows.Err()
}
