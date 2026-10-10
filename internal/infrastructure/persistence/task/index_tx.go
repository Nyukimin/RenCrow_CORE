package task

import (
	"bytes"
	"context"
	"fmt"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// txOverlay holds what a write transaction has appended so far, as the values
// the log would hold: each record is decoded again from the bytes about to be
// appended, so the transaction sees the same values after the JSON round trip
// that it will read back once committed. It is only a latest-by-ID view; every
// rule about what may be written is checked where it always was (the Save
// methods) and again, against the index, by the dry run before commit.
type txOverlay struct {
	tasks  map[key16]domaintask.Task
	runs   map[key16]domaintask.Run
	ctxs   map[key16]domaintask.SharedRoleContext
	notifs []overlayNotification
}

type overlayNotification struct {
	key   key16
	value domaintask.Notification
}

func newTxOverlay() *txOverlay {
	return &txOverlay{
		tasks: make(map[key16]domaintask.Task),
		runs:  make(map[key16]domaintask.Run),
		ctxs:  make(map[key16]domaintask.SharedRoleContext),
	}
}

// record decodes the encoded record about to be appended to filename. A record
// that cannot be keyed canonically is refused before it is appended.
func (o *txOverlay) record(filename string, encoded []byte) error {
	line := bytes.TrimSpace(encoded)
	switch filename {
	case stateFilename:
		var value domaintask.Task
		if err := decodeStrictLine(line, &value); err != nil {
			return err
		}
		key, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
		if err != nil {
			return fmt.Errorf("task_id: %w", err)
		}
		o.tasks[key] = cloneTransactionTask(value)
	case runFilename:
		var value domaintask.Run
		if err := decodeStrictLine(line, &value); err != nil {
			return err
		}
		runKey, err := parseCanonicalKey(string(value.RunID), runKeyPrefix)
		if err != nil {
			return fmt.Errorf("run_id: %w", err)
		}
		if _, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix); err != nil {
			return fmt.Errorf("task_id: %w", err)
		}
		o.runs[runKey] = cloneTransactionRun(value)
	case contextFilename:
		var value domaintask.SharedRoleContext
		if err := decodeStrictLine(line, &value); err != nil {
			return err
		}
		key, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
		if err != nil {
			return fmt.Errorf("task_id: %w", err)
		}
		o.ctxs[key] = value
	case notificationsFilename:
		var value domaintask.Notification
		if err := decodeStrictLine(line, &value); err != nil {
			return err
		}
		key, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
		if err != nil {
			return fmt.Errorf("task_id: %w", err)
		}
		o.notifs = append(o.notifs, overlayNotification{key: key, value: value})
	}
	return nil
}

func noopUnlock() {}

// indexLock takes the index read lock unless the enclosing read transaction
// already holds it for its whole callback (re-acquiring a read lock could
// deadlock against a waiting commit). Write transactions hold the OS lock, so
// the index cannot change under them; they still take the read lock around each
// access so Go's memory model orders them after the previous commit's merge.
func (s *transactionStore) indexLock() func() {
	if s.idxLocked {
		return noopUnlock
	}
	s.parent.idx.mu.RLock()
	return s.parent.idx.mu.RUnlock
}

