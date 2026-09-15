package task

import (
	"context"
	"errors"
	"os"
	"sync"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	errTaskExecutionFenceScope = errors.New("task execution fence scope mismatch")
	errTaskExecutionFenceEntry = errors.New("task execution fence entry is unavailable")
)

// taskExecutionFence is an owner-local, exclusive gate for one Task. It is
// deliberately kept outside the JSONL batch lock: a leaf effect may hold this
// gate while the batch lock is released, allowing unrelated Tasks to commit.
type taskExecutionFence struct {
	mu      sync.Mutex
	entries map[modulecore.TaskID]*taskExecutionFenceEntry
}

type taskExecutionFenceEntry struct {
	held    bool
	refs    int
	changed chan struct{}
}

type taskExecutionLease struct {
	fence *taskExecutionFence
	task  modulecore.TaskID
	entry *taskExecutionFenceEntry
	once  sync.Once
}

func newTaskExecutionFence() *taskExecutionFence {
	return &taskExecutionFence{entries: make(map[modulecore.TaskID]*taskExecutionFenceEntry)}
}

func (f *taskExecutionFence) acquire(ctx context.Context, taskID modulecore.TaskID) (*taskExecutionLease, error) {
	if f == nil {
		return nil, errTaskExecutionFenceEntry
	}
	if ctx == nil {
		return nil, errors.New("task execution fence context is nil")
	}
	if err := taskID.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f.mu.Lock()
	if f.entries == nil {
		f.entries = make(map[modulecore.TaskID]*taskExecutionFenceEntry)
	}
	entry := f.entries[taskID]
	if entry == nil {
		entry = &taskExecutionFenceEntry{changed: make(chan struct{})}
		f.entries[taskID] = entry
	}
	entry.refs++
	for entry.held {
		changed := entry.changed
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			f.dropWaiter(taskID, entry)
			return nil, ctx.Err()
		case <-changed:
		}
		f.mu.Lock()
	}
	if err := ctx.Err(); err != nil {
		if entry.refs > 0 {
			entry.refs--
		}
		if entry.refs == 0 && !entry.held {
			delete(f.entries, taskID)
		}
		f.mu.Unlock()
		return nil, err
	}
	entry.held = true
	lease := &taskExecutionLease{fence: f, task: taskID, entry: entry}
	f.mu.Unlock()
	return lease, nil
}

func (f *taskExecutionFence) dropWaiter(taskID modulecore.TaskID, entry *taskExecutionFenceEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if current := f.entries[taskID]; current != entry {
		return
	}
	if entry.refs > 0 {
		entry.refs--
	}
	if entry.refs == 0 && !entry.held {
		delete(f.entries, taskID)
	}
}

func (lease *taskExecutionLease) release() {
	if lease == nil || lease.fence == nil || lease.entry == nil {
		return
	}
	lease.once.Do(func() {
		f := lease.fence
		f.mu.Lock()
		if current := f.entries[lease.task]; current != lease.entry {
			f.mu.Unlock()
			return
		}
		lease.entry.held = false
		if lease.entry.refs > 0 {
			lease.entry.refs--
		}
		close(lease.entry.changed)
		lease.entry.changed = make(chan struct{})
		if lease.entry.refs == 0 {
			delete(f.entries, lease.task)
		}
		f.mu.Unlock()
	})
}

func (f *taskExecutionFence) hasActive(taskIDs map[modulecore.TaskID]struct{}, owner *taskExecutionLease) modulecore.TaskID {
	if f == nil || len(taskIDs) == 0 {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for taskID := range taskIDs {
		entry := f.entries[taskID]
		if entry == nil || !entry.held {
			continue
		}
		if owner != nil && owner.task == taskID && owner.entry == entry {
			continue
		}
		return taskID
	}
	return ""
}

// taskStoreLifecycle rejects new scoped operations once close starts and lets
// Close wait for every admitted operation, including fence waiters.
type taskStoreLifecycle struct {
	mu      sync.Mutex
	closing bool
	closed  bool
	active  int
	changed chan struct{}
	done    chan struct{}
}

func newTaskStoreLifecycle() *taskStoreLifecycle {
	return &taskStoreLifecycle{changed: make(chan struct{}), done: make(chan struct{})}
}

func (l *taskStoreLifecycle) admit(ctx context.Context) (func(), error) {
	if l == nil {
		return nil, os.ErrClosed
	}
	if ctx == nil {
		return nil, errors.New("task store lifecycle context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closing || l.closed {
		l.mu.Unlock()
		return nil, os.ErrClosed
	}
	l.active++
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if l.active > 0 {
				l.active--
			}
			if l.active == 0 {
				close(l.changed)
				l.changed = make(chan struct{})
			}
			l.mu.Unlock()
		})
	}, nil
}

func (l *taskStoreLifecycle) beginClose() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return false
	}
	if l.closing {
		done := l.done
		l.mu.Unlock()
		<-done
		return false
	}
	l.closing = true
	l.mu.Unlock()
	return true
}

func (l *taskStoreLifecycle) wait() {
	if l == nil {
		return
	}
	for {
		l.mu.Lock()
		if l.active == 0 {
			l.mu.Unlock()
			return
		}
		changed := l.changed
		l.mu.Unlock()
		<-changed
	}
}

func (l *taskStoreLifecycle) finishClose() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.done)
	}
	l.mu.Unlock()
}
