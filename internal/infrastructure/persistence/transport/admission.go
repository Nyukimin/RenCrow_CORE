package transport

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrAdmissionUnavailable   = errors.New("transport RAM admission is unavailable")
	ErrAdmissionBusy          = errors.New("transport RAM admission is blocked by synchronous store access")
	ErrAdmissionQueueMismatch = errors.New("transport RAM admission uses a different persistence queue")
	errQueueAlreadyConfigured = errors.New("transport persistence queue is already configured differently")
	errQueueConfigured        = errors.New("transport persistence queue became configured; retry through FIFO")
)

// SetPersistenceQueue connects this JSONL owner to the process's shared RAM
// FIFO. Once connected, every write enters the same volatile read-your-writes
// view and FIFO; callers without an Admission wait for their own durable ticket.
func (s *JSONLStore) SetPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || queue == nil {
		return ErrAdmissionUnavailable
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if s.writeQueue != nil && s.writeQueue != queue {
		return errQueueAlreadyConfigured
	}
	s.writeQueue = queue
	return nil
}

func (s *JSONLStore) admittedTransaction(
	ctx context.Context,
	callback func(domaintransport.Store) error,
	admission *persistencequeue.Admission,
) error {
	_, _, queued, err := s.queueTransaction(ctx, callback, admission)
	if err != nil {
		return err
	}
	if !queued {
		return ErrAdmissionUnavailable
	}
	return nil
}

// queueTransaction snapshots the RAM view, runs the owner transaction, and
// enqueues its immutable write-set while holding the same short mutex. It
// publishes the new view only after enqueue succeeds. With a nil admission it
// reports queued=false if this owner has not been connected to the FIFO yet.
func (s *JSONLStore) queueTransaction(
	ctx context.Context,
	callback func(domaintransport.Store) error,
	admission *persistencequeue.Admission,
) (*persistencequeue.Queue[persistencequeue.PersistenceCommand], persistencequeue.Ticket, bool, error) {
	if ctx == nil {
		return nil, 0, false, persistencequeue.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if admission != nil {
		if err := s.validateAdmissionLocked(admission); err != nil {
			return nil, 0, false, err
		}
	} else {
		if s.writeQueue == nil {
			return nil, 0, false, nil
		}
		if s.admissionErr != nil {
			return nil, 0, true, fmt.Errorf("transport store is unavailable after a synchronous write failure: %w", s.admissionErr)
		}
		if failure := s.writeQueue.Status().Failure; failure != nil {
			return nil, 0, true, fmt.Errorf("shared transport persistence queue failed: %w", failure)
		}
	}
	queue := s.writeQueue
	tx := &memoryStore{
		requests:         cloneRequests(s.requests),
		responses:        cloneResponses(s.responses),
		changedRequests:  map[modulecore.RequestID]domaintransport.Request{},
		changedResponses: map[modulecore.ResponseID]domaintransport.Response{},
	}
	if err := callback(tx); err != nil {
		return queue, 0, true, err
	}
	payloads, err := tx.payloads()
	if err != nil {
		return queue, 0, true, err
	}
	if len(payloads) == 0 {
		return queue, s.lastTicket, true, nil
	}
	if err := ctx.Err(); err != nil {
		return queue, 0, true, err
	}
	var sizeBytes int64
	for _, payload := range payloads {
		sizeBytes += int64(len(payload))
	}
	if sizeBytes < 1 {
		return queue, 0, true, ErrAdmissionUnavailable
	}
	immutablePayloads := clonePayloads(payloads)
	command := persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerTransport,
		Kind:      "transport.receipt_transaction",
		SizeBytes: sizeBytes,
		Apply: func(workerContext context.Context) error {
			return s.batch.Write(workerContext, func() (map[string][]byte, error) {
				return clonePayloads(immutablePayloads), nil
			})
		},
	}
	var ticket persistencequeue.Ticket
	if admission != nil {
		ticket, err = admission.Enqueue(command)
	} else {
		var sequence uint64
		sequence, err = queue.Enqueue(command, sizeBytes)
		ticket = persistencequeue.Ticket(sequence)
	}
	if err != nil {
		return queue, 0, true, fmt.Errorf("enqueue transport transaction: %w", err)
	}
	// The accepted command and its in-memory projection become visible under
	// the same short critical section. The worker does not need this lock while
	// it waits for or writes the JSONL OS lock.
	s.requests = cloneRequests(tx.requests)
	s.responses = cloneResponses(tx.responses)
	s.lastTicket = ticket
	return queue, ticket, true, nil
}

