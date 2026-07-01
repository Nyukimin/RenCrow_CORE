package conversation

import (
	"testing"
)

func TestNewAgentStatus(t *testing.T) {
	status := NewAgentStatus("mio")

	if status.AgentName != "mio" {
		t.Errorf("Expected agent name 'mio', got '%s'", status.AgentName)
	}

	if !status.IsIdle {
		t.Error("Expected new agent to be idle")
	}

	if !status.ConversationOK {
		t.Error("Expected Mio to be conversation-ready")
	}

	if status.CurrentTask != "" {
		t.Errorf("Expected empty current task, got '%s'", status.CurrentTask)
	}
}

func TestNewAgentStatusNonMio(t *testing.T) {
	status := NewAgentStatus("shiro")

	if status.AgentName != "shiro" {
		t.Errorf("Expected agent name 'shiro', got '%s'", status.AgentName)
	}

	if !status.IsIdle {
		t.Error("Expected new agent to be idle")
	}

	if status.ConversationOK {
		t.Error("Expected non-Mio agent to not be conversation-ready by default")
	}
}

func TestCanJoinConversation_Mio(t *testing.T) {
	status := NewAgentStatus("mio")

	// Mioは常時参加可能
	if !status.CanJoinConversation() {
		t.Error("Expected Mio to always be able to join conversation")
	}

	// タスク中でも参加可能
	status.IsIdle = false
	status.CurrentTask = "processing"
	if !status.CanJoinConversation() {
		t.Error("Expected Mio to join conversation even during task")
	}
}

func TestCanJoinConversation_Shiro_Idle(t *testing.T) {
	status := NewAgentStatus("shiro")

	// Shiroはアイドル状態のみ参加可能
	if !status.CanJoinConversation() {
		t.Error("Expected idle Shiro to be able to join conversation")
	}
}

func TestCanJoinConversation_Shiro_Busy(t *testing.T) {
	status := NewAgentStatus("shiro")
	status.IsIdle = false
	status.CurrentTask = "executing command"

	// Shiroはビジー時は参加不可
	if status.CanJoinConversation() {
		t.Error("Expected busy Shiro to not be able to join conversation")
	}
}

func TestCanJoinConversation_Shiro_HasTask(t *testing.T) {
	status := NewAgentStatus("shiro")
	status.CurrentTask = "pending task"

	// Shiroはタスクがある場合は参加不可
	if status.CanJoinConversation() {
		t.Error("Expected Shiro with task to not be able to join conversation")
	}
}

func TestCanJoinConversation_OtherAgents(t *testing.T) {
	agents := []string{"aka", "ao", "gin"}

	for _, agentName := range agents {
		status := NewAgentStatus(agentName)

		// Coder系はデフォルトで参加不可（条件未定）
		if status.CanJoinConversation() {
			t.Errorf("Expected %s to not be able to join conversation by default", agentName)
		}
	}
}

func TestAgentStatusApplyKPIUpdatesLevel(t *testing.T) {
	status := NewAgentStatus("mio")
	status.ApplyKPI("user_thumbs_up", 6)
	status.ApplyKPI("recall_success", 4)

	if status.KPI["user_thumbs_up"] != 6 || status.KPI["recall_success"] != 4 {
		t.Fatalf("unexpected KPI map: %+v", status.KPI)
	}
	if status.TotalKPI() != 10 {
		t.Fatalf("expected total KPI 10, got %d", status.TotalKPI())
	}
	if status.Level != 1 {
		t.Fatalf("expected level 1 at total KPI 10, got %d", status.Level)
	}
	status.ApplyKPI("search_success", -100)
	if status.KPI["search_success"] != 0 {
		t.Fatalf("negative KPI should clamp at 0, got %+v", status.KPI)
	}
}
