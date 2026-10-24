package action

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrActionAdmissionUnavailable   = errors.New("Action RAM admission is unavailable")
	ErrActionAdmissionBusy          = errors.New("Action RAM admission is blocked by an active synchronous writer")
	ErrActionAdmissionQueueMismatch = errors.New("Action RAM admission uses a different persistence queue")
	ErrActionAdmissionIndexStale    = errors.New("Action RAM admission requires a current indexed snapshot")
	errActionQueueAlreadyConfigured = errors.New("Action persistence queue is already configured differently")
)

type pendingActionValue struct {
	value      domainaction.Action
	generation uint64
}

type pendingAttemptValue struct {
	value      domainaction.Attempt
	generation uint64
}

// SetPersistenceQueue connects the Action owner to the process's shared RAM
// FIFO. It does not change the legacy synchronous API; callers opt into
// admission by carrying a matching persistencequeue.Admission in context.
func (s *JSONLStore) SetPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || queue == nil {
		return ErrActionAdmissionUnavailable
	}
	if !s.admissionMu.TryLock() {
		return ErrActionAdmissionBusy
	}
	defer s.admissionMu.Unlock()
	if s.writeQueue != nil && s.writeQueue != queue {
		return errActionQueueAlreadyConfigured
	}
	s.writeQueue = queue
	if s.pendingActions == nil {
		s.pendingActions = make(map[modulecore.ActionID]pendingActionValue)
	}
	if s.pendingAttempts == nil {
		s.pendingAttempts = make(map[modulecore.AttemptID]pendingAttemptValue)
	}
	return nil
}

// withAdmissionFence preserves the synchronous behavior of non-admitted calls.
// It waits for this Action store's last accepted write without holding the
// admission mutex, then locks admission against a newer request before running.
func (s *JSONLStore) withAdmissionFence(ctx context.Context, run func() error) error {
	if run == nil {
		return errors.New("Action store operation is nil")
	}
	for {
		s.admissionMu.Lock()
		queue := s.writeQueue
		ticket := s.admissionTicket
		failure := s.admissionErr
		s.admissionMu.Unlock()
		if failure != nil {
			return fmt.Errorf("Action persistence queue failed: %w", failure)
		}
		if queue != nil {
			if queueFailure := queue.Status().Failure; queueFailure != nil {
				return fmt.Errorf("shared persistence queue failed: %w", queueFailure)
			}
		}
		if queue == nil || ticket == 0 {
			s.admissionMu.Lock()
			if s.writeQueue == queue && s.admissionTicket == ticket && s.admissionErr == nil {
				return s.runWithAdmissionLock(run)
			}
			s.admissionMu.Unlock()
			continue
		}
		if err := queue.Wait(ctx, uint64(ticket)); err != nil {
			return fmt.Errorf("wait for accepted Action persistence: %w", err)
		}
		s.admissionMu.Lock()
		if s.admissionErr != nil {
			failure := s.admissionErr
			s.admissionMu.Unlock()
			return fmt.Errorf("Action persistence queue failed: %w", failure)
		}
		if s.writeQueue != queue || s.admissionTicket != ticket {
			s.admissionMu.Unlock()
			continue
		}
		return s.runWithAdmissionLock(run)
	}
}

// runWithAdmissionLock releases the synchronous fence on both normal return
// and panic. Snapshot hooks can panic after the JSONL commit has been written;
// leaving admissionMu locked would make the recovery read deadlock permanently.
func (s *JSONLStore) runWithAdmissionLock(run func() error) error {
	defer s.admissionMu.Unlock()
	return run()
}