func (s *transactionStore) overlaySnapshot() overlaySnapshot {
	snapshot := overlaySnapshot{}
	if s.state == nil {
		return snapshot
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	ov := s.state.ov
	if ov == nil {
		return snapshot
	}
	snapshot.tasks = make(map[key16]domaintask.Task, len(ov.tasks))
	for key, value := range ov.tasks {
		snapshot.tasks[key] = value
	}
	snapshot.runs = make(map[key16]domaintask.Run, len(ov.runs))
	for key, value := range ov.runs {
		snapshot.runs[key] = value
	}
	snapshot.notifs = append([]overlayNotification(nil), ov.notifs...)
	return snapshot
}

func (s *transactionStore) overlayTask(key key16) (domaintask.Task, bool) {
	if s.state == nil {
		return domaintask.Task{}, false
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.ov == nil {
		return domaintask.Task{}, false
	}
	value, ok := s.state.ov.tasks[key]
	return value, ok
}

func (s *transactionStore) overlayRun(key key16) (domaintask.Run, bool) {
	if s.state == nil {
		return domaintask.Run{}, false
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.ov == nil {
		return domaintask.Run{}, false
	}
	value, ok := s.state.ov.runs[key]
	return value, ok
}

func (s *transactionStore) overlayContext(key key16) (domaintask.SharedRoleContext, bool) {
	if s.state == nil {
		return domaintask.SharedRoleContext{}, false
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.ov == nil {
		return domaintask.SharedRoleContext{}, false
	}
	value, ok := s.state.ov.ctxs[key]
	return value, ok
}

// idxCheckSaveTask is the check appendTask makes for an indexed store, answered
// from the pending records and the index instead of a fold of the whole log:
// the latest saved version of the Task fixes its criteria revision and its
// Resume claims, and a Task cannot be first saved with claims. The caller holds
// the transaction state lock.
func (s *transactionStore) idxCheckSaveTask(value domaintask.Task) error {
	key, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
	if err != nil {
		return fmt.Errorf("task_id: %w", err)
	}
	var previous taskPin
	found := false
	if pending, ok := s.state.ov.tasks[key]; ok {
		previous, found = pinOfTask(pending), true
	} else {
		ix := s.parent.idx
		unlock := s.indexLock()
		flags, pos, committed := ix.committedPinLocator(key)
		unlock()
		if committed {
			if previous, err = ix.readTaskPin(flags, pos, key); err != nil {
				return err
			}
			found = true
		}
	}
	if found {
		return checkTaskPinTransition(previous, value)
	}
	if len(value.NativeResumeClaims) != 0 {
		return ErrNativeOPSResumeClaimImmutable
	}
	return nil
}

func (s *transactionStore) idxGetTask(taskID modulecore.TaskID) (domaintask.Task, error) {
	key, err := parseCanonicalKey(string(taskID), taskKeyPrefix)
	if err != nil {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	if value, ok := s.overlayTask(key); ok {
		return cloneTransactionTask(value), nil
	}
	ix := s.parent.idx
	unlock := s.indexLock()
	pos, ok := ix.taskTailLocked(key)
	unlock()
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	return ix.readTaskAt(pos, key)
}

func (s *transactionStore) idxTaskKnown(key key16) bool {
	if _, ok := s.overlayTask(key); ok {
		return true
	}
	unlock := s.indexLock()
	defer unlock()
	_, ok := s.parent.idx.taskMap[key]
	return ok
}

func (s *transactionStore) idxGetRun(runID modulecore.RunID) (domaintask.Run, error) {
	key, err := parseCanonicalKey(string(runID), runKeyPrefix)
	if err != nil {
		return domaintask.Run{}, domaintask.ErrNotFound
	}
	run, found, err := s.idxLatestRun(key)
	if err != nil {
		return domaintask.Run{}, err
	}
	if !found {
		return domaintask.Run{}, domaintask.ErrNotFound
	}
	return run, nil
}

// idxLatestRun returns the newest version of a Run: pending, else committed.
func (s *transactionStore) idxLatestRun(key key16) (domaintask.Run, bool, error) {
	if value, ok := s.overlayRun(key); ok {
		return cloneTransactionRun(value), true, nil
	}
	ix := s.parent.idx
	unlock := s.indexLock()
	pos, ok := ix.runTailLocked(key)
	unlock()
	if !ok {
		return domaintask.Run{}, false, nil
	}
	run, err := ix.readRunAt(pos, key)
	return run, err == nil, err
}

// idxOtherActiveRun reports a running Run of the Task other than runKey,
// looking at pending Runs first and committed ones they do not override.
func (s *transactionStore) idxOtherActiveRun(taskID modulecore.TaskID, taskKey, runKey key16) (modulecore.RunID, bool) {
	pending := s.overlaySnapshot().runs
	for key, run := range pending {
		if key != runKey && run.TaskID == taskID && run.Status == domaintask.RunStatusRunning {
			return run.RunID, true
		}
	}
	ix := s.parent.idx
	unlock := s.indexLock()
	defer unlock()
	slot, ok := ix.taskMap[taskKey]
	if !ok {
		return "", false
	}
	running, found := ix.enums.lookup(string(domaintask.RunStatusRunning))
	if !found {
		return "", false
	}
	for run := ix.tasks[slot].runHead; run != 0; run = ix.runs[run-1].nextRun {
		rec := &ix.runs[run-1]
		if rec.key == runKey {
			continue
		}
		if _, overridden := pending[rec.key]; overridden {
			continue
		}
		if uint32(rec.status) == running {
			return modulecore.RunID(rec.key.id(runKeyPrefix)), true
		}
	}
	return "", false
}

// idxSaveRunChecks are the checks SaveRun has always made, answered from the
// pending records and the index instead of a fold of the whole log.
func (s *transactionStore) idxSaveRunChecks(value domaintask.Run) error {
	taskKey, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
	if err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	runKey, err := parseCanonicalKey(string(value.RunID), runKeyPrefix)
	if err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if !s.idxTaskKnown(taskKey) {
		return fmt.Errorf("run task is unavailable: %w", domaintask.ErrNotFound)
	}
	existing, existingRun, err := s.idxLatestRun(runKey)
	if err != nil {
		return err
	}
	if existingRun {
		if err := validateRunUpdate(existing, value); err != nil {
			return err
		}
	}
	generation, err := s.WriterGeneration()
	if err != nil {
		return err
	}
	if !existingRun && (value.WriterGeneration == 0 || value.WriterGeneration != generation) {
		return fmt.Errorf("new run requires current writer generation")
	}
	if value.Status == domaintask.RunStatusRunning {
		if other, found := s.idxOtherActiveRun(value.TaskID, taskKey, runKey); found {
			return fmt.Errorf("task already has active run %s", other)
		}
	}
	return nil
}

func (s *transactionStore) idxListTasks(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	ix := s.parent.idx
	pending := s.overlaySnapshot().tasks
	unlock := s.indexLock()
	best := newBestN(filter.Limit, taskCandBefore)
	ix.collectTasksLocked(filter, pending, best.add)
	cands := best.result()
	unlock()
	result := make([]domaintask.Task, 0, len(cands))
	for _, cand := range cands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cand.value != nil {
			result = append(result, cloneTransactionTask(*cand.value))
			continue
		}
		value, err := ix.readTaskAt(cand.pos, cand.key)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *transactionStore) idxListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	ix := s.parent.idx
	var taskKey key16
	taskKeyValid := false
	if filter.TaskID != "" {
		if key, err := parseCanonicalKey(string(filter.TaskID), taskKeyPrefix); err == nil {
			taskKey, taskKeyValid = key, true
		}
	}
	pending := s.overlaySnapshot().runs
	unlock := s.indexLock()
	best := newBestN(filter.Limit, runCandBefore)
	ix.collectRunsLocked(filter, taskKey, taskKeyValid, pending, best.add)
	cands := best.result()
	unlock()
	result := make([]domaintask.Run, 0, len(cands))
	for _, cand := range cands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cand.value != nil {
			result = append(result, cloneTransactionRun(*cand.value))
			continue
		}
		value, err := ix.readRunAt(cand.pos, cand.key)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *transactionStore) idxGetContext(taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	key, err := parseCanonicalKey(string(taskID), taskKeyPrefix)
	if err != nil {
		return domaintask.SharedRoleContext{}, domaintask.ErrNotFound
	}
	if value, ok := s.overlayContext(key); ok {
		return value, nil
	}
	ix := s.parent.idx
	unlock := s.indexLock()
	pos, ok := ix.contextTailLocked(key)
	unlock()
	if !ok {
		return domaintask.SharedRoleContext{}, domaintask.ErrNotFound
	}
	return ix.readContextAt(pos, key)
}

func (s *transactionStore) idxListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domaintask.Notification, error) {
	ix := s.parent.idx
	pending := s.overlaySnapshot().notifs
	unlock := s.indexLock()
	best := newBestN(limit, notifCandBefore)
	ix.collectNotificationsLocked(interruptOnly, pending, best.add)
	cands := best.result()
	unlock()
	result := make([]domaintask.Notification, 0, len(cands))
	for _, cand := range cands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cand.value != nil {
			result = append(result, *cand.value)
			continue
		}
		value, err := ix.readNotificationAt(cand.pos, cand.key)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}
