package dci

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

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var dciStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var ErrDCIStorageHostConflict = errors.New("dci storage-host operation conflict")

type StorageHostOperationIdentity struct {
	OpID             string `json:"op_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	WriterGeneration int64  `json:"writer_generation"`
}
type StorageHostSaveKind string

const (
	StorageHostSaveTrace  StorageHostSaveKind = "save_search_trace"
	StorageHostSaveResult StorageHostSaveKind = "save_search_result"
)

type StorageHostSave struct {
	Kind           StorageHostSaveKind
	Trace          *domaindci.SearchTrace
	Result         *domaindci.SearchResult
	IdempotencyKey string
}

func (s *SQLiteStore) SaveForStorageHostOperation(ctx context.Context, identity StorageHostOperationIdentity, mutation StorageHostSave) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("dci sqlite store is closed")
	}
	if err := validateDCIStorageHostIdentity(identity); err != nil {
		return err
	}
	result, effectJSON, err := prepareDCIStorageHostSave(mutation)
	if err != nil {
		return err
	}
	effectHash := sha256.Sum256(effectJSON)
	effectSHA := hex.EncodeToString(effectHash[:])
	resultJSON := []byte("null")
	resultHash := sha256.Sum256(resultJSON)
	resultSHA := hex.EncodeToString(resultHash[:])
	proofSHA := dciStorageHostProofSHA(string(mutation.Kind), string(result.Trace.ActionID), effectSHA, resultJSON)
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, found, err := findDCIStorageHostReceipt(ctx, s.db, identity.OpID)
	if err != nil {
		return err
	}
	if found {
		return verifyDCIStorageHostReceipt(ctx, s, stored, identity, mutation, string(result.Trace.ActionID), effectJSON)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertSearchResultTx(ctx, tx, result, nil); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO dci_storagehost_receipt
		(op_id, operation, payload_sha256, writer_generation, effect_id, effect_sha256, proof_sha256, result_json, result_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, string(mutation.Kind), identity.PayloadSHA256, identity.WriterGeneration, string(result.Trace.ActionID), effectSHA, proofSHA, string(resultJSON), resultSHA, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) LookupStorageHostOperationReceipt(ctx context.Context, identity StorageHostOperationIdentity, mutation StorageHostSave) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("dci sqlite store is closed")
	}
	if err := validateDCIStorageHostIdentity(identity); err != nil {
		return false, err
	}
	result, effectJSON, err := prepareDCIStorageHostSave(mutation)
	if err != nil {
		return false, err
	}
	stored, found, err := findDCIStorageHostReceipt(ctx, s.db, identity.OpID)
	if err != nil {
		return false, err
	}
	if found {
		if err := verifyDCIStorageHostReceipt(ctx, s, stored, identity, mutation, string(result.Trace.ActionID), effectJSON); err != nil {
			return false, err
		}
		return true, nil
	}
	_, exists, err := s.FindSearchResultByActionID(ctx, result.Trace.ActionID)
	if err != nil {
		return false, err
	}
	if exists {
		return false, fmt.Errorf("dci storage-host proof missing for existing effect")
	}
	return false, nil
}

type dciStorageHostReceipt struct {
	opID, operation, payloadSHA256, effectID, effectSHA256, proofSHA256, resultJSON, resultSHA256 string
	writerGeneration                                                                              int64
}

func findDCIStorageHostReceipt(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opID string) (dciStorageHostReceipt, bool, error) {
	var v dciStorageHostReceipt
	var created string
	err := q.QueryRowContext(ctx, `SELECT op_id, operation, payload_sha256, writer_generation, effect_id, effect_sha256, proof_sha256, result_json, result_sha256, created_at FROM dci_storagehost_receipt WHERE op_id=?`, opID).Scan(&v.opID, &v.operation, &v.payloadSHA256, &v.writerGeneration, &v.effectID, &v.effectSHA256, &v.proofSHA256, &v.resultJSON, &v.resultSHA256, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
		return v, false, fmt.Errorf("dci storage-host receipt timestamp is invalid")
	}
	return v, true, nil
}
func verifyDCIStorageHostReceipt(ctx context.Context, s *SQLiteStore, stored dciStorageHostReceipt, identity StorageHostOperationIdentity, mutation StorageHostSave, effectID string, effectJSON []byte) error {
	h := sha256.Sum256(effectJSON)
	effectSHA := hex.EncodeToString(h[:])
	if stored.opID != identity.OpID || stored.operation != string(mutation.Kind) || stored.payloadSHA256 != identity.PayloadSHA256 || stored.writerGeneration != identity.WriterGeneration || stored.effectID != effectID || stored.effectSHA256 != effectSHA {
		return fmt.Errorf("%w: op_id %q", ErrDCIStorageHostConflict, identity.OpID)
	}
	rh := sha256.Sum256([]byte(stored.resultJSON))
	if stored.resultJSON != "null" || stored.resultSHA256 != hex.EncodeToString(rh[:]) || stored.proofSHA256 != dciStorageHostProofSHA(stored.operation, effectID, effectSHA, []byte(stored.resultJSON)) {
		return fmt.Errorf("dci storage-host result proof is invalid")
	}
	actual, found, err := loadDCIStorageHostEffect(ctx, s, mutation.Kind, modulecore.ActionID(effectID))
	if err != nil || !found || !bytes.Equal(actual, effectJSON) {
		return fmt.Errorf("dci storage-host effect is missing or altered")
	}
	return nil
}

type dciTraceEffect struct {
	Trace          domaindci.SearchTrace `json:"trace"`
	IdempotencyKey string                `json:"idempotency_key,omitempty"`
}
type dciResultEffect struct {
	Result         domaindci.SearchResult `json:"result"`
	IdempotencyKey string                 `json:"idempotency_key,omitempty"`
}

func prepareDCIStorageHostSave(m StorageHostSave) (domaindci.SearchResult, []byte, error) {
	if (m.Trace == nil) == (m.Result == nil) {
		return domaindci.SearchResult{}, nil, fmt.Errorf("dci storage-host save must contain exactly one value")
	}
	switch m.Kind {
	case StorageHostSaveTrace:
		if m.Trace == nil {
			return domaindci.SearchResult{}, nil, fmt.Errorf("dci storage-host save kind mismatch")
		}
		trace := *m.Trace
		trace.IdempotencyKey = m.IdempotencyKey
		if trace.Status != "completed" || trace.FinalEvidenceCount != 0 {
			return domaindci.SearchResult{}, nil, fmt.Errorf("dci SaveSearchTrace requires a completed zero-evidence trace")
		}
		result := domaindci.SearchResult{Trace: trace, Pack: domaindci.EvidencePack{ActionID: trace.ActionID, Query: trace.UserQuery, CorpusScope: append([]string(nil), trace.CorpusScope...)}}
		if err := domaindci.ValidateSearchResult(result); err != nil {
			return result, nil, err
		}
		raw, err := json.Marshal(dciTraceEffect{Trace: trace, IdempotencyKey: m.IdempotencyKey})
		return result, raw, err
	case StorageHostSaveResult:
		if m.Result == nil {
			return domaindci.SearchResult{}, nil, fmt.Errorf("dci storage-host save kind mismatch")
		}
		result := *m.Result
		result.Trace.IdempotencyKey = m.IdempotencyKey
		if err := domaindci.ValidateSearchResult(result); err != nil {
			return result, nil, err
		}
		raw, err := json.Marshal(dciResultEffect{Result: result, IdempotencyKey: m.IdempotencyKey})
		return result, raw, err
	default:
		return domaindci.SearchResult{}, nil, fmt.Errorf("unsupported dci storage-host save kind")
	}
}
func loadDCIStorageHostEffect(ctx context.Context, s *SQLiteStore, kind StorageHostSaveKind, actionID modulecore.ActionID) ([]byte, bool, error) {
	switch kind {
	case StorageHostSaveTrace:
		v, found, err := s.FindSearchTraceByActionID(ctx, actionID)
		if err != nil || !found {
			return nil, found, err
		}
		raw, err := json.Marshal(dciTraceEffect{Trace: v, IdempotencyKey: v.IdempotencyKey})
		return raw, err == nil, err
	case StorageHostSaveResult:
		v, found, err := s.FindSearchResultByActionID(ctx, actionID)
		if err != nil || !found {
			return nil, found, err
		}
		raw, err := json.Marshal(dciResultEffect{Result: v, IdempotencyKey: v.Trace.IdempotencyKey})
		return raw, err == nil, err
	default:
		return nil, false, fmt.Errorf("unsupported dci storage-host save kind")
	}
}
func validateDCIStorageHostIdentity(v StorageHostOperationIdentity) error {
	if !dciStorageHostOpIDPattern.MatchString(v.OpID) || v.WriterGeneration <= 0 || len(v.PayloadSHA256) != sha256.Size*2 || strings.ToLower(v.PayloadSHA256) != v.PayloadSHA256 {
		return fmt.Errorf("dci storage-host identity is invalid")
	}
	if _, err := hex.DecodeString(v.PayloadSHA256); err != nil {
		return fmt.Errorf("dci storage-host identity is invalid")
	}
	return nil
}
func dciStorageHostProofSHA(op, id, effect string, result []byte) string {
	h := sha256.New()
	for _, v := range []string{op, id, effect} {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(result)
	return hex.EncodeToString(h.Sum(nil))
}
