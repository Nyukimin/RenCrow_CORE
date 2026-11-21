package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// emptyPromotionExtractor extracts nothing: the batch is claimed, run and completed
// without candidates, which is all these tests need from the inner run.
type emptyPromotionExtractor struct{}

func (emptyPromotionExtractor) Extract(context.Context, *domconv.Thread, domconv.UserProfile) (*domconv.ProfileExtractionResult, error) {
	return &domconv.ProfileExtractionResult{}, nil
}

// claimableIntegration wires the real L1 job table, the real Service (and so the
// real probe and claim) and the wrapper to real Task and Action stores.
type claimableIntegration struct {
	t       *testing.T
	l1      *l1sqlite.L1SQLiteStore
	tasks   *taskmanager.Manager
	actions *actionmanager.Manager
	clock   time.Time
	runner  memoryPromotionRunner
}

func newClaimableIntegration(t *testing.T, maxAttempts int, start time.Time) *claimableIntegration {
	t.Helper()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l1.Close() })
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	f := &claimableIntegration{
		t:       t,
		l1:      l1,
		tasks:   taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()),
		actions: actionmanager.New(actionStore),
		clock:   start,
	}
	service := memorypromotionapp.NewService(l1, emptyPromotionExtractor{}, memorypromotionapp.Options{
		UserID: "ren", MaxAttempts: maxAttempts, LeaseDuration: time.Minute,
		Now: func() time.Time { return f.clock },
	})
	f.runner, err = newActionMemoryPromotionRunner(service, f.actions, f.tasks, "midori")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// saveJob stores one user message, which creates one pending promotion job.
func (f *claimableIntegration) saveJob() {
	f.t.Helper()
	threadID := modulecore.NewThreadID()
	msg := domconv.NewMessage(domconv.SpeakerUser, "claimable integration message", nil)
	if err := f.l1.SaveMessage(context.Background(), string(modulecore.NewSessionID()), threadID, 1,
		modulecore.ThreadKindUserConversation, "conv:"+string(threadID), msg, l1sqlite.MemoryStateObserved); err != nil {
		f.t.Fatal(err)
	}
}

// claimAt claims the job as a worker would at the given time (lease: one minute).
func (f *claimableIntegration) claimAt(at time.Time) domainmemory.ProfilePromotionBatch {
	f.t.Helper()
	batch, err := f.l1.ClaimProfilePromotionBatch(context.Background(), 24, 5, time.Minute, at)
	if err != nil || batch == nil {
		f.t.Fatalf("seed claim batch=%v err=%v", batch, err)
	}
	return *batch
}

func (f *claimableIntegration) failAt(batch domainmemory.ProfilePromotionBatch, at time.Time) {
	f.t.Helper()
	if err := f.l1.FailProfilePromotionBatch(context.Background(), batch, 5, at, "seed failure"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *claimableIntegration) assertNoExecutionSubject() {
	f.t.Helper()
	taskList, err := f.tasks.List(context.Background(), domaintask.Filter{})
	if err != nil {
		f.t.Fatal(err)
	}
	actionList, err := f.actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil {
		f.t.Fatal(err)
	}
	runs, err := f.tasks.ListRuns(context.Background(), domaintask.RunFilter{})
	if err != nil {
		f.t.Fatal(err)
	}
	if len(taskList) != 0 || len(actionList) != 0 || len(runs) != 0 {
		f.t.Fatalf("claimless execution created Tasks=%+v Actions=%+v Runs=%+v", taskList, actionList, runs)
	}
}

func (f *claimableIntegration) assertOneExecutionAndEvaluatingSuccessor() domaintask.Task {
	f.t.Helper()
	ctx := context.Background()
	tasks, err := f.tasks.List(ctx, domaintask.Filter{Limit: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	actions, err := f.actions.ListActions(ctx, domainaction.Filter{})
	if err != nil {
		f.t.Fatal(err)
	}
	runs, err := f.tasks.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		f.t.Fatal(err)
	}
	if len(tasks) != 2 || len(actions) != 1 || len(runs) != 1 {
		f.t.Fatalf("Tasks=%d Actions=%d Runs=%d, want one execution subject plus one passive successor", len(tasks), len(actions), len(runs))
	}
	var execution, successor domaintask.Task
	for _, candidate := range tasks {
		if candidate.TaskID == actions[0].TaskID {
			execution = candidate
		} else {
			successor = candidate
		}
	}
	if execution.TaskID == "" || execution.Route != domaintask.RouteResearch || execution.Activity == nil ||
		execution.Activity.Kind != domaintask.ActivityKindWork || execution.Activity.Passive || !domaintask.IsTerminal(execution.Status) {
		f.t.Fatalf("execution Task=%+v, want one terminal active-work Task linked by the Action", execution)
	}
	if runs[0].TaskID != execution.TaskID || runs[0].RunID != actions[0].RunID || !domaintask.IsRunTerminal(runs[0].Status) {
		f.t.Fatalf("Run=%+v Action=%+v, want the one terminal Run bound to the execution Task", runs[0], actions[0])
	}
	if successor.TaskID == "" || successor.Status != domaintask.StatusRunning || successor.Activity == nil ||
		successor.Activity.Kind != domaintask.ActivityKindEvaluating || !successor.Activity.Passive {
		f.t.Fatalf("successor Task=%+v, want one passive evaluating Task", successor)
	}
	if successorRuns, err := f.tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: successor.TaskID}); err != nil || len(successorRuns) != 0 {
		f.t.Fatalf("passive successor Runs=%+v err=%v, want no execution Run", successorRuns, err)
	}
	current, err := f.tasks.SnapshotCurrentActivities(ctx, []string{"midori"})
	if err != nil || current["midori"].Task.TaskID != successor.TaskID {
		f.t.Fatalf("current midori activity=%+v err=%v, want the passive successor", current, err)
	}
	return execution
}

