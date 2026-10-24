package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	conversationTurnFollowerLease                = time.Minute
	conversationTurnRedisProjectionTimeout       = 5 * time.Second
	conversationTurnFollowerWorkTimeout          = 45 * time.Second
	conversationTurnOutboxBookkeepingReserve     = 10 * time.Second
	conversationTurnProjectionBookkeepingTimeout = 2 * time.Second
	conversationTurnMinimumWorkBudget            = 100 * time.Millisecond
)

// ConversationTurnTargets reports the followers owned by the advanced
// manager. Callers must not choose these targets; the engine obtains them from
// this capability before committing the L1 ledger row.
func (r *RealConversationManager) ConversationTurnTargets() []domconv.ConversationTurnTarget {
	if r == nil {
		return nil
	}
	return []domconv.ConversationTurnTarget{
		domconv.ConversationTurnTargetRedisProjection,
		domconv.ConversationTurnTargetThreadFollowers,
	}
}

// LoadActiveConversationThread returns the active typed projection. An
// explicitly admitted request sees its pending RAM head before L1 catches up.
// Redis is deliberately not consulted for canonical EndTurn boundary decisions.
func (r *RealConversationManager) LoadActiveConversationThread(ctx context.Context, sessionID string) (*domconv.Thread, error) {
	if r == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	if active, _ := r.conversationTurnOverlayForRead(ctx, sessionID); active != nil {
		return active, nil
	}
	if r == nil || r.l1Store == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	store, ok := r.l1Store.(conversationTurnL1Store)
	if !ok {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	events, err := store.LoadActiveConversationThreadProjection(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return conversationThreadFromL1Projection(events, domconv.ThreadActive)
}

// CommitConversationTurn is the canonical manager boundary for an EndTurn.
// L1 commits first and the active Redis projection is applied synchronously.
// Closed-thread followers remain durable but ineligible until the exact public
// final response has been appended to the canonical event store.
func (r *RealConversationManager) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	base := failedConversationTurnManagerResult(request, domconv.ConversationTurnErrorUnavailable)
	if r == nil || r.l1Store == nil {
		return base, domconv.ErrConversationTurnUnavailable
	}
	store, ok := r.l1Store.(conversationTurnL1Store)
	if !ok {
		return base, domconv.ErrConversationTurnUnavailable
	}
	normalized, err := domconv.NormalizeConversationTurnRequest(request)
	if err != nil {
		base.ErrorCode = domconv.ConversationTurnErrorInvalid
		return base, err
	}
	if admission, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		return r.acceptConversationTurn(ctx, store, admission, normalized)
	}
	r.turnOverlayMu.RLock()
	_, hasOverlay := r.turnOverlays[normalized.SessionID]
	r.turnOverlayMu.RUnlock()
	if hasOverlay {
		base.ErrorCode = domconv.ConversationTurnErrorConflict
		return base, domconv.ErrConversationTurnConflict
	}
	return r.commitConversationTurnSynchronously(ctx, store, normalized)
}

func (r *RealConversationManager) commitConversationTurnSynchronously(ctx context.Context, store conversationTurnL1Store, normalized domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	base := failedConversationTurnManagerResult(normalized, domconv.ConversationTurnErrorUnavailable)
	// The background recovery worker may replay legacy Redis projection rows.
	// Keep this acquisition cancellation-aware so a caller can leave while a
	// recovery pass owns the required projection boundary.
	if err := r.turnProjectionMu.Lock(ctx); err != nil {
		return base, err
	}
	defer r.turnProjectionMu.Unlock()
	result, err := store.CommitConversationTurn(ctx, normalized)
	if err != nil {
		return result, err
	}
	idempotentReplay := result.IdempotentReplay
	redisTarget := string(domconv.ConversationTurnTargetRedisProjection)
	if !conversationTurnTargetRequested(normalized.Targets, domconv.ConversationTurnTargetRedisProjection) || !conversationTurnResultHasTarget(result.PendingTargets, redisTarget) {
		return result, nil
	}
	redisStarted := time.Now().UTC()
	claimCtx, claimCancel := context.WithDeadline(ctx, redisStarted.Add(conversationTurnRedisProjectionTimeout))
	claim, claimErr := store.ClaimConversationTurnOutboxTarget(claimCtx, string(normalized.TurnID), redisTarget, redisStarted, conversationTurnFollowerLease)
	claimCancel()
	if claimErr != nil {
		return result, claimErr
	}
	if claim == nil {
		return result, domconv.ErrConversationTurnUnavailable
	}
	if claim.Target != redisTarget {
		return result, domconv.ErrConversationTurnConflict
	}
	workDeadline, hasWorkBudget := conversationTurnWorkDeadline(redisStarted, conversationTurnRedisProjectionTimeout, claim.LeaseExpiresAt, time.Now().UTC())
	var operationErr error
	if !hasWorkBudget {
		operationErr = domconv.ErrConversationTurnUnavailable
	} else if err := ctx.Err(); err != nil {
		operationErr = err
	} else {
		if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(workDeadline) {
			workDeadline = callerDeadline
		}
		if time.Until(workDeadline) < conversationTurnMinimumWorkBudget {
			operationErr = domconv.ErrConversationTurnUnavailable
		} else {
			workCtx, workCancel := context.WithDeadline(ctx, workDeadline)
			operationErr = r.applyConversationTurnOutbox(workCtx, claim)
			workCancel()
		}
	}
	finishedAt := time.Now().UTC()
	if operationErr != nil {
		code := conversationTurnFollowerErrorCode(operationErr)
		bookCtx, bookCancel := conversationTurnProjectionBookkeepingContext()
		updated, finishErr := store.FailConversationTurnOutbox(bookCtx, string(claim.TurnID), claim.Target, claim.LeaseToken, code, finishedAt)
		bookCancel()
		if finishErr != nil {
			return updated, finishErr
		}
		return updated, operationErr
	}
	bookCtx, bookCancel := conversationTurnProjectionBookkeepingContext()
	updated, finishErr := store.CompleteConversationTurnOutbox(bookCtx, string(claim.TurnID), claim.Target, claim.LeaseToken, finishedAt)
	bookCancel()
	if finishErr != nil {
		return updated, finishErr
	}
	result = updated
	result.IdempotentReplay = idempotentReplay
	if conversationTurnResultHasTarget(result.PendingTargets, redisTarget) {
		return result, domconv.ErrConversationTurnUnavailable
	}
	return result, nil
}

