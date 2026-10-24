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

func TestCanonicalSessionOwnerBindingIsSetAtCreationAndSurvivesTurnMutation(t *testing.T) {
	id := modulecore.NewSessionID()
	logicalDate := "2026-09-02"
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 9, 2, 1, 2, 3, 0, time.UTC)

	sess, err := NewCanonicalSessionWithOwner(id, "ren", logicalDate, address, createdAt)
	if err != nil {
		t.Fatalf("NewCanonicalSessionWithOwner: %v", err)
	}
	if got := sess.OwnerID(); got != "ren" {
		t.Fatalf("OwnerID() = %q, want ren", got)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "hello", address)
	if err != nil {
		t.Fatal(err)
	}
	sess.AddTurnInput(input.WithSessionID(sess.ID()))
	if got := sess.OwnerID(); got != "ren" {
		t.Fatalf("OwnerID() after AddTurnInput = %q, want immutable owner ren", got)
	}

	restored, err := ReconstructCanonicalSessionWithOwner(id, sess.OwnerID(), logicalDate, address, sess.GetHistory(), nil, createdAt, sess.UpdatedAt())
	if err != nil {
		t.Fatalf("ReconstructCanonicalSessionWithOwner: %v", err)
	}
	if got := restored.OwnerID(); got != "ren" {
		t.Fatalf("restored OwnerID() = %q, want ren", got)
	}
}

func TestCanonicalSessionPublicationIncarnationIsOpaqueStableAndRestored(t *testing.T) {
	id := modulecore.NewSessionID()
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC)
	sess, err := NewCanonicalSession(id, "2026-10-24", address, createdAt)
	if err != nil {
		t.Fatalf("NewCanonicalSession: %v", err)
	}
	incarnation := sess.PublicationIncarnation()
	if err := ValidatePublicationIncarnation(incarnation); err != nil {
		t.Fatalf("created PublicationIncarnation: %v", err)
	}

	sess.SetMemory("turn", "updated")
	if got := sess.PublicationIncarnation(); got != incarnation {
		t.Fatalf("PublicationIncarnation after ordinary Session mutation=%q, want %q", got, incarnation)
	}

	restored, err := ReconstructCanonicalSessionWithPublicationIncarnation(
		id, "2026-10-24", address, nil, nil, createdAt, sess.UpdatedAt(), incarnation,
	)
	if err != nil {
		t.Fatalf("ReconstructCanonicalSessionWithPublicationIncarnation: %v", err)
	}
	if got := restored.PublicationIncarnation(); got != incarnation {
		t.Fatalf("restored PublicationIncarnation=%q, want exact stored value %q", got, incarnation)
	}

	other, err := NewCanonicalSession(id, "2026-10-25", address, createdAt)
	if err != nil {
		t.Fatalf("NewCanonicalSession same-ID new binding: %v", err)
	}
	if other.PublicationIncarnation() == incarnation {
		t.Fatal("new same-ID binding reused the previous publication incarnation")
	}
}

func TestNewReservedV2ErrorPublicationBindingUsesExistingSessionIncarnation(t *testing.T) {
	address, err := conversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC)
	sess, err := NewCanonicalSession(modulecore.NewSessionID(), "2026-10-24", address, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	incarnation := sess.PublicationIncarnation()

	binding, err := NewReservedV2ErrorPublicationBinding(sess, "ren")
	if err != nil {
		t.Fatalf("NewReservedV2ErrorPublicationBinding: %v", err)
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("reserved error publication binding is invalid: %v", err)
	}
	if binding.SessionID() != modulecore.SessionID(sess.ID()) || binding.OwnerID() != "ren" ||
		binding.Audience() != conversation.SessionAudiencePrivate || binding.ChannelAddress() != address ||
		binding.PublicationIncarnation() != incarnation {
		t.Fatalf("binding=%+v, want the reservation owner/address and existing Session incarnation", binding)
	}
	if sess.OwnerID() != "" {
		t.Fatalf("reserved Session OwnerID=%q, want no Session owner mutation", sess.OwnerID())
	}
}

func TestNewReservedV2ErrorPublicationBindingRejectsInvalidReservationIdentity(t *testing.T) {
	createdAt := time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC)
	address, err := conversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	wrongAddress, err := conversation.NewChannelAddress("line", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	owned, err := NewCanonicalSessionWithOwner(modulecore.NewSessionID(), "ren", "2026-10-24", address, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	wrongRoute, err := NewCanonicalSession(modulecore.NewSessionID(), "2026-10-24", wrongAddress, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := NewCanonicalSession(modulecore.NewSessionID(), "2026-10-24", address, createdAt)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		sess    *Session
		ownerID string
	}{
		{name: "missing owner", sess: valid, ownerID: ""},
		{name: "wrong address owner", sess: valid, ownerID: "other"},
		{name: "non viewer route", sess: wrongRoute, ownerID: "ren"},
		{name: "already owner bound", sess: owned, ownerID: "ren"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewReservedV2ErrorPublicationBinding(tc.sess, tc.ownerID); err == nil {
				t.Fatal("NewReservedV2ErrorPublicationBinding error=nil, want invalid reservation rejection")
			}
		})
	}
}

func TestSessionPublicationIncarnationRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"", "not-opaque", "0123456789abcdef0123456789ABCDEf"} {
		if err := ValidatePublicationIncarnation(value); err == nil {
			t.Errorf("ValidatePublicationIncarnation(%q) error=nil, want error", value)
		}
	}
}

func TestCanonicalSessionOwnerBindingRejectsMissingOrUnnormalizedOwner(t *testing.T) {
	id := modulecore.NewSessionID()
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	for _, ownerID := range []string{"", " ", " ren "} {
		if _, err := NewCanonicalSessionWithOwner(id, ownerID, "2026-09-02", address, time.Now().UTC()); err == nil {
			t.Errorf("NewCanonicalSessionWithOwner(%q) error = nil", ownerID)
		}
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
