package superagent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type actorPlannedScheduler interface {
	PlanForActor(context.Context, string) (*domainsuperagent.RunQueueItem, domaintask.Task, error)
	RunPlanned(context.Context, modulecore.QueueItemID, string) (bool, error)
}

type actorPlannedQueueStore interface {
	ListRunQueueCandidatesForTasks(context.Context, []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error)
	ClaimRunQueueItem(context.Context, modulecore.QueueItemID, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
	ReleaseRunQueueItem(context.Context, modulecore.QueueItemID, string) (bool, error)
}

func requireActorPlannedScheduler(t *testing.T, scheduler *RunQueueScheduler) actorPlannedScheduler {
	t.Helper()
	planned, ok := any(scheduler).(actorPlannedScheduler)
	if !ok {
		t.Fatal("RunQueueScheduler does not expose actor-scoped planning and execution")
	}
	return planned
}

func TestRunQueueSchedulerPlanForActorUsesCanonicalTaskOrderingAndIsReadOnly(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	actor := "mio"
	tieCreatedAt := now.Add(-3 * time.Minute)
	tieTaskA, tieTaskB := modulecore.NewTaskID(), modulecore.NewTaskID()
	firstTieID, secondTieID := tieTaskA, tieTaskB
	if string(secondTieID) < string(firstTieID) {
		firstTieID, secondTieID = secondTieID, firstTieID
	}
	highLaterTask := modulecore.NewTaskID()
	normalOldTask := modulecore.NewTaskID()
	wrongActorTask := modulecore.NewTaskID()
	runningTask := modulecore.NewTaskID()
	legacyAttemptTask := modulecore.NewTaskID()
	futureTask := modulecore.NewTaskID()
	unsafeResumeTask := modulecore.NewTaskID()
	queueTieA, queueTieB := modulecore.NewQueueItemID(), modulecore.NewQueueItemID()
	queueHighLater, queueNormalOld := modulecore.NewQueueItemID(), modulecore.NewQueueItemID()
	queueWrongActor, queueRunning := modulecore.NewQueueItemID(), modulecore.NewQueueItemID()
	queueOldAttempt, queueFuture := modulecore.NewQueueItemID(), modulecore.NewQueueItemID()
	queueUnsafeResume := modulecore.NewQueueItemID()

	tasks := map[modulecore.TaskID]domaintask.Task{
		tieTaskA:       actorPlanningTask(tieTaskA, actor, domaintask.StatusQueued, domaintask.PriorityHigh, tieCreatedAt),
		tieTaskB:       actorPlanningTask(tieTaskB, actor, domaintask.StatusQueued, domaintask.PriorityHigh, tieCreatedAt),
		highLaterTask:  actorPlanningTask(highLaterTask, actor, domaintask.StatusQueued, domaintask.PriorityHigh, now.Add(-2*time.Minute)),
		normalOldTask:  actorPlanningTask(normalOldTask, actor, domaintask.StatusQueued, domaintask.PriorityNormal, now.Add(-time.Hour)),
		wrongActorTask: actorPlanningTask(wrongActorTask, "shiro", domaintask.StatusQueued, domaintask.PriorityCritical, now.Add(-2*time.Hour)),
		runningTask:    actorPlanningTask(runningTask, actor, domaintask.StatusRunning, domaintask.PriorityCritical, now.Add(-3*time.Hour)),
		legacyAttemptTask: actorPlanningTask(legacyAttemptTask, actor, domaintask.StatusQueued,
			domaintask.PriorityCritical, now.Add(-4*time.Hour)),
		futureTask: actorPlanningTask(futureTask, actor, domaintask.StatusQueued, domaintask.PriorityCritical, now.Add(-5*time.Hour)),
		unsafeResumeTask: actorPlanningTask(unsafeResumeTask, actor, domaintask.StatusQueued,
			domaintask.PriorityCritical, now.Add(-6*time.Hour)),
	}
	items := []domainsuperagent.RunQueueItem{
		actorPlanningItem(queueTieA, tieTaskA, 50, now.Add(-10*time.Minute)),
		actorPlanningItem(queueTieB, tieTaskB, -50, now.Add(-9*time.Minute)),
		actorPlanningItem(queueHighLater, highLaterTask, 10000, now.Add(-20*time.Minute)),
		actorPlanningItem(queueNormalOld, normalOldTask, 100000, now.Add(-30*time.Minute)),
		actorPlanningItem(queueWrongActor, wrongActorTask, 1000000, now.Add(-40*time.Minute)),
		actorPlanningItem(queueRunning, runningTask, 1000000, now.Add(-50*time.Minute)),
		actorPlanningItem(queueOldAttempt, legacyAttemptTask, 1000000, now.Add(-60*time.Minute)),
		actorPlanningItem(queueFuture, futureTask, 1000000, now.Add(-70*time.Minute)),
		actorPlanningItem(queueUnsafeResume, unsafeResumeTask, 1000000, now.Add(-80*time.Minute)),
	}
	items[6].AttemptCount = 1
	items[7].NotBefore = now.Add(time.Hour)
	items[8].RunStartReason = domaintask.RunStartReasonLeaseReacquire
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: items}}
	owner := &recordingTaskOwner{tasks: tasks}
	processorCalls := 0
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		processorCalls++
		return "", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ActorTaskLister: actorPlanningTaskLister(owner)})
	before := append([]domainsuperagent.RunQueueItem(nil), store.items...)

	planned := requireActorPlannedScheduler(t, scheduler)
	item, task, err := planned.PlanForActor(context.Background(), actor)
	if err != nil {
		t.Fatalf("PlanForActor() error = %v", err)
	}
	if item == nil {
		t.Fatal("PlanForActor() returned nil item")
	}
	if item.TaskID != firstTieID {
		t.Fatalf("selected TaskID = %s, want oldest highest-priority TaskID %s (tie IDs %s, %s)", item.TaskID, firstTieID, tieTaskA, tieTaskB)
	}
	if task.TaskID != firstTieID || task.Assignee != actor {
		t.Fatalf("selected Task = %#v", task)
	}
	if !reflect.DeepEqual(store.items, before) {
		t.Fatal("PlanForActor() mutated queue state")
	}
	if len(owner.calls) != 0 || processorCalls != 0 || len(store.traces) != 0 {
		t.Fatalf("planning caused execution: starts=%d processor=%d events=%d", len(owner.calls), processorCalls, len(store.traces))
	}
}

