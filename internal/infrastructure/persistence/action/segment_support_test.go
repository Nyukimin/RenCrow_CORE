package action

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/segmentname"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Support for the segment tests (design 5.3, AC-13). A store is moved to
// segments by opening it with Rotation; a test clock stands in for the day, and
// failpoints stand in for the places a process can stop.

// rotClock is a clock the test sets.
type rotClock struct {
	mu sync.Mutex
	t  time.Time
}

func newRotClock(t time.Time) *rotClock { return &rotClock{t: t} }

func (c *rotClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *rotClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *rotClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

var rotStart = time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)

// rotOptions opens a store with the snapshot and rotation, on the given clock.
func rotOptions(clock *rotClock, valve int64, failpoint func(string)) OpenOptions {
	return OpenOptions{Index: true, Rotation: true, RotationSizeValve: valve, now: clock.Now, failpoint: failpoint}
}

type simulatedCrash struct{ stage string }

// didCrash runs fn and reports whether it stopped at a failpoint that panicked
// with simulatedCrash. The files are left as they were at that point.
func didCrash(fn func()) (crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(simulatedCrash); ok {
				crashed = true
				return
			}
			panic(r)
		}
	}()
	fn()
	return false
}

// pair is one Action with its first Attempt: the open version, and the
// terminal version a later transaction writes.
type pair struct {
	action, closedAction   domainaction.Action
	attempt, closedAttempt domainaction.Attempt
}

// makePairs builds n pairs once, so that two stores can be given exactly the
// same records and compared byte for byte.
func makePairs(t testing.TB, n int, base time.Time) []pair {
	t.Helper()
	tasks := []modulecore.TaskID{modulecore.NewTaskID(), modulecore.NewTaskID()}
	runs := []modulecore.RunID{modulecore.NewRunID(), modulecore.NewRunID(), modulecore.NewRunID()}
	out := make([]pair, n)
	for i := range out {
		at := base.Add(time.Duration(i) * time.Second)
		action, attempt := newOpenPair(tasks[i%len(tasks)], runs[i%len(runs)], domainaction.KindTool, "seg-"+string(rune('a'+i%4)), at)
		closedAction, closedAttempt := closePair(t, action, attempt, at.Add(500*time.Millisecond), domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)
		out[i] = pair{action: action, attempt: attempt, closedAction: closedAction, closedAttempt: closedAttempt}
	}
	return out
}

func (p pair) create(ctx context.Context, store *JSONLStore) error {
	return savePair(ctx, store, p.action, p.attempt)
}

func (p pair) finish(ctx context.Context, store *JSONLStore) error {
	return savePair(ctx, store, p.closedAction, p.closedAttempt)
}

// writeAll creates and finishes pairs[from:to] one transaction each.
func writeAll(t testing.TB, store *JSONLStore, pairs []pair, from, to int) {
	t.Helper()
	ctx := context.Background()
	for i := from; i < to; i++ {
		if err := pairs[i].create(ctx, store); err != nil {
			t.Fatalf("create pair %d: %v", i, err)
		}
		if err := pairs[i].finish(ctx, store); err != nil {
			t.Fatalf("finish pair %d: %v", i, err)
		}
	}
}

// requireHolds fails unless the store holds every pair in pairs[from:to] in its
// terminal state.
func requireHolds(t testing.TB, store *JSONLStore, pairs []pair, from, to int) {
	t.Helper()
	ctx := context.Background()
	for i := from; i < to; i++ {
		got, err := store.GetAction(ctx, pairs[i].action.ActionID)
		if err != nil {
			t.Fatalf("pair %d is gone: %v", i, err)
		}
		if got.Status != pairs[i].closedAction.Status {
			t.Fatalf("pair %d is %s, want %s", i, got.Status, pairs[i].closedAction.Status)
		}
		if _, err := store.GetAttempt(ctx, pairs[i].attempt.AttemptID); err != nil {
			t.Fatalf("the Attempt of pair %d is gone: %v", i, err)
		}
	}
}

// segmentNames lists the segment directories under logs/.
func segmentNames(t testing.TB, root string) []string {
	t.Helper()
	names, err := segmentname.ListDirs(logsDir(root))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// fingerprint is the content of every regular file under root, by relative
// path. Modes are not part of it.
func fingerprint(t testing.TB, root string) map[string][sha256.Size]byte {
	t.Helper()
	out := map[string][sha256.Size]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// requireUnchanged fails unless every file in before is still there with the
// same content (new files are allowed unless strict is set).
func requireUnchanged(t testing.TB, before, after map[string][sha256.Size]byte, strict bool, what string) {
	t.Helper()
	paths := make([]string, 0, len(before))
	for p := range before {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		got, ok := after[p]
		if !ok {
			t.Fatalf("%s: %s is gone", what, p)
		}
		if got != before[p] {
			t.Fatalf("%s: %s changed", what, p)
		}
	}
	if strict && len(after) != len(before) {
		t.Fatalf("%s: %d files before, %d after", what, len(before), len(after))
	}
}

// makeAllWritable undoes the read-only mode of every frozen file under root so a
// test can remove the directory (t.TempDir cleanup, and copies of a frozen
// store).
func makeAllWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type().IsRegular() {
			_ = os.Chmod(path, 0o644)
		}
		return nil
	})
}

// copyTree copies a directory tree, keeping file modes. The copy is what a
// process that died leaves behind: nothing in it is cleaned up.
func copyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// plainStore writes pairs[from:to] to a store on the plain layout under root, the
// state a store is in before the first open with Rotation.
func plainStore(t testing.TB, root string, pairs []pair, n int) {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	writeAll(t, store, pairs, 0, n)
}

// captureLog collects what the log package prints until the returned function
// is called, which restores the previous destination and returns the text.
func captureLog(t testing.TB) func() string {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	var once sync.Once
	var text string
	finish := func() string {
		once.Do(func() {
			log.SetOutput(prev)
			mu.Lock()
			text = buf.String()
			mu.Unlock()
		})
		return text
	}
	t.Cleanup(func() { finish() })
	return finish
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// requireSnapshotEqualsLogOrPlain checks a store that may have no snapshot by
// reading it in full.
func requireSnapshotEqualsLogOrPlain(t testing.TB, store *JSONLStore) {
	t.Helper()
	if store.idx != nil {
		requireSnapshotEqualsLog(t, store)
		return
	}
	if _, err := store.loadActions(context.Background()); err != nil {
		t.Fatal(err)
	}
}
