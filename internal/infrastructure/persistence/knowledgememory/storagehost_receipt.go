package knowledgememory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

var knowledgeMemoryStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// KnowledgeMemoryStorageHostOperationIdentity binds one storage-host writer
// epoch and payload to the owner transaction that creates a private candidate.
type KnowledgeMemoryStorageHostOperationIdentity struct {
	OpID             string `json:"op_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	WriterGeneration int64  `json:"writer_generation"`
}

// CreativeCandidateStorageHostResult is the exact durable result of a private
// candidate mutation.
type CreativeCandidateStorageHostResult struct {
	Replayed bool `json:"replayed"`
}

func knowledgeMemoryStorageHostReceiptSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS knowledge_memory_storagehost_receipts (
		op_id TEXT PRIMARY KEY,
		payload_sha256 TEXT NOT NULL,
		writer_generation INTEGER NOT NULL,
		action_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		item_id TEXT NOT NULL,
		candidate_sha256 TEXT NOT NULL,
		request_receipt_sha256 TEXT NOT NULL,
		effect_sha256 TEXT NOT NULL,
		result_json TEXT NOT NULL,
		result_sha256 TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`, `CREATE TABLE IF NOT EXISTS knowledge_memory_storagehost_save_receipts (
		op_id TEXT PRIMARY KEY,
		operation TEXT NOT NULL,
		payload_sha256 TEXT NOT NULL,
		writer_generation INTEGER NOT NULL,
		effect_id TEXT NOT NULL,
		effect_sha256 TEXT NOT NULL,
		proof_sha256 TEXT NOT NULL,
		result_json TEXT NOT NULL,
		result_sha256 TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`}
}

// SaveCreativeCandidateForStorageHostOperation atomically stores the private
// candidate, product receipt, and exact storage-host result proof.
func (s *SQLiteStore) SaveCreativeCandidateForStorageHostOperation(ctx context.Context, identity KnowledgeMemoryStorageHostOperationIdentity, item domainkm.CreativeKnowledgeItem, receipt KnowledgeMemoryRequestReceipt) (CreativeCandidateStorageHostResult, error) {
	if s == nil || s.db == nil {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	if err := validateKnowledgeMemoryStorageHostIdentity(identity); err != nil {
		return CreativeCandidateStorageHostResult{}, err
	}
	item, receipt, payload, err := prepareCreativeCandidateWrite(item, receipt)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, err
	}
	candidateSHA, requestReceiptSHA, err := knowledgeMemoryCandidateEffectHashes(item, receipt)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("begin knowledge memory storage-host transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stored, found, err := findKnowledgeMemoryStorageHostReceipt(ctx, tx, identity.OpID)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, err
	}
	if found {
		result, err := verifyKnowledgeMemoryStorageHostReceipt(ctx, tx, stored, identity, item, receipt, candidateSHA, requestReceiptSHA)
		if err != nil {
			return CreativeCandidateStorageHostResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CreativeCandidateStorageHostResult{}, fmt.Errorf("commit knowledge memory storage-host replay: %w", err)
		}
		return result, nil
	}

	replayed, err := saveCreativeCandidateWithReceiptTx(ctx, tx, item, receipt, payload)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, err
	}
	result := CreativeCandidateStorageHostResult{Replayed: replayed}
	resultJSON, resultSHA, err := encodeKnowledgeMemoryStorageHostResult(result)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, err
	}
	effectSHA := knowledgeMemoryStorageHostProofSHA(candidateSHA, requestReceiptSHA, resultJSON)
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_memory_storagehost_receipts
		(op_id, payload_sha256, writer_generation, action_id, user_id, actor_id, item_id,
		 candidate_sha256, request_receipt_sha256, effect_sha256, result_json, result_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		identity.OpID, identity.PayloadSHA256, identity.WriterGeneration, string(receipt.ActionID), receipt.UserID,
		receipt.ActorID, receipt.ItemID, candidateSHA, requestReceiptSHA, effectSHA, string(resultJSON), resultSHA,
		time.Now().UTC().Format(timeFormatRFC3339Nano)); err != nil {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("insert knowledge memory storage-host receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("commit knowledge memory storage-host transaction: %w", err)
	}
	return result, nil
}

