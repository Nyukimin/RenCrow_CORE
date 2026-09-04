package archivesqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/parquet-go/parquet-go"
)

func testThreadSummaryReceipt() *domconv.ThreadSummaryReceipt {
	return &domconv.ThreadSummaryReceipt{
		SchemaVersion:   domconv.ThreadSummaryReceiptSchemaVersion,
		GenerationMode:  domconv.ThreadSummaryGenerationLLM,
		Provider:        "mock",
		EvidenceSHA256:  strings.Repeat("a", 64),
		SourceTurnCount: 2,
		Roles:           []string{"user", "mio"},
		CreatedAt:       time.Date(2026, 8, 16, 18, 0, 0, 0, time.UTC),
	}
}

func receiptForRoles(roles []string) *domconv.ThreadSummaryReceipt {
	receipt := testThreadSummaryReceipt()
	receipt.Roles = append([]string(nil), roles...)
	if receipt.SourceTurnCount < 1 {
		receipt.SourceTurnCount = 1
	}
	return receipt
}

func TestArchiveSQLiteStoreThreadSummaryReceiptRoundTripAndLegacyRead(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()

	receipt := testThreadSummaryReceipt()
	if err := store.SaveThreadSummaryWithReceipt(ctx, &domconv.ThreadSummary{
		ThreadID:   archiveTestThreadID("501"),
		ThreadSeq:  1,
		ThreadKind: domconv.ThreadKindUserConversation,
		SessionID:  archiveTestSessionID("receipt-session"),
		StartTime:  time.Date(2026, 8, 16, 17, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 8, 16, 17, 1, 0, 0, time.UTC),
		Domain:     "memory",
		Summary:    "receipt summary",
		Keywords:   []string{"one", "two", "three"},
		Roles:      []string{"user", "mio"},
	}, receipt); err != nil {
		t.Fatalf("SaveThreadSummaryWithReceipt failed: %v", err)
	}
	_, err = store.db.ExecContext(ctx, `
INSERT INTO session_thread (thread_id, thread_seq, thread_kind, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, archiveTestThreadID("502"), 2, domconv.ThreadKindUserConversation, archiveTestSessionID("receipt-session"), time.Date(2026, 8, 16, 16, 0, 0, 0, time.UTC), time.Date(2026, 8, 16, 16, 1, 0, 0, time.UTC), "memory", "legacy summary", `["old"]`, `[]`, false)
	if err != nil {
		t.Fatalf("insert legacy summary: %v", err)
	}

	got, err := store.GetSessionHistory(ctx, archiveTestSessionID("receipt-session"), 10)
	if err != nil {
		t.Fatalf("GetSessionHistory failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two summaries, got %d", len(got))
	}
	var current, legacy *domconv.ThreadSummary
	for _, summary := range got {
		switch summary.ThreadID {
		case archiveTestThreadID("501"):
			current = summary
		case archiveTestThreadID("502"):
			legacy = summary
		}
	}
	if current == nil || current.Receipt == nil || current.Receipt.GenerationMode != domconv.ThreadSummaryGenerationLLM {
		t.Fatalf("receipt did not round-trip: %#v", current)
	}
	if current.Receipt.Provider != "mock" || current.Receipt.Roles[1] != "mio" || current.Receipt.SourceTurnCount != 2 {
		t.Fatalf("receipt fields did not round-trip: %#v", current.Receipt)
	}
	if legacy == nil || legacy.Receipt == nil || legacy.Receipt.GenerationMode != domconv.ThreadSummaryGenerationLegacyUnverified {
		t.Fatalf("legacy row was not marked unverified: %#v", legacy)
	}
}

func TestArchiveSQLiteStoreRejectsReceiptlessNewWrite(t *testing.T) {
	store, err := NewArchiveSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()
	if err := store.SaveThreadSummary(context.Background(), &domconv.ThreadSummary{
		ThreadID: archiveTestThreadID("504"), ThreadSeq: 1, ThreadKind: domconv.ThreadKindUserConversation, Summary: "must have receipt", Keywords: []string{"one", "two", "three"},
	}); err == nil {
		t.Fatal("receiptless new write was accepted")
	}
}

func TestArchiveSQLiteStoreThreadSummaryReceiptIsImmutable(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()
	initial := &domconv.ThreadSummary{
		ThreadID:   archiveTestThreadID("505"),
		ThreadSeq:  1,
		ThreadKind: domconv.ThreadKindUserConversation,
		SessionID:  archiveTestSessionID("immutable-session"),
		StartTime:  time.Date(2026, 8, 16, 18, 1, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 8, 16, 18, 2, 0, 0, time.UTC),
		Domain:     "memory",
		Summary:    "first summary",
		Keywords:   []string{"one", "two", "three"},
		Roles:      []string{"user", "mio"},
	}
	receipt := testThreadSummaryReceipt()
	if err := store.SaveThreadSummaryWithReceipt(ctx, initial, receipt); err != nil {
		t.Fatalf("initial write failed: %v", err)
	}
	if err := store.SaveThreadSummaryWithReceipt(ctx, initial, receipt); err != nil {
		t.Fatalf("identical replay must be idempotent: %v", err)
	}
	changed := *initial
	changed.Summary = "changed summary"
	if err := store.SaveThreadSummaryWithReceipt(ctx, &changed, receipt); err == nil {
		t.Fatal("changed summary replay was accepted")
	}
	changedReceipt := *receipt
	changedReceipt.EvidenceSHA256 = strings.Repeat("b", 64)
	if err := store.SaveThreadSummaryWithReceipt(ctx, initial, &changedReceipt); err == nil {
		t.Fatal("changed receipt replay was accepted")
	}
	got, err := store.GetSessionHistory(ctx, archiveTestSessionID("immutable-session"), 10)
	if err != nil || len(got) != 1 || got[0].Summary != "first summary" || got[0].Receipt.EvidenceSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("immutable row was modified: got=%#v err=%v", got, err)
	}
}

func TestArchiveSQLiteStoreRequiresPositiveSourceCountAndExactRoles(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()
	zeroCount := testThreadSummaryReceipt()
	zeroCount.SourceTurnCount = 0
	if err := store.SaveThreadSummaryWithReceipt(ctx, &domconv.ThreadSummary{ThreadID: archiveTestThreadID("506"), ThreadSeq: 1, ThreadKind: domconv.ThreadKindUserConversation, SessionID: archiveTestSessionID("zero-count"), Summary: "summary", Keywords: []string{"one", "two", "three"}, Roles: []string{"user", "mio"}}, zeroCount); err == nil {
		t.Fatal("zero source turn count was accepted")
	}
	mismatch := testThreadSummaryReceipt()
	if err := store.SaveThreadSummaryWithReceipt(ctx, &domconv.ThreadSummary{ThreadID: archiveTestThreadID("507"), ThreadSeq: 1, ThreadKind: domconv.ThreadKindUserConversation, SessionID: archiveTestSessionID("mismatch"), Summary: "summary", Keywords: []string{"one", "two", "three"}, Roles: []string{"user"}}, mismatch); err == nil {
		t.Fatal("mismatched roles were accepted")
	}
}

func TestArchiveSQLiteStoreThreadSummaryReceiptRollsBackSummaryOnReceiptFailure(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()
	_, err = store.db.ExecContext(ctx, `
CREATE TRIGGER reject_thread_summary_receipt
BEFORE INSERT ON conversation_thread_summary_receipt
BEGIN
	SELECT RAISE(ABORT, 'test receipt rejection');
END;
`)
	if err != nil {
		t.Fatalf("create receipt trigger: %v", err)
	}

	if err := store.SaveThreadSummaryWithReceipt(ctx, &domconv.ThreadSummary{
		ThreadID:   archiveTestThreadID("503"),
		ThreadSeq:  1,
		ThreadKind: domconv.ThreadKindUserConversation,
		SessionID:  archiveTestSessionID("rollback-session"),
		StartTime:  time.Now().UTC(),
		Summary:    "rollback summary",
		Keywords:   []string{"one", "two", "three"},
		Roles:      []string{"user", "mio"},
	}, testThreadSummaryReceipt()); err == nil {
		t.Fatal("expected receipt write failure")
	}
	var summaryCount, receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM session_thread WHERE thread_id = ?`, archiveTestThreadID("503")).Scan(&summaryCount); err != nil {
		t.Fatalf("count summary rows: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_thread_summary_receipt WHERE thread_id = ?`, archiveTestThreadID("503")).Scan(&receiptCount); err != nil {
		t.Fatalf("count receipt rows: %v", err)
	}
	if summaryCount != 0 || receiptCount != 0 {
		t.Fatalf("transaction was not atomic: summary=%d receipt=%d", summaryCount, receiptCount)
	}
}

func TestArchiveSQLiteStoreParquetIncludesThreadSummaryReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()
	receipt := receiptForRoles([]string{"user", "mio"})
	if err := store.SaveThreadSummaryWithReceipt(ctx, &domconv.ThreadSummary{
		ThreadID:   archiveTestThreadID("508"),
		ThreadSeq:  1,
		ThreadKind: domconv.ThreadKindUserConversation,
		SessionID:  archiveTestSessionID("parquet-receipt"),
		StartTime:  time.Date(2026, 8, 16, 19, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 8, 16, 19, 1, 0, 0, time.UTC),
		Summary:    "parquet summary",
		Keywords:   []string{"one", "two", "three"},
		Roles:      []string{"user", "mio"},
	}, receipt); err != nil {
		t.Fatalf("SaveThreadSummaryWithReceipt failed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO session_thread (thread_id, thread_seq, thread_kind, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, archiveTestThreadID("509"), 2, domconv.ThreadKindUserConversation, archiveTestSessionID("parquet-legacy"), time.Date(2026, 8, 16, 18, 0, 0, 0, time.UTC), time.Date(2026, 8, 16, 18, 1, 0, 0, time.UTC), "memory", "legacy parquet summary", `[]`, `[]`, false); err != nil {
		t.Fatalf("insert legacy parquet summary: %v", err)
	}
	path := filepath.Join(t.TempDir(), "thread_summaries.parquet")
	if err := store.ExportThreadSummariesParquet(ctx, path); err != nil {
		t.Fatalf("ExportThreadSummariesParquet failed: %v", err)
	}
	rows, err := parquet.ReadFile[threadSummaryParquetRow](path)
	if err != nil {
		t.Fatalf("read parquet: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two parquet rows, got %d", len(rows))
	}
	var current, legacy *threadSummaryParquetRow
	for i := range rows {
		switch rows[i].ThreadID {
		case string(archiveTestThreadID("508")):
			current = &rows[i]
		case string(archiveTestThreadID("509")):
			legacy = &rows[i]
		}
	}
	if current == nil || current.SchemaVersion == nil || *current.SchemaVersion != receipt.SchemaVersion || current.GenerationMode == nil || *current.GenerationMode != receipt.GenerationMode || current.Provider == nil || *current.Provider != receipt.Provider || current.FailureCode == nil || *current.FailureCode != receipt.FailureCode || current.EvidenceSHA256 == nil || *current.EvidenceSHA256 != receipt.EvidenceSHA256 || current.SourceTurnCount == nil || *current.SourceTurnCount != int64(receipt.SourceTurnCount) || current.RolesJSON == nil || *current.RolesJSON != `["user","mio"]` || current.ReceiptCreatedAt == nil {
		t.Fatalf("receipt fields did not round-trip through parquet: %#v", current)
	}
	if current.ThreadID != string(archiveTestThreadID("508")) || current.ThreadSeq != 1 || current.ThreadKind != string(domconv.ThreadKindUserConversation) {
		t.Fatalf("current thread tuple did not round-trip through parquet: %#v", current)
	}
	if !current.ReceiptCreatedAt.Equal(receipt.CreatedAt) {
		t.Fatalf("receipt created_at did not round-trip: got=%v want=%v", current.ReceiptCreatedAt, receipt.CreatedAt)
	}
	if legacy == nil || legacy.SchemaVersion != nil || legacy.GenerationMode != nil || legacy.Provider != nil || legacy.FailureCode != nil || legacy.EvidenceSHA256 != nil || legacy.SourceTurnCount != nil || legacy.RolesJSON != nil || legacy.ReceiptCreatedAt != nil {
		t.Fatalf("legacy parquet row unexpectedly claims receipt: %#v", legacy)
	}
	if legacy.ThreadID != string(archiveTestThreadID("509")) || legacy.ThreadSeq != 2 || legacy.ThreadKind != string(domconv.ThreadKindUserConversation) {
		t.Fatalf("legacy thread tuple did not round-trip through parquet: %#v", legacy)
	}
	snapshot, err := store.readArchiveParquetSnapshot(ctx)
	var snapshotCurrent *threadSummaryParquetRow
	for i := range snapshot.Threads {
		if snapshot.Threads[i].ThreadID == string(archiveTestThreadID("508")) {
			snapshotCurrent = &snapshot.Threads[i]
		}
	}
	if err != nil || len(snapshot.Threads) != 2 || snapshotCurrent == nil || snapshotCurrent.SchemaVersion == nil || snapshotCurrent.RolesJSON == nil {
		t.Fatalf("owner parquet snapshot lost receipt fields: rows=%#v err=%v", snapshot.Threads, err)
	}
}
