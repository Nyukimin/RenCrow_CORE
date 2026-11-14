package agentroutine

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"
)

const dateLayout = "2006-01-02"

var jstLocation = func() *time.Location {
	loc, err := time.LoadLocation(JST)
	if err == nil {
		return loc
	}
	return time.FixedZone(JST, 9*60*60)
}()

// ScheduleFor returns the stable logical sleep and wake schedule for an actor/date.
// The date denotes the calendar day containing both the 00:00-02:00 sleep window
// and the 05:00-08:00 wake window.
func ScheduleFor(actorID, date string) (time.Time, time.Time, error) {
	if strings.TrimSpace(actorID) == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("actor ID is required")
	}
	day, err := time.ParseInLocation(dateLayout, date, jstLocation)
	if err != nil || day.Format(dateLayout) != date {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid JST date %q", date)
	}
	seed := sha256.Sum256([]byte(actorID + "\n" + date))
	sleepMinute := binary.BigEndian.Uint32(seed[0:4]) % 120
	wakeMinute := binary.BigEndian.Uint32(seed[4:8]) % 180
	sleepAt := day.Add(time.Duration(sleepMinute) * time.Minute)
	wakeAt := day.Add(5*time.Hour + time.Duration(wakeMinute)*time.Minute)
	return sleepAt, wakeAt, nil
}

// Decide selects the next routine policy result without mutating state or asserting
// physical WORLD outcomes. Presence and ActualWakeAt are caller-supplied observations.
func Decide(now time.Time, actor ActorState, state State, presence Presence, canHobby bool) Plan {
	now = now.In(jstLocation)
	date := now.Format(dateLayout)
	scheduleDate := date
	if now.Hour() >= 22 {
		scheduleDate = now.AddDate(0, 0, 1).Format(dateLayout)
	}
	sleepAt, wakeAt := scheduledTimes(actor.ID, scheduleDate, state)
	plan := Plan{
		Kind:         ActivityWait,
		Date:         date,
		ScheduleDate: scheduleDate,
		PlannedSleep: sleepAt,
		PlannedWake:  wakeAt,
		HobbyIndex:   state.ChoiceIndex / 4,
	}
	if actor.ID == "" {
		plan.Reason = "actor ID is required"
		return plan
	}

	if state.Pending != nil {
		pending := state.Pending
		if pending.ActorID == actor.ID && pending.ID != "" {
			plan.Kind = pending.Kind
			plan.Date = pending.Date
			if pending.Kind == ActivityMorningChat {
				plan.Participants = cloneStrings(pending.Participants)
			}
			plan.Resuming = true
			plan.IntentID = pending.ID
			plan.LogicalSleep = pending.Kind == ActivitySleep
			plan.Reason = "resume persisted routine intent"
			return plan
		}
		plan.Reason = "pending intent belongs to another actor or has no identity"
		return plan
	}

	actualWake := confirmedWakeToday(actor.ActualWakeAt, date, now)
	logicalSleepActive := state.ScheduleDate == scheduleDate && state.LogicalSleepUntil.After(now) && actualWake.IsZero()
	if logicalSleepActive {
		plan.Kind = ActivitySleep
		plan.LogicalSleep = true
		plan.Reason = "logical sleep window remains active until planned wake"
		return plan
	}
	if state.ScheduleDate == scheduleDate && !state.LogicalSleepUntil.IsZero() &&
		!now.Before(state.LogicalSleepUntil) && state.LogicalSleepUntil.Equal(wakeAt) {
		plan.Kind = ActivityWake
		plan.LogicalSleep = false
		plan.Reason = "logical sleep window reached its planned end"
		return plan
	}
	if !actualWake.IsZero() && state.ScheduleDate == scheduleDate && state.LogicalSleepUntil.After(now) {
		plan.Kind = ActivityWake
		plan.LogicalSleep = false
		plan.Reason = "confirmed wake observation ended the logical sleep window"
		return plan
	}

	sleepEntryDue := scheduleDate == date && !now.Before(sleepAt) && now.Before(jstDateAt(date, 2, 0)) && actualWake.IsZero()
	if sleepEntryDue && presence.OwnRoom == AvailabilityAvailable {
		plan.Kind = ActivitySleep
		plan.LogicalSleep = true
		plan.Reason = "planned sleep start reached before 02:00 JST"
		return plan
	}

	if awakeForRoutine(logicalSleepActive) {
		if diaryDate, ok := nextDiary(state, date); ok {
			plan.Kind = ActivityDiary
			plan.Date = diaryDate
			plan.Reason = "create the oldest eligible prior-day diary before other activity"
			return plan
		}
	}

	if state.CooldownUntil.After(now) {
		plan.Reason = "routine cooldown is active"
		return plan
	}

	if now.Hour() >= 22 {
		if presence.OwnRoom != AvailabilityAvailable {
			plan.Reason = "night activity requires confirmed own-room availability"
			return plan
		}
		if canHobby {
			plan.Kind = ActivityHobby
			plan.Reason = "awake in the private night hobby window"
			return plan
		}
		plan.Kind = ActivityRelax
		plan.Reason = "no executable hobby is available in the confirmed own room"
		return plan
	}

	if sleepEntryDue {
		if presence.OwnRoom == AvailabilityAvailable {
			plan.Kind = ActivitySleep
			plan.LogicalSleep = true
			plan.Reason = "planned sleep start reached before 02:00 JST"
			return plan
		}
		plan.Reason = "sleep requires confirmed own-room availability"
		return plan
	}

	effectiveWake := wakeAt
	if !actualWake.IsZero() {
		effectiveWake = actualWake
	}
	if now.Before(effectiveWake) {
		if now.Hour() < 6 && isNightWindow(now) && canHobby && presence.OwnRoom == AvailabilityAvailable {
			plan.Kind = ActivityHobby
			plan.Reason = "awake before the allowed wake time during the private night window"
			return plan
		}
		if now.Hour() < 6 && isNightWindow(now) && !canHobby && presence.OwnRoom == AvailabilityAvailable {
			plan.Kind = ActivityRelax
			plan.Reason = "no executable hobby is available in the confirmed own room"
			return plan
		}
		plan.Reason = "waiting for actual or allowed wake time"
		return plan
	}

	if now.Hour() < 9 {
		return morningPlan(plan, actor, state, presence, canHobby)
	}

	plan.Kind = ActivityDaytime
	plan.Reason = "daytime activity is delegated to existing idle-time handling"
	return plan
}