// LookupCreativeCandidateStorageHostOperationReceipt verifies the durable
// operation proof and its canonical candidate effect before returning it.
// Existing canonical effect rows without an operation proof are uncertain,
// not permission to execute the private mutation again.
func (s *SQLiteStore) LookupCreativeCandidateStorageHostOperationReceipt(ctx context.Context, identity KnowledgeMemoryStorageHostOperationIdentity, item domainkm.CreativeKnowledgeItem, receipt KnowledgeMemoryRequestReceipt) (CreativeCandidateStorageHostResult, bool, error) {
	if s == nil || s.db == nil {
		return CreativeCandidateStorageHostResult{}, false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	if err := validateKnowledgeMemoryStorageHostIdentity(identity); err != nil {
		return CreativeCandidateStorageHostResult{}, false, err
	}
	item, receipt, _, err := prepareCreativeCandidateWrite(item, receipt)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, false, err
	}
	candidateSHA, requestReceiptSHA, err := knowledgeMemoryCandidateEffectHashes(item, receipt)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, false, err
	}
	stored, found, err := findKnowledgeMemoryStorageHostReceipt(ctx, s.db, identity.OpID)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, false, err
	}
	if found {
		result, err := verifyKnowledgeMemoryStorageHostReceipt(ctx, s.db, stored, identity, item, receipt, candidateSHA, requestReceiptSHA)
		return result, err == nil, err
	}
	_, candidateFound, candidateErr := findCreativeCandidateByID(ctx, s.db, item.ItemID)
	_, requestFound, requestErr := findKnowledgeMemoryActionReceipt(ctx, s.db, receipt.ActionID, "")
	if candidateErr != nil || requestErr != nil {
		return CreativeCandidateStorageHostResult{}, false, fmt.Errorf("verify missing knowledge memory storage-host receipt")
	}
	if candidateFound || requestFound {
		return CreativeCandidateStorageHostResult{}, false, fmt.Errorf("knowledge memory storage-host receipt missing for an existing effect")
	}
	return CreativeCandidateStorageHostResult{}, false, nil
}

type knowledgeMemoryStorageHostReceipt struct {
	opID                 string
	payloadSHA256        string
	writerGeneration     int64
	actionID             string
	userID               string
	actorID              string
	itemID               string
	candidateSHA256      string
	requestReceiptSHA256 string
	effectSHA256         string
	resultJSON           string
	resultSHA256         string
}

