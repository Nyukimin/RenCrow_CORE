package persona

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

var errPersonaStoreClosing = errors.New("persona store is closing")

var (
	ErrPersonaAdmissionUnavailable          = errors.New("Persona RAM admission is unavailable")
	ErrPersonaAdmissionBusy                 = errors.New("Persona RAM admission is blocked by a synchronous store operation")
	ErrPersonaAdmissionQueueMismatch        = errors.New("Persona RAM admission uses a different persistence queue")
	ErrPersonaAdmissionUnsupportedOperation = errors.New("Persona operation requires synchronous result and cannot run with RAM admission")
	ErrPersonaAdmissionSnapshotUnavailable  = errors.New("Persona admission read snapshot is unavailable or incomplete")
)

const personaAdmissionListSnapshotLimit = 50

type personaAdmissionWrite struct {
	admission *persistencequeue.Admission
	ticket    persistencequeue.Ticket
	table     string
	key       string
	payload   string
}

// personaAdmissionState holds only volatile writes accepted for an explicit
// request admission. The configured Persona store remains the durable source
// of truth; snapshots are bounded read projections warmed before admission.
type personaAdmissionState struct {
	mu               sync.Mutex
	changed          chan struct{}
	closing          bool
	synchronousWrite bool
	admittedWriters  int
	failure          error
	queue            *persistencequeue.Queue[persistencequeue.PersistenceCommand]
	pending          map[*persistencequeue.Admission][]*personaAdmissionWrite
	snapshots        map[string][]personaAdmissionWrite
	snapshotLimits   map[string]int
	snapshotComplete map[string]bool
}

func newPersonaAdmissionState() *personaAdmissionState {
	return &personaAdmissionState{
		changed:          make(chan struct{}),
		pending:          make(map[*persistencequeue.Admission][]*personaAdmissionWrite),
		snapshots:        make(map[string][]personaAdmissionWrite),
		snapshotLimits:   make(map[string]int),
		snapshotComplete: make(map[string]bool),
	}
}

func (s *personaAdmissionState) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *personaAdmissionState) setQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || queue == nil {
		return ErrPersonaAdmissionUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queue != nil && s.queue != queue {
		return ErrPersonaAdmissionQueueMismatch
	}
	if s.closing {
		return errPersonaStoreClosing
	}
	if failure := queue.Status().Failure; failure != nil {
		return fmt.Errorf("shared persistence queue failed: %w", failure)
	}
	s.queue = queue
	return nil
}

func (s *personaAdmissionState) validateQueueBinding(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || queue == nil {
		return ErrPersonaAdmissionUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errPersonaStoreClosing
	}
	if s.queue != nil && s.queue != queue {
		return ErrPersonaAdmissionQueueMismatch
	}
	return nil
}

func (s *personaAdmissionState) matchesQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool {
	if s == nil || queue == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queue != nil && s.queue == queue
}

func (s *personaAdmissionState) validateAdmission(admission *persistencequeue.Admission) error {
	if s == nil || admission == nil {
		return ErrPersonaAdmissionUnavailable
	}
	s.mu.Lock()
	queue := s.queue
	closing := s.closing
	ownerFailure := s.failure
	s.mu.Unlock()
	if closing {
		return errPersonaStoreClosing
	}
	if queue == nil {
		return ErrPersonaAdmissionUnavailable
	}
	if !admission.MatchesQueue(queue) {
		return ErrPersonaAdmissionQueueMismatch
	}
	if ownerFailure != nil {
		return fmt.Errorf("Persona persistence queue failed: %w", errors.Join(persistencequeue.ErrFailed, ownerFailure))
	}
	if failure := queue.Status().Failure; failure != nil {
		return fmt.Errorf("shared persistence queue failed: %w", failure)
	}
	return nil
}

