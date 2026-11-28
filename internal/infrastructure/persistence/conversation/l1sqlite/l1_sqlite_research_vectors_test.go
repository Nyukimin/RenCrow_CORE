package l1sqlite

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResearchVectorsPublishIdempotentlyAndReadOnlyCurrentScopedDocuments(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	insertResearchRecallItem(t, store, "public-vector", "kb:research", "event-public", "public", "", "Public evidence", now, time.Time{})
	insertResearchRecallItem(t, store, "alice-vector", "user:alice", "event-alice", "user", "alice", "Alice evidence", now, time.Time{})
	insertResearchRecallItem(t, store, "bob-vector", "user:bob", "event-bob", "user", "bob", "Bob evidence", now, time.Time{})
	for _, input := range []L1ResearchVectorDocument{
		researchVectorInput("public", "", "public-vector", "event-public", "Public evidence"),
		researchVectorInput("user", "alice", "alice-vector", "event-alice", "Alice evidence"),
		researchVectorInput("user", "bob", "bob-vector", "event-bob", "Bob evidence"),
	} {
		if err := store.PublishResearchDocumentVectors(ctx, input); err != nil {
			t.Fatalf("PublishResearchDocumentVectors(%s): %v", input.DocumentID, err)
		}
		if err := store.PublishResearchDocumentVectors(ctx, input); err != nil {
			t.Fatalf("idempotent PublishResearchDocumentVectors(%s): %v", input.DocumentID, err)
		}
	}
	for _, tc := range []struct {
		scope, owner, want string
	}{
		{"public", "", "public-vector"}, {"user", "alice", "alice-vector"}, {"user", "bob", "bob-vector"},
	} {
		result, err := store.ReadResearchVectorCandidates(ctx, researchVectorReadRequest(tc.scope, tc.owner, 20))
		if err != nil || result.State != L1ResearchVectorReadAvailable || len(result.Candidates) != 1 || result.Candidates[0].DocumentID != tc.want || result.Candidates[0].Vector[0] != 1 {
			t.Fatalf("scoped result (%s,%s) = %+v err=%v", tc.scope, tc.owner, result, err)
		}
	}
	foreign, err := store.ReadResearchVectorCandidates(ctx, researchVectorReadRequest("user", "alice", 1))
	if err != nil || len(foreign.Candidates) != 1 || foreign.Candidates[0].DocumentID == "bob-vector" {
		t.Fatalf("alice saw foreign vector candidate: %+v err=%v", foreign, err)
	}
	empty, err := store.ReadResearchVectorCandidates(ctx, researchVectorReadRequest("user", "nobody", 20))
	if err != nil || empty.State != L1ResearchVectorReadEmpty {
		t.Fatalf("no matching vectors = %+v err=%v, want explicit empty", empty, err)
	}
	conflict := researchVectorInput("public", "", "public-vector", "event-public", "Public evidence")
	conflict.Chunks[0].Vector = []float32{0, 1}
	if err := store.PublishResearchDocumentVectors(ctx, conflict); err == nil {
		t.Fatal("same generation key accepted a different payload")
	}
	result, err := store.ReadResearchVectorCandidates(ctx, researchVectorReadRequest("public", "", 20))
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Vector[0] != 1 {
		t.Fatalf("prior complete generation changed after rejected replacement: %+v err=%v", result, err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_vector_chunks BEFORE INSERT ON l1_research_vector_chunk BEGIN SELECT RAISE(ABORT, 'injected chunk write failure'); END`); err != nil {
		t.Fatal(err)
	}
	failedReplacement := researchVectorInput("public", "", "public-vector", "event-public", "Public evidence")
	failedReplacement.ParserRevision = "parser-v2"
	if err := store.PublishResearchDocumentVectors(ctx, failedReplacement); err == nil {
		t.Fatal("injected generation failure returned nil")
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_vector_chunks`); err != nil {
		t.Fatal(err)
	}
	result, err = store.ReadResearchVectorCandidates(ctx, researchVectorReadRequest("public", "", 20))
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].ParserRevision != "parser-v1" {
		t.Fatalf("failed replacement discarded prior complete generation: %+v err=%v", result, err)
	}
	alias := researchVectorReadRequest("public", "", 20)
	alias.ModelRevision = "different-model-revision"
	wrongDimension := researchVectorReadRequest("public", "", 20)
	wrongDimension.Dimension = 3
	for _, request := range []L1ResearchVectorReadRequest{alias, wrongDimension} {
		mismatch, err := store.ReadResearchVectorCandidates(ctx, request)
		if err != nil || mismatch.State != L1ResearchVectorReadEmpty || len(mismatch.Candidates) != 0 {
			t.Fatalf("revision/dimension mismatch returned candidates: %+v err=%v", mismatch, err)
		}
	}
}

