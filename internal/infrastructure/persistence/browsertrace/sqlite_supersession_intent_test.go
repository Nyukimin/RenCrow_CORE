package browsertrace

import (
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

// supersessionFactFixture mints the supersession envelope of one edge about to be
// established. Issuance is the caller's job in production, so this helper plays that caller:
// it mints a fresh v7 event id and carries the two digests the rows hold right now. The
// accepted artifact fixture workstream ("ws_1") is not a canonical envelope workstream id,
// so these fixtures mint one and use the same value on both sides.
func supersessionFactFixture(t *testing.T, predecessor, successor domaintrace.APIArtifact) modulecore.EventEnvelope {
	t.Helper()
	fact := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		EventSeq:      0,
		TraceID:       modulecore.NewTraceID(),
		EventType:     domaintrace.APIArtifactSupersededEventType,
		ComponentID:   domaintrace.APIArtifactPublicationComponentID,
		OccurredAt:    time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		WorkstreamID:  modulecore.WorkstreamID(predecessor.WorkstreamID),
		TaskID:        predecessor.TaskID,
		RunID:         predecessor.RunID,
		ActorID:       predecessor.ActorID,
		ActorKind:     intentFixtureActorKind,
		ArtifactID:    predecessor.ArtifactID,
		Payload: map[string]any{
			domaintrace.APIArtifactSupersessionPayloadSupersededBy:           string(successor.ArtifactID),
			domaintrace.APIArtifactSupersessionPayloadPredecessorContentHash: predecessor.ContentHash,
			domaintrace.APIArtifactSupersessionPayloadSuccessorContentHash:   successor.ContentHash,
		},
	}
	if err := modulecore.ValidateEventEnvelope(fact); err != nil {
		t.Fatalf("supersession fact fixture %s is invalid before any owner operation: %v", fact.EventID, err)
	}
	if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
		t.Fatalf("supersession fact fixture %s fails the persisted-fact contract: %v", fact.EventID, err)
	}
	return fact
}

// supersessionFixtureSavedPair saves two valid edge-free artifacts and returns them, so a
// refusal below can only come from the supersession rules.
func supersessionFixtureSavedPair(t *testing.T, store *SQLiteStore) (domaintrace.APIArtifact, domaintrace.APIArtifact) {
	t.Helper()
	workstreamID := intentFixtureWorkstreamID(t)
	predecessor := intentFixtureArtifact(t, workstreamID)
	successor := intentFixtureArtifact(t, workstreamID)
	mustSaveSupersedeArtifacts(t, store, predecessor, successor)
	return predecessor, successor
}

