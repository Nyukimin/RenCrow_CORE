package jsonlutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	appendBatchInterval    = 20 * time.Millisecond
	appendBatchMaxRecords  = 64
	appendBatchMaxBytes    = 1 << 20
	appendBatchMaxFileRuns = 64
)

type AppendStage string

const (
	AppendStageDirectory AppendStage = "directory"
	AppendStageOpen      AppendStage = "open"
	AppendStageWrite     AppendStage = "write"
	AppendStageClose     AppendStage = "close"
)

// AppendError identifies the file operation that failed while preserving the
// underlying error for errors.Is and errors.As callers.
type AppendError struct {
	Stage AppendStage
	Err   error
}

func (e *AppendError) Error() string { return fmt.Sprintf("append %s: %v", e.Stage, e.Err) }
func (e *AppendError) Unwrap() error { return e.Err }

type appendBatchRequest struct {
	line             []byte
	size             int
	immediate        bool
	operation        func(string) error
	queuedAt         time.Time
	result           chan error
	waitingImmediate bool
}

type appendBatchState struct {
	path             string
	mu               sync.Mutex
	queue            []*appendBatchRequest
	pendingB         int
	active           bool
	changed          chan struct{}
	closed           bool
	lastStart        time.Time
	immediateWaiters int
}

type appendBatchCoordinator struct {
	mu        sync.Mutex
	states    map[string]*appendBatchState
	fileSlots chan struct{}
	interval  time.Duration
	maxCount  int
	maxBytes  int
	write     func(string, [][]byte) error
	stats     appendBatchCounters
}

type appendBatchCounters struct {
	normalAttempts      atomic.Uint64
	normalSuccesses     atomic.Uint64
	normalFailures      atomic.Uint64
	normalShortWrite    atomic.Uint64
	normalRecords       atomic.Uint64
	normalBytes         atomic.Uint64
	immediateAttempts   atomic.Uint64
	immediateSuccesses  atomic.Uint64
	immediateFailures   atomic.Uint64
	immediateShortWrite atomic.Uint64
	immediateRecords    atomic.Uint64
	immediateBytes      atomic.Uint64
	immediateFences     atomic.Uint64
}

// AppendBatchStats is a process-lifetime aggregate of synchronous JSONL append work.
type AppendBatchStats struct {
	NormalFlushAttempts     uint64 `json:"normal_flush_attempts"`
	NormalFlushSuccesses    uint64 `json:"normal_flush_successes"`
	NormalFlushFailures     uint64 `json:"normal_flush_failures"`
	NormalShortWrites       uint64 `json:"normal_short_writes"`
	NormalRecordsWritten    uint64 `json:"normal_records_written"`
	NormalBytesWritten      uint64 `json:"normal_bytes_written"`
	ImmediateWriteAttempts  uint64 `json:"immediate_write_attempts"`
	ImmediateWriteSuccesses uint64 `json:"immediate_write_successes"`
	ImmediateWriteFailures  uint64 `json:"immediate_write_failures"`
	ImmediateShortWrites    uint64 `json:"immediate_short_writes"`
	ImmediateRecordsWritten uint64 `json:"immediate_records_written"`
	ImmediateBytesWritten   uint64 `json:"immediate_bytes_written"`
	ImmediateFences         uint64 `json:"immediate_fences"`
}

// AppendBatchStatsSnapshot returns process-lifetime append counters without file or
// per-path state. The counters include only append write attempts and successes.
func AppendBatchStatsSnapshot() AppendBatchStats {
	c := &appendBatches.stats
	return AppendBatchStats{
		NormalFlushAttempts:     c.normalAttempts.Load(),
		NormalFlushSuccesses:    c.normalSuccesses.Load(),
		NormalFlushFailures:     c.normalFailures.Load(),
		NormalShortWrites:       c.normalShortWrite.Load(),
		NormalRecordsWritten:    c.normalRecords.Load(),
		NormalBytesWritten:      c.normalBytes.Load(),
		ImmediateWriteAttempts:  c.immediateAttempts.Load(),
		ImmediateWriteSuccesses: c.immediateSuccesses.Load(),
		ImmediateWriteFailures:  c.immediateFailures.Load(),
		ImmediateShortWrites:    c.immediateShortWrite.Load(),
		ImmediateRecordsWritten: c.immediateRecords.Load(),
		ImmediateBytesWritten:   c.immediateBytes.Load(),
		ImmediateFences:         c.immediateFences.Load(),
	}
}

