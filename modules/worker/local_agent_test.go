package worker

import (
	"errors"
	"testing"
)

func TestLocalAgentEnabled(t *testing.T) {
	availability := LocalAgentAvailability{Coder1: true, Coder3: true}

	if !LocalAgentEnabled("mio", availability) || !LocalAgentEnabled("shiro", availability) {
		t.Fatal("mio and shiro should always be local-enabled")
	}
	if !LocalAgentEnabled("coder1", availability) || !LocalAgentEnabled("coder3", availability) {
		t.Fatal("configured coders should be local-enabled")
	}
	if LocalAgentEnabled("coder2", availability) || LocalAgentEnabled("coder4", availability) {
		t.Fatal("nil coders should be disabled")
	}
	if !LocalAgentEnabled("custom", availability) {
		t.Fatal("unknown local agents should remain enabled")
	}
}

func TestDistributedAgentAvailable(t *testing.T) {
	if !DistributedAgentAvailable("coder1", true, false) {
		t.Fatal("local transport should make agent available")
	}
	if !DistributedAgentAvailable("coder3", false, true) {
		t.Fatal("ssh transport should make agent available")
	}
	if DistributedAgentAvailable("coder2", false, false) {
		t.Fatal("agent without transports should be unavailable")
	}
}

func TestFormatAgentUnavailableReason(t *testing.T) {
	got := FormatAgentUnavailableReason("ssh connect failed", errors.New("connection reset by peer"))
	want := "ssh connect failed: connection reset by peer"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestLocalCoderReplyTarget(t *testing.T) {
	if got := LocalCoderReplyTarget("Shiro"); got != "mio" {
		t.Fatalf("shiro reply target = %q", got)
	}
	if got := LocalCoderReplyTarget(" aka "); got != "aka" {
		t.Fatalf("normal reply target = %q", got)
	}
}