func (r *RealConversationManager) acceptConversationTurn(
	ctx context.Context,
	store conversationTurnL1Store,
	admission *persistencequeue.Admission,
	request domconv.ConversationTurnRequest,
) (domconv.ConversationTurnResult, error) {
	base := failedConversationTurnManagerResult(request, domconv.ConversationTurnErrorUnavailable)
	if !conversationTurnTargetRequested(request.Targets, domconv.ConversationTurnTargetRedisProjection) {
		base.ErrorCode = domconv.ConversationTurnErrorInvalid
		return base, domconv.ErrConversationTurnInvalid
	}
	request = cloneConversationTurnRequest(request)
	request.ThreadPlan = nil
	payloadSHA, err := domconv.ConversationTurnPayloadSHA256(request)
	if err != nil {
		base.ErrorCode = domconv.ConversationTurnErrorInvalid
		return base, err
	}

	r.turnOverlayMu.Lock()
	defer r.turnOverlayMu.Unlock()
	if r.turnOverlays == nil {
		r.turnOverlays = make(map[string]*conversationTurnOverlay)
	}
	previous, hadPrevious := r.turnOverlays[request.SessionID]
	if hadPrevious {
		if previous.ownerID != request.OwnerID {
			base.ErrorCode = domconv.ConversationTurnErrorConflict
			return base, domconv.ErrConversationTurnConflict
		}
		if previous.headFailure != nil {
			base.ErrorCode = domconv.ConversationTurnErrorUnavailable
			return base, domconv.ErrConversationTurnUnavailable
		}
		for _, pending := range previous.pending {
			if pending.request.TurnID != request.TurnID {
				continue
			}
			if pending.payloadSHA != payloadSHA {
				base.ErrorCode = domconv.ConversationTurnErrorConflict
				return base, domconv.ErrConversationTurnConflict
			}
			replay := cloneConversationTurnResult(pending.result)
			replay.IdempotentReplay = true
			return replay, nil
		}
	}

	var active *domconv.Thread
	if hadPrevious && previous.head != nil {
		active = cloneThreadForStore(previous.head)
	} else {
		events, loadErr := store.LoadActiveConversationThreadProjection(ctx, request.SessionID)
		if loadErr != nil && !errors.Is(loadErr, domconv.ErrThreadNotFound) {
			return base, loadErr
		}
		if loadErr == nil {
			active, loadErr = conversationThreadFromL1Projection(events, domconv.ThreadActive)
			if loadErr != nil {
				return base, loadErr
			}
		}
	}
	var activeState *domconv.ConversationTurnActiveThreadState
	if active != nil {
		activeState = &domconv.ConversationTurnActiveThreadState{
			Thread: domconv.ConversationTurnThreadIdentity{ID: active.ID, Seq: active.ThreadSeq, Kind: active.ThreadKind},
			Domain: active.Domain, MessageCount: int64(len(active.Turns)),
		}
	}
	plan, err := domconv.PlanConversationTurnThread(domconv.ConversationTurnThreadPlanInput{
		Active: activeState, Boundary: request.Boundary, Domain: request.Domain, NewThreadID: modulecore.NewThreadID(),
	})
	if err != nil {
		base.ErrorCode = domconv.ConversationTurnErrorInvalid
		return base, err
	}
	request.ThreadPlan = &domconv.ConversationTurnThreadPlanBinding{
		SessionID: request.SessionID, OwnerID: request.OwnerID, Plan: plan,
	}
	head, closed, err := acceptedConversationThread(request, plan, active)
	if err != nil {
		base.ErrorCode = domconv.ConversationTurnErrorInternal
		return base, err
	}
	accepted := acceptedConversationTurnResult(request, plan, payloadSHA)
	commandBytes, err := json.Marshal(request)
	if err != nil {
		base.ErrorCode = domconv.ConversationTurnErrorInternal
		return base, domconv.ErrConversationTurnInternal
	}
	commandRequest := cloneConversationTurnRequest(request)
	next := cloneConversationTurnOverlay(previous)
	if !hadPrevious {
		next = &conversationTurnOverlay{ownerID: request.OwnerID}
	}
	next.head = head
	if closed != nil {
		next.closed = closed
		next.closedByTurnID = string(request.TurnID)
	}
	next.pending = append(next.pending, acceptedConversationTurn{
		request: commandRequest, payloadSHA: payloadSHA, result: cloneConversationTurnResult(accepted),
	})
	r.turnOverlays[request.SessionID] = next
	_, err = admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerConversation,
		Kind:      "conversation_turn",
		SizeBytes: int64(len(commandBytes)),
		Apply: func(workerCtx context.Context) error {
			return r.applyAcceptedConversationTurn(workerCtx, commandRequest)
		},
	})
	if err != nil {
		if hadPrevious {
			r.turnOverlays[request.SessionID] = previous
		} else {
			delete(r.turnOverlays, request.SessionID)
		}
		base.ErrorCode = domconv.ConversationTurnErrorUnavailable
		return base, err
	}
	return accepted, nil
}

