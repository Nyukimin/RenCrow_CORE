package main

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
)

func TestLivingSeatRoomRecognizesAllSharedSeats(t *testing.T) {
	for slot, wantRoom := range map[string]string{
		"sofa": "living", "sofa-guest": "living",
		"dining": "dining", "dining-guest": "dining",
		"dining-aisle-north": "dining", "dining-aisle-south": "dining",
		"dining-window-north": "dining", "dining-window-south": "dining",
	} {
		if got := livingSeatRoom(slot); got != wantRoom {
			t.Errorf("livingSeatRoom(%q) = %q, want %q", slot, got, wantRoom)
		}
	}
}

func TestLivingRestSeatCandidatesRotateAcrossEightSeats(t *testing.T) {
	want := map[string]string{
		"sofa": "living", "sofa-guest": "living",
		"dining": "dining", "dining-guest": "dining",
		"dining-aisle-north": "dining", "dining-aisle-south": "dining",
		"dining-window-north": "dining", "dining-window-south": "dining",
	}
	firstByActor := map[string]string{}
	for _, actorID := range []string{"mio", "kuro", "shiro", "aka"} {
		seats := livingRestSeatCandidates(worldread.Snapshot{}, actorID)
		if len(seats) != len(want) {
			t.Fatalf("%s candidates = %d, want all %d shared seats: %+v", actorID, len(seats), len(want), seats)
		}
		seen := make(map[string]bool, len(seats))
		for _, seat := range seats {
			if want[seat.slot] != seat.room || seen[seat.slot] {
				t.Fatalf("%s candidate is unknown or duplicated: %+v", actorID, seat)
			}
			seen[seat.slot] = true
		}
		firstByActor[actorID] = seats[0].slot
	}
	if firstByActor["mio"] == firstByActor["kuro"] {
		t.Fatalf("actor-specific ordering did not spread first choices: %+v", firstByActor)
	}
}

func TestLivingRestSeatCandidatesKeepOwnersCurrentSeatFirst(t *testing.T) {
	snapshot := worldread.Snapshot{Actors: []worldread.Actor{
		{ActorID: "mio", Present: true, RoomID: "dining", SlotID: "dining-window-south"},
		{ActorID: "other", Present: true, RoomID: "living", SlotID: "sofa"},
	}}
	seats := livingRestSeatCandidates(snapshot, "mio")
	if len(seats) == 0 || seats[0] != (livingSeatActivity{"dining", "dining-window-south", ""}) {
		t.Fatalf("owner should keep its occupied seat first: %+v", seats)
	}
}

func TestLivingRestSeatCandidatesExcludeOccupiedAndReservedSeats(t *testing.T) {
	snapshot := worldread.Snapshot{Actors: []worldread.Actor{
		{ActorID: "occupied", Present: true, RoomID: "living", SlotID: "sofa"},
		{ActorID: "moving", Present: true, RoomID: "living", SlotID: "dining-guest",
			ActivityID: "move-1", ActivityKind: "move", TargetRoomID: "dining", TargetSlotID: "dining-aisle-north"},
		{ActorID: "absent", Present: false, ActivityID: "move-2", ActivityKind: "move",
			TargetRoomID: "dining", TargetSlotID: "dining-aisle-south"},
		{ActorID: "target-only", Present: true, TargetSlotID: "dining-window-south"},
		{ActorID: "mio-world", Present: true, RoomID: "living", SlotID: "sofa-guest",
			ActivityID: "rest-move", ActivityKind: "move", TargetRoomID: "dining", TargetSlotID: "dining-window-north"},
	}}
	seats := livingRestSeatCandidates(snapshot, "mio-world")
	if len(seats) == 0 || seats[0] != (livingSeatActivity{"dining", "dining-window-north", ""}) {
		t.Fatalf("own in-flight target should be first for resume, got %+v", seats)
	}
	seen := make(map[string]bool, len(seats))
	for _, seat := range seats {
		seen[seat.slot] = true
	}
	for _, slot := range []string{"sofa", "sofa-guest", "dining-guest", "dining-aisle-north", "dining-window-south"} {
		if seen[slot] {
			t.Errorf("occupied, moving-source, or reserved seat %q was returned: %+v", slot, seats)
		}
	}
	if !seen["dining-aisle-south"] {
		t.Errorf("seat reserved only by an absent actor was excluded: %+v", seats)
	}
}

