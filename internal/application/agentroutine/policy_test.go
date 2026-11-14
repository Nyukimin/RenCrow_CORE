package agentroutine

import (
	"testing"
	"time"
)

func jst(year int, month time.Month, day, hour, minute int) time.Time {
	loc, err := time.LoadLocation(JST)
	if err != nil {
		panic(err)
	}
	return time.Date(year, month, day, hour, minute, 0, 0, loc)
}

func TestDecideUsesJSTAndStableNextDateScheduleAtNight(t *testing.T) {
	actor := ActorState{ID: "Mio"}
	presence := Presence{OwnRoom: AvailabilityAvailable}
	first := Decide(jst(2026, time.November, 13, 23, 0), actor, State{}, presence, true)
	second := Decide(jst(2026, time.November, 13, 23, 0), actor, State{}, presence, true)
	if first.ScheduleDate != "2026-11-14" {
		t.Fatalf("ScheduleDate = %q, want next JST date", first.ScheduleDate)
	}
	if first.PlannedSleep != second.PlannedSleep || first.PlannedWake != second.PlannedWake {
		t.Fatalf("schedule changed for same actor/date: first=%+v second=%+v", first, second)
	}
	sleep := first.PlannedSleep.In(first.PlannedSleep.Location())
	wake := first.PlannedWake.In(first.PlannedWake.Location())
	if sleep.Format("2006-01-02") != first.ScheduleDate || sleep.Hour() < 0 || sleep.Hour() >= 2 {
		t.Fatalf("planned sleep outside 00:00-02:00 JST: %v", sleep)
	}
	if wake.Format("2006-01-02") != first.ScheduleDate || wake.Hour() < 5 || wake.Hour() >= 8 {
		t.Fatalf("planned wake outside 05:00-08:00 JST: %v", wake)
	}
	if first.Kind != ActivityHobby {
		t.Fatalf("night kind = %q, want hobby", first.Kind)
	}
}

func TestDecideSkipsMissedSleepAfterTwoAndWaitsUntilWake(t *testing.T) {
	date := "2026-11-14"
	sleepAt := jst(2026, time.November, 14, 1, 0)
	wakeAt := jst(2026, time.November, 14, 6, 0)
	state := State{ScheduleDate: date, PlannedSleep: sleepAt, PlannedWake: wakeAt}
	presence := Presence{OwnRoom: AvailabilityAvailable}
	missed := Decide(jst(2026, time.November, 14, 2, 0), ActorState{ID: "Mio"}, state, presence, true)
	if missed.Kind == ActivitySleep {
		t.Fatalf("missed sleep was scheduled after 02:00: %+v", missed)
	}
	if missed.ScheduleDate != date {
		t.Fatalf("ScheduleDate = %q, want %q", missed.ScheduleDate, date)
	}

	state.LogicalSleepUntil = wakeAt
	stillSleeping := Decide(jst(2026, time.November, 14, 5, 59), ActorState{ID: "Mio"}, state, Presence{}, true)
	if stillSleeping.Kind != ActivitySleep || !stillSleeping.LogicalSleep {
		t.Fatalf("before planned wake = %+v, want logical sleep", stillSleeping)
	}
	wake := Decide(wakeAt, ActorState{ID: "Mio"}, state, Presence{}, true)
	if wake.Kind != ActivityWake || wake.LogicalSleep {
		t.Fatalf("at planned wake = %+v, want logical wake boundary", wake)
	}
}

func TestDecideEntersConfirmedPlannedSleepBeforeDiaryAndCooldown(t *testing.T) {
	date := "2026-11-14"
	sleepAt := jst(2026, time.November, 14, 1, 59)
	wakeAt := jst(2026, time.November, 14, 6, 0)
	now := sleepAt
	state := State{
		ActivatedOn:   "2026-11-13",
		NextDiaryDate: "2026-11-13",
		ScheduleDate:  date,
		PlannedSleep:  sleepAt,
		PlannedWake:   wakeAt,
		CooldownUntil: jst(2026, time.November, 14, 2, 5),
	}
	plan := Decide(now, ActorState{ID: "Mio"}, state, Presence{OwnRoom: AvailabilityAvailable}, true)
	if plan.Kind != ActivitySleep || !plan.LogicalSleep {
		t.Fatalf("confirmed sleep entry before cutoff = %+v, want logical sleep despite diary and cooldown", plan)
	}

	unknownRoom := Decide(now, ActorState{ID: "Mio"}, state, Presence{}, true)
	if unknownRoom.Kind == ActivitySleep || unknownRoom.LogicalSleep {
		t.Fatalf("sleep began without confirmed own-room availability: %+v", unknownRoom)
	}
}

