package eventstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrPersistenceAdmissionRequired      = errors.New("eventstore persistence admission is required")
	ErrPersistenceAdmissionQueueMismatch = errors.New("eventstore persistence admission belongs to a different queue")
	ErrEventSeqWritePending              = errors.New("an earlier EventSeq write is pending")
	ErrChatLeaseActive                   = errors.New("eventstore CHAT lease is already active")
	ErrChatLeaseRequired                 = errors.New("eventstore CHAT lease is required")
	ErrChatAdmissionUnavailable          = errors.New("eventstore queue does not support reserved admissions")
)

// AdmissionWriteQueue is the optional WriteQueue capability required for
// asynchronous CHAT commands. It proves that the request reservation belongs
// to the same shared queue before accepting a command.
type AdmissionWriteQueue interface {
	MatchesAdmission(*persistencequeue.Admission) bool
	CheckAdmission(*persistencequeue.Admission) error
	EnqueueAdmission(context.Context, *persistencequeue.Admission, int64, func(context.Context) error) (uint64, error)
}

// PersistenceQueueWriteQueue adapts the shared persistence queue to the
// EventStore's synchronous and request-reserved write contracts.
type PersistenceQueueWriteQueue struct {
	queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]
}

// NewPersistenceQueueWriteQueue binds EventStore to the shared in-memory
// persistence queue used for both ordinary writes and reserved admissions.
func NewPersistenceQueueWriteQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) (*PersistenceQueueWriteQueue, error) {
	if queue == nil {
		return nil, persistencequeue.ErrNilPersistenceQueue
	}
	return &PersistenceQueueWriteQueue{queue: queue}, nil
}

func (q *PersistenceQueueWriteQueue) Enqueue(ctx context.Context, sizeBytes int64, apply func(context.Context) error) (uint64, error) {
	if q == nil || q.queue == nil {
		return 0, persistencequeue.ErrNilPersistenceQueue
	}
	if ctx == nil {
		return 0, persistencequeue.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return q.queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "eventstore.write", SizeBytes: sizeBytes, Apply: apply,
	}, sizeBytes)
}

func (q *PersistenceQueueWriteQueue) Wait(ctx context.Context, ticket uint64) error {
	if q == nil || q.queue == nil {
		return persistencequeue.ErrNilPersistenceQueue
	}
	return q.queue.Wait(ctx, ticket)
}

func (q *PersistenceQueueWriteQueue) MatchesAdmission(admission *persistencequeue.Admission) bool {
	return q != nil && q.queue != nil && admission != nil && admission.MatchesQueue(q.queue)
}

func (q *PersistenceQueueWriteQueue) CheckAdmission(admission *persistencequeue.Admission) error {
	if !q.MatchesAdmission(admission) {
		return ErrPersistenceAdmissionQueueMismatch
	}
	status := q.queue.Status()
	if status.Failure != nil {
		return status.Failure
	}
	if !status.Accepting {
		return persistencequeue.ErrClosed
	}
	return nil
}

func (q *PersistenceQueueWriteQueue) EnqueueAdmission(
	ctx context.Context,
	admission *persistencequeue.Admission,
	sizeBytes int64,
	apply func(context.Context) error,
) (uint64, error) {
	if ctx == nil {
		return 0, persistencequeue.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !q.MatchesAdmission(admission) {
		return 0, ErrPersistenceAdmissionQueueMismatch
	}
	ticket, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "eventstore.chat.admitted", SizeBytes: sizeBytes, Apply: apply,
	})
	return uint64(ticket), err
}

// ChatLease gates EventSeq admissions while a combined CHAT request is active.
type ChatLease struct {
	store                          *SQLiteStore
	admission                      *persistencequeue.Admission
	done                           chan struct{}
	ended                          bool // guarded by store.sequenceAdmissionMu
	committedOrchestratorHighWater modulecore.EventSeq
}

