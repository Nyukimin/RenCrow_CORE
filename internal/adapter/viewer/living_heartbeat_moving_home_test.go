package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestLivingHeartbeatProjectsVerifiedMoveForRunningRoutineOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		board    bool
		private  bool
		wantMove bool
		wantKind string
	}{
		{name: "routine keeps move label while running", wantMove: true, wantKind: "routine"},
		{name: "board keeps precedence", board: true, wantKind: "board"},
		{name: "private task keeps precedence", private: true, wantKind: "private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			sources := agentheartbeat.Sources{}
			work := agentheartbeat.Work{ID: "live-work", Priority: domaintask.PriorityLow, ActivityKind: agentheartbeat.ActivityKindRoutine, Run: holdHeartbeatWork}
			if tc.board {
				sources.Board = func(context.Context, string) (agentheartbeat.Work, bool, error) { return work, true, nil }
			} else if tc.private {
				sources.Private = func(context.Context, string) (agentheartbeat.Work, bool, error) { return work, true, nil }
			} else {
				sources.Idle = func(context.Context, string) (agentheartbeat.Work, bool, error) { return work, true, nil }
			}
			scheduler := agentheartbeat.New([]string{"mio"}, sources, nil)
			started := make(chan error, 1)
			go func() { started <- scheduler.Start(ctx) }()
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
				waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
				defer waitCancel()
				if err := scheduler.Wait(waitCtx); err != nil {
					t.Errorf("scheduler Wait: %v", err)
				}
			})
			deadline := time.Now().Add(time.Second)
			var state, activityKind string
			for time.Now().Before(deadline) {
				activities, ready := scheduler.LivingSnapshot(context.Background(), nil)
				if ready && len(activities) == 1 && activities[0].State == "running" {
					state, activityKind = activities[0].State, activities[0].ActivityKind
					break
				}
				time.Sleep(time.Millisecond)
			}
			if state != "running" {
				t.Fatal("scheduler did not reserve active work")
			}

			readerCalls := 0
			reader := routineActivityReaderFunc(func(context.Context, string) (RoutineActivity, error) {
				readerCalls++
				return RoutineActivity{Availability: "available", OngoingActivity: "moving_home"}, nil
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
			if rec.Code != http.StatusOK || len(payload.Agents) != 1 {
				t.Fatalf("heartbeat = %s / %+v", rec.Body.String(), payload.Agents)
			}
			agent := payload.Agents[0]
			if tc.wantMove {
				if readerCalls != 1 || activityKind != "routine" || agent.ActivityKind != "routine" || agent.State != "running" || agent.RoutineAvailability != "available" || agent.OngoingActivity != "moving_home" {
					t.Fatalf("running routine projection calls=%d kind=%s agent=%+v", readerCalls, activityKind, agent)
				}
			} else if readerCalls != 0 || agent.OngoingActivity != "" || agent.RoutineAvailability != "" || agent.ActivityKind != tc.wantKind {
				t.Fatalf("non-routine activity was overridden: calls=%d want kind=%s agent=%+v", readerCalls, tc.wantKind, agent)
			}
		})
	}
}

func holdHeartbeatWork(_ context.Context, stop <-chan struct{}) error {
	<-stop
	return nil
}

func TestLivingHeartbeatProjectsMoveWhileSchedulerWaiting(t *testing.T) {
	scheduler, _ := startRoutineProjectionScheduler(t)
	readerCalls := 0
	reader := routineActivityReaderFunc(func(context.Context, string) (RoutineActivity, error) {
		readerCalls++
		return RoutineActivity{Availability: "available", OngoingActivity: "moving_home"}, nil
	})
	rec := serveRoutineProjection(scheduler, reader)
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
	if rec.Code != http.StatusOK || len(payload.Agents) != 1 || payload.Agents[0].State != "waiting" ||
		payload.Agents[0].RoutineAvailability != "available" || payload.Agents[0].OngoingActivity != "moving_home" || readerCalls != 1 {
		t.Fatalf("waiting move projection calls=%d body=%s agents=%+v", readerCalls, rec.Body.String(), payload.Agents)
	}
}
