package main

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
)

func TestAgentHeartbeatHealthDoesNotDegradeWithoutScheduledBoardActors(t *testing.T) {
	cfg := &config.Config{}
	cfg.Heartbeat.AgentSchedulerEnabled = true
	cfg.BoardCommunication.Enabled = true
	cfg.BoardCommunication.Responders = []config.BoardCommunicationResponderConfig{{ActorID: "lumina"}}
	deps := &Dependencies{
		agentHeartbeat:        agentheartbeat.New(agentHeartbeatActorIDs, agentheartbeat.Sources{}, nil),
		agentHeartbeatStarted: true,
	}

	_, message, _ := agentHeartbeatHealth(cfg, deps)
	if !strings.Contains(message, "BOARD enabled; no scheduled CORE actors") {
		t.Fatalf("agentHeartbeatHealth() message = %q, want BOARD-only responders identified without a false 0/0 degraded state", message)
	}
	if strings.Contains(message, "BOARD degraded (0/0") {
		t.Fatalf("agentHeartbeatHealth() message = %q, must not mark intentional BOARD-only event-driven responders degraded", message)
	}
}

func TestAgentHeartbeatActorsIncludeEightExactChatActors(t *testing.T) {
	want := []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"}
	if len(agentHeartbeatActorIDs) != len(want) {
		t.Fatalf("agentHeartbeatActorIDs length=%d, want %d actors: %v", len(agentHeartbeatActorIDs), len(want), agentHeartbeatActorIDs)
	}
	for _, actor := range want {
		found := false
		for _, got := range agentHeartbeatActorIDs {
			if got == actor {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("agentHeartbeatActorIDs=%v does not include %q", agentHeartbeatActorIDs, actor)
		}
		if !isAgentHeartbeatActor(actor) {
			t.Errorf("isAgentHeartbeatActor(%q) = false, want true", actor)
		}
	}
	for _, actor := range []string{"lumina", "worker", "MIO", "aka "} {
		if isAgentHeartbeatActor(actor) {
			t.Errorf("isAgentHeartbeatActor(%q) = true, want false", actor)
		}
	}
}
