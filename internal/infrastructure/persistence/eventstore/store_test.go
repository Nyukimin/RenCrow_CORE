package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var _ modulecore.SequencedEventAppender = (*SQLiteStore)(nil)

func TestAppendAndReadRootChildParallelJoin(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	root := eventFixture(traceID, "task.created", "orchestrator")
	left := eventFixture(traceID, "attempt.started", "superagent")
	left.CausationEventID = root.EventID
	right := eventFixture(traceID, "attempt.started", "ai_workflow")
	right.CausationEventID = root.EventID
	join := eventFixture(traceID, "verification.completed", "orchestrator")
	join.CausationEventID = left.EventID
	join.DependencyEventIDs = []modulecore.EventID{right.EventID}

	events := []modulecore.EventEnvelope{root, left, right, join}
	for index, event := range events {
		stored, err := store.AppendSequenced(ctx, event)
		if err != nil {
			t.Fatalf("AppendSequenced(%q) error = %v", event.EventID, err)
		}
		events[index] = stored
	}

	gotJoin, found, err := store.GetByID(ctx, join.EventID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !found {
		t.Fatal("GetByID() found = false, want true")
	}
	if !reflect.DeepEqual(gotJoin, events[3]) {
		t.Fatalf("GetByID() = %#v, want %#v", gotJoin, events[3])
	}

	got, err := store.ListByComponent(ctx, "orchestrator", 10)
	if err != nil {
		t.Fatalf("ListByComponent() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByComponent() length = %d, want 2", len(got))
	}
	if got[0].EventID != join.EventID || got[1].EventID != root.EventID {
		t.Fatalf("ListByComponent() IDs = %q, %q, want %q, %q", got[0].EventID, got[1].EventID, join.EventID, root.EventID)
	}
}

func TestNewSQLiteStoreRejectsUnconfiguredPath(t *testing.T) {
	if _, err := NewSQLiteStore(""); err == nil {
		t.Fatal("NewSQLiteStore(\"\") error = nil")
	}
}

func TestFreshSchemaContainsEventSeqColumnAndIndex(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rows, err := store.db.QueryContext(ctx, `PRAGMA table_info(event_envelope)`)
	if err != nil {
		t.Fatalf("event_envelope table_info: %v", err)
	}
	defer rows.Close()
	var foundEventSeq bool
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("event_envelope table_info scan: %v", err)
		}
		if name == "event_seq" {
			foundEventSeq = true
			if columnType != "INTEGER" || notNull != 1 {
				t.Fatalf("event_seq schema = type %q notnull=%d, want INTEGER NOT NULL", columnType, notNull)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("event_envelope table_info rows: %v", err)
	}
	if !foundEventSeq {
		t.Fatal("event_envelope.event_seq column is missing")
	}

	var indexCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('event_envelope') WHERE name = 'event_envelope_seq_idx'`).Scan(&indexCount); err != nil {
		t.Fatalf("event sequence index lookup: %v", err)
	}
	if indexCount != 1 {
		t.Fatal("event_envelope_seq_idx is missing")
	}
}

func TestAppendSequencedAssignsMonotonicSequenceAndAppendDelegates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	first, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "sequence.first", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(first): %v", err)
	}
	if first.EventSeq != 1 {
		t.Fatalf("first EventSeq = %d, want 1", first.EventSeq)
	}
	second, err := store.AppendSequenced(ctx, eventFixture(first.TraceID, "sequence.second", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(second): %v", err)
	}
	if second.EventSeq != 2 {
		t.Fatalf("second EventSeq = %d, want 2", second.EventSeq)
	}

	third := eventFixture(first.TraceID, "sequence.third", "dci")
	if err := store.Append(ctx, third); err != nil {
		t.Fatalf("Append(third): %v", err)
	}
	got, found, err := store.GetByID(ctx, third.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID(third): found=%t err=%v", found, err)
	}
	if got.EventSeq != 3 {
		t.Fatalf("delegated Append EventSeq = %d, want 3", got.EventSeq)
	}
}

func TestAppendSequencedRejectsPreassignedSequence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	preassigned := eventFixture(modulecore.NewTraceID(), "sequence.preassigned", "dci")
	preassigned.EventSeq = 7
	_, err := store.AppendSequenced(ctx, preassigned)
	assertAppendErrorContains(t, err, "zero for live append")
	assertEventAbsent(t, store, preassigned.EventID)

	auto := eventFixture(preassigned.TraceID, "sequence.after-preassigned", "dci")
	stored, err := store.AppendSequenced(ctx, auto)
	if err != nil {
		t.Fatalf("AppendSequenced(auto): %v", err)
	}
	if stored.EventSeq != 1 {
		t.Fatalf("auto EventSeq = %d, want 1", stored.EventSeq)
	}

	negative := eventFixture(preassigned.TraceID, "sequence.negative", "dci")
	negative.EventSeq = -1
	_, err = store.AppendSequenced(ctx, negative)
	assertAppendErrorContains(t, err, "event_seq")
	assertEventAbsent(t, store, negative.EventID)
}

func TestAppendSequencedContinuesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	ctx := context.Background()
	firstStore, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(first): %v", err)
	}
	first, err := firstStore.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "sequence.before-restart", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(first): %v", err)
	}
	if first.EventSeq != 1 {
		t.Fatalf("first EventSeq = %d, want 1", first.EventSeq)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	secondStore, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(second): %v", err)
	}
	defer func() { _ = secondStore.Close() }()
	second, err := secondStore.AppendSequenced(ctx, eventFixture(first.TraceID, "sequence.after-restart", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(second): %v", err)
	}
	if second.EventSeq != 2 {
		t.Fatalf("second EventSeq = %d, want 2", second.EventSeq)
	}
}

func TestListByComponentOrdersByEventSeq(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := eventFixture(modulecore.NewTraceID(), "sequence.component.first", "dci")
	first.OccurredAt = first.OccurredAt.Add(10 * time.Minute)
	second := eventFixture(first.TraceID, "sequence.component.second", "dci")
	second.OccurredAt = second.OccurredAt.Add(-10 * time.Minute)
	if _, err := store.AppendSequenced(ctx, first); err != nil {
		t.Fatalf("AppendSequenced(first): %v", err)
	}
	if _, err := store.AppendSequenced(ctx, second); err != nil {
		t.Fatalf("AppendSequenced(second): %v", err)
	}
	got, err := store.ListByComponent(ctx, "dci", 10)
	if err != nil {
		t.Fatalf("ListByComponent(): %v", err)
	}
	if len(got) != 2 || got[0].EventID != second.EventID || got[1].EventID != first.EventID {
		t.Fatalf("ListByComponent() order = %#v, want newest event sequence first", got)
	}
}

func TestAppendBatchAssignsZeroSequencesPreservesPositiveAndRejectsDuplicates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	first := eventFixture(traceID, "batch.first", "dci")
	preserved := eventFixture(traceID, "batch.preserved", "dci")
	preserved.EventSeq = 5
	last := eventFixture(traceID, "batch.last", "dci")
	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{first, preserved, last}); err != nil {
		t.Fatalf("AppendBatch(): %v", err)
	}
	for eventID, want := range map[modulecore.EventID]modulecore.EventSeq{
		first.EventID:     1,
		preserved.EventID: 5,
		last.EventID:      2,
	} {
		got, found, err := store.GetByID(ctx, eventID)
		if err != nil || !found {
			t.Fatalf("GetByID(%q): found=%t err=%v", eventID, found, err)
		}
		if got.EventSeq != want {
			t.Fatalf("event %q EventSeq = %d, want %d", eventID, got.EventSeq, want)
		}
	}

	duplicateA := eventFixture(traceID, "batch.duplicate.a", "dci")
	duplicateA.EventSeq = 9
	duplicateB := eventFixture(traceID, "batch.duplicate.b", "dci")
	duplicateB.EventSeq = 9
	assertAppendErrorContains(t, store.AppendBatch(ctx, []modulecore.EventEnvelope{duplicateA, duplicateB}), "duplicate event_seq")
	assertEventAbsent(t, store, duplicateA.EventID)
	assertEventAbsent(t, store, duplicateB.EventID)
}

func TestAppendBatchSkipsExplicitSequenceImmediatelyAfterPersistedMaximum(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	for index := 0; index < 4; index++ {
		if _, err := store.AppendSequenced(ctx, eventFixture(traceID, fmt.Sprintf("seed.%d", index), "dci")); err != nil {
			t.Fatalf("seed append %d: %v", index, err)
		}
	}
	zero := eventFixture(traceID, "batch.zero", "dci")
	reserved := eventFixture(traceID, "batch.reserved", "dci")
	reserved.EventSeq = 5
	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{zero, reserved}); err != nil {
		t.Fatalf("AppendBatch(): %v", err)
	}
	storedZero, found, err := store.GetByID(ctx, zero.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID(zero): found=%t err=%v", found, err)
	}
	if storedZero.EventSeq != 6 {
		t.Fatalf("zero EventSeq = %d, want 6 after reserved 5", storedZero.EventSeq)
	}
}

func TestNewSQLiteStoreRejectsLegacySchemaWithoutEventSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-event-store.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(legacy): %v", err)
	}
	_, err = db.Exec(`CREATE TABLE event_envelope (
		event_id TEXT PRIMARY KEY NOT NULL,
		trace_id TEXT NOT NULL,
		schema_version TEXT NOT NULL,
		event_type TEXT NOT NULL,
		component_id TEXT NOT NULL,
		occurred_at TEXT NOT NULL,
		envelope_json TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		t.Fatalf("create legacy event_envelope: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close(legacy): %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("NewSQLiteStore(legacy) error = nil, want Step09 migration-required error")
	} else if !strings.Contains(strings.ToLower(err.Error()), "step09 migration required") {
		t.Fatalf("legacy schema error = %q, want explicit Step09 migration-required error", err)
	}
}

func TestNewSQLiteStoreRejectsIncompleteEventSeqSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incomplete-event-store.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(incomplete): %v", err)
	}
	_, err = db.Exec(`CREATE TABLE event_envelope (
		event_id TEXT PRIMARY KEY NOT NULL,
		event_seq INTEGER NOT NULL,
		trace_id TEXT NOT NULL,
		schema_version TEXT NOT NULL,
		event_type TEXT NOT NULL,
		component_id TEXT NOT NULL,
		occurred_at TEXT NOT NULL,
		envelope_json TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		t.Fatalf("create incomplete event_envelope: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close(incomplete): %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("NewSQLiteStore(incomplete) error = nil, want Step09 migration-required error")
	} else if !strings.Contains(strings.ToLower(err.Error()), "step09 migration required") {
		t.Fatalf("incomplete schema error = %q, want explicit Step09 migration-required error", err)
	}
}

func TestReadRejectsEventSeqColumnJSONMismatch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "sequence.drift", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(): %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		t.Fatalf("drop append-only trigger: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE event_envelope SET event_seq = event_seq + 1 WHERE event_id = ?`, event.EventID); err != nil {
		t.Fatalf("mutate event sequence column: %v", err)
	}
	if _, _, err := store.GetByID(ctx, event.EventID); err == nil {
		t.Fatal("GetByID() accepted event sequence column/JSON mismatch")
	}
}

