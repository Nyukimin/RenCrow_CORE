package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The three places that used to list every Action of the history to join it to a
// Run or a name (the restart recoveries and the Playback receipt) ask for what
// they need instead. These tests hold them to the same results as before, with
// the Action store opened both ways, and to never listing the history again.

func TestRecoveriesGiveTheSameResultsWithEveryAskedForActionAndNoWholeHistoryList(t *testing.T) {
	type outcome struct {
		actionRuns, orphanRuns int
		states                 map[string]string
	}
	results := map[string]outcome{}
	for _, mode := range actionStoreModes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			s := newRestartScenario(t, mode.index)
			s.history(t, 12)

			// Left behind by the process that stopped:
			openTask, openRun := newRunningTaskRun(t, s.runtime.firstTasks, "open action", domaintask.RouteGeneral, "mio")
			openAction, _, err := s.actions.CreateAction(ctx, actionmanager.CreateInput{TaskID: openTask.TaskID, RunID: openRun.RunID, Kind: domainaction.KindLLM, Name: "llm.worker.generate"})
			if err != nil {
				t.Fatal(err)
			}
			doneTask, doneRun := newRunningTaskRun(t, s.runtime.firstTasks, "action finished, run not closed", domaintask.RouteGeneral, "mio")
			doneAction, doneAttempt, err := s.actions.CreateAction(ctx, actionmanager.CreateInput{TaskID: doneTask.TaskID, RunID: doneRun.RunID, Kind: domainaction.KindTool, Name: "tool"})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.actions.CompleteAttempt(ctx, doneAction.ActionID, doneAttempt.AttemptID, domainaction.AttemptStatusFailed, domainaction.StatusFailed, "failed"); err != nil {
				t.Fatal(err)
			}
			orphanTask, orphanRun := newRunningTaskRun(t, s.runtime.firstTasks, "no action at all", domaintask.RouteOperations, "shiro")
			s.restart(t)
			s.spy.reset()

			actionRuns, err := recoverActionRunsAfterRestart(ctx, s.actions, s.tasks)
			if err != nil {
				t.Fatal(err)
			}
			orphanRuns, err := recoverOrphanTaskRunsAfterRestart(ctx, s.actions, s.tasks)
			if err != nil {
				t.Fatal(err)
			}
			if whole := wholeHistory(s.spy.seen()); len(whole) > 0 {
				t.Fatalf("a restart recovery listed the history: %+v", whole)
			}
			if len(s.spy.seen()) == 0 {
				t.Fatal("the spy saw no list at all; the test measures nothing")
			}

			states := map[string]string{}
			for label, id := range map[string]modulecore.RunID{"open": openRun.RunID, "done": doneRun.RunID, "orphan": orphanRun.RunID} {
				run, err := s.tasks.GetRun(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				states["run "+label] = string(run.Status)
			}
			for label, id := range map[string]modulecore.TaskID{"open": openTask.TaskID, "done": doneTask.TaskID, "orphan": orphanTask.TaskID} {
				task, err := s.tasks.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				states["task "+label] = string(task.Status)
			}
			action, err := s.actions.GetAction(ctx, openAction.ActionID)
			if err != nil {
				t.Fatal(err)
			}
			states["open action"] = string(action.Status)
			results[mode.name] = outcome{actionRuns: actionRuns, orphanRuns: orphanRuns, states: states}

			// What the recoveries have always done, spelled out.
			if actionRuns != 2 || orphanRuns != 1 {
				t.Fatalf("recovered %d Action-backed and %d orphan Runs, want 2 and 1", actionRuns, orphanRuns)
			}
			if states["open action"] != string(domainaction.StatusCancelled) || states["task done"] != string(domaintask.StatusFailed) || states["task orphan"] != string(domaintask.StatusFailed) {
				t.Fatalf("states = %v", states)
			}
		})
	}
	if a, b := results["full read"], results["snapshot"]; a.actionRuns != b.actionRuns || a.orphanRuns != b.orphanRuns || fmt.Sprint(a.states) != fmt.Sprint(b.states) {
		t.Fatalf("the two stores recovered differently:\n  full read: %+v\n  snapshot:  %+v", a, b)
	}
}

