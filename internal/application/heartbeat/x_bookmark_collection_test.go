package heartbeat

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blockingXBookmarkCollector struct {
	calls    atomic.Int32
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
	report   XBookmarkCollectionReport
	err      error
}

func (c *blockingXBookmarkCollector) Collect(ctx context.Context) (XBookmarkCollectionReport, error) {
	c.calls.Add(1)
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		select {
		case c.canceled <- struct{}{}:
		default:
		}
		return XBookmarkCollectionReport{}, ctx.Err()
	case <-c.release:
		return c.report, c.err
	}
}

func TestXBookmarkCollectionRunsAsynchronouslyAndSkipsOverlap(t *testing.T) {
	collector := &blockingXBookmarkCollector{
		started:  make(chan struct{}, 2),
		release:  make(chan struct{}),
		canceled: make(chan struct{}, 1),
		report: XBookmarkCollectionReport{
			Collected: 3,
			Imported:  3,
		},
	}
	listener := &recordingEventListener{}
	svc := NewHeartbeatService(&mockWorkerAgent{response: "HEARTBEAT_OK"}, nil, t.TempDir(), 30).
		WithEventListener(listener).
		WithXBookmarkCollection(collector, time.Hour, time.Minute, true)

	if !svc.startXBookmarkCollection() {
		t.Fatal("first collection should start")
	}
	select {
	case <-collector.started:
	case <-time.After(time.Second):
		t.Fatal("collector did not start")
	}
	if svc.startXBookmarkCollection() {
		t.Fatal("overlapping collection must be skipped")
	}
	if got := collector.calls.Load(); got != 1 {
		t.Fatalf("collector calls=%d, want 1", got)
	}
	close(collector.release)
	svc.waitForXBookmarkCollection()

	var started, skipped, completed bool
	for _, event := range listener.events {
		switch event.Type {
		case "heartbeat.x_bookmarks.started":
			started = true
		case "heartbeat.x_bookmarks.skipped_running":
			skipped = true
		case "heartbeat.x_bookmarks.completed":
			completed = strings.Contains(event.Content, "collected=3") && strings.Contains(event.Content, "imported=3")
		}
	}
	if !started || !skipped || !completed {
		t.Fatalf("missing collection events: %+v", listener.events)
	}
}

func TestXBookmarkCollectionTimeoutCancelsCollectorAndEmitsError(t *testing.T) {
	collector := &blockingXBookmarkCollector{
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		canceled: make(chan struct{}, 1),
	}
	listener := &recordingEventListener{}
	svc := NewHeartbeatService(&mockWorkerAgent{response: "HEARTBEAT_OK"}, nil, t.TempDir(), 30).
		WithEventListener(listener).
		WithXBookmarkCollection(collector, time.Hour, 20*time.Millisecond, true)

	if !svc.startXBookmarkCollection() {
		t.Fatal("collection should start")
	}
	select {
	case <-collector.canceled:
	case <-time.After(time.Second):
		t.Fatal("collector was not canceled at timeout")
	}
	svc.waitForXBookmarkCollection()
	for _, event := range listener.events {
		if event.Type == "heartbeat.x_bookmarks.error" && strings.Contains(event.Content, context.DeadlineExceeded.Error()) {
			return
		}
	}
	t.Fatalf("timeout error event not emitted: %+v", listener.events)
}

func TestStopXBookmarkCollectionCancelsInFlightRun(t *testing.T) {
	collector := &blockingXBookmarkCollector{
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		canceled: make(chan struct{}, 1),
		err:      errors.New("unexpected"),
	}
	svc := NewHeartbeatService(&mockWorkerAgent{response: "HEARTBEAT_OK"}, nil, t.TempDir(), 30).
		WithXBookmarkCollection(collector, time.Hour, time.Minute, true)
	if !svc.startXBookmarkCollection() {
		t.Fatal("collection should start")
	}
	select {
	case <-collector.started:
	case <-time.After(time.Second):
		t.Fatal("collector did not start")
	}
	svc.stopXBookmarkCollection()
	select {
	case <-collector.canceled:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel collector")
	}
}

func TestXBookmarkCollectionRunsOnStartAndAtItsOwnInterval(t *testing.T) {
	collector := &blockingXBookmarkCollector{
		started:  make(chan struct{}, 8),
		release:  make(chan struct{}),
		canceled: make(chan struct{}, 1),
	}
	close(collector.release)
	svc := NewHeartbeatService(&mockWorkerAgent{response: "HEARTBEAT_OK"}, nil, t.TempDir(), 30).
		WithXBookmarkCollection(collector, 20*time.Millisecond, time.Second, true)
	svc.Start()
	t.Cleanup(svc.Stop)
	deadline := time.After(time.Second)
	for collector.calls.Load() < 2 {
		select {
		case <-collector.started:
		case <-deadline:
			t.Fatalf("collection calls=%d, want at least 2", collector.calls.Load())
		}
	}
}
