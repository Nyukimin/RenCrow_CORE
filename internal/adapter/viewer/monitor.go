package viewer

import (
	"context"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

const (
	monitorOfflineAfter          = 120 * time.Second
	monitorMaxLogs               = 2000
	monitorMaxTaskActivityEvents = 200
)

var monitorAgents = []string{"mio", "shiro", "kuro", "midori", "coder1", "coder2", "coder3", "coder4"}

type MonitorStore struct {
	mu                             sync.RWMutex
	visibilityMu                   sync.RWMutex
	publicEventVisibilityPredicate MonitorEventVisibilityPredicate
	logs                           []orchestrator.OrchestratorEvent
	agents                         map[string]AgentSnapshot
	operationalAgents              map[string]AgentSnapshot
	taskActivities                 map[string]*TaskActivitySnapshot
	evidence                       EvidenceLister
	archive                        EventLogReader
}

// MonitorEventVisibilityPredicate decides whether an event may enter or leave
// the public Monitor projection. Implementations must resolve ownership using
// trusted runtime state and return false or an error when visibility is unknown.
type MonitorEventVisibilityPredicate func(context.Context, orchestrator.OrchestratorEvent) (bool, error)

func NewMonitorStore(evidence EvidenceLister, archive EventLogReader) *MonitorStore {
	s := &MonitorStore{
		agents:            make(map[string]AgentSnapshot, len(monitorAgents)),
		operationalAgents: make(map[string]AgentSnapshot),
		taskActivities:    make(map[string]*TaskActivitySnapshot),
		evidence:          evidence,
		archive:           archive,
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

// SetPublicEventVisibilityPredicate installs the trusted runtime visibility
// check used for Monitor event ingestion and RAM/archive reads. Until a
// predicate is installed, event access fails closed.
func (s *MonitorStore) SetPublicEventVisibilityPredicate(predicate MonitorEventVisibilityPredicate) {
	if s == nil {
		return
	}
	s.visibilityMu.Lock()
	s.publicEventVisibilityPredicate = predicate
	s.visibilityMu.Unlock()
}

func (s *MonitorStore) eventIsPublic(ctx context.Context, event orchestrator.OrchestratorEvent) bool {
	if s == nil {
		return false
	}
	s.visibilityMu.RLock()
	predicate := s.publicEventVisibilityPredicate
	s.visibilityMu.RUnlock()
	if predicate == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	visible, err := predicate(ctx, event)
	return err == nil && visible
}
