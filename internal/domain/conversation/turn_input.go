package conversation

import (
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// TurnInput is the canonical value object for one user conversation input.
// It carries the complete identity assigned at the conversation boundary and
// remains immutable through its With... methods.
type TurnInput struct {
	rootTaskID      modulecore.TaskID
	turnID          modulecore.TurnID
	traceID         modulecore.TraceID
	userMessageID   modulecore.MessageID
	agentMessageID  modulecore.MessageID
	messageText     string
	channelAddress  ChannelAddress
	sessionID       string
	attachments     []attachment.Attachment
	viewerRecipient string
	forcedRoute     routing.Route
	route           routing.Route
}

// NewTurnInput creates a user input with a supplied root task and fresh turn,
// trace, user-message, and agent-message identities.
func NewTurnInput(rootTaskID modulecore.TaskID, messageText string, address ChannelAddress) (TurnInput, error) {
	return ReconstructTurnInput(
		rootTaskID,
		modulecore.NewTurnID(),
		modulecore.NewTraceID(),
		modulecore.NewMessageID(),
		modulecore.NewMessageID(),
		messageText,
		address,
	)
}

// ReconstructTurnInput restores an input whose canonical identities were
// already assigned at ingress or persisted by the session owner.
func ReconstructTurnInput(
	rootTaskID modulecore.TaskID,
	turnID modulecore.TurnID,
	traceID modulecore.TraceID,
	userMessageID modulecore.MessageID,
	agentMessageID modulecore.MessageID,
	messageText string,
	address ChannelAddress,
) (TurnInput, error) {
	input := TurnInput{
		rootTaskID:      rootTaskID,
		turnID:          turnID,
		traceID:         traceID,
		userMessageID:   userMessageID,
		agentMessageID:  agentMessageID,
		messageText:     messageText,
		channelAddress:  address,
		attachments:     nil,
		viewerRecipient: "",
		forcedRoute:     "",
		route:           "",
	}
	if err := input.Validate(); err != nil {
		return TurnInput{}, err
	}
	return input, nil
}

// Validate checks the five canonical identities and channel address.
// Optional input fields may remain empty during the input lifecycle.
func (t TurnInput) Validate() error {
	if err := t.rootTaskID.Validate(); err != nil {
		return fmt.Errorf("root task ID is invalid: %w", err)
	}
	if err := t.turnID.Validate(); err != nil {
		return fmt.Errorf("turn ID is invalid: %w", err)
	}
	if err := t.traceID.Validate(); err != nil {
		return fmt.Errorf("trace ID is invalid: %w", err)
	}
	if err := t.userMessageID.Validate(); err != nil {
		return fmt.Errorf("user message ID is invalid: %w", err)
	}
	if err := t.agentMessageID.Validate(); err != nil {
		return fmt.Errorf("agent message ID is invalid: %w", err)
	}
	if t.userMessageID == t.agentMessageID {
		return fmt.Errorf("user and agent message IDs must differ")
	}
	if err := t.channelAddress.Validate(); err != nil {
		return fmt.Errorf("channel address is invalid: %w", err)
	}
	return nil
}

// RootTaskID returns the root task identity for this input.
func (t TurnInput) RootTaskID() modulecore.TaskID {
	return t.rootTaskID
}

// TurnID returns the conversation turn identity for this input.
func (t TurnInput) TurnID() modulecore.TurnID {
	return t.turnID
}

// TraceID returns the causal trace identity for this input.
func (t TurnInput) TraceID() modulecore.TraceID {
	return t.traceID
}

// UserMessageID returns the user message identity for this input.
func (t TurnInput) UserMessageID() modulecore.MessageID {
	return t.userMessageID
}

// AgentMessageID returns the agent message identity reserved for this input.
func (t TurnInput) AgentMessageID() modulecore.MessageID {
	return t.agentMessageID
}

// MessageText returns the input message text.
func (t TurnInput) MessageText() string {
	return t.messageText
}

// ChannelAddress returns the external conversation routing address.
func (t TurnInput) ChannelAddress() ChannelAddress {
	return t.channelAddress
}

// SessionID returns the associated session identity, when assigned.
func (t TurnInput) SessionID() string {
	return t.sessionID
}

// Attachments returns a copy of the user-provided attachments.
func (t TurnInput) Attachments() []attachment.Attachment {
	return append([]attachment.Attachment(nil), t.attachments...)
}

// ViewerRecipient returns the requested Viewer recipient, when assigned.
func (t TurnInput) ViewerRecipient() string {
	return t.viewerRecipient
}

// ForcedRoute returns the explicitly requested route, when assigned.
func (t TurnInput) ForcedRoute() routing.Route {
	return t.forcedRoute
}

// Route returns the route selected for this input.
func (t TurnInput) Route() routing.Route {
	return t.route
}

// WithMessageText returns a copy with replacement input text.
func (t TurnInput) WithMessageText(messageText string) TurnInput {
	t.messageText = messageText
	return t
}

// WithSessionID returns a copy with the associated session identity.
func (t TurnInput) WithSessionID(sessionID string) TurnInput {
	t.sessionID = sessionID
	return t
}

// WithAttachments returns a copy with copied user attachments.
func (t TurnInput) WithAttachments(attachments []attachment.Attachment) TurnInput {
	t.attachments = append([]attachment.Attachment(nil), attachments...)
	return t
}

// WithViewerRecipient returns a copy with the requested Viewer recipient.
func (t TurnInput) WithViewerRecipient(recipient string) TurnInput {
	t.viewerRecipient = recipient
	return t
}

// WithForcedRoute returns a copy with an explicit route.
func (t TurnInput) WithForcedRoute(route routing.Route) TurnInput {
	t.forcedRoute = route
	return t
}

// WithRoute returns a copy with the selected route.
func (t TurnInput) WithRoute(route routing.Route) TurnInput {
	t.route = route
	return t
}

// HasForcedRoute reports whether an explicit route was assigned.
func (t TurnInput) HasForcedRoute() bool {
	return t.forcedRoute != ""
}
