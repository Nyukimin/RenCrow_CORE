package conversation

import "errors"

// BoardCommunicationMeetingChannelID is the only channel authorized for the
// first fixed set of CORE and external-agent meeting routes.
const BoardCommunicationMeetingChannelID = "slack_c0c6j86evsr"

// BoardCommunicationMeetingMioPrincipalID binds the legacy Mio inbox actor
// to its authenticated BOARD principal.
const BoardCommunicationMeetingMioPrincipalID = "rencrow-comm"

// BoardCommunicationMeetingRoute describes one explicitly permitted BOARD
// principal-to-principal direction. RecipientActorID is the CORE actor bound
// to an internal recipient; it is empty for external recipients.
type BoardCommunicationMeetingRoute struct {
	ChannelID            string `yaml:"channel_id" json:"channel_id"`
	SenderPrincipalID    string `yaml:"sender_principal_id" json:"sender_principal_id"`
	RecipientActorID     string `yaml:"recipient_actor_id" json:"recipient_actor_id"`
	RecipientPrincipalID string `yaml:"recipient_principal_id" json:"recipient_principal_id"`
}

// Matches reports whether the route permits this exact channel and direction.
func (route BoardCommunicationMeetingRoute) Matches(channelID, senderPrincipalID, recipientPrincipalID string) bool {
	return route.ChannelID != "" && route.SenderPrincipalID != "" && route.RecipientPrincipalID != "" &&
		route.ChannelID == channelID && route.SenderPrincipalID == senderPrincipalID &&
		route.RecipientPrincipalID == recipientPrincipalID
}

// BoardCommunicationMeetingInternalActorID returns the only CORE actor bound
// to an internal participant principal. Lumina is intentionally excluded.
func BoardCommunicationMeetingInternalActorID(principalID string) (string, bool) {
	if principalID == BoardCommunicationMeetingMioPrincipalID {
		return "mio", true
	}
	if principalID == "lumina" || !IsExternalPeerResponderActor(principalID) {
		return "", false
	}
	return principalID, true
}

// IsBoardCommunicationMeetingExternalPrincipal recognizes the three fixed
// external participants used by the initial meeting routes.
func IsBoardCommunicationMeetingExternalPrincipal(principalID string) bool {
	switch principalID {
	case "dots-comm", "grok-comm", "muse-comm":
		return true
	default:
		return false
	}
}

// ValidateBoardCommunicationMeetingRoutes checks the shared route contract
// before configuration-specific profile and channel scopes are applied.
func ValidateBoardCommunicationMeetingRoutes(routes []BoardCommunicationMeetingRoute) error {
	seen := make(map[struct{ channel, sender, recipient string }]struct{}, len(routes))
	for _, route := range routes {
		if route.ChannelID != BoardCommunicationMeetingChannelID ||
			!route.Matches(route.ChannelID, route.SenderPrincipalID, route.RecipientPrincipalID) {
			return errors.New("route must use the fixed meeting channel and nonempty principals")
		}
		if route.SenderPrincipalID == route.RecipientPrincipalID {
			return errors.New("self routes are not allowed")
		}
		_, senderIsInternal := BoardCommunicationMeetingInternalActorID(route.SenderPrincipalID)
		senderIsExternal := IsBoardCommunicationMeetingExternalPrincipal(route.SenderPrincipalID)
		recipientActorID, recipientIsInternal := BoardCommunicationMeetingInternalActorID(route.RecipientPrincipalID)
		recipientIsExternal := IsBoardCommunicationMeetingExternalPrincipal(route.RecipientPrincipalID)
		if (!senderIsInternal && !senderIsExternal) || (!recipientIsInternal && !recipientIsExternal) {
			return errors.New("route principal is not a supported meeting participant")
		}
		if senderIsExternal && recipientIsExternal {
			return errors.New("external-to-external routes are not allowed")
		}
		if recipientIsInternal && route.RecipientActorID != recipientActorID {
			return errors.New("recipient actor does not match the internal recipient principal")
		}
		if recipientIsExternal && route.RecipientActorID != "" {
			return errors.New("external recipients must not have a CORE actor")
		}
		key := struct{ channel, sender, recipient string }{
			channel: route.ChannelID, sender: route.SenderPrincipalID, recipient: route.RecipientPrincipalID,
		}
		if _, exists := seen[key]; exists {
			return errors.New("duplicate directions are not allowed")
		}
		seen[key] = struct{}{}
	}
	return nil
}
