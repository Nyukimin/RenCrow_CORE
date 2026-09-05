package viewer

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
		TaskActivities: s.taskActivitySnapshotsLocked(TaskActivityFilter{}),
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
	activities := make([]TaskActivitySnapshot, 0, 4)
	for _, activity := range s.taskActivities {
		if strings.EqualFold(activity.Owner, id) || (agent.TaskID != "" && strings.EqualFold(activity.TaskID, agent.TaskID)) {
			activities = append(activities, *activity)
		}
	}
	s.mu.RUnlock()

	sortTaskActivities(activities)
	if limit > 0 && len(activities) > limit {
		activities = activities[:limit]
	}

	events, err := s.ArchivedLogs(ctx, LogFilter{Agent: id, Limit: limit})
	if err != nil || len(events) == 0 {
		events = s.Logs(LogFilter{Agent: id, Limit: limit})
	}
	return AgentDetail{Agent: agent, ActiveTaskActivities: activities, Events: events}, true
}

func (s *MonitorStore) TaskActivities(filter TaskActivityFilter) []TaskActivitySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.taskActivitySnapshotsLocked(filter)
}

func (s *MonitorStore) taskActivitySnapshotsLocked(filter TaskActivityFilter) []TaskActivitySnapshot {
	items := make([]TaskActivitySnapshot, 0, len(s.taskActivities))
	for _, activity := range s.taskActivities {
		if filter.Route != "" && !strings.EqualFold(activity.Route, filter.Route) {
			continue
		}
		if filter.Status != "" && !strings.EqualFold(activity.Status, filter.Status) {
			continue
		}
		if filter.Owner != "" && !strings.EqualFold(activity.Owner, filter.Owner) {
			continue
		}
		if filter.SessionID != "" && !strings.EqualFold(activity.SessionID, filter.SessionID) {
			continue
		}
		if filter.ChatID != "" && !strings.EqualFold(activity.ChatID, filter.ChatID) {
			continue
		}
		items = append(items, *activity)
	}
	sortTaskActivities(items)
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
	}
	sortEventsNewestFirst(items)
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items
}

func (s *MonitorStore) ArchivedLogs(ctx context.Context, filter LogFilter) ([]orchestrator.OrchestratorEvent, error) {
	if s.archive == nil {
		return nil, nil
	}
	return s.archive.Query(ctx, filter)
}

func (s *MonitorStore) TaskActivityDetail(ctx context.Context, taskID modulecore.TaskID) (TaskActivityDetail, bool) {
	if err := taskID.Validate(); err != nil {
		return TaskActivityDetail{}, false
	}
	taskKey := taskID.String()
	s.mu.RLock()
	activity, ok := s.taskActivities[taskKey]
	if !ok {
		s.mu.RUnlock()
		return TaskActivityDetail{}, false
	}
	item := *activity
	s.mu.RUnlock()

	if events, err := s.ArchivedLogs(ctx, LogFilter{TaskID: taskID, Limit: monitorMaxTaskActivityEvents}); err == nil && len(events) > 0 {
		item.Events = events
	}

	var evidence *domainexecution.ExecutionReport
	if s.evidence != nil {
		if ev, err := s.evidence.GetByTaskID(ctx, taskID); err == nil {
			evidence = &ev
		}
	}
	return TaskActivityDetail{Item: item, Evidence: evidence}, true
}

func sortTaskActivities(items []TaskActivitySnapshot) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].EventSeq != items[j].EventSeq {
			if items[i].EventSeq == 0 {
				return false
			}
			if items[j].EventSeq == 0 {
				return true
			}
			return items[i].EventSeq > items[j].EventSeq
		}
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		return items[i].TaskID > items[j].TaskID
	})
}

func sortEventsNewestFirst(items []orchestrator.OrchestratorEvent) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].EventSeq != items[j].EventSeq {
			if items[i].EventSeq == 0 {
				return false
			}
			if items[j].EventSeq == 0 {
				return true
			}
			return items[i].EventSeq > items[j].EventSeq
		}
		return false
	})
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
