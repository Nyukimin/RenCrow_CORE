package chat

import "testing"

func TestNormalizeViewerRecipientAllowsPublicChatTargets(t *testing.T) {
	tests := []struct {
		raw  string
		want ViewerRecipient
	}{
		{"mio", ViewerRecipientMio},
		{" shiro ", ViewerRecipientShiro},
		{"KURO", ViewerRecipientKuro},
		{"Midori", ViewerRecipientMidori},
	}
	for _, tt := range tests {
		got, err := NormalizeViewerRecipient(tt.raw)
		if err != nil {
			t.Fatalf("NormalizeViewerRecipient(%q) returned error: %v", tt.raw, err)
		}
		if got != tt.want {
			t.Fatalf("NormalizeViewerRecipient(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestNormalizeViewerRecipientDefaultsToMio(t *testing.T) {
	got, err := NormalizeViewerRecipient(" ")
	if err != nil {
		t.Fatalf("NormalizeViewerRecipient returned error: %v", err)
	}
	if got != ViewerRecipientMio {
		t.Fatalf("default recipient = %q, want %q", got, ViewerRecipientMio)
	}
}

func TestNormalizeViewerRecipientRejectsUnknownTarget(t *testing.T) {
	tests := []string{"worker", "coder", "ops", "heavy", "wild", "unknown"}
	for _, raw := range tests {
		if got, err := NormalizeViewerRecipient(raw); err == nil {
			t.Fatalf("NormalizeViewerRecipient(%q) = %q, want error", raw, got)
		}
	}
}

func TestViewerRecipientDoesNotImplyExecutionRoute(t *testing.T) {
	recipient, err := NormalizeViewerRecipient("shiro")
	if err != nil {
		t.Fatalf("NormalizeViewerRecipient returned error: %v", err)
	}
	if recipient != ViewerRecipientShiro {
		t.Fatalf("recipient = %q, want %q", recipient, ViewerRecipientShiro)
	}
	if route := NormalizeRouteName(string(recipient)); route == RouteWorker {
		t.Fatalf("to=shiro must not imply worker execution route")
	}
}
