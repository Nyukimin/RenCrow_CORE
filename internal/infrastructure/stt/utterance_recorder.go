package stt

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md and
// docs/調査/20261014_194143_STT_CORE中継化_設計とFailureKnowledge.md.
//
// Recording the utterances of /stt.
//
// Recognition itself happens in RenCrow_STT, which CORE only relays to. What
// CORE still owns is the evidence of the external effect: one short-lived
// Task / Run / Action per utterance for which STT called its recognition
// target, written after the terminal event was relayed.
//
// Two rules shape this file.
//
//   - The Run admission does not precede the utterance. Recognition must not
//     depend on the execution capacity (Global=3, which counts read-only Tasks
//     too), so the record is made afterwards and may fail without anyone being
//     told except the log and the counters.
//   - The record is bounded. A bounded queue, one worker, a finite retry for
//     the capacity refusal, and a bounded flush at shutdown: PORTAL streams for
//     hours and a store that slows down must not make CORE grow without limit.
//     Everything that is lost is counted and logged with its request and
//     utterance ids, because a lost record is lost evidence.
//
// The Run is started and closed here, through the Task owner's one closing rule
// (taskmanager.Manager.CloseStartedRun). No transport receipt is made for the
// stream: a receipt needs a Run and an Action bound before the request, and
// nothing precedes the utterance. The correlation with STT's log is the
// request_id and utterance_id in the Action name and summary.

// UtteranceOutcome is how an utterance ended, as far as the record is concerned.
type UtteranceOutcome string

const (
	// OutcomeFinal is a final event (recognised, or empty after the target answered).
	OutcomeFinal UtteranceOutcome = "final"
	// OutcomeError is an utterance-level error from STT.
	OutcomeError UtteranceOutcome = "error"
	// OutcomeCancelled is a cancelled event from STT.
	OutcomeCancelled UtteranceOutcome = "cancelled"
	// OutcomeClientAborted is an utterance whose client went away before STT
	// sent a terminal event. CORE closes the stream, which aborts the work.
	OutcomeClientAborted UtteranceOutcome = "client_aborted"
)

// TargetCalled says whether STT called its recognition target for the utterance.
type TargetCalled string

const (
	// TargetCalledTrue is STT's own statement that it called the target.
	TargetCalledTrue TargetCalled = "true"
	// TargetCalledMissing is a terminal STT owns that carried no target_called.
	// It is recorded: dropping it would lose the evidence silently.
	TargetCalledMissing TargetCalled = "missing"
	// TargetCalledUnknown is an utterance CORE ended itself (the client left):
	// STT never said whether the target was called.
	TargetCalledUnknown TargetCalled = "unknown"
)

// UtteranceRecord is what the record of one utterance is made from.
type UtteranceRecord struct {
	RequestID    string
	UtteranceID  string
	Outcome      UtteranceOutcome
	Code         string
	EmptyReason  string
	EndReason    string
	AudioMS      int64
	QueueMS      int64
	ProcessingMS int64
	TargetCalled TargetCalled
}

// RecordFromTerminal builds the record of a terminal STT sent.
func RecordFromTerminal(requestID string, t modulestt.TerminalRecord) UtteranceRecord {
	rec := UtteranceRecord{
		RequestID:    requestID,
		UtteranceID:  t.UtteranceID,
		Code:         t.Code,
		EmptyReason:  t.EmptyReason,
		EndReason:    t.EndReason,
		AudioMS:      t.AudioMS,
		QueueMS:      t.QueueMS,
		ProcessingMS: t.ProcessingMS,
		TargetCalled: TargetCalledTrue,
	}
	if t.TargetCalledMissing {
		rec.TargetCalled = TargetCalledMissing
	}
	switch t.Type {
	case modulestt.WebSocketEventTypeError:
		rec.Outcome = OutcomeError
	case modulestt.WebSocketEventTypeCancelled:
		rec.Outcome = OutcomeCancelled
	default:
		rec.Outcome = OutcomeFinal
	}
	return rec
}

// AbortedRecord is the record of an utterance whose client disconnected before
// its terminal event. The contract does not let CORE say whether the target was
// called, so the record says "unknown".
func AbortedRecord(requestID, utteranceID string) UtteranceRecord {
	return UtteranceRecord{RequestID: requestID, UtteranceID: utteranceID, Outcome: OutcomeClientAborted, TargetCalled: TargetCalledUnknown}
}

