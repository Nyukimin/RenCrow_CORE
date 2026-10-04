package verification

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// JSONLReportStore persists verification reports separately from execution evidence.
type JSONLReportStore struct {
	path                    string
	mu                      sync.Mutex
	afterReportHook         func() error
	operationParentSyncHook func(string) error
	operationParentSynced   bool
	reportSyncHook          func(*os.File) error
}

var (
	ErrVerificationReportNotFound          = errors.New("verification report not found")
	ErrVerificationReportOperationConflict = errors.New("verification report storage-host operation conflict")
	ErrVerificationReportOperationUnknown  = errors.New("verification report storage-host operation outcome unknown")
	ErrVerificationReportOperationCorrupt  = errors.New("verification report storage-host operation log is corrupt")
)

const verificationReportOperationVersion = 1

var verificationReportOperationIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type StorageHostOperationIdentity struct {
	OpID             string
	PayloadSHA256    string
	WriterGeneration int64
}

func NewJSONLReportStore(path string) (*JSONLReportStore, error) {
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("touch file: %w", err)
	}
	_ = f.Close()
	return &JSONLReportStore{path: path, operationParentSyncHook: syncVerificationReportDirectory, reportSyncHook: func(file *os.File) error { return file.Sync() }}, nil
}

func (s *JSONLReportStore) Save(_ context.Context, report domainverification.VerificationReport) error {
	if err := report.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open for append: %w", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(report); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return nil
}

func (s *JSONLReportStore) ListRecent(_ context.Context, limit int) ([]domainverification.VerificationReport, error) {
	if limit <= 0 {
		limit = 20
	}
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *JSONLReportStore) GetByTaskID(_ context.Context, taskID modulecore.TaskID) (domainverification.VerificationReport, error) {
	if err := taskID.Validate(); err != nil {
		return domainverification.VerificationReport{}, fmt.Errorf("task_id: %w", err)
	}
	items, err := s.readAll()
	if err != nil {
		return domainverification.VerificationReport{}, err
	}
	var best domainverification.VerificationReport
	found := false
	for _, item := range items {
		if item.TaskID != taskID {
			continue
		}
		if !found || item.CreatedAt.After(best.CreatedAt) {
			best = item
			found = true
		}
	}
	if !found {
		return domainverification.VerificationReport{}, ErrVerificationReportNotFound
	}
	return best, nil
}

func (s *JSONLReportStore) Summary(_ context.Context) (map[string]map[string]int, error) {
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int{
		"status": {
			string(domainverification.StatusVerified):        0,
			string(domainverification.StatusWeaklySupported): 0,
			string(domainverification.StatusUnsupported):     0,
			string(domainverification.StatusConflict):        0,
			string(domainverification.StatusNotChecked):      0,
		},
		"trigger_level": {
			string(domainverification.TriggerLow):    0,
			string(domainverification.TriggerMedium): 0,
			string(domainverification.TriggerHigh):   0,
		},
	}
	for _, item := range items {
		if _, ok := out["status"][string(item.Status)]; ok {
			out["status"][string(item.Status)]++
		}
		if _, ok := out["trigger_level"][string(item.TriggerLevel)]; ok {
			out["trigger_level"][string(item.TriggerLevel)]++
		}
	}
	return out, nil
}

func (s *JSONLReportStore) readAll() ([]domainverification.VerificationReport, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	items := make([]domainverification.VerificationReport, 0)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var report domainverification.VerificationReport
		if err := json.Unmarshal(sc.Bytes(), &report); err != nil {
			return nil, fmt.Errorf("decode verification report: %w", err)
		}
		if err := report.Validate(); err != nil {
			return nil, fmt.Errorf("validate verification report: %w", err)
		}
		items = append(items, report)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}
	return items, nil
}

type verificationReportOperationRecord struct {
	Version      int                          `json:"version"`
	Kind         string                       `json:"kind"`
	Identity     StorageHostOperationIdentity `json:"identity"`
	BeforeSize   int64                        `json:"before_size"`
	BeforeSHA256 string                       `json:"before_sha256"`
	ReportSize   int64                        `json:"report_size"`
	ReportSHA256 string                       `json:"report_sha256"`
	ResultJSON   string                       `json:"result_json,omitempty"`
	ResultSHA256 string                       `json:"result_sha256,omitempty"`
	CreatedAt    time.Time                    `json:"created_at"`
}

type verificationReportOperationState struct {
	begin     verificationReportOperationRecord
	committed bool
}

func (identity StorageHostOperationIdentity) validate() error {
	if !verificationReportOperationIDPattern.MatchString(identity.OpID) || !validVerificationReportSHA256(identity.PayloadSHA256) || identity.WriterGeneration <= 0 {
		return errors.New("verification report storage-host operation identity is invalid")
	}
	return nil
}

