package threadmigration

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareFullCohortMergesEverySurfaceBeforeMaterialization(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	l1Destination, archiveDestination := newCohortDestinations(t, fixture, "full_success")
	redisEntries := []RedisEntry{{
		Key: "sess:redis-full", Value: redisTestSessionJSON(t, "redis-full", 12), ExpireAtUnixMilli: 1800000001200,
	}}
	qdrantPoints := []QdrantPointSnapshot{inventoryQdrantPoint("full-qdrant", "qdrant-full", 17, []float32{1, 2})}
	redisBefore := cloneRedisEntries(redisEntries)
	qdrantBefore := cloneQdrantInventoryPoints(qdrantPoints)

	result, err := PrepareFullCohort(context.Background(), FullCohortInput{
		SQLiteTopic: SQLiteTopicCohortInput{
			L1Source: fixture.l1, ArchiveSource: fixture.archive,
			L1Destination: l1Destination, ArchiveDestination: archiveDestination,
			TopicSource: strings.NewReader(""),
		},
		Redis: redisEntries, Qdrant: qdrantPoints,
	})
	if err != nil {
		t.Fatalf("PrepareFullCohort() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() error = %v", err)
	}
	if result.Receipt.Status != FullCohortStatus || result.SQLiteTopic.Receipt.Status != SQLiteTopicCohortStatus {
		t.Fatalf("unexpected statuses: %+v / %+v", result.Receipt, result.SQLiteTopic.Receipt)
	}
	if result.Plan.MappingSHA256 != result.SQLiteTopic.Plan.MappingSHA256 || result.Plan.MappingSHA256 != result.RedisPreparation.Plan.MappingSHA256 || result.Plan.MappingSHA256 != result.QdrantPreparation.Plan.MappingSHA256 {
		t.Fatal("child operations do not share the global mapping plan")
	}
	redisSession, err := canonicalGenericSessionID("redis-full")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Plan.LookupGeneric(redisSession, 12); !ok {
		t.Fatal("Redis-only mapping is absent from global plan")
	}
	qdrantSession, err := canonicalGenericSessionID("qdrant-full")
	if err != nil {
		t.Fatal(err)
	}
	qdrantMapping, ok := result.Plan.LookupGeneric(qdrantSession, 17)
	if !ok {
		t.Fatal("Qdrant-only mapping is absent from global plan")
	}
	if len(result.QdrantPreparation.Points) != 1 || result.QdrantPreparation.Points[0].PointID != strings.TrimPrefix(string(qdrantMapping.ThreadID), "thr_") {
		t.Fatalf("Qdrant output = %+v", result.QdrantPreparation.Points)
	}
	if !reflect.DeepEqual(redisEntries, redisBefore) || !reflect.DeepEqual(qdrantPoints, qdrantBefore) {
		t.Fatal("full cohort mutated caller-owned snapshots")
	}
}

func TestPrepareFullCohortRejectsQdrantConflictBeforeSQLiteMutation(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	l1Destination, archiveDestination := newCohortDestinations(t, fixture, "full_prewrite_failure")
	l1Before := snapshotSQLiteTables(t, l1Destination, canonicalL1MaterializationTables)
	archiveBefore := snapshotSQLiteTables(t, archiveDestination, canonicalArchiveMaterializationTables)
	payload := func(body string) map[string]json.RawMessage {
		return map[string]json.RawMessage{
			"session_id": json.RawMessage(`"qdrant-conflict"`),
			"thread_id":  json.RawMessage(`23`),
			"body":       json.RawMessage(body),
		}
	}
	_, err := PrepareFullCohort(context.Background(), FullCohortInput{
		SQLiteTopic: SQLiteTopicCohortInput{
			L1Source: fixture.l1, ArchiveSource: fixture.archive,
			L1Destination: l1Destination, ArchiveDestination: archiveDestination,
			TopicSource: strings.NewReader(""),
		},
		Qdrant: []QdrantPointSnapshot{
			{PointID: qdrantTestPointID("full-conflict-a"), Vector: []float32{1}, Payload: payload(`{"value":1}`)},
			{PointID: qdrantTestPointID("full-conflict-b"), Vector: []float32{1}, Payload: payload(`{"value":2}`)},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "different vector or nonidentity payload") {
		t.Fatalf("error = %v, want Qdrant conflict", err)
	}
	if after := snapshotSQLiteTables(t, l1Destination, canonicalL1MaterializationTables); !reflect.DeepEqual(after, l1Before) {
		t.Fatal("L1 destination changed before Qdrant preparation failed")
	}
	if after := snapshotSQLiteTables(t, archiveDestination, canonicalArchiveMaterializationTables); !reflect.DeepEqual(after, archiveBefore) {
		t.Fatal("archive destination changed before Qdrant preparation failed")
	}
}

func TestPrepareFullCohortAcceptsEmptyRedisAndQdrantSnapshots(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	l1Destination, archiveDestination := newCohortDestinations(t, fixture, "full_empty_external")
	result, err := PrepareFullCohort(context.Background(), FullCohortInput{
		SQLiteTopic: SQLiteTopicCohortInput{
			L1Source: fixture.l1, ArchiveSource: fixture.archive,
			L1Destination: l1Destination, ArchiveDestination: archiveDestination,
			TopicSource: strings.NewReader(""),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RedisPreparation.Entries) != 0 || len(result.QdrantPreparation.Points) != 0 {
		t.Fatalf("unexpected external outputs: %+v / %+v", result.RedisPreparation, result.QdrantPreparation)
	}
}
