package dcimigration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEventStoreReadersAcceptLegacyAndCurrentOwnerSchemas(t *testing.T) {
	for _, schema := range []string{"legacy", "current owner"} {
		t.Run(schema, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "event-store.sqlite")
			if schema == "legacy" {
				writeEventStoreTestDB(t, path, nil)
			} else {
				writeCurrentEventStoreTestDB(t, path)
			}

			ids, counts, hashes, err := loadEventStore(context.Background(), path)
			if err != nil {
				t.Fatalf("loadEventStore() error = %v", err)
			}
			if len(ids) != 0 || counts.EventStore != 0 || hashes.Schema == "" || hashes.NonDCI == "" {
				t.Fatalf("loadEventStore() = ids=%d counts=%#v hashes=%#v", len(ids), counts, hashes)
			}
			rows, err := readBuildEventStorePath(context.Background(), path, true)
			if err != nil {
				t.Fatalf("readBuildEventStorePath() error = %v", err)
			}
			if rows.envelopeCount != 0 || rows.dependencyCount != 0 {
				t.Fatalf("build reader counts = %#v, want empty Event Store", rows)
			}
		})
	}
}

func TestDryRunAcceptsCurrentOwnerEventStoreSchemaWithoutMutatingSource(t *testing.T) {
	snapshot := makeTestSnapshot(t, "current-owner-event-store")
	path := filepath.Join(snapshot, "source-event-store")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeCurrentEventStoreTestDB(t, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
	if err != nil || manifest.Status != StatusReady {
		t.Fatalf("DryRun() = %#v, err=%v; want ready", manifest, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("DryRun() mutated current owner Event Store source")
	}
}

func TestEventStoreReadersRejectUnknownPartialAndMalformedOwnerSchemas(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{
			name: "unknown table",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `CREATE TABLE unexpected_event_store_table (value TEXT)`)
			},
		},
		{
			name: "partial live table set",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `DROP TABLE live_work_outbox`)
			},
		},
		{
			name: "malformed live table",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `ALTER TABLE live_operation_receipt ADD COLUMN unexpected TEXT`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "event-store.sqlite")
			writeCurrentEventStoreTestDB(t, path)
			db := openTestDB(t, path)
			test.mutate(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			if _, _, _, err := loadEventStore(context.Background(), path); errorCode(err, "") != "unknown_schema" {
				t.Fatalf("loadEventStore() error = %v, want unknown_schema", err)
			}
			if _, err := readBuildEventStorePath(context.Background(), path, true); errorCode(err, "") != "unknown_schema" {
				t.Fatalf("readBuildEventStorePath() error = %v, want unknown_schema", err)
			}
		})
	}
}

func TestDryRunRejectsUnknownPartialAndMalformedOwnerSchemas(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{
			name: "unknown table",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `CREATE TABLE unexpected_event_store_table (value TEXT)`)
			},
		},
		{
			name: "partial live table set",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `DROP TABLE live_work_outbox`)
			},
		},
		{
			name: "malformed live table",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `ALTER TABLE live_operation_receipt ADD COLUMN unexpected TEXT`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := makeTestSnapshot(t, "reject-current-owner-"+test.name)
			path := filepath.Join(snapshot, "source-event-store")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			writeCurrentEventStoreTestDB(t, path)
			db := openTestDB(t, path)
			test.mutate(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
			if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "unknown_schema" {
				t.Fatalf("DryRun() = %#v, err=%v; want unknown_schema", manifest, err)
			}
		})
	}
}

func TestCreateBuiltEventStorePreservesCurrentLiveRowsReceiptsAndOutbox(t *testing.T) {
	fixture := newBuildEventStoreFixtureWithSourceMutation(t, func(t *testing.T, path string) {
		populateCurrentLiveJournalTestData(t, path)
	})
	sourceBefore, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := liveJournalTestRowCounts(t, fixture.source)
	if wantCounts["live_operation_receipt"] == 0 || wantCounts["live_work_outbox"] == 0 {
		t.Fatalf("fixture did not populate receipts and outbox: %#v", wantCounts)
	}

	evidence, err := createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan)
	if err != nil {
		t.Fatalf("createBuiltEventStore() error = %v", err)
	}
	if evidence.SourceNonDCISHA256 == "" || evidence.OutputNonDCISHA256 != evidence.SourceNonDCISHA256 {
		t.Fatalf("non-DCI logical hash changed across build: %#v", evidence)
	}
	if got := liveJournalTestRowCounts(t, fixture.target); !equalIntMaps(got, wantCounts) {
		t.Fatalf("live journal row counts after build = %#v, want %#v", got, wantCounts)
	}
	sourceAfter, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != string(sourceBefore) {
		t.Fatal("Event Store source bytes changed during live-state build")
	}
}

