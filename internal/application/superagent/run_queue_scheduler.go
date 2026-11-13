package superagent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
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

// RunQueuePrivateTextTaskOwner extends the canonical Task owner with the
// cursor operations that authorize private text continuation. Queue receipts
// carry only the cursor digest and predecessor identity, never prompt/output.
type RunQueuePrivateTextTaskOwner interface {
	RunQueueTaskOwner
	StartPrivateTextRun(context.Context, modulecore.TaskID, modulecore.QueueItemID, string, modulecore.RunID, string) (domaintask.Run, error)
	GetPrivateTextCheckpoint(context.Context, modulecore.TaskID) (domaintask.PrivateTextCheckpoint, bool, error)
	SavePrivateTextOutput(context.Context, modulecore.TaskID, modulecore.RunID, string, string) error
	CompletePrivateTextRun(context.Context, modulecore.TaskID, modulecore.RunID, string, bool) error
}

// RunQueuePrivateTextIntentStore provides insert-only idempotency for the
// private checkpoint continuation. Existing receipts are validated and never
// rewritten when a retry observes them.
type RunQueuePrivateTextIntentStore interface {
	InsertRunQueueItemIfAbsent(context.Context, domainsuperagent.RunQueueItem) (domainsuperagent.RunQueueItem, bool, error)
}

// RunQueuePauseOutcome is a typed normal control result. The scheduler inserts
// and validates a private successor intent before it completes the source
// receipt, so a stop request is not reported as a failed run.
type RunQueuePauseOutcome struct{}

func (RunQueuePauseOutcome) Error() string { return "private task paused at durable checkpoint" }

func IsRunQueuePauseOutcome(err error) bool {
	var paused RunQueuePauseOutcome
	return errors.As(err, &paused)
}

const privateTextPausedQueueReason = "private task paused"
const privateTextResumeQueuedTraceSummary = "private text checkpoint resume queued"
const privateTextQueueProjectionSummary = "private task completed"

var errRunQueueLeaseLost = errors.New("run queue lease lost")

type runQueueStopSignalsContextKey struct{}

func withRunQueueStopSignal(ctx context.Context, stop <-chan struct{}) context.Context {
	if ctx == nil || stop == nil {
		return ctx
	}
	signals, _ := ctx.Value(runQueueStopSignalsContextKey{}).([]<-chan struct{})
	copyOfSignals := append(append([]<-chan struct{}(nil), signals...), stop)
	return context.WithValue(ctx, runQueueStopSignalsContextKey{}, copyOfSignals)
}

// RunQueueStopRequested reports an explicit stop signal without conflating it
// with cancellation of the Task persistence context.
func RunQueueStopRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	signals, _ := ctx.Value(runQueueStopSignalsContextKey{}).([]<-chan struct{})
	for _, signal := range signals {
		select {
		case <-signal:
			return true
		default:
		}
	}
	return false
}

// RunQueueStopSignal combines actor preemption and queue-lease loss into one
// cooperative signal for bounded provider calls. The returned channel also
// closes when ctx is cancelled, while Task writes use a detached bounded
// persistence context.
func RunQueueStopSignal(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	signals, _ := ctx.Value(runQueueStopSignalsContextKey{}).([]<-chan struct{})
	if len(signals) == 0 {
		return ctx.Done()
	}
	if len(signals) == 1 {
		return signals[0]
	}
	combined := make(chan struct{})
	cases := make([]reflect.SelectCase, 0, len(signals)+1)
	for _, signal := range signals {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(signal)})
	}
	if ctx.Done() != nil {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})
	}
	go func() {
		reflect.Select(cases)
		close(combined)
	}()
	return combined
}

type RunQueueProcessorFunc func(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error)

func (f RunQueueProcessorFunc) ProcessRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
	return f(ctx, item, traceID)
}

type RunQueueSchedulerOptions struct {
	ClaimLimit      int
	Interval        time.Duration
	LeaseDuration   time.Duration
	Now             func() time.Time
	LeaseToken      func() (string, error)
	ActorTaskLister RunQueueActorTaskLister
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
		if err := s.runReservedItem(ctx, item); err != nil {
			return processed, err
		}
		processed++
		now = s.options.Now().UTC()
	}
	return processed, nil
}

func (s *RunQueueScheduler) runReservedItem(ctx context.Context, item *domainsuperagent.RunQueueItem) error {
	return s.runReservedItemWithStartFailure(ctx, item, nil)
}

