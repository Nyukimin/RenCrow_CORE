package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const agentOpsAcceptedInputReadTimeout = 5 * time.Second

type agentOpsAcceptedInputReader struct {
	parentContext context.Context
	store         conversation.AcceptedOPSInputStore
	expected      conversation.AcceptedOPSInput
	mu            sync.Mutex
	used          bool
}

func newAgentOpsAcceptedInputReader(
	parentContext context.Context,
	store conversation.AcceptedOPSInputStore,
	expected conversation.AcceptedOPSInput,
) (*agentOpsAcceptedInputReader, error) {
	if parentContext == nil || store == nil || expected.Receipt.Validate() != nil || expected.RawMessage == "" {
		return nil, conversation.ErrAcceptedOPSInputInvalid
	}
	receipt := expected.Receipt
	scope, found := domaintool.ToolExecutionScopeFromContext(parentContext)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindUser ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP || scope.ActorID != receipt.OwnerID ||
		scope.AuthenticatedUserID != receipt.OwnerID || scope.RequestID != receipt.RequestID || !scope.Allows(domaintool.DataScopeUser) {
		return nil, conversation.ErrAcceptedOPSInputForbidden
	}
	if receipt.ActorID != receipt.OwnerID || receipt.RawSHA256 != acceptedOPSRawSHA256(expected.RawMessage) {
		return nil, conversation.ErrAcceptedOPSInputConflict
	}
	payloadHash, err := conversation.AcceptedOPSInputPayloadSHA256(acceptedOPSRequestFromRead(expected))
	if err != nil || payloadHash != receipt.PayloadSHA256 {
		return nil, conversation.ErrAcceptedOPSInputConflict
	}
	return &agentOpsAcceptedInputReader{
		parentContext: parentContext,
		store:         store,
		expected:      expected,
	}, nil
}

func (r *agentOpsAcceptedInputReader) ReadAcceptedInput(ctx context.Context) (conversation.AcceptedOPSInput, error) {
	if r == nil || ctx == nil || r.parentContext == nil || r.store == nil {
		return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputUnavailable
	}
	if err := r.validateExecutionScope(ctx); err != nil {
		return conversation.AcceptedOPSInput{}, err
	}
	if err := ctx.Err(); err != nil {
		return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputUnavailable
	}
	r.mu.Lock()
	if r.used {
		r.mu.Unlock()
		return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputUnavailable
	}
	r.used = true
	r.mu.Unlock()

	deadline := time.Now().Add(agentOpsAcceptedInputReadTimeout)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	if parentDeadline, ok := r.parentContext.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	if !deadline.After(time.Now()) {
		return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputUnavailable
	}
	readContext, cancel := context.WithDeadline(r.parentContext, deadline)
	stopCallerCancellation := context.AfterFunc(ctx, cancel)
	defer func() {
		stopCallerCancellation()
		cancel()
	}()
	if err := readContext.Err(); err != nil {
		return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputUnavailable
	}
	expected := r.expected
	read, err := r.store.ReadAcceptedOPSInput(readContext, conversation.AcceptedOPSInputReadRequest{
		RequestID: expected.Receipt.RequestID,
		OwnerID:   expected.Receipt.OwnerID,
	})
	if err != nil {
		return conversation.AcceptedOPSInput{}, err
	}
	if !sameAcceptedOPSReceipt(expected.Receipt, read.Receipt) || read.RawMessage != expected.RawMessage ||
		read.Receipt.Validate() != nil || read.Receipt.RawSHA256 != acceptedOPSRawSHA256(read.RawMessage) {
		return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputConflict
	}
	payloadHash, err := conversation.AcceptedOPSInputPayloadSHA256(acceptedOPSRequestFromRead(read))
	if err != nil || payloadHash != read.Receipt.PayloadSHA256 {
		return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputConflict
	}
	return read, nil
}

func (r *agentOpsAcceptedInputReader) validateExecutionScope(ctx context.Context) error {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent ||
		scope.ActorID != "shiro" || scope.AgentRole != "worker" || scope.Purpose != "ops" ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator ||
		scope.RequestID != r.expected.Receipt.RequestID || scope.AuthenticatedUserID != r.expected.Receipt.OwnerID ||
		!scope.Allows(domaintool.DataScopeUser) {
		return conversation.ErrAcceptedOPSInputForbidden
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil || identity.TaskID != r.expected.Receipt.TaskID || identity.TraceID != r.expected.Receipt.TraceID {
		return conversation.ErrAcceptedOPSInputForbidden
	}
	return nil
}

func acceptedOPSRequestFromRead(input conversation.AcceptedOPSInput) conversation.AcceptedOPSInputRequest {
	receipt := input.Receipt
	return conversation.AcceptedOPSInputRequest{
		RequestID: receipt.RequestID, OwnerID: receipt.OwnerID, ActorID: receipt.ActorID,
		SessionID: receipt.SessionID, FirstThreadID: receipt.ThreadID,
		TaskID: receipt.TaskID, TurnID: receipt.TurnID, TraceID: receipt.TraceID,
		UserMessageID: receipt.UserMessageID, AgentMessageID: receipt.AgentMessageID,
		RawMessage: input.RawMessage, DeclaredOrigin: receipt.DeclaredOrigin,
	}
}

func acceptedOPSRawSHA256(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

var _ nativeharnessclient.AcceptedInputReader = (*agentOpsAcceptedInputReader)(nil)