func TestDecideMorningChoiceIsThreeToOneOnlyWhenBothCandidatesAreValid(t *testing.T) {
	actor := ActorState{ID: "Mio"}
	presence := Presence{SharedRoom: AvailabilityAvailable, OwnRoom: AvailabilityAvailable, ChatParticipants: []string{"Shiro"}}
	for i := uint64(0); i < 4; i++ {
		state := State{
			ChoiceIndex:  i,
			ScheduleDate: "2026-11-14",
			PlannedSleep: jst(2026, time.November, 14, 1, 0),
			PlannedWake:  jst(2026, time.November, 14, 6, 0),
		}
		plan := Decide(jst(2026, time.November, 14, 7, 0), actor, state, presence, true)
		want := ActivityMorningChat
		if i == 3 {
			want = ActivityHobby
		}
		if plan.Kind != want || !plan.CountChoice {
			t.Fatalf("choice %d = %+v, want %q with CountChoice", i, plan, want)
		}
		if want == ActivityMorningChat && len(plan.Participants) != 1 {
			t.Fatalf("choice %d chat participants = %v, want Shiro", i, plan.Participants)
		}
		if want != ActivityMorningChat && len(plan.Participants) != 0 {
			t.Fatalf("choice %d solo participants = %v, want none", i, plan.Participants)
		}
	}

	fallback := Decide(jst(2026, time.November, 14, 7, 0), actor, State{
		ChoiceIndex:  3,
		ScheduleDate: "2026-11-14",
		PlannedSleep: jst(2026, time.November, 14, 1, 0),
		PlannedWake:  jst(2026, time.November, 14, 6, 0),
	}, Presence{
		SharedRoom:       AvailabilityAvailable,
		ChatParticipants: []string{"Shiro"},
	}, false)
	if fallback.Kind != ActivityMorningChat || fallback.CountChoice {
		t.Fatalf("chat-only fallback = %+v, want chat without choice count", fallback)
	}
	if len(fallback.Participants) != 1 || fallback.Participants[0] != "Shiro" {
		t.Fatalf("chat-only fallback participants = %v, want Shiro", fallback.Participants)
	}
}

func TestDecideReturnsParticipantsOnlyForMorningChat(t *testing.T) {
	date := "2026-11-14"
	actor := ActorState{ID: "Mio", ActualWakeAt: jst(2026, time.November, 14, 6, 0)}
	state := State{ScheduleDate: date, PlannedSleep: jst(2026, time.November, 14, 1, 0), PlannedWake: actor.ActualWakeAt}
	now := jst(2026, time.November, 14, 7, 0)
	participants := []string{"Shiro"}

	solo := []struct {
		name     string
		presence Presence
		canHobby bool
		want     ActivityKind
	}{
		{
			name:     "hobby",
			presence: Presence{OwnRoom: AvailabilityAvailable, ChatParticipants: participants},
			canHobby: true,
			want:     ActivityHobby,
		},
		{
			name:     "relax",
			presence: Presence{OwnRoom: AvailabilityAvailable, ChatParticipants: participants},
			want:     ActivityRelax,
		},
		{
			name:     "wait",
			presence: Presence{ChatParticipants: participants},
			want:     ActivityWait,
		},
	}
	for _, test := range solo {
		t.Run(test.name, func(t *testing.T) {
			plan := Decide(now, actor, state, test.presence, test.canHobby)
			if plan.Kind != test.want || len(plan.Participants) != 0 {
				t.Fatalf("solo plan = %+v, want %q without participants", plan, test.want)
			}
		})
	}

	pending := Decide(now, actor, State{Pending: &Intent{ID: "hobby-1", ActorID: "Mio", Kind: ActivityHobby, Participants: participants}}, Presence{}, false)
	if !pending.Resuming || pending.Kind != ActivityHobby || len(pending.Participants) != 0 {
		t.Fatalf("resumed solo hobby = %+v, want no participants", pending)
	}
}

func TestDecideDoesNotTreatUnknownRoomAsAvailable(t *testing.T) {
	plan := Decide(jst(2026, time.November, 14, 23, 30), ActorState{ID: "Mio"}, State{}, Presence{}, true)
	if plan.Kind != ActivityWait {
		t.Fatalf("unknown private room produced action %q", plan.Kind)
	}
}

func TestDecideUsesConfirmedRoomForMorningRelaxAndDrainsDiaryBeforeNightHobby(t *testing.T) {
	sharedRelax := Decide(jst(2026, time.November, 14, 7, 0), ActorState{ID: "Mio"}, State{
		ScheduleDate: "2026-11-14",
		PlannedSleep: jst(2026, time.November, 14, 1, 0),
		PlannedWake:  jst(2026, time.November, 14, 6, 0),
	}, Presence{SharedRoom: AvailabilityAvailable}, false)
	if sharedRelax.Kind != ActivityRelax {
		t.Fatalf("confirmed shared room fallback = %+v, want relax", sharedRelax)
	}

	diaryFirst := Decide(jst(2026, time.November, 13, 23, 30), ActorState{ID: "Mio"}, State{
		ActivatedOn:   "2026-11-12",
		NextDiaryDate: "2026-11-12",
	}, Presence{OwnRoom: AvailabilityAvailable}, true)
	if diaryFirst.Kind != ActivityDiary || diaryFirst.Date != "2026-11-12" {
		t.Fatalf("night diary priority = %+v, want oldest eligible diary before hobby", diaryFirst)
	}
}

