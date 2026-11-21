package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type currentActivityReaderFunc func(context.Context) (map[string]LivingHeartbeatCurrentActivity, error)

func (f currentActivityReaderFunc) CurrentActivities(ctx context.Context) (map[string]LivingHeartbeatCurrentActivity, error) {
	return f(ctx)
}

type currentActivityTaskReader map[modulecore.TaskID]domaintask.Task

func (r currentActivityTaskReader) GetTask(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	task, ok := r[taskID]
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	return task, nil
}

func TestLivingHeartbeatWithActivitiesRequiresAllActorsAndRechecksSnapshot(t *testing.T) {
	current := heartbeatCurrentActivityFixture()
	reads := 0
	reader := currentActivityReaderFunc(func(context.Context) (map[string]LivingHeartbeatCurrentActivity, error) {
		reads++
		if reads == 1 {
			return current, nil
		}
		updated := heartbeatCurrentActivityFixture()
		activity := updated["mio"]
		activity.Revision++
		updated["mio"] = activity
		return updated, nil
	})
	response := serveCurrentActivityHeartbeat(HandleLivingHeartbeatWithActivities(nil, nil, reader))
	if response.Code != http.StatusServiceUnavailable || reads != 2 {
		t.Fatalf("changed snapshot response=%d reads=%d body=%s; want 503 after exact recheck", response.Code, reads, response.Body.String())
	}

	missing := heartbeatCurrentActivityFixture()
	delete(missing, "gin")
	response = serveCurrentActivityHeartbeat(HandleLivingHeartbeatWithActivities(nil, nil,
		currentActivityReaderFunc(func(context.Context) (map[string]LivingHeartbeatCurrentActivity, error) { return missing, nil })))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("incomplete actor snapshot response=%d body=%s; want 503", response.Code, response.Body.String())
	}
}

func TestLivingHeartbeatWithActivitiesProjectsSafeDescriptorWithoutTaskID(t *testing.T) {
	current := heartbeatCurrentActivityFixture()
	reader := currentActivityReaderFunc(func(context.Context) (map[string]LivingHeartbeatCurrentActivity, error) { return current, nil })
	response := serveCurrentActivityHeartbeat(HandleLivingHeartbeatWithActivities(nil, nil, reader))
	if response.Code != http.StatusOK {
		t.Fatalf("response=%d body=%s; want 200", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-task-id") || strings.Contains(response.Body.String(), "secret title") {
		t.Fatalf("public heartbeat leaked private Task identity: %s", response.Body.String())
	}
	var payload struct {
		Agents []struct {
			ActorID string `json:"actor_id"`
			Current *struct {
				Kind     string `json:"kind"`
				Label    string `json:"label"`
				Phase    string `json:"phase"`
				Revision uint64 `json:"revision"`
			} `json:"current_activity"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Agents) != len(superagent.PrivateChatQueueActorIDs()) {
		t.Fatalf("actor count=%d; want %d", len(payload.Agents), len(superagent.PrivateChatQueueActorIDs()))
	}
	if payload.Agents[0].Current == nil || payload.Agents[0].Current.Kind != "rest" || payload.Agents[0].Current.Label != "休憩中" ||
		payload.Agents[0].Current.Phase != "resting" || payload.Agents[0].Current.Revision != 3 {
		t.Fatalf("current activity=%+v; want safe current rest descriptor", payload.Agents[0].Current)
	}
}

func TestLivingHeartbeatWithActivitiesRechecksVisibilityBeforeEmittingTitle(t *testing.T) {
	current := heartbeatCurrentActivityFixture()
	tasks := make(currentActivityTaskReader, len(current))
	for _, activity := range current {
		tasks[activity.Task.TaskID] = activity.Task
	}
	visibilityChecks := 0
	visibility := PublicTaskVisibility(func(context.Context, domaintask.Task) (bool, error) {
		visibilityChecks++
		return visibilityChecks <= len(current), nil
	})
	reader := currentActivityReaderFunc(func(context.Context) (map[string]LivingHeartbeatCurrentActivity, error) { return current, nil })
	response := serveCurrentActivityHeartbeat(HandleLivingHeartbeatWithActivities(tasks, visibility, reader))
	if response.Code != http.StatusServiceUnavailable || visibilityChecks <= len(current) {
		t.Fatalf("revoked visibility response=%d checks=%d body=%s; want 503 after final visibility recheck", response.Code, visibilityChecks, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret title") {
		t.Fatalf("revoked Task title was emitted: %s", response.Body.String())
	}
}

func heartbeatCurrentActivityFixture() map[string]LivingHeartbeatCurrentActivity {
	result := make(map[string]LivingHeartbeatCurrentActivity, 8)
	for _, actorID := range superagent.PrivateChatQueueActorIDs() {
		id := modulecore.NewTaskID()
		kind, label, phase := "evaluating", "Next activity", "waiting"
		if actorID == "mio" {
			kind, label, phase = "rest", "Rest", "resting"
		}
		task := domaintask.Task{TaskID: id, Title: "secret title", Status: domaintask.StatusRunning,
			Activity: &domaintask.ActivityMetadata{Kind: domaintask.ActivityKind(kind), Label: label, Phase: phase, Revision: 3,
				Participants: []domaintask.ActivityParticipant{{ActorID: actorID, IsCurrent: true, Revision: 3}}}}
		formatted, _ := FormatPublicCurrentActivity(kind, label, phase, actorID, 3)
		result[actorID] = LivingHeartbeatCurrentActivity{Task: task, Kind: formatted.Kind, Label: formatted.Label,
			Phase: formatted.Phase, Revision: 3, State: "waiting", ActivityKind: "routine"}
	}
	return result
}

func serveCurrentActivityHeartbeat(handler http.HandlerFunc) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	handler.ServeHTTP(response, request)
	return response
}
