package conversation

import (
	"context"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// GetAgentStatus は Agent の状態を取得
func (r *RealConversationManager) GetAgentStatus(ctx context.Context, agentName string) (*domconv.AgentStatus, error) {
	if r.agentStatuses != nil {
		if status, ok := r.agentStatuses[agentName]; ok {
			cp := *status
			cp.KPI = copyKPI(status.KPI)
			return &cp, nil
		}
	}
	return domconv.NewAgentStatus(agentName), nil
}

// UpdateAgentStatus は Agent の状態を更新
func (r *RealConversationManager) UpdateAgentStatus(_ context.Context, status *domconv.AgentStatus) error {
	if status == nil {
		return nil
	}
	if r.agentStatuses == nil {
		r.agentStatuses = map[string]*domconv.AgentStatus{}
	}
	cp := *status
	cp.KPI = copyKPI(status.KPI)
	r.agentStatuses[status.AgentName] = &cp
	return nil
}

func copyKPI(in map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
