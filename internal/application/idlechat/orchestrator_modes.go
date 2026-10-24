package idlechat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// PlaybackStopRequest carries the foreground policy revision that caused an
// IdleChat playback stop. Surface presence remains the owner of these values;
// IdleChat only returns the applied revision in its receipt.
type PlaybackStopRequest struct {
	RuntimeEpoch  string
	StateRevision uint64
	Reason        string
}

// PlaybackStopReceipt reports the in-memory playback transition. Cleanup is
// tracked separately because Task/Run durability may finish after the caller
// has moved to the foreground surface.
type PlaybackStopReceipt struct {
	RuntimeEpoch         string
	AppliedStateRevision uint64
	Generation           uint64
	PlaybackStopped      bool
	ResumeBlocked        bool
	CleanupPending       bool
	GenerationQuiesced   bool
}

func (o *IdleChatOrchestrator) Start() {
	if err := o.finalizePendingConversationRun(); err != nil {
		log.Printf("[IdleChat] conversation run finalization retry during start failed: %v", err)
	}
	o.mu.Lock()
	newsSourceConfig := o.newsSourceConfig
	newsSourceConfig.RedditCommunities = append([]string(nil), newsSourceConfig.RedditCommunities...)
	newsSourceConfig.XQueries = append([]XNewsQuery(nil), newsSourceConfig.XQueries...)
	o.mu.Unlock()

	o.runNightPreparationScheduler()
	o.startDailySeedRefreshScheduler(newsSourceConfig)
	o.RequestNightPreparation("startup")

	o.wg.Add(1)
	go o.monitorLoop()
	log.Printf("[IdleChat] Started (participants=%v, interval=%s, maxTurns=%d)",
		o.participants, o.interval, o.maxTurns)
}

func (o *IdleChatOrchestrator) SetIntervalSeconds(seconds int) {
	if seconds < 1 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.interval = time.Duration(seconds) * time.Second
	o.intervalMin = (seconds + 59) / 60
}

// Stop はIdleChatを停止

func (o *IdleChatOrchestrator) Stop() {
	o.closeGenerationWorkAdmission()
	if scheduler := o.currentNightPreparationScheduler(); scheduler != nil {
		scheduler.Close()
	}
	o.cancel()
	o.conversationRunFinalizeWorkerMu.Lock()
	o.conversationRunFinalizeWorkerClosed = true
	finalizerDone := o.conversationRunFinalizeWorkerDone
	o.conversationRunFinalizeWorkerMu.Unlock()
	// Stop must wait for an in-flight owner admission to reconcile its exact
	// Task/Run pair before the Task owner is closed by runtime shutdown.
	o.conversationRunStartMu.Lock()
	o.cancelTopicProduction("stop")
	if err := o.cancelIdleRun(); err != nil {
		log.Printf("[IdleChat] conversation run finalization failed during stop: %v", err)
	}
	o.conversationRunStartMu.Unlock()
	o.wg.Wait()
	o.waitGenerationWork()
	o.finalizePendingGenerationRunsDuringStop()
	if finalizerDone != nil {
		<-finalizerDone
	}
	log.Println("[IdleChat] Stopped")
}

func (o *IdleChatOrchestrator) finalizePendingGenerationRunsDuringStop() {
	o.mu.Lock()
	dialogueService := o.dialogueEpisodeService
	o.mu.Unlock()
	cleanupCtx, cancel := o.conversationRunCleanupContext()
	defer cancel()
	if err := o.finalizePendingTopicRuns(cleanupCtx); err != nil {
		log.Printf("[IdleChat] topic generation completion retry failed during stop: %v", err)
	}
	if dialogueService != nil && cleanupCtx.Err() == nil {
		if err := dialogueService.FinalizePendingRuns(cleanupCtx); err != nil {
			log.Printf("[IdleChat] dialogue generation completion retry failed during stop: %v", err)
		}
	}
}

// NotifyActivity はタスク到着を通知（雑談セッションを中断）

func (o *IdleChatOrchestrator) NotifyActivity() {
	o.interruptForActivity("activity")
}

// SetChatBusy はChat(mio)の活性状態を更新する。

func (o *IdleChatOrchestrator) SetChatBusy(busy bool) {
	o.mu.Lock()
	o.chatBusy = busy
	o.mu.Unlock()
	if busy {
		o.interruptForActivity("chat_busy")
	}
	o.NotifyNightPreparationPolicyChanged()
}

