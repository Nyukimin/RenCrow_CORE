package action

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/segmentname"
)

// rotationRetryAfter is how long a store waits after a rotation that failed before
// it tries again. A rotation holds the store's write lock for as long as it runs,
// so a failing one must not be repeated by every transaction.
const rotationRetryAfter = 30 * time.Second

// rotationTimeout bounds the steps of a rotation that wait on the disk or on the
// WAL lock.
const rotationTimeout = 30 * time.Second

// rotationState is the policy and bookkeeping of segment rotation for one writer.
type rotationState struct {
	enabled    bool
	valve      int64
	now        func() time.Time
	failpoint  func(stage string)
	pause      func(time.Duration) // told the stop time of each rotation (tests)
	retryAfter time.Duration

	// incomplete is true while the current segment is sealed and the segment that
	// follows it has not been made. It is read and written with JSONLStore.mu held
	// for writing.
	incomplete bool

	mu          sync.Mutex // guards the fields below
	retryAt     time.Time
	warnedLimit bool
	behindDay   string // the lowest clock day reported while the clock is behind the newest segment; "" when it is not
}

func newRotationState(opts OpenOptions) *rotationState {
	r := &rotationState{enabled: opts.Rotation, valve: opts.RotationSizeValve, now: opts.now, failpoint: opts.failpoint, pause: opts.rotationPause, retryAfter: rotationRetryAfter}
	if r.valve <= 0 {
		r.valve = DefaultRotationSizeValve
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r
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

func (r *rotationState) succeeded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryAt = time.Time{}
}

// observeClock bounds what the log says about a clock that is behind the newest
// segment. The store keeps writing to that segment and never goes back to an
// earlier one, so nothing else shows that the clock is wrong. One stretch of the
// clock being behind produces: a WARN when it is first seen (clock_behind);
// another each time it is seen further back than any day reported in the stretch
// (clock_further_behind); a line that is not a warning when it is no longer
// behind (clock_recovered). A partial recovery that is still behind says nothing,
// and neither does any number of writes in an unchanged state. The state is the
// process's: a restart under a clock that is still behind warns again at its
// first write. It is judged and updated under one lock, so concurrent writers log
// one line between them; the log itself is written outside the lock.
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
		log.Printf("[ActionStore] the clock is no longer behind the newest segment segment=%s segment_day=%s clock_day=%s reason=%s", segment, segmentDay, clockDay, reason)
	default:
		log.Printf("[ActionStore] WARN the clock is behind the newest segment; writes stay in it and never go to an earlier one segment=%s segment_day=%s clock_day=%s reason=%s", segment, segmentDay, clockDay, reason)
	}
}

// nextSegmentName is the name of the segment that follows the one named cur: the
// first segment of today when today is a later UTC day, else the next of cur's
// day. It never names an earlier day than cur, whatever the clock says.
func nextSegmentName(cur string, now time.Time) (string, error) {
	day, sequence := segmentname.Day(cur), segmentname.Sequence(cur)
	if today := segmentname.UTCDay(now); today > day {
		return segmentname.Format(today, 1), nil
	}
	if sequence >= maxSegmentsPerDay {
		return "", fmt.Errorf("segment %s is the last of its day (%d segments)", cur, maxSegmentsPerDay)
	}
	return segmentname.Format(day, sequence+1), nil
}

// rotationTarget says which segment the next write belongs to, when that is not
// the current one: a later UTC day starts that day's first segment; a current
// segment whose logs have outgrown the size valve is followed by the next segment
// of its day. The size is that of the two logs together, because the prefix hash
// jsonlbatch takes at every append grows with the files the transaction appends
// to. A clock that has gone back never selects an earlier segment: the day only
// ever moves forward from the segment that exists, and a clock that is behind it
// is logged by observeClock. It uses the bytes the snapshot has decoded, so it
// costs no system call.
func (s *JSONLStore) rotationTarget(now time.Time) (name, reason string) {
	cur := s.idx.current()
	if !segmentname.IsName(cur.name) {
		return "", ""
	}
	size := s.idx.currentBytes()
	day, sequence := segmentname.Day(cur.name), segmentname.Sequence(cur.name)
	s.rot.observeClock(cur.name, day, segmentname.UTCDay(s.rot.now()))
	if today := segmentname.UTCDay(now); today > day {
		return segmentname.Format(today, 1), "day"
	}
	if size > s.rot.valve {
		if sequence >= maxSegmentsPerDay {
			s.rot.mu.Lock()
			warned := s.rot.warnedLimit
			s.rot.warnedLimit = true
			s.rot.mu.Unlock()
			if !warned {
				log.Printf("[ActionStore] WARN segment %s is over the size valve but the day already has %d segments; writes stay in it", cur.name, maxSegmentsPerDay)
			}
			return "", ""
		}
		return segmentname.Format(day, sequence+1), "size"
	}
	return "", ""
}