func TestResearchVectorsImmediatelyExcludeChangedDeletedOrRevokedCanonicalRows(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"changed-vector", "deleted-vector", "revoked-vector", "promoted-vector"} {
		insertResearchRecallItem(t, store, id, "kb:research", "event-"+id, "public", "", "Canonical evidence", now, time.Time{})
		if err := store.PublishResearchDocumentVectors(ctx, researchVectorInput("public", "", id, "event-"+id, "Canonical evidence")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE l1_staging_item SET raw_text='changed',raw_hash=?,meta_json=json_set(meta_json,'$.research_digest',?) WHERE id='changed-vector'`, rawTextHash("changed"), rawTextHash("changed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM l1_staging_item WHERE id='deleted-vector'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE l1_staging_item SET meta_json=json_set(meta_json,'$.retention_allowed',0) WHERE id='revoked-vector'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE l1_staging_item SET validation_status='validated' WHERE id='promoted-vector'`); err != nil {
		t.Fatal(err)
	}
	result, err := store.ReadResearchVectorCandidates(ctx, researchVectorReadRequest("public", "", 20))
	if err != nil || len(result.Candidates) != 0 || result.State != L1ResearchVectorReadEmpty {
		t.Fatalf("stale canonical vectors remain visible: %+v err=%v", result, err)
	}
}