// SetWorkerBusy はWorker(shiro/coder)の活性状態を更新する。

func (o *IdleChatOrchestrator) SetWorkerBusy(busy bool) {
	o.mu.Lock()
	o.workerBusy = busy
	o.mu.Unlock()
	if busy {
		o.interruptForActivity("worker_busy")
	}
	o.NotifyNightPreparationPolicyChanged()
}

// interruptForActivity ends the active conversation with a RAM-only state
// transition. Foreground request paths must not wait for timeline emission,
// TTS callbacks, Task persistence, or finalization.
func (o *IdleChatOrchestrator) interruptForActivity(reason string) {
	if o == nil {
		return
	}
	o.cancelTopicProduction(reason)
	o.mu.Lock()
	dailyEnrichmentJob := o.dailyEnrichmentJob
	wasActive := o.manualMode || o.chatActive || o.runCancel != nil || o.conversationRunStarting
	wasPending := o.pendingConversationRun != nil
	if !wasActive && !wasPending {
		o.clearIdleEventPublicationLocked()
		o.mu.Unlock()
		if dailyEnrichmentJob != nil {
			dailyEnrichmentJob.cancelBatch()
		}
		return
	}
	if !wasActive && wasPending {
		o.clearIdleEventPublicationLocked()
		o.mu.Unlock()
		if dailyEnrichmentJob != nil {
			dailyEnrichmentJob.cancelBatch()
		}
		o.schedulePendingConversationRunFinalization()
		return
	}
	cancel, shouldFinalize, finalizeErr := o.prepareConversationRunStopLocked(0, domaintask.StatusCancelled, "IdleChat conversation interrupted", reason)
	onInterrupt := o.onInterrupt
	interruptedRuntimeEpoch := o.runtimeEpoch
	interruptedPlaybackGeneration := o.activeGeneration
	if o.manualMode || o.chatActive {
		log.Printf("[IdleChat] Interrupted: reason=%s generation=%d session=%s", strings.TrimSpace(reason), o.activeGeneration, o.activeSessionID)
	}
	o.manualMode = false
	o.chatActive = false
	o.sessionMode = ""
	o.currentTopic = ""
	o.sessionContext = ""
	o.lastActivity = time.Now()
	if o.activeSessionID != "" {
		if o.interruptedSessions == nil {
			o.interruptedSessions = make(map[string]struct{})
		}
		o.interruptedSessions[o.activeSessionID] = struct{}{}
	}
	o.activeGeneration++
	o.clearIdleEventPublicationLocked()
	o.watchdogStage = "interrupted"
	o.watchdogDetail = strings.TrimSpace(reason)
	o.watchdogFrom = ""
	o.watchdogTo = ""
	o.watchdogMessageID = ""
	o.watchdogTurnIndex = 0
	o.watchdogUpdatedAt = time.Now().UTC()
	o.watchdogStageDeadlineAt = time.Time{}
	o.activeSessionID = ""
	if o.activeThread != nil {
		o.activeThread.Close()
		o.activeThread = nil
	}
	o.activeTraceID = ""
	o.activeTraceSessionID = ""
	cleanupPending := o.pendingConversationRun != nil || shouldFinalize || o.conversationRunStarting
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if dailyEnrichmentJob != nil {
		dailyEnrichmentJob.cancelBatch()
	}
	if onInterrupt != nil && wasActive {
		go onInterrupt(interruptedRuntimeEpoch, interruptedPlaybackGeneration)
	}
	if finalizeErr != nil {
		log.Printf("[IdleChat] conversation run finalization preparation failed: %v", finalizeErr)
	} else if cleanupPending {
		o.schedulePendingConversationRunFinalization()
	}
}

func (o *IdleChatOrchestrator) SetExternalLLMBusyFunc(fn func() bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.externalLLMBusy = fn
}