func (s *JSONLStore) admittedTransaction(ctx context.Context, op string, callback func(domainaction.Store) error, admission *persistencequeue.Admission) error {
	if admission == nil {
		return ErrActionAdmissionUnavailable
	}
	if !s.admissionMu.TryLock() {
		return ErrActionAdmissionBusy
	}
	defer s.admissionMu.Unlock()
	if s.writeQueue == nil {
		return ErrActionAdmissionUnavailable
	}
	if !admission.MatchesQueue(s.writeQueue) {
		return ErrActionAdmissionQueueMismatch
	}
	if s.idx == nil {
		return ErrActionAdmissionUnavailable
	}
	if s.admissionErr != nil {
		return fmt.Errorf("Action persistence queue failed: %w", s.admissionErr)
	}
	if queueFailure := s.writeQueue.Status().Failure; queueFailure != nil {
		return fmt.Errorf("shared persistence queue failed: %w", queueFailure)
	}
	unlockIndex, err := s.tryLockCurrentAdmissionIndex()
	if err != nil {
		return err
	}
	defer unlockIndex()
	tx := newSnapshotTransaction(s.idx.st, false)
	tx.overlayActions = actionOverlayValues(s.pendingActions)
	tx.overlayAttempts = attemptOverlayValues(s.pendingAttempts)
	defer tx.invalidate()
	if err := callback(tx); err != nil {
		return err
	}
	payload, err := tx.finalize()
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}

	var sizeBytes int64
	for _, data := range payload {
		sizeBytes += int64(len(data))
	}
	if sizeBytes < 1 {
		return ErrActionAdmissionUnavailable
	}
	nextGeneration := s.admissionGen
	newActions := clonePendingActions(s.pendingActions)
	newAttempts := clonePendingAttempts(s.pendingAttempts)
	actionGenerations := make(map[modulecore.ActionID]uint64, len(tx.actions))
	attemptGenerations := make(map[modulecore.AttemptID]uint64, len(tx.attempts))
	for id, value := range tx.actions {
		if nextGeneration == math.MaxUint64 {
			return ErrActionAdmissionUnavailable
		}
		nextGeneration++
		newActions[id] = pendingActionValue{value: cloneAction(value), generation: nextGeneration}
		actionGenerations[id] = nextGeneration
	}
	for id, value := range tx.attempts {
		if nextGeneration == math.MaxUint64 {
			return ErrActionAdmissionUnavailable
		}
		nextGeneration++
		newAttempts[id] = pendingAttemptValue{value: cloneAttempt(value), generation: nextGeneration}
		attemptGenerations[id] = nextGeneration
	}
	immutablePayload := clonePayload(payload)
	command := persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerAction,
		Kind:      "action." + op,
		SizeBytes: sizeBytes,
		Apply: func(workerContext context.Context) error {
			return s.applyAdmittedPayload(workerContext, immutablePayload, actionGenerations, attemptGenerations)
		},
	}
	ticket, err := admission.Enqueue(command)
	if err != nil {
		return fmt.Errorf("enqueue Action transaction: %w", err)
	}
	s.pendingActions = newActions
	s.pendingAttempts = newAttempts
	s.admissionGen = nextGeneration
	s.admissionTicket = ticket
	return nil
}

func (s *JSONLStore) admittedReadTransaction(ctx context.Context, callback func(domainaction.Store) error, admission *persistencequeue.Admission) error {
	if admission == nil {
		return ErrActionAdmissionUnavailable
	}
	if !s.admissionMu.TryLock() {
		return ErrActionAdmissionBusy
	}
	defer s.admissionMu.Unlock()
	if s.writeQueue == nil {
		return ErrActionAdmissionUnavailable
	}
	if !admission.MatchesQueue(s.writeQueue) {
		return ErrActionAdmissionQueueMismatch
	}
	if s.idx == nil {
		return ErrActionAdmissionUnavailable
	}
	if s.admissionErr != nil {
		return fmt.Errorf("Action persistence queue failed: %w", s.admissionErr)
	}
	if queueFailure := s.writeQueue.Status().Failure; queueFailure != nil {
		return fmt.Errorf("shared persistence queue failed: %w", queueFailure)
	}
	unlockIndex, err := s.tryLockCurrentAdmissionIndex()
	if err != nil {
		return err
	}
	defer unlockIndex()
	tx := newSnapshotTransaction(s.idx.st, true)
	tx.overlayActions = actionOverlayValues(s.pendingActions)
	tx.overlayAttempts = attemptOverlayValues(s.pendingAttempts)
	defer tx.invalidate()
	return callback(tx)
}

