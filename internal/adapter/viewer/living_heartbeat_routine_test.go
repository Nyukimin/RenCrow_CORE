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
)

type routineActivityReaderFunc func(context.Context, string) (RoutineActivity, error)

func (f routineActivityReaderFunc) CurrentRoutineActivity(ctx context.Context, actorID string) (RoutineActivity, error) {
	return f(ctx, actorID)
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

func TestLivingHeartbeatMergesStepStartedRestOnlyIntoRunningRoutine(t *testing.T) {
	for _, activityValue := range []string{"sofa_reading", "sofa_chat", "dining_tea_chat", "dining_tea"} {
		t.Run(activityValue, func(t *testing.T) {
			scheduler, cancel, waitRun := startRunningRoutineProjectionScheduler(t)
			defer cancel()
			waitRun()
			reader := routineActivityReaderFunc(func(context.Context, string) (RoutineActivity, error) {
				return RoutineActivity{Availability: "available", OngoingActivity: activityValue}, nil
			})
			rec := serveRoutineProjection(scheduler, reader)
			var payload struct {
				Agents []struct {
					State               string `json:"state"`
					ActivityKind        string `json:"activity_kind"`
					RoutineAvailability string `json:"routine_availability"`
					OngoingActivity     string `json:"ongoing_activity"`
				} `json:"agents"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || len(payload.Agents) != 1 ||
				payload.Agents[0].State != "running" || payload.Agents[0].ActivityKind != "routine" ||
				payload.Agents[0].RoutineAvailability != "available" || payload.Agents[0].OngoingActivity != activityValue {
				t.Fatalf("status=%d agents=%+v body=%s", rec.Code, payload.Agents, rec.Body.String())
			}
		})
	}
}

func startRunningRoutineProjectionScheduler(t *testing.T) (*agentheartbeat.Scheduler, context.CancelFunc, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	scheduler := agentheartbeat.New([]string{"mio"}, agentheartbeat.Sources{
		Idle: func(context.Context, string) (agentheartbeat.Work, bool, error) {
			return agentheartbeat.Work{
				ID: "routine-test", ActivityKind: agentheartbeat.ActivityKindRoutine,
				Run: func(runCtx context.Context, stop <-chan struct{}) error {
					close(started)
					select {
					case <-runCtx.Done():
					case <-stop:
					}
					return nil
				},
			}, true, nil
		},
	}, nil)
	startedScheduler := make(chan error, 1)
	go func() { startedScheduler <- scheduler.Start(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("routine Work did not start")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-startedScheduler:
			if err != nil {
				t.Errorf("scheduler Start: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("routine scheduler did not stop")
		}
	})
	return scheduler, cancel, func() {}
}

func TestValidRoutineActivityAllowsOnlyTheFourRestProjections(t *testing.T) {
	for _, value := range []string{"sofa_reading", "sofa_chat", "dining_tea_chat", "dining_tea"} {
		got := validRoutineActivity(RoutineActivity{Availability: "available", OngoingActivity: value})
		if got.Availability != "available" || got.OngoingActivity != value {
			t.Errorf("validRoutineActivity(%q) = %+v", value, got)
		}
	}
	for _, value := range []string{"reading", "tea", "chat", "private_task"} {
		got := validRoutineActivity(RoutineActivity{Availability: "available", OngoingActivity: value})
		if got.Availability != "unavailable" || got.OngoingActivity != "" {
			t.Errorf("invalid routine activity %q was accepted: %+v", value, got)
		}
	}
}
