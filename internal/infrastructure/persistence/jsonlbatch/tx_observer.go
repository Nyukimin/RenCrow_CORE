package jsonlbatch

import (
	"context"
	"time"
)

// TxKind distinguishes the two lock-holding operations that carry a callback.
type TxKind string

const (
	// TxKindRead is a Read call.
	TxKindRead TxKind = "read"
	// TxKindWrite is a Write call.
	TxKindWrite TxKind = "write"
)

// TxObservation is the timing of one Read or Write call. The four durations
// partition the call, in order, so their sum is the time the caller was blocked
// inside the Store (apart from the final unlock):
//
//	LockWait  the call until the OS lock was acquired (or until it gave up)
//	Settle    under the lock, before the callback: WAL recovery and checkpoint for
//	          Write, the committed-journal check for Read
//	Exec      the caller's callback (for the task store this includes decoding
//	          every record)
//	Commit    Write only, after the callback: payload validation, prefix and
//	          payload SHA-256, append, fsync and the commit record
type TxObservation struct {
	Kind     TxKind
	LockWait time.Duration
	Settle   time.Duration
	Exec     time.Duration
	Commit   time.Duration
	// Acquired is false when the call ended before it held the lock (context
	// cancelled or deadline exceeded while waiting, or a lock error). Settle,
	// Exec and Commit are zero in that case.
	Acquired bool
	// Files lists the sorted names of the files a Write appended to. It is empty
	// for Read, for a Write whose callback failed or returned no payload.
	Files []string
	// Err is the error the call returned, nil on success.
	Err error
}

// TxObserver receives one TxObservation for every Read and Write call. It is
// invoked synchronously on the calling goroutine after the OS lock has been
// released, so its own cost never extends the lock hold time. It runs
// concurrently for concurrent callers and must not block for long. ctx is the
// context the caller passed to Read or Write (context.Background when it passed
// nil). Recover is not a transaction and is never observed.
type TxObserver func(ctx context.Context, tx TxObservation)

// SetTxObserver installs observer, or removes it when observer is nil. It is
// safe to call while other goroutines use the Store. A Store without an observer
// does no timing work at all.
func (s *Store) SetTxObserver(observer TxObserver) {
	if observer == nil {
		s.observer.Store(nil)
		return
	}
	s.observer.Store(&observer)
}

// txRecorder collects the phase boundaries of one call. A nil recorder is the
// "no observer" case: every method is a no-op that takes no timestamp.
type txRecorder struct {
	observer  TxObserver
	kind      TxKind
	start     time.Time
	locked    time.Time
	execStart time.Time
	execEnd   time.Time
	held      time.Time
	files     []string
}

func (s *Store) beginTx(kind TxKind) *txRecorder {
	observer := s.observer.Load()
	if observer == nil {
		return nil
	}
	return &txRecorder{observer: *observer, kind: kind, start: time.Now()}
}

func (r *txRecorder) lockAcquired() {
	if r != nil {
		r.locked = time.Now()
	}
}

func (r *txRecorder) execBegin() {
	if r != nil {
		r.execStart = time.Now()
	}
}

func (r *txRecorder) execFinished() {
	if r != nil {
		r.execEnd = time.Now()
	}
}

// lockedSectionReturned marks the moment the locked section finished, before
// the unlock, so unlock and close are not attributed to Commit.
func (r *txRecorder) lockedSectionReturned() {
	if r != nil {
		r.held = time.Now()
	}
}

func (r *txRecorder) setFiles(prepared []preparedFile) {
	if r == nil || len(prepared) == 0 {
		return
	}
	r.files = make([]string, len(prepared))
	for i := range prepared {
		r.files[i] = prepared[i].journal.Name
	}
}

// report builds the observation and delivers it. The caller must have released
// the OS lock already.
func (r *txRecorder) report(ctx context.Context, err error) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.observer(ctx, r.observation(time.Now(), err))
}

func (r *txRecorder) observation(end time.Time, err error) TxObservation {
	obs := TxObservation{Kind: r.kind, Files: r.files, Err: err}
	if r.locked.IsZero() {
		obs.LockWait = end.Sub(r.start)
		return obs
	}
	obs.Acquired = true
	obs.LockWait = r.locked.Sub(r.start)
	held := r.held
	if held.IsZero() {
		held = end
	}
	if r.execStart.IsZero() {
		obs.Settle = held.Sub(r.locked)
		return obs
	}
	obs.Settle = r.execStart.Sub(r.locked)
	execEnd := r.execEnd
	if execEnd.IsZero() {
		execEnd = held
	}
	obs.Exec = execEnd.Sub(r.execStart)
	if r.kind == TxKindWrite {
		obs.Commit = held.Sub(execEnd)
	}
	return obs
}
