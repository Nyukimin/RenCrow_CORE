package browsertrace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Publication intent fixtures. The accepted supersede fixtures stay untouched, but
// their workstream_id ("ws_1") is only a browsertrace string boundary and is not a
// canonical core.EventEnvelope workstream id, so intent tests mint one and use the
// same value in the artifact row and the envelope. Both fixtures are prevalidated
// here so a malformed fixture cannot be mistaken for an intent contract failure.
const intentFixtureContent = "openapi: 3.1.0\ninfo:\n  title: publication intent fixture\n"

// updatedIntentBody is a second valid body used to exercise the owner's ordinary in-place
// update, whose digest and title differ from the created bytes that the creation intent
// still has to report.
const updatedIntentBody = "openapi: 3.1.0\ninfo:\n  title: publication intent body update\n"

// intentFixtureComponentID is the stable documented browsertrace component id that a
// creation envelope has to carry.
const intentFixtureComponentID = "browsertrace"

// intentFixtureActorKind is the provenance the discovery route records for an
// authenticated core actor, so an intent that leaves it empty does not carry the
// actor evidence publication replay depends on.
const intentFixtureActorKind = "agent"

func intentFixtureArtifact(t *testing.T, workstreamID modulecore.WorkstreamID) domaintrace.APIArtifact {
	t.Helper()
	item := domaintrace.APIArtifact{
		ArtifactID:   modulecore.NewArtifactID(),
		Kind:         modulecore.ArtifactKindSpecification,
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001",
		RunID:        "run_00000000-0000-5000-8000-000000000002",
		ActorID:      "mio",
		WorkstreamID: string(workstreamID),
		Type:         domaintrace.APIArtifactTypeObservedOpenAPI,
		Title:        "Observed OpenAPI Draft",
		Status:       "generated",
		Content:      intentFixtureContent,
		ContentHash:  modulecore.ContentHashOf([]byte(intentFixtureContent)),
		CreatedAt:    time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC),
	}
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		t.Fatalf("intent fixture artifact %s is invalid before any owner operation: %v", item.ArtifactID, err)
	}
	return item
}

func intentFixtureEnvelope(t *testing.T, item domaintrace.APIArtifact) modulecore.EventEnvelope {
	t.Helper()
	envelope := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		EventSeq:      0,
		TraceID:       modulecore.NewTraceID(),
		EventType:     domaintrace.APIArtifactCreatedEventType,
		ComponentID:   intentFixtureComponentID,
		OccurredAt:    item.CreatedAt,
		WorkstreamID:  modulecore.WorkstreamID(item.WorkstreamID),
		TaskID:        item.TaskID,
		RunID:         item.RunID,
		ActorID:       item.ActorID,
		ActorKind:     intentFixtureActorKind,
		ArtifactID:    item.ArtifactID,
		Payload: map[string]any{
			"artifact_kind": string(item.Kind),
			"artifact_type": item.Type,
			"content_hash":  item.ContentHash,
		},
	}
	if err := modulecore.ValidateEventEnvelope(envelope); err != nil {
		t.Fatalf("intent fixture envelope %s is invalid before any owner operation: %v", envelope.EventID, err)
	}
	return envelope
}

func intentFixtureWorkstreamID(t *testing.T) modulecore.WorkstreamID {
	t.Helper()
	workstreamID := modulecore.NewWorkstreamID()
	if err := workstreamID.Validate(); err != nil {
		t.Fatalf("intent fixture workstream id %s is invalid: %v", workstreamID, err)
	}
	return workstreamID
}

