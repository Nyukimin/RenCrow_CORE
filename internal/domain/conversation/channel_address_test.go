package conversation

import "testing"

func TestNewChannelAddressNormalizesValues(t *testing.T) {
	address, err := NewChannelAddress(" LINE ", " U123 ")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	if got, want := address.ChannelType(), "line"; got != want {
		t.Fatalf("ChannelType() = %q, want %q", got, want)
	}
	if got, want := address.ExternalConversationID(), "U123"; got != want {
		t.Fatalf("ExternalConversationID() = %q, want %q", got, want)
	}
	if err := address.Validate(); err != nil {
		t.Fatalf("normalized address Validate() error = %v", err)
	}
}

func TestNewChannelAddressRejectsEmptyValues(t *testing.T) {
	for _, test := range []struct {
		name    string
		channel string
		address string
	}{
		{name: "empty channel", channel: "", address: "U123"},
		{name: "whitespace channel", channel: "  ", address: "U123"},
		{name: "empty address", channel: "line", address: ""},
		{name: "whitespace address", channel: "line", address: "  "},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewChannelAddress(test.channel, test.address); err == nil {
				t.Fatalf("NewChannelAddress(%q, %q) error = nil", test.channel, test.address)
			}
		})
	}
}

func TestChannelAddressValidateRejectsUnnormalizedValues(t *testing.T) {
	for _, test := range []struct {
		name    string
		address ChannelAddress
	}{
		{name: "uppercase channel", address: ChannelAddress{channelType: "LINE", externalConversationID: "U123"}},
		{name: "padded channel", address: ChannelAddress{channelType: " line", externalConversationID: "U123"}},
		{name: "padded external address", address: ChannelAddress{channelType: "line", externalConversationID: " U123"}},
		{name: "empty channel", address: ChannelAddress{externalConversationID: "U123"}},
		{name: "empty external address", address: ChannelAddress{channelType: "line"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.address.Validate(); err == nil {
				t.Fatal("ChannelAddress.Validate() error = nil")
			}
		})
	}
}