func (s *RunQueueScheduler) runReservedItemWithStartFailure(ctx context.Context, item *domainsuperagent.RunQueueItem, onStartFailure func(error) (bool, error)) error {
	if item == nil {
		return fmt.Errorf("run queue item is nil")
	}

	run, err := s.startRun(ctx, item)
	if err != nil {
		if isPrivateTextQueueItem(*item) {
			if run.RunID != "" {
				// The owner has already issued a cursor-bound Run. Keep that exact
				// Task state for startup recovery; generic cleanup could close a
				// newer owner or erase the only resumable cursor.
				return errors.New("private task Run was issued but queue start verification failed; exact startup recovery is required")
			}
			if onStartFailure != nil {
				handled, handlerErr := onStartFailure(errors.New("private task Run start failed"))
				if handled {
					return handlerErr
				}
			}
			leaseStore, ok := s.store.(RunQueueExactLeaseStore)
			if !ok {
				return errors.New("private task Run start failed and exact queue release is unavailable")
			}
			released, releaseErr := leaseStore.ReleaseRunQueueItem(ctx, item.QueueItemID, item.LeaseToken)
			if releaseErr != nil || !released {
				return errors.New("private task Run start failed; exact queue lease changed before release")
			}
			return errors.New("private task Run start failed; intent released for retry")
		}
		if onStartFailure != nil {
			handled, handlerErr := onStartFailure(err)
			if handled {
				return handlerErr
			}
		}
		var cleanupErr error
		if run.RunID != "" {
			cleanupErr = s.interruptIssuedRun(ctx, *item, run, err.Error())
		}
		taskStateErr := s.terminalizeStartFailureTask(ctx, item.TaskID, err.Error())
		blockErr := s.blockReservation(ctx, *item, err.Error(), s.options.Now().UTC())
		if blockErr != nil {
			return errors.Join(err, cleanupErr, taskStateErr, fmt.Errorf("block queue item: %w", blockErr))
		}
		blocked := *item
		blocked.Status = "blocked"
		blocked.Reason = strings.TrimSpace(err.Error())
		blocked.RunID = ""
		blocked.CompletedAt = s.options.Now().UTC()
		blocked.LeaseToken, blocked.LeaseUntil = "", time.Time{}
		if _, eventErr := s.saveTrace(ctx, blocked, modulecore.NewTraceID(), "", "run_queue.blocked", "blocked", blocked.Reason); eventErr != nil {
			return errors.Join(err, cleanupErr, taskStateErr, eventErr)
		}
		if cleanupErr != nil || taskStateErr != nil {
			return errors.Join(err, cleanupErr, taskStateErr)
		}
		return err
	}
	if leaseStore, ok := s.store.(RunQueueLeaseStore); ok {
		attached, attachErr := leaseStore.AttachRunQueueRun(ctx, string(item.QueueItemID), item.LeaseToken, run.RunID)
		if attachErr != nil {
			if isPrivateTextQueueItem(*item) {
				return errors.New("private task Run was issued but exact queue attachment failed; startup recovery will reconcile its cursor")
			}
			originalErr := fmt.Errorf("attach canonical run to run queue item: %w", attachErr)
			if cleanupErr := s.interruptIssuedRun(ctx, *item, run, originalErr.Error()); cleanupErr != nil {
				return errors.Join(originalErr, cleanupErr)
			}
			return originalErr
		}
		if !attached {
			if isPrivateTextQueueItem(*item) {
				return errors.New("private task Run was issued but its exact queue lease changed; startup recovery will reconcile its cursor")
			}
			// The Task owner has already issued the canonical Run, but the
			// reservation is no longer ours. Do not process the Run or emit a
			// queue/projection/event receipt that could overwrite the newer
			// claimant. Close the exact issued Run through the Task owner;
			// never transition the Task or touch the newer claimant's Run.
			originalErr := fmt.Errorf("run queue lease lost before attaching canonical run")
			if cleanupErr := s.interruptIssuedRun(ctx, *item, run, originalErr.Error()); cleanupErr != nil {
				return errors.Join(originalErr, cleanupErr)
			}
			return originalErr
		}
	} else {
		// The in-memory/non-lease store is a single-store test fallback;
		// retain its local state transition because there is no lease CAS.
		item.RunID = run.RunID
		item.Status = "claimed"
		if err := s.store.SaveRunQueueItem(ctx, *item); err != nil {
			return fmt.Errorf("persist claimed run queue item: %w", err)
		}
	}
	item.RunID = run.RunID
	item.Status = "claimed"
	traceID := modulecore.NewTraceID()
	claimedEventID, err := s.saveTrace(ctx, *item, traceID, "", "run_queue.claimed", "claimed", item.Action)
	if err != nil {
		return err
	}
	summary, execErr := s.processWithHeartbeat(ctx, *item, traceID)
	completedAt := s.options.Now().UTC()
	if IsRunQueuePauseOutcome(execErr) {
		if !isPrivateTextQueueItem(*item) {
			return fmt.Errorf("non-private run queue processor returned a pause outcome")
		}
		return s.completePrivateTextPause(ctx, *item, traceID, claimedEventID, completedAt)
	}
	if execErr != nil {
		task, taskErr := s.taskOwner.Get(ctx, item.TaskID)
		if taskErr != nil {
			return errors.Join(execErr, fmt.Errorf("get canonical task after run queue error: %w", taskErr))
		}
		if isPrivateTextQueueItem(*item) {
			if errors.Is(execErr, errRunQueueLeaseLost) {
				return errors.New("private text queue lease was lost; canonical Task left unchanged")
			}
			privateOwner, ok := s.taskOwner.(RunQueuePrivateTextTaskOwner)
			if !ok {
				return errors.New("canonical Task owner cannot verify private text cursor ownership")
			}
			checkpoint, found, cursorErr := privateOwner.GetPrivateTextCheckpoint(ctx, item.TaskID)
			if cursorErr != nil || !found || checkpoint.TaskID != item.TaskID || checkpoint.RunID != item.RunID ||
				checkpoint.QueueItemID != item.QueueItemID || checkpoint.ActorID != task.Assignee {
				return errors.New("private text error belongs to a superseded or unavailable checkpoint; canonical Task left unchanged")
			}
			if task.Status == domaintask.StatusWaiting && task.WaitingReason == domaintask.PrivateTextWaitingReason {
				return s.completePrivateTextPause(ctx, *item, traceID, claimedEventID, completedAt)
			}
			if task.Status == domaintask.StatusSucceeded && checkpoint.Output != "" {
				if err := s.complete(ctx, *item, "completed", privateTextQueueProjectionSummary, completedAt); err != nil {
					return err
				}
				if err := s.completeAgentRun(ctx, *item, "completed", privateTextQueueProjectionSummary, completedAt); err != nil {
					return err
				}
				_, err := s.saveTrace(ctx, *item, traceID, claimedEventID, "run_queue.completed", "completed", privateTextQueueProjectionSummary)
				return err
			}
			leaseStore, ok := s.store.(RunQueueLeaseStore)
			if !ok {
				return errors.New("private text failure cannot verify its exact queue lease")
			}
			owned, leaseErr := leaseStore.RenewRunQueueLease(ctx, string(item.QueueItemID), item.LeaseToken, s.options.Now().UTC().Add(s.options.LeaseDuration))
			if leaseErr != nil || !owned {
				return errors.New("private text failure lost its exact queue lease; canonical Task left unchanged")
			}
		}
		switch task.Status {
		case domaintask.StatusWaiting:
			reason := strings.TrimSpace(task.WaitingReason)
			if reason == "" {
				reason = execErr.Error()
			}
			if err := s.complete(ctx, *item, "cancelled", reason, completedAt); err != nil {
				return err
			}
			if _, eventErr := s.saveTrace(ctx, *item, traceID, claimedEventID, "run_queue.cancelled", "cancelled", reason); eventErr != nil {
				return eventErr
			}
			return execErr
		case domaintask.StatusCancelled:
			reason := strings.TrimSpace(task.Summary)
			if reason == "" {
				reason = execErr.Error()
			}
			if err := s.complete(ctx, *item, "cancelled", reason, completedAt); err != nil {
				return err
			}
			if err := s.completeAgentRun(ctx, *item, "cancelled", reason, completedAt); err != nil {
				return err
			}
			if _, eventErr := s.saveTrace(ctx, *item, traceID, claimedEventID, "run_queue.cancelled", "cancelled", reason); eventErr != nil {
				return eventErr
			}
			return execErr
		case domaintask.StatusRunning:
			failureReason := execErr.Error()
			if isPrivateTextQueueItem(*item) {
				failureReason = "private task execution failed"
			}
			if _, failErr := s.taskOwner.Fail(ctx, item.TaskID, failureReason, nil); failErr != nil {
				return fmt.Errorf("fail canonical task after run queue error: %w", failErr)
			}
		case domaintask.StatusFailed:
			// The canonical Task already records the failure. Do not issue a
			// second owner transition; finish the queue receipt below.
		default:
			return fmt.Errorf("run queue processor failed while canonical task %s is %s", item.TaskID, task.Status)
		}
		failureReason := execErr.Error()
		if isPrivateTextQueueItem(*item) {
			failureReason = "private task execution failed"
		}
		if err := s.complete(ctx, *item, "failed", failureReason, completedAt); err != nil {
			return err
		}
		if err := s.completeAgentRun(ctx, *item, "failed", failureReason, completedAt); err != nil {
			return err
		}
		if _, eventErr := s.saveTrace(ctx, *item, traceID, claimedEventID, "run_queue.failed", "failed", failureReason); eventErr != nil {
			return eventErr
		}
		return execErr
	}
	task, err := s.taskOwner.Get(ctx, item.TaskID)
	if err != nil {
		return fmt.Errorf("get canonical task after run queue success: %w", err)
	}
	if task.Status != domaintask.StatusSucceeded {
		return fmt.Errorf("run queue processor succeeded while canonical task %s is %s", item.TaskID, task.Status)
	}
	completionSummary := strings.TrimSpace(summary)
	if isPrivateTextQueueItem(*item) {
		completionSummary = privateTextQueueProjectionSummary
	}
	if err := s.complete(ctx, *item, "completed", completionSummary, completedAt); err != nil {
		return err
	}
	if err := s.completeAgentRun(ctx, *item, "completed", completionSummary, completedAt); err != nil {
		return err
	}
	if _, err := s.saveTrace(ctx, *item, traceID, claimedEventID, "run_queue.completed", "completed", completionSummary); err != nil {
		return err
	}
	return nil
}