func (r *RealConversationManager) applyAcceptedConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) error {
	store, ok := r.l1Store.(conversationTurnL1Store)
	if !ok {
		r.failAcceptedConversationTurn(request.SessionID, request.TurnID, domconv.ErrConversationTurnUnavailable)
		return domconv.ErrConversationTurnUnavailable
	}
	result, err := r.commitConversationTurnSynchronously(ctx, store, request)
	if err == nil && (!conversationTurnResultHasTarget(result.CompletedTargets, string(domconv.ConversationTurnTargetRedisProjection)) ||
		conversationTurnResultHasTarget(result.PendingTargets, string(domconv.ConversationTurnTargetRedisProjection))) {
		err = domconv.ErrConversationTurnUnavailable
	}
	if err != nil {
		r.failAcceptedConversationTurn(request.SessionID, request.TurnID, err)
		return err
	}
	r.completeAcceptedConversationTurn(request.SessionID, request.TurnID)
	return nil
}

func acceptedConversationThread(
	request domconv.ConversationTurnRequest,
	plan domconv.ConversationTurnThreadPlan,
	active *domconv.Thread,
) (*domconv.Thread, *domconv.Thread, error) {
	var thread *domconv.Thread
	var closed *domconv.Thread
	if active != nil && active.ID == plan.Thread.ID {
		thread = cloneThreadForStore(active)
	} else {
		var err error
		thread, err = domconv.NewThread(request.SessionID, plan.Domain, plan.Thread.Kind, plan.Thread.Seq)
		if err != nil {
			return nil, nil, domconv.ErrConversationTurnInvalid
		}
		thread.ID = plan.Thread.ID
		if active != nil {
			if plan.ClosedThread.ID != active.ID || plan.ClosedThread.Seq != active.ThreadSeq || plan.ClosedThread.Kind != active.ThreadKind {
				return nil, nil, domconv.ErrConversationTurnConflict
			}
			closed = cloneThreadForStore(active)
			if err := closed.Close(); err != nil {
				return nil, nil, domconv.ErrConversationTurnInvalid
			}
		}
	}
	thread.Domain = plan.Domain
	now := time.Now().UTC()
	userMeta := conversationTurnOverlayMessageMeta(request, domconv.SpeakerUser)
	agentMeta := conversationTurnOverlayMessageMeta(request, request.AgentSpeaker)
	userMessage := domconv.NewMessage(domconv.SpeakerUser, request.UserMessage, userMeta)
	userMessage.Timestamp = now
	agentMessage := domconv.NewMessage(request.AgentSpeaker, request.AgentMessage, agentMeta)
	agentMessage.Timestamp = now.Add(time.Nanosecond)
	if err := thread.AddMessage(userMessage); err != nil {
		return nil, nil, domconv.ErrConversationTurnInvalid
	}
	if err := thread.AddMessage(agentMessage); err != nil || int64(len(thread.Turns)) != plan.MessageCount {
		return nil, nil, domconv.ErrConversationTurnInvalid
	}
	return thread, closed, nil
}

func conversationTurnOverlayMessageMeta(request domconv.ConversationTurnRequest, speaker domconv.Speaker) map[string]interface{} {
	messageID := request.UserMessageID
	from := string(speaker)
	to := string(request.AgentSpeaker)
	if speaker == request.AgentSpeaker {
		messageID = request.AgentMessageID
		to = string(domconv.SpeakerUser)
	}
	return map[string]interface{}{
		"domain": string(request.Domain), "message_id": string(messageID), "turn_id": string(request.TurnID),
		"speaker": string(speaker), "from": from, "to": to,
	}
}

func acceptedConversationTurnResult(request domconv.ConversationTurnRequest, plan domconv.ConversationTurnThreadPlan, payloadSHA string) domconv.ConversationTurnResult {
	requested := make([]string, 0, len(request.Targets))
	for _, target := range request.Targets {
		requested = append(requested, string(target))
	}
	result := domconv.ConversationTurnResult{
		TurnID: request.TurnID, TraceID: request.TraceID, RootTaskID: request.RootTaskID, SessionID: request.SessionID,
		ThreadID: plan.Thread.ID, ThreadSeq: plan.Thread.Seq, ThreadKind: plan.Thread.Kind,
		ClosedThreadID: plan.ClosedThread.ID, ClosedThreadSeq: plan.ClosedThread.Seq, ClosedThreadKind: plan.ClosedThread.Kind,
		UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
		MessageIDs: []string{string(request.UserMessageID), string(request.AgentMessageID)}, PayloadSHA256: payloadSHA,
		Status: domconv.ConversationTurnAccepted, RequestedTargets: requested, PendingTargets: append([]string(nil), requested...),
	}
	return result
}

