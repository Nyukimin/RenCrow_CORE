package jsonlbatch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	lockFilename    = ".jsonlbatch.lock"
	journalFilename = ".jsonlbatch.wal"
	journalVersion  = 1

	journalPrepare  = "prepare"
	journalCommit   = "commit"
	journalRollback = "rollback"

	// Bounds apply to one payload and one journal record. The journal itself is
	// deliberately append-only; callers should compact it only as an explicit
	// owner-level operation after the corresponding evidence is available.
	maxPayloadBytes       = 64 << 20
	maxJSONLRecordBytes   = 16 << 20
	maxJournalRecordBytes = 1 << 20
)

var (
	// ErrRecoveryRequired means that a reader cannot establish a committed
	// snapshot because the journal contains a torn or pending transaction.
	ErrRecoveryRequired = errors.New("jsonl batch recovery required")
	// ErrJournalCorrupt is returned together with ErrRecoveryRequired for a
	// complete journal record that violates the journal contract.
	ErrJournalCorrupt = errors.New("jsonl batch journal is corrupt")
	// ErrCommitUncertain means the commit record append did not produce a
	// confirmed durable result. The data is deliberately left untouched; the
	// next writer must inspect and repair the WAL before proceeding.
	ErrCommitUncertain = errors.New("jsonl batch commit is uncertain")
	errInvalidFilename = errors.New("jsonl batch filename is invalid")
)

// Store coordinates append-only JSONL files that share one owner-local WAL.
// The helper knows only file names and byte payloads; JSON schema remains with
// the owning persistence package.
type Store struct {
	root      string
	filenames []string
	allowed   map[string]struct{}
	lockPath  string
	walPath   string
	readOnly  bool
	// syncFn and closeFn are nil in production. They are narrow package-local
	// seams for deterministic durability-failure tests; callers cannot replace
	// the owner-level file operations through the public API.
	syncFn  func(*os.File) error
	closeFn func(*os.File) error
}

type journalFile struct {
	Name          string `json:"name"`
	Offset        int64  `json:"offset"`
	AppendLength  int64  `json:"append_length"`
	PrefixSHA256  string `json:"prefix_sha256"`
	PayloadSHA256 string `json:"payload_sha256"`
}

type journalRecord struct {
	Version int           `json:"version"`
	Kind    string        `json:"kind"`
	TxID    string        `json:"tx_id"`
	Files   []journalFile `json:"files,omitempty"`
}

type journalState struct {
	pending     *journalRecord
	torn        bool
	completeLen int64
}

type preparedFile struct {
	journal journalFile
	payload []byte
}

// New creates or opens the fixed set of owner-local files. It intentionally
// does not recover a pending WAL; recovery is a writer operation and must be
// requested explicitly through Recover or Write.
func New(root string, filenames []string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("jsonl batch root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve jsonl batch root: %w", err)
	}
	if err := rejectSymlinkPath(absRoot); err != nil {
		return nil, fmt.Errorf("jsonl batch root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create jsonl batch root: %w", err)
	}
	rootInfo, err := os.Lstat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("stat jsonl batch root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("jsonl batch root is not a directory")
	}
	if err := rejectSymlinkPath(absRoot); err != nil {
		return nil, fmt.Errorf("jsonl batch root: %w", err)
	}

	allowed, ordered, err := validateFilenames(filenames)
	if err != nil {
		return nil, err
	}
	s := &Store{
		root:      absRoot,
		filenames: ordered,
		allowed:   allowed,
		lockPath:  filepath.Join(absRoot, lockFilename),
		walPath:   filepath.Join(absRoot, journalFilename),
	}
	if err := ensureRegularFile(s.lockPath, 0o600); err != nil {
		return nil, fmt.Errorf("initialize jsonl batch lock: %w", err)
	}
	if err := ensureRegularFile(s.walPath, 0o600); err != nil {
		return nil, fmt.Errorf("initialize jsonl batch journal: %w", err)
	}
	for _, name := range s.filenames {
		if err := ensureRegularFile(s.pathFor(name), 0o644); err != nil {
			return nil, fmt.Errorf("initialize jsonl batch file %q: %w", name, err)
		}
	}
	if err := syncDirectory(absRoot); err != nil {
		return nil, fmt.Errorf("persist jsonl batch directory entries: %w", err)
	}
	return s, nil
}

