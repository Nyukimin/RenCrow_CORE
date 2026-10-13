package action

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// IndexStats reports the in-memory snapshot.
type IndexStats struct {
	Actions       int
	Attempts      int
	ActionBytes   int64
	AttemptBytes  int64
	BuildDuration time.Duration
	// Rebuilds counts the times the snapshot was rebuilt from the logs after it
	// was opened. A store that is the only writer of its logs never rebuilds.
	Rebuilds int64
	// Dirty means a write ended without a known commit boundary; Broken means a
	// change to the snapshot may have been left half done. Either way the next
	// operation settles the question against the logs before it answers.
	Dirty  bool
	Broken bool
}

// snapshot is the holder of the content: the lock that readers and the one
// applier share, and the marks that say when the content cannot be trusted.
//
// Lock order is Manager.mu, then JSONLStore.mu, then the OS lock of jsonlbatch,
// then snapshot.mu; nothing is acquired while snapshot.mu is held, and a reader
// never goes from RLock to Lock (a rebuild starts with no snapshot lock held).
//
// The content is changed only with the OS lock held (by the hook that follows a
// commit, and by the rebuild), so a writer's callback, which also holds it, can
// read the content under RLock while nothing can change it.
type snapshot struct {
	mu    sync.RWMutex
	st    *snapState
	paths [fileCount]string

	dirty    atomic.Bool
	broken   atomic.Bool
	rebuilds atomic.Int64
	buildNs  atomic.Int64

	// hookFault is a seam for tests: called at the named points of the hook, with
	// the marks and the lock in the state a real stop would leave them in.
	hookFault func(stage string)
}

func newSnapshot(actionPath, attemptPath string) *snapshot {
	return &snapshot{paths: [fileCount]string{fileAction: actionPath, fileAttempt: attemptPath}}
}

func (ix *snapshot) fault(stage string) {
	if ix.hookFault != nil {
		ix.hookFault(stage)
	}
}

func (ix *snapshot) stats() IndexStats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	stats := IndexStats{
		BuildDuration: time.Duration(ix.buildNs.Load()),
		Rebuilds:      ix.rebuilds.Load(),
		Dirty:         ix.dirty.Load(),
		Broken:        ix.broken.Load(),
	}
	if ix.st != nil {
		stats.Actions = len(ix.st.actions.latest)
		stats.Attempts = len(ix.st.attempts.latest)
		stats.ActionBytes = ix.st.applied[fileAction]
		stats.AttemptBytes = ix.st.applied[fileAttempt]
	}
	return stats
}

// IndexStats reports the snapshot; the second result is false when the store was
// opened without one.
func (s *JSONLStore) IndexStats() (IndexStats, bool) {
	if s == nil || s.idx == nil {
		return IndexStats{}, false
	}
	return s.idx.stats(), true
}

// buildSnapState reads both logs in full and folds them into a new content,
// then holds the whole of it to the whole-store rule. This is the work the full
// read does for every transaction; here it is done to open and to rebuild.
func buildSnapState(ctx context.Context, actionPath, attemptPath string) (*snapState, error) {
	st := newSnapState(1<<10, 1<<10)
	var err error
	if st.applied[fileAction], err = foldLog(ctx, actionPath, func(line []byte) error {
		var item domainaction.Action
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		return st.applyAction(item)
	}); err != nil {
		return nil, fmt.Errorf("validate action state: %w", err)
	}
	if st.applied[fileAttempt], err = foldLog(ctx, attemptPath, func(line []byte) error {
		var item domainaction.Attempt
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		return st.applyAttempt(item)
	}); err != nil {
		return nil, fmt.Errorf("validate attempt state: %w", err)
	}
	actions, attempts := st.slices()
	if err := validateSnapshot(actions, attempts); err != nil {
		return nil, fmt.Errorf("validate action snapshot: %w", err)
	}
	return st, nil
}

