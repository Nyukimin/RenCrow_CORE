package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const livingHeartbeatPath = "/viewer/living/heartbeat"

const livingHeartbeatTitleReadTimeout = 500 * time.Millisecond

type LivingHeartbeatTaskReader interface {
	GetTask(context.Context, modulecore.TaskID) (domaintask.Task, error)
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
	ActorID             string `json:"actor_id"`
	State               string `json:"state"`
	ActivityKind        string `json:"activity_kind,omitempty"`
	Title               string `json:"title,omitempty"`
	RoutineAvailability string `json:"routine_availability,omitempty"`
	OngoingActivity     string `json:"ongoing_activity,omitempty"`
}

// HandleLivingHeartbeat returns a small read-only view of the actual CORE
// actor scheduler leases. It omits task identity, errors, prompts, and source
// content. A canonical Task title is included only after the existing public
// Task visibility policy authorizes its current publication binding.
func HandleLivingHeartbeat(scheduler *agentheartbeat.Scheduler, tasks LivingHeartbeatTaskReader, visibility PublicTaskVisibility, routineReaders ...RoutineActivityReader) http.HandlerFunc {
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
		// Routine reads may block on WORLD. Re-snapshot afterward so a newly
		// acquired or actively running lease always wins over idle activity.
		activities, ready = scheduler.LivingSnapshot(readCtx, titleLookup)
		if !ready {
			http.Error(w, "agent heartbeat scheduler unavailable", http.StatusServiceUnavailable)
			return
		}
		agents := make([]livingHeartbeatAgent, 0, len(activities))
		for _, activity := range activities {
			agent := livingHeartbeatAgent{
				ActorID: activity.ActorID, State: activity.State, ActivityKind: activity.ActivityKind, Title: activity.Title,
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

func validRoutineActivity(value RoutineActivity) RoutineActivity {
	if (value.Availability == "available" || value.Availability == "disabled" || value.Availability == "unavailable") &&
		(value.OngoingActivity == "" || value.OngoingActivity == "sleep_window" || value.OngoingActivity == "rest" || value.OngoingActivity == "waiting" || value.OngoingActivity == "moving_home") &&
		(value.Availability == "available" || value.OngoingActivity == "") {
		return value
	}
	return RoutineActivity{Availability: "unavailable"}
}