// snapshotSupersessionIntentRows captures the fact table including its index columns, so
// "the retry wrote nothing" is a row comparison rather than an inference from a count.
func snapshotSupersessionIntentRows(t *testing.T, store *SQLiteStore) []string {
	t.Helper()
	rows, err := store.db.QueryContext(intentTestContext(t), `SELECT artifact_id, event_id, payload FROM `+supersessionIntentTable+` ORDER BY artifact_id`)
	if err != nil {
		t.Fatalf("snapshot %s: %v", supersessionIntentTable, err)
	}
	defer rows.Close()
	snapshot := []string{}
	for rows.Next() {
		var artifactID, eventID, payload string
		if err := rows.Scan(&artifactID, &eventID, &payload); err != nil {
			t.Fatalf("scan %s snapshot: %v", supersessionIntentTable, err)
		}
		snapshot = append(snapshot, strings.Join([]string{artifactID, eventID, payload}, "\x00"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s snapshot: %v", supersessionIntentTable, err)
	}
	return snapshot
}

// mustSupersedeWithFact runs the owner supersede that also persists the fact.
func mustSupersedeWithFact(t *testing.T, store *SQLiteStore, predecessorID, successorID modulecore.ArtifactID, fact modulecore.EventEnvelope) {
	t.Helper()
	if err := store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessorID, successorID, fact); err != nil {
		t.Fatalf("SupersedeAPIArtifactWithPublicationIntent(%s -> %s) error = %v", predecessorID, successorID, err)
	}
}

// mustDrainSupersessionFacts walks every page with the given page size and concatenates the
// result, which is how a caller proves nothing older was dropped.
func mustDrainSupersessionFacts(t *testing.T, store *SQLiteStore, pageSize int) []modulecore.EventEnvelope {
	t.Helper()
	ctx := intentTestContext(t)
	drained := []modulecore.EventEnvelope{}
	var after modulecore.ArtifactID
	for {
		page, err := store.ListAPIArtifactSupersessionFacts(ctx, after, pageSize)
		if err != nil {
			t.Fatalf("ListAPIArtifactSupersessionFacts(after %q, limit %d) error = %v", after, pageSize, err)
		}
		if len(page) == 0 {
			return drained
		}
		drained = append(drained, page...)
		after = page[len(page)-1].ArtifactID
		if len(drained) > 1000 {
			t.Fatalf("ListAPIArtifactSupersessionFacts never drained after %d facts", len(drained))
		}
	}
}

// TestSQLiteStoreSupersedeAPIArtifactWithPublicationIntentCoPersistsEdgeAndFact pins the
// write boundary: the edge and the typed supersession envelope land in one transaction, and
// a reopened store resolves both rows plus the fact unchanged.
func TestSQLiteStoreSupersedeAPIArtifactWithPublicationIntentCoPersistsEdgeAndFact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
	store := newSupersedeStoreAt(t, path)
	predecessor, successor := supersessionFixtureSavedPair(t, store)
	fact := supersessionFactFixture(t, predecessor, successor)

	mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

	wantPredecessor := predecessor
	wantPredecessor.SupersededBy = successor.ArtifactID
	if got := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID); !reflect.DeepEqual(got, wantPredecessor) {
		t.Errorf("predecessor row after supersede = %+v, want %+v", got, wantPredecessor)
	}
	if got := mustFindSupersedeArtifact(t, store, successor.ArtifactID); !reflect.DeepEqual(got, successor) {
		t.Errorf("successor row after supersede = %+v, want the unchanged successor %+v", got, successor)
	}
	rows := snapshotSupersessionIntentRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("%s holds %d rows after one supersede, want 1: %q", supersessionIntentTable, len(rows), rows)
	}
	if !strings.HasPrefix(rows[0], string(predecessor.ArtifactID)+"\x00"+string(fact.EventID)+"\x00") {
		t.Errorf("%s row = %q, want the predecessor and event id as validated indexes of the payload", supersessionIntentTable, rows[0])
	}
	drained := mustDrainSupersessionFacts(t, store, 10)
	if len(drained) != 1 || !reflect.DeepEqual(drained[0], fact) {
		t.Fatalf("stored facts after supersede = %d %v, want exactly the envelope %s", len(drained), intentIDs(drained), fact.EventID)
	}

	reopened := newSupersedeStoreAt(t, path)
	if got := mustDrainSupersessionFacts(t, reopened, 10); len(got) != 1 || !reflect.DeepEqual(got[0], fact) {
		t.Errorf("facts after reopen = %d %v, want exactly %s", len(got), intentIDs(got), fact.EventID)
	}
	if got := mustFindSupersedeArtifact(t, reopened, predecessor.ArtifactID); !reflect.DeepEqual(got, wantPredecessor) {
		t.Errorf("predecessor row after reopen = %+v, want %+v", got, wantPredecessor)
	}
}

