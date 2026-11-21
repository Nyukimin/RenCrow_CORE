package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const livingHeartbeatPath = "/viewer/living/heartbeat"

const livingHeartbeatTitleReadTimeout = 500 * time.Millisecond

type LivingHeartbeatTaskReader interface {
	GetTask(context.Context, modulecore.TaskID) (domaintask.Task, error)
}

// LivingHeartbeatCurrentTaskReader provides canonical running Task identity
// for the internal activity merge. IDs are never serialized by this handler.
type LivingHeartbeatCurrentTaskReader interface {
	CurrentRunningTaskIDs(context.Context) (map[string][]modulecore.TaskID, error)
}

type LivingHeartbeatCurrentActivity struct {
	Task                domaintask.Task
	Kind                string
	Label               string
	Phase               string
	Revision            uint64
	State               string
	ActivityKind        string
	GameID              string
	RoutineAvailability string
	OngoingActivity     string
}

type LivingHeartbeatActivityReader interface {
	CurrentActivities(context.Context) (map[string]LivingHeartbeatCurrentActivity, error)
}

type RoutineActivityReader interface {
	CurrentRoutineActivity(context.Context, string) (RoutineActivity, error)
}

type RoutineActivityBatchReader interface {
	CurrentRoutineActivities(context.Context, []string) (map[string]RoutineActivity, error)
}

type RoutineActivity struct {
	Availability    string
	OngoingActivity string
}

type livingHeartbeatResponse struct {
	ObservedAt string                 `json:"observed_at"`
	Agents     []livingHeartbeatAgent `json:"agents"`
}

type livingHeartbeatAgent struct {
	ActorID             string                          `json:"actor_id"`
	State               string                          `json:"state"`
	ActivityKind        string                          `json:"activity_kind,omitempty"`
	GameID              string                          `json:"game_id,omitempty"`
	Title               string                          `json:"title,omitempty"`
	RoutineAvailability string                          `json:"routine_availability,omitempty"`
	OngoingActivity     string                          `json:"ongoing_activity,omitempty"`
	CurrentActivity     *livingHeartbeatCurrentActivity `json:"current_activity,omitempty"`
}

type livingHeartbeatCurrentActivity = PublicCurrentActivity

// HandleLivingHeartbeat returns a small read-only view of the actual CORE
// actor scheduler leases. It omits task identity, errors, prompts, and source
// content. A canonical Task title is included only after the existing public
// Task visibility policy authorizes its current publication binding.
func HandleLivingHeartbeat(scheduler *agentheartbeat.Scheduler, tasks LivingHeartbeatTaskReader, visibility PublicTaskVisibility, routineReaders ...RoutineActivityReader) http.HandlerFunc {
	return handleLivingHeartbeat(scheduler, tasks, visibility, nil, routineReaders...)
}

func HandleLivingHeartbeatWithCurrentTasks(scheduler *agentheartbeat.Scheduler, tasks LivingHeartbeatTaskReader, visibility PublicTaskVisibility, currentTasks LivingHeartbeatCurrentTaskReader, routineReaders ...RoutineActivityReader) http.HandlerFunc {
	return handleLivingHeartbeat(scheduler, tasks, visibility, currentTasks, routineReaders...)
}

