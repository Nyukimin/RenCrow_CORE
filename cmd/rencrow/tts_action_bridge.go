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
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type actionTTSBridge struct {
	inner     orchestrator.TTSBridge
	actions   *actionmanager.Manager
	tasks     *taskmanager.Manager
	transport *transportmanager.Manager
	actorID   string

	mu       sync.Mutex
	sessions map[string]ttsActionSession
}

type actionTTSDisplayBridge struct {
	*actionTTSBridge
	displayInner orchestrator.TTSDisplayBridge
}

type ttsActionSession struct {
	identity     domainexecution.Identity
	action       domainaction.Action
	attempt      domainaction.Attempt
	ownsRun      bool
	operationErr error
	completed    bool
}

func newActionTTSBridge(inner orchestrator.TTSBridge, actions *actionmanager.Manager, tasks *taskmanager.Manager, transport *transportmanager.Manager, actorID string) (orchestrator.TTSBridge, error) {
	if inner == nil {
		return nil, nil
	}
	if actions == nil || tasks == nil || transport == nil {
		return nil, errors.New("TTS execution owners are required")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, errors.New("TTS actor_id is required")
	}
	base := &actionTTSBridge{
		inner:     inner,
		actions:   actions,
		tasks:     tasks,
		transport: transport,
		actorID:   actorID,
		sessions:  make(map[string]ttsActionSession),
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
		err = fmt.Errorf("create TTS action: %w", err)
		if ownsRun {
			// The Run this call started holds an execution slot, and no Action
			// exists to close it later.
			return b.tasks.AbortStartedRun(ctx, identity.TaskID, identity.RunID, b.actorID, err)
		}
		return err
	}
	session := ttsActionSession{identity: identity, action: action, attempt: attempt, ownsRun: ownsRun}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return b.complete(ctx, session, err)
	}
	ownedCtx, err = domaintransport.WithReceiptOwner(ownedCtx, b.transport)
	if err != nil {
		return b.complete(ctx, session, err)
	}
	if err := b.inner.StartSession(ownedCtx, req); err != nil {
		return b.complete(ctx, session, err)
	}
	b.mu.Lock()
	_, exists := b.sessions[sessionID]
	if !exists {
		b.sessions[sessionID] = session
	}
	b.mu.Unlock()
	if exists {
		// Closing writes to the stores for up to a minute per write, so the lock
		// that guards every session is not held across it.
		return b.complete(ctx, session, errors.New("TTS session already exists"))
	}
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

func (b *actionTTSBridge) PushSpeechPlan(ctx context.Context, ttsSessionID, conversationSessionID, turnID, speakerID string, plan speech.SpeechPlan) error {
	session, ownedCtx, err := b.sessionContext(ctx, ttsSessionID)
	if err != nil {
		return err
	}
	err = ownedCtx.Err()
	if err == nil {
		pusher, ok := b.inner.(orchestrator.SpeechPlanTTSBridge)
		if !ok {
			err = errors.New("TTS bridge does not support SpeechPlan synthesis")
		} else {
			err = pusher.PushSpeechPlan(ownedCtx, ttsSessionID, conversationSessionID, turnID, speakerID, plan)
		}
	}
	if err != nil {
		b.recordSessionError(ttsSessionID, session, err)
		b.mu.Lock()
		if current, ok := b.sessions[strings.TrimSpace(ttsSessionID)]; ok && current.action.ActionID == session.action.ActionID && current.attempt.AttemptID == session.attempt.AttemptID {
			session = current
		}
		b.mu.Unlock()
		closed, completionErr := b.completeOwned(context.WithoutCancel(ctx), session, session.operationErr)
		if closed {
			b.mu.Lock()
			if current, ok := b.sessions[strings.TrimSpace(ttsSessionID)]; ok && current.action.ActionID == session.action.ActionID && current.attempt.AttemptID == session.attempt.AttemptID {
				current.completed = true
				b.sessions[strings.TrimSpace(ttsSessionID)] = current
			}
			b.mu.Unlock()
		}
		if completionErr != nil {
			return completionErr
		}
		return err
	}
	return err
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
	endErr := b.inner.EndSession(ownedCtx, sessionID)
	if session.completed {
		return endErr
	}
	return b.complete(ctx, session, errors.Join(session.operationErr, endErr))
}

