package conversation

import (
	"context"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func bindingTestAddress(t *testing.T, channel, conversationID string) ChannelAddress {
	t.Helper()
	address, err := NewChannelAddress(channel, conversationID)
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	return address
}

func TestSessionBindingValidatesExplicitAudienceAndOwner(t *testing.T) {
	tests := []struct {
		name     string
		audience SessionAudience
		ownerID  string
		wantErr  bool
	}{
		{name: "public without owner", audience: SessionAudiencePublic},
		{name: "private with owner", audience: SessionAudiencePrivate, ownerID: "ren"},
		{name: "public cannot claim owner", audience: SessionAudiencePublic, ownerID: "ren", wantErr: true},
		{name: "private requires owner", audience: SessionAudiencePrivate, wantErr: true},
		{name: "private owner must be normalized", audience: SessionAudiencePrivate, ownerID: " ren", wantErr: true},
		{name: "unknown audience", audience: SessionAudience("internal"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding, err := NewSessionBinding(modulecore.NewSessionID(), bindingTestAddress(t, "viewer", "chat"), tt.audience, tt.ownerID, 0)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewSessionBinding error=%v, wantErr=%v", err, tt.wantErr)
			}
			if err == nil {
				if err := binding.Validate(); err != nil {
					t.Fatalf("Validate: %v", err)
				}
				if binding.BindingGeneration() != BindingGenerationUnavailable {
					t.Fatalf("binding generation=%d, want unavailable generation 0", binding.BindingGeneration())
				}
			}
		})
	}
}

func TestSessionBindingContextOverwritesEarlierBinding(t *testing.T) {
	address := bindingTestAddress(t, "viewer", "cmd:ren")
	first, err := NewSessionBinding(modulecore.NewSessionID(), address, SessionAudiencePrivate, "ren", 0)
	if err != nil {
		t.Fatalf("NewSessionBinding first: %v", err)
	}
	secondAddress := bindingTestAddress(t, "viewer", "viewer-user")
	second, err := NewSessionBinding(modulecore.NewSessionID(), secondAddress, SessionAudiencePublic, "", 0)
	if err != nil {
		t.Fatalf("NewSessionBinding second: %v", err)
	}

	ctx := WithSessionBinding(context.Background(), first)
	ctx = WithSessionBinding(ctx, second)
	observed, found := SessionBindingFromContext(ctx)
	if !found {
		t.Fatal("SessionBindingFromContext did not return the latest binding")
	}
	if observed.SessionID() != second.SessionID() || observed.ChannelAddress() != secondAddress ||
		observed.Audience() != SessionAudiencePublic || observed.OwnerID() != "" {
		t.Fatalf("observed binding=%+v, want latest public binding for %q", observed, second.SessionID())
	}
	if _, found := SessionBindingFromContext(WithSessionBinding(ctx, SessionBinding{})); found {
		t.Fatal("invalid replacement binding left the earlier binding visible")
	}
}

func TestSessionBindingCarriesValidatedPublicationIncarnation(t *testing.T) {
	address := bindingTestAddress(t, "viewer", "cmd:ren")
	incarnation := "0123456789abcdef0123456789abcdef"
	binding, err := NewSessionBindingWithPublicationIncarnation(
		modulecore.NewSessionID(), address, SessionAudiencePrivate, "ren", 7, incarnation,
	)
	if err != nil {
		t.Fatalf("NewSessionBindingWithPublicationIncarnation: %v", err)
	}
	if err := binding.ValidateForPublication(); err != nil {
		t.Fatalf("ValidateForPublication: %v", err)
	}
	if got := binding.PublicationIncarnation(); got != incarnation {
		t.Fatalf("PublicationIncarnation()=%q, want %q", got, incarnation)
	}

	unresolved, err := NewSessionBinding(modulecore.NewSessionID(), address, SessionAudiencePublic, "", 0)
	if err != nil {
		t.Fatalf("NewSessionBinding unresolved: %v", err)
	}
	if err := unresolved.ValidateForPublication(); err == nil {
		t.Fatal("ValidateForPublication accepted a binding without a publication incarnation")
	}
	if _, err := NewSessionBindingWithPublicationIncarnation(modulecore.NewSessionID(), address, SessionAudiencePublic, "", 0, " "); err == nil {
		t.Fatal("NewSessionBindingWithPublicationIncarnation accepted an empty incarnation")
	}
}