// HandleLivingHeartbeatWithActivities publishes the durable current Task
// assignment. The runtime reader must fail if any configured actor is missing
// or has an invalid current assignment; this handler never manufactures one.
func HandleLivingHeartbeatWithActivities(tasks LivingHeartbeatTaskReader, visibility PublicTaskVisibility, activities LivingHeartbeatActivityReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !security.IsDirectLoopbackRequest(r) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != livingHeartbeatPath || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if activities == nil {
			http.Error(w, "current activity unavailable", http.StatusServiceUnavailable)
			return
		}
		readCtx, cancel := context.WithTimeout(r.Context(), livingHeartbeatTitleReadTimeout)
		defer cancel()
		current, err := activities.CurrentActivities(readCtx)
		actorIDs := superagent.PrivateChatQueueActorIDs()
		if err != nil || len(current) != len(actorIDs) {
			http.Error(w, "current activity unavailable", http.StatusServiceUnavailable)
			return
		}
		agents := make([]livingHeartbeatAgent, 0, len(actorIDs))
		for _, actorID := range actorIDs {
			activity, found := current[actorID]
			if !found || activity.Task.TaskID.Validate() != nil || activity.Kind == "" || activity.Label == "" || activity.Phase == "" || activity.Revision == 0 {
				http.Error(w, "current activity unavailable", http.StatusServiceUnavailable)
				return
			}
			agent := livingHeartbeatAgent{
				ActorID: actorID, State: activity.State, ActivityKind: activity.ActivityKind, GameID: activity.GameID,
				RoutineAvailability: activity.RoutineAvailability, OngoingActivity: activity.OngoingActivity,
				CurrentActivity: &livingHeartbeatCurrentActivity{Kind: activity.Kind, Label: activity.Label, Phase: activity.Phase, Revision: activity.Revision},
			}
			if tasks != nil {
				freshTask, taskErr := tasks.GetTask(readCtx, activity.Task.TaskID)
				if taskErr != nil {
					// Private and typed Task owners may intentionally hide the full
					// Task from the generic public reader. The canonical activity
					// snapshot still supplies its safe descriptor below.
					agents = append(agents, agent)
					continue
				}
				if !heartbeatTaskStillCurrent(freshTask, activity, actorID) {
					http.Error(w, "current activity unavailable", http.StatusServiceUnavailable)
					return
				}
				if visibility != nil {
					visible, visibilityErr := visibility(readCtx, freshTask)
					if visibilityErr == nil && visible {
						agent.Title = freshTask.Title
					}
				}
			}
			agents = append(agents, agent)
		}
		for index, actorID := range actorIDs {
			if agents[index].Title == "" {
				continue
			}
			activity := current[actorID]
			freshTask, taskErr := tasks.GetTask(readCtx, activity.Task.TaskID)
			if taskErr != nil {
				agents[index].Title = ""
				continue
			}
			if !heartbeatTaskStillCurrent(freshTask, activity, actorID) || visibility == nil {
				http.Error(w, "current activity unavailable", http.StatusServiceUnavailable)
				return
			}
			visible, visibilityErr := visibility(readCtx, freshTask)
			if visibilityErr != nil || !visible {
				http.Error(w, "current activity unavailable", http.StatusServiceUnavailable)
				return
			}
			agents[index].Title = freshTask.Title
		}
		latest, err := activities.CurrentActivities(readCtx)
		if err != nil || len(latest) != len(actorIDs) {
			http.Error(w, "current activity unavailable", http.StatusServiceUnavailable)
			return
		}
		for _, actorID := range actorIDs {
			before, beforeOK := current[actorID]
			after, afterOK := latest[actorID]
			if !beforeOK || !afterOK || !heartbeatSameCurrentActivity(before, after, actorID) {
				http.Error(w, "current activity changed during projection", http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(livingHeartbeatResponse{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Agents: agents})
	}
}

func heartbeatTaskStillCurrent(task domaintask.Task, activity LivingHeartbeatCurrentActivity, actorID string) bool {
	if task.TaskID == "" || task.TaskID != activity.Task.TaskID || task.Activity == nil || activity.Task.Activity == nil ||
		task.Activity.Revision != activity.Revision || task.Activity.Kind != activity.Task.Activity.Kind {
		return false
	}
	for _, participant := range task.Activity.Participants {
		if participant.ActorID == actorID && participant.IsCurrent {
			return true
		}
	}
	return false
}

func heartbeatSameCurrentActivity(left, right LivingHeartbeatCurrentActivity, actorID string) bool {
	return left.Task.TaskID == right.Task.TaskID && left.Revision == right.Revision &&
		heartbeatTaskStillCurrent(right.Task, left, actorID)
}

func handleLivingHeartbeat(scheduler *agentheartbeat.Scheduler, tasks LivingHeartbeatTaskReader, visibility PublicTaskVisibility, currentTasks LivingHeartbeatCurrentTaskReader, routineReaders ...RoutineActivityReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !security.IsDirectLoopbackRequest(r) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != livingHeartbeatPath || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")

		var titleLookup func(context.Context, modulecore.TaskID) (string, error)
		if tasks != nil && visibility != nil {
			titleLookup = func(ctx context.Context, taskID modulecore.TaskID) (string, error) {
				task, err := tasks.GetTask(ctx, taskID)
				if err != nil {
					return "", err
				}
				visible, err := visibility(ctx, task)
				if err != nil || !visible {
					return "", err
				}
				return task.Title, nil
			}
		}
		readCtx, cancel := context.WithTimeout(r.Context(), livingHeartbeatTitleReadTimeout)
		defer cancel()
		observedAt := time.Now().UTC()
		activities, ready := scheduler.LivingSnapshot(readCtx, titleLookup)
		if !ready {
			http.Error(w, "agent heartbeat scheduler unavailable", http.StatusServiceUnavailable)
			return
		}
		routineReaderProvided := len(routineReaders) > 0
		var routineReader RoutineActivityReader
		if routineReaderProvided {
			routineReader = routineReaders[0]
		}
		routineActors := make([]string, 0, len(activities))
		for _, activity := range activities {
			if activity.State == "waiting" || (activity.State == "running" && activity.ActivityKind == "routine") {
				routineActors = append(routineActors, activity.ActorID)
			}
		}
		routineByActor := make(map[string]RoutineActivity, len(routineActors))
		if routineReaderProvided {
			if batchReader, ok := routineReader.(RoutineActivityBatchReader); ok {
				values, err := batchReader.CurrentRoutineActivities(readCtx, routineActors)
				if err != nil {
					for _, actorID := range routineActors {
						routineByActor[actorID] = RoutineActivity{Availability: "unavailable"}
					}
				} else {
					for _, actorID := range routineActors {
						routineByActor[actorID] = validRoutineActivity(values[actorID])
					}
				}
			} else {
				for _, actorID := range routineActors {
					projection := RoutineActivity{Availability: "disabled"}
					if routineReader != nil {
						value, err := routineReader.CurrentRoutineActivity(readCtx, actorID)
						if err != nil {
							projection = RoutineActivity{Availability: "unavailable"}
						} else {
							projection = validRoutineActivity(value)
						}
					}
					routineByActor[actorID] = projection
				}
			}
		}
		var currentByActor map[string][]modulecore.TaskID
		if currentTasks != nil {
			var taskErr error
			currentByActor, taskErr = currentTasks.CurrentRunningTaskIDs(readCtx)
			if taskErr != nil {
				http.Error(w, "agent task projection unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		// Routine/WORLD and Task reads may block. Re-snapshot after both so a
		// newly acquired or actively running lease always wins over idle activity.
		activities, ready = scheduler.LivingSnapshot(readCtx, titleLookup)
		if !ready {
			http.Error(w, "agent heartbeat scheduler unavailable", http.StatusServiceUnavailable)
			return
		}
		if currentTasks != nil {
			mergeCurrentRunningTaskActivities(activities, currentByActor)
		}
		agents := make([]livingHeartbeatAgent, 0, len(activities))
		for _, activity := range activities {
			agent := livingHeartbeatAgent{
				ActorID: activity.ActorID, State: activity.State, ActivityKind: activity.ActivityKind, GameID: activity.GameID, Title: activity.Title,
			}
			projection, hasProjection := routineByActor[activity.ActorID]
			if activity.State == "waiting" {
				if !hasProjection {
					projection = RoutineActivity{Availability: "unavailable"}
				}
				if routineReaderProvided {
					agent.RoutineAvailability = projection.Availability
				}
				if projection.Availability == "available" && (projection.OngoingActivity == "sleep_window" || projection.OngoingActivity == "rest" || projection.OngoingActivity == "moving_home") {
					agent.OngoingActivity = projection.OngoingActivity
				} else if projection.Availability == "available" {
					agent.OngoingActivity = "waiting"
				}
			} else if activity.State == "running" && activity.ActivityKind == "routine" &&
				routineReaderProvided && hasProjection && projection.Availability == "available" &&
				projection.OngoingActivity == "moving_home" {
				agent.RoutineAvailability = "available"
				agent.OngoingActivity = "moving_home"
			}
			agents = append(agents, agent)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(livingHeartbeatResponse{
			ObservedAt: observedAt.Format(time.RFC3339Nano),
			Agents:     agents,
		})
	}
}

func mergeCurrentRunningTaskActivities(activities []agentheartbeat.LivingActivity, currentByActor map[string][]modulecore.TaskID) {
	for index := range activities {
		if len(currentByActor[activities[index].ActorID]) == 0 || activities[index].State == "running" || activities[index].State == "stopping" {
			continue
		}
		activities[index].State = "running"
		activities[index].ActivityKind = string(agentheartbeat.ActivityKindTask)
		activities[index].GameID = ""
		activities[index].Title = ""
	}
}

func validRoutineActivity(value RoutineActivity) RoutineActivity {
	if (value.Availability == "available" || value.Availability == "disabled" || value.Availability == "unavailable") &&
		(value.OngoingActivity == "" || value.OngoingActivity == "sleep_window" || value.OngoingActivity == "rest" || value.OngoingActivity == "waiting" || value.OngoingActivity == "moving_home") &&
		(value.Availability == "available" || value.OngoingActivity == "") {
		return value
	}
	return RoutineActivity{Availability: "unavailable"}
}