func TestResearchVectorsRejectInvalidPayloadBeforeWriteAndReportRowCap(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	insertResearchRecallItem(t, store, "invalid-vector", "kb:research", "event-invalid", "public", "", "abc", now, time.Time{})
	base := researchVectorInput("public", "", "invalid-vector", "event-invalid", "abc")
	badInputs := []L1ResearchVectorDocument{}
	bad := base
	bad.Chunks = append([]L1ResearchVectorChunk(nil), base.Chunks...)
	bad.Chunks[0].Vector = append([]float32(nil), base.Chunks[0].Vector...)
	bad.Chunks[0].Vector[0] = float32(math.NaN())
	badInputs = append(badInputs, bad)
	bad = base
	bad.Chunks = append([]L1ResearchVectorChunk(nil), base.Chunks...)
	bad.Chunks[0].Vector = append([]float32(nil), base.Chunks[0].Vector...)
	bad.Chunks[0].Vector[0] = float32(math.Inf(1))
	badInputs = append(badInputs, bad)
	bad = base
	bad.Chunks = append([]L1ResearchVectorChunk(nil), base.Chunks...)
	bad.Chunks[0].Vector = append([]float32(nil), base.Chunks[0].Vector...)
	bad.Chunks[0].Vector = []float32{0, 0}
	badInputs = append(badInputs, bad)
	bad = base
	bad.Chunks = append([]L1ResearchVectorChunk(nil), base.Chunks...)
	bad.Chunks[0].Vector = append([]float32(nil), base.Chunks[0].Vector...)
	bad.Chunks[0].RuneEnd = 4
	badInputs = append(badInputs, bad)
	bad = base
	bad.Chunks = append([]L1ResearchVectorChunk(nil), base.Chunks...)
	bad.Chunks[0].Vector = append([]float32(nil), base.Chunks[0].Vector...)
	bad.Chunks[0].SHA256 = "invalid"
	badInputs = append(badInputs, bad)
	for i, input := range badInputs {
		if err := store.PublishResearchDocumentVectors(ctx, input); err == nil {
			t.Fatalf("invalid payload %d was accepted", i)
		}
	}
	var writes int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM l1_research_vector_generation`).Scan(&writes); err != nil || writes != 0 {
		t.Fatalf("invalid requests wrote %d generations, err=%v", writes, err)
	}

	insertResearchRecallItem(t, store, "capped-vector", "kb:research", "event-capped", "public", "", "abc", now, time.Time{})
	capped := base
	capped.DocumentID, capped.EventID = "capped-vector", "event-capped"
	capped.Chunks = []L1ResearchVectorChunk{base.Chunks[0], base.Chunks[0], base.Chunks[0]}
	if err := store.PublishResearchDocumentVectors(ctx, capped); err != nil {
		t.Fatal(err)
	}
	result, err := store.ReadResearchVectorCandidates(ctx, researchVectorReadRequest("public", "", 2))
	if err != nil || result.State != L1ResearchVectorReadTruncated || len(result.Candidates) != 2 {
		t.Fatalf("row cap result = %+v err=%v, want two candidates and truncation", result, err)
	}
}

func TestResearchVectorsDistinguishMissingIndexFromEmptyIndex(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	request := researchVectorReadRequest("public", "", 20)
	empty, err := store.ReadResearchVectorCandidates(ctx, request)
	if err != nil || empty.State != L1ResearchVectorReadEmpty {
		t.Fatalf("initialized empty index = %+v err=%v", empty, err)
	}
	for _, table := range []string{"l1_research_vector_active", "l1_research_vector_chunk", "l1_research_vector_generation"} {
		if _, err := store.db.ExecContext(ctx, `DROP TABLE `+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	missing, err := store.ReadResearchVectorCandidates(ctx, request)
	if err != nil || missing.State != L1ResearchVectorReadMissing {
		t.Fatalf("missing index = %+v err=%v", missing, err)
	}
}

func TestResearchVectorsReadRejectsPersistedZeroVector(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	insertResearchRecallItem(t, store, "zero-vector", "kb:research", "event-zero", "public", "", "Zero vector evidence", now, time.Time{})
	input := researchVectorInput("public", "", "zero-vector", "event-zero", "Zero vector evidence")
	if err := store.PublishResearchDocumentVectors(ctx, input); err != nil {
		t.Fatalf("PublishResearchDocumentVectors: %v", err)
	}
	updated, err := store.db.ExecContext(ctx, `UPDATE l1_research_vector_chunk SET vector_blob = ? WHERE generation_key = ? AND chunk_index = 0`, make([]byte, input.Dimension*4), l1ResearchVectorGenerationKey(input))
	if err != nil {
		t.Fatalf("corrupt persisted vector: %v", err)
	}
	rowsAffected, err := updated.RowsAffected()
	if err != nil || rowsAffected != 1 {
		t.Fatalf("corrupted vector rows = %d err=%v, want exactly one", rowsAffected, err)
	}

	result, err := store.ReadResearchVectorCandidates(ctx, researchVectorReadRequest("public", "", 20))
	if err == nil || !strings.Contains(err.Error(), "zero norm") {
		t.Fatalf("read persisted zero vector = %+v err=%v, want zero-norm error", result, err)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("read returned candidates after corrupt vector decode failure: %+v", result.Candidates)
	}
}

func researchVectorInput(scope, owner, id, eventID, text string) L1ResearchVectorDocument {
	runes := []rune(text)
	return L1ResearchVectorDocument{
		Scope: scope, OwnerID: owner, DocumentID: id, EventID: eventID,
		ExpectedRawHash: rawTextHash(text), ParserRevision: "parser-v1", ChunkerRevision: "chunker-v1",
		ModelRevision: "llm-owner-model-rev-1", Dimension: 2,
		Chunks: []L1ResearchVectorChunk{{RuneStart: 0, RuneEnd: len(runes), SHA256: rawTextHash(string(runes)), Vector: []float32{1, 0}}},
	}
}

func researchVectorReadRequest(scope, owner string, limit int) L1ResearchVectorReadRequest {
	return L1ResearchVectorReadRequest{
		Scope: scope, OwnerID: owner, ParserRevision: "parser-v1", ChunkerRevision: "chunker-v1",
		ModelRevision: "llm-owner-model-rev-1", Dimension: 2, Limit: limit,
	}
}
