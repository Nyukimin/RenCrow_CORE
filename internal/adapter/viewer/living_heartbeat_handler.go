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

type livingHeartbeatResponse struct {
	ObservedAt string                 `json:"observed_at"`
	Agents     []livingHeartbeatAgent `json:"agents"`
}

type livingHeartbeatAgent struct {
	ActorID      string `json:"actor_id"`
	State        string `json:"state"`
	ActivityKind string `json:"activity_kind,omitempty"`
	Title        string `json:"title,omitempty"`
}

// HandleLivingHeartbeat returns a small read-only view of the actual CORE
// actor scheduler leases. It omits task identity, errors, prompts, and source
// content. A canonical Task title is included only after the existing public
// Task visibility policy authorizes its current publication binding.
func HandleLivingHeartbeat(scheduler *agentheartbeat.Scheduler, tasks LivingHeartbeatTaskReader, visibility PublicTaskVisibility) http.HandlerFunc {
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
		agents := make([]livingHeartbeatAgent, 0, len(activities))
		for _, activity := range activities {
			agents = append(agents, livingHeartbeatAgent{
				ActorID: activity.ActorID, State: activity.State, ActivityKind: activity.ActivityKind, Title: activity.Title,
			})
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(livingHeartbeatResponse{
			ObservedAt: observedAt.Format(time.RFC3339Nano),
			Agents:     agents,
		})
	}
}
