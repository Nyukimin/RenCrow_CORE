package main

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

func TestAgentRoutinePresenceKeepsExactReservedPeerAndRejectsBusyOrSleepingPeer(t *testing.T) {
	loc, err := time.LoadLocation(agentroutine.JST)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 13, 7, 0, 0, 0, loc)
	workers := map[string]privateTaskWorker{
		"mio":   &privateActorTextWorker{actorID: "mio", provider: &privateTextWorkerProviderSpy{response: "ok"}},
		"shiro": &privateActorTextWorker{actorID: "shiro", provider: &privateTextWorkerProviderSpy{response: "ok"}},
	}
	runtime := &agentRoutineRuntime{
		cfg:           &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{SharedRooms: []string{"attic"}}}},
		worldActorIDs: map[string]string{"world-mio": "mio", "world-shiro": "shiro"},
		workers:       workers, canStartIdle: func(string) bool { return false }, now: func() time.Time { return now },
	}
	snapshot := worldread.Snapshot{WorldID: "world", Actors: []worldread.Actor{
		{ActorID: "world-mio", Present: true, RoomID: "attic"},
		{ActorID: "world-shiro", Present: true, RoomID: "attic"},
	}}
	if got := runtime.presence(snapshot, "mio", now, true, nil).ChatParticipants; len(got) != 0 {
		t.Fatalf("busy peer candidates = %v, want none", got)
	}
	if got := runtime.presence(snapshot, "mio", now, true, []string{"shiro"}).ChatParticipants; len(got) != 1 || got[0] != "shiro" {
		t.Fatalf("reserved peer candidates = %v, want exact reserved Shiro", got)
	}
	snapshot.Actors[1].ActivityID = "sleep"
	if got := runtime.presence(snapshot, "mio", now, true, []string{"shiro"}).ChatParticipants; len(got) != 0 {
		t.Fatalf("sleeping peer candidates = %v, want none", got)
	}
	aliasedWithoutConfig := worldread.Snapshot{WorldID: "world", Actors: []worldread.Actor{{ActorID: "mio", Present: true, RoomID: "attic"}}}
	if got := runtime.presence(aliasedWithoutConfig, "mio", now, false, nil); got.SharedRoom != agentroutine.AvailabilityUnknown {
		t.Fatalf("same-spelling WORLD identity presence = %+v, want unknown without explicit mapping", got)
	}
}

func TestAgentRoutineHobbyRotationAndEligibilityDeadlines(t *testing.T) {
	if routineHobbyLabel("midori", "2026-11-13", 100) == routineHobbyLabel("midori", "2026-11-13", 101) {
		t.Fatal("adjacent JST half-hour slots did not rotate the hobby category")
	}
	loc, err := time.LoadLocation(agentroutine.JST)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 13, 7, 15, 0, 0, loc)
	runtime := &agentRoutineRuntime{now: func() time.Time { return now }}
	chat := &agentroutine.Intent{ActorID: "mio", Kind: agentroutine.ActivityMorningChat}
	chatCtx, cancel := runtime.withRoutineWindow(context.Background(), chat, agentroutine.State{})
	defer cancel()
	deadline, ok := chatCtx.Deadline()
	if !ok || time.Until(deadline) < time.Hour+44*time.Minute || time.Until(deadline) > time.Hour+46*time.Minute {
		t.Fatalf("morning chat deadline = %v, has=%t; want about 1h45m from the injected 07:15 clock", deadline, ok)
	}

	now = time.Date(2026, time.November, 13, 23, 0, 0, 0, loc)
	date := now.AddDate(0, 0, 1).Format("2006-01-02")
	sleepAt, _, err := agentroutine.ScheduleFor("mio", date)
	if err != nil {
		t.Fatal(err)
	}
	hobby := &agentroutine.Intent{ActorID: "mio", Kind: agentroutine.ActivityHobby}
	hobbyCtx, cancelHobby := runtime.withRoutineWindow(context.Background(), hobby, agentroutine.State{})
	defer cancelHobby()
	hobbyDeadline, ok := hobbyCtx.Deadline()
	wantRemaining := sleepAt.Sub(now)
	if !ok || time.Until(hobbyDeadline) < wantRemaining-time.Second || time.Until(hobbyDeadline) > wantRemaining+time.Second {
		t.Fatalf("night hobby deadline = %v, has=%t; want about %s until planned sleep entry", hobbyDeadline, ok, wantRemaining)
	}
}