// StopPlaybackForForeground applies the foreground playback policy using only
// IdleChat RAM state. It deliberately avoids emitMu, event/TTS callbacks, and
// Task owner completion; pending Task/Run cleanup is handed to the managed
// finalization worker after the stop state is visible.
func (o *IdleChatOrchestrator) StopPlaybackForForeground(ctx context.Context, request PlaybackStopRequest) (PlaybackStopReceipt, error) {
	if o == nil {
		return PlaybackStopReceipt{}, errors.New("idlechat is not configured")
	}
	if ctx == nil {
		return PlaybackStopReceipt{}, errors.New("foreground stop context is nil")
	}
	if err := ctx.Err(); err != nil {
		return PlaybackStopReceipt{}, err
	}
	request.RuntimeEpoch = strings.TrimSpace(request.RuntimeEpoch)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.RuntimeEpoch == "" || request.StateRevision == 0 || request.Reason == "" {
		return PlaybackStopReceipt{}, errors.New("foreground stop epoch, state revision, and reason are required")
	}
	o.NotifyNightPreparationPolicyChanged()
	if receipt, done, err := o.foregroundStopReplay(request); done || err != nil {
		if done && err == nil && receipt.CleanupPending {
			o.schedulePendingConversationRunFinalization()
		}
		return receipt, err
	}

	// The existing disabled mode blocks automatic playback. We do not close
	// generation-work admission here because explicit prepared playback uses the
	// same lease gate and must remain available after a later policy revision.
	generationWorkQuiesced := o.generationWorkIsQuiesced()

	o.mu.Lock()
	if receipt, done, err := o.foregroundStopReplayLocked(request); done || err != nil {
		o.mu.Unlock()
		if done && err == nil && receipt.CleanupPending {
			o.schedulePendingConversationRunFinalization()
		}
		return receipt, err
	}
	wasActive := o.chatActive || o.manualMode || o.runCancel != nil || o.conversationRunStarting
	wasStarting := o.conversationRunStarting
	stoppedPlaybackGeneration := o.activeGeneration
	stoppedRuntimeEpoch := o.runtimeEpoch
	oldSessionID := o.activeSessionID
	cancel, shouldFinalize, finalizeErr := o.prepareConversationRunStopLocked(0, domaintask.StatusCancelled, "IdleChat playback stopped for foreground use", request.Reason)
	onInterrupt := o.onInterrupt
	dailyEnrichmentJob := o.dailyEnrichmentJob
	if oldSessionID != "" {
		if o.interruptedSessions == nil {
			o.interruptedSessions = make(map[string]struct{})
		}
		o.interruptedSessions[oldSessionID] = struct{}{}
	}
	o.disabled = true
	o.manualMode = false
	o.chatActive = false
	o.sessionMode = ""
	o.currentTopic = ""
	o.sessionContext = ""
	o.lastActivity = time.Now()
	o.activeGeneration++
	o.clearIdleEventPublicationLocked()
	o.activeSessionID = ""
	if o.activeThread != nil {
		o.activeThread.Close()
		o.activeThread = nil
	}
	o.activeTraceID = ""
	o.activeTraceSessionID = ""
	o.watchdogStage = "foreground_stop"
	o.watchdogDetail = request.Reason
	o.watchdogFrom = ""
	o.watchdogTo = ""
	o.watchdogMessageID = ""
	o.watchdogTurnIndex = 0
	o.watchdogUpdatedAt = time.Now().UTC()
	o.watchdogStageDeadlineAt = time.Time{}

	cleanupPending := o.pendingConversationRun != nil || shouldFinalize || wasStarting
	receipt := PlaybackStopReceipt{
		RuntimeEpoch:         request.RuntimeEpoch,
		AppliedStateRevision: request.StateRevision,
		Generation:           o.activeGeneration,
		PlaybackStopped:      wasActive,
		ResumeBlocked:        true,
		CleanupPending:       cleanupPending,
		GenerationQuiesced:   !wasActive && generationWorkQuiesced,
	}
	o.foregroundStopEpoch = request.RuntimeEpoch
	o.foregroundStopRevision = request.StateRevision
	o.foregroundStopReceipt = receipt
	o.mu.Unlock()
	// Publish the stop snapshot before cancellation and asynchronous cleanup can
	// enter owner persistence. The callback only projects RAM state and performs
	// nonblocking control fanout.
	o.notifyPlaybackControlSnapshotChanged()

	if cancel != nil {
		cancel()
	}
	o.cancelTopicProduction(request.Reason)
	if dailyEnrichmentJob != nil {
		dailyEnrichmentJob.cancelBatch()
	}
	if onInterrupt != nil && wasActive {
		go onInterrupt(stoppedRuntimeEpoch, stoppedPlaybackGeneration)
	}
	if finalizeErr != nil {
		log.Printf("[IdleChat] foreground stop finalization preparation failed: %v", finalizeErr)
	} else if cleanupPending {
		o.schedulePendingConversationRunFinalization()
	}
	return receipt, nil
}