func readStoreFileLines(t *testing.T, root, filename string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filename))
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		t.Fatalf("%s ends inside a record: %q", filename, raw[max(0, len(raw)-40):])
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// TestJSONLStoreCreateAPIArtifactWithPublicationIntentCoPersistsBothRecords pins the
// creation boundary: a newly created APIArtifact and the canonical event envelope that
// publication will replay are stored together, in their own files, so a crash after the
// append leaves either both records or neither. The artifact file keeps only artifact
// records and the intent file keeps only the typed envelope, because the accepted
// artifact reader decodes every record of its own file as an APIArtifact.
func TestJSONLStoreCreateAPIArtifactWithPublicationIntentCoPersistsBothRecords(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	ctx := context.Background()
	workstreamID := intentFixtureWorkstreamID(t)
	artifact := intentFixtureArtifact(t, workstreamID)
	envelope := intentFixtureEnvelope(t, artifact)

	if err := store.CreateAPIArtifactWithPublicationIntent(ctx, artifact, envelope); err != nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntent(%s) error = %v", artifact.ArtifactID, err)
	}

	artifactLines := readStoreFileLines(t, root, artifactFilename)
	if len(artifactLines) != 1 {
		t.Errorf("%s holds %d records, want 1: %v", artifactFilename, len(artifactLines), artifactLines)
	}
	if strings.Contains(artifactLines[0], `"event_seq"`) {
		t.Errorf("%s mixes an event envelope into artifact records: %s", artifactFilename, artifactLines[0])
	}
	intentLines := readStoreFileLines(t, root, artifactIntentFilename)
	if len(intentLines) != 1 {
		t.Fatalf("%s holds %d records, want 1: %v", artifactIntentFilename, len(intentLines), intentLines)
	}
	if strings.Contains(intentLines[0], `"content":`) {
		t.Errorf("%s mixes an artifact record into intents: %s", artifactIntentFilename, intentLines[0])
	}

	var stored modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(intentLines[0]), &stored); err != nil {
		t.Fatalf("decode stored intent: %v", err)
	}
	if stored.EventSeq != 0 {
		t.Errorf("stored intent event_seq = %d, want 0 (the canonical store assigns the sequence)", stored.EventSeq)
	}
	if stored.EventID != envelope.EventID {
		t.Errorf("stored intent event_id = %s, want %s", stored.EventID, envelope.EventID)
	}
	if stored.ArtifactID != artifact.ArtifactID {
		t.Errorf("stored intent artifact_id = %s, want %s", stored.ArtifactID, artifact.ArtifactID)
	}

	if got := mustAPIArtifactRecords(t, store); len(got) != 1 || got[0] != artifact {
		t.Errorf("ListAPIArtifacts() after create = %#v, want [%#v]", got, artifact)
	}
	intents, err := store.ListAPIArtifactPublicationIntents(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListAPIArtifactPublicationIntents() error = %v", err)
	}
	if len(intents) != 1 || intents[0].EventID != envelope.EventID {
		t.Fatalf("ListAPIArtifactPublicationIntents() = %d intents, want the one envelope %s", len(intents), envelope.EventID)
	}
	if !reflect.DeepEqual(intents[0], envelope) {
		t.Errorf("stored intent = %#v, want the exact caller envelope %#v", intents[0], envelope)
	}
}

// readStoreFileBytes snapshots a physical file so a test can state that a rejected or
// idempotent operation appended nothing at all, not merely that the reader still looks
// correct.
func readStoreFileBytes(t *testing.T, root, filename string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filename))
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	return raw
}

func mustListPublicationIntents(t *testing.T, store *JSONLStore, after modulecore.ArtifactID, limit int) []modulecore.EventEnvelope {
	t.Helper()
	intents, err := store.ListAPIArtifactPublicationIntents(context.Background(), after, limit)
	if err != nil {
		t.Fatalf("ListAPIArtifactPublicationIntents(after %q, limit %d) error = %v", after, limit, err)
	}
	return intents
}

