package conversation

import (
	"fmt"
	"strings"
	"unicode"
)

// ExternalPeerBinding records the canonical BOARD source tuple attached to an
// accepted external conversation input. WireKind preserves the raw BOARD kind
// exactly, including an empty value.
type ExternalPeerBinding struct {
	BoardInstanceID      string                     `json:"board_instance_id"`
	RecipientPrincipalID string                     `json:"recipient_principal_id"`
	ChannelID            string                     `json:"channel_id"`
	ThreadID             string                     `json:"thread_id"`
	MessageID            string                     `json:"message_id"`
	SenderPrincipalID    string                     `json:"sender_principal_id"`
	ResponderActorID     string                     `json:"responder_actor_id,omitempty"`
	WireKind             string                     `json:"wire_kind"`
	Classification       ExternalPeerClassification `json:"classification"`
	ReplyTo              string                     `json:"reply_to"`
}

const (
	ExternalPeerResponderActorShiro  = "shiro"
	ExternalPeerResponderActorMidori = "midori"
	ExternalPeerResponderActorKuro   = "kuro"
)

// IsExternalPeerResponderActor is the single allowlist for explicitly
// configured BOARD CHAT responders. Mio remains the legacy default and is not
// an explicit responder; Kuro here is a CHAT-only capability, not Heavy.
func IsExternalPeerResponderActor(actorID string) bool {
	switch actorID {
	case ExternalPeerResponderActorShiro, ExternalPeerResponderActorMidori, ExternalPeerResponderActorKuro:
		return true
	default:
		return false
	}
}

// ExternalPeerClassification is CORE's logical classification of a BOARD
// source. It is separate from the source's raw WireKind.
type ExternalPeerClassification string

const (
	ExternalPeerClassificationRequest ExternalPeerClassification = "request"
	// CorrelatedReplyTerminal is assigned only after CORE verifies the saved
	// publication, expected peer, scope tuple, and causation.
	ExternalPeerClassificationCorrelatedReplyTerminal ExternalPeerClassification = "correlated_reply_terminal"
	// UnknownReply describes a reply-shaped source that must be rejected. It is
	// not an accepted-turn classification.
	ExternalPeerClassificationUnknownReply ExternalPeerClassification = "unknown_reply"
)

// ParseExternalPeerBinding reads the complete new-format binding with its one
// optional CORE-derived responder field. It rejects legacy "kind" and all
// unsupported fields so wire kind is never reinterpreted as classification.
func ParseExternalPeerBinding(payload map[string]any) (ExternalPeerBinding, error) {
	raw, ok := payload["external_peer_binding"].(map[string]any)
	if !ok {
		return ExternalPeerBinding{}, fmt.Errorf("external peer binding is required")
	}
	wantFields := map[string]struct{}{
		"board_instance_id": {}, "recipient_principal_id": {}, "channel_id": {}, "thread_id": {},
		"message_id": {}, "sender_principal_id": {}, "wire_kind": {}, "classification": {}, "reply_to": {},
	}
	if len(raw) != len(wantFields) && len(raw) != len(wantFields)+1 {
		return ExternalPeerBinding{}, fmt.Errorf("external peer binding must contain exactly %d fields", len(wantFields))
	}
	for name := range raw {
		if _, ok := wantFields[name]; !ok && name != "responder_actor_id" {
			return ExternalPeerBinding{}, fmt.Errorf("external peer binding field %q is unsupported", name)
		}
	}
	stringField := func(name string) (string, error) {
		value, ok := raw[name].(string)
		if !ok {
			return "", fmt.Errorf("external peer binding %s must be a string", name)
		}
		return value, nil
	}
	var binding ExternalPeerBinding
	fields := []struct {
		name   string
		target *string
	}{
		{"board_instance_id", &binding.BoardInstanceID},
		{"recipient_principal_id", &binding.RecipientPrincipalID},
		{"channel_id", &binding.ChannelID},
		{"thread_id", &binding.ThreadID},
		{"message_id", &binding.MessageID},
		{"sender_principal_id", &binding.SenderPrincipalID},
		{"wire_kind", &binding.WireKind},
		{"reply_to", &binding.ReplyTo},
	}
	for _, field := range fields {
		value, err := stringField(field.name)
		if err != nil {
			return ExternalPeerBinding{}, err
		}
		*field.target = value
	}
	if value, exists := raw["responder_actor_id"]; exists {
		responderActorID, ok := value.(string)
		if !ok {
			return ExternalPeerBinding{}, fmt.Errorf("external peer binding responder_actor_id must be a string")
		}
		if responderActorID == "" {
			return ExternalPeerBinding{}, fmt.Errorf("external peer binding responder_actor_id must be omitted or a supported actor")
		}
		binding.ResponderActorID = responderActorID
	}
	classification, err := stringField("classification")
	if err != nil {
		return ExternalPeerBinding{}, err
	}
	binding.Classification = ExternalPeerClassification(classification)
	if err := binding.Validate(); err != nil {
		return ExternalPeerBinding{}, err
	}
	return binding, nil
}

