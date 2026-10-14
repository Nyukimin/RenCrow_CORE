package stt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
// The recorder starts a Run (CreateAndStartRun) for every utterance it records,
// so it runs the shared scenarios of taskmanagertest.VerifyStartedRunClosure.
func TestUtteranceRecorderClosesTheRunItStarted(t *testing.T) {
	taskmanagertest.VerifyStartedRunClosure(t, taskmanagertest.StartedRunPath{
		WorkFailure: taskmanagertest.WorkFailureIsRecorded,
		Start: func(t *testing.T, c *taskmanagertest.Closure, workErr error) func(context.Context) error {
			recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{})
			rec := UtteranceRecord{RequestID: "req-1", UtteranceID: "u-1", Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue, AudioMS: 1200}
			if workErr != nil {
				rec.Outcome, rec.Code = OutcomeError, "STT_TARGET_FAILED"
				if errors.Is(workErr, context.Canceled) {
					rec.Outcome = OutcomeCancelled
				}
			}
			return func(ctx context.Context) error { return recorder.Record(ctx, rec) }
		},
	})
}

// newTestRecorder builds a recorder and closes it, with a bounded flush, when the test ends.
func newTestRecorder(t *testing.T, actions *actionmanager.Manager, tasks *taskmanager.Manager, cfg RecorderConfig) *UtteranceRecorder {
	t.Helper()
	recorder, err := NewUtteranceRecorder(actions, tasks, "mio", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = recorder.Close(ctx)
	})
	return recorder
}

func TestNewUtteranceRecorderRequiresItsOwners(t *testing.T) {
	c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	if _, err := NewUtteranceRecorder(nil, c.Tasks, "mio", RecorderConfig{}); err == nil {
		t.Fatal("an Action owner is required")
	}
	if _, err := NewUtteranceRecorder(c.Actions, nil, "mio", RecorderConfig{}); err == nil {
		t.Fatal("a Task owner is required")
	}
	if _, err := NewUtteranceRecorder(c.Actions, c.Tasks, "  ", RecorderConfig{}); err == nil {
		t.Fatal("an actor id is required")
	}
}

func TestUtteranceRecorderRecordsEachOutcomeAsAShortLivedTaskRunAndAction(t *testing.T) {
	tests := []struct {
		name       string
		rec        UtteranceRecord
		wantAction domainaction.Status
		wantTask   domaintask.Status
		wantInText []string
	}{
		{"recognised final", UtteranceRecord{Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue, AudioMS: 1200, QueueMS: 3, ProcessingMS: 210, EndReason: "silence"},
			domainaction.StatusSucceeded, domaintask.StatusSucceeded, []string{"audio_ms=1200", "queue_ms=3", "processing_ms=210", "target_called=true", "end_reason=silence"}},
		{"empty final the target answered", UtteranceRecord{Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue, EmptyReason: "filtered"},
			domainaction.StatusSucceeded, domaintask.StatusSucceeded, []string{"empty_reason=filtered"}},
		{"target failure", UtteranceRecord{Outcome: OutcomeError, TargetCalled: TargetCalledTrue, Code: "STT_TARGET_FAILED"},
			domainaction.StatusFailed, domaintask.StatusFailed, []string{"code=STT_TARGET_FAILED"}},
		{"cancelled during recognition", UtteranceRecord{Outcome: OutcomeCancelled, TargetCalled: TargetCalledTrue},
			domainaction.StatusCancelled, domaintask.StatusCancelled, nil},
		{"flag missing", UtteranceRecord{Outcome: OutcomeFinal, TargetCalled: TargetCalledMissing},
			domainaction.StatusSucceeded, domaintask.StatusSucceeded, []string{"target_called=missing"}},
		{"client went away", UtteranceRecord{Outcome: OutcomeClientAborted, TargetCalled: TargetCalledUnknown},
			domainaction.StatusCancelled, domaintask.StatusCancelled, []string{"target_called=unknown", "client disconnected"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
			recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{})
			tt.rec.RequestID, tt.rec.UtteranceID = "req-7", "u-9"
			if err := recorder.Record(context.Background(), tt.rec); err != nil {
				t.Fatalf("Record() = %v", err)
			}
			actions, err := c.Actions.ListActions(context.Background(), domainaction.Filter{})
			if err != nil || len(actions) != 1 {
				t.Fatalf("Actions=%d err=%v", len(actions), err)
			}
			if actions[0].Kind != domainaction.KindSTT || actions[0].Name != "stt.stream:req-7:u-9" || actions[0].Status != tt.wantAction {
				t.Fatalf("Action = %+v", actions[0])
			}
			attempts, err := c.Actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: actions[0].ActionID})
			if err != nil || len(attempts) != 1 {
				t.Fatalf("Attempts=%d err=%v", len(attempts), err)
			}
			summary := attempts[0].Summary
			for _, want := range append([]string{"request_id=req-7", "utterance_id=u-9"}, tt.wantInText...) {
				if !strings.Contains(summary, want) {
					t.Fatalf("the Action summary %q must carry %q", summary, want)
				}
			}
			task, run := c.OnlyTaskAndRun(t)
			if task.Status != tt.wantTask || run.Status == domaintask.RunStatusRunning {
				t.Fatalf("Task/Run = %s/%s, want a closed pair ending %s", task.Status, run.Status, tt.wantTask)
			}
		})
	}
}

