package viewer

import (
	"encoding/json"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
)

func classifyJobPhase(ev orchestrator.OrchestratorEvent, current *JobSnapshot) (string, string) {
	from := strings.ToLower(strings.TrimSpace(ev.From))
	to := strings.ToLower(strings.TrimSpace(ev.To))
	content := ev.Content
	switch ev.Type {
	case "message.received":
		return "received", monitorAgentOrDefault(to, "mio")
	case "routing.decision":
		return "routing", valueOr(current.Owner, "mio")
	case "agent.delegate":
		return "delegating", valueOr(to, current.Owner)
	case "agent.dispatch":
		return "delegating", valueOr(to, current.Owner)
	case "agent.thinking":
		return "chatting", valueOr(from, current.Owner)
	case "agent.waiting":
		return "waiting", valueOr(from, current.Owner)
	case "worker.retry_request":
		return "retrying", valueOr(to, "coder1")
	case "worker.request":
		return "worker_verifying", "worker"
	case "worker.result":
		return "reporting", "shiro"
	case "worker.classified_failure", "agent.error", "mailbox.error":
		return "error", valueOr(from, current.Owner)
	case "entry.stage":
		switch terminalOutcomeFromEntryStage(content) {
		case "ok":
			return "done", "system"
		case "failed", "blocked", "cancelled":
			return "error", "system"
		}
		switch strings.ToLower(strings.TrimSpace(content)) {
		case "received":
			return "received", "mio"
		case "contract_ready", "planning":
			return "planning", "mio"
		case "applying":
			return "applying", "worker"
		case "verifying":
			return "worker_verifying", "worker"
		}
	case "mailbox.sent":
		return "queued", valueOr(to, current.Owner)
	case "mailbox.received":
		return "processing", valueOr(from, current.Owner)
	case "agent.start":
		if to == "shiro" {
			if strings.Contains(content, "Worker実行") || strings.Contains(content, "Patch") || strings.Contains(content, "整形") {
				return "worker_verifying", "shiro"
			}
			return "delegated_to_worker", "shiro"
		}
		if strings.HasPrefix(to, "coder") {
			return "delegated_to_coder", to
		}
		if to == "mio" {
			return "reporting", "mio"
		}
	case "agent.response":
		if to == "user" && isMonitorAgent(from) {
			if responseLooksLikeFailure(content) {
				return "error", from
			}
			return "done", from
		}
		if from == "shiro" && to == "mio" {
			return "reporting", "mio"
		}
		if strings.HasPrefix(from, "coder") && to == "shiro" {
			return "worker_verifying", "shiro"
		}
	case "agent.report":
		if from == "shiro" && to == "mio" {
			return "reporting", "mio"
		}
	}
	return valueOr(current.Phase, "received"), valueOr(current.Owner, "-")
}

func terminalOutcomeFromEntryStage(content string) string {
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "completed", "complete", "done", "ok", "success", "succeeded":
		return "ok"
	case "failed", "failure", "error":
		return "failed"
	case "blocked":
		return "blocked"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return ""
	}
}

func summarizeCoderState(items []AgentSnapshot) string {
	status := "idle"
	for _, item := range items {
		switch item.State {
		case "error":
			return "error"
		case "unavailable":
			if status != "running" {
				status = "degraded"
			}
		case "thinking", "running":
			status = "running"
		case "offline":
			if status == "idle" {
				status = "offline"
			}
		}
	}
	return status
}

func componentFromAgent(agent AgentSnapshot) ComponentSnapshot {
	return ComponentSnapshot{
		Status:    agent.State,
		AgentID:   agent.ID,
		JobID:     agent.JobID,
		Route:     agent.Route,
		LastEvent: agent.LastEvent,
		UpdatedAt: agent.UpdatedAt,
		Preview:   agent.Preview,
		Reason:    agent.Reason,
	}
}

func latestUpdatedAt(values ...string) string {
	best := ""
	for _, v := range values {
		if v > best {
			best = v
		}
	}
	return best
}

func agentUpdatedAtValues(items []AgentSnapshot) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.UpdatedAt)
	}
	return values
}

func shortText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}

func isMonitorAgent(id string) bool {
	for _, item := range monitorAgents {
		if item == id {
			return true
		}
	}
	return false
}

func agentRole(id string) string {
	switch id {
	case "mio", "kuro", "midori":
		return "chat"
	case "shiro":
		return "worker"
	default:
		if strings.HasPrefix(id, "coder") {
			return "coder"
		}
		return "agent"
	}
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func monitorAgentOrDefault(id, fallback string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if isMonitorAgent(id) {
		return id
	}
	return fallback
}

func (d JobDetail) MarshalJSON() ([]byte, error) {
	type alias JobDetail
	if d.Item.Events == nil {
		d.Item.Events = []orchestrator.OrchestratorEvent{}
	}
	return json.Marshal(alias(d))
}
