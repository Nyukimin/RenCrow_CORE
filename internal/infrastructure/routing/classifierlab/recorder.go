package classifierlab

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

type AsyncRecorder struct {
	sink        RecordSink
	queue       chan ComparisonRecord
	closing     chan struct{}
	workerCtx   context.Context
	cancel      context.CancelFunc
	stopService func() bool
	closed      atomic.Bool
	closeOnce   sync.Once
	queueMu     sync.RWMutex
	dropped     atomic.Uint64
	failed      atomic.Uint64
	done        chan struct{}
}

// NewAsyncRecorder starts one bounded writer. The service context contributes
// cancellation only; its values are not passed to the sink.
func NewAsyncRecorder(serviceCtx context.Context, capacity int, sink RecordSink) (*AsyncRecorder, error) {
	if serviceCtx == nil {
		return nil, ErrNilServiceContext
	}
	if capacity < 1 || capacity > 4096 {
		return nil, errors.New("recorder capacity must be between 1 and 4096")
	}
	if sink == nil {
		return nil, errors.New("recorder sink is nil")
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	recorder := &AsyncRecorder{
		sink:      sink,
		queue:     make(chan ComparisonRecord, capacity),
		closing:   make(chan struct{}),
		workerCtx: workerCtx,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	recorder.stopService = context.AfterFunc(serviceCtx, cancel)
	go recorder.run()
	return recorder, nil
}

// TryRecord clones the bounded record and enqueues it without waiting for the
// sink. It returns false when the queue is full or closing.
func (r *AsyncRecorder) TryRecord(record ComparisonRecord) bool {
	if r == nil || r.closed.Load() {
		if r != nil {
			r.dropped.Add(1)
		}
		return false
	}
	copy := cloneComparisonRecord(record)
	r.queueMu.RLock()
	defer r.queueMu.RUnlock()
	if r.closed.Load() {
		r.dropped.Add(1)
		return false
	}
	select {
	case r.queue <- copy:
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

func (r *AsyncRecorder) Dropped() uint64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

func (r *AsyncRecorder) Failed() uint64 {
	if r == nil {
		return 0
	}
	return r.failed.Load()
}

// Done reports when the writer has stopped and all accepted records have
// either reached the sink or been counted as dropped.
func (r *AsyncRecorder) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.done
}

// Close stops new writes and waits for the bounded queue to drain. On caller
// cancellation it cancels the sink context and returns promptly.
func (r *AsyncRecorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.closeOnce.Do(func() {
		r.queueMu.Lock()
		r.closed.Store(true)
		close(r.closing)
		r.queueMu.Unlock()
	})
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		r.cancel()
		return ctx.Err()
	}
}

func (r *AsyncRecorder) run() {
	defer close(r.done)
	defer r.cancel()
	defer func() {
		if r.stopService != nil {
			r.stopService()
		}
	}()
	for {
		select {
		case <-r.workerCtx.Done():
			r.dropQueued()
			return
		case <-r.closing:
			r.flushQueued()
			return
		case record := <-r.queue:
			r.write(record)
		}
	}
}

func (r *AsyncRecorder) flushQueued() {
	for {
		select {
		case record := <-r.queue:
			r.write(record)
		case <-r.workerCtx.Done():
			r.dropQueued()
			return
		default:
			return
		}
	}
}

func (r *AsyncRecorder) dropQueued() {
	for {
		select {
		case <-r.queue:
			r.dropped.Add(1)
		default:
			return
		}
	}
}

func (r *AsyncRecorder) write(record ComparisonRecord) {
	failed := false
	func() {
		defer func() {
			if recover() != nil {
				failed = true
			}
		}()
		if err := r.sink(r.workerCtx, record); err != nil {
			failed = true
		}
	}()
	if failed {
		r.failed.Add(1)
	}
}

var _ ComparisonRecorder = (*AsyncRecorder)(nil)