func cloneConversationTurnRequest(request domconv.ConversationTurnRequest) domconv.ConversationTurnRequest {
	request.Targets = append([]domconv.ConversationTurnTarget(nil), request.Targets...)
	request.RecallTraceItems = append([]domconv.RecallTraceItem(nil), request.RecallTraceItems...)
	for index := range request.RecallTraceItems {
		request.RecallTraceItems[index].SourceURLs = append([]string(nil), request.RecallTraceItems[index].SourceURLs...)
	}
	if request.ThreadPlan != nil {
		plan := *request.ThreadPlan
		request.ThreadPlan = &plan
	}
	return request
}

func cloneConversationTurnResult(result domconv.ConversationTurnResult) domconv.ConversationTurnResult {
	result.MessageIDs = append([]string(nil), result.MessageIDs...)
	result.RequestedTargets = append([]string(nil), result.RequestedTargets...)
	result.PendingTargets = append([]string(nil), result.PendingTargets...)
	result.CompletedTargets = append([]string(nil), result.CompletedTargets...)
	return result
}

func cloneConversationTurnOverlay(overlay *conversationTurnOverlay) *conversationTurnOverlay {
	if overlay == nil {
		return nil
	}
	copyOverlay := *overlay
	copyOverlay.head = cloneThreadForStore(overlay.head)
	copyOverlay.closed = cloneThreadForStore(overlay.closed)
	copyOverlay.pending = make([]acceptedConversationTurn, len(overlay.pending))
	for index, pending := range overlay.pending {
		copyOverlay.pending[index] = acceptedConversationTurn{
			request: cloneConversationTurnRequest(pending.request), payloadSHA: pending.payloadSHA,
			result: cloneConversationTurnResult(pending.result),
		}
	}
	return &copyOverlay
}

func (r *RealConversationManager) conversationTurnOverlayForRead(ctx context.Context, sessionID string) (*domconv.Thread, *domconv.Thread) {
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); !admitted {
		return nil, nil
	}
	r.turnOverlayMu.RLock()
	overlay := r.turnOverlays[sessionID]
	if overlay == nil {
		r.turnOverlayMu.RUnlock()
		return nil, nil
	}
	head := cloneThreadForStore(overlay.head)
	closed := cloneThreadForStore(overlay.closed)
	r.turnOverlayMu.RUnlock()
	return head, closed
}

func (r *RealConversationManager) failAcceptedConversationTurn(sessionID string, turnID modulecore.TurnID, err error) {
	r.turnOverlayMu.Lock()
	defer r.turnOverlayMu.Unlock()
	if overlay := r.turnOverlays[sessionID]; overlay != nil {
		for _, pending := range overlay.pending {
			if pending.request.TurnID == turnID {
				overlay.headFailure = err
				return
			}
		}
	}
}

func (r *RealConversationManager) completeAcceptedConversationTurn(sessionID string, turnID modulecore.TurnID) {
	r.turnOverlayMu.Lock()
	defer r.turnOverlayMu.Unlock()
	overlay := r.turnOverlays[sessionID]
	if overlay == nil {
		return
	}
	for index, pending := range overlay.pending {
		if pending.request.TurnID != turnID {
			continue
		}
		overlay.pending = append(overlay.pending[:index], overlay.pending[index+1:]...)
		if overlay.closedByTurnID == string(turnID) {
			overlay.closed = nil
			overlay.closedByTurnID = ""
		}
		if len(overlay.pending) == 0 {
			delete(r.turnOverlays, sessionID)
		}
		return
	}
}

func conversationTurnWorkDeadline(started time.Time, timeout time.Duration, leaseExpiresAt, now time.Time) (time.Time, bool) {
	if started.IsZero() || timeout <= 0 || leaseExpiresAt.IsZero() || now.IsZero() {
		return time.Time{}, false
	}
	deadline := started.Add(timeout)
	leaseDeadline := leaseExpiresAt.Add(-conversationTurnOutboxBookkeepingReserve)
	if leaseDeadline.Before(deadline) {
		deadline = leaseDeadline
	}
	if deadline.Sub(now) < conversationTurnMinimumWorkBudget {
		return time.Time{}, false
	}
	return deadline, true
}

func conversationTurnTargetRequested(targets []domconv.ConversationTurnTarget, target domconv.ConversationTurnTarget) bool {
	for _, candidate := range targets {
		if candidate == target {
			return true
		}
	}
	return false
}

func conversationTurnResultHasTarget(targets []string, target string) bool {
	for _, candidate := range targets {
		if candidate == target {
			return true
		}
	}
	return false
}

// MarkConversationTurnResponsePublished records the exact public final event
// boundary in the selected L1 owner. The caller invokes this only after the
// canonical event append and normal event projections have succeeded.
func (r *RealConversationManager) MarkConversationTurnResponsePublished(ctx context.Context, turnID modulecore.TurnID, traceID modulecore.TraceID, sessionID modulecore.SessionID, at time.Time) error {
	if r == nil || r.l1Store == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	store, ok := r.l1Store.(conversationTurnL1Store)
	if !ok {
		return domconv.ErrConversationTurnUnavailable
	}
	return store.MarkConversationTurnResponsePublished(ctx, turnID, traceID, sessionID, at)
}