// Validate checks source identity and field shape without treating a terminal
// classification as proof that its reply target is correlated.
func (binding ExternalPeerBinding) Validate() error {
	for name, value := range map[string]string{
		"recipient_principal_id": binding.RecipientPrincipalID,
		"channel_id":             binding.ChannelID,
		"thread_id":              binding.ThreadID,
		"message_id":             binding.MessageID,
	} {
		if !validExternalPeerReference(value) {
			return fmt.Errorf("external peer binding %s is invalid", name)
		}
	}
	if binding.ReplyTo != "" && !validExternalPeerReference(binding.ReplyTo) {
		return fmt.Errorf("external peer binding reply_to is invalid")
	}
	if !validExternalPeerNamespaceComponent(binding.BoardInstanceID) || !validExternalPeerNamespaceComponent(binding.SenderPrincipalID) {
		return fmt.Errorf("external peer instance and sender must be delimiter-safe namespace components")
	}
	if binding.ResponderActorID != "" {
		if !IsExternalPeerResponderActor(binding.ResponderActorID) {
			return fmt.Errorf("external peer responder actor %q is unsupported", binding.ResponderActorID)
		}
		if binding.ResponderActorID != binding.RecipientPrincipalID {
			return fmt.Errorf("external peer responder actor does not match recipient principal")
		}
	}
	switch binding.Classification {
	case ExternalPeerClassificationRequest, ExternalPeerClassificationCorrelatedReplyTerminal, ExternalPeerClassificationUnknownReply:
		return nil
	default:
		return fmt.Errorf("external peer classification %q is unsupported", binding.Classification)
	}
}

// ValidateNewRequest restricts generation to uncorrelated BOARD requests.
func (binding ExternalPeerBinding) ValidateNewRequest() error {
	if err := binding.Validate(); err != nil {
		return err
	}
	if binding.Classification != ExternalPeerClassificationRequest ||
		(binding.WireKind != "" && binding.WireKind != "request") || binding.ReplyTo != "" {
		return fmt.Errorf("only BOARD requests without reply_to can start an external turn")
	}
	return nil
}

// SourceActorID returns a delimiter-safe provenance identity, never a CORE
// Agent identity.
func (binding ExternalPeerBinding) SourceActorID() (string, error) {
	if err := binding.Validate(); err != nil {
		return "", err
	}
	return "board:" + binding.BoardInstanceID + ":" + binding.SenderPrincipalID, nil
}

// Equal compares the full immutable source binding, including the raw kind and
// logical classification.
func (binding ExternalPeerBinding) Equal(other ExternalPeerBinding) bool {
	return binding == other
}

// ToPayload returns the one canonical new-format payload representation.
func (binding ExternalPeerBinding) ToPayload() map[string]any {
	payload := map[string]any{
		"board_instance_id":      binding.BoardInstanceID,
		"recipient_principal_id": binding.RecipientPrincipalID,
		"channel_id":             binding.ChannelID,
		"thread_id":              binding.ThreadID,
		"message_id":             binding.MessageID,
		"sender_principal_id":    binding.SenderPrincipalID,
		"wire_kind":              binding.WireKind,
		"classification":         string(binding.Classification),
		"reply_to":               binding.ReplyTo,
	}
	if binding.ResponderActorID != "" {
		payload["responder_actor_id"] = binding.ResponderActorID
	}
	return payload
}

// EffectiveResponderActorID returns the explicit CORE responder, or the legacy
// Mio actor when older accepted bindings omit responder_actor_id.
func (binding ExternalPeerBinding) EffectiveResponderActorID() string {
	if binding.ResponderActorID == "" {
		return "mio"
	}
	return binding.ResponderActorID
}

// SameContext compares the tuple that bounds an authorized thread view.
func (binding ExternalPeerBinding) SameContext(other ExternalPeerBinding) bool {
	return binding.BoardInstanceID == other.BoardInstanceID &&
		binding.RecipientPrincipalID == other.RecipientPrincipalID &&
		binding.ResponderActorID == other.ResponderActorID &&
		binding.SenderPrincipalID == other.SenderPrincipalID &&
		binding.ChannelID == other.ChannelID && binding.ThreadID == other.ThreadID
}

func validExternalPeerNamespaceComponent(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validExternalPeerReference(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}
