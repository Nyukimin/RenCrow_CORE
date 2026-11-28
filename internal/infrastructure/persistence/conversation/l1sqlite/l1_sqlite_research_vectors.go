package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	L1ResearchVectorMaxChunks    = 2000
	L1ResearchVectorMaxDimension = 4096
	L1ResearchVectorMaxRevision  = 128
)

type L1ResearchVectorChunk struct {
	RuneStart int
	RuneEnd   int
	SHA256    string
	Vector    []float32
}

// L1ResearchVectorDocument is a per-document publication request from the
// research owner. The canonical text is loaded and checked inside the write
// transaction; no text is copied to the derived vector tables.
type L1ResearchVectorDocument struct {
	Scope           string
	OwnerID         string
	DocumentID      string
	EventID         string
	ExpectedRawHash string
	ParserRevision  string
	ChunkerRevision string
	ModelRevision   string
	Dimension       int
	Chunks          []L1ResearchVectorChunk
}

type L1ResearchVectorCandidate struct {
	DocumentID      string
	EventID         string
	RawHash         string
	GenerationKey   string
	ChunkIndex      int
	RuneStart       int
	RuneEnd         int
	ChunkSHA256     string
	Dimension       int
	ParserRevision  string
	ChunkerRevision string
	ModelRevision   string
	Vector          []float32
}

type L1ResearchVectorReadState string

const (
	L1ResearchVectorReadMissing L1ResearchVectorReadState = "missing"
	// L1ResearchVectorReadEmpty means no eligible current canonical active vectors
	// matched the request; missing or stale vectors can therefore yield this
	// state. It does not represent a completed similarity search with no matches.
	L1ResearchVectorReadEmpty     L1ResearchVectorReadState = "empty"
	L1ResearchVectorReadAvailable L1ResearchVectorReadState = "available"
	L1ResearchVectorReadTruncated L1ResearchVectorReadState = "truncated"
)

type L1ResearchVectorReadResult struct {
	State      L1ResearchVectorReadState
	Candidates []L1ResearchVectorCandidate
}

type L1ResearchVectorReadRequest struct {
	Scope           string
	OwnerID         string
	ParserRevision  string
	ChunkerRevision string
	ModelRevision   string
	Dimension       int
	Limit           int
}

