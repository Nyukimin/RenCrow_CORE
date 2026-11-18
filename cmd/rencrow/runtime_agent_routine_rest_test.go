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

func TestInPlaceRelaxationCheckpointKeepsOriginalCompletionTime(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	observedAt := clock
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
	if _, _, err := runtime.ensureState(ctx, "mio"); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.draftIntent(ctx, "mio", agentroutine.Plan{Kind: agentroutine.ActivityRelax, Date: "2026-11-15"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	initialProgress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = runtime.confirmInPlaceRelaxation(ctx, state, initialProgress)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil || !checkpoint.EffectCompleted || !checkpoint.CompletedAt.Equal(observedAt) {
		t.Fatalf("persisted relaxation = %+v err=%v; want original observation and completion saved together", checkpoint, err)
	}
	clock = clock.Add(3 * time.Hour)
	window, ok := runtime.routineRestWindow(state, *state.Pending, checkpoint)
	if !ok || !window.Until.Equal(observedAt.Add(45*time.Minute)) || window.Until.After(clock) {
		t.Fatalf("replayed relaxation interval = %+v eligible=%t; want original, already expired interval", window, ok)
	}
}
