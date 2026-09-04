package archivesqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/parquet-go/parquet-go"
)

func TestArchiveSQLiteStoreOwnerParquetExportEmptyArchive(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "exports")

	req := parquetExportRequest("empty-request", "ren", "ren")
	req.CreatedAt = time.Date(2026, 8, 16, 1, 2, 3, 4, time.UTC)
	result, err := store.ExportConversationArchiveParquet(ctx, req, root)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	if result.Receipt.Status != "completed" || result.Receipt.InputCount != 0 || result.Receipt.OutputCount != 5 || result.Receipt.IdempotentReplay {
		t.Fatalf("receipt = %+v", result.Receipt)
	}
	if result.RunRelPath != "runs/empty-request" || result.ManifestRelPath != "runs/empty-request/manifest.json" {
		t.Fatalf("relative paths = %q %q", result.RunRelPath, result.ManifestRelPath)
	}

	runDir := filepath.Join(root, result.RunRelPath)
	assertMode(t, runDir, 0o700)
	manifestPath := filepath.Join(runDir, "manifest.json")
	assertMode(t, manifestPath, 0o600)
	manifest := readParquetManifest(t, manifestPath)
	if manifest.Format != domainmemory.ConversationArchiveParquetFormat || manifest.ExportID != "empty-request" || manifest.TotalRows != 0 || len(manifest.Files) != 5 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if result.CreatedAt != req.CreatedAt || manifest.CreatedAt != req.CreatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("created_at = result=%s manifest=%s request=%s", result.CreatedAt, manifest.CreatedAt, req.CreatedAt)
	}
	for _, entry := range manifest.Files {
		path := filepath.Join(runDir, filepath.FromSlash(entry.RelativePath))
		assertMode(t, path, 0o600)
		if entry.RowCount != 0 || entry.Bytes <= 0 || entry.SHA256 != testSHA256File(t, path) {
			t.Fatalf("manifest entry = %+v", entry)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			t.Fatal(err)
		}
		reader, err := parquet.OpenFile(file, info.Size())
		if err != nil {
			file.Close()
			t.Fatalf("open parquet %s: %v", entry.RelativePath, err)
		}
		if reader.NumRows() != 0 {
			t.Fatalf("rows %s = %d", entry.RelativePath, reader.NumRows())
		}
		_ = file.Close()
	}
	if _, err := os.Stat(filepath.Join(root, ".staging")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging should be cleaned, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "empty-request")); err != nil {
		t.Fatalf("final run missing: %v", err)
	}
}

func TestArchiveSQLiteStoreOwnerParquetExportPopulatedAndSourceUnchanged(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC)
	insertArchiveRows(t, store.db, now)
	before := archiveSourceCounts(t, store.db)

	root := filepath.Join(t.TempDir(), "exports")
	result, err := store.ExportConversationArchiveParquet(ctx, parquetExportRequest("populated-request", "ren", "ren"), root)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	if result.Receipt.InputCount != 6 || result.Receipt.OutputCount != 5 {
		t.Fatalf("receipt = %+v", result.Receipt)
	}
	manifest := readParquetManifest(t, filepath.Join(root, result.ManifestRelPath))
	if manifest.TotalRows != 6 {
		t.Fatalf("manifest total rows = %d", manifest.TotalRows)
	}
	if got := result.Files; len(got) != 5 {
		t.Fatalf("files = %+v", got)
	}
	threadRows, err := parquet.ReadFile[threadSummaryParquetRow](filepath.Join(root, result.RunRelPath, "thread_summaries.parquet"))
	if err != nil {
		t.Fatalf("read thread parquet: %v", err)
	}
	if len(threadRows) != 2 || threadRows[0].ThreadID != string(archiveTestThreadID("owner-thread-2")) || threadRows[1].ThreadID != string(archiveTestThreadID("owner-thread-1")) || threadRows[0].ThreadSeq != 2 || threadRows[1].ThreadSeq != 1 || threadRows[0].ThreadKind != "user_conversation" || threadRows[1].ThreadKind != "user_conversation" {
		t.Fatalf("thread ordering = %+v", threadRows)
	}
	memoryRows, err := parquet.ReadFile[l1MemoryEventParquetRow](filepath.Join(root, result.RunRelPath, "l1", "l1_memory_event.parquet"))
	if err != nil {
		t.Fatalf("read memory parquet: %v", err)
	}
	if len(memoryRows) != 1 || memoryRows[0].ID != "m-1" || memoryRows[0].ThreadID != string(archiveTestThreadID("owner-memory-1")) || memoryRows[0].ThreadSeq != 1 || memoryRows[0].ThreadKind != "user_conversation" {
		t.Fatalf("memory rows = %+v", memoryRows)
	}
	after := archiveSourceCounts(t, store.db)
	if before != after {
		t.Fatalf("source counts changed: before=%+v after=%+v", before, after)
	}
	paths := make([]string, 0, len(result.Files))
	for _, entry := range result.Files {
		paths = append(paths, entry.RelativePath)
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("file entries not sorted: %v", paths)
	}
}

