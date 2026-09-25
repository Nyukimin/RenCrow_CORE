package browsertrace

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// These tests cover the JSONL owner of the supersession fact: the edge and the canonical
// supersession envelope have to reach api_artifact.jsonl and
// api_artifact_supersession.jsonl through one batch WAL write, so an interruption leaves
// either both records or neither. The outcomes mirror the accepted SQLite semantics.

// jsonlSupersessionFixturePair saves two valid edge-free artifacts that share one canonical
// workstream, which is the state a supersede is allowed to establish an edge between.
func jsonlSupersessionFixturePair(t *testing.T, store *JSONLStore) (domaintrace.APIArtifact, domaintrace.APIArtifact) {
	t.Helper()
	workstreamID := intentFixtureWorkstreamID(t)
	predecessor := intentFixtureArtifact(t, workstreamID)
	successor := intentFixtureArtifact(t, workstreamID)
	mustSaveJSONLArtifacts(t, store, predecessor, successor)
	return predecessor, successor
}

// jsonlArtifactRows returns the rows a reader resolves right now, keyed by artifact id, so
// "nothing moved" is a whole-row comparison rather than a count.
func jsonlArtifactRows(t *testing.T, store *JSONLStore) map[modulecore.ArtifactID]domaintrace.APIArtifact {
	t.Helper()
	items, err := store.ListAPIArtifacts(intentTestContext(t), 1000)
	if err != nil {
		t.Fatalf("ListAPIArtifacts error = %v", err)
	}
	rows := make(map[modulecore.ArtifactID]domaintrace.APIArtifact, len(items))
	for _, item := range items {
		rows[item.ArtifactID] = item
	}
	return rows
}

// mustSupersedeJSONLWithFact runs the owner supersede that also persists the fact.
func mustSupersedeJSONLWithFact(t *testing.T, store *JSONLStore, predecessorID, successorID modulecore.ArtifactID, fact modulecore.EventEnvelope) {
	t.Helper()
	if err := store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessorID, successorID, fact); err != nil {
		t.Fatalf("SupersedeAPIArtifactWithPublicationIntent(%s -> %s) error = %v", predecessorID, successorID, err)
	}
}

// mustDrainJSONLSupersessionFacts walks every page with the given page size and concatenates
// the result, which is how a caller proves nothing older was dropped.
func mustDrainJSONLSupersessionFacts(t *testing.T, store *JSONLStore, pageSize int) []modulecore.EventEnvelope {
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

// appendJSONLFileBytes appends raw bytes to one store file without going through an owner
// method. It is a corruption injection used to prove the reader fails closed, not an owner
// operation and not runtime evidence.
func appendJSONLFileBytes(t *testing.T, root, filename string, raw []byte) {
	t.Helper()
	path := filepath.Join(root, filename)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	if err := os.WriteFile(path, append(before, raw...), 0o644); err != nil {
		t.Fatalf("inject into %s: %v", filename, err)
	}
}

// writeJSONLFileBytes replaces one store file. As above, this is corruption injection: the
// owner methods cannot produce an undecodable or duplicated fact record in the first place.
func writeJSONLFileBytes(t *testing.T, root, filename string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filename), raw, 0o644); err != nil {
		t.Fatalf("inject %s: %v", filename, err)
	}
}

func mustMarshalFactRecord(t *testing.T, fact modulecore.EventEnvelope) []byte {
	t.Helper()
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatalf("marshal supersession fact %s: %v", fact.EventID, err)
	}
	return append(raw, '\n')
}

// jsonlFactRecordsForTest returns the supersession fact records that are physically on disk,
// one non-blank line each, so a test can state that a write appended exactly one record and a
// refusal appended none, next to what the owner's own list path reports.
func jsonlFactRecordsForTest(t *testing.T, root string) []string {
	t.Helper()
	return readStoreFileLines(t, root, supersessionIntentFilename)
}