// ReconcileConversationTurnResponsePublications marks only exact public final
// envelopes already durable in the canonical EventStore. L1 reply text alone
// never opens the thread-follower gate.
func (r *RealConversationManager) ReconcileConversationTurnResponsePublications(ctx context.Context, events interface {
	HasPublicConversationFinalResponse(context.Context, modulecore.TraceID, modulecore.TurnID, modulecore.SessionID) (bool, error)
}, limit int) error {
	if r == nil || r.l1Store == nil || events == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	store, ok := r.l1Store.(conversationTurnL1Store)
	if !ok {
		return domconv.ErrConversationTurnUnavailable
	}
	if limit <= 0 || limit > 100 {
		return domconv.ErrConversationTurnInvalid
	}
	r.publicationReconcileMu.Lock()
	defer r.publicationReconcileMu.Unlock()
	candidates, err := store.ListUnpublishedConversationTurnThreadFollowersAfter(ctx, r.publicationCursor, limit)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		r.publicationCursor = ""
		return nil
	}
	var firstErr error
	for _, candidate := range candidates {
		r.publicationCursor = string(candidate.TurnID)
		published, err := events.HasPublicConversationFinalResponse(ctx, candidate.TraceID, candidate.TurnID, modulecore.SessionID(candidate.SessionID))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !published {
			continue
		}
		if err := store.MarkConversationTurnResponsePublished(ctx, candidate.TurnID, candidate.TraceID, modulecore.SessionID(candidate.SessionID), time.Now().UTC()); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func hasPublicConversationFinalResponse(events []modulecore.EventEnvelope, traceID modulecore.TraceID, turnID modulecore.TurnID, sessionID modulecore.SessionID) bool {
	for _, event := range events {
		if event.TraceID != traceID || event.TurnID != turnID || event.SessionID != sessionID || event.ComponentID != "orchestrator" || event.EventType != "agent.response" || event.Payload == nil {
			continue
		}
		to, ok := event.Payload["to"].(string)
		if ok && strings.EqualFold(strings.TrimSpace(to), "user") {
			return true
		}
	}
	return false
}

// DrainConversationTurnOutbox replays a bounded batch of all eligible targets.
// It alternates target ownership and excludes each claim for the rest of this
// pass so a failed remote claim cannot consume attempts repeatedly.
func (r *RealConversationManager) DrainConversationTurnOutbox(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	if r == nil || r.l1Store == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	store, ok := r.l1Store.(conversationTurnL1Store)
	if !ok {
		return domconv.ErrConversationTurnUnavailable
	}
	excluded := make(map[string]struct{}, limit)
	var firstFollowerErr error
	nextTargetIndex := 0
	targets := []string{string(domconv.ConversationTurnTargetRedisProjection), string(domconv.ConversationTurnTargetThreadFollowers)}
	for processed := 0; processed < limit; {
		var claim *domconv.ConversationTurnOutbox
		var claimErr error
		var projectionLocked bool
		var targetStarted time.Time
		var targetTimeout time.Duration
		for attempt := 0; attempt < len(targets); attempt++ {
			target := targets[nextTargetIndex%len(targets)]
			nextTargetIndex++
			if target == string(domconv.ConversationTurnTargetRedisProjection) {
				if !r.turnProjectionMu.TryLock() {
					continue
				}
				projectionLocked = true
				targetTimeout = conversationTurnRedisProjectionTimeout
			} else {
				targetTimeout = conversationTurnFollowerWorkTimeout
			}
			targetStarted = time.Now().UTC()
			claimCtx, claimCancel := context.WithDeadline(ctx, targetStarted.Add(targetTimeout))
			claim, claimErr = store.ClaimNextConversationTurnOutboxTargetExcluding(claimCtx, target, targetStarted, conversationTurnFollowerLease, excluded)
			claimCancel()
			if claimErr != nil || claim != nil {
				break
			}
			if projectionLocked {
				r.turnProjectionMu.Unlock()
				projectionLocked = false
			}
		}
		if claimErr != nil {
			if projectionLocked {
				r.turnProjectionMu.Unlock()
			}
			return claimErr
		}
		if claim == nil {
			break
		}
		processed++
		key := conversationTurnOutboxKey(claim.TurnID, claim.Target)
		if _, already := excluded[key]; already {
			if projectionLocked {
				r.turnProjectionMu.Unlock()
			}
			return domconv.ErrConversationTurnConflict
		}
		excluded[key] = struct{}{}
		workDeadline, hasWorkBudget := conversationTurnWorkDeadline(targetStarted, targetTimeout, claim.LeaseExpiresAt, time.Now().UTC())
		var operationErr error
		if !hasWorkBudget {
			operationErr = domconv.ErrConversationTurnUnavailable
		} else if err := ctx.Err(); err != nil {
			operationErr = err
		} else {
			if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(workDeadline) {
				workDeadline = callerDeadline
			}
			if time.Until(workDeadline) < conversationTurnMinimumWorkBudget {
				operationErr = domconv.ErrConversationTurnUnavailable
			} else {
				workCtx, workCancel := context.WithDeadline(ctx, workDeadline)
				operationErr = r.applyConversationTurnOutbox(workCtx, claim)
				workCancel()
			}
		}
		finishedAt := time.Now().UTC()
		bookCtx, bookCancel := conversationTurnBookkeepingContext()
		if projectionLocked {
			bookCancel()
			bookCtx, bookCancel = conversationTurnProjectionBookkeepingContext()
		}
		if operationErr != nil {
			code := conversationTurnFollowerErrorCode(operationErr)
			_, claimErr = store.FailConversationTurnOutbox(bookCtx, string(claim.TurnID), claim.Target, claim.LeaseToken, code, finishedAt)
			if claimErr == nil && firstFollowerErr == nil {
				firstFollowerErr = operationErr
			}
		} else {
			_, claimErr = store.CompleteConversationTurnOutbox(bookCtx, string(claim.TurnID), claim.Target, claim.LeaseToken, finishedAt)
		}
		bookCancel()
		if projectionLocked {
			r.turnProjectionMu.Unlock()
		}
		if claimErr != nil {
			return claimErr
		}
	}
	return firstFollowerErr
}

// conversationTurnBookkeepingContext returns a short-lived context detached
// from the caller. The outbox ledger update must succeed even when the
// follower's own context already expired; otherwise a timed-out follower can
// never be marked failed and remains leased until a later worker pass.
func conversationTurnBookkeepingContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// conversationTurnProjectionBookkeepingContext caps the time the manager can
// retain the Redis projection lock after Redis work has returned.
func conversationTurnProjectionBookkeepingContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), conversationTurnProjectionBookkeepingTimeout)
}