// foldLog streams one log through fn and returns the number of bytes it covers.
// A log that does not end with a newline was not written by the append path, and
// appending to it would join two records, so it is refused.
func foldLog(ctx context.Context, path string, fn func(line []byte) error) (int64, error) {
	read, terminated, err := scanJSONLFile(ctx, path, fn)
	if err != nil {
		return 0, err
	}
	if !terminated {
		return 0, fmt.Errorf("%s ends with an unterminated line", logBase(path))
	}
	return read, nil
}

func logBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

// build makes the first content. The OS lock must be held (open does it inside a
// batch Read).
func (ix *snapshot) build(ctx context.Context) error {
	started := time.Now()
	st, err := buildSnapState(ctx, ix.paths[fileAction], ix.paths[fileAttempt])
	if err != nil {
		return err
	}
	ix.mu.Lock()
	ix.st = st
	ix.mu.Unlock()
	elapsed := time.Since(started)
	ix.buildNs.Store(int64(elapsed))
	log.Printf("[ActionStore] index loaded actions=%d attempts=%d action_bytes=%d attempt_bytes=%d dur=%s",
		len(st.actions.latest), len(st.attempts.latest), st.applied[fileAction], st.applied[fileAttempt], elapsed.Round(time.Millisecond))
	return nil
}

// sizes reads the length of both logs.
func (ix *snapshot) sizes() ([fileCount]int64, error) {
	var sizes [fileCount]int64
	for kind, path := range ix.paths {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return sizes, fmt.Errorf("stat %s for the action snapshot: %w", logBase(path), err)
		}
		sizes[kind] = info.Size()
	}
	return sizes, nil
}

// stale reports whether the content must be settled against the logs before it
// is used: a mark is set, or a log is not exactly as long as the content covers.
// It takes no OS lock, so what it reports is the state at the instant of the
// stat calls; a commit that lands later is seen by the next operation.
func (ix *snapshot) stale() (bool, error) {
	if ix.dirty.Load() || ix.broken.Load() {
		return true, nil
	}
	sizes, err := ix.sizes()
	if err != nil {
		return false, err
	}
	ix.mu.RLock()
	covered := ix.st.applied
	ix.mu.RUnlock()
	return sizes != covered, nil
}

// ensureSynced makes the content equal to the logs. The OS lock must be held
// after the WAL was settled (the start of a Write callback, or inside a Read),
// because only then is every byte on disk committed. When the content already
// covers exactly the bytes there are, nothing changes; otherwise the logs are read
// in full again. A log that holds anything the folds refuse fails closed, and the
// content stays marked so that the next operation looks again.
func (ix *snapshot) ensureSynced(ctx context.Context) error {
	sizes, err := ix.sizes()
	if err != nil {
		return err
	}
	ix.mu.RLock()
	covered := ix.st.applied
	ix.mu.RUnlock()
	if !ix.broken.Load() && sizes == covered {
		ix.dirty.Store(false)
		return nil
	}
	reason := "bytes the snapshot was not told about"
	switch {
	case ix.broken.Load():
		reason = "a change to the snapshot was not completed"
	case sizes[fileAction] < covered[fileAction] || sizes[fileAttempt] < covered[fileAttempt]:
		reason = "a log is shorter than the snapshot covers"
	case ix.dirty.Load():
		reason = "a write ended without a known commit boundary"
	}
	return ix.rebuild(ctx, reason)
}

// rebuild replaces the content with a full read of the logs. It builds beside the
// current content and swaps it in, so a log that fails the folds leaves the
// previous content in place, marked broken.
func (ix *snapshot) rebuild(ctx context.Context, reason string) error {
	started := time.Now()
	st, err := buildSnapState(ctx, ix.paths[fileAction], ix.paths[fileAttempt])
	if err != nil {
		ix.broken.Store(true)
		log.Printf("[ActionStore] WARN index could not be rebuilt from the log reason=%q: %v", reason, err)
		return err
	}
	ix.mu.Lock()
	ix.st = st
	ix.mu.Unlock()
	elapsed := time.Since(started)
	ix.buildNs.Store(int64(elapsed))
	ix.rebuilds.Add(1)
	ix.broken.Store(false)
	ix.dirty.Store(false)
	log.Printf("[ActionStore] WARN index rebuilt from the log reason=%q actions=%d attempts=%d dur=%s",
		reason, len(st.actions.latest), len(st.attempts.latest), elapsed.Round(time.Millisecond))
	return nil
}

