package superagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunQueueSchedulerRunOnceClaimsAndCompletesDueItem(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
	owner := &recordingTaskOwner{
		now:       func() time.Time { return now },
		tasks:     map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusSucceeded}},
		responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	store := &recordingRunQueueStore{
		runs: []domainsuperagent.AgentRun{{
			RunID: runID, TaskID: taskID, ActorID: "mio", Goal: "run this", Status: "running", StartedAt: now.Add(-time.Minute),
			ResumePolicy: "checkpoint", CheckpointRevision: 1, CheckpointSummary: "request committed", NextAction: "run this", LastCheckpointAt: now.Add(-time.Minute),
		}},
		items: []domainsuperagent.RunQueueItem{
			{
				QueueID:        "q-low",
				TaskID:         modulecore.NewTaskID(),
				RunStartReason: domaintask.RunStartReasonFirst,
				Goal:           "later",
				Action:         "resume",
				Status:         "queued",
				Priority:       1,
				CreatedAt:      now.Add(-2 * time.Minute),
			},
			{
				QueueID:        "q-high",
				TaskID:         taskID,
				RunStartReason: domaintask.RunStartReasonFirst,
				Goal:           "run this",
				Action:         "resume",
				Status:         "queued",
				Priority:       10,
				CreatedAt:      now.Add(-time.Minute),
			},
			{
				QueueID:        "q-future",
				TaskID:         modulecore.NewTaskID(),
				RunStartReason: domaintask.RunStartReasonFirst,
				Goal:           "not yet",
				Action:         "resume",
				Status:         "queued",
				Priority:       100,
				NotBefore:      now.Add(time.Hour),
				CreatedAt:      now.Add(-time.Minute),
			},
		},
	}
	var processed domainsuperagent.RunQueueItem
	var processedTrace modulecore.TraceID
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
		processed = item
		processedTrace = traceID
		return "ok", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("RunOnce() count = %d, want 1", count)
	}
	if processed.QueueID != "q-high" {
		t.Fatalf("processed queue = %q, want q-high", processed.QueueID)
	}
	item := store.item("q-high")
	if item.Status != "completed" || item.Reason != "ok" || item.ClaimedAt.IsZero() || item.CompletedAt.IsZero() {
		t.Fatalf("completed item = %#v", item)
	}
	if run := store.runs[0]; run.Status != "completed" || run.Summary != "ok" || run.CompletedAt.IsZero() {
		t.Fatalf("completed run = %#v", run)
	}
	if len(store.traces) != 2 || store.traces[0].EventType != "run_queue.claimed" || store.traces[1].EventType != "run_queue.completed" || store.traces[1].CausationEventID != store.traces[0].EventID {
		t.Fatalf("unexpected traces = %#v", store.traces)
	}
	for _, event := range store.traces {
		if event.TaskID != taskID || event.RunID != runID {
			t.Fatalf("event identity = task=%q run=%q, want task=%q run=%q", event.TaskID, event.RunID, taskID, runID)
		}
		if _, ok := event.Payload["run_reference"]; ok {
			t.Fatalf("event payload retained run_reference: %#v", event.Payload)
		}
		if _, ok := event.Payload["actor_label"]; ok {
			t.Fatalf("event payload retained actor_label: %#v", event.Payload)
		}
	}
	if processedTrace.Validate() != nil || processedTrace != store.traces[0].TraceID || processedTrace != store.traces[1].TraceID {
		t.Fatalf("processor trace=%q events=%q/%q", processedTrace, store.traces[0].TraceID, store.traces[1].TraceID)
	}
}

func TestRunQueueSchedulerClaimNextReservesLeaseBeforeCanonicalRun(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.UTC)
	item := domainsuperagent.RunQueueItem{
		QueueID:        "reserve-first",
		TaskID:         modulecore.NewTaskID(),
		RunStartReason: domaintask.RunStartReasonFirst,
		RunID:          modulecore.NewRunID(),
		Goal:           "reserve",
		Action:         "resume",
		Status:         "queued",
		CreatedAt:      now.Add(-time.Minute),
	}
	store := &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{item}}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		return "", nil
	}), &recordingTaskOwner{}, RunQueueSchedulerOptions{Now: func() time.Time { return now }, LeaseDuration: time.Minute})

	reserved, err := scheduler.claimNext(context.Background(), now, "lease-first")
	if err != nil {
		t.Fatalf("claimNext() error = %v", err)
	}
	if reserved == nil {
		t.Fatal("claimNext() returned nil item")
	}
	if reserved.Status != "reserved" || reserved.RunID != "" || reserved.AttemptCount != 1 {
		t.Fatalf("reserved item = %#v", *reserved)
	}
	if reserved.LeaseToken != "lease-first" || !reserved.LeaseUntil.Equal(now.Add(time.Minute)) || !reserved.ClaimedAt.Equal(now) {
		t.Fatalf("reservation lease = %#v", *reserved)
	}
}

func TestRunQueueSchedulerDoesNotProcessOrOverwriteAfterLeaseLostBeforeAttachment(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 30, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	base := &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{{
		QueueID:        "stale-attach",
		TaskID:         taskID,
		RunStartReason: domaintask.RunStartReasonFirst,
		Goal:           "attach only while lease is current",
		Action:         "resume",
		Status:         "queued",
		CreatedAt:      now,
	}}}
	store := &staleLeaseRunQueueStore{recordingRunQueueStore: base, now: now}
	owner := &recordingTaskOwner{
		now:       func() time.Time { return now },
		tasks:     map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusSucceeded}},
		responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	processorCalls := 0
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		processorCalls++
		return "must not run", nil
	}), owner, RunQueueSchedulerOptions{
		Now:        func() time.Time { return now },
		ClaimLimit: 1,
		LeaseToken: func() (string, error) { return "owner-1", nil },
	})

	count, err := scheduler.RunOnce(context.Background())
	if count != 0 || err == nil || !strings.Contains(err.Error(), "lease lost before attaching canonical run") {
		t.Fatalf("RunOnce() count=%d err=%v, want explicit attachment lease loss", count, err)
	}
	if processorCalls != 0 {
		t.Fatalf("processor calls=%d, want 0", processorCalls)
	}
	if len(owner.calls) != 1 || owner.calls[0].taskID != taskID {
		t.Fatalf("Task owner calls=%#v, want one canonical Run issuance", owner.calls)
	}
	if len(owner.interruptCalls) != 1 || owner.interruptCalls[0].taskID != taskID || owner.interruptCalls[0].runID != runID || !strings.Contains(owner.interruptCalls[0].summary, "lease lost before attaching canonical run") {
		t.Fatalf("stale scheduler interrupt calls=%#v, want exact issued Run cleanup", owner.interruptCalls)
	}
	if len(owner.failCalls) != 0 || len(owner.blockCalls) != 0 {
		t.Fatalf("stale scheduler changed Task state: fail=%#v block=%#v", owner.failCalls, owner.blockCalls)
	}
	if store.saveCalls != 0 {
		t.Fatalf("stale scheduler SaveRunQueueItem calls=%d, want 0", store.saveCalls)
	}
	if len(base.traces) != 0 {
		t.Fatalf("stale scheduler traces=%#v, want none", base.traces)
	}
	item := base.item("stale-attach")
	if item.Status != "reserved" || item.LeaseToken != "owner-2" || item.RunID != "" || item.AttemptCount != 2 {
		t.Fatalf("newer reservation was overwritten: %#v", item)
	}
}

