package eventstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

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

	for _, event := range []modulecore.EventEnvelope{root, left, right, join} {
		if err := store.Append(ctx, event); err != nil {
			t.Fatalf("Append(%q) error = %v", event.EventID, err)
		}
	}

	gotJoin, found, err := store.GetByID(ctx, join.EventID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !found {
		t.Fatal("GetByID() found = false, want true")
	}
	if !reflect.DeepEqual(gotJoin, join) {
		t.Fatalf("GetByID() = %#v, want %#v", gotJoin, join)
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
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append(event) error = %v", err)
	}

	got, found, err := store.GetByID(ctx, event.EventID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !found {
		t.Fatal("GetByID() found = false, want true")
	}
	wantJSON, err := json.Marshal(event)
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
