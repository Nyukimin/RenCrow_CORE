package action

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Writers and readers on one store, and on two handles of one root. Run with
// -race: the snapshot is read by many goroutines while commits apply to it.

func TestSnapshotUnderConcurrentWritersAndReaders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	ix := requireIndexed(t, store)

	const writers, perWriter, readers = 4, 15, 4
	var created sync.Map
	var stop atomic.Bool
	var wg sync.WaitGroup
	errs := make(chan error, writers+readers)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			task, run := modulecore.NewTaskID(), modulecore.NewRunID()
			for i := 0; i < perWriter; i++ {
				at := supportBase.Add(time.Duration(w*1000+i) * time.Second)
				action, attempt := newOpenPair(task, run, domainaction.KindTool, fmt.Sprintf("w%d", w), at)
				if err := savePair(ctx, store, action, attempt); err != nil {
					errs <- fmt.Errorf("create: %w", err)
					return
				}
				created.Store(action.ActionID, run)
				closedAction, closedAttempt := closePair(t, action, attempt, at.Add(time.Millisecond), domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)
				if err := savePair(ctx, store, closedAction, closedAttempt); err != nil {
					errs <- fmt.Errorf("close: %w", err)
					return
				}
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				err := store.ReadTransaction(ctx, func(tx domainaction.Store) error {
					open, err := tx.ListActions(ctx, domainaction.Filter{Status: domainaction.StatusOpen})
					if err != nil {
						return err
					}
					for _, a := range open {
						// One read transaction is one point in time: an open Action's
						// current Attempt is there, belongs to it, and is the running one.
						at, err := tx.GetAttempt(ctx, a.CurrentAttemptID)
						if err != nil {
							return fmt.Errorf("open Action without its current Attempt: %w", err)
						}
						if at.ActionID != a.ActionID || at.Status != domainaction.AttemptStatusRunning {
							return fmt.Errorf("incoherent pair")
						}
						attempts, err := tx.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: a.ActionID})
						if err != nil || len(attempts) != 1 {
							return fmt.Errorf("attempts of an open Action = %d, %v", len(attempts), err)
						}
					}
					return nil
				})
				if err != nil {
					errs <- err
					return
				}
				if _, err := store.ListActions(ctx, domainaction.Filter{Limit: 5}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	// Writers finish on their own; then the readers are released.
	deadline := time.After(55 * time.Second)
	for {
		count := 0
		created.Range(func(_, _ any) bool { count++; return true })
		if count == writers*perWriter {
			break
		}
		select {
		case err := <-errs:
			stop.Store(true)
			wg.Wait()
			t.Fatal(err)
		case <-deadline:
			stop.Store(true)
			wg.Wait()
			t.Fatalf("writers did not finish: %d of %d", count, writers*perWriter)
		case <-time.After(20 * time.Millisecond):
		}
	}
	time.Sleep(50 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	requireSnapshotEqualsLog(t, store)
	if stats := ix.stats(); stats.Rebuilds != 0 || stats.Actions != writers*perWriter {
		t.Fatalf("stats = %+v; one handle following its own commits never rebuilds", stats)
	}
	created.Range(func(key, _ any) bool {
		if _, err := store.GetAction(ctx, key.(modulecore.ActionID)); err != nil {
			t.Errorf("acknowledged Action lost: %v", err)
		}
		return true
	})
}

func TestSnapshotUnderTwoHandlesOfOneRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := filepath.Join(t.TempDir(), "actions")
	handles := []*JSONLStore{openIndexed(t, root), openIndexed(t, root)}

	const perHandle = 12
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	var acked sync.Map
	for h, store := range handles {
		wg.Add(2)
		go func(h int, store *JSONLStore) {
			defer wg.Done()
			task, run := modulecore.NewTaskID(), modulecore.NewRunID()
			for i := 0; i < perHandle; i++ {
				action, attempt := newOpenPair(task, run, domainaction.KindTool, fmt.Sprintf("h%d", h), supportBase.Add(time.Duration(h*1000+i)*time.Second))
				if err := savePair(ctx, store, action, attempt); err != nil {
					errs <- err
					return
				}
				acked.Store(action.ActionID, true)
			}
		}(h, store)
		go func(store *JSONLStore) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if _, err := store.ListActions(ctx, domainaction.Filter{Status: domainaction.StatusOpen}); err != nil {
					errs <- err
					return
				}
			}
		}(store)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, store := range handles {
		count := 0
		acked.Range(func(key, _ any) bool {
			count++
			if _, err := store.GetAction(ctx, key.(modulecore.ActionID)); err != nil {
				t.Errorf("a handle does not see an acknowledged Action: %v", err)
			}
			return true
		})
		if count != 2*perHandle {
			t.Fatalf("acknowledged %d, want %d", count, 2*perHandle)
		}
		requireSnapshotEqualsLog(t, store)
	}
	requireSameAnswers(t, openFull(t, root), handles[0])
}
