package sandbox

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

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

var sandboxStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var ErrSandboxStorageHostConflict = errors.New("sandbox storage-host operation conflict")

type StorageHostOperationIdentity struct {
	OpID             string `json:"op_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	WriterGeneration int64  `json:"writer_generation"`
}
type StorageHostSaveKind string

const (
	StorageHostSaveSandbox   StorageHostSaveKind = "save_sandbox"
	StorageHostSaveArtifact  StorageHostSaveKind = "save_sandbox_artifact"
	StorageHostSavePromotion StorageHostSaveKind = "save_promotion_request"
	StorageHostSaveGateLog   StorageHostSaveKind = "save_promotion_gate_log"
)

type StorageHostSave struct {
	Kind      StorageHostSaveKind
	Sandbox   *domainsandbox.SandboxRecord
	Artifact  *domainsandbox.SandboxArtifact
	Promotion *domainsandbox.PromotionRequest
	GateLog   *domainsandbox.PromotionGateLog
}

func (s *SQLiteStore) SaveForStorageHostOperation(ctx context.Context, identity StorageHostOperationIdentity, mutation StorageHostSave) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sandbox sqlite store is closed")
	}
	if err := validateSandboxStorageHostIdentity(identity); err != nil {
		return err
	}
	effectID, effectJSON, args, err := prepareSandboxStorageHostSave(mutation)
	if err != nil {
		return err
	}
	effectHash := sha256.Sum256(effectJSON)
	effectSHA := hex.EncodeToString(effectHash[:])
	resultJSON := []byte("null")
	resultHash := sha256.Sum256(resultJSON)
	resultSHA := hex.EncodeToString(resultHash[:])
	proofSHA := sandboxStorageHostProofSHA(string(mutation.Kind), effectID, effectSHA, resultJSON)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stored, found, err := findSandboxStorageHostReceipt(ctx, tx, identity.OpID)
	if err != nil {
		return err
	}
	if found {
		if err := verifySandboxStorageHostReceipt(ctx, tx, stored, identity, mutation, effectID, effectJSON); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := applySandboxStorageHostSave(ctx, tx, mutation.Kind, args, effectJSON); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sandbox_storagehost_receipt
		(op_id, operation, payload_sha256, writer_generation, effect_id, effect_sha256, proof_sha256, result_json, result_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, string(mutation.Kind), identity.PayloadSHA256,
		identity.WriterGeneration, effectID, effectSHA, proofSHA, string(resultJSON), resultSHA, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) LookupStorageHostOperationReceipt(ctx context.Context, identity StorageHostOperationIdentity, mutation StorageHostSave) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("sandbox sqlite store is closed")
	}
	if err := validateSandboxStorageHostIdentity(identity); err != nil {
		return false, err
	}
	effectID, effectJSON, _, err := prepareSandboxStorageHostSave(mutation)
	if err != nil {
		return false, err
	}
	stored, found, err := findSandboxStorageHostReceipt(ctx, s.db, identity.OpID)
	if err != nil {
		return false, err
	}
	if found {
		if err := verifySandboxStorageHostReceipt(ctx, s.db, stored, identity, mutation, effectID, effectJSON); err != nil {
			return false, err
		}
		return true, nil
	}
	_, exists, err := loadSandboxStorageHostEffect(ctx, s.db, mutation.Kind, effectID)
	if err != nil {
		return false, err
	}
	if exists {
		return false, fmt.Errorf("sandbox storage-host proof missing for existing effect")
	}
	return false, nil
}

type sandboxQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type sandboxStorageHostReceipt struct {
	opID, operation, payloadSHA256, effectID, effectSHA256 string
	proofSHA256, resultJSON, resultSHA256                  string
	writerGeneration                                       int64
}

func findSandboxStorageHostReceipt(ctx context.Context, q sandboxQueryer, opID string) (sandboxStorageHostReceipt, bool, error) {
	var v sandboxStorageHostReceipt
	var created string
	err := q.QueryRowContext(ctx, `SELECT op_id, operation, payload_sha256, writer_generation, effect_id, effect_sha256,
		proof_sha256, result_json, result_sha256, created_at FROM sandbox_storagehost_receipt WHERE op_id = ?`, opID).Scan(
		&v.opID, &v.operation, &v.payloadSHA256, &v.writerGeneration, &v.effectID, &v.effectSHA256, &v.proofSHA256, &v.resultJSON, &v.resultSHA256, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
		return v, false, fmt.Errorf("sandbox storage-host receipt timestamp is invalid")
	}
	return v, true, nil
}
func verifySandboxStorageHostReceipt(ctx context.Context, q sandboxQueryer, stored sandboxStorageHostReceipt, identity StorageHostOperationIdentity, mutation StorageHostSave, effectID string, effectJSON []byte) error {
	h := sha256.Sum256(effectJSON)
	effectSHA := hex.EncodeToString(h[:])
	if stored.opID != identity.OpID || stored.operation != string(mutation.Kind) || stored.payloadSHA256 != identity.PayloadSHA256 ||
		stored.writerGeneration != identity.WriterGeneration || stored.effectID != effectID || stored.effectSHA256 != effectSHA {
		return fmt.Errorf("%w: op_id %q", ErrSandboxStorageHostConflict, identity.OpID)
	}
	rh := sha256.Sum256([]byte(stored.resultJSON))
	if stored.resultJSON != "null" || stored.resultSHA256 != hex.EncodeToString(rh[:]) ||
		stored.proofSHA256 != sandboxStorageHostProofSHA(stored.operation, effectID, effectSHA, []byte(stored.resultJSON)) {
		return fmt.Errorf("sandbox storage-host result proof is invalid")
	}
	actual, found, err := loadSandboxStorageHostEffect(ctx, q, mutation.Kind, effectID)
	if err != nil || !found || !bytes.Equal(actual, effectJSON) {
		return fmt.Errorf("sandbox storage-host effect is missing or altered")
	}
	return nil
}

func prepareSandboxStorageHostSave(m StorageHostSave) (string, []byte, []any, error) {
	count := 0
	for _, p := range []bool{m.Sandbox != nil, m.Artifact != nil, m.Promotion != nil, m.GateLog != nil} {
		if p {
			count++
		}
	}
	if count != 1 {
		return "", nil, nil, fmt.Errorf("sandbox storage-host save must contain exactly one item")
	}
	var id string
	var item any
	var args []any
	var err error
	switch m.Kind {
	case StorageHostSaveSandbox:
		if m.Sandbox == nil {
			return "", nil, nil, fmt.Errorf("sandbox storage-host save kind mismatch")
		}
		v := *m.Sandbox
		id, item, err = v.SandboxID, v, domainsandbox.ValidateSandboxRecord(v)
		args = []any{v.SandboxID, v.WorkstreamID, v.GoalID, v.Type, v.Path, v.Status, v.CreatedAt.Format(timeFormatRFC3339Nano)}
	case StorageHostSaveArtifact:
		if m.Artifact == nil {
			return "", nil, nil, fmt.Errorf("sandbox storage-host save kind mismatch")
		}
		v := *m.Artifact
		id, item, err = v.ArtifactID, v, domainsandbox.ValidateSandboxArtifact(v)
		args = []any{v.ArtifactID, v.SandboxID, v.Type, v.FilePath, v.Status, v.CreatedAt.Format(timeFormatRFC3339Nano)}
	case StorageHostSavePromotion:
		if m.Promotion == nil {
			return "", nil, nil, fmt.Errorf("sandbox storage-host save kind mismatch")
		}
		v := *m.Promotion
		id, item, err = v.PromotionID, v, domainsandbox.ValidatePromotionRequest(v)
		args = []any{v.PromotionID, v.SandboxID, v.WorkstreamID, v.GoalID, v.TargetPath, domainsandbox.EvaluatePromotionRequest(v).Status, v.CreatedAt.Format(timeFormatRFC3339Nano)}
	case StorageHostSaveGateLog:
		if m.GateLog == nil {
			return "", nil, nil, fmt.Errorf("sandbox storage-host save kind mismatch")
		}
		v := *m.GateLog
		id, item, err = v.EventID, v, domainsandbox.ValidatePromotionGateLog(v)
		args = []any{v.EventID, v.PromotionID, v.GateStatus, v.CreatedAt.Format(timeFormatRFC3339Nano)}
	default:
		return "", nil, nil, fmt.Errorf("unsupported sandbox storage-host save kind")
	}
	if err != nil {
		return "", nil, nil, err
	}
	raw, err := json.Marshal(item)
	return id, raw, args, err
}
func applySandboxStorageHostSave(ctx context.Context, tx *sql.Tx, kind StorageHostSaveKind, args []any, effectJSON []byte) error {
	var query string
	switch kind {
	case StorageHostSaveSandbox:
		query = `INSERT OR REPLACE INTO sandbox_registry (sandbox_id, workstream_id, goal_id, sandbox_type, path, status, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	case StorageHostSaveArtifact:
		query = `INSERT OR REPLACE INTO sandbox_artifact (artifact_id, sandbox_id, artifact_type, file_path, status, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`
	case StorageHostSavePromotion:
		query = `INSERT OR REPLACE INTO sandbox_promotion_request (promotion_id, sandbox_id, workstream_id, goal_id, target_path, status, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	case StorageHostSaveGateLog:
		query = `INSERT OR REPLACE INTO promotion_gate_log (event_id, promotion_id, gate_status, created_at, payload) VALUES (?, ?, ?, ?, ?)`
	default:
		return fmt.Errorf("unsupported sandbox storage-host save kind")
	}
	args = append(args, string(effectJSON))
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}
func loadSandboxStorageHostEffect(ctx context.Context, q sandboxQueryer, kind StorageHostSaveKind, id string) ([]byte, bool, error) {
	var query string
	switch kind {
	case StorageHostSaveSandbox:
		query = `SELECT payload FROM sandbox_registry WHERE sandbox_id = ?`
	case StorageHostSaveArtifact:
		query = `SELECT payload FROM sandbox_artifact WHERE artifact_id = ?`
	case StorageHostSavePromotion:
		query = `SELECT payload FROM sandbox_promotion_request WHERE promotion_id = ?`
	case StorageHostSaveGateLog:
		query = `SELECT payload FROM promotion_gate_log WHERE event_id = ?`
	default:
		return nil, false, fmt.Errorf("unsupported sandbox storage-host save kind")
	}
	var raw []byte
	err := q.QueryRowContext(ctx, query, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return raw, err == nil, err
}
func validateSandboxStorageHostIdentity(v StorageHostOperationIdentity) error {
	if !sandboxStorageHostOpIDPattern.MatchString(v.OpID) || v.WriterGeneration <= 0 || len(v.PayloadSHA256) != sha256.Size*2 || strings.ToLower(v.PayloadSHA256) != v.PayloadSHA256 {
		return fmt.Errorf("sandbox storage-host identity is invalid")
	}
	if _, err := hex.DecodeString(v.PayloadSHA256); err != nil {
		return fmt.Errorf("sandbox storage-host identity is invalid")
	}
	return nil
}
func sandboxStorageHostProofSHA(operation, effectID, effectSHA string, result []byte) string {
	h := sha256.New()
	for _, v := range []string{operation, effectID, effectSHA} {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(result)
	return hex.EncodeToString(h.Sum(nil))
}
