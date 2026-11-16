package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

func TestInPlaceRelaxationRejectsUnconfirmedOrBusyActor(t *testing.T) {
	for _, test := range []struct {
		name       string
		room       string
		activityID string
		kind       string
		wantErr    error
	}{
		{name: "unknown room", room: "outside", wantErr: errRoutineRestUnavailable},
		{name: "sleeping in allowed room", room: "living", activityID: "sleep-1", kind: "sleep", wantErr: errRoutineRestUnavailable},
		{name: "moving through allowed room", room: "living", activityID: "move-1", kind: "move", wantErr: errRoutineRestUnavailable},
		{name: "unidentified active activity", room: "living", kind: "exercise", wantErr: errRoutineRestUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
			runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet {
					t.Error("in-place rest attempted a WORLD mutation")
					http.Error(w, "read only", http.StatusMethodNotAllowed)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"schema_version": 1, "world_id": "world", "timeline_id": "timeline", "timeline_epoch": "epoch",
					"state_seq": 1, "state_hash": strings.Repeat("0", 64),
					"state": map[string]any{"actors": map[string]any{"mio-world": map[string]any{
						"room_id": test.room, "activity_id": test.activityID, "activity_kind": test.kind,
					}}},
				})
			}))
			defer server.Close()
			reader, err := worldread.New(worldread.Config{
				BaseURL: server.URL, Credential: "reader-token", WorldID: "world", TimelineID: "timeline", TimelineEpoch: "epoch",
				ActorIDs: []string{"mio-world"}, Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime.world = reader
			state := agentroutine.State{ActorID: "mio", Pending: &agentroutine.Intent{ActorID: "mio", Kind: agentroutine.ActivityRelax}}
			_, progress, err := runtime.confirmInPlaceRelaxation(context.Background(), state, routineProgress{})
			if !errors.Is(err, test.wantErr) || progress.EffectCompleted || progress.World != nil {
				t.Fatalf("unconfirmed relaxation = %+v err=%v; want no completed effect or room evidence", progress, err)
			}
			facts, err := runtime.store.ListFacts(context.Background(), "mio", "2026-11-15")
			if err != nil || len(facts) != 0 {
				t.Fatalf("unconfirmed relaxation facts = %+v err=%v; want none", facts, err)
			}
		})
	}
}

func TestPrivateRoomConfirmationDoesNotCompleteRelaxation(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 23, 30, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "mio-room")
	state, _, err := runtime.ensureState(ctx, "mio")
	if err != nil {
		t.Fatal(err)
	}
	state.ActivatedOn = "2026-11-16"
	state.NextDiaryDate = "2026-11-17"
	state, err = runtime.store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	plan, now, err := runtime.planFresh(ctx, "mio", false, nil)
	if err != nil || plan.Kind != agentroutine.ActivityRelax || plan.RelaxRoom != agentroutine.RelaxRoomPrivate {
		t.Fatalf("private quiet-time plan = %+v err=%v", plan, err)
	}
	state, err = runtime.draftIntent(ctx, "mio", plan, now)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		t.Fatal(err)
	}
	state, progress, err = runtime.confirmInPlaceRelaxation(ctx, state, progress)
	if err != nil {
		t.Fatalf("confirm private room: %v", err)
	}
	checkpoint, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil || checkpoint.EffectCompleted || !checkpoint.CompletedAt.IsZero() ||
		checkpoint.Rest == nil || checkpoint.Rest.PrivateReflection != "" || checkpoint.World == nil || checkpoint.World.RelaxRoomID != "mio-room" {
		t.Fatalf("room-only checkpoint = %+v err=%v; want room evidence without a completed activity", checkpoint, err)
	}
	if progress.EffectCompleted || progress.Rest.PrivateReflection != "" {
		t.Fatalf("returned room-only progress = %+v; must not claim completed reflection", progress)
	}
	_, progress, err = runtime.executeRelaxation(ctx, nil, state, checkpoint)
	if !errors.Is(err, errRoutineRestUnavailable) || progress.EffectCompleted || progress.Rest.PrivateReflection != "" {
		t.Fatalf("private room without a worker = %+v err=%v; want unavailable and incomplete", progress, err)
	}
}

func TestRelaxationCannotSucceedFromRoomPresenceWithoutAnExactSeat(t *testing.T) {
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
	state, _, err := runtime.ensureState(context.Background(), "mio")
	if err != nil {
		t.Fatal(err)
	}
	state, err = runtime.draftIntent(context.Background(), "mio", agentroutine.Plan{
		Kind: agentroutine.ActivityRelax, RelaxRoom: agentroutine.RelaxRoomShared, Date: "2026-11-15",
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		t.Fatal(err)
	}
	_, progress, err = runtime.executeRelaxation(context.Background(), nil, state, progress)
	if err == nil || progress.EffectCompleted || progress.World != nil {
		t.Fatalf("room-only relaxation = %+v err=%v; want unavailable without a confirmed exact seat and activity", progress, err)
	}
}