// TestJSONLStoreCreateAPIArtifactWithPublicationIntentRetryAppendsNothing pins the
// idempotence a replay loop depends on: resubmitting the exact creation pair adds no
// record to either file, so a rerun after an unconfirmed commit cannot mint a second
// event for one creation, and the state a reopened store resolves stays a single
// artifact bound to a single intent.
func TestJSONLStoreCreateAPIArtifactWithPublicationIntentRetryAppendsNothing(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	ctx := context.Background()
	artifact := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	envelope := intentFixtureEnvelope(t, artifact)
	if err := store.CreateAPIArtifactWithPublicationIntent(ctx, artifact, envelope); err != nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntent(%s) error = %v", artifact.ArtifactID, err)
	}
	artifactBytes := readStoreFileBytes(t, root, artifactFilename)
	intentBytes := readStoreFileBytes(t, root, artifactIntentFilename)

	for attempt := 1; attempt <= 2; attempt++ {
		if err := store.CreateAPIArtifactWithPublicationIntent(ctx, artifact, envelope); err != nil {
			t.Fatalf("retry %d of the exact creation pair error = %v", attempt, err)
		}
	}
	if got := readStoreFileBytes(t, root, artifactFilename); !bytes.Equal(got, artifactBytes) {
		t.Errorf("%s changed on retry: %d bytes before, %d after", artifactFilename, len(artifactBytes), len(got))
	}
	if got := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(got, intentBytes) {
		t.Errorf("%s changed on retry: %d bytes before, %d after", artifactIntentFilename, len(intentBytes), len(got))
	}
	if got := mustAPIArtifactRecords(t, store); len(got) != 1 || got[0] != artifact {
		t.Errorf("ListAPIArtifacts() after retries = %d records, want exactly the created artifact %s", len(got), artifact.ArtifactID)
	}
	if got := mustListPublicationIntents(t, store, "", 10); len(got) != 1 || !reflect.DeepEqual(got[0], envelope) {
		t.Errorf("ListAPIArtifactPublicationIntents() after retries = %d intents, want exactly %s", len(got), envelope.EventID)
	}

	reopened, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() after retries error = %v", err)
	}
	if got := mustAPIArtifactRecords(t, reopened); len(got) != 1 || got[0] != artifact {
		t.Errorf("reopened artifact state = %d records, want exactly %s", len(got), artifact.ArtifactID)
	}
	if got := mustListPublicationIntents(t, reopened, "", 10); len(got) != 1 || !reflect.DeepEqual(got[0], envelope) {
		t.Errorf("reopened intents = %d, want exactly %s", len(got), envelope.EventID)
	}
}

// TestJSONLStoreCreateAPIArtifactWithPublicationIntentRejectsConflictingIntent pins the
// other side of idempotence: only the byte-identical stored envelope is a retry. A valid
// envelope carrying a different event id for an artifact that already has a creation
// intent is a second event for one creation and must be refused without overwriting the
// intent that publication already binds.
func TestJSONLStoreCreateAPIArtifactWithPublicationIntentRejectsConflictingIntent(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	ctx := context.Background()
	artifact := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	envelope := intentFixtureEnvelope(t, artifact)
	if err := store.CreateAPIArtifactWithPublicationIntent(ctx, artifact, envelope); err != nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntent(%s) error = %v", artifact.ArtifactID, err)
	}
	artifactBytes := readStoreFileBytes(t, root, artifactFilename)
	intentBytes := readStoreFileBytes(t, root, artifactIntentFilename)

	conflicting := intentFixtureEnvelope(t, artifact)
	if conflicting.EventID == envelope.EventID {
		t.Fatalf("fixture minted the same event id twice: %s", envelope.EventID)
	}
	err = store.CreateAPIArtifactWithPublicationIntent(ctx, artifact, conflicting)
	if err == nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntent with a second event id returned nil, want a conflict on artifact %s", artifact.ArtifactID)
	}
	if !strings.Contains(err.Error(), "second event") {
		t.Errorf("conflicting intent error = %v, want it to name the second event for one creation", err)
	}
	if got := readStoreFileBytes(t, root, artifactFilename); !bytes.Equal(got, artifactBytes) {
		t.Errorf("%s changed after a rejected conflicting intent: %d bytes before, %d after", artifactFilename, len(artifactBytes), len(got))
	}
	if got := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(got, intentBytes) {
		t.Errorf("%s changed after a rejected conflicting intent: %d bytes before, %d after", artifactIntentFilename, len(intentBytes), len(got))
	}
	if got := mustListPublicationIntents(t, store, "", 10); len(got) != 1 || !reflect.DeepEqual(got[0], envelope) {
		t.Errorf("intents after the rejection = %d, want the untouched %s", len(got), envelope.EventID)
	}
}