// noteWriteResult records that a write ended without a known commit boundary.
func (ix *snapshot) noteWriteResult(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, jsonlbatch.ErrCommitUncertain) || errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
		ix.dirty.Store(true)
	}
}

// onCommit is the jsonlbatch post-commit hook. The commit is durable and the OS
// lock is held, so the hook cannot fail the transaction: if it cannot absorb the
// bytes it leaves the content marked broken, and the next operation rebuilds. The
// mark is set first and cleared last, so a panic inside leaves it set.
func (ix *snapshot) onCommit(_ context.Context, appends []jsonlbatch.CommittedAppend) {
	ix.broken.Store(true)
	ix.fault("hook-start")
	if err := ix.absorb(appends); err != nil {
		log.Printf("[ActionStore] WARN index could not absorb a committed transaction; it will be rebuilt from the log: %v", err)
		return
	}
	ix.broken.Store(false)
	ix.fault("hook-done")
}

// absorb applies the bytes a commit appended: Actions first, then Attempts,
// whatever order the commit listed its files in. The bytes are decoded outside
// the lock; the lock covers the folds and the check of the groups they touched.
func (ix *snapshot) absorb(appends []jsonlbatch.CommittedAppend) error {
	var chunks [fileCount]*jsonlbatch.CommittedAppend
	for i := range appends {
		for kind, name := range logFilenames {
			if appends[i].Name == name {
				chunks[kind] = &appends[i]
			}
		}
	}
	ix.mu.RLock()
	covered := ix.st.applied
	ix.mu.RUnlock()

	var actions []domainaction.Action
	var attempts []domainaction.Attempt
	for kind, chunk := range chunks {
		if chunk == nil {
			continue
		}
		if chunk.Offset != covered[kind] {
			return fmt.Errorf("%s was appended to at %d but the snapshot covers %d bytes", logFilenames[kind], chunk.Offset, covered[kind])
		}
		if len(chunk.Payload) == 0 || chunk.Payload[len(chunk.Payload)-1] != '\n' {
			return fmt.Errorf("an append to %s does not end with a newline", logFilenames[kind])
		}
		for _, line := range bytes.Split(chunk.Payload, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var err error
			if kind == fileAction {
				var item domainaction.Action
				if err = json.Unmarshal(line, &item); err == nil {
					actions = append(actions, item)
				}
			} else {
				var item domainaction.Attempt
				if err = json.Unmarshal(line, &item); err == nil {
					attempts = append(attempts, item)
				}
			}
			if err != nil {
				return fmt.Errorf("decode an append to %s: %w", logFilenames[kind], err)
			}
		}
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	st := ix.st
	touched := make(map[modulecore.ActionID]struct{}, len(actions)+len(attempts))
	for _, item := range actions {
		if err := st.applyAction(item); err != nil {
			return err
		}
		touched[item.ActionID] = struct{}{}
	}
	ix.fault("hook-after-actions")
	for _, item := range attempts {
		if err := st.applyAttempt(item); err != nil {
			return err
		}
		touched[item.ActionID] = struct{}{}
	}
	for kind, chunk := range chunks {
		if chunk != nil {
			st.applied[kind] += int64(len(chunk.Payload))
		}
	}
	// The transaction was held to the group rule before it was committed; the
	// groups it touched are checked again on what was decoded from the bytes, so a
	// difference between the two never survives in the content.
	for id := range touched {
		action, ok := st.action(id)
		if !ok {
			return fmt.Errorf("an Attempt names an Action the snapshot does not hold")
		}
		if err := validateActionGroup(action, st.attemptsOfAction(id), st.attempt); err != nil {
			return err
		}
	}
	return nil
}

// checkAgainst compares the content with a full read of the logs.
func (ix *snapshot) checkAgainst(actions []domainaction.Action, attempts []domainaction.Attempt) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.st.check(actions, attempts)
}
