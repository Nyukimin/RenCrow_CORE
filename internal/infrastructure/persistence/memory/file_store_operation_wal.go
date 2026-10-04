package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	StorageHostMemoryWriteLongTerm = "write_long_term"
	StorageHostMemoryAppendToday   = "append_today"
	StorageHostMemorySaveForDate   = "save_daily_note_for_date"

	maxStorageHostMemoryFileBytes   = 1 << 20
	maxStorageHostMemoryResultBytes = 8 << 20
	maxStorageHostMemoryRecentDays  = 31
	maxOperationMemoryWALBytes      = 4 << 20
	operationMemoryWALVersion       = 1
)

const (
	operationMemoryDirectoryName = ".operation-memory"
	operationMemoryReceiptsName  = "receipts"
	operationMemoryPendingName   = "pending.json"
)

var (
	ErrStorageHostOperationConflict         = errors.New("memory: operation id reused with different content")
	errStorageHostOperationIdentityRequired = errors.New("memory: storage-host mutations require an operation identity")
	errOperationMemoryRecoveryMismatch      = errors.New("memory: operation-memory target does not match its durable recovery record")
)

type StorageHostMemoryOperation struct {
	OpID        string
	Operation   string
	PayloadHash string
	Content     string
	Date        string
}

type StorageHostMemoryReceipt struct {
	OpID        string
	Operation   string
	PayloadHash string
	ResultJSON  []byte
}

type StorageHostMemoryResolution uint8

const (
	StorageHostMemoryNotCommitted StorageHostMemoryResolution = iota + 1
	StorageHostMemoryCommitted
)

type StorageHostMemoryLookup struct {
	Resolution StorageHostMemoryResolution
	Receipt    StorageHostMemoryReceipt
}

type operationMemoryWAL struct {
	Version       int             `json:"version"`
	OpID          string          `json:"op_id"`
	Operation     string          `json:"operation"`
	PayloadHash   string          `json:"payload_sha256"`
	Date          string          `json:"date,omitempty"`
	Target        string          `json:"target"`
	BeforeExists  bool            `json:"before_exists"`
	BeforeContent []byte          `json:"before_content,omitempty"`
	BeforeSHA256  string          `json:"before_sha256,omitempty"`
	AfterContent  []byte          `json:"after_content"`
	AfterSHA256   string          `json:"after_sha256"`
	Result        json.RawMessage `json:"result"`
	RecordSHA256  string          `json:"record_sha256"`
}

type operationMemoryReceiptRecord struct {
	Version      int             `json:"version"`
	OpID         string          `json:"op_id"`
	Operation    string          `json:"operation"`
	PayloadHash  string          `json:"payload_sha256"`
	Date         string          `json:"date,omitempty"`
	Target       string          `json:"target"`
	AfterSHA256  string          `json:"after_sha256"`
	Result       json.RawMessage `json:"result"`
	RecordSHA256 string          `json:"record_sha256"`
}