func TestRunQueueSchedulerRunPlannedClaimsOnlyExactItem(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	taskID, otherTaskID := modulecore.NewTaskID(), modulecore.NewTaskID()
	targetID, higherPriorityID := modulecore.NewQueueItemID(), modulecore.NewQueueItemID()
	target := actorPlanningItem(targetID, taskID, 1, now.Add(-time.Minute))
	higherPriority := actorPlanningItem(higherPriorityID, otherTaskID, 999, now.Add(-2*time.Minute))
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{target, higherPriority}}}
	runID := modulecore.NewRunID()
	owner := &recordingTaskOwner{
		now: func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{
			taskID: actorPlanningTask(taskID, "mio", domaintask.StatusQueued, domaintask.PriorityNormal, now.Add(-time.Hour)),
		},
		responses: []domaintask.Run{{RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	var processed modulecore.QueueItemID
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, item domainsuperagent.RunQueueItem, _ modulecore.TraceID) (string, error) {
		processed = item.QueueItemID
		owner.tasks[taskID] = actorPlanningTask(taskID, "mio", domaintask.StatusSucceeded, domaintask.PriorityNormal, now.Add(-time.Hour))
		return "done", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, LeaseDuration: time.Minute, ActorTaskLister: actorPlanningTaskLister(owner)})

	ran, err := requireActorPlannedScheduler(t, scheduler).RunPlanned(context.Background(), targetID, "mio")
	if err != nil {
		t.Fatalf("RunPlanned() error = %v", err)
	}
	if !ran {
		t.Fatal("RunPlanned() = false, want exact queued item executed")
	}
	if processed != targetID || store.item(targetID).Status != "completed" {
		t.Fatalf("processed=%s target=%#v", processed, store.item(targetID))
	}
	if got := store.item(higherPriorityID); got.Status != "queued" || got.AttemptCount != 0 {
		t.Fatalf("nonselected item changed: %#v", got)
	}
	if len(owner.calls) != 1 || owner.calls[0].taskID != taskID {
		t.Fatalf("canonical starts = %#v, want only Task %s", owner.calls, taskID)
	}
}

