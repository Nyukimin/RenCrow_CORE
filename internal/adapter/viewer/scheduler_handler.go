package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
)

type SchedulerStore interface {
	ListSchedules(ctx context.Context, limit int) ([]domainscheduler.Schedule, error)
	SaveSchedule(ctx context.Context, schedule domainscheduler.Schedule) error
	SaveRunLog(ctx context.Context, log domainscheduler.RunLog) error
	ListRunLogs(ctx context.Context, limit int) ([]domainscheduler.RunLog, error)
}

type filteredSchedulerReader interface {
	ListSchedulesFiltered(context.Context, int, func(domainscheduler.Schedule) bool) ([]domainscheduler.Schedule, []string, error)
}

type exactSchedulerReader interface {
	FindScheduleByID(context.Context, string) (domainscheduler.Schedule, bool, error)
}

func HandleScheduler(store SchedulerStore) http.HandlerFunc {
	return HandleSchedulerWithExecutor(store, nil)
}

func HandleSchedulerWithExecutor(store SchedulerStore, executor schedulerapp.Executor) http.HandlerFunc {
	return HandleSchedulerWithExecutorAndProtectedTargets(store, executor, nil, nil)
}

// HandleSchedulerWithExecutorAndProtectedTargets hides owner-projected target
// schedules from the generic scheduler API and prevents generic callers from
// creating, running, or disabling them. The owner remains responsible for its
// single canonical cadence.
func HandleSchedulerWithExecutorAndProtectedTargets(
	store SchedulerStore,
	executor schedulerapp.Executor,
	protectedTargets []string,
	protectedScheduleIDs []string,
) http.HandlerFunc {
	policy := newProtectedSchedulerPolicy(protectedTargets, protectedScheduleIDs)
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "scheduler store unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 200)
			if err != nil {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			schedules, hiddenScheduleIDs, err := policy.listVisibleSchedules(r.Context(), store, limit)
			if err != nil {
				http.Error(w, "scheduler owner policy is unavailable", http.StatusServiceUnavailable)
				return
			}
			logs, err := store.ListRunLogs(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load scheduler run logs", http.StatusInternalServerError)
				return
			}
			if len(policy.targets) != 0 || len(policy.ids) != 0 {
				knownTargets := make(map[string]string, len(schedules))
				for _, schedule := range schedules {
					knownTargets[strings.TrimSpace(string(schedule.ScheduleID))] = schedule.Target
				}
				logs, err = policy.filterRunLogs(r.Context(), store, logs, hiddenScheduleIDs, knownTargets)
				if err != nil {
					http.Error(w, "scheduler owner policy is unavailable", http.StatusServiceUnavailable)
					return
				}
			}
			if schedules == nil {
				schedules = []domainscheduler.Schedule{}
			}
			if logs == nil {
				logs = []domainscheduler.RunLog{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"schedules": schedules, "run_logs": logs})
		case http.MethodPost:
			handleSchedulerPost(w, r, store, executor, policy)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleSchedulerPost(w http.ResponseWriter, r *http.Request, store SchedulerStore, executor schedulerapp.Executor, policy protectedSchedulerPolicy) {
	var req struct {
		Action     string                   `json:"action"`
		Schedule   domainscheduler.Schedule `json:"schedule"`
		ScheduleID string                   `json:"schedule_id"`
		DisabledBy string                   `json:"disabled_by"`
		Trigger    string                   `json:"trigger"`
		Limit      int                      `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid scheduler payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	svc := schedulerapp.NewService(store, executor)
	switch strings.TrimSpace(req.Action) {
	case "create":
		if strings.TrimSpace(req.Schedule.Target) != req.Schedule.Target {
			http.Error(w, "scheduler target must not contain surrounding whitespace", http.StatusBadRequest)
			return
		}
		if policy.protectsTarget(req.Schedule.Target) {
			http.Error(w, "scheduler target is owner-managed", http.StatusForbidden)
			return
		}
		if req.Schedule.ScheduleID != "" {
			scheduleID := string(req.Schedule.ScheduleID)
			if strings.TrimSpace(scheduleID) != scheduleID {
				http.Error(w, "schedule_id must not contain surrounding whitespace", http.StatusBadRequest)
				return
			}
			protected, scanErr := policy.isProtectedScheduleID(r.Context(), store, scheduleID)
			if scanErr != nil {
				http.Error(w, "scheduler owner policy is unavailable", http.StatusServiceUnavailable)
				return
			}
			if protected {
				http.NotFound(w, r)
				return
			}
		}
		schedule, err := svc.CreateSchedule(r.Context(), req.Schedule)
		if err != nil {
			http.Error(w, "failed to create scheduler schedule: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"schedule": schedule})
	case "run":
		scheduleID := firstNonEmptyString(req.ScheduleID, string(req.Schedule.ScheduleID))
		protected, scanErr := policy.isProtectedScheduleID(r.Context(), store, scheduleID)
		if scanErr != nil {
			http.Error(w, "scheduler owner policy is unavailable", http.StatusServiceUnavailable)
			return
		}
		if protected {
			http.NotFound(w, r)
			return
		}
		log, err := svc.RunSchedule(r.Context(), scheduleID, firstNonEmptyString(req.Trigger, "manual"))
		if err != nil {
			http.Error(w, "failed to run scheduler schedule: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"run_log": log})
	case "disable":
		scheduleID := firstNonEmptyString(req.ScheduleID, string(req.Schedule.ScheduleID))
		protected, scanErr := policy.isProtectedScheduleID(r.Context(), store, scheduleID)
		if scanErr != nil {
			http.Error(w, "scheduler owner policy is unavailable", http.StatusServiceUnavailable)
			return
		}
		if protected {
			http.NotFound(w, r)
			return
		}
		schedule, err := svc.DisableSchedule(r.Context(), scheduleID, req.DisabledBy)
		if err != nil {
			http.Error(w, "failed to disable scheduler schedule: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"schedule": schedule})
	case "due":
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		dueService := schedulerapp.NewService(protectedSchedulerReadStore{SchedulerStore: store, policy: policy}, executor)
		due, err := dueService.DueSchedules(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to list due scheduler schedules: "+err.Error(), http.StatusBadRequest)
			return
		}
		due = policy.filterDue(due)
		writeJSON(w, http.StatusOK, map[string]any{"due": due})
	default:
		http.Error(w, "action must be create, run, disable, or due", http.StatusBadRequest)
	}
}

const protectedSchedulerPolicyScanLimit = 1000

type protectedSchedulerPolicy struct {
	targets map[string]struct{}
	ids     map[string]struct{}
}

func newProtectedSchedulerPolicy(targets, scheduleIDs []string) protectedSchedulerPolicy {
	policy := protectedSchedulerPolicy{targets: make(map[string]struct{}, len(targets)), ids: make(map[string]struct{}, len(scheduleIDs))}
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target != "" {
			policy.targets[target] = struct{}{}
		}
	}
	for _, id := range scheduleIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			policy.ids[id] = struct{}{}
		}
	}
	return policy
}

func (p protectedSchedulerPolicy) protectsTarget(target string) bool {
	_, ok := p.targets[strings.TrimSpace(target)]
	return ok
}

func (p protectedSchedulerPolicy) protectsID(id string) bool {
	_, ok := p.ids[strings.TrimSpace(id)]
	return ok
}

func (p protectedSchedulerPolicy) listVisibleSchedules(ctx context.Context, store SchedulerStore, limit int) ([]domainscheduler.Schedule, map[string]struct{}, error) {
	if len(p.targets) == 0 && len(p.ids) == 0 {
		schedules, err := store.ListSchedules(ctx, limit)
		return schedules, map[string]struct{}{}, err
	}
	if filtered, ok := store.(filteredSchedulerReader); ok {
		schedules, hiddenIDs, err := filtered.ListSchedulesFiltered(ctx, limit, func(schedule domainscheduler.Schedule) bool {
			return !p.protectsTarget(schedule.Target) && !p.protectsID(string(schedule.ScheduleID))
		})
		if err != nil {
			return nil, nil, err
		}
		hidden := make(map[string]struct{}, len(hiddenIDs)+len(p.ids))
		for _, id := range hiddenIDs {
			hidden[strings.TrimSpace(id)] = struct{}{}
		}
		for id := range p.ids {
			hidden[id] = struct{}{}
		}
		return schedules, hidden, nil
	}
	all, err := store.ListSchedules(ctx, protectedSchedulerPolicyScanLimit)
	if err != nil {
		return nil, nil, err
	}
	if len(all) >= protectedSchedulerPolicyScanLimit {
		return nil, nil, errors.New("protected scheduler target scan reached its bound")
	}
	hidden := make(map[string]struct{}, len(p.ids))
	for id := range p.ids {
		hidden[id] = struct{}{}
	}
	visible := make([]domainscheduler.Schedule, 0, min(limit, len(all)))
	for _, schedule := range all {
		if p.protectsTarget(schedule.Target) {
			if schedule.ScheduleID != "" {
				hidden[strings.TrimSpace(string(schedule.ScheduleID))] = struct{}{}
			}
			continue
		}
		if p.protectsID(string(schedule.ScheduleID)) {
			continue
		}
		if len(visible) < limit {
			visible = append(visible, schedule)
		}
	}
	return visible, hidden, nil
}

func (p protectedSchedulerPolicy) filterRunLogs(
	ctx context.Context,
	store SchedulerStore,
	logs []domainscheduler.RunLog,
	hidden map[string]struct{},
	knownTargets map[string]string,
) ([]domainscheduler.RunLog, error) {
	visible := make([]domainscheduler.RunLog, 0, len(logs))
	for _, entry := range logs {
		scheduleID := strings.TrimSpace(string(entry.ScheduleID))
		if p.protectsID(scheduleID) {
			continue
		}
		if _, ok := hidden[scheduleID]; ok {
			continue
		}
		if target, ok := knownTargets[scheduleID]; ok {
			if p.protectsTarget(target) {
				continue
			}
			visible = append(visible, entry)
			continue
		}
		if exact, ok := store.(exactSchedulerReader); ok {
			schedule, found, err := exact.FindScheduleByID(ctx, scheduleID)
			if err != nil {
				return nil, err
			}
			if found && strings.TrimSpace(string(schedule.ScheduleID)) != scheduleID {
				return nil, errors.New("exact scheduler lookup returned a different schedule identity")
			}
			if found && !p.protectsID(string(schedule.ScheduleID)) && !p.protectsTarget(schedule.Target) {
				visible = append(visible, entry)
			}
			continue
		}
		// Unknown or missing schedule identity cannot prove that a private
		// owner target is unrelated, so its run log stays hidden.
	}
	return visible, nil
}

func (p protectedSchedulerPolicy) isProtectedScheduleID(ctx context.Context, store SchedulerStore, scheduleID string) (bool, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	if p.protectsID(scheduleID) {
		return true, nil
	}
	if len(p.targets) == 0 {
		return false, nil
	}
	if exact, ok := store.(exactSchedulerReader); ok {
		schedule, found, err := exact.FindScheduleByID(ctx, scheduleID)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		if strings.TrimSpace(string(schedule.ScheduleID)) != scheduleID {
			return false, errors.New("exact scheduler lookup returned a different schedule identity")
		}
		return p.protectsID(string(schedule.ScheduleID)) || p.protectsTarget(schedule.Target), nil
	}
	schedules, err := store.ListSchedules(ctx, protectedSchedulerPolicyScanLimit)
	if err != nil {
		return false, err
	}
	if len(schedules) >= protectedSchedulerPolicyScanLimit {
		return false, errors.New("protected scheduler target scan reached its bound")
	}
	for _, schedule := range schedules {
		if strings.TrimSpace(string(schedule.ScheduleID)) == scheduleID && p.protectsTarget(schedule.Target) {
			return true, nil
		}
	}
	return false, nil
}

func (p protectedSchedulerPolicy) filterDue(due []domainscheduler.DueSchedule) []domainscheduler.DueSchedule {
	visible := make([]domainscheduler.DueSchedule, 0, len(due))
	for _, entry := range due {
		if p.protectsTarget(entry.Schedule.Target) || p.protectsID(string(entry.Schedule.ScheduleID)) {
			continue
		}
		visible = append(visible, entry)
	}
	return visible
}

type protectedSchedulerReadStore struct {
	SchedulerStore
	policy protectedSchedulerPolicy
}

func (s protectedSchedulerReadStore) ListSchedules(ctx context.Context, limit int) ([]domainscheduler.Schedule, error) {
	schedules, _, err := s.policy.listVisibleSchedules(ctx, s.SchedulerStore, limit)
	return schedules, err
}