func findKnowledgeMemoryStorageHostReceipt(ctx context.Context, queryer knowledgeMemoryQueryer, opID string) (knowledgeMemoryStorageHostReceipt, bool, error) {
	var stored knowledgeMemoryStorageHostReceipt
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT op_id, payload_sha256, writer_generation, action_id, user_id, actor_id, item_id,
		candidate_sha256, request_receipt_sha256, effect_sha256, result_json, result_sha256, created_at
		FROM knowledge_memory_storagehost_receipts WHERE op_id = ?`, opID).Scan(
		&stored.opID, &stored.payloadSHA256, &stored.writerGeneration, &stored.actionID, &stored.userID, &stored.actorID,
		&stored.itemID, &stored.candidateSHA256, &stored.requestReceiptSHA256, &stored.effectSHA256,
		&stored.resultJSON, &stored.resultSHA256, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return knowledgeMemoryStorageHostReceipt{}, false, nil
	}
	if err != nil {
		return knowledgeMemoryStorageHostReceipt{}, false, fmt.Errorf("find knowledge memory storage-host receipt: %w", err)
	}
	if _, err := time.Parse(timeFormatRFC3339Nano, createdAt); err != nil {
		return knowledgeMemoryStorageHostReceipt{}, false, fmt.Errorf("invalid knowledge memory storage-host receipt timestamp")
	}
	return stored, true, nil
}

func verifyKnowledgeMemoryStorageHostReceipt(ctx context.Context, queryer knowledgeMemoryQueryer, stored knowledgeMemoryStorageHostReceipt, identity KnowledgeMemoryStorageHostOperationIdentity, item domainkm.CreativeKnowledgeItem, receipt KnowledgeMemoryRequestReceipt, candidateSHA, requestReceiptSHA string) (CreativeCandidateStorageHostResult, error) {
	if stored.opID != identity.OpID || stored.payloadSHA256 != identity.PayloadSHA256 || stored.writerGeneration != identity.WriterGeneration ||
		stored.actionID != string(receipt.ActionID) || stored.userID != receipt.UserID || stored.actorID != receipt.ActorID || stored.itemID != receipt.ItemID ||
		stored.candidateSHA256 != candidateSHA || stored.requestReceiptSHA256 != requestReceiptSHA {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("%w: storage-host operation %q", ErrKnowledgeMemoryRequestConflict, identity.OpID)
	}
	result, err := decodeKnowledgeMemoryStorageHostResult([]byte(stored.resultJSON), stored.resultSHA256)
	if err != nil {
		return CreativeCandidateStorageHostResult{}, err
	}
	if stored.effectSHA256 != knowledgeMemoryStorageHostProofSHA(candidateSHA, requestReceiptSHA, []byte(stored.resultJSON)) {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("knowledge memory storage-host effect proof mismatch")
	}
	actualItem, itemFound, err := findCreativeCandidateByID(ctx, queryer, receipt.ItemID)
	if err != nil || !itemFound || !creativeCandidateEqual(actualItem, item) {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("knowledge memory storage-host candidate effect is missing or altered")
	}
	actualReceipt, receiptFound, err := findKnowledgeMemoryActionReceipt(ctx, queryer, receipt.ActionID, "")
	if err != nil || !receiptFound || !knowledgeMemoryReceiptBindingEqual(actualReceipt, receipt) {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("knowledge memory storage-host request receipt is missing or altered")
	}
	actualCandidateSHA, actualRequestSHA, err := knowledgeMemoryCandidateEffectHashes(actualItem, actualReceipt)
	if err != nil || actualCandidateSHA != stored.candidateSHA256 || actualRequestSHA != stored.requestReceiptSHA256 {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("knowledge memory storage-host effect fingerprint mismatch")
	}
	return result, nil
}

func validateKnowledgeMemoryStorageHostIdentity(identity KnowledgeMemoryStorageHostOperationIdentity) error {
	if !knowledgeMemoryStorageHostOpIDPattern.MatchString(identity.OpID) || identity.WriterGeneration <= 0 || len(identity.PayloadSHA256) != sha256.Size*2 || strings.ToLower(identity.PayloadSHA256) != identity.PayloadSHA256 {
		return fmt.Errorf("knowledge memory storage-host operation identity is invalid")
	}
	if _, err := hex.DecodeString(identity.PayloadSHA256); err != nil {
		return fmt.Errorf("knowledge memory storage-host operation identity is invalid")
	}
	return nil
}

func knowledgeMemoryCandidateEffectHashes(item domainkm.CreativeKnowledgeItem, receipt KnowledgeMemoryRequestReceipt) (string, string, error) {
	candidateJSON, err := json.Marshal(item)
	if err != nil {
		return "", "", err
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return "", "", err
	}
	candidateHash := sha256.Sum256(candidateJSON)
	receiptHash := sha256.Sum256(receiptJSON)
	return hex.EncodeToString(candidateHash[:]), hex.EncodeToString(receiptHash[:]), nil
}

func knowledgeMemoryStorageHostProofSHA(candidateSHA, requestReceiptSHA string, resultJSON []byte) string {
	proofHash := sha256.New()
	_, _ = io.WriteString(proofHash, candidateSHA)
	_, _ = io.WriteString(proofHash, "\x00")
	_, _ = io.WriteString(proofHash, requestReceiptSHA)
	_, _ = io.WriteString(proofHash, "\x00")
	_, _ = proofHash.Write(resultJSON)
	return hex.EncodeToString(proofHash.Sum(nil))
}

func encodeKnowledgeMemoryStorageHostResult(result CreativeCandidateStorageHostResult) ([]byte, string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(raw)
	return raw, hex.EncodeToString(hash[:]), nil
}

func decodeKnowledgeMemoryStorageHostResult(raw []byte, wantSHA string) (CreativeCandidateStorageHostResult, error) {
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != wantSHA {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("knowledge memory storage-host result fingerprint mismatch")
	}
	var result CreativeCandidateStorageHostResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("knowledge memory storage-host result is malformed")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return CreativeCandidateStorageHostResult{}, fmt.Errorf("knowledge memory storage-host result has trailing data")
	}
	return result, nil
}