// factRecordWith returns a copy of the fact whose payload predecessor digest is replaced, so
// a refusal can only come from the digest the fact claims rather than from a malformed
// envelope.
func factRecordWithDigests(t *testing.T, fact modulecore.EventEnvelope, predecessorHash, successorHash string) modulecore.EventEnvelope {
	t.Helper()
	out := copyAPIArtifactPublicationIntent(fact)
	if predecessorHash != "" {
		out.Payload[domaintrace.APIArtifactSupersessionPayloadPredecessorContentHash] = predecessorHash
	}
	if successorHash != "" {
		out.Payload[domaintrace.APIArtifactSupersessionPayloadSuccessorContentHash] = successorHash
	}
	if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(out); err != nil {
		t.Fatalf("digest-mutated supersession fact %s fails the persisted-fact contract: %v", out.EventID, err)
	}
	return out
}

// TestJSONLStoreSupersedeAPIArtifactWithPublicationIntentCoPersistsEdgeAndFact pins the
// write boundary: the updated predecessor record and the typed supersession envelope are
// handed to one batch write, and a reopened store resolves both rows and the same fact.
func TestJSONLStoreSupersedeAPIArtifactWithPublicationIntentCoPersistsEdgeAndFact(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore(%s) error = %v", root, err)
	}
	predecessor, successor := jsonlSupersessionFixturePair(t, store)
	fact := supersessionFactFixture(t, predecessor, successor)
	if got := jsonlFactRecordsForTest(t, root); len(got) != 0 {
		t.Fatalf("a fresh store reports %d stored supersession facts: %v", len(got), got)
	}

	mustSupersedeJSONLWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

	records := jsonlFactRecordsForTest(t, root)
	if len(records) != 1 {
		t.Fatalf("supersession fact file holds %d records, want exactly 1: %v", len(records), records)
	}
	var stored modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(records[0]), &stored); err != nil {
		t.Fatalf("decode stored supersession fact: %v", err)
	}
	if !reflect.DeepEqual(stored, fact) {
		t.Fatalf("stored supersession fact = %+v, want the handed-in envelope %+v", stored, fact)
	}

	rows := jsonlArtifactRows(t, store)
	wantPredecessor := predecessor
	wantPredecessor.SupersededBy = successor.ArtifactID
	if got := rows[predecessor.ArtifactID]; !reflect.DeepEqual(got, wantPredecessor) {
		t.Fatalf("predecessor row = %+v, want %+v", got, wantPredecessor)
	}
	if got := rows[successor.ArtifactID]; !reflect.DeepEqual(got, successor) {
		t.Fatalf("successor row = %+v, want the stored row unchanged %+v", got, successor)
	}

	reopened, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore after the supersede error = %v", err)
	}
	if got := mustDrainJSONLSupersessionFacts(t, reopened, 0); len(got) != 1 || !reflect.DeepEqual(got[0], fact) {
		t.Fatalf("reopened store drained %+v, want exactly the stored fact %+v", got, fact)
	}
	if got := jsonlArtifactRows(t, reopened)[predecessor.ArtifactID]; !reflect.DeepEqual(got, wantPredecessor) {
		t.Fatalf("reopened predecessor row = %+v, want %+v", got, wantPredecessor)
	}
}

// TestJSONLStoreSupersedeAPIArtifactWithPublicationIntentRetryAppendsNothing pins that a
// rerun of the same edge with the same stored envelope adds no record to either file, which
// is what keeps an unconfirmed commit from becoming a second event for one supersession.
func TestJSONLStoreSupersedeAPIArtifactWithPublicationIntentRetryAppendsNothing(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore(%s) error = %v", root, err)
	}
	predecessor, successor := jsonlSupersessionFixturePair(t, store)
	fact := supersessionFactFixture(t, predecessor, successor)
	mustSupersedeJSONLWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

	factsBefore := readStoreFileBytes(t, root, supersessionIntentFilename)
	artifactsBefore := readStoreFileBytes(t, root, artifactFilename)
	rowsBefore := jsonlArtifactRows(t, store)

	mustSupersedeJSONLWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)
	mustSupersedeJSONLWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

	if got := readStoreFileBytes(t, root, supersessionIntentFilename); !bytes.Equal(got, factsBefore) {
		t.Fatalf("retrying the same supersession fact changed %s from %q to %q", supersessionIntentFilename, factsBefore, got)
	}
	if got := readStoreFileBytes(t, root, artifactFilename); !bytes.Equal(got, artifactsBefore) {
		t.Fatalf("retrying the same supersession fact changed %s bytes", artifactFilename)
	}
	if got := jsonlArtifactRows(t, store); !reflect.DeepEqual(got, rowsBefore) {
		t.Fatalf("retrying the same supersession fact changed rows: before %+v after %+v", rowsBefore, got)
	}
	if got := mustDrainJSONLSupersessionFacts(t, store, 0); len(got) != 1 || !reflect.DeepEqual(got[0], fact) {
		t.Fatalf("store drained %+v after retries, want exactly one stored fact %+v", got, fact)
	}
}

