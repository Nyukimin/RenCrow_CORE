package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type playbackActionRecorder struct {
	actions *actionmanager.Manager
	tasks   *taskmanager.Manager
	actorID string
}

func newPlaybackActionRecorder(actions *actionmanager.Manager, tasks *taskmanager.Manager, actorID string) (*playbackActionRecorder, error) {
	if actions == nil || tasks == nil || strings.TrimSpace(actorID) == "" {
		return nil, errors.New("Playback execution owners are required")
	}
	return &playbackActionRecorder{actions: actions, tasks: tasks, actorID: strings.TrimSpace(actorID)}, nil
}

func (r *playbackActionRecorder) Record(ctx context.Context, ack ttsPlaybackAckRequest) error {
	publicRef := strings.TrimSpace(ack.PublicPlaybackRef)
	if publicRef == "" {
		return errors.New("public_playback_ref is required")
	}
	name := "playback_receipt:" + publicRef + ":" + strings.TrimSpace(ack.UtteranceID) + ":" + strconv.Itoa(ack.ChunkIndex)
	// A receipt is recorded once. Ask for the Actions of this Kind and exact name,
	// not for the history to search it: only a finished one counts as recorded.
	existing, err := r.actions.ListActions(ctx, domainaction.Filter{Kind: domainaction.KindPlayback, Name: name})
	if err != nil {
		return err
	}
	for _, action := range existing {
		if domainaction.IsTerminal(action.Status) {
			return nil
		}
	}
	task, run, err := r.tasks.CreateAndStartRun(ctx, domaintask.Task{
		Title:    "Playback receipt",
		Route:    domaintask.RouteCHAT,
		OwnerID:  r.actorID,
		Assignee: r.actorID,
		ReadOnly: true,
	}, domaintask.SharedRoleContext{
		UserIntent:  publicRef,
		CurrentPlan: "record actual audio playback outcome",
	})
	if err != nil {
		return err
	}
	// From here on the Run holds an execution slot, so every path that ends
	// the call closes it: the failed Action creation here, the receipt below.
	action, attempt, err := r.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: task.TaskID,
		RunID:  run.RunID,
		Kind:   domainaction.KindPlayback,
		Name:   name,
	})
	if err != nil {
		return r.tasks.CloseStartedRun(ctx, task.TaskID, run.RunID, r.actorID, taskmanager.RunOutcomeOf(err, ""), err)
	}
	attemptOutcome, runOutcome := playbackReceiptOutcome(ack)
	// The outcome is the receipt's own, not an error, so there is no cause to return.
	if closed, err := r.actions.CloseAttempt(ctx, action.ActionID, attempt.AttemptID, attemptOutcome, nil); !closed {
		return fmt.Errorf("complete Playback action: %w", err)
	}
	if err := r.tasks.CloseStartedRun(ctx, task.TaskID, run.RunID, r.actorID, runOutcome, nil); err != nil {
		return fmt.Errorf("complete Playback run: %w", err)
	}
	return nil
}

// playbackReceiptOutcome maps the playback status the viewer reported to how the
// Attempt, Action, Run, and Task end.
func playbackReceiptOutcome(ack ttsPlaybackAckRequest) (actionmanager.AttemptOutcome, taskmanager.RunOutcome) {
	status := strings.ToLower(strings.TrimSpace(ack.Status))
	if status == "ended" || status == "completed" || status == "succeeded" {
		const completed = "Playback completed"
		return actionmanager.AttemptOutcome{Attempt: domainaction.AttemptStatusSucceeded, Action: domainaction.StatusSucceeded, Summary: completed},
			taskmanager.RunOutcome{Status: domaintask.StatusSucceeded, Summary: completed}
	}
	summary := strings.TrimSpace(ack.Error)
	if summary == "" {
		summary = strings.TrimSpace(ack.ErrorCode)
	}
	if summary == "" {
		summary = "Playback status rejected: " + status
	}
	if status == "cancelled" || status == "aborted" {
		return actionmanager.AttemptOutcome{Attempt: domainaction.AttemptStatusCancelled, Action: domainaction.StatusCancelled, Summary: summary},
			taskmanager.RunOutcome{Status: domaintask.StatusCancelled, Summary: summary}
	}
	return actionmanager.AttemptOutcome{Attempt: domainaction.AttemptStatusFailed, Action: domainaction.StatusFailed, Summary: summary},
		taskmanager.RunOutcome{Status: domaintask.StatusFailed, Summary: summary}
}