func (s *RunQueueScheduler) completePrivateTextPause(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID, claimedEventID modulecore.EventID, completedAt time.Time) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	owner, ok := s.taskOwner.(RunQueuePrivateTextTaskOwner)
	if !ok {
		return fmt.Errorf("canonical Task owner does not support private text checkpoint pause")
	}
	queue, ok := s.store.(RunQueuePrivateTextIntentStore)
	if !ok {
		return fmt.Errorf("run queue store does not support insert-only private text resume intents")
	}
	task, err := owner.Get(persistCtx, item.TaskID)
	if err != nil {
		return fmt.Errorf("read private text Task after pause: %w", err)
	}
	if task.TaskID != item.TaskID || task.Status != domaintask.StatusWaiting || task.WaitingReason != domaintask.PrivateTextWaitingReason {
		return fmt.Errorf("private text pause did not leave the canonical Task in its fixed waiting state")
	}
	checkpoint, found, err := owner.GetPrivateTextCheckpoint(persistCtx, item.TaskID)
	if err != nil {
		return fmt.Errorf("read private text cursor after pause: %w", err)
	}
	if !found || checkpoint.TaskID != item.TaskID || checkpoint.RunID != item.RunID || checkpoint.QueueItemID != item.QueueItemID || checkpoint.ActorID != task.Assignee {
		return fmt.Errorf("private text pause cursor does not match the exact Task, Run, queue item, and actor")
	}
	if checkpoint.InputSHA256 == "" || checkpoint.Digest() == "" {
		return fmt.Errorf("private text pause cursor is incomplete")
	}
	next := domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: item.TaskID,
		PredecessorRunID: item.RunID, CheckpointDigest: checkpoint.Digest(),
		RunStartReason: domaintask.RunStartReasonCheckpointResume, WorkstreamID: string(task.WorkstreamID),
		Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", Status: "queued",
		Priority: taskPriorityRank(task.Priority), IdempotencyKey: domainsuperagent.PrivateTextResumeQueueKey(item.RunID),
		CreatedAt: task.CreatedAt,
	}
	if err := domainsuperagent.ValidateRunQueueItem(next); err != nil {
		return fmt.Errorf("validate private text successor intent: %w", err)
	}
	stored, inserted, err := queue.InsertRunQueueItemIfAbsent(persistCtx, next)
	if err != nil {
		return fmt.Errorf("insert private text successor intent: %w", err)
	}
	if !privateTextResumeIntentReceiptMatches(stored, next) {
		return fmt.Errorf("stored private text successor receipt does not match the exact checkpoint intent")
	}
	if stored.Status != "queued" && stored.Status != "reserved" && stored.Status != "claimed" {
		return fmt.Errorf("private text successor intent already has terminal status %q", stored.Status)
	}
	if inserted {
		if _, err := s.saveTrace(persistCtx, stored, traceID, claimedEventID, "run_queue.resume_queued", "queued", privateTextResumeQueuedTraceSummary); err != nil {
			return fmt.Errorf("record private text successor intent: %w", err)
		}
	}
	if err := s.complete(persistCtx, item, "cancelled", privateTextPausedQueueReason, completedAt); err != nil {
		return fmt.Errorf("complete paused private text source receipt: %w", err)
	}
	if _, err := s.saveTrace(persistCtx, item, traceID, claimedEventID, "run_queue.paused", "waiting", privateTextPausedQueueReason); err != nil {
		return fmt.Errorf("record private text pause: %w", err)
	}
	return nil
}