func newAppendBatchCoordinator(interval time.Duration, maxCount, maxBytes, maxFiles int, write func(string, [][]byte) error) *appendBatchCoordinator {
	return &appendBatchCoordinator{
		states:    make(map[string]*appendBatchState),
		fileSlots: make(chan struct{}, maxFiles),
		interval:  interval,
		maxCount:  maxCount,
		maxBytes:  maxBytes,
		write:     write,
	}
}

var appendBatches = newAppendBatchCoordinator(appendBatchInterval, appendBatchMaxRecords, appendBatchMaxBytes, appendBatchMaxFileRuns, writeAppendLines)

func (c *appendBatchCoordinator) submit(path string, req *appendBatchRequest, immediate bool) error {
	canonical, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if req.result == nil {
		req.result = make(chan error, 1)
	}
	size := req.size
	if size == 0 {
		size = len(req.line)
	}
	for {
		c.mu.Lock()
		state := c.states[canonical]
		if state == nil {
			c.mu.Unlock()
			c.fileSlots <- struct{}{}
			c.mu.Lock()
			if existing := c.states[canonical]; existing != nil {
				c.mu.Unlock()
				<-c.fileSlots
				continue
			}
			state = &appendBatchState{path: canonical, changed: make(chan struct{})}
			c.states[canonical] = state
			state.mu.Lock()
			c.enqueueLocked(state, req, size, immediate)
			state.mu.Unlock()
			c.mu.Unlock()
			go c.run(state)
			return <-req.result
		}
		state.mu.Lock()
		if state.closed {
			state.mu.Unlock()
			c.mu.Unlock()
			continue
		}
		if c.canQueue(state, size) {
			if immediate && req.waitingImmediate {
				state.immediateWaiters--
				req.waitingImmediate = false
			}
			c.enqueueLocked(state, req, size, immediate)
			state.mu.Unlock()
			c.mu.Unlock()
			return <-req.result
		}
		if immediate && !req.waitingImmediate {
			state.immediateWaiters++
			req.waitingImmediate = true
			c.notifyLocked(state)
		}
		changed := state.changed
		state.mu.Unlock()
		c.mu.Unlock()
		<-changed
	}
}

func (c *appendBatchCoordinator) canQueue(state *appendBatchState, size int) bool {
	if len(state.queue) >= c.maxCount {
		return false
	}
	if size > c.maxBytes {
		return len(state.queue) == 0 && !state.active
	}
	return state.pendingB+size <= c.maxBytes
}

func (c *appendBatchCoordinator) enqueueLocked(state *appendBatchState, req *appendBatchRequest, size int, immediate bool) {
	req.size = size
	req.immediate = immediate
	req.queuedAt = time.Now()
	state.queue = append(state.queue, req)
	state.pendingB += size
	c.notifyLocked(state)
}

func (c *appendBatchCoordinator) run(state *appendBatchState) {
	for {
		state.mu.Lock()
		if len(state.queue) == 0 {
			if state.immediateWaiters > 0 {
				changed := state.changed
				state.mu.Unlock()
				<-changed
				continue
			}
			state.mu.Unlock()
			if c.removeIdle(state) {
				return
			}
			continue
		}

		first := state.queue[0]
		if first.operation != nil || first.immediate {
			state.queue = state.queue[1:]
			state.pendingB -= first.size
			state.active = true
			state.lastStart = time.Now()
			c.notifyLocked(state)
			state.mu.Unlock()
			var err error
			if first.operation != nil {
				c.stats.immediateFences.Add(1)
				err = first.operation(state.path)
			} else {
				err = c.writeImmediate(state.path, [][]byte{first.line})
			}
			c.finishRequest(first, err)
			state.mu.Lock()
			state.active = false
			c.notifyLocked(state)
			state.mu.Unlock()
			continue
		}
		boundary := len(state.queue)
		for i, req := range state.queue {
			if req.operation != nil || req.immediate {
				boundary = i
				break
			}
		}
		flushRequested := state.immediateWaiters > 0
		queueLen := len(state.queue)
		due := first.queuedAt.Add(c.interval)
		if cadence := state.lastStart.Add(c.interval); cadence.After(due) {
			due = cadence
		}
		changed := state.changed
		state.mu.Unlock()
		if !flushRequested && boundary == queueLen && time.Now().Before(due) {
			timer := time.NewTimer(time.Until(due))
			select {
			case <-changed:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				continue
			case <-timer.C:
			}
		}

		state.mu.Lock()
		if len(state.queue) == 0 {
			state.mu.Unlock()
			continue
		}
		if state.queue[0].operation != nil || state.queue[0].immediate {
			state.mu.Unlock()
			continue
		}
		batch, lines := c.takeBatchLocked(state)
		state.active = true
		state.lastStart = time.Now()
		c.notifyLocked(state)
		state.mu.Unlock()
		err := c.writeNormal(state.path, lines)
		for _, req := range batch {
			c.finishRequest(req, err)
		}
		state.mu.Lock()
		state.active = false
		c.notifyLocked(state)
		state.mu.Unlock()
	}
}

