package conversation

import (
	"fmt"
	"strings"
)

// ChannelAddress identifies an external conversation within a channel.
//
// The normalized values are intentionally private so callers must use the
// constructor and accessors rather than constructing routing data ad hoc.
type ChannelAddress struct {
	channelType            string
	externalConversationID string
}

// NewChannelAddress creates a normalized address for an external conversation.
func NewChannelAddress(channelType, externalConversationID string) (ChannelAddress, error) {
	address := ChannelAddress{
		channelType:            strings.ToLower(strings.TrimSpace(channelType)),
		externalConversationID: strings.TrimSpace(externalConversationID),
	}
	if err := address.Validate(); err != nil {
		return ChannelAddress{}, err
	}
	return address, nil
}

// Validate checks that both address components are present and normalized.
func (a ChannelAddress) Validate() error {
	if a.channelType == "" {
		return fmt.Errorf("channel type is required")
	}
	if strings.ToLower(strings.TrimSpace(a.channelType)) != a.channelType {
		return fmt.Errorf("channel type is not normalized")
	}
	if a.externalConversationID == "" {
		return fmt.Errorf("external conversation ID is required")
	}
	if strings.TrimSpace(a.externalConversationID) != a.externalConversationID {
		return fmt.Errorf("external conversation ID is not normalized")
	}
	return nil
}

// ChannelType returns the normalized channel type.
func (a ChannelAddress) ChannelType() string {
	return a.channelType
}

// ExternalConversationID returns the normalized external conversation ID.
func (a ChannelAddress) ExternalConversationID() string {
	return a.externalConversationID
}