// TestJSONLStoreCreateAPIArtifactWithPublicationIntentRejectsSharedEventID covers the
// binding a create cannot see on its own: the intent file may already hold this event id as
// the creation fact of another artifact, so the submitted envelope would attach one event to
// two creations. That has to be refused before the append, instead of leaving an intent file
// that only becomes unloadable on the next open.
func TestJSONLStoreCreateAPIArtifactWithPublicationIntentRejectsSharedEventID(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	ctx := context.Background()
	first := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	firstIntent := intentFixtureEnvelope(t, first)
	if err := store.CreateAPIArtifactWithPublicationIntent(ctx, first, firstIntent); err != nil {
		t.Fatalf("create of artifact %s error = %v", first.ArtifactID, err)
	}
	artifactBytes := readStoreFileBytes(t, root, artifactFilename)
	intentBytes := readStoreFileBytes(t, root, artifactIntentFilename)

	// The second artifact has no row and no intent yet, so the event id it claims is the
	// only thing this create can get wrong.
	second := intentFixtureArtifact(t, modulecore.WorkstreamID(first.WorkstreamID))
	if second.ArtifactID == first.ArtifactID {
		t.Fatalf("fixture minted the same artifact id twice: %s", first.ArtifactID)
	}
	sharing := intentFixtureEnvelope(t, second)
	sharing.EventID = firstIntent.EventID
	err = store.CreateAPIArtifactWithPublicationIntent(ctx, second, sharing)
	if err == nil {
		t.Fatalf("create of %s with event id %s already owned by %s returned nil, want a rejection", second.ArtifactID, sharing.EventID, first.ArtifactID)
	}
	if !strings.Contains(err.Error(), string(first.ArtifactID)) {
		t.Errorf("shared event id error = %v, want it to name artifact %s that already claims the event", err, first.ArtifactID)
	}
	if got := readStoreFileBytes(t, root, artifactFilename); !bytes.Equal(got, artifactBytes) {
		t.Errorf("%s changed after the shared event id rejection: %d bytes before, %d after", artifactFilename, len(artifactBytes), len(got))
	}
	if got := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(got, intentBytes) {
		t.Errorf("%s grew after the shared event id rejection: %d bytes before, %d after", artifactIntentFilename, len(intentBytes), len(got))
	}
	if got := mustListPublicationIntents(t, store, "", 10); len(got) != 1 || !reflect.DeepEqual(got[0], firstIntent) {
		t.Errorf("intents after the rejection = %d, want only the stored %s", len(got), firstIntent.EventID)
	}
}

