package persistencequeue

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestBlockedWriterDoesNotBlockEnqueueAndCapacityIncludesInFlight(t *testing.T) {
	started := make(chan Command[int], 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	q, err := New(2, 1024, func(command Command[int]) error {
		started <- command
		if command.Value == 1 {
			<-release
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	if sequence, err := q.Enqueue(1, 1); err != nil || sequence != 1 {
		t.Fatalf("first enqueue = (%d, %v), want (1, nil)", sequence, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}

	type enqueueResult struct {
		sequence uint64
		err      error
	}
	enqueued := make(chan enqueueResult, 1)
	go func() {
		sequence, err := q.Enqueue(2, 1)
		enqueued <- enqueueResult{sequence: sequence, err: err}
	}()
	select {
	case result := <-enqueued:
		if result.err != nil || result.sequence != 2 {
			t.Fatalf("second enqueue = (%d, %v), want (2, nil)", result.sequence, result.err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("enqueue waited for the blocked writer")
	}

	full := make(chan error, 1)
	go func() {
		_, err := q.Enqueue(3, 1)
		full <- err
	}()
	select {
	case err := <-full:
		if !errors.Is(err, ErrFull) {
			t.Fatalf("enqueue at capacity error = %v, want ErrFull", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("enqueue at capacity waited instead of returning immediately")
	}

	releaseOnce.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestQueueWritesCommandsInSequenceOrder(t *testing.T) {
	written := make(chan Command[string], 3)
	q, err := New(3, 1024, func(command Command[string]) error {
		written <- command
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range []string{"first", "second", "third"} {
		sequence, err := q.Enqueue(value, 1)
		if err != nil {
			t.Fatalf("enqueue %q: %v", value, err)
		}
		if want := uint64(index + 1); sequence != want {
			t.Fatalf("enqueue %q sequence = %d, want %d", value, sequence, want)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := q.Wait(ctx, 1); err != nil {
		t.Fatalf("wait for first persisted command: %v", err)
	}
	close(written)
	var got []Command[string]
	for command := range written {
		got = append(got, command)
	}
	want := []Command[string]{{Sequence: 1, Value: "first"}, {Sequence: 2, Value: "second"}, {Sequence: 3, Value: "third"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("written commands = %#v, want %#v", got, want)
	}
}

func TestWriterFailureRetainsHeadAndBlocksLaterCommands(t *testing.T) {
	started := make(chan Command[int], 1)
	finishWriter := make(chan struct{})
	writeFailure := errors.New("disk unavailable")
	written := make(chan Command[int], 3)
	q, err := New(3, 1024, func(command Command[int]) error {
		written <- command
		if command.Value == 1 {
			started <- command
			<-finishWriter
			return writeFailure
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-finishWriter:
		default:
			close(finishWriter)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	if _, err := q.Enqueue(1, 4); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	if sequence, err := q.Enqueue(2, 5); err != nil || sequence != 2 {
		t.Fatalf("second enqueue = (%d, %v), want (2, nil)", sequence, err)
	}
	close(finishWriter)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); !errors.Is(err, ErrFailed) || !errors.Is(err, writeFailure) {
		t.Fatalf("shutdown error = %v, want ErrFailed wrapping writer error", err)
	}
	status := q.Status()
	if status.Accepting {
		t.Fatal("queue still accepts commands after writer failure")
	}
	if status.Pending != 2 || status.PendingBytes != 9 || status.OldestSequence != 1 || status.OldestPendingAt.IsZero() || status.InFlight {
		t.Fatalf("status after failure = %+v, want 2 pending commands / 9 bytes from sequence 1 and no in-flight write", status)
	}
	if status.Failure == nil || status.Failure.Sequence != 1 || !errors.Is(status.Failure, writeFailure) {
		t.Fatalf("failure status = %+v, want sequence 1 and writer error", status.Failure)
	}
	for _, sequence := range []uint64{1, 2} {
		if err := q.Wait(ctx, sequence); !errors.Is(err, ErrFailed) || !errors.Is(err, writeFailure) {
			t.Errorf("wait for sequence %d = %v, want blocked-head failure", sequence, err)
		}
	}
	if _, err := q.Enqueue(3, 1); !errors.Is(err, ErrFailed) {
		t.Fatalf("enqueue after failure = %v, want ErrFailed", err)
	}
	var writtenCommands []Command[int]
	for {
		select {
		case command := <-written:
			writtenCommands = append(writtenCommands, command)
		default:
			if want := []Command[int]{{Sequence: 1, Value: 1}}; !reflect.DeepEqual(writtenCommands, want) {
				t.Fatalf("writer calls = %#v, want only failed head %#v", writtenCommands, want)
			}
			return
		}
	}
}

func TestWaitForCompletedSequenceSurvivesLaterFailure(t *testing.T) {
	writeFailure := errors.New("second write failed")
	q, err := New(2, 1024, func(command Command[int]) error {
		if command.Value == 2 {
			return writeFailure
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(2, 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); !errors.Is(err, ErrFailed) {
		t.Fatalf("shutdown error = %v, want ErrFailed", err)
	}
	if err := q.Wait(ctx, 1); err != nil {
		t.Fatalf("wait for completed first sequence = %v, want nil", err)
	}
	if err := q.Wait(ctx, 2); !errors.Is(err, writeFailure) {
		t.Fatalf("wait for failed second sequence = %v, want writer error", err)
	}
}

func TestShutdownStopsAdmissionAndPerformsBoundedGracefulDrain(t *testing.T) {
	started := make(chan Command[int], 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	written := make(chan Command[int], 2)
	q, err := New(2, 1024, func(command Command[int]) error {
		written <- command
		if command.Value == 1 {
			started <- command
			<-release
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	if _, err := q.Enqueue(1, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	if _, err := q.Enqueue(2, 1); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = q.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded shutdown error = %v, want context deadline", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
	if err := q.Wait(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait for blocked command = %v, want context deadline", err)
	}
	cancel()
	if _, err := q.Enqueue(3, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("enqueue after shutdown started = %v, want ErrClosed", err)
	}
	status := q.Status()
	if status.Accepting || status.Pending != 2 || status.OldestSequence != 1 {
		t.Fatalf("status during bounded drain = %+v, want closed with 2 pending from sequence 1", status)
	}

	releaseOnce.Do(func() { close(release) })
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown after writer resumed: %v", err)
	}
	if err := q.Wait(ctx, 2); err != nil {
		t.Fatalf("wait for second persisted command: %v", err)
	}
	close(written)
	var values []int
	for command := range written {
		values = append(values, command.Value)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(values, want) {
		t.Fatalf("written values = %v, want %v", values, want)
	}
}

func TestReservationUsesCapacityAndCancellationReleasesIt(t *testing.T) {
	q, err := New(1, 1024, func(Command[int]) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	reservation, err := q.Reserve(1, 1)
	if err != nil {
		t.Fatalf("reserve one slot: %v", err)
	}
	if status := q.Status(); status.Reserved != 1 {
		t.Fatalf("status after reserve = %+v, want one reserved slot", status)
	}
	if _, err := q.Reserve(1, 1); !errors.Is(err, ErrFull) {
		t.Fatalf("reserve at capacity = %v, want ErrFull", err)
	}
	if _, err := q.Enqueue(1, 1); !errors.Is(err, ErrFull) {
		t.Fatalf("normal enqueue into reserved capacity = %v, want ErrFull", err)
	}
	if err := q.CancelReservation(reservation); err != nil {
		t.Fatalf("cancel reservation: %v", err)
	}
	if status := q.Status(); status.Reserved != 0 {
		t.Fatalf("status after cancellation = %+v, want no reserved slots", status)
	}
	if sequence, err := q.Enqueue(1, 1); err != nil || sequence != 1 {
		t.Fatalf("enqueue after cancellation = (%d, %v), want (1, nil)", sequence, err)
	}
	if err := q.CancelReservation(reservation); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("cancel reservation twice = %v, want ErrInvalidReservation", err)
	}
	if _, err := q.EnqueueReserved(reservation, 2, 1); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("enqueue with canceled reservation = %v, want ErrInvalidReservation", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestReservedEnqueueSucceedsWhenNormalCapacityIsFull(t *testing.T) {
	started := make(chan Command[int], 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	written := make(chan Command[int], 2)
	q, err := New(2, 1024, func(command Command[int]) error {
		written <- command
		if command.Value == 1 {
			started <- command
			<-release
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	reservation, err := q.Reserve(1, 1)
	if err != nil {
		t.Fatalf("reserve one slot: %v", err)
	}
	if sequence, err := q.Enqueue(1, 1); err != nil || sequence != 1 {
		t.Fatalf("normal enqueue = (%d, %v), want (1, nil)", sequence, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	if _, err := q.Enqueue(3, 1); !errors.Is(err, ErrFull) {
		t.Fatalf("normal enqueue at reserved capacity = %v, want ErrFull", err)
	}
	if sequence, err := q.EnqueueReserved(reservation, 2, 1); err != nil || sequence != 2 {
		t.Fatalf("reserved enqueue = (%d, %v), want (2, nil)", sequence, err)
	}
	if status := q.Status(); status.Reserved != 0 || status.Pending != 2 || !status.InFlight {
		t.Fatalf("status after reserved enqueue = %+v, want two pending and no remaining reservation", status)
	}

	releaseOnce.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	close(written)
	var got []Command[int]
	for command := range written {
		got = append(got, command)
	}
	want := []Command[int]{{Sequence: 1, Value: 1}, {Sequence: 2, Value: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("written commands = %#v, want %#v", got, want)
	}
}

func TestReservationsAreQueueBoundAndSequenceInterleavedEnqueues(t *testing.T) {
	written := make(chan Command[int], 4)
	q, err := New(4, 1024, func(command Command[int]) error {
		written <- command
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(1, 1024, func(Command[int]) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var firstTurn, secondTurn *Reservation[int]
	t.Cleanup(func() {
		if firstTurn != nil {
			_ = q.CancelReservation(firstTurn)
		}
		if secondTurn != nil {
			_ = q.CancelReservation(secondTurn)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
		_ = other.Shutdown(ctx)
	})

	firstTurn, err = q.Reserve(2, 2)
	if err != nil {
		t.Fatalf("reserve first turn: %v", err)
	}
	secondTurn, err = q.Reserve(2, 2)
	if err != nil {
		t.Fatalf("reserve second turn: %v", err)
	}
	if _, err := other.EnqueueReserved(firstTurn, 99, 1); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("cross-queue reserved enqueue = %v, want ErrInvalidReservation", err)
	}
	if _, err := q.EnqueueReserved(&Reservation[int]{}, 99, 1); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("forged reserved enqueue = %v, want ErrInvalidReservation", err)
	}

	for index, item := range []struct {
		reservation *Reservation[int]
		value       int
	}{{firstTurn, 10}, {secondTurn, 20}, {firstTurn, 11}, {secondTurn, 21}} {
		sequence, err := q.EnqueueReserved(item.reservation, item.value, 1)
		if err != nil {
			t.Fatalf("interleaved enqueue %d: %v", index, err)
		}
		if want := uint64(index + 1); sequence != want {
			t.Fatalf("interleaved enqueue %d sequence = %d, want %d", index, sequence, want)
		}
	}
	if _, err := q.EnqueueReserved(firstTurn, 12, 1); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("reserved enqueue after exhaustion = %v, want ErrInvalidReservation", err)
	}
	if err := q.CancelReservation(secondTurn); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("cancel exhausted reservation = %v, want ErrInvalidReservation", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown queue: %v", err)
	}
	if err := other.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown other queue: %v", err)
	}
	close(written)
	var got []Command[int]
	for command := range written {
		got = append(got, command)
	}
	want := []Command[int]{{Sequence: 1, Value: 10}, {Sequence: 2, Value: 20}, {Sequence: 3, Value: 11}, {Sequence: 4, Value: 21}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("written commands = %#v, want %#v", got, want)
	}
}

func TestShutdownWaitsForReservationToBeSubmittedOrCanceled(t *testing.T) {
	written := make(chan Command[int], 1)
	q, err := New(2, 1024, func(command Command[int]) error {
		written <- command
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	reservation, err := q.Reserve(2, 2)
	if err != nil {
		t.Fatalf("reserve two slots: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = q.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown with unresolved reservation = %v, want context deadline", err)
	}
	if _, err := q.Enqueue(7, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("normal enqueue after shutdown = %v, want ErrClosed", err)
	}
	if status := q.Status(); status.Accepting || status.Reserved != 2 {
		t.Fatalf("status with unresolved reservation = %+v, want closed with two reserved slots", status)
	}
	if sequence, err := q.EnqueueReserved(reservation, 42, 1); err != nil || sequence != 1 {
		t.Fatalf("reserved enqueue after shutdown = (%d, %v), want (1, nil)", sequence, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = q.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown with remaining reserved slot = %v, want context deadline", err)
	}
	if err := q.CancelReservation(reservation); err != nil {
		t.Fatalf("cancel unused reserved slot: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown after reservation resolved: %v", err)
	}
	if err := q.Wait(ctx, 1); err != nil {
		t.Fatalf("wait for reserved command: %v", err)
	}
	if command := <-written; command != (Command[int]{Sequence: 1, Value: 42}) {
		t.Fatalf("written command = %+v, want reserved value 42 at sequence 1", command)
	}
}

func TestByteCapacityIncludesInFlightAndReleasesAfterWrite(t *testing.T) {
	firstStarted := make(chan struct{}, 1)
	secondStarted := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	var releaseFirstOnce, releaseSecondOnce sync.Once
	q, err := New(3, 10, func(command Command[int]) error {
		switch command.Sequence {
		case 1:
			firstStarted <- struct{}{}
			<-releaseFirst
		case 2:
			secondStarted <- struct{}{}
			<-releaseSecond
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
		releaseSecondOnce.Do(func() { close(releaseSecond) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	if _, err := q.Enqueue(1, 6); err != nil {
		t.Fatalf("enqueue first command: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first command did not reach writer")
	}
	if _, err := q.Enqueue(2, 4); err != nil {
		t.Fatalf("enqueue second command at byte capacity: %v", err)
	}
	status := q.Status()
	if status.PendingBytes != 10 || status.Pending != 2 || status.OldestSequence != 1 || status.OldestPendingAt.IsZero() {
		t.Fatalf("status at byte capacity = %+v, want 2 pending / 10 bytes from sequence 1", status)
	}
	if _, err := q.Enqueue(3, 1); !errors.Is(err, ErrFull) {
		t.Fatalf("enqueue beyond byte capacity = %v, want ErrFull", err)
	}

	releaseFirstOnce.Do(func() { close(releaseFirst) })
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second command did not reach writer after first completed")
	}
	status = q.Status()
	if status.PendingBytes != 4 || status.Pending != 1 || status.OldestSequence != 2 || status.OldestPendingAt.IsZero() {
		t.Fatalf("status after first write = %+v, want 1 pending / 4 bytes from sequence 2", status)
	}
	releaseSecondOnce.Do(func() { close(releaseSecond) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown after writes: %v", err)
	}
	status = q.Status()
	if status.PendingBytes != 0 || !status.OldestPendingAt.IsZero() {
		t.Fatalf("status after drain = %+v, want no pending bytes or timestamp", status)
	}
}

func TestReservationByteBudgetAndCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	written := make(chan Command[int], 3)
	q, err := New(4, 20, func(command Command[int]) error {
		written <- command
		if command.Sequence == 1 {
			started <- struct{}{}
			<-release
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	reservation, err := q.Reserve(2, 14)
	if err != nil {
		t.Fatalf("reserve two commands / 14 bytes: %v", err)
	}
	if status := q.Status(); status.Reserved != 2 || status.ReservedBytes != 14 {
		t.Fatalf("status after reserve = %+v, want 2 reserved / 14 bytes", status)
	}
	if _, err := q.Enqueue(1, 6); err != nil {
		t.Fatalf("enqueue ordinary command beside reservation: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ordinary command did not reach writer")
	}
	if _, err := q.EnqueueReserved(reservation, 2, 8); err != nil {
		t.Fatalf("enqueue reserved command: %v", err)
	}
	status := q.Status()
	if status.PendingBytes != 14 || status.Reserved != 1 || status.ReservedBytes != 6 {
		t.Fatalf("status after reserved enqueue = %+v, want 14 pending / 6 reserved bytes", status)
	}
	if _, err := q.EnqueueReserved(reservation, 3, 7); !errors.Is(err, ErrReservationBytes) {
		t.Fatalf("reserved enqueue over byte budget = %v, want ErrReservationBytes", err)
	}
	if status := q.Status(); status.PendingBytes != 14 || status.Reserved != 1 || status.ReservedBytes != 6 {
		t.Fatalf("status after rejected reserved enqueue = %+v, want accounting unchanged", status)
	}
	if err := q.CancelReservation(reservation); err != nil {
		t.Fatalf("cancel partial reservation: %v", err)
	}
	status = q.Status()
	if status.PendingBytes != 14 || status.Reserved != 0 || status.ReservedBytes != 0 {
		t.Fatalf("status after cancel = %+v, want 14 pending / no reserved capacity", status)
	}
	if sequence, err := q.Enqueue(4, 6); err != nil || sequence != 3 {
		t.Fatalf("enqueue after cancel = (%d, %v), want sequence 3 and released byte capacity", sequence, err)
	}

	releaseOnce.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	want := []Command[int]{{Sequence: 1, Value: 1}, {Sequence: 2, Value: 2}, {Sequence: 3, Value: 4}}
	var got []Command[int]
	for range want {
		got = append(got, <-written)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("written commands = %#v, want %#v", got, want)
	}
}

func TestFinalReservedCommandReleasesUnusedReservedBytes(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	q, err := New(2, 10, func(command Command[int]) error {
		if command.Sequence == 1 {
			started <- struct{}{}
			<-release
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	reservation, err := q.Reserve(1, 10)
	if err != nil {
		t.Fatalf("reserve one command / 10 bytes: %v", err)
	}
	if _, err := q.EnqueueReserved(reservation, 1, 4); err != nil {
		t.Fatalf("enqueue 4-byte reserved command: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reserved command did not reach writer")
	}
	status := q.Status()
	if status.PendingBytes != 4 || status.Reserved != 0 || status.ReservedBytes != 0 {
		t.Fatalf("status after exhausting count reservation = %+v, want 4 pending / no reserved bytes", status)
	}
	if _, err := q.Enqueue(2, 6); err != nil {
		t.Fatalf("enqueue using released unused reservation bytes: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestByteCapacityOverflowAndInvalidSizes(t *testing.T) {
	if _, err := New[int](1, 0, func(Command[int]) error { return nil }); !errors.Is(err, ErrInvalidByteCapacity) {
		t.Fatalf("zero byte capacity constructor error = %v, want ErrInvalidByteCapacity", err)
	}
	if _, err := New[int](1, -1, func(Command[int]) error { return nil }); !errors.Is(err, ErrInvalidByteCapacity) {
		t.Fatalf("negative byte capacity constructor error = %v, want ErrInvalidByteCapacity", err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	q, err := New(2, int64(1<<63-1), func(Command[int]) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = q.Shutdown(ctx)
	})

	if _, err := q.Enqueue(1, 0); !errors.Is(err, ErrInvalidCommandSize) {
		t.Fatalf("zero command size = %v, want ErrInvalidCommandSize", err)
	}
	if _, err := q.Enqueue(1, -1); !errors.Is(err, ErrInvalidCommandSize) {
		t.Fatalf("negative command size = %v, want ErrInvalidCommandSize", err)
	}
	maxBytes := int64(1<<63 - 1)
	if _, err := q.Enqueue(1, maxBytes); err != nil {
		t.Fatalf("enqueue max-sized command: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("max-sized command did not reach writer")
	}
	if _, err := q.Enqueue(2, 1); !errors.Is(err, ErrFull) {
		t.Fatalf("enqueue after max byte reservation = %v, want ErrFull", err)
	}
	if status := q.Status(); status.PendingBytes != maxBytes {
		t.Fatalf("status with max-sized command = %+v, want pending bytes %d", status, maxBytes)
	}
}
