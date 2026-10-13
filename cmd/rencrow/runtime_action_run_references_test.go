package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestVerifyActionRunReferencesPassesAHistoryWhoseRunsAllExist(t *testing.T) {
	for _, mode := range actionStoreModes {
		t.Run(mode.name, func(t *testing.T) {
			s := newRestartScenario(t, mode.index)
			s.history(t, 5)
			s.restart(t)
			if err := verifyActionRunReferences(context.Background(), s.actions, s.tasks); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifyActionRunReferencesCountsTheRunsNoTaskOwns(t *testing.T) {
	for _, mode := range actionStoreModes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			s := newRestartScenario(t, mode.index)
			s.history(t, 3)
			// Two Actions of one missing Run count once; a second missing Run counts again.
			for _, run := range []modulecore.RunID{modulecore.NewRunID(), modulecore.NewRunID()} {
				task := modulecore.NewTaskID()
				for i := 0; i < 2; i++ {
					action, attempt, err := s.actions.CreateAction(ctx, actionmanager.CreateInput{TaskID: task, RunID: run, Kind: domainaction.KindTool, Name: "orphaned"})
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := s.actions.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done"); err != nil {
						t.Fatal(err)
					}
				}
			}
			s.restart(t)
			err := verifyActionRunReferences(ctx, s.actions, s.tasks)
			if err == nil || !strings.Contains(err.Error(), "2 Run reference(s)") {
				t.Fatalf("the check = %v, want it to report the two Runs no Task owns", err)
			}
		})
	}
}
