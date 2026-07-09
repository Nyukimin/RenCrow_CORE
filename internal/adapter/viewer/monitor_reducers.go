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
	jid := ev.JobID

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
		} else if job := s.jobs[jid]; job != nil {
			target = monitorAgentOrDefault(job.Owner, "mio")
		}
		s.patchAgent(target, AgentSnapshot{
			State:     "running",
			Route:     route,
			JobID:     jid,
			SessionID: ev.SessionID,
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
			JobID:     jid,
			SessionID: ev.SessionID,
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
			JobID:     jid,
			SessionID: ev.SessionID,
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
			JobID:     jid,
			SessionID: ev.SessionID,
			LastEvent: ev.Type,
			Preview:   shortText(ev.Content, 80),
			Reason:    "",
			UpdatedAt: ts,
		})
	}
	if isUserFacingFinalResponse(ev) {
		s.clearActiveAgentsForJob(ev)
	}
}

func (s *MonitorStore) reduceJobs(ev orchestrator.OrchestratorEvent) {
	jid := strings.TrimSpace(ev.JobID)
	if jid == "" {
		return
	}
	job := s.jobs[jid]
	if job == nil {
		job = &JobSnapshot{
			JobID:     jid,
			Route:     valueOr(ev.Route, "-"),
			Phase:     "received",
			Owner:     "mio",
			Status:    "running",
			SessionID: ev.SessionID,
			Channel:   ev.Channel,
			ChatID:    ev.ChatID,
			StartedAt: ev.Timestamp,
			UpdatedAt: ev.Timestamp,
		}
		s.jobs[jid] = job
	}
	job.UpdatedAt = ev.Timestamp
	if ev.Route != "" {
		job.Route = ev.Route
	}
	if ev.SessionID != "" {
		job.SessionID = ev.SessionID
	}
	if ev.Channel != "" {
		job.Channel = ev.Channel
	}
	if ev.ChatID != "" {
		job.ChatID = ev.ChatID
	}
	if ev.Content != "" {
		job.Summary = shortText(ev.Content, 160)
	}
	job.Phase, job.Owner = classifyJobPhase(ev, job)
	if ev.Type == "worker.classified_failure" || ev.Type == "agent.error" || ev.Type == "mailbox.error" {
		raw := strings.TrimSpace(ev.Content)
		if idx := strings.Index(raw, ":"); idx >= 0 {
			job.FailureKind = strings.TrimSpace(raw[:idx])
			job.FailureReason = strings.TrimSpace(raw[idx+1:])
		} else {
			job.FailureReason = raw
		}
		job.Status = "error"
	}
	if ev.Type == "entry.stage" {
		switch terminalOutcomeFromEntryStage(ev.Content) {
		case "ok":
			job.Status = "done"
			job.TerminalOutcome = "ok"
			job.FailureKind = ""
			job.FailureReason = ""
		case "failed":
			job.Status = "error"
			job.TerminalOutcome = "failed"
			if strings.TrimSpace(job.FailureReason) == "" {
				job.FailureReason = "entry stage failed"
			}
		case "blocked":
			job.Status = "error"
			job.TerminalOutcome = "blocked"
			if strings.TrimSpace(job.FailureReason) == "" {
				job.FailureReason = "entry stage blocked"
			}
		case "cancelled":
			job.Status = "error"
			job.TerminalOutcome = "cancelled"
			if strings.TrimSpace(job.FailureReason) == "" {
				job.FailureReason = "entry stage cancelled"
			}
		}
	}
	if clearsJobFailure(ev) {
		job.FailureKind = ""
		job.FailureReason = ""
		if job.Status == "error" {
			job.Status = "running"
		}
	}
	if ev.Type == "agent.response" {
		if isUserFacingFinalResponse(ev) {
			job.FinalUserReport = ev.Content
			job.MioReported = strings.EqualFold(ev.From, "mio")
			if responseLooksLikeFailure(ev.Content) {
				job.Status = "error"
				job.TerminalOutcome = "failed"
			} else {
				job.FailureKind = ""
				job.FailureReason = ""
				job.Status = "done"
				job.TerminalOutcome = "ok"
			}
		} else if job.Status != "error" && job.TerminalOutcome == "" {
			job.Status = "running"
		}
	}
	job.Events = append(job.Events, ev)
	if len(job.Events) > monitorMaxJobEvents {
		job.Events = job.Events[len(job.Events)-monitorMaxJobEvents:]
	}
}

func isUserFacingFinalResponse(ev orchestrator.OrchestratorEvent) bool {
	return ev.Type == "agent.response" &&
		strings.EqualFold(strings.TrimSpace(ev.To), "user") &&
		isMonitorAgent(strings.ToLower(strings.TrimSpace(ev.From)))
}

func (s *MonitorStore) clearActiveAgentsForJob(ev orchestrator.OrchestratorEvent) {
	jid := strings.TrimSpace(ev.JobID)
	if jid == "" {
		return
	}
	speaker := monitorAgentOrDefault(ev.From, "agent")
	preview := shortText("cleared by final response from "+speaker, 80)
	for id, agent := range s.agents {
		if strings.TrimSpace(agent.JobID) != jid {
			continue
		}
		if agent.State != "running" && agent.State != "thinking" {
			continue
		}
		s.patchAgent(id, AgentSnapshot{
			State:     "idle",
			Route:     ev.Route,
			JobID:     jid,
			SessionID: ev.SessionID,
			LastEvent: ev.Type,
			Preview:   preview,
			Reason:    "",
			UpdatedAt: ev.Timestamp,
		})
	}
}

func clearsJobFailure(ev orchestrator.OrchestratorEvent) bool {
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
	if patch.JobID != "" {
		cur.JobID = patch.JobID
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
