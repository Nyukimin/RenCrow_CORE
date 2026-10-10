package storagehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	l1OpAcceptOPSInput = "accept_ops_input"
	l1OpReadOPSInput   = "read_accepted_ops_input"
)

// l1AcceptedOPSInputPayload is an explicit transport DTO. The domain request
// deliberately hides RawMessage from JSON, so the exact input bytes must be
// carried once in this owner RPC payload.
type l1AcceptedOPSInputPayload struct {
	RequestID      string                         `json:"request_id"`
	OwnerID        string                         `json:"owner_id"`
	ActorID        string                         `json:"actor_id"`
	SessionID      core.SessionID                 `json:"session_id"`
	FirstThreadID  core.ThreadID                  `json:"first_thread_id"`
	TaskID         core.TaskID                    `json:"task_id"`
	TurnID         core.TurnID                    `json:"turn_id"`
	TraceID        core.TraceID                   `json:"trace_id"`
	UserMessageID  core.MessageID                 `json:"user_message_id"`
	AgentMessageID core.MessageID                 `json:"agent_message_id"`
	DeclaredOrigin domconv.AcceptedOPSInputOrigin `json:"declared_origin"`
	RawMessage     string                         `json:"raw_message"`
}

func (payload l1AcceptedOPSInputPayload) domain() domconv.AcceptedOPSInputRequest {
	return domconv.AcceptedOPSInputRequest{
		RequestID: payload.RequestID, OwnerID: payload.OwnerID, ActorID: payload.ActorID,
		SessionID: payload.SessionID, FirstThreadID: payload.FirstThreadID,
		TaskID: payload.TaskID, TurnID: payload.TurnID, TraceID: payload.TraceID,
		UserMessageID: payload.UserMessageID, AgentMessageID: payload.AgentMessageID,
		DeclaredOrigin: payload.DeclaredOrigin, RawMessage: payload.RawMessage,
	}
}

func l1AcceptedOPSInputPayloadFromDomain(request domconv.AcceptedOPSInputRequest) l1AcceptedOPSInputPayload {
	return l1AcceptedOPSInputPayload{
		RequestID: request.RequestID, OwnerID: request.OwnerID, ActorID: request.ActorID,
		SessionID: request.SessionID, FirstThreadID: request.FirstThreadID,
		TaskID: request.TaskID, TurnID: request.TurnID, TraceID: request.TraceID,
		UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
		DeclaredOrigin: request.DeclaredOrigin, RawMessage: request.RawMessage,
	}
}

type l1AcceptedOPSReadPayload struct {
	RequestID string `json:"request_id"`
	OwnerID   string `json:"owner_id"`
}

type l1AcceptedOPSReceiptResult struct {
	Receipt domconv.AcceptedOPSInputReceipt `json:"receipt"`
}

type l1AcceptedOPSReadResult struct {
	Receipt    domconv.AcceptedOPSInputReceipt `json:"receipt"`
	RawMessage string                          `json:"raw_message"`
}