func (s *JSONLReportStore) SaveForStorageHostOperation(ctx context.Context, identity StorageHostOperationIdentity, report domainverification.VerificationReport) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return errors.New("verification report store is unavailable")
	}
	if err := identity.validate(); err != nil {
		return err
	}
	if err := report.Validate(); err != nil {
		return err
	}
	reportLine, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode verification report operation: %w", err)
	}
	reportLine = append(reportLine, '\n')
	if len(reportLine) > 2<<20 {
		return errors.New("verification report operation exceeds size bound")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	states, err := s.readOperationStates()
	if err != nil {
		return err
	}
	if state, found := states[identity.OpID]; found {
		if state.begin.Identity != identity {
			return ErrVerificationReportOperationConflict
		}
		if err := s.ensureOperationParentSynced(); err != nil {
			return err
		}
		disposition, err := s.operationEffectDisposition(state)
		if err != nil {
			return err
		}
		switch disposition {
		case operationEffectCommitted:
			return nil
		case operationEffectNotCommitted:
			reportHash := sha256.Sum256(reportLine)
			if int64(len(reportLine)) != state.begin.ReportSize || hexSHA256(reportHash) != state.begin.ReportSHA256 {
				return ErrVerificationReportOperationConflict
			}
			return s.appendOperationEffect(ctx, state.begin, reportLine)
		default:
			return ErrVerificationReportOperationUnknown
		}
	}
	beforeSize, beforeSHA256, err := verificationReportFileState(s.path)
	if err != nil {
		return err
	}
	reportHash := sha256.Sum256(reportLine)
	begin := verificationReportOperationRecord{
		Version: verificationReportOperationVersion, Kind: "begin", Identity: identity,
		BeforeSize: beforeSize, BeforeSHA256: beforeSHA256, ReportSize: int64(len(reportLine)),
		ReportSHA256: hexSHA256(reportHash), CreatedAt: time.Now().UTC(),
	}
	if err := s.appendOperationRecord(begin); err != nil {
		return err
	}
	return s.appendOperationEffect(ctx, begin, reportLine)
}

func (s *JSONLReportStore) LookupStorageHostOperationReceipt(ctx context.Context, identity StorageHostOperationIdentity) (json.RawMessage, bool, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, false, errors.New("verification report store is unavailable")
	}
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	states, err := s.readOperationStates()
	if err != nil {
		return nil, false, err
	}
	state, found := states[identity.OpID]
	if !found {
		return nil, false, nil
	}
	if state.begin.Identity != identity {
		return nil, false, ErrVerificationReportOperationConflict
	}
	if err := s.ensureOperationParentSynced(); err != nil {
		return nil, false, err
	}
	disposition, err := s.operationEffectDisposition(state)
	if err != nil {
		return nil, false, err
	}
	if disposition == operationEffectNotCommitted {
		return nil, false, nil
	}
	if disposition != operationEffectCommitted {
		return nil, false, ErrVerificationReportOperationUnknown
	}
	return json.RawMessage("null"), true, nil
}

func (s *JSONLReportStore) operationPath() string {
	return s.path + ".storagehost-operations.jsonl"
}

func (s *JSONLReportStore) appendOperationEffect(ctx context.Context, begin verificationReportOperationRecord, reportLine []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open verification report effect: %w", err)
	}
	if _, err := f.Write(reportLine); err != nil {
		_ = f.Close()
		return fmt.Errorf("append verification report effect: %w", err)
	}
	if err := s.reportSyncHook(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync verification report effect: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close verification report effect: %w", err)
	}
	if s.afterReportHook != nil {
		if err := s.afterReportHook(); err != nil {
			return err
		}
	}
	result := []byte("null")
	resultHash := sha256.Sum256(result)
	commit := begin
	commit.Kind = "commit"
	commit.ResultJSON = string(result)
	commit.ResultSHA256 = hexSHA256(resultHash)
	commit.CreatedAt = time.Now().UTC()
	return s.appendOperationRecord(commit)
}

func (s *JSONLReportStore) appendOperationRecord(record verificationReportOperationRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode verification report operation record: %w", err)
	}
	encoded = append(encoded, '\n')
	f, err := os.OpenFile(s.operationPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open verification report operation log: %w", err)
	}
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		return fmt.Errorf("append verification report operation log: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync verification report operation log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close verification report operation log: %w", err)
	}
	return s.ensureOperationParentSynced()
}

func (s *JSONLReportStore) ensureOperationParentSynced() error {
	if s.operationParentSynced {
		return nil
	}
	if err := s.operationParentSyncHook(filepath.Dir(s.operationPath())); err != nil {
		return fmt.Errorf("sync verification report operation log parent: %w", err)
	}
	s.operationParentSynced = true
	return nil
}