func TestRunQueueSchedulerCleansIssuedRunWhenAttachmentErrors(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 45, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	attachErr := errors.New("attachment write failed")
	base := &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{{
		QueueID:        "attach-error",
		TaskID:         taskID,
		RunStartReason: domaintask.RunStartReasonFirst,
		Goal:           "cleanup after attachment error",
		Action:         "resume",
		Status:         "queued",
		CreatedAt:      now,
	}}}
	store := &staleLeaseRunQueueStore{recordingRunQueueStore: base, now: now, attachErr: attachErr}
	owner := &recordingTaskOwner{
		now:       func() time.Time { return now },
		tasks:     map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusRunning}},
		responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	processorCalls := 0
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		processorCalls++
		return "must not run", nil
	}), owner, RunQueueSchedulerOptions{
		Now:        func() time.Time { return now },
		ClaimLimit: 1,
		LeaseToken: func() (string, error) { return "owner-1", nil },
	})

	count, err := scheduler.RunOnce(context.Background())
	if count != 0 || err == nil || !errors.Is(err, attachErr) {
		t.Fatalf("RunOnce() count=%d err=%v, want original attachment error", count, err)
	}
	if processorCalls != 0 {
		t.Fatalf("processor calls=%d, want 0", processorCalls)
	}
	if len(owner.interruptCalls) != 1 || owner.interruptCalls[0].taskID != taskID || owner.interruptCalls[0].runID != runID || !strings.Contains(owner.interruptCalls[0].summary, "attachment write failed") {
		t.Fatalf("attachment-error interrupt calls=%#v, want exact issued Run cleanup", owner.interruptCalls)
	}
	if len(owner.failCalls) != 0 || len(owner.blockCalls) != 0 || len(base.traces) != 0 {
		t.Fatalf("attachment-error side effects: fail=%#v block=%#v traces=%#v", owner.failCalls, owner.blockCalls, base.traces)
	}
}

func TestRunQueueSchedulerRunOnceMarksFailure(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	taskID := modulecore.NewTaskID()
	store := &recordingRunQueueStore{
		items: []domainsuperagent.RunQueueItem{{
			QueueID:        "q1",
			TaskID:         taskID,
			RunStartReason: domaintask.RunStartReasonFirst,
			Goal:           "run",
			Action:         "resume",
			Status:         "queued",
			CreatedAt:      now,
		}},
	}
	owner := &recordingTaskOwner{now: func() time.Time { return now }}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, _ domainsuperagent.RunQueueItem, _ modulecore.TraceID) (string, error) {
		return "", errors.New("worker failed")
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want error")
	}
	if count != 0 {
		t.Fatalf("RunOnce() count = %d, want 0", count)
	}
	item := store.item("q1")
	if item.Status != "failed" || item.Reason != "worker failed" || item.CompletedAt.IsZero() {
		t.Fatalf("failed item = %#v", item)
	}
	if len(owner.failCalls) != 1 || owner.failCalls[0].taskID != taskID || owner.failCalls[0].summary != "worker failed" {
		t.Fatalf("canonical task failure calls = %#v", owner.failCalls)
	}
	if task := owner.tasks[taskID]; task.Status != domaintask.StatusFailed || task.Summary != "worker failed" {
		t.Fatalf("canonical task after failure = %#v", task)
	}
	if len(store.traces) != 2 || store.traces[1].EventType != "run_queue.failed" || store.traces[1].CausationEventID != store.traces[0].EventID {
		t.Fatalf("unexpected traces = %#v", store.traces)
	}
}