func TestReadRejectsPersistedUnassignedEventSeq(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "sequence.unassigned", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(): %v", err)
	}
	var payload string
	if err := store.db.QueryRowContext(ctx, `SELECT envelope_json FROM event_envelope WHERE event_id = ?`, event.EventID).Scan(&payload); err != nil {
		t.Fatalf("read envelope JSON: %v", err)
	}
	var tampered modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(payload), &tampered); err != nil {
		t.Fatalf("decode envelope JSON: %v", err)
	}
	tampered.EventSeq = 0
	tamperedPayload, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("encode envelope JSON: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		t.Fatalf("drop append-only trigger: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE event_envelope SET envelope_json = ? WHERE event_id = ?`, string(tamperedPayload), event.EventID); err != nil {
		t.Fatalf("mutate envelope JSON: %v", err)
	}
	if _, _, err := store.GetByID(ctx, event.EventID); err == nil {
		t.Fatal("GetByID() accepted persisted unassigned event sequence")
	} else if !strings.Contains(strings.ToLower(err.Error()), "event sequence must be positive") {
		t.Fatalf("GetByID() error = %q, want positive event sequence failure", err)
	}
}

func TestAppendRejectsMissingCausationAndDependency(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()

	missingCausation := eventFixture(traceID, "run.started", "superagent")
	missingCausation.CausationEventID = modulecore.NewEventID()
	assertAppendErrorContains(t, store.Append(ctx, missingCausation), "causation")
	assertEventAbsent(t, store, missingCausation.EventID)

	missingDependency := eventFixture(traceID, "verification.started", "superagent")
	missingDependency.DependencyEventIDs = []modulecore.EventID{modulecore.NewEventID()}
	assertAppendErrorContains(t, store.Append(ctx, missingDependency), "dependency")
	assertEventAbsent(t, store, missingDependency.EventID)
}

func TestAppendRejectsCrossTraceReference(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	root := eventFixture(modulecore.NewTraceID(), "task.created", "orchestrator")
	if err := store.Append(ctx, root); err != nil {
		t.Fatalf("Append(root) error = %v", err)
	}
	child := eventFixture(modulecore.NewTraceID(), "run.started", "superagent")
	child.CausationEventID = root.EventID

	assertAppendErrorContains(t, store.Append(ctx, child), "trace")
	assertEventAbsent(t, store, child.EventID)
}

func TestAppendRejectsDuplicateEventID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event := eventFixture(modulecore.NewTraceID(), "task.created", "orchestrator")
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append(event) error = %v", err)
	}

	assertAppendErrorContains(t, store.Append(ctx, event), "duplicate")
	got, err := store.ListByComponent(ctx, event.ComponentID, 10)
	if err != nil {
		t.Fatalf("ListByComponent() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByComponent() length = %d, want 1", len(got))
	}
}

func TestAppendRollbackLeavesNoPartialEnvelopeOrDependencies(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	left := eventFixture(traceID, "branch.left", "orchestrator")
	right := eventFixture(traceID, "branch.right", "orchestrator")
	for _, event := range []modulecore.EventEnvelope{left, right} {
		if err := store.Append(ctx, event); err != nil {
			t.Fatalf("Append(%q) error = %v", event.EventID, err)
		}
	}

	_, err := store.db.Exec(`
		CREATE TRIGGER eventstore_test_abort_dependency
		BEFORE INSERT ON event_dependency
		WHEN NEW.relation_type = 'dependency'
		BEGIN
			SELECT RAISE(ABORT, 'forced dependency failure');
		END`)
	if err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}
	defer func() { _, _ = store.db.Exec(`DROP TRIGGER eventstore_test_abort_dependency`) }()

	join := eventFixture(traceID, "branch.join", "orchestrator")
	join.CausationEventID = left.EventID
	join.DependencyEventIDs = []modulecore.EventID{right.EventID}
	assertAppendErrorContains(t, store.Append(ctx, join), "forced dependency failure")
	assertEventAbsent(t, store, join.EventID)

	var envelopeCount, dependencyCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM event_envelope`).Scan(&envelopeCount); err != nil {
		t.Fatalf("count envelopes: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM event_dependency`).Scan(&dependencyCount); err != nil {
		t.Fatalf("count dependencies: %v", err)
	}
	if envelopeCount != 2 || dependencyCount != 0 {
		t.Fatalf("after rollback envelopes = %d, dependencies = %d, want 2, 0", envelopeCount, dependencyCount)
	}
}

func TestAppendJSONRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event := eventFixture(modulecore.NewTraceID(), "payload.recorded", "orchestrator")
	event.OccurredAt = time.Date(2026, 8, 29, 23, 45, 12, 123456789, time.FixedZone("JST", 9*60*60))
	event.Payload = map[string]any{
		"message": "round trip",
		"nested":  map[string]any{"ok": true, "score": 1.5},
		"items":   []any{"one", "two"},
	}
	stored, err := store.AppendSequenced(ctx, event)
	if err != nil {
		t.Fatalf("AppendSequenced(event) error = %v", err)
	}

	got, found, err := store.GetByID(ctx, stored.EventID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !found {
		t.Fatal("GetByID() found = false, want true")
	}
	wantJSON, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("json.Marshal(want): %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(got): %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON round trip = %s, want %s", gotJSON, wantJSON)
	}
}

func TestAppendCannotCreateCycleBecauseReferencesMustPreexist(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	first := eventFixture(traceID, "cycle.first", "orchestrator")
	second := eventFixture(traceID, "cycle.second", "orchestrator")
	first.CausationEventID = second.EventID
	assertAppendErrorContains(t, store.Append(ctx, first), "causation")

	second.CausationEventID = first.EventID
	assertAppendErrorContains(t, store.Append(ctx, second), "causation")
	assertEventAbsent(t, store, first.EventID)
	assertEventAbsent(t, store, second.EventID)
}

