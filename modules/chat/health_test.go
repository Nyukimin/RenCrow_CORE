package chat

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

func TestBuildServiceHealth(t *testing.T) {
	got := BuildServiceHealth(ServiceHealthSnapshot{Ready: true})
	if got.Module != "chat" || got.Status != core.HealthReady || !got.Ready || got.Detail != "legacy orchestrator processor configured" {
		t.Fatalf("unexpected ready health: %+v", got)
	}

	got = BuildServiceHealth(ServiceHealthSnapshot{})
	if got.Module != "chat" || got.Status != core.HealthDown || got.Ready || got.Detail != "chat processor is nil" {
		t.Fatalf("unexpected unavailable health: %+v", got)
	}
}