func TestRunQueueSchedulerPauseCancelsQueueAndPreservesPausedProjection(t *testing.T) {
	now := time.Date(2026, 8, 23, 18, 0, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	processorErr := errors.New("pause requested")
	pausedAt := now.Add(-time.Second)
	store := &recordingRunQueueStore{
		runs: []domainsuperagent.AgentRun{{
			RunID: runID, TaskID: taskID, ActorID: "mio", Goal: "pause this", Status: "paused",
			StartedAt: now.Add(-time.Minute), CompletedAt: pausedAt, Summary: "user requested pause",
		}},
		items: []domainsuperagent.RunQueueItem{{
			QueueID: "pause-q", TaskID: taskID, RunStartReason: domaintask.RunStartReasonFirst,
			Goal: "pause this", Action: "resume", Status: "queued", CreatedAt: now,
		}},
	}
	owner := &recordingTaskOwner{
		now:       func() time.Time { return now },
		responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
		tasks: map[modulecore.TaskID]domaintask.Task{taskID: {
			TaskID: taskID, Status: domaintask.StatusWaiting, WaitingReason: "user requested pause",
		}},
	}
	var processedTrace modulecore.TraceID
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, _ domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
		processedTrace = traceID
		return "", processorErr
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if count != 0 || !errors.Is(err, processorErr) {
		t.Fatalf("RunOnce() count=%d err=%v, want original processor error", count, err)
	}
	item := store.item("pause-q")
	if item.Status != "cancelled" || item.Reason != "user requested pause" || item.RunID != runID || item.CompletedAt.IsZero() {
		t.Fatalf("cancelled queue item = %#v", item)
	}
	if run := store.runs[0]; run.Status != "paused" || run.Summary != "user requested pause" || !run.CompletedAt.Equal(pausedAt) {
		t.Fatalf("paused AgentRun was overwritten = %#v", run)
	}
	if len(owner.failCalls) != 0 || len(owner.getCalls) != 1 || owner.getCalls[0] != taskID {
		t.Fatalf("canonical task owner calls = get=%#v fail=%#v", owner.getCalls, owner.failCalls)
	}
	if len(store.traces) != 2 || store.traces[0].EventType != "run_queue.claimed" || store.traces[1].EventType != "run_queue.cancelled" || store.traces[1].CausationEventID != store.traces[0].EventID {
		t.Fatalf("unexpected pause traces = %#v", store.traces)
	}
	for _, event := range store.traces {
		if event.TaskID != taskID || event.RunID != runID || event.TraceID != processedTrace {
			t.Fatalf("pause event identity = task=%q run=%q trace=%q, want task=%q run=%q trace=%q", event.TaskID, event.RunID, event.TraceID, taskID, runID, processedTrace)
		}
	}
}

func TestRunQueueSchedulerProcessorErrorKeepsClaimWhenTaskLookupFails(t *testing.T) {
	now := time.Date(2026, 8, 23, 18, 30, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	lookupErr := errors.New("canonical task lookup unavailable")
	store := &recordingRunQueueStore{
		runs:  []domainsuperagent.AgentRun{{RunID: runID, TaskID: taskID, ActorID: "mio", Goal: "run", Status: "running", StartedAt: now}},
		items: []domainsuperagent.RunQueueItem{{QueueID: "lookup-fail", TaskID: taskID, RunStartReason: domaintask.RunStartReasonFirst, Goal: "run", Action: "resume", Status: "queued", CreatedAt: now}},
	}
	processorErr := errors.New("worker failed")
	owner := &recordingTaskOwner{
		now:       func() time.Time { return now },
		getErr:    lookupErr,
		responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		return "", processorErr
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if count != 0 || !errors.Is(err, processorErr) || !errors.Is(err, lookupErr) {
		t.Fatalf("RunOnce() count=%d err=%v, want processor and lookup errors", count, err)
	}
	if item := store.item("lookup-fail"); item.Status != "claimed" || !item.CompletedAt.IsZero() {
		t.Fatalf("lookup failure queue item = %#v, want claimed lease", item)
	}
	if run := store.runs[0]; run.Status != "running" || !run.CompletedAt.IsZero() {
		t.Fatalf("lookup failure AgentRun = %#v, want unchanged projection", run)
	}
	if len(owner.failCalls) != 0 || len(store.traces) != 1 || store.traces[0].EventType != "run_queue.claimed" {
		t.Fatalf("lookup failure owner/traces = fail=%#v traces=%#v", owner.failCalls, store.traces)
	}
}

func TestRunQueueSchedulerProcessorErrorDoesNotReFailCanonicalFailedTask(t *testing.T) {
	now := time.Date(2026, 8, 23, 18, 45, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	processorErr := errors.New("worker failed")
	store := &recordingRunQueueStore{
		runs:  []domainsuperagent.AgentRun{{RunID: runID, TaskID: taskID, ActorID: "mio", Goal: "already failed", Status: "running", StartedAt: now}},
		items: []domainsuperagent.RunQueueItem{{QueueID: "already-failed", TaskID: taskID, RunStartReason: domaintask.RunStartReasonFirst, Goal: "already failed", Action: "resume", Status: "queued", CreatedAt: now}},
	}
	owner := &recordingTaskOwner{
		now:       func() time.Time { return now },
		tasks:     map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusFailed, Summary: "canonical failure"}},
		responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		return "", processorErr
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if count != 0 || !errors.Is(err, processorErr) {
		t.Fatalf("RunOnce() count=%d err=%v, want original processor error", count, err)
	}
	if item := store.item("already-failed"); item.Status != "failed" || item.Reason != processorErr.Error() {
		t.Fatalf("already-failed queue item = %#v", item)
	}
	if len(owner.failCalls) != 0 {
		t.Fatalf("canonical failed task was transitioned again = %#v", owner.failCalls)
	}
	if task := owner.tasks[taskID]; task.Status != domaintask.StatusFailed || task.Summary != "canonical failure" {
		t.Fatalf("canonical failed task changed = %#v", task)
	}
}

func TestRunQueueSchedulerProcessorErrorDoesNotOverwriteTerminalTask(t *testing.T) {
	now := time.Date(2026, 8, 23, 19, 0, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	store := &recordingRunQueueStore{
		runs:  []domainsuperagent.AgentRun{{RunID: runID, TaskID: taskID, ActorID: "mio", Goal: "already done", Status: "completed", StartedAt: now.Add(-time.Minute), CompletedAt: now.Add(-time.Second), Summary: "already done"}},
		items: []domainsuperagent.RunQueueItem{{QueueID: "terminal-task", TaskID: taskID, RunStartReason: domaintask.RunStartReasonFirst, Goal: "already done", Action: "resume", Status: "queued", CreatedAt: now}},
	}
	owner := &recordingTaskOwner{
		now:       func() time.Time { return now },
		tasks:     map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusSucceeded}},
		responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		return "", errors.New("stale processor error")
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if count != 0 || err == nil || !strings.Contains(err.Error(), string(domaintask.StatusSucceeded)) {
		t.Fatalf("RunOnce() count=%d err=%v, want explicit terminal-task conflict", count, err)
	}
	if item := store.item("terminal-task"); item.Status != "claimed" {
		t.Fatalf("terminal-task queue was finalized despite conflict = %#v", item)
	}
	if run := store.runs[0]; run.Status != "completed" || run.Summary != "already done" {
		t.Fatalf("terminal AgentRun was overwritten = %#v", run)
	}
	if len(owner.failCalls) != 0 || len(store.traces) != 1 || store.traces[0].EventType != "run_queue.claimed" {
		t.Fatalf("terminal-task owner/traces = fail=%#v traces=%#v", owner.failCalls, store.traces)
	}
}

func TestRunQueueSchedulerSuccessRequiresCanonicalSucceededTask(t *testing.T) {
	now := time.Date(2026, 8, 23, 19, 30, 0, 0, time.UTC)
	statuses := []struct {
		name      string
		status    domaintask.Status
		getErr    error
		wantError string
	}{
		{name: "running", status: domaintask.StatusRunning, wantError: string(domaintask.StatusRunning)},
		{name: "waiting", status: domaintask.StatusWaiting, wantError: string(domaintask.StatusWaiting)},
		{name: "failed", status: domaintask.StatusFailed, wantError: string(domaintask.StatusFailed)},
		{name: "blocked", status: domaintask.StatusBlocked, wantError: string(domaintask.StatusBlocked)},
		{name: "lookup-failed", getErr: errors.New("canonical task lookup unavailable"), wantError: "get canonical task after run queue success"},
	}
	for _, testCase := range statuses {
		t.Run(testCase.name, func(t *testing.T) {
			taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
			store := &recordingRunQueueStore{
				runs:  []domainsuperagent.AgentRun{{RunID: runID, TaskID: taskID, ActorID: "mio", Goal: "complete", Status: "running", StartedAt: now}},
				items: []domainsuperagent.RunQueueItem{{QueueID: "success-state-" + testCase.name, TaskID: taskID, RunStartReason: domaintask.RunStartReasonFirst, Goal: "complete", Action: "resume", Status: "queued", CreatedAt: now}},
			}
			owner := &recordingTaskOwner{
				now:       func() time.Time { return now },
				getErr:    testCase.getErr,
				responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
			}
			if testCase.getErr == nil {
				owner.tasks = map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: testCase.status}}
			}
			scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
				return "ok", nil
			}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

			count, err := scheduler.RunOnce(context.Background())
			if count != 0 || err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("RunOnce() count=%d err=%v, want canonical success rejection", count, err)
			}
			item := store.item("success-state-" + testCase.name)
			if item.Status != "claimed" || !item.CompletedAt.IsZero() {
				t.Fatalf("success-state queue item = %#v, want claimed lease", item)
			}
			if run := store.runs[0]; run.Status != "running" || !run.CompletedAt.IsZero() {
				t.Fatalf("success-state AgentRun = %#v, want unchanged projection", run)
			}
			if len(store.traces) != 1 || store.traces[0].EventType != "run_queue.claimed" || len(owner.failCalls) != 0 {
				t.Fatalf("success-state owner/traces = fail=%#v traces=%#v", owner.failCalls, store.traces)
			}
		})
	}
}