func scheduledTimes(actorID, date string, state State) (time.Time, time.Time) {
	if state.ScheduleDate == date && validSchedule(date, state.PlannedSleep, state.PlannedWake) {
		return state.PlannedSleep.In(jstLocation), state.PlannedWake.In(jstLocation)
	}
	sleepAt, wakeAt, err := ScheduleFor(actorID, date)
	if err != nil {
		return time.Time{}, time.Time{}
	}
	return sleepAt, wakeAt
}

func validSchedule(date string, sleepAt, wakeAt time.Time) bool {
	if sleepAt.IsZero() || wakeAt.IsZero() {
		return false
	}
	sleepAt = sleepAt.In(jstLocation)
	wakeAt = wakeAt.In(jstLocation)
	return sleepAt.Format(dateLayout) == date && sleepAt.Hour() < 2 &&
		wakeAt.Format(dateLayout) == date && wakeAt.Hour() >= 5 && wakeAt.Hour() < 8
}

func confirmedWakeToday(at time.Time, date string, now time.Time) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	at = at.In(jstLocation)
	if at.Format(dateLayout) != date || at.After(now) {
		return time.Time{}
	}
	return at
}

func awakeForRoutine(sleeping bool) bool {
	return !sleeping
}

func nextDiary(state State, today string) (string, bool) {
	if state.ActivatedOn == "" || state.NextDiaryDate == "" {
		return "", false
	}
	if !validDate(state.ActivatedOn) || !validDate(state.NextDiaryDate) {
		return "", false
	}
	return state.NextDiaryDate, state.NextDiaryDate >= state.ActivatedOn && state.NextDiaryDate < today
}

func validDate(date string) bool {
	t, err := time.ParseInLocation(dateLayout, date, jstLocation)
	return err == nil && t.Format(dateLayout) == date
}

func morningPlan(plan Plan, actor ActorState, state State, presence Presence, canHobby bool) Plan {
	participants := uniqueSorted(presence.ChatParticipants)
	chatAvailable := presence.SharedRoom == AvailabilityAvailable && len(participants) > 0
	hobbyAvailable := presence.OwnRoom == AvailabilityAvailable && canHobby
	plan.CountChoice = chatAvailable && hobbyAvailable
	if plan.CountChoice {
		if state.ChoiceIndex%4 == 3 {
			plan.Kind = ActivityHobby
			plan.Reason = "both morning choices are valid; persisted 3:1 position selects hobby"
			return plan
		}
		plan.Kind = ActivityMorningChat
		plan.Participants = participants
		plan.Reason = "both morning choices are valid; persisted 3:1 position selects chat"
		return plan
	}
	if chatAvailable {
		plan.Kind = ActivityMorningChat
		plan.Participants = participants
		plan.Reason = "chat is the only valid morning choice; fallback does not consume 3:1 position"
		return plan
	}
	if hobbyAvailable {
		plan.Kind = ActivityHobby
		plan.Reason = "hobby is the only valid morning choice; fallback does not consume 3:1 position"
		return plan
	}
	if presence.SharedRoom == AvailabilityAvailable {
		plan.Kind = ActivityRelax
		plan.Reason = "neither optional morning choice is valid; relax in confirmed shared room"
		return plan
	}
	if presence.OwnRoom == AvailabilityAvailable {
		plan.Kind = ActivityRelax
		plan.Reason = "neither optional morning choice is valid; relax in confirmed own room"
		return plan
	}
	plan.Kind = ActivityWait
	plan.Reason = "morning activity requires a confirmed room"
	return plan
}

func isNightWindow(now time.Time) bool {
	return now.Hour() >= 22 || now.Hour() < 6
}

func jstDateAt(date string, hour, minute int) time.Time {
	day, err := time.ParseInLocation(dateLayout, date, jstLocation)
	if err != nil {
		return time.Time{}
	}
	return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}