// rotateIfDue starts the next segment when the next write belongs to one. It runs
// at the start of every write, with the store's write lock held, so the decision
// and the rotation are one step (no write is in flight, and none can start) and
// two writers cannot both rotate. A rotation that fails before it has sealed the
// segment leaves the store writing to the segment it has (the failure is logged
// and the next attempt is later). One that fails after the seal returns an error:
// the segment can no longer be written, and the next write completes the rotation
// first.
func (s *JSONLStore) rotateIfDue(ctx context.Context) error {
	r := s.rot
	if r == nil || s.idx == nil || !s.idx.isLayoutV2() {
		return nil
	}
	if r.incomplete {
		return s.completeRotation(ctx, "completion")
	}
	if !r.enabled || r.backingOff() {
		return nil
	}
	name, reason := s.rotationTarget(r.now())
	if name == "" {
		return nil
	}
	// Waiting for the reads in flight is what lets the rotation go through under a
	// steady stream of reads: a pending writer holds off new readers, so the wait
	// is as long as the longest read in flight (a few microseconds; a read that
	// rebuilds the snapshot holds it for as long as the rebuild takes).
	s.idx.segMu.Lock()
	defer s.idx.segMu.Unlock()
	started := time.Now()
	from := s.idx.current().name
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rotationTimeout)
	defer cancel()
	if err := s.rotateLocked(rctx, name); err != nil {
		if r.incomplete {
			log.Printf("[ActionStore] WARN segment rotation from=%s to=%s reason=%s sealed %s but could not make its successor; writes wait for it: %v", from, name, reason, from, err)
			return err
		}
		r.failed()
		log.Printf("[ActionStore] WARN segment rotation from=%s to=%s reason=%s failed; writes stay in %s and the next attempt is in %s: %v", from, name, reason, from, r.retryAfter, err)
		return nil
	}
	r.succeeded()
	pause := time.Since(started)
	if r.pause != nil {
		r.pause(pause)
	}
	log.Printf("[ActionStore] segment rotated from=%s to=%s reason=%s pause=%s", from, name, reason, pause.Round(time.Microsecond))
	return nil
}

// completeRotation makes the segment that follows the sealed current one, after
// an earlier rotation sealed it and stopped. It takes the segment lock, which the
// caller does not hold.
func (s *JSONLStore) completeRotation(ctx context.Context, why string) error {
	s.idx.segMu.Lock()
	defer s.idx.segMu.Unlock()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rotationTimeout)
	defer cancel()
	if err := s.makeSuccessor(rctx); err != nil {
		return fmt.Errorf("make the segment that follows %s: %w", s.idx.current().name, err)
	}
	log.Printf("[ActionStore] segment rotation completed after the seal segment=%s why=%s", s.idx.current().name, why)
	return nil
}