// TestJSONLStoreSupersedeAPIArtifactWithPublicationIntentRefusesEverySecondFact pins the four
// ways a caller can ask for a second supersession event, and that each refusal appends nothing
// to either file. Each case sets up its own state and the snapshot is taken after that setup,
// so "appended nothing" is a raw-byte comparison of the state the setup left behind, which is
// the same harness shape the accepted SQLite fact-table test uses. The reported refusals are
// the accepted SQLite outcomes: a stored fact is what owns one supersession, so a second event
// id is refused as a second event before the stored edge is considered.
func TestJSONLStoreSupersedeAPIArtifactWithPublicationIntentRefusesEverySecondFact(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact)
		fact  func(t *testing.T, store *JSONLStore, root string, predecessor, successor, third domaintrace.APIArtifact) modulecore.EventEnvelope
		want  string
	}{
		{
			name: "a second event id for one supersession",
			setup: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact) {
				mustSupersedeJSONLWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, supersessionFactFixture(t, predecessor, successor))
			},
			fact: func(t *testing.T, store *JSONLStore, root string, predecessor, successor, third domaintrace.APIArtifact) modulecore.EventEnvelope {
				return supersessionFactFixture(t, predecessor, successor)
			},
			want: "would be a second event for one supersession",
		},
		{
			name: "an event id another predecessor already owns",
			setup: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact) {
				// The other predecessor has to live in the predecessor's scope, otherwise the
				// scope guard refuses it and the shared event id is never reached.
				other := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
				mustSaveJSONLArtifacts(t, store, other)
				mustSupersedeJSONLWithFact(t, store, other.ArtifactID, successor.ArtifactID, supersessionFactFixture(t, other, successor))
			},
			fact: func(t *testing.T, store *JSONLStore, root string, predecessor, successor, third domaintrace.APIArtifact) modulecore.EventEnvelope {
				stored := mustDrainJSONLSupersessionFacts(t, store, 0)
				if len(stored) != 1 {
					t.Fatalf("setup stored %d supersession facts, want the one fact of the other predecessor", len(stored))
				}
				// Same event id, but issued for this predecessor and its own digests.
				claimed := supersessionFactFixture(t, predecessor, third)
				claimed.EventID = stored[0].EventID
				return claimed
			},
			want: "cannot claim the same event",
		},
		{
			name: "an edge already established without a fact",
			setup: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact) {
				if err := store.SupersedeAPIArtifact(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID); err != nil {
					t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
				}
			},
			fact: func(t *testing.T, store *JSONLStore, root string, predecessor, successor, third domaintrace.APIArtifact) modulecore.EventEnvelope {
				return supersessionFactFixture(t, predecessor, successor)
			},
			want: "predates SupersedeAPIArtifactWithPublicationIntent",
		},
		{
			name: "a predecessor already superseded by another successor",
			setup: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact) {
				mustSupersedeJSONLWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, supersessionFactFixture(t, predecessor, successor))
			},
			fact: func(t *testing.T, store *JSONLStore, root string, predecessor, successor, third domaintrace.APIArtifact) modulecore.EventEnvelope {
				// The first fact is stored, so a fact naming another successor is refused as a
				// second event for one supersession before the stored edge is considered.
				return supersessionFactFixture(t, predecessor, third)
			},
			want: "would be a second event for one supersession",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewJSONLStore(root)
			if err != nil {
				t.Fatalf("NewJSONLStore(%s) error = %v", root, err)
			}
			predecessor, successor := jsonlSupersessionFixturePair(t, store)
			third := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
			mustSaveJSONLArtifacts(t, store, third)
			tc.setup(t, store, root, predecessor, successor)

			fact := tc.fact(t, store, root, predecessor, successor, third)
			factsBefore := readStoreFileBytes(t, root, supersessionIntentFilename)
			artifactsBefore := readStoreFileBytes(t, root, artifactFilename)
			rowsBefore := jsonlArtifactRows(t, store)

			err = store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID, fact)
			if err == nil {
				t.Fatalf("supersede %s -> %s with fact %s returned nil, want a refusal", predecessor.ArtifactID, successor.ArtifactID, fact.EventID)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %v, want it to report %q", err, tc.want)
			}
			if got := readStoreFileBytes(t, root, supersessionIntentFilename); !bytes.Equal(got, factsBefore) {
				t.Errorf("refused supersession changed %s from %q to %q", supersessionIntentFilename, factsBefore, got)
			}
			if got := readStoreFileBytes(t, root, artifactFilename); !bytes.Equal(got, artifactsBefore) {
				t.Errorf("refused supersession changed %s bytes", artifactFilename)
			}
			if got := jsonlArtifactRows(t, store); !reflect.DeepEqual(got, rowsBefore) {
				t.Errorf("refused supersession changed rows: before %+v after %+v", rowsBefore, got)
			}
			if _, err := NewJSONLStore(root); err != nil {
				t.Errorf("NewJSONLStore after the refusal error = %v; the refusal must not damage stored history", err)
			}
		})
	}
}

