package storagehost

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	GroupTurn = "turn"

	turnMaxPayloadBytes       = 8 << 20
	turnMaxLeaseTokenBytes    = 256
	turnMaxOutboxPayloadBytes = 64 << 10
	turnMaxProjectionEvents   = 12
	turnErrorCodeInternal     = "conversation_turn_internal"
	turnErrorCodeThreadAbsent = "thread_not_found"
)

// TurnGroupOwner is the only durable owner surface exposed by this group.
// L1SQLiteStore remains authoritative for turn state and transaction rules.
type TurnGroupOwner interface {
	CommitConversationTurn(context.Context, domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error)
	GetConversationTurnReceipt(context.Context, string) (domconv.ConversationTurnResult, error)
	ClaimConversationTurnOutbox(context.Context, string, time.Time, time.Duration) (*domconv.ConversationTurnOutbox, error)
	ClaimNextConversationTurnOutbox(context.Context, time.Time, time.Duration) (*domconv.ConversationTurnOutbox, error)
	CompleteConversationTurnOutbox(context.Context, string, string, string, time.Time) (domconv.ConversationTurnResult, error)
	FailConversationTurnOutbox(context.Context, string, string, string, domconv.ConversationTurnErrorCode, time.Time) (domconv.ConversationTurnResult, error)
	LoadConversationThreadProjection(context.Context, string, modulecore.ThreadID) ([]l1sqlite.L1MemoryEvent, error)
	LoadActiveConversationThreadProjection(context.Context, string) ([]l1sqlite.L1MemoryEvent, error)
}

// TurnGroupRecoveryOwner adds the narrow durable owner operations needed to
// reconcile claims and finishes after the protocol journal's commit gap.
type TurnGroupRecoveryOwner interface {
	TurnGroupOwner
	ClaimConversationTurnOutboxForOperation(context.Context, l1sqlite.ConversationTurnOperationIdentity, string, time.Time, time.Duration) (*domconv.ConversationTurnOutbox, error)
	ClaimNextConversationTurnOutboxForOperation(context.Context, l1sqlite.ConversationTurnOperationIdentity, time.Time, time.Duration) (*domconv.ConversationTurnOutbox, error)
	CompleteConversationTurnOutboxForOperation(context.Context, l1sqlite.ConversationTurnOperationIdentity, string, string, string, time.Time) (domconv.ConversationTurnResult, error)
	FailConversationTurnOutboxForOperation(context.Context, l1sqlite.ConversationTurnOperationIdentity, string, string, string, domconv.ConversationTurnErrorCode, time.Time) (domconv.ConversationTurnResult, error)
	GetConversationTurnClaimOperationReceipt(context.Context, l1sqlite.ConversationTurnOperationIdentity) (*domconv.ConversationTurnOutbox, bool, error)
	GetConversationTurnFinishOperationReceipt(context.Context, l1sqlite.ConversationTurnOperationIdentity, string, string, string) (domconv.ConversationTurnResult, bool, error)
}

type turnCommitPayload struct {
	Request turnRequestDTO `json:"request"`
}

type turnRequestDTO struct {
	TurnID           modulecore.TurnID                `json:"turn_id"`
	TraceID          modulecore.TraceID               `json:"trace_id"`
	RootTaskID       modulecore.TaskID                `json:"root_task_id"`
	UserMessageID    modulecore.MessageID             `json:"user_message_id"`
	AgentMessageID   modulecore.MessageID             `json:"agent_message_id"`
	SessionID        string                           `json:"session_id"`
	OwnerID          string                           `json:"owner_id"`
	Domain           string                           `json:"domain"`
	UserMessage      string                           `json:"user_message"`
	AgentMessage     string                           `json:"agent_message"`
	AgentSpeaker     domconv.Speaker                  `json:"agent_speaker"`
	RecallTraceItems []turnRecallTraceDTO             `json:"recall_trace_items,omitempty"`
	Boundary         bool                             `json:"boundary"`
	BoundaryReason   string                           `json:"boundary_reason,omitempty"`
	Targets          []domconv.ConversationTurnTarget `json:"targets,omitempty"`
}

type turnRecallTraceDTO struct {
	Layer         string    `json:"layer"`
	Kind          string    `json:"kind"`
	MemoryID      string    `json:"memory_id"`
	SourceID      string    `json:"source_id"`
	SourceType    string    `json:"source_type"`
	Summary       string    `json:"summary"`
	Query         string    `json:"query"`
	Provider      string    `json:"provider"`
	SourceURLs    []string  `json:"source_urls,omitempty"`
	RetrievedAt   time.Time `json:"retrieved_at,omitempty"`
	Score         float32   `json:"score"`
	Decision      string    `json:"decision"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason"`
	MemoryState   string    `json:"memory_state"`
	Sensitivity   string    `json:"sensitivity"`
	PromptSection string    `json:"prompt_section"`
	TokenCount    int       `json:"token_count"`
	PromptIndex   int       `json:"prompt_index"`
}

type turnReceiptPayload struct {
	TurnID string `json:"turn_id"`
}

type turnClaimPayload struct {
	TurnID          string        `json:"turn_id"`
	Now             time.Time     `json:"now"`
	LeaseDurationNS time.Duration `json:"lease_duration_ns"`
}

type turnClaimNextPayload struct {
	Now             time.Time     `json:"now"`
	LeaseDurationNS time.Duration `json:"lease_duration_ns"`
}

type turnFinishPayload struct {
	TurnID     string    `json:"turn_id"`
	Target     string    `json:"target"`
	LeaseToken string    `json:"lease_token"`
	Now        time.Time `json:"now"`
}

