package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

const conversationTurnFollowerLease = time.Minute

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

// LoadActiveConversationThread returns the authoritative active projection
// from L1. Redis is deliberately not consulted for canonical EndTurn
// boundary decisions.
func (r *RealConversationManager) LoadActiveConversationThread(ctx context.Context, sessionID string) (*domconv.Thread, error) {
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

type conversationTurnOutboxExcludingClaimer interface {
	ClaimConversationTurnOutboxExcluding(context.Context, string, time.Time, time.Duration, map[string]struct{}) (*domconv.ConversationTurnOutbox, error)
	ClaimNextConversationTurnOutboxExcluding(context.Context, time.Time, time.Duration, map[string]struct{}) (*domconv.ConversationTurnOutbox, error)
}

// CommitConversationTurn is the canonical manager boundary for an EndTurn.
// L1 commits first; requested followers are then processed through their
// durable outbox rows and never through the legacy Store path.
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
	result, err := store.CommitConversationTurn(ctx, normalized)
	if err != nil {
		return result, err
	}
	idempotentReplay := result.IdempotentReplay
	if len(normalized.Targets) == 0 {
		return result, nil
	}

	excluded := make(map[string]struct{}, len(normalized.Targets))
	var firstFollowerErr error
	for range normalized.Targets {
		claim, claimErr := claimTurnOutbox(ctx, store, normalized.TurnID, time.Now().UTC(), excluded)
		if claimErr != nil {
			return result, claimErr
		}
		if claim == nil {
			break
		}
		key := conversationTurnOutboxKey(claim.TurnID, claim.Target)
		excluded[key] = struct{}{}
		operationErr := r.applyConversationTurnOutbox(ctx, claim)
		finishedAt := time.Now().UTC()
		if operationErr != nil {
			code := conversationTurnFollowerErrorCode(operationErr)
			updated, finishErr := store.FailConversationTurnOutbox(ctx, claim.TurnID, claim.Target, claim.LeaseToken, code, finishedAt)
			if finishErr != nil {
				return updated, finishErr
			}
			result = updated
			if firstFollowerErr == nil {
				firstFollowerErr = operationErr
			}
			continue
		}
		updated, finishErr := store.CompleteConversationTurnOutbox(ctx, claim.TurnID, claim.Target, claim.LeaseToken, finishedAt)
		if finishErr != nil {
			return updated, finishErr
		}
		result = updated
	}
	if receipt, receiptErr := store.GetConversationTurnReceipt(ctx, normalized.TurnID); receiptErr == nil {
		result = receipt
		result.IdempotentReplay = idempotentReplay
	} else if firstFollowerErr == nil {
		return result, receiptErr
	}
	if firstFollowerErr != nil {
		return result, firstFollowerErr
	}
	return result, nil
}

// DrainConversationTurnOutbox replays a bounded global startup batch. Each
// claimed key is excluded for the rest of this call, so a failure advances to
// the next target instead of immediately retrying the same failed row.
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
	for processed := 0; processed < limit; processed++ {
		claim, err := claimNextTurnOutbox(ctx, store, time.Now().UTC(), excluded)
		if err != nil {
			return err
		}
		if claim == nil {
			break
		}
		key := conversationTurnOutboxKey(claim.TurnID, claim.Target)
		if _, already := excluded[key]; already {
			// Custom stores without the exclusion extension may still return a
			// duplicate. It is left leased and will be recovered by the next
			// startup call rather than processed twice here.
			break
		}
		excluded[key] = struct{}{}
		operationErr := r.applyConversationTurnOutbox(ctx, claim)
		finishedAt := time.Now().UTC()
		if operationErr != nil {
			code := conversationTurnFollowerErrorCode(operationErr)
			_, finishErr := store.FailConversationTurnOutbox(ctx, claim.TurnID, claim.Target, claim.LeaseToken, code, finishedAt)
			if finishErr != nil {
				return finishErr
			}
			if firstFollowerErr == nil {
				firstFollowerErr = operationErr
			}
			continue
		}
		if _, err := store.CompleteConversationTurnOutbox(ctx, claim.TurnID, claim.Target, claim.LeaseToken, finishedAt); err != nil {
			return err
		}
	}
	return firstFollowerErr
}

