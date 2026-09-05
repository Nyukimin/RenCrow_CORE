package superagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type RunQueueStore interface {
	ListRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error)
	SaveRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) error
	modulecore.EventAppender
}

type RunQueueLeaseStore interface {
	ClaimNextRunQueueItem(context.Context, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
	AttachRunQueueRun(context.Context, string, string, modulecore.RunID) (bool, error)
	RenewRunQueueLease(context.Context, string, string, time.Time) (bool, error)
	CompleteRunQueueItem(context.Context, string, string, string, string, time.Time) (bool, error)
}

type RunQueueAgentRunStore interface {
	ListAgentRuns(context.Context, int) ([]domainsuperagent.AgentRun, error)
	SaveAgentRun(context.Context, domainsuperagent.AgentRun) error
}

type RunQueueProcessor interface {
	ProcessRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error)
}

// RunQueueTaskOwner is the only owner allowed to inspect or transition the
// canonical Task and its execution Run for a queued item. The queue stores the
// intent and lease; it never invents or reuses a RunID itself.
type RunQueueTaskOwner interface {
	Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	StartRunWithReason(context.Context, modulecore.TaskID, domaintask.RunStartReason) (domaintask.Run, error)
	InterruptRun(context.Context, modulecore.TaskID, modulecore.RunID, string) (domaintask.Run, error)
	Block(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
	Fail(context.Context, modulecore.TaskID, string, []string) (domaintask.Task, error)
}

type RunQueueProcessorFunc func(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error)

func (f RunQueueProcessorFunc) ProcessRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
	return f(ctx, item, traceID)
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
	taskOwner RunQueueTaskOwner
	options   RunQueueSchedulerOptions
}

func NewRunQueueScheduler(store RunQueueStore, processor RunQueueProcessor, taskOwner RunQueueTaskOwner, options RunQueueSchedulerOptions) *RunQueueScheduler {
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
		taskOwner: taskOwner,
		options:   options,
	}
}