// TestSQLiteStoreSupersedeAPIArtifactWithPublicationIntentRetryAppendsNothing proves the
// no-op at row level: resubmitting the exact edge and fact writes nothing, so a rerun after
// an unconfirmed commit cannot mint a second event for one supersession.
func TestSQLiteStoreSupersedeAPIArtifactWithPublicationIntentRetryAppendsNothing(t *testing.T) {
	store := newSupersedeStore(t)
	predecessor, successor := supersessionFixtureSavedPair(t, store)
	fact := supersessionFactFixture(t, predecessor, successor)
	mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

	beforeArtifacts := snapshotAPIArtifactRows(t, store)
	beforeFacts := snapshotSupersessionIntentRows(t, store)
	if len(beforeFacts) != 1 {
		t.Fatalf("%s holds %d rows after one supersede, want 1", supersessionIntentTable, len(beforeFacts))
	}

	ctx := intentTestContext(t)
	for attempt := 1; attempt <= 3; attempt++ {
		if err := store.SupersedeAPIArtifactWithPublicationIntent(ctx, predecessor.ArtifactID, successor.ArtifactID, fact); err != nil {
			t.Errorf("retry %d returned %v, want a no-op for the stored edge and fact", attempt, err)
		}
	}
	if got := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(got, beforeArtifacts) {
		t.Errorf("api_artifact rows after retries changed:\nbefore %q\nafter  %q", beforeArtifacts, got)
	}
	if got := snapshotSupersessionIntentRows(t, store); !reflect.DeepEqual(got, beforeFacts) {
		t.Errorf("%s rows after retries changed:\nbefore %q\nafter  %q", supersessionIntentTable, beforeFacts, got)
	}
}

// TestSQLiteStoreSupersedeAPIArtifactWithPublicationIntentRefusesEverySecondFact covers the
// refusals that protect one supersession from carrying two events: a differently issued fact
// for an edge that already has one, an event id another predecessor already owns, and the
// two edges that are already taken.
func TestSQLiteStoreSupersedeAPIArtifactWithPublicationIntentRefusesEverySecondFact(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact) domaintrace.APIArtifact
		fact  func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact, third domaintrace.APIArtifact) modulecore.EventEnvelope
		want  string
	}{
		{
			name: "a second event id for one supersession",
			setup: func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact) domaintrace.APIArtifact {
				mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, supersessionFactFixture(t, predecessor, successor))
				return successor
			},
			fact: func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact, _ domaintrace.APIArtifact) modulecore.EventEnvelope {
				return supersessionFactFixture(t, predecessor, successor)
			},
			want: "second event for one supersession",
		},
		{
			name: "an event id another predecessor already owns",
			setup: func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact) domaintrace.APIArtifact {
				// The other predecessor has to live in the successor's scope, otherwise the
				// scope guard rejects it and the shared event id is never reached.
				other := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
				otherFact := supersessionFactFixture(t, other, successor)
				mustSaveSupersedeArtifacts(t, store, other)
				mustSupersedeWithFact(t, store, other.ArtifactID, successor.ArtifactID, otherFact)
				return other
			},
			fact: func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact, third domaintrace.APIArtifact) modulecore.EventEnvelope {
				otherFact := modulecore.EventEnvelope{}
				for _, row := range snapshotSupersessionIntentRows(t, store) {
					if err := json.Unmarshal([]byte(strings.SplitN(row, "\x00", 3)[2]), &otherFact); err != nil {
						t.Fatalf("decode stored fact: %v", err)
					}
				}
				if otherFact.EventID == "" {
					t.Fatalf("setup stored no supersession fact to share")
				}
				// Same event id, but issued for this predecessor and its own digests.
				claimed := supersessionFactFixture(t, predecessor, third)
				claimed.EventID = otherFact.EventID
				return claimed
			},
			want: "cannot claim the same event",
		},
		{
			name: "an edge already established without a fact",
			setup: func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact) domaintrace.APIArtifact {
				if err := store.SupersedeAPIArtifact(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID); err != nil {
					t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
				}
				return successor
			},
			fact: func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact, _ domaintrace.APIArtifact) modulecore.EventEnvelope {
				return supersessionFactFixture(t, predecessor, successor)
			},
			want: "predates SupersedeAPIArtifactWithPublicationIntent",
		},
		{
			name: "a predecessor already superseded by another successor",
			setup: func(t *testing.T, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact) domaintrace.APIArtifact {
				mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, supersessionFactFixture(t, predecessor, successor))
				return successor
			},
			fact: func(t *testing.T, store *SQLiteStore, predecessor, _ domaintrace.APIArtifact, third domaintrace.APIArtifact) modulecore.EventEnvelope {
				// The first fact is stored, so a different successor has to be refused as a
				// second event for one supersession before the edge itself is considered.
				return supersessionFactFixture(t, predecessor, third)
			},
			want: "second event for one supersession",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newSupersedeStore(t)
			predecessor, successor := supersessionFixtureSavedPair(t, store)
			third := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
			mustSaveSupersedeArtifacts(t, store, third)
			tc.setup(t, store, predecessor, successor)

			fact := tc.fact(t, store, predecessor, successor, third)
			beforeArtifacts := snapshotAPIArtifactRows(t, store)
			beforeFacts := snapshotSupersessionIntentRows(t, store)

			err := store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID, fact)
			if err == nil {
				t.Fatalf("supersede %s -> %s with fact %s returned nil, want a refusal", predecessor.ArtifactID, successor.ArtifactID, fact.EventID)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %v, want it to report %q", err, tc.want)
			}
			if got := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(got, beforeArtifacts) {
				t.Errorf("api_artifact rows changed after the refused supersede:\nbefore %q\nafter  %q", beforeArtifacts, got)
			}
			if got := snapshotSupersessionIntentRows(t, store); !reflect.DeepEqual(got, beforeFacts) {
				t.Errorf("%s rows changed after the refused supersede:\nbefore %q\nafter  %q", supersessionIntentTable, beforeFacts, got)
			}
		})
	}
}