// TestJSONLStoreSupersedeAPIArtifactWithPublicationIntentRefusesBrokenPairs pins that the
// fact is only minted for an edge whose two rows exist and whose digests are the bytes being
// superseded, with no record appended when they are not.
func TestJSONLStoreSupersedeAPIArtifactWithPublicationIntentRefusesBrokenPairs(t *testing.T) {
	foreignDigest := modulecore.ContentHashOf([]byte("bytes no stored row holds"))
	tests := []struct {
		name            string
		omitSuccessor   bool
		omitPredecessor bool
		mutate          func(t *testing.T, fact modulecore.EventEnvelope, predecessor, successor domaintrace.APIArtifact) modulecore.EventEnvelope
		want            string
	}{
		{name: "predecessor row missing", omitPredecessor: true, want: "is not in api_artifact.jsonl"},
		{name: "successor row missing", omitSuccessor: true, want: "is not in api_artifact.jsonl"},
		{
			name: "digest is not the superseded bytes",
			mutate: func(t *testing.T, fact modulecore.EventEnvelope, predecessor, successor domaintrace.APIArtifact) modulecore.EventEnvelope {
				return factRecordWithDigests(t, fact, foreignDigest, "")
			},
			want: "payload predecessor_content_hash",
		},
		{
			name: "successor digest is foreign",
			mutate: func(t *testing.T, fact modulecore.EventEnvelope, predecessor, successor domaintrace.APIArtifact) modulecore.EventEnvelope {
				return factRecordWithDigests(t, fact, "", foreignDigest)
			},
			want: "payload successor_content_hash",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewJSONLStore(root)
			if err != nil {
				t.Fatalf("NewJSONLStore(%s) error = %v", root, err)
			}
			predecessor, successor := jsonlSupersessionFixturePair(t, store)
			fact := supersessionFactFixture(t, predecessor, successor)
			if tc.mutate != nil {
				fact = tc.mutate(t, fact, predecessor, successor)
			}
			if tc.omitPredecessor || tc.omitSuccessor {
				// Dropping a stored row is a corruption injection: append-only files cannot
				// delete a record, so the row the fact names simply never arrives.
				root := t.TempDir()
				store, err = NewJSONLStore(root)
				if err != nil {
					t.Fatalf("NewJSONLStore(%s) error = %v", root, err)
				}
				if tc.omitPredecessor {
					mustSaveJSONLArtifacts(t, store, successor)
				} else {
					mustSaveJSONLArtifacts(t, store, predecessor)
				}
			}

			artifactsBefore := readStoreFileBytes(t, root, artifactFilename)
			factsBefore := readStoreFileBytes(t, root, supersessionIntentFilename)

			err = store.SupersedeAPIArtifactWithPublicationIntent(intentTestContext(t), predecessor.ArtifactID, successor.ArtifactID, fact)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("supersede of a broken pair error = %v, want it to report %q", err, tc.want)
			}
			if got := readStoreFileBytes(t, root, supersessionIntentFilename); !bytes.Equal(got, factsBefore) {
				t.Fatalf("refused supersede appended to %s: before %q after %q", supersessionIntentFilename, factsBefore, got)
			}
			if got := readStoreFileBytes(t, root, artifactFilename); !bytes.Equal(got, artifactsBefore) {
				t.Fatalf("refused supersede appended to %s", artifactFilename)
			}
			if rows := jsonlArtifactRows(t, store); len(rows) == 0 {
				t.Fatalf("store reports no artifact rows at all after the refusal")
			} else {
				for id, row := range rows {
					if row.SupersededBy != "" {
						t.Fatalf("refused supersede left artifact %s with superseded_by %s", id, row.SupersededBy)
					}
				}
			}
		})
	}
}