func TestUtteranceRecorderRecordsAnUtteranceOnce(t *testing.T) {
	c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{})
	rec := UtteranceRecord{RequestID: "req", UtteranceID: "u", Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue}
	for i := 0; i < 2; i++ {
		if err := recorder.Record(context.Background(), rec); err != nil {
			t.Fatalf("Record() #%d = %v", i, err)
		}
	}
	c.OnlyTaskAndRun(t) // fails unless exactly one Task and one Run exist
	// The same utterance id on another connection is another utterance.
	rec.RequestID = "other-request"
	if err := recorder.Record(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	tasks, err := c.Tasks.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("Tasks=%d err=%v, want one per (request, utterance)", len(tasks), err)
	}
}

func TestUtteranceRecordFromTerminalMapsTheRelayDecision(t *testing.T) {
	got := RecordFromTerminal("req-1", modulestt.TerminalRecord{
		UtteranceID: "u", Type: modulestt.WebSocketEventTypeError, Code: "STT_TARGET_FAILED",
		AudioMS: 5, QueueMS: 6, ProcessingMS: 7, EndReason: "stop", EmptyReason: "x",
	})
	want := UtteranceRecord{RequestID: "req-1", UtteranceID: "u", Outcome: OutcomeError, Code: "STT_TARGET_FAILED",
		AudioMS: 5, QueueMS: 6, ProcessingMS: 7, EndReason: "stop", EmptyReason: "x", TargetCalled: TargetCalledTrue}
	if got != want {
		t.Fatalf("RecordFromTerminal() = %+v, want %+v", got, want)
	}
	missing := RecordFromTerminal("r", modulestt.TerminalRecord{UtteranceID: "u", Type: modulestt.WebSocketEventTypeFinal, TargetCalledMissing: true})
	if missing.TargetCalled != TargetCalledMissing || missing.Outcome != OutcomeFinal {
		t.Fatalf("a missing flag stays visible: %+v", missing)
	}
	cancelled := RecordFromTerminal("r", modulestt.TerminalRecord{UtteranceID: "u", Type: modulestt.WebSocketEventTypeCancelled})
	if cancelled.Outcome != OutcomeCancelled {
		t.Fatalf("cancelled: %+v", cancelled)
	}
	aborted := AbortedRecord("r", "u")
	if aborted.Outcome != OutcomeClientAborted || aborted.TargetCalled != TargetCalledUnknown {
		t.Fatalf("AbortedRecord() = %+v", aborted)
	}
}