type turnFailPayload struct {
	TurnID     string                            `json:"turn_id"`
	Target     string                            `json:"target"`
	LeaseToken string                            `json:"lease_token"`
	Code       domconv.ConversationTurnErrorCode `json:"code"`
	Now        time.Time                         `json:"now"`
}

type turnProjectionPayload struct {
	SessionID string              `json:"session_id"`
	ThreadID  modulecore.ThreadID `json:"thread_id"`
}

type turnActiveProjectionPayload struct {
	SessionID string `json:"session_id"`
}

type turnResultPayload struct {
	Result domconv.ConversationTurnResult `json:"result"`
}

type turnOutboxResultPayload struct {
	Outbox *turnOutboxDTO `json:"outbox,omitempty"`
}

type turnOutboxDTO struct {
	TurnID           modulecore.TurnID                    `json:"turn_id"`
	TraceID          modulecore.TraceID                   `json:"trace_id"`
	RootTaskID       modulecore.TaskID                    `json:"root_task_id"`
	Target           string                               `json:"target"`
	SessionID        string                               `json:"session_id"`
	ThreadID         modulecore.ThreadID                  `json:"thread_id"`
	ThreadSeq        domconv.ThreadSeq                    `json:"thread_seq"`
	ThreadKind       domconv.ThreadKind                   `json:"thread_kind"`
	ClosedThreadID   modulecore.ThreadID                  `json:"closed_thread_id,omitempty"`
	ClosedThreadSeq  domconv.ThreadSeq                    `json:"closed_thread_seq,omitempty"`
	ClosedThreadKind domconv.ThreadKind                   `json:"closed_thread_kind,omitempty"`
	PayloadSHA256    string                               `json:"payload_sha256"`
	PayloadJSON      string                               `json:"payload_json"`
	Status           domconv.ConversationTurnOutboxStatus `json:"status"`
	LeaseToken       string                               `json:"lease_token"`
	LeaseExpiresAt   time.Time                            `json:"lease_expires_at,omitempty"`
	Attempts         int                                  `json:"attempts"`
	LastError        domconv.ConversationTurnErrorCode    `json:"last_error,omitempty"`
	CreatedAt        time.Time                            `json:"created_at"`
	UpdatedAt        time.Time                            `json:"updated_at"`
}

type turnProjectionResultPayload struct {
	Events []turnProjectionEventDTO `json:"events"`
}