func (r *RealConversationManager) applyConversationTurnOutbox(ctx context.Context, outbox *domconv.ConversationTurnOutbox) error {
	if outbox == nil {
		return domconv.ErrConversationTurnInvalid
	}
	payload, err := decodeConversationTurnOutboxPayload(outbox)
	if err != nil {
		return err
	}
	switch domconv.ConversationTurnTarget(outbox.Target) {
	case domconv.ConversationTurnTargetRedisProjection:
		return r.applyRedisConversationProjection(ctx, outbox, payload.OwnerID)
	case domconv.ConversationTurnTargetThreadFollowers:
		return r.applyConversationThreadFollowers(ctx, outbox)
	default:
		return domconv.ErrConversationTurnInvalid
	}
}

type conversationTurnOutboxPayload struct {
	Version          string                `json:"version"`
	TurnID           modulecore.TurnID     `json:"turn_id"`
	TraceID          modulecore.TraceID    `json:"trace_id"`
	RootTaskID       modulecore.TaskID     `json:"root_task_id"`
	SessionID        string                `json:"session_id"`
	OwnerID          string                `json:"owner_id"`
	ThreadID         modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq        modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind       modulecore.ThreadKind `json:"thread_kind"`
	ClosedThreadID   modulecore.ThreadID   `json:"closed_thread_id,omitempty"`
	ClosedThreadSeq  modulecore.ThreadSeq  `json:"closed_thread_seq,omitempty"`
	ClosedThreadKind modulecore.ThreadKind `json:"closed_thread_kind,omitempty"`
	UserMessageID    modulecore.MessageID  `json:"user_message_id"`
	AgentMessageID   modulecore.MessageID  `json:"agent_message_id"`
	Target           string                `json:"target"`
	PayloadSHA256    string                `json:"payload_sha256"`
}

