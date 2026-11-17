package main

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestLivingConversationUsesDistinctSeatsAndTracksCurrentTurn(t *testing.T) {
	for _, room := range []string{"living", "dining"} {
		t.Run(room, func(t *testing.T) {
			runtime := &agentRoutineRuntime{}
			end := runtime.showLivingConversation("mio", "shiro", room)
			host := runtime.livingConversation["mio"]
			guest := runtime.livingConversation["shiro"]
			if host.slot == "" || guest.slot == "" || host.slot == guest.slot ||
				livingSeatRoom(host.slot) != room || livingSeatRoom(guest.slot) != room ||
				host.pose != "" || guest.pose != "" {
				t.Fatalf("unexpected conversation seats: host=%+v guest=%+v", host, guest)
			}
			runtime.livingConversationSpeaking("mio", true)
			if runtime.livingConversation["mio"].pose != "talk" || runtime.livingConversation["shiro"].pose != "" {
				t.Fatal("only the active participant should use the talk pose")
			}
			runtime.livingConversationSpeaking("mio", false)
			runtime.livingConversationSpeaking("shiro", true)
			if runtime.livingConversation["mio"].pose != "" || runtime.livingConversation["shiro"].pose != "talk" {
				t.Fatal("the second turn should transfer the talk pose")
			}
			end()
			if len(runtime.livingConversation) != 0 {
				t.Fatal("ending the conversation did not release both seat targets")
			}
		})
	}
}

func TestRunningGameLeaseTargetsOwnDeskAndProjectsTyping(t *testing.T) {
	_, deps, server := newRoutineHouseViewTestRuntime(t, true)
	defer server.Close()
	scheduler := startLivingSeatTestScheduler(t)
	deps.agentHeartbeat = scheduler
	runtime := deps.agentRoutine
	runtime.deps = deps

	release, err := scheduler.AcquireGameTurn(context.Background(), "mio", domaintask.PriorityHigh, "nethack")
	if err != nil {
		t.Fatalf("acquire real game-turn lease: %v", err)
	}
	defer release()

	seat, ready := runtime.livingSeats()
	if !ready {
		t.Fatal("running scheduler snapshot is unavailable")
	}
	if got, want := seat["mio"], (livingSeatActivity{room: "living", slot: "desk-mio", pose: "type"}); got != want {
		t.Fatalf("running mio game seat = %+v, want %+v", got, want)
	}

	actors := make([]worldread.Actor, 0, len(runtime.worldActorIDs))
	for worldID, coreID := range runtime.worldActorIDs {
		actor := worldread.Actor{
			ActorID: worldID, Present: true, RoomID: "room-" + coreID,
			Position: [3]float64{1, 2, 0}, HasPosition: true,
			HomeSceneSHA256: routineHouseViewTestSceneSHA256,
		}
		if coreID == "mio" {
			actor.RoomID = "living"
			actor.SlotID = "desk-mio"
		}
		actors = append(actors, actor)
	}
	read := routineSnapshotRead{
		snapshot: worldread.Snapshot{
			WorldID: "home-world", StateSeq: 45, Actors: actors,
			HomeSceneSHA256: routineHouseViewTestSceneSHA256,
		},
		readAt: time.Date(2026, time.November, 16, 0, 0, 0, 0, time.UTC),
	}
	projected, err := runtimeRoutineHouseViewProject(runtime, read)
	if err != nil {
		t.Fatalf("project canonical house view: %v", err)
	}
	foundMio := false
	for _, actor := range projected.Actors {
		if actor.ID != "mio" {
			continue
		}
		foundMio = true
		if actor.SeatID != "desk-mio" || actor.SeatAction != "type" {
			t.Fatalf("projected mio seat = %q/%q, want desk-mio/type", actor.SeatID, actor.SeatAction)
		}
	}
	if !foundMio {
		t.Fatal("canonical house projection omitted mio")
	}

	release()
	seat, ready = runtime.livingSeats()
	if !ready {
		t.Fatal("running scheduler snapshot is unavailable after release")
	}
	if _, exists := seat["mio"]; exists {
		t.Fatalf("released game lease left a stale desired seat: %+v", seat["mio"])
	}
	projected, err = runtimeRoutineHouseViewProject(runtime, read)
	if err != nil {
		t.Fatalf("project house view after game lease release: %v", err)
	}
	for _, actor := range projected.Actors {
		if actor.ID == "mio" && actor.SeatAction != "" {
			t.Fatalf("released game lease retained typing pose %q", actor.SeatAction)
		}
	}
}

func startLivingSeatTestScheduler(t *testing.T) *agentheartbeat.Scheduler {
	t.Helper()
	scheduler := agentheartbeat.New(agentHeartbeatActorIDs, agentheartbeat.Sources{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer waitCancel()
		if err := scheduler.Wait(waitCtx); err != nil {
			t.Errorf("wait for scheduler shutdown: %v", err)
		}
	})
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if _, ready := scheduler.LivingSnapshot(context.Background(), nil); ready {
			return scheduler
		}
		select {
		case <-deadline.C:
			t.Fatal("scheduler did not become available")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
