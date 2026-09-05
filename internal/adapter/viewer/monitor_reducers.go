package viewer

import (
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func (s *MonitorStore) agentSnapshotLocked(id string, now time.Time) AgentSnapshot {
	agent := s.agents[id]
	if agent.UpdatedAt == "" {
		return agent
	}
	if agent.State == "unavailable" {
		return agent
	}
	ts, err := time.Parse(time.RFC3339, agent.UpdatedAt)
	if err == nil && now.Sub(ts) > monitorOfflineAfter {
		agent.State = "offline"
	}
	return agent
}

func (s *MonitorStore) reduceAgents(ev orchestrator.OrchestratorEvent) {
	ts := ev.Timestamp
	route := ev.Route
	taskID := ev.TaskID.String()

	if ev.Type == "agent.unavailable" {
		s.patchAgent(strings.ToLower(strings.TrimSpace(ev.From)), AgentSnapshot{
			State:     "unavailable",
			LastEvent: ev.Type,
			Preview:   shortText(ev.Content, 80),
			Reason:    shortText(ev.Content, 160),
			UpdatedAt: ts,
		})
		return
	}

	if ev.Type == "message.received" || ev.Type == "routing.decision" {
		target := "mio"
		if ev.Type == "message.received" {
			target = monitorAgentOrDefault(ev.To, "mio")
		} else if activity := s.taskActivities[taskID]; activity != nil {
			target = monitorAgentOrDefault(activity.Owner, "mio")
		}
		s.patchAgent(target, AgentSnapshot{
			State:     "running",
			Route:     route,
			TaskID:    taskID,
			EventSeq:  ev.EventSeq,
			SessionID: string(ev.SessionID),
			LastEvent: ev.Type,
			Preview:   shortText(ev.Content, 80),
			UpdatedAt: ts,
		})
		return
	}

	from := strings.ToLower(strings.TrimSpace(ev.From))
	to := strings.ToLower(strings.TrimSpace(ev.To))
	if isMonitorAgent(from) {
		state := "running"
		switch ev.Type {
		case "agent.thinking", "agent.waiting":
			state = "thinking"
		case "agent.response":
			lower := strings.ToLower(ev.Content)
			if strings.Contains(lower, "error") || strings.Contains(lower, "失敗") {
				state = "error"
			} else {
				state = "idle"
			}
		case "agent.error", "mailbox.error":
			state = "error"
		}
		s.patchAgent(from, AgentSnapshot{
			State:     state,
			Route:     route,
			TaskID:    taskID,
			EventSeq:  ev.EventSeq,
			SessionID: string(ev.SessionID),
			LastEvent: ev.Type,
			Preview:   shortText(ev.Content, 80),
			Reason:    "",
			UpdatedAt: ts,
		})
	}
	if (ev.Type == "agent.start" || ev.Type == "agent.dispatch" || ev.Type == "mailbox.sent") && isMonitorAgent(to) {
		s.patchAgent(to, AgentSnapshot{
			State:     "running",
			Route:     route,
			TaskID:    taskID,
			EventSeq:  ev.EventSeq,
			SessionID: string(ev.SessionID),
			LastEvent: ev.Type,
			Preview:   shortText(ev.Content, 80),
			Reason:    "",
			UpdatedAt: ts,
		})
	}
	if ev.Type == "agent.response" && to == "mio" {
		s.patchAgent("mio", AgentSnapshot{
			State:     "idle",
			Route:     route,
			TaskID:    taskID,
			EventSeq:  ev.EventSeq,
			SessionID: string(ev.SessionID),
			LastEvent: ev.Type,
			Preview:   shortText(ev.Content, 80),
			Reason:    "",
			UpdatedAt: ts,
		})
	}
	if isUserFacingFinalResponse(ev) {
		s.clearActiveAgentsForTask(ev)
	}
}

// reduceTaskActivity projects one canonical Task event into the bounded
// monitor activity view.
func (s *MonitorStore) reduceTaskActivity(ev orchestrator.OrchestratorEvent) {
	taskID := strings.TrimSpace(ev.TaskID.String())
	if taskID == "" {
		return
	}
	activity := s.taskActivities[taskID]
	if activity == nil {
		activity = &TaskActivitySnapshot{
			TaskID:    taskID,
			EventSeq:  ev.EventSeq,
			Route:     valueOr(ev.Route, "-"),
			Phase:     "received",
			Owner:     "mio",
			Status:    "running",
			SessionID: string(ev.SessionID),
			Channel:   ev.Channel,
			ChatID:    ev.ChatID,
			StartedAt: ev.Timestamp,
			UpdatedAt: ev.Timestamp,
		}
		s.taskActivities[taskID] = activity
	}
	activity.EventSeq = ev.EventSeq
	activity.UpdatedAt = ev.Timestamp
	if ev.Route != "" {
		activity.Route = ev.Route
	}
	if ev.SessionID != "" {
		activity.SessionID = string(ev.SessionID)
	}
	if ev.Channel != "" {
		activity.Channel = ev.Channel
	}
	if ev.ChatID != "" {
		activity.ChatID = ev.ChatID
	}
	if ev.Content != "" {
		activity.Summary = shortText(ev.Content, 160)
	}
	activity.Phase, activity.Owner = classifyTaskActivityPhase(ev, activity)
	if ev.Type == "worker.classified_failure" || ev.Type == "agent.error" || ev.Type == "mailbox.error" {
		raw := strings.TrimSpace(ev.Content)
		if idx := strings.Index(raw, ":"); idx >= 0 {
			activity.FailureKind = strings.TrimSpace(raw[:idx])
			activity.FailureReason = strings.TrimSpace(raw[idx+1:])
		} else {
			activity.FailureReason = raw
		}
		activity.Status = "error"
	}
	if ev.Type == "entry.stage" {
		switch terminalOutcomeFromEntryStage(ev.Content) {
		case "ok":
			activity.Status = "done"
			activity.TerminalOutcome = "ok"
			activity.FailureKind = ""
			activity.FailureReason = ""
		case "failed":
			activity.Status = "error"
			activity.TerminalOutcome = "failed"
			if strings.TrimSpace(activity.FailureReason) == "" {
				activity.FailureReason = "entry stage failed"
			}
		case "blocked":
			activity.Status = "error"
			activity.TerminalOutcome = "blocked"
			if strings.TrimSpace(activity.FailureReason) == "" {
				activity.FailureReason = "entry stage blocked"
			}
		case "cancelled":
			activity.Status = "error"
			activity.TerminalOutcome = "cancelled"
			if strings.TrimSpace(activity.FailureReason) == "" {
				activity.FailureReason = "entry stage cancelled"
			}
		}
	}
	if clearsTaskActivityFailure(ev) {
		activity.FailureKind = ""
		activity.FailureReason = ""
		if activity.Status == "error" {
			activity.Status = "running"
		}
	}
	if ev.Type == "agent.response" {
		if isUserFacingFinalResponse(ev) {
			activity.FinalUserReport = ev.Content
			activity.MioReported = strings.EqualFold(ev.From, "mio")
			if responseLooksLikeFailure(ev.Content) {
				activity.Status = "error"
				activity.TerminalOutcome = "failed"
			} else {
				activity.FailureKind = ""
				activity.FailureReason = ""
				activity.Status = "done"
				activity.TerminalOutcome = "ok"
			}
		} else if activity.Status != "error" && activity.TerminalOutcome == "" {
			activity.Status = "running"
		}
	}
	activity.Events = append(activity.Events, ev)
	if len(activity.Events) > monitorMaxTaskActivityEvents {
		activity.Events = activity.Events[len(activity.Events)-monitorMaxTaskActivityEvents:]
	}
}

func isUserFacingFinalResponse(ev orchestrator.OrchestratorEvent) bool {
	return ev.Type == "agent.response" &&
		strings.EqualFold(strings.TrimSpace(ev.To), "user") &&
		isMonitorAgent(strings.ToLower(strings.TrimSpace(ev.From)))
}

func (s *MonitorStore) clearActiveAgentsForTask(ev orchestrator.OrchestratorEvent) {
	taskID := strings.TrimSpace(ev.TaskID.String())
	if taskID == "" {
		return
	}
	speaker := monitorAgentOrDefault(ev.From, "agent")
	preview := shortText("cleared by final response from "+speaker, 80)
	for id, agent := range s.agents {
		if strings.TrimSpace(agent.TaskID) != taskID {
			continue
		}
		if agent.State != "running" && agent.State != "thinking" {
			continue
		}
		s.patchAgent(id, AgentSnapshot{
			State:     "idle",
			Route:     ev.Route,
			TaskID:    taskID,
			EventSeq:  ev.EventSeq,
			SessionID: string(ev.SessionID),
			LastEvent: ev.Type,
			Preview:   preview,
			Reason:    "",
			UpdatedAt: ev.Timestamp,
		})
	}
}

func clearsTaskActivityFailure(ev orchestrator.OrchestratorEvent) bool {
	from := strings.ToLower(strings.TrimSpace(ev.From))
	to := strings.ToLower(strings.TrimSpace(ev.To))
	switch ev.Type {
	case "mailbox.received":
		return strings.Contains(strings.ToLower(ev.Content), "type=result")
	case "agent.response":
		if to == "user" && isMonitorAgent(from) {
			return !responseLooksLikeFailure(ev.Content)
		}
		return (strings.HasPrefix(from, "coder") && to == "shiro") || (from == "shiro" && to == "mio")
	default:
		return false
	}
}

func responseLooksLikeFailure(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "失敗: 0") || strings.Contains(lower, "failures: 0") || strings.Contains(lower, "failed: 0") {
		return false
	}
	return strings.Contains(lower, "error") || strings.Contains(lower, "失敗")
}

func (s *MonitorStore) patchAgent(id string, patch AgentSnapshot) {
	cur, ok := s.agents[id]
	if !ok {
		cur = AgentSnapshot{ID: id, Role: agentRole(id), State: "offline"}
	}
	if patch.State != "" {
		cur.State = patch.State
	}
	if patch.Route != "" {
		cur.Route = patch.Route
	}
	if patch.TaskID != "" {
		cur.TaskID = patch.TaskID
	}
	if patch.EventSeq != 0 {
		cur.EventSeq = patch.EventSeq
	}
	if patch.SessionID != "" {
		cur.SessionID = patch.SessionID
	}
	if patch.LastEvent != "" {
		cur.LastEvent = patch.LastEvent
	}
	if patch.Preview != "" {
		cur.Preview = patch.Preview
	}
	if patch.Reason != "" || cur.State != patch.State {
		cur.Reason = patch.Reason
	}
	if patch.UpdatedAt != "" {
		cur.UpdatedAt = patch.UpdatedAt
	}
	cur.EventCount++
	s.agents[id] = cur
}
