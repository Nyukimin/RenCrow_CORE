package channel

import (
	"testing"
	"time"
)

func TestBuildSessionID(t *testing.T) {
	now := time.Date(2026, 3, 9, 1, 2, 3, 0, time.UTC)
	got := BuildSessionID(now, "line", "U123")
	if got != "20260309-line-U123" {
		t.Fatalf("unexpected session id: %s", got)
	}
}

func TestNormalizeEntryPlatformChannel(t *testing.T) {
	tests := []struct {
		name         string
		platform     string
		channel      string
		wantPlatform string
		wantChannel  string
	}{
		{name: "CLI defaults to local", platform: "cli", channel: "", wantPlatform: "cli", wantChannel: "local"},
		{name: "Chrome defaults to local", platform: "chrome", channel: "", wantPlatform: "chrome", wantChannel: "local"},
		{name: "Viewer defaults to viewer", platform: "viewer", channel: "", wantPlatform: "viewer", wantChannel: "viewer"},
		{name: "Line defaults to line", platform: "line", channel: "", wantPlatform: "line", wantChannel: "line"},
		{name: "Unknown platform fallback", platform: "unknown", channel: "", wantPlatform: "viewer", wantChannel: "viewer"},
		{name: "Explicit channel wins", platform: "viewer", channel: "discord", wantPlatform: "viewer", wantChannel: "discord"},
		{name: "Invalid explicit channel falls back", platform: "cli", channel: "weird", wantPlatform: "cli", wantChannel: "local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotP, gotC := NormalizeEntryPlatformChannel(tt.platform, tt.channel)
			if gotP != tt.wantPlatform || gotC != tt.wantChannel {
				t.Fatalf("got (%s,%s), want (%s,%s)", gotP, gotC, tt.wantPlatform, tt.wantChannel)
			}
		})
	}
}