// TestJSONLStoreCreateAPIArtifactWithPublicationIntentRejectsRowStoredWithoutIntent keeps a
// migration backfill from posing as an ordinary create. The WAL writes a creation pair
// together, so a row that is stored while its creation fact is missing predates this create
// path, and the issuance, trace and verified actor that a creation fact carries belong to
// that original creation, not to the call that noticed the gap later. Creating such a row
// therefore has to mint nothing and append nothing, and it must not report success while
// quietly dating a history entry that it cannot evidence.
func TestJSONLStoreCreateAPIArtifactWithPublicationIntentRejectsRowStoredWithoutIntent(t *testing.T) {
	ctx := context.Background()

	t.Run("row_saved_by_an_ordinary_save", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewJSONLStore(root)
		if err != nil {
			t.Fatalf("NewJSONLStore() error = %v", err)
		}
		artifact := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
		if err := store.SaveAPIArtifact(ctx, artifact); err != nil {
			t.Fatalf("SaveAPIArtifact(%s) error = %v", artifact.ArtifactID, err)
		}
		artifactBytes := readStoreFileBytes(t, root, artifactFilename)
		intentBytes := readStoreFileBytes(t, root, artifactIntentFilename)

		err = store.CreateAPIArtifactWithPublicationIntent(ctx, artifact, intentFixtureEnvelope(t, artifact))
		if err == nil {
			t.Fatalf("create of the pre-existing row %s returned nil, want a rejection that mints no creation fact", artifact.ArtifactID)
		}
		if !strings.Contains(err.Error(), "predates") {
			t.Errorf("predating row error = %v, want it to say the row predates this create", err)
		}
		if got := readStoreFileBytes(t, root, artifactFilename); !bytes.Equal(got, artifactBytes) {
			t.Errorf("%s changed: %d bytes before, %d after", artifactFilename, len(artifactBytes), len(got))
		}
		if got := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(got, intentBytes) {
			t.Errorf("%s changed after a create that mints nothing: %d bytes before, %d after", artifactIntentFilename, len(intentBytes), len(got))
		}
		if got := mustAPIArtifactRecords(t, store); len(got) != 1 || got[0] != artifact {
			t.Errorf("stored row changed: %d records, want exactly %s", len(got), artifact.ArtifactID)
		}
		if got := mustListPublicationIntents(t, store, "", 10); len(got) != 0 {
			t.Errorf("intents after the rejection = %d, want none recorded for %s", len(got), artifact.ArtifactID)
		}
		// The rejection is a create-level decision, not a broken store: the intent-less row
		// stays readable at this source preparation stage and the store still writes.
		reopened, err := NewJSONLStore(root)
		if err != nil {
			t.Fatalf("NewJSONLStore() after the rejection error = %v", err)
		}
		if got := mustAPIArtifactRecords(t, reopened); len(got) != 1 || got[0] != artifact {
			t.Errorf("reopened state lost the intent-less row: %d records, want %s", len(got), artifact.ArtifactID)
		}
	})

	t.Run("row_that_was_already_superseded", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewJSONLStore(root)
		if err != nil {
			t.Fatalf("NewJSONLStore() error = %v", err)
		}
		predecessor := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
		successor := intentFixtureArtifact(t, modulecore.WorkstreamID(predecessor.WorkstreamID))
		if err := store.SaveAPIArtifact(ctx, predecessor); err != nil {
			t.Fatalf("SaveAPIArtifact(%s) error = %v", predecessor.ArtifactID, err)
		}
		if err := store.SaveAPIArtifact(ctx, successor); err != nil {
			t.Fatalf("SaveAPIArtifact(%s) error = %v", successor.ArtifactID, err)
		}
		if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
			t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
		}
		intentBytes := readStoreFileBytes(t, root, artifactIntentFilename)

		err = store.CreateAPIArtifactWithPublicationIntent(ctx, predecessor, intentFixtureEnvelope(t, predecessor))
		if err == nil {
			t.Fatalf("create of the superseded row %s returned nil, want a rejection that mints no creation fact", predecessor.ArtifactID)
		}
		if !strings.Contains(err.Error(), "predates") {
			t.Errorf("predating row error = %v, want it to say the row predates this create", err)
		}
		if got := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(got, intentBytes) {
			t.Errorf("%s changed after a create that mints nothing: %d bytes before, %d after", artifactIntentFilename, len(intentBytes), len(got))
		}
		if got := mustAPIArtifactRecords(t, store); len(got) != 2 || got[0].SupersededBy != successor.ArtifactID {
			t.Errorf("superseded rows changed: %d records with edge %q, want 2 rows still edged to %s", len(got), got[0].SupersededBy, successor.ArtifactID)
		}
	})
}