// PublishResearchDocumentVectors validates canonical ownership and content,
// then commits an immutable complete generation and its active pointer in one
// existing L1 writer transaction.
func (s *L1SQLiteStore) PublishResearchDocumentVectors(ctx context.Context, input L1ResearchVectorDocument) error {
	if s == nil || s.db == nil || ctx == nil {
		return errors.New("l1 research vector store and context are required")
	}
	namespace, err := researchRecallNamespace(input.Scope, input.OwnerID)
	if err != nil {
		return err
	}
	if err := validateL1ResearchVectorDocument(input); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin l1 research vector publication: %w", err)
	}
	defer tx.Rollback()

	var rawText string
	var canonicalHash string
	var metaJSON string
	err = tx.QueryRowContext(ctx, `
SELECT raw_text, raw_hash, meta_json
FROM l1_staging_item
WHERE id = ? AND event_id = ? AND kind = ? AND namespace = ? AND validation_status = ?`,
		input.DocumentID, input.EventID, L1StagingKindExternalFetch, namespace, L1StagingStatusPending,
	).Scan(&rawText, &canonicalHash, &metaJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("l1 research vector canonical document is unavailable")
		}
		return fmt.Errorf("read l1 research vector canonical document: %w", err)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return errors.New("l1 research vector canonical metadata is invalid")
	}
	if canonicalHash != input.ExpectedRawHash || rawTextHash(rawText) != canonicalHash ||
		stringMeta(meta, "research_scope") != input.Scope ||
		stringMeta(meta, "research_namespace") != namespace ||
		stringMeta(meta, "research_digest") != canonicalHash ||
		boolMeta(meta, "content_complete") != true || boolMeta(meta, "retention_allowed") != true {
		return errors.New("l1 research vector canonical document changed or is not retained")
	}
	if err := validateL1ResearchVectorChunks(input.Chunks, []rune(rawText), input.Dimension); err != nil {
		return err
	}

	generationKey := l1ResearchVectorGenerationKey(input)
	var existingDimension, existingCount int
	err = tx.QueryRowContext(ctx, `
SELECT dimension, chunk_count FROM l1_research_vector_generation
WHERE generation_key = ? AND scope = ? AND owner_id = ? AND document_id = ? AND raw_hash = ?
	AND parser_revision = ? AND chunker_revision = ? AND model_revision = ? AND complete = 1`,
		generationKey, input.Scope, input.OwnerID, input.DocumentID, input.ExpectedRawHash,
		input.ParserRevision, input.ChunkerRevision, input.ModelRevision,
	).Scan(&existingDimension, &existingCount)
	switch {
	case err == nil:
		if existingDimension != input.Dimension || existingCount != len(input.Chunks) {
			return errors.New("l1 research vector generation key conflicts with different payload")
		}
		for i, chunk := range input.Chunks {
			var start, end int
			var digest string
			var blob []byte
			if err := tx.QueryRowContext(ctx, `SELECT rune_start, rune_end, chunk_sha256, vector_blob FROM l1_research_vector_chunk WHERE generation_key = ? AND chunk_index = ?`, generationKey, i).Scan(&start, &end, &digest, &blob); err != nil {
				return fmt.Errorf("read existing l1 research vector chunk: %w", err)
			}
			if start != chunk.RuneStart || end != chunk.RuneEnd || digest != chunk.SHA256 || !equalL1VectorBlob(blob, chunk.Vector) {
				return errors.New("l1 research vector generation key conflicts with different payload")
			}
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO l1_research_vector_generation (
generation_key, scope, owner_id, document_id, event_id, raw_hash, parser_revision, chunker_revision,
model_revision, dimension, chunk_count, complete, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`, generationKey, input.Scope, input.OwnerID,
			input.DocumentID, input.EventID, input.ExpectedRawHash, input.ParserRevision, input.ChunkerRevision,
			input.ModelRevision, input.Dimension, len(input.Chunks), time.Now().UTC()); err != nil {
			return fmt.Errorf("insert l1 research vector generation: %w", err)
		}
		for i, chunk := range input.Chunks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO l1_research_vector_chunk (generation_key, chunk_index, rune_start, rune_end, chunk_sha256, vector_blob) VALUES (?, ?, ?, ?, ?, ?)`, generationKey, i, chunk.RuneStart, chunk.RuneEnd, chunk.SHA256, encodeL1Vector(chunk.Vector)); err != nil {
				return fmt.Errorf("insert l1 research vector chunk: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE l1_research_vector_generation SET complete = 1 WHERE generation_key = ? AND complete = 0`, generationKey); err != nil {
			return fmt.Errorf("complete l1 research vector generation: %w", err)
		}
	default:
		return fmt.Errorf("read l1 research vector generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO l1_research_vector_active (scope, owner_id, document_id, generation_key, updated_at)
VALUES (?, ?, ?, ?, ?) ON CONFLICT(scope, owner_id, document_id) DO UPDATE SET generation_key=excluded.generation_key, updated_at=excluded.updated_at`, input.Scope, input.OwnerID, input.DocumentID, generationKey, time.Now().UTC()); err != nil {
		return fmt.Errorf("switch active l1 research vector generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit l1 research vector generation: %w", err)
	}
	return nil
}

// ReadResearchVectorCandidates returns only active vectors whose current
// canonical staging row still matches scope, digest, retention, and status.
// Scope predicates are applied before LIMIT; callers must exact-read candidates
// again before using them as evidence.
func (s *L1SQLiteStore) ReadResearchVectorCandidates(ctx context.Context, request L1ResearchVectorReadRequest) (L1ResearchVectorReadResult, error) {
	if s == nil || ctx == nil {
		return L1ResearchVectorReadResult{}, errors.New("l1 research vector store and context are required")
	}
	if request.Limit < 1 || request.Limit > L1ResearchVectorMaxChunks {
		return L1ResearchVectorReadResult{}, errors.New("l1 research vector limit must be between 1 and 2000")
	}
	if request.Dimension < 1 || request.Dimension > L1ResearchVectorMaxDimension || !validL1ResearchVectorRevision(request.ParserRevision) || !validL1ResearchVectorRevision(request.ChunkerRevision) || !validL1ResearchVectorRevision(request.ModelRevision) {
		return L1ResearchVectorReadResult{}, errors.New("l1 research vector read revisions and dimension must be valid and bounded")
	}
	namespace, err := researchRecallNamespace(request.Scope, request.OwnerID)
	if err != nil {
		return L1ResearchVectorReadResult{}, err
	}
	queryDB := s.readDB
	if queryDB == nil {
		queryDB = s.db
	}
	if queryDB == nil {
		return L1ResearchVectorReadResult{}, errors.New("l1 research vector store is closed")
	}
	var tableCount int
	if err := queryDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('l1_research_vector_generation','l1_research_vector_chunk','l1_research_vector_active')`).Scan(&tableCount); err != nil {
		return L1ResearchVectorReadResult{}, fmt.Errorf("check l1 research vector index: %w", err)
	}
	if tableCount != 3 {
		return L1ResearchVectorReadResult{State: L1ResearchVectorReadMissing}, nil
	}
	rows, err := queryDB.QueryContext(ctx, `
SELECT g.document_id, g.event_id, g.raw_hash, g.generation_key, c.chunk_index,
       c.rune_start, c.rune_end, c.chunk_sha256, g.dimension, g.parser_revision,
       g.chunker_revision, g.model_revision, c.vector_blob
FROM l1_research_vector_active a
JOIN l1_research_vector_generation g ON g.generation_key = a.generation_key AND g.complete = 1
JOIN l1_research_vector_chunk c ON c.generation_key = g.generation_key
JOIN l1_staging_item d ON d.id = g.document_id AND d.event_id = g.event_id
WHERE a.scope = ? AND a.owner_id = ? AND g.scope = ? AND g.owner_id = ?
  AND g.parser_revision = ? AND g.chunker_revision = ? AND g.model_revision = ? AND g.dimension = ?
  AND d.kind = ? AND d.namespace = ? AND d.validation_status = ?
  AND json_valid(d.meta_json) = 1
  AND json_extract(d.meta_json, '$.research_scope') = ?
  AND json_extract(d.meta_json, '$.research_namespace') = ?
  AND json_extract(d.meta_json, '$.content_complete') = 1
  AND json_extract(d.meta_json, '$.retention_allowed') = 1
  AND json_extract(d.meta_json, '$.research_digest') = d.raw_hash
  AND d.raw_hash = g.raw_hash
ORDER BY g.document_id, c.chunk_index
LIMIT ?`, request.Scope, request.OwnerID, request.Scope, request.OwnerID, request.ParserRevision, request.ChunkerRevision, request.ModelRevision, request.Dimension, L1StagingKindExternalFetch, namespace, L1StagingStatusPending, request.Scope, namespace, request.Limit+1)
	if err != nil {
		return L1ResearchVectorReadResult{}, fmt.Errorf("query scoped l1 research vectors: %w", err)
	}
	defer rows.Close()
	result := L1ResearchVectorReadResult{State: L1ResearchVectorReadEmpty, Candidates: make([]L1ResearchVectorCandidate, 0, request.Limit)}
	for rows.Next() {
		var item L1ResearchVectorCandidate
		var vectorBlob []byte
		if err := rows.Scan(&item.DocumentID, &item.EventID, &item.RawHash, &item.GenerationKey, &item.ChunkIndex, &item.RuneStart, &item.RuneEnd, &item.ChunkSHA256, &item.Dimension, &item.ParserRevision, &item.ChunkerRevision, &item.ModelRevision, &vectorBlob); err != nil {
			return L1ResearchVectorReadResult{}, fmt.Errorf("scan scoped l1 research vector: %w", err)
		}
		if len(result.Candidates) == request.Limit {
			result.State = L1ResearchVectorReadTruncated
			break
		}
		vector, err := decodeL1Vector(vectorBlob, item.Dimension)
		if err != nil {
			return L1ResearchVectorReadResult{}, fmt.Errorf("decode scoped l1 research vector: %w", err)
		}
		item.Vector = vector
		result.Candidates = append(result.Candidates, item)
	}
	if err := rows.Err(); err != nil {
		return L1ResearchVectorReadResult{}, fmt.Errorf("iterate scoped l1 research vectors: %w", err)
	}
	if result.State != L1ResearchVectorReadTruncated && len(result.Candidates) > 0 {
		result.State = L1ResearchVectorReadAvailable
	}
	return result, nil
}

func validateL1ResearchVectorDocument(input L1ResearchVectorDocument) error {
	if input.DocumentID == "" || strings.TrimSpace(input.DocumentID) != input.DocumentID || len(input.DocumentID) > 512 || input.EventID == "" || strings.TrimSpace(input.EventID) != input.EventID || len(input.EventID) > 512 {
		return errors.New("l1 research vector document and event IDs must be non-empty and bounded")
	}
	if !validL1SHA256(input.ExpectedRawHash) || input.Dimension < 1 || input.Dimension > L1ResearchVectorMaxDimension {
		return errors.New("l1 research vector hash or dimension is invalid")
	}
	for _, revision := range []string{input.ParserRevision, input.ChunkerRevision, input.ModelRevision} {
		if !validL1ResearchVectorRevision(revision) {
			return errors.New("l1 research vector revisions must be non-empty and bounded")
		}
	}
	if len(input.Chunks) == 0 || len(input.Chunks) > L1ResearchVectorMaxChunks {
		return errors.New("l1 research vector chunk count must be between 1 and 2000")
	}
	return nil
}

func validL1ResearchVectorRevision(revision string) bool {
	return revision != "" && strings.TrimSpace(revision) == revision && len(revision) <= L1ResearchVectorMaxRevision && utf8.ValidString(revision)
}

func validateL1ResearchVectorChunks(chunks []L1ResearchVectorChunk, canonical []rune, dimension int) error {
	for _, chunk := range chunks {
		if chunk.RuneStart < 0 || chunk.RuneEnd <= chunk.RuneStart || chunk.RuneEnd > len(canonical) || !validL1SHA256(chunk.SHA256) {
			return errors.New("l1 research vector chunk range or hash is invalid")
		}
		if rawTextHash(string(canonical[chunk.RuneStart:chunk.RuneEnd])) != chunk.SHA256 {
			return errors.New("l1 research vector chunk hash does not match canonical text")
		}
		if len(chunk.Vector) != dimension {
			return errors.New("l1 research vector dimension mismatch")
		}
		norm := float64(0)
		for _, value := range chunk.Vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return errors.New("l1 research vector contains a non-finite value")
			}
			norm += float64(value) * float64(value)
		}
		if norm == 0 {
			return errors.New("l1 research vector must have a non-zero norm")
		}
	}
	return nil
}