func claimTurnOutbox(ctx context.Context, store conversationTurnL1Store, turnID string, now time.Time, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	if claimer, ok := store.(conversationTurnOutboxExcludingClaimer); ok {
		return claimer.ClaimConversationTurnOutboxExcluding(ctx, turnID, now, conversationTurnFollowerLease, excluded)
	}
	return store.ClaimConversationTurnOutbox(ctx, turnID, now, conversationTurnFollowerLease)
}

func claimNextTurnOutbox(ctx context.Context, store conversationTurnL1Store, now time.Time, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	if claimer, ok := store.(conversationTurnOutboxExcludingClaimer); ok {
		return claimer.ClaimNextConversationTurnOutboxExcluding(ctx, now, conversationTurnFollowerLease, excluded)
	}
	return store.ClaimNextConversationTurnOutbox(ctx, now, conversationTurnFollowerLease)
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
	Version        string `json:"version"`
	TurnID         string `json:"turn_id"`
	TraceID        string `json:"trace_id"`
	SessionID      string `json:"session_id"`
	OwnerID        string `json:"owner_id"`
	ThreadID       int64  `json:"thread_id"`
	ClosedThreadID int64  `json:"closed_thread_id,omitempty"`
	UserMessageID  string `json:"user_message_id"`
	AgentMessageID string `json:"agent_message_id"`
	Target         string `json:"target"`
	PayloadSHA256  string `json:"payload_sha256"`
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
	if payload.Version != "rencrow.conversation_turn_outbox.v1" || payload.TurnID != outbox.TurnID || payload.TraceID != payload.TurnID || payload.SessionID != outbox.SessionID || strings.TrimSpace(payload.OwnerID) == "" || payload.ThreadID != outbox.ThreadID || payload.Target != outbox.Target || payload.PayloadSHA256 != outbox.PayloadSHA256 || payload.UserMessageID == "" || payload.AgentMessageID == "" {
		return payload, domconv.ErrConversationTurnInvalid
	}
	if payload.ClosedThreadID != outbox.ClosedThreadID {
		return payload, domconv.ErrConversationTurnInvalid
	}
	return payload, nil
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
	if outbox.ClosedThreadID <= 0 || r.redisStore == nil || r.archiveStore == nil {
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
	if err := r.redisStore.DeleteThread(ctx, thread.ID); err != nil {
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
		ThreadID: thread.ID, SessionID: thread.SessionID, Domain: thread.Domain,
		Summary: residual.Summary, Keywords: append([]string(nil), residual.Keywords...), Roles: append([]string(nil), roles...), Receipt: receipt,
		Embedding: append([]float32(nil), embedding...), StartTime: thread.StartTime, EndTime: archiveAt.UTC(),
	}
	if err := r.archiveStore.SaveThreadSummaryWithReceipt(ctx, summary, receipt); err != nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	return summary, nil
}

func validateConversationThreadFollowerSummary(summary *domconv.ThreadSummary, thread *domconv.Thread) error {
	if summary == nil || thread == nil || summary.ThreadID != thread.ID || summary.SessionID != thread.SessionID || summary.Receipt == nil {
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
	thread := &domconv.Thread{ID: events[0].ThreadID, SessionID: events[0].SessionID, Domain: stringMeta(events[0].Meta, "domain"), Turns: make([]domconv.Message, 0, len(events)), Targets: []string{}, Cooldown: map[string]int{}, StartTime: events[0].CreatedAt.UTC(), Status: status}
	if thread.ID <= 0 || thread.SessionID == "" || thread.Domain == "" {
		return nil, domconv.ErrConversationTurnInvalid
	}
	for _, event := range events {
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

func conversationTurnOutboxKey(turnID, target string) string {
	return turnID + "\x00" + target
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
	result := domconv.ConversationTurnResult{TurnID: strings.TrimSpace(request.TurnID), TraceID: strings.TrimSpace(request.TurnID), SessionID: strings.TrimSpace(request.SessionID), Status: domconv.ConversationTurnFailed, ErrorCode: code}
	if userID, agentID, err := domconv.ConversationTurnMessageIDs(result.TurnID); err == nil {
		result.UserMessageID, result.AgentMessageID = userID, agentID
		result.MessageIDs = []string{userID, agentID}
	}
	return result
}
