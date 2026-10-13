package task

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// rotationRetryAfter is how long a store waits after a rotation that failed
// before it tries again. A failed rotation holds the store's exclusive lock for
// as long as it runs, so it must not be repeated by every transaction.
const rotationRetryAfter = 30 * time.Second

// rotationTimeout bounds the steps of a rotation that wait on the disk or on the
// WAL lock.
const rotationTimeout = 30 * time.Second

// rotationLockWait is how long a write waits for the moment no transaction is in
// flight, which a rotation needs. It never queues for the store's exclusive lock
// (a queued exclusive lock holds off every new reader, and a storage host session
// keeps its read lock for as long as the client takes): it only tries. To make
// that moment come under a steady stream of writes, the writes that arrive while
// a rotation is trying wait at a gate (readers do not); the ones already in flight
// finish. When the store stays busy for the whole wait, the gate opens, this write
// goes on in the current segment and the next attempt is rotationDeferAfter later.
const (
	rotationLockWait   = 250 * time.Millisecond
	rotationDeferAfter = 5 * time.Second
)

// rotationState is the policy and bookkeeping of segment rotation for one writer.
type rotationState struct {
	enabled    bool
	valve      int64
	now        func() time.Time
	failpoint  func(stage string)
	pause      func(time.Duration) // told the stop time of each rotation (tests)
	retryAfter time.Duration
	lockWait   time.Duration
	deferAfter time.Duration
	attempting atomic.Bool                   // one writer at a time tries to rotate
	gate       atomic.Pointer[chan struct{}] // set while a rotation tries: new writes wait on it

	mu          sync.Mutex // guards the fields below
	retryAt     time.Time
	warnedLimit bool
	behindDay   string // the lowest clock day reported while the clock is behind the newest segment; "" when it is not
}

func newRotationState(opts OpenOptions) *rotationState {
	r := &rotationState{enabled: opts.Rotation, valve: opts.RotationSizeValve, now: opts.now, failpoint: opts.failpoint, pause: opts.rotationPause, retryAfter: rotationRetryAfter, lockWait: rotationLockWait, deferAfter: rotationDeferAfter}
	if r.valve <= 0 {
		r.valve = DefaultRotationSizeValve
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r
}

// waitGate holds a write at the gate while a rotation is trying to find the store
// idle.
func (r *rotationState) waitGate(ctx context.Context) {
	if g := r.gate.Load(); g != nil {
		select {
		case <-*g:
		case <-ctx.Done():
		}
	}
}

func (r *rotationState) fire(stage string) {
	if r.failpoint != nil {
		r.failpoint(stage)
	}
}

// backingOff reports whether a recent failure says to leave rotation alone.
func (r *rotationState) backingOff() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.retryAt.IsZero() && time.Now().Before(r.retryAt)
}

func (r *rotationState) failed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryAt = time.Now().Add(r.retryAfter)
}

// deferred records that the store was too busy to rotate: the next attempt is
// rotationDeferAfter from now.
func (r *rotationState) deferred() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryAt = time.Now().Add(r.deferAfter)
}

func (r *rotationState) succeeded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryAt = time.Time{}
}

// observeClock bounds what the log says about a clock that is behind the newest
// segment (spec 5.3, 6.6). The store keeps writing to that segment and never goes
// back to an earlier one, so nothing else shows that the clock is wrong. One
// stretch of the clock being behind produces: a WARN when it is first seen
// (clock_behind); another each time it is seen further back than any day reported
// in the stretch (clock_further_behind); a line that is not a warning when it is
// no longer behind (clock_recovered). A partial recovery that is still behind says
// nothing, and neither does any number of writes in an unchanged state. The state
// is the process's: a restart under a clock that is still behind warns again at
// its first write. It is judged and updated under one lock, so concurrent writers
// log one line between them; the log itself is written outside the lock.
func (r *rotationState) observeClock(segment, segmentDay, clockDay string) {
	var reason string
	r.mu.Lock()
	switch {
	case clockDay >= segmentDay:
		if r.behindDay != "" {
			r.behindDay, reason = "", "clock_recovered"
		}
	case r.behindDay == "":
		r.behindDay, reason = clockDay, "clock_behind"
	case clockDay < r.behindDay:
		r.behindDay, reason = clockDay, "clock_further_behind"
	}
	r.mu.Unlock()
	switch reason {
	case "":
	case "clock_recovered":
		log.Printf("[TaskStore] the clock is no longer behind the newest segment segment=%s segment_day=%s clock_day=%s reason=%s", segment, segmentDay, clockDay, reason)
	default:
		log.Printf("[TaskStore] WARN the clock is behind the newest segment; writes stay in it and never go to an earlier one segment=%s segment_day=%s clock_day=%s reason=%s", segment, segmentDay, clockDay, reason)
	}
}