type turnProjectionEventDTO struct {
	ID          string                `json:"id"`
	Namespace   string                `json:"namespace"`
	SessionID   string                `json:"session_id"`
	ThreadID    modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq   modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind  modulecore.ThreadKind `json:"thread_kind"`
	Speaker     domconv.Speaker       `json:"speaker"`
	Message     string                `json:"message"`
	Meta        turnProjectionMetaDTO `json:"meta"`
	MemoryState string                `json:"memory_state"`
	Layer       string                `json:"layer"`
	Source      string                `json:"source"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

type turnProjectionMetaDTO struct {
	Domain    string `json:"domain"`
	MessageID string `json:"message_id"`
	TurnID    string `json:"turn_id"`
	Speaker   string `json:"speaker"`
	From      string `json:"from"`
	To        string `json:"to"`
}

// RegisterTurnGroup exposes the fixed conversation turn contract. Every
// mutation is owner-recoverable; only deterministic pre-owner validation can
// release an op_id without an owner receipt.
func RegisterTurnGroup(h *Handler, store TurnGroupOwner) error {
	if h == nil || store == nil {
		return errors.New("storagehost: turn group needs a handler and an owner store")
	}
	recoveryOwner, ok := store.(TurnGroupRecoveryOwner)
	if !ok {
		return errors.New("storagehost: turn group owner lacks durable operation receipts")
	}

	if err := h.RegisterRecoverable(GroupTurn, "commit", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		request, err := decodeTurnCommitRequest(mutation.Payload)
		if err != nil {
			return nil, rejectTurnPayload("commit", true)
		}
		result, err := store.CommitConversationTurn(ctx, request)
		if err != nil || !turnReceiptMatchesRequest(result, request) {
			return nil, turnMutationUnavailable()
		}
		return turnResultPayload{Result: result}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		request, err := decodeTurnCommitRequest(mutation.Payload)
		if err != nil {
			return ConfirmedNotCommitted(), nil
		}
		result, err := store.GetConversationTurnReceipt(ctx, string(request.TurnID))
		if err != nil || !turnReceiptMatchesRequest(result, request) {
			return UnknownOutcome(), nil
		}
		return Committed(turnResultPayload{Result: result}), nil
	}); err != nil {
		return err
	}

	if err := h.Register(GroupTurn, "receipt", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload turnReceiptPayload
		if decodeTurnPayload(raw, &payload) != nil || !validTurnID(payload.TurnID) {
			return nil, rejectTurnPayload("receipt", false)
		}
		result, err := store.GetConversationTurnReceipt(ctx, payload.TurnID)
		if err != nil {
			return nil, turnReadError(err)
		}
		if !validTurnResult(result) || result.TurnID != modulecore.TurnID(payload.TurnID) {
			return nil, NewError(ErrorCodeStoreUnavailable, "conversation turn owner returned an invalid receipt")
		}
		return turnResultPayload{Result: result}, nil
	}); err != nil {
		return err
	}

	if err := h.RegisterRecoverable(GroupTurn, "outbox_claim", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeTurnClaimPayload(mutation.Payload)
		if err != nil {
			return nil, rejectTurnPayload("outbox_claim", true)
		}
		identity := turnOperationIdentity(mutation, "outbox_claim")
		outbox, err := recoveryOwner.ClaimConversationTurnOutboxForOperation(ctx, identity, payload.TurnID, payload.Now, payload.LeaseDurationNS)
		if err != nil {
			return nil, turnMutationUnavailable()
		}
		if outbox != nil && !validClaimedTurnOutbox(outbox) {
			return nil, turnMutationUnavailable()
		}
		return turnOutboxResultPayload{Outbox: turnOutboxToDTO(outbox)}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		if _, err := decodeTurnClaimPayload(mutation.Payload); err != nil {
			return ConfirmedNotCommitted(), nil
		}
		outbox, found, err := recoveryOwner.GetConversationTurnClaimOperationReceipt(ctx, turnOperationIdentity(mutation, "outbox_claim"))
		if err != nil || !found || (outbox != nil && !validClaimedTurnOutbox(outbox)) {
			return UnknownOutcome(), nil
		}
		return Committed(turnOutboxResultPayload{Outbox: turnOutboxToDTO(outbox)}), nil
	}); err != nil {
		return err
	}

	if err := h.RegisterRecoverable(GroupTurn, "outbox_claim_next", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeTurnClaimNextPayload(mutation.Payload)
		if err != nil {
			return nil, rejectTurnPayload("outbox_claim_next", true)
		}
		identity := turnOperationIdentity(mutation, "outbox_claim_next")
		outbox, err := recoveryOwner.ClaimNextConversationTurnOutboxForOperation(ctx, identity, payload.Now, payload.LeaseDurationNS)
		if err != nil {
			return nil, turnMutationUnavailable()
		}
		if outbox != nil && !validClaimedTurnOutbox(outbox) {
			return nil, turnMutationUnavailable()
		}
		return turnOutboxResultPayload{Outbox: turnOutboxToDTO(outbox)}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		if _, err := decodeTurnClaimNextPayload(mutation.Payload); err != nil {
			return ConfirmedNotCommitted(), nil
		}
		outbox, found, err := recoveryOwner.GetConversationTurnClaimOperationReceipt(ctx, turnOperationIdentity(mutation, "outbox_claim_next"))
		if err != nil || !found || (outbox != nil && !validClaimedTurnOutbox(outbox)) {
			return UnknownOutcome(), nil
		}
		return Committed(turnOutboxResultPayload{Outbox: turnOutboxToDTO(outbox)}), nil
	}); err != nil {
		return err
	}

	if err := h.RegisterRecoverable(GroupTurn, "complete", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeTurnFinishPayload(mutation.Payload)
		if err != nil {
			return nil, rejectTurnPayload("complete", true)
		}
		identity := turnOperationIdentity(mutation, "complete")
		result, err := recoveryOwner.CompleteConversationTurnOutboxForOperation(ctx, identity, payload.TurnID, payload.Target, payload.LeaseToken, payload.Now)
		if err != nil || !validTurnResult(result) || result.TurnID != modulecore.TurnID(payload.TurnID) {
			return nil, turnMutationUnavailable()
		}
		return turnResultPayload{Result: result}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeTurnFinishPayload(mutation.Payload)
		if err != nil {
			return ConfirmedNotCommitted(), nil
		}
		result, found, err := recoveryOwner.GetConversationTurnFinishOperationReceipt(ctx, turnOperationIdentity(mutation, "complete"), payload.TurnID, payload.Target, payload.LeaseToken)
		if err != nil || !found || !validTurnResult(result) || result.TurnID != modulecore.TurnID(payload.TurnID) {
			return UnknownOutcome(), nil
		}
		return Committed(turnResultPayload{Result: result}), nil
	}); err != nil {
		return err
	}

	if err := h.RegisterRecoverable(GroupTurn, "fail", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeTurnFailPayload(mutation.Payload)
		if err != nil {
			return nil, rejectTurnPayload("fail", true)
		}
		identity := turnOperationIdentity(mutation, "fail")
		result, err := recoveryOwner.FailConversationTurnOutboxForOperation(ctx, identity, payload.TurnID, payload.Target, payload.LeaseToken, payload.Code, payload.Now)
		if err != nil || !validTurnResult(result) || result.TurnID != modulecore.TurnID(payload.TurnID) {
			return nil, turnMutationUnavailable()
		}
		return turnResultPayload{Result: result}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeTurnFailPayload(mutation.Payload)
		if err != nil {
			return ConfirmedNotCommitted(), nil
		}
		result, found, err := recoveryOwner.GetConversationTurnFinishOperationReceipt(ctx, turnOperationIdentity(mutation, "fail"), payload.TurnID, payload.Target, payload.LeaseToken)
		if err != nil || !found || !validTurnResult(result) || result.TurnID != modulecore.TurnID(payload.TurnID) {
			return UnknownOutcome(), nil
		}
		return Committed(turnResultPayload{Result: result}), nil
	}); err != nil {
		return err
	}

	if err := h.Register(GroupTurn, "projection", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload turnProjectionPayload
		if decodeTurnPayload(raw, &payload) != nil || !validTurnSessionID(payload.SessionID) || payload.ThreadID.Validate() != nil {
			return nil, rejectTurnPayload("projection", false)
		}
		events, err := store.LoadConversationThreadProjection(ctx, payload.SessionID, payload.ThreadID)
		if err != nil {
			return nil, turnReadError(err)
		}
		projected, err := turnProjectionEventsToDTO(events)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "conversation turn owner returned an invalid projection")
		}
		return turnProjectionResultPayload{Events: projected}, nil
	}); err != nil {
		return err
	}

	return h.Register(GroupTurn, "active_projection", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload turnActiveProjectionPayload
		if decodeTurnPayload(raw, &payload) != nil || !validTurnSessionID(payload.SessionID) {
			return nil, rejectTurnPayload("active_projection", false)
		}
		events, err := store.LoadActiveConversationThreadProjection(ctx, payload.SessionID)
		if err != nil {
			return nil, turnReadError(err)
		}
		projected, err := turnProjectionEventsToDTO(events)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "conversation turn owner returned an invalid active projection")
		}
		return turnProjectionResultPayload{Events: projected}, nil
	})
}

// TurnStoreClient implements the existing conversation turn L1 contract over
// the storage host. Mutating calls use Client.Call so op_id and uncertainty
// behavior stay owned by the protocol client.
type TurnStoreClient struct{ client *Client }

var (
	_ TurnGroupOwner         = (*l1sqlite.L1SQLiteStore)(nil)
	_ TurnGroupRecoveryOwner = (*l1sqlite.L1SQLiteStore)(nil)
	_ TurnGroupOwner         = (*TurnStoreClient)(nil)
)

func NewTurnStoreClient(c *Client) *TurnStoreClient { return &TurnStoreClient{client: c} }

func (s *TurnStoreClient) call(ctx context.Context, operation string, payload, result any) error {
	if s == nil || s.client == nil {
		return NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if err := s.client.Call(ctx, GroupTurn, operation, payload, result); err != nil {
		return mapTurnClientError(err)
	}
	return nil
}

func (s *TurnStoreClient) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	normalized, err := domconv.NormalizeConversationTurnRequest(request)
	if err != nil || !validTurnRequestTimes(normalized) {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInvalid
	}
	var output turnResultPayload
	if err := s.call(ctx, "commit", turnCommitPayload{Request: turnRequestToDTO(normalized)}, &output); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	if !validTurnResult(output.Result) || !turnResultMatchesRequest(output.Result, normalized) {
		return domconv.ConversationTurnResult{}, NewError(ErrorCodeOutcomeUnknown, "conversation turn commit result is invalid")
	}
	return output.Result, nil
}

func (s *TurnStoreClient) GetConversationTurnReceipt(ctx context.Context, turnID string) (domconv.ConversationTurnResult, error) {
	if !validTurnID(turnID) {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInvalid
	}
	var output turnResultPayload
	if err := s.call(ctx, "receipt", turnReceiptPayload{TurnID: turnID}, &output); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	if !validTurnResult(output.Result) || output.Result.TurnID != modulecore.TurnID(turnID) {
		return domconv.ConversationTurnResult{}, NewError(ErrorCodeStoreUnavailable, "conversation turn receipt result is invalid")
	}
	return output.Result, nil
}

func (s *TurnStoreClient) ClaimConversationTurnOutbox(ctx context.Context, turnID string, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	if !validTurnID(turnID) || !validTurnTime(now) || !validTurnLease(leaseDuration) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	var output turnOutboxResultPayload
	if err := s.call(ctx, "outbox_claim", turnClaimPayload{TurnID: turnID, Now: now, LeaseDurationNS: leaseDuration}, &output); err != nil {
		return nil, err
	}
	return turnOutboxFromDTO(output.Outbox)
}

func (s *TurnStoreClient) ClaimNextConversationTurnOutbox(ctx context.Context, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	if !validTurnTime(now) || !validTurnLease(leaseDuration) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	var output turnOutboxResultPayload
	if err := s.call(ctx, "outbox_claim_next", turnClaimNextPayload{Now: now, LeaseDurationNS: leaseDuration}, &output); err != nil {
		return nil, err
	}
	return turnOutboxFromDTO(output.Outbox)
}

func (s *TurnStoreClient) CompleteConversationTurnOutbox(ctx context.Context, turnID, target, leaseToken string, now time.Time) (domconv.ConversationTurnResult, error) {
	if !validTurnID(turnID) || !validTurnTarget(target) || !validTurnLeaseToken(leaseToken) || !validTurnTime(now) {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInvalid
	}
	var output turnResultPayload
	if err := s.call(ctx, "complete", turnFinishPayload{TurnID: turnID, Target: target, LeaseToken: leaseToken, Now: now}, &output); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	if !validTurnResult(output.Result) || output.Result.TurnID != modulecore.TurnID(turnID) {
		return domconv.ConversationTurnResult{}, NewError(ErrorCodeOutcomeUnknown, "conversation turn completion result is invalid")
	}
	return output.Result, nil
}

func (s *TurnStoreClient) FailConversationTurnOutbox(ctx context.Context, turnID, target, leaseToken string, code domconv.ConversationTurnErrorCode, now time.Time) (domconv.ConversationTurnResult, error) {
	if !validTurnID(turnID) || !validTurnTarget(target) || !validTurnLeaseToken(leaseToken) || !validTurnErrorCode(code) || !validTurnTime(now) {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInvalid
	}
	var output turnResultPayload
	if err := s.call(ctx, "fail", turnFailPayload{TurnID: turnID, Target: target, LeaseToken: leaseToken, Code: code, Now: now}, &output); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	if !validTurnResult(output.Result) || output.Result.TurnID != modulecore.TurnID(turnID) {
		return domconv.ConversationTurnResult{}, NewError(ErrorCodeOutcomeUnknown, "conversation turn failure result is invalid")
	}
	return output.Result, nil
}

func (s *TurnStoreClient) LoadConversationThreadProjection(ctx context.Context, sessionID string, threadID modulecore.ThreadID) ([]l1sqlite.L1MemoryEvent, error) {
	if !validTurnSessionID(sessionID) || threadID.Validate() != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	var output turnProjectionResultPayload
	if err := s.call(ctx, "projection", turnProjectionPayload{SessionID: sessionID, ThreadID: threadID}, &output); err != nil {
		return nil, err
	}
	return turnProjectionEventsFromDTO(output.Events)
}

func (s *TurnStoreClient) LoadActiveConversationThreadProjection(ctx context.Context, sessionID string) ([]l1sqlite.L1MemoryEvent, error) {
	if !validTurnSessionID(sessionID) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	var output turnProjectionResultPayload
	if err := s.call(ctx, "active_projection", turnActiveProjectionPayload{SessionID: sessionID}, &output); err != nil {
		return nil, err
	}
	return turnProjectionEventsFromDTO(output.Events)
}

func turnRequestToDTO(request domconv.ConversationTurnRequest) turnRequestDTO {
	items := make([]turnRecallTraceDTO, len(request.RecallTraceItems))
	for index, item := range request.RecallTraceItems {
		items[index] = turnRecallTraceDTO{
			Layer: item.Layer, Kind: item.Kind, MemoryID: item.MemoryID, SourceID: item.SourceID,
			SourceType: item.SourceType, Summary: item.Summary, Query: item.Query, Provider: item.Provider,
			SourceURLs: append([]string(nil), item.SourceURLs...), RetrievedAt: item.RetrievedAt, Score: item.Score,
			Decision: item.Decision, Status: item.Status, Reason: item.Reason, MemoryState: item.MemoryState,
			Sensitivity: item.Sensitivity, PromptSection: item.PromptSection, TokenCount: item.TokenCount, PromptIndex: item.PromptIndex,
		}
	}
	return turnRequestDTO{
		TurnID: request.TurnID, TraceID: request.TraceID, RootTaskID: request.RootTaskID,
		UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
		SessionID: request.SessionID, OwnerID: request.OwnerID, Domain: request.Domain,
		UserMessage: request.UserMessage, AgentMessage: request.AgentMessage, AgentSpeaker: request.AgentSpeaker,
		RecallTraceItems: items, Boundary: request.Boundary, BoundaryReason: request.BoundaryReason,
		Targets: append([]domconv.ConversationTurnTarget(nil), request.Targets...),
	}
}

func (request turnRequestDTO) domain() domconv.ConversationTurnRequest {
	items := make([]domconv.RecallTraceItem, len(request.RecallTraceItems))
	for index, item := range request.RecallTraceItems {
		items[index] = domconv.RecallTraceItem{
			Layer: item.Layer, Kind: item.Kind, MemoryID: item.MemoryID, SourceID: item.SourceID,
			SourceType: item.SourceType, Summary: item.Summary, Query: item.Query, Provider: item.Provider,
			SourceURLs: append([]string(nil), item.SourceURLs...), RetrievedAt: item.RetrievedAt, Score: item.Score,
			Decision: item.Decision, Status: item.Status, Reason: item.Reason, MemoryState: item.MemoryState,
			Sensitivity: item.Sensitivity, PromptSection: item.PromptSection, TokenCount: item.TokenCount, PromptIndex: item.PromptIndex,
		}
	}
	return domconv.ConversationTurnRequest{
		TurnID: request.TurnID, TraceID: request.TraceID, RootTaskID: request.RootTaskID,
		UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
		SessionID: request.SessionID, OwnerID: request.OwnerID, Domain: request.Domain,
		UserMessage: request.UserMessage, AgentMessage: request.AgentMessage, AgentSpeaker: request.AgentSpeaker,
		RecallTraceItems: items, Boundary: request.Boundary, BoundaryReason: request.BoundaryReason,
		Targets: append([]domconv.ConversationTurnTarget(nil), request.Targets...),
	}
}

func decodeTurnPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > turnMaxPayloadBytes || trimmed[0] != '{' {
		return errors.New("invalid turn payload boundary")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("turn payload has trailing data")
	}
	return nil
}

func decodeTurnCommitRequest(raw json.RawMessage) (domconv.ConversationTurnRequest, error) {
	var payload turnCommitPayload
	if err := decodeTurnPayload(raw, &payload); err != nil {
		return domconv.ConversationTurnRequest{}, err
	}
	request, err := domconv.NormalizeConversationTurnRequest(payload.Request.domain())
	if err != nil || !validTurnRequestTimes(request) {
		return domconv.ConversationTurnRequest{}, domconv.ErrConversationTurnInvalid
	}
	return request, nil
}

func decodeTurnClaimPayload(raw json.RawMessage) (turnClaimPayload, error) {
	var payload turnClaimPayload
	if err := decodeTurnPayload(raw, &payload); err != nil || !validTurnID(payload.TurnID) || !validTurnTime(payload.Now) || !validTurnLease(payload.LeaseDurationNS) {
		return turnClaimPayload{}, domconv.ErrConversationTurnInvalid
	}
	return payload, nil
}

func decodeTurnClaimNextPayload(raw json.RawMessage) (turnClaimNextPayload, error) {
	var payload turnClaimNextPayload
	if err := decodeTurnPayload(raw, &payload); err != nil || !validTurnTime(payload.Now) || !validTurnLease(payload.LeaseDurationNS) {
		return turnClaimNextPayload{}, domconv.ErrConversationTurnInvalid
	}
	return payload, nil
}

func decodeTurnFinishPayload(raw json.RawMessage) (turnFinishPayload, error) {
	var payload turnFinishPayload
	if err := decodeTurnPayload(raw, &payload); err != nil || !validTurnID(payload.TurnID) || !validTurnTarget(payload.Target) ||
		!validTurnLeaseToken(payload.LeaseToken) || !validTurnTime(payload.Now) {
		return turnFinishPayload{}, domconv.ErrConversationTurnInvalid
	}
	return payload, nil
}

func decodeTurnFailPayload(raw json.RawMessage) (turnFailPayload, error) {
	var payload turnFailPayload
	if err := decodeTurnPayload(raw, &payload); err != nil || !validTurnID(payload.TurnID) || !validTurnTarget(payload.Target) ||
		!validTurnLeaseToken(payload.LeaseToken) || !validTurnErrorCode(payload.Code) || !validTurnTime(payload.Now) {
		return turnFailPayload{}, domconv.ErrConversationTurnInvalid
	}
	return payload, nil
}

func turnOperationIdentity(mutation MutationMetadata, operation string) l1sqlite.ConversationTurnOperationIdentity {
	return l1sqlite.ConversationTurnOperationIdentity{
		OpID: mutation.OpID, Operation: operation, PayloadSHA256: payloadHash(GroupTurn, operation, mutation.Payload),
	}
}

func turnReceiptMatchesRequest(result domconv.ConversationTurnResult, request domconv.ConversationTurnRequest) bool {
	payloadSHA256, err := request.PayloadSHA256()
	if err != nil || !validTurnResult(result) || !turnResultMatchesRequest(result, request) || result.PayloadSHA256 != payloadSHA256 ||
		len(result.RequestedTargets) != len(request.Targets) {
		return false
	}
	for index, target := range request.Targets {
		if result.RequestedTargets[index] != string(target) {
			return false
		}
	}
	return true
}

func rejectTurnPayload(operation string, mutating bool) error {
	err := NewError(ErrorCodeSchemaRejected, "turn "+operation+" payload rejected")
	if mutating {
		return ownerRolledBack(err)
	}
	return err
}

func turnMutationUnavailable() error {
	// The owner may have committed before returning an error. The protocol
	// handler therefore keeps its begun entry and turns this into outcome_unknown.
	return NewError(ErrorCodeStoreUnavailable, "conversation turn owner failed")
}

func turnReadError(err error) error {
	switch {
	case errors.Is(err, domconv.ErrConversationTurnInvalid):
		return NewError(ErrorCodeSchemaRejected, "conversation turn request rejected")
	case errors.Is(err, domconv.ErrConversationTurnConflict):
		return NewError(ErrorCodeDuplicateConflict, "conversation turn conflicts with the stored receipt")
	case errors.Is(err, domconv.ErrConversationTurnUnavailable):
		return NewError(ErrorCodeStoreUnavailable, "conversation turn owner is unavailable")
	case errors.Is(err, domconv.ErrConversationTurnInternal):
		return NewError(turnErrorCodeInternal, "conversation turn owner failed")
	case errors.Is(err, domconv.ErrThreadNotFound):
		return NewError(turnErrorCodeThreadAbsent, "active conversation thread was not found")
	default:
		return NewError(ErrorCodeStoreUnavailable, "conversation turn owner failed")
	}
}

type turnDomainCallError struct {
	wire   *Error
	domain error
}

func (e *turnDomainCallError) Error() string { return e.wire.Error() }

func (e *turnDomainCallError) Unwrap() []error { return []error{e.wire, e.domain} }

func mapTurnClientError(err error) error {
	var wire *Error
	if !errors.As(err, &wire) {
		return err
	}
	var domain error
	switch wire.Code {
	case ErrorCodeSchemaRejected:
		domain = domconv.ErrConversationTurnInvalid
	case ErrorCodeDuplicateConflict:
		domain = domconv.ErrConversationTurnConflict
	case ErrorCodeStoreUnavailable:
		domain = domconv.ErrConversationTurnUnavailable
	case turnErrorCodeInternal:
		domain = domconv.ErrConversationTurnInternal
	case turnErrorCodeThreadAbsent:
		domain = domconv.ErrThreadNotFound
	}
	if domain == nil {
		return err
	}
	return &turnDomainCallError{wire: wire, domain: domain}
}

func validTurnRequestTimes(request domconv.ConversationTurnRequest) bool {
	for _, item := range request.RecallTraceItems {
		if !validTurnTime(item.RetrievedAt) {
			return false
		}
	}
	return true
}

func validTurnID(value string) bool {
	return boundedTurnText(value, domconv.ConversationTurnMaxIDRunes, true, true) && modulecore.TurnID(value).Validate() == nil
}

func validTurnSessionID(value string) bool {
	return boundedTurnText(value, domconv.ConversationTurnMaxIDRunes, true, true) && modulecore.SessionID(value).Validate() == nil
}

func validTurnLeaseToken(value string) bool {
	return len(value) <= turnMaxLeaseTokenBytes && boundedTurnText(value, turnMaxLeaseTokenBytes, true, true)
}

func validTurnTarget(value string) bool {
	target := domconv.ConversationTurnTarget(value)
	return target == domconv.ConversationTurnTargetRedisProjection || target == domconv.ConversationTurnTargetThreadFollowers
}

func validTurnErrorCode(value domconv.ConversationTurnErrorCode) bool {
	switch value {
	case domconv.ConversationTurnErrorInvalid, domconv.ConversationTurnErrorConflict,
		domconv.ConversationTurnErrorUnavailable, domconv.ConversationTurnErrorInternal:
		return true
	default:
		return false
	}
}

func validTurnLease(value time.Duration) bool {
	return value >= 0 && value <= 24*time.Hour
}

func validTurnTime(value time.Time) bool {
	if value.IsZero() {
		return true
	}
	_, err := value.MarshalJSON()
	return err == nil
}

func boundedTurnText(value string, maxRunes int, required, canonicalTrim bool) bool {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len([]rune(value)) > maxRunes {
		return false
	}
	if required && strings.TrimSpace(value) == "" {
		return false
	}
	return !canonicalTrim || strings.TrimSpace(value) == value
}

func validTurnResult(result domconv.ConversationTurnResult) bool {
	if !validTurnID(string(result.TurnID)) || result.TraceID.Validate() != nil || result.RootTaskID.Validate() != nil ||
		!validTurnSessionID(result.SessionID) || result.UserMessageID.Validate() != nil || result.AgentMessageID.Validate() != nil ||
		result.UserMessageID == result.AgentMessageID || len(result.MessageIDs) != 2 ||
		result.MessageIDs[0] != string(result.UserMessageID) || result.MessageIDs[1] != string(result.AgentMessageID) ||
		!validSHA256(result.PayloadSHA256) || !validBoundThreadTuple(result.ThreadID, result.ThreadSeq, result.ThreadKind, true) ||
		!validBoundThreadTuple(result.ClosedThreadID, result.ClosedThreadSeq, result.ClosedThreadKind, false) {
		return false
	}
	switch result.Status {
	case domconv.ConversationTurnCompleted, domconv.ConversationTurnPartial, domconv.ConversationTurnFailed:
	default:
		return false
	}
	if result.ErrorCode != "" && !validTurnErrorCode(result.ErrorCode) {
		return false
	}
	return validTurnTargetList(result.RequestedTargets) && validTurnTargetList(result.PendingTargets) && validTurnTargetList(result.CompletedTargets)
}

func turnResultMatchesRequest(result domconv.ConversationTurnResult, request domconv.ConversationTurnRequest) bool {
	return result.TurnID == request.TurnID && result.TraceID == request.TraceID && result.RootTaskID == request.RootTaskID &&
		result.SessionID == request.SessionID && result.UserMessageID == request.UserMessageID && result.AgentMessageID == request.AgentMessageID
}

func validTurnTargetList(values []string) bool {
	if len(values) > domconv.ConversationTurnMaxTargets {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validTurnTarget(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validBoundThreadTuple(id modulecore.ThreadID, seq domconv.ThreadSeq, kind domconv.ThreadKind, required bool) bool {
	if id == "" {
		return !required && seq == 0 && kind == ""
	}
	return id.Validate() == nil && seq.Validate() == nil && kind.Validate() == nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validClaimedTurnOutbox(outbox *domconv.ConversationTurnOutbox) bool {
	if outbox == nil || !validTurnID(string(outbox.TurnID)) || outbox.TraceID.Validate() != nil || outbox.RootTaskID.Validate() != nil ||
		!validTurnTarget(outbox.Target) || !validTurnSessionID(outbox.SessionID) ||
		!validBoundThreadTuple(outbox.ThreadID, outbox.ThreadSeq, outbox.ThreadKind, true) ||
		!validBoundThreadTuple(outbox.ClosedThreadID, outbox.ClosedThreadSeq, outbox.ClosedThreadKind, false) ||
		!validSHA256(outbox.PayloadSHA256) || len(outbox.PayloadJSON) > turnMaxOutboxPayloadBytes ||
		!boundedTurnText(outbox.PayloadJSON, turnMaxOutboxPayloadBytes, true, false) ||
		outbox.Status != domconv.ConversationTurnOutboxRunning || !validTurnLeaseToken(outbox.LeaseToken) ||
		!validTurnTime(outbox.LeaseExpiresAt) || outbox.LeaseExpiresAt.IsZero() || outbox.Attempts < 1 ||
		outbox.Attempts > domconv.ConversationTurnMaxOutboxAttempts || !validTurnTime(outbox.CreatedAt) || outbox.CreatedAt.IsZero() ||
		!validTurnTime(outbox.UpdatedAt) || outbox.UpdatedAt.IsZero() {
		return false
	}
	return outbox.LastError == "" || validTurnErrorCode(outbox.LastError)
}

func turnOutboxToDTO(outbox *domconv.ConversationTurnOutbox) *turnOutboxDTO {
	if outbox == nil {
		return nil
	}
	return &turnOutboxDTO{
		TurnID: outbox.TurnID, TraceID: outbox.TraceID, RootTaskID: outbox.RootTaskID, Target: outbox.Target,
		SessionID: outbox.SessionID, ThreadID: outbox.ThreadID, ThreadSeq: outbox.ThreadSeq, ThreadKind: outbox.ThreadKind,
		ClosedThreadID: outbox.ClosedThreadID, ClosedThreadSeq: outbox.ClosedThreadSeq, ClosedThreadKind: outbox.ClosedThreadKind,
		PayloadSHA256: outbox.PayloadSHA256, PayloadJSON: outbox.PayloadJSON, Status: outbox.Status, LeaseToken: outbox.LeaseToken,
		LeaseExpiresAt: outbox.LeaseExpiresAt, Attempts: outbox.Attempts, LastError: outbox.LastError,
		CreatedAt: outbox.CreatedAt, UpdatedAt: outbox.UpdatedAt,
	}
}

func turnOutboxFromDTO(dto *turnOutboxDTO) (*domconv.ConversationTurnOutbox, error) {
	if dto == nil {
		return nil, nil
	}
	outbox := &domconv.ConversationTurnOutbox{
		TurnID: dto.TurnID, TraceID: dto.TraceID, RootTaskID: dto.RootTaskID, Target: dto.Target,
		SessionID: dto.SessionID, ThreadID: dto.ThreadID, ThreadSeq: dto.ThreadSeq, ThreadKind: dto.ThreadKind,
		ClosedThreadID: dto.ClosedThreadID, ClosedThreadSeq: dto.ClosedThreadSeq, ClosedThreadKind: dto.ClosedThreadKind,
		PayloadSHA256: dto.PayloadSHA256, PayloadJSON: dto.PayloadJSON, Status: dto.Status, LeaseToken: dto.LeaseToken,
		LeaseExpiresAt: dto.LeaseExpiresAt, Attempts: dto.Attempts, LastError: dto.LastError,
		CreatedAt: dto.CreatedAt, UpdatedAt: dto.UpdatedAt,
	}
	if !validClaimedTurnOutbox(outbox) {
		return nil, NewError(ErrorCodeStoreUnavailable, "conversation turn outbox result is invalid")
	}
	return outbox, nil
}

func turnProjectionEventsToDTO(events []l1sqlite.L1MemoryEvent) ([]turnProjectionEventDTO, error) {
	if len(events) < 2 || len(events) > turnMaxProjectionEvents || len(events)%2 != 0 {
		return nil, errors.New("projection event count is outside the bounded pair contract")
	}
	result := make([]turnProjectionEventDTO, len(events))
	for index, event := range events {
		dto, err := turnProjectionEventToDTO(event)
		if err != nil {
			return nil, err
		}
		result[index] = dto
	}
	return result, nil
}

func turnProjectionEventToDTO(event l1sqlite.L1MemoryEvent) (turnProjectionEventDTO, error) {
	if !boundedTurnText(event.ID, domconv.ConversationTurnMaxIDRunes, true, true) || modulecore.MessageID(event.ID).Validate() != nil ||
		!validTurnSessionID(event.SessionID) || !validBoundThreadTuple(event.ThreadID, event.ThreadSeq, event.ThreadKind, true) ||
		event.Namespace != "conv:"+string(event.ThreadID) || event.Source != "conversation" || event.Layer != l1sqlite.MemoryLayerL1 ||
		event.MemoryState != l1sqlite.MemoryStateObserved || !boundedTurnText(event.Message, domconv.ConversationTurnMaxTextRunes, true, false) ||
		!validTurnTime(event.CreatedAt) || event.CreatedAt.IsZero() || !validTurnTime(event.UpdatedAt) || event.UpdatedAt.IsZero() {
		return turnProjectionEventDTO{}, errors.New("projection event fields are invalid")
	}
	if !validTurnSpeaker(event.Speaker) {
		return turnProjectionEventDTO{}, errors.New("projection speaker is invalid")
	}
	if len(event.Meta) != 6 {
		return turnProjectionEventDTO{}, errors.New("projection metadata shape is invalid")
	}
	for key := range event.Meta {
		switch key {
		case "domain", "message_id", "turn_id", "speaker", "from", "to":
		default:
			return turnProjectionEventDTO{}, errors.New("projection metadata has an unknown field")
		}
	}
	meta := turnProjectionMetaDTO{}
	for key, destination := range map[string]*string{
		"domain": &meta.Domain, "message_id": &meta.MessageID, "turn_id": &meta.TurnID,
		"speaker": &meta.Speaker, "from": &meta.From, "to": &meta.To,
	} {
		value, ok := event.Meta[key].(string)
		if !ok || !boundedTurnText(value, domconv.ConversationTurnMaxIDRunes, true, true) {
			return turnProjectionEventDTO{}, errors.New("projection metadata value is invalid")
		}
		*destination = value
	}
	if modulecore.MessageID(meta.MessageID).Validate() != nil || modulecore.TurnID(meta.TurnID).Validate() != nil ||
		meta.MessageID != event.ID || meta.Speaker != string(event.Speaker) || meta.From != string(event.Speaker) ||
		!validTurnSpeaker(domconv.Speaker(meta.To)) {
		return turnProjectionEventDTO{}, errors.New("projection metadata identity is invalid")
	}
	return turnProjectionEventDTO{
		ID: event.ID, Namespace: event.Namespace, SessionID: event.SessionID, ThreadID: event.ThreadID,
		ThreadSeq: event.ThreadSeq, ThreadKind: event.ThreadKind, Speaker: event.Speaker, Message: event.Message,
		Meta: meta, MemoryState: event.MemoryState, Layer: event.Layer, Source: event.Source,
		CreatedAt: event.CreatedAt, UpdatedAt: event.UpdatedAt,
	}, nil
}

func validTurnSpeaker(speaker domconv.Speaker) bool {
	if speaker == domconv.SpeakerUser {
		return true
	}
	canonical, ok := domconv.CanonicalChatAgentSpeaker(speaker)
	return ok && canonical == speaker
}

func turnProjectionEventsFromDTO(events []turnProjectionEventDTO) ([]l1sqlite.L1MemoryEvent, error) {
	if len(events) < 2 || len(events) > turnMaxProjectionEvents || len(events)%2 != 0 {
		return nil, NewError(ErrorCodeStoreUnavailable, "conversation turn projection result is invalid")
	}
	result := make([]l1sqlite.L1MemoryEvent, len(events))
	for index, dto := range events {
		event := l1sqlite.L1MemoryEvent{
			ID: dto.ID, Namespace: dto.Namespace, SessionID: dto.SessionID, ThreadID: dto.ThreadID,
			ThreadSeq: dto.ThreadSeq, ThreadKind: dto.ThreadKind, Speaker: dto.Speaker, Message: dto.Message,
			Meta: map[string]interface{}{
				"domain": dto.Meta.Domain, "message_id": dto.Meta.MessageID, "turn_id": dto.Meta.TurnID,
				"speaker": dto.Meta.Speaker, "from": dto.Meta.From, "to": dto.Meta.To,
			},
			MemoryState: dto.MemoryState, Layer: dto.Layer, Source: dto.Source, CreatedAt: dto.CreatedAt, UpdatedAt: dto.UpdatedAt,
		}
		validated, err := turnProjectionEventToDTO(event)
		if err != nil || validated != dto {
			return nil, NewError(ErrorCodeStoreUnavailable, "conversation turn projection result is invalid")
		}
		result[index] = event
	}
	return result, nil
}
