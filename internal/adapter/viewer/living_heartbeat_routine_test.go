package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type routineActivityReaderFunc func(context.Context, string) (RoutineActivity, error)

func (f routineActivityReaderFunc) CurrentRoutineActivity(ctx context.Context, actorID string) (RoutineActivity, error) {
	return f(ctx, actorID)
}

type livingHeartbeatCurrentTaskReaderFunc func(context.Context) (map[string][]modulecore.TaskID, error)

func (f livingHeartbeatCurrentTaskReaderFunc) CurrentRunningTaskIDs(ctx context.Context) (map[string][]modulecore.TaskID, error) {
	return f(ctx)
}

func TestLivingHeartbeatRoutineActivityOnlyMergesIntoWaitingAndReportsReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reader      RoutineActivityReader
		wantAvail   string
		wantOngoing string
	}{
		{name: "disabled provider", wantAvail: "disabled"},
		{name: "read failure", reader: routineActivityReaderFunc(func(context.Context, string) (RoutineActivity, error) {
			return RoutineActivity{}, errors.New("private database detail")
		}), wantAvail: "unavailable"},
		{name: "available waiting", reader: routineActivityReaderFunc(func(context.Context, string) (RoutineActivity, error) {
			return RoutineActivity{Availability: "available", OngoingActivity: "waiting"}, nil
		}), wantAvail: "available", wantOngoing: "waiting"},
		{name: "disabled never exposes stale activity", reader: routineActivityReaderFunc(func(context.Context, string) (RoutineActivity, error) {
			return RoutineActivity{Availability: "disabled", OngoingActivity: "rest"}, nil
		}), wantAvail: "unavailable"},
		{name: "sleep window", reader: routineActivityReaderFunc(func(context.Context, string) (RoutineActivity, error) {
			return RoutineActivity{Availability: "available", OngoingActivity: "sleep_window"}, nil
		}), wantAvail: "available", wantOngoing: "sleep_window"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler, cancel := startRoutineProjectionScheduler(t)
			defer cancel()
			rec := serveRoutineProjection(scheduler, tc.reader)
			var payload struct {
				Agents []struct {
					State               string `json:"state"`
					RoutineAvailability string `json:"routine_availability"`
					OngoingActivity     string `json:"ongoing_activity"`
				} `json:"agents"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || len(payload.Agents) != 1 || payload.Agents[0].State != "waiting" || payload.Agents[0].RoutineAvailability != tc.wantAvail || payload.Agents[0].OngoingActivity != tc.wantOngoing {
				t.Fatalf("status=%d agents=%+v body=%s", rec.Code, payload.Agents, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "private database detail") {
				t.Fatalf("routine read error leaked into response: %s", rec.Body.String())
			}
		})
	}
}

func TestLivingHeartbeatRechecksLeaseAfterRoutineReadAndActiveWorkWins(t *testing.T) {
	scheduler, cancel := startRoutineProjectionScheduler(t)
	defer cancel()
	var release func()
	reader := routineActivityReaderFunc(func(ctx context.Context, actorID string) (RoutineActivity, error) {
		if actorID != "mio" {
			t.Errorf("reader actor=%q, want mio", actorID)
		}
		var err error
		release, err = scheduler.Acquire(ctx, "mio", domaintask.PriorityCritical)
		if err != nil {
			return RoutineActivity{}, err
		}
		return RoutineActivity{Availability: "available", OngoingActivity: "sleep_window"}, nil
	})
	rec := serveRoutineProjection(scheduler, reader)
	if release != nil {
		release()
	}
	var payload struct {
		Agents []struct {
			State           string `json:"state"`
			OngoingActivity string `json:"ongoing_activity"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(payload.Agents) != 1 || payload.Agents[0].State != "running" || payload.Agents[0].OngoingActivity != "" {
		t.Fatalf("state changed during reader call was hidden: status=%d agents=%+v body=%s", rec.Code, payload.Agents, rec.Body.String())
	}
}

func TestLivingHeartbeatMergesCanonicalRunningTasksWithoutPublishingIdentity(t *testing.T) {
	scheduler, cancel := startRoutineProjectionScheduler(t)
	defer cancel()
	taskID := modulecore.NewTaskID()
	currentTasks := livingHeartbeatCurrentTaskReaderFunc(func(context.Context) (map[string][]modulecore.TaskID, error) {
		return map[string][]modulecore.TaskID{"mio": {taskID}}, nil
	})
	routine := routineActivityReaderFunc(func(context.Context, string) (RoutineActivity, error) {
		return RoutineActivity{Availability: "available", OngoingActivity: "rest"}, nil
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	HandleLivingHeartbeatWithCurrentTasks(scheduler, nil, nil, currentTasks, routine).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Agents []struct {
			ActorID             string `json:"actor_id"`
			State               string `json:"state"`
			ActivityKind        string `json:"activity_kind"`
			RoutineAvailability string `json:"routine_availability"`
			OngoingActivity     string `json:"ongoing_activity"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Agents) != 1 || payload.Agents[0].ActorID != "mio" || payload.Agents[0].State != "running" ||
		payload.Agents[0].ActivityKind != string(agentheartbeat.ActivityKindTask) ||
		payload.Agents[0].RoutineAvailability != "" || payload.Agents[0].OngoingActivity != "" {
		t.Fatalf("heartbeat agent = %+v, want running foreground with rest suppressed", payload.Agents)
	}
	if strings.Contains(recorder.Body.String(), string(taskID)) || strings.Contains(recorder.Body.String(), "task_id") {
		t.Fatalf("public heartbeat exposed canonical Task identity: %s", recorder.Body.String())
	}
}

func TestCurrentTaskMergePreservesActiveAndStoppingLeases(t *testing.T) {
	activities := []agentheartbeat.LivingActivity{
		{ActorID: "mio", State: "waiting"},
		{ActorID: "shiro", State: "checking"},
		{ActorID: "midori", State: "running", ActivityKind: string(agentheartbeat.ActivityKindGame), GameID: "nethack"},
		{ActorID: "aka", State: "stopping", ActivityKind: string(agentheartbeat.ActivityKindPrivate), Title: "visible title"},
	}
	current := map[string][]modulecore.TaskID{
		"mio":    {modulecore.NewTaskID()},
		"shiro":  {modulecore.NewTaskID()},
		"midori": {modulecore.NewTaskID()},
		"aka":    {modulecore.NewTaskID()},
	}
	mergeCurrentRunningTaskActivities(activities, current)
	if activities[0].State != "running" || activities[0].ActivityKind != string(agentheartbeat.ActivityKindTask) ||
		activities[1].State != "running" || activities[1].ActivityKind != string(agentheartbeat.ActivityKindTask) {
		t.Fatalf("idle scheduler activities = %+v, want task activity", activities[:2])
	}
	if activities[2].ActivityKind != string(agentheartbeat.ActivityKindGame) || activities[2].GameID != "nethack" ||
		activities[3].State != "stopping" || activities[3].ActivityKind != string(agentheartbeat.ActivityKindPrivate) || activities[3].Title != "visible title" {
		t.Fatalf("active game/stopping lease was replaced by Task projection: %+v", activities[2:])
	}
}

func TestLivingHeartbeatRechecksLeaseAfterTaskProjection(t *testing.T) {
	scheduler, cancel := startRoutineProjectionScheduler(t)
	defer cancel()
	var release func()
	currentTasks := livingHeartbeatCurrentTaskReaderFunc(func(ctx context.Context) (map[string][]modulecore.TaskID, error) {
		var err error
		release, err = scheduler.AcquireGameTurn(ctx, "mio", domaintask.PriorityCritical, "nethack")
		if err != nil {
			return nil, err
		}
		return map[string][]modulecore.TaskID{"mio": {modulecore.NewTaskID()}}, nil
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	HandleLivingHeartbeatWithCurrentTasks(scheduler, nil, nil, currentTasks).ServeHTTP(recorder, request)
	if release != nil {
		release()
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"activity_kind":"game"`) ||
		!strings.Contains(recorder.Body.String(), `"game_id":"nethack"`) || strings.Contains(recorder.Body.String(), `"activity_kind":"task"`) {
		t.Fatalf("lease acquired during Task read was hidden: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestLivingHeartbeatKeepsExplicitGameLeaseAndFailsWhenTaskProjectionIsUnavailable(t *testing.T) {
	scheduler, cancel := startRoutineProjectionScheduler(t)
	defer cancel()
	release, err := scheduler.AcquireGameTurn(context.Background(), "mio", domaintask.PriorityCritical, "nethack")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	currentTasks := livingHeartbeatCurrentTaskReaderFunc(func(context.Context) (map[string][]modulecore.TaskID, error) {
		return map[string][]modulecore.TaskID{"mio": {modulecore.NewTaskID()}}, nil
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	HandleLivingHeartbeatWithCurrentTasks(scheduler, nil, nil, currentTasks).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"activity_kind":"game"`) {
		t.Fatalf("explicit game activity was not preserved: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	failure := livingHeartbeatCurrentTaskReaderFunc(func(context.Context) (map[string][]modulecore.TaskID, error) {
		return nil, errors.New("private task store detail")
	})
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	HandleLivingHeartbeatWithCurrentTasks(scheduler, nil, nil, failure).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "private task store detail") {
		t.Fatalf("task projection failure status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func startRoutineProjectionScheduler(t *testing.T) (*agentheartbeat.Scheduler, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := agentheartbeat.New([]string{"mio"}, agentheartbeat.Sources{}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ready := scheduler.LivingSnapshot(context.Background(), nil); ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, ready := scheduler.LivingSnapshot(context.Background(), nil); !ready {
		cancel()
		t.Fatal("scheduler did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-started:
			if err != nil {
				t.Errorf("scheduler Start: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("scheduler did not stop")
		}
	})
	return scheduler, cancel
}

func serveRoutineProjection(scheduler *agentheartbeat.Scheduler, reader RoutineActivityReader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	HandleLivingHeartbeat(scheduler, nil, nil, reader).ServeHTTP(rec, request)
	return rec
}