func TestCreateBuiltEventStoreAcceptsLegacyEventStoreSchema(t *testing.T) {
	fixture := newLegacyBuildEventStoreFixture(t)
	sourceBefore, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan)
	if err != nil {
		t.Fatalf("createBuiltEventStore(legacy): %v", err)
	}
	if evidence.SourceEnvelopeCount != 2 || evidence.OutputEnvelopeCount != evidence.SourceEnvelopeCount+evidence.PlannedEnvelopeCount || evidence.OutputSchemaSHA256 == evidence.SourceSchemaSHA256 {
		t.Fatalf("legacy build evidence = %#v", evidence)
	}
	output, err := readBuildEventStorePath(context.Background(), fixture.target, false)
	if err != nil {
		t.Fatalf("read legacy build output: %v", err)
	}
	if err := verifyBuildEventStoreOutput(fixture.sourceRows, output, fixture.planned); err != nil {
		t.Fatalf("verify legacy build output: %v", err)
	}
	sourceAfter, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != string(sourceBefore) {
		t.Fatal("legacy Event Store source bytes changed during build")
	}
}

func TestCreateBuiltEventStoreRejectsUnknownPartialAndMalformedOwnerSchemas(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{
			name: "unknown table",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `CREATE TABLE unexpected_event_store_table (value TEXT)`)
			},
		},
		{
			name: "partial live table set",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `DROP TABLE live_work_outbox`)
			},
		},
		{
			name: "malformed live table",
			mutate: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `ALTER TABLE live_operation_receipt ADD COLUMN unexpected TEXT`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBuildEventStoreFixture(t)
			db := openTestDB(t, fixture.source)
			test.mutate(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			sourceBefore, err := os.ReadFile(fixture.source)
			if err != nil {
				t.Fatal(err)
			}

			_, err = createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan)
			if errorCode(err, "") != "unknown_schema" {
				t.Fatalf("createBuiltEventStore() error = %v, want unknown_schema", err)
			}
			if _, err := os.Lstat(fixture.target); !os.IsNotExist(err) {
				t.Fatalf("build target exists after schema refusal: %v", err)
			}
			sourceAfter, err := os.ReadFile(fixture.source)
			if err != nil {
				t.Fatal(err)
			}
			if string(sourceAfter) != string(sourceBefore) {
				t.Fatal("Event Store source changed during rejected build")
			}
		})
	}
}

func writeCurrentEventStoreTestDB(t *testing.T, path string) {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("eventstore.NewSQLiteStore(%q): %v", path, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current owner Event Store: %v", err)
	}
}

func populateCurrentLiveJournalTestData(t *testing.T, path string) {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("eventstore.NewSQLiteStore(%q): %v", path, err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close populated Event Store: %v", err)
		}
	}()

	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	input := modulecore.NewRootEventEnvelope("orchestrator", "conversation.user_message", time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC), nil)
	input.SessionID = sessionID
	input.TurnID = turnID
	input.MessageID = modulecore.NewMessageID()
	input.Payload = map[string]any{"from": "user", "source_actor_id": "viewer-user", "to": "mio", "content": "hello", "text": "hello"}
	accepted, err := store.AcceptTurn(ctx, eventstore.AcceptInput{
		OperationID: "accept-live-preservation", OwnerID: "owner-live-preservation", RuntimeVersion: "v2",
		ClientSubmissionID: "submission-live-preservation", ActorID: "viewer-user", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: input,
		InputMessage: eventstore.MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "viewer-user", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn() error = %v", err)
	}

	reply := modulecore.NewEventEnvelope(input.TraceID, input.EventID, nil, "orchestrator", "conversation.message.completed", input.OccurredAt.Add(time.Second), nil)
	reply.SessionID = sessionID
	reply.TurnID = turnID
	reply.MessageID = modulecore.NewMessageID()
	reply.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "hello back", "text": "hello back"}
	if _, err := store.CommitTurnBundle(ctx, eventstore.CommitInput{
		OperationID: "commit-live-preservation", SessionID: sessionID, TurnID: turnID, GenerationEpoch: accepted.GenerationEpoch,
		ResultState: "completed", Events: []modulecore.EventEnvelope{reply},
		MessageIndexes: []eventstore.MessageIndex{{
			MessageID: reply.MessageID, EventID: reply.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "mio", ToKind: "user", ToActor: "owner-live-preservation", Kind: "assistant_message", Visibility: "public",
		}},
		Outbox: []eventstore.WorkItem{{WorkID: "work-live-preservation", DedupeKey: "work-live-preservation", Kind: "conversation_projection", SourceEventID: reply.EventID, SessionID: sessionID, Priority: 10}},
	}); err != nil {
		t.Fatalf("CommitTurnBundle() error = %v", err)
	}
}

func liveJournalTestRowCounts(t *testing.T, path string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts := map[string]int{}
	for _, table := range eventstore.LiveJournalTableNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&count); err != nil {
			t.Fatalf("count live table %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func equalIntMaps(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
