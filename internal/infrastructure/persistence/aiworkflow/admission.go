package aiworkflow

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
)

var (
	ErrAdmissionUnavailable   = errors.New("AI Workflow RAM admission is unavailable")
	ErrAdmissionBusy          = errors.New("AI Workflow persistence is fenced by a synchronous operation")
	ErrAdmissionQueueMismatch = errors.New("AI Workflow admission uses a different persistence queue")
	errStoreClosing           = errors.New("AI Workflow persistence store is closing")
)

// admissionState keeps only the queue binding and the last accepted owner
// ticket. SQLite/JSONL remains the durable source of truth; there is no pending
// read projection because CHAT does not read these records on the write path.
type admissionState struct {
	// Synchronous operations take the write lock only after waiting for queued
	// owner commands. Admission uses TryRLock so it never waits for disk I/O.
	syncAccess sync.RWMutex
	mu         sync.Mutex
	queue      *persistencequeue.Queue[persistencequeue.PersistenceCommand]
	lastTicket persistencequeue.Ticket
	closing    bool
}

func (s *SQLiteStore) SetPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || s.db == nil {
		return ErrAdmissionUnavailable
	}
	return s.admissionWrites.setQueue(queue)
}

func (s *SQLiteStore) MatchesPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool {
	return s != nil && s.admissionWrites.matchesQueue(queue)
}

func (s *JSONLStore) SetPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil {
		return ErrAdmissionUnavailable
	}
	return s.admissionWrites.setQueue(queue)
}

func (s *JSONLStore) MatchesPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool {
	return s != nil && s.admissionWrites.matchesQueue(queue)
}

func (s *admissionState) setQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || queue == nil {
		return ErrAdmissionUnavailable
	}
	if !s.syncAccess.TryLock() {
		return ErrAdmissionBusy
	}
	defer s.syncAccess.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errStoreClosing
	}
	if s.queue != nil && s.queue != queue {
		return ErrAdmissionQueueMismatch
	}
	s.queue = queue
	return nil
}

func (s *admissionState) matchesQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool {
	if s == nil || queue == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closing && s.queue == queue
}

func (s *admissionState) enqueue(
	ctx context.Context,
	admission *persistencequeue.Admission,
	kind string,
	payload []byte,
	apply func(context.Context, []byte) error,
) error {
	if s == nil || admission == nil || len(payload) == 0 || apply == nil {
		return ErrAdmissionUnavailable
	}
	if ctx == nil {
		return persistencequeue.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.syncAccess.TryRLock() {
		return ErrAdmissionBusy
	}
	defer s.syncAccess.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errStoreClosing
	}
	if s.queue == nil {
		return ErrAdmissionUnavailable
	}
	if !admission.MatchesQueue(s.queue) {
		return ErrAdmissionQueueMismatch
	}
	if failure := s.queue.Status().Failure; failure != nil {
		return fmt.Errorf("shared persistence queue failed: %w", failure)
	}

	immutablePayload := append([]byte(nil), payload...)
	ticket, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerAIWorkflow,
		Kind:      kind,
		SizeBytes: int64(len(immutablePayload)),
		Apply: func(workerCtx context.Context) error {
			return apply(workerCtx, immutablePayload)
		},
	})
	if err != nil {
		return fmt.Errorf("enqueue AI Workflow persistence command %s: %w", kind, err)
	}
	s.lastTicket = ticket
	return nil
}

func persist(
	ctx context.Context,
	state *admissionState,
	kind string,
	payload []byte,
	apply func(context.Context, []byte) error,
) error {
	if admission, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		return state.enqueue(ctx, admission, kind, payload, apply)
	}
	return state.withSynchronousFence(ctx, func() error {
		return apply(ctx, payload)
	})
}

// withSynchronousFence preserves the existing synchronous APIs. It waits for
// the last accepted owner ticket without holding syncAccess, then excludes new
// admissions while the direct database or filesystem operation runs.
func (s *admissionState) withSynchronousFence(ctx context.Context, run func() error) error {
	if s == nil {
		return ErrAdmissionUnavailable
	}
	if ctx == nil {
		return persistencequeue.ErrNilContext
	}
	if run == nil {
		return errors.New("AI Workflow synchronous operation is nil")
	}
	for {
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			return errStoreClosing
		}
		queue, ticket := s.queue, s.lastTicket
		s.mu.Unlock()

		if err := ctx.Err(); err != nil {
			return err
		}
		if queue != nil {
			if failure := queue.Status().Failure; failure != nil {
				return fmt.Errorf("shared persistence queue failed: %w", failure)
			}
			if ticket != 0 {
				if err := queue.Wait(ctx, uint64(ticket)); err != nil {
					return fmt.Errorf("wait for accepted AI Workflow persistence: %w", err)
				}
			}
		}

		s.syncAccess.Lock()
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			s.syncAccess.Unlock()
			return errStoreClosing
		}
		if s.queue != queue || s.lastTicket != ticket {
			s.mu.Unlock()
			s.syncAccess.Unlock()
			continue
		}
		if queue != nil {
			if failure := queue.Status().Failure; failure != nil {
				s.mu.Unlock()
				s.syncAccess.Unlock()
				return fmt.Errorf("shared persistence queue failed: %w", failure)
			}
		}
		s.mu.Unlock()
		err := run()
		s.syncAccess.Unlock()
		return err
	}
}

func (s *admissionState) stopAndFence(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	if ctx == nil {
		return nil, persistencequeue.ErrNilContext
	}
	s.mu.Lock()
	s.closing = true
	queue, ticket := s.queue, s.lastTicket
	s.mu.Unlock()

	var waitErr error
	if queue != nil && ticket != 0 {
		waitErr = queue.Wait(ctx, uint64(ticket))
	}
	s.syncAccess.Lock()
	return s.syncAccess.Unlock, waitErr
}