func TestRunQueueSchedulerRunPlannedReleasesWhenActorOrTaskIsNoLongerEligible(t *testing.T) {
	now := time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)
	taskID := modulecore.NewTaskID()
	queueID := modulecore.NewQueueItemID()
	item := actorPlanningItem(queueID, taskID, 20, now.Add(-time.Minute))
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{item}}}
	owner := &recordingTaskOwner{tasks: map[modulecore.TaskID]domaintask.Task{
		taskID: actorPlanningTask(taskID, "shiro", domaintask.StatusQueued, domaintask.PriorityHigh, now.Add(-time.Hour)),
	}}
	processorCalls := 0
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		processorCalls++
		return "unexpected", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, LeaseDuration: time.Minute})

	ran, err := requireActorPlannedScheduler(t, scheduler).RunPlanned(context.Background(), queueID, "mio")
	if err != nil {
		t.Fatalf("RunPlanned() error = %v", err)
	}
	if ran {
		t.Fatal("RunPlanned() = true for a Task assigned to another actor")
	}
	got := store.item(queueID)
	if got.Status != "queued" || got.AttemptCount != 0 || got.LeaseToken != "" || !got.LeaseUntil.IsZero() {
		t.Fatalf("stale reservation was not released without changing the queue item: %#v", got)
	}
	if len(owner.calls) != 0 || processorCalls != 0 || len(store.traces) != 0 {
		t.Fatalf("ineligible Task caused an effect: starts=%d processor=%d events=%d", len(owner.calls), processorCalls, len(store.traces))
	}
}

func TestRunQueueSchedulerPlanForActorSkipsTasksDeniedByAdmission(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 30, 0, 0, time.UTC)
	actor := "mio"
	blockedID, runnableID := modulecore.NewTaskID(), modulecore.NewTaskID()
	blockedQueue, runnableQueue := modulecore.NewQueueItemID(), modulecore.NewQueueItemID()
	blockedTask := actorPlanningTask(blockedID, actor, domaintask.StatusQueued, domaintask.PriorityCritical, now.Add(-time.Hour))
	runnableTask := actorPlanningTask(runnableID, actor, domaintask.StatusQueued, domaintask.PriorityHigh, now.Add(-time.Minute))
	owner := &actorAdmissionTaskOwner{
		recordingTaskOwner: &recordingTaskOwner{tasks: map[modulecore.TaskID]domaintask.Task{blockedID: blockedTask, runnableID: runnableTask}},
		allowed:            true,
		blocked:            map[modulecore.TaskID]bool{blockedID: true},
	}
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{
		actorPlanningItem(blockedQueue, blockedID, 0, now.Add(-time.Minute)),
		actorPlanningItem(runnableQueue, runnableID, 0, now),
	}}}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		return "", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ActorTaskLister: actorPlanningTaskLister(owner.recordingTaskOwner)})

	item, task, err := requireActorPlannedScheduler(t, scheduler).PlanForActor(context.Background(), actor)
	if err != nil {
		t.Fatalf("PlanForActor() error = %v", err)
	}
	if item == nil || item.QueueItemID != runnableQueue || task.TaskID != runnableID {
		t.Fatalf("PlanForActor() = item %#v task %#v; want runnable queue %s", item, task, runnableQueue)
	}
	if owner.admissionChecks != 2 {
		t.Fatalf("CanStart checks = %d, want both sorted candidates", owner.admissionChecks)
	}
}

func TestRunQueueSchedulerPlanForActorDoesNotTreatCheckpointSummaryAsCursor(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 45, 0, 0, time.UTC)
	actor := "mio"
	resumeTaskID, freshTaskID := modulecore.NewTaskID(), modulecore.NewTaskID()
	resumeQueueID, freshQueueID := modulecore.NewQueueItemID(), modulecore.NewQueueItemID()
	resume := actorPlanningItem(resumeQueueID, resumeTaskID, 1000, now.Add(-time.Hour))
	resume.RunStartReason = domaintask.RunStartReasonProcessRestartResume
	resume.CheckpointID = modulecore.NewCheckpointID()
	resume.CheckpointRevision = 3
	resume.CheckpointSummary = "step committed"
	resume.NextAction = "continue"
	resume.IdempotencyKey = string(resume.CheckpointID)
	owner := &recordingTaskOwner{tasks: map[modulecore.TaskID]domaintask.Task{
		resumeTaskID: actorPlanningTask(resumeTaskID, actor, domaintask.StatusQueued, domaintask.PriorityCritical, now.Add(-time.Hour)),
		freshTaskID:  actorPlanningTask(freshTaskID, actor, domaintask.StatusQueued, domaintask.PriorityNormal, now),
	}}
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{
		resume,
		actorPlanningItem(freshQueueID, freshTaskID, -1, now),
	}}}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		return "", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ActorTaskLister: actorPlanningTaskLister(owner)})

	item, _, err := requireActorPlannedScheduler(t, scheduler).PlanForActor(context.Background(), actor)
	if err != nil {
		t.Fatalf("PlanForActor() error = %v", err)
	}
	if item == nil || item.QueueItemID != freshQueueID {
		t.Fatalf("PlanForActor() = %#v, want fresh queue item %s", item, freshQueueID)
	}
}