func privateTextResumeIntentReceiptMatches(stored, expected domainsuperagent.RunQueueItem) bool {
	return domainsuperagent.ValidateRunQueueItem(stored) == nil && domainsuperagent.IsPrivateTextResumeQueueItem(stored) &&
		stored.TaskID == expected.TaskID && stored.PredecessorRunID == expected.PredecessorRunID &&
		stored.CheckpointDigest == expected.CheckpointDigest && stored.RunStartReason == expected.RunStartReason &&
		stored.WorkstreamID == expected.WorkstreamID && stored.Goal == expected.Goal && stored.Action == expected.Action &&
		stored.Priority == expected.Priority && stored.IdempotencyKey == expected.IdempotencyKey && stored.CreatedAt.Equal(expected.CreatedAt)
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
	if isPrivateTextFirstChatQueueItem(*item) {
		owner, ok := s.taskOwner.(RunQueuePrivateTextTaskOwner)
		if !ok {
			return domaintask.Run{}, fmt.Errorf("canonical Task owner does not support private text checkpoints")
		}
		task, err := owner.Get(ctx, item.TaskID)
		if err != nil {
			return domaintask.Run{}, fmt.Errorf("recheck private text Task before first run: %w", err)
		}
		if !actorPlannableTask(task, item.TaskID, task.Assignee) || task.Status != domaintask.StatusQueued {
			return domaintask.Run{}, fmt.Errorf("private text first-run Task is no longer queued")
		}
		run, err := owner.StartPrivateTextRun(ctx, item.TaskID, item.QueueItemID, task.Assignee, "", "")
		if err != nil {
			return run, fmt.Errorf("start private text first run for task %s: %w", item.TaskID, err)
		}
		if err := validateStartedQueueRun(run, *item, domaintask.RunStartReasonFirst); err != nil {
			return run, err
		}
		if run.Assignee != task.Assignee {
			return run, fmt.Errorf("Task owner returned private text Run for actor %q, want %q", run.Assignee, task.Assignee)
		}
		checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, item.TaskID)
		if err != nil {
			return run, fmt.Errorf("verify private text first-run checkpoint: %w", err)
		}
		if !found || checkpoint.TaskID != item.TaskID || checkpoint.RunID != run.RunID || checkpoint.QueueItemID != item.QueueItemID ||
			checkpoint.ActorID != task.Assignee || checkpoint.InputSHA256 != privateTextInputDigest(task.Title) || checkpoint.Output != "" {
			return run, fmt.Errorf("Task owner did not persist the exact private text first-run cursor")
		}
		return run, nil
	}
	if domainsuperagent.IsPrivateTextResumeQueueItem(*item) {
		owner, ok := s.taskOwner.(RunQueuePrivateTextTaskOwner)
		if !ok {
			return domaintask.Run{}, fmt.Errorf("canonical Task owner does not support private text checkpoint resume")
		}
		task, err := owner.Get(ctx, item.TaskID)
		if err != nil {
			return domaintask.Run{}, fmt.Errorf("recheck private text Task before resume: %w", err)
		}
		checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, item.TaskID)
		if err != nil {
			return domaintask.Run{}, fmt.Errorf("read private text checkpoint before resume: %w", err)
		}
		if !found || !privateTextResumeCheckpointMatches(task, *item, checkpoint) {
			return domaintask.Run{}, fmt.Errorf("private text resume intent no longer matches the canonical checkpoint")
		}
		run, err := owner.StartPrivateTextRun(ctx, item.TaskID, item.QueueItemID, task.Assignee, item.PredecessorRunID, item.CheckpointDigest)
		if err != nil {
			return run, fmt.Errorf("start private text checkpoint run for task %s: %w", item.TaskID, err)
		}
		if err := validateStartedQueueRun(run, *item, domaintask.RunStartReasonCheckpointResume); err != nil {
			return run, err
		}
		if run.Assignee != task.Assignee {
			return run, fmt.Errorf("Task owner returned private text Run for actor %q, want %q", run.Assignee, task.Assignee)
		}
		return run, nil
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