// gate lets a test hold the worker inside a record call.
type recordGate struct {
	mu       sync.Mutex
	started  chan string
	release  chan struct{}
	recorded []string
}

func newRecordGate() *recordGate {
	return &recordGate{started: make(chan string, 64), release: make(chan struct{})}
}

func (g *recordGate) fn(ctx context.Context, rec UtteranceRecord) error {
	g.started <- rec.UtteranceID
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	g.recorded = append(g.recorded, rec.UtteranceID)
	g.mu.Unlock()
	return nil
}

func (g *recordGate) done() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.recorded...)
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) Logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logSink) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestUtteranceRecorderQueueIsBoundedAndCountsWhatItDrops(t *testing.T) {
	c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	sink := &logSink{}
	gate := newRecordGate()
	recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{QueueSize: 2, Logf: sink.Logf})
	recorder.recordFn = gate.fn

	accepted := 0
	if !recorder.Submit(UtteranceRecord{RequestID: "r", UtteranceID: "u0", Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue}) {
		t.Fatal("the first submit must be accepted")
	}
	accepted++
	select {
	case <-gate.started: // the worker holds u0; the queue is empty again
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not pick the record up")
	}
	for i := 1; i <= 6; i++ {
		if recorder.Submit(UtteranceRecord{RequestID: "r", UtteranceID: fmt.Sprintf("u%d", i), Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue}) {
			accepted++
		}
	}
	if accepted != 3 {
		t.Fatalf("accepted %d, want 1 in flight + a queue of 2", accepted)
	}
	stats := recorder.Stats()
	if stats.Dropped != 4 {
		t.Fatalf("Dropped = %d, want 4 (the submit never blocks)", stats.Dropped)
	}
	if text := sink.text(); !strings.Contains(text, "ERROR") || !strings.Contains(text, "u6") || !strings.Contains(text, "request_id=r") {
		t.Fatalf("a drop is an ERROR line that names the utterance:\n%s", text)
	}
	close(gate.release)
}

func TestUtteranceRecorderRetriesAParallelLimitRefusalAFiniteNumberOfTimes(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	actionsHolder := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	sink := &logSink{}
	var mu sync.Mutex
	var sleeps []time.Duration
	recorder := newTestRecorder(t, actionsHolder.Actions, saturated.Manager, RecorderConfig{
		RetryAttempts: 3, RetryBackoff: 4 * time.Second, Logf: sink.Logf,
		Sleep: func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			sleeps = append(sleeps, d)
			mu.Unlock()
			return nil
		},
	})

	recorder.Submit(UtteranceRecord{RequestID: "req", UtteranceID: "u", Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue})
	closeAndWait(t, recorder)

	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second}
	if fmt.Sprint(sleeps) != fmt.Sprint(want) {
		t.Fatalf("backoff = %v, want %v (exponential, three retries, 28s in all)", sleeps, want)
	}
	stats := recorder.Stats()
	if stats.Failed != 1 || stats.Recorded != 0 {
		t.Fatalf("stats = %+v, want the record counted as failed after the retries", stats)
	}
	if text := sink.text(); !strings.Contains(text, "ERROR") || !strings.Contains(text, "utterance_id=u") {
		t.Fatalf("a record that is lost is an ERROR line that names the utterance:\n%s", text)
	}
	saturated.AssertNothingPersisted(t)
}

