package voiceinput

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestSideEffectsDoesNotBlockWhenFull(t *testing.T) {
	block := make(chan struct{})
	var recorded atomic.Int64
	queue := NewSideEffects(1, 3*time.Second)
	defer close(block)

	queue.Enqueue(SideEffect{Name: "first", Run: func() error {
		recorded.Add(1)
		<-block
		return nil
	}})
	queue.Enqueue(SideEffect{Name: "second", Run: func() error {
		recorded.Add(1)
		return nil
	}})
	started := time.Now()
	queue.Enqueue(SideEffect{Name: "third", Run: func() error {
		recorded.Add(1)
		return nil
	}})
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("enqueue should not block when queue is full: elapsed=%s", elapsed)
	}
}

func TestSideEffectsRunsQueuedItem(t *testing.T) {
	done := make(chan struct{})
	queue := NewSideEffects(1, 3*time.Second)

	if ok := queue.Enqueue(SideEffect{Name: "event_log", Run: func() error {
		close(done)
		return nil
	}}); !ok {
		t.Fatal("expected enqueue to succeed")
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected queued side effect to run")
	}
}