// jsonlArtifactRowsSorted returns the rows a reader resolves right now, sorted by artifact id
// and minus the ids given, which is what a corruption injection needs to rewrite
// api_artifact.jsonl while dropping a row.
func jsonlArtifactRowsSorted(t *testing.T, store *JSONLStore, drop ...modulecore.ArtifactID) []domaintrace.APIArtifact {
	t.Helper()
	skip := make(map[modulecore.ArtifactID]struct{}, len(drop))
	for _, id := range drop {
		skip[id] = struct{}{}
	}
	rows := make([]domaintrace.APIArtifact, 0, len(jsonlArtifactRows(t, store)))
	for id, item := range jsonlArtifactRows(t, store) {
		if _, found := skip[id]; found {
			continue
		}
		rows = append(rows, item)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ArtifactID < rows[j].ArtifactID })
	return rows
}

// rewriteJSONLArtifactRows replaces api_artifact.jsonl with exactly the given rows. An
// append-only owner file cannot delete or edit a row, so this writes the state an interrupted
// or externally edited writer leaves: corruption injection, not an owner operation and not
// runtime evidence.
func rewriteJSONLArtifactRows(t *testing.T, root string, rows []domaintrace.APIArtifact) {
	t.Helper()
	var buf bytes.Buffer
	for _, item := range rows {
		record, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal injected artifact row %s: %v", item.ArtifactID, err)
		}
		buf.Write(record)
		buf.WriteByte('\n')
	}
	writeJSONLFileBytes(t, root, artifactFilename, buf.Bytes())
}

// injectJSONLArtifactRowReplace replaces one stored artifact row with the given row in
// api_artifact.jsonl, leaving the other rows byte-identical. As above this is a corruption
// injection: no owner method edits a stored row outside the guarded update paths.
func injectJSONLArtifactRowReplace(t *testing.T, store *JSONLStore, root string, item domaintrace.APIArtifact) {
	t.Helper()
	rows := jsonlArtifactRowsSorted(t, store)
	for i := range rows {
		if rows[i].ArtifactID == item.ArtifactID {
			rows[i] = item
			rewriteJSONLArtifactRows(t, root, rows)
			return
		}
	}
	t.Fatalf("artifact %s has no stored row to replace", item.ArtifactID)
}

