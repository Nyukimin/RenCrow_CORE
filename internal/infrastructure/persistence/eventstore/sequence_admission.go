package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"modernc.org/sqlite"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// WriteQueue is the eventstore-facing contract for the process-wide bounded
// persistence queue. Enqueue must preserve FIFO order and return without
// running apply inline. Implementations own the worker; EventStore only submits
// commands and waits for their tickets.
type WriteQueue interface {
	Enqueue(ctx context.Context, sizeBytes int64, apply func(context.Context) error) (ticket uint64, err error)
	Wait(ctx context.Context, ticket uint64) error
}

type eventSequenceRange struct {
	next      int64
	limit     int64
	previous  int64
	planned   []modulecore.EventSeq
	plannedAt int
	usePlan   bool
}

func (r *eventSequenceRange) take() (modulecore.EventSeq, error) {
	if r != nil && r.usePlan {
		if r.plannedAt >= len(r.planned) {
			return 0, errors.New("eventstore command exhausted its reserved event_seq range")
		}
		sequence := r.planned[r.plannedAt]
		r.plannedAt++
		return sequence, nil
	}
	if r == nil || r.next > r.limit {
		return 0, errors.New("eventstore command exhausted its reserved event_seq range")
	}
	sequence := r.next
	r.next++
	return modulecore.EventSeq(sequence), nil
}

type eventStoreCommandState struct {
	store         *SQLiteStore
	sequences     *eventSequenceRange
	sequenceFloor int64
}

type eventStoreCommandContextKey struct{}

func (s *SQLiteStore) nextSequence(ctx context.Context) (modulecore.EventSeq, error) {
	if ctx != nil {
		state, _ := ctx.Value(eventStoreCommandContextKey{}).(*eventStoreCommandState)
		if state != nil && state.store == s && state.sequences != nil {
			return state.sequences.take()
		}
	}
	return s.sequence.next()
}

func (s *SQLiteStore) sequenceFloor(ctx context.Context) int64 {
	if ctx != nil {
		state, _ := ctx.Value(eventStoreCommandContextKey{}).(*eventStoreCommandState)
		if state != nil && state.store == s {
			return state.sequenceFloor
		}
	}
	if s == nil || s.sequence == nil {
		return 0
	}
	return s.sequence.last.Load()
}

func (s *SQLiteStore) isQueueWorkerContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	state, _ := ctx.Value(eventStoreCommandContextKey{}).(*eventStoreCommandState)
	return state != nil && state.store == s
}

func (s *SQLiteStore) shouldQueueWrite(ctx context.Context) bool {
	return s != nil && s.writeQueue != nil && !s.isQueueWorkerContext(ctx)
}

func (s *SQLiteStore) enqueueOrderedWrite(ctx context.Context, sequenceCount int, sizeBytes int64, apply func(context.Context) error) error {
	return s.enqueueOrderedWriteWithReservation(ctx, sizeBytes, func() (*eventSequenceRange, int64, error) {
		return s.sequence.reserve(sequenceCount)
	}, apply)
}

func (s *SQLiteStore) enqueueOrderedBatchWrite(ctx context.Context, events []modulecore.EventEnvelope, sizeBytes int64, apply func(context.Context) error) error {
	return s.enqueueOrderedWriteWithReservation(ctx, sizeBytes, func() (*eventSequenceRange, int64, error) {
		return s.sequence.reserveBatch(events)
	}, apply)
}