// TestJSONLStoreCreateAPIArtifactWithPublicationIntentRetryRequiresTheArtifactRow closes
// the hole a stored intent alone cannot prove: the retry no-op is only honest while the row
// the intent names still resolves and still carries the identity the envelope claims. The
// artifact rows are removed by a fixture rewrite, which is a corruption injection to
// reproduce a broken reference and not an owner operation, because the owner path appends
// and never deletes a row.
func TestJSONLStoreCreateAPIArtifactWithPublicationIntentRetryRequiresTheArtifactRow(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	ctx := context.Background()
	artifact := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	envelope := intentFixtureEnvelope(t, artifact)
	if err := store.CreateAPIArtifactWithPublicationIntent(ctx, artifact, envelope); err != nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntent(%s) error = %v", artifact.ArtifactID, err)
	}
	intentBytes := readStoreFileBytes(t, root, artifactIntentFilename)

	if err := os.WriteFile(filepath.Join(root, artifactFilename), nil, 0644); err != nil {
		t.Fatalf("fixture dropped the artifact row in %s: %v", artifactFilename, err)
	}

	err = store.CreateAPIArtifactWithPublicationIntent(ctx, artifact, envelope)
	if err == nil {
		t.Fatalf("retry of the exact pair returned nil while artifact %s has no row, want a rejection of the broken reference", artifact.ArtifactID)
	}
	if !strings.Contains(err.Error(), string(artifact.ArtifactID)) {
		t.Errorf("missing row error = %v, want it to name the artifact %s that has no record", err, artifact.ArtifactID)
	}
	if got := readStoreFileBytes(t, root, artifactFilename); len(got) != 0 {
		t.Errorf("%s grew by %d bytes on a retry that resolves no row; a retry must append nothing", artifactFilename, len(got))
	}
	if got := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(got, intentBytes) {
		t.Errorf("%s changed on the rejected retry: %d bytes before, %d after", artifactIntentFilename, len(intentBytes), len(got))
	}
}

// TestJSONLStoreSaveAndSupersedePreserveCreationIntent pins that neither an ordinary
// body update nor the supersession edge rewrites the original creation intent: the intent
// file stays byte-for-byte the same and, after a reopen, still reports the digest and
// identity of the bytes that were created, not the later mutable row.
func TestJSONLStoreSaveAndSupersedePreserveCreationIntent(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	ctx := context.Background()
	workstreamID := intentFixtureWorkstreamID(t)
	predecessor := intentFixtureArtifact(t, workstreamID)
	predecessorIntent := intentFixtureEnvelope(t, predecessor)
	successor := intentFixtureArtifact(t, workstreamID)
	successorIntent := intentFixtureEnvelope(t, successor)
	for _, pair := range []struct {
		item     domaintrace.APIArtifact
		envelope modulecore.EventEnvelope
	}{{predecessor, predecessorIntent}, {successor, successorIntent}} {
		if err := store.CreateAPIArtifactWithPublicationIntent(ctx, pair.item, pair.envelope); err != nil {
			t.Fatalf("create of artifact %s error = %v", pair.item.ArtifactID, err)
		}
	}
	originalIntents := readStoreFileBytes(t, root, artifactIntentFilename)

	updated := predecessor
	updated.Content = updatedIntentBody
	updated.ContentHash = modulecore.ContentHashOf([]byte(updatedIntentBody))
	updated.Title = "Observed OpenAPI Revised"
	updated.Status = "draft"
	if err := store.SaveAPIArtifact(ctx, updated); err != nil {
		t.Fatalf("SaveAPIArtifact(%s) after update error = %v", updated.ArtifactID, err)
	}
	if got := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(got, originalIntents) {
		t.Errorf("%s changed on an ordinary body update: %d bytes before, %d after", artifactIntentFilename, len(originalIntents), len(got))
	}

	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
	}
	if got := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(got, originalIntents) {
		t.Errorf("%s changed on Supersede: %d bytes before, %d after", artifactIntentFilename, len(originalIntents), len(got))
	}

	reopened, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() after update and supersede error = %v", err)
	}
	got := mustListPublicationIntents(t, reopened, "", 10)
	if len(got) != 2 {
		t.Fatalf("reopened intents = %d, want both creation intents", len(got))
	}
	for _, want := range []modulecore.EventEnvelope{predecessorIntent, successorIntent} {
		found := false
		for _, intent := range got {
			if reflect.DeepEqual(intent, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("reopened intents lost the creation envelope %s for artifact %s", want.EventID, want.ArtifactID)
		}
	}

	// The exact creation pair stays a no-op even after the row moved on, which is what a
	// publication retry needs once the update and the edge landed.
	bytesBeforeRetry := readStoreFileBytes(t, root, artifactIntentFilename)
	if err := reopened.CreateAPIArtifactWithPublicationIntent(ctx, predecessor, predecessorIntent); err != nil {
		t.Errorf("retry of the creation pair after update and supersede error = %v, want the stored intent to be recognised", err)
	}
	if after := readStoreFileBytes(t, root, artifactIntentFilename); !bytes.Equal(after, bytesBeforeRetry) {
		t.Errorf("%s grew on the retry: %d bytes before, %d after", artifactIntentFilename, len(bytesBeforeRetry), len(after))
	}
}

