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
)

// SetPersistenceQueue connects this JSONL owner to the process's shared RAM
// FIFO. Existing calls remain synchronous; only a context carrying an
// Admission created from this exact queue uses the volatile read-your-writes
// view and deferred JSONL append.
func (s *JSONLStore) SetPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || queue == nil {
		return ErrAdmissionUnavailable
	}
	if !s.admissionMu.TryLock() {
		return ErrAdmissionBusy
	}
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
	if admission == nil {
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
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if err := s.validateAdmissionLocked(admission); err != nil {
		return err
	}
	tx := &memoryStore{
		requests:         cloneRequests(s.requests),
		responses:        cloneResponses(s.responses),
		changedRequests:  map[modulecore.RequestID]domaintransport.Request{},
		changedResponses: map[modulecore.ResponseID]domaintransport.Response{},
	}
	if err := callback(tx); err != nil {
		return err
	}
	payloads, err := tx.payloads()
	if err != nil {
		return err
	}
	if len(payloads) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var sizeBytes int64
	for _, payload := range payloads {
		sizeBytes += int64(len(payload))
	}
	if sizeBytes < 1 {
		return ErrAdmissionUnavailable
	}
	immutablePayloads := clonePayloads(payloads)
	ticket, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerTransport,
		Kind:      "transport.receipt_transaction",
		SizeBytes: sizeBytes,
		Apply: func(workerContext context.Context) error {
			return s.batch.Write(workerContext, func() (map[string][]byte, error) {
				return clonePayloads(immutablePayloads), nil
			})
		},
	})
	if err != nil {
		return fmt.Errorf("enqueue transport transaction: %w", err)
	}
	// The accepted command and its in-memory projection become visible under
	// the same short critical section. The worker does not need this lock while
	// it waits for or writes the JSONL OS lock.
	s.requests = cloneRequests(tx.requests)
	s.responses = cloneResponses(tx.responses)
	s.lastTicket = ticket
	return nil
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
	if !s.syncAccess.TryRLock() {
		return ErrAdmissionBusy
	}
	defer s.syncAccess.RUnlock()
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

// withSynchronousFence keeps the existing synchronous path intact while
// ensuring it observes every accepted command from this owner first. Once the
// exclusive gate is acquired, admitted calls fail immediately instead of
// waiting behind this filesystem operation.
func (s *JSONLStore) withSynchronousFence(ctx context.Context, run func() error) error {
	if ctx == nil {
		return persistencequeue.ErrNilContext
	}
	if run == nil {
		return errors.New("transport synchronous operation is nil")
	}
	for {
		s.admissionMu.Lock()
		queue, ticket := s.writeQueue, s.lastTicket
		storeErr := s.admissionErr
		s.admissionMu.Unlock()
		if storeErr != nil {
			return fmt.Errorf("transport store is unavailable after a synchronous write failure: %w", storeErr)
		}
		if queue != nil {
			if failure := queue.Status().Failure; failure != nil {
				return fmt.Errorf("shared transport persistence queue failed: %w", failure)
			}
			if ticket != 0 {
				if err := queue.Wait(ctx, uint64(ticket)); err != nil {
					return fmt.Errorf("wait for accepted transport persistence: %w", err)
				}
			}
		}

		s.syncAccess.Lock()
		s.admissionMu.Lock()
		if s.writeQueue != queue || s.lastTicket != ticket {
			s.admissionMu.Unlock()
			s.syncAccess.Unlock()
			continue
		}
		if s.admissionErr != nil {
			err := s.admissionErr
			s.admissionMu.Unlock()
			s.syncAccess.Unlock()
			return fmt.Errorf("transport store is unavailable after a synchronous write failure: %w", err)
		}
		if queue != nil {
			if failure := queue.Status().Failure; failure != nil {
				s.admissionMu.Unlock()
				s.syncAccess.Unlock()
				return fmt.Errorf("shared transport persistence queue failed: %w", failure)
			}
		}
		err := run()
		s.admissionMu.Unlock()
		s.syncAccess.Unlock()
		return err
	}
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