// TestSQLiteStoreSupersedeAPIArtifactWithPublicationIntentRefusesBrokenPairs keeps a fact
// that does not describe the rows being written out of history: a missing end of the edge, a
// digest that is not the superseded bytes, and a predecessor that already carries an edge.
func TestSQLiteStoreSupersedeAPIArtifactWithPublicationIntentRefusesBrokenPairs(t *testing.T) {
	t.Run("predecessor row missing", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		if _, err := store.db.ExecContext(intentTestContext(t), `DELETE FROM api_artifact WHERE artifact_id = ?`, string(predecessor.ArtifactID)); err != nil {
			t.Fatalf("fixture delete of the predecessor: %v", err)
		}
		err := store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID, fact)
		if err == nil || !strings.Contains(err.Error(), "is not in api_artifact") {
			t.Errorf("supersede without its predecessor row = %v, want the missing-row refusal", err)
		}
		if got := snapshotSupersessionIntentRows(t, store); len(got) != 0 {
			t.Errorf("%s rows = %q, want no fact written for a missing row", supersessionIntentTable, got)
		}
	})

	t.Run("successor row missing", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		if _, err := store.db.ExecContext(intentTestContext(t), `DELETE FROM api_artifact WHERE artifact_id = ?`, string(successor.ArtifactID)); err != nil {
			t.Fatalf("fixture delete of the successor: %v", err)
		}
		err := store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID, fact)
		if err == nil || !strings.Contains(err.Error(), "is not in api_artifact") {
			t.Errorf("supersede without its successor row = %v, want the missing-row refusal", err)
		}
		if got := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID); got.SupersededBy != "" {
			t.Errorf("predecessor edge = %q, want no edge written beside a missing successor", got.SupersededBy)
		}
	})

	t.Run("digest is not the superseded bytes", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		fact.Payload[domaintrace.APIArtifactSupersessionPayloadPredecessorContentHash] = modulecore.ContentHashOf([]byte("bytes that were never stored"))
		err := store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID, fact)
		if err == nil || !strings.Contains(err.Error(), "predecessor_content_hash") {
			t.Errorf("supersede with a foreign digest = %v, want the digest mismatch", err)
		}
		if got := snapshotSupersessionIntentRows(t, store); len(got) != 0 {
			t.Errorf("%s rows = %q, want nothing written beside a digest mismatch", supersessionIntentTable, got)
		}
	})

	t.Run("successor digest is foreign", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		fact.Payload[domaintrace.APIArtifactSupersessionPayloadSuccessorContentHash] = modulecore.ContentHashOf([]byte("other successor body"))
		err := store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID, fact)
		if err == nil || !strings.Contains(err.Error(), "successor_content_hash") {
			t.Errorf("supersede with a foreign successor digest = %v, want the digest mismatch", err)
		}
	})
}

