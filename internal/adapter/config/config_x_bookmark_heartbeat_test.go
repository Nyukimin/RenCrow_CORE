package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeXBookmarkHeartbeatConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "server:\n  port: 8080\nheartbeat:\n" + body
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigXBookmarkHeartbeatDefaults(t *testing.T) {
	path := writeXBookmarkHeartbeatConfig(t, "  enabled: true\n  x_bookmarks:\n    enabled: true\n    output_root: /var/tmp/rencrow-x-bookmarks\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	x := cfg.Heartbeat.XBookmarks
	if x.IntervalMinutes != 360 || x.TimeoutMinutes != 90 || x.Command != "rencrow-x-bookmarks" || x.MaxScrollsValue() != 100 || !x.RunOnStartEnabled() {
		t.Fatalf("x bookmark heartbeat defaults=%+v", x)
	}
}

func TestLoadConfigAllowsZeroXBookmarkScrolls(t *testing.T) {
	path := writeXBookmarkHeartbeatConfig(t, "  enabled: true\n  x_bookmarks:\n    enabled: true\n    output_root: /var/tmp/rencrow-x-bookmarks\n    max_scrolls: 0\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Heartbeat.XBookmarks.MaxScrollsValue(); got != 0 {
		t.Fatalf("max_scrolls=%d, want 0", got)
	}
}

func TestLoadConfigRejectsXBookmarkHeartbeatWithoutHeartbeat(t *testing.T) {
	path := writeXBookmarkHeartbeatConfig(t, "  enabled: false\n  x_bookmarks:\n    enabled: true\n    output_root: /var/tmp/rencrow-x-bookmarks\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "requires heartbeat.enabled=true") {
		t.Fatalf("LoadConfig error=%v", err)
	}
}

func TestLoadConfigRejectsUnsafeXBookmarkHeartbeatSettings(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "relative output", body: "    output_root: relative/path\n", want: "output_root must be an absolute path"},
		{name: "short interval", body: "    output_root: /var/tmp/x\n    interval_minutes: 10\n", want: "interval_minutes must be between 60 and 10080"},
		{name: "long timeout", body: "    output_root: /var/tmp/x\n    timeout_minutes: 181\n", want: "timeout_minutes must be between 1 and 180"},
		{name: "scroll limit", body: "    output_root: /var/tmp/x\n    max_scrolls: 1001\n", want: "max_scrolls must be between 0 and 1000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeXBookmarkHeartbeatConfig(t, "  enabled: true\n  x_bookmarks:\n    enabled: true\n"+tc.body)
			if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadConfig error=%v, want %q", err, tc.want)
			}
		})
	}
}