func validateStartedQueueRun(run domaintask.Run, item domainsuperagent.RunQueueItem, reason domaintask.RunStartReason) error {
	if err := run.Validate(); err != nil {
		return fmt.Errorf("task owner returned invalid run: %w", err)
	}
	if run.TaskID != item.TaskID {
		return fmt.Errorf("task owner returned run for task %s, want %s", run.TaskID, item.TaskID)
	}
	if run.StartReason != reason {
		return fmt.Errorf("task owner returned start reason %q, want %q", run.StartReason, reason)
	}
	if run.Status != domaintask.RunStatusRunning {
		return fmt.Errorf("task owner returned run status %q, want running", run.Status)
	}
	return nil
}

func privateTextResumeCheckpointMatches(task domaintask.Task, item domainsuperagent.RunQueueItem, checkpoint domaintask.PrivateTextCheckpoint) bool {
	return task.TaskID == item.TaskID && task.Status == domaintask.StatusWaiting &&
		task.WaitingReason == domaintask.PrivateTextWaitingReason && task.Assignee != "" &&
		checkpoint.TaskID == item.TaskID && checkpoint.RunID == item.PredecessorRunID &&
		checkpoint.ActorID == task.Assignee && checkpoint.Digest() == item.CheckpointDigest
}

func isPrivateTextFirstChatQueueItem(item domainsuperagent.RunQueueItem) bool {
	return item.IdempotencyKey == domainsuperagent.RunQueueIntentPrivateFirstChat &&
		item.RunStartReason == domaintask.RunStartReasonFirst &&
		item.Goal == domainsuperagent.RunQueueGoalPrivateFirstChat && item.Action == "chat"
}