func TestArchiveSQLiteStoreOwnerParquetExportReplayAndBindingConflict(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "exports")
	req := parquetExportRequest("replay-request", "ren", "ren")
	first, err := store.ExportConversationArchiveParquet(ctx, req, root)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, first.ManifestRelPath)
	before, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	replay, err := store.ExportConversationArchiveParquet(ctx, req, root)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if !replay.Receipt.IdempotentReplay || replay.ManifestSHA256 != first.ManifestSHA256 || replay.RunRelPath != first.RunRelPath {
		t.Fatalf("replay = %+v first=%+v", replay, first)
	}
	after, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("replay rewrote manifest: before=%v after=%v", before.ModTime(), after.ModTime())
	}
	manifestBackup, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExportConversationArchiveParquet(ctx, req, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		t.Fatalf("replay missing artifact = %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestBackup, 0o600); err != nil {
		t.Fatal(err)
	}
	conflict := req
	conflict.ActorID = "shiro"
	_, err = store.ExportConversationArchiveParquet(ctx, conflict, root)
	if !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		t.Fatalf("binding conflict = %v", err)
	}
}

func TestArchiveSQLiteStoreOwnerParquetRejectsUnsafeRootIDsAndOwnerLookup(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "exports")
	request := parquetExportRequest("safe-request", "ren", "ren")
	if _, err := store.ExportConversationArchiveParquet(ctx, request, root); err != nil {
		t.Fatal(err)
	}
	for _, requestID := range []string{"../x", "encoded%2Fseparator", "runs/x"} {
		request.RequestID = requestID
		if _, err := store.ExportConversationArchiveParquet(ctx, request, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) {
			t.Fatalf("request id %q error = %v", requestID, err)
		}
	}
	if _, err := store.ExportConversationArchiveParquet(ctx, parquetExportRequest("root-request", "ren", "ren"), string(filepath.Separator)); !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("root separator error = %v", err)
	}
	linkRoot := filepath.Join(t.TempDir(), "exports-link")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Logf("root symlink test unavailable: %v", err)
	} else if _, err := store.ExportConversationArchiveParquet(ctx, parquetExportRequest("symlink-root-request", "ren", "ren"), linkRoot); !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("root symlink error = %v", err)
	}
	wrongOwner := l1sqlite.OwnerParquetVerifyRequest{RequestID: "verify-wrong-owner", UserID: "other", ActorID: "other", TargetExportRequestID: "safe-request", PayloadHash: "hash", CreatedAt: time.Now().UTC()}
	if _, err := store.VerifyConversationArchiveParquet(ctx, wrongOwner, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound) {
		t.Fatalf("wrong owner error = %v", err)
	}
	notFound := wrongOwner
	notFound.UserID = "ren"
	notFound.ActorID = "ren"
	notFound.TargetExportRequestID = "missing-request"
	if _, err := store.VerifyConversationArchiveParquet(ctx, notFound, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound) {
		t.Fatalf("not found error = %v", err)
	}
}