// 5.4 / D5: the recovery checks the Run of every Action it acts on, and no longer
// every Run of the history.
func TestActionRecoveryChecksTheRunOfWhatItActsOnAndNotTheWholeHistory(t *testing.T) {
	for _, mode := range actionStoreModes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			s := newRestartScenario(t, mode.index)
			// A finished Action whose Run no Task owns: old data from before the Task
			// store was rebuilt, say.
			dangling, danglingAttempt, err := s.actions.CreateAction(ctx, actionmanager.CreateInput{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "old"})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.actions.CompleteAttempt(ctx, dangling.ActionID, danglingAttempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done"); err != nil {
				t.Fatal(err)
			}
			s.restart(t)

			if _, err := recoverActionRunsAfterRestart(ctx, s.actions, s.tasks); err != nil {
				t.Fatalf("a finished Action with an unknown Run stopped the recovery: %v", err)
			}
			// The whole-history check is still there for whoever asks for it.
			if err := verifyActionRunReferences(ctx, s.actions, s.tasks); err == nil {
				t.Fatal("the whole-history check passed a reference to a Run that does not exist")
			} else if !strings.Contains(err.Error(), "1 Run reference") {
				t.Fatalf("the whole-history check does not count what it found: %v", err)
			}

			// An Action the recovery does act on keeps the old rule.
			open, _, err := s.actions.CreateAction(ctx, actionmanager.CreateInput{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "stale"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recoverActionRunsAfterRestart(ctx, s.actions, s.tasks); err == nil {
				t.Fatal("an open Action whose Run does not exist was recovered without an error")
			}
			cancelled, err := s.actions.GetAction(ctx, open.ActionID)
			if err != nil || cancelled.Status != domainaction.StatusCancelled {
				t.Fatalf("the stale Action was left %v (%v); the recovery cancels it before it looks for the Run, as it always did", cancelled.Status, err)
			}
		})
	}
}

func TestPlaybackReceiptAsksForItsOwnNameAndNothingElse(t *testing.T) {
	for _, mode := range actionStoreModes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = taskStore.Close() })
			tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
			actions, spy := openSpiedActionManager(t, filepath.Join(t.TempDir(), "actions"), mode.index)
			recorder, err := newPlaybackActionRecorder(actions, tasks, "mio")
			if err != nil {
				t.Fatal(err)
			}
			ack := ttsPlaybackAckRequest{PublicPlaybackRef: "response-1", UtteranceID: "utterance-1", ChunkIndex: 2, Status: "ended"}
			wantName := "playback_receipt:response-1:utterance-1:2"

			// Other Actions that must not be taken for the receipt: another Kind with
			// the same name, and the same Kind with another chunk.
			task, run := newRunningTaskRun(t, tasks, "other", domaintask.RouteGeneral, "mio")
			for _, spec := range []struct {
				kind domainaction.Kind
				name string
			}{{domainaction.KindTool, wantName}, {domainaction.KindPlayback, "playback_receipt:response-1:utterance-1:3"}} {
				action, attempt, err := actions.CreateAction(ctx, actionmanager.CreateInput{TaskID: task.TaskID, RunID: run.RunID, Kind: spec.kind, Name: spec.name})
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := actions.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done"); err != nil {
					t.Fatal(err)
				}
			}
			// And an open Playback Action of the same name: only a finished one is a
			// receipt already recorded.
			openAction, _, err := actions.CreateAction(ctx, actionmanager.CreateInput{TaskID: task.TaskID, RunID: run.RunID, Kind: domainaction.KindPlayback, Name: wantName})
			if err != nil {
				t.Fatal(err)
			}
			spy.reset()

			if err := recorder.Record(ctx, ack); err != nil {
				t.Fatal(err)
			}
			if err := recorder.Record(ctx, ack); err != nil {
				t.Fatalf("idempotent retry: %v", err)
			}
			listed, err := actions.ListActions(ctx, domainaction.Filter{Kind: domainaction.KindPlayback, Name: wantName})
			if err != nil {
				t.Fatal(err)
			}
			terminal := 0
			for _, a := range listed {
				if domainaction.IsTerminal(a.Status) {
					terminal++
				}
			}
			openFound := false
			for _, a := range listed {
				openFound = openFound || a.ActionID == openAction.ActionID
			}
			if len(listed) != 2 || terminal != 1 || !openFound {
				t.Fatalf("Playback Actions of the name: %d, %d finished; want the open one and exactly one receipt", len(listed), terminal)
			}
			for _, f := range spy.seen() {
				if f.Kind != domainaction.KindPlayback || f.Name != wantName {
					t.Fatalf("a Playback receipt listed with %+v; it must ask for its own Kind and name", f)
				}
			}
		})
	}
}