func registerAcceptedOPSInputOperations(handler *Handler, owner domconv.AcceptedOPSInputStore, configuredPrincipal string) error {
	configuredPrincipal = strings.TrimSpace(configuredPrincipal)
	if handler == nil || owner == nil || configuredPrincipal == "" {
		return errors.New("storagehost: accepted OPS operations need a handler, owner and configured principal")
	}
	if _, err := domconv.NormalizeAcceptedOPSInputReadRequest(domconv.AcceptedOPSInputReadRequest{
		RequestID: "accepted-ops-config-check", OwnerID: configuredPrincipal,
	}); err != nil {
		return errors.New("storagehost: accepted OPS configured principal is invalid")
	}
	if err := handler.Register(GroupL1, l1OpAcceptOPSInput, true, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload l1AcceptedOPSInputPayload
		if decodeL1Payload(raw, &payload) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "accepted OPS input payload rejected"))
		}
		request, err := domconv.NormalizeAcceptedOPSInputRequest(payload.domain())
		if err != nil {
			return nil, ownerRolledBack(acceptedOPSOwnerError(err))
		}
		if request.OwnerID != configuredPrincipal || request.ActorID != configuredPrincipal {
			return nil, ownerRolledBack(NewError(ErrorCodeForbidden, "accepted OPS owner assertion rejected"))
		}
		ownerCtx, err := acceptedOPSConfiguredUserContext(ctx, configuredPrincipal, request.RequestID)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeForbidden, "accepted OPS owner scope rejected"))
		}
		receipt, err := owner.AcceptOPSInput(ownerCtx, request)
		if err != nil {
			ownerErr := acceptedOPSOwnerError(err)
			if errors.Is(err, domconv.ErrAcceptedOPSInputInvalid) || errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) || errors.Is(err, domconv.ErrAcceptedOPSInputConflict) {
				return nil, ownerRolledBack(ownerErr)
			}
			return nil, ownerErr
		}
		if !acceptedOPSReceiptMatchesRequest(receipt, request) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "accepted OPS owner returned an invalid receipt")
		}
		return l1ReadyResult(l1AcceptedOPSReceiptResult{Receipt: receipt})
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupL1, l1OpReadOPSInput, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload l1AcceptedOPSReadPayload
		if decodeL1Payload(raw, &payload) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "accepted OPS read payload rejected")
		}
		request, err := domconv.NormalizeAcceptedOPSInputReadRequest(domconv.AcceptedOPSInputReadRequest{
			RequestID: payload.RequestID, OwnerID: payload.OwnerID,
		})
		if err != nil {
			return nil, acceptedOPSOwnerError(err)
		}
		if request.OwnerID != configuredPrincipal {
			return nil, NewError(ErrorCodeForbidden, "accepted OPS owner assertion rejected")
		}
		ownerCtx, err := acceptedOPSConfiguredUserContext(ctx, configuredPrincipal, request.RequestID)
		if err != nil {
			return nil, NewError(ErrorCodeForbidden, "accepted OPS owner scope rejected")
		}
		input, err := owner.ReadAcceptedOPSInput(ownerCtx, request)
		if err != nil {
			return nil, acceptedOPSOwnerError(err)
		}
		if !acceptedOPSReadMatchesRequest(input, request) {
			return nil, NewError(ErrorCodeStoreUnavailable, "accepted OPS owner returned an invalid read result")
		}
		return l1ReadyResult(l1AcceptedOPSReadResult{Receipt: input.Receipt, RawMessage: input.RawMessage})
	}); err != nil {
		return err
	}
	return nil
}

func acceptedOPSConfiguredUserContext(ctx context.Context, configuredPrincipal, requestID string) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scope, err := domaintool.NewToolExecutionScope(
		requestID, domaintool.ActorKindUser, configuredPrincipal, configuredPrincipal,
		[]string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		return nil, err
	}
	return domaintool.WithToolExecutionScope(ctx, scope), nil
}

func acceptedOPSOwnerError(err error) error {
	switch {
	case errors.Is(err, domconv.ErrAcceptedOPSInputInvalid):
		return NewError(ErrorCodeSchemaRejected, "accepted OPS input rejected")
	case errors.Is(err, domconv.ErrAcceptedOPSInputForbidden):
		return NewError(ErrorCodeForbidden, "accepted OPS owner rejected the request scope")
	case errors.Is(err, domconv.ErrAcceptedOPSInputConflict):
		return NewError(ErrorCodeDuplicateConflict, "accepted OPS request identity conflicts with stored input")
	case errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable):
		return NewError(ErrorCodeStoreUnavailable, "accepted OPS owner is unavailable")
	default:
		return NewError(ErrorCodeOutcomeUnknown, "accepted OPS owner outcome could not be confirmed")
	}
}

func acceptedOPSReceiptMatchesRequest(receipt domconv.AcceptedOPSInputReceipt, request domconv.AcceptedOPSInputRequest) bool {
	if receipt.Validate() != nil {
		return false
	}
	payloadHash, err := domconv.AcceptedOPSInputPayloadSHA256(request)
	if err != nil || receipt.RequestID != request.RequestID || receipt.OwnerID != request.OwnerID ||
		receipt.ActorID != request.ActorID || receipt.DeclaredOrigin != request.DeclaredOrigin ||
		receipt.PayloadSHA256 != payloadHash || receipt.RawSHA256 != acceptedOPSRawSHA256(request.RawMessage) {
		return false
	}
	if receipt.IdempotentReplay {
		// The owner/request key and canonical input select the stored receipt.
		// A valid retry may allocate fresh IDs, so the owner's original IDs are
		// authoritative once it reports an idempotent replay.
		return true
	}
	return receipt.SessionID == request.SessionID && receipt.ThreadID == request.FirstThreadID &&
		receipt.TaskID == request.TaskID && receipt.TurnID == request.TurnID && receipt.TraceID == request.TraceID &&
		receipt.UserMessageID == request.UserMessageID && receipt.AgentMessageID == request.AgentMessageID
}