// TryBeginChat creates the single EventStore CHAT lease without waiting for
// SQLite. It requires an admission reserved from this store's shared queue and
// fails if an earlier synchronous EventSeq write or a waiting synchronous
// writer could otherwise be overtaken. An earlier CHAT admission may still be
// draining after its caller ended that lease following publication.
func (s *SQLiteStore) TryBeginChat(ctx context.Context) (*ChatLease, error) {
	if ctx == nil {
		return nil, errors.New("eventstore CHAT context is required")
	}
	admission, ok := persistencequeue.AdmissionFromContext(ctx)
	if !ok {
		return nil, ErrPersistenceAdmissionRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	queue, ok := s.writeQueue.(AdmissionWriteQueue)
	if !ok {
		return nil, ErrChatAdmissionUnavailable
	}
	if !queue.MatchesAdmission(admission) {
		return nil, ErrPersistenceAdmissionQueueMismatch
	}
	if s.sequenceAdmissionMu == nil {
		return nil, errors.New("eventstore sequence admission mutex is unavailable")
	}
	s.sequenceAdmissionMu.Lock()
	defer s.sequenceAdmissionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.chatLease != nil && !s.chatLease.ended {
		return nil, ErrChatLeaseActive
	}
	if s.pendingSyncSequenceWrites != 0 || s.waitingSyncSequenceWrites != 0 {
		return nil, ErrEventSeqWritePending
	}
	if err := queue.CheckAdmission(admission); err != nil {
		return nil, err
	}
	lease := &ChatLease{
		store: s, admission: admission, done: make(chan struct{}),
		committedOrchestratorHighWater: s.OrchestratorCommittedHighWater(),
	}
	s.chatLease = lease
	return lease, nil
}

// BeginChatAdmission is the adapter-friendly lease API. Its high-water value
// is captured atomically with lease activation and requires no database read.
func (s *SQLiteStore) BeginChatAdmission(ctx context.Context) (func() error, modulecore.EventSeq, error) {
	lease, err := s.TryBeginChat(ctx)
	if err != nil {
		return nil, 0, err
	}
	return lease.EndChat, lease.OrchestratorCommittedHighWater(), nil
}

// OrchestratorCommittedHighWater returns the immutable acquisition-time
// snapshot used to prove whether a committed orchestrator predecessor has
// already reached live publication.
func (lease *ChatLease) OrchestratorCommittedHighWater() modulecore.EventSeq {
	if lease == nil {
		return 0
	}
	return lease.committedOrchestratorHighWater
}

// EndChat releases the logical CHAT lease. The caller must first publish every
// EventSeq accepted through the lease; EndChat does not wait for SQLite. Once
// ended, later CHAT admissions may proceed in queue order while prior appends
// are still draining. It is safe to call more than once.
func (lease *ChatLease) EndChat() error {
	if lease == nil || lease.store == nil {
		return errors.New("eventstore CHAT lease is invalid")
	}
	s := lease.store
	if s.sequenceAdmissionMu == nil {
		return errors.New("eventstore sequence admission mutex is unavailable")
	}
	s.sequenceAdmissionMu.Lock()
	defer s.sequenceAdmissionMu.Unlock()
	if lease.ended {
		return nil
	}
	lease.ended = true
	close(lease.done)
	if s.chatLease == lease {
		s.chatLease = nil
	}
	return nil
}

// AdmitSequenced assigns one EventSeq and submits an immutable Event envelope
// through the active CHAT lease's reserved queue admission. It returns after
// RAM queue acceptance, with the queue-local FIFO ticket; persistence remains
// asynchronous and every append error poisons the queue head.
func (s *SQLiteStore) AdmitSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, uint64, error) {
	if ctx == nil {
		return modulecore.EventEnvelope{}, 0, errors.New("eventstore CHAT context is required")
	}
	admission, ok := persistencequeue.AdmissionFromContext(ctx)
	if !ok {
		return modulecore.EventEnvelope{}, 0, ErrPersistenceAdmissionRequired
	}
	if err := ctx.Err(); err != nil {
		return modulecore.EventEnvelope{}, 0, err
	}
	if err := s.ensureOpen(); err != nil {
		return modulecore.EventEnvelope{}, 0, err
	}
	queue, ok := s.writeQueue.(AdmissionWriteQueue)
	if !ok {
		return modulecore.EventEnvelope{}, 0, ErrChatAdmissionUnavailable
	}
	if !queue.MatchesAdmission(admission) {
		return modulecore.EventEnvelope{}, 0, ErrPersistenceAdmissionQueueMismatch
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return modulecore.EventEnvelope{}, 0, fmt.Errorf("validate event envelope: %w", err)
	}
	if event.EventSeq != 0 {
		return modulecore.EventEnvelope{}, 0, errors.New("event_seq must be zero for live append; positive sequences are reserved for AppendBatch")
	}
	queuedEvent := cloneEventEnvelope(event)
	if s.sequenceAdmissionMu == nil {
		return modulecore.EventEnvelope{}, 0, errors.New("eventstore sequence admission mutex is unavailable")
	}
	s.sequenceAdmissionMu.Lock()
	if err := ctx.Err(); err != nil {
		s.sequenceAdmissionMu.Unlock()
		return modulecore.EventEnvelope{}, 0, err
	}
	lease := s.chatLease
	if lease == nil || lease.ended || lease.admission != admission {
		s.sequenceAdmissionMu.Unlock()
		return modulecore.EventEnvelope{}, 0, ErrChatLeaseRequired
	}
	sequences, _, err := s.sequence.reserve(1)
	if err != nil {
		s.sequenceAdmissionMu.Unlock()
		return modulecore.EventEnvelope{}, 0, err
	}
	queuedEvent.EventSeq = modulecore.EventSeq(sequences.next)
	sizeBytes, err := queuedWriteSize(queuedEvent)
	if err != nil {
		s.sequence.rollback(sequences)
		s.sequenceAdmissionMu.Unlock()
		return modulecore.EventEnvelope{}, 0, fmt.Errorf("size queued EventStore envelope: %w", err)
	}
	commandState := &eventStoreCommandState{store: s, sequences: sequences, sequenceFloor: sequences.previous}
	ticket, err := queue.EnqueueAdmission(ctx, admission, sizeBytes, func(workerCtx context.Context) error {
		// The queue may start the worker before EnqueueAdmission returns. Keep this
		// synchronization short; the lease and admission mutex are never held for
		// SQLite I/O or for the CHAT/LLM lifetime.
		s.sequenceAdmissionMu.Lock()
		s.sequenceAdmissionMu.Unlock()
		if workerCtx == nil {
			workerCtx = context.Background()
		}
		commandCtx := context.WithValue(workerCtx, eventStoreCommandContextKey{}, commandState)
		if _, appendErr := s.appendSequencedInline(commandCtx, queuedEvent); appendErr != nil {
			// Every error from an accepted async append is a queue-head failure.
			return appendErr
		}
		return nil
	})
	if err != nil {
		s.sequence.rollback(sequences)
		s.sequenceAdmissionMu.Unlock()
		return modulecore.EventEnvelope{}, 0, err
	}
	s.sequenceAdmissionMu.Unlock()
	return queuedEvent, ticket, nil
}
