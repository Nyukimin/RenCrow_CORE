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
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

// KnowledgeMemoryStorageHostSaveKind is a closed owner mutation discriminator.
type KnowledgeMemoryStorageHostSaveKind string

const (
	KnowledgeMemorySavePersonalArchive   KnowledgeMemoryStorageHostSaveKind = "save_personal_archive"
	KnowledgeMemorySaveCreativeKnowledge KnowledgeMemoryStorageHostSaveKind = "save_creative_knowledge"
	KnowledgeMemorySaveNewsKnowledge     KnowledgeMemoryStorageHostSaveKind = "save_news_knowledge"
	KnowledgeMemorySaveDailyRule         KnowledgeMemoryStorageHostSaveKind = "save_daily_rule"
	KnowledgeMemorySaveTemporalMarker    KnowledgeMemoryStorageHostSaveKind = "save_temporal_marker"
	KnowledgeMemorySaveDreamRun          KnowledgeMemoryStorageHostSaveKind = "save_dream_run"
)

// KnowledgeMemoryStorageHostSave is a closed typed union. Exactly one item,
// matching Kind, is accepted.
type KnowledgeMemoryStorageHostSave struct {
	Kind              KnowledgeMemoryStorageHostSaveKind
	PersonalArchive   *domainkm.PersonalArchiveEntry
	CreativeKnowledge *domainkm.CreativeKnowledgeItem
	NewsKnowledge     *domainkm.NewsKnowledgeItem
	DailyRule         *domainkm.DailyIntakeRule
	TemporalMarker    *domainkm.TemporalMemoryMarker
	DreamRun          *domainkm.DreamConsolidationRun
}

// SaveKnowledgeMemoryForStorageHostOperation stores one canonical item and
// its storage-host proof in the same SQLite transaction.
func (s *SQLiteStore) SaveKnowledgeMemoryForStorageHostOperation(ctx context.Context, identity KnowledgeMemoryStorageHostOperationIdentity, mutation KnowledgeMemoryStorageHostSave) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge memory sqlite store is closed")
	}
	if err := validateKnowledgeMemoryStorageHostIdentity(identity); err != nil {
		return err
	}
	effectID, effectJSON, err := validateKnowledgeMemoryStorageHostSave(mutation)
	if err != nil {
		return err
	}
	effectHash := sha256.Sum256(effectJSON)
	effectSHA := hex.EncodeToString(effectHash[:])
	resultJSON := []byte("null")
	resultHash := sha256.Sum256(resultJSON)
	resultSHA := hex.EncodeToString(resultHash[:])
	proofSHA := knowledgeMemorySaveProofSHA(string(mutation.Kind), effectID, effectSHA, resultJSON)

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin knowledge memory storage-host save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, found, err := findKnowledgeMemoryStorageHostSaveReceipt(ctx, tx, identity.OpID)
	if err != nil {
		return err
	}
	if found {
		if err := verifyKnowledgeMemoryStorageHostSaveReceipt(ctx, tx, stored, identity, mutation, effectID, effectJSON); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := applyKnowledgeMemoryStorageHostSaveTx(ctx, tx, mutation, effectID, effectJSON); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_memory_storagehost_save_receipts
		(op_id, operation, payload_sha256, writer_generation, effect_id, effect_sha256, proof_sha256, result_json, result_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		identity.OpID, string(mutation.Kind), identity.PayloadSHA256, identity.WriterGeneration, effectID, effectSHA,
		proofSHA, string(resultJSON), resultSHA, time.Now().UTC().Format(timeFormatRFC3339Nano)); err != nil {
		return fmt.Errorf("insert knowledge memory storage-host save receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit knowledge memory storage-host save: %w", err)
	}
	return nil
}