func TestRunQueueSchedulerRecoversOnlyExpiredClaimWithSameCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	oldRunID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
	freshRunID := modulecore.NewRunID()
	owner := &recordingTaskOwner{
		now:       func() time.Time { return now },
		tasks:     map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusSucceeded}},
		responses: []domaintask.Run{{RunID: freshRunID, TaskID: taskID, StartReason: domaintask.RunStartReasonLeaseReacquire, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	store := &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{
		{QueueID: "expired", TaskID: taskID, RunID: oldRunID, RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "continue", Action: "resume", Status: "claimed", LeaseToken: "dead-owner", LeaseUntil: now.Add(-time.Second), CheckpointRevision: 4, AttemptCount: 1, CreatedAt: now.Add(-time.Hour)},
		{QueueID: "active", TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "do not duplicate", Action: "resume", Status: "claimed", LeaseToken: "live-owner", LeaseUntil: now.Add(time.Minute), CheckpointRevision: 2, AttemptCount: 1, CreatedAt: now.Add(-time.Hour)},
	}}
	var processed domainsuperagent.RunQueueItem
	var recoveredTrace modulecore.TraceID
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
		processed = item
		recoveredTrace = traceID
		return "resumed", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1, LeaseDuration: 3 * time.Minute})

	count, err := scheduler.RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if processed.QueueID != "expired" || processed.TaskID != taskID || processed.RunID != freshRunID || processed.CheckpointRevision != 4 || processed.AttemptCount != 2 {
		t.Fatalf("recovered item=%#v", processed)
	}
	if len(owner.calls) != 1 || owner.calls[0].taskID != taskID || owner.calls[0].reason != domaintask.RunStartReasonLeaseReacquire {
		t.Fatalf("run owner calls=%#v", owner.calls)
	}
	if recoveredTrace.Validate() != nil || len(store.traces) == 0 || recoveredTrace != store.traces[0].TraceID {
		t.Fatalf("recovered processor trace=%q events=%#v", recoveredTrace, store.traces)
	}
	if got := store.item("active"); got.LeaseToken != "live-owner" || got.Status != "claimed" {
		t.Fatalf("unexpired claim changed: %#v", got)
	}
}

func TestRunQueueSchedulerOwnerFailureBlocksWithoutInventedRun(t *testing.T) {
	now := time.Date(2026, 8, 23, 17, 0, 0, 0, time.UTC)
	taskID := modulecore.NewTaskID()
	store := &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{{
		QueueID: "blocked-owner", TaskID: taskID, RunStartReason: domaintask.RunStartReasonProcessRestartResume,
		Goal: "resume", Action: "resume", Status: "queued", CreatedAt: now,
	}}}
	owner := &recordingTaskOwner{now: func() time.Time { return now }, err: errors.New("task owner unavailable")}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		t.Fatal("processor must not run when canonical run start fails")
		return "", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	if _, err := scheduler.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want owner error")
	}
	item := store.item("blocked-owner")
	if item.Status != "blocked" || item.RunID != "" || item.LeaseToken != "" || item.CompletedAt.IsZero() || item.Reason == "" {
		t.Fatalf("blocked item = %#v", item)
	}
	if len(store.traces) != 1 || store.traces[0].EventType != "run_queue.blocked" || store.traces[0].TaskID != taskID || store.traces[0].RunID != "" {
		t.Fatalf("blocked trace = %#v", store.traces)
	}
	if _, ok := store.traces[0].Payload["run_reference"]; ok {
		t.Fatalf("blocked payload retained run_reference: %#v", store.traces[0].Payload)
	}
	if _, ok := store.traces[0].Payload["actor_label"]; ok {
		t.Fatalf("blocked payload retained actor_label: %#v", store.traces[0].Payload)
	}
	if owner.tasks[taskID].Status != domaintask.StatusBlocked || len(owner.blockCalls) != 1 || owner.blockCalls[0].taskID != taskID {
		t.Fatalf("canonical Task after start failure = %#v block calls=%#v, want blocked", owner.tasks[taskID], owner.blockCalls)
	}
}

func TestRunQueueSchedulerCleansOwnerRunWhenPostStartValidationFails(t *testing.T) {
	now := time.Date(2026, 8, 23, 17, 30, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	store := &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{{
		QueueID:        "invalid-owner-run",
		TaskID:         taskID,
		RunStartReason: domaintask.RunStartReasonFirst,
		Goal:           "cleanup invalid owner metadata",
		Action:         "resume",
		Status:         "queued",
		CreatedAt:      now,
	}}}
	owner := &recordingTaskOwner{
		now:   func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusRunning}},
		responses: []domaintask.Run{{
			RunID:       runID,
			TaskID:      taskID,
			StartReason: domaintask.RunStartReasonCheckpointResume,
			Status:      domaintask.RunStatusRunning,
			StartedAt:   now,
		}},
	}
	processorCalls := 0
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		processorCalls++
		return "must not run", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if count != 0 || err == nil || !strings.Contains(err.Error(), "start reason") {
		t.Fatalf("RunOnce() count=%d err=%v, want post-start validation error", count, err)
	}
	if processorCalls != 0 {
		t.Fatalf("processor calls=%d, want 0", processorCalls)
	}
	if item := store.item("invalid-owner-run"); item.Status != "blocked" || item.RunID != "" || item.Reason == "" || item.CompletedAt.IsZero() {
		t.Fatalf("blocked queue item = %#v", item)
	}
	if len(owner.interruptCalls) != 1 || owner.interruptCalls[0].taskID != taskID || owner.interruptCalls[0].runID != runID || !strings.Contains(owner.interruptCalls[0].summary, "start reason") {
		t.Fatalf("owner interrupt calls=%#v, want exact issued Run cleanup", owner.interruptCalls)
	}
	if len(owner.failCalls) != 0 || len(owner.blockCalls) != 1 || owner.blockCalls[0].taskID != taskID || owner.tasks[taskID].Status != domaintask.StatusBlocked {
		t.Fatalf("post-start validation Task state: tasks=%#v fail=%#v block=%#v, want blocked", owner.tasks, owner.failCalls, owner.blockCalls)
	}
	if len(store.traces) != 1 || store.traces[0].EventType != "run_queue.blocked" || store.traces[0].RunID != "" {
		t.Fatalf("blocked trace = %#v", store.traces)
	}
}

