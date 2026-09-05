package session

import (
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNewCanonicalSessionSeparatesOpaqueIDFromRoutingAttributes(t *testing.T) {
	id := modulecore.NewSessionID()
	logicalDate := "2026-09-02"
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}

	sess, err := NewCanonicalSession(id, logicalDate, address, time.Date(2026, 9, 2, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewCanonicalSession: %v", err)
	}
	if got := sess.ID(); got != string(id) {
		t.Fatalf("ID() = %q, want %q", got, id)
	}
	if got := sess.LogicalDate(); got != logicalDate {
		t.Fatalf("LogicalDate() = %q, want %q", got, logicalDate)
	}
	if got := sess.ChannelAddress(); got != address {
		t.Fatalf("ChannelAddress() = %#v, want %#v", got, address)
	}
}

func TestCanonicalSessionRejectsInvalidIdentityAndRoutingAttributes(t *testing.T) {
	validID := modulecore.NewSessionID()
	validAddress, err := conversation.NewChannelAddress("viewer", "ren")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		id      modulecore.SessionID
		date    string
		address conversation.ChannelAddress
	}{
		{name: "legacy id", id: "20260902-line-U123", date: "2026-09-02", address: validAddress},
		{name: "invalid date", id: validID, date: "2026092", address: validAddress},
		{name: "empty address", id: validID, date: "2026-09-02", address: conversation.ChannelAddress{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewCanonicalSession(tt.id, tt.date, tt.address, time.Now()); err == nil {
				t.Fatal("NewCanonicalSession() error = nil")
			}
		})
	}
}

func TestConversationChannelAddressNormalizesAndRejectsAmbiguousValues(t *testing.T) {
	got, err := conversation.NewChannelAddress(" LINE ", " U123 ")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	if got.ChannelType() != "line" || got.ExternalConversationID() != "U123" {
		t.Fatalf("address = %#v", got)
	}
	for _, input := range [][2]string{{"", "U123"}, {"line", ""}} {
		if _, err := conversation.NewChannelAddress(input[0], input[1]); err == nil {
			t.Fatalf("NewChannelAddress(%q, %q) error = nil", input[0], input[1])
		}
	}
}
