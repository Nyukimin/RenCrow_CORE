package agent

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

type mioThreadBoundaryAttestationContextKey struct{}

type mioThreadBoundaryAttestation struct {
	turnID      string
	traceID     string
	rootTaskID  string
	messageID   string
	sessionID   string
	messageHash [32]byte
	proposal    conversation.ThreadBoundaryProposal
	preparation *conversation.ConversationTurnPreparation
	guard       conversation.PreparedChatTurnGuard
}

func normalizeMioThreadBoundaryProposal(proposal conversation.ThreadBoundaryProposal) conversation.ThreadBoundaryProposal {
	switch proposal {
	case conversation.ThreadBoundaryKeep, conversation.ThreadBoundaryNew, conversation.ThreadBoundaryUnknown:
		return proposal
	default:
		return conversation.ThreadBoundaryUnknown
	}
}

func withMioThreadBoundaryAttestation(
	ctx context.Context,
	turn conversation.TurnInput,
	preparation *conversation.ConversationTurnPreparation,
	guard conversation.PreparedChatTurnGuard,
	proposal conversation.ThreadBoundaryProposal,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if preparation == nil || guard == nil {
		return ctx
	}
	attestation := &mioThreadBoundaryAttestation{
		turnID:      string(turn.TurnID()),
		traceID:     string(turn.TraceID()),
		rootTaskID:  string(turn.RootTaskID()),
		messageID:   string(turn.UserMessageID()),
		sessionID:   turn.SessionID(),
		messageHash: sha256.Sum256([]byte(turn.MessageText())),
		proposal:    normalizeMioThreadBoundaryProposal(proposal),
		preparation: preparation,
		guard:       guard,
	}
	return context.WithValue(ctx, mioThreadBoundaryAttestationContextKey{}, attestation)
}

// MioThreadBoundaryProposalFromContext is the conversation owner's read-only
// access point for an attested proposal. The caller's request fields alone do
// not create this context capability.
func MioThreadBoundaryProposalFromContext(ctx context.Context, request conversation.ConversationTurnRequest, resolvedOwner string) (conversation.ThreadBoundaryProposal, bool, error) {
	if ctx == nil {
		return conversation.ThreadBoundaryUnknown, false, nil
	}
	attestation, ok := ctx.Value(mioThreadBoundaryAttestationContextKey{}).(*mioThreadBoundaryAttestation)
	if !ok || attestation == nil {
		return conversation.ThreadBoundaryUnknown, false, nil
	}
	if request.AgentSpeaker != conversation.SpeakerMio || request.Preparation == nil || attestation.preparation == nil || attestation.guard == nil {
		return conversation.ThreadBoundaryUnknown, true, conversation.ErrConversationTurnConflict
	}
	if string(request.TurnID) != attestation.turnID || string(request.TraceID) != attestation.traceID ||
		string(request.RootTaskID) != attestation.rootTaskID || string(request.UserMessageID) != attestation.messageID ||
		request.SessionID != attestation.sessionID || sha256.Sum256([]byte(request.UserMessage)) != attestation.messageHash ||
		resolvedOwner == "" || request.Preparation.TurnID != request.TurnID || request.Preparation.SessionID != request.SessionID ||
		request.Preparation.OwnerID != resolvedOwner || attestation.preparation.TurnID != request.TurnID ||
		attestation.preparation.SessionID != request.SessionID || attestation.preparation.OwnerID != resolvedOwner {
		return conversation.ThreadBoundaryUnknown, true, conversation.ErrConversationTurnConflict
	}
	guardOwner := attestation.guard.OwnerID()
	if guardOwner != "" && guardOwner != resolvedOwner {
		return conversation.ThreadBoundaryUnknown, true, conversation.ErrConversationTurnConflict
	}
	if guardOwner == "" {
		binding, bound := conversation.SessionBindingFromContext(ctx)
		if !bound || binding.Validate() != nil || binding.Audience() != conversation.SessionAudiencePublic ||
			string(binding.SessionID()) != request.SessionID || resolvedOwner != "public:"+request.SessionID {
			return conversation.ThreadBoundaryUnknown, true, conversation.ErrConversationTurnConflict
		}
	}
	if !attestation.guard.MatchesPreparation(attestation.preparation) || !attestation.guard.MatchesPreparation(request.Preparation) {
		return conversation.ThreadBoundaryUnknown, true, conversation.ErrConversationTurnConflict
	}
	if err := attestation.guard.Validate(); err != nil {
		if errors.Is(err, conversation.ErrConversationTurnUnavailable) || errors.Is(err, conversation.ErrConversationTurnConflict) {
			return conversation.ThreadBoundaryUnknown, true, err
		}
		return conversation.ThreadBoundaryUnknown, true, errors.Join(conversation.ErrConversationTurnConflict, err)
	}
	return normalizeMioThreadBoundaryProposal(attestation.proposal), true, nil
}