const (
	defaultRecorderQueueSize     = 32
	defaultRecorderRetryAttempts = 3
	// 4s, 8s and 16s: three retries within 28 seconds.
	defaultRecorderRetryBackoff = 4 * time.Second
	// One attempt is bounded like the terminal writes of a started Run.
	recorderAttemptTimeout = taskmanager.StartedRunTerminalWriteTimeout
	// After the flush deadline the worker is told to stop; Close waits this long
	// for it to count what it could not finish.
	recorderStopGrace = time.Second
	// A record that already started its Run closes it with writes that outlast the
	// stop (detached, up to StartedRunTerminalWriteTimeout each). Close waits for
	// them, because a Run left started holds an execution slot until the next restart.
	recorderRunCloseGrace = taskmanager.StartedRunTerminalWriteTimeout + time.Second
)

// ErrRecorderFlushIncomplete is returned by Close when the queue was not empty
// by the flush deadline. What was lost is counted and logged.
var ErrRecorderFlushIncomplete = errors.New("STT utterance recorder did not finish its queue before the flush deadline")

// RecorderConfig tunes a recorder. Zero values mean the defaults.
type RecorderConfig struct {
	// QueueSize bounds the records waiting for the worker (default 32).
	QueueSize int
	// RetryAttempts is how many times a refusal by the parallel limit is retried (default 3).
	RetryAttempts int
	// RetryBackoff is the first wait between retries; it doubles (default 4s).
	RetryBackoff time.Duration
	// Sleep waits for d or returns the context's error. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Logf receives the recorder's log lines. Default: the standard logger.
	Logf func(format string, args ...any)
}

// RecorderStats counts what the recorder did. Lost is Dropped + Failed.
type RecorderStats struct {
	Recorded int64
	// Dropped records were never attempted: the queue was full or the recorder was closed.
	Dropped int64
	// Failed records were attempted and not stored (a store error, the capacity
	// refusal outlasting the retries, the flush deadline).
	Failed int64
	// MissingTargetCalled counts terminals STT sent without target_called.
	MissingTargetCalled int64
}

// UtteranceRecorder records utterances asynchronously and boundedly.
type UtteranceRecorder struct {
	actions *actionmanager.Manager
	tasks   *taskmanager.Manager
	actorID string
	cfg     RecorderConfig

	// recordFn stores one record. It is r.Record; tests replace it.
	recordFn func(ctx context.Context, rec UtteranceRecord) error

	mu     sync.Mutex
	closed bool
	queue  chan UtteranceRecord

	stopCtx context.Context
	stop    context.CancelFunc
	done    chan struct{}

	recorded, dropped, failed, missing atomic.Int64
	// activeRuns is the number of Runs started and not yet closed by Record (0 or 1: one worker).
	activeRuns atomic.Int32
}