func TestDecideMarksPrivateNightRelaxAndSharedMorningRelax(t *testing.T) {
	now := jst(2026, time.November, 14, 23, 30)
	night := Decide(now, ActorState{ID: "Mio"}, State{}, Presence{OwnRoom: AvailabilityAvailable}, false)
	if night.Kind != ActivityRelax || night.RelaxRoom != RelaxRoomPrivate {
		t.Fatalf("night relax = %+v, want private-room relaxation", night)
	}

	beforeWake := Decide(jst(2026, time.November, 14, 5, 30), ActorState{ID: "Mio"}, State{
		ScheduleDate: "2026-11-14", PlannedSleep: jst(2026, time.November, 14, 1, 0),
		PlannedWake: jst(2026, time.November, 14, 6, 0),
	}, Presence{OwnRoom: AvailabilityAvailable}, false)
	if beforeWake.Kind != ActivityRelax || beforeWake.RelaxRoom != RelaxRoomPrivate {
		t.Fatalf("pre-wake relax = %+v, want private-room relaxation", beforeWake)
	}

	morning := Decide(jst(2026, time.November, 14, 5, 30), ActorState{ID: "Mio", ActualWakeAt: jst(2026, time.November, 14, 5, 10)}, State{
		ScheduleDate: "2026-11-14", PlannedSleep: jst(2026, time.November, 14, 1, 0),
		PlannedWake: jst(2026, time.November, 14, 6, 0), LogicalSleepUntil: jst(2026, time.November, 14, 6, 0),
	}, Presence{SharedRoom: AvailabilityAvailable}, false)
	if morning.Kind != ActivityWake {
		t.Fatalf("actual pre-schedule wake = %+v, want wake completion before morning policy", morning)
	}

	sharedMorning := Decide(jst(2026, time.November, 14, 7, 0), ActorState{ID: "Mio", ActualWakeAt: jst(2026, time.November, 14, 5, 10)}, State{
		ScheduleDate: "2026-11-14", PlannedSleep: jst(2026, time.November, 14, 1, 0),
		PlannedWake: jst(2026, time.November, 14, 6, 0),
	}, Presence{SharedRoom: AvailabilityAvailable}, false)
	if sharedMorning.Kind != ActivityRelax || sharedMorning.RelaxRoom != RelaxRoomShared {
		t.Fatalf("post-wake relax = %+v, want shared-room morning relaxation", sharedMorning)
	}
}

func TestDecideBuildsPreviousDiaryBeforeMorningActivityOnlyWhenAwake(t *testing.T) {
	date := "2026-11-14"
	wakeAt := jst(2026, time.November, 14, 6, 0)
	state := State{
		ActivatedOn:   "2026-11-13",
		NextDiaryDate: "2026-11-13",
		ScheduleDate:  date,
		PlannedSleep:  jst(2026, time.November, 14, 1, 0),
		PlannedWake:   wakeAt,
	}
	awake := Decide(jst(2026, time.November, 14, 6, 15), ActorState{ID: "Mio", ActualWakeAt: wakeAt}, state,
		Presence{SharedRoom: AvailabilityAvailable, OwnRoom: AvailabilityAvailable, ChatParticipants: []string{"Shiro"}}, true)
	if awake.Kind != ActivityDiary || awake.Date != "2026-11-13" {
		t.Fatalf("awake plan = %+v, want prior-day diary first", awake)
	}
	state.LogicalSleepUntil = wakeAt.Add(time.Hour)
	asleep := Decide(jst(2026, time.November, 14, 6, 15), ActorState{ID: "Mio"}, state, Presence{}, true)
	if asleep.Kind != ActivitySleep {
		t.Fatalf("sleeping actor started diary: %+v", asleep)
	}
	state.NextDiaryDate = "2026-11-12"
	state.LogicalSleepUntil = time.Time{}
	beforeActivation := Decide(jst(2026, time.November, 14, 6, 15), ActorState{ID: "Mio", ActualWakeAt: wakeAt}, state, Presence{}, true)
	if beforeActivation.Kind == ActivityDiary {
		t.Fatalf("diary before activation was scheduled: %+v", beforeActivation)
	}
}

func TestDecideResumesPendingIntentWithoutConsumingChoice(t *testing.T) {
	state := State{ChoiceIndex: 2, Pending: &Intent{ID: "intent-1", ActorID: "Mio", Kind: ActivityMorningChat, ChoiceEligible: true, ChoiceCommitted: true}}
	plan := Decide(jst(2026, time.November, 14, 7, 0), ActorState{ID: "Mio"}, state, Presence{}, false)
	if !plan.Resuming || plan.CountChoice || plan.IntentID != "intent-1" || plan.Kind != ActivityMorningChat {
		t.Fatalf("resume plan = %+v", plan)
	}
}
