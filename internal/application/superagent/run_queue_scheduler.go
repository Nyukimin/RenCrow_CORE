package superagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type RunQueueStore interface {
	ListRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error)
	SaveRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) error
	modulecore.EventAppender
}

type RunQueueLeaseStore interface {
	ClaimNextRunQueueItem(context.Context, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
	RenewRunQueueLease(context.Context, string, string, time.Time) (bool, error)
	CompleteRunQueueItem(context.Context, string, string, string, string, time.Time) (bool, error)
}

type RunQueueAgentRunStore interface {
	ListAgentRuns(context.Context, int) ([]domainsuperagent.AgentRun, error)
	SaveAgentRun(context.Context, domainsuperagent.AgentRun) error
}

type RunQueueProcessor interface {
	ProcessRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) (string, error)
}

type RunQueueProcessorFunc func(ctx context.Context, item domainsuperagent.RunQueueItem) (string, error)

func (f RunQueueProcessorFunc) ProcessRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) (string, error) {
	return f(ctx, item)
}

type RunQueueSchedulerOptions struct {
	ClaimLimit    int
	Interval      time.Duration
	LeaseDuration time.Duration
	Now           func() time.Time
	LeaseToken    func() (string, error)
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
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = 2 * time.Minute
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.LeaseToken == nil {
		options.LeaseToken = newRunQueueLeaseToken
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
	processed := 0
	for processed < s.options.ClaimLimit {
		token, err := s.options.LeaseToken()
		if err != nil {
			return processed, err
		}
		item, err := s.claimNext(ctx, now, token)
		if err != nil {
			return processed, err
		}
		if item == nil {
			return processed, nil
		}
		traceID := modulecore.NewTraceID()
		claimedEventID, err := s.saveTrace(ctx, *item, traceID, "", "run_queue.claimed", "claimed", item.Action)
		if err != nil {
			return processed, err
		}
		summary, execErr := s.processWithHeartbeat(ctx, *item)
		completedAt := s.options.Now().UTC()
		if execErr != nil {
			if err := s.complete(ctx, *item, "failed", execErr.Error(), completedAt); err != nil {
				return processed, err
			}
			if err := s.completeAgentRun(ctx, *item, "failed", execErr.Error(), completedAt); err != nil {
				return processed, err
			}
			if _, eventErr := s.saveTrace(ctx, *item, traceID, claimedEventID, "run_queue.failed", "failed", execErr.Error()); eventErr != nil {
				return processed, eventErr
			}
			return processed, execErr
		}
		if err := s.complete(ctx, *item, "completed", strings.TrimSpace(summary), completedAt); err != nil {
			return processed, err
		}
		if err := s.completeAgentRun(ctx, *item, "completed", strings.TrimSpace(summary), completedAt); err != nil {
			return processed, err
		}
		if _, err := s.saveTrace(ctx, *item, traceID, claimedEventID, "run_queue.completed", "completed", summary); err != nil {
			return processed, err
		}
		processed++
		now = s.options.Now().UTC()
	}
	return processed, nil
}

func (s *RunQueueScheduler) completeAgentRun(ctx context.Context, item domainsuperagent.RunQueueItem, status, summary string, at time.Time) error {
	store, ok := s.store.(RunQueueAgentRunStore)
	if !ok || strings.TrimSpace(item.RunID) == "" {
		return nil
	}
	runs, err := store.ListAgentRuns(ctx, 500)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.RunID != item.RunID {
			continue
		}
		run.Status, run.Summary, run.CompletedAt = status, summary, at
		return store.SaveAgentRun(ctx, run)
	}
	return nil
}

func (s *RunQueueScheduler) claimNext(ctx context.Context, now time.Time, token string) (*domainsuperagent.RunQueueItem, error) {
	leaseUntil := now.Add(s.options.LeaseDuration)
	if store, ok := s.store.(RunQueueLeaseStore); ok {
		return store.ClaimNextRunQueueItem(ctx, now, leaseUntil, token)
	}
	items, err := s.store.ListRunQueueItems(ctx, 500)
	if err != nil {
		return nil, err
	}
	item, ok := nextDueRunQueueItem(items, now)
	if !ok {
		return nil, nil
	}
	item.Status, item.ClaimedAt, item.LeaseToken, item.LeaseUntil = "claimed", now, token, leaseUntil
	item.AttemptCount++
	item.CompletedAt = time.Time{}
	if err := s.store.SaveRunQueueItem(ctx, item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *RunQueueScheduler) processWithHeartbeat(ctx context.Context, item domainsuperagent.RunQueueItem) (string, error) {
	store, ok := s.store.(RunQueueLeaseStore)
	if !ok {
		return s.processor.ProcessRunQueueItem(ctx, item)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatErr := make(chan error, 1)
	interval := s.options.LeaseDuration / 3
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				heartbeatErr <- nil
				return
			case <-ticker.C:
				ok, err := store.RenewRunQueueLease(runCtx, item.QueueID, item.LeaseToken, s.options.Now().UTC().Add(s.options.LeaseDuration))
				if err != nil || !ok {
					if err == nil {
						err = fmt.Errorf("run queue lease lost")
					}
					heartbeatErr <- err
					cancel()
					return
				}
			}
		}
	}()
	summary, err := s.processor.ProcessRunQueueItem(runCtx, item)
	cancel()
	if heartbeat := <-heartbeatErr; heartbeat != nil {
		return "", heartbeat
	}
	return summary, err
}

func (s *RunQueueScheduler) complete(ctx context.Context, item domainsuperagent.RunQueueItem, status, reason string, at time.Time) error {
	if store, ok := s.store.(RunQueueLeaseStore); ok {
		completed, err := store.CompleteRunQueueItem(ctx, item.QueueID, item.LeaseToken, status, reason, at)
		if err != nil {
			return err
		}
		if !completed {
			return fmt.Errorf("run queue lease lost before completion")
		}
		return nil
	}
	item.Status, item.Reason, item.CompletedAt = status, reason, at
	item.LeaseToken, item.LeaseUntil = "", time.Time{}
	return s.store.SaveRunQueueItem(ctx, item)
}

func newRunQueueLeaseToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create run queue lease token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
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

func (s *RunQueueScheduler) saveTrace(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID, causationEventID modulecore.EventID, eventType, status, summary string) (modulecore.EventID, error) {
	if s == nil || s.store == nil {
		return "", fmt.Errorf("run queue event store is not configured")
	}
	now := s.options.Now().UTC()
	event := modulecore.NewEventEnvelope(traceID, causationEventID, nil, "superagent", eventType, now, map[string]any{
		"queue_reference": item.QueueID, "run_reference": item.RunID,
		"actor_label": "RunQueueScheduler", "status": status, "summary": strings.TrimSpace(summary),
	})
	if err := s.store.Append(ctx, event); err != nil {
		return "", fmt.Errorf("append run queue event: %w", err)
	}
	return event.EventID, nil
}

func nextDueRunQueueItem(items []domainsuperagent.RunQueueItem, now time.Time) (domainsuperagent.RunQueueItem, bool) {
	var selected domainsuperagent.RunQueueItem
	found := false
	for _, item := range items {
		if item.Status != "queued" && !(item.Status == "claimed" && !item.LeaseUntil.After(now)) {
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
