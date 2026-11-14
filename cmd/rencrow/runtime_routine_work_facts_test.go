package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type routineFactJournalStub struct {
	public   []eventstore.RoutinePublicResponseFact
	board    []eventstore.RoutineBoardPublicationFact
	boardErr error
}

func (s routineFactJournalStub) ListRoutinePublicResponseFacts(context.Context, string, string, time.Time, time.Time) ([]eventstore.RoutinePublicResponseFact, error) {
	return s.public, nil
}

func (s routineFactJournalStub) ListRoutineBoardPublicationFacts(context.Context, string, string, time.Time, time.Time) ([]eventstore.RoutineBoardPublicationFact, error) {
	return s.board, s.boardErr
}

func routineWorkFactsManager(t *testing.T) (*taskmanager.Manager, *taskpersistence.JSONLStore) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	return taskmanager.New(store, taskmanager.DefaultParallelLimits()), store
}

func routineFactTask(ownerID, assignee, moduleID string, created, updated time.Time) domaintask.Task {
	return domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "PRIVATE_TITLE_SENTINEL", ModuleID: moduleID,
		Route: domaintask.RouteGeneral, OwnerID: ownerID, Assignee: assignee,
		Status: domaintask.StatusSucceeded, Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: created, UpdatedAt: updated,
		Summary: "PRIVATE_SUMMARY_SENTINEL", WaitingReason: "PRIVATE_WAITING_SENTINEL",
		Artifacts: []string{"PRIVATE_SECRET_REFERENCE"},
	}
}

func routineFactRun(task domaintask.Task, assignee string, generation uint64, started, completed time.Time) domaintask.Run {
	return domaintask.Run{
		WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst,
		Assignee: assignee, Status: domaintask.RunStatusSucceeded, StartedAt: started,
		CompletedAt: &completed, Summary: "PRIVATE_RUN_OUTPUT_SENTINEL",
	}
}