// rotateLocked closes the current segment and starts the one named name. The
// caller holds the store's write lock and the snapshot's segment lock, so no
// transaction is in flight and no read is between its freshness check and its
// answer. Nothing that happens here changes a byte of a closed segment's logs.
//
//	R1 settle the current segment's WAL; make the snapshot absorb whatever the
//	   logs hold; check the logs are what the snapshot absorbed. A failure ends the
//	   rotation here, before anything was written.
//	R2 seal the current segment: SEALED records the final size of its logs, and
//	   the files are made read-only. This is the point of no return: from here the
//	   segment is closed, whatever else happens (a process that finds the newest
//	   segment sealed makes the next one).
//	R3 create the next segment (jsonlbatch.New) and make its directory entry
//	   durable.
//	R4 switch: the snapshot follows the new segment, writes go to its Store.
func (s *JSONLStore) rotateLocked(ctx context.Context, name string) error {
	r := s.rot
	cur := s.idx.current()
	// Another writer may have closed this segment already. Sealing it again and
	// making a successor of our own would fork the store.
	if err := s.requireOpenSegment(); err != nil {
		return err
	}
	if err := s.batch.Recover(ctx); err != nil {
		return fmt.Errorf("settle the WAL of the current segment: %w", err)
	}
	var final sizes
	if err := s.batch.Read(withTxLabel(ctx, txOpRotate), func() error {
		if err := s.idx.ensureSynced(ctx); err != nil {
			return err
		}
		onDisk, err := segmentFileSizes(cur.dir)
		if err != nil {
			return err
		}
		final = s.idx.appliedNow()
		if onDisk != final {
			return fmt.Errorf("segment %s holds %v bytes, the snapshot absorbed %v", cur.name, onDisk, final)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("bring the snapshot up to the current segment: %w", err)
	}
	r.fire("rotate-settled")

	installed, err := writeSealed(cur.dir, final, r.failpoint)
	if installed {
		// Even when the directory could not be synced the record may be there for
		// the next open to find, so the segment is no longer one to write to.
		r.incomplete = true
	}
	if err != nil {
		return fmt.Errorf("seal segment %s: %w", cur.name, err)
	}
	r.fire("rotate-sealed")
	if err := freezeFiles(segmentFilesOf(cur.dir)); err != nil {
		// The size record is the defense; the read-only mode is a guard on top.
		log.Printf("[ActionStore] WARN segment %s could not be made read-only: %v", cur.name, err)
	}
	return s.makeSuccessorNamed(ctx, name)
}

// makeSuccessor makes the segment after the sealed current one, named by the
// clock. The caller holds the store's write lock and the segment lock.
func (s *JSONLStore) makeSuccessor(ctx context.Context) error {
	name, err := nextSegmentName(s.idx.current().name, s.rot.now())
	if err != nil {
		return err
	}
	return s.makeSuccessorNamed(ctx, name)
}

func (s *JSONLStore) makeSuccessorNamed(_ context.Context, name string) error {
	r := s.rot
	cur := s.idx.current()
	// The seal must be durable before a successor exists: writeSealed syncs the
	// directory, but when that failed the record is in place without being known
	// to survive a power cut, and a successor holding data next to a segment whose
	// seal was lost is a store that does not open.
	if err := jsonlbatch.SyncDirectory(cur.dir); err != nil {
		return fmt.Errorf("sync segment %s after its seal: %w", cur.name, err)
	}
	ref, err := planSuccessor(s.root, cur.name, s.idx.segmentCount(), name)
	if err != nil {
		return err
	}
	next, err := createSegment(s.root, ref)
	if err != nil {
		return err
	}
	r.fire("rotate-created")
	if err := s.installSegment(next, ref); err != nil {
		return err
	}
	r.incomplete = false
	r.fire("rotate-switched")
	return nil
}

func (ix *snapshot) segmentCount() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.segs)
}

// planSuccessor chooses the segment that follows cur: the one named name, unless an
// earlier attempt created a newer, still empty segment directory and then failed
// to switch to it. That directory is the newest on disk and is what the next open
// would choose, so the switch goes to it (under any other name the ordinals of the
// segments on disk and in memory would disagree after a restart). A newer
// directory that holds data is not an orphan: the store is in a state this build
// does not understand. ord is the ordinal the new segment takes, the number of
// segments the snapshot follows.
func planSuccessor(root, cur string, ord int, name string) (segmentRef, error) {
	names, err := segmentname.ListDirs(logsDir(root))
	if err != nil {
		return segmentRef{}, err
	}
	if len(names) > 0 && names[len(names)-1] > cur {
		newest := names[len(names)-1]
		if len(names) != ord {
			return segmentRef{}, fmt.Errorf("segment %s is newer than %s but %d segments are known", newest, cur, ord)
		}
		empty, err := segmentHoldsNoData(segmentDir(root, newest))
		if err != nil {
			return segmentRef{}, err
		}
		if !empty {
			return segmentRef{}, fmt.Errorf("segment %s is newer than the current segment %s and holds data", newest, cur)
		}
		name = newest
	}
	if ord <= 0 || ord > maxSegmentCount {
		return segmentRef{}, fmt.Errorf("segment %q cannot be given an ordinal", name)
	}
	return segmentRef{ord: ord, name: name, dir: segmentDir(root, name)}, nil
}

// createSegment creates the directory of ref with its logs, WAL and lock, and
// makes the entries durable.
func createSegment(root string, ref segmentRef) (*jsonlbatch.Store, error) {
	store, err := jsonlbatch.New(ref.dir, logFilenames[:])
	if err != nil {
		return nil, fmt.Errorf("create segment %s: %w", ref.name, err)
	}
	if err := jsonlbatch.SyncDirectory(logsDir(root)); err != nil {
		return nil, fmt.Errorf("sync the segment directory: %w", err)
	}
	if err := jsonlbatch.SyncDirectory(root); err != nil {
		return nil, fmt.Errorf("sync the action store directory: %w", err)
	}
	return store, nil
}

