package superagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainsuperagent "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/superagent"
)

type RunQueueStore interface {
	ListRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error)
	SaveRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) error
	SaveTraceEvent(ctx context.Context, item domainsuperagent.TraceEvent) error
}

type RunQueueProcessor interface {
	ProcessRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) (string, error)
}

type RunQueueProcessorFunc func(ctx context.Context, item domainsuperagent.RunQueueItem) (string, error)

func (f RunQueueProcessorFunc) ProcessRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) (string, error) {
	return f(ctx, item)
}

type RunQueueSchedulerOptions struct {
	ClaimLimit int
	Interval   time.Duration
	Now        func() time.Time
}

type RunQueueScheduler struct {
	store     RunQueueStore
	processor RunQueueProcessor
	options   RunQueueSchedulerOptions
}

func NewRunQueueScheduler(store RunQueueStore, processor RunQueueProcessor, options RunQueueSchedulerOptions) *RunQueueScheduler {
	if options.ClaimLimit <= 0 {
		options.ClaimLimit = 1
	}
	if options.Interval <= 0 {
		options.Interval = time.Minute
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	return &RunQueueScheduler{
		store:     store,
		processor: processor,
		options:   options,
	}
}

func (s *RunQueueScheduler) RunOnce(ctx context.Context) (int, error) {
	if s == nil || s.store == nil || s.processor == nil {
		return 0, fmt.Errorf("run queue scheduler is not configured")
	}
	now := s.options.Now().UTC()
	items, err := s.store.ListRunQueueItems(ctx, 500)
	if err != nil {
		return 0, err
	}
	processed := 0
	for processed < s.options.ClaimLimit {
		item, ok := nextDueRunQueueItem(items, now)
		if !ok {
			return processed, nil
		}
		item.Status = "claimed"
		item.ClaimedAt = now
		if err := s.store.SaveRunQueueItem(ctx, item); err != nil {
			return processed, err
		}
		s.saveTrace(ctx, item, "run_queue_claimed", "claimed", item.Action)
		summary, execErr := s.processor.ProcessRunQueueItem(ctx, item)
		item.CompletedAt = s.options.Now().UTC()
		if execErr != nil {
			item.Status = "failed"
			item.Reason = execErr.Error()
			_ = s.store.SaveRunQueueItem(ctx, item)
			s.saveTrace(ctx, item, "run_queue_failed", "failed", execErr.Error())
			return processed, execErr
		}
		item.Status = "completed"
		item.Reason = strings.TrimSpace(summary)
		if err := s.store.SaveRunQueueItem(ctx, item); err != nil {
			return processed, err
		}
		s.saveTrace(ctx, item, "run_queue_completed", "completed", summary)
		processed++
		items, err = s.store.ListRunQueueItems(ctx, 500)
		if err != nil {
			return processed, err
		}
	}
	return processed, nil
}

func (s *RunQueueScheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(s.options.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = s.RunOnce(ctx)
			}
		}
	}()
}

func (s *RunQueueScheduler) saveTrace(ctx context.Context, item domainsuperagent.RunQueueItem, eventType, status, summary string) {
	if s == nil || s.store == nil {
		return
	}
	now := s.options.Now().UTC()
	trace := domainsuperagent.TraceEvent{
		EventID:        fmt.Sprintf("trace-run-queue-%d", now.UnixNano()),
		RunID:          item.RunID,
		EventType:      eventType,
		Actor:          "RunQueueScheduler",
		PayloadSummary: strings.TrimSpace(summary),
		Status:         status,
		CreatedAt:      now,
	}
	_ = s.store.SaveTraceEvent(ctx, trace)
}

func nextDueRunQueueItem(items []domainsuperagent.RunQueueItem, now time.Time) (domainsuperagent.RunQueueItem, bool) {
	var selected domainsuperagent.RunQueueItem
	found := false
	for _, item := range items {
		if item.Status != "queued" {
			continue
		}
		if !item.NotBefore.IsZero() && item.NotBefore.After(now) {
			continue
		}
		if !found || item.Priority > selected.Priority || (item.Priority == selected.Priority && item.CreatedAt.Before(selected.CreatedAt)) {
			selected = item
			found = true
		}
	}
	return selected, found
}