func TestRunQueueSchedulerReportsTaskMismatchWithoutGuessingCleanupOwner(t *testing.T) {
	now := time.Date(2026, 8, 23, 17, 45, 0, 0, time.UTC)
	taskID, otherTaskID, runID := modulecore.NewTaskID(), modulecore.NewTaskID(), modulecore.NewRunID()
	store := &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{{
		QueueID:        "mismatched-owner-run",
		TaskID:         taskID,
		RunStartReason: domaintask.RunStartReasonFirst,
		Goal:           "do not guess cleanup task",
		Action:         "resume",
		Status:         "queued",
		CreatedAt:      now,
	}}}
	owner := &recordingTaskOwner{
		now: func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{
			taskID:      {TaskID: taskID, Status: domaintask.StatusRunning},
			otherTaskID: {TaskID: otherTaskID, Status: domaintask.StatusRunning},
		},
		responses: []domaintask.Run{{
			RunID:       runID,
			TaskID:      otherTaskID,
			StartReason: domaintask.RunStartReasonFirst,
			Status:      domaintask.RunStatusRunning,
			StartedAt:   now,
		}},
	}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		t.Fatal("processor must not run after owner Task mismatch")
		return "", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if count != 0 || err == nil || !strings.Contains(err.Error(), "does not match queue task_id") {
		t.Fatalf("RunOnce() count=%d err=%v, want explicit cleanup boundary", count, err)
	}
	if len(owner.interruptCalls) != 0 {
		t.Fatalf("mismatched owner was cleaned through guessed Task: %#v", owner.interruptCalls)
	}
	if len(owner.blockCalls) != 1 || owner.blockCalls[0].taskID != taskID || owner.tasks[taskID].Status != domaintask.StatusBlocked {
		t.Fatalf("requested Task was not blocked after mismatch: tasks=%#v block=%#v", owner.tasks, owner.blockCalls)
	}
	if item := store.item("mismatched-owner-run"); item.Status != "blocked" || item.RunID != "" {
		t.Fatalf("queue item = %#v, want blocked without Run attachment", item)
	}
}

type recordingRunQueueStore struct {
	runs   []domainsuperagent.AgentRun
	items  []domainsuperagent.RunQueueItem
	traces []modulecore.EventEnvelope
}

type staleLeaseRunQueueStore struct {
	*recordingRunQueueStore
	now       time.Time
	saveCalls int
	attachErr error
}

func (s *staleLeaseRunQueueStore) SaveRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) error {
	s.saveCalls++
	return s.recordingRunQueueStore.SaveRunQueueItem(ctx, item)
}

func (s *staleLeaseRunQueueStore) ClaimNextRunQueueItem(_ context.Context, now, leaseUntil time.Time, leaseToken string) (*domainsuperagent.RunQueueItem, error) {
	for index := range s.items {
		item := s.items[index]
		if item.Status != "queued" && !(item.Status == "reserved" && !item.LeaseUntil.After(now)) {
			continue
		}
		item.Status = "reserved"
		item.ClaimedAt = now
		item.LeaseToken = leaseToken
		item.LeaseUntil = leaseUntil
		item.RunID = ""
		item.AttemptCount++
		item.CompletedAt = time.Time{}
		s.items[index] = item
		return &item, nil
	}
	return nil, nil
}

func (s *staleLeaseRunQueueStore) AttachRunQueueRun(_ context.Context, queueID, leaseToken string, _ modulecore.RunID) (bool, error) {
	if s.attachErr != nil {
		return false, s.attachErr
	}
	for index := range s.items {
		if s.items[index].QueueID != queueID || s.items[index].LeaseToken != leaseToken {
			continue
		}
		item := s.items[index]
		item.Status = "reserved"
		item.LeaseToken = "owner-2"
		item.LeaseUntil = s.now.Add(2 * time.Minute)
		item.AttemptCount++
		item.RunID = ""
		s.items[index] = item
		return false, nil
	}
	return false, nil
}

func (s *staleLeaseRunQueueStore) RenewRunQueueLease(context.Context, string, string, time.Time) (bool, error) {
	return true, nil
}

func (s *staleLeaseRunQueueStore) CompleteRunQueueItem(context.Context, string, string, string, string, time.Time) (bool, error) {
	return false, nil
}

func (s *recordingRunQueueStore) ListAgentRuns(context.Context, int) ([]domainsuperagent.AgentRun, error) {
	return append([]domainsuperagent.AgentRun{}, s.runs...), nil
}

func (s *recordingRunQueueStore) SaveAgentRun(_ context.Context, item domainsuperagent.AgentRun) error {
	for index := range s.runs {
		if s.runs[index].RunID == item.RunID {
			s.runs[index] = item
			return nil
		}
	}
	s.runs = append(s.runs, item)
	return nil
}

func (s *recordingRunQueueStore) ListRunQueueItems(context.Context, int) ([]domainsuperagent.RunQueueItem, error) {
	return append([]domainsuperagent.RunQueueItem{}, s.items...), nil
}