// installSegment is the switch itself: the snapshot follows ref and the store
// appends through next from now on. The caller holds the store's write lock and
// the snapshot's segment lock (or the store is not shared yet).
func (s *JSONLStore) installSegment(next *jsonlbatch.Store, ref segmentRef) error {
	if err := s.idx.switchTo(ref); err != nil {
		return err
	}
	next.SetTxObserver(s.txObserver.Observe)
	next.SetPostCommitHook(s.idx.onCommitFor(ref.ord))
	s.batch.SetPostCommitHook(nil)
	s.batch = next
	paths := ref.paths()
	s.hot.Store(&paths)
	return nil
}

// sealedExists reports whether the segment in dir has been sealed.
func sealedExists(dir string) (bool, error) {
	_, err := os.Stat(sealedPath(dir))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// requireOpenSegment fails unless the segment the store writes to is still open.
// A sealed segment was closed by a rotation, and a writer that did not make that
// rotation (another process, or this one before a restart) must not append to it.
func (s *JSONLStore) requireOpenSegment() error {
	cur := s.idx.current()
	if cur.ord == 0 {
		return nil
	}
	sealed, err := sealedExists(cur.dir)
	if err != nil {
		return fmt.Errorf("check whether segment %s is sealed: %w", cur.name, err)
	}
	if sealed {
		return fmt.Errorf("%w: segment %s was closed by another writer; restart the process to follow the store", ErrSegmentMissing, cur.name)
	}
	return nil
}

// migrationAborted says layout v2 was not started and nothing durable was changed
// that matters: the store goes on as layout v1.
type migrationAborted struct{ cause error }

func (e *migrationAborted) Error() string { return "segment migration aborted: " + e.cause.Error() }
func (e *migrationAborted) Unwrap() error { return e.cause }

// migrateToSegments moves a store from layout v1 to layout v2: the files in the
// root become segment 0, frozen, and the first segment is created. It runs at
// open, before the store is shared.
//
//	M1 settle segment 0 and make the snapshot absorb everything it holds; measure it.
//	M3 create the first segment (an empty one left by an earlier attempt is reused).
//	M4 write LAYOUT with the sizes M1 measured. From here on the store is layout v2.
//	M5 make segment 0 read-only.
//
// Until M4 completes a failure leaves a store that is still layout v1 and the open
// goes on as such (migrationAborted). After it, the failure is the open's: the
// next open finds LAYOUT, checks segment 0 against it, freezes it again if M5 did
// not finish, and writes to the first segment.
func (s *JSONLStore) migrateToSegments(ctx context.Context) error {
	r := s.rot
	abort := func(err error) error { return &migrationAborted{cause: err} }
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idx.segMu.Lock()
	defer s.idx.segMu.Unlock()

	// M1
	if err := s.batch.Recover(ctx); err != nil {
		return abort(fmt.Errorf("settle segment 0: %w", err))
	}
	var measured sizes
	if err := s.batch.Read(withTxLabel(ctx, txOpMigrate), func() error {
		if err := s.idx.ensureSynced(ctx); err != nil {
			return err
		}
		onDisk, err := segmentFileSizes(s.root)
		if err != nil {
			return err
		}
		measured = s.idx.appliedNow()
		if onDisk != measured {
			return fmt.Errorf("segment 0 holds %v bytes, the snapshot absorbed %v", onDisk, measured)
		}
		return nil
	}); err != nil {
		return abort(fmt.Errorf("bring the snapshot up to segment 0: %w", err))
	}
	r.fire("migrate-M1")

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
	installed, err := writeFileAtomically(layoutPath(s.root), formatSizeRecord(layoutVersionLine, layoutFrozenWord, measured), r.failpoint, "layout")
	if err != nil {
		if !installed {
			return abort(err)
		}
		return fmt.Errorf("LAYOUT was installed but could not be made durable: %w", err)
	}
	r.fire("migrate-M4")

	// M5
	if err := freezeSegmentZero(s.root); err != nil {
		log.Printf("[ActionStore] WARN segment 0 could not be made read-only: %v", err)
	}
	r.fire("migrate-M5")

	s.idx.enterLayoutV2(measured)
	if err := s.installSegment(next, ref); err != nil {
		return err
	}
	log.Printf("[ActionStore] segment migration done: segment 0 frozen at %d bytes, first segment %s", measured[fileAction]+measured[fileAttempt], ref.name)
	return nil
}

// firstSegment chooses the segment the migration creates: the one an earlier,
// interrupted attempt left (an empty directory under logs/), else today's first.
func firstSegment(root string, now time.Time) (segmentRef, error) {
	names, err := segmentname.ListDirs(logsDir(root))
	if err != nil {
		return segmentRef{}, err
	}
	switch len(names) {
	case 0:
		name := segmentname.Format(segmentname.UTCDay(now), 1)
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

// openLayout reads LAYOUT and lists the segments of the store at root. On layout
// v2 it also holds segment 0 against the sizes LAYOUT recorded when it froze it,
// and refuses a store that has no segment under logs/ (LAYOUT is written after
// the first one exists). It is the one door every opener of a store goes through,
// so none of them can see a history that is not the recorded one.
func openLayout(root string) (layoutInfo, []segmentRef, error) {
	layout, err := readLayout(root)
	if err != nil {
		return layoutInfo{}, nil, err
	}
	refs, err := listSegments(root, layout.v2)
	if err != nil {
		return layoutInfo{}, nil, err
	}
	if layout.v2 {
		if len(refs) < 2 {
			return layoutInfo{}, nil, fmt.Errorf("%w: LAYOUT v2 is present but there is no segment under %s", ErrSegmentMissing, logsDirName)
		}
		if err := verifyFrozenSegmentZero(root, layout.frozen); err != nil {
			return layoutInfo{}, nil, err
		}
	}
	return layout, refs, nil
}

// openWriterBatch opens the jsonlbatch Store the writer appends to: the files in
// the root on layout v1, the newest segment on layout v2. On layout v2 segment 0
// is frozen (and kept so), and the segments before the newest must hold no
// unsettled WAL; the newest is repaired by the Recover that follows. A newest
// segment that is sealed was closed by a rotation that stopped before it made the
// next segment: the next one is made here, whatever Rotation says, because a
// sealed segment is not written.
func openWriterBatch(root string, layout layoutInfo, refs []segmentRef, now func() time.Time, failpoint func(string)) (*jsonlbatch.Store, []segmentRef, error) {
	if !layout.v2 {
		batch, err := jsonlbatch.New(root, logFilenames[:])
		return batch, refs, err
	}
	if err := freezeSegmentZero(root); err != nil {
		// The size check is the defense; the read-only mode is a guard on top.
		log.Printf("[ActionStore] WARN segment 0 could not be made read-only: %v", err)
	}
	for _, ref := range refs[:len(refs)-1] {
		// A rotation settles the WAL of the segment it closes before it seals it, so
		// a closed segment with a transaction pending or torn is not what a crash
		// leaves behind. Repairing it would roll back (cut) bytes that something
		// else wrote; the store is not opened instead, and the bytes are left where
		// they are for someone to look at.
		if err := jsonlbatch.CheckSettled(ref.dir, logFilenames[:]); err != nil {
			return nil, nil, fmt.Errorf("segment %s is closed but its WAL is not settled: %w", ref.name, err)
		}
	}
	newest := refs[len(refs)-1]
	sealed, err := sealedExists(newest.dir)
	if err != nil {
		return nil, nil, err
	}
	if !sealed {
		batch, err := jsonlbatch.New(newest.dir, logFilenames[:])
		return batch, refs, err
	}
	if err := jsonlbatch.CheckSettled(newest.dir, logFilenames[:]); err != nil {
		return nil, nil, fmt.Errorf("segment %s is sealed but its WAL is not settled: %w", newest.name, err)
	}
	if err := jsonlbatch.SyncDirectory(newest.dir); err != nil {
		return nil, nil, fmt.Errorf("sync segment %s after its seal: %w", newest.name, err)
	}
	if now == nil {
		now = time.Now
	}
	name, err := nextSegmentName(newest.name, now())
	if err != nil {
		return nil, nil, err
	}
	ref, err := planSuccessor(root, newest.name, len(refs), name)
	if err != nil {
		return nil, nil, err
	}
	batch, err := createSegment(root, ref)
	if err != nil {
		return nil, nil, err
	}
	if failpoint != nil {
		failpoint("open-completed-rotation")
	}
	log.Printf("[ActionStore] segment rotation completed at open: %s was sealed, next segment %s", newest.name, ref.name)
	return batch, append(refs, ref), nil
}