// rotationTarget says which segment the next write belongs to, when that is not
// the current one: a later UTC day starts that day's first segment; a current
// segment whose files have outgrown the size valve is followed by the next
// segment of its day. The size is that of the six files together, because the
// prefix hash jsonlbatch takes at every append grows with the files the
// transaction appends to. A clock that has gone back never selects an earlier
// segment: the day only ever moves forward from the segment that exists. A clock
// that is behind the segment is logged by observeClock, which judges it on a
// reading of the clock taken after the segment was read: the caller's now may be
// older than a rotation another writer has just made, which would look like a
// clock that is behind.
func (s *JSONLStore) rotationTarget(now time.Time) (name, reason string) {
	ix := s.idx
	ix.mu.RLock()
	cur := ix.segs[ix.cur]
	var size int64
	for _, v := range ix.applied {
		size += v
	}
	ix.mu.RUnlock()
	if info, err := os.Stat(filepath.Join(cur.dir, taskExecutionFenceFilename)); err == nil {
		size += info.Size()
	}
	day, sequence := segmentDay(cur.name), segmentSequence(cur.name)
	s.rot.observeClock(cur.name, day, utcDay(s.rot.now()))
	if today := utcDay(now); today > day {
		return formatSegmentName(today, 1), "day"
	}
	if size > s.rot.valve {
		if sequence >= maxSegmentsPerDay {
			s.rot.mu.Lock()
			warned := s.rot.warnedLimit
			s.rot.warnedLimit = true
			s.rot.mu.Unlock()
			if !warned {
				log.Printf("[TaskStore] WARN segment %s is over the size valve but the day already has %d segments; writes stay in it", cur.name, maxSegmentsPerDay)
			}
			return "", ""
		}
		return formatSegmentName(day, sequence+1), "size"
	}
	return "", ""
}

// rotateIfDue starts the next segment when the next write belongs to one (spec
// 5.3). Every write entry point calls it before it takes the store's read lock:
// the rotation needs the exclusive lock, which waits for the transactions in
// flight and holds off new ones for the few milliseconds it takes. Whether a
// rotation is due is decided again under that lock, so two writers cannot both
// rotate, and a failure leaves the store writing to the segment it has.
func (s *JSONLStore) rotateIfDue(ctx context.Context) {
	r := s.rot
	if r == nil || !r.enabled || s.idx == nil || !s.idx.isLayoutV2() {
		return
	}
	r.waitGate(ctx)
	if r.backingOff() {
		return
	}
	if name, _ := s.rotationTarget(r.now()); name == "" {
		return
	}
	if !r.attempting.CompareAndSwap(false, true) {
		return
	}
	gate := make(chan struct{})
	r.gate.Store(&gate)
	defer func() {
		r.gate.Store(nil)
		close(gate)
		r.attempting.Store(false)
	}()
	if !s.tryLockExclusive(ctx, r.lockWait) {
		r.deferred()
		return
	}
	defer s.mu.Unlock()
	if s.closed || s.batch == nil || s.readOnly {
		return
	}
	name, reason := s.rotationTarget(r.now())
	if name == "" {
		return
	}
	started := time.Now()
	from := s.idx.currentSegmentName()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rotationTimeout)
	defer cancel()
	if err := s.rotateLocked(rctx, name); err != nil {
		r.failed()
		log.Printf("[TaskStore] WARN segment rotation from=%s to=%s reason=%s failed; writes stay in %s and the next attempt is in %s: %v", from, name, reason, from, r.retryAfter, err)
		return
	}
	r.succeeded()
	pause := time.Since(started)
	if r.pause != nil {
		r.pause(pause)
	}
	log.Printf("[TaskStore] segment rotated from=%s to=%s reason=%s pause=%s", from, name, reason, pause.Round(time.Microsecond))
}