// OpenRecoverableFileStoreAt opens the storage-host owner variant of FileStore.
// Its pending WAL is recovered before the store is returned. NewFileStoreAt
// remains the local compatibility constructor and does not add owner metadata
// to the existing memory directory layout.
func OpenRecoverableFileStoreAt(memoryDir string) (*FileStore, error) {
	if strings.TrimSpace(memoryDir) == "" {
		return nil, errors.New("memory: directory required")
	}
	_, rootStatErr := os.Lstat(memoryDir)
	rootCreated := errors.Is(rootStatErr, os.ErrNotExist)
	if rootStatErr != nil && !rootCreated {
		return nil, fmt.Errorf("memory: inspect directory: %w", rootStatErr)
	}
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return nil, fmt.Errorf("memory: create directory: %w", err)
	}
	rootInfo, err := os.Lstat(memoryDir)
	if err != nil {
		return nil, fmt.Errorf("memory: inspect directory: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("memory: directory must be a real directory")
	}
	if rootCreated {
		if err := syncOperationMemoryDirectory(filepath.Dir(memoryDir)); err != nil {
			return nil, fmt.Errorf("memory: sync directory parent: %w", err)
		}
	}

	operationDir := filepath.Join(memoryDir, operationMemoryDirectoryName)
	createdOperationDir, err := ensureOperationMemoryDirectory(operationDir, 0o700)
	if err != nil {
		return nil, fmt.Errorf("memory: open operation metadata directory: %w", err)
	}
	if createdOperationDir {
		if err := syncOperationMemoryDirectory(memoryDir); err != nil {
			return nil, fmt.Errorf("memory: sync operation metadata parent: %w", err)
		}
	}
	receiptsDir := filepath.Join(operationDir, operationMemoryReceiptsName)
	createdReceiptsDir, err := ensureOperationMemoryDirectory(receiptsDir, 0o700)
	if err != nil {
		return nil, fmt.Errorf("memory: open operation receipt directory: %w", err)
	}
	if createdReceiptsDir {
		if err := syncOperationMemoryDirectory(operationDir); err != nil {
			return nil, fmt.Errorf("memory: sync receipt directory parent: %w", err)
		}
	}

	store := &FileStore{
		memoryDir:    memoryDir,
		memoryFile:   filepath.Join(memoryDir, "MEMORY.md"),
		now:          time.Now,
		recoverable:  true,
		operationDir: operationDir,
	}
	store.mu.Lock()
	err = store.recoverOperationMemoryLocked()
	store.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("memory: recover operation metadata: %w", err)
	}
	return store, nil
}

// ReadStorageHostMemoryLongTerm is the bounded, checked read path used by the
// storage-host RPC. Legacy domain Store reads retain their existing behavior.
func (fs *FileStore) ReadStorageHostMemoryLongTerm() (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.readStorageHostMemoryTargetLocked("MEMORY.md")
}

// ReadStorageHostMemoryToday selects the canonical daily file using the owner
// clock and rejects symlinks, non-regular files, oversized files, and invalid
// UTF-8 instead of collapsing an I/O error into an empty value.
func (fs *FileStore) ReadStorageHostMemoryToday() (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	now := time.Now()
	if fs.now != nil {
		now = fs.now()
	}
	target, err := operationMemoryTarget(StorageHostMemoryAppendToday, now.Format("2006-01-02"))
	if err != nil {
		return "", err
	}
	return fs.readStorageHostMemoryTargetLocked(target)
}

