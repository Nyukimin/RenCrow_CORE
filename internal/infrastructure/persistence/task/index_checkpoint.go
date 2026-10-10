package task

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

const (
	sidecarDirName    = "index"
	sidecarLegacyFile = "seg-legacy.tsi"
	sidecarTmpSuffix  = ".tmp"

	// A checkpoint is due when this much time has passed or this many log lines
	// were committed since the last one (whichever comes first), when the writer
	// closes, and right after a rebuild. They are constants, not configuration.
	checkpointInterval      = 10 * time.Minute
	checkpointLineThreshold = 5000
	// After a failed checkpoint the next attempt waits retryBase, doubling up to
	// retryMax, so a full disk is not hammered by every commit.
	checkpointRetryBase = 30 * time.Second
	checkpointRetryMax  = 10 * time.Minute
)

// sidecarRenameBackoff are the pauses between attempts to replace the sidecar:
// five attempts, about 150 ms in all. A replacement fails on Windows while
// another process holds the destination open, which is usually brief.
var sidecarRenameBackoff = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond}

// checkpointPolicy says when the checkpointer writes. manual means no goroutine:
// the caller drives checkpointNow (tests, and the offline rebuild).
type checkpointPolicy struct {
	interval      time.Duration
	lineThreshold int64
	retryBase     time.Duration
	retryMax      time.Duration
	manual        bool
}

func defaultCheckpointPolicy() checkpointPolicy {
	return checkpointPolicy{
		interval: checkpointInterval, lineThreshold: checkpointLineThreshold,
		retryBase: checkpointRetryBase, retryMax: checkpointRetryMax,
	}
}

// checkpointer keeps the sidecar of one index up to date. Exactly one goroutine
// (run) exists per writable store when the policy is not manual. Its start and
// stop are a pair: openJSONLStore calls start once the store is open, and Close
// calls stopAndFlush, which is idempotent, waits for the goroutine to exit, and
// only then writes the last checkpoint, so two writers of the sidecar never
// overlap.
type checkpointer struct {
	ix     *taskIndex
	dir    string
	path   string
	policy checkpointPolicy

	// Test seams. fp is called at each stage of a replacement (nil in
	// production); rename is os.Rename.
	fp            func(stage string)
	rename        func(oldpath, newpath string) error
	renameBackoff []time.Duration

	kick      chan struct{} // capacity 1; a send never blocks (see noteLines)
	stopCh    chan struct{}
	done      chan struct{} // closed when no goroutine is running
	stopOnce  sync.Once
	flushOnce sync.Once
	started   bool

	// lastLines is the number of log lines the latest sidecar covers.
	lastLines atomic.Int64
	count     atomic.Uint64
	failures  atomic.Int64

	mu          sync.Mutex // one replacement at a time; guards the fields below
	written     bool       // a sidecar of this run exists
	lastApplied [kindCount]int64
	nextTry     time.Time
	retryDelay  time.Duration
}

func newCheckpointer(ix *taskIndex, root string, policy checkpointPolicy) *checkpointer {
	dir := filepath.Join(root, sidecarDirName)
	c := &checkpointer{
		ix: ix, dir: dir, path: filepath.Join(dir, sidecarLegacyFile), policy: policy,
		rename: os.Rename, renameBackoff: sidecarRenameBackoff,
		kick: make(chan struct{}, 1), stopCh: make(chan struct{}), done: make(chan struct{}),
	}
	close(c.done) // nothing is running yet; start replaces it
	// A temporary file here is the remains of a checkpoint that a crash cut off.
	// It is this writer's own scratch name and never a sidecar (the writer lock
	// is held), so it goes. The sidecar itself is never removed.
	_ = os.Remove(c.path + sidecarTmpSuffix)
	return c
}

// start launches the checkpoint goroutine, unless the policy is manual.
func (c *checkpointer) start() {
	if c.policy.manual || c.started {
		return
	}
	c.started = true
	c.done = make(chan struct{})
	go c.run()
}

func (c *checkpointer) run() {
	defer close(c.done)
	ticker := time.NewTicker(c.policy.interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.checkpointIfDue("interval")
		case <-c.kick:
			c.checkpointIfDue("lines")
		}
	}
}

// stop ends the goroutine and waits for it. It is safe to call any number of
// times, from any goroutine.
func (c *checkpointer) stop() {
	c.stopOnce.Do(func() { close(c.stopCh) })
	<-c.done
}

// stopAndFlush is the second stage of closing: stop the loop, then try one last
// checkpoint. A failure is logged and does not fail Close; the sidecar then
// simply lags the log and the next open replays the difference.
func (c *checkpointer) stopAndFlush() {
	c.stop()
	c.flushOnce.Do(func() { _ = c.checkpointIfDue("close") })
}