// OpenReader opens an existing data set without creating data files or a WAL.
// The lock file is shared metadata and is initialized when it is absent so a
// reader opened during the writer migration still participates in the same OS
// exclusion. A missing WAL represents the pre-WAL empty history and is valid
// for reads; a reader can never invoke Write or Recover.
func OpenReader(root string, filenames []string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("jsonl batch root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve jsonl batch root: %w", err)
	}
	if err := rejectSymlinkPath(absRoot); err != nil {
		return nil, fmt.Errorf("jsonl batch root: %w", err)
	}
	rootInfo, err := os.Lstat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("open jsonl batch reader root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("jsonl batch reader root is not a directory")
	}
	allowed, ordered, err := validateFilenames(filenames)
	if err != nil {
		return nil, err
	}
	s := &Store{
		root:      absRoot,
		filenames: ordered,
		allowed:   allowed,
		lockPath:  filepath.Join(absRoot, lockFilename),
		walPath:   filepath.Join(absRoot, journalFilename),
		readOnly:  true,
	}
	// The lock is coordination metadata, not source data. Initializing it is
	// required to make a reader safe against a writer's first migration.
	if err := ensureRegularFile(s.lockPath, 0o600); err != nil {
		return nil, fmt.Errorf("initialize jsonl batch reader lock: %w", err)
	}
	for _, name := range s.filenames {
		if err := ensureReadableFile(s.pathFor(name)); err != nil {
			return nil, fmt.Errorf("open jsonl batch reader file %q: %w", name, err)
		}
	}
	if info, statErr := os.Lstat(s.walPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("jsonl batch reader journal is not a regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("stat jsonl batch reader journal: %w", statErr)
	}
	return s, nil
}

