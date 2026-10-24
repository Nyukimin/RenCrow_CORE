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
	projection := s.currentPublicProjection(context.Background())
	if projection == nil {
		return StatusSnapshot{}
	}

	now := time.Now()
	chat := projection.agentSnapshotLocked("mio", now)
	worker := projection.agentSnapshotLocked("shiro", now)
	coders := projection.coderSnapshotsLocked(now)
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
		TaskActivities: projection.taskActivitySnapshotsLocked(TaskActivityFilter{}),
	}
}

func (s *MonitorStore) Agents() []AgentSnapshot {
	projection := s.currentPublicProjection(context.Background())
	if projection == nil {
		return nil
	}

	now := time.Now()
	items := make([]AgentSnapshot, 0, len(monitorAgents))
	for _, id := range monitorAgents {
		items = append(items, projection.agentSnapshotLocked(id, now))
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
	projection := s.currentPublicProjection(ctx)
	if projection == nil {
		return AgentDetail{}, false
	}
	now := time.Now()
	_, ok := projection.agents[id]
	if !ok {
		return AgentDetail{}, false
	}
	agent := projection.agentSnapshotLocked(id, now)
	activities := make([]TaskActivitySnapshot, 0, 4)
	for _, activity := range projection.taskActivities {
		if strings.EqualFold(activity.Owner, id) || (agent.TaskID != "" && strings.EqualFold(activity.TaskID, agent.TaskID)) {
			activities = append(activities, *activity)
		}
	}

	sortTaskActivities(activities)
	if limit > 0 && len(activities) > limit {
		activities = activities[:limit]
	}

	events, err := s.ArchivedLogs(ctx, LogFilter{Agent: id, Limit: limit})
	if err != nil || len(events) == 0 {
		events = filterMonitorEvents(projection.logs, LogFilter{Agent: id, Limit: limit})
	}
	return AgentDetail{Agent: agent, ActiveTaskActivities: activities, Events: events}, true
}

func (s *MonitorStore) TaskActivities(filter TaskActivityFilter) []TaskActivitySnapshot {
	projection := s.currentPublicProjection(context.Background())
	if projection == nil {
		return nil
	}
	return projection.taskActivitySnapshotsLocked(filter)
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
		if filter.ChannelAddress != "" && !strings.EqualFold(activity.ChannelAddress, filter.ChannelAddress) {
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
	return s.LogsContext(context.Background(), filter)
}

// LogsContext rechecks the current trusted visibility predicate when reading
// the RAM projection. Admission-time visibility alone is insufficient because
// a canonical Session can be deleted or rebound after an event was retained.
func (s *MonitorStore) LogsContext(ctx context.Context, filter LogFilter) []orchestrator.OrchestratorEvent {
	events := s.currentVisibleEvents(ctx)
	return filterMonitorEvents(events, filter)
}

func (s *MonitorStore) ArchivedLogs(ctx context.Context, filter LogFilter) ([]orchestrator.OrchestratorEvent, error) {
	if s.archive == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	queryFilter := filter
	queryFilter.Limit = 0
	events, err := s.archive.Query(ctx, queryFilter)
	if err != nil {
		return nil, err
	}
	visible := make([]orchestrator.OrchestratorEvent, 0, len(events))
	for _, event := range events {
		if s.eventIsPublic(ctx, event) {
			visible = append(visible, event)
		}
	}
	if filter.Limit > 0 && len(visible) > filter.Limit {
		visible = visible[:filter.Limit]
	}
	return visible, nil
}

func (s *MonitorStore) TaskActivityDetail(ctx context.Context, taskID modulecore.TaskID) (TaskActivityDetail, bool) {
	if err := taskID.Validate(); err != nil {
		return TaskActivityDetail{}, false
	}
	taskKey := taskID.String()
	filter := LogFilter{TaskID: taskID}
	events, err := s.ArchivedLogs(ctx, filter)
	if err != nil || len(events) == 0 {
		events = filterMonitorEvents(s.currentVisibleEvents(ctx), filter)
	}
	if len(events) == 0 {
		return TaskActivityDetail{}, false
	}
	projection := monitorProjectionFromVisibleEvents(events)
	activity, ok := projection.taskActivities[taskKey]
	if !ok || activity == nil {
		return TaskActivityDetail{}, false
	}
	item := *activity
	sortEventsNewestFirst(item.Events)

	var evidence *domainexecution.ExecutionReport
	if s.evidence != nil {
		if ev, err := s.evidence.GetByTaskID(ctx, taskID); err == nil {
			evidence = &ev
		}
	}
	if ctx != nil && ctx.Err() != nil {
		return TaskActivityDetail{}, false
	}
	for _, event := range item.Events {
		if !s.eventIsPublic(ctx, event) {
			return TaskActivityDetail{}, false
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
	projection := s.currentPublicProjection(context.Background())
	if projection == nil {
		return AuditSummary{ByType: map[string]int{}, ByAgent: map[string]int{}, ByRoute: map[string]int{}}
	}

	out := AuditSummary{
		StoredLogs: len(projection.logs),
		ByType:     map[string]int{},
		ByAgent:    map[string]int{},
		ByRoute:    map[string]int{},
	}
	for _, ev := range projection.logs {
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

// currentVisibleEvents snapshots bounded RAM history and rechecks every event
// against the current trusted visibility owner before any derived read.
func (s *MonitorStore) currentVisibleEvents(ctx context.Context) []orchestrator.OrchestratorEvent {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return nil
	}
	s.mu.RLock()
	snapshot := append([]orchestrator.OrchestratorEvent(nil), s.logs...)
	s.mu.RUnlock()
	visible := make([]orchestrator.OrchestratorEvent, 0, len(snapshot))
	for _, event := range snapshot {
		if s.eventIsPublic(ctx, event) {
			visible = append(visible, event)
		}
	}
	return visible
}

// currentPublicProjection rebuilds all event-derived Monitor fields from the
// currently authorized bounded RAM events. Previously reduced state cannot
// be safely subtracted when Session visibility changes, so every public read
// starts from the source events again.
func (s *MonitorStore) currentPublicProjection(ctx context.Context) *MonitorStore {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	events := s.currentVisibleEvents(ctx)
	s.mu.RLock()
	operationalAgents := make(map[string]AgentSnapshot, len(s.operationalAgents))
	for id, agent := range s.operationalAgents {
		operationalAgents[id] = agent
	}
	s.mu.RUnlock()
	return monitorProjectionFromVisibleEvents(events, operationalAgents)
}

func monitorProjectionFromVisibleEvents(events []orchestrator.OrchestratorEvent, operationalAgents ...map[string]AgentSnapshot) *MonitorStore {
	projection := NewMonitorStore(nil, nil)
	if len(operationalAgents) > 0 {
		for id, agent := range operationalAgents[0] {
			projection.agents[id] = agent
		}
	}
	chronological := append([]orchestrator.OrchestratorEvent(nil), events...)
	sortEventsOldestFirst(chronological)
	for _, event := range chronological {
		projection.logs = append(projection.logs, event)
		projection.reduceAgents(event)
		projection.reduceTaskActivity(event)
	}
	return projection
}

func filterMonitorEvents(events []orchestrator.OrchestratorEvent, filter LogFilter) []orchestrator.OrchestratorEvent {
	items := make([]orchestrator.OrchestratorEvent, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		if matchesLogFilter(events[i], filter) {
			items = append(items, events[i])
		}
	}
	sortEventsNewestFirst(items)
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items
}

func sortEventsOldestFirst(items []orchestrator.OrchestratorEvent) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].EventSeq == 0 || items[j].EventSeq == 0 || items[i].EventSeq == items[j].EventSeq {
			return false
		}
		return items[i].EventSeq < items[j].EventSeq
	})
}
