package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// memoryPromotionClaimProbe answers, read-only, whether a run could find work.
type memoryPromotionClaimProbe interface {
	HasClaimable(context.Context) (bool, error)
}

// memoryPromotionClaimingRunner is the inner runner the execution wrapper needs:
// the run itself, and the read-only question asked before an execution subject is
// opened for it.
type memoryPromotionClaimingRunner interface {
	memoryPromotionRunner
	memoryPromotionClaimProbe
}

type actionMemoryPromotionRunner struct {
	inner   memoryPromotionClaimingRunner
	actions *actionmanager.Manager
	tasks   *taskmanager.Manager
	actorID string
	stats   *memoryPromotionRunStats
}

func newActionMemoryPromotionRunner(inner memoryPromotionClaimingRunner, actions *actionmanager.Manager, tasks *taskmanager.Manager, actorID string) (memoryPromotionRunner, error) {
	if inner == nil {
		return nil, errors.New("Memory Promotion runner is required")
	}
	if actions == nil || tasks == nil || actorID == "" {
		return nil, errors.New("Memory Promotion execution owners are required")
	}
	return &actionMemoryPromotionRunner{inner: inner, actions: actions, tasks: tasks, actorID: actorID, stats: newMemoryPromotionRunStats(time.Now, log.Printf)}, nil
}

// RunOne opens an execution subject (Task, Run, Action) only when the inner
// runner could find work. The probe is read-only and no narrower than the claim,
// so skipping on "nothing" loses no work; a probe that says "work" which the
// claim then does not find only costs the subject the old code always opened, and
// that subject is closed below like any other.
func (r *actionMemoryPromotionRunner) RunOne(ctx context.Context) (result memorypromotionapp.RunResult, err error) {
	claimable, probeErr := r.inner.HasClaimable(ctx)
	if probeErr != nil {
		r.stats.probeFailed()
		return memorypromotionapp.RunResult{}, fmt.Errorf("check Memory Promotion work: %w", probeErr)
	}
	if !claimable {
		r.stats.idle()
		return memorypromotionapp.RunResult{}, nil
	}
	defer func() { r.stats.worked(result.Processed, err) }()
	task, run, err := r.tasks.CreateAndStartRun(ctx, domaintask.Task{
		Title:    "Memory Promotion",
		Route:    domaintask.RouteResearch,
		OwnerID:  r.actorID,
		Assignee: r.actorID,
		ReadOnly: false,
	}, domaintask.SharedRoleContext{CurrentPlan: "promote canonical memory candidates"})
	if err != nil {
		return memorypromotionapp.RunResult{}, err
	}
	// From here on the Task is running with its first Run and holds the single
	// long-research slot, so every failure that stops the run before the inner
	// runner executes must close it. A Run left open defers every later Memory
	// Promotion until the next restart.
	ownedCtx, err := domainexecution.WithIdentity(ctx, task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		return memorypromotionapp.RunResult{}, r.tasks.AbortStartedRun(ctx, task.TaskID, run.RunID, r.actorID, err)
	}
	action, attempt, err := r.actions.CreateAction(ownedCtx, actionmanager.CreateInput{
		TaskID: task.TaskID,
		RunID:  run.RunID,
		Kind:   domainaction.KindMemoryPromotion,
		Name:   "memory_promotion",
	})
	if err != nil {
		return memorypromotionapp.RunResult{}, r.tasks.AbortStartedRun(ctx, task.TaskID, run.RunID, r.actorID, err)
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		// The Action and Attempt exist, so they are closed together with the Run.
		return memorypromotionapp.RunResult{}, r.complete(ctx, task.TaskID, run.RunID, action, attempt, err)
	}
	result, operationErr := r.inner.RunOne(ownedCtx)
	return result, r.complete(ctx, task.TaskID, run.RunID, action, attempt, operationErr)
}

// complete closes the Attempt and Action, then the Run and Task. If the Attempt
// cannot be closed the Run stays open with it, and the error says so.
func (r *actionMemoryPromotionRunner) complete(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, action domainaction.Action, attempt domainaction.Attempt, operationErr error) error {
	const completed = "Memory Promotion completed"
	if closed, err := r.actions.CloseAttempt(ctx, action.ActionID, attempt.AttemptID, actionmanager.AttemptOutcomeOf(operationErr, completed), operationErr); !closed {
		return err
	}
	return r.tasks.CloseStartedRun(ctx, taskID, runID, r.actorID, taskmanager.RunOutcomeOf(operationErr, completed), operationErr)
}

// memoryPromotionRunLeftOpen reports whether err carries a failed terminal write:
// the Run is still running and holds the long-research slot. The Attempt's own
// marker counts, because the Run is not closed after a failed Attempt.
func memoryPromotionRunLeftOpen(err error) bool {
	return errors.Is(err, taskmanager.ErrStartedRunNotClosed) || errors.Is(err, actionmanager.ErrAttemptNotClosed)
}

// memoryPromotionRunSummaryInterval is the gap between two run summary lines.
const memoryPromotionRunSummaryInterval = 10 * time.Minute

// memoryPromotionRunStats counts what RunOne decided and writes one summary line
// per interval, never one line per run. It replaces the record the idle runs used
// to leave in the Action store: idle is the number of runs that opened no
// execution subject. It starts no goroutine and no timer; the line is written from
// the RunOne call that finds the interval over.
type memoryPromotionRunStats struct {
	mu          sync.Mutex
	now         func() time.Time
	logf        func(format string, args ...any)
	interval    time.Duration
	windowStart time.Time

	idleRuns, startedRuns, refusedRuns, processedRuns, probeErrors int
}

func newMemoryPromotionRunStats(now func() time.Time, logf func(format string, args ...any)) *memoryPromotionRunStats {
	return &memoryPromotionRunStats{now: now, logf: logf, interval: memoryPromotionRunSummaryInterval, windowStart: now()}
}

// idle records a run that found nothing to claim and opened no subject.
func (s *memoryPromotionRunStats) idle() { s.record(func() { s.idleRuns++ }) }

// worked records a run that found work and went on to open a subject. A refusal
// for lack of execution capacity opened none and is counted as refused instead;
// processed is whether the run claimed a batch.
func (s *memoryPromotionRunStats) worked(processed bool, err error) {
	s.record(func() {
		if errors.Is(err, taskmanager.ErrParallelLimit) {
			s.refusedRuns++
			return
		}
		s.startedRuns++
		if processed {
			s.processedRuns++
		}
	})
}

// probeFailed records a run that could not tell whether work was waiting.
func (s *memoryPromotionRunStats) probeFailed() { s.record(func() { s.probeErrors++ }) }

func (s *memoryPromotionRunStats) record(count func()) {
	s.mu.Lock()
	count()
	now := s.now()
	window := now.Sub(s.windowStart)
	if window < s.interval {
		s.mu.Unlock()
		return
	}
	line := fmt.Sprintf("Memory ProfilePromotion run summary window=%s idle=%d started=%d refused=%d processed=%d probe_errors=%d",
		window.Round(time.Second), s.idleRuns, s.startedRuns, s.refusedRuns, s.processedRuns, s.probeErrors)
	s.windowStart = now
	s.idleRuns, s.startedRuns, s.refusedRuns, s.processedRuns, s.probeErrors = 0, 0, 0, 0, 0
	s.mu.Unlock()
	s.logf("%s", line)
}