// TestJSONLStoreListAPIArtifactSupersessionFactsDrainsEveryPage proves the page limit bounds
// one call only: repeated pages return every stored fact in ascending predecessor id order,
// with no duplicates and no cutoff of older history, and a malformed cursor is refused.
func TestJSONLStoreListAPIArtifactSupersessionFactsDrainsEveryPage(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore(%s) error = %v", root, err)
	}
	pairs := make([][2]domaintrace.APIArtifact, 0, 5)
	for range 5 {
		first, second := jsonlSupersessionFixturePair(t, store)
		pairs = append(pairs, [2]domaintrace.APIArtifact{first, second})
	}
	wantIDs := make([]string, 0, len(pairs))
	for i, pair := range pairs {
		if i == 2 {
			// One edge established without a fact: it is not a fact to drain, and the drain
			// must not fail closed on it while the plain Supersede caller is not switched over.
			if err := store.SupersedeAPIArtifact(intentTestContext(t), pair[0].ArtifactID, pair[1].ArtifactID); err != nil {
				t.Fatalf("plain SupersedeAPIArtifact(%s -> %s) error = %v", pair[0].ArtifactID, pair[1].ArtifactID, err)
			}
			continue
		}
		mustSupersedeJSONLWithFact(t, store, pair[0].ArtifactID, pair[1].ArtifactID, supersessionFactFixture(t, pair[0], pair[1]))
		wantIDs = append(wantIDs, string(pair[0].ArtifactID))
	}
	sort.Strings(wantIDs)

	for _, pageSize := range []int{1, 2, 3, 0, 5_000} {
		drained := mustDrainJSONLSupersessionFacts(t, store, pageSize)
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

// TestJSONLStoreSaveAndPlainSupersedePreserveSupersessionFact keeps the operations that
// legitimately change an artifact away from a stored supersession fact: an in-place body
// update replaces the digest the fact recorded as history, and a plain supersede of another
// pair writes only api_artifact.jsonl. Neither may turn the stored fact into a load failure,
// and the fact still has to make the retry path a no-op after a reopen.
func TestJSONLStoreSaveAndPlainSupersedePreserveSupersessionFact(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore(%s) error = %v", root, err)
	}
	predecessor, successor := jsonlSupersessionFixturePair(t, store)
	fact := supersessionFactFixture(t, predecessor, successor)
	mustSupersedeJSONLWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)
	factsBefore := readStoreFileBytes(t, root, supersessionIntentFilename)

	unrelated := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
	unrelatedSuccessor := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
	mustSaveJSONLArtifacts(t, store, unrelated, unrelatedSuccessor)

	ctx := intentTestContext(t)
	updated := predecessor
	// The row already carries the edge, so an in-place body update has to carry it: clearing
	// it here would be an ordinary Save trying to undo a Supersede.
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

	if got := readStoreFileBytes(t, root, supersessionIntentFilename); !bytes.Equal(got, factsBefore) {
		t.Errorf("%s changed across a body update and an unrelated supersede: before %q after %q", supersessionIntentFilename, factsBefore, got)
	}
	// The digest the fact names is history: the row now carries the updated body.
	if got := jsonlArtifactRows(t, store)[predecessor.ArtifactID]; got.ContentHash != updated.ContentHash {
		t.Errorf("predecessor row digest = %q, want the updated %q", got.ContentHash, updated.ContentHash)
	}

	reopened, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore after the update error = %v; the stored fact must stay loadable", err)
	}
	drained := mustDrainJSONLSupersessionFacts(t, reopened, 10)
	if len(drained) != 1 || !reflect.DeepEqual(drained[0], fact) {
		t.Fatalf("facts after reopen = %d %v, want exactly %s unchanged by the later update", len(drained), intentIDs(drained), fact.EventID)
	}
	// The fact still describes the edge, so the ordinary retry path is still a no-op.
	mustSupersedeJSONLWithFact(t, reopened, predecessor.ArtifactID, successor.ArtifactID, fact)
	if got := readStoreFileBytes(t, root, supersessionIntentFilename); !bytes.Equal(got, factsBefore) {
		t.Errorf("retry on a reopened store changed %s from %q to %q", supersessionIntentFilename, factsBefore, got)
	}
}