func TestAppendBatchIsOrderIndependentAndAtomic(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	root := eventFixture(traceID, "migration.root", "ai_workflow")
	child := eventFixture(traceID, "migration.child", "ai_workflow")
	child.CausationEventID = root.EventID

	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{child, root}); err != nil {
		t.Fatalf("AppendBatch() error = %v", err)
	}
	if _, found, err := store.GetByID(ctx, child.EventID); err != nil || !found {
		t.Fatalf("child found=%t err=%v", found, err)
	}

	valid := eventFixture(modulecore.NewTraceID(), "migration.valid", "superagent")
	invalid := eventFixture(modulecore.NewTraceID(), "migration.invalid", "superagent")
	invalid.CausationEventID = modulecore.NewEventID()
	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{valid, invalid}); err == nil {
		t.Fatal("AppendBatch() error = nil, want closed-graph rejection")
	}
	assertEventAbsent(t, store, valid.EventID)
	assertEventAbsent(t, store, invalid.EventID)
}

func TestAppendEnablesForeignKeysAndReadIsBounded(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	var foreignKeys int
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1", foreignKeys)
	}

	traceID := modulecore.NewTraceID()
	for i := 0; i < 3; i++ {
		event := eventFixture(traceID, "bounded.event", "orchestrator")
		event.OccurredAt = event.OccurredAt.Add(time.Duration(i) * time.Second)
		if err := store.Append(ctx, event); err != nil {
			t.Fatalf("Append(%q) error = %v", event.EventID, err)
		}
	}
	got, err := store.ListByComponent(ctx, "orchestrator", 2)
	if err != nil {
		t.Fatalf("ListByComponent() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByComponent() length = %d, want 2", len(got))
	}
}