func decodeConversationTurnOutboxPayload(outbox *domconv.ConversationTurnOutbox) (conversationTurnOutboxPayload, error) {
	if outbox == nil || strings.TrimSpace(outbox.PayloadJSON) == "" {
		return conversationTurnOutboxPayload{}, domconv.ErrConversationTurnInvalid
	}
	var payload conversationTurnOutboxPayload
	decoder := json.NewDecoder(strings.NewReader(outbox.PayloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, domconv.ErrConversationTurnInvalid
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return payload, domconv.ErrConversationTurnInvalid
	}
	canonical, err := json.Marshal(payload)
	if err != nil || !bytes.Equal(canonical, []byte(outbox.PayloadJSON)) {
		return payload, domconv.ErrConversationTurnInvalid
	}
	if err := validateConversationThreadTuple(payload.ThreadID, payload.ThreadSeq, payload.ThreadKind, true); err != nil {
		return payload, err
	}
	if err := validateConversationThreadTuple(outbox.ThreadID, outbox.ThreadSeq, outbox.ThreadKind, true); err != nil {
		return payload, err
	}
	if err := validateConversationThreadTuple(payload.ClosedThreadID, payload.ClosedThreadSeq, payload.ClosedThreadKind, false); err != nil {
		return payload, err
	}
	if err := validateConversationThreadTuple(outbox.ClosedThreadID, outbox.ClosedThreadSeq, outbox.ClosedThreadKind, false); err != nil {
		return payload, err
	}
	if payload.TurnID.Validate() != nil || payload.TraceID.Validate() != nil || payload.RootTaskID.Validate() != nil || payload.UserMessageID.Validate() != nil || payload.AgentMessageID.Validate() != nil || payload.UserMessageID == payload.AgentMessageID ||
		outbox.TurnID.Validate() != nil || outbox.TraceID.Validate() != nil || outbox.RootTaskID.Validate() != nil {
		return payload, domconv.ErrConversationTurnInvalid
	}
	if payload.Version != "rencrow.conversation_turn_outbox.v2" || payload.TurnID != outbox.TurnID || payload.TraceID != outbox.TraceID || payload.RootTaskID != outbox.RootTaskID || payload.SessionID != outbox.SessionID || strings.TrimSpace(payload.OwnerID) == "" || payload.ThreadID != outbox.ThreadID || payload.ThreadSeq != outbox.ThreadSeq || payload.ThreadKind != outbox.ThreadKind || payload.Target != outbox.Target || payload.PayloadSHA256 != outbox.PayloadSHA256 {
		return payload, domconv.ErrConversationTurnInvalid
	}
	if payload.ClosedThreadID != outbox.ClosedThreadID || payload.ClosedThreadSeq != outbox.ClosedThreadSeq || payload.ClosedThreadKind != outbox.ClosedThreadKind {
		return payload, domconv.ErrConversationTurnInvalid
	}
	return payload, nil
}

func validateConversationThreadTuple(threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, required bool) error {
	if threadID == "" {
		if required || threadSeq != 0 || threadKind != "" {
			return domconv.ErrConversationTurnInvalid
		}
		return nil
	}
	if threadID.Validate() != nil || threadSeq.Validate() != nil || threadKind.Validate() != nil {
		return domconv.ErrConversationTurnInvalid
	}
	return nil
}

func (r *RealConversationManager) applyRedisConversationProjection(ctx context.Context, outbox *domconv.ConversationTurnOutbox, ownerID string) error {
	if r.redisStore == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	store, ok := r.l1Store.(conversationTurnL1Store)
	if !ok {
		return domconv.ErrConversationTurnUnavailable
	}
	events, err := store.LoadActiveConversationThreadProjection(ctx, outbox.SessionID)
	if err != nil {
		return err
	}
	thread, err := conversationThreadFromL1Projection(events, domconv.ThreadActive)
	if err != nil {
		return err
	}
	if thread.ID != outbox.ThreadID || thread.ThreadSeq != outbox.ThreadSeq || thread.ThreadKind != outbox.ThreadKind {
		return domconv.ErrConversationTurnInvalid
	}
	session, err := r.redisStore.GetSession(ctx, outbox.SessionID)
	if errors.Is(err, domconv.ErrSessionNotFound) {
		session = domconv.NewSessionConversation(outbox.SessionID, strings.TrimSpace(ownerID))
	} else if err != nil {
		return domconv.ErrConversationTurnUnavailable
	}
	if session == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	if strings.TrimSpace(ownerID) != "" {
		session.UserID = strings.TrimSpace(ownerID)
	}
	session.LastThreadID = thread.ID
	session.LastThreadSeq = thread.ThreadSeq
	session.LastThreadKind = thread.ThreadKind
	session.UpdatedAt = time.Now().UTC()
	if err := r.redisStore.SaveThread(ctx, thread); err != nil {
		return domconv.ErrConversationTurnUnavailable
	}
	if err := r.redisStore.SaveSession(ctx, session); err != nil {
		return domconv.ErrConversationTurnUnavailable
	}
	return nil
}

func (r *RealConversationManager) applyConversationThreadFollowers(ctx context.Context, outbox *domconv.ConversationTurnOutbox) error {
	if err := validateConversationThreadTuple(outbox.ClosedThreadID, outbox.ClosedThreadSeq, outbox.ClosedThreadKind, false); err != nil {
		return err
	}
	if outbox.ClosedThreadID == "" || r.redisStore == nil || r.archiveStore == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	store, ok := r.l1Store.(conversationTurnL1Store)
	if !ok {
		return domconv.ErrConversationTurnUnavailable
	}
	archive, ok := r.archiveStore.(conversationThreadArchiveStore)
	if !ok {
		return domconv.ErrConversationTurnUnavailable
	}
	events, err := store.LoadConversationThreadProjection(ctx, outbox.SessionID, outbox.ClosedThreadID)
	if err != nil {
		return err
	}
	thread, err := conversationThreadFromL1Projection(events, domconv.ThreadClosed)
	if err != nil {
		return err
	}
	if thread.ID != outbox.ClosedThreadID || thread.ThreadSeq != outbox.ClosedThreadSeq || thread.ThreadKind != outbox.ClosedThreadKind {
		return domconv.ErrConversationTurnInvalid
	}
	summary, err := archive.GetThreadSummary(ctx, outbox.ClosedThreadID)
	if err != nil && !errors.Is(err, domconv.ErrThreadNotFound) {
		return domconv.ErrConversationTurnUnavailable
	}
	if err != nil || summary == nil {
		residual, generationMode, failureCode := r.generateSummaryResidual(ctx, thread)
		var embedding []float32
		if r.embedder != nil {
			embedding, err = r.embedder.Embed(ctx, residual.Summary)
			if err != nil {
				return domconv.ErrConversationTurnUnavailable
			}
		}
		summary, err = r.persistConversationThreadFollowerSummary(ctx, thread, residual, generationMode, failureCode, embedding)
		if err != nil {
			return err
		}
	}
	if err := validateConversationThreadFollowerSummary(summary, thread); err != nil {
		return err
	}
	if len(summary.Embedding) > 0 {
		if r.vectordbStore == nil {
			return domconv.ErrConversationTurnUnavailable
		}
		if err := r.vectordbStore.SaveThreadSummary(ctx, summary); err != nil {
			return domconv.ErrConversationTurnUnavailable
		}
	}
	redisCtx, redisCancel := context.WithTimeout(ctx, conversationTurnRedisProjectionTimeout)
	defer redisCancel()
	if err := r.redisStore.DeleteThread(redisCtx, thread.ID); err != nil {
		return domconv.ErrConversationTurnUnavailable
	}
	return nil
}

func (r *RealConversationManager) persistConversationThreadFollowerSummary(ctx context.Context, thread *domconv.Thread, residual domconv.SummaryResidual, generationMode, failureCode string, embedding []float32) (*domconv.ThreadSummary, error) {
	if r.archiveStore == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	roles, evidenceSHA256, err := deriveThreadSummaryEvidence(thread)
	if err != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	archiveAt, err := stableThreadSummaryTime(thread)
	if err != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	receipt := &domconv.ThreadSummaryReceipt{
		SchemaVersion:  domconv.ThreadSummaryReceiptSchemaVersion,
		GenerationMode: generationMode,
		Provider:       strings.TrimSpace(residual.Provider), FailureCode: failureCode,
		EvidenceSHA256: evidenceSHA256, SourceTurnCount: len(thread.Turns), Roles: append([]string(nil), roles...), CreatedAt: archiveAt.UTC(),
	}
	if err := receipt.ValidateForWrite(); err != nil || domconv.ValidateSummaryResidual(residual) != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	summary := &domconv.ThreadSummary{
		ThreadID: thread.ID, ThreadSeq: thread.ThreadSeq, ThreadKind: thread.ThreadKind,
		SessionID: thread.SessionID, Domain: thread.Domain,
		Summary: residual.Summary, Keywords: append([]string(nil), residual.Keywords...), Roles: append([]string(nil), roles...), Receipt: receipt,
		Embedding: append([]float32(nil), embedding...), StartTime: thread.StartTime, EndTime: archiveAt.UTC(),
	}
	if err := r.archiveStore.SaveThreadSummaryWithReceipt(ctx, summary, receipt); err != nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	return summary, nil
}

func validateConversationThreadFollowerSummary(summary *domconv.ThreadSummary, thread *domconv.Thread) error {
	if summary == nil || thread == nil || summary.ThreadID != thread.ID || summary.ThreadSeq != thread.ThreadSeq || summary.ThreadKind != thread.ThreadKind || summary.SessionID != thread.SessionID || summary.Receipt == nil {
		return domconv.ErrConversationTurnInvalid
	}
	roles, evidence, err := deriveThreadSummaryEvidence(thread)
	if err != nil || summary.Domain != thread.Domain || summary.Receipt.ValidateForWrite() != nil || summary.Receipt.EvidenceSHA256 != evidence || summary.Receipt.SourceTurnCount != len(thread.Turns) || !sameStringSlice(summary.Receipt.Roles, roles) || !sameStringSlice(summary.Roles, roles) {
		return domconv.ErrConversationTurnInvalid
	}
	return nil
}

func conversationThreadFromL1Projection(events []l1sqlite.L1MemoryEvent, status domconv.ThreadStatus) (*domconv.Thread, error) {
	if len(events) < 2 || len(events) > 12 || len(events)%2 != 0 {
		return nil, domconv.ErrConversationTurnInvalid
	}
	if status != domconv.ThreadActive && status != domconv.ThreadClosed {
		return nil, domconv.ErrConversationTurnInvalid
	}
	thread := &domconv.Thread{ID: events[0].ThreadID, ThreadSeq: events[0].ThreadSeq, ThreadKind: events[0].ThreadKind, SessionID: events[0].SessionID, Domain: stringMeta(events[0].Meta, "domain"), Turns: make([]domconv.Message, 0, len(events)), Targets: []string{}, Cooldown: map[string]int{}, StartTime: events[0].CreatedAt.UTC(), Status: status}
	if thread.ID.Validate() != nil || thread.ThreadSeq.Validate() != nil || thread.ThreadKind.Validate() != nil || modulecore.SessionID(thread.SessionID).Validate() != nil || thread.Domain == "" {
		return nil, domconv.ErrConversationTurnInvalid
	}
	for _, event := range events {
		if event.ThreadID != thread.ID || event.ThreadSeq != thread.ThreadSeq || event.ThreadKind != thread.ThreadKind || event.SessionID != thread.SessionID {
			return nil, domconv.ErrConversationTurnInvalid
		}
		thread.Turns = append(thread.Turns, domconv.Message{Speaker: event.Speaker, Msg: event.Message, Timestamp: event.CreatedAt.UTC(), Meta: cloneConversationTurnMeta(event.Meta)})
		if event.CreatedAt.Before(thread.StartTime) {
			thread.StartTime = event.CreatedAt.UTC()
		}
	}
	end := thread.Turns[len(thread.Turns)-1].Timestamp
	if status == domconv.ThreadClosed {
		thread.EndTime = &end
	}
	return thread, nil
}

func cloneConversationTurnMeta(meta map[string]interface{}) map[string]interface{} {
	if meta == nil {
		return nil
	}
	copyMeta := make(map[string]interface{}, len(meta))
	for key, value := range meta {
		copyMeta[key] = value
	}
	return copyMeta
}

func stringMeta(meta map[string]interface{}, key string) string {
	if meta == nil {
		return ""
	}
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func conversationTurnOutboxKey(turnID modulecore.TurnID, target string) string {
	return string(turnID) + "\x00" + target
}

func conversationTurnFollowerErrorCode(err error) domconv.ConversationTurnErrorCode {
	switch {
	case errors.Is(err, domconv.ErrConversationTurnInvalid):
		return domconv.ConversationTurnErrorInvalid
	case errors.Is(err, domconv.ErrConversationTurnConflict):
		return domconv.ConversationTurnErrorConflict
	case errors.Is(err, domconv.ErrConversationTurnInternal):
		return domconv.ConversationTurnErrorInternal
	default:
		return domconv.ConversationTurnErrorUnavailable
	}
}

func failedConversationTurnManagerResult(request domconv.ConversationTurnRequest, code domconv.ConversationTurnErrorCode) domconv.ConversationTurnResult {
	return domconv.ConversationTurnResult{
		TurnID: request.TurnID, TraceID: request.TraceID, RootTaskID: request.RootTaskID,
		UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
		MessageIDs: []string{string(request.UserMessageID), string(request.AgentMessageID)},
		SessionID:  strings.TrimSpace(request.SessionID), Status: domconv.ConversationTurnFailed, ErrorCode: code,
	}
}