// Read runs callback while holding the same exclusive OS lock used by writes.
// A reader never repairs the WAL, so a torn or pending transaction fails
// closed with ErrRecoveryRequired before callback is invoked.
func (s *Store) Read(ctx context.Context, callback func() error) error {
	if callback == nil {
		return fmt.Errorf("jsonl batch read callback is nil")
	}
	return s.withLock(ctx, func() error {
		if err := s.readCommittedLocked(); err != nil {
			return err
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		return callback()
	})
}

// Write obtains the owner-local lock, repairs an earlier pending transaction,
// obtains schema-specific append bytes from callback, and commits all selected
// files through one append-only WAL transaction.
func (s *Store) Write(ctx context.Context, callback func() (map[string][]byte, error)) error {
	if callback == nil {
		return fmt.Errorf("jsonl batch write callback is nil")
	}
	if s.readOnly {
		return fmt.Errorf("jsonl batch reader is read-only")
	}
	return s.withLock(ctx, func() error {
		if err := s.recoverLocked(ctx); err != nil {
			return err
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		payloads, err := callback()
		if err != nil {
			return err
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		prepared, err := s.preparePayloads(payloads)
		if err != nil {
			return err
		}
		if len(prepared) == 0 {
			return nil
		}
		txID, err := newTxID()
		if err != nil {
			return err
		}
		prepare := journalRecord{
			Version: journalVersion,
			Kind:    journalPrepare,
			TxID:    txID,
			Files:   make([]journalFile, len(prepared)),
		}
		for i := range prepared {
			prepare.Files[i] = prepared[i].journal
		}
		if err := s.appendJournal(prepare); err != nil {
			// A failed append or Sync may have left a terminal torn record. A
			// writer must leave this visible as recovery-required rather than
			// claiming a clean transaction.
			return errors.Join(err, ErrRecoveryRequired)
		}

		for _, item := range prepared {
			if err := contextError(ctx); err != nil {
				return s.rollbackFailedTransaction(prepare, err)
			}
			if err := s.appendPrepared(ctx, item); err != nil {
				return s.rollbackFailedTransaction(prepare, err)
			}
		}
		if err := contextError(ctx); err != nil {
			return s.rollbackFailedTransaction(prepare, err)
		}
		commit := journalRecord{Version: journalVersion, Kind: journalCommit, TxID: txID}
		if err := s.appendJournal(commit); err != nil {
			// The commit append may have written a complete line before Sync or
			// Close failed. Rolling back here could leave a committed WAL paired
			// with truncated data. Leave the bytes untouched and make the next
			// writer determine the outcome from the WAL shape.
			return errors.Join(ErrCommitUncertain, ErrRecoveryRequired, err)
		}
		return nil
	})
}

// Recover is the explicit writer-side WAL repair operation. It truncates only
// a terminal torn record, rolls back a complete pending prepare, and records
// that rollback as another append-only journal record.
func (s *Store) Recover(ctx context.Context) error {
	if s.readOnly {
		return fmt.Errorf("jsonl batch reader is read-only")
	}
	return s.withLock(ctx, func() error { return s.recoverLocked(ctx) })
}

func (s *Store) pathFor(name string) string {
	return filepath.Join(s.root, name)
}

func (s *Store) readCommittedLocked() error {
	data, err := s.readJournalData()
	if err != nil {
		return err
	}
	state, err := parseJournal(data, s.allowed)
	if err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if state.torn {
		return fmt.Errorf("%w: journal has a torn terminal record", ErrRecoveryRequired)
	}
	if state.pending != nil {
		return fmt.Errorf("%w: transaction %q is pending", ErrRecoveryRequired, state.pending.TxID)
	}
	for _, name := range s.filenames {
		if _, err := regularFileInfo(s.pathFor(name)); err != nil {
			return fmt.Errorf("validate jsonl batch file %q: %w", name, err)
		}
	}
	return nil
}

func (s *Store) recoverLocked(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	data, err := s.readJournalData()
	if err != nil {
		return err
	}
	state, err := parseJournal(data, s.allowed)
	if err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if state.torn {
		if err := truncateJournal(s.walPath, state.completeLen); err != nil {
			return errors.Join(ErrRecoveryRequired, err)
		}
		data = data[:state.completeLen]
		state, err = parseJournal(data, s.allowed)
		if err != nil {
			return errors.Join(ErrRecoveryRequired, err)
		}
	}
	if state.pending == nil {
		return nil
	}
	if err := s.rollbackRecord(*state.pending); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	rollback := journalRecord{
		Version: journalVersion,
		Kind:    journalRollback,
		TxID:    state.pending.TxID,
	}
	if err := s.appendJournal(rollback); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	return nil
}

func (s *Store) preparePayloads(payloads map[string][]byte) ([]preparedFile, error) {
	if len(payloads) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(payloads))
	for name := range payloads {
		if _, ok := s.allowed[name]; !ok {
			return nil, fmt.Errorf("jsonl batch payload filename %q is not allowed", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	prepared := make([]preparedFile, 0, len(names))
	for _, name := range names {
		payload := payloads[name]
		if err := validatePayload(payload); err != nil {
			return nil, fmt.Errorf("validate payload %q: %w", name, err)
		}
		path := s.pathFor(name)
		info, err := regularFileInfo(path)
		if err != nil {
			return nil, fmt.Errorf("stat payload file %q: %w", name, err)
		}
		prefix, err := hashFileRange(path, 0, info.Size())
		if err != nil {
			return nil, fmt.Errorf("hash payload prefix %q: %w", name, err)
		}
		prepared = append(prepared, preparedFile{
			journal: journalFile{
				Name:          name,
				Offset:        info.Size(),
				AppendLength:  int64(len(payload)),
				PrefixSHA256:  prefix,
				PayloadSHA256: hashBytes(payload),
			},
			payload: append([]byte(nil), payload...),
		})
	}
	return prepared, nil
}

func (s *Store) appendPrepared(ctx context.Context, item preparedFile) (result error) {
	if err := contextError(ctx); err != nil {
		return err
	}
	path := s.pathFor(item.journal.Name)
	if err := rejectSymlinkPath(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open jsonl batch append %q: %w", item.journal.Name, err)
	}
	defer func() {
		result = errors.Join(result, s.closeFileFor(f))
	}()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat jsonl batch append %q: %w", item.journal.Name, err)
	}
	if !info.Mode().IsRegular() || info.Size() != item.journal.Offset {
		return fmt.Errorf("jsonl batch append %q changed after prepare", item.journal.Name)
	}
	prefix, err := hashFileRange(path, 0, item.journal.Offset)
	if err != nil {
		return fmt.Errorf("verify jsonl batch prefix %q: %w", item.journal.Name, err)
	}
	if prefix != item.journal.PrefixSHA256 {
		return fmt.Errorf("jsonl batch prefix %q changed after prepare", item.journal.Name)
	}
	if err := writeFull(f, item.payload); err != nil {
		return fmt.Errorf("append jsonl batch file %q: %w", item.journal.Name, err)
	}
	if err := s.syncFileFor(f); err != nil {
		return fmt.Errorf("sync jsonl batch file %q: %w", item.journal.Name, err)
	}
	info, err = f.Stat()
	if err != nil {
		return fmt.Errorf("stat appended jsonl batch file %q: %w", item.journal.Name, err)
	}
	if info.Size() != item.journal.Offset+item.journal.AppendLength {
		return fmt.Errorf("jsonl batch append %q has unexpected size", item.journal.Name)
	}
	payloadHash, err := hashFileRange(path, item.journal.Offset, item.journal.AppendLength)
	if err != nil {
		return fmt.Errorf("verify jsonl batch append %q: %w", item.journal.Name, err)
	}
	if payloadHash != item.journal.PayloadSHA256 {
		return fmt.Errorf("jsonl batch append %q changed after write", item.journal.Name)
	}
	return result
}

func (s *Store) rollbackFailedTransaction(prepare journalRecord, cause error) error {
	rollbackErr := s.rollbackRecord(prepare)
	if rollbackErr != nil {
		return errors.Join(cause, ErrRecoveryRequired, rollbackErr)
	}
	rollback := journalRecord{Version: journalVersion, Kind: journalRollback, TxID: prepare.TxID}
	if err := s.appendJournal(rollback); err != nil {
		return errors.Join(cause, ErrRecoveryRequired, err)
	}
	return cause
}

func (s *Store) appendJournal(record journalRecord) error {
	return appendJournalRecordWith(s.walPath, record, s.syncFileFor, s.closeFileFor)
}

func (s *Store) syncFileFor(file *os.File) error {
	if s.syncFn != nil {
		return s.syncFn(file)
	}
	return file.Sync()
}

func (s *Store) closeFileFor(file *os.File) error {
	if s.closeFn != nil {
		return s.closeFn(file)
	}
	return file.Close()
}

func (s *Store) rollbackRecord(record journalRecord) error {
	for _, item := range record.Files {
		path := s.pathFor(item.Name)
		if err := rejectSymlinkPath(path); err != nil {
			return err
		}
		info, err := regularFileInfo(path)
		if err != nil {
			return fmt.Errorf("stat rollback file %q: %w", item.Name, err)
		}
		end := item.Offset + item.AppendLength
		if item.Offset < 0 || item.AppendLength <= 0 || info.Size() < item.Offset || info.Size() > end {
			return fmt.Errorf("rollback file %q is outside prepared range", item.Name)
		}
		prefix, err := hashFileRange(path, 0, item.Offset)
		if err != nil {
			return fmt.Errorf("hash rollback prefix %q: %w", item.Name, err)
		}
		if prefix != item.PrefixSHA256 {
			return fmt.Errorf("rollback prefix %q does not match prepare", item.Name)
		}
		if info.Size() == end {
			payloadHash, err := hashFileRange(path, item.Offset, item.AppendLength)
			if err != nil {
				return fmt.Errorf("hash rollback payload %q: %w", item.Name, err)
			}
			if payloadHash != item.PayloadSHA256 {
				return fmt.Errorf("rollback payload %q does not match prepare", item.Name)
			}
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("open rollback file %q: %w", item.Name, err)
		}
		truncateErr := f.Truncate(item.Offset)
		syncErr := error(nil)
		if truncateErr == nil {
			syncErr = s.syncFileFor(f)
		}
		closeErr := s.closeFileFor(f)
		if err := errors.Join(truncateErr, syncErr, closeErr); err != nil {
			return fmt.Errorf("rollback file %q: %w", item.Name, err)
		}
	}
	return nil
}

func validateFilenames(filenames []string) (map[string]struct{}, []string, error) {
	if len(filenames) == 0 {
		return nil, nil, fmt.Errorf("jsonl batch requires at least one data filename")
	}
	allowed := make(map[string]struct{}, len(filenames))
	ordered := make([]string, 0, len(filenames))
	for _, name := range filenames {
		if err := validateFilename(name); err != nil {
			return nil, nil, err
		}
		if _, exists := allowed[name]; exists {
			return nil, nil, fmt.Errorf("duplicate jsonl batch filename %q", name)
		}
		for existing := range allowed {
			if strings.EqualFold(existing, name) {
				return nil, nil, fmt.Errorf("case-insensitive duplicate jsonl batch filename %q", name)
			}
		}
		allowed[name] = struct{}{}
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return allowed, ordered, nil
}

func validateFilename(name string) error {
	if name == "" || name == "." || name == ".." || strings.IndexByte(name, 0) >= 0 {
		return fmt.Errorf("%w: %q", errInvalidFilename, name)
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" || filepath.Base(name) != name {
		return fmt.Errorf("%w: %q must be a relative basename", errInvalidFilename, name)
	}
	if strings.ContainsAny(name, `/\\:`) || strings.ContainsAny(name, `<>"|?*`) {
		return fmt.Errorf("%w: %q contains a path or non-portable character", errInvalidFilename, name)
	}
	if strings.EqualFold(name, lockFilename) || strings.EqualFold(name, journalFilename) {
		return fmt.Errorf("%w: %q is reserved", errInvalidFilename, name)
	}
	return nil
}

func validatePayload(payload []byte) error {
	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return fmt.Errorf("payload size must be between 1 and %d bytes", maxPayloadBytes)
	}
	if payload[len(payload)-1] != '\n' {
		return fmt.Errorf("payload must end with newline")
	}
	start := 0
	for i, b := range payload {
		if b != '\n' {
			continue
		}
		if i == start {
			return fmt.Errorf("payload contains an empty JSONL record")
		}
		if i-start > maxJSONLRecordBytes {
			return fmt.Errorf("JSONL record exceeds %d bytes", maxJSONLRecordBytes)
		}
		start = i + 1
	}
	return nil
}

func validateJournalRecord(record journalRecord, allowed map[string]struct{}) error {
	if record.Version != journalVersion {
		return fmt.Errorf("unsupported journal version %d", record.Version)
	}
	if record.TxID == "" || len(record.TxID) > 128 || strings.TrimSpace(record.TxID) != record.TxID {
		return fmt.Errorf("invalid journal transaction id")
	}
	switch record.Kind {
	case journalPrepare:
		if len(record.Files) == 0 {
			return fmt.Errorf("prepare record has no files")
		}
		seen := make(map[string]struct{}, len(record.Files))
		for _, item := range record.Files {
			if err := validateFilename(item.Name); err != nil {
				return err
			}
			if allowed != nil {
				if _, ok := allowed[item.Name]; !ok {
					return fmt.Errorf("journal file %q is outside allowlist", item.Name)
				}
			}
			if _, ok := seen[item.Name]; ok {
				return fmt.Errorf("prepare record repeats file %q", item.Name)
			}
			seen[item.Name] = struct{}{}
			if item.Offset < 0 || item.AppendLength <= 0 || item.AppendLength > maxPayloadBytes || item.Offset > (1<<63-1)-item.AppendLength {
				return fmt.Errorf("invalid prepared range for %q", item.Name)
			}
			if !validHash(item.PrefixSHA256) || !validHash(item.PayloadSHA256) {
				return fmt.Errorf("invalid prepared hash for %q", item.Name)
			}
		}
	case journalCommit, journalRollback:
		if len(record.Files) != 0 {
			return fmt.Errorf("%s record must not contain files", record.Kind)
		}
	default:
		return fmt.Errorf("unknown journal record kind %q", record.Kind)
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func parseJournal(data []byte, allowed map[string]struct{}) (journalState, error) {
	state := journalState{completeLen: int64(len(data))}
	if len(data) == 0 {
		return state, nil
	}
	complete := data
	if data[len(data)-1] != '\n' {
		state.torn = true
		last := bytes.LastIndexByte(data, '\n')
		state.completeLen = int64(last + 1)
		if len(data)-last-1 > maxJournalRecordBytes {
			return journalState{}, fmt.Errorf("%w: torn journal record exceeds %d bytes", ErrJournalCorrupt, maxJournalRecordBytes)
		}
		complete = data[:last+1]
	}
	active := make(map[string]journalRecord)
	finished := make(map[string]struct{})
	if len(complete) == 0 {
		return state, nil
	}
	lines := bytes.Split(complete[:len(complete)-1], []byte{'\n'})
	for _, line := range lines {
		if len(line) == 0 || len(line) > maxJournalRecordBytes {
			return journalState{}, fmt.Errorf("%w: journal record length is invalid", ErrJournalCorrupt)
		}
		var record journalRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return journalState{}, fmt.Errorf("%w: decode journal record: %v", ErrJournalCorrupt, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return journalState{}, fmt.Errorf("%w: journal line contains multiple values", ErrJournalCorrupt)
			}
			return journalState{}, fmt.Errorf("%w: trailing journal value: %v", ErrJournalCorrupt, err)
		}
		if err := validateJournalRecord(record, allowed); err != nil {
			return journalState{}, fmt.Errorf("%w: %v", ErrJournalCorrupt, err)
		}
		switch record.Kind {
		case journalPrepare:
			if len(active) != 0 {
				return journalState{}, fmt.Errorf("%w: more than one transaction is pending", ErrJournalCorrupt)
			}
			if _, ok := active[record.TxID]; ok {
				return journalState{}, fmt.Errorf("%w: duplicate prepare %q", ErrJournalCorrupt, record.TxID)
			}
			if _, ok := finished[record.TxID]; ok {
				return journalState{}, fmt.Errorf("%w: reused transaction id %q", ErrJournalCorrupt, record.TxID)
			}
			active[record.TxID] = record
		case journalCommit, journalRollback:
			if _, ok := active[record.TxID]; !ok {
				return journalState{}, fmt.Errorf("%w: %s without prepare %q", ErrJournalCorrupt, record.Kind, record.TxID)
			}
			delete(active, record.TxID)
			finished[record.TxID] = struct{}{}
		}
	}
	if len(active) > 1 {
		return journalState{}, fmt.Errorf("%w: multiple pending transactions", ErrJournalCorrupt)
	}
	for _, record := range active {
		copyRecord := record
		state.pending = &copyRecord
	}
	return state, nil
}

func (s *Store) readJournalData() ([]byte, error) {
	if err := rejectSymlinkPath(s.walPath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.walPath)
	if errors.Is(err, os.ErrNotExist) && s.readOnly {
		return []byte{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read jsonl batch journal: %w", err)
	}
	return data, nil
}

func appendJournalRecord(path string, record journalRecord) error {
	return appendJournalRecordWith(path, record, nil, nil)
}

func appendJournalRecordWith(path string, record journalRecord, syncFn func(*os.File) error, closeFn func(*os.File) error) error {
	if err := validateJournalRecord(record, nil); err != nil {
		return err
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode jsonl batch journal: %w", err)
	}
	if len(line)+1 > maxJournalRecordBytes {
		return fmt.Errorf("journal record exceeds %d bytes", maxJournalRecordBytes)
	}
	line = append(line, '\n')
	if err := rejectSymlinkPath(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open jsonl batch journal: %w", err)
	}
	writeErr := error(nil)
	if info, statErr := f.Stat(); statErr != nil {
		writeErr = statErr
	} else if !info.Mode().IsRegular() {
		writeErr = fmt.Errorf("jsonl batch journal is not a regular file")
	} else {
		writeErr = writeFull(f, line)
		if writeErr == nil {
			if syncFn != nil {
				writeErr = syncFn(f)
			} else {
				writeErr = f.Sync()
			}
		}
	}
	closeErr := error(nil)
	if closeFn != nil {
		closeErr = closeFn(f)
	} else {
		closeErr = f.Close()
	}
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("append jsonl batch journal: %w", err)
	}
	return nil
}

func truncateJournal(path string, size int64) error {
	if size < 0 {
		return fmt.Errorf("journal truncation offset is negative")
	}
	if err := rejectSymlinkPath(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open jsonl batch journal for repair: %w", err)
	}
	truncateErr := f.Truncate(size)
	syncErr := error(nil)
	if truncateErr == nil {
		syncErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(truncateErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("repair jsonl batch journal: %w", err)
	}
	return nil
}

func ensureRegularFile(path string, mode os.FileMode) error {
	if err := rejectSymlinkPath(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, mode)
	if errors.Is(err, os.ErrExist) {
		if err := rejectSymlinkPath(path); err != nil {
			return err
		}
		f, err = os.OpenFile(path, os.O_RDWR, mode)
	}
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	syncErr := error(nil)
	if statErr == nil && info.Mode().IsRegular() {
		syncErr = f.Sync()
	}
	closeErr := f.Close()
	if statErr != nil {
		return statErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	if err := rejectSymlinkPath(path); err != nil {
		return err
	}
	return nil
}

func regularFileInfo(path string) (os.FileInfo, error) {
	if err := rejectSymlinkPath(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file")
	}
	return info, nil
}

func ensureReadableFile(path string) error {
	if err := rejectSymlinkPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

func rejectSymlinkPath(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absPath)
	rest := strings.TrimPrefix(absPath, volume)
	current := volume
	separator := string(filepath.Separator)
	if strings.HasPrefix(rest, separator) {
		current += separator
		rest = strings.TrimLeft(rest, separator)
	}
	for _, part := range strings.Split(rest, separator) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed: %s", current)
		}
	}
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashFileRange(path string, offset, length int64) (string, error) {
	if offset < 0 || length < 0 {
		return "", fmt.Errorf("hash range is negative")
	}
	if err := rejectSymlinkPath(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if offset > info.Size() || length > info.Size()-offset {
		return "", fmt.Errorf("hash range exceeds file size")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, f, length); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func newTxID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate jsonl batch transaction id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
