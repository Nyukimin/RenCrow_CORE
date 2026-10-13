package action

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
)

// Readers and writers while the store rotates (design 5.3, the lock order in
// snapshot.go). Run with -race.

func TestRotationUnderConcurrentReadersAndWriters(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	const writers, perWriter = 3, 30
	pairs := makePairs(t, writers*perWriter, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 2500)
	ctx := context.Background()

	var created atomic.Int64
	stop := make(chan struct{})
	var readers sync.WaitGroup
	var readErr atomic.Value
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func(r int) {
			defer readers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				n := int(created.Load())
				if n == 0 {
					continue
				}
				p := pairs[(i+r)%n]
				if _, err := store.GetAction(ctx, p.action.ActionID); err != nil && !errors.Is(err, domainaction.ErrNotFound) {
					readErr.Store(err)
					return
				}
				if _, err := store.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: p.action.ActionID}); err != nil {
					readErr.Store(err)
					return
				}
				if _, err := store.ListActions(ctx, domainaction.Filter{RunID: p.action.RunID}); err != nil {
					readErr.Store(err)
					return
				}
			}
		}(r)
	}

	var wg sync.WaitGroup
	var writeErr atomic.Value
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				p := pairs[w*perWriter+i]
				if err := p.create(ctx, store); err != nil {
					writeErr.Store(err)
					return
				}
				created.Add(1)
				if i%7 == 0 {
					clock.Advance(9 * time.Hour) // some of the rotations are by day
				}
				if err := p.finish(ctx, store); err != nil {
					writeErr.Store(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	if err, _ := writeErr.Load().(error); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err, _ := readErr.Load().(error); err != nil {
		t.Fatalf("read: %v", err)
	}

	if names := segmentNames(t, root); len(names) < 5 {
		t.Fatalf("only %d segments; the test is meant to rotate under load: %v", len(names), names)
	}
	requireHolds(t, store, pairs, 0, len(pairs))
	requireSnapshotEqualsLog(t, store)
	stats, ok := store.IndexStats()
	if !ok || stats.Rebuilds != 0 {
		t.Fatalf("stats = %+v (ok=%v); a rotation must not make the snapshot rebuild", stats, ok)
	}
	if stats.Segments != len(segmentNames(t, root))+1 {
		t.Fatalf("the snapshot follows %d segments, the store has %d", stats.Segments, len(segmentNames(t, root))+1)
	}
	report, err := VerifyActionStore(ctx, root)
	if err != nil || !report.OK {
		t.Fatalf("verify: ok=%v err=%v problems=%v", report.OK, err, report.Problems)
	}
}

// A handle that did not rotate sees what another handle wrote to the newest
// segment, the way it sees an append to a plain store: it reads the logs again.
func TestAHandleSeesAnotherHandlesWritesToTheNewestSegment(t *testing.T) {
	base, pairs, clock := segmentedStore(t)
	root := copyOf(t, base)
	a := openRotating(t, root, clock, 0)
	b := openRotating(t, root, clock, 0)
	extra := makePairs(t, 3, rotStart.Add(72*time.Hour))
	writeAll(t, b, extra, 0, 3)
	requireHolds(t, a, extra, 0, 3)
	requireHolds(t, a, pairs, 0, 12)
	requireSnapshotEqualsLog(t, a)
	stats, _ := a.IndexStats()
	if stats.Rebuilds == 0 {
		t.Fatal("handle A saw handle B's writes without reading the logs again")
	}
}
