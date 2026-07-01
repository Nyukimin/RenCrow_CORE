package viewer

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
)

func (s *MonitorStore) Status() StatusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	chat := s.agentSnapshotLocked("mio", now)
	worker := s.agentSnapshotLocked("shiro", now)
	coders := s.coderSnapshotsLocked(now)
	updatedAtValues := []string{chat.UpdatedAt, worker.UpdatedAt}
	for _, coder := range coders {
		updatedAtValues = append(updatedAtValues, coder.UpdatedAt)
	}
	return StatusSnapshot{
		UpdatedAt: latestUpdatedAt(updatedAtValues...),
		Chat:      componentFromAgent(chat),
		Worker:    componentFromAgent(worker),
		Coders: CodersSnapshot{
			Status:    summarizeCoderState(coders),
			UpdatedAt: latestUpdatedAt(agentUpdatedAtValues(coders)...),
			Items:     coders,
		},
	}
}

func (s *MonitorStore) Agents() []AgentSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	items := make([]AgentSnapshot, 0, len(monitorAgents))
	for _, id := range monitorAgents {
		items = append(items, s.agentSnapshotLocked(id, now))
	}
	return items
}

func (s *MonitorStore) coderSnapshotsLocked(now time.Time) []AgentSnapshot {
	items := make([]AgentSnapshot, 0, len(monitorAgents))
	for _, id := range monitorAgents {
		if strings.HasPrefix(id, "coder") {
			items = append(items, s.agentSnapshotLocked(id, now))
		}
	}
	return items
}

func (s *MonitorStore) AgentDetail(ctx context.Context, id string, limit int) (AgentDetail, bool) {
	s.mu.RLock()
	now := time.Now()
	_, ok := s.agents[id]
	if !ok {
		s.mu.RUnlock()
		return AgentDetail{}, false
	}
	agent := s.agentSnapshotLocked(id, now)
	jobs := make([]JobSnapshot, 0, 4)
	for _, job := range s.jobs {
		if strings.EqualFold(job.Owner, id) || (agent.JobID != "" && strings.EqualFold(job.JobID, agent.JobID)) {
			jobs = append(jobs, *job)
		}
	}
	s.mu.RUnlock()

	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].UpdatedAt > jobs[j].UpdatedAt
	})
	if limit > 0 && len(jobs) > limit {
		jobs = jobs[:limit]
	}

	events, err := s.ArchivedLogs(ctx, LogFilter{Agent: id, Limit: limit})
	if err != nil || len(events) == 0 {
		events = s.Logs(LogFilter{Agent: id, Limit: limit})
	}
	return AgentDetail{Agent: agent, ActiveJobs: jobs, Events: events}, true
}

func (s *MonitorStore) Jobs(filter JobFilter) []JobSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]JobSnapshot, 0, len(s.jobs))
	for _, job := range s.jobs {
		if filter.Route != "" && !strings.EqualFold(job.Route, filter.Route) {
			continue
		}
		if filter.Status != "" && !strings.EqualFold(job.Status, filter.Status) {
			continue
		}
		if filter.Owner != "" && !strings.EqualFold(job.Owner, filter.Owner) {
			continue
		}
		if filter.SessionID != "" && !strings.EqualFold(job.SessionID, filter.SessionID) {
			continue
		}
		if filter.ChatID != "" && !strings.EqualFold(job.ChatID, filter.ChatID) {
			continue
		}
		items = append(items, *job)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items
}

func (s *MonitorStore) Logs(filter LogFilter) []orchestrator.OrchestratorEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]orchestrator.OrchestratorEvent, 0, len(s.logs))
	for i := len(s.logs) - 1; i >= 0; i-- {
		ev := s.logs[i]
		if !matchesLogFilter(ev, filter) {
			continue
		}
		items = append(items, ev)
		if filter.Limit > 0 && len(items) >= filter.Limit {
			break
		}
	}
	return items
}

func (s *MonitorStore) ArchivedLogs(ctx context.Context, filter LogFilter) ([]orchestrator.OrchestratorEvent, error) {
	if s.archive == nil {
		return nil, nil
	}
	return s.archive.Query(ctx, filter)
}

func (s *MonitorStore) JobDetail(ctx context.Context, jobID string) (JobDetail, bool) {
	s.mu.RLock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.RUnlock()
		return JobDetail{}, false
	}
	item := *job
	s.mu.RUnlock()

	if events, err := s.ArchivedLogs(ctx, LogFilter{JobID: jobID, Limit: monitorMaxJobEvents}); err == nil && len(events) > 0 {
		item.Events = events
	}

	var evidence *domainexecution.ExecutionReport
	if s.evidence != nil {
		if ev, err := s.evidence.GetByJobID(ctx, jobID); err == nil {
			evidence = &ev
		}
	}
	return JobDetail{Item: item, Evidence: evidence}, true
}

func (s *MonitorStore) Summary() AuditSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := AuditSummary{
		StoredLogs: len(s.logs),
		ByType:     map[string]int{},
		ByAgent:    map[string]int{},
		ByRoute:    map[string]int{},
	}
	for _, ev := range s.logs {
		if ev.Type != "" {
			out.ByType[ev.Type]++
		}
		if ev.From != "" {
			out.ByAgent[strings.ToLower(ev.From)]++
		}
		if ev.Route != "" {
			out.ByRoute[strings.ToUpper(ev.Route)]++
		}
	}
	return out
}
