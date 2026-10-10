package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (d *delegation) acceptedOriginProof(ctx context.Context, destinationThreadID string) (*protocol.OriginProof, error) {
	if d.reference == nil {
		return nil, nil
	}
	reader, found := nativeharnessclient.AcceptedInputReaderFromContext(ctx)
	if !found {
		return nil, blocked(codeAcceptedInputUnavailable)
	}
	accepted, err := reader.ReadAcceptedInput(ctx)
	if err != nil {
		if errors.Is(err, conversation.ErrAcceptedOPSInputConflict) || errors.Is(err, conversation.ErrAcceptedOPSInputInvalid) || errors.Is(err, conversation.ErrAcceptedOPSInputForbidden) {
			return nil, rejected("accepted_input_invalid")
		}
		return nil, blocked(codeAcceptedInputUnavailable)
	}
	if err := d.validateAcceptedInput(ctx, accepted); err != nil {
		return nil, rejected("accepted_input_invalid")
	}
	if accepted.Receipt.DeclaredOrigin == conversation.AcceptedOPSInputOriginAutomation {
		return nil, nil
	}
	if accepted.Receipt.DeclaredOrigin != conversation.AcceptedOPSInputOriginHuman {
		return nil, rejected("accepted_input_invalid")
	}
	if d.r.originProofSigner == nil {
		return nil, blocked(codeOriginProofUnavailable)
	}
	receipt := accepted.Receipt
	proof, err := d.r.originProofSigner.Sign(nativeharnessclient.OriginProofRequest{
		Text: d.userText,
		Accepted: nativeharnessclient.AcceptedInput{
			MessageID: receipt.UserMessageID,
			ThreadID:  receipt.ThreadID,
			Sequence:  uint64(receipt.AcceptanceSequence),
			Raw:       []byte(accepted.RawMessage),
		},
		DestinationThreadID: destinationThreadID,
		MutationKey:         d.key("start"),
	})
	if err != nil {
		return nil, rejected("human_origin_proof_failed")
	}
	return protocolOriginProof(proof), nil
}

func (d *delegation) validateAcceptedInput(ctx context.Context, accepted conversation.AcceptedOPSInput) error {
	receipt := accepted.Receipt
	if d.reference == nil || receipt.Validate() != nil || accepted.RawMessage == "" || accepted.RawMessage != d.userText || accepted.RawMessage != d.input.MessageText() {
		return conversation.ErrAcceptedOPSInputConflict
	}
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != tool.ActorKindAgent || scope.ActorID != shiroActorID ||
		scope.AgentRole != "worker" || scope.Purpose != "ops" || scope.RequestID != d.reference.RequestID ||
		scope.AuthenticatedUserID != d.reference.OwnerID || !scope.Allows(tool.DataScopeUser) {
		return conversation.ErrAcceptedOPSInputForbidden
	}
	if receipt.RequestID != d.reference.RequestID || receipt.OwnerID != d.reference.OwnerID || receipt.ActorID != d.reference.OwnerID ||
		receipt.PayloadSHA256 != d.reference.PayloadSHA256 || receipt.TaskID != d.parent.TaskID || receipt.TaskID != d.input.RootTaskID() ||
		receipt.SessionID != d.task.OriginSessionID || receipt.ThreadID != d.task.OriginThreadID ||
		receipt.TurnID != d.input.TurnID() || receipt.TurnID != d.task.OriginTurnID || receipt.TraceID != d.parent.TraceID ||
		receipt.UserMessageID != d.input.UserMessageID() || receipt.UserMessageID != d.task.OriginMessageID ||
		receipt.AgentMessageID != d.input.AgentMessageID() || receipt.AcceptanceSequence <= 0 {
		return conversation.ErrAcceptedOPSInputConflict
	}
	request := conversation.AcceptedOPSInputRequest{
		RequestID: receipt.RequestID, OwnerID: receipt.OwnerID, ActorID: receipt.ActorID,
		SessionID: receipt.SessionID, FirstThreadID: receipt.ThreadID, TaskID: receipt.TaskID,
		TurnID: receipt.TurnID, TraceID: receipt.TraceID, UserMessageID: receipt.UserMessageID,
		AgentMessageID: receipt.AgentMessageID, RawMessage: accepted.RawMessage, DeclaredOrigin: receipt.DeclaredOrigin,
	}
	payloadHash, err := conversation.AcceptedOPSInputPayloadSHA256(request)
	if err != nil || payloadHash != receipt.PayloadSHA256 {
		return conversation.ErrAcceptedOPSInputConflict
	}
	rawHash := sha256.Sum256([]byte(accepted.RawMessage))
	if receipt.RawSHA256 != hex.EncodeToString(rawHash[:]) {
		return conversation.ErrAcceptedOPSInputConflict
	}
	return nil
}

func protocolOriginProof(proof nativeharnessclient.OriginProof) *protocol.OriginProof {
	return &protocol.OriginProof{
		Issuer: proof.Issuer, KeyID: proof.KeyID, Audience: proof.Audience, Origin: proof.Origin,
		SourceMessageID: proof.SourceMessageID, SourceThreadID: proof.SourceThreadID,
		DestinationThreadID: proof.DestinationThreadID, MutationKey: proof.MutationKey,
		RawHash: proof.RawHash, Sequence: proof.Sequence, IssuedAt: proof.IssuedAt,
		ExpiresAt: proof.ExpiresAt, Nonce: proof.Nonce, MAC: proof.MAC,
	}
}