func acceptedOPSReadMatchesRequest(input domconv.AcceptedOPSInput, request domconv.AcceptedOPSInputReadRequest) bool {
	if input.Receipt.RequestID != request.RequestID || input.Receipt.OwnerID != request.OwnerID ||
		input.Receipt.ActorID != request.OwnerID {
		return false
	}
	acceptedRequest := domconv.AcceptedOPSInputRequest{
		RequestID: input.Receipt.RequestID, OwnerID: input.Receipt.OwnerID, ActorID: input.Receipt.ActorID,
		SessionID: input.Receipt.SessionID, FirstThreadID: input.Receipt.ThreadID,
		TaskID: input.Receipt.TaskID, TurnID: input.Receipt.TurnID, TraceID: input.Receipt.TraceID,
		UserMessageID: input.Receipt.UserMessageID, AgentMessageID: input.Receipt.AgentMessageID,
		DeclaredOrigin: input.Receipt.DeclaredOrigin, RawMessage: input.RawMessage,
	}
	return acceptedOPSReceiptMatchesRequest(input.Receipt, acceptedRequest)
}

func acceptedOPSRawSHA256(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func validateAcceptedOPSRemoteUserScope(ctx context.Context, requestID, ownerID, actorID string) error {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.RequestID != requestID ||
		scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != ownerID || actorID != ownerID ||
		scope.AuthenticatedUserID != ownerID || !scope.Allows(domaintool.DataScopeUser) ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
		return domconv.ErrAcceptedOPSInputForbidden
	}
	return nil
}

func acceptedOPSClientError(err error) error {
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr == nil {
		return err
	}
	switch hostErr.Code {
	case ErrorCodeSchemaRejected:
		return domconv.ErrAcceptedOPSInputInvalid
	case ErrorCodeForbidden:
		return domconv.ErrAcceptedOPSInputForbidden
	case ErrorCodeDuplicateConflict:
		return domconv.ErrAcceptedOPSInputConflict
	case ErrorCodeStoreUnavailable, ErrorCodeUnreachable:
		return domconv.ErrAcceptedOPSInputUnavailable
	default:
		return err
	}
}

// AcceptOPSInput implements the Conversation owner port over the canonical
// storage-host L1 operation group.
func (s *L1StoreClient) AcceptOPSInput(ctx context.Context, request domconv.AcceptedOPSInputRequest) (domconv.AcceptedOPSInputReceipt, error) {
	normalized, err := domconv.NormalizeAcceptedOPSInputRequest(request)
	if err != nil {
		return domconv.AcceptedOPSInputReceipt{}, err
	}
	if normalized.OwnerID != normalized.ActorID {
		return domconv.AcceptedOPSInputReceipt{}, domconv.ErrAcceptedOPSInputForbidden
	}
	if err := validateAcceptedOPSRemoteUserScope(ctx, normalized.RequestID, normalized.OwnerID, normalized.ActorID); err != nil {
		return domconv.AcceptedOPSInputReceipt{}, err
	}
	var result l1AcceptedOPSReceiptResult
	err = s.call(ctx, l1OpAcceptOPSInput, l1AcceptedOPSInputPayloadFromDomain(normalized), true, &result, func() error {
		if !acceptedOPSReceiptMatchesRequest(result.Receipt, normalized) {
			return errors.New("accepted OPS receipt does not match request")
		}
		return nil
	})
	if err != nil {
		return domconv.AcceptedOPSInputReceipt{}, acceptedOPSClientError(err)
	}
	return result.Receipt, nil
}

// ReadAcceptedOPSInput is deliberately user-parent only. Agent/worker read
// scope is not transported or reconstructed by this storage RPC client.
func (s *L1StoreClient) ReadAcceptedOPSInput(ctx context.Context, request domconv.AcceptedOPSInputReadRequest) (domconv.AcceptedOPSInput, error) {
	normalized, err := domconv.NormalizeAcceptedOPSInputReadRequest(request)
	if err != nil {
		return domconv.AcceptedOPSInput{}, err
	}
	if err := validateAcceptedOPSRemoteUserScope(ctx, normalized.RequestID, normalized.OwnerID, normalized.OwnerID); err != nil {
		return domconv.AcceptedOPSInput{}, err
	}
	var result l1AcceptedOPSReadResult
	err = s.call(ctx, l1OpReadOPSInput, l1AcceptedOPSReadPayload{
		RequestID: normalized.RequestID, OwnerID: normalized.OwnerID,
	}, false, &result, func() error {
		if !acceptedOPSReadMatchesRequest(domconv.AcceptedOPSInput{Receipt: result.Receipt, RawMessage: result.RawMessage}, normalized) {
			return errors.New("accepted OPS read result does not match request")
		}
		return nil
	})
	if err != nil {
		return domconv.AcceptedOPSInput{}, acceptedOPSClientError(err)
	}
	return domconv.AcceptedOPSInput{Receipt: result.Receipt, RawMessage: result.RawMessage}, nil
}

var _ domconv.AcceptedOPSInputStore = (*L1StoreClient)(nil)
