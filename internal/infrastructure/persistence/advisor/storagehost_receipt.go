package advisor

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
	"reflect"
	"regexp"
	"strings"
	"time"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
)

var advisorStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var ErrAdvisorStorageHostConflict = errors.New("advisor storage-host operation conflict")

type StorageHostOperationIdentity struct {
	OpID             string `json:"op_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	WriterGeneration int64  `json:"writer_generation"`
}

type StorageHostSaveKind string

const (
	StorageHostSaveAdviceRun      StorageHostSaveKind = "save_advice_run"
	StorageHostSaveAdoption       StorageHostSaveKind = "save_advisor_adoption"
	StorageHostSaveScoreSnapshot  StorageHostSaveKind = "save_advisor_score_snapshot"
	StorageHostSavePolicyDecision StorageHostSaveKind = "save_agent_policy_decision"
)

type StorageHostSave struct {
	Kind           StorageHostSaveKind
	AdviceRun      *advisorDomain.AdviceRunRecord
	Adoption       *advisorDomain.AdvisorAdoptionRecord
	ScoreSnapshot  *advisorDomain.AdvisorScoreSnapshot
	PolicyDecision *domainagentprofile.PolicyDecision
}

type StorageHostSaveResult struct {
	Replayed bool `json:"replayed"`
}

func (s *SQLiteStore) SaveForStorageHostOperation(ctx context.Context, identity StorageHostOperationIdentity, mutation StorageHostSave) (StorageHostSaveResult, error) {
	if s == nil || s.db == nil {
		return StorageHostSaveResult{}, fmt.Errorf("advisor sqlite store is closed")
	}
	if err := validateAdvisorStorageHostIdentity(identity); err != nil {
		return StorageHostSaveResult{}, err
	}
	effectID, createdAt, effectJSON, err := validateAdvisorStorageHostSave(mutation)
	if err != nil {
		return StorageHostSaveResult{}, err
	}
	effectHash := sha256.Sum256(effectJSON)
	effectSHA := hex.EncodeToString(effectHash[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StorageHostSaveResult{}, fmt.Errorf("begin advisor storage-host save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, found, err := findAdvisorStorageHostReceipt(ctx, tx, identity.OpID)
	if err != nil {
		return StorageHostSaveResult{}, err
	}
	if found {
		result, err := verifyAdvisorStorageHostReceipt(ctx, tx, stored, identity, mutation, effectID, effectJSON)
		if err != nil {
			return StorageHostSaveResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return StorageHostSaveResult{}, err
		}
		return result, nil
	}

	replayed, err := applyAdvisorStorageHostSave(ctx, tx, mutation, effectID, createdAt, effectJSON)
	if err != nil {
		return StorageHostSaveResult{}, err
	}
	result := StorageHostSaveResult{Replayed: replayed}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return StorageHostSaveResult{}, err
	}
	resultHash := sha256.Sum256(resultJSON)
	resultSHA := hex.EncodeToString(resultHash[:])
	proofSHA := advisorStorageHostProofSHA(string(mutation.Kind), effectID, effectSHA, resultJSON)
	if _, err := tx.ExecContext(ctx, `INSERT INTO advisor_storagehost_receipt
		(op_id, operation, payload_sha256, writer_generation, effect_id, effect_sha256, proof_sha256, result_json, result_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		identity.OpID, string(mutation.Kind), identity.PayloadSHA256, identity.WriterGeneration, effectID, effectSHA,
		proofSHA, string(resultJSON), resultSHA, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return StorageHostSaveResult{}, fmt.Errorf("insert advisor storage-host receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return StorageHostSaveResult{}, fmt.Errorf("commit advisor storage-host save: %w", err)
	}
	return result, nil
}

func (s *SQLiteStore) LookupStorageHostOperationReceipt(ctx context.Context, identity StorageHostOperationIdentity, mutation StorageHostSave) (StorageHostSaveResult, bool, error) {
	if s == nil || s.db == nil {
		return StorageHostSaveResult{}, false, fmt.Errorf("advisor sqlite store is closed")
	}
	if err := validateAdvisorStorageHostIdentity(identity); err != nil {
		return StorageHostSaveResult{}, false, err
	}
	effectID, _, effectJSON, err := validateAdvisorStorageHostSave(mutation)
	if err != nil {
		return StorageHostSaveResult{}, false, err
	}
	stored, found, err := findAdvisorStorageHostReceipt(ctx, s.db, identity.OpID)
	if err != nil {
		return StorageHostSaveResult{}, false, err
	}
	if found {
		result, err := verifyAdvisorStorageHostReceipt(ctx, s.db, stored, identity, mutation, effectID, effectJSON)
		return result, err == nil, err
	}
	_, effectFound, err := loadAdvisorStorageHostEffect(ctx, s.db, mutation.Kind, effectID)
	if err != nil {
		return StorageHostSaveResult{}, false, err
	}
	if effectFound {
		return StorageHostSaveResult{}, false, fmt.Errorf("advisor storage-host proof missing for existing effect")
	}
	return StorageHostSaveResult{}, false, nil
}

type advisorQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type advisorStorageHostReceipt struct {
	opID, operation, payloadSHA256, effectID, effectSHA256 string
	proofSHA256, resultJSON, resultSHA256                  string
	writerGeneration                                       int64
}

func findAdvisorStorageHostReceipt(ctx context.Context, queryer advisorQueryer, opID string) (advisorStorageHostReceipt, bool, error) {
	var stored advisorStorageHostReceipt
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT op_id, operation, payload_sha256, writer_generation, effect_id,
		effect_sha256, proof_sha256, result_json, result_sha256, created_at
		FROM advisor_storagehost_receipt WHERE op_id = ?`, opID).Scan(
		&stored.opID, &stored.operation, &stored.payloadSHA256, &stored.writerGeneration, &stored.effectID,
		&stored.effectSHA256, &stored.proofSHA256, &stored.resultJSON, &stored.resultSHA256, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return advisorStorageHostReceipt{}, false, nil
	}
	if err != nil {
		return advisorStorageHostReceipt{}, false, err
	}
	if _, err := time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return advisorStorageHostReceipt{}, false, fmt.Errorf("advisor storage-host receipt timestamp is invalid")
	}
	return stored, true, nil
}

func verifyAdvisorStorageHostReceipt(ctx context.Context, queryer advisorQueryer, stored advisorStorageHostReceipt, identity StorageHostOperationIdentity, mutation StorageHostSave, effectID string, effectJSON []byte) (StorageHostSaveResult, error) {
	effectHash := sha256.Sum256(effectJSON)
	effectSHA := hex.EncodeToString(effectHash[:])
	if stored.opID != identity.OpID || stored.operation != string(mutation.Kind) || stored.payloadSHA256 != identity.PayloadSHA256 ||
		stored.writerGeneration != identity.WriterGeneration || stored.effectID != effectID || stored.effectSHA256 != effectSHA {
		return StorageHostSaveResult{}, fmt.Errorf("%w: op_id %q", ErrAdvisorStorageHostConflict, identity.OpID)
	}
	resultHash := sha256.Sum256([]byte(stored.resultJSON))
	if stored.resultSHA256 != hex.EncodeToString(resultHash[:]) ||
		stored.proofSHA256 != advisorStorageHostProofSHA(stored.operation, effectID, effectSHA, []byte(stored.resultJSON)) {
		return StorageHostSaveResult{}, fmt.Errorf("advisor storage-host result proof is invalid")
	}
	var result StorageHostSaveResult
	decoder := json.NewDecoder(strings.NewReader(stored.resultJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return StorageHostSaveResult{}, fmt.Errorf("advisor storage-host result is malformed")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return StorageHostSaveResult{}, fmt.Errorf("advisor storage-host result has trailing data")
	}
	actual, found, err := loadAdvisorStorageHostEffect(ctx, queryer, mutation.Kind, effectID)
	if err != nil || !found || !bytes.Equal(actual, effectJSON) {
		return StorageHostSaveResult{}, fmt.Errorf("advisor storage-host effect is missing or altered")
	}
	return result, nil
}

func validateAdvisorStorageHostSave(mutation StorageHostSave) (string, time.Time, []byte, error) {
	count := 0
	for _, present := range []bool{mutation.AdviceRun != nil, mutation.Adoption != nil, mutation.ScoreSnapshot != nil, mutation.PolicyDecision != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return "", time.Time{}, nil, fmt.Errorf("advisor storage-host save must contain exactly one item")
	}
	var id string
	var createdAt time.Time
	var item any
	var err error
	switch mutation.Kind {
	case StorageHostSaveAdviceRun:
		if mutation.AdviceRun == nil {
			return "", time.Time{}, nil, fmt.Errorf("advisor storage-host save kind mismatch")
		}
		id, createdAt, item, err = mutation.AdviceRun.RunID, mutation.AdviceRun.FinishedAt, *mutation.AdviceRun, mutation.AdviceRun.Validate()
	case StorageHostSaveAdoption:
		if mutation.Adoption == nil {
			return "", time.Time{}, nil, fmt.Errorf("advisor storage-host save kind mismatch")
		}
		id, createdAt, item, err = mutation.Adoption.AdoptionID, mutation.Adoption.CreatedAt, *mutation.Adoption, mutation.Adoption.Validate()
	case StorageHostSaveScoreSnapshot:
		if mutation.ScoreSnapshot == nil {
			return "", time.Time{}, nil, fmt.Errorf("advisor storage-host save kind mismatch")
		}
		id, createdAt, item, err = mutation.ScoreSnapshot.SnapshotID, mutation.ScoreSnapshot.CreatedAt, *mutation.ScoreSnapshot, mutation.ScoreSnapshot.Validate()
	case StorageHostSavePolicyDecision:
		if mutation.PolicyDecision == nil {
			return "", time.Time{}, nil, fmt.Errorf("advisor storage-host save kind mismatch")
		}
		id, createdAt, item, err = mutation.PolicyDecision.DecisionID, mutation.PolicyDecision.CreatedAt, *mutation.PolicyDecision, mutation.PolicyDecision.Validate()
	default:
		return "", time.Time{}, nil, fmt.Errorf("unsupported advisor storage-host save kind")
	}
	if err != nil {
		return "", time.Time{}, nil, err
	}
	raw, err := json.Marshal(item)
	return id, createdAt, raw, err
}

func applyAdvisorStorageHostSave(ctx context.Context, tx *sql.Tx, mutation StorageHostSave, effectID string, createdAt time.Time, effectJSON []byte) (bool, error) {
	table, idColumn, err := advisorStorageHostTable(mutation.Kind)
	if err != nil {
		return false, err
	}
	if mutation.Kind == StorageHostSaveAdoption {
		existing, found, err := loadAdvisorStorageHostEffect(ctx, tx, mutation.Kind, effectID)
		if err != nil {
			return false, err
		}
		if found {
			var stored advisorDomain.AdvisorAdoptionRecord
			if json.Unmarshal(existing, &stored) != nil || stored.Validate() != nil || !reflect.DeepEqual(stored, *mutation.Adoption) {
				return false, fmt.Errorf("%w: adoption_id %q", ErrAdvisorStorageHostConflict, effectID)
			}
			return true, nil
		}
	}
	query := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)", table, idColumn)
	if _, err := tx.ExecContext(ctx, query, effectID, createdAt.UTC().Format(time.RFC3339Nano), string(effectJSON)); err != nil {
		return false, err
	}
	return false, nil
}

func loadAdvisorStorageHostEffect(ctx context.Context, queryer advisorQueryer, kind StorageHostSaveKind, id string) ([]byte, bool, error) {
	table, idColumn, err := advisorStorageHostTable(kind)
	if err != nil {
		return nil, false, err
	}
	var raw []byte
	err = queryer.QueryRowContext(ctx, fmt.Sprintf("SELECT payload FROM %s WHERE %s = ?", table, idColumn), id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return raw, err == nil, err
}

func advisorStorageHostTable(kind StorageHostSaveKind) (string, string, error) {
	switch kind {
	case StorageHostSaveAdviceRun:
		return "advisor_run", "run_id", nil
	case StorageHostSaveAdoption:
		return "advisor_adoption", "adoption_id", nil
	case StorageHostSaveScoreSnapshot:
		return "advisor_score_snapshot", "snapshot_id", nil
	case StorageHostSavePolicyDecision:
		return "agent_policy_decision", "decision_id", nil
	default:
		return "", "", fmt.Errorf("unsupported advisor storage-host save kind")
	}
}

func validateAdvisorStorageHostIdentity(identity StorageHostOperationIdentity) error {
	if !advisorStorageHostOpIDPattern.MatchString(identity.OpID) || identity.WriterGeneration <= 0 ||
		len(identity.PayloadSHA256) != sha256.Size*2 || strings.ToLower(identity.PayloadSHA256) != identity.PayloadSHA256 {
		return fmt.Errorf("advisor storage-host identity is invalid")
	}
	if _, err := hex.DecodeString(identity.PayloadSHA256); err != nil {
		return fmt.Errorf("advisor storage-host identity is invalid")
	}
	return nil
}

func advisorStorageHostProofSHA(operation, effectID, effectSHA string, resultJSON []byte) string {
	hash := sha256.New()
	for _, value := range []string{operation, effectID, effectSHA} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	_, _ = hash.Write(resultJSON)
	return hex.EncodeToString(hash.Sum(nil))
}