func TestUtteranceRecorderRecoversWhenTheParallelLimitFreesUp(t *testing.T) {
	c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	var calls int
	var sleeps int
	recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{
		RetryAttempts: 3, RetryBackoff: time.Second,
		Sleep: func(context.Context, time.Duration) error { sleeps++; return nil },
	})
	recorder.recordFn = func(context.Context, UtteranceRecord) error {
		calls++
		if calls < 3 {
			return fmt.Errorf("%w: occupied", taskmanager.ErrParallelLimit)
		}
		return nil
	}
	recorder.Submit(UtteranceRecord{RequestID: "r", UtteranceID: "u", Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue})
	closeAndWait(t, recorder)
	if calls != 3 || sleeps != 2 {
		t.Fatalf("calls=%d sleeps=%d, want 3 and 2", calls, sleeps)
	}
	if stats := recorder.Stats(); stats.Recorded != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestUtteranceRecorderDoesNotRetryAnyOtherError(t *testing.T) {
	c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	var calls int
	recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{
		Sleep: func(context.Context, time.Duration) error {
			t.Error("no backoff for an error that is not a parallel limit")
			return nil
		},
	})
	recorder.recordFn = func(context.Context, UtteranceRecord) error { calls++; return errors.New("store refused") }
	recorder.Submit(UtteranceRecord{RequestID: "r", UtteranceID: "u", Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue})
	closeAndWait(t, recorder)
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if stats := recorder.Stats(); stats.Failed != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestUtteranceRecorderBackoffIsInterruptedByTheFlushDeadline(t *testing.T) {
	c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	retrying := make(chan struct{})
	recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{
		RetryAttempts: 3, RetryBackoff: time.Hour, // a real sleep this long must be cut short
	})
	recorder.recordFn = func(context.Context, UtteranceRecord) error {
		select {
		case retrying <- struct{}{}:
		default:
		}
		return fmt.Errorf("%w: occupied", taskmanager.ErrParallelLimit)
	}
	recorder.Submit(UtteranceRecord{RequestID: "r", UtteranceID: "u", Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue})
	select {
	case <-retrying:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := recorder.Close(ctx)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Close took %v: the flush must be bounded by its context", elapsed)
	}
	if err == nil {
		t.Fatal("Close must say the flush did not finish")
	}
	if stats := recorder.Stats(); stats.Failed+stats.Dropped != 1 {
		t.Fatalf("the record that could not be finished is counted: %+v", stats)
	}
}

func TestUtteranceRecorderFlushesWhatIsQueuedWhenClosed(t *testing.T) {
	c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	gate := newRecordGate()
	recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{QueueSize: 8})
	recorder.recordFn = gate.fn
	for i := 0; i < 3; i++ {
		recorder.Submit(UtteranceRecord{RequestID: "r", UtteranceID: fmt.Sprintf("u%d", i), Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue})
	}
	go func() {
		for range gate.started {
		}
	}()
	close(gate.release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got := gate.done(); fmt.Sprint(got) != "[u0 u1 u2]" {
		t.Fatalf("recorded %v, want the queue flushed in order", got)
	}
	if recorder.Submit(UtteranceRecord{RequestID: "r", UtteranceID: "late", Outcome: OutcomeFinal, TargetCalled: TargetCalledTrue}) {
		t.Fatal("a closed recorder accepts nothing")
	}
	if stats := recorder.Stats(); stats.Dropped != 1 || stats.Recorded != 3 {
		t.Fatalf("stats = %+v", stats)
	}
	if err := recorder.Close(ctx); err != nil {
		t.Fatalf("a second Close() = %v, want nil", err)
	}
}

func TestUtteranceRecorderWarnsAndCountsAMissingTargetCalledButStillRecords(t *testing.T) {
	c := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
	sink := &logSink{}
	recorder := newTestRecorder(t, c.Actions, c.Tasks, RecorderConfig{Logf: sink.Logf})
	recorder.Submit(UtteranceRecord{RequestID: "r", UtteranceID: "u", Outcome: OutcomeFinal, TargetCalled: TargetCalledMissing})
	closeAndWait(t, recorder)
	stats := recorder.Stats()
	if stats.MissingTargetCalled != 1 || stats.Recorded != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if text := sink.text(); !strings.Contains(text, "WARN") || !strings.Contains(text, "target_called") {
		t.Fatalf("a missing flag is a WARN line:\n%s", text)
	}
	actions, err := c.Actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(actions) != 1 {
		t.Fatalf("the evidence of the external effect must not disappear: Actions=%d err=%v", len(actions), err)
	}
}

func closeAndWait(t *testing.T, recorder *UtteranceRecorder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}