func (s *recordingRunQueueStore) SaveRunQueueItem(_ context.Context, item domainsuperagent.RunQueueItem) error {
	for idx := range s.items {
		if s.items[idx].QueueID == item.QueueID {
			s.items[idx] = item
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}

func (s *recordingRunQueueStore) Append(_ context.Context, item modulecore.EventEnvelope) error {
	s.traces = append(s.traces, item)
	return nil
}

func (s *recordingRunQueueStore) item(queueID string) domainsuperagent.RunQueueItem {
	for _, item := range s.items {
		if item.QueueID == queueID {
			return item
		}
	}
	return domainsuperagent.RunQueueItem{}
}

func TestRecoverInterruptedAgentRunsQueuesOnlyDurableCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)
	resumableRunID, legacyRunID, finishedRunID, queueFinishedRunID := modulecore.NewRunID(), modulecore.NewRunID(), modulecore.NewRunID(), modulecore.NewRunID()
	resumableTaskID, legacyTaskID, finishedTaskID, queueFinishedTaskID := modulecore.NewTaskID(), modulecore.NewTaskID(), modulecore.NewTaskID(), modulecore.NewTaskID()
	finishedQueueID := "resume:" + string(finishedRunID) + ":1"
	queueFinishedQueueID := "resume:" + string(queueFinishedRunID) + ":2"
	store := &recordingRunQueueStore{runs: []domainsuperagent.AgentRun{
		{RunID: resumableRunID, TaskID: resumableTaskID, WorkstreamID: "thread-1", ActorID: "mio", Goal: "continue", Status: "running", StartedAt: now.Add(-time.Hour), ResumePolicy: "checkpoint", CheckpointRevision: 5, CheckpointSummary: "step four committed", NextAction: "step five", LastCheckpointAt: now.Add(-time.Minute)},
		{RunID: legacyRunID, TaskID: legacyTaskID, ActorID: "mio", Goal: "unknown position", Status: "running", StartedAt: now.Add(-time.Hour)},
		{RunID: finishedRunID, TaskID: finishedTaskID, ActorID: "mio", Goal: "done", Status: "completed", StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-time.Minute), Summary: "receipt committed", ResumePolicy: "checkpoint", CheckpointRevision: 1, CheckpointSummary: "dispatch", NextAction: "execute", LastCheckpointAt: now.Add(-time.Hour)},
		{RunID: queueFinishedRunID, TaskID: queueFinishedTaskID, ActorID: "mio", Goal: "done by queue", Status: "running", StartedAt: now.Add(-time.Hour), ResumePolicy: "checkpoint", CheckpointRevision: 2, CheckpointSummary: "dispatch", NextAction: "execute", LastCheckpointAt: now.Add(-time.Hour)},
	}, items: []domainsuperagent.RunQueueItem{
		{QueueID: finishedQueueID, TaskID: finishedTaskID, RunID: finishedRunID, RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "done", Action: "resume", Status: "claimed", ClaimedAt: now.Add(-2 * time.Minute), LeaseToken: "dead", LeaseUntil: now.Add(time.Minute), CheckpointRevision: 1, CreatedAt: now.Add(-2 * time.Minute)},
		{QueueID: queueFinishedQueueID, TaskID: queueFinishedTaskID, RunID: queueFinishedRunID, RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "done by queue", Action: "resume", Status: "completed", Reason: "queue receipt", CompletedAt: now.Add(-time.Minute), CheckpointRevision: 2, CreatedAt: now.Add(-2 * time.Minute)},
	}}
	finishedAt := now.Add(-time.Minute)
	queueFinishedAt := now.Add(-30 * time.Second)
	owner := &recordingTaskOwner{
		now: func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{
			resumableTaskID:     {TaskID: resumableTaskID, Status: domaintask.StatusRunning},
			legacyTaskID:        {TaskID: legacyTaskID, Status: domaintask.StatusRunning},
			finishedTaskID:      {TaskID: finishedTaskID, Status: domaintask.StatusSucceeded, Summary: "receipt committed"},
			queueFinishedTaskID: {TaskID: queueFinishedTaskID, Status: domaintask.StatusSucceeded, Summary: "canonical queue receipt"},
		},
		runs: map[modulecore.TaskID][]domaintask.Run{
			resumableTaskID:     {{RunID: resumableRunID, TaskID: resumableTaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now.Add(-time.Hour)}},
			legacyTaskID:        {{RunID: legacyRunID, TaskID: legacyTaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now.Add(-time.Hour)}},
			finishedTaskID:      {{RunID: finishedRunID, TaskID: finishedTaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusSucceeded, StartedAt: now.Add(-time.Hour), CompletedAt: &finishedAt, Summary: "receipt committed"}},
			queueFinishedTaskID: {{RunID: queueFinishedRunID, TaskID: queueFinishedTaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusSucceeded, StartedAt: now.Add(-time.Hour), CompletedAt: &queueFinishedAt, Summary: "canonical queue receipt"}},
		},
	}
	queued, blocked, err := RecoverInterruptedAgentRuns(context.Background(), store, owner, now)
	if err != nil || queued != 1 || blocked != 1 {
		t.Fatalf("RecoverInterruptedAgentRuns queued=%d blocked=%d err=%v", queued, blocked, err)
	}
	resumableQueueID := "resume:" + string(resumableTaskID) + ":" + string(resumableRunID) + ":5"
	resumable := store.item(resumableQueueID)
	if len(store.items) != 3 || resumable.TaskID != resumableTaskID || resumable.RunID != "" || resumable.RunStartReason != domaintask.RunStartReasonProcessRestartResume || resumable.IdempotencyKey != resumableQueueID || resumable.CheckpointSummary != "step four committed" || resumable.NextAction != "step five" || store.item(finishedQueueID).Status != "claimed" {
		t.Fatalf("recovery queue=%#v", store.items)
	}
	if run := store.runs[3]; run.Status != "completed" || run.Summary != "canonical queue receipt" || !run.CompletedAt.Equal(queueFinishedAt) {
		t.Fatalf("canonical terminal did not repair projection: %#v", run)
	}
	queued, blocked, err = RecoverInterruptedAgentRuns(context.Background(), store, owner, now.Add(time.Second))
	if err != nil || queued != 0 || blocked != 0 || len(store.items) != 3 {
		t.Fatalf("idempotent recovery queued=%d blocked=%d items=%#v err=%v", queued, blocked, store.items, err)
	}
}

func TestRecoverInterruptedAgentRunsRepairsHistoricalRunWhileRecoveringCurrentRun(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	taskID := modulecore.NewTaskID()
	oldRunID, activeRunID := modulecore.NewRunID(), modulecore.NewRunID()
	oldCompletedAt := now.Add(-time.Hour)
	store := &recordingRunQueueStore{
		runs: []domainsuperagent.AgentRun{
			{RunID: oldRunID, TaskID: taskID, ActorID: "mio", Goal: "historical", Status: "running", StartedAt: now.Add(-2 * time.Hour), Summary: "stale projection"},
			{RunID: activeRunID, TaskID: taskID, ActorID: "mio", Goal: "continue current", Status: "running", StartedAt: now.Add(-time.Minute), ResumePolicy: "checkpoint", CheckpointRevision: 4, CheckpointSummary: "step four committed", NextAction: "step five", LastCheckpointAt: now.Add(-30 * time.Second)},
		},
		items: []domainsuperagent.RunQueueItem{{
			QueueID: "resume:" + string(taskID) + ":" + string(oldRunID) + ":4", TaskID: taskID, RunID: oldRunID,
			RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "historical", Action: "resume", Status: "completed",
			CheckpointRevision: 4, CompletedAt: oldCompletedAt, CreatedAt: now.Add(-time.Hour),
		}},
	}
	owner := &recordingTaskOwner{
		now:   func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusRunning}},
		runs: map[modulecore.TaskID][]domaintask.Run{taskID: {
			{RunID: oldRunID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusSucceeded, StartedAt: now.Add(-2 * time.Hour), CompletedAt: &oldCompletedAt, Summary: "historical success"},
			{RunID: activeRunID, TaskID: taskID, StartReason: domaintask.RunStartReasonProcessRestartResume, Status: domaintask.RunStatusRunning, StartedAt: now.Add(-time.Minute)},
		}},
	}

	queued, blocked, err := RecoverInterruptedAgentRuns(context.Background(), store, owner, now)
	if err != nil || queued != 1 || blocked != 0 {
		t.Fatalf("RecoverInterruptedAgentRuns queued=%d blocked=%d err=%v", queued, blocked, err)
	}
	var historical domainsuperagent.AgentRun
	for _, run := range store.runs {
		if run.RunID == oldRunID {
			historical = run
			break
		}
	}
	if historical.Status != "completed" || historical.Summary != "historical success" || !historical.CompletedAt.Equal(oldCompletedAt) {
		t.Fatalf("historical projection=%#v", historical)
	}
	queueID := "resume:" + string(taskID) + ":" + string(activeRunID) + ":4"
	resumed := store.item(queueID)
	if resumed.QueueID != queueID || resumed.TaskID != taskID || resumed.RunID != "" || resumed.Status != "queued" || resumed.IdempotencyKey != queueID {
		t.Fatalf("active recovery queue=%#v", resumed)
	}
}