func TestRunQueueSchedulerRunPlannedDefersOnAdmissionRaceAtStart(t *testing.T) {
	now := time.Date(2026, 11, 12, 13, 30, 0, 0, time.UTC)
	taskID, queueID := modulecore.NewTaskID(), modulecore.NewQueueItemID()
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{
		actorPlanningItem(queueID, taskID, 0, now.Add(-time.Minute)),
	}}}
	baseOwner := &recordingTaskOwner{
		now: func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{
			taskID: actorPlanningTask(taskID, "mio", domaintask.StatusQueued, domaintask.PriorityHigh, now.Add(-time.Hour)),
		},
	}
	owner := &actorAdmissionTaskOwner{recordingTaskOwner: baseOwner, allowed: true, denyAfter: 3, startErr: errors.New("parallel limit")}
	processorCalls := 0
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		processorCalls++
		return "unexpected", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, LeaseDuration: time.Minute, ActorTaskLister: actorPlanningTaskLister(baseOwner)})
	planned := requireActorPlannedScheduler(t, scheduler)
	item, _, err := planned.PlanForActor(context.Background(), "mio")
	if err != nil || item == nil {
		t.Fatalf("PlanForActor() = %#v, %v; want a candidate while capacity is available", item, err)
	}

	ran, err := planned.RunPlanned(context.Background(), queueID, "mio")
	if err != nil {
		t.Fatalf("RunPlanned() error = %v", err)
	}
	if ran {
		t.Fatal("RunPlanned() = true after admission was lost at StartRun")
	}
	got := store.item(queueID)
	if got.Status != "queued" || got.AttemptCount != 0 || got.LeaseToken != "" {
		t.Fatalf("admission-race queue item = %#v; want released queued item", got)
	}
	if owner.tasks[taskID].Status != domaintask.StatusQueued || processorCalls != 0 || len(store.traces) != 0 {
		t.Fatalf("admission race had effects: task=%s processor=%d events=%d", owner.tasks[taskID].Status, processorCalls, len(store.traces))
	}
	if owner.admissionChecks != 3 {
		t.Fatalf("CanStart checks=%d, want plan, pre-start, and post-refusal recheck", owner.admissionChecks)
	}
}

func TestRunQueueSchedulerPlanForActorKeepsExplicitCheckpointResume(t *testing.T) {
	now := time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC)
	taskID, queueID := modulecore.NewTaskID(), modulecore.NewQueueItemID()
	item := actorPlanningItem(queueID, taskID, 0, now)
	item.RunStartReason = domaintask.RunStartReasonCheckpointResume
	item.CheckpointID = modulecore.NewCheckpointID()
	item.CheckpointRevision = 2
	item.CheckpointSummary = "committed checkpoint"
	item.NextAction = "resume from the explicit Viewer request"
	item.IdempotencyKey = string(item.CheckpointID)
	owner := &recordingTaskOwner{tasks: map[modulecore.TaskID]domaintask.Task{
		taskID: actorPlanningTask(taskID, "mio", domaintask.StatusQueued, domaintask.PriorityNormal, now),
	}}
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{item}}}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		return "", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, ActorTaskLister: actorPlanningTaskLister(owner)})

	selected, _, err := requireActorPlannedScheduler(t, scheduler).PlanForActor(context.Background(), "mio")
	if err != nil || selected == nil || selected.RunStartReason != domaintask.RunStartReasonCheckpointResume {
		t.Fatalf("PlanForActor() = %#v, %v; want validated explicit checkpoint resume", selected, err)
	}
}