func (o *IdleChatOrchestrator) foregroundStopReplay(request PlaybackStopRequest) (PlaybackStopReceipt, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.foregroundStopReplayLocked(request)
}

func (o *IdleChatOrchestrator) foregroundStopReplayLocked(request PlaybackStopRequest) (PlaybackStopReceipt, bool, error) {
	if o.foregroundStopEpoch != request.RuntimeEpoch {
		return PlaybackStopReceipt{}, false, nil
	}
	if request.StateRevision < o.foregroundStopRevision {
		return PlaybackStopReceipt{}, true, fmt.Errorf("foreground stop revision %d is older than applied revision %d", request.StateRevision, o.foregroundStopRevision)
	}
	if request.StateRevision == o.foregroundStopRevision && o.foregroundStopRevision != 0 {
		return o.foregroundStopReceipt, true, nil
	}
	return PlaybackStopReceipt{}, false, nil
}

// StartManualMode starts idle chat mode immediately.

func (o *IdleChatOrchestrator) StopManualMode() {
	o.closeGenerationWorkAdmission()
	o.stopAndDisable("manual_stop")
	o.waitGenerationWork()
	o.finalizePendingGenerationRunsDuringStop()
	o.reopenGenerationWorkAdmission()
}

func (o *IdleChatOrchestrator) Interrupt(reason string) {
	o.interruptLockedWithReason(reason)
}

func (o *IdleChatOrchestrator) interruptLockedWithReason(reason string) {
	o.cancelTopicProduction(reason)
	o.emitMu.Lock()
	o.mu.Lock()
	cancel := o.runCancel
	onInterrupt := o.onInterrupt
	interruptedRuntimeEpoch := o.runtimeEpoch
	interruptedPlaybackGeneration := o.activeGeneration
	dailyEnrichmentJob := o.dailyEnrichmentJob
	finalizeCancel, shouldFinalize, finalizeErr := o.prepareConversationRunStopLocked(0, domaintask.StatusCancelled, "IdleChat conversation interrupted", reason)
	if cancel == nil {
		cancel = finalizeCancel
	}
	if o.manualMode || o.chatActive {
		log.Printf("[IdleChat] Interrupted: reason=%s generation=%d session=%s", strings.TrimSpace(reason), o.activeGeneration, o.activeSessionID)
	}
	o.manualMode = false
	o.chatActive = false
	o.sessionMode = ""
	o.currentTopic = ""
	o.sessionContext = ""
	o.lastActivity = time.Now()
	if o.activeSessionID != "" {
		if o.interruptedSessions == nil {
			o.interruptedSessions = make(map[string]struct{})
		}
		o.interruptedSessions[o.activeSessionID] = struct{}{}
	}
	o.activeGeneration++
	o.clearIdleEventPublicationLocked()
	o.watchdogStage = "interrupted"
	o.watchdogDetail = strings.TrimSpace(reason)
	o.watchdogFrom = ""
	o.watchdogTo = ""
	o.watchdogMessageID = ""
	o.watchdogTurnIndex = 0
	o.watchdogUpdatedAt = time.Now().UTC()
	o.watchdogStageDeadlineAt = time.Time{}
	o.activeSessionID = ""
	if o.activeThread != nil {
		o.activeThread.Close()
		o.activeThread = nil
	}
	o.activeTraceID = ""
	o.activeTraceSessionID = ""
	o.mu.Unlock()
	o.emitMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if onInterrupt != nil {
		onInterrupt(interruptedRuntimeEpoch, interruptedPlaybackGeneration)
	}
	if dailyEnrichmentJob != nil {
		dailyEnrichmentJob.cancelBatch()
	}
	if finalizeErr != nil {
		log.Printf("[IdleChat] conversation run finalization preparation failed: %v", finalizeErr)
	} else if shouldFinalize {
		if err := o.finalizePendingConversationRun(); err != nil {
			log.Printf("[IdleChat] conversation run finalization failed after interrupt: %v", err)
		}
	}
}

func (o *IdleChatOrchestrator) stopAndDisable(reason string) {
	o.mu.Lock()
	o.disabled = true
	o.mu.Unlock()
	o.interruptLockedWithReason(reason)
}