func TestRecoverInterruptedAgentRunsRebuildsViewerCheckpointResumeGap(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	completedAt := now.Add(-time.Minute)
	store := &recordingRunQueueStore{runs: []domainsuperagent.AgentRun{{
		RunID: runID, TaskID: taskID, WorkstreamID: "thread-gap", ActorID: "mio", Goal: "continue after viewer crash", Status: "paused", StartedAt: now.Add(-time.Hour), CompletedAt: completedAt,
		ResumePolicy: "checkpoint", CheckpointRevision: 7, CheckpointSummary: "step seven committed", NextAction: "step eight", LastCheckpointAt: now.Add(-2 * time.Minute),
	}}}
	owner := &recordingTaskOwner{
		now:   func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusQueued}},
		runs: map[modulecore.TaskID][]domaintask.Run{taskID: {{
			RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusWaiting, StartedAt: now.Add(-time.Hour), CompletedAt: &completedAt, Summary: "waiting for checkpoint resume",
		}}},
	}

	queued, blocked, err := RecoverInterruptedAgentRuns(context.Background(), store, owner, now)
	if err != nil || queued != 1 || blocked != 0 {
		t.Fatalf("RecoverInterruptedAgentRuns queued=%d blocked=%d err=%v", queued, blocked, err)
	}
	queueID := "resume:" + string(taskID) + ":" + string(runID) + ":7"
	item := store.item(queueID)
	if item.QueueID != queueID || item.TaskID != taskID || item.RunID != "" || item.RunStartReason != domaintask.RunStartReasonCheckpointResume || item.Status != "queued" || item.WorkstreamID != "thread-gap" || item.Goal != "continue after viewer crash" || item.Action != "resume" || item.CheckpointRevision != 7 || item.CheckpointSummary != "step seven committed" || item.NextAction != "step eight" || item.IdempotencyKey != queueID {
		t.Fatalf("checkpoint resume queue=%#v", item)
	}
	if store.runs[0].Status != "paused" || !store.runs[0].CompletedAt.Equal(completedAt) {
		t.Fatalf("waiting projection=%#v", store.runs[0])
	}
	queued, blocked, err = RecoverInterruptedAgentRuns(context.Background(), store, owner, now.Add(time.Second))
	if err != nil || queued != 0 || blocked != 0 || len(store.items) != 1 {
		t.Fatalf("idempotent checkpoint resume queued=%d blocked=%d items=%#v err=%v", queued, blocked, store.items, err)
	}
}

func TestRecoverInterruptedAgentRunsRejectsMismatchedViewerCheckpointResumeIntent(t *testing.T) {
	now := time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	completedAt := now.Add(-time.Minute)
	queueID := "resume:" + string(taskID) + ":" + string(runID) + ":7"
	store := &recordingRunQueueStore{
		runs: []domainsuperagent.AgentRun{{
			RunID: runID, TaskID: taskID, WorkstreamID: "thread-gap", ActorID: "mio", Goal: "continue after viewer crash", Status: "running", StartedAt: now.Add(-time.Hour),
			ResumePolicy: "checkpoint", CheckpointRevision: 7, CheckpointSummary: "step seven committed", NextAction: "step eight", LastCheckpointAt: now.Add(-2 * time.Minute),
		}},
		items: []domainsuperagent.RunQueueItem{{
			QueueID: queueID, TaskID: taskID, RunStartReason: domaintask.RunStartReasonCheckpointResume, WorkstreamID: "thread-gap", Goal: "wrong goal", Action: "resume", Status: "queued",
			CheckpointRevision: 7, CheckpointSummary: "step seven committed", NextAction: "step eight", IdempotencyKey: queueID, CreatedAt: now,
		}},
	}
	owner := &recordingTaskOwner{
		now:   func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{taskID: {TaskID: taskID, Status: domaintask.StatusQueued}},
		runs: map[modulecore.TaskID][]domaintask.Run{taskID: {{
			RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusWaiting, StartedAt: now.Add(-time.Hour), CompletedAt: &completedAt, Summary: "waiting for checkpoint resume",
		}}},
	}

	beforeProjection := store.runs[0]
	beforeQueue := store.items[0]
	queued, blocked, err := RecoverInterruptedAgentRuns(context.Background(), store, owner, now)
	if err == nil || !strings.Contains(err.Error(), "recovery queue intent mismatch") || queued != 0 || blocked != 0 {
		t.Fatalf("mismatched recovery queued=%d blocked=%d err=%v", queued, blocked, err)
	}
	if store.runs[0] != beforeProjection || store.items[0] != beforeQueue {
		t.Fatalf("mismatched intent caused mutation: projection=%#v queue=%#v", store.runs[0], store.items[0])
	}
}