func TestListByTraceIDIsExactBoundedAndIndexed(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	first := eventFixture(traceID, "trace.first", "dci")
	first.OccurredAt = first.OccurredAt.Add(2 * time.Second)
	second := eventFixture(traceID, "trace.second", "dci")
	second.OccurredAt = second.OccurredAt.Add(time.Second)
	third := eventFixture(traceID, "trace.third", "dci")
	for _, event := range []modulecore.EventEnvelope{first, second, third} {
		if err := store.Append(ctx, event); err != nil {
			t.Fatalf("Append(%q): %v", event.EventID, err)
		}
	}
	got, err := store.ListByTraceID(ctx, traceID, 3)
	if err != nil {
		t.Fatalf("ListByTraceID(): %v", err)
	}
	if len(got) != 3 || got[0].EventID != first.EventID || got[1].EventID != second.EventID || got[2].EventID != third.EventID {
		t.Fatalf("ListByTraceID() order = %#v, want event sequence order", got)
	}
	if _, err := store.ListByTraceID(ctx, traceID, 2); err == nil {
		t.Fatal("ListByTraceID() over-bound query returned nil error")
	}
	if got, err := store.ListByTraceID(ctx, modulecore.NewTraceID(), 3); err != nil || len(got) != 0 {
		t.Fatalf("ListByTraceID() missing trace = %#v, %v; want empty success", got, err)
	}
	for _, invalid := range []struct {
		trace modulecore.TraceID
		max   int
	}{
		{trace: modulecore.TraceID(""), max: 1},
		{trace: traceID, max: 0},
	} {
		if _, err := store.ListByTraceID(ctx, invalid.trace, invalid.max); err == nil {
			t.Fatalf("ListByTraceID(%q,%d) returned nil error", invalid.trace, invalid.max)
		}
	}
	if _, err := store.ListByTraceID(ctx, traceID, maxListLimit+1); err == nil {
		t.Fatal("ListByTraceID() accepted a maximum above the hard bound")
	}
	var indexExists int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('event_envelope') WHERE name = 'event_envelope_trace_idx'`).Scan(&indexExists); err != nil {
		t.Fatalf("trace index lookup: %v", err)
	}
	if indexExists != 1 {
		t.Fatal("event_envelope_trace_idx is missing")
	}
	var planID, planParent, planNotUsed int
	var queryPlan string
	if err := store.db.QueryRowContext(ctx, `EXPLAIN QUERY PLAN SELECT event_id FROM event_envelope WHERE trace_id = ? ORDER BY event_seq ASC LIMIT 4`, traceID).Scan(&planID, &planParent, &planNotUsed, &queryPlan); err != nil {
		t.Fatalf("trace query plan: %v", err)
	}
	if !strings.Contains(strings.ToUpper(queryPlan), "EVENT_ENVELOPE_TRACE_IDX") {
		t.Fatalf("exact trace lookup did not use trace index: %q", queryPlan)
	}
}

func TestListByTraceIDRejectsEnvelopeColumnDrift(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event := eventFixture(modulecore.NewTraceID(), "trace.drift", "dci")
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append(): %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		t.Fatalf("drop append-only trigger: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE event_envelope SET component_id = 'other' WHERE event_id = ?`, event.EventID); err != nil {
		t.Fatalf("mutate canonical column: %v", err)
	}
	if _, err := store.ListByTraceID(ctx, event.TraceID, 1); err == nil {
		t.Fatal("ListByTraceID() accepted column/envelope mismatch")
	}
}

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func eventFixture(traceID modulecore.TraceID, eventType, componentID string) modulecore.EventEnvelope {
	return modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		TraceID:       traceID,
		EventType:     eventType,
		ComponentID:   componentID,
		OccurredAt:    time.Date(2026, 8, 29, 22, 30, 0, 0, time.UTC),
		Payload:       map[string]any{"source": componentID},
	}
}

func assertAppendErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Append() error = nil, want text containing %q", want)
	}
	if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("Append() error = %q, want text containing %q", err, want)
	}
}

func assertEventAbsent(t *testing.T, store *SQLiteStore, eventID modulecore.EventID) {
	t.Helper()
	_, found, err := store.GetByID(context.Background(), eventID)
	if err != nil {
		t.Fatalf("GetByID(%q) error = %v", eventID, err)
	}
	if found {
		t.Fatalf("GetByID(%q) found = true, want false", eventID)
	}
}