func (s *personaAdmissionState) beginDirectOperation(ctx context.Context) (func(), error) {
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		if err := s.validateAdmission(admission); err != nil {
			return nil, err
		}
		return nil, ErrPersonaAdmissionUnsupportedOperation
	}
	return s.beginSynchronous(ctx)
}

func (s *personaAdmissionState) enqueue(
	admission *persistencequeue.Admission,
	table, key, kind, payload string,
	apply func(context.Context) error,
) error {
	if s == nil || admission == nil || apply == nil || len(payload) == 0 {
		return ErrPersonaAdmissionUnavailable
	}
	write := &personaAdmissionWrite{
		admission: admission,
		table:     table,
		key:       key,
		payload:   payload,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errPersonaStoreClosing
	}
	if s.synchronousWrite {
		return ErrPersonaAdmissionBusy
	}
	queue := s.queue
	if queue == nil {
		return ErrPersonaAdmissionUnavailable
	}
	if !admission.MatchesQueue(queue) {
		return ErrPersonaAdmissionQueueMismatch
	}
	if failure := queue.Status().Failure; failure != nil {
		return fmt.Errorf("shared persistence queue failed: %w", failure)
	}
	ticket, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerPersona,
		Kind:      kind,
		SizeBytes: int64(len(payload)),
		Apply: func(workerCtx context.Context) error {
			release, err := s.beginAdmitted(workerCtx)
			if err != nil {
				s.failPending(err)
				return err
			}
			defer release()
			err = apply(workerCtx)
			if err != nil {
				s.failPending(err)
				return err
			}
			s.completeCommand(admission, table, key, payload)
			return nil
		},
	})
	if err != nil {
		return err
	}
	write.ticket = ticket
	s.pending[admission] = append(s.pending[admission], write)
	s.signalLocked()
	return nil
}