// TestJSONLStoreListAPIArtifactSupersessionFactsFailsClosedOnBrokenFact refuses to hand a
// replay a fact that resolves to a missing row, disagrees with the edge it names, duplicates
// an event id, or cannot be decoded. Every broken record is written straight into the store
// files as a corruption injection, not through an owner method, and the constructor has to
// refuse the same state a later read refuses.
func TestJSONLStoreListAPIArtifactSupersessionFactsFailsClosedOnBrokenFact(t *testing.T) {
	foreignDigest := modulecore.ContentHashOf([]byte("bytes no stored row holds"))
	tests := []struct {
		name   string
		inject func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope)
		want   []string
	}{
		{
			name: "fact bound to a missing predecessor row",
			inject: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope) {
				rewriteJSONLArtifactRows(t, root, jsonlArtifactRowsSorted(t, store, predecessor.ArtifactID))
			},
			want: []string{"is bound to artifact", "has no record in " + artifactFilename},
		},
		{
			name: "successor row gone",
			inject: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope) {
				rewriteJSONLArtifactRows(t, root, jsonlArtifactRowsSorted(t, store, successor.ArtifactID))
			},
			want: []string{"points at successor artifact", "has no record in " + artifactFilename},
		},
		{
			name: "stored edge is no longer the edge the fact names",
			inject: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope) {
				cleared := predecessor
				cleared.SupersededBy = ""
				injectJSONLArtifactRowReplace(t, store, root, cleared)
			},
			want: []string{"stores superseded_by"},
		},
		{
			name: "predecessor identity moved",
			inject: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope) {
				moved := predecessor
				moved.TaskID = "tsk_00000000-0000-5000-8000-000000000009"
				if err := domaintrace.ValidateAPIArtifact(moved); err != nil {
					t.Fatalf("moved fixture is invalid: %v", err)
				}
				injectJSONLArtifactRowReplace(t, store, root, moved)
			},
			want: []string{"has task_id"},
		},
		{
			name: "a second fact record for one predecessor",
			inject: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope) {
				// The duplicated record is individually valid and names other bytes, so the
				// only reason it cannot be history is the fact already stored here.
				appendJSONLFileBytes(t, root, supersessionIntentFilename, mustMarshalFactRecord(t, factRecordWithDigests(t, fact, foreignDigest, "")))
			},
			want: []string{"conflicts with the stored fact"},
		},
		{
			name: "one event id bound to two predecessors",
			inject: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope) {
				other := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
				mustSaveJSONLArtifacts(t, store, other)
				claim := copyAPIArtifactPublicationIntent(fact)
				claim.ArtifactID = other.ArtifactID
				appendJSONLFileBytes(t, root, supersessionIntentFilename, mustMarshalFactRecord(t, claim))
			},
			want: []string{"is bound to both artifact"},
		},
		{
			name: "payload corrupt",
			inject: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope) {
				writeJSONLFileBytes(t, root, supersessionIntentFilename, append([]byte(`{"event_id":"not-an-event-id"}`), '\n'))
			},
			want: []string{supersessionIntentFilename, "line 1", "schema_version"},
		},
		{
			name: "payload is not decodable",
			inject: func(t *testing.T, store *JSONLStore, root string, predecessor, successor domaintrace.APIArtifact, fact modulecore.EventEnvelope) {
				writeJSONLFileBytes(t, root, supersessionIntentFilename, append([]byte(`{"event_id":`), '\n'))
			},
			want: []string{supersessionIntentFilename, "line 1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewJSONLStore(root)
			if err != nil {
				t.Fatalf("NewJSONLStore(%s) error = %v", root, err)
			}
			predecessor, successor := jsonlSupersessionFixturePair(t, store)
			fact := supersessionFactFixture(t, predecessor, successor)
			mustSupersedeJSONLWithFact(t, store, predecessor.ArtifactID, successor.ArtifactID, fact)

			tc.inject(t, store, root, predecessor, successor, fact)

			_, err = store.ListAPIArtifactSupersessionFacts(intentTestContext(t), "", 10)
			if err == nil {
				t.Fatalf("list over an injected break returned nil, want a fail-closed error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("list over an injected break = %v, want it to report %q", err, want)
				}
			}
			// Reading a broken fact file must not repair it: a later store construction
			// refuses the same state instead of building on it.
			if _, err := NewJSONLStore(root); err == nil {
				t.Errorf("NewJSONLStore accepted the injected break, want the constructor to fail closed too")
			}
		})
	}
}