func TestLivingRestSeatCandidatesReturnUnavailableWhenAllSeatsAreOccupied(t *testing.T) {
	snapshot := worldread.Snapshot{Actors: []worldread.Actor{
		{ActorID: "one", Present: true, RoomID: "living", SlotID: "sofa"},
		{ActorID: "two", Present: true, RoomID: "living", SlotID: "sofa-guest"},
		{ActorID: "three", Present: true, RoomID: "dining", SlotID: "dining"},
		{ActorID: "four", Present: true, RoomID: "dining", SlotID: "dining-guest"},
		{ActorID: "five", Present: true, RoomID: "dining", SlotID: "dining-aisle-north"},
		{ActorID: "six", Present: true, RoomID: "dining", SlotID: "dining-aisle-south"},
		{ActorID: "seven", Present: true, RoomID: "dining", SlotID: "dining-window-north"},
		{ActorID: "eight", Present: true, RoomID: "dining", SlotID: "dining-window-south"},
	}}
	if seats := livingRestSeatCandidates(snapshot, "mio"); len(seats) != 0 {
		t.Fatalf("unavailable room returned candidates: %+v", seats)
	}
}

func TestLivingRestSeatCandidateRaceRespectsConcurrentMoveReservation(t *testing.T) {
	stale := worldread.Snapshot{}
	first := livingRestSeatCandidates(stale, "mio")
	secondFromStale := livingRestSeatCandidates(stale, "aka")
	if len(first) == 0 || len(secondFromStale) == 0 || first[0] != secondFromStale[0] {
		t.Fatalf("fixture must select the same initially free seat: first=%+v second=%+v", first, secondFromStale)
	}

	accepted := first[0]
	fresh := worldread.Snapshot{Actors: []worldread.Actor{{
		ActorID: "mio", Present: true, ActivityID: "move-accepted", ActivityKind: "move",
		TargetRoomID: accepted.room, TargetSlotID: accepted.slot,
	}}}
	retried := livingRestSeatCandidates(fresh, "aka")
	if len(retried) == 0 || retried[0].slot == accepted.slot {
		t.Fatalf("fresh in-flight reservation was reused after a concurrent acceptance: accepted=%+v retry=%+v", accepted, retried)
	}
}

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

func TestRestSeatReconciliationProtectsPendingMoveAndAllowsPriorityDeparture(t *testing.T) {
	restSeat := livingSeatActivity{room: "living", slot: "sofa"}
	prioritySeat := livingSeatActivity{room: "living", slot: "desk-mio"}

	pendingAtRestSeat := worldread.Actor{
		Present: true, ActivityKind: "move", ActivityID: "rest-move",
		TargetRoomID: restSeat.room, TargetSlotID: restSeat.slot,
	}
	if !shouldDeferRestSeatReconcile(pendingAtRestSeat, livingSeatActivity{}, restSeat, true) {
		t.Fatal("empty desired target evicted an in-flight pending rest move")
	}
	if !shouldDeferRestSeatReconcile(pendingAtRestSeat, prioritySeat, restSeat, true) {
		t.Fatal("reconciler raced a higher-priority move against the routine's in-flight move")
	}
	idleInSeat := worldread.Actor{Present: true, RoomID: restSeat.room, SlotID: restSeat.slot}
	if shouldDeferRestSeatReconcile(idleInSeat, prioritySeat, restSeat, true) {
		t.Fatal("a different higher-priority seat target was blocked after the rest move settled")
	}
	if !shouldDeferRestSeatReconcile(idleInSeat, restSeat, restSeat, true) {
		t.Fatal("matching confirmed rest target was not retained")
	}
	if shouldDeferRestSeatReconcile(idleInSeat, prioritySeat, restSeat, false) {
		t.Fatal("rest reconciliation hold applied without a pending rest request")
	}
}
