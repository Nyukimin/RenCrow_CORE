package persistencequeue

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var (
	ErrFull                = errors.New("persistence queue is full")
	ErrClosed              = errors.New("persistence queue is closed")
	ErrFailed              = errors.New("persistence queue writer failed")
	ErrInvalidCapacity     = errors.New("persistence queue capacity must be positive")
	ErrInvalidByteCapacity = errors.New("persistence queue byte capacity must be positive")
	ErrInvalidReservation  = errors.New("persistence queue reservation is invalid or inactive")
	ErrReservationBytes    = errors.New("persistence queue reservation byte budget exceeded")
	ErrInvalidCommandSize  = errors.New("persistence queue command size must be positive")
	ErrNilWriter           = errors.New("persistence queue writer is nil")
	ErrNilContext          = errors.New("context must not be nil")
	ErrUnknownSequence     = errors.New("unknown persistence queue sequence")
	ErrSequenceExhausted   = errors.New("persistence queue sequence exhausted")
)

// Command is an accepted persistence command and its queue-local FIFO sequence.
// Value must be immutable after admission, including any data reachable through pointers.
type Command[T any] struct {
	Sequence uint64
	Value    T
}

// Reservation holds queue capacity for a bounded group of later commands.
// Its zero value and copied values are invalid; use Queue.Reserve to create one.
type Reservation[T any] struct {
	marker byte
}

// Failure records the first command the single worker could not write.
type Failure struct {
	Sequence uint64
	Err      error
}

func (f *Failure) Error() string {
	if f == nil {
		return ErrFailed.Error()
	}
	return fmt.Sprintf("%s at sequence %d: %v", ErrFailed, f.Sequence, f.Err)
}

func (f *Failure) Unwrap() error { return f.Err }

func (f *Failure) Is(target error) bool {
	return target == ErrFailed
}

// Status is a point-in-time view. Pending and PendingBytes include the command
// currently in the writer. OldestPendingAt is zero when the queue has no pending
// commands.
type Status struct {
	Accepting       bool
	Pending         int
	Reserved        int
	PendingBytes    int64
	ReservedBytes   int64
	OldestSequence  uint64
	OldestPendingAt time.Time
	InFlight        bool
	Failure         *Failure
}

type queuedItem[T any] struct {
	command    Command[T]
	sizeBytes  int64
	acceptedAt time.Time
}

type reservationState struct {
	count     int
	bytesLeft int64
}

type Queue[T any] struct {
	mu                sync.Mutex
	ready             *sync.Cond
	changed           chan struct{}
	items             []queuedItem[T]
	reservations      map[*Reservation[T]]reservationState
	reserved          int
	capacity          int
	byteCapacity      int64
	pendingBytes      int64
	reservedBytes     int64
	writer            func(Command[T]) error
	accepting         bool
	inFlight          bool
	lastSequence      uint64
	completedSequence uint64
	failure           *Failure
	workerDone        chan struct{}
}

func New[T any](capacity int, byteCapacity int64, writer func(Command[T]) error) (*Queue[T], error) {
	if capacity < 1 {
		return nil, ErrInvalidCapacity
	}
	if byteCapacity < 1 {
		return nil, ErrInvalidByteCapacity
	}
	if writer == nil {
		return nil, ErrNilWriter
	}
	q := &Queue[T]{
		changed:      make(chan struct{}),
		reservations: make(map[*Reservation[T]]reservationState),
		capacity:     capacity,
		byteCapacity: byteCapacity,
		writer:       writer,
		accepting:    true,
		workerDone:   make(chan struct{}),
	}
	q.ready = sync.NewCond(&q.mu)
	go q.run()
	return q, nil
}

// Reserve holds n capacity slots and byteSize bytes for commands that will be
// persisted later. Reservations consume capacity immediately but receive no
// sequence numbers.
func (q *Queue[T]) Reserve(n int, byteSize int64) (*Reservation[T], error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failure != nil {
		return nil, q.copyFailureLocked()
	}
	if !q.accepting {
		return nil, ErrClosed
	}
	if n < 1 || byteSize < 1 {
		return nil, ErrInvalidReservation
	}
	if n > q.capacity-len(q.items)-q.reserved {
		return nil, ErrFull
	}
	if byteSize > q.byteCapacity-q.pendingBytes-q.reservedBytes {
		return nil, ErrFull
	}
	reservation := &Reservation[T]{marker: 1}
	q.reservations[reservation] = reservationState{count: n, bytesLeft: byteSize}
	q.reserved += n
	q.reservedBytes += byteSize
	q.notifyChangedLocked()
	return reservation, nil
}

// Enqueue accepts a command whose immutable payload is sizeBytes long. The
// caller supplies the canonical size and must keep it accurate for the
// lifetime of the accepted command; the queue cannot infer the size of T.
func (q *Queue[T]) Enqueue(value T, sizeBytes int64) (uint64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failure != nil {
		return 0, q.copyFailureLocked()
	}
	if !q.accepting {
		return 0, ErrClosed
	}
	if sizeBytes < 1 {
		return 0, ErrInvalidCommandSize
	}
	if len(q.items)+q.reserved >= q.capacity || sizeBytes > q.byteCapacity-q.pendingBytes-q.reservedBytes {
		return 0, ErrFull
	}
	if q.lastSequence == math.MaxUint64 {
		return 0, ErrSequenceExhausted
	}
	sequence := q.lastSequence + 1
	q.items = append(q.items, queuedItem[T]{
		command:    Command[T]{Sequence: sequence, Value: value},
		sizeBytes:  sizeBytes,
		acceptedAt: time.Now(),
	})
	q.pendingBytes += sizeBytes
	q.lastSequence = sequence
	q.ready.Signal()
	q.notifyChangedLocked()
	return sequence, nil
}