func TestRecoverInterruptedAgentRunsRejectsMalformedProcessRestartIntentBeforeProjectionRepair(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	terminalTaskID, terminalRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	activeTaskID, activeRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	terminalCompletedAt := now.Add(-time.Hour)
	queueID := "resume:" + string(activeTaskID) + ":" + string(activeRunID) + ":8"
	store := &recordingRunQueueStore{
		runs: []domainsuperagent.AgentRun{
			{RunID: terminalRunID, TaskID: terminalTaskID, ActorID: "mio", Goal: "historical", Status: "running", StartedAt: now.Add(-2 * time.Hour), Summary: "stale historical projection"},
			{RunID: activeRunID, TaskID: activeTaskID, WorkstreamID: "thread-active", ActorID: "mio", Goal: "continue active", Status: "running", StartedAt: now.Add(-time.Minute), ResumePolicy: "checkpoint", CheckpointRevision: 8, CheckpointSummary: "step eight committed", NextAction: "step nine", LastCheckpointAt: now.Add(-30 * time.Second)},
		},
		items: []domainsuperagent.RunQueueItem{{
			QueueID: queueID, TaskID: activeTaskID, RunStartReason: domaintask.RunStartReasonProcessRestartResume, WorkstreamID: "thread-active", Goal: "wrong active goal", Action: "resume", Status: "queued",
			CheckpointRevision: 8, CheckpointSummary: "step eight committed", NextAction: "step nine", IdempotencyKey: queueID, CreatedAt: now,
		}},
	}
	owner := &recordingTaskOwner{
		now: func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{
			terminalTaskID: {TaskID: terminalTaskID, Status: domaintask.StatusSucceeded},
			activeTaskID:   {TaskID: activeTaskID, Status: domaintask.StatusRunning},
		},
		runs: map[modulecore.TaskID][]domaintask.Run{
			terminalTaskID: {{RunID: terminalRunID, TaskID: terminalTaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusSucceeded, StartedAt: now.Add(-2 * time.Hour), CompletedAt: &terminalCompletedAt, Summary: "historical success"}},
			activeTaskID:   {{RunID: activeRunID, TaskID: activeTaskID, StartReason: domaintask.RunStartReasonProcessRestartResume, Status: domaintask.RunStatusRunning, StartedAt: now.Add(-time.Minute)}},
		},
	}
	beforeTerminal := store.runs[0]
	beforeActive := store.runs[1]
	beforeQueue := store.items[0]
	queued, blocked, err := RecoverInterruptedAgentRuns(context.Background(), store, owner, now)
	if err == nil || !strings.Contains(err.Error(), "recovery queue intent mismatch") || queued != 0 || blocked != 0 {
		t.Fatalf("malformed process restart queued=%d blocked=%d err=%v", queued, blocked, err)
	}
	if store.runs[0] != beforeTerminal || store.runs[1] != beforeActive || store.items[0] != beforeQueue {
		t.Fatalf("malformed process restart caused mutation: runs=%#v queue=%#v", store.runs, store.items)
	}
}

type runOwnerCall struct {
	taskID modulecore.TaskID
	reason domaintask.RunStartReason
}

type runOwnerInterruptCall struct {
	taskID  modulecore.TaskID
	runID   modulecore.RunID
	summary string
}

type recordingTaskOwner struct {
	now            func() time.Time
	responses      []domaintask.Run
	err            error
	calls          []runOwnerCall
	interruptErr   error
	interruptCalls []runOwnerInterruptCall
	tasks          map[modulecore.TaskID]domaintask.Task
	runs           map[modulecore.TaskID][]domaintask.Run
	getErr         error
	getCalls       []modulecore.TaskID
	blockErr       error
	blockCalls     []taskOwnerBlockCall
	failErr        error
	failCalls      []taskOwnerFailCall
}

type taskOwnerFailCall struct {
	taskID      modulecore.TaskID
	summary     string
	nextActions []string
}

type taskOwnerBlockCall struct {
	taskID modulecore.TaskID
	reason string
}

func (o *recordingTaskOwner) Get(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	o.getCalls = append(o.getCalls, taskID)
	if o.getErr != nil {
		return domaintask.Task{}, o.getErr
	}
	if task, ok := o.tasks[taskID]; ok {
		return task, nil
	}
	return domaintask.Task{TaskID: taskID, Status: domaintask.StatusRunning}, nil
}

func (o *recordingTaskOwner) ListRuns(_ context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	return append([]domaintask.Run(nil), o.runs[filter.TaskID]...), nil
}

func (o *recordingTaskOwner) Block(_ context.Context, taskID modulecore.TaskID, reason string) (domaintask.Task, error) {
	o.blockCalls = append(o.blockCalls, taskOwnerBlockCall{taskID: taskID, reason: reason})
	if o.blockErr != nil {
		return domaintask.Task{}, o.blockErr
	}
	now := time.Now().UTC()
	if o.now != nil {
		now = o.now().UTC()
	}
	task, ok := o.tasks[taskID]
	if !ok {
		task = domaintask.Task{TaskID: taskID}
	}
	task.Status, task.Summary, task.WaitingReason = domaintask.StatusBlocked, reason, reason
	task.FinishedAt = &now
	if o.tasks == nil {
		o.tasks = make(map[modulecore.TaskID]domaintask.Task)
	}
	o.tasks[taskID] = task
	for index := range o.runs[taskID] {
		if o.runs[taskID][index].Status != domaintask.RunStatusRunning {
			continue
		}
		closed, err := o.runs[taskID][index].Close(domaintask.RunStatusBlocked, now, reason)
		if err != nil {
			return domaintask.Task{}, err
		}
		o.runs[taskID][index] = closed
		break
	}
	return task, nil
}

func (o *recordingTaskOwner) Fail(_ context.Context, taskID modulecore.TaskID, summary string, nextActions []string) (domaintask.Task, error) {
	o.failCalls = append(o.failCalls, taskOwnerFailCall{taskID: taskID, summary: summary, nextActions: append([]string(nil), nextActions...)})
	if o.failErr != nil {
		return domaintask.Task{}, o.failErr
	}
	task, ok := o.tasks[taskID]
	if !ok {
		task = domaintask.Task{TaskID: taskID, Status: domaintask.StatusRunning}
	}
	task.Status = domaintask.StatusFailed
	task.Summary = summary
	if o.tasks == nil {
		o.tasks = make(map[modulecore.TaskID]domaintask.Task)
	}
	o.tasks[taskID] = task
	return task, nil
}

func (o *recordingTaskOwner) StartRunWithReason(_ context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error) {
	o.calls = append(o.calls, runOwnerCall{taskID: taskID, reason: reason})
	if o.err != nil {
		return domaintask.Run{}, o.err
	}
	if len(o.responses) > 0 {
		run := o.responses[0]
		o.responses = o.responses[1:]
		return run, nil
	}
	now := time.Now().UTC()
	if o.now != nil {
		now = o.now().UTC()
	}
	return domaintask.Run{
		RunID:       modulecore.NewRunID(),
		TaskID:      taskID,
		StartReason: reason,
		Status:      domaintask.RunStatusRunning,
		StartedAt:   now,
	}, nil
}

func (o *recordingTaskOwner) InterruptRun(_ context.Context, taskID modulecore.TaskID, runID modulecore.RunID, summary string) (domaintask.Run, error) {
	o.interruptCalls = append(o.interruptCalls, runOwnerInterruptCall{taskID: taskID, runID: runID, summary: summary})
	if o.interruptErr != nil {
		return domaintask.Run{}, o.interruptErr
	}
	for index := range o.runs[taskID] {
		if o.runs[taskID][index].RunID != runID || o.runs[taskID][index].Status != domaintask.RunStatusRunning {
			continue
		}
		now := time.Now().UTC()
		if o.now != nil {
			now = o.now().UTC()
		}
		closed, err := o.runs[taskID][index].Close(domaintask.RunStatusInterrupted, now, summary)
		if err != nil {
			return domaintask.Run{}, err
		}
		o.runs[taskID][index] = closed
		return closed, nil
	}
	return domaintask.Run{RunID: runID, TaskID: taskID, Status: domaintask.RunStatusInterrupted, Summary: summary}, nil
}