func TestAgentRoutineWindowStopsAtPlannedSleepBetweenMidnightAndTwo(t *testing.T) {
	loc, err := time.LoadLocation(agentroutine.JST)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 14, 0, 30, 0, 0, loc)
	sleepAt := time.Date(2026, time.November, 14, 1, 15, 0, 0, loc)
	runtime := &agentRoutineRuntime{now: func() time.Time { return now }}
	state := agentroutine.State{PlannedSleep: sleepAt}
	for _, kind := range []agentroutine.ActivityKind{agentroutine.ActivityHobby, agentroutine.ActivityMorningChat} {
		ctx, cancel := runtime.withRoutineWindow(context.Background(), &agentroutine.Intent{ActorID: "mio", Kind: kind}, state)
		deadline, ok := ctx.Deadline()
		remaining := sleepAt.Sub(now)
		if !ok || time.Until(deadline) < remaining-time.Second || time.Until(deadline) > remaining+time.Second {
			cancel()
			t.Errorf("%s deadline = %v, has=%t; want about %s until planned sleep entry", kind, deadline, ok, remaining)
			continue
		}
		cancel()
	}
}

func TestAgentRoutineRelaxWindowStopsAtPlannedSleepAfter22(t *testing.T) {
	loc, err := time.LoadLocation(agentroutine.JST)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 14, 23, 0, 0, 0, loc)
	sleepDate := now.AddDate(0, 0, 1).Format("2006-01-02")
	sleepAt, _, err := agentroutine.ScheduleFor("mio", sleepDate)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &agentRoutineRuntime{now: func() time.Time { return now }}
	ctx, cancel := runtime.withRoutineWindow(context.Background(), &agentroutine.Intent{
		ActorID: "mio", Kind: agentroutine.ActivityRelax,
	}, agentroutine.State{})
	defer cancel()
	deadline, ok := ctx.Deadline()
	remaining := sleepAt.Sub(now)
	if !ok || time.Until(deadline) < remaining-time.Second || time.Until(deadline) > remaining+time.Second {
		t.Fatalf("late-evening relax deadline = %v, has=%t; want planned sleep in %s", deadline, ok, remaining)
	}

	for _, kind := range []agentroutine.ActivityKind{agentroutine.ActivitySleep, agentroutine.ActivityWake} {
		ctx, cancel := runtime.withRoutineWindow(context.Background(), &agentroutine.Intent{ActorID: "mio", Kind: kind}, agentroutine.State{})
		if _, ok := ctx.Deadline(); ok {
			cancel()
			t.Errorf("%s unexpectedly received a routine window deadline", kind)
			continue
		}
		cancel()
	}
}

func TestAgentRoutineDiaryWindowStopsAtPlannedSleepAndBoundaryActivitiesDoNotConsumeCooldown(t *testing.T) {
	loc, err := time.LoadLocation(agentroutine.JST)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 14, 23, 0, 0, 0, loc)
	runtime := &agentRoutineRuntime{now: func() time.Time { return now }}
	date := now.AddDate(0, 0, 1).Format("2006-01-02")
	sleepAt, _, err := agentroutine.ScheduleFor("mio", date)
	if err != nil {
		t.Fatal(err)
	}
	diaryCtx, cancel := runtime.withRoutineWindow(context.Background(), &agentroutine.Intent{ActorID: "mio", Kind: agentroutine.ActivityDiary}, agentroutine.State{})
	defer cancel()
	deadline, ok := diaryCtx.Deadline()
	remaining := sleepAt.Sub(now)
	if !ok || time.Until(deadline) < remaining-time.Second || time.Until(deadline) > remaining+time.Second {
		t.Fatalf("diary deadline = %v, has=%t; want planned sleep in %s", deadline, ok, remaining)
	}
	for _, kind := range []agentroutine.ActivityKind{agentroutine.ActivitySleep, agentroutine.ActivityWake, agentroutine.ActivityDiary} {
		if got := routineCooldown(kind); got != 0 {
			t.Errorf("routineCooldown(%s) = %s, want zero because schedule/diary state gates the next attempt", kind, got)
		}
	}
	for kind, want := range map[agentroutine.ActivityKind]time.Duration{
		agentroutine.ActivityMorningChat: 15 * time.Minute,
		agentroutine.ActivityHobby:       30 * time.Minute,
		agentroutine.ActivityRelax:       45 * time.Minute,
	} {
		if got := routineCooldown(kind); got != want {
			t.Errorf("routineCooldown(%s) = %s, want %s", kind, got, want)
		}
	}
}
