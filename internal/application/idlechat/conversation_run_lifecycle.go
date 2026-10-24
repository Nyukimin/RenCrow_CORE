package idlechat

import (
	"context"
	"errors"
	"log"
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
	generation         uint64
	taskID             modulecore.TaskID
	runID              modulecore.RunID
	status             domaintask.Status
	summary            string
	reason             string
	completionTicket   uint64
	completionAdmitted bool
}

type conversationRunCompletionAdmissionOwner interface {
	RunCompletionAdmissionAvailable() bool
	AdmitRunCompletion(context.Context, modulecore.TaskID, modulecore.RunID, domaintask.Status, string, string) (uint64, error)
	WaitRunCompletion(context.Context, uint64, modulecore.TaskID, modulecore.RunID, domaintask.Status) error
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
	o.notifyPlaybackControlSnapshotChanged()
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
		o.notifyPlaybackControlSnapshotChanged()
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
	o.clearIdleEventPublicationLocked()
	cancel := o.runCancel
	o.runCancel = nil
	o.runCtx = o.ctx
	o.activeTraceID = ""
	o.activeTraceSessionID = ""
	o.mu.Unlock()
	o.emitMu.Unlock()
	o.notifyPlaybackControlSnapshotChanged()
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
	ctx, cancel := o.conversationRunCleanupContext()
	defer cancel()
	return o.finalizePendingConversationRunWithContext(ctx)
}

