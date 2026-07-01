package conversation

import (
	"math"
	"time"
)

// AgentStatus はAgentのタスク状態を管理
type AgentStatus struct {
	AgentName      string         `json:"agent_name"`
	IsIdle         bool           `json:"is_idle"`
	LastTaskTime   time.Time      `json:"last_task"`
	CurrentTask    string         `json:"current_task"`
	ConversationOK bool           `json:"conversation_ok"`
	KPI            map[string]int `json:"kpi,omitempty"`
	Level          int            `json:"level"`
}

// NewAgentStatus は新しいAgentStatusを生成
func NewAgentStatus(agentName string) *AgentStatus {
	return &AgentStatus{
		AgentName:      agentName,
		IsIdle:         true,
		ConversationOK: agentName == "mio", // Mioは常時参加可能
		KPI:            map[string]int{},
	}
}

func (s *AgentStatus) ApplyKPI(name string, delta int) {
	if s.KPI == nil {
		s.KPI = map[string]int{}
	}
	next := s.KPI[name] + delta
	if next < 0 {
		next = 0
	}
	s.KPI[name] = next
	s.Level = int(math.Floor(math.Sqrt(float64(s.TotalKPI()) / 10.0)))
}

func (s *AgentStatus) TotalKPI() int {
	total := 0
	for _, value := range s.KPI {
		if value > 0 {
			total += value
		}
	}
	return total
}

// CanJoinConversation は会話参加可能かを判定
func (s *AgentStatus) CanJoinConversation() bool {
	// Chat（Mio）は常時参加可能
	if s.AgentName == "mio" {
		return true
	}

	// Worker（Shiro）はタスクが空いている時のみ
	if s.AgentName == "shiro" {
		return s.IsIdle && s.CurrentTask == ""
	}

	// Coder（将来）: 条件未定
	return false
}
