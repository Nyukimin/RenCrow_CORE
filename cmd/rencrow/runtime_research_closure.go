package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

// researchStartedRunGuard owns only the exact Run returned by Task admission.
// Call Close for the normal outcome and defer CloseFallback immediately after
// registering the guard.
type researchStartedRunGuard struct {
	tasks     *taskmanager.Manager
	taskID    core.TaskID
	runID     core.RunID
	actor     string
	parent    context.Context
	closed    bool
	attempted bool
}

func newResearchStartedRunGuard(tasks *taskmanager.Manager, taskID core.TaskID, runID core.RunID, actor string, parent context.Context) *researchStartedRunGuard {
	return &researchStartedRunGuard{tasks: tasks, taskID: taskID, runID: runID, actor: actor, parent: parent}
}

func (g *researchStartedRunGuard) Close(status domaintask.Status, summary, waitingReason string, cause error) error {
	if g == nil || g.tasks == nil {
		return errors.Join(cause, fmt.Errorf("research started-run owner unavailable"))
	}
	if g.closed {
		return cause
	}
	g.attempted = true
	parent := g.parent
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), taskmanager.StartedRunTerminalWriteTimeout)
	defer cancel()
	var err error
	if status == domaintask.StatusWaiting {
		if waitingReason == "" {
			waitingReason = "research requires additional input"
		}
		_, err = g.tasks.CompleteRun(ctx, g.taskID, g.runID, g.actor, status, summary, waitingReason)
		if err == nil {
			err = g.tasks.VerifyRunCompletion(ctx, g.taskID, g.runID, g.actor, status)
		}
	} else {
		outcome := taskmanager.RunOutcome{Status: status, Summary: summary}
		err = g.tasks.CloseStartedRun(ctx, g.taskID, g.runID, g.actor, outcome, cause)
	}
	if err == nil || errors.Is(err, cause) && cause != nil && !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
		g.closed = true
	}
	return err
}

func (g *researchStartedRunGuard) CloseFallback() error {
	if g == nil || g.closed || g.attempted {
		return nil
	}
	status := domaintask.StatusFailed
	var cause error = errors.New("research operation exited before recording its outcome")
	if g.parent != nil && errors.Is(g.parent.Err(), context.Canceled) {
		status = domaintask.StatusCancelled
		cause = context.Canceled
	}
	return g.Close(status, "research operation did not complete", "", cause)
}

func (g *researchStartedRunGuard) DeferFallback() {
	if err := g.CloseFallback(); err != nil {
		log.Print("research started-run fallback terminal receipt unavailable")
	}
}

func researchTaskOutcome(status string, operationErr error) struct {
	status        domaintask.Status
	waitingReason string
} {
	if status == "waiting_input" && operationErr == nil {
		return struct {
			status        domaintask.Status
			waitingReason string
		}{domaintask.StatusWaiting, "research manual seed required"}
	}
	if status == "cancelled" || errors.Is(operationErr, context.Canceled) {
		return struct {
			status        domaintask.Status
			waitingReason string
		}{domaintask.StatusCancelled, ""}
	}
	if status == "succeeded" && operationErr == nil {
		return struct {
			status        domaintask.Status
			waitingReason string
		}{domaintask.StatusSucceeded, ""}
	}
	return struct {
		status        domaintask.Status
		waitingReason string
	}{domaintask.StatusFailed, ""}
}

func researchClosureFailure(result c.Result, err error) c.Result {
	if err == nil {
		return result
	}
	if result.Counts.RecordsSaved > 0 || result.Counts.DocumentsFetched > 0 || result.Counts.BoardPostsCreated > 0 {
		result.Status = "partial"
	} else {
		result.Status = "failed"
	}
	result.Missing = append(result.Missing, "task_receipt")
	return result
}