func (s *JSONLStore) admittedReadTransaction(
	ctx context.Context,
	callback func(domaintransport.Store) error,
	admission *persistencequeue.Admission,
) error {
	if admission == nil {
		return ErrAdmissionUnavailable
	}
	if ctx == nil {
		return persistencequeue.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if err := s.validateAdmissionLocked(admission); err != nil {
		return err
	}
	return callback(&memoryStore{
		requests:  cloneRequests(s.requests),
		responses: cloneResponses(s.responses),
		readOnly:  true,
	})
}

// queuedReadTransaction waits for the owner's last accepted FIFO ticket, then
// reads the durable files without replacing the shared RAM view. It returns
// queued=false only when this owner has not been connected to the FIFO.
func (s *JSONLStore) queuedReadTransaction(
	ctx context.Context,
	callback func(domaintransport.Store) error,
) (bool, error) {
	if ctx == nil {
		return false, persistencequeue.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.admissionMu.Lock()
	queue, ticket, storeErr := s.writeQueue, s.lastTicket, s.admissionErr
	if queue != nil && storeErr == nil {
		if failure := queue.Status().Failure; failure != nil {
			storeErr = fmt.Errorf("shared transport persistence queue failed: %w", failure)
		}
	}
	s.admissionMu.Unlock()
	if queue == nil {
		return false, nil
	}
	if storeErr != nil {
		return true, fmt.Errorf("transport store is unavailable: %w", storeErr)
	}
	if ticket != 0 {
		if err := queue.Wait(ctx, uint64(ticket)); err != nil {
			return true, fmt.Errorf("wait for accepted transport persistence: %w", err)
		}
	} else if err := ctx.Err(); err != nil {
		return true, err
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.batch.Read(ctx, func() error {
		requests, err := s.loadRequests()
		if err != nil {
			return err
		}
		responses, err := s.loadResponses()
		if err != nil {
			return err
		}
		return callback(&memoryStore{requests: requests, responses: responses, readOnly: true})
	})
	return true, err
}

func (s *JSONLStore) validateAdmissionLocked(admission *persistencequeue.Admission) error {
	if s.writeQueue == nil {
		return ErrAdmissionUnavailable
	}
	if s.admissionErr != nil {
		return fmt.Errorf("transport store is unavailable after a synchronous write failure: %w", s.admissionErr)
	}
	if !admission.MatchesQueue(s.writeQueue) {
		return ErrAdmissionQueueMismatch
	}
	if failure := s.writeQueue.Status().Failure; failure != nil {
		return fmt.Errorf("shared transport persistence queue failed: %w", failure)
	}
	return nil
}

// withUnqueuedSynchronousAccess preserves the legacy synchronous path only
// before this owner is connected to the shared FIFO. The admission mutex is
// held through the filesystem operation so queue setup cannot race a direct
// write or read.
func (s *JSONLStore) withUnqueuedSynchronousAccess(ctx context.Context, run func() error) error {
	if ctx == nil {
		return persistencequeue.ErrNilContext
	}
	if run == nil {
		return errors.New("transport synchronous operation is nil")
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if s.writeQueue != nil {
		return errQueueConfigured
	}
	if s.admissionErr != nil {
		return fmt.Errorf("transport store is unavailable after a synchronous write failure: %w", s.admissionErr)
	}
	return run()
}

func cloneRequests(values map[modulecore.RequestID]domaintransport.Request) map[modulecore.RequestID]domaintransport.Request {
	result := make(map[modulecore.RequestID]domaintransport.Request, len(values))
	for key, value := range values {
		result[key] = cloneRequest(value)
	}
	return result
}

func cloneResponses(values map[modulecore.ResponseID]domaintransport.Response) map[modulecore.ResponseID]domaintransport.Response {
	result := make(map[modulecore.ResponseID]domaintransport.Response, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneRequest(value domaintransport.Request) domaintransport.Request {
	if value.CompletedAt != nil {
		completed := *value.CompletedAt
		value.CompletedAt = &completed
	}
	return value
}

func clonePayloads(values map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(values))
	for key, value := range values {
		result[key] = append([]byte(nil), value...)
	}
	return result
}
