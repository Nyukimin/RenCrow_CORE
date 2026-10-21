package worldprojection

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const DefaultProjectionInterval = time.Minute

// ProjectorRunner is the full-snapshot producer used by the runtime loop.
type ProjectorRunner interface {
	Sync(context.Context) (SyncResult, error)
}

// OutboxDrainer is the bounded delivery pass used by the runtime loop.
type OutboxDrainer interface {
	Drain(context.Context) (DrainResult, error)
}

// Service runs the CORE projection loop. It performs one snapshot/drain pass at
// startup and then on the interval or an explicit Notify. A failed pass is
// reported to OnError and the loop continues; it never turns a transient WORLD
// outage into a CORE shutdown or an in-memory retry queue.
type Service struct {
	Projector ProjectorRunner
	Worker    OutboxDrainer
	Interval  time.Duration
	Notify    <-chan struct{}
	OnError   func(error)
}

// Tick performs one projection and delivery pass. Drain still runs when Sync
// fails so already-durable records are not stranded behind a source read error.
func (s *Service) Tick(ctx context.Context) (SyncResult, DrainResult, error) {
	if s == nil || s.Projector == nil || s.Worker == nil {
		return SyncResult{}, DrainResult{}, errors.New("world projection service: projector and worker are required")
	}
	if ctx == nil {
		return SyncResult{}, DrainResult{}, errors.New("world projection service: context is nil")
	}
	syncResult, syncErr := s.Projector.Sync(ctx)
	drainResult, drainErr := s.Worker.Drain(ctx)
	if syncErr != nil {
		return syncResult, drainResult, fmt.Errorf("world projection service: sync: %w", syncErr)
	}
	if drainErr != nil {
		return syncResult, drainResult, fmt.Errorf("world projection service: drain: %w", drainErr)
	}
	return syncResult, drainResult, nil
}

// Run blocks until ctx is cancelled. It performs a startup pass before waiting
// so a restart immediately reconciles a Task update that committed just before
// the previous process stopped.
func (s *Service) Run(ctx context.Context) error {
	if s == nil || s.Projector == nil || s.Worker == nil {
		return errors.New("world projection service: projector and worker are required")
	}
	if ctx == nil {
		return errors.New("world projection service: context is nil")
	}
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultProjectionInterval
	}
	report := func(err error) {
		if err != nil && s.OnError != nil {
			s.OnError(err)
		}
	}
	run := func() {
		_, _, err := s.Tick(ctx)
		report(err)
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			run()
		case <-s.Notify:
			run()
		}
	}
}