func (s *JSONLReportStore) readOperationStates() (map[string]verificationReportOperationState, error) {
	f, err := os.Open(s.operationPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]verificationReportOperationState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open verification report operation log: %w", err)
	}
	defer f.Close()
	states := map[string]verificationReportOperationState{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var record verificationReportOperationRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil || validateVerificationReportOperationRecord(record) != nil {
			return nil, ErrVerificationReportOperationCorrupt
		}
		state, exists := states[record.Identity.OpID]
		switch record.Kind {
		case "begin":
			if exists {
				return nil, ErrVerificationReportOperationCorrupt
			}
			states[record.Identity.OpID] = verificationReportOperationState{begin: record}
		case "commit":
			if !exists || state.committed || !sameVerificationReportOperationProof(state.begin, record) {
				return nil, ErrVerificationReportOperationCorrupt
			}
			state.committed = true
			states[record.Identity.OpID] = state
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan verification report operation log: %w", err)
	}
	return states, nil
}

func validateVerificationReportOperationRecord(record verificationReportOperationRecord) error {
	if record.Version != verificationReportOperationVersion || record.Identity.validate() != nil || record.BeforeSize < 0 || record.ReportSize <= 0 ||
		!validVerificationReportSHA256(record.BeforeSHA256) || !validVerificationReportSHA256(record.ReportSHA256) || record.CreatedAt.IsZero() {
		return ErrVerificationReportOperationCorrupt
	}
	switch record.Kind {
	case "begin":
		if record.ResultJSON != "" || record.ResultSHA256 != "" {
			return ErrVerificationReportOperationCorrupt
		}
	case "commit":
		if record.ResultJSON != "null" || !validVerificationReportSHA256(record.ResultSHA256) {
			return ErrVerificationReportOperationCorrupt
		}
		hash := sha256.Sum256([]byte(record.ResultJSON))
		if record.ResultSHA256 != hexSHA256(hash) {
			return ErrVerificationReportOperationCorrupt
		}
	default:
		return ErrVerificationReportOperationCorrupt
	}
	return nil
}

func sameVerificationReportOperationProof(begin, commit verificationReportOperationRecord) bool {
	return begin.Identity == commit.Identity && begin.BeforeSize == commit.BeforeSize && begin.BeforeSHA256 == commit.BeforeSHA256 &&
		begin.ReportSize == commit.ReportSize && begin.ReportSHA256 == commit.ReportSHA256
}

type operationEffectDisposition uint8

const (
	operationEffectUnknown operationEffectDisposition = iota
	operationEffectNotCommitted
	operationEffectCommitted
)

func (s *JSONLReportStore) operationEffectDisposition(state verificationReportOperationState) (operationEffectDisposition, error) {
	// Recovery may need to re-establish durability for a visible BEGIN-only
	// effect. Open a write-capable handle because Windows File.Sync may reject
	// a read-only handle even though all inspection below remains read-only.
	f, err := os.OpenFile(s.path, os.O_RDWR, 0)
	if err != nil {
		return operationEffectUnknown, fmt.Errorf("open verification report effect: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return operationEffectUnknown, fmt.Errorf("stat verification report effect: %w", err)
	}
	if stat.Size() < state.begin.BeforeSize {
		return operationEffectUnknown, nil
	}
	prefixHash, err := hashVerificationReportRange(f, 0, state.begin.BeforeSize)
	if err != nil || prefixHash != state.begin.BeforeSHA256 {
		return operationEffectUnknown, err
	}
	if stat.Size() == state.begin.BeforeSize {
		if state.committed {
			return operationEffectUnknown, nil
		}
		return operationEffectNotCommitted, nil
	}
	if stat.Size() < state.begin.BeforeSize+state.begin.ReportSize {
		return operationEffectUnknown, nil
	}
	reportHash, err := hashVerificationReportRange(f, state.begin.BeforeSize, state.begin.ReportSize)
	if err != nil || reportHash != state.begin.ReportSHA256 {
		return operationEffectUnknown, err
	}
	if !state.committed {
		if err := s.reportSyncHook(f); err != nil {
			return operationEffectUnknown, fmt.Errorf("%w: re-sync verification report effect: %v", ErrVerificationReportOperationUnknown, err)
		}
	}
	return operationEffectCommitted, nil
}

func verificationReportFileState(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("open verification report file: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return 0, "", fmt.Errorf("stat verification report file: %w", err)
	}
	hash, err := hashVerificationReportRange(f, 0, stat.Size())
	return stat.Size(), hash, err
}

func hashVerificationReportRange(f *os.File, offset, size int64) (string, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, f, size); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func validVerificationReportSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func hexSHA256(hash [sha256.Size]byte) string {
	return fmt.Sprintf("%x", hash[:])
}
