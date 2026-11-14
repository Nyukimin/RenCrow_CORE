package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
)

func TestRoutineWorldDueWaitUsesOwnerClockInsteadOfSchedulerClock(t *testing.T) {
	ownerNow := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC)
	schedulerNow := ownerNow.Add(24 * 365 * time.Hour)
	due := ownerNow.Add(60 * time.Millisecond)
	runtime := &agentRoutineRuntime{
		now:      func() time.Time { return schedulerNow },
		worldNow: func() time.Time { return ownerNow },
	}
	started := time.Now()
	if err := runtime.waitRoutineWorldDue(context.Background(), nil, due, time.Second); err != nil {
		t.Fatalf("waitRoutineWorldDue: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("owner due wait returned after %s; scheduler clock incorrectly made owner activity immediately due", elapsed)
	}
}

func TestRoutineWorldDiaryIncludesLifecycleStartAndSafeRoomLabel(t *testing.T) {
	tests := []struct {
		kind       string
		payload    string
		want       string
		wantKind   string
		wantNoText string
	}{
		{
			kind: "life.home.sleep.started", payload: `{"actor_id":"mio","room_id":"upper-room-2-1","activity_kind":"sleep","started_at":"2026-11-14T01:00:00Z"}`,
			want: "ミオの部屋", wantKind: "sleep", wantNoText: "upper-room-2-1",
		},
		{
			kind: "life.home.move.started", payload: `{"actor_id":"shiro","target_room_id":"living","activity_kind":"move","started_at":"2026-11-14T01:00:00Z"}`,
			want: "リビング", wantKind: "relax", wantNoText: "target_room_id",
		},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			summary, ok := routineWorldEventSummary(worldread.Event{Kind: test.kind, Payload: []byte(test.payload)})
			if !ok || !strings.Contains(summary, test.want) {
				t.Fatalf("summary=%q relevant=%v, want readable room %q", summary, ok, test.want)
			}
			if strings.Contains(summary, test.wantNoText) {
				t.Fatalf("diary summary exposed raw room/payload identifier: %q", summary)
			}
			kind := routineWorldDiaryActivityKind(worldread.Event{Kind: test.kind, Payload: []byte(test.payload)})
			if string(kind) != test.wantKind {
				t.Fatalf("diary activity kind = %q, want %q", kind, test.wantKind)
			}
		})
	}
}