func TestRunQueueSchedulerRunPlannedRecoversExpiredUnattachedReservation(t *testing.T) {
	now := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	taskID, queueID := modulecore.NewTaskID(), modulecore.NewQueueItemID()
	item := actorPlanningItem(queueID, taskID, 0, now.Add(-time.Hour))
	item.Status = "reserved"
	item.AttemptCount = 5
	item.ClaimedAt = now.Add(-2 * time.Minute)
	item.LeaseToken = "dead-owner"
	item.LeaseUntil = now.Add(-time.Second)
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{item}}}
	owner := &recordingTaskOwner{
		now: func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{
			taskID: actorPlanningTask(taskID, "mio", domaintask.StatusQueued, domaintask.PriorityHigh, now.Add(-2*time.Hour)),
		},
		responses: []domaintask.Run{{RunID: modulecore.NewRunID(), TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}},
	}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		owner.tasks[taskID] = actorPlanningTask(taskID, "mio", domaintask.StatusSucceeded, domaintask.PriorityHigh, now.Add(-2*time.Hour))
		return "recovered", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, LeaseDuration: time.Minute})

	ran, err := requireActorPlannedScheduler(t, scheduler).RunPlanned(context.Background(), queueID, "mio")
	if err != nil {
		t.Fatalf("RunPlanned() error = %v", err)
	}
	if !ran || len(owner.calls) != 1 || owner.calls[0].reason != domaintask.RunStartReasonFirst {
		t.Fatalf("recovered=%v starts=%#v; want one fresh first run", ran, owner.calls)
	}
	got := store.item(queueID)
	if got.Status != "completed" || got.RunID == "" {
		t.Fatalf("recovered queue item = %#v; want completed with a new Run", got)
	}
}

