package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

func TestResearchStartedRunClosureUsesExactRunAndTerminalOwner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  domaintask.Status
		wantRun domaintask.RunStatus
		cause   error
	}{
		{name: "success", status: domaintask.StatusSucceeded, wantRun: domaintask.RunStatusSucceeded},
		{name: "failure", status: domaintask.StatusFailed, wantRun: domaintask.RunStatusFailed, cause: errors.New("work failed")},
		{name: "cancel", status: domaintask.StatusCancelled, wantRun: domaintask.RunStatusCancelled, cause: context.Canceled},
		{name: "waiting", status: domaintask.StatusWaiting, wantRun: domaintask.RunStatusWaiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
			task, run, err := closure.Tasks.CreateAndStartRun(context.Background(), domaintask.Task{Title: "research", Assignee: "ren", Route: domaintask.RouteResearch}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatal(err)
			}
			guard := newResearchStartedRunGuard(closure.Tasks, task.TaskID, run.RunID, "ren", context.Background())
			if err := guard.Close(tc.status, "research outcome", "dependency input required", tc.cause); err != tc.cause {
				t.Fatalf("Close error=%v, want cause %v", err, tc.cause)
			}
			if !guard.closed {
				t.Fatal("successful closure was not marked closed")
			}
			closure.AssertRunState(t, tc.status, tc.wantRun)
		})
	}
}

func TestResearchStartedRunGuardFallbackAndForeignRun(t *testing.T) {
	t.Run("fallback closes after panic", func(t *testing.T) {
		closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
		task, run, err := closure.Tasks.CreateAndStartRun(context.Background(), domaintask.Task{Title: "research", Assignee: "ren", Route: domaintask.RouteResearch}, domaintask.SharedRoleContext{})
		if err != nil {
			t.Fatal(err)
		}
		guard := newResearchStartedRunGuard(closure.Tasks, task.TaskID, run.RunID, "ren", context.Background())
		func() {
			defer func() {
				_ = guard.CloseFallback()
				if recovered := recover(); recovered == nil {
					t.Fatal("panic was not preserved")
				}
			}()
			panic("injected")
		}()
		closure.AssertRunState(t, domaintask.StatusFailed, domaintask.RunStatusFailed)
	})
	t.Run("caller cancellation fallback closes detached", func(t *testing.T) {
		closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
		task, run, err := closure.Tasks.CreateAndStartRun(context.Background(), domaintask.Task{Title: "research", Assignee: "ren", Route: domaintask.RouteResearch}, domaintask.SharedRoleContext{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		guard := newResearchStartedRunGuard(closure.Tasks, task.TaskID, run.RunID, "ren", ctx)
		if err := guard.CloseFallback(); !errors.Is(err, context.Canceled) || !guard.closed {
			t.Fatalf("fallback error=%v closed=%t, want preserved cancellation after successful close", err, guard.closed)
		}
		closure.AssertRunState(t, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
	})

	t.Run("foreign run remains untouched", func(t *testing.T) {
		closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})
		firstTask, firstRun, err := closure.Tasks.CreateAndStartRun(context.Background(), domaintask.Task{Title: "first", Assignee: "ren", Route: domaintask.RouteResearch}, domaintask.SharedRoleContext{})
		if err != nil {
			t.Fatal(err)
		}
		if err := closure.Tasks.CloseStartedRun(context.Background(), firstTask.TaskID, firstRun.RunID, "ren", taskmanager.RunOutcomeOf(errors.New("cleanup"), ""), nil); err != nil {
			t.Fatal(err)
		}
		_, foreignRun, err := closure.Tasks.CreateAndStartRun(context.Background(), domaintask.Task{Title: "second", Assignee: "ren", Route: domaintask.RouteResearch}, domaintask.SharedRoleContext{})
		if err != nil {
			t.Fatal(err)
		}
		guard := newResearchStartedRunGuard(closure.Tasks, firstTask.TaskID, foreignRun.RunID, "ren", context.Background())
		if err := guard.Close(domaintask.StatusSucceeded, "wrong", "", nil); err == nil {
			t.Fatal("foreign run was accepted")
		}
		stored, err := closure.Tasks.GetRun(context.Background(), foreignRun.RunID)
		if err != nil || stored.Status != domaintask.RunStatusRunning {
			t.Fatalf("foreign run changed: %+v %v", stored, err)
		}
	})
}

func TestResearchStartedRunReceiptFailureIsReportedAndDoesNotEraseResearchResult(t *testing.T) {
	closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{TaskTerminalErr: errors.New("terminal write refused")})
	task, run, err := closure.Tasks.CreateAndStartRun(context.Background(), domaintask.Task{Title: "research", Assignee: "ren", Route: domaintask.RouteResearch}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	guard := newResearchStartedRunGuard(closure.Tasks, task.TaskID, run.RunID, "ren", context.Background())
	result := c.Result{Status: "succeeded", Counts: c.Counts{RecordsSaved: 1}}
	closeErr := guard.Close(domaintask.StatusSucceeded, "summary", "", nil)
	if closeErr == nil || !errors.Is(closeErr, taskmanager.ErrStartedRunNotClosed) {
		t.Fatalf("Close error=%v, want marked terminal-write failure", closeErr)
	}
	result = researchClosureFailure(result, closeErr)
	if result.Status != "partial" || len(result.Missing) != 1 || result.Missing[0] != "task_receipt" || result.Counts.RecordsSaved != 1 {
		t.Fatalf("receipt failure lost result or was reported as success: %+v", result)
	}
	closure.AssertRunLeftRunning(t)
}