func (s *JSONLStore) tryLockCurrentAdmissionIndex() (func(), error) {
	if s.idx == nil || !s.idx.mu.TryRLock() {
		return nil, ErrActionAdmissionBusy
	}
	unlock := s.idx.mu.RUnlock
	if s.idx.dirty.Load() || s.idx.broken.Load() {
		unlock()
		return nil, ErrActionAdmissionIndexStale
	}
	paths := s.idx.segs[len(s.idx.segs)-1].paths()
	covered := s.idx.st.applied
	got, err := statSizes(paths)
	if err != nil {
		unlock()
		return nil, fmt.Errorf("check Action index before RAM admission: %w", err)
	}
	if got != covered {
		unlock()
		return nil, ErrActionAdmissionIndexStale
	}
	return unlock, nil
}

func (s *JSONLStore) applyAdmittedPayload(ctx context.Context, payload map[string][]byte, actionGenerations map[modulecore.ActionID]uint64, attemptGenerations map[modulecore.AttemptID]uint64) error {
	// Do not hold admissionMu during filesystem work. New requests can build on
	// the committed index plus pending overlay while this FIFO item waits for
	// the OS writer lock.
	s.mu.Lock()
	var writeErr error
	if s.idx == nil {
		writeErr = ErrActionAdmissionUnavailable
	} else if writeErr = s.rotateIfDue(ctx); writeErr == nil {
		writeErr = s.batch.Write(withTxLabel(ctx, txOpTransaction), func() (map[string][]byte, error) {
			return clonePayload(payload), nil
		})
		s.idx.noteWriteResult(writeErr)
	}
	s.mu.Unlock()
	if writeErr != nil {
		s.admissionMu.Lock()
		if s.admissionErr == nil {
			s.admissionErr = writeErr
		}
		poison := s.admissionErr
		s.admissionMu.Unlock()
		return poison
	}

	// Generations prevent this completion from removing a newer overlay value
	// admitted for the same Action or Attempt during the filesystem write.
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	for id, generation := range actionGenerations {
		if current, ok := s.pendingActions[id]; ok && current.generation == generation {
			delete(s.pendingActions, id)
		}
	}
	for id, generation := range attemptGenerations {
		if current, ok := s.pendingAttempts[id]; ok && current.generation == generation {
			delete(s.pendingAttempts, id)
		}
	}
	return nil
}

func actionOverlayValues(values map[modulecore.ActionID]pendingActionValue) map[modulecore.ActionID]domainaction.Action {
	if len(values) == 0 {
		return nil
	}
	result := make(map[modulecore.ActionID]domainaction.Action, len(values))
	for id, item := range values {
		result[id] = cloneAction(item.value)
	}
	return result
}

func attemptOverlayValues(values map[modulecore.AttemptID]pendingAttemptValue) map[modulecore.AttemptID]domainaction.Attempt {
	if len(values) == 0 {
		return nil
	}
	result := make(map[modulecore.AttemptID]domainaction.Attempt, len(values))
	for id, item := range values {
		result[id] = cloneAttempt(item.value)
	}
	return result
}

func clonePendingActions(values map[modulecore.ActionID]pendingActionValue) map[modulecore.ActionID]pendingActionValue {
	result := make(map[modulecore.ActionID]pendingActionValue, len(values)+1)
	for id, item := range values {
		item.value = cloneAction(item.value)
		result[id] = item
	}
	return result
}

func clonePendingAttempts(values map[modulecore.AttemptID]pendingAttemptValue) map[modulecore.AttemptID]pendingAttemptValue {
	result := make(map[modulecore.AttemptID]pendingAttemptValue, len(values)+1)
	for id, item := range values {
		item.value = cloneAttempt(item.value)
		result[id] = item
	}
	return result
}

func clonePayload(payload map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(payload))
	for name, data := range payload {
		result[name] = append([]byte(nil), data...)
	}
	return result
}