func actorPlanningTask(id modulecore.TaskID, assignee string, status domaintask.Status, priority domaintask.Priority, createdAt time.Time) domaintask.Task {
	return domaintask.Task{
		TaskID: id, Title: "planned task", Assignee: assignee, Status: status, Priority: priority,
		Route: domaintask.RouteGeneral, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func actorPlanningItem(queueID modulecore.QueueItemID, taskID modulecore.TaskID, priority int, createdAt time.Time) domainsuperagent.RunQueueItem {
	return domainsuperagent.RunQueueItem{
		QueueItemID: queueID, TaskID: taskID, RunStartReason: domaintask.RunStartReasonFirst,
		Goal: "run task", Action: "resume", Status: "queued", Priority: priority, CreatedAt: createdAt,
	}
}

type actorPlanningQueueStore struct {
	*recordingRunQueueStore
}

func (s *actorPlanningQueueStore) ListRunQueueCandidatesForTasks(_ context.Context, taskIDs []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error) {
	allowed := make(map[modulecore.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		allowed[taskID] = struct{}{}
	}
	var result []domainsuperagent.RunQueueItem
	for _, item := range s.items {
		if _, ok := allowed[item.TaskID]; ok && (item.Status == "queued" || item.Status == "reserved") {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *actorPlanningQueueStore) ClaimRunQueueItem(_ context.Context, queueID modulecore.QueueItemID, now, leaseUntil time.Time, token string) (*domainsuperagent.RunQueueItem, error) {
	for index := range s.items {
		item := s.items[index]
		queued := item.Status == "queued" && item.AttemptCount == 0
		expiredReservation := item.Status == "reserved" && item.RunID == "" && item.AttemptCount > 0 && !item.LeaseUntil.After(now)
		if item.QueueItemID != queueID || (!queued && !expiredReservation) || (!item.NotBefore.IsZero() && item.NotBefore.After(now)) {
			continue
		}
		item.Status, item.ClaimedAt, item.LeaseToken, item.LeaseUntil = "reserved", now, token, leaseUntil
		item.AttemptCount = 1
		item.CompletedAt = time.Time{}
		s.items[index] = item
		return &item, nil
	}
	return nil, nil
}

func (s *actorPlanningQueueStore) ReleaseRunQueueItem(_ context.Context, queueID modulecore.QueueItemID, token string) (bool, error) {
	for index := range s.items {
		item := s.items[index]
		if item.QueueItemID != queueID || item.Status != "reserved" || item.LeaseToken != token || item.RunID != "" {
			continue
		}
		item.Status = "queued"
		item.ClaimedAt, item.LeaseToken, item.LeaseUntil = time.Time{}, "", time.Time{}
		if item.AttemptCount > 0 {
			item.AttemptCount--
		}
		s.items[index] = item
		return true, nil
	}
	return false, nil
}

func (s *actorPlanningQueueStore) AttachRunQueueRun(_ context.Context, queueID, token string, runID modulecore.RunID) (bool, error) {
	for index := range s.items {
		item := s.items[index]
		if item.QueueItemID != modulecore.QueueItemID(queueID) || item.Status != "reserved" || item.LeaseToken != token || item.RunID != "" {
			continue
		}
		item.Status, item.RunID = "claimed", runID
		s.items[index] = item
		return true, nil
	}
	return false, nil
}

func (s *actorPlanningQueueStore) RenewRunQueueLease(_ context.Context, queueID, token string, leaseUntil time.Time) (bool, error) {
	for index := range s.items {
		item := s.items[index]
		if item.QueueItemID != modulecore.QueueItemID(queueID) || (item.Status != "reserved" && item.Status != "claimed") || item.LeaseToken != token {
			continue
		}
		item.LeaseUntil = leaseUntil
		s.items[index] = item
		return true, nil
	}
	return false, nil
}

func (s *actorPlanningQueueStore) CompleteRunQueueItem(_ context.Context, queueID, token, status, reason string, completedAt time.Time) (bool, error) {
	for index := range s.items {
		item := s.items[index]
		if item.QueueItemID != modulecore.QueueItemID(queueID) || item.LeaseToken != token || (item.Status != "claimed" && !(status == "blocked" && item.Status == "reserved")) {
			continue
		}
		item.Status, item.Reason, item.CompletedAt = status, reason, completedAt
		item.LeaseToken, item.LeaseUntil = "", time.Time{}
		s.items[index] = item
		return true, nil
	}
	return false, nil
}

func TestRunQueueSchedulerRunPlannedDefersWhenTaskAdmissionIsFull(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	taskID, queueID := modulecore.NewTaskID(), modulecore.NewQueueItemID()
	item := actorPlanningItem(queueID, taskID, 20, now.Add(-time.Minute))
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{item}}}
	baseOwner := &recordingTaskOwner{
		now: func() time.Time { return now },
		tasks: map[modulecore.TaskID]domaintask.Task{
			taskID: actorPlanningTask(taskID, "mio", domaintask.StatusQueued, domaintask.PriorityHigh, now.Add(-time.Hour)),
		},
	}
	owner := &actorAdmissionTaskOwner{recordingTaskOwner: baseOwner, allowed: true}
	processorCalls := 0
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(context.Context, domainsuperagent.RunQueueItem, modulecore.TraceID) (string, error) {
		processorCalls++
		return "unexpected", nil
	}), owner, RunQueueSchedulerOptions{Now: func() time.Time { return now }, LeaseDuration: time.Minute, ActorTaskLister: actorPlanningTaskLister(baseOwner)})
	planned := requireActorPlannedScheduler(t, scheduler)
	plan, _, err := planned.PlanForActor(context.Background(), "mio")
	if err != nil || plan == nil || plan.QueueItemID != queueID {
		t.Fatalf("PlanForActor() = %#v, %v; want queue item %s", plan, err, queueID)
	}

	owner.allowed = false
	ran, err := planned.RunPlanned(context.Background(), queueID, "mio")
	if err != nil {
		t.Fatalf("RunPlanned() error = %v", err)
	}
	if ran {
		t.Fatal("RunPlanned() = true while TaskManager admission is full")
	}
	got := store.item(queueID)
	if got.Status != "queued" || got.AttemptCount != 0 || got.LeaseToken != "" {
		t.Fatalf("capacity deferral did not restore the queued item: %#v", got)
	}
	if owner.admissionChecks != 2 || len(owner.calls) != 0 || processorCalls != 0 || len(store.traces) != 0 {
		t.Fatalf("capacity deferral caused effects: admission=%d starts=%d processor=%d events=%d", owner.admissionChecks, len(owner.calls), processorCalls, len(store.traces))
	}
}

type actorAdmissionTaskOwner struct {
	*recordingTaskOwner
	allowed         bool
	blocked         map[modulecore.TaskID]bool
	admissionChecks int
	denyAfter       int
	startErr        error
}

func (o *actorAdmissionTaskOwner) CanStart(_ context.Context, task domaintask.Task) (bool, string, error) {
	o.admissionChecks++
	allowed := o.allowed && !o.blocked[task.TaskID]
	if o.denyAfter > 0 && o.admissionChecks >= o.denyAfter {
		allowed = false
	}
	return allowed, "capacity is full", nil
}

func (o *actorAdmissionTaskOwner) StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error) {
	if o.startErr != nil {
		return domaintask.Run{}, o.startErr
	}
	return o.recordingTaskOwner.StartRunWithReason(ctx, taskID, reason)
}

func actorPlanningTaskLister(owner *recordingTaskOwner) RunQueueActorTaskLister {
	return func(ctx context.Context, actor string, limit int) ([]domaintask.Task, error) {
		return owner.List(ctx, domaintask.Filter{Assignee: actor, Status: domaintask.StatusQueued, Limit: limit})
	}
}