func TestCollectRoutineWorkFactsUsesExactTaskOwnerAndAssigneeAndStatusOnly(t *testing.T) {
	ctx := context.Background()
	manager, store := routineWorkFactsManager(t)
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	utc := func(year int, month time.Month, day, hour int) time.Time {
		return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	}
	matching := routineFactTask("ren", "mio", "code", utc(2026, time.November, 12, 14), utc(2026, time.November, 13, 2))
	matchingStarted := utc(2026, time.November, 12, 15)
	matching.StartedAt = &matchingStarted
	if err := store.SaveTask(ctx, matching); err != nil {
		t.Fatal(err)
	}
	matchingRun := routineFactRun(matching, "mio", generation, utc(2026, time.November, 12, 16), utc(2026, time.November, 12, 18))
	if err := store.SaveRun(ctx, matchingRun); err != nil {
		t.Fatal(err)
	}

	fallback := routineFactTask("ren", "mio", "code", utc(2026, time.November, 13, 2), utc(2026, time.November, 13, 5))
	fallbackStarted := utc(2026, time.November, 13, 3)
	fallbackFinished := utc(2026, time.November, 13, 4)
	fallback.StartedAt = &fallbackStarted
	fallback.FinishedAt = &fallbackFinished
	if err := store.SaveTask(ctx, fallback); err != nil {
		t.Fatal(err)
	}

	queuedOnly := routineFactTask("ren", "mio", "code", utc(2026, time.November, 13, 2), utc(2026, time.November, 13, 5))
	if err := store.SaveTask(ctx, queuedOnly); err != nil {
		t.Fatal(err)
	}

	wrongOwner := routineFactTask("ren-else", "mio", "code", utc(2026, time.November, 13, 0), utc(2026, time.November, 13, 3))
	wrongAssignee := routineFactTask("ren", "Mio", "code", utc(2026, time.November, 13, 0), utc(2026, time.November, 13, 3))
	routineModule := routineFactTask("ren", "mio", "agent_routine", utc(2026, time.November, 13, 0), utc(2026, time.November, 13, 3))
	outOfDay := routineFactTask("ren", "mio", "code", utc(2026, time.November, 10, 0), utc(2026, time.November, 11, 0))
	for _, task := range []domaintask.Task{wrongOwner, wrongAssignee, routineModule, outOfDay} {
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []domaintask.Task{wrongOwner, wrongAssignee, routineModule} {
		if err := store.SaveRun(ctx, routineFactRun(task, "mio", generation, utc(2026, time.November, 13, 1), utc(2026, time.November, 13, 2))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveRun(ctx, routineFactRun(matching, "Mio", generation, utc(2026, time.November, 13, 1), utc(2026, time.November, 13, 2))); err != nil {
		t.Fatal(err)
	}

	facts, err := collectRoutineWorkFacts(ctx, manager, nil, "mio", "ren", "2026-11-13")
	if err != nil {
		t.Fatalf("collectRoutineWorkFacts: %v", err)
	}
	if len(facts) != 4 {
		t.Fatalf("facts = %#v, want exact Run transitions and started Task fallback only", facts)
	}
	wantRunStart := utc(2026, time.November, 12, 16)
	wantRunFinish := utc(2026, time.November, 12, 18)
	seen := map[string]int{}
	for _, fact := range facts {
		if fact.ActorID != "mio" || fact.Date != "2026-11-13" || fact.Kind != "work" || !fact.Confirmed || fact.Key == "" {
			t.Errorf("fact identity = %+v", fact)
		}
		if strings.Contains(fact.Summary, "PRIVATE_") || strings.Contains(fact.Summary, "ren") {
			t.Errorf("fact summary exposed non-status data: %q", fact.Summary)
		}
		switch fact.Source {
		case "canonical_task":
			if fact.SourceID == string(fallback.TaskID) {
				if fact.At.Equal(fallbackStarted) && fact.Summary == "作業を開始した" {
					seen["task_start"]++
				} else if fact.At.Equal(fallbackFinished) && fact.Summary == "作業を完了した" {
					seen["task_finish"]++
				}
			}
		case "canonical_run":
			if fact.SourceID == string(matchingRun.RunID) {
				if fact.At.Equal(wantRunStart) && fact.Summary == "作業を開始した" {
					seen["run_start"]++
				} else if fact.At.Equal(wantRunFinish) && fact.Summary == "作業を完了した" {
					seen["run_finish"]++
				}
			}
		default:
			t.Errorf("unexpected fact source %q", fact.Source)
		}
	}
	for _, key := range []string{"task_start", "task_finish", "run_start", "run_finish"} {
		if seen[key] != 1 {
			t.Errorf("%s count = %d, want 1; facts: %#v", key, seen[key], facts)
		}
	}
	for _, fact := range facts {
		if fact.SourceID == string(queuedOnly.TaskID) {
			t.Errorf("queued Task without StartedAt was reported as work: %+v", fact)
		}
	}
}

func TestCollectRoutineWorkFactsRejectsInvalidScope(t *testing.T) {
	manager, _ := routineWorkFactsManager(t)
	if _, err := collectRoutineWorkFacts(context.Background(), manager, nil, "Mio", "ren", "2026-11-13"); err == nil {
		t.Fatal("accepted noncanonical actor ID")
	}
	if _, err := collectRoutineWorkFacts(context.Background(), manager, nil, "mio", " ren ", "2026-11-13"); err == nil {
		t.Fatal("accepted owner ID with surrounding whitespace")
	}
	if _, err := collectRoutineWorkFacts(context.Background(), manager, nil, "mio", "ren", "2026-02-30"); err == nil {
		t.Fatal("accepted invalid calendar date")
	}
}

func TestCollectRoutineWorkFactsUsesScopedBoardPublicationMetadata(t *testing.T) {
	manager, _ := routineWorkFactsManager(t)
	boardAt := time.Date(2026, time.November, 13, 4, 30, 0, 0, time.UTC)
	journal := routineFactJournalStub{
		public: []eventstore.RoutinePublicResponseFact{{
			EventID: "event:duplicate", ActorID: "mio", OccurredAt: boardAt,
		}},
		board: []eventstore.RoutineBoardPublicationFact{
			{EventID: "event:duplicate", ActorID: "mio", BoardMessageID: "board-message:duplicate", StoredAt: boardAt},
			{EventID: "event:board-1", ActorID: "mio", BoardMessageID: "board-message-1", StoredAt: boardAt.Add(time.Minute)},
		},
	}

	facts, err := collectRoutineWorkFacts(context.Background(), manager, journal, "mio", "ren", "2026-11-13")
	if err != nil {
		t.Fatalf("collectRoutineWorkFacts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %#v, want one v2 response and one nonduplicate BOARD publication", facts)
	}
	var boardFact *agentroutine.Activity
	for i := range facts {
		if facts[i].Source == "canonical_board_publication" {
			boardFact = &facts[i]
		}
	}
	if boardFact == nil {
		t.Fatalf("missing BOARD publication fact: %#v", facts)
	}
	if boardFact.ActorID != "mio" || boardFact.Date != "2026-11-13" || boardFact.Kind != agentroutine.ActivityConversation ||
		boardFact.Key != "conversation:board_publication:board-message-1" || boardFact.SourceID != "board-message-1" ||
		!boardFact.At.Equal(boardAt.Add(time.Minute)) || !boardFact.Confirmed || boardFact.Summary != "BOARDで会話に応答した" {
		t.Errorf("BOARD fact = %+v, want exact scoped metadata and safe summary", *boardFact)
	}
}

func TestCollectRoutineWorkFactsRejectsOutOfScopeBoardMetadata(t *testing.T) {
	manager, _ := routineWorkFactsManager(t)
	journal := routineFactJournalStub{board: []eventstore.RoutineBoardPublicationFact{{
		EventID: "event:bad", ActorID: "shiro", BoardMessageID: "board-message-1",
		StoredAt: time.Date(2026, time.November, 13, 4, 30, 0, 0, time.UTC),
	}}}
	if _, err := collectRoutineWorkFacts(context.Background(), manager, journal, "mio", "ren", "2026-11-13"); err == nil {
		t.Fatal("accepted BOARD metadata for a different actor")
	}
}
