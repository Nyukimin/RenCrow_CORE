package skillgovernance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

var skillStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var ErrSkillStorageHostConflict = errors.New("skill governance storage-host operation conflict")

type StorageHostOperationIdentity struct {
	OpID             string `json:"op_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	WriterGeneration int64  `json:"writer_generation"`
}

type StorageHostSaveKind string

const (
	StorageHostSaveManifest     StorageHostSaveKind = "save_skill_manifest"
	StorageHostSaveTrigger      StorageHostSaveKind = "save_skill_trigger_log"
	StorageHostSaveChange       StorageHostSaveKind = "save_skill_change_log"
	StorageHostSaveContribution StorageHostSaveKind = "save_contribution_gate_log"
	StorageHostSaveExternalPR   StorageHostSaveKind = "save_external_pr_submit_record"
	StorageHostSaveTranscript   StorageHostSaveKind = "save_coder_transcript_entry"
)

type StorageHostSave struct {
	Kind         StorageHostSaveKind
	Manifest     *domainskill.SkillManifest
	Trigger      *domainskill.SkillTriggerLog
	Change       *domainskill.SkillChangeLog
	Contribution *domainskill.ContributionGateLog
	ExternalPR   *domainskill.ExternalPRSubmitRecord
	Transcript   *domainskill.CoderTranscriptEntry
}

type skillStorageHostEffect struct {
	operation StorageHostSaveKind
	effectID  string
	secondary string
	timestamp string
	itemJSON  []byte
	payload   []byte
}

type skillStorageHostEffectEnvelope struct {
	EffectID  string          `json:"effect_id"`
	Secondary string          `json:"secondary,omitempty"`
	Timestamp string          `json:"timestamp"`
	Item      json.RawMessage `json:"item"`
}

func (s *SQLiteStore) SaveForStorageHostOperation(ctx context.Context, identity StorageHostOperationIdentity, mutation StorageHostSave) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("skill governance sqlite store is closed")
	}
	if err := validateSkillStorageHostIdentity(identity); err != nil {
		return err
	}
	effect, err := prepareSkillStorageHostEffect(mutation)
	if err != nil {
		return err
	}
	effectHash := sha256.Sum256(effect.payload)
	effectSHA := hex.EncodeToString(effectHash[:])
	resultJSON := []byte("null")
	resultHash := sha256.Sum256(resultJSON)
	resultSHA := hex.EncodeToString(resultHash[:])
	proofSHA := skillStorageHostProofSHA(string(effect.operation), effect.effectID, effectSHA, resultJSON)

	s.mu.Lock()
	defer s.mu.Unlock()
	stored, found, err := findSkillStorageHostReceipt(ctx, s.db, identity.OpID)
	if err != nil {
		return err
	}
	if found {
		return verifySkillStorageHostReceipt(ctx, s.db, stored, identity, effect)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveSkillStorageHostEffect(ctx, tx, effect); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO skill_storagehost_receipt
		(op_id, operation, payload_sha256, writer_generation, effect_id, effect_sha256, proof_sha256, result_json, result_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, string(effect.operation), identity.PayloadSHA256, identity.WriterGeneration, effect.effectID, effectSHA, proofSHA, string(resultJSON), resultSHA, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) LookupStorageHostOperationReceipt(ctx context.Context, identity StorageHostOperationIdentity, mutation StorageHostSave) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("skill governance sqlite store is closed")
	}
	if err := validateSkillStorageHostIdentity(identity); err != nil {
		return false, err
	}
	effect, err := prepareSkillStorageHostEffect(mutation)
	if err != nil {
		return false, err
	}
	stored, found, err := findSkillStorageHostReceipt(ctx, s.db, identity.OpID)
	if err != nil {
		return false, err
	}
	if found {
		if err := verifySkillStorageHostReceipt(ctx, s.db, stored, identity, effect); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, exists, err := loadSkillStorageHostEffect(ctx, s.db, effect.operation, effect.effectID); err != nil {
		return false, err
	} else if exists {
		return false, fmt.Errorf("skill governance storage-host proof missing for existing effect")
	}
	return false, nil
}

type skillStorageHostReceipt struct {
	opID, operation, payloadSHA256, effectID, effectSHA256, proofSHA256, resultJSON, resultSHA256 string
	writerGeneration                                                                              int64
}

func findSkillStorageHostReceipt(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opID string) (skillStorageHostReceipt, bool, error) {
	var v skillStorageHostReceipt
	var created string
	err := q.QueryRowContext(ctx, `SELECT op_id, operation, payload_sha256, writer_generation, effect_id, effect_sha256, proof_sha256, result_json, result_sha256, created_at FROM skill_storagehost_receipt WHERE op_id=?`, opID).Scan(&v.opID, &v.operation, &v.payloadSHA256, &v.writerGeneration, &v.effectID, &v.effectSHA256, &v.proofSHA256, &v.resultJSON, &v.resultSHA256, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
		return v, false, fmt.Errorf("skill governance storage-host receipt timestamp is invalid")
	}
	return v, true, nil
}

func verifySkillStorageHostReceipt(ctx context.Context, db *sql.DB, stored skillStorageHostReceipt, identity StorageHostOperationIdentity, effect skillStorageHostEffect) error {
	h := sha256.Sum256(effect.payload)
	effectSHA := hex.EncodeToString(h[:])
	if stored.opID != identity.OpID || stored.operation != string(effect.operation) || stored.payloadSHA256 != identity.PayloadSHA256 || stored.writerGeneration != identity.WriterGeneration || stored.effectID != effect.effectID || stored.effectSHA256 != effectSHA {
		return fmt.Errorf("%w: op_id %q", ErrSkillStorageHostConflict, identity.OpID)
	}
	rh := sha256.Sum256([]byte(stored.resultJSON))
	if stored.resultJSON != "null" || stored.resultSHA256 != hex.EncodeToString(rh[:]) || stored.proofSHA256 != skillStorageHostProofSHA(stored.operation, stored.effectID, stored.effectSHA256, []byte(stored.resultJSON)) {
		return fmt.Errorf("skill governance storage-host result proof is invalid")
	}
	actual, found, err := loadSkillStorageHostEffect(ctx, db, effect.operation, effect.effectID)
	if err != nil || !found || !bytes.Equal(actual, effect.payload) {
		return fmt.Errorf("skill governance storage-host effect is missing or altered")
	}
	return nil
}

func prepareSkillStorageHostEffect(m StorageHostSave) (skillStorageHostEffect, error) {
	count := 0
	for _, present := range []bool{m.Manifest != nil, m.Trigger != nil, m.Change != nil, m.Contribution != nil, m.ExternalPR != nil, m.Transcript != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return skillStorageHostEffect{}, fmt.Errorf("skill governance storage-host save must contain exactly one value")
	}
	var id string
	var secondary string
	var timestamp time.Time
	var item any
	var validate error
	switch m.Kind {
	case StorageHostSaveManifest:
		if m.Manifest == nil {
			return skillStorageHostEffect{}, fmt.Errorf("skill governance storage-host save kind mismatch")
		}
		id, timestamp, item, validate = m.Manifest.SkillID, m.Manifest.UpdatedAt, *m.Manifest, domainskill.ValidateSkillManifest(*m.Manifest)
	case StorageHostSaveTrigger:
		if m.Trigger == nil {
			return skillStorageHostEffect{}, fmt.Errorf("skill governance storage-host save kind mismatch")
		}
		id, secondary, timestamp, item, validate = m.Trigger.EventID, m.Trigger.SkillID, m.Trigger.CreatedAt, *m.Trigger, domainskill.ValidateSkillTriggerLog(*m.Trigger)
	case StorageHostSaveChange:
		if m.Change == nil {
			return skillStorageHostEffect{}, fmt.Errorf("skill governance storage-host save kind mismatch")
		}
		id, secondary, timestamp, item, validate = m.Change.ChangeID, m.Change.SkillID, m.Change.CreatedAt, *m.Change, domainskill.ValidateSkillChangeLog(*m.Change)
	case StorageHostSaveContribution:
		if m.Contribution == nil {
			return skillStorageHostEffect{}, fmt.Errorf("skill governance storage-host save kind mismatch")
		}
		id, secondary, timestamp, item, validate = m.Contribution.EventID, m.Contribution.Repo, m.Contribution.CreatedAt, *m.Contribution, domainskill.ValidateContributionGateLog(*m.Contribution)
	case StorageHostSaveExternalPR:
		if m.ExternalPR == nil {
			return skillStorageHostEffect{}, fmt.Errorf("skill governance storage-host save kind mismatch")
		}
		id, secondary, timestamp, item, validate = string(m.ExternalPR.ActionID), m.ExternalPR.Repo, m.ExternalPR.CreatedAt, *m.ExternalPR, domainskill.ValidateExternalPRSubmitRecord(*m.ExternalPR)
	case StorageHostSaveTranscript:
		if m.Transcript == nil {
			return skillStorageHostEffect{}, fmt.Errorf("skill governance storage-host save kind mismatch")
		}
		id, secondary, timestamp, item, validate = m.Transcript.EventID, m.Transcript.TaskID.String(), m.Transcript.CreatedAt, *m.Transcript, domainskill.ValidateCoderTranscriptEntry(*m.Transcript)
	default:
		return skillStorageHostEffect{}, fmt.Errorf("unsupported skill governance storage-host save kind")
	}
	if validate != nil {
		return skillStorageHostEffect{}, validate
	}
	itemJSON, err := json.Marshal(item)
	if err != nil {
		return skillStorageHostEffect{}, err
	}
	timestampText := timestamp.Format(timeFormatRFC3339Nano)
	payload, err := marshalSkillStorageHostEffect(id, secondary, timestampText, itemJSON)
	return skillStorageHostEffect{operation: m.Kind, effectID: id, secondary: secondary, timestamp: timestampText, itemJSON: itemJSON, payload: payload}, err
}

func saveSkillStorageHostEffect(ctx context.Context, tx *sql.Tx, effect skillStorageHostEffect) error {
	var query string
	switch effect.operation {
	case StorageHostSaveManifest:
		query = `INSERT OR REPLACE INTO skill_registry (skill_id, updated_at, payload) VALUES (?, ?, ?)`
		_, err := tx.ExecContext(ctx, query, effect.effectID, effect.timestamp, string(effect.itemJSON))
		return err
	case StorageHostSaveTrigger:
		query = `INSERT OR REPLACE INTO skill_trigger_log (event_id, skill_id, created_at, payload) VALUES (?, ?, ?, ?)`
	case StorageHostSaveChange:
		query = `INSERT OR REPLACE INTO skill_change_log (change_id, skill_id, created_at, payload) VALUES (?, ?, ?, ?)`
	case StorageHostSaveContribution:
		query = `INSERT OR REPLACE INTO contribution_gate_log (event_id, repo, created_at, payload) VALUES (?, ?, ?, ?)`
	case StorageHostSaveExternalPR:
		query = `INSERT OR REPLACE INTO external_pr_submit_log (action_id, repo, created_at, payload) VALUES (?, ?, ?, ?)`
	case StorageHostSaveTranscript:
		query = `INSERT OR REPLACE INTO coder_transcript_log (event_id, task_id, created_at, payload) VALUES (?, ?, ?, ?)`
	default:
		return fmt.Errorf("unsupported skill governance storage-host save kind")
	}
	_, err := tx.ExecContext(ctx, query, effect.effectID, effect.secondary, effect.timestamp, string(effect.itemJSON))
	return err
}

func loadSkillStorageHostEffect(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, operation StorageHostSaveKind, effectID string) ([]byte, bool, error) {
	var query string
	manifest := false
	switch operation {
	case StorageHostSaveManifest:
		query, manifest = `SELECT updated_at, payload FROM skill_registry WHERE skill_id=?`, true
	case StorageHostSaveTrigger:
		query = `SELECT skill_id, created_at, payload FROM skill_trigger_log WHERE event_id=?`
	case StorageHostSaveChange:
		query = `SELECT skill_id, created_at, payload FROM skill_change_log WHERE change_id=?`
	case StorageHostSaveContribution:
		query = `SELECT repo, created_at, payload FROM contribution_gate_log WHERE event_id=?`
	case StorageHostSaveExternalPR:
		query = `SELECT repo, created_at, payload FROM external_pr_submit_log WHERE action_id=?`
	case StorageHostSaveTranscript:
		query = `SELECT task_id, created_at, payload FROM coder_transcript_log WHERE event_id=?`
	default:
		return nil, false, fmt.Errorf("unsupported skill governance storage-host save kind")
	}
	var secondary, timestamp, itemJSON string
	row := q.QueryRowContext(ctx, query, effectID)
	var err error
	if manifest {
		err = row.Scan(&timestamp, &itemJSON)
	} else {
		err = row.Scan(&secondary, &timestamp, &itemJSON)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	payload, err := marshalSkillStorageHostEffect(effectID, secondary, timestamp, []byte(itemJSON))
	return payload, err == nil, err
}

func marshalSkillStorageHostEffect(effectID, secondary, timestamp string, itemJSON []byte) ([]byte, error) {
	return json.Marshal(skillStorageHostEffectEnvelope{EffectID: effectID, Secondary: secondary, Timestamp: timestamp, Item: json.RawMessage(itemJSON)})
}

func validateSkillStorageHostIdentity(v StorageHostOperationIdentity) error {
	if !skillStorageHostOpIDPattern.MatchString(v.OpID) || v.WriterGeneration <= 0 || len(v.PayloadSHA256) != sha256.Size*2 || strings.ToLower(v.PayloadSHA256) != v.PayloadSHA256 {
		return fmt.Errorf("skill governance storage-host identity is invalid")
	}
	if _, err := hex.DecodeString(v.PayloadSHA256); err != nil {
		return fmt.Errorf("skill governance storage-host identity is invalid")
	}
	return nil
}

func skillStorageHostProofSHA(op, id, effect string, result []byte) string {
	h := sha256.New()
	for _, v := range []string{op, id, effect} {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(result)
	return hex.EncodeToString(h.Sum(nil))
}

func validateSkillStorageHostReceiptSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info('skill_storagehost_receipt')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := []string{"op_id", "operation", "payload_sha256", "writer_generation", "effect_id", "effect_sha256", "proof_sha256", "result_json", "result_sha256", "created_at"}
	var got []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		expectedType := "TEXT"
		if name == "writer_generation" {
			expectedType = "INTEGER"
		}
		if cid != len(got) || strings.ToUpper(typ) != expectedType || notNull != 1 || (name == "op_id" && pk != 1) || (name != "op_id" && pk != 0) {
			return fmt.Errorf("skill governance storage-host receipt schema is invalid")
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(got) != len(want) {
		return fmt.Errorf("skill governance storage-host receipt schema is invalid")
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("skill governance storage-host receipt schema is invalid")
		}
	}
	return nil
}