// TestSQLiteStoreListAPIArtifactSupersessionFactsDrainsEveryPage pins keyset paging: the page
// size bounds one call only, so repeated pages return every stored fact in ascending
// predecessor id order with no duplicates and no cutoff of older history.
func TestSQLiteStoreListAPIArtifactSupersessionFactsDrainsEveryPage(t *testing.T) {
	store := newSupersedeStore(t)
	pairs := make([][2]domaintrace.APIArtifact, 0, 5)
	for i := 0; i < 5; i++ {
		first, second := supersessionFixtureSavedPair(t, store)
		pairs = append(pairs, [2]domaintrace.APIArtifact{first, second})
	}
	wantIDs := make([]string, 0, len(pairs))
	for i, pair := range pairs {
		if i == 2 {
			// One edge established without a fact: it is not a fact to drain, and the drain
			// must not fail closed on it while the create caller is not switched over.
			if err := store.SupersedeAPIArtifact(intentTestContext(t), pair[0].ArtifactID, pair[1].ArtifactID); err != nil {
				t.Fatalf("plain SupersedeAPIArtifact(%s -> %s) error = %v", pair[0].ArtifactID, pair[1].ArtifactID, err)
			}
			continue
		}
		mustSupersedeWithFact(t, store, pair[0].ArtifactID, pair[1].ArtifactID, supersessionFactFixture(t, pair[0], pair[1]))
		wantIDs = append(wantIDs, string(pair[0].ArtifactID))
	}
	sort.Strings(wantIDs)

	for _, pageSize := range []int{1, 2, 3, 0, 5_000} {
		drained := mustDrainSupersessionFacts(t, store, pageSize)
		gotIDs := intentArtifactIDs(drained)
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Errorf("drain with page size %d returned predecessor ids %q, want every fact in ascending order %q", pageSize, gotIDs, wantIDs)
		}
		for _, fact := range drained {
			if fact.EventSeq != 0 || fact.EventType != domaintrace.APIArtifactSupersededEventType {
				t.Errorf("drained fact %s lost its unpublishable shape: seq %d type %q", fact.EventID, fact.EventSeq, fact.EventType)
			}
		}
	}

	if _, err := store.ListAPIArtifactSupersessionFacts(intentTestContext(t), modulecore.ArtifactID("not-an-id"), 10); err == nil {
		t.Errorf("ListAPIArtifactSupersessionFacts accepted a malformed cursor, want a rejection")
	}
}