// ReadStorageHostMemoryRecentDailyNotes returns the same descending owner-day
// view as FileStore.GetRecentDailyNotes, with bounded checked reads.
func (fs *FileStore) ReadStorageHostMemoryRecentDailyNotes(days int) (string, error) {
	if days < 0 || days > maxStorageHostMemoryRecentDays {
		return "", errors.New("memory: recent-note day bound exceeded")
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.readStorageHostMemoryRecentDailyNotesLocked(days)
}

// ReadStorageHostMemoryContext is the checked storage-host counterpart of
// FileStore.GetMemoryContext. It preserves the existing projection format.
func (fs *FileStore) ReadStorageHostMemoryContext() (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	longTerm, err := fs.readStorageHostMemoryTargetLocked("MEMORY.md")
	if err != nil {
		return "", err
	}
	recentNotes, err := fs.readStorageHostMemoryRecentDailyNotesLocked(3)
	if err != nil {
		return "", err
	}
	var parts []string
	if longTerm != "" {
		parts = append(parts, "## Long-term Memory\n\n"+longTerm)
	}
	if recentNotes != "" {
		parts = append(parts, "## Recent Daily Notes\n\n"+recentNotes)
	}
	if len(parts) == 0 {
		return "", nil
	}
	content := "# Memory\n\n" + strings.Join(parts, "\n\n---\n\n")
	if len(content) > maxStorageHostMemoryResultBytes {
		return "", errors.New("memory: context result exceeds storage-host bound")
	}
	return content, nil
}

func (fs *FileStore) readStorageHostMemoryTargetLocked(target string) (string, error) {
	if !fs.recoverable {
		return "", errors.New("memory: recoverable owner store required")
	}
	exists, content, err := fs.readOperationMemoryTargetLocked(target)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	return string(content), nil
}

func (fs *FileStore) readStorageHostMemoryRecentDailyNotesLocked(days int) (string, error) {
	if !fs.recoverable {
		return "", errors.New("memory: recoverable owner store required")
	}
	now := time.Now()
	if fs.now != nil {
		now = fs.now()
	}
	var result strings.Builder
	notes := 0
	for offset := 0; offset < days; offset++ {
		date := now.AddDate(0, 0, -offset).Format("2006-01-02")
		target, err := operationMemoryTarget(StorageHostMemoryAppendToday, date)
		if err != nil {
			return "", err
		}
		exists, content, err := fs.readOperationMemoryTargetLocked(target)
		if err != nil {
			return "", err
		}
		if !exists {
			continue
		}
		separator := ""
		if notes > 0 {
			separator = "\n\n---\n\n"
		}
		if result.Len()+len(separator)+len(content) > maxStorageHostMemoryResultBytes {
			return "", errors.New("memory: recent-note result exceeds storage-host bound")
		}
		result.WriteString(separator)
		result.Write(content)
		notes++
	}
	return result.String(), nil
}

// ApplyStorageHostMemoryOperation is the only mutation entrypoint for a
// recoverable FileStore. A prepared WAL is durable before the canonical
// Markdown target changes; a receipt is durable before the WAL is removed.
func (fs *FileStore) ApplyStorageHostMemoryOperation(operation StorageHostMemoryOperation) (StorageHostMemoryReceipt, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.recoverable {
		return StorageHostMemoryReceipt{}, errors.New("memory: recoverable owner store required")
	}
	if err := validateStorageHostMemoryOperation(operation); err != nil {
		return StorageHostMemoryReceipt{}, err
	}
	if err := fs.recoverOperationMemoryLocked(); err != nil {
		return StorageHostMemoryReceipt{}, err
	}
	if receipt, found, err := fs.readOperationMemoryReceiptLocked(operation.OpID); err != nil {
		return StorageHostMemoryReceipt{}, err
	} else if found {
		if !sameMemoryOperationIdentity(receipt.OpID, receipt.Operation, receipt.PayloadHash, operation.OpID, operation.Operation, operation.PayloadHash) {
			return StorageHostMemoryReceipt{}, ErrStorageHostOperationConflict
		}
		return publicMemoryReceipt(receipt), nil
	}

	record, err := fs.newOperationMemoryWALLocked(operation)
	if err != nil {
		return StorageHostMemoryReceipt{}, err
	}
	if err := fs.writePendingOperationMemoryLocked(record); err != nil {
		return StorageHostMemoryReceipt{}, err
	}
	if err := fs.applyOperationMemoryTargetLocked(record); err != nil {
		return StorageHostMemoryReceipt{}, err
	}
	if err := fs.writeOperationMemoryReceiptLocked(receiptFromWAL(record)); err != nil {
		return StorageHostMemoryReceipt{}, err
	}
	if err := fs.removePendingOperationMemoryLocked(); err != nil {
		return StorageHostMemoryReceipt{}, err
	}
	return publicMemoryReceipt(receiptFromWAL(record)), nil
}

// LookupStorageHostMemoryOperation is read-only. It proves a committed result
// from a durable receipt or prepared WAL, and proves non-commit only when the
// target still has the recorded before identity. It never repairs state.
func (fs *FileStore) LookupStorageHostMemoryOperation(opID, operation, payloadHash string) (StorageHostMemoryLookup, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.recoverable {
		return StorageHostMemoryLookup{}, errors.New("memory: recoverable owner store required")
	}
	if !validOperationID(opID) || !validMemoryOperationName(operation) || !validMemorySHA256(payloadHash) {
		return StorageHostMemoryLookup{}, errors.New("memory: invalid operation identity")
	}
	if receipt, found, err := fs.readOperationMemoryReceiptLocked(opID); err != nil {
		return StorageHostMemoryLookup{}, err
	} else if found {
		if !sameMemoryOperationIdentity(receipt.OpID, receipt.Operation, receipt.PayloadHash, opID, operation, payloadHash) {
			return StorageHostMemoryLookup{}, ErrStorageHostOperationConflict
		}
		return StorageHostMemoryLookup{Resolution: StorageHostMemoryCommitted, Receipt: publicMemoryReceipt(receipt)}, nil
	}

	record, found, err := fs.readPendingOperationMemoryLocked()
	if err != nil {
		return StorageHostMemoryLookup{}, err
	}
	if !found {
		return StorageHostMemoryLookup{Resolution: StorageHostMemoryNotCommitted}, nil
	}
	if record.OpID == opID && !sameMemoryOperationIdentity(record.OpID, record.Operation, record.PayloadHash, opID, operation, payloadHash) {
		return StorageHostMemoryLookup{}, ErrStorageHostOperationConflict
	}
	exists, content, err := fs.readOperationMemoryTargetLocked(record.Target)
	if err != nil {
		return StorageHostMemoryLookup{}, err
	}
	if operationMemoryStateMatches(exists, content, true, record.AfterContent) {
		return StorageHostMemoryLookup{}, errors.New("memory: prepared operation reached target without a durable receipt")
	}
	if operationMemoryStateMatches(exists, content, record.BeforeExists, record.BeforeContent) {
		return StorageHostMemoryLookup{Resolution: StorageHostMemoryNotCommitted}, nil
	}
	return StorageHostMemoryLookup{}, errOperationMemoryRecoveryMismatch
}

func validateStorageHostMemoryOperation(operation StorageHostMemoryOperation) error {
	if !validOperationID(operation.OpID) || !validMemoryOperationName(operation.Operation) || !validMemorySHA256(operation.PayloadHash) {
		return errors.New("memory: invalid operation identity")
	}
	if !utf8.ValidString(operation.Content) || len(operation.Content) > maxStorageHostMemoryFileBytes {
		return errors.New("memory: operation content exceeds the storage-host bound")
	}
	switch operation.Operation {
	case StorageHostMemoryWriteLongTerm, StorageHostMemoryAppendToday:
		if operation.Date != "" {
			return errors.New("memory: unexpected operation date")
		}
	case StorageHostMemorySaveForDate:
		if !canonicalOperationMemoryDate(operation.Date) {
			return errors.New("memory: operation date is not canonical")
		}
	}
	return nil
}

func (fs *FileStore) newOperationMemoryWALLocked(operation StorageHostMemoryOperation) (operationMemoryWAL, error) {
	date := operation.Date
	if operation.Operation == StorageHostMemoryAppendToday {
		now := time.Now()
		if fs.now != nil {
			now = fs.now()
		}
		date = now.Format("2006-01-02")
	}
	target, err := operationMemoryTarget(operation.Operation, date)
	if err != nil {
		return operationMemoryWAL{}, err
	}
	exists, before, err := fs.readOperationMemoryTargetLocked(target)
	if err != nil {
		return operationMemoryWAL{}, err
	}
	after := []byte(operation.Content)
	switch operation.Operation {
	case StorageHostMemoryAppendToday:
		if len(before) == 0 {
			after = []byte("# " + date + "\n\n" + operation.Content)
		} else {
			after = append(append(append([]byte(nil), before...), '\n'), []byte(operation.Content)...)
		}
	case StorageHostMemorySaveForDate:
		if len(before) == 0 {
			after = []byte("# " + date + "\n\n" + operation.Content)
		} else {
			after = append(append(append([]byte(nil), before...), '\n', '\n'), []byte(operation.Content)...)
		}
	}
	if len(after) > maxStorageHostMemoryFileBytes || !utf8.Valid(after) {
		return operationMemoryWAL{}, errors.New("memory: resulting note exceeds the storage-host bound")
	}
	record := operationMemoryWAL{
		Version:       operationMemoryWALVersion,
		OpID:          operation.OpID,
		Operation:     operation.Operation,
		PayloadHash:   operation.PayloadHash,
		Date:          date,
		Target:        target,
		BeforeExists:  exists,
		BeforeContent: append([]byte(nil), before...),
		AfterContent:  append([]byte(nil), after...),
		AfterSHA256:   memorySHA256(after),
		Result:        json.RawMessage("null"),
	}
	if exists {
		record.BeforeSHA256 = memorySHA256(before)
	}
	if err := sealOperationMemoryWAL(&record); err != nil {
		return operationMemoryWAL{}, err
	}
	return record, nil
}

func (fs *FileStore) applyOperationMemoryTargetLocked(record operationMemoryWAL) error {
	if err := validateOperationMemoryWAL(record); err != nil {
		return err
	}
	exists, content, err := fs.readOperationMemoryTargetLocked(record.Target)
	if err != nil {
		return err
	}
	if operationMemoryStateMatches(exists, content, true, record.AfterContent) {
		return nil
	}
	if !operationMemoryStateMatches(exists, content, record.BeforeExists, record.BeforeContent) {
		return errOperationMemoryRecoveryMismatch
	}
	return fs.writeOperationMemoryTargetLocked(record.Target, record.AfterContent)
}

func (fs *FileStore) recoverOperationMemoryLocked() error {
	record, found, err := fs.readPendingOperationMemoryLocked()
	if err != nil || !found {
		return err
	}
	if receipt, exists, err := fs.readOperationMemoryReceiptLocked(record.OpID); err != nil {
		return err
	} else if exists {
		if !sameReceiptAsWAL(receipt, record) {
			return errOperationMemoryRecoveryMismatch
		}
		if err := fs.verifyReceiptTargetLocked(receipt); err != nil {
			return err
		}
		return fs.removePendingOperationMemoryLocked()
	}
	if err := fs.applyOperationMemoryTargetLocked(record); err != nil {
		return err
	}
	if err := fs.writeOperationMemoryReceiptLocked(receiptFromWAL(record)); err != nil {
		return err
	}
	return fs.removePendingOperationMemoryLocked()
}

func (fs *FileStore) writePendingOperationMemoryLocked(record operationMemoryWAL) error {
	if err := validateOperationMemoryWAL(record); err != nil {
		return err
	}
	if _, found, err := fs.readPendingOperationMemoryLocked(); err != nil {
		return err
	} else if found {
		return errors.New("memory: another operation-memory mutation is pending")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maxOperationMemoryWALBytes {
		return errors.New("memory: operation-memory WAL exceeds size limit")
	}
	return writeOperationMemoryFile(filepath.Join(fs.operationDir, operationMemoryPendingName), data, 0o600)
}

func (fs *FileStore) readPendingOperationMemoryLocked() (operationMemoryWAL, bool, error) {
	path := filepath.Join(fs.operationDir, operationMemoryPendingName)
	data, found, err := readOperationMemoryFile(path, maxOperationMemoryWALBytes)
	if err != nil || !found {
		return operationMemoryWAL{}, found, err
	}
	var record operationMemoryWAL
	if err := decodeOperationMemoryJSON(data, &record); err != nil {
		return operationMemoryWAL{}, false, fmt.Errorf("memory: operation-memory WAL is corrupt: %w", err)
	}
	if err := validateOperationMemoryWAL(record); err != nil {
		return operationMemoryWAL{}, false, fmt.Errorf("memory: operation-memory WAL is corrupt: %w", err)
	}
	return record, true, nil
}

func (fs *FileStore) writeOperationMemoryReceiptLocked(receipt operationMemoryReceiptRecord) error {
	if err := validateOperationMemoryReceipt(receipt); err != nil {
		return err
	}
	path := fs.operationMemoryReceiptPath(receipt.OpID)
	if existing, found, err := fs.readOperationMemoryReceiptLocked(receipt.OpID); err != nil {
		return err
	} else if found {
		if !sameReceiptRecords(existing, receipt) {
			return ErrStorageHostOperationConflict
		}
		return fs.verifyReceiptTargetLocked(existing)
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return writeOperationMemoryFile(path, data, 0o600)
}

func (fs *FileStore) readOperationMemoryReceiptLocked(opID string) (operationMemoryReceiptRecord, bool, error) {
	path := fs.operationMemoryReceiptPath(opID)
	data, found, err := readOperationMemoryFile(path, 16<<10)
	if err != nil || !found {
		return operationMemoryReceiptRecord{}, found, err
	}
	var receipt operationMemoryReceiptRecord
	if err := decodeOperationMemoryJSON(data, &receipt); err != nil {
		return operationMemoryReceiptRecord{}, false, fmt.Errorf("memory: operation-memory receipt is corrupt: %w", err)
	}
	if err := validateOperationMemoryReceipt(receipt); err != nil || receipt.OpID != opID {
		return operationMemoryReceiptRecord{}, false, errors.New("memory: operation-memory receipt is corrupt")
	}
	return receipt, true, nil
}

func (fs *FileStore) verifyReceiptTargetLocked(receipt operationMemoryReceiptRecord) error {
	exists, content, err := fs.readOperationMemoryTargetLocked(receipt.Target)
	if err != nil {
		return err
	}
	if !exists || memorySHA256(content) != receipt.AfterSHA256 {
		return errOperationMemoryRecoveryMismatch
	}
	return nil
}

func (fs *FileStore) writeOperationMemoryTargetLocked(target string, content []byte) error {
	if !validOperationMemoryTarget(target) {
		return errors.New("memory: operation-memory target rejected")
	}
	if _, err := ensureOperationMemoryDirectory(fs.memoryDir, 0o755); err != nil {
		return fmt.Errorf("memory: validate owner directory: %w", err)
	}
	path := filepath.Join(fs.memoryDir, filepath.FromSlash(target))
	dir := filepath.Dir(path)
	created, err := ensureOperationMemoryDirectory(dir, 0o755)
	if err != nil {
		return fmt.Errorf("memory: create note directory: %w", err)
	}
	if created {
		if err := syncOperationMemoryDirectory(fs.memoryDir); err != nil {
			return fmt.Errorf("memory: sync note directory parent: %w", err)
		}
	}
	if err := writeOperationMemoryFile(path, content, 0o644); err != nil {
		return fmt.Errorf("memory: atomically write note: %w", err)
	}
	return nil
}

func (fs *FileStore) readOperationMemoryTargetLocked(target string) (bool, []byte, error) {
	if !validOperationMemoryTarget(target) {
		return false, nil, errors.New("memory: operation-memory target rejected")
	}
	parentsExist, err := validateOperationMemoryReadParents(fs.memoryDir, target)
	if err != nil {
		return false, nil, err
	}
	if !parentsExist {
		return false, nil, nil
	}
	path := filepath.Join(fs.memoryDir, filepath.FromSlash(target))
	data, found, err := readOperationMemoryFile(path, maxStorageHostMemoryFileBytes)
	if err != nil || !found {
		return false, nil, err
	}
	if !utf8.Valid(data) {
		return false, nil, errors.New("memory: canonical memory file is not valid UTF-8")
	}
	return true, data, nil
}

func validateOperationMemoryReadParents(memoryDir, target string) (bool, error) {
	rootInfo, err := os.Lstat(memoryDir)
	if err != nil {
		return false, fmt.Errorf("memory: inspect owner directory: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("memory: owner directory is not a real directory")
	}
	if target == "MEMORY.md" {
		return true, nil
	}
	monthPath := filepath.Join(memoryDir, filepath.FromSlash(target[:6]))
	monthInfo, err := os.Lstat(monthPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memory: inspect note directory: %w", err)
	}
	if !monthInfo.IsDir() || monthInfo.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("memory: note parent is not a real directory")
	}
	return true, nil
}

func (fs *FileStore) operationMemoryReceiptPath(opID string) string {
	name := memorySHA256([]byte(opID)) + ".json"
	return filepath.Join(fs.operationDir, operationMemoryReceiptsName, name)
}

func (fs *FileStore) removePendingOperationMemoryLocked() error {
	path := filepath.Join(fs.operationDir, operationMemoryPendingName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncOperationMemoryDirectory(fs.operationDir)
}

func operationMemoryTarget(operation, date string) (string, error) {
	switch operation {
	case StorageHostMemoryWriteLongTerm:
		if date != "" {
			return "", errors.New("memory: long-term operation has a date")
		}
		return "MEMORY.md", nil
	case StorageHostMemoryAppendToday, StorageHostMemorySaveForDate:
		if !canonicalOperationMemoryDate(date) {
			return "", errors.New("memory: noncanonical operation date")
		}
		compact := strings.ReplaceAll(date, "-", "")
		return compact[:6] + "/" + compact + ".md", nil
	default:
		return "", errors.New("memory: unsupported storage-host operation")
	}
}

func validOperationMemoryTarget(target string) bool {
	if target == "MEMORY.md" {
		return true
	}
	if len(target) != len("200601/20060102.md") || target[6] != '/' || !strings.HasSuffix(target, ".md") {
		return false
	}
	day := target[7:15]
	if target[:6] != day[:6] {
		return false
	}
	date := day[:4] + "-" + day[4:6] + "-" + day[6:8]
	return canonicalOperationMemoryDate(date)
}

func validateOperationMemoryWAL(record operationMemoryWAL) error {
	if record.Version != operationMemoryWALVersion || !validOperationID(record.OpID) || !validMemoryOperationName(record.Operation) || !validMemorySHA256(record.PayloadHash) {
		return errors.New("invalid operation-memory WAL identity")
	}
	wantTarget, err := operationMemoryTarget(record.Operation, record.Date)
	if err != nil || wantTarget != record.Target {
		return errors.New("invalid operation-memory WAL target")
	}
	if len(record.BeforeContent) > maxStorageHostMemoryFileBytes || len(record.AfterContent) > maxStorageHostMemoryFileBytes || !utf8.Valid(record.BeforeContent) || !utf8.Valid(record.AfterContent) {
		return errors.New("invalid operation-memory WAL content size")
	}
	if record.BeforeExists {
		if !validMemorySHA256(record.BeforeSHA256) || memorySHA256(record.BeforeContent) != record.BeforeSHA256 {
			return errors.New("invalid operation-memory WAL before identity")
		}
	} else if len(record.BeforeContent) != 0 || record.BeforeSHA256 != "" {
		return errors.New("invalid absent operation-memory WAL before identity")
	}
	if !validMemorySHA256(record.AfterSHA256) || memorySHA256(record.AfterContent) != record.AfterSHA256 || !bytes.Equal(record.Result, []byte("null")) {
		return errors.New("invalid operation-memory WAL after identity or result")
	}
	return verifyOperationMemoryChecksum(record.RecordSHA256, func() ([]byte, error) {
		copy := record
		copy.RecordSHA256 = ""
		return json.Marshal(copy)
	})
}

func validateOperationMemoryReceipt(receipt operationMemoryReceiptRecord) error {
	if receipt.Version != operationMemoryWALVersion || !validOperationID(receipt.OpID) || !validMemoryOperationName(receipt.Operation) || !validMemorySHA256(receipt.PayloadHash) || !validMemorySHA256(receipt.AfterSHA256) || !bytes.Equal(receipt.Result, []byte("null")) {
		return errors.New("invalid operation-memory receipt fields")
	}
	wantTarget, err := operationMemoryTarget(receipt.Operation, receipt.Date)
	if err != nil || wantTarget != receipt.Target {
		return errors.New("invalid operation-memory receipt target")
	}
	return verifyOperationMemoryChecksum(receipt.RecordSHA256, func() ([]byte, error) {
		copy := receipt
		copy.RecordSHA256 = ""
		return json.Marshal(copy)
	})
}

func sealOperationMemoryWAL(record *operationMemoryWAL) error {
	record.RecordSHA256 = ""
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	record.RecordSHA256 = memorySHA256(data)
	return nil
}

func sealOperationMemoryReceipt(receipt *operationMemoryReceiptRecord) error {
	receipt.RecordSHA256 = ""
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receipt.RecordSHA256 = memorySHA256(data)
	return nil
}

func verifyOperationMemoryChecksum(actual string, makeBody func() ([]byte, error)) error {
	if !validMemorySHA256(actual) {
		return errors.New("invalid operation-memory record checksum")
	}
	body, err := makeBody()
	if err != nil {
		return err
	}
	if memorySHA256(body) != actual {
		return errors.New("operation-memory record checksum mismatch")
	}
	return nil
}

func receiptFromWAL(record operationMemoryWAL) operationMemoryReceiptRecord {
	receipt := operationMemoryReceiptRecord{
		Version:     record.Version,
		OpID:        record.OpID,
		Operation:   record.Operation,
		PayloadHash: record.PayloadHash,
		Date:        record.Date,
		Target:      record.Target,
		AfterSHA256: record.AfterSHA256,
		Result:      append(json.RawMessage(nil), record.Result...),
	}
	_ = sealOperationMemoryReceipt(&receipt)
	return receipt
}

func publicMemoryReceipt(receipt operationMemoryReceiptRecord) StorageHostMemoryReceipt {
	return StorageHostMemoryReceipt{
		OpID:        receipt.OpID,
		Operation:   receipt.Operation,
		PayloadHash: receipt.PayloadHash,
		ResultJSON:  append([]byte(nil), receipt.Result...),
	}
}

func sameMemoryOperationIdentity(gotID, gotOperation, gotHash, wantID, wantOperation, wantHash string) bool {
	return gotID == wantID && gotOperation == wantOperation && gotHash == wantHash
}

func sameReceiptAsWAL(receipt operationMemoryReceiptRecord, record operationMemoryWAL) bool {
	return receipt.OpID == record.OpID && receipt.Operation == record.Operation && receipt.PayloadHash == record.PayloadHash &&
		receipt.Date == record.Date && receipt.Target == record.Target && receipt.AfterSHA256 == record.AfterSHA256 &&
		bytes.Equal(receipt.Result, record.Result)
}

func sameReceiptRecords(left, right operationMemoryReceiptRecord) bool {
	return left.Version == right.Version && left.OpID == right.OpID && left.Operation == right.Operation &&
		left.PayloadHash == right.PayloadHash && left.Date == right.Date && left.Target == right.Target &&
		left.AfterSHA256 == right.AfterSHA256 && bytes.Equal(left.Result, right.Result) && left.RecordSHA256 == right.RecordSHA256
}

func operationMemoryStateMatches(gotExists bool, got []byte, wantExists bool, want []byte) bool {
	if gotExists != wantExists {
		return false
	}
	if !wantExists {
		return true
	}
	return bytes.Equal(got, want)
}

func canonicalOperationMemoryDate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Year() > 0 && parsed.Format("2006-01-02") == value
}

func validOperationID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '.' && char != '_' && char != ':' && char != '-' {
			return false
		}
	}
	return true
}

func validMemoryOperationName(value string) bool {
	switch value {
	case StorageHostMemoryWriteLongTerm, StorageHostMemoryAppendToday, StorageHostMemorySaveForDate:
		return true
	default:
		return false
	}
}

func validMemorySHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func memorySHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func decodeOperationMemoryJSON(data []byte, destination any) error {
	if len(data) == 0 || !json.Valid(data) {
		return errors.New("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func ensureOperationMemoryDirectory(path string, mode os.FileMode) (bool, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("operation-memory path is not a real directory")
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.Mkdir(path, mode); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("operation-memory path was concurrently replaced")
		}
		return false, nil
	}
	return true, nil
}

func readOperationMemoryFile(path string, maxBytes int64) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return nil, false, errors.New("operation-memory file type or size rejected")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, false, errors.New("operation-memory file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return nil, false, errors.New("operation-memory file exceeds size limit")
	}
	return data, true, nil
}

func writeOperationMemoryFile(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".operation-memory-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	written, err := tmp.Write(content)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if written != len(content) {
		_ = tmp.Close()
		return io.ErrShortWrite
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncOperationMemoryDirectory(dir)
}

func syncOperationMemoryDirectory(path string) error {
	// NTFS persists directory metadata through its journal; Windows does not
	// permit flushing a directory handle. Unix filesystems require the explicit
	// parent-directory fsync after a rename or entry removal.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