// NewUtteranceRecorder starts a recorder with its single worker.
func NewUtteranceRecorder(actions *actionmanager.Manager, tasks *taskmanager.Manager, actorID string, cfg RecorderConfig) (*UtteranceRecorder, error) {
	actorID = strings.TrimSpace(actorID)
	if actions == nil || tasks == nil || actorID == "" {
		return nil, errors.New("STT utterance recorder needs the Action owner, the Task owner and an actor id")
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultRecorderQueueSize
	}
	if cfg.RetryAttempts <= 0 {
		cfg.RetryAttempts = defaultRecorderRetryAttempts
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = defaultRecorderRetryBackoff
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	stopCtx, stop := context.WithCancel(context.Background())
	r := &UtteranceRecorder{
		actions: actions, tasks: tasks, actorID: actorID, cfg: cfg,
		queue:   make(chan UtteranceRecord, cfg.QueueSize),
		stopCtx: stopCtx, stop: stop,
		done: make(chan struct{}),
	}
	r.recordFn = r.Record
	go r.run()
	return r, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Submit queues a record without ever blocking. It returns false, and counts
// and logs the loss, when the queue is full or the recorder is closed.
func (r *UtteranceRecorder) Submit(rec UtteranceRecord) bool {
	if rec.TargetCalled == TargetCalledMissing {
		r.missing.Add(1)
		r.cfg.Logf("[STT][recorder] WARN the terminal event carried no target_called; recording it anyway: %s", describeRecord(rec))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		r.drop(rec, "the recorder is closed")
		return false
	}
	select {
	case r.queue <- rec:
		return true
	default:
		r.drop(rec, "the queue is full")
		return false
	}
}

func (r *UtteranceRecorder) drop(rec UtteranceRecord, reason string) {
	r.dropped.Add(1)
	r.cfg.Logf("[STT][recorder] ERROR dropped an utterance record (%s): %s", reason, describeRecord(rec))
}

func describeRecord(rec UtteranceRecord) string {
	return fmt.Sprintf("request_id=%s utterance_id=%s outcome=%s", cleanField(rec.RequestID), cleanField(rec.UtteranceID), rec.Outcome)
}

// Stats returns the counters.
func (r *UtteranceRecorder) Stats() RecorderStats {
	return RecorderStats{
		Recorded:            r.recorded.Load(),
		Dropped:             r.dropped.Load(),
		Failed:              r.failed.Load(),
		MissingTargetCalled: r.missing.Load(),
	}
}

func (r *UtteranceRecorder) run() {
	defer close(r.done)
	for rec := range r.queue {
		if r.stopCtx.Err() != nil {
			r.drop(rec, "shutdown reached it before it could be recorded")
			continue
		}
		if err := r.recordWithRetry(rec); err != nil {
			r.failed.Add(1)
			r.cfg.Logf("[STT][recorder] ERROR could not record an utterance: %s: %v", describeRecord(rec), err)
			continue
		}
		r.recorded.Add(1)
	}
}

// recordWithRetry retries only the refusal by the parallel limit, which is the
// one failure that passes by itself, and only a finite number of times.
func (r *UtteranceRecorder) recordWithRetry(rec UtteranceRecord) error {
	backoff := r.cfg.RetryBackoff
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(r.stopCtx, recorderAttemptTimeout)
		err := r.recordFn(ctx, rec)
		cancel()
		if err == nil || !errors.Is(err, taskmanager.ErrParallelLimit) || attempt >= r.cfg.RetryAttempts {
			return err
		}
		if sleepErr := r.cfg.Sleep(r.stopCtx, backoff); sleepErr != nil {
			return errors.Join(err, sleepErr)
		}
		backoff *= 2
	}
}

// Close stops accepting records and waits for the queue to drain until ctx
// ends. It is idempotent. When ctx ends first it tells the worker to stop,
// waits a moment for it to count what it did not finish, and returns
// ErrRecorderFlushIncomplete.
func (r *UtteranceRecorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.queue)
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		r.stop()
		return nil
	case <-ctx.Done():
	}
	r.stop()
	r.awaitStop()
	return ErrRecorderFlushIncomplete
}

// awaitStop waits for the worker after it was told to stop: briefly when it
// holds no Run, and as long as the Run needs to be closed when it does.
func (r *UtteranceRecorder) awaitStop() {
	began := time.Now()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-tick.C:
		}
		waited := time.Since(began)
		if r.activeRuns.Load() == 0 && waited >= recorderStopGrace {
			return
		}
		if waited >= recorderRunCloseGrace {
			return
		}
	}
}

