package worker

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

func TestBuildExecutorHealth(t *testing.T) {
	got := BuildExecutorHealth(ExecutorHealthSnapshot{Ready: true})
	if got.Module != "worker" || got.Status != core.HealthReady || !got.Ready || got.Detail != "worker execution service configured" {
		t.Fatalf("unexpected ready health: %+v", got)
	}

	got = BuildExecutorHealth(ExecutorHealthSnapshot{})
	if got.Module != "worker" || got.Status != core.HealthDown || got.Ready || got.Detail != "worker execution service is nil" {
		t.Fatalf("unexpected unavailable health: %+v", got)
	}
}