func (o *IdleChatOrchestrator) beginIdleRunLocked() (uint64, error) {
	// Every conversation path eventually begins here. Foreground dialogue owns
	// CodexExe priority, so background Stock generation yields immediately and
	// keeps its durable checkpoint for the next idle refill.
	if o.pendingConversationRun != nil {
		return 0, errIdleChatConversationRunPending
	}
	if o.conversationRunStarting {
		return 0, errIdleChatConversationRunStarting
	}
	if o.runCancel != nil {
		return 0, errors.New("idlechat conversation run is already active")
	}
	if o.activeTaskID != "" || o.activeRunID != "" {
		return 0, errors.New("idlechat conversation run owner is not finalized")
	}
	o.cancelTopicProduction("conversation_start")
	o.activeGeneration++
	o.clearIdleEventPublicationLocked()
	if o.activeThread != nil {
		o.activeThread.Close()
		o.activeThread = nil
	}
	o.activeTraceID = modulecore.NewTraceID()
	o.activeTraceSessionID = ""
	o.runCtx, o.runCancel = context.WithCancel(o.ctx)
	o.conversationRunOutcome = nil
	o.conversationRunStarting = o.runIssuer != nil
	o.watchdogStage = "run_started"
	o.watchdogDetail = ""
	o.watchdogFrom = ""
	o.watchdogTo = ""
	o.watchdogMessageID = ""
	o.watchdogTurnIndex = 0
	o.watchdogUpdatedAt = time.Now().UTC()
	o.watchdogStageDeadlineAt = time.Time{}
	return o.activeGeneration, nil
}

func (o *IdleChatOrchestrator) cancelIdleRun() error {
	o.emitMu.Lock()
	o.mu.Lock()
	cancel, shouldFinalize, err := o.prepareConversationRunStopLocked(0, domaintask.StatusCancelled, "IdleChat conversation cancelled", "conversation cleanup")
	o.mu.Unlock()
	o.emitMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err != nil || !shouldFinalize {
		if err != nil {
			return err
		}
		return o.finalizePendingConversationRun()
	}
	return o.finalizePendingConversationRun()
}

