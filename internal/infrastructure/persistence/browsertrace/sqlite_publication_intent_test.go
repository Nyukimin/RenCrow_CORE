package browsertrace

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// intentTestContext bounds every SQLite intent call so a regression on the reserved
// connection fails as a deadline instead of hanging the package.
func intentTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// snapshotPublicationIntentRows captures the intent table including its index columns, so
// "the retry appended nothing" is a byte comparison rather than an inference from a count.
func snapshotPublicationIntentRows(t *testing.T, store *SQLiteStore) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT artifact_id, event_id, payload FROM `+publicationIntentTable+` ORDER BY artifact_id`)
	if err != nil {
		t.Fatalf("snapshot %s: %v", publicationIntentTable, err)
	}
	defer rows.Close()
	snapshot := []string{}
	for rows.Next() {
		var artifactID, eventID, payload string
		if err := rows.Scan(&artifactID, &eventID, &payload); err != nil {
			t.Fatalf("scan %s snapshot: %v", publicationIntentTable, err)
		}
		snapshot = append(snapshot, strings.Join([]string{artifactID, eventID, payload}, "\x00"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s snapshot: %v", publicationIntentTable, err)
	}
	return snapshot
}

// mustDrainPublicationIntents walks every page with the given page size and returns the
// concatenated result, which is how a caller proves that nothing older was dropped.
func mustDrainPublicationIntents(t *testing.T, store *SQLiteStore, pageSize int) []modulecore.EventEnvelope {
	t.Helper()
	ctx := intentTestContext(t)
	drained := []modulecore.EventEnvelope{}
	var after modulecore.ArtifactID
	for {
		page, err := store.ListAPIArtifactPublicationIntents(ctx, after, pageSize)
		if err != nil {
			t.Fatalf("ListAPIArtifactPublicationIntents(after %q, limit %d) error = %v", after, pageSize, err)
		}
		if len(page) == 0 {
			return drained
		}
		drained = append(drained, page...)
		after = page[len(page)-1].ArtifactID
		if len(drained) > 1000 {
			t.Fatalf("ListAPIArtifactPublicationIntents never drained after %d intents", len(drained))
		}
	}
}

func mustCreateArtifactWithIntent(t *testing.T, store *SQLiteStore, item domaintrace.APIArtifact, intent modulecore.EventEnvelope) {
	t.Helper()
	if err := store.CreateAPIArtifactWithPublicationIntent(intentTestContext(t), item, intent); err != nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntent(%s) error = %v", item.ArtifactID, err)
	}
}

// TestSQLiteStoreCreateAPIArtifactWithPublicationIntentCoPersistsBothRecords pins the
// creation boundary on SQLite: the artifact row and the typed creation envelope land in
// one transaction, and a reopened store resolves the same pair.
func TestSQLiteStoreCreateAPIArtifactWithPublicationIntentCoPersistsBothRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
	store := newSupersedeStoreAt(t, path)
	workstreamID := intentFixtureWorkstreamID(t)
	item := intentFixtureArtifact(t, workstreamID)
	intent := intentFixtureEnvelope(t, item)

	mustCreateArtifactWithIntent(t, store, item, intent)

	if got := mustFindSupersedeArtifact(t, store, item.ArtifactID); !reflect.DeepEqual(got, item) {
		t.Errorf("artifact row after create = %+v, want the created artifact %+v", got, item)
	}
	page := mustDrainPublicationIntents(t, store, 10)
	if len(page) != 1 || !reflect.DeepEqual(page[0], intent) {
		t.Errorf("stored intents after create = %d %v, want exactly the envelope %s", len(page), intentIDs(page), intent.EventID)
	}

	reopened := newSupersedeStoreAt(t, path)
	if got := mustDrainPublicationIntents(t, reopened, 10); len(got) != 1 || !reflect.DeepEqual(got[0], intent) {
		t.Errorf("stored intents after reopen = %d %v, want exactly the envelope %s", len(got), intentIDs(got), intent.EventID)
	}
}

// TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRetryAppendsNothing proves the
// no-op outcome at the row level: the intent table keeps the one row it already had, byte
// for byte, so a rerun after an unconfirmed commit cannot mint a second event.
func TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRetryAppendsNothing(t *testing.T) {
	store := newSupersedeStore(t)
	item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	intent := intentFixtureEnvelope(t, item)
	mustCreateArtifactWithIntent(t, store, item, intent)

	beforeArtifacts := snapshotAPIArtifactRows(t, store)
	beforeIntents := snapshotPublicationIntentRows(t, store)
	if len(beforeIntents) != 1 {
		t.Fatalf("%s holds %d rows after one create, want 1", publicationIntentTable, len(beforeIntents))
	}

	ctx := intentTestContext(t)
	for attempt := 1; attempt <= 3; attempt++ {
		if err := store.CreateAPIArtifactWithPublicationIntent(ctx, item, intent); err != nil {
			t.Errorf("retry %d returned %v, want a no-op for the stored pair", attempt, err)
		}
	}
	if afterArtifacts := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(afterArtifacts, beforeArtifacts) {
		t.Errorf("api_artifact rows after retries changed:\nbefore %q\nafter  %q", beforeArtifacts, afterArtifacts)
	}
	if afterIntents := snapshotPublicationIntentRows(t, store); !reflect.DeepEqual(afterIntents, beforeIntents) {
		t.Errorf("%s rows after retries changed:\nbefore %q\nafter  %q", publicationIntentTable, beforeIntents, afterIntents)
	}
}

// TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRejectsConflictingIntent keeps a
// second, differently issued creation fact for one artifact from overwriting the first.
func TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRejectsConflictingIntent(t *testing.T) {
	store := newSupersedeStore(t)
	item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	intent := intentFixtureEnvelope(t, item)
	mustCreateArtifactWithIntent(t, store, item, intent)

	conflicting := intentFixtureEnvelope(t, item)
	if conflicting.EventID == intent.EventID {
		t.Fatalf("conflicting fixture reused event id %s", intent.EventID)
	}
	beforeArtifacts := snapshotAPIArtifactRows(t, store)
	beforeIntents := snapshotPublicationIntentRows(t, store)

	err := store.CreateAPIArtifactWithPublicationIntent(intentTestContext(t), item, conflicting)
	if err == nil {
		t.Fatalf("create with a second event id for artifact %s returned nil, want a conflict", item.ArtifactID)
	}
	if !strings.Contains(err.Error(), "second event for one creation") {
		t.Errorf("conflicting create error = %v, want the already-carries-intent conflict", err)
	}
	if got := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(got, beforeArtifacts) {
		t.Errorf("api_artifact rows changed after the rejected create:\nbefore %q\nafter  %q", beforeArtifacts, got)
	}
	if got := snapshotPublicationIntentRows(t, store); !reflect.DeepEqual(got, beforeIntents) {
		t.Errorf("%s rows changed after the rejected create:\nbefore %q\nafter  %q", publicationIntentTable, beforeIntents, got)
	}
	if page := mustDrainPublicationIntents(t, store, 10); len(page) != 1 || !reflect.DeepEqual(page[0], intent) {
		t.Errorf("stored intents after the rejected create = %v, want only %s", intentIDs(page), intent.EventID)
	}
}

// TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRejectsSharedEventID covers the
// binding that one creation fact belongs to one artifact: a brand new artifact cannot
// claim the event id another artifact already owns.
func TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRejectsSharedEventID(t *testing.T) {
	store := newSupersedeStore(t)
	workstreamID := intentFixtureWorkstreamID(t)
	first := intentFixtureArtifact(t, workstreamID)
	firstIntent := intentFixtureEnvelope(t, first)
	mustCreateArtifactWithIntent(t, store, first, firstIntent)

	second := intentFixtureArtifact(t, workstreamID)
	sharing := intentFixtureEnvelope(t, second)
	sharing.EventID = firstIntent.EventID

	beforeArtifacts := snapshotAPIArtifactRows(t, store)
	beforeIntents := snapshotPublicationIntentRows(t, store)

	err := store.CreateAPIArtifactWithPublicationIntent(intentTestContext(t), second, sharing)
	if err == nil {
		t.Fatalf("create of artifact %s with event id %s returned nil, want a conflict with artifact %s", second.ArtifactID, sharing.EventID, first.ArtifactID)
	}
	if !strings.Contains(err.Error(), "cannot claim the same event") {
		t.Errorf("shared event id error = %v, want the already-a-creation-fact conflict", err)
	}
	if got := mustFindSupersedeArtifact(t, store, first.ArtifactID); !reflect.DeepEqual(got, first) {
		t.Errorf("first artifact changed to %+v", got)
	}
	if got := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(got, beforeArtifacts) {
		t.Errorf("api_artifact rows changed after the rejected create:\nbefore %q\nafter  %q", beforeArtifacts, got)
	}
	if got := snapshotPublicationIntentRows(t, store); !reflect.DeepEqual(got, beforeIntents) {
		t.Errorf("%s rows changed after the rejected create:\nbefore %q\nafter  %q", publicationIntentTable, beforeIntents, got)
	}
}

// TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRejectsRowStoredWithoutIntent keeps
// a migration backfill from posing as an ordinary create: a row written by SaveAPIArtifact
// has no creation fact of its own here, and this call mints and appends nothing.
func TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRejectsRowStoredWithoutIntent(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, store *SQLiteStore, item, successor domaintrace.APIArtifact)
	}{
		{"row saved by an ordinary save", func(t *testing.T, store *SQLiteStore, item, _ domaintrace.APIArtifact) {
			mustSaveSupersedeArtifacts(t, store, item)
		}},
		{"row that was already superseded", func(t *testing.T, store *SQLiteStore, item, successor domaintrace.APIArtifact) {
			mustSaveSupersedeArtifacts(t, store, item, successor)
			if err := store.SupersedeAPIArtifact(intentTestContext(t), item.ArtifactID, successor.ArtifactID); err != nil {
				t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", item.ArtifactID, successor.ArtifactID, err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newSupersedeStore(t)
			item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
			successor := intentFixtureArtifact(t, modulecore.WorkstreamID(item.WorkstreamID))
			tc.setup(t, store, item, successor)

			beforeArtifacts := snapshotAPIArtifactRows(t, store)
			beforeIntents := snapshotPublicationIntentRows(t, store)

			err := store.CreateAPIArtifactWithPublicationIntent(intentTestContext(t), item, intentFixtureEnvelope(t, item))
			if err == nil {
				t.Fatalf("create over a row written without an intent returned nil, want a predates rejection")
			}
			if !strings.Contains(err.Error(), "predates CreateAPIArtifactWithPublicationIntent") {
				t.Errorf("create over an intent-less row = %v, want the predates rejection", err)
			}
			if got := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(got, beforeArtifacts) {
				t.Errorf("api_artifact rows changed:\nbefore %q\nafter  %q", beforeArtifacts, got)
			}
			if got := snapshotPublicationIntentRows(t, store); !reflect.DeepEqual(got, beforeIntents) {
				t.Errorf("%s rows changed:\nbefore %q\nafter  %q", publicationIntentTable, beforeIntents, got)
			}
			if len(beforeIntents) != 0 {
				t.Fatalf("fixture wrote %d intent rows, want a row that predates this create", len(beforeIntents))
			}
		})
	}
}

// TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRetryRequiresTheArtifactRow closes
// the hole where a stored intent alone was enough to call a retry delivered: with the row
// gone the pair resolves to nothing, so the retry has to fail closed. The row removal is a
// deliberate fixture corruption, not an owner operation.
func TestSQLiteStoreCreateAPIArtifactWithPublicationIntentRetryRequiresTheArtifactRow(t *testing.T) {
	store := newSupersedeStore(t)
	item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	intent := intentFixtureEnvelope(t, item)
	mustCreateArtifactWithIntent(t, store, item, intent)

	result, err := store.db.ExecContext(intentTestContext(t), `DELETE FROM api_artifact WHERE artifact_id = ?`, string(item.ArtifactID))
	if err != nil {
		t.Fatalf("fixture delete of artifact %s: %v", item.ArtifactID, err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("fixture delete affected %d rows, want 1", affected)
	}

	err = store.CreateAPIArtifactWithPublicationIntent(intentTestContext(t), item, intent)
	if err == nil {
		t.Fatalf("retry with a stored intent but no artifact row returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "no row in api_artifact") {
		t.Errorf("retry without its row = %v, want the missing-row rejection", err)
	}
	remaining, err := store.ListAPIArtifacts(intentTestContext(t), 50)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() after the rejected retry error = %v", err)
	}
	for _, stored := range remaining {
		if stored.ArtifactID == item.ArtifactID {
			t.Errorf("failed retry recreated the artifact row: %+v", stored)
		}
	}
	if got := snapshotPublicationIntentRows(t, store); len(got) != 1 || !strings.HasPrefix(got[0], string(item.ArtifactID)+"\x00"+string(intent.EventID)+"\x00") {
		t.Errorf("%s rows after the failed retry = %q, want the one untouched intent", publicationIntentTable, got)
	}
}

// TestSQLiteStoreListAPIArtifactPublicationIntentsDrainsEveryPage pins keyset paging: the
// page size bounds one call only, so repeated pages return every stored intent in
// ascending artifact id order with no duplicates and no total-history cutoff.
func TestSQLiteStoreListAPIArtifactPublicationIntentsDrainsEveryPage(t *testing.T) {
	store := newSupersedeStore(t)
	workstreamID := intentFixtureWorkstreamID(t)
	created := []modulecore.EventEnvelope{}
	for i := 0; i < 5; i++ {
		item := intentFixtureArtifact(t, workstreamID)
		intent := intentFixtureEnvelope(t, item)
		mustCreateArtifactWithIntent(t, store, item, intent)
		created = append(created, intent)
	}
	wantIDs := make([]string, 0, len(created))
	for _, intent := range created {
		wantIDs = append(wantIDs, string(intent.ArtifactID))
	}
	sort.Strings(wantIDs)

	for _, pageSize := range []int{1, 2, 3, 0, 5_000} {
		drained := mustDrainPublicationIntents(t, store, pageSize)
		gotIDs := intentArtifactIDs(drained)
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Errorf("drain with page size %d returned artifact ids %q, want every intent in ascending order %q", pageSize, gotIDs, wantIDs)
		}
		for _, intent := range drained {
			if intent.EventSeq != 0 || intent.EventType != domaintrace.APIArtifactCreatedEventType {
				t.Errorf("drained intent %s lost its unpublishable shape: seq %d type %q", intent.EventID, intent.EventSeq, intent.EventType)
			}
		}
	}

	if _, err := store.ListAPIArtifactPublicationIntents(intentTestContext(t), modulecore.ArtifactID("not-an-id"), 10); err == nil {
		t.Errorf("ListAPIArtifactPublicationIntents accepted a malformed cursor, want a rejection")
	}
}

// TestSQLiteStoreSaveAndSupersedeAPIArtifactPreservePublicationIntent keeps the two owner
// operations that legitimately change an artifact row away from the creation fact: an
// in-place body update and a supersession must leave the original envelope untouched, and a
// reopened store must still be able to list it.
func TestSQLiteStoreSaveAndSupersedeAPIArtifactPreservePublicationIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
	store := newSupersedeStoreAt(t, path)
	workstreamID := intentFixtureWorkstreamID(t)
	predecessor := intentFixtureArtifact(t, workstreamID)
	predecessorIntent := intentFixtureEnvelope(t, predecessor)
	successor := intentFixtureArtifact(t, workstreamID)
	successorIntent := intentFixtureEnvelope(t, successor)
	mustCreateArtifactWithIntent(t, store, predecessor, predecessorIntent)
	mustCreateArtifactWithIntent(t, store, successor, successorIntent)

	beforeIntents := snapshotPublicationIntentRows(t, store)
	if len(beforeIntents) != 2 {
		t.Fatalf("%s holds %d rows after two creates, want 2", publicationIntentTable, len(beforeIntents))
	}

	ctx := intentTestContext(t)
	updated := predecessor
	updated.Content = updatedIntentBody
	updated.ContentHash = modulecore.ContentHashOf([]byte(updatedIntentBody))
	updated.Title = "Updated After Creation"
	if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
		t.Fatalf("updated fixture is invalid before any owner operation: %v", err)
	}
	if err := store.SaveAPIArtifact(ctx, updated); err != nil {
		t.Fatalf("SaveAPIArtifact(%s) body update error = %v", updated.ArtifactID, err)
	}
	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
	}

	stored := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID)
	if stored.ContentHash != updated.ContentHash || stored.SupersededBy != successor.ArtifactID {
		t.Errorf("predecessor row = hash %q edge %q, want the updated digest and the %s edge", stored.ContentHash, stored.SupersededBy, successor.ArtifactID)
	}
	if got := snapshotPublicationIntentRows(t, store); !reflect.DeepEqual(got, beforeIntents) {
		t.Errorf("%s rows changed after an update and a supersession:\nbefore %q\nafter  %q", publicationIntentTable, beforeIntents, got)
	}

	reopened := newSupersedeStoreAt(t, path)
	drained := mustDrainPublicationIntents(t, reopened, 10)
	want := map[modulecore.EventID]modulecore.EventEnvelope{
		predecessorIntent.EventID: predecessorIntent,
		successorIntent.EventID:   successorIntent,
	}
	if len(drained) != 2 {
		t.Fatalf("reopened store listed %d intents %v, want the two creation envelopes", len(drained), intentIDs(drained))
	}
	for _, intent := range drained {
		original, found := want[intent.EventID]
		if !found {
			t.Errorf("reopened store listed an unknown intent %s", intent.EventID)
			continue
		}
		if !reflect.DeepEqual(intent, original) {
			t.Errorf("creation intent %s after reopen = %+v, want the original creation envelope %+v", intent.EventID, intent, original)
		}
	}
}

// TestSQLiteStoreListAPIArtifactPublicationIntentsFailsClosedOnBrokenIntent refuses to hand
// a replay an intent that resolves to nothing, disagrees with its own index columns, or
// names an identity its artifact row no longer carries. Each broken row is written straight
// into the database as a corruption injection, not through an owner method.
func TestSQLiteStoreListAPIArtifactPublicationIntentsFailsClosedOnBrokenIntent(t *testing.T) {
	t.Run("intent bound to a missing artifact row", func(t *testing.T) {
		store := newSupersedeStore(t)
		item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
		intent := intentFixtureEnvelope(t, item)
		mustCreateArtifactWithIntent(t, store, item, intent)

		// Corruption injection: drop the row the way an interrupted writer would.
		if _, err := store.db.ExecContext(intentTestContext(t), `DELETE FROM api_artifact WHERE artifact_id = ?`, string(item.ArtifactID)); err != nil {
			t.Fatalf("fixture delete: %v", err)
		}
		_, err := store.ListAPIArtifactPublicationIntents(intentTestContext(t), "", 10)
		if err == nil {
			t.Fatalf("list over an orphan intent returned nil, want a fail-closed error")
		}
		if !strings.Contains(err.Error(), "no row in api_artifact") {
			t.Errorf("orphan intent error = %v, want the missing-row report", err)
		}
	})

	t.Run("index column disagrees with the payload", func(t *testing.T) {
		store := newSupersedeStore(t)
		item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
		intent := intentFixtureEnvelope(t, item)
		mustCreateArtifactWithIntent(t, store, item, intent)

		// Corruption injection: rewrite only the event_id index column.
		result, err := store.db.ExecContext(intentTestContext(t),
			`UPDATE `+publicationIntentTable+` SET event_id = ? WHERE artifact_id = ?`,
			string(modulecore.NewEventID()), string(item.ArtifactID))
		if err != nil {
			t.Fatalf("fixture update of the event_id column: %v", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			t.Fatalf("fixture update affected %d rows, want 1", affected)
		}
		_, err = store.ListAPIArtifactPublicationIntents(intentTestContext(t), "", 10)
		if err == nil {
			t.Fatalf("list over a mismatched event_id column returned nil, want a fail-closed error")
		}
		if !strings.Contains(err.Error(), "event_id column") {
			t.Errorf("mismatched column error = %v, want the column disagreement report", err)
		}
	})

	t.Run("artifact row moved out of the claimed identity", func(t *testing.T) {
		store := newSupersedeStore(t)
		item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
		intent := intentFixtureEnvelope(t, item)
		mustCreateArtifactWithIntent(t, store, item, intent)

		// Corruption injection: move the row's task, which no owner operation may do.
		moved := item
		moved.TaskID = "tsk_00000000-0000-5000-8000-000000000009"
		if err := domaintrace.ValidateAPIArtifact(moved); err != nil {
			t.Fatalf("moved fixture is invalid: %v", err)
		}
		payload, err := json.Marshal(moved)
		if err != nil {
			t.Fatalf("marshal moved fixture: %v", err)
		}
		result, err := store.db.ExecContext(intentTestContext(t), `UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), string(item.ArtifactID))
		if err != nil {
			t.Fatalf("fixture update of the artifact payload: %v", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			t.Fatalf("fixture update affected %d rows, want 1", affected)
		}
		_, err = store.ListAPIArtifactPublicationIntents(intentTestContext(t), "", 10)
		if err == nil {
			t.Fatalf("list over a row whose task moved returned nil, want a fail-closed error")
		}
		if !strings.Contains(err.Error(), "task_id") {
			t.Errorf("moved identity error = %v, want the immutable-reference report", err)
		}
	})
}

func intentIDs(intents []modulecore.EventEnvelope) []modulecore.EventID {
	ids := make([]modulecore.EventID, 0, len(intents))
	for _, intent := range intents {
		ids = append(ids, intent.EventID)
	}
	return ids
}

func intentArtifactIDs(intents []modulecore.EventEnvelope) []string {
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		ids = append(ids, string(intent.ArtifactID))
	}
	return ids
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
