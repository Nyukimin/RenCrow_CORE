package idlechat

import (
	"context"
	"errors"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const idleChatConversationRunCleanupTimeout = 30 * time.Second

var (
	errIdleChatConversationRunPending     = errors.New("idlechat conversation run finalization is pending")
	errIdleChatConversationRunStarting    = errors.New("idlechat conversation run admission is in progress")
	errIdleChatConversationRunInvalidated = errors.New("idlechat conversation run admission was invalidated")
)

type conversationRunOutcome struct {
	generation uint64
	status     domaintask.Status
	summary    string
	reason     string
}

type conversationRunFinalization struct {
	generation uint64
	taskID     modulecore.TaskID
	runID      modulecore.RunID
	status     domaintask.Status
	summary    string
	reason     string
}

// startIdleRun performs the owner admission outside the orchestrator locks. The
// locked half only reserves a generation and context; the owner-issued pair is
// installed only when that same reservation is still current.
func (o *IdleChatOrchestrator) startIdleRun() (uint64, error) {
	if o == nil {
		return 0, errors.New("idlechat is not configured")
	}
	// Keep an invalidated owner admission in flight until its exact pair has
	// either been installed or finalized. Otherwise a concurrent start could
	// issue a second Task/Run before the first admission result is reconciled.
	o.conversationRunStartMu.Lock()
	defer o.conversationRunStartMu.Unlock()
	if err := o.finalizePendingConversationRun(); err != nil {
		return 0, errors.Join(errIdleChatConversationRunPending, err)
	}
	if o.ctx != nil {
		if err := o.ctx.Err(); err != nil {
			return 0, err
		}
	}

	o.emitMu.Lock()
	o.mu.Lock()
	if o.pendingConversationRun != nil {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return 0, errIdleChatConversationRunPending
	}
	if o.ctx != nil {
		if err := o.ctx.Err(); err != nil {
			o.mu.Unlock()
			o.emitMu.Unlock()
			return 0, err
		}
	}
	if o.runCancel != nil {
		if o.conversationRunStarting {
			o.mu.Unlock()
			o.emitMu.Unlock()
			return 0, errIdleChatConversationRunStarting
		}
		generation := o.activeGeneration
		o.mu.Unlock()
		o.emitMu.Unlock()
		return generation, nil
	}
	if !o.chatActive {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return 0, errors.New("idlechat conversation is not active")
	}
	generation, err := o.beginIdleRunLocked()
	if err != nil {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return 0, err
	}
	issuer := o.runIssuer
	runCtx := o.runCtx
	if issuer == nil {
		o.conversationRunStarting = false
	}
	o.mu.Unlock()
	o.emitMu.Unlock()
	if issuer == nil {
		return generation, nil
	}

	taskID, runID, err := issueIdleChatRun(runCtx, issuer, "IdleChat conversation", "shiro", domaintask.RunStartReasonFirst, "")
	if err != nil {
		o.abortIdleRunStart(generation)
		return generation, err
	}

	o.emitMu.Lock()
	o.mu.Lock()
	current := o.activeGeneration == generation &&
		o.conversationRunStarting &&
		o.chatActive &&
		o.pendingConversationRun == nil &&
		o.runCancel != nil &&
		o.runCtx != nil &&
		o.runCtx.Err() == nil
	if current {
		o.activeTaskID = taskID
		o.activeRunID = runID
		o.conversationRunStarting = false
		o.mu.Unlock()
		o.emitMu.Unlock()
		return generation, nil
	}
	if o.activeGeneration == generation {
		o.conversationRunStarting = false
	}
	o.mu.Unlock()
	o.emitMu.Unlock()

	// The owner may have committed immediately before cancellation became
	// visible. Preserve and close that exact pair through the canonical owner.
	o.emitMu.Lock()
	o.mu.Lock()
	if o.pendingConversationRun == nil {
		o.pendingConversationRun = &conversationRunFinalization{
			generation: generation,
			taskID:     taskID,
			runID:      runID,
			status:     domaintask.StatusCancelled,
			summary:    "IdleChat conversation admission cancelled",
			reason:     "conversation run admission invalidated",
		}
	}
	o.mu.Unlock()
	o.emitMu.Unlock()
	finalizeErr := o.finalizePendingConversationRun()
	return generation, errors.Join(errIdleChatConversationRunInvalidated, finalizeErr)
}

func (o *IdleChatOrchestrator) abortIdleRunStart(generation uint64) {
	if o == nil {
		return
	}
	o.emitMu.Lock()
	o.mu.Lock()
	if o.activeGeneration != generation {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return
	}
	if o.conversationRunStarting {
		o.conversationRunStarting = false
	}
	cancel := o.runCancel
	o.runCancel = nil
	o.runCtx = o.ctx
	o.activeTraceID = ""
	o.activeTraceSessionID = ""
	o.mu.Unlock()
	o.emitMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (o *IdleChatOrchestrator) conversationRunCleanupContext() (context.Context, context.CancelFunc) {
	base := context.Background()
	if o != nil && o.ctx != nil {
		base = context.WithoutCancel(o.ctx)
	}
	return context.WithTimeout(base, idleChatConversationRunCleanupTimeout)
}

func (o *IdleChatOrchestrator) finalizePendingConversationRun() error {
	if o == nil {
		return nil
	}
	o.conversationRunFinalizeMu.Lock()
	defer o.conversationRunFinalizeMu.Unlock()

	o.emitMu.Lock()
	o.mu.Lock()
	if o.pendingConversationRun == nil {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return nil
	}
	pending := *o.pendingConversationRun
	issuer := o.runIssuer
	o.mu.Unlock()
	o.emitMu.Unlock()
	if issuer == nil {
		return errors.New("idlechat conversation run owner is not configured")
	}

	ctx, cancel := o.conversationRunCleanupContext()
	err := completeGenerationRun(ctx, issuer, pending.taskID, pending.runID, pending.status, pending.summary, pending.reason)
	cancel()

	o.emitMu.Lock()
	o.mu.Lock()
	if current := o.pendingConversationRun; current != nil &&
		current.generation == pending.generation && current.taskID == pending.taskID && current.runID == pending.runID {
		if err == nil {
			o.pendingConversationRun = nil
			if o.activeTaskID == pending.taskID && o.activeRunID == pending.runID {
				o.activeTaskID = ""
				o.activeRunID = ""
			}
			if o.conversationRunOutcome != nil && o.conversationRunOutcome.generation == pending.generation {
				o.conversationRunOutcome = nil
			}
		}
	}
	o.mu.Unlock()
	o.emitMu.Unlock()
	return err
}

func (o *IdleChatOrchestrator) setConversationRunOutcome(generation uint64, status domaintask.Status, summary, reason string) {
	if o == nil || generation == 0 {
		return
	}
	if !generationRunCompletionStatusValid(status) {
		return
	}
	o.emitMu.Lock()
	o.mu.Lock()
	if o.activeGeneration == generation && o.pendingConversationRun == nil && !o.conversationRunStarting {
		if o.conversationRunOutcome == nil || o.conversationRunOutcome.generation != generation ||
			generationRunCompletionStatusValid(o.conversationRunOutcome.status) == false {
			o.conversationRunOutcome = &conversationRunOutcome{generation: generation, status: status, summary: strings.TrimSpace(summary), reason: strings.TrimSpace(reason)}
		}
	}
	o.mu.Unlock()
	o.emitMu.Unlock()
}

func generationRunCompletionStatusValid(status domaintask.Status) bool {
	_, ok := generationRunCompletionStatus(status)
	return ok
}

func (o *IdleChatOrchestrator) markConversationRunSucceeded(generation uint64, summary string) {
	o.setConversationRunOutcome(generation, domaintask.StatusSucceeded, summary, "")
}

func (o *IdleChatOrchestrator) markConversationRunFailed(generation uint64, summary, reason string) {
	o.setConversationRunOutcome(generation, domaintask.StatusFailed, summary, reason)
}

func (o *IdleChatOrchestrator) prepareConversationRunStopLocked(generation uint64, defaultStatus domaintask.Status, summary, reason string) (context.CancelFunc, bool, error) {
	if o.pendingConversationRun != nil && (generation == 0 || o.pendingConversationRun.generation == generation) {
		return nil, true, nil
	}
	if o.pendingConversationRun != nil && generation != 0 {
		return nil, false, nil
	}
	if generation != 0 && o.activeGeneration != generation {
		return nil, false, nil
	}
	cancel := o.runCancel
	o.runCancel = nil
	o.runCtx = o.ctx
	o.conversationRunStarting = false
	if o.activeThread != nil {
		o.activeThread.Close()
		o.activeThread = nil
	}
	o.activeTraceID = ""
	o.activeTraceSessionID = ""
	if o.activeTaskID == "" && o.activeRunID == "" {
		return cancel, false, nil
	}
	if o.activeTaskID == "" || o.activeRunID == "" {
		return cancel, false, errors.New("idlechat conversation run has incomplete owner identity")
	}
	if o.conversationRunOutcome != nil && o.conversationRunOutcome.generation == o.activeGeneration {
		if _, valid := generationRunCompletionStatus(o.conversationRunOutcome.status); valid {
			defaultStatus = o.conversationRunOutcome.status
			summary = o.conversationRunOutcome.summary
			reason = o.conversationRunOutcome.reason
		}
	}
	o.pendingConversationRun = &conversationRunFinalization{
		generation: o.activeGeneration,
		taskID:     o.activeTaskID,
		runID:      o.activeRunID,
		status:     defaultStatus,
		summary:    strings.TrimSpace(summary),
		reason:     strings.TrimSpace(reason),
	}
	return cancel, true, nil
}

func (o *IdleChatOrchestrator) stopConversationRun(generation uint64, defaultStatus domaintask.Status, summary, reason string) error {
	o.emitMu.Lock()
	o.mu.Lock()
	cancel, shouldFinalize, err := o.prepareConversationRunStopLocked(generation, defaultStatus, summary, reason)
	o.mu.Unlock()
	o.emitMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err != nil {
		return err
	}
	if !shouldFinalize {
		return nil
	}
	return o.finalizePendingConversationRun()
}

func (o *IdleChatOrchestrator) resetIdleSessionState(generation uint64) {
	if o == nil {
		return
	}
	o.emitMu.Lock()
	o.mu.Lock()
	if generation == 0 || o.activeGeneration == generation {
		o.chatActive = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.sessionContext = ""
		o.activeSessionID = ""
	}
	o.mu.Unlock()
	o.emitMu.Unlock()
}