func validL1SHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func l1ResearchVectorGenerationKey(input L1ResearchVectorDocument) string {
	payload, _ := json.Marshal([]string{input.Scope, input.OwnerID, input.DocumentID, input.EventID, input.ExpectedRawHash, input.ParserRevision, input.ChunkerRevision, input.ModelRevision})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func encodeL1Vector(vector []float32) []byte {
	blob := make([]byte, len(vector)*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(value))
	}
	return blob
}

func decodeL1Vector(blob []byte, dimension int) ([]float32, error) {
	if dimension < 1 || dimension > L1ResearchVectorMaxDimension || len(blob) != dimension*4 {
		return nil, errors.New("stored l1 research vector has invalid dimension")
	}
	vector := make([]float32, dimension)
	norm := float64(0)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
		if math.IsNaN(float64(vector[i])) || math.IsInf(float64(vector[i]), 0) {
			return nil, errors.New("stored l1 research vector has a non-finite value")
		}
		norm += float64(vector[i]) * float64(vector[i])
	}
	if norm == 0 {
		return nil, errors.New("stored l1 research vector has a zero norm")
	}
	return vector, nil
}

func equalL1VectorBlob(blob []byte, vector []float32) bool {
	expected := encodeL1Vector(vector)
	if len(blob) != len(expected) {
		return false
	}
	for i := range blob {
		if blob[i] != expected[i] {
			return false
		}
	}
	return true
}

func boolMeta(meta map[string]any, key string) bool {
	value, ok := meta[key].(bool)
	return ok && value
}
