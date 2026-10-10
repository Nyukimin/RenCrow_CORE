package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// OpenOptions selects optional behavior when a Task store is opened for
// writing. The zero value is the behavior this package has always had.
type OpenOptions struct {
	// Index keeps an in-memory ID and summary index of the log. Reads and
	// transactions then touch only the records they name instead of folding the
	// whole history. The log stays the only source of truth: the index is built
	// from it at open and followed after every commit, and a store opened
	// without Index reads the same files.
	Index bool
}

// defaultIndexOption is what NewJSONLStore uses. It is false in every build
// except one made with the taskindexdefault tag, which exists so the existing
// consumer tests can run against the index (see index_default_on.go).
var defaultIndexOption = false

// NewJSONLStoreWithOptions opens the writable Task store at root.
func NewJSONLStoreWithOptions(root string, opts OpenOptions) (*JSONLStore, error) {
	return openJSONLStore(root, false, opts)
}

// IndexStats reports the in-memory index. The second result is false when the
// store was opened without an index.
func (s *JSONLStore) IndexStats() (IndexStats, bool) {
	if s == nil || s.idx == nil {
		return IndexStats{}, false
	}
	return s.idx.stats(), true
}

// writeBatch is the one place a Task store transaction reaches jsonlbatch.Write.
// With an index it first brings the index up to the log (the WAL is settled by
// then, so every byte on disk is committed), dry-runs the payloads the callback
// produced so a record the index would refuse never reaches the log, and records
// a write that ended without a known commit boundary.
func (s *JSONLStore) writeBatch(ctx context.Context, callback func() (map[string][]byte, error)) error {
	if s.idx == nil {
		return s.batch.Write(ctx, callback)
	}
	err := s.batch.Write(ctx, func() (map[string][]byte, error) {
		if err := s.idx.ensureSynced(); err != nil {
			return nil, err
		}
		payloads, err := callback()
		if err != nil {
			return nil, err
		}
		if len(payloads) > 0 {
			if err := s.idx.validateAppend(payloads); err != nil {
				return nil, err
			}
		}
		return payloads, nil
	})
	s.idx.noteWriteResult(err)
	return err
}

// syncIfDirty resynchronizes the index through the OS lock when a write ended
// without a known commit boundary. Reads still do not take the lock in the
// normal case; this keeps them from answering from a stale state afterwards,
// and fails closed with ErrRecoveryRequired exactly where the WAL is unsettled.
func (s *JSONLStore) syncIfDirty(ctx context.Context) error {
	if s.idx == nil || !s.idx.dirty.Load() {
		return nil
	}
	return s.batch.Read(withTxLabel(ctx, "IndexResync", ""), func() error {
		return s.idx.ensureSynced()
	})
}

// beginIndexRead performs the checks every read makes before it touches the
// index and returns the release for the store-level read lock.
func (s *JSONLStore) beginIndexRead(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, errors.New("task read transaction context is nil")
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, os.ErrClosed
	}
	if s.batch == nil {
		s.mu.RUnlock()
		return nil, errors.New("task store batch is unavailable")
	}
	if err := ctx.Err(); err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	if err := s.syncIfDirty(ctx); err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	return s.mu.RUnlock, nil
}

// indexReadTransaction runs fn against a snapshot: the index read lock is held
// for the whole callback, so a commit cannot land between two reads of it.
func (s *JSONLStore) indexReadTransaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.syncIfDirty(ctx); err != nil {
		return err
	}
	s.idx.mu.RLock()
	defer s.idx.mu.RUnlock()
	tx := newTaskTransaction(s, true, "", nil)
	tx.idxLocked = true
	defer tx.end()
	return fn(tx)
}

func (s *JSONLStore) indexGetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	release, err := s.beginIndexRead(ctx)
	if err != nil {
		return domaintask.Task{}, err
	}
	defer release()
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	key, err := parseCanonicalKey(string(taskID), taskKeyPrefix)
	if err != nil {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	s.idx.mu.RLock()
	pos, ok := s.idx.taskTailLocked(key)
	s.idx.mu.RUnlock()
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	return s.idx.readTaskAt(pos, key)
}

func (s *JSONLStore) indexGetRun(ctx context.Context, runID modulecore.RunID) (domaintask.Run, error) {
	release, err := s.beginIndexRead(ctx)
	if err != nil {
		return domaintask.Run{}, err
	}
	defer release()
	if err := runID.Validate(); err != nil {
		return domaintask.Run{}, err
	}
	key, err := parseCanonicalKey(string(runID), runKeyPrefix)
	if err != nil {
		return domaintask.Run{}, domaintask.ErrNotFound
	}
	s.idx.mu.RLock()
	pos, ok := s.idx.runTailLocked(key)
	s.idx.mu.RUnlock()
	if !ok {
		return domaintask.Run{}, domaintask.ErrNotFound
	}
	return s.idx.readRunAt(pos, key)
}

func (s *JSONLStore) indexGetContext(ctx context.Context, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	release, err := s.beginIndexRead(ctx)
	if err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	defer release()
	if err := taskID.Validate(); err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	key, err := parseCanonicalKey(string(taskID), taskKeyPrefix)
	if err != nil {
		return domaintask.SharedRoleContext{}, domaintask.ErrNotFound
	}
	s.idx.mu.RLock()
	pos, ok := s.idx.contextTailLocked(key)
	s.idx.mu.RUnlock()
	if !ok {
		return domaintask.SharedRoleContext{}, domaintask.ErrNotFound
	}
	return s.idx.readContextAt(pos, key)
}

// loadReceipt returns the stored receipt for operationID. With an index it is
// one position lookup and one line read; without, it reads the whole receipt log
// as it always did.
func (s *JSONLStore) loadReceipt(ctx context.Context, operationID string) (TaskOperationReceipt, bool, error) {
	if s.idx != nil {
		s.idx.mu.RLock()
		rec, ok := s.idx.receiptLocked(operationID)
		s.idx.mu.RUnlock()
		if !ok {
			return TaskOperationReceipt{}, false, nil
		}
		receipt, err := s.idx.readReceiptAt(rec.pos, operationID)
		if err != nil {
			return TaskOperationReceipt{}, false, err
		}
		return receipt, true, nil
	}
	receipts, err := readTaskOperationReceipts(ctx, filepath.Join(s.root, taskOperationReceiptFilename))
	if err != nil {
		return TaskOperationReceipt{}, false, err
	}
	receipt, ok := receipts[operationID]
	return receipt, ok, nil
}

func (s *JSONLStore) indexLookupTaskOperation(ctx context.Context, operationID string) (TaskOperationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return TaskOperationReceipt{}, err
	}
	if err := s.syncIfDirty(ctx); err != nil {
		return TaskOperationReceipt{}, err
	}
	receipt, ok, err := s.loadReceipt(ctx, operationID)
	if err != nil {
		return TaskOperationReceipt{}, err
	}
	if !ok {
		return TaskOperationReceipt{}, ErrTaskOperationNotFound
	}
	receipt.Result = append(json.RawMessage(nil), receipt.Result...)
	return receipt, nil
}

// checkIndexGenerations applies the writer-generation fence to what the index
// holds, with the messages the full scans used.
func checkIndexGenerations(ix *taskIndex, generation uint64) error {
	if ix.maxRunGeneration >= generation {
		return fmt.Errorf("writer generation does not advance persisted Run ownership")
	}
	if ix.maxReceiptGeneration >= generation {
		return fmt.Errorf("writer generation does not advance persisted task operation receipt ownership")
	}
	return nil
}