// LookupKnowledgeMemoryStorageHostOperationReceipt validates the proof and
// exact canonical effect. An existing effect without proof is uncertain.
func (s *SQLiteStore) LookupKnowledgeMemoryStorageHostOperationReceipt(ctx context.Context, identity KnowledgeMemoryStorageHostOperationIdentity, mutation KnowledgeMemoryStorageHostSave) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	if err := validateKnowledgeMemoryStorageHostIdentity(identity); err != nil {
		return false, err
	}
	effectID, effectJSON, err := validateKnowledgeMemoryStorageHostSave(mutation)
	if err != nil {
		return false, err
	}
	stored, found, err := findKnowledgeMemoryStorageHostSaveReceipt(ctx, s.db, identity.OpID)
	if err != nil {
		return false, err
	}
	if found {
		if err := verifyKnowledgeMemoryStorageHostSaveReceipt(ctx, s.db, stored, identity, mutation, effectID, effectJSON); err != nil {
			return false, err
		}
		return true, nil
	}
	canonical, exists, err := loadKnowledgeMemoryStorageHostEffect(ctx, s.db, mutation.Kind, effectID)
	if err != nil {
		return false, err
	}
	if exists {
		_ = canonical
		return false, fmt.Errorf("knowledge memory storage-host save proof missing for existing effect")
	}
	return false, nil
}

type knowledgeMemoryStorageHostSaveReceipt struct {
	opID, operation, payloadSHA256, effectID, effectSHA256 string
	proofSHA256, resultJSON, resultSHA256                  string
	writerGeneration                                       int64
}