// tryLockExclusive takes the store's exclusive lock if it can within wait, without
// ever queueing for it: Go holds off new readers as soon as an exclusive lock is
// waited for, and a reader that stays (a storage host session) would then stop
// every other read with it.
func (s *JSONLStore) tryLockExclusive(ctx context.Context, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if s.mu.TryLock() {
			return true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// rotateLocked closes the current segment and starts name. The caller holds the
// store's exclusive lock, so no transaction is in flight. Nothing that happens
// here changes a byte of a closed segment's data files: the WAL is settled (a
// file of its own), the new directory is created beside it.
//
//	R2 settle the current segment's WAL; make the index absorb whatever the files
//	   hold; check the files are what the index absorbed. Any failure ends the
//	   rotation before anything was created.
//	R3 create the new segment (jsonlbatch.New) and make its directory entry durable.
//	R4 switch: the index follows the new segment, writes go to its Store.
//
// The sidecar that records the closed segment is written after the lock is
// released (the checkpoint goroutine), as a crash before it only means that the
// next start replays the segment from the log.
func (s *JSONLStore) rotateLocked(ctx context.Context, name string) error {
	r := s.rot
	if err := s.batch.Recover(ctx); err != nil {
		return fmt.Errorf("settle the WAL of the current segment: %w", err)
	}
	if err := s.idx.ensureSynced(); err != nil {
		return fmt.Errorf("bring the index up to the current segment: %w", err)
	}
	r.fire("rotate-settled")
	name, err := s.reuseOrphanSegment(name)
	if err != nil {
		return err
	}
	ref := segmentRef{ord: uint16(s.idx.segmentCount()), name: name, dir: segmentDir(s.root, name)}
	if ref.ord == 0 || int(ref.ord) > maxSegmentOrdinal {
		return fmt.Errorf("segment %q cannot be given an ordinal", name)
	}
	next, err := createSegment(s.root, ref)
	if err != nil {
		return err
	}
	r.fire("rotate-created")
	if err := s.installSegment(next, ref); err != nil {
		return err
	}
	r.fire("rotate-switched")
	s.idx.ck.request("rotate")
	return nil
}

// reuseOrphanSegment returns the segment a rotation should start: name, unless an
// earlier rotation created a newer, still empty segment directory and then failed
// to switch to it. That directory is the newest on disk and is what the next open
// would choose, so the switch goes to it (under any other name the ordinals of
// the segments on disk and in memory would disagree after a restart). A newer
// directory that holds data is not an orphan: the store is in a state this build
// does not understand.
func (s *JSONLStore) reuseOrphanSegment(name string) (string, error) {
	names, err := listSegmentDirs(s.root)
	if err != nil {
		return "", err
	}
	current := s.idx.currentSegmentName()
	if len(names) == 0 || names[len(names)-1] <= current {
		return name, nil
	}
	newest := names[len(names)-1]
	empty, err := segmentHoldsNoData(segmentDir(s.root, newest))
	if err != nil {
		return "", err
	}
	if !empty {
		return "", fmt.Errorf("segment %s is newer than the current segment %s and holds data", newest, current)
	}
	return newest, nil
}

// createSegment creates the directory of ref with its six files, WAL and lock,
// and makes the entries durable.
func createSegment(root string, ref segmentRef) (*jsonlbatch.Store, error) {
	store, err := jsonlbatch.New(ref.dir, taskBatchFilenames())
	if err != nil {
		return nil, fmt.Errorf("create segment %s: %w", ref.name, err)
	}
	if err := jsonlbatch.SyncDirectory(logsDir(root)); err != nil {
		return nil, fmt.Errorf("sync the segment directory: %w", err)
	}
	if err := jsonlbatch.SyncDirectory(root); err != nil {
		return nil, fmt.Errorf("sync the task store directory: %w", err)
	}
	return store, nil
}

// installSegment is the switch itself: the index closes the segment it follows
// and follows ref, and the store appends through next from now on. The caller
// holds the store's exclusive lock (or the store is not shared yet).
func (s *JSONLStore) installSegment(next *jsonlbatch.Store, ref segmentRef) error {
	if err := s.idx.closeCurrentSegment(ref); err != nil {
		return fmt.Errorf("close the current segment in the index: %w", err)
	}
	next.SetTxObserver(s.txObserver.Observe)
	next.SetPostCommitHook(s.idx.onCommitFor(ref.ord))
	s.batch.SetPostCommitHook(nil)
	s.batch = next
	s.statePath = filepath.Join(ref.dir, stateFilename)
	s.runPath = filepath.Join(ref.dir, runFilename)
	s.contextPath = filepath.Join(ref.dir, contextFilename)
	s.notificationsPath = filepath.Join(ref.dir, notificationsFilename)
	s.txHot.set(s.statePath, s.runPath)
	return nil
}

// migrationAborted says layout v2 was not started and nothing durable was
// changed that matters: the store goes on as layout v1.
type migrationAborted struct{ cause error }

func (e *migrationAborted) Error() string { return "segment migration aborted: " + e.cause.Error() }
func (e *migrationAborted) Unwrap() error { return e.cause }

// migrateToSegments moves a store from layout v1 to layout v2 (spec 10.3): the
// files in the root become segment 0, frozen, and the first segment is created.
// It runs at open, before the store is shared.
//
//	M1 settle segment 0 and make the index absorb everything it holds; measure it.
//	M2 write the sidecar of segment 0 as it is now (seg-legacy.tsi). It is not
//	   written again; a return to layout v1 can start from it.
//	M3 create the first segment (an empty one left by an earlier attempt is reused).
//	M4 write LAYOUT with the sizes M1 measured. From here on the store is layout v2.
//	M5 make segment 0 read-only.
//
// Until M4 completes a failure leaves a store that is still layout v1 and the
// open goes on as such (migrationAborted). After it, the failure is the open's:
// the next open finds LAYOUT, checks segment 0 against it, freezes it again if
// M5 did not finish, and writes to the first segment.
func (s *JSONLStore) migrateToSegments(ctx context.Context) error {
	r := s.rot
	abort := func(err error) error { return &migrationAborted{cause: err} }
	s.mu.Lock()
	defer s.mu.Unlock()

	// M1
	if err := s.batch.Recover(ctx); err != nil {
		return abort(fmt.Errorf("settle segment 0: %w", err))
	}
	if err := s.idx.ensureSynced(); err != nil {
		return abort(fmt.Errorf("bring the index up to segment 0: %w", err))
	}
	sizes, err := segmentFileSizes(s.root)
	if err != nil {
		return abort(fmt.Errorf("measure segment 0: %w", err))
	}
	if err := s.idx.requireAppliedEquals(sizes); err != nil {
		return abort(err)
	}
	r.fire("migrate-M1")

	// M2
	if err := s.idx.ck.checkpointNow("migrate"); err != nil {
		return abort(fmt.Errorf("write the sidecar of segment 0: %w", err))
	}
	r.fire("migrate-M2")

	// M3
	ref, err := firstSegment(s.root, r.now())
	if err != nil {
		return abort(err)
	}
	next, err := createSegment(s.root, ref)
	if err != nil {
		return abort(err)
	}
	r.fire("migrate-M3")

	// M4: the point of no return.
	installed, err := writeLayoutReport(s.root, sizes, r.failpoint)
	if err != nil {
		if !installed {
			return abort(err)
		}
		return fmt.Errorf("LAYOUT was installed but could not be made durable: %w", err)
	}
	r.fire("migrate-M4")

	// M5
	if err := freezeSegmentZero(s.root); err != nil {
		log.Printf("[TaskStore] WARN segment 0 could not be made read-only: %v", err)
	}
	r.fire("migrate-M5")

	s.idx.enterLayoutV2(sizes)
	if err := s.installSegment(next, ref); err != nil {
		return err
	}
	if err := s.idx.ck.checkpointNow("migrated"); err != nil {
		// Derived data: the next start rebuilds it from the log.
		log.Printf("[TaskStore] WARN the sidecar of layout v2 could not be written: %v", err)
	}
	log.Printf("[TaskStore] segment migration done: segment 0 frozen at %d bytes, first segment %s", sumSizes(sizes), ref.name)
	return nil
}

func sumSizes(sizes [sizedFileCount]int64) int64 {
	var total int64
	for _, v := range sizes {
		total += v
	}
	return total
}

// firstSegment chooses the segment the migration creates: the one an earlier,
// interrupted attempt left (an empty directory under logs/), else today's first.
func firstSegment(root string, now time.Time) (segmentRef, error) {
	names, err := listSegmentDirs(root)
	if err != nil {
		return segmentRef{}, err
	}
	switch len(names) {
	case 0:
		name := formatSegmentName(utcDay(now), 1)
		return segmentRef{ord: 1, name: name, dir: segmentDir(root, name)}, nil
	case 1:
		if empty, err := segmentHoldsNoData(segmentDir(root, names[0])); err != nil {
			return segmentRef{}, err
		} else if empty {
			return segmentRef{ord: 1, name: names[0], dir: segmentDir(root, names[0])}, nil
		}
	}
	return segmentRef{}, fmt.Errorf("logs/ holds %d segment directories but the store has no LAYOUT", len(names))
}

// requireAppliedEquals checks that the index has absorbed exactly the files of
// the current segment: what a rotation or a migration may close.
func (ix *taskIndex) requireAppliedEquals(sizes [sizedFileCount]int64) error {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for kind := fileKind(0); kind < kindCount; kind++ {
		if sizes[kind] != ix.applied[kind] {
			return fmt.Errorf("%w: %s is %d bytes, the index absorbed %d", ErrIndexInconsistent, kindFilename[kind], sizes[kind], ix.applied[kind])
		}
	}
	return nil
}

// enterLayoutV2 records that the store is on layout v2 now, with segment 0 frozen
// at the given sizes.
func (ix *taskIndex) enterLayoutV2(frozen [sizedFileCount]int64) {
	ix.applyMu.Lock()
	defer ix.applyMu.Unlock()
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.layoutV2 = true
	ix.segs[0].size = frozen
}

func (ix *taskIndex) segmentCount() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.segs)
}

func (ix *taskIndex) currentSegmentName() string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.segs[ix.cur].name
}

var _ = errors.Is