func (o *IdleChatOrchestrator) cancelIdleRunIfGeneration(generation uint64) error {
	o.emitMu.Lock()
	o.mu.Lock()
	cancel, shouldFinalize, err := o.prepareConversationRunStopLocked(generation, domaintask.StatusCancelled, "IdleChat conversation cancelled", "conversation cleanup")
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

func (o *IdleChatOrchestrator) idleRunContext() context.Context {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.runCtx != nil {
		return llm.WithBusySource(o.runCtx, "idlechat")
	}
	return llm.WithBusySource(o.ctx, "idlechat")
}

func (o *IdleChatOrchestrator) activateIdleSession(sessionID string) (uint64, error) {
	generation, err := o.startIdleRun()
	if err != nil {
		return generation, err
	}
	o.emitMu.Lock()
	o.mu.Lock()
	if generation == 0 || o.activeGeneration != generation || o.conversationRunStarting {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return generation, errIdleChatConversationRunInvalidated
	}
	if err := o.bindIdleSessionLocked(sessionID); err != nil {
		o.mu.Unlock()
		o.emitMu.Unlock()
		bindErr := fmt.Errorf("bind idlechat session: %w", err)
		o.markConversationRunFailed(generation, "IdleChat session setup failed", bindErr.Error())
		if finalizeErr := o.cancelIdleRunIfGeneration(generation); finalizeErr != nil {
			bindErr = errors.Join(bindErr, finalizeErr)
		}
		return generation, bindErr
	}
	o.mu.Unlock()
	o.emitMu.Unlock()
	return generation, nil
}

func (o *IdleChatOrchestrator) activeThreadIdentityLocked(sessionID string) (modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, bool) {
	thread := o.activeThread
	if validateIdleChatSessionID(sessionID) != nil || o.activeSessionID != sessionID || thread == nil || thread.SessionID != sessionID || validateIdleChatSessionID(thread.SessionID) != nil {
		return "", 0, "", false
	}
	if thread.Status != domconv.ThreadActive || thread.Domain != "idlechat" || thread.ThreadKind != domconv.ThreadKindIdleChat || thread.ID.Validate() != nil || thread.ThreadSeq.Validate() != nil || thread.ThreadKind.Validate() != nil {
		return "", 0, "", false
	}
	return thread.ID, thread.ThreadSeq, thread.ThreadKind, true
}

func (o *IdleChatOrchestrator) ownerThreadIdentityLocked(sessionID string, generation uint64) (modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, bool) {
	if generation == 0 || generation != o.activeGeneration {
		return "", 0, "", false
	}
	threadID, threadSeq, threadKind, ok := o.activeThreadIdentityLocked(sessionID)
	if !ok || o.activeTraceSessionID != sessionID || o.activeTraceID.Validate() != nil {
		return "", 0, "", false
	}
	return threadID, threadSeq, threadKind, true
}

func (o *IdleChatOrchestrator) copyActiveThreadIdentityLocked(record *SessionSummary, sessionID string, generation uint64) bool {
	if record == nil {
		return false
	}
	if validateIdleChatSessionID(sessionID) != nil || record.SessionID != sessionID {
		return false
	}
	threadID, threadSeq, threadKind, ok := o.ownerThreadIdentityLocked(sessionID, generation)
	if !ok {
		return false
	}
	if (record.ThreadID != "" && record.ThreadID != threadID) ||
		(record.ThreadSeq != 0 && record.ThreadSeq != threadSeq) ||
		(record.ThreadKind != "" && record.ThreadKind != threadKind) {
		return false
	}
	record.ThreadID = threadID
	record.ThreadSeq = threadSeq
	record.ThreadKind = threadKind
	return true
}

func (o *IdleChatOrchestrator) allocateIdleThreadSeqLocked(sessionID string) (modulecore.ThreadSeq, error) {
	if o.topicThreadSeq == nil {
		o.topicThreadSeq = make(map[string]modulecore.ThreadSeq)
	}
	current := o.topicThreadSeq[sessionID]
	if current >= maxThreadSeq {
		return 0, fmt.Errorf("sequence overflow for session %q", sessionID)
	}
	next := current + 1
	o.topicThreadSeq[sessionID] = next
	return next, nil
}

func (o *IdleChatOrchestrator) openIdleThreadLocked(sessionID string) (*domconv.Thread, error) {
	if err := validateIdleChatSessionID(sessionID); err != nil {
		return nil, err
	}
	if o.topicStore != nil {
		return o.topicStore.OpenThread(sessionID)
	}
	threadSeq, err := o.allocateIdleThreadSeqLocked(sessionID)
	if err != nil {
		return nil, err
	}
	return domconv.NewThread(sessionID, "idlechat", domconv.ThreadKindIdleChat, threadSeq)
}

func (o *IdleChatOrchestrator) bindIdleSessionLocked(sessionID string) error {
	o.clearIdleEventPublicationLocked()
	if strings.TrimSpace(sessionID) == "" {
		if o.activeThread != nil {
			o.activeThread.Close()
			o.activeThread = nil
		}
		o.chatActive = false
		o.manualMode = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.activeSessionID = ""
		o.activeTraceSessionID = ""
		o.activeTraceID = ""
		o.messageIDs = make(map[string]string)
		return fmt.Errorf("idlechat session id is required")
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return fmt.Errorf("idlechat session id is invalid: %w", err)
	}
	if o.activeThread != nil {
		o.activeThread.Close()
		o.activeThread = nil
	}
	thread, err := o.openIdleThreadLocked(sessionID)
	if err != nil {
		o.chatActive = false
		o.manualMode = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.activeSessionID = ""
		o.activeTraceSessionID = ""
		o.activeTraceID = ""
		o.messageIDs = make(map[string]string)
		return fmt.Errorf("open idlechat thread: %w", err)
	}
	if thread == nil || thread.Domain != "idlechat" || thread.ThreadKind != domconv.ThreadKindIdleChat || thread.ID.Validate() != nil || thread.ThreadSeq.Validate() != nil || thread.ThreadKind.Validate() != nil || thread.Status != domconv.ThreadActive {
		o.chatActive = false
		o.manualMode = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.activeSessionID = ""
		o.activeTraceSessionID = ""
		o.activeTraceID = ""
		o.messageIDs = make(map[string]string)
		return fmt.Errorf("create idlechat thread: invalid thread identity")
	}
	o.activeThread = thread
	if o.activeTraceID.Validate() != nil || (o.activeTraceSessionID != "" && o.activeTraceSessionID != sessionID) {
		o.activeTraceID = modulecore.NewTraceID()
	}
	o.activeSessionID = sessionID
	o.activeTraceSessionID = sessionID
	if o.interruptedSessions != nil {
		// A prepared episode can legitimately replay the same SessionID after
		// an interruption. The new bind is the authoritative owner transition.
		delete(o.interruptedSessions, sessionID)
	}
	o.messageIDs = make(map[string]string)
	return nil
}

func (o *IdleChatOrchestrator) traceForSession(sessionID string, explicit modulecore.TraceID, generation uint64) (modulecore.TraceID, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || generation == 0 {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _, _, ok := o.ownerThreadIdentityLocked(sessionID, generation)
	if !ok {
		return "", false
	}
	if explicit == "" {
		return o.activeTraceID, true
	}
	if explicit.Validate() != nil || o.activeTraceID != explicit {
		return "", false
	}
	return explicit, true
}

func (o *IdleChatOrchestrator) ownsIdleSessionLocked(sessionID string, generation uint64) bool {
	_, _, _, ok := o.ownerThreadIdentityLocked(sessionID, generation)
	return ok
}

func (o *IdleChatOrchestrator) ownsIdleSession(sessionID string, generation uint64) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ownsIdleSessionLocked(sessionID, generation)
}

func (o *IdleChatOrchestrator) isIdleSessionActive(sessionID string, generation uint64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if generation != 0 && generation != o.activeGeneration {
		return false
	}
	if strings.TrimSpace(sessionID) != "" && o.activeSessionID != "" && strings.TrimSpace(sessionID) != o.activeSessionID {
		return false
	}
	return o.chatActive
}

func (o *IdleChatOrchestrator) isInterruptedSession(sessionID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.TrimSpace(sessionID) == "" || o.interruptedSessions == nil {
		return false
	}
	_, ok := o.interruptedSessions[strings.TrimSpace(sessionID)]
	return ok
}

// IsManualMode returns whether manual idle chat mode is enabled.

func idleChatPendingTopic(mode string) string {
	switch strings.TrimSpace(mode) {
	case "forecast":
		return "未来展望のお題を準備中"
	case "story", "story-simple":
		return "物語のお題を準備中"
	default:
		return "今日のお題を準備中"
	}
}

// GetHistory returns newest-first session summaries.

func (o *IdleChatOrchestrator) GetHistory(limit int) []SessionSummary {
	o.mu.Lock()
	store := o.topicStore
	if store != nil {
		o.mu.Unlock()
		return store.GetRecent(limit)
	}
	defer o.mu.Unlock()
	if limit <= 0 || limit > len(o.history) {
		limit = len(o.history)
	}
	out := make([]SessionSummary, 0, limit)
	for i := len(o.history) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, o.history[i])
	}
	return out
}

