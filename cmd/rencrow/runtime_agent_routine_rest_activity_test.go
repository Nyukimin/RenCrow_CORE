package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type routineRestTestActor struct {
	room, slot, activityID, activityKind, targetRoom, targetSlot string
}

func installRoutineRestSnapshot(t *testing.T, runtime *agentRoutineRuntime, actors map[string]routineRestTestActor) {
	t.Helper()
	worldActors := make(map[string]any, len(actors))
	worldIDs := make([]string, 0, len(actors))
	runtime.worldActorIDs = make(map[string]string, len(actors))
	for coreID, actor := range actors {
		worldID := coreID + "-world"
		worldIDs = append(worldIDs, worldID)
		runtime.worldActorIDs[worldID] = coreID
		worldActors[worldID] = map[string]any{
			"present": true, "room_id": actor.room, "slot_id": actor.slot,
			"activity_id": actor.activityID, "activity_kind": actor.activityKind,
			"target_room_id": actor.targetRoom, "target_slot_id": actor.targetSlot,
		}
	}
	encoded, err := json.Marshal(map[string]any{
		"schema_version": 1, "world_id": "world", "timeline_id": "timeline", "timeline_epoch": "epoch",
		"state_seq": 20, "state_hash": strings.Repeat("0", 64),
		"state": map[string]any{"actors": worldActors}, "home_move_points": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(encoded) }))
	t.Cleanup(server.Close)
	client, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "reader-token", WorldID: "world", TimelineID: "timeline",
		TimelineEpoch: "epoch", ActorIDs: worldIDs, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.world = client
}

type routineRestTestWorker struct {
	calls    int
	outputs  []string
	errors   []error
	messages []string
	onCall   func(context.Context, conversation.TurnInput)
}

func (w *routineRestTestWorker) ExecutePrivateWork(ctx context.Context, input conversation.TurnInput, _ []string) (string, error) {
	w.calls++
	w.messages = append(w.messages, input.MessageText())
	if w.onCall != nil {
		w.onCall(ctx, input)
	}
	index := w.calls - 1
	if index < len(w.errors) && w.errors[index] != nil {
		return "", w.errors[index]
	}
	if len(w.outputs) == 0 {
		return "", errors.New("test worker has no configured output")
	}
	if index >= len(w.outputs) {
		index = len(w.outputs) - 1
	}
	return w.outputs[index], nil
}

type routineRestTestReader struct {
	chunk routineRestBookChunk
	err   error
	calls int
}

func (r *routineRestTestReader) FetchNext(context.Context, routineRestReadCursor) (routineRestBookChunk, error) {
	r.calls++
	if r.err != nil {
		return routineRestBookChunk{}, r.err
	}
	return r.chunk, nil
}

func restJSONMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	value := make(map[string]any)
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func restString(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func restObject(value map[string]any, key string) map[string]any {
	result, _ := value[key].(map[string]any)
	return result
}

func TestAgentRoutineDaytimeRelaxCreatesCanonicalTaskAndRun(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
	runtime.worldLife = &worldread.LifeClient{}
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{"mio": {room: "living", slot: "sofa"}})
	body := "実際に取得した本文。" + strings.Repeat("本文", 590)
	chunk := routineRestBookChunk{
		Title: "注文の多い料理店", Author: "宮沢賢治",
		SourceURL: "https://www.aozora.gr.jp/cards/000081/card43754.html",
		RightsURL: "https://www.aozora.gr.jp/guide/kijyunn.html",
		BodyHash:  strings.Repeat("a", 64), StartRune: 0, EndRune: len([]rune(body)), NextRune: len([]rune(body)),
		BodyRunes: 1500, Text: body,
	}
	reader := &routineRestTestReader{chunk: chunk}
	runtime.restReader = reader
	worker := &routineRestTestWorker{outputs: []string{"この文章の雰囲気についての感想。"}}
	worker.onCall = func(callCtx context.Context, _ conversation.TurnInput) {
		activity, err := runtime.CurrentRoutineActivity(callCtx, "mio")
		if err != nil {
			t.Errorf("read step projection: %v", err)
		} else if activity.OngoingActivity != "sofa_reading" {
			t.Errorf("reader step projection = %+v; want sofa_reading only after chunk presentation", activity)
		}
	}
	runtime.workers = map[string]privateTaskWorker{"mio": worker}

	work, found, err := runtime.idleSource(nil)(ctx, "mio")
	if err != nil || !found || !strings.HasSuffix(work.ID, ":relax") || work.Run == nil {
		t.Fatalf("daytime discovery = work=%+v found=%t err=%v; want a relaxation Work", work, found, err)
	}
	if err := work.Run(ctx, make(chan struct{})); err != nil {
		t.Fatalf("run discovered daytime relaxation: %v", err)
	}
	if reader.calls != 1 || worker.calls != 1 || !strings.Contains(worker.messages[0], body) {
		t.Fatalf("reader/provider calls = %d/%d; provider did not receive the fetched chunk", reader.calls, worker.calls)
	}
	state, err := runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending != nil || state.LastFinishedIntentID == "" || state.RestWindow == nil || state.RestWindow.RoomID != "living" {
		t.Fatalf("completed routine state = %+v err=%v; want finished activity linked to its confirmed room", state, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	if err != nil || len(facts) != 1 || facts[0].Kind != agentroutine.ActivityRelax || !facts[0].Confirmed {
		t.Fatalf("relaxation facts = %+v err=%v; want one confirmed activity fact", facts, err)
	}
	reference := restJSONMap(t, facts[0].Reference)
	read := restObject(reference, "read")
	seats := restObject(reference, "seats")
	seat, _ := seats["mio"].(map[string]any)
	if restString(reference, "mode") != routineRestModeReading || read == nil ||
		restString(read, "title") != chunk.Title || restString(read, "author") != chunk.Author ||
		restString(read, "source_url") != chunk.SourceURL || restString(read, "rights_url") != chunk.RightsURL ||
		restString(read, "body_hash") != chunk.BodyHash || restString(read, "reflection") != worker.outputs[0] ||
		restString(seat, "slot_id") != "sofa" || read["start_rune"] != float64(chunk.StartRune) ||
		read["end_rune"] != float64(chunk.EndRune) || read["body_runes"] != float64(chunk.BodyRunes) ||
		read["next_rune"] != float64(chunk.NextRune) {
		t.Fatalf("saved reading evidence = %+v; want source/range/reflection and exact sofa seat", reference)
	}
	if strings.Contains(facts[0].Reference, body) || strings.Contains(facts[0].Summary, body) {
		t.Fatal("the fetched book body was persisted into the activity record")
	}
	all, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio"})
	if err != nil || len(all) != 1 || all[0].Status != domaintask.StatusSucceeded || all[0].Title != routineTaskTitle(agentroutine.ActivityRelax) {
		t.Fatalf("canonical routine Tasks = %+v err=%v; want one succeeded relax Task", all, err)
	}
	runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: all[0].TaskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("canonical routine Runs = %+v err=%v; want one succeeded Run", runs, err)
	}
}

func TestAgentRoutineDaytimeSoloDiningRunsTeaReflection(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "dining")
	runtime.cfg.Heartbeat.Routine.SharedRooms = []string{"living", "dining"}
	runtime.worldLife = &worldread.LifeClient{}
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{"mio": {room: "dining", slot: "dining-window-north"}})
	reader := &routineRestTestReader{err: errRoutineRestBookUnavailable}
	runtime.restReader = reader
	worker := &routineRestTestWorker{outputs: []string{"窓辺でひと息つく時間についての短い記録。"}}
	worker.onCall = func(callCtx context.Context, _ conversation.TurnInput) {
		activity, err := runtime.CurrentRoutineActivity(callCtx, "mio")
		if err != nil {
			t.Errorf("read tea projection: %v", err)
		} else if activity.OngoingActivity != "dining_tea" {
			t.Errorf("tea step projection = %+v; want dining_tea after the structured tea start checkpoint", activity)
		}
	}
	runtime.workers = map[string]privateTaskWorker{"mio": worker}
	work, found, err := runtime.idleSource(nil)(ctx, "mio")
	if err != nil || !found {
		t.Fatalf("daytime dining discovery = %+v found=%t err=%v", work, found, err)
	}
	if err := work.Run(ctx, make(chan struct{})); err != nil {
		state, loadErr := runtime.store.Load(ctx, "mio")
		var progress routineProgress
		if loadErr == nil && state.Pending != nil {
			progress, loadErr = decodeRoutineProgress(state.Pending.Output)
		}
		t.Fatalf("run solo tea activity: %v; calls=%d pending=%+v progress=%+v loadErr=%v", err, worker.calls, state.Pending, progress, loadErr)
	}
	if reader.calls != 0 || worker.calls != 1 {
		t.Fatalf("solo dining should choose tea without a book read; reader/provider calls = %d/%d", reader.calls, worker.calls)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	if err != nil || len(facts) != 1 {
		t.Fatalf("tea activity facts = %+v err=%v", facts, err)
	}
	reference := restJSONMap(t, facts[0].Reference)
	seats := restObject(reference, "seats")
	seat, _ := seats["mio"].(map[string]any)
	if restString(reference, "mode") != routineRestModeTea || reference["tea_virtual"] != true ||
		restString(reference, "tea_reflection") != worker.outputs[0] ||
		restString(seat, "room_id") != "dining" || restString(seat, "slot_id") != "dining-window-north" {
		t.Fatalf("tea activity record = %+v; want tea event, actual reflection, and exact dining slot", reference)
	}
	if strings.Contains(facts[0].Summary, "飲んだ") || strings.Contains(facts[0].Summary, "飲用") {
		t.Fatalf("tea summary asserts a physical drinking action: %q", facts[0].Summary)
	}
}