func (o *IdleChatOrchestrator) finalizePendingConversationRunWithContext(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("idlechat conversation run finalization context is required")
	}
	o.conversationRunFinalizeMu.Lock()
	cleanupCompleted := false
	defer func() {
		o.conversationRunFinalizeMu.Unlock()
		if cleanupCompleted {
			o.notifyPlaybackControlSnapshotChanged()
		}
	}()

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

	err := completePendingGenerationRun(ctx, issuer, pending, func(ticket uint64) error {
		o.mu.Lock()
		defer o.mu.Unlock()
		current := o.pendingConversationRun
		if current == nil || current.generation != pending.generation || current.taskID != pending.taskID || current.runID != pending.runID {
			return errors.New("idlechat conversation run changed before completion ticket was retained")
		}
		if current.completionAdmitted && current.completionTicket != ticket {
			return errors.New("idlechat conversation run already retains a different completion ticket")
		}
		current.completionTicket = ticket
		current.completionAdmitted = true
		return nil
	})

	o.emitMu.Lock()
	o.mu.Lock()
	if current := o.pendingConversationRun; current != nil &&
		current.generation == pending.generation && current.taskID == pending.taskID && current.runID == pending.runID {
		if err == nil {
			o.pendingConversationRun = nil
			cleanupCompleted = true
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

// completePendingGenerationRun uses RAM admission only when the canonical Task
// owner confirms it is bound to the shared queue. Queue admission and its
// durable wait both happen on the managed finalizer goroutine, never in the
// foreground stop request. Other Task owner configurations retain the
// existing synchronous CompleteRun contract on that same background worker.
func completePendingGenerationRun(ctx context.Context, issuer idlechatRunIssuer, pending conversationRunFinalization, retainTicket func(uint64) error) error {
	queueOwner, ok := issuer.(conversationRunCompletionAdmissionOwner)
	if pending.completionAdmitted {
		if !ok {
			return errors.New("admitted idlechat completion ticket owner is unavailable")
		}
		if !queueOwner.RunCompletionAdmissionAvailable() {
			return errors.New("admitted idlechat completion ticket queue is unavailable")
		}
		return queueOwner.WaitRunCompletion(ctx, pending.completionTicket, pending.taskID, pending.runID, pending.status)
	}
	if !ok || !queueOwner.RunCompletionAdmissionAvailable() {
		return completeGenerationRun(ctx, issuer, pending.taskID, pending.runID, pending.status, pending.summary, pending.reason)
	}
	if err := validateGenerationRunInputs(ctx, issuer, pending.taskID, pending.runID); err != nil {
		return err
	}
	if pending.status == domaintask.StatusWaiting && strings.TrimSpace(pending.reason) == "" {
		return errors.New("waiting reason is required")
	}
	run, err := inspectGenerationRun(ctx, issuer, pending.taskID, pending.runID)
	if err != nil {
		return err
	}
	if run.Status != domaintask.RunStatusRunning {
		owner, err := generationRunOwnerFromIssuer(issuer)
		if err != nil {
			return err
		}
		return owner.VerifyRunCompletion(ctx, pending.taskID, pending.runID, run.Assignee, pending.status)
	}
	ticket, err := queueOwner.AdmitRunCompletion(ctx, pending.taskID, pending.runID, pending.status, pending.summary, pending.reason)
	if err != nil {
		return err
	}
	if retainTicket == nil {
		return errors.New("idlechat completion ticket retention is required")
	}
	if err := retainTicket(ticket); err != nil {
		return err
	}
	return queueOwner.WaitRunCompletion(ctx, ticket, pending.taskID, pending.runID, pending.status)
}

// schedulePendingConversationRunFinalization owns at most one asynchronous
// cleanup attempt at a time. A failed attempt leaves the pending pair intact
// for the existing retry/recovery paths.
func (o *IdleChatOrchestrator) schedulePendingConversationRunFinalization() {
	if o == nil {
		return
	}
	o.conversationRunFinalizeWorkerMu.Lock()
	if o.conversationRunFinalizeWorkerClosed || o.conversationRunFinalizeWorkerDone != nil {
		o.conversationRunFinalizeWorkerMu.Unlock()
		return
	}
	o.mu.Lock()
	pending := o.pendingConversationRun != nil
	o.mu.Unlock()
	if !pending {
		o.conversationRunFinalizeWorkerMu.Unlock()
		return
	}
	done := make(chan struct{})
	o.conversationRunFinalizeWorkerDone = done
	o.conversationRunFinalizeWorkerMu.Unlock()
	go func() {
		ctx, cancel := o.conversationRunCleanupContext()
		defer cancel()
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			err = o.finalizePendingConversationRunWithContext(ctx)
			if err == nil || ctx.Err() != nil {
				break
			}
			if attempt < 2 {
				delay := time.Duration(attempt+1) * 20 * time.Millisecond
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
				case <-timer.C:
				}
			}
		}
		if err != nil {
			log.Printf("[IdleChat] background conversation run finalization remains pending after bounded retries: %v", err)
		}
		o.conversationRunFinalizeWorkerMu.Lock()
		if o.conversationRunFinalizeWorkerDone == done {
			o.conversationRunFinalizeWorkerDone = nil
		}
		close(done)
		o.conversationRunFinalizeWorkerMu.Unlock()
	}()
}

func (o *IdleChatOrchestrator) conversationRunFinalizationWorkerDone() <-chan struct{} {
	if o == nil {
		return nil
	}
	o.conversationRunFinalizeWorkerMu.Lock()
	defer o.conversationRunFinalizeWorkerMu.Unlock()
	return o.conversationRunFinalizeWorkerDone
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
		o.clearIdleEventPublicationLocked()
		return nil, true, nil
	}
	if o.pendingConversationRun != nil && generation != 0 {
		return nil, false, nil
	}
	if generation != 0 && o.activeGeneration != generation {
		return nil, false, nil
	}
	o.clearIdleEventPublicationLocked()
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
	o.notifyPlaybackControlSnapshotChanged()
	return o.finalizePendingConversationRun()
}

func (o *IdleChatOrchestrator) resetIdleSessionState(generation uint64) {
	if o == nil {
		return
	}
	o.emitMu.Lock()
	o.mu.Lock()
	if generation == 0 || o.activeGeneration == generation {
		o.clearIdleEventPublicationLocked()
		o.chatActive = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.sessionContext = ""
		o.activeSessionID = ""
	}
	o.mu.Unlock()
	o.emitMu.Unlock()
	o.notifyPlaybackControlSnapshotChanged()
}
