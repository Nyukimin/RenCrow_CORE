package viewer

import (
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

const (
	monitorOfflineAfter = 120 * time.Second
	monitorMaxLogs      = 2000
	monitorMaxJobEvents = 200
)

var monitorAgents = []string{"mio", "shiro", "kuro", "midori", "coder1", "coder2", "coder3", "coder4"}

type MonitorStore struct {
	mu       sync.RWMutex
	logs     []orchestrator.OrchestratorEvent
	agents   map[string]AgentSnapshot
	jobs     map[string]*JobSnapshot
	evidence EvidenceLister
	archive  EventLogReader
}

func NewMonitorStore(evidence EvidenceLister, archive EventLogReader) *MonitorStore {
	s := &MonitorStore{
		agents:   make(map[string]AgentSnapshot, len(monitorAgents)),
		jobs:     make(map[string]*JobSnapshot),
		evidence: evidence,
		archive:  archive,
	}
	for _, id := range monitorAgents {
		s.agents[id] = AgentSnapshot{
			ID:    id,
			Role:  agentRole(id),
			State: "offline",
		}
	}
	return s
}