// noteLines is called after every merge with the total number of lines the
// index has absorbed. It only signals; it never blocks the commit path.
func (c *checkpointer) noteLines(total int64) {
	if c.policy.manual || total-c.lastLines.Load() < c.policy.lineThreshold {
		return
	}
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// markWritten records the state a sidecar on disk already covers, so the loop
// does not rewrite it unchanged (used after a restore that needed no replay).
func (c *checkpointer) markWritten(applied [kindCount]int64, lines int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written, c.lastApplied = true, applied
	c.lastLines.Store(lines)
}

// checkpointIfDue writes a checkpoint unless the index is unchanged since the
// last one or a recent failure is still backing off.
func (c *checkpointer) checkpointIfDue(reason string) error {
	c.mu.Lock()
	if reason != "close" && time.Now().Before(c.nextTry) {
		c.mu.Unlock()
		return nil
	}
	if c.written && c.ix.appliedNow() == c.lastApplied {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.checkpointNow(reason)
}

// appliedNow is the committed prefix the index has absorbed, per file.
func (ix *taskIndex) appliedNow() [kindCount]int64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.applied
}

// checkpointNow snapshots the index and replaces the sidecar. A failure never
// reaches the write path: it is counted, logged, and retried later.
func (c *checkpointer) checkpointNow(reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	started := time.Now()
	snap := c.ix.snapshot()
	data := encodeSidecar(snap)
	if err := c.replaceSidecar(data); err != nil {
		failures := c.failures.Add(1)
		if c.retryDelay == 0 {
			c.retryDelay = c.policy.retryBase
		} else if c.retryDelay *= 2; c.retryDelay > c.policy.retryMax {
			c.retryDelay = c.policy.retryMax
		}
		c.nextTry = time.Now().Add(c.retryDelay)
		log.Printf("[TaskStore] WARN index checkpoint seg=legacy result=error failures=%d reason=%s retry_in=%s: %v", failures, reason, c.retryDelay, err)
		return err
	}
	c.failures.Store(0)
	c.retryDelay, c.nextTry = 0, time.Time{}
	c.written, c.lastApplied = true, snap.applied
	c.lastLines.Store(snap.totalLines())
	c.count.Add(1)
	log.Printf("[TaskStore] index checkpoint seg=legacy bytes=%d dur=%s reason=%s", len(data), time.Since(started).Round(time.Millisecond), reason)
	return nil
}

func (c *checkpointer) failpoint(stage string) {
	if c.fp != nil {
		c.fp(stage)
	}
}

// replaceSidecar installs data as the sidecar: write a temporary file, fsync it,
// rename it over the old sidecar, fsync the directory. At every point the
// sidecar path holds either the whole old file or the whole new one; a crash
// leaves at worst a temporary file, which the next writer deletes.
func (c *checkpointer) replaceSidecar(data []byte) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("create sidecar directory: %w", err)
	}
	tmp := c.path + sidecarTmpSuffix
	if err := c.writeTemporary(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	c.failpoint("pre-rename")
	if err := c.renameWithRetry(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	c.failpoint("post-rename")
	// The rename must reach the disk before the checkpoint counts as written;
	// the new directory entry of "index" itself needs the parent synced too.
	if err := jsonlbatch.SyncDirectory(c.dir); err != nil {
		return fmt.Errorf("sync sidecar directory: %w", err)
	}
	if err := jsonlbatch.SyncDirectory(filepath.Dir(c.dir)); err != nil {
		return fmt.Errorf("sync task store directory: %w", err)
	}
	c.failpoint("dir-synced")
	return nil
}

func (c *checkpointer) writeTemporary(tmp string, data []byte) error {
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create sidecar temporary file: %w", err)
	}
	c.failpoint("tmp-created")
	half := len(data) / 2
	if _, err := file.Write(data[:half]); err != nil {
		_ = file.Close()
		return fmt.Errorf("write sidecar temporary file: %w", err)
	}
	c.failpoint("tmp-partial")
	if _, err := file.Write(data[half:]); err != nil {
		_ = file.Close()
		return fmt.Errorf("write sidecar temporary file: %w", err)
	}
	c.failpoint("tmp-written")
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync sidecar temporary file: %w", err)
	}
	c.failpoint("tmp-synced")
	// Closed before the rename: Windows will not replace an open file.
	if err := file.Close(); err != nil {
		return fmt.Errorf("close sidecar temporary file: %w", err)
	}
	return nil
}

// renameWithRetry replaces newpath with oldpath, retrying a bounded number of
// times. os.Rename replaces an existing file on every platform (MoveFileEx with
// MOVEFILE_REPLACE_EXISTING on Windows) but fails there while another process
// holds newpath open. Giving up is safe: the old sidecar is still in place, the
// index keeps working, and the next checkpoint tries again.
func (c *checkpointer) renameWithRetry(oldpath, newpath string) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = c.rename(oldpath, newpath); err == nil {
			return nil
		}
		if attempt >= len(c.renameBackoff) {
			return fmt.Errorf("replace sidecar after %d attempts: %w", attempt+1, err)
		}
		time.Sleep(c.renameBackoff[attempt])
	}
}
