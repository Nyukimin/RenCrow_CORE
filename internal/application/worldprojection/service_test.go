package worldprojection

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeServiceProjector struct {
	syncs atomic.Int32
	err   error
}

func (f *fakeServiceProjector) Sync(context.Context) (SyncResult, error) {
	f.syncs.Add(1)
	return SyncResult{}, f.err
}

type fakeServiceDrainer struct {
	drains atomic.Int32
	err    error
}

func (f *fakeServiceDrainer) Drain(context.Context) (DrainResult, error) {
	f.drains.Add(1)
	return DrainResult{}, f.err
}

func TestServiceTickSyncsThenDrains(t *testing.T) {
	projector := &fakeServiceProjector{}
	worker := &fakeServiceDrainer{}
	service := &Service{Projector: projector, Worker: worker}
	if _, _, err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if projector.syncs.Load() != 1 || worker.drains.Load() != 1 {
		t.Fatalf("syncs = %d, drains = %d", projector.syncs.Load(), worker.drains.Load())
	}
}

func TestServiceTickDrainsWhenSyncFails(t *testing.T) {
	syncErr := errors.New("source unavailable")
	projector := &fakeServiceProjector{err: syncErr}
	worker := &fakeServiceDrainer{}
	service := &Service{Projector: projector, Worker: worker}
	_, _, err := service.Tick(context.Background())
	if !errors.Is(err, syncErr) {
		t.Fatalf("Tick() error = %v, want %v", err, syncErr)
	}
	if worker.drains.Load() != 1 {
		t.Fatalf("drains = %d, want 1", worker.drains.Load())
	}
}

func TestServiceRunPerformsStartupPassAndWakesOnNotify(t *testing.T) {
	projector := &fakeServiceProjector{}
	worker := &fakeServiceDrainer{}
	notify := make(chan struct{}, 1)
	service := &Service{Projector: projector, Worker: worker, Interval: time.Hour, Notify: notify}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	waitFor(t, func() bool { return projector.syncs.Load() == 1 && worker.drains.Load() == 1 })
	notify <- struct{}{}
	waitFor(t, func() bool { return projector.syncs.Load() == 2 && worker.drains.Load() == 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestServiceRunReportsErrorsAndKeepsRunning(t *testing.T) {
	syncErr := errors.New("temporary source failure")
	projector := &fakeServiceProjector{err: syncErr}
	worker := &fakeServiceDrainer{}
	notify := make(chan struct{}, 1)
	reported := make(chan error, 4)
	service := &Service{
		Projector: projector, Worker: worker, Interval: time.Hour, Notify: notify,
		OnError: func(err error) { reported <- err },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case err := <-reported:
		if !errors.Is(err, syncErr) {
			t.Fatalf("reported error = %v, want %v", err, syncErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service did not report startup error")
	}
	if projector.syncs.Load() != 1 || worker.drains.Load() != 1 {
		t.Fatalf("syncs = %d, drains = %d", projector.syncs.Load(), worker.drains.Load())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestServiceRejectsIncompleteConfiguration(t *testing.T) {
	if _, _, err := (&Service{}).Tick(context.Background()); err == nil {
		t.Fatal("Tick() error = nil")
	}
	if err := (&Service{}).Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