func (c *appendBatchCoordinator) takeBatchLocked(state *appendBatchState) ([]*appendBatchRequest, [][]byte) {
	var requests []*appendBatchRequest
	var lines [][]byte
	bytes := 0
	for len(state.queue) > 0 && len(requests) < c.maxCount {
		req := state.queue[0]
		if req.operation != nil || req.immediate || len(requests) > 0 && bytes+len(req.line) > c.maxBytes {
			break
		}
		requests = append(requests, req)
		lines = append(lines, req.line)
		bytes += len(req.line)
		state.queue = state.queue[1:]
		state.pendingB -= req.size
		if bytes > c.maxBytes {
			break
		}
	}
	return requests, lines
}

func (c *appendBatchCoordinator) removeIdle(state *appendBatchState) bool {
	c.mu.Lock()
	state.mu.Lock()
	if len(state.queue) != 0 || state.active || state.immediateWaiters != 0 {
		state.mu.Unlock()
		c.mu.Unlock()
		return false
	}
	state.closed = true
	c.notifyLocked(state)
	if c.states[state.path] == state {
		delete(c.states, state.path)
	}
	state.mu.Unlock()
	c.mu.Unlock()
	<-c.fileSlots
	return true
}

func (c *appendBatchCoordinator) finishRequest(req *appendBatchRequest, err error) {
	req.result <- err
}

func (c *appendBatchCoordinator) writeNormal(path string, lines [][]byte) error {
	c.stats.normalAttempts.Add(1)
	err := c.write(path, lines)
	if err != nil {
		c.stats.normalFailures.Add(1)
		if errors.Is(err, io.ErrShortWrite) {
			c.stats.normalShortWrite.Add(1)
		}
		return err
	}
	c.stats.normalSuccesses.Add(1)
	c.stats.normalRecords.Add(uint64(len(lines)))
	c.stats.normalBytes.Add(uint64(appendLinesSize(lines)))
	return nil
}

func (c *appendBatchCoordinator) writeImmediate(path string, lines [][]byte) error {
	c.stats.immediateAttempts.Add(1)
	err := c.write(path, lines)
	if err != nil {
		c.stats.immediateFailures.Add(1)
		if errors.Is(err, io.ErrShortWrite) {
			c.stats.immediateShortWrite.Add(1)
		}
		return err
	}
	c.stats.immediateSuccesses.Add(1)
	c.stats.immediateRecords.Add(uint64(len(lines)))
	c.stats.immediateBytes.Add(uint64(appendLinesSize(lines)))
	return nil
}

func appendLinesSize(lines [][]byte) int {
	total := 0
	for _, line := range lines {
		total += len(line)
	}
	return total
}

func (c *appendBatchCoordinator) submitLine(path string, line []byte, immediate bool) error {
	// Callers pass immutable encoded records and retain them until this synchronous
	// call returns, so blocked admission does not allocate an uncharged copy.
	return c.submit(path, &appendBatchRequest{line: line, size: len(line)}, immediate)
}

func (c *appendBatchCoordinator) submitOperation(path string, retainedBytes int, fn func(string) error) error {
	return c.submit(path, &appendBatchRequest{size: retainedBytes, operation: fn}, true)
}

func (c *appendBatchCoordinator) notifyLocked(state *appendBatchState) {
	close(state.changed)
	state.changed = make(chan struct{})
}

// AppendLine synchronously appends an already encoded newline-terminated record.
// The caller must keep line immutable until this call returns.
func AppendLine(path string, line []byte) error {
	return appendBatches.submitLine(path, line, false)
}

// AppendLineImmediate synchronously appends a record after earlier accepted
// writes to path, without the normal batching collection interval.
func AppendLineImmediate(path string, line []byte) error {
	return appendBatches.submitLine(path, line, true)
}

func writeAppendLines(path string, lines [][]byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return &AppendError{Stage: AppendStageDirectory, Err: err}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return &AppendError{Stage: AppendStageOpen, Err: err}
	}
	var data []byte
	if len(lines) == 1 {
		data = lines[0]
	} else {
		data = make([]byte, 0, appendLinesSize(lines))
		for _, line := range lines {
			data = append(data, line...)
		}
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	closeErr := f.Close()
	if writeErr != nil {
		return &AppendError{Stage: AppendStageWrite, Err: writeErr}
	}
	if closeErr != nil {
		return &AppendError{Stage: AppendStageClose, Err: closeErr}
	}
	return nil
}
