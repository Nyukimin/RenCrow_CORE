package viewer

import (
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func (s *MonitorStore) OnEvent(ev orchestrator.OrchestratorEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logs = append(s.logs, ev)
	if len(s.logs) > monitorMaxLogs {
		s.logs = s.logs[len(s.logs)-monitorMaxLogs:]
	}

	s.reduceAgents(ev)
	s.reduceJobs(ev)
}

func (s *MonitorStore) SetAgentUnavailable(id, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	s.patchAgent(id, AgentSnapshot{
		State:     "unavailable",
		LastEvent: "agent.unavailable",
		Preview:   shortText(reason, 120),
		Reason:    shortText(reason, 160),
		UpdatedAt: now,
	})
}
