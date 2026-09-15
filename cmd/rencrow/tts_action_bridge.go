package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type actionTTSBridge struct {
	inner   orchestrator.TTSBridge
	actions *actionmanager.Manager
	tasks   *taskmanager.Manager
	actorID string

	mu       sync.Mutex
	sessions map[string]ttsActionSession
}

type actionTTSDisplayBridge struct {
	*actionTTSBridge
	displayInner orchestrator.TTSDisplayBridge
}

type ttsActionSession struct {
	identity domainexecution.Identity
	action   domainaction.Action
	attempt  domainaction.Attempt
	ownsRun  bool
}

func newActionTTSBridge(inner orchestrator.TTSBridge, actions *actionmanager.Manager, tasks *taskmanager.Manager, actorID string) (orchestrator.TTSBridge, error) {
	if inner == nil {
		return nil, nil
	}
	if actions == nil || tasks == nil {
		return nil, errors.New("TTS execution owners are required")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, errors.New("TTS actor_id is required")
	}
	base := &actionTTSBridge{
		inner:    inner,
		actions:  actions,
		tasks:    tasks,
		actorID:  actorID,
		sessions: make(map[string]ttsActionSession),
	}
	if display, ok := inner.(orchestrator.TTSDisplayBridge); ok {
		return &actionTTSDisplayBridge{actionTTSBridge: base, displayInner: display}, nil
	}
	return base, nil
}

func (b *actionTTSBridge) StartSession(ctx context.Context, req orchestrator.TTSSessionStart) error {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return errors.New("TTS session_id is required")
	}
	ownedCtx, identity, ownsRun, err := b.executionContext(ctx)
	if err != nil {
		return err
	}
	action, attempt, err := b.actions.CreateAction(ownedCtx, actionmanager.CreateInput{
		TaskID: identity.TaskID,
		RunID:  identity.RunID,
		Kind:   domainaction.KindTTS,
		Name:   "tts_session",
	})
	if err != nil {
		return fmt.Errorf("create TTS action: %w", err)
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return err
	}
	if err := b.inner.StartSession(ownedCtx, req); err != nil {
		return b.complete(ctx, ttsActionSession{identity: identity, action: action, attempt: attempt, ownsRun: ownsRun}, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.sessions[sessionID]; exists {
		return b.complete(ctx, ttsActionSession{identity: identity, action: action, attempt: attempt, ownsRun: ownsRun}, errors.New("TTS session already exists"))
	}
	b.sessions[sessionID] = ttsActionSession{identity: identity, action: action, attempt: attempt, ownsRun: ownsRun}
	return nil
}

func (b *actionTTSBridge) PushText(ctx context.Context, sessionID string, text string, emotion *moduletts.EmotionState) error {
	session, ownedCtx, err := b.sessionContext(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := b.inner.PushText(ownedCtx, sessionID, text, emotion); err != nil {
		b.deleteSession(sessionID)
		return b.complete(ctx, session, err)
	}
	return nil
}

func (b *actionTTSDisplayBridge) PushTextWithDisplay(ctx context.Context, sessionID string, speechText string, displayText string, emotion *moduletts.EmotionState) error {
	session, ownedCtx, err := b.sessionContext(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := b.displayInner.PushTextWithDisplay(ownedCtx, sessionID, speechText, displayText, emotion); err != nil {
		b.deleteSession(sessionID)
		return b.complete(ctx, session, err)
	}
	return nil
}

func (b *actionTTSBridge) EndSession(ctx context.Context, sessionID string) error {
	session, ownedCtx, err := b.sessionContext(ctx, sessionID)
	if err != nil {
		return err
	}
	b.deleteSession(sessionID)
	return b.complete(ctx, session, b.inner.EndSession(ownedCtx, sessionID))
}

func (b *actionTTSBridge) executionContext(ctx context.Context) (context.Context, domainexecution.Identity, bool, error) {
	if identity, err := domainexecution.IdentityFromContext(ctx); err == nil {
		return ctx, identity, false, nil
	}
	task, err := b.tasks.Create(ctx, domaintask.Task{
		Title:    "TTS playback session",
		Route:    domaintask.RouteCHAT,
		OwnerID:  b.actorID,
		Assignee: b.actorID,
		ReadOnly: true,
	}, domaintask.SharedRoleContext{CurrentPlan: "synthesize and play response audio"})
	if err != nil {
		return nil, domainexecution.Identity{}, false, err
	}
	run, err := b.tasks.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		return nil, domainexecution.Identity{}, false, err
	}
	identity := domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID, TraceID: modulecore.NewTraceID()}
	ownedCtx, err := domainexecution.WithIdentity(ctx, identity.TaskID, identity.RunID, identity.TraceID)
	return ownedCtx, identity, true, err
}

func (b *actionTTSBridge) sessionContext(ctx context.Context, sessionID string) (ttsActionSession, context.Context, error) {
	b.mu.Lock()
	session, ok := b.sessions[strings.TrimSpace(sessionID)]
	b.mu.Unlock()
	if !ok {
		return ttsActionSession{}, nil, errors.New("TTS Action session is unavailable")
	}
	ownedCtx := ctx
	if _, err := domainexecution.IdentityFromContext(ownedCtx); err != nil {
		var bindErr error
		ownedCtx, bindErr = domainexecution.WithIdentity(ownedCtx, session.identity.TaskID, session.identity.RunID, session.identity.TraceID)
		if bindErr != nil {
			return ttsActionSession{}, nil, bindErr
		}
	}
	ownedCtx, err := domainexecution.WithChildBoundActionAttempt(ownedCtx, session.action.ActionID, session.attempt.AttemptID)
	return session, ownedCtx, err
}

func (b *actionTTSBridge) deleteSession(sessionID string) {
	b.mu.Lock()
	delete(b.sessions, strings.TrimSpace(sessionID))
	b.mu.Unlock()
}

func (b *actionTTSBridge) complete(ctx context.Context, session ttsActionSession, operationErr error) error {
	attemptStatus := domainaction.AttemptStatusSucceeded
	actionStatus := domainaction.StatusSucceeded
	taskStatus := domaintask.StatusSucceeded
	summary := "TTS session completed"
	switch {
	case errors.Is(operationErr, context.Canceled):
		attemptStatus, actionStatus, taskStatus, summary = domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, domaintask.StatusCancelled, operationErr.Error()
	case errors.Is(operationErr, context.DeadlineExceeded):
		attemptStatus, actionStatus, taskStatus, summary = domainaction.AttemptStatusTimedOut, domainaction.StatusFailed, domaintask.StatusFailed, operationErr.Error()
	case operationErr != nil:
		attemptStatus, actionStatus, taskStatus, summary = domainaction.AttemptStatusFailed, domainaction.StatusFailed, domaintask.StatusFailed, operationErr.Error()
	}
	completionCtx := context.WithoutCancel(ctx)
	if _, _, err := b.actions.CompleteAttempt(completionCtx, session.action.ActionID, session.attempt.AttemptID, attemptStatus, actionStatus, summary); err != nil {
		return errors.Join(operationErr, err)
	}
	if session.ownsRun {
		if _, err := b.tasks.CompleteRun(completionCtx, session.identity.TaskID, session.identity.RunID, b.actorID, taskStatus, summary, ""); err != nil {
			return errors.Join(operationErr, err)
		}
	}
	return operationErr
}