// Record stores one utterance as a Task, a Run and an Action, all closed. It is
// what the worker calls, and it is idempotent: an utterance that already has a
// finished Action is not recorded twice. Every path that ends after the Run was
// started closes it.
func (r *UtteranceRecorder) Record(ctx context.Context, rec UtteranceRecord) error {
	name := recordActionName(rec)
	// An utterance is recorded once. Ask for the Actions of this Kind and exact
	// name rather than searching the history: only a finished one counts.
	existing, err := r.actions.ListActions(ctx, domainaction.Filter{Kind: domainaction.KindSTT, Name: name})
	if err != nil {
		return err
	}
	for _, action := range existing {
		if domainaction.IsTerminal(action.Status) {
			return nil
		}
	}
	task, run, err := r.tasks.CreateAndStartRun(ctx, domaintask.Task{
		Title:    "STT utterance",
		Route:    domaintask.RouteCHAT,
		OwnerID:  r.actorID,
		Assignee: r.actorID,
		ReadOnly: true,
	}, domaintask.SharedRoleContext{
		UserIntent:  cleanField(rec.UtteranceID),
		CurrentPlan: "record the outcome of one relayed STT utterance",
	})
	if err != nil {
		return err
	}
	r.activeRuns.Add(1)
	defer r.activeRuns.Add(-1)
	// From here on the Run holds an execution slot, so every path that ends the
	// call closes it: the failed Action creation here, the outcome below.
	action, attempt, err := r.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: task.TaskID,
		RunID:  run.RunID,
		Kind:   domainaction.KindSTT,
		Name:   name,
	})
	if err != nil {
		return r.tasks.CloseStartedRun(ctx, task.TaskID, run.RunID, r.actorID, taskmanager.RunOutcomeOf(err, ""), err)
	}
	attemptOutcome, runOutcome := recordOutcome(rec)
	// The outcome is the utterance's own, not an error, so there is no cause to return.
	if closed, err := r.actions.CloseAttempt(ctx, action.ActionID, attempt.AttemptID, attemptOutcome, nil); !closed {
		return fmt.Errorf("complete STT utterance action: %w", err)
	}
	if err := r.tasks.CloseStartedRun(ctx, task.TaskID, run.RunID, r.actorID, runOutcome, nil); err != nil {
		return fmt.Errorf("complete STT utterance run: %w", err)
	}
	return nil
}

// recordActionName is unique per stream connection and utterance: STT's ids are
// only unique within one connection.
func recordActionName(rec UtteranceRecord) string {
	return "stt.stream:" + rec.RequestID + ":" + rec.UtteranceID
}

// recordOutcome maps how the utterance ended to how its Attempt, Action, Run
// and Task end, and writes the diagnostics into the summary.
func recordOutcome(rec UtteranceRecord) (actionmanager.AttemptOutcome, taskmanager.RunOutcome) {
	summary := recordSummary(rec)
	switch rec.Outcome {
	case OutcomeFinal:
		return actionmanager.AttemptOutcome{Attempt: domainaction.AttemptStatusSucceeded, Action: domainaction.StatusSucceeded, Summary: summary},
			taskmanager.RunOutcome{Status: domaintask.StatusSucceeded, Summary: summary}
	case OutcomeCancelled, OutcomeClientAborted:
		return actionmanager.AttemptOutcome{Attempt: domainaction.AttemptStatusCancelled, Action: domainaction.StatusCancelled, Summary: summary},
			taskmanager.RunOutcome{Status: domaintask.StatusCancelled, Summary: summary}
	default:
		return actionmanager.AttemptOutcome{Attempt: domainaction.AttemptStatusFailed, Action: domainaction.StatusFailed, Summary: summary},
			taskmanager.RunOutcome{Status: domaintask.StatusFailed, Summary: summary}
	}
}

func recordSummary(rec UtteranceRecord) string {
	var lead string
	switch rec.Outcome {
	case OutcomeFinal:
		lead = "STT utterance completed"
	case OutcomeCancelled:
		lead = "STT utterance cancelled"
	case OutcomeClientAborted:
		lead = "STT utterance interrupted: client disconnected before the terminal event"
	default:
		lead = "STT utterance failed"
	}
	target := string(rec.TargetCalled)
	if target == "" {
		target = string(TargetCalledUnknown)
	}
	fields := []string{
		"request_id=" + cleanField(rec.RequestID),
		"utterance_id=" + cleanField(rec.UtteranceID),
		fmt.Sprintf("audio_ms=%d", rec.AudioMS),
		fmt.Sprintf("queue_ms=%d", rec.QueueMS),
		fmt.Sprintf("processing_ms=%d", rec.ProcessingMS),
		"target_called=" + target,
	}
	if rec.EndReason != "" {
		fields = append(fields, "end_reason="+cleanField(rec.EndReason))
	}
	if rec.EmptyReason != "" {
		fields = append(fields, "empty_reason="+cleanField(rec.EmptyReason))
	}
	if rec.Code != "" {
		fields = append(fields, "code="+cleanField(rec.Code))
	}
	return lead + " (" + strings.Join(fields, " ") + ")"
}

// cleanField keeps an id or a code from STT or the client printable and short
// before it goes into a log line or a summary.
func cleanField(value string) string {
	const limit = 128
	var b strings.Builder
	for _, r := range value {
		if b.Len() >= limit {
			break
		}
		switch {
		case unicode.IsControl(r) || unicode.IsSpace(r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