func (o *IdleChatOrchestrator) ActiveSessionTranscript(limit int) (string, []ActiveTranscriptEntry) {
	o.mu.Lock()
	sessionID := strings.TrimSpace(o.activeSessionID)
	memory := o.memory
	o.mu.Unlock()
	if sessionID == "" || memory == nil {
		return sessionID, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	entries := memory.GetUnifiedView(0)
	out := make([]ActiveTranscriptEntry, 0, limit)
	for _, entry := range entries {
		msg := entry.Message
		if strings.TrimSpace(msg.SessionID) != sessionID || msg.Type != domaintransport.MessageTypeIdleChat {
			continue
		}
		messageID, turnIndex := idleChatMessageMetadata(msg, len(out)+1)
		if messageID == "" {
			continue
		}
		timestamp := strings.TrimSpace(msg.Timestamp)
		if timestamp == "" && !entry.Timestamp.IsZero() {
			timestamp = entry.Timestamp.In(jst).Format(time.RFC3339)
		}
		out = append(out, ActiveTranscriptEntry{
			Type:      "idlechat.message",
			From:      msg.From,
			To:        msg.To,
			Content:   msg.Content,
			SessionID: sessionID,
			MessageID: messageID,
			TurnIndex: turnIndex,
			Timestamp: timestamp,
		})
		if len(out) >= limit {
			break
		}
	}
	return sessionID, out
}

func idleChatMessageMetadata(msg domaintransport.Message, fallbackIndex int) (string, int) {
	turnIndex := fallbackIndex
	if turnIndex < 1 {
		turnIndex = 1
	}
	if msg.Context != nil {
		switch v := msg.Context["turn_index"].(type) {
		case int:
			turnIndex = v
		case int64:
			turnIndex = int(v)
		case float64:
			turnIndex = int(v)
		}
		if turnIndex < 1 {
			turnIndex = fallbackIndex
			if turnIndex < 1 {
				turnIndex = 1
			}
		}
		if id, ok := msg.Context["message_id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id), turnIndex
		}
	}
	return "", turnIndex
}

func (o *IdleChatOrchestrator) getHistoricalTitleThemes(limit int) []string {
	history := o.GetHistory(limit)
	if len(history) == 0 {
		return nil
	}
	themes := make([]string, 0, len(history))
	seen := make(map[string]struct{}, len(history))
	for _, item := range history {
		theme := themeFromSummaryTitle(item.Title)
		if theme == "" {
			continue
		}
		key := normalizeLoopText(theme)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		themes = append(themes, theme)
	}
	return themes
}

func themeFromSummaryTitle(title string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, "日の"); idx >= 0 {
		s = strings.TrimSpace(s[idx+len("日の"):])
	}
	for _, suffix := range []string{"の話題まとめ", "のまとめ", "まとめ"} {
		s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
	}
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end >= 0 {
			s = strings.TrimSpace(s[end+1:])
		}
	}
	return normalizeIdleTopic(s, false)
}