// TestSQLiteStoreSaveAndPlainSupersedePreserveSupersessionFact keeps the operations that
// legitimately change an artifact away from the supersession fact: an in-place body update
// replaces the digest the fact records as history, and that must not turn the stored fact
// into a load failure for a reopened store.
func TestSQLiteStoreSaveAndPlainSupersedePreserveSupersessionFact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
	store := newSupersedeStoreAt(t, path)
	predecessor, successor := supersessionFixtureSavedPair(t, store)
	fact := supersessionFactFixture(t, predecessor, successor)
	mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

	unrelated := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
	unrelatedSuccessor := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
	mustSaveSupersedeArtifacts(t, store, unrelated, unrelatedSuccessor)

	ctx := intentTestContext(t)
	updated := predecessor
	// The edge is already established in the row, so an in-place body update has to carry it:
	// clearing it here would be an ordinary Save trying to undo a Supersede.
	updated.SupersededBy = successor.ArtifactID
	updated.Content = updatedIntentBody
	updated.ContentHash = modulecore.ContentHashOf([]byte(updatedIntentBody))
	updated.Title = "Updated After Supersession"
	if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
		t.Fatalf("updated fixture is invalid before any owner operation: %v", err)
	}
	if err := store.SaveAPIArtifact(ctx, updated); err != nil {
		t.Fatalf("SaveAPIArtifact(%s) body update error = %v", updated.ArtifactID, err)
	}
	if err := store.SupersedeAPIArtifact(ctx, unrelated.ArtifactID, unrelatedSuccessor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", unrelated.ArtifactID, unrelatedSuccessor.ArtifactID, err)
	}

	if got := snapshotSupersessionIntentRows(t, store); len(got) != 1 || !strings.HasPrefix(got[0], string(predecessor.ArtifactID)+"\x00"+string(fact.EventID)+"\x00") {
		t.Errorf("%s rows after an update and an unrelated supersede = %q, want the one untouched fact", supersessionIntentTable, got)
	}
	// The digest the fact names is history: the row now carries the updated body.
	if got := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID); got.ContentHash != updated.ContentHash {
		t.Errorf("predecessor row digest = %q, want the updated %q", got.ContentHash, updated.ContentHash)
	}

	reopened := newSupersedeStoreAt(t, path)
	drained := mustDrainSupersessionFacts(t, reopened, 10)
	if len(drained) != 1 || !reflect.DeepEqual(drained[0], fact) {
		t.Fatalf("facts after reopen = %d %v, want exactly %s unchanged by the later update", len(drained), intentIDs(drained), fact.EventID)
	}
	// The fact still describes the edge, so the ordinary retry path is still a no-op.
	mustSupersedeWithFact(t, reopened, predecessor.ArtifactID, successor.ArtifactID, fact)
	if got := snapshotSupersessionIntentRows(t, reopened); len(got) != 1 {
		t.Errorf("%s rows after a retry on a reopened store = %q, want still 1", supersessionIntentTable, got)
	}
}

