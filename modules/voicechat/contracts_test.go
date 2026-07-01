package voicechat

import "testing"

func TestNormalizeVoiceInputModeDefaultsToSTTPrimary(t *testing.T) {
	for _, raw := range []string{"", "unknown", "stt_primary"} {
		if got := NormalizeVoiceInputMode(raw); got != VoiceInputModeSTTPrimary {
			t.Fatalf("NormalizeVoiceInputMode(%q) = %q, want %q", raw, got, VoiceInputModeSTTPrimary)
		}
	}
}

func TestNormalizeVoiceInputModePreservesKnownModes(t *testing.T) {
	if got := NormalizeVoiceInputMode("vds_sub"); got != VoiceInputModeVDSSub {
		t.Fatalf("unexpected mode: %q", got)
	}
	if got := NormalizeVoiceInputMode("parallel_caption"); got != VoiceInputModeParallelCaption {
		t.Fatalf("unexpected mode: %q", got)
	}
}

func TestWebSocketRoutePathsIncludePrimaryAndAlias(t *testing.T) {
	if len(WebSocketRoutePaths) != 2 {
		t.Fatalf("expected 2 route paths, got %d", len(WebSocketRoutePaths))
	}
	if WebSocketRoutePaths[0] != RoutePathPrimary || WebSocketRoutePaths[1] != RoutePathAlias {
		t.Fatalf("unexpected route paths: %#v", WebSocketRoutePaths)
	}
}