func (s *personaAdmissionState) beginAdmitted(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	if ctx == nil {
		return nil, persistencequeue.ErrNilContext
	}
	for {
		s.mu.Lock()
		if !s.synchronousWrite {
			s.admittedWriters++
			s.mu.Unlock()
			return s.endAdmitted, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (s *personaAdmissionState) endAdmitted() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.admittedWriters > 0 {
		s.admittedWriters--
	}
	s.signalLocked()
	s.mu.Unlock()
}

func (s *personaAdmissionState) beginSynchronous(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	if ctx == nil {
		return nil, persistencequeue.ErrNilContext
	}
	for {
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			return nil, errPersonaStoreClosing
		}
		if s.failure != nil {
			failure := errors.Join(persistencequeue.ErrFailed, s.failure)
			s.mu.Unlock()
			return nil, fmt.Errorf("Persona persistence queue failed: %w", failure)
		}
		if s.queue != nil {
			if failure := s.queue.Status().Failure; failure != nil {
				s.mu.Unlock()
				return nil, fmt.Errorf("shared persistence queue failed: %w", failure)
			}
		}
		if !s.synchronousWrite && s.admittedWriters == 0 && s.pendingCountLocked() == 0 {
			s.synchronousWrite = true
			s.mu.Unlock()
			return func() {
				s.mu.Lock()
				s.synchronousWrite = false
				s.signalLocked()
				s.mu.Unlock()
			}, nil
		}
		changed := s.changed
		admissions := s.pendingAdmissionsLocked()
		s.mu.Unlock()

		if len(admissions) > 0 {
			for _, admission := range admissions {
				if err := admission.Barrier(ctx); err != nil {
					return nil, err
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (s *personaAdmissionState) completeCommand(admission *persistencequeue.Admission, table, key, payload string) {
	if s == nil || admission == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordSnapshotLocked(table, key, payload)
	queue := s.pending[admission]
	for index, pending := range queue {
		if pending == nil || pending.table != table || pending.key != key || pending.payload != payload {
			continue
		}
		copy(queue[index:], queue[index+1:])
		queue[len(queue)-1] = nil
		queue = queue[:len(queue)-1]
		if len(queue) == 0 {
			delete(s.pending, admission)
		} else {
			s.pending[admission] = queue
		}
		s.signalLocked()
		return
	}
}

func (s *personaAdmissionState) setSnapshot(table string, writes []personaAdmissionWrite, limit int, complete bool) {
	if s == nil || table == "" || limit < 1 {
		return
	}
	s.mu.Lock()
	s.snapshots[table] = append([]personaAdmissionWrite(nil), writes...)
	s.snapshotLimits[table] = limit
	s.snapshotComplete[table] = complete
	s.mu.Unlock()
}

func (s *personaAdmissionState) snapshotAndPendingFor(admission *persistencequeue.Admission, table string, limit int) ([]personaAdmissionWrite, []personaAdmissionWrite, bool, error) {
	if err := s.validateAdmission(admission); err != nil {
		return nil, nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	available, ok := s.snapshotLimits[table]
	if !ok || limit > available {
		return nil, nil, false, ErrPersonaAdmissionSnapshotUnavailable
	}
	if s.synchronousWrite {
		return nil, nil, false, ErrPersonaAdmissionBusy
	}
	pending := make([]personaAdmissionWrite, 0)
	for _, writes := range s.pending {
		for _, write := range writes {
			if write != nil && write.table == table {
				pending = append(pending, *write)
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ticket < pending[j].ticket })
	return append([]personaAdmissionWrite(nil), s.snapshots[table]...), pending, s.snapshotComplete[table], nil
}

func (s *personaAdmissionState) observationSnapshotAndPending(admission *persistencequeue.Admission, eventID string) (personaAdmissionWrite, bool, bool, error) {
	if err := s.validateAdmission(admission); err != nil {
		return personaAdmissionWrite{}, false, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.snapshotLimits["observation_log"]
	if !ok {
		return personaAdmissionWrite{}, false, false, ErrPersonaAdmissionSnapshotUnavailable
	}
	if s.synchronousWrite {
		return personaAdmissionWrite{}, false, false, ErrPersonaAdmissionBusy
	}
	var pending []*personaAdmissionWrite
	for _, writes := range s.pending {
		if writes == nil {
			continue
		}
		for _, write := range writes {
			if write != nil && write.table == "observation_log" && write.key == eventID {
				if len(pending) == 0 {
					pending = []*personaAdmissionWrite{write}
				} else {
					pending = append(pending, write)
				}
			}
		}
	}
	if len(pending) > 0 {
		latest := pending[0]
		for _, write := range pending[1:] {
			if write.ticket > latest.ticket {
				latest = write
			}
		}
		return *latest, true, true, nil
	}
	for _, write := range s.snapshots["observation_log"] {
		if write.key == eventID {
			return write, true, true, nil
		}
	}
	return personaAdmissionWrite{}, false, s.snapshotComplete["observation_log"], nil
}

func (s *personaAdmissionState) recordSynchronous(table, key, payload string) {
	if s == nil || table == "" || key == "" || payload == "" {
		return
	}
	s.mu.Lock()
	s.recordSnapshotLocked(table, key, payload)
	s.mu.Unlock()
}

func (s *personaAdmissionState) recordSnapshotLocked(table, key, payload string) {
	limit, cached := s.snapshotLimits[table]
	if !cached || limit < 1 {
		return
	}
	writes := s.snapshots[table]
	found := false
	for _, write := range writes {
		if write.key == key {
			found = true
			break
		}
	}
	updated := make([]personaAdmissionWrite, 0, min(limit, len(writes)+1))
	updated = append(updated, personaAdmissionWrite{table: table, key: key, payload: payload})
	for _, write := range writes {
		if write.key == key {
			continue
		}
		if len(updated) == limit {
			break
		}
		updated = append(updated, write)
	}
	s.snapshots[table] = updated
	if !found && len(writes) >= limit {
		s.snapshotComplete[table] = false
	}
}

func (s *personaAdmissionState) failPending(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if err != nil && s.failure == nil {
		s.failure = err
	}
	s.pending = make(map[*persistencequeue.Admission][]*personaAdmissionWrite)
	s.signalLocked()
	s.mu.Unlock()
}

func (s *personaAdmissionState) pendingFor(admission *persistencequeue.Admission, table string) []personaAdmissionWrite {
	if s == nil || admission == nil {
		return nil
	}
	s.mu.Lock()
	writes := make([]personaAdmissionWrite, 0)
	for _, queue := range s.pending {
		for _, write := range queue {
			if write != nil && write.table == table {
				writes = append(writes, *write)
			}
		}
	}
	s.mu.Unlock()
	sort.Slice(writes, func(i, j int) bool { return writes[i].ticket < writes[j].ticket })
	return writes
}

func (s *personaAdmissionState) stopAcceptingAndDrain() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closing = true
	s.signalLocked()
	s.mu.Unlock()

	var result error
	for {
		s.mu.Lock()
		if s.pendingCountLocked() == 0 && !s.synchronousWrite && s.admittedWriters == 0 {
			s.mu.Unlock()
			return result
		}
		admissions := s.pendingAdmissionsLocked()
		changed := s.changed
		s.mu.Unlock()

		if len(admissions) == 0 {
			<-changed
			continue
		}
		var barrierErr error
		for _, admission := range admissions {
			if err := admission.Barrier(context.Background()); err != nil {
				barrierErr = errors.Join(barrierErr, err)
			}
		}
		if barrierErr != nil {
			result = errors.Join(result, barrierErr)
			s.mu.Lock()
			if s.failure == nil {
				s.failure = barrierErr
			}
			// The shared queue latches the first writer failure and will not run
			// the later accepted commands. Drop only their volatile read overlay.
			s.pending = make(map[*persistencequeue.Admission][]*personaAdmissionWrite)
			s.signalLocked()
			s.mu.Unlock()
		}
	}
}

func (s *personaAdmissionState) pendingCountLocked() int {
	count := 0
	for _, writes := range s.pending {
		count += len(writes)
	}
	return count
}

func (s *personaAdmissionState) pendingAdmissionsLocked() []*persistencequeue.Admission {
	admissions := make([]*persistencequeue.Admission, 0, len(s.pending))
	for admission, writes := range s.pending {
		if len(writes) > 0 && admission != nil {
			admissions = append(admissions, admission)
		}
	}
	return admissions
}

func decodePersonaAdmissionWrite[T any](write personaAdmissionWrite) (T, error) {
	var item T
	if err := json.Unmarshal([]byte(write.payload), &item); err != nil {
		return item, err
	}
	return item, nil
}

func personaRecordKey(item any) (string, bool) {
	switch value := item.(type) {
	case domainpersona.DiscomfortLog:
		return value.EventID, true
	case domainpersona.TriggerLog:
		return value.EventID, true
	case domainpersona.CanonicalResponseLog:
		return value.EventID, true
	case domainpersona.ObservationLog:
		return value.EventID, true
	case domainpersona.MetaProfileUpdate:
		return value.UpdateID, true
	case domainpersona.InterfaceSession:
		return value.SessionID, true
	default:
		return "", false
	}
}

func mergePersonaAdmissionWrites[T any](items []T, writes []personaAdmissionWrite, limit int) ([]T, error) {
	if len(writes) == 0 {
		return items, nil
	}
	seen := make(map[string]struct{}, len(writes)+len(items))
	merged := make([]T, 0, len(writes)+len(items))
	for index := len(writes) - 1; index >= 0; index-- {
		write := writes[index]
		if _, ok := seen[write.key]; ok {
			continue
		}
		item, err := decodePersonaAdmissionWrite[T](write)
		if err != nil {
			return nil, err
		}
		seen[write.key] = struct{}{}
		merged = append(merged, item)
	}
	for _, item := range items {
		key, ok := personaRecordKey(item)
		if !ok {
			return nil, errors.New("persona record type is unsupported by admission overlay")
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, item)
	}
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}