func (b *actionTTSBridge) recordSessionError(sessionID string, session ttsActionSession, operationErr error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	current, ok := b.sessions[strings.TrimSpace(sessionID)]
	if !ok || current.action.ActionID != session.action.ActionID || current.attempt.AttemptID != session.attempt.AttemptID {
		return
	}
	current.operationErr = errors.Join(current.operationErr, operationErr)
	b.sessions[strings.TrimSpace(sessionID)] = current
}

func (b *actionTTSBridge) executionContext(ctx context.Context) (context.Context, domainexecution.Identity, bool, error) {
	if identity, err := domainexecution.IdentityFromContext(ctx); err == nil {
		return ctx, identity, false, nil
	}
	task, run, err := b.tasks.CreateAndStartRun(ctx, domaintask.Task{
		Title:    "TTS playback session",
		Route:    domaintask.RouteCHAT,
		OwnerID:  b.actorID,
		Assignee: b.actorID,
		ReadOnly: true,
	}, domaintask.SharedRoleContext{CurrentPlan: "synthesize and play response audio"})
	if err != nil {
		return nil, domainexecution.Identity{}, false, err
	}
	identity := domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID, TraceID: modulecore.NewTraceID()}
	ownedCtx, err := domainexecution.WithIdentity(ctx, identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		return nil, domainexecution.Identity{}, false, b.tasks.AbortStartedRun(ctx, identity.TaskID, identity.RunID, b.actorID, err)
	}
	return ownedCtx, identity, true, nil
}

func (b *actionTTSBridge) sessionContext(ctx context.Context, sessionID string) (ttsActionSession, context.Context, error) {
	b.mu.Lock()
	session, ok := b.sessions[strings.TrimSpace(sessionID)]
	b.mu.Unlock()
	if !ok {
		return ttsActionSession{}, nil, errors.New("TTS Action session is unavailable")
	}
	ownedCtx, err := domainexecution.WithIdentity(ctx, session.identity.TaskID, session.identity.RunID, session.identity.TraceID)
	if err != nil {
		return ttsActionSession{}, nil, err
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, session.action.ActionID, session.attempt.AttemptID)
	if err == nil {
		ownedCtx, err = domaintransport.WithReceiptOwner(ownedCtx, b.transport)
	}
	return session, ownedCtx, err
}

func (b *actionTTSBridge) deleteSession(sessionID string) {
	b.mu.Lock()
	delete(b.sessions, strings.TrimSpace(sessionID))
	b.mu.Unlock()
}

// complete closes the Attempt and Action of a session, and the Run when the
// session started it. If the Attempt cannot be closed the Run stays open with it.
func (b *actionTTSBridge) complete(ctx context.Context, session ttsActionSession, operationErr error) error {
	_, err := b.completeOwned(ctx, session, operationErr)
	return err
}

func (b *actionTTSBridge) completeOwned(ctx context.Context, session ttsActionSession, operationErr error) (bool, error) {
	const completed = "TTS session completed"
	if closed, err := b.actions.CloseAttempt(ctx, session.action.ActionID, session.attempt.AttemptID, actionmanager.AttemptOutcomeOf(operationErr, completed), operationErr); !closed {
		return false, err
	}
	if !session.ownsRun {
		return true, operationErr
	}
	if err := b.tasks.CloseStartedRun(ctx, session.identity.TaskID, session.identity.RunID, b.actorID, taskmanager.RunOutcomeOf(operationErr, completed), operationErr); err != nil {
		return true, err
	}
	return true, operationErr
}
