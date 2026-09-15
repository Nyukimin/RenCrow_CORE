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
	existing, err := r.actions.ListActions(ctx, domainaction.Filter{})
	if err != nil {
		return err
	}
	for _, action := range existing {
		if action.Kind == domainaction.KindPlayback && action.Name == name && domainaction.IsTerminal(action.Status) {
			return nil
		}
	}
	task, err := r.tasks.Create(ctx, domaintask.Task{
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
	run, err := r.tasks.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		return err
	}
	action, attempt, err := r.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: task.TaskID,
		RunID:  run.RunID,
		Kind:   domainaction.KindPlayback,
		Name:   name,
	})
	if err != nil {
		return err
	}
	attemptStatus, actionStatus, taskStatus, summary := playbackReceiptOutcome(ack)
	completionCtx := context.WithoutCancel(ctx)
	if _, _, err := r.actions.CompleteAttempt(completionCtx, action.ActionID, attempt.AttemptID, attemptStatus, actionStatus, summary); err != nil {
		return fmt.Errorf("complete Playback action: %w", err)
	}
	if _, err := r.tasks.CompleteRun(completionCtx, task.TaskID, run.RunID, r.actorID, taskStatus, summary, ""); err != nil {
		return fmt.Errorf("complete Playback run: %w", err)
	}
	return nil
}

func playbackReceiptOutcome(ack ttsPlaybackAckRequest) (domainaction.AttemptStatus, domainaction.Status, domaintask.Status, string) {
	status := strings.ToLower(strings.TrimSpace(ack.Status))
	if status == "ended" || status == "completed" || status == "succeeded" {
		return domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, domaintask.StatusSucceeded, "Playback completed"
	}
	summary := strings.TrimSpace(ack.Error)
	if summary == "" {
		summary = strings.TrimSpace(ack.ErrorCode)
	}
	if summary == "" {
		summary = "Playback status rejected: " + status
	}
	if status == "cancelled" || status == "aborted" {
		return domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, domaintask.StatusCancelled, summary
	}
	return domainaction.AttemptStatusFailed, domainaction.StatusFailed, domaintask.StatusFailed, summary
}
