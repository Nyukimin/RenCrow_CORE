package conversationruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrExternalTurnJournalRequired  = errors.New("external conversation requires the typed atomic journal")
	ErrExternalTriggerViewerRefused = errors.New("Viewer processing refuses external BOARD triggers")
)

// TurnStatusPublicationPending means the canonical response and private
// publication intent are committed; it does not mean BOARD delivered it.
const TurnStatusPublicationPending TurnStatus = "publication_pending"

type ExternalFailureStage string

const (
	ExternalFailureStageReadView          ExternalFailureStage = "read_view"
	ExternalFailureStagePromptComposition ExternalFailureStage = "prompt_composition"
	ExternalFailureStageGeneration        ExternalFailureStage = "generation"
)

// ExternalTurnLookup is the complete scope required to recover one accepted
// external response without authorizing a new generation.
type ExternalTurnLookup struct {
	OwnerID           string
	SessionID         modulecore.SessionID
	TurnID            modulecore.TurnID
	GenerationEpoch   int64
	OperationID       string
	SourceBinding     domainconversation.ExternalPeerBinding
	SourceFingerprint string
}

// ExternalReplyTarget is the resolved destination retained for publication.
type ExternalReplyTarget struct {
	BoardInstanceID string
	CorePrincipalID string
	PeerPrincipalID string
	ChannelID       string
	ThreadID        string
	ReplyTo         string
}

// ExternalPublicationIntent is private persistence metadata. It points at the
// canonical response message instead of carrying a second copy of its body.
type ExternalPublicationIntent struct {
	CanonicalResponseMessageID modulecore.MessageID
	Target                     ExternalReplyTarget
	StableLogicalKey           string
}

// ExternalCommitBundle is the typed atomic commit request for a response and
// its private BOARD publication intent.
type ExternalCommitBundle struct {
	ExpectedLookup    ExternalTurnLookup
	ResultState       string
	SafeError         bool
	FailureStage      ExternalFailureStage
	CanonicalEvents   []modulecore.EventEnvelope
	MessageIndexes    []MessageIndex
	InvocationReceipt agent.InvocationReceipt
	PublicationIntent ExternalPublicationIntent
}

// ExternalCommittedBundle is the typed result returned by the external journal.
type ExternalCommittedBundle struct {
	Lookup            ExternalTurnLookup
	ResultState       string
	SafeError         bool
	FailureStage      ExternalFailureStage
	CanonicalEvents   []modulecore.EventEnvelope
	MessageIndexes    []MessageIndex
	InvocationReceipt agent.InvocationReceipt
	PublicationIntent ExternalPublicationIntent
}

// ExternalTurnJournal atomically commits or recovers an external response and
// its private publication intent. It is deliberately separate from TurnJournal
// so a legacy adapter cannot discard the external intent.
type ExternalTurnJournal interface {
	CommitExternalTurnBundle(context.Context, ExternalCommitBundle) (ExternalCommittedBundle, error)
	LoadExternalCommittedOutcome(context.Context, ExternalTurnLookup) (ExternalCommittedBundle, bool, error)
}

// ValidateExternalCommittedBundle exposes the runtime's exact external result
// validator to the persistence adapter so both boundaries enforce one contract.
func ValidateExternalCommittedBundle(bundle ExternalCommittedBundle, expected ExternalTurnLookup, accepted AcceptedTurn) error {
	return validateExternalCommittedBundle(bundle, expected, accepted)
}

func validateConfiguredExternalResponderActorID(actorID string) error {
	if actorID != "" && !domainconversation.IsExternalPeerResponderActor(actorID) {
		return errors.New("external conversation responder actor must name a supported responder when explicitly configured")
	}
	return nil
}

func validateRuntimeExternalPeerBinding(request ReadViewRequest, configuredResponderActorID string) error {
	if err := validateConfiguredExternalResponderActorID(configuredResponderActorID); err != nil {
		return err
	}
	if err := validateAcceptedExternalPeerInput(request, request.Accepted.InputEvent); err != nil {
		return err
	}
	binding, err := domainconversation.ParseExternalPeerBinding(request.Accepted.InputEvent.Payload)
	if err != nil {
		return fmt.Errorf("external peer responder binding is invalid: %w", err)
	}
	expectedResponderActorID := configuredResponderActorID
	if expectedResponderActorID == "" {
		expectedResponderActorID = conversationActorMio
	}
	if binding.EffectiveResponderActorID() != expectedResponderActorID {
		return errors.New("external peer responder binding does not match the trusted runtime actor and recipient principal")
	}
	return nil
}