// EnqueueReserved adds one command using a slot and byte budget held by
// reservation. The supplied size must match the caller's immutable payload
// accounting and fit within that reservation's remaining byte budget. A valid
// reservation remains usable after Shutdown closes ordinary admission.
func (q *Queue[T]) EnqueueReserved(reservation *Reservation[T], value T, sizeBytes int64) (uint64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if reservation == nil {
		return 0, ErrInvalidReservation
	}
	state, ok := q.reservations[reservation]
	if !ok {
		return 0, ErrInvalidReservation
	}
	if q.failure != nil {
		return 0, q.copyFailureLocked()
	}
	if sizeBytes < 1 {
		return 0, ErrInvalidCommandSize
	}
	if sizeBytes > state.bytesLeft {
		return 0, ErrReservationBytes
	}
	if q.lastSequence == math.MaxUint64 {
		return 0, ErrSequenceExhausted
	}
	sequence := q.lastSequence + 1
	q.items = append(q.items, queuedItem[T]{
		command:    Command[T]{Sequence: sequence, Value: value},
		sizeBytes:  sizeBytes,
		acceptedAt: time.Now(),
	})
	q.pendingBytes += sizeBytes
	q.lastSequence = sequence
	q.reserved--
	if state.count == 1 {
		q.reservedBytes -= state.bytesLeft
		delete(q.reservations, reservation)
	} else {
		state.count--
		state.bytesLeft -= sizeBytes
		q.reservedBytes -= sizeBytes
		q.reservations[reservation] = state
	}
	q.ready.Signal()
	q.notifyChangedLocked()
	return sequence, nil
}

// CancelReservation releases any count slots and byte budget the reservation
// has not used.
func (q *Queue[T]) CancelReservation(reservation *Reservation[T]) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if reservation == nil {
		return ErrInvalidReservation
	}
	state, ok := q.reservations[reservation]
	if !ok {
		return ErrInvalidReservation
	}
	delete(q.reservations, reservation)
	q.reserved -= state.count
	q.reservedBytes -= state.bytesLeft
	q.ready.Broadcast()
	q.notifyChangedLocked()
	return nil
}

// Wait returns when sequence is written, the queue's blocked head fails, or ctx ends.
// Only sequences returned by Enqueue or EnqueueReserved are valid tickets.
func (q *Queue[T]) Wait(ctx context.Context, sequence uint64) error {
	if ctx == nil {
		return ErrNilContext
	}
	for {
		q.mu.Lock()
		if sequence == 0 || sequence > q.lastSequence {
			q.mu.Unlock()
			return fmt.Errorf("%w: %d", ErrUnknownSequence, sequence)
		}
		if sequence <= q.completedSequence {
			q.mu.Unlock()
			return nil
		}
		if q.failure != nil {
			failure := q.copyFailureLocked()
			q.mu.Unlock()
			return failure
		}
		if err := ctx.Err(); err != nil {
			q.mu.Unlock()
			return err
		}
		changed := q.changed
		q.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Shutdown stops new reservations and ordinary admission, then waits for all
// accepted commands and outstanding reservations to resolve or fail. Valid
// reservations can still submit commands while shutdown is draining. ctx bounds
// the caller's wait; it does not cancel an in-progress writer call.
func (q *Queue[T]) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	q.mu.Lock()
	if q.accepting {
		q.accepting = false
		q.ready.Broadcast()
		q.notifyChangedLocked()
	}
	q.mu.Unlock()

	select {
	case <-q.workerDone:
		q.mu.Lock()
		failure := q.copyFailureLocked()
		q.mu.Unlock()
		if failure == nil {
			return nil
		}
		return failure
	default:
	}
	select {
	case <-q.workerDone:
		q.mu.Lock()
		failure := q.copyFailureLocked()
		q.mu.Unlock()
		if failure == nil {
			return nil
		}
		return failure
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *Queue[T]) Status() Status {
	q.mu.Lock()
	defer q.mu.Unlock()
	status := Status{
		Accepting:     q.accepting,
		Pending:       len(q.items),
		Reserved:      q.reserved,
		PendingBytes:  q.pendingBytes,
		ReservedBytes: q.reservedBytes,
		InFlight:      q.inFlight,
		Failure:       q.copyFailureLocked(),
	}
	if len(q.items) > 0 {
		status.OldestSequence = q.items[0].command.Sequence
		status.OldestPendingAt = q.items[0].acceptedAt
	}
	return status
}

func (q *Queue[T]) run() {
	defer close(q.workerDone)
	for {
		q.mu.Lock()
		for len(q.items) == 0 && (q.accepting || q.reserved > 0) {
			q.ready.Wait()
		}
		if q.failure != nil || (len(q.items) == 0 && !q.accepting && q.reserved == 0) {
			q.mu.Unlock()
			return
		}
		item := q.items[0]
		command := item.command
		q.inFlight = true
		q.mu.Unlock()

		err := q.writer(command)

		q.mu.Lock()
		q.inFlight = false
		if err != nil {
			q.failure = &Failure{Sequence: command.Sequence, Err: err}
			q.accepting = false
			q.ready.Broadcast()
			q.notifyChangedLocked()
			q.mu.Unlock()
			return
		}
		q.pendingBytes -= item.sizeBytes
		q.items[0] = queuedItem[T]{}
		q.items = q.items[1:]
		if len(q.items) == 0 {
			q.items = nil
		}
		q.completedSequence = command.Sequence
		q.notifyChangedLocked()
		q.mu.Unlock()
	}
}

func (q *Queue[T]) notifyChangedLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

func (q *Queue[T]) copyFailureLocked() *Failure {
	if q.failure == nil {
		return nil
	}
	failure := *q.failure
	return &failure
}