func (o *IdleChatOrchestrator) StartManualMode() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.participants) < 2 {
		return fmt.Errorf("idlechat requires at least 2 participants")
	}
	o.disabled = false
	o.manualMode = true
	o.lastActivity = time.Now()
	log.Println("[IdleChat] Manual mode started")
	return nil
}

// StartForecastMode switches from manual idlechat into forecast mode immediately.

func (o *IdleChatOrchestrator) StartForecastMode() error {
	o.emitMu.Lock()
	o.mu.Lock()
	if len(o.participants) < 2 {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return fmt.Errorf("idlechat requires at least 2 participants")
	}
	if o.chatActive {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return fmt.Errorf("chat session already active")
	}
	o.disabled = false
	o.manualMode = false
	o.chatActive = true
	o.sessionMode = "forecast"
	o.currentTopic = idleChatPendingTopic("forecast")
	o.sessionContext = ""
	o.mu.Unlock()
	o.emitMu.Unlock()
	generation, err := o.startIdleRun()
	if err != nil {
		o.resetIdleSessionState(generation)
		return fmt.Errorf("start forecast conversation: %w", err)
	}
	o.mu.Lock()
	o.lastActivity = time.Now()
	o.mu.Unlock()
	log.Println("[Forecast] Forecast mode started")
	return nil
}

// StartStoryMode switches from manual idlechat into story mode immediately.

func (o *IdleChatOrchestrator) StartStoryMode() error {
	o.emitMu.Lock()
	o.mu.Lock()
	if len(o.participants) < 2 {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return fmt.Errorf("idlechat requires at least 2 participants")
	}
	if o.chatActive {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return fmt.Errorf("chat session already active")
	}
	o.disabled = false
	o.manualMode = false
	o.chatActive = true
	o.sessionMode = "story"
	o.currentTopic = idleChatPendingTopic("story")
	o.sessionContext = ""
	o.mu.Unlock()
	o.emitMu.Unlock()
	generation, err := o.startIdleRun()
	if err != nil {
		o.resetIdleSessionState(generation)
		return fmt.Errorf("start story conversation: %w", err)
	}
	o.mu.Lock()
	o.lastActivity = time.Now()
	o.mu.Unlock()
	log.Println("[Story] Story mode started")
	return nil
}

// StopManualMode stops idle chat mode and interrupts an ongoing session.

func (o *IdleChatOrchestrator) IsManualMode() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.manualMode
}

// IsChatActive は雑談セッションが進行中かを返す

func (o *IdleChatOrchestrator) IsChatActive() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.chatActive
}

func (o *IdleChatOrchestrator) IsDisabled() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.disabled
}

// CurrentMode returns the current idlechat/forecast mode.

func (o *IdleChatOrchestrator) CurrentMode() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.chatActive && o.sessionMode != "" {
		return o.sessionMode
	}
	if o.manualMode {
		return "manual"
	}
	return ""
}

// CurrentTopic は現在のIdleChatトピックを返す。

func (o *IdleChatOrchestrator) CurrentTopic() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.TrimSpace(o.currentTopic) == "" && o.chatActive {
		return idleChatPendingTopic(o.sessionMode)
	}
	return o.currentTopic
}