func isPrivateTextQueueItem(item domainsuperagent.RunQueueItem) bool {
	return isPrivateTextFirstChatQueueItem(item) || domainsuperagent.IsPrivateTextResumeQueueItem(item)
}

func privateTextInputDigest(input string) string {
	digest := sha256.Sum256([]byte(input))
	return hex.EncodeToString(digest[:])
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
		blocked, err := store.CompleteRunQueueItem(ctx, string(item.QueueItemID), item.LeaseToken, "blocked", item.Reason, at)
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
	if isPrivateTextQueueItem(item) {
		leaseStop := make(chan struct{})
		runCtx := withRunQueueStopSignal(ctx, leaseStop)
		heartbeatErr := make(chan error, 1)
		processorDone := make(chan struct{})
		interval := s.options.LeaseDuration / 3
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					heartbeatErr <- nil
					return
				case <-processorDone:
					heartbeatErr <- nil
					return
				case <-ticker.C:
					ok, err := store.RenewRunQueueLease(ctx, string(item.QueueItemID), item.LeaseToken, s.options.Now().UTC().Add(s.options.LeaseDuration))
					if err != nil || !ok {
						if err == nil {
							err = fmt.Errorf("run queue lease lost")
						}
						close(leaseStop)
						heartbeatErr <- err
						return
					}
				}
			}
		}()
		summary, err := s.processor.ProcessRunQueueItem(runCtx, item, traceID)
		close(processorDone)
		if heartbeat := <-heartbeatErr; heartbeat != nil && !IsRunQueuePauseOutcome(err) {
			return summary, fmt.Errorf("%w", errRunQueueLeaseLost)
		}
		return summary, err
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
				ok, err := store.RenewRunQueueLease(runCtx, string(item.QueueItemID), item.LeaseToken, s.options.Now().UTC().Add(s.options.LeaseDuration))
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
		completed, err := store.CompleteRunQueueItem(ctx, string(item.QueueItemID), item.LeaseToken, status, reason, at)
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
		"queue_reference": item.QueueItemID, "status": status, "summary": strings.TrimSpace(summary),
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
