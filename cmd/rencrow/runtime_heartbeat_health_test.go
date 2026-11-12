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
	cfg.BoardCommunication.Responders = []config.BoardCommunicationResponderConfig{{ActorID: "aka"}}
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