func (s *RunQueueScheduler) RunOnce(ctx context.Context) (int, error) {
	if s == nil || s.store == nil || s.processor == nil || s.taskOwner == nil {
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
		run, err := s.startRun(ctx, item)
		if err != nil {
			var cleanupErr error
			if run.RunID != "" {
				cleanupErr = s.interruptIssuedRun(ctx, *item, run, err.Error())
			}
			taskStateErr := s.terminalizeStartFailureTask(ctx, item.TaskID, err.Error())
			blockErr := s.blockReservation(ctx, *item, err.Error(), s.options.Now().UTC())
			if blockErr != nil {
				return processed, errors.Join(err, cleanupErr, taskStateErr, fmt.Errorf("block queue item: %w", blockErr))
			}
			blocked := *item
			blocked.Status = "blocked"
			blocked.Reason = strings.TrimSpace(err.Error())
			blocked.RunID = ""
			blocked.CompletedAt = s.options.Now().UTC()
			blocked.LeaseToken, blocked.LeaseUntil = "", time.Time{}
			if _, eventErr := s.saveTrace(ctx, blocked, modulecore.NewTraceID(), "", "run_queue.blocked", "blocked", blocked.Reason); eventErr != nil {
				return processed, errors.Join(err, cleanupErr, taskStateErr, eventErr)
			}
			if cleanupErr != nil || taskStateErr != nil {
				return processed, errors.Join(err, cleanupErr, taskStateErr)
			}
			return processed, err
		}
		if leaseStore, ok := s.store.(RunQueueLeaseStore); ok {
			attached, attachErr := leaseStore.AttachRunQueueRun(ctx, item.QueueID, item.LeaseToken, run.RunID)
			if attachErr != nil {
				originalErr := fmt.Errorf("attach canonical run to run queue item: %w", attachErr)
				if cleanupErr := s.interruptIssuedRun(ctx, *item, run, originalErr.Error()); cleanupErr != nil {
					return processed, errors.Join(originalErr, cleanupErr)
				}
				return processed, originalErr
			}
			if !attached {
				// The Task owner has already issued the canonical Run, but the
				// reservation is no longer ours. Do not process the Run or emit a
				// queue/projection/event receipt that could overwrite the newer
				// claimant. Close the exact issued Run through the Task owner;
				// never transition the Task or touch the newer claimant's Run.
				originalErr := fmt.Errorf("run queue lease lost before attaching canonical run")
				if cleanupErr := s.interruptIssuedRun(ctx, *item, run, originalErr.Error()); cleanupErr != nil {
					return processed, errors.Join(originalErr, cleanupErr)
				}
				return processed, originalErr
			}
		} else {
			// The in-memory/non-lease store is a single-store test fallback;
			// retain its local state transition because there is no lease CAS.
			item.RunID = run.RunID
			item.Status = "claimed"
			if err := s.store.SaveRunQueueItem(ctx, *item); err != nil {
				return processed, fmt.Errorf("persist claimed run queue item: %w", err)
			}
		}
		item.RunID = run.RunID
		item.Status = "claimed"
		traceID := modulecore.NewTraceID()
		claimedEventID, err := s.saveTrace(ctx, *item, traceID, "", "run_queue.claimed", "claimed", item.Action)
		if err != nil {
			return processed, err
		}
		summary, execErr := s.processWithHeartbeat(ctx, *item, traceID)
		completedAt := s.options.Now().UTC()
		if execErr != nil {
			task, taskErr := s.taskOwner.Get(ctx, item.TaskID)
			if taskErr != nil {
				return processed, errors.Join(execErr, fmt.Errorf("get canonical task after run queue error: %w", taskErr))
			}
			switch task.Status {
			case domaintask.StatusWaiting:
				reason := strings.TrimSpace(task.WaitingReason)
				if reason == "" {
					reason = execErr.Error()
				}
				if err := s.complete(ctx, *item, "cancelled", reason, completedAt); err != nil {
					return processed, err
				}
				if _, eventErr := s.saveTrace(ctx, *item, traceID, claimedEventID, "run_queue.cancelled", "cancelled", reason); eventErr != nil {
					return processed, eventErr
				}
				return processed, execErr
			case domaintask.StatusRunning:
				if _, failErr := s.taskOwner.Fail(ctx, item.TaskID, execErr.Error(), nil); failErr != nil {
					return processed, fmt.Errorf("fail canonical task after run queue error: %w", failErr)
				}
			case domaintask.StatusFailed:
				// The canonical Task already records the failure. Do not issue a
				// second owner transition; finish the queue receipt below.
			default:
				return processed, fmt.Errorf("run queue processor failed while canonical task %s is %s", item.TaskID, task.Status)
			}
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
		task, err := s.taskOwner.Get(ctx, item.TaskID)
		if err != nil {
			return processed, fmt.Errorf("get canonical task after run queue success: %w", err)
		}
		if task.Status != domaintask.StatusSucceeded {
			return processed, fmt.Errorf("run queue processor succeeded while canonical task %s is %s", item.TaskID, task.Status)
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
	if !ok || item.RunID == "" {
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
		item, err := store.ClaimNextRunQueueItem(ctx, now, leaseUntil, token)
		if err != nil || item == nil {
			return item, err
		}
		// The storage claim owns only the lease reservation. A canonical Run is
		// issued below by the Task owner, so no previous RunID may cross this
		// boundary.
		item.Status = "reserved"
		item.RunID = ""
		return item, nil
	}
	items, err := s.store.ListRunQueueItems(ctx, 500)
	if err != nil {
		return nil, err
	}
	item, ok := nextDueRunQueueItem(items, now)
	if !ok {
		return nil, nil
	}
	item.Status, item.ClaimedAt, item.LeaseToken, item.LeaseUntil = "reserved", now, token, leaseUntil
	item.RunID = ""
	item.AttemptCount++
	item.CompletedAt = time.Time{}
	if err := s.store.SaveRunQueueItem(ctx, item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *RunQueueScheduler) startRun(ctx context.Context, item *domainsuperagent.RunQueueItem) (domaintask.Run, error) {
	if item == nil {
		return domaintask.Run{}, fmt.Errorf("run queue item is nil")
	}
	reason := item.RunStartReason
	if item.AttemptCount > 1 {
		reason = domaintask.RunStartReasonLeaseReacquire
	}
	run, err := s.taskOwner.StartRunWithReason(ctx, item.TaskID, reason)
	if err != nil {
		return domaintask.Run{}, fmt.Errorf("start canonical run for task %s: %w", item.TaskID, err)
	}
	if err := run.Validate(); err != nil {
		return run, fmt.Errorf("task owner returned invalid run: %w", err)
	}
	if run.TaskID != item.TaskID {
		return run, fmt.Errorf("task owner returned run for task %s, want %s", run.TaskID, item.TaskID)
	}
	if run.StartReason != reason {
		return run, fmt.Errorf("task owner returned start reason %q, want %q", run.StartReason, reason)
	}
	if run.Status != domaintask.RunStatusRunning {
		return run, fmt.Errorf("task owner returned run status %q, want running", run.Status)
	}
	return run, nil
}

func (s *RunQueueScheduler) interruptIssuedRun(ctx context.Context, item domainsuperagent.RunQueueItem, run domaintask.Run, summary string) error {
	if err := item.TaskID.Validate(); err != nil {
		return fmt.Errorf("interrupt issued run: task_id is invalid: %w", err)
	}
	if err := run.RunID.Validate(); err != nil {
		return fmt.Errorf("interrupt issued run: run_id is invalid: %w", err)
	}
	if run.TaskID != item.TaskID {
		return fmt.Errorf("interrupt issued run: run task_id %s does not match queue task_id %s", run.TaskID, item.TaskID)
	}
	if _, err := s.taskOwner.InterruptRun(ctx, item.TaskID, run.RunID, strings.TrimSpace(summary)); err != nil {
		return fmt.Errorf("interrupt issued canonical run: %w", err)
	}
	return nil
}

func (s *RunQueueScheduler) terminalizeStartFailureTask(ctx context.Context, taskID modulecore.TaskID, reason string) error {
	task, err := s.taskOwner.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get canonical task after run start error: %w", err)
	}
	if err := validateTaskOwnerState(task, taskID); err != nil {
		return err
	}
	switch task.Status {
	case domaintask.StatusRunning, domaintask.StatusWaiting:
		blocked, err := s.taskOwner.Block(ctx, taskID, strings.TrimSpace(reason))
		if err != nil {
			return fmt.Errorf("block canonical task after run start error: %w", err)
		}
		if err := validateTaskOwnerState(blocked, taskID); err != nil {
			return err
		}
		if blocked.Status != domaintask.StatusBlocked {
			return fmt.Errorf("task owner returned task status %q after block, want %q", blocked.Status, domaintask.StatusBlocked)
		}
		return nil
	case domaintask.StatusQueued:
		failed, err := s.taskOwner.Fail(ctx, taskID, strings.TrimSpace(reason), nil)
		if err != nil {
			return fmt.Errorf("fail canonical task after run start error: %w", err)
		}
		if err := validateTaskOwnerState(failed, taskID); err != nil {
			return err
		}
		if failed.Status != domaintask.StatusFailed {
			return fmt.Errorf("task owner returned task status %q after fail, want %q", failed.Status, domaintask.StatusFailed)
		}
		return nil
	case domaintask.StatusBlocked, domaintask.StatusFailed:
		return nil
	default:
		return fmt.Errorf("cannot terminalize canonical task %s after run start error from status %s", taskID, task.Status)
	}
}

func validateTaskOwnerState(task domaintask.Task, taskID modulecore.TaskID) error {
	if err := task.TaskID.Validate(); err != nil {
		return fmt.Errorf("task owner returned invalid task: %w", err)
	}
	if task.TaskID != taskID {
		return fmt.Errorf("task owner returned task %s, want %s", task.TaskID, taskID)
	}
	return nil
}

func (s *RunQueueScheduler) blockReservation(ctx context.Context, item domainsuperagent.RunQueueItem, reason string, at time.Time) error {
	item.Status = "blocked"
	item.Reason = strings.TrimSpace(reason)
	if item.Reason == "" {
		item.Reason = "canonical task run could not be started"
	}
	item.RunID = ""
	item.CompletedAt = at
	if store, ok := s.store.(RunQueueLeaseStore); ok {
		blocked, err := store.CompleteRunQueueItem(ctx, item.QueueID, item.LeaseToken, "blocked", item.Reason, at)
		if err != nil {
			return err
		}
		if !blocked {
			return fmt.Errorf("run queue lease lost before blocking reservation")
		}
		return nil
	}
	item.LeaseToken, item.LeaseUntil = "", time.Time{}
	if err := s.store.SaveRunQueueItem(ctx, item); err != nil {
		return err
	}
	return nil
}

func (s *RunQueueScheduler) processWithHeartbeat(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
	store, ok := s.store.(RunQueueLeaseStore)
	if !ok {
		return s.processor.ProcessRunQueueItem(ctx, item, traceID)
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
	summary, err := s.processor.ProcessRunQueueItem(runCtx, item, traceID)
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
		"queue_reference": item.QueueID, "status": status, "summary": strings.TrimSpace(summary),
	})
	event.TaskID = item.TaskID
	event.RunID = item.RunID
	if err := s.store.Append(ctx, event); err != nil {
		return "", fmt.Errorf("append run queue event: %w", err)
	}
	return event.EventID, nil
}

func nextDueRunQueueItem(items []domainsuperagent.RunQueueItem, now time.Time) (domainsuperagent.RunQueueItem, bool) {
	var selected domainsuperagent.RunQueueItem
	found := false
	for _, item := range items {
		if item.Status != "queued" && !(item.Status == "reserved" && !item.LeaseUntil.After(now)) && !(item.Status == "claimed" && !item.LeaseUntil.After(now)) {
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
