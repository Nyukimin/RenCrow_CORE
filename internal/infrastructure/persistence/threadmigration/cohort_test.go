package threadmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMaterializeSQLiteTopicCohortMergesBeforeWritingAndBindsReceipts(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	l1Destination, archiveDestination := newCohortDestinations(t, fixture, "success")
	topic := `{"session_id":"idle-topic-session","summary":"one"}` + "\n"

	result, err := MaterializeSQLiteTopicCohort(context.Background(), SQLiteTopicCohortInput{
		L1Source: fixture.l1, ArchiveSource: fixture.archive,
		L1Destination: l1Destination, ArchiveDestination: archiveDestination,
		TopicSource: strings.NewReader(topic),
	})
	if err != nil {
		t.Fatalf("MaterializeSQLiteTopicCohort() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() error = %v", err)
	}
	if result.Receipt.Status != SQLiteTopicCohortStatus || result.Receipt.TopicSourceCount != 1 || result.Receipt.TopicOutputCount != 1 || result.Receipt.TopicQuarantineCount != 0 {
		t.Fatalf("unexpected cohort receipt = %+v", result.Receipt)
	}
	if result.Receipt.SQLitePlanSHA256 == result.Receipt.MergedMappingSHA256 {
		t.Fatal("merged mapping hash unexpectedly equals SQLite-only plan hash")
	}
	if result.L1Receipt.MappingSHA256 != result.Plan.MappingSHA256 || result.ArchiveReceipt.MappingSHA256 != result.Plan.MappingSHA256 {
		t.Fatal("SQLite materialization receipts are not bound to merged plan")
	}
	if !strings.Contains(string(result.Topic.OutputJSONL), `"thread_id":"thr_`) || !strings.Contains(string(result.Topic.OutputJSONL), `"thread_kind":"idlechat"`) {
		t.Fatalf("topic output is not canonical: %s", result.Topic.OutputJSONL)
	}
	if result.L1Receipt.IdentityAudit.LegacyNumericRows != 0 || result.ArchiveReceipt.IdentityAudit.LegacyNumericRows != 0 {
		t.Fatalf("legacy identity remained: l1=%+v archive=%+v", result.L1Receipt.IdentityAudit, result.ArchiveReceipt.IdentityAudit)
	}
}

func TestMaterializeSQLiteTopicCohortRejectsContradictoryPlanBeforeMutation(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	l1Destination, archiveDestination := newCohortDestinations(t, fixture, "collision")
	l1Before := snapshotSQLiteTables(t, l1Destination, canonicalL1MaterializationTables)
	archiveBefore := snapshotSQLiteTables(t, archiveDestination, canonicalArchiveMaterializationTables)
	canonicalSession, err := canonicalGenericSessionID(fixture.genericID)
	if err != nil {
		t.Fatal(err)
	}
	var topic strings.Builder
	for index := 0; index < 7; index++ {
		fmt.Fprintf(&topic, `{"session_id":%q,"summary":%q}`+"\n", canonicalSession, fmt.Sprintf("topic-%d", index+1))
	}

	_, err = MaterializeSQLiteTopicCohort(context.Background(), SQLiteTopicCohortInput{
		L1Source: fixture.l1, ArchiveSource: fixture.archive,
		L1Destination: l1Destination, ArchiveDestination: archiveDestination,
		TopicSource: strings.NewReader(topic.String()),
	})
	var typed *SQLiteTopicCohortError
	if !errors.As(err, &typed) || typed.Code != "plan_merge_failed" || typed.Phase != "plan_merge" || typed.L1Committed || typed.ArchiveCommitted {
		t.Fatalf("error = %#v, want pre-write plan_merge failure", err)
	}
	if after := snapshotSQLiteTables(t, l1Destination, canonicalL1MaterializationTables); !reflect.DeepEqual(after, l1Before) {
		t.Fatalf("L1 destination mutated before merge rejection: before=%v after=%v", l1Before, after)
	}
	if after := snapshotSQLiteTables(t, archiveDestination, canonicalArchiveMaterializationTables); !reflect.DeepEqual(after, archiveBefore) {
		t.Fatalf("archive destination mutated before merge rejection: before=%v after=%v", archiveBefore, after)
	}
}

func TestMaterializeSQLiteTopicCohortReportsDisposablePartialCommit(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	l1Destination, archiveDestination := newCohortDestinations(t, fixture, "partial")
	if _, err := archiveDestination.Exec(`DROP TABLE conversation_thread_summary_receipt`); err != nil {
		t.Fatal(err)
	}

	_, err := MaterializeSQLiteTopicCohort(context.Background(), SQLiteTopicCohortInput{
		L1Source: fixture.l1, ArchiveSource: fixture.archive,
		L1Destination: l1Destination, ArchiveDestination: archiveDestination,
		TopicSource: strings.NewReader(`{"session_id":"idle-partial","summary":"one"}` + "\n"),
	})
	var typed *SQLiteTopicCohortError
	if !errors.As(err, &typed) || typed.Code != "archive_materialization_failed" || typed.Phase != "archive_materialization" || !typed.L1Committed || typed.ArchiveCommitted {
		t.Fatalf("error = %#v, want archive failure after L1 commit", err)
	}
	var threadIDType string
	if err := l1Destination.QueryRow(`SELECT typeof(thread_id) FROM l1_memory_event WHERE id = 'event-generic'`).Scan(&threadIDType); err != nil {
		t.Fatal(err)
	}
	if threadIDType != "text" {
		t.Fatalf("L1 destination thread identity type = %q, want committed canonical text", threadIDType)
	}
}

func TestMaterializeSQLiteTopicCohortMergesAdditionalPlansBeforeWriting(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	l1Destination, archiveDestination := newCohortDestinations(t, fixture, "additional")
	additional, err := BuildPlan([]LegacyThreadFact{{
		Surface: "redis_thread", RecordKey: "thread:44", SessionID: "redis-only", LegacyThreadID: 44,
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := MaterializeSQLiteTopicCohort(context.Background(), SQLiteTopicCohortInput{
		L1Source: fixture.l1, ArchiveSource: fixture.archive,
		L1Destination: l1Destination, ArchiveDestination: archiveDestination,
		TopicSource: strings.NewReader(""), AdditionalPlans: []Plan{additional},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Receipt.AdditionalPlanSHA256) != 1 || result.Receipt.AdditionalPlanSHA256[0] != additional.MappingSHA256 {
		t.Fatalf("additional plan hashes = %#v", result.Receipt.AdditionalPlanSHA256)
	}
	canonicalSessionID, err := canonicalGenericSessionID("redis-only")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Plan.LookupGeneric(canonicalSessionID, 44); !ok {
		t.Fatal("additional plan mapping is absent from materialization plan")
	}
	if result.L1Receipt.MappingSHA256 != result.Plan.MappingSHA256 || result.ArchiveReceipt.MappingSHA256 != result.Plan.MappingSHA256 {
		t.Fatal("materialization receipts are not bound to the global plan")
	}
}

func newCohortDestinations(t *testing.T, fixture sqliteInventoryFixture, suffix string) (*sql.DB, *sql.DB) {
	t.Helper()
	base := "file:threadmigration_cohort_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "_" + suffix
	l1 := openInventoryTestDB(t, base+"_l1?mode=memory&cache=shared")
	archive := openInventoryTestDB(t, base+"_archive?mode=memory&cache=shared")
	createLegacyL1Schema(t, l1)
	createLegacyArchiveSchema(t, archive)
	cloneLegacyL1Rows(t, fixture.l1, l1)
	cloneLegacyArchiveRows(t, fixture.archive, archive)
	return l1, archive
}
