package superagent

import (
	"context"
	"reflect"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunQueueSchedulerMatchingSkipsDeferredCandidateWithoutBlockingOtherWork(t *testing.T) {
	type matchingPlanner interface {
		PlanForActorMatching(context.Context, string, func(domaintask.Task) bool) (*domainsuperagent.RunQueueItem, domaintask.Task, error)
	}
	now := time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)
	deferredID, ordinaryID := modulecore.NewTaskID(), modulecore.NewTaskID()
	owner := &recordingTaskOwner{tasks: map[modulecore.TaskID]domaintask.Task{
		deferredID: actorPlanningTask(deferredID, "mio", domaintask.StatusQueued, domaintask.PriorityLow, now.Add(-time.Hour)),
		ordinaryID: actorPlanningTask(ordinaryID, "mio", domaintask.StatusQueued, domaintask.PriorityLow, now.Add(-time.Minute)),
	}}
	store := &actorPlanningQueueStore{recordingRunQueueStore: &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{
		actorPlanningItem(modulecore.NewQueueItemID(), deferredID, 1, now.Add(-time.Hour)),
		actorPlanningItem(modulecore.NewQueueItemID(), ordinaryID, 1, now.Add(-time.Minute)),
	}}}
	scheduler := NewRunQueueScheduler(store, nil, owner, RunQueueSchedulerOptions{
		Now: func() time.Time { return now }, ActorTaskLister: actorPlanningTaskLister(owner),
	})
	planner, ok := any(scheduler).(matchingPlanner)
	if !ok {
		t.Fatal("actor planning cannot defer a candidate while continuing to select ordinary work")
	}
	before := append([]domainsuperagent.RunQueueItem(nil), store.items...)
	item, task, err := planner.PlanForActorMatching(context.Background(), "mio", func(task domaintask.Task) bool {
		return task.TaskID != deferredID
	})
	if err != nil || item == nil || task.TaskID != ordinaryID || item.TaskID != ordinaryID {
		t.Fatalf("matching planning item=%+v task=%+v err=%v; want ordinary work after deferred first candidate", item, task, err)
	}
	if !reflect.DeepEqual(store.items, before) || len(owner.calls) != 0 {
		t.Fatal("candidate filtering mutated durable queue or issued a Run")
	}
	item, _, err = planner.PlanForActorMatching(context.Background(), "mio", func(domaintask.Task) bool { return false })
	if err != nil || item != nil {
		t.Fatalf("all deferred planning item=%+v err=%v", item, err)
	}
	item, _, err = scheduler.PlanForActor(context.Background(), "mio")
	if err != nil || item == nil || item.TaskID != deferredID {
		t.Fatalf("unfiltered planning changed: item=%+v err=%v", item, err)
	}
}