// TestSQLiteStoreListAPIArtifactSupersessionFactsFailsClosedOnBrokenFact refuses to hand a
// replay a fact that resolves to nothing, disagrees with its own index column, or whose edge
// is gone. Each broken row is written straight into the database as a corruption injection,
// not through an owner method.
func TestSQLiteStoreListAPIArtifactSupersessionFactsFailsClosedOnBrokenFact(t *testing.T) {
	t.Run("fact bound to a missing predecessor row", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

		// Corruption injection: drop the predecessor the way an interrupted writer would.
		if _, err := store.db.ExecContext(intentTestContext(t), `DELETE FROM api_artifact WHERE artifact_id = ?`, string(predecessor.ArtifactID)); err != nil {
			t.Fatalf("fixture delete: %v", err)
		}
		_, err := store.ListAPIArtifactSupersessionFacts(intentTestContext(t), "", 10)
		if err == nil || !strings.Contains(err.Error(), "is not in api_artifact") {
			t.Errorf("list over an orphan fact = %v, want the missing-row report", err)
		}
	})

	t.Run("successor row gone", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

		if _, err := store.db.ExecContext(intentTestContext(t), `DELETE FROM api_artifact WHERE artifact_id = ?`, string(successor.ArtifactID)); err != nil {
			t.Fatalf("fixture delete of the successor: %v", err)
		}
		_, err := store.ListAPIArtifactSupersessionFacts(intentTestContext(t), "", 10)
		if err == nil || !strings.Contains(err.Error(), "is not in api_artifact") {
			t.Errorf("list over a fact whose successor is gone = %v, want the missing-row report", err)
		}
	})

	t.Run("index column disagrees with the payload", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

		result, err := store.db.ExecContext(intentTestContext(t),
			`UPDATE `+supersessionIntentTable+` SET event_id = ? WHERE artifact_id = ?`,
			string(modulecore.NewEventID()), string(predecessor.ArtifactID))
		if err != nil {
			t.Fatalf("fixture update of the event_id column: %v", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			t.Fatalf("fixture update affected %d rows, want 1", affected)
		}
		_, err = store.ListAPIArtifactSupersessionFacts(intentTestContext(t), "", 10)
		if err == nil || !strings.Contains(err.Error(), "event_id column") {
			t.Errorf("list over a mismatched event_id column = %v, want the column disagreement report", err)
		}
	})

	t.Run("stored edge is no longer the edge the fact names", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

		// Corruption injection: clear the edge, which no owner operation may do.
		cleared := predecessor
		cleared.SupersededBy = ""
		payload, err := json.Marshal(cleared)
		if err != nil {
			t.Fatalf("marshal cleared fixture: %v", err)
		}
		if _, err := store.db.ExecContext(intentTestContext(t), `UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), string(predecessor.ArtifactID)); err != nil {
			t.Fatalf("fixture update of the artifact payload: %v", err)
		}
		_, err = store.ListAPIArtifactSupersessionFacts(intentTestContext(t), "", 10)
		if err == nil || !strings.Contains(err.Error(), "stores superseded_by") {
			t.Errorf("list over a cleared edge = %v, want the edge mismatch report", err)
		}
	})

	t.Run("predecessor identity moved", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

		moved := predecessor
		moved.TaskID = "tsk_00000000-0000-5000-8000-000000000009"
		if err := domaintrace.ValidateAPIArtifact(moved); err != nil {
			t.Fatalf("moved fixture is invalid: %v", err)
		}
		payload, err := json.Marshal(moved)
		if err != nil {
			t.Fatalf("marshal moved fixture: %v", err)
		}
		if _, err := store.db.ExecContext(intentTestContext(t), `UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), string(predecessor.ArtifactID)); err != nil {
			t.Fatalf("fixture update of the artifact payload: %v", err)
		}
		_, err = store.ListAPIArtifactSupersessionFacts(intentTestContext(t), "", 10)
		if err == nil || !strings.Contains(err.Error(), "task_id") {
			t.Errorf("list over a row whose task moved = %v, want the immutable-reference report", err)
		}
	})

	t.Run("payload corrupt", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

		if _, err := store.db.ExecContext(intentTestContext(t),
			`UPDATE `+supersessionIntentTable+` SET payload = ? WHERE artifact_id = ?`,
			`{"event_id":"not-an-event-id"}`, string(predecessor.ArtifactID)); err != nil {
			t.Fatalf("fixture update of the fact payload: %v", err)
		}
		_, err := store.ListAPIArtifactSupersessionFacts(intentTestContext(t), "", 10)
		if err == nil {
			t.Fatalf("list over a corrupt fact payload returned nil, want a fail-closed error")
		}
		if !strings.Contains(err.Error(), "supersession fact") || !strings.Contains(err.Error(), "schema_version") {
			t.Errorf("corrupt payload error = %v, want the report naming the unusable fact and its reason", err)
		}
	})

	t.Run("payload is not decodable", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor, successor := supersessionFixtureSavedPair(t, store)
		fact := supersessionFactFixture(t, predecessor, successor)
		mustSupersedeWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

		// Corruption injection: the bytes in the payload column are not JSON at all.
		if _, err := store.db.ExecContext(intentTestContext(t),
			`UPDATE `+supersessionIntentTable+` SET payload = ? WHERE artifact_id = ?`,
			`{"event_id":`, string(predecessor.ArtifactID)); err != nil {
			t.Fatalf("fixture update of the fact payload: %v", err)
		}
		_, err := store.ListAPIArtifactSupersessionFacts(intentTestContext(t), "", 10)
		if err == nil {
			t.Fatalf("list over undecodable fact bytes returned nil, want a fail-closed error")
		}
		if !strings.Contains(err.Error(), supersessionIntentTable) || !strings.Contains(err.Error(), "corrupt") {
			t.Errorf("undecodable payload error = %v, want the report naming %s and its corrupt payload", err, supersessionIntentTable)
		}
	})
}