func findKnowledgeMemoryStorageHostSaveReceipt(ctx context.Context, queryer knowledgeMemoryQueryer, opID string) (knowledgeMemoryStorageHostSaveReceipt, bool, error) {
	var stored knowledgeMemoryStorageHostSaveReceipt
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT op_id, operation, payload_sha256, writer_generation, effect_id,
		effect_sha256, proof_sha256, result_json, result_sha256, created_at
		FROM knowledge_memory_storagehost_save_receipts WHERE op_id = ?`, opID).Scan(
		&stored.opID, &stored.operation, &stored.payloadSHA256, &stored.writerGeneration, &stored.effectID,
		&stored.effectSHA256, &stored.proofSHA256, &stored.resultJSON, &stored.resultSHA256, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return knowledgeMemoryStorageHostSaveReceipt{}, false, nil
	}
	if err != nil {
		return knowledgeMemoryStorageHostSaveReceipt{}, false, fmt.Errorf("find knowledge memory storage-host save receipt: %w", err)
	}
	if _, err := time.Parse(timeFormatRFC3339Nano, createdAt); err != nil {
		return knowledgeMemoryStorageHostSaveReceipt{}, false, fmt.Errorf("invalid knowledge memory storage-host save timestamp")
	}
	return stored, true, nil
}

func verifyKnowledgeMemoryStorageHostSaveReceipt(ctx context.Context, queryer knowledgeMemoryQueryer, stored knowledgeMemoryStorageHostSaveReceipt, identity KnowledgeMemoryStorageHostOperationIdentity, mutation KnowledgeMemoryStorageHostSave, effectID string, effectJSON []byte) error {
	effectHash := sha256.Sum256(effectJSON)
	effectSHA := hex.EncodeToString(effectHash[:])
	if stored.opID != identity.OpID || stored.operation != string(mutation.Kind) || stored.payloadSHA256 != identity.PayloadSHA256 ||
		stored.writerGeneration != identity.WriterGeneration || stored.effectID != effectID || stored.effectSHA256 != effectSHA {
		return fmt.Errorf("%w: storage-host operation %q", ErrKnowledgeMemoryRequestConflict, identity.OpID)
	}
	resultHash := sha256.Sum256([]byte(stored.resultJSON))
	if stored.resultJSON != "null" || stored.resultSHA256 != hex.EncodeToString(resultHash[:]) ||
		stored.proofSHA256 != knowledgeMemorySaveProofSHA(stored.operation, effectID, effectSHA, []byte(stored.resultJSON)) {
		return fmt.Errorf("knowledge memory storage-host save result proof is invalid")
	}
	canonical, found, err := loadKnowledgeMemoryStorageHostEffect(ctx, queryer, mutation.Kind, effectID)
	if err != nil || !found || !bytes.Equal(canonical, effectJSON) {
		return fmt.Errorf("knowledge memory storage-host save effect is missing or altered")
	}
	return nil
}

func validateKnowledgeMemoryStorageHostSave(mutation KnowledgeMemoryStorageHostSave) (string, []byte, error) {
	count := 0
	for _, present := range []bool{mutation.PersonalArchive != nil, mutation.CreativeKnowledge != nil, mutation.NewsKnowledge != nil, mutation.DailyRule != nil, mutation.TemporalMarker != nil, mutation.DreamRun != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return "", nil, fmt.Errorf("knowledge memory storage-host save must contain exactly one item")
	}
	var effectID string
	var item any
	var err error
	switch mutation.Kind {
	case KnowledgeMemorySavePersonalArchive:
		if mutation.PersonalArchive == nil {
			return "", nil, fmt.Errorf("knowledge memory storage-host save kind mismatch")
		}
		effectID, item, err = mutation.PersonalArchive.EntryID, *mutation.PersonalArchive, domainkm.ValidatePersonalArchiveEntry(*mutation.PersonalArchive)
	case KnowledgeMemorySaveCreativeKnowledge:
		if mutation.CreativeKnowledge == nil {
			return "", nil, fmt.Errorf("knowledge memory storage-host save kind mismatch")
		}
		effectID, item, err = mutation.CreativeKnowledge.ItemID, *mutation.CreativeKnowledge, domainkm.ValidateCreativeKnowledgeItem(*mutation.CreativeKnowledge)
	case KnowledgeMemorySaveNewsKnowledge:
		if mutation.NewsKnowledge == nil {
			return "", nil, fmt.Errorf("knowledge memory storage-host save kind mismatch")
		}
		effectID, item, err = mutation.NewsKnowledge.ItemID, *mutation.NewsKnowledge, domainkm.ValidateNewsKnowledgeItem(*mutation.NewsKnowledge)
	case KnowledgeMemorySaveDailyRule:
		if mutation.DailyRule == nil {
			return "", nil, fmt.Errorf("knowledge memory storage-host save kind mismatch")
		}
		effectID, item, err = mutation.DailyRule.RuleID, *mutation.DailyRule, domainkm.ValidateDailyIntakeRule(*mutation.DailyRule)
	case KnowledgeMemorySaveTemporalMarker:
		if mutation.TemporalMarker == nil {
			return "", nil, fmt.Errorf("knowledge memory storage-host save kind mismatch")
		}
		effectID, item, err = mutation.TemporalMarker.MarkerID, *mutation.TemporalMarker, domainkm.ValidateTemporalMemoryMarker(*mutation.TemporalMarker)
	case KnowledgeMemorySaveDreamRun:
		if mutation.DreamRun == nil {
			return "", nil, fmt.Errorf("knowledge memory storage-host save kind mismatch")
		}
		effectID, item, err = string(mutation.DreamRun.RunID), *mutation.DreamRun, domainkm.ValidateDreamConsolidationRun(*mutation.DreamRun)
	default:
		return "", nil, fmt.Errorf("unsupported knowledge memory storage-host save kind")
	}
	if err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return "", nil, err
	}
	return effectID, raw, nil
}

func applyKnowledgeMemoryStorageHostSaveTx(ctx context.Context, tx *sql.Tx, mutation KnowledgeMemoryStorageHostSave, effectID string, effectJSON []byte) error {
	createdAt := ""
	var err error
	switch mutation.Kind {
	case KnowledgeMemorySavePersonalArchive:
		createdAt = mutation.PersonalArchive.CreatedAt.Format(timeFormatRFC3339Nano)
		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO personal_archive (entry_id, user_id, created_at, payload) VALUES (?, ?, ?, ?)`, effectID, mutation.PersonalArchive.UserID, createdAt, string(effectJSON))
	case KnowledgeMemorySaveCreativeKnowledge:
		return saveKnowledgeItemWithProjectionTx(ctx, tx, creativeKnowledgeRecordType, effectID, mutation.CreativeKnowledge.CreatedAt.Format(timeFormatRFC3339Nano), string(effectJSON), creativeSearchProjection(*mutation.CreativeKnowledge))
	case KnowledgeMemorySaveNewsKnowledge:
		return saveKnowledgeItemWithProjectionTx(ctx, tx, newsKnowledgeRecordType, effectID, mutation.NewsKnowledge.CreatedAt.Format(timeFormatRFC3339Nano), string(effectJSON), newsSearchProjection(*mutation.NewsKnowledge))
	case KnowledgeMemorySaveDailyRule:
		createdAt = mutation.DailyRule.CreatedAt.Format(timeFormatRFC3339Nano)
		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO daily_intake_rule (rule_id, user_id, created_at, payload) VALUES (?, ?, ?, ?)`, effectID, mutation.DailyRule.UserID, createdAt, string(effectJSON))
	case KnowledgeMemorySaveTemporalMarker:
		createdAt = mutation.TemporalMarker.CreatedAt.Format(timeFormatRFC3339Nano)
		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO temporal_memory_marker (marker_id, user_id, created_at, payload) VALUES (?, ?, ?, ?)`, effectID, mutation.TemporalMarker.UserID, createdAt, string(effectJSON))
	case KnowledgeMemorySaveDreamRun:
		createdAt = mutation.DreamRun.CreatedAt.Format(timeFormatRFC3339Nano)
		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO dream_consolidation_run (run_id, created_at, payload) VALUES (?, ?, ?)`, effectID, createdAt, string(effectJSON))
	}
	if err != nil {
		return fmt.Errorf("save knowledge memory storage-host effect: %w", err)
	}
	return nil
}

func loadKnowledgeMemoryStorageHostEffect(ctx context.Context, queryer knowledgeMemoryQueryer, kind KnowledgeMemoryStorageHostSaveKind, effectID string) ([]byte, bool, error) {
	query := ""
	switch kind {
	case KnowledgeMemorySavePersonalArchive:
		query = `SELECT payload FROM personal_archive WHERE entry_id = ?`
	case KnowledgeMemorySaveCreativeKnowledge:
		query = `SELECT payload FROM creative_knowledge WHERE item_id = ?`
	case KnowledgeMemorySaveNewsKnowledge:
		query = `SELECT payload FROM news_knowledge WHERE item_id = ?`
	case KnowledgeMemorySaveDailyRule:
		query = `SELECT payload FROM daily_intake_rule WHERE rule_id = ?`
	case KnowledgeMemorySaveTemporalMarker:
		query = `SELECT payload FROM temporal_memory_marker WHERE marker_id = ?`
	case KnowledgeMemorySaveDreamRun:
		query = `SELECT payload FROM dream_consolidation_run WHERE run_id = ?`
	default:
		return nil, false, fmt.Errorf("unsupported knowledge memory storage-host save kind")
	}
	var raw []byte
	err := queryer.QueryRowContext(ctx, query, effectID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read knowledge memory storage-host effect: %w", err)
	}
	return raw, true, nil
}

func knowledgeMemorySaveProofSHA(operation, effectID, effectSHA string, resultJSON []byte) string {
	proof := sha256.New()
	for _, value := range []string{operation, effectID, effectSHA} {
		_, _ = proof.Write([]byte(value))
		_, _ = proof.Write([]byte{0})
	}
	_, _ = proof.Write(resultJSON)
	return hex.EncodeToString(proof.Sum(nil))
}