func (f *claimableIntegration) jobState() string {
	f.t.Helper()
	jobs, err := f.l1.ListProfilePromotionJobs(context.Background(), 10)
	if err != nil || len(jobs) != 1 {
		f.t.Fatalf("jobs=%d err=%v, want exactly one", len(jobs), err)
	}
	return jobs[0].State
}

// Every state the claim can act on still gets its Task, Run and Action and is
// processed as before; every state it cannot act on creates nothing at all. The
// real probe, claim and stores are used, so the predicate and the wrapper are
// checked together.
func TestActionMemoryPromotionRunnerAgainstTheRealClaim(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		maxAttempts   int
		seed          func(f *claimableIntegration)
		now           time.Time
		wantCreated   bool
		wantProcessed bool
		wantJobState  string
	}{
		{name: "no jobs", maxAttempts: 5, seed: func(*claimableIntegration) {}, now: t0, wantJobState: ""},
		{
			name: "pending job", maxAttempts: 5, now: t0, wantCreated: true, wantProcessed: true, wantJobState: domainmemory.ProfilePromotionCompleted,
			seed: func(f *claimableIntegration) { f.saveJob() },
		},
		{
			name: "retry wait not yet due", maxAttempts: 5, now: t0, wantJobState: domainmemory.ProfilePromotionRetryWait,
			seed: func(f *claimableIntegration) { f.saveJob(); f.failAt(f.claimAt(t0), t0) },
		},
		{
			name: "retry wait that became due", maxAttempts: 5, now: t0.Add(24 * time.Hour), wantCreated: true, wantProcessed: true, wantJobState: domainmemory.ProfilePromotionCompleted,
			seed: func(f *claimableIntegration) { f.saveJob(); f.failAt(f.claimAt(t0), t0) },
		},
		{
			name: "running with a live lease", maxAttempts: 5, now: t0.Add(30 * time.Second), wantJobState: domainmemory.ProfilePromotionRunning,
			seed: func(f *claimableIntegration) { f.saveJob(); f.claimAt(t0) },
		},
		{
			name: "running with an expired lease", maxAttempts: 5, now: t0.Add(2 * time.Minute), wantCreated: true, wantProcessed: true, wantJobState: domainmemory.ProfilePromotionCompleted,
			seed: func(f *claimableIntegration) { f.saveJob(); f.claimAt(t0) },
		},
		{
			// The claim closes it as failed and returns no batch: the probe must
			// still let the run through so that update is not skipped.
			name: "retry wait at the attempt limit", maxAttempts: 1, now: t0, wantCreated: true, wantProcessed: false, wantJobState: domainmemory.ProfilePromotionFailed,
			seed: func(f *claimableIntegration) { f.saveJob(); f.failAt(f.claimAt(t0), t0) },
		},
		{
			name: "failed job only", maxAttempts: 1, now: t0.Add(24 * time.Hour), wantJobState: domainmemory.ProfilePromotionFailed,
			seed: func(f *claimableIntegration) {
				f.saveJob()
				batch := f.claimAt(t0)
				if err := f.l1.FailProfilePromotionBatch(context.Background(), batch, 1, t0, "seed failure"); err != nil {
					f.t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaimableIntegration(t, tc.maxAttempts, t0)
			tc.seed(f)
			f.clock = tc.now

			result, err := f.runner.RunOne(context.Background())

			if err != nil {
				t.Fatalf("RunOne err=%v", err)
			}
			if result.Processed != tc.wantProcessed {
				t.Fatalf("Processed=%v, want %v", result.Processed, tc.wantProcessed)
			}
			if tc.wantCreated {
				f.assertOneExecutionAndEvaluatingSuccessor()
			} else {
				f.assertNoExecutionSubject()
			}
			if tc.wantJobState != "" {
				if got := f.jobState(); got != tc.wantJobState {
					t.Fatalf("job state=%q, want %q", got, tc.wantJobState)
				}
			}
		})
	}
}

// Once the only job is done, later runs create nothing: the idle loop that used
// to leave a Task, Run and Action per tick leaves none.
func TestActionMemoryPromotionRunnerLeavesNothingOnEveryIdleTickAfterTheWorkIsDone(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	f := newClaimableIntegration(t, 5, t0)
	f.saveJob()

	result, err := f.runner.RunOne(context.Background())
	if err != nil || !result.Processed {
		t.Fatalf("first RunOne result=%#v err=%v, want the job processed", result, err)
	}
	firstExecution := f.assertOneExecutionAndEvaluatingSuccessor()
	for tick := 1; tick <= 20; tick++ {
		f.clock = t0.Add(time.Duration(tick) * 11 * time.Second)
		result, err := f.runner.RunOne(context.Background())
		if err != nil || result.Processed {
			f.t.Fatalf("tick %d result=%#v err=%v, want an idle tick", tick, result, err)
		}
	}
	lastExecution := f.assertOneExecutionAndEvaluatingSuccessor()
	if lastExecution.TaskID != firstExecution.TaskID {
		t.Fatalf("idle ticks changed the execution Task from %s to %s", firstExecution.TaskID, lastExecution.TaskID)
	}
}