// TestJSONLStoreListAPIArtifactPublicationIntentsDrainsEveryPage pins the paging
// contract publication recovery relies on: the limit bounds one call, the cursor is
// exclusive and ordered by artifact id, and repeated pages exhaust the whole history, so
// an older source event never becomes unobservable behind a display bound.
func TestJSONLStoreListAPIArtifactPublicationIntentsDrainsEveryPage(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	ctx := context.Background()
	workstreamID := intentFixtureWorkstreamID(t)
	var want []modulecore.EventEnvelope
	for i := 0; i < 3; i++ {
		item := intentFixtureArtifact(t, workstreamID)
		envelope := intentFixtureEnvelope(t, item)
		if err := store.CreateAPIArtifactWithPublicationIntent(ctx, item, envelope); err != nil {
			t.Fatalf("create of artifact %s error = %v", item.ArtifactID, err)
		}
		want = append(want, envelope)
	}

	for _, limit := range []int{1, 2, 3, maxPublicationIntentPageLimit + 500} {
		var drained []modulecore.EventEnvelope
		cursor := modulecore.ArtifactID("")
		for page := 0; page < 20; page++ {
			pageItems, err := store.ListAPIArtifactPublicationIntents(ctx, cursor, limit)
			if err != nil {
				t.Fatalf("ListAPIArtifactPublicationIntents(after %q, limit %d) error = %v", cursor, limit, err)
			}
			if len(pageItems) == 0 {
				break
			}
			drained = append(drained, pageItems...)
			cursor = pageItems[len(pageItems)-1].ArtifactID
		}
		if len(drained) != len(want) {
			t.Errorf("draining with limit %d returned %d intents, want all %d", limit, len(drained), len(want))
			continue
		}
		for i := range drained {
			if i > 0 && drained[i-1].ArtifactID >= drained[i].ArtifactID {
				t.Errorf("limit %d page order is not ascending by artifact id at index %d: %s then %s", limit, i, drained[i-1].ArtifactID, drained[i].ArtifactID)
			}
			found := false
			for _, expected := range want {
				if reflect.DeepEqual(drained[i], expected) {
					found = true
				}
			}
			if !found {
				t.Errorf("limit %d drain returned intent %s for artifact %s, which is not a stored creation envelope", limit, drained[i].EventID, drained[i].ArtifactID)
			}
		}
	}

	if _, err := store.ListAPIArtifactPublicationIntents(ctx, modulecore.ArtifactID("art_not_an_id"), 5); err == nil {
		t.Errorf("ListAPIArtifactPublicationIntents with a malformed cursor returned nil, want a cursor validation error")
	} else if !strings.Contains(err.Error(), "page cursor") {
		t.Errorf("malformed cursor error = %v, want it to name the page cursor", err)
	}
}