func TestAgentRoutineSharedConversationCheckpointsTurnsAndResumes(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
	runtime.worldLife = &worldread.LifeClient{}
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{
		"mio":   {room: "living", slot: "sofa"},
		"shiro": {room: "living", slot: "sofa-guest"},
	})
	mio := &routineRestTestWorker{outputs: []string{"窓から見える雲の話をしました。"}}
	shiro := &routineRestTestWorker{outputs: []string{"少し形が変わって見えるね。"}, errors: []error{errors.New("temporary provider failure"), nil}}
	runtime.workers = map[string]privateTaskWorker{"mio": mio, "shiro": shiro}
	for _, worker := range []*routineRestTestWorker{mio, shiro} {
		worker.onCall = func(callCtx context.Context, _ conversation.TurnInput) {
			activity, err := runtime.CurrentRoutineActivity(callCtx, "mio")
			if err != nil {
				t.Errorf("read conversation projection: %v", err)
			} else if activity.OngoingActivity != "sofa_chat" {
				t.Errorf("conversation projection = %+v; want sofa_chat after seats and conversation start", activity)
			}
		}
	}
	work, found, err := runtime.idleSource(nil)(ctx, "mio")
	if err != nil || !found || len(work.Participants) != 1 || work.Participants[0] != "shiro" {
		t.Fatalf("daytime conversation discovery = %+v found=%t err=%v; want one eligible companion", work, found, err)
	}
	if err := work.Run(ctx, make(chan struct{})); err == nil {
		t.Fatal("first run must preserve provider failure and remain resumable")
	}
	state, err := runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending == nil {
		t.Fatalf("failed conversation state = %+v err=%v; want retained pending checkpoint", state, err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil || progress.EffectCompleted || progress.Rest == nil || progress.Rest.Conversation == nil ||
		!progress.Rest.Conversation.CompletedAt.IsZero() || progress.Actors[0].Output != mio.outputs[0] ||
		progress.Actors[1].Output != "" || progress.Rest.Conversation.Turns != 1 {
		t.Fatalf("conversation checkpoint after partial failure = %+v err=%v", progress, err)
	}
	clock = state.Pending.RetryAfter.Add(time.Second)
	if err := runtime.run(ctx, make(chan struct{}), "mio", agentroutine.Plan{Kind: agentroutine.ActivityRelax}); err != nil {
		t.Fatalf("resume conversation from checkpoint: %v", err)
	}
	if mio.calls != 1 || shiro.calls != 2 || len(shiro.messages) < 2 || !strings.Contains(shiro.messages[1], mio.outputs[0]) {
		t.Fatalf("turn resume calls=%d/%d; host turn must not repeat and peer must see saved text", mio.calls, shiro.calls)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	if err != nil || len(facts) != 1 {
		t.Fatalf("conversation facts = %+v err=%v; want one host record", facts, err)
	}
	reference := restJSONMap(t, facts[0].Reference)
	transcript, _ := reference["conversation"].([]any)
	seats := restObject(reference, "seats")
	hostSeat, _ := seats["mio"].(map[string]any)
	peerSeat, _ := seats["shiro"].(map[string]any)
	first, _ := transcript[0].(map[string]any)
	second, _ := transcript[1].(map[string]any)
	if restString(reference, "mode") != routineRestModeConversation || len(transcript) != 2 ||
		restString(first, "actor_id") != "mio" || restString(first, "text") != mio.outputs[0] ||
		restString(second, "actor_id") != "shiro" || restString(second, "text") != shiro.outputs[0] ||
		restString(hostSeat, "slot_id") != "sofa" || restString(peerSeat, "slot_id") != "sofa-guest" {
		t.Fatalf("saved transcript/seats = %+v / %+v", transcript, seats)
	}
}

func TestAgentRoutineRestCompanionLeavingBeforeConversationFallsBackToSoloReading(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
	runtime.cfg.Heartbeat.Routine.SharedRooms = []string{"living"}
	runtime.worldLife = &worldread.LifeClient{}
	body := strings.Repeat("book passage. ", 80)
	bodyRunes := len([]rune(body))
	reader := &routineRestTestReader{chunk: routineRestBookChunk{
		Title: "Test Book", Author: "Test Author", SourceURL: "https://example.test/book",
		RightsURL: "https://example.test/rights", BodyHash: strings.Repeat("d", 64),
		StartRune: 0, EndRune: bodyRunes, NextRune: bodyRunes, BodyRunes: bodyRunes + 20, Text: body,
	}}
	runtime.restReader = reader
	mio := &routineRestTestWorker{outputs: []string{"A short reflection on the passage."}}
	shiro := &routineRestTestWorker{outputs: []string{"A conversational reply that should not be requested."}}
	runtime.workers = map[string]privateTaskWorker{"mio": mio, "shiro": shiro}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		peerTasks, err := tasks.List(context.Background(), domaintask.Filter{Assignee: "shiro"})
		if err != nil {
			http.Error(w, "task read failed", http.StatusInternalServerError)
			return
		}
		peerRoom := "living"
		peerSlot := "sofa-guest"
		peerActivityID, peerActivityKind := "", ""
		if len(peerTasks) > 0 {
			peerRoom = "bedroom"
			peerSlot = "bed-bedroom"
			peerActivityID, peerActivityKind = "higher-priority-work", "work"
		}
		payload := map[string]any{
			"schema_version": 1, "world_id": "world", "timeline_id": "timeline", "timeline_epoch": "epoch",
			"state_seq": 200, "state_hash": strings.Repeat("0", 64),
			"state": map[string]any{"actors": map[string]any{
				"mio-world": map[string]any{"present": true, "room_id": "living", "slot_id": "sofa"},
				"shiro-world": map[string]any{
					"present": true, "room_id": peerRoom, "slot_id": peerSlot,
					"activity_id": peerActivityID, "activity_kind": peerActivityKind,
				},
			}},
			"home_move_points": map[string]any{},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			http.Error(w, "encode snapshot", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	world, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "reader-token", WorldID: "world", TimelineID: "timeline",
		TimelineEpoch: "epoch", ActorIDs: []string{"mio-world", "shiro-world"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.world = world
	runtime.worldActorIDs = map[string]string{"mio-world": "mio", "shiro-world": "shiro"}

	work, found, err := runtime.idleSource(nil)(ctx, "mio")
	if err != nil || !found || len(work.Participants) != 1 || work.Participants[0] != "shiro" {
		t.Fatalf("rest discovery = %+v found=%t err=%v; want conversation prepared with shiro", work, found, err)
	}
	if err := work.Run(ctx, make(chan struct{})); err != nil {
		t.Fatalf("conversation peer departure should cancel the unstarted peer Run and continue as solo reading: %v", err)
	}
	if mio.calls != 1 || shiro.calls != 0 || reader.calls != 1 {
		t.Fatalf("actual activity calls = mio:%d shiro:%d reader:%d; want host-only reading", mio.calls, shiro.calls, reader.calls)
	}
	state, err := runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending != nil || state.RestWindow == nil {
		t.Fatalf("host routine state = %+v err=%v; want completed solo rest activity", state, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	if err != nil || len(facts) != 1 {
		t.Fatalf("host rest facts = %+v err=%v; want one activity record", facts, err)
	}
	reference := restJSONMap(t, facts[0].Reference)
	if restString(reference, "mode") != routineRestModeReading || reference["conversation"] != nil {
		t.Fatalf("host fact should record reading without invented conversation: %+v", reference)
	}
	hostTasks, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio"})
	if err != nil || len(hostTasks) != 1 || hostTasks[0].Status != domaintask.StatusSucceeded {
		t.Fatalf("host canonical Task = %+v err=%v; want succeeded", hostTasks, err)
	}
	peerTasks, err := tasks.List(ctx, domaintask.Filter{Assignee: "shiro"})
	if err != nil || len(peerTasks) != 1 || peerTasks[0].Status != domaintask.StatusCancelled {
		t.Fatalf("peer canonical Task = %+v err=%v; want cancelled before conversation", peerTasks, err)
	}
	peerRuns, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: peerTasks[0].TaskID})
	if err != nil || len(peerRuns) != 1 || peerRuns[0].Status != domaintask.RunStatusCancelled {
		t.Fatalf("peer canonical Run = %+v err=%v; want cancelled before conversation", peerRuns, err)
	}
}

func TestRetryingDaytimeRestYieldsToNightSleepAndClosesWithoutRestFact(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
	state, err := runtime.draftIntent(ctx, "mio", agentroutine.Plan{
		Kind: agentroutine.ActivityRelax, RelaxRoom: agentroutine.RelaxRoomShared, Date: clock.Format("2006-01-02"),
	}, clock)
	if err != nil || state.Pending == nil {
		t.Fatalf("draft daytime rest = %+v err=%v", state, err)
	}
	restID := state.Pending.ID
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		t.Fatal(err)
	}
	progress.Rest = &routineRestProgress{Scope: agentroutine.RelaxRoomShared, FailureReason: "seat_move_rejected"}
	state, _, err = runtime.saveProgress(ctx, state, "rest_activity_deferred", progress)
	if err != nil {
		t.Fatal(err)
	}
	state.Pending.RetryAfter = clock.Add(2 * time.Hour)
	state, err = runtime.store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}

	sleepAt, _, scheduleErr := agentroutine.ScheduleFor("mio", "2026-11-16")
	if scheduleErr != nil {
		t.Fatal(scheduleErr)
	}
	clock = sleepAt.Add(5 * time.Minute)
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending == nil {
		t.Fatalf("load pending rest before night handoff: %+v err=%v", state, err)
	}
	state.Pending.RetryAfter = clock.Add(time.Hour)
	state, err = runtime.store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{"mio": {room: "mio-room"}})
	work, found, err := runtime.idleSource(nil)(ctx, "mio")
	if err != nil || !found || !strings.HasSuffix(work.ID, ":sleep") {
		t.Fatalf("night routine discovery with retrying daytime rest = %+v found=%t err=%v; want sleep work", work, found, err)
	}
	if err := work.Run(ctx, make(chan struct{})); err != nil {
		t.Fatalf("night sleep must proceed while the daytime rest retry is pending: %v", err)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending != nil || state.LastFinishedIntentID == restID {
		t.Fatalf("after sleep priority handling = %+v err=%v; want rest closed and sleep completed", state, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range facts {
		if fact.Kind == agentroutine.ActivityRelax {
			t.Fatalf("night handoff wrote an unverified rest success fact: %+v", fact)
		}
	}
	all, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio"})
	if err != nil || len(all) != 1 || all[0].Title != routineTaskTitle(agentroutine.ActivitySleep) ||
		all[0].Status != domaintask.StatusSucceeded {
		t.Fatalf("night canonical Tasks = %+v err=%v; want completed Sleep Task", all, err)
	}
}

func TestAgentRoutinePrivateNightRestRequiresAndRecordsRealReflection(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 23, 30, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "mio-room")
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{"mio": {room: "mio-room"}})
	state, _, err := runtime.ensureState(ctx, "mio")
	if err != nil {
		t.Fatal(err)
	}
	state.ActivatedOn = "2026-11-16"
	state.NextDiaryDate = "2026-11-17"
	if _, err := runtime.store.Save(ctx, state, state.Revision); err != nil {
		t.Fatal(err)
	}
	plan, _, err := runtime.planFresh(ctx, "mio", false, nil)
	if err != nil || plan.Kind != agentroutine.ActivityRelax || plan.RelaxRoom != agentroutine.RelaxRoomPrivate {
		t.Fatalf("night private fallback plan = %+v err=%v", plan, err)
	}
	if err := runtime.run(ctx, make(chan struct{}), "mio", plan); err == nil {
		t.Fatal("private rest without an executable actor worker must not complete")
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending == nil {
		t.Fatalf("unavailable private rest state = %+v err=%v; want retained checkpoint", state, err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil || progress.EffectCompleted || progress.Rest == nil || progress.Rest.PrivateReflection != "" {
		t.Fatalf("unavailable private rest checkpoint = %+v err=%v; want incomplete without invented text", progress, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", "2026-11-15")
	if err != nil || len(facts) != 0 {
		t.Fatalf("unavailable private rest facts = %+v err=%v; want no success fact", facts, err)
	}

	worker := &routineRestTestWorker{outputs: []string{"静かな時間に気持ちを整えられました。"}}
	runtime.workers = map[string]privateTaskWorker{"mio": worker}
	clock = state.Pending.RetryAfter.Add(time.Second)
	if err := runtime.run(ctx, make(chan struct{}), "mio", plan); err != nil {
		t.Fatalf("resume private reflection: %v", err)
	}
	if worker.calls != 1 {
		t.Fatalf("private reflection worker calls = %d, want one", worker.calls)
	}
	facts, err = runtime.store.ListFacts(ctx, "mio", "2026-11-15")
	if err != nil || len(facts) != 1 || !facts[0].Confirmed || facts[0].Kind != agentroutine.ActivityRelax {
		t.Fatalf("private reflection facts = %+v err=%v; want one confirmed completed activity", facts, err)
	}
	reference := restJSONMap(t, facts[0].Reference)
	if restString(reference, "scope") != "private" || restString(reference, "room_id") != "mio-room" ||
		restString(reference, "reflection") != worker.outputs[0] || strings.Contains(facts[0].Summary, "休憩完了") {
		t.Fatalf("private reflection record = %+v summary=%q", reference, facts[0].Summary)
	}
}

func TestAgentRoutineRestStopPausesRunAndResumesTheSameSeat(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "dining")
	runtime.cfg.Heartbeat.Routine.SharedRooms = []string{"living", "dining"}
	runtime.worldLife = &worldread.LifeClient{}
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{"mio": {room: "dining", slot: "dining-window-south"}})
	worker := &routineRestTestWorker{
		outputs: []string{"休憩中に考えた短い記録。"},
		errors:  []error{context.Canceled, nil},
	}
	entered := make(chan struct{})
	worker.onCall = func(callCtx context.Context, _ conversation.TurnInput) {
		close(entered)
		<-callCtx.Done()
	}
	runtime.workers = map[string]privateTaskWorker{"mio": worker}
	work, found, err := runtime.idleSource(nil)(ctx, "mio")
	if err != nil || !found {
		t.Fatalf("daytime tea discovery = %+v found=%t err=%v", work, found, err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- work.Run(ctx, stop) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("tea worker did not start after the seat and step checkpoint")
	}
	activity, err := runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || activity.OngoingActivity != "dining_tea" {
		t.Fatalf("active tea projection = %+v err=%v; want visible only during running provider step", activity, err)
	}
	pendingSeat, pending := runtime.pendingRoutineSharedRestSeat("mio")
	activeSeat, active := runtime.activeRoutineRestSeat("mio")
	if !pending || pendingSeat.slot != "dining-window-south" || !active || activeSeat.slot != pendingSeat.slot {
		t.Fatalf("seat checkpoints before interrupt pending=%+v/%t active=%+v/%t", pendingSeat, pending, activeSeat, active)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("stop rest step: %v", err)
	}
	state, err := runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending == nil {
		t.Fatalf("interrupted rest state = %+v err=%v; want durable pending checkpoint", state, err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil || progress.EffectCompleted || progress.Rest == nil ||
		progress.Rest.TeaStartedAt.IsZero() || progress.Rest.TeaReflection != "" ||
		progress.Rest.Seats["mio"].SlotID != "dining-window-south" {
		t.Fatalf("interrupted rest progress = %+v err=%v; want started step, held seat, and no invented result", progress, err)
	}
	teaStartedAt := progress.Rest.TeaStartedAt
	current, err := runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || current.OngoingActivity != "waiting" {
		t.Fatalf("paused rest projection = %+v err=%v; waiting Runs must not remain displayed as active tea", current, err)
	}
	all, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio"})
	if err != nil || len(all) != 1 {
		t.Fatalf("interrupted canonical Tasks = %+v err=%v", all, err)
	}
	runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: all[0].TaskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("interrupted canonical Runs = %+v err=%v; want waiting", runs, err)
	}

	worker.onCall = nil
	failedSnapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary WORLD snapshot failure", http.StatusServiceUnavailable)
	}))
	defer failedSnapshotServer.Close()
	failedWorld, err := worldread.New(worldread.Config{
		BaseURL: failedSnapshotServer.URL, Credential: "reader-token", WorldID: "world",
		TimelineID: "timeline", TimelineEpoch: "epoch", ActorIDs: []string{"mio-world"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.world = failedWorld
	clock = clock.Add(time.Second)
	if err := runtime.run(ctx, make(chan struct{}), "mio", agentroutine.Plan{Kind: agentroutine.ActivityRelax}); err == nil {
		t.Fatal("a WORLD snapshot failure should defer the pending rest activity")
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending == nil || state.Pending.ID == "" ||
		!state.Pending.RetryAfter.After(clock) {
		t.Fatalf("snapshot-failed rest state = %+v err=%v; want same pending intent with bounded retry", state, err)
	}
	progress, err = decodeRoutineProgress(state.Pending.Output)
	if err != nil || progress.EffectCompleted || progress.Rest == nil ||
		!progress.Rest.TeaStartedAt.Equal(teaStartedAt) || progress.Rest.TeaReflection != "" ||
		progress.Rest.Seats["mio"].SlotID != "dining-window-south" {
		t.Fatalf("snapshot-failed rest checkpoint = %+v err=%v; want same activity and seat", progress, err)
	}
	interruptedIntentID := state.Pending.ID
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{"mio": {room: "dining", slot: "dining-window-south"}})
	clock = state.Pending.RetryAfter.Add(time.Second)
	if err := runtime.run(ctx, make(chan struct{}), "mio", agentroutine.Plan{Kind: agentroutine.ActivityRelax}); err != nil {
		t.Fatalf("resume interrupted rest Task/Run after WORLD recovery: %v", err)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending != nil || state.LastFinishedIntentID != interruptedIntentID {
		t.Fatalf("recovered rest state = %+v err=%v; want same intent completed", state, err)
	}
	if worker.calls != 2 {
		t.Fatalf("resumed worker calls = %d; want one interrupted attempt and one resumed attempt", worker.calls)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	if err != nil || len(facts) != 1 || !facts[0].Confirmed {
		t.Fatalf("resumed rest facts = %+v err=%v; want one completed record", facts, err)
	}
}

func TestLegacyRoomOnlyRestCooldownDoesNotBlockDaytimeActivityDiscovery(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
	runtime.worldLife = &worldread.LifeClient{}
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{"mio": {room: "living", slot: "sofa"}})
	body := strings.Repeat("real book text. ", 100)
	bodyRunes := len([]rune(body))
	runtime.restReader = &routineRestTestReader{chunk: routineRestBookChunk{
		Title: "Test Book", Author: "Test Author", SourceURL: "https://example.test/book",
		RightsURL: "https://example.test/rights", BodyHash: strings.Repeat("b", 64),
		BodyRunes: bodyRunes + 10, StartRune: 0, EndRune: bodyRunes, NextRune: bodyRunes, Text: body,
	}}
	runtime.workers = map[string]privateTaskWorker{"mio": &routineRestTestWorker{outputs: []string{"a reflection on the read passage"}}}
	state, _, err := runtime.ensureState(ctx, "mio")
	if err != nil {
		t.Fatal(err)
	}
	observed := clock.Add(-time.Minute)
	until := clock.Add(44 * time.Minute)
	state.LastFinishedIntentID = "legacy-room-only-rest"
	state.CooldownUntil = until
	state.RestWindow = &agentroutine.RestWindow{ObservedAt: observed, Until: until, RoomID: "living"}
	saved, err := runtime.store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.store.SaveFact(ctx, agentroutine.Activity{
		ActorID: "mio", Date: clock.Format("2006-01-02"), Key: "legacy-room-only-rest:relax",
		Kind: agentroutine.ActivityRelax, At: observed, Source: agentRoutineModuleID,
		SourceID: "legacy-room-only-rest", Confirmed: true, Summary: "legacy room observation",
	})
	if err != nil {
		t.Fatal(err)
	}

	work, found, err := runtime.idleSource(nil)(ctx, "mio")
	if err != nil || !found || !strings.HasSuffix(work.ID, ":relax") {
		t.Fatalf("legacy cooldown discovery = %+v found=%t err=%v; expected eligible daytime rest", work, found, err)
	}
	persisted, err := runtime.store.Load(ctx, "mio")
	if err != nil || !persisted.CooldownUntil.Equal(until) || persisted.RestWindow == nil || persisted.Revision != saved.Revision {
		t.Fatalf("discovery mutated durable legacy state: %+v err=%v; scheduler projection must stay read-only", persisted, err)
	}

	if err := work.Run(ctx, make(chan struct{})); err != nil {
		t.Fatalf("legacy room-only Work.Run: %v; expected actual rest execution", err)
	}
	tasksCreated, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio"})
	if err != nil || len(tasksCreated) != 1 || tasksCreated[0].Status != domaintask.StatusSucceeded {
		t.Fatalf("legacy cooldown canonical Tasks = %+v err=%v; want one completed activity Task", tasksCreated, err)
	}
	runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: tasksCreated[0].TaskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("legacy cooldown canonical Runs = %+v err=%v; want one completed activity Run", runs, err)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending != nil || state.LastFinishedIntentID == "legacy-room-only-rest" ||
		state.RestWindow == nil || !state.CooldownUntil.After(clock) {
		t.Fatalf("legacy cooldown activity result = %+v err=%v; want new completed rest checkpoint", state, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	foundActivity := false
	for _, fact := range facts {
		if fact.SourceID == state.LastFinishedIntentID && fact.Confirmed && strings.Contains(fact.Summary, "Test Book") {
			foundActivity = true
		}
	}
	if err != nil || !foundActivity {
		t.Fatalf("legacy cooldown activity facts = %+v err=%v; want a new confirmed reading fact", facts, err)
	}
}

func TestLegacyRoomOnlyCompletedPendingIntentIsClosedWithoutSuccessFact(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
	state, err := runtime.draftIntent(ctx, "mio", agentroutine.Plan{
		Kind: agentroutine.ActivityRelax, RelaxRoom: agentroutine.RelaxRoomShared, Date: clock.Format("2006-01-02"),
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		t.Fatal(err)
	}
	progress.EffectCompleted = true
	progress.CompletedAt = clock
	progress.World = &routineWorldProgress{RelaxRoomID: "living", RelaxObservedAt: clock.Add(-time.Minute)}
	state, progress, err = runtime.saveProgress(ctx, state, "legacy_room_only_completed", progress)
	if err != nil {
		t.Fatal(err)
	}
	prepared, state, progress, err := runtime.prepareRuns(ctx, nil, state, progress)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.completeRuns(ctx, prepared, "legacy room-only completion"); err != nil {
		t.Fatal(err)
	}
	intentID := state.Pending.ID
	if err := runtime.run(ctx, make(chan struct{}), "mio", agentroutine.Plan{Kind: agentroutine.ActivityRelax}); err != nil {
		t.Fatalf("close unverified legacy pending intent: %v", err)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending != nil || state.LastFinishedIntentID != intentID ||
		!state.CooldownUntil.IsZero() || state.RestWindow != nil {
		t.Fatalf("migrated legacy intent = %+v err=%v; want closed without cooldown/rest window", state, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	if err != nil || len(facts) != 0 {
		t.Fatalf("legacy pending intent wrote success facts: %+v err=%v", facts, err)
	}
	all, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio"})
	if err != nil || len(all) != 1 || all[0].Status != domaintask.StatusSucceeded {
		t.Fatalf("preexisting terminal Task history was rewritten: %+v err=%v", all, err)
	}
}

func TestRestSeatMoveRejectedReceiptsUseNewOperationIDsAndStopAfterBound(t *testing.T) {
	seat := routineRestSeatCheckpoint{RoomID: "living", SlotID: "sofa"}
	actorID := "mio"
	progress := routineProgress{}
	base := routineRestSeatPurposePrefix + actorID + "-" + seat.SlotID
	if purpose, err := routineRestSeatMovePurpose(progress, actorID, seat); err != nil || purpose != base {
		t.Fatalf("first move purpose = %q err=%v; want %q", purpose, err, base)
	}
	if progress.World == nil {
		progress.World = &routineWorldProgress{}
	}
	purposes := []string{base + ":move", base + ":retry:1:move", base + ":retry:2:move"}
	for index, purpose := range purposes {
		progress.World.Operations = append(progress.World.Operations, routineWorldOperation{
			Purpose: purpose, Action: worldread.HomeActionMove, ActorID: actorID,
			Payload:   worldread.HomeActionPayload{ActorID: "mio-world", RoomID: seat.RoomID, SlotID: seat.SlotID},
			ReceiptID: "receipt-" + strconv.Itoa(index+1), ReceiptStatus: "rejected",
			ReceiptReason: "handler_error", StateSeq: 20,
		})
		next, err := routineRestSeatMovePurpose(progress, actorID, seat)
		if index+1 == routineRestSeatMaxMoveAttempts {
			if !errors.Is(err, errRoutineRestSeatMoveExhausted) {
				t.Fatalf("after %d rejected moves, next purpose = %q err=%v; want terminal exhaustion", index+1, next, err)
			}
			continue
		}
		want := base + ":retry:" + strconv.Itoa(index+1)
		if err != nil || next != want {
			t.Fatalf("after %d rejected moves, next purpose = %q err=%v; want fresh purpose %q", index+1, next, err, want)
		}
	}
}

func TestRejectedRestSeatMoveLimitCancelsWithoutSuccessFact(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
	runtime.worldLife = &worldread.LifeClient{}
	installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{"mio": {room: "living", slot: "living-entry"}})
	state, err := runtime.draftIntent(ctx, "mio", agentroutine.Plan{
		Kind: agentroutine.ActivityRelax, RelaxRoom: agentroutine.RelaxRoomShared, Date: clock.Format("2006-01-02"),
	}, clock)
	if err != nil || state.Pending == nil {
		t.Fatalf("draft pending rest = %+v err=%v", state, err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		t.Fatal(err)
	}
	seat := routineRestSeatCheckpoint{RoomID: "living", SlotID: "sofa", ObservedStateSeq: 10}
	progress.Rest = &routineRestProgress{
		Scope: agentroutine.RelaxRoomShared, Mode: routineRestModeReading,
		Seats: map[string]routineRestSeatCheckpoint{"mio": seat},
	}
	base := routineRestSeatPurposePrefix + "mio-sofa"
	progress.World = &routineWorldProgress{}
	for index, purpose := range []string{base + ":move", base + ":retry:1:move", base + ":retry:2:move"} {
		progress.World.Operations = append(progress.World.Operations, routineWorldOperation{
			Purpose: purpose, Action: worldread.HomeActionMove, ActorID: "mio",
			Payload:   worldread.HomeActionPayload{ActorID: "mio-world", RoomID: "living", SlotID: "sofa"},
			ReceiptID: "receipt-" + strconv.Itoa(index+1), ReceiptStatus: "rejected",
			ReceiptReason: "handler_error", StateSeq: 10,
		})
	}
	state, _, err = runtime.saveProgress(ctx, state, "rest_seats_requested", progress)
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.run(ctx, make(chan struct{}), "mio", agentroutine.Plan{Kind: agentroutine.ActivityRelax})
	if !errors.Is(err, errRoutineRestSeatMoveExhausted) {
		t.Fatalf("run after bounded rejected moves = %v; want terminal seat failure", err)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending != nil || state.RestWindow != nil {
		t.Fatalf("exhausted seat activity state = %+v err=%v; want closed without successful RestWindow", state, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
	if err != nil || len(facts) != 0 {
		t.Fatalf("exhausted seat activity facts = %+v err=%v; want no invented completion fact", facts, err)
	}
	created, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio"})
	if err != nil || len(created) != 0 {
		t.Fatalf("seat failure before Task/Run start created canonical Tasks: %+v err=%v", created, err)
	}
}

func TestRejectedRestMoveRetriesOnlyWhenFreshSnapshotConfirmsSeatConflict(t *testing.T) {
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
	seat := routineRestSeatCheckpoint{RoomID: "living", SlotID: "sofa"}
	for _, test := range []struct {
		name      string
		rejectSeq uint64
		reason    string
		occupant  routineRestTestActor
		wantRetry bool
	}{
		{name: "current occupant", rejectSeq: 11, reason: "handler_error", occupant: routineRestTestActor{room: "living", slot: "sofa"}, wantRetry: true},
		{name: "inflight reservation", rejectSeq: 12, reason: "resource_conflict", occupant: routineRestTestActor{targetRoom: "living", targetSlot: "sofa", activityID: "move-2", activityKind: "move"}, wantRetry: true},
		{name: "stale snapshot", rejectSeq: 21, reason: "resource_conflict", occupant: routineRestTestActor{room: "living", slot: "sofa"}},
		{name: "no conflicting occupant", rejectSeq: 12, reason: "handler_error", occupant: routineRestTestActor{room: "living", slot: "sofa-guest"}},
		{name: "unrelated receipt reason", rejectSeq: 12, reason: "unavailable", occupant: routineRestTestActor{room: "living", slot: "sofa"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			installRoutineRestSnapshot(t, runtime, map[string]routineRestTestActor{
				"mio": {room: "living", slot: "living-entry"}, "shiro": test.occupant,
			})
			progress := routineProgress{World: &routineWorldProgress{Operations: []routineWorldOperation{{
				Purpose: "rest-seat-mio-sofa:move", Action: worldread.HomeActionMove, ActorID: "mio",
				Payload:   worldread.HomeActionPayload{ActorID: "mio-world", RoomID: "living", SlotID: "sofa"},
				ReceiptID: "receipt-1", ReceiptStatus: "rejected", ReceiptReason: test.reason, StateSeq: test.rejectSeq,
			}}}}
			got := runtime.routineRestSeatRejectedOccupied(context.Background(), progress, "mio", seat)
			if got != test.wantRetry {
				t.Fatalf("rejection classification = %t, want retry=%t", got, test.wantRetry)
			}
		})
	}
}

func TestPendingRestWorldSnapshotFailsClosedForMissingActorStaleSequenceAndSceneChange(t *testing.T) {
	scene := strings.Repeat("a", 64)
	progress := routineProgress{
		Actors: []routineActorProgress{{ActorID: "mio", TaskID: modulecore.NewTaskID()}},
		Rest: &routineRestProgress{
			Scope: agentroutine.RelaxRoomShared,
			Seats: map[string]routineRestSeatCheckpoint{
				"mio": {RoomID: "living", SlotID: "sofa", MoveActivityID: "seat-move-1", ObservedStateSeq: 10},
			},
		},
		World: &routineWorldProgress{Operations: []routineWorldOperation{{
			Purpose: "rest-seat-mio-sofa:move", Action: worldread.HomeActionMove, ActorID: "mio",
			ReceiptStatus: "committed", EventVerified: true, ActivityID: "seat-move-1",
			ActivityKind: "move", SceneSHA256: scene,
		}}},
	}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	intent := &agentroutine.Intent{ID: "rest-scene-1", ActorID: "mio", Kind: agentroutine.ActivityRelax, Output: string(encoded)}
	runtime := &agentRoutineRuntime{worldActorIDs: map[string]string{"mio-world": "mio"}}
	base := worldread.Snapshot{
		SchemaVersion: 1, WorldID: "world", StateHash: strings.Repeat("b", 64), StateSeq: 20,
		HomeSceneSHA256: scene,
		Actors:          []worldread.Actor{{ActorID: "mio-world", Present: true, RoomID: "living", SlotID: "sofa", HomeSceneSHA256: scene}},
	}
	if err := runtime.validatePendingRestSnapshot(base, intent); err != nil {
		t.Fatalf("matching fresh snapshot rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*worldread.Snapshot, *routineProgress)
	}{
		{
			name: "missing actor",
			mutate: func(snapshot *worldread.Snapshot, _ *routineProgress) {
				snapshot.Actors = nil
			},
		},
		{
			name: "stale state sequence",
			mutate: func(snapshot *worldread.Snapshot, progress *routineProgress) {
				snapshot.StateSeq = 9
				seat := progress.Rest.Seats["mio"]
				seat.ObservedStateSeq = 10
				progress.Rest.Seats["mio"] = seat
			},
		},
		{
			name: "scene changed",
			mutate: func(snapshot *worldread.Snapshot, _ *routineProgress) {
				changed := strings.Repeat("c", 64)
				snapshot.HomeSceneSHA256 = changed
				snapshot.Actors[0].HomeSceneSHA256 = changed
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := base
			current.Actors = append([]worldread.Actor(nil), base.Actors...)
			currentProgress := progress
			currentProgress.Rest = &routineRestProgress{
				Scope: progress.Rest.Scope,
				Seats: map[string]routineRestSeatCheckpoint{"mio": progress.Rest.Seats["mio"]},
			}
			currentProgress.World = progress.World
			test.mutate(&current, &currentProgress)
			currentEncoded, err := json.Marshal(currentProgress)
			if err != nil {
				t.Fatal(err)
			}
			currentIntent := *intent
			currentIntent.Output = string(currentEncoded)
			if err := runtime.validatePendingRestSnapshot(current, &currentIntent); err == nil {
				t.Fatal("untrusted snapshot must defer the pending rest instead of allowing cancellation")
			}
		})
	}
}

func TestRetryingRestAllowsDaytimeFallbackWithoutLosingRestCheckpoint(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
	state, err := runtime.draftIntent(ctx, "mio", agentroutine.Plan{
		Kind: agentroutine.ActivityRelax, RelaxRoom: agentroutine.RelaxRoomShared, Date: clock.Format("2006-01-02"),
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		t.Fatal(err)
	}
	progress.Rest = &routineRestProgress{
		Scope: agentroutine.RelaxRoomShared, Mode: routineRestModeReading,
		Seats: map[string]routineRestSeatCheckpoint{"mio": {
			RoomID: "living", SlotID: "sofa", RequestedAt: clock.Add(-time.Minute),
		}},
		Read: &routineRestReadProgress{
			Title: "Saved book", Author: "Author", SourceURL: "https://example.test/book",
			RightsURL: "https://example.test/rights", BodyHash: strings.Repeat("a", 64),
			BodyRunes: 1500, StartRune: 0, EndRune: 1200, NextRune: 1200,
			StartedAt: clock.Add(-time.Minute),
		},
	}
	state, progress, err = runtime.saveProgress(ctx, state, "rest_reading_checkpoint_saved", progress)
	if err != nil {
		t.Fatal(err)
	}
	state.Pending.RetryAfter = clock.Add(15 * time.Minute)
	state, err = runtime.store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	beforeOutput, beforeIntentID, beforeRetry := state.Pending.Output, state.Pending.ID, state.Pending.RetryAfter
	fallbackCalls := 0
	source := runtime.idleSource(func(context.Context, string) (agentheartbeat.Work, bool, error) {
		return agentheartbeat.Work{
			ID: "daytime-priority-work",
			Run: func(context.Context, <-chan struct{}) error {
				fallbackCalls++
				return nil
			},
		}, true, nil
	})
	work, found, err := source(ctx, "mio")
	if err != nil || !found || work.ID != "daytime-priority-work" {
		t.Fatalf("pending rest daytime priority discovery = %+v found=%t err=%v", work, found, err)
	}
	if err := work.Run(ctx, make(chan struct{})); err != nil {
		t.Fatalf("daytime priority Work.Run: %v", err)
	}
	after, err := runtime.store.Load(ctx, "mio")
	if err != nil || after.Pending == nil || after.Pending.ID != beforeIntentID ||
		after.Pending.Output != beforeOutput || !after.Pending.RetryAfter.Equal(beforeRetry) {
		t.Fatalf("daytime priority changed resumable rest checkpoint: %+v err=%v", after, err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("daytime fallback calls = %d; want one", fallbackCalls)
	}
}

func TestRoutineCheckpointMergesScheduleRolloverButRejectsNewerProgress(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 21, 59, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
	state, err := runtime.draftIntent(ctx, "mio", agentroutine.Plan{
		Kind: agentroutine.ActivityRelax, RelaxRoom: agentroutine.RelaxRoomShared, Date: clock.Format("2006-01-02"),
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		t.Fatal(err)
	}
	progress.Rest = &routineRestProgress{Scope: agentroutine.RelaxRoomShared, Mode: routineRestModeReading}
	state, progress, err = runtime.saveProgress(ctx, state, "before_schedule_rollover", progress)
	if err != nil {
		t.Fatal(err)
	}
	staleState := state
	clock = time.Date(2026, time.November, 15, 22, 0, 1, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	rolled, _, err := runtime.ensureState(ctx, "mio")
	if err != nil || rolled.ScheduleDate != "2026-11-16" || rolled.Revision <= staleState.Revision {
		t.Fatalf("schedule rollover = %+v err=%v; want next-day state revision", rolled, err)
	}
	progress.Rest.FailureReason = "schedule_rollover_checkpoint"
	saved, progress, err := runtime.saveProgress(ctx, staleState, "after_schedule_rollover", progress)
	if err != nil || saved.ScheduleDate != "2026-11-16" || saved.Pending == nil ||
		saved.Pending.Step != "after_schedule_rollover" || saved.Revision <= rolled.Revision {
		t.Fatalf("checkpoint did not safely inherit schedule revision: state=%+v err=%v", saved, err)
	}
	nightPlan, _, err := runtime.planFresh(ctx, "mio", true, nil)
	if err != nil || (nightPlan.Kind == agentroutine.ActivityRelax && nightPlan.RelaxRoom == agentroutine.RelaxRoomShared) {
		t.Fatalf("22:00 replan = %+v err=%v; shared daytime rest must yield to night policy", nightPlan, err)
	}

	staleAfterSave := saved
	firstProgress := progress
	firstRest := *progress.Rest
	firstProgress.Rest = &firstRest
	firstProgress.Rest.FailureReason = "first concurrent result"
	latest, firstProgress, err := runtime.saveProgress(ctx, saved, "first_progress_writer", firstProgress)
	if err != nil {
		t.Fatal(err)
	}
	secondProgress := progress
	secondRest := *progress.Rest
	secondProgress.Rest = &secondRest
	secondProgress.Rest.FailureReason = "stale competing result"
	if _, _, err := runtime.saveProgress(ctx, staleAfterSave, "stale_progress_writer", secondProgress); !errors.Is(err, agentroutine.ErrConflict) {
		t.Fatalf("stale progress writer error = %v; want CAS conflict without overwriting newer checkpoint", err)
	}
	persisted, err := runtime.store.Load(ctx, "mio")
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeRoutineProgress(persisted.Pending.Output)
	if err != nil || got.Rest == nil || got.Rest.FailureReason != firstProgress.Rest.FailureReason ||
		persisted.Revision != latest.Revision {
		t.Fatalf("newer progress checkpoint was overwritten: %+v err=%v", persisted, err)
	}
}