func TestArchiveSQLiteStoreOwnerParquetExportReceiptFailureQuarantinesRun(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "exports")
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_parquet_receipt BEFORE INSERT ON conversation_archive_parquet_receipt BEGIN SELECT RAISE(ABORT, 'receipt failure'); END;`); err != nil {
		t.Fatal(err)
	}
	_, err = store.ExportConversationArchiveParquet(ctx, parquetExportRequest("failure-request", "ren", "ren"), root)
	if !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("receipt failure = %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_archive_parquet_receipt`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("completed receipt count = %d", count)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "failure-request")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final run exposed, err=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".staging"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("staging entries = %v", entries)
	}
	quarantineEntries, err := os.ReadDir(filepath.Join(root, ".quarantine"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err == nil && len(quarantineEntries) == 0 {
		t.Fatal("expected quarantined artifact after receipt failure")
	}
}

func TestArchiveSQLiteStoreOwnerParquetVerifyDetectsTampering(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "exports")
	req := parquetExportRequest("verify-request", "ren", "ren")
	export, err := store.ExportConversationArchiveParquet(ctx, req, root)
	if err != nil {
		t.Fatal(err)
	}
	verifyReq := l1sqlite.OwnerParquetVerifyRequest{RequestID: "verify-now", UserID: "ren", ActorID: "ren", TargetExportRequestID: req.RequestID, PayloadHash: "verify-hash", CreatedAt: time.Now().UTC()}
	verified, err := store.VerifyConversationArchiveParquet(ctx, verifyReq, root)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if verified.Receipt.InputCount != 5 || verified.Receipt.OutputCount != 5 || verified.Receipt.AuditReference != req.RequestID {
		t.Fatalf("verify receipt = %+v", verified.Receipt)
	}

	manifestPath := filepath.Join(root, export.ManifestRelPath)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest parquetManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, export.RunRelPath, filepath.FromSlash(manifest.Files[0].RelativePath))
	original, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, append(original, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyConversationArchiveParquet(ctx, verifyReq, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		t.Fatalf("modified bytes = %v", err)
	}
	if err := os.WriteFile(filePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	extraPath := filepath.Join(root, export.RunRelPath, "extra.txt")
	if err := os.WriteFile(extraPath, []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyConversationArchiveParquet(ctx, verifyReq, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		t.Fatalf("extra file = %v", err)
	}
	if err := os.Remove(extraPath); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(root, export.RunRelPath, "symlink-extra")
	if err := os.Symlink(filepath.Base(manifestPath), symlinkPath); err != nil {
		t.Logf("symlink test unavailable: %v", err)
	} else {
		if _, err := store.VerifyConversationArchiveParquet(ctx, verifyReq, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
			t.Fatalf("symlink = %v", err)
		}
		if err := os.Remove(symlinkPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyConversationArchiveParquet(ctx, verifyReq, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		t.Fatalf("missing file = %v", err)
	}
	if err := os.WriteFile(filePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var tampered map[string]interface{}
	if err := json.Unmarshal(manifestBytes, &tampered); err != nil {
		t.Fatal(err)
	}
	fileEntries := tampered["files"].([]interface{})
	fileEntry := fileEntries[0].(map[string]interface{})
	fileEntry["row_count"] = float64(fileEntry["row_count"].(float64) + 1)
	tamperedManifest, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, tamperedManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	tamperedHash := sha256Bytes(tamperedManifest)
	if _, err := store.db.ExecContext(ctx, `UPDATE conversation_archive_parquet_receipt SET manifest_sha256 = ? WHERE request_id = ?`, tamperedHash, req.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyConversationArchiveParquet(ctx, verifyReq, root); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		t.Fatalf("row-count mismatch = %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE conversation_archive_parquet_receipt SET manifest_sha256 = ? WHERE request_id = ?`, export.ManifestSHA256, req.RequestID); err != nil {
		t.Fatal(err)
	}
}

type testParquetManifest struct {
	Format    string                     `json:"format"`
	ExportID  string                     `json:"export_id"`
	CreatedAt string                     `json:"created_at"`
	TotalRows int64                      `json:"total_rows"`
	Files     []testParquetManifestEntry `json:"files"`
}

type testParquetManifestEntry struct {
	RelativePath string `json:"relative_path"`
	RowCount     int64  `json:"row_count"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
}

// Keep a local alias in the test until the implementation exposes the
// canonical manifest helper type.
type parquetManifest = testParquetManifest

func parquetExportRequest(requestID, userID, actorID string) l1sqlite.OwnerParquetExportRequest {
	return l1sqlite.OwnerParquetExportRequest{RequestID: requestID, UserID: userID, ActorID: actorID, PayloadHash: requestID + "-payload", CreatedAt: time.Now().UTC()}
}

func readParquetManifest(t *testing.T, path string) parquetManifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest parquetManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return manifest
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode %s = %o, want %o", path, info.Mode().Perm(), want)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("symlink artifact: %s", path)
	}
}

func testSHA256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func insertArchiveRows(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO session_thread (thread_id, thread_seq, thread_kind, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel, created_at)
VALUES (?, 2, 'user_conversation', 'thread-2', ?, ?, 'memory', 'summary-2', '[]', '[]', 0, ?), (?, 1, 'user_conversation', 'thread-1', ?, ?, 'memory', 'summary-1', '[]', '[]', 1, ?);`, archiveTestThreadID("owner-thread-2"), now.Add(time.Minute), now.Add(2*time.Minute), now, archiveTestThreadID("owner-thread-1"), now.Add(3*time.Minute), now.Add(4*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO l1_memory_event_archive (id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES ('m-1','conv:1','s',?,1,'user_conversation','user','m','{}','observed','L1','test',?,?)`, archiveTestThreadID("owner-memory-1"), now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO l1_news_item_archive (id, staging_id, category, source_id, source_url, published_at, fetched_at, raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json, created_at, updated_at) VALUES ('n-1','st-1','ai','src','https://example.invalid',NULL,?,'news','hash','summary','[]','','{}',?,?)`, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO l1_knowledge_item_archive (id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json, created_at, updated_at) VALUES ('k-1','st-2','ai','title','src','https://example.invalid','knowledge','hash','summary','[]','','{}',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO l1_staging_item_archive (id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at, raw_text, raw_hash, summary_draft, keywords_json, license_note, validation_status, meta_json, created_at, updated_at) VALUES ('s-1','search_result','conv:1','e-1','src','https://example.invalid',?,NULL,'staging','hash','summary','[]','','validated','{}',?,?)`, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

type archiveCounts struct {
	Threads, Memory, News, Knowledge, Staging int
}

func archiveSourceCounts(t *testing.T, db *sql.DB) archiveCounts {
	t.Helper()
	var counts archiveCounts
	queries := []struct {
		query string
		dest  *int
	}{{`SELECT count(*) FROM session_thread`, &counts.Threads}, {`SELECT count(*) FROM l1_memory_event_archive`, &counts.Memory}, {`SELECT count(*) FROM l1_news_item_archive`, &counts.News}, {`SELECT count(*) FROM l1_knowledge_item_archive`, &counts.Knowledge}, {`SELECT count(*) FROM l1_staging_item_archive`, &counts.Staging}}
	for _, item := range queries {
		if err := db.QueryRow(item.query).Scan(item.dest); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}
