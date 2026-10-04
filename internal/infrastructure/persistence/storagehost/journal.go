package storagehost

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ErrJournalCorrupt is reported when a journal record in the middle of the
// durable file cannot be decoded. Mid-stream corruption drops the dedup
// identity of a committed operation, so the storage host fails closed instead
// of silently skipping records.
var ErrJournalCorrupt = errors.New("storagehost: journal record is corrupt")

const writerLockFileName = "writer.lock"

// Journal statuses. A mutating operation is recorded as begun and fsynced
// before the owner store is touched, and as done only after the store commit
// succeeded. An entry left in begun means "the commit may exist": it is never
// re-executed blindly, and the caller must resolve it through the store's own
// read path (read-your-writes) or a fresh op_id issued by the owner.
const (
	journalStatusBegun   = "begun"
	journalStatusDone    = "done"
	journalStatusAborted = "aborted"
)

const (
	journalFileName    = "journal.jsonl"
	generationFileName = "generation"
	generationTempName = "generation.tmp"
)

// syncDir makes a directory's entry metadata durable. It is a variable so
// tests can observe the durability order; the platform implementations
// define the semantics (unix fsync, NTFS write-through metadata).
var syncDir = syncDirPlatform

// JournalEntry is the durable record of one mutating operation.
type JournalEntry struct {
	OpID        string          `json:"op_id"`
	Group       string          `json:"group"`
	Op          string          `json:"op"`
	PayloadHash string          `json:"payload_hash"`
	Scope       string          `json:"scope"`
	Generation  int64           `json:"generation"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
}

// journal is the storage host idempotency journal. It lives in the storage
// host's own durable directory and is written by the single writer process.
type journal struct {
	mu    sync.Mutex
	path  *os.File
	index map[string]JournalEntry
	order []string
	gen   int64

	// lock is held for the whole handler lifetime: only one process may open
	// the durable journal directory at a time.
	lock *writerLock
}

type journalRecord = JournalEntry

// openJournal loads the durable journal in dir and advances the writer
// generation epoch. The generation is monotonic across storage host restarts:
// a CORE that still holds an older epoch is rejected on every write.
func openJournal(dir string) (*journal, error) {
	if dir == "" {
		return nil, errors.New("storagehost: journal dir required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("storagehost: journal dir is not a directory")
	}
	// The writer lock is acquired before the generation is advanced and
	// before the journal is recovered, and held until Close. A second process
	// over the same directory is refused here, not at first write.
	lock, err := acquireWriterLock(filepath.Join(dir, writerLockFileName))
	if err != nil {
		return nil, err
	}
	gen, err := loadGeneration(filepath.Join(dir, generationFileName))
	if err != nil {
		return nil, err
	}
	jn := &journal{index: map[string]JournalEntry{}, gen: gen, lock: lock}
	if err := jn.load(filepath.Join(dir, journalFileName)); err != nil {
		_ = lock.release()
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, journalFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = lock.release()
		return nil, err
	}
	// The journal file's creation is durable only once the parent directory
	// entry is synced: the sync happens after the file exists and before the
	// writer accepts owner writes. Process-crash tests are not power-loss
	// proofs; this pins the platform durability ordering that is tested.
	if err := syncDir(dir); err != nil {
		_ = f.Close()
		_ = lock.release()
		return nil, err
	}
	jn.path = f
	return jn, nil
}

func loadGeneration(path string) (int64, error) {
	var current int64
	if body, err := os.ReadFile(path); err == nil {
		parsed, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
		if err != nil {
			return 0, errors.New("storagehost: writer generation file is corrupt")
		}
		if parsed < 0 {
			return 0, errors.New("storagehost: writer generation file is corrupt")
		}
		current = parsed
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	next := current + 1
	tmp := filepath.Join(filepath.Dir(path), generationTempName)
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(next, 10)+"\n"), 0o600); err != nil {
		return 0, err
	}
	f, err := os.Open(tmp)
	if err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return 0, err
	}
	return next, nil
}

func (j *journal) load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	// A trailing record without its newline is a crash artefact. Validate the
	// boundary: a decodable record is kept with its newline repaired, anything
	// else is truncated at the last complete record and fsynced before the
	// writer appends again.
	if data[len(data)-1] != '\n' {
		last := bytes.LastIndexByte(data, '\n')
		tail := data[last+1:]
		var rec journalRecord
		if json.Valid(tail) && json.Unmarshal(tail, &rec) == nil && validJournalStatus(rec.Status) && rec.OpID != "" {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return err
			}
			if _, err := f.Write([]byte("\n")); err != nil {
				f.Close()
				return err
			}
			if err := f.Sync(); err != nil {
				f.Close()
				return err
			}
			err = f.Close()
			if err != nil {
				return err
			}
			data = append(data, '\n')
		} else {
			boundary := int64(last + 1)
			f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if err := f.Truncate(boundary); err != nil {
				f.Close()
				return err
			}
			if err := f.Sync(); err != nil {
				f.Close()
				return err
			}
			err = f.Close()
			if err != nil {
				return err
			}
			data = data[:boundary]
		}
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var rec journalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("%w: %v", ErrJournalCorrupt, err)
		}
		if rec.OpID == "" || !validJournalStatus(rec.Status) {
			return fmt.Errorf("%w: record fields rejected", ErrJournalCorrupt)
		}
		j.apply(rec)
	}
	return nil
}

func validJournalStatus(status string) bool {
	switch status {
	case journalStatusBegun, journalStatusDone, journalStatusAborted:
		return true
	}
	return false
}

func (j *journal) apply(rec journalRecord) {
	if rec.OpID == "" {
		return
	}
	switch rec.Status {
	case journalStatusAborted:
		if _, ok := j.index[rec.OpID]; ok {
			delete(j.index, rec.OpID)
			j.dropOrder(rec.OpID)
		}
	case journalStatusBegun, journalStatusDone:
		if _, ok := j.index[rec.OpID]; !ok {
			j.order = append(j.order, rec.OpID)
		}
		j.index[rec.OpID] = rec
	}
}

func (j *journal) dropOrder(opID string) {
	for i, id := range j.order {
		if id != opID {
			continue
		}
		j.order = append(j.order[:i], j.order[i+1:]...)
		return
	}
}

// Generation is the storage host writer epoch.
func (j *journal) Generation() int64 { return j.gen }

func (j *journal) lookup(opID string) (JournalEntry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	entry, ok := j.index[opID]
	return entry, ok
}

func (j *journal) entries() []JournalEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]JournalEntry, 0, len(j.order))
	for _, id := range j.order {
		if entry, ok := j.index[id]; ok {
			out = append(out, entry)
		}
	}
	return out
}

func (j *journal) begin(opID, group, op, hash string, scope string, generation int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.index[opID]; ok {
		return nil
	}
	rec := journalRecord{OpID: opID, Group: group, Op: op, PayloadHash: hash, Scope: scope, Generation: generation, Status: journalStatusBegun}
	if err := j.append(rec); err != nil {
		return err
	}
	j.index[opID] = rec
	j.order = append(j.order, opID)
	return nil
}

func (j *journal) complete(opID string, result []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	rec, ok := j.index[opID]
	if !ok {
		return errors.New("storagehost: journal entry is missing")
	}
	rec.Status = journalStatusDone
	rec.Result = json.RawMessage(append([]byte(nil), result...))
	if err := j.append(rec); err != nil {
		return err
	}
	j.index[opID] = rec
	return nil
}

func (j *journal) abort(opID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.index[opID]; !ok {
		return nil
	}
	err := j.append(journalRecord{OpID: opID, Status: journalStatusAborted})
	if err != nil {
		return err
	}
	delete(j.index, opID)
	j.dropOrder(opID)
	return nil
}

func (j *journal) append(rec journalRecord) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := j.path.Write(append(body, '\n')); err != nil {
		return err
	}
	return j.path.Sync()
}

func (j *journal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var errs []error
	if j.path != nil {
		errs = append(errs, j.path.Close())
		j.path = nil
	}
	if j.lock != nil {
		errs = append(errs, j.lock.release())
		j.lock = nil
	}
	return errors.Join(errs...)
}

// payloadHash binds an op_id to the exact operation and payload it was issued
// for, so a replay carries a verifiable identity instead of a bare counter.
func payloadHash(group, op string, payload json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(ContractVersion))
	h.Write([]byte{0})
	h.Write([]byte(group))
	h.Write([]byte{0})
	h.Write([]byte(op))
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// scopeHash fingerprints the credential that issued an operation. An op_id
// issued under a different credential is rejected rather than replayed.
func scopeHash(token string) string {
	sum := sha256.Sum256([]byte(ContractVersion + "\x00" + token))
	return hex.EncodeToString(sum[:8])
}