func (s *SQLiteStore) enqueueOrderedWriteWithReservation(
	ctx context.Context,
	sizeBytes int64,
	reserve func() (*eventSequenceRange, int64, error),
	apply func(context.Context) error,
) error {
	if ctx == nil {
		return errors.New("eventstore write context is required")
	}
	if s == nil || s.writeQueue == nil || reserve == nil {
		return errors.New("eventstore write queue is unavailable")
	}
	if sizeBytes < 1 || apply == nil {
		return errors.New("eventstore queued write size and callback are required")
	}
	if s.sequenceAdmissionMu == nil {
		return errors.New("eventstore sequence admission mutex is unavailable")
	}
	result := make(chan error, 1)
	var waitingForChatLease bool
	for {
		s.sequenceAdmissionMu.Lock()
		if err := ctx.Err(); err != nil {
			if waitingForChatLease && s.waitingSyncSequenceWrites > 0 {
				s.waitingSyncSequenceWrites--
			}
			s.sequenceAdmissionMu.Unlock()
			return err
		}
		if lease := s.chatLease; lease != nil && !lease.ended {
			if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok && admission == lease.admission {
				if waitingForChatLease && s.waitingSyncSequenceWrites > 0 {
					s.waitingSyncSequenceWrites--
				}
				s.sequenceAdmissionMu.Unlock()
				return ErrChatLeaseActive
			}
			if !waitingForChatLease {
				s.waitingSyncSequenceWrites++
				waitingForChatLease = true
			}
			leaseDone := lease.done
			s.sequenceAdmissionMu.Unlock()
			select {
			case <-ctx.Done():
			case <-leaseDone:
			}
			continue
		}
		if waitingForChatLease && s.waitingSyncSequenceWrites > 0 {
			s.waitingSyncSequenceWrites--
			waitingForChatLease = false
		}
		break
	}
	sequences, sequenceFloor, err := reserve()
	if err != nil {
		s.sequenceAdmissionMu.Unlock()
		return err
	}
	ticket, err := s.writeQueue.Enqueue(ctx, sizeBytes, func(workerCtx context.Context) error {
		// Enqueue may wake the worker before it returns. Wait for the short
		// sequence reservation/enqueue critical section to end, then release the
		// mutex before touching SQLite.
		s.sequenceAdmissionMu.Lock()
		s.sequenceAdmissionMu.Unlock()
		if err := ctx.Err(); err != nil {
			s.finishSynchronousSequenceWrite()
			result <- err
			return nil
		}
		if workerCtx != nil {
			if err := workerCtx.Err(); err != nil {
				s.finishSynchronousSequenceWrite()
				result <- err
				return nil
			}
		}
		commandCtx := context.WithValue(ctx, eventStoreCommandContextKey{}, &eventStoreCommandState{
			store: s, sequences: sequences, sequenceFloor: sequenceFloor,
		})
		commandErr := apply(commandCtx)
		result <- commandErr
		if isFatalEventStoreWriteError(ctx, commandErr) {
			return commandErr
		}
		s.finishSynchronousSequenceWrite()
		return nil
	})
	if err != nil {
		s.sequence.rollback(sequences)
		s.sequenceAdmissionMu.Unlock()
		return err
	}
	s.pendingSyncSequenceWrites++
	s.sequenceAdmissionMu.Unlock()

	if err := s.writeQueue.Wait(ctx, ticket); err != nil {
		select {
		case commandErr := <-result:
			if commandErr != nil {
				return commandErr
			}
		default:
		}
		return err
	}
	return <-result
}

func (s *SQLiteStore) finishSynchronousSequenceWrite() {
	if s == nil || s.sequenceAdmissionMu == nil {
		return
	}
	s.sequenceAdmissionMu.Lock()
	if s.pendingSyncSequenceWrites > 0 {
		s.pendingSyncSequenceWrites--
	}
	s.sequenceAdmissionMu.Unlock()
}

func enqueueOrderedWriteResult[T any](
	s *SQLiteStore,
	ctx context.Context,
	sequenceCount int,
	command any,
	apply func(context.Context) (T, error),
) (T, error) {
	var zero T
	sizeBytes, err := queuedWriteSize(command)
	if err != nil {
		return zero, fmt.Errorf("size queued EventStore write: %w", err)
	}
	var result T
	err = s.enqueueOrderedWrite(ctx, sequenceCount, sizeBytes, func(commandCtx context.Context) error {
		var applyErr error
		result, applyErr = apply(commandCtx)
		return applyErr
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}

func cloneQueuedValue[T any](value T) T {
	cloned := cloneEventValue(reflect.ValueOf(value))
	if cloned.IsValid() {
		if result, ok := cloned.Interface().(T); ok {
			return result
		}
	}
	return value
}

func isFatalEventStoreWriteError(requestContext context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	var sqliteError *sqlite.Error
	if errors.As(err, &sqliteError) {
		// Constraint violations are command outcomes (duplicate IDs, conflict
		// fences, and related expected races). Other SQLite errors describe a
		// storage failure and must fail the shared queue head closed.
		if sqliteError.Code()&0xff == 19 {
			return false
		}
		if requestContext != nil && requestContext.Err() != nil && sqliteError.Code()&0xff == 9 {
			return false
		}
		return true
	}
	if requestContext != nil && requestContext.Err() != nil {
		return false
	}
	return errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone)
}

func queuedWriteSize(value any) (int64, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	if len(encoded) == 0 {
		return 1, nil
	}
	return int64(len(encoded)), nil
}

func cloneMutablePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	cloned, _ := cloneEventValue(reflect.ValueOf(payload)).Interface().(map[string]any)
	return cloned
}

func cloneEventEnvelope(event modulecore.EventEnvelope) modulecore.EventEnvelope {
	event.Payload = cloneMutablePayload(event.Payload)
	event.DependencyEventIDs = append([]modulecore.EventID(nil), event.DependencyEventIDs...)
	return event
}

func cloneEventValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneEventValue(value.Elem())
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(cloneEventValue(value.Elem()))
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(cloneEventValue(iterator.Key()), cloneEventValue(iterator.Value()))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneEventValue(value.Index(index)))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneEventValue(value.Index(index)))
		}
		return result
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if result.Field(index).CanSet() && value.Type().Field(index).IsExported() {
				result.Field(index).Set(cloneEventValue(value.Field(index)))
			}
		}
		return result
	default:
		return value
	}
}
