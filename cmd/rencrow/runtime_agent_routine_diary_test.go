package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestExecuteDiaryBuildsConciseMixedDayAndBoundsReflectionInput(t *testing.T) {
	const date = "2026-11-13"
	ctx := context.Background()
	runtime := newRoutineDiaryTestRuntime(t, nil)
	location := time.FixedZone("JST", 9*60*60)

	kinds := []agentroutine.ActivityKind{
		agentroutine.ActivityMorningChat,
		agentroutine.ActivityConversation,
		agentroutine.ActivityWork,
		agentroutine.ActivityHobby,
		agentroutine.ActivityRelax,
		agentroutine.ActivityWake,
		agentroutine.ActivitySleep,
		agentroutine.ActivityConversation,
	}
	const activityCount = 12
	for index := 0; index < activityCount; index++ {
		kind := kinds[index%len(kinds)]
		fact := agentroutine.Activity{
			ActorID: "mio", Date: date, Key: fmt.Sprintf("activity-%02d", index), Kind: kind,
			At:     time.Date(2026, time.November, 13, index+1, 0, 0, 0, location),
			Source: "diary-test", Confirmed: true,
			Summary: fmt.Sprintf("ACTIVITY-%02d %s", index, strings.Repeat("確認済み活動の記録。", 180)),
		}
		if _, err := runtime.store.SaveFact(ctx, fact); err != nil {
			t.Fatalf("SaveFact(%s): %v", fact.Key, err)
		}
	}

	dayStart := time.Date(2026, time.November, 13, 0, 0, 0, 0, location)
	gamePath := filepath.Join(t.TempDir(), "game_bridge_events.jsonl")
	writeRoutineDiaryGameFixture(t, gamePath, []viewer.GameBridgeEvent{
		{EventID: "game-turn-1", GameID: "nethack", SessionID: "mio-session", Turn: 1, Persona: "mio", ExecutedActions: []string{"move"}, Result: map[string]any{"success": true}, CreatedAt: dayStart.Add(22 * time.Hour).Format(time.RFC3339Nano)},
		{EventID: "game-turn-2", GameID: "nethack", SessionID: "mio-session", Turn: 2, Persona: "mio", ExecutedActions: []string{"wait"}, Result: map[string]any{"success": true}, CreatedAt: dayStart.Add(23 * time.Hour).Format(time.RFC3339Nano)},
	})
	runtime.deps.gameBridgeStore = viewer.NewGameBridgeStore(gamePath)

	worker := &routineDiaryPromptCaptureWorker{}
	runtime.workers = map[string]privateTaskWorker{"mio": worker}
	correlationID := "diary-test-receipt"
	var boardWrite struct {
		Revision   int64  `json:"revision"`
		Body       string `json:"body"`
		Reflection string `json:"reflection"`
	}
	boardServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/diaries/"+date {
			t.Errorf("BOARD request = %s %s, want PUT diary date", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&boardWrite); err != nil {
			t.Errorf("decode BOARD diary request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", correlationID)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entry": map[string]any{
				"actor_id": "mio", "date": date, "revision": boardWrite.Revision,
				"body": boardWrite.Body, "reflection": boardWrite.Reflection,
				"updated_at": time.Date(2026, time.November, 14, 0, 0, 0, 0, time.UTC),
			},
			"correlation_id": correlationID,
		})
	}))
	t.Cleanup(boardServer.Close)
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL:         boardServer.URL,
		TokenSource: func(context.Context) ([]byte, error) { return []byte("test-token"), nil },
		ExpectedConnection: boardcommunication.ConnectionIdentity{
			PrincipalID: "rencrow-comm", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)
	runtime.cfg = &config.Config{}
	runtime.deps.boardCommunicationActorBoots = map[string]*boardCommunicationBoot{
		"mio": {composition: &boardCommunicationComposition{boardClient: client}},
	}

	progress := routineProgress{Actors: []routineActorProgress{{ActorID: "mio", TaskID: modulecore.NewTaskID()}}}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.store.DraftIntent(ctx, agentroutine.Intent{
		ID: string(modulecore.NewRequestID()), ActorID: "mio", Date: date,
		TaskID: string(progress.Actors[0].TaskID), Kind: agentroutine.ActivityDiary, Label: "daily_diary",
		Step: "planned", Output: string(encoded), CreatedAt: dayStart,
	})
	if err != nil {
		t.Fatalf("DraftIntent: %v", err)
	}
	prepared, state, progress, err := runtime.prepareRuns(ctx, nil, state, progress)
	if err != nil || len(prepared) != 1 {
		t.Fatalf("prepare diary Run = %d runs, err=%v", len(prepared), err)
	}
	state, progress, err = runtime.executeDiary(ctx, nil, state, progress, prepared[0])
	if err != nil {
		t.Fatalf("executeDiary: %v", err)
	}
	if !progress.EffectCompleted || progress.DiaryReceipt != correlationID || state.Pending == nil {
		t.Fatalf("diary completion state=%+v progress=%+v", state, progress)
	}
	if len(boardWrite.Body) >= 24<<10 || strings.Contains(boardWrite.Body, strings.Repeat("確認済み活動の記録。", 40)) || strings.Count(boardWrite.Body, "ACTIVITY-") >= activityCount {
		t.Fatalf("BOARD diary remained a raw event dump: %d bytes", len(boardWrite.Body))
	}
	gameSummary := "NetHackを2ターン（22:00〜23:00）遊びました"
	if !strings.Contains(boardWrite.Body, gameSummary) {
		t.Fatalf("BOARD diary omitted final canonical game activity %q", gameSummary)
	}
	if strings.Contains(boardWrite.Body, "残り") || len(boardWrite.Body) > 120<<10 {
		t.Fatalf("BOARD diary body has an omission marker or exceeds CORE's safe body limit: %d bytes", len(boardWrite.Body))
	}
	if len(worker.prompt) > 24<<10 {
		t.Fatalf("reflection provider input = %d bytes, want at most 24 KiB", len(worker.prompt))
	}
	if strings.Contains(worker.prompt, "中略") || !strings.Contains(worker.prompt, gameSummary) {
		t.Fatalf("concise reflection input should contain the activity without a truncation marker: %s", worker.prompt[len(worker.prompt)-min(500, len(worker.prompt)):])
	}
	if boardWrite.Body != progress.DiaryBody || boardWrite.Reflection != progress.DiaryReflection ||
		progress.DiaryReflectionVersion != agentRoutineDiaryReflectionVersion || boardWrite.Revision != 1 {
		t.Fatal("BOARD did not receive the exact complete saved diary revision")
	}
	diary, err := runtime.store.GetDiary(ctx, "mio", date)
	if err != nil || diary.Receipt != correlationID || diary.Revision != 1 || diary.Body != boardWrite.Body {
		t.Fatalf("saved diary receipt=%+v err=%v, want exact one-row revision receipt", diary, err)
	}
}

func TestExecuteDiaryRefreshesPublishedReflectionVersionWithoutChangingBody(t *testing.T) {
	const actorID, date = "mio", "2026-11-15"
	ctx := context.Background()
	runtime := newRoutineDiaryTestRuntime(t, nil)
	loc := runtime.location()
	runtime.now = func() time.Time { return time.Date(2026, time.November, 16, 10, 0, 0, 0, loc) }
	fact := agentroutine.Activity{
		ActorID: actorID, Date: date, Key: "confirmed-work", Kind: agentroutine.ActivityWork,
		At: time.Date(2026, time.November, 15, 12, 0, 0, 0, loc), Source: "diary-test", Confirmed: true,
		Summary: "確認済みの作業を完了しました。",
	}
	if _, err := runtime.store.SaveFact(ctx, fact); err != nil {
		t.Fatalf("SaveFact: %v", err)
	}
	body, err := runtime.buildDiaryBody(ctx, actorID, date)
	if err != nil {
		t.Fatalf("buildDiaryBody: %v", err)
	}
	old, err := runtime.store.SaveDiary(ctx, agentroutine.Diary{
		ActorID: actorID, Date: date, Body: body, Reflection: "旧版の感想", Revision: 1,
	})
	if err != nil {
		t.Fatalf("SaveDiary old publication draft: %v", err)
	}
	old, err = runtime.store.SaveDiary(ctx, agentroutine.Diary{
		ActorID: actorID, Date: date, Body: old.Body, Reflection: old.Reflection,
		Revision: old.Revision, PostingKey: old.PostingKey, Receipt: "old-board-receipt", ReceiptRevision: "1",
	})
	if err != nil {
		t.Fatalf("SaveDiary old publication receipt: %v", err)
	}
	boardCalls := 0
	var boardWrite struct {
		Revision   int64  `json:"revision"`
		Body       string `json:"body"`
		Reflection string `json:"reflection"`
	}
	boardServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		boardCalls++
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/diaries/"+date {
			t.Errorf("BOARD request = %s %s, want PUT dated diary", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&boardWrite); err != nil {
			t.Errorf("decode BOARD diary request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", "new-board-receipt")
		_ = json.NewEncoder(w).Encode(map[string]any{"entry": map[string]any{
			"actor_id": actorID, "date": date, "revision": boardWrite.Revision,
			"body": boardWrite.Body, "reflection": boardWrite.Reflection,
			"updated_at": time.Date(2026, time.November, 16, 1, 0, 0, 0, time.UTC),
		}, "correlation_id": "new-board-receipt"})
	}))
	t.Cleanup(boardServer.Close)
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL: boardServer.URL, TokenSource: func(context.Context) ([]byte, error) { return []byte("test-token"), nil },
		ExpectedConnection: boardcommunication.ConnectionIdentity{
			PrincipalID: "rencrow-comm", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)
	if changed, err := runtime.checkDiaryAmendment(ctx, actorID, date); err != nil || changed {
		t.Fatalf("checkDiaryAmendment before BOARD readiness = %t, %v; want unavailable without caching a negative result", changed, err)
	}
	runtime.deps.boardCommunicationActorBoots = map[string]*boardCommunicationBoot{
		actorID: {composition: &boardCommunicationComposition{boardClient: client}},
	}
	taskManager := runtime.tasks
	runtime.tasks = nil
	changed, err := runtime.readDiaryAmendment(ctx, actorID, date)
	runtime.tasks = taskManager
	if err != nil || !changed {
		t.Fatalf("readDiaryAmendment for published old reflection without work-fact scope = %t, %v; want old reflection version to be an immediate candidate", changed, err)
	}
	worker := &routineDiaryPromptCaptureWorker{}
	runtime.workers = map[string]privateTaskWorker{actorID: worker}
	state, err := runtime.store.Load(ctx, actorID)
	if err != nil {
		t.Fatalf("Load routine state: %v", err)
	}
	state.ActivatedOn, state.NextDiaryDate, state.ScheduleDate = "2026-11-14", "2026-11-16", "2026-11-16"
	state.PlannedSleep, state.PlannedWake, err = agentroutine.ScheduleFor(actorID, state.ScheduleDate)
	if err != nil {
		t.Fatalf("ScheduleFor: %v", err)
	}
	state.CooldownUntil = runtime.now().Add(time.Hour)
	if _, err := runtime.store.Save(ctx, state, state.Revision); err != nil {
		t.Fatalf("Save routine cooldown state: %v", err)
	}
	work, found, err := runtime.idleSource(nil)(ctx, actorID)
	if err != nil {
		t.Fatalf("idleSource with stale reflection version: %v", err)
	}
	if !found || work.ID != "agent-routine:mio:2026-11-15:diary" {
		t.Fatalf("idleSource with stale reflection version = %q, found=%t; want previous-day diary despite cooldown", work.ID, found)
	}
	if err := work.Run(ctx, make(chan struct{})); err != nil {
		t.Fatalf("run scheduled reflection refresh: %v", err)
	}
	if boardCalls != 1 || worker.prompt == "" {
		t.Fatalf("BOARD calls=%d reflection prompt empty=%t; want one publication with a regenerated reflection", boardCalls, worker.prompt == "")
	}
	if boardWrite.Revision != old.Revision+1 || boardWrite.Body != old.Body || boardWrite.Reflection == old.Reflection {
		t.Fatalf("BOARD amendment = revision %d body same=%t reflection changed=%t; want next revision, exact body, new reflection", boardWrite.Revision, boardWrite.Body == old.Body, boardWrite.Reflection != old.Reflection)
	}
	updated, err := runtime.store.GetDiary(ctx, actorID, date)
	if err != nil || updated.Revision != old.Revision+1 || updated.ReflectionVersion != agentRoutineDiaryReflectionVersion || updated.Receipt != "new-board-receipt" {
		t.Fatalf("saved updated diary = %+v, %v; want acknowledged current reflection revision", updated, err)
	}
	if candidate, err := runtime.readDiaryAmendment(ctx, actorID, date); err != nil || candidate {
		t.Fatalf("readDiaryAmendment after acknowledged refresh = %t, %v; want no next candidate", candidate, err)
	}
	state, err = runtime.store.Load(ctx, actorID)
	if err != nil {
		t.Fatalf("Load current-version routine state: %v", err)
	}
	state.CooldownUntil = runtime.now().Add(time.Hour)
	if _, err := runtime.store.Save(ctx, state, state.Revision); err != nil {
		t.Fatalf("Save current-version cooldown state: %v", err)
	}
	if work, found, err := runtime.idleSource(nil)(ctx, actorID); err != nil || found {
		t.Fatalf("idleSource after current-version publication = %q, found=%t, err=%v; want cooldown to remain in force", work.ID, found, err)
	}
}

func TestExecuteDiaryReplaysUnacknowledgedOldReflectionExactly(t *testing.T) {
	const actorID, date = "mio", "2026-11-15"
	ctx := context.Background()
	runtime := newRoutineDiaryTestRuntime(t, nil)
	old, err := runtime.store.SaveDiary(ctx, agentroutine.Diary{
		ActorID: actorID, Date: date, Body: "saved complete body", Reflection: "saved old reflection", Revision: 1,
	})
	if err != nil {
		t.Fatalf("SaveDiary old outbox: %v", err)
	}
	var boardWrite struct {
		Revision   int64  `json:"revision"`
		Body       string `json:"body"`
		Reflection string `json:"reflection"`
	}
	boardServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&boardWrite); err != nil {
			t.Errorf("decode BOARD diary request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", "exact-retry-receipt")
		_ = json.NewEncoder(w).Encode(map[string]any{"entry": map[string]any{
			"actor_id": actorID, "date": date, "revision": boardWrite.Revision,
			"body": boardWrite.Body, "reflection": boardWrite.Reflection,
			"updated_at": time.Date(2026, time.November, 16, 1, 0, 0, 0, time.UTC),
		}, "correlation_id": "exact-retry-receipt"})
	}))
	t.Cleanup(boardServer.Close)
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL: boardServer.URL, TokenSource: func(context.Context) ([]byte, error) { return []byte("test-token"), nil },
		ExpectedConnection: boardcommunication.ConnectionIdentity{
			PrincipalID: "rencrow-comm", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)
	runtime.deps.boardCommunicationActorBoots = map[string]*boardCommunicationBoot{
		actorID: {composition: &boardCommunicationComposition{boardClient: client}},
	}
	worker := &routineDiaryPromptCaptureWorker{}
	runtime.workers = map[string]privateTaskWorker{actorID: worker}
	taskID := modulecore.NewTaskID()
	task, run, err := runtime.tasks.CreateAndStartRun(ctx, domaintask.Task{
		TaskID: taskID, Title: "routine diary retry", ModuleID: agentRoutineModuleID, OwnerID: runtime.ownerID,
		Assignee: actorID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
	}, domaintask.SharedRoleContext{TaskID: taskID, UserIntent: "CORE routine step", CurrentPlan: "routine diary retry"})
	if err != nil {
		t.Fatalf("CreateAndStartRun: %v", err)
	}
	state, err := runtime.store.DraftIntent(ctx, agentroutine.Intent{
		ID: "old-diary-retry", ActorID: actorID, Date: date, TaskID: string(taskID),
		Kind: agentroutine.ActivityDiary, Label: "daily_diary", Step: "diary_put_pending",
	})
	if err != nil {
		t.Fatalf("DraftIntent: %v", err)
	}
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: actorID, TaskID: taskID, RunID: run.RunID}}}
	state, progress, err = runtime.executeDiary(ctx, nil, state, progress, routinePreparedRun{task: task, run: run})
	if err != nil {
		t.Fatalf("executeDiary exact retry: %v", err)
	}
	if worker.prompt != "" || boardWrite.Revision != old.Revision || boardWrite.Body != old.Body || boardWrite.Reflection != old.Reflection {
		t.Fatalf("retry regenerated=%t revision=%d body exact=%t reflection exact=%t; want exact saved draft without generation", worker.prompt != "", boardWrite.Revision, boardWrite.Body == old.Body, boardWrite.Reflection == old.Reflection)
	}
	if progress.DiaryReflectionVersion != old.ReflectionVersion {
		t.Fatalf("retry progress reflection version = %q; want saved legacy version %q", progress.DiaryReflectionVersion, old.ReflectionVersion)
	}
	replayed, err := runtime.store.GetDiary(ctx, actorID, date)
	if err != nil || replayed.Revision != old.Revision || replayed.PostingKey != old.PostingKey || replayed.ReflectionVersion != old.ReflectionVersion || replayed.Receipt != "exact-retry-receipt" {
		t.Fatalf("saved exact retry = %+v, %v; want old payload and posting identity retained", replayed, err)
	}
}

func TestBuildDiaryBodyErrorsInsteadOfPublishingAboveBOARDTextBudget(t *testing.T) {
	const date = "2026-11-13"
	runtime := newRoutineDiaryTestRuntime(t, nil)
	for index := 0; index < 256; index++ {
		fact := agentroutine.Activity{
			ActorID: "mio", Date: date, Key: fmt.Sprintf("oversized-%03d", index), Kind: agentroutine.ActivityHobby,
			At: time.Date(2026, time.November, 13, 0, index, 0, 0, time.UTC), Source: "diary-test", Confirmed: true,
			Summary: fmt.Sprintf("活動%03d %s", index, strings.Repeat("事", 1200)),
		}
		if _, err := runtime.store.SaveFact(context.Background(), fact); err != nil {
			t.Fatalf("SaveFact(%s): %v", fact.Key, err)
		}
	}
	body, err := runtime.buildDiaryBody(context.Background(), "mio", date)
	if err == nil || !strings.Contains(err.Error(), "refusing incomplete diary") {
		t.Fatalf("buildDiaryBody error = %v, want explicit incomplete-diary rejection", err)
	}
	if body != "" {
		t.Fatalf("buildDiaryBody returned a partial body of %d bytes alongside hard-limit error", len(body))
	}
}

func TestBuildDiaryBodyIncludesVerifiedMediaPlaybackOffsets(t *testing.T) {
	const date = "2026-11-13"
	runtime := newRoutineDiaryTestRuntime(t, nil)
	startedAt := time.Date(2026, time.November, 13, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(12 * time.Second)
	reference, err := json.Marshal(routineMediaFactReference{
		ItemID: "clip-1", Kind: "video", StartMS: 65_250, EndMS: 77_250, ElapsedMS: 12_000,
		DurationMS: 180_000, StartedAt: startedAt, FinishedAt: finishedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	fact := agentroutine.Activity{
		ActorID: "mio", Date: date, Key: "media-offset-test", Kind: agentroutine.ActivityHobby,
		At: finishedAt, Source: "routine_media_playback", SourceID: "intent-1:clip-1", Confirmed: true,
		Summary: "動画「作品タイトル」を合計12.0秒再生（出典: 公式）", Reference: string(reference),
	}
	if _, err := runtime.store.SaveFact(context.Background(), fact); err != nil {
		t.Fatalf("SaveFact: %v", err)
	}
	body, err := runtime.buildDiaryBody(context.Background(), "mio", date)
	if err != nil {
		t.Fatalf("buildDiaryBody: %v", err)
	}
	if !strings.Contains(body, "再生範囲 00:01:05.250〜00:01:17.250") {
		t.Fatalf("diary body omitted validated playback offsets: %s", body)
	}
}

type routineDiaryPromptCaptureWorker struct{ prompt string }

func (w *routineDiaryPromptCaptureWorker) ExecutePrivateWork(_ context.Context, input conversation.TurnInput, _ []string) (string, error) {
	w.prompt = input.MessageText()
	return "今日は確認できた活動を振り返りました。", nil
}

func newRoutineDiaryTestRuntime(t *testing.T, gameStore *viewer.GameBridgeStore) *agentRoutineRuntime {
	t.Helper()
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatalf("open routine store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatalf("open Task store: %v", err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	return &agentRoutineRuntime{
		store: store, tasks: taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()), ownerID: "ren",
		cfg: &config.Config{}, deps: &Dependencies{gameBridgeStore: gameStore},
		now: func() time.Time { return time.Date(2026, time.November, 14, 10, 0, 0, 0, time.UTC) },
	}
}

func TestAgentRoutineRetryingDiaryPausesIntoSleepInSameWorkItem(t *testing.T) {
	cfg, worldDeps, worldServer := newRoutineHouseViewTestRuntime(t, true)
	defer worldServer.Close()
	runtime := newRoutineDiaryTestRuntime(t, nil)
	loc := runtime.location()
	const actorID = "midori"
	cfg.Heartbeat.Routine.PrivateRooms = map[string]string{actorID: "room-midori"}
	runtime.cfg = cfg
	runtime.world = worldDeps.agentRoutine.world
	runtime.worldActorIDs = worldDeps.agentRoutine.worldActorIDs
	now := time.Date(2026, time.November, 15, 1, 59, 50, 0, loc)
	runtime.now = func() time.Time { return now }
	sleepAt, wakeAt, err := agentroutine.ScheduleFor(actorID, "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	taskID := modulecore.NewTaskID()
	startedTask, startedRun, err := runtime.tasks.CreateAndStartRun(context.Background(), domaintask.Task{
		TaskID: taskID, Title: "routine diary", ModuleID: agentRoutineModuleID, OwnerID: runtime.ownerID,
		Assignee: actorID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
	}, domaintask.SharedRoleContext{TaskID: taskID, UserIntent: "CORE routine step", CurrentPlan: "routine diary"})
	if err != nil {
		t.Fatalf("start existing canonical diary Run: %v", err)
	}
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: actorID, TaskID: taskID, RunID: startedRun.RunID}}}
	encodedProgress, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.store.Load(context.Background(), actorID)
	if err != nil {
		t.Fatal(err)
	}
	state.ActivatedOn, state.NextDiaryDate, state.ScheduleDate = "2026-11-14", "2026-11-14", "2026-11-15"
	state.PlannedSleep, state.PlannedWake = sleepAt, wakeAt
	state.Pending = &agentroutine.Intent{
		ID: "diary-retry", ActorID: actorID, Date: "2026-11-14", TaskID: string(progress.Actors[0].TaskID),
		Kind: agentroutine.ActivityDiary, Step: "diary_put_pending", Output: string(encodedProgress), RetryAfter: now.Add(15 * time.Minute),
	}
	if _, err := runtime.store.Save(context.Background(), state, state.Revision); err != nil {
		t.Fatalf("save pending diary fixture: %v", err)
	}
	work, ok, err := runtime.idleSource(nil)(context.Background(), actorID)
	if err != nil || !ok || !strings.HasSuffix(work.ID, ":sleep") {
		t.Fatalf("idleSource at 01:59:50 = work %q, ok=%t, err=%v; want sleep despite diary retry backoff", work.ID, ok, err)
	}
	if err := work.Run(context.Background(), make(chan struct{})); err != nil {
		t.Fatalf("same work item did not complete planned sleep: %v", err)
	}
	state, err = runtime.store.Load(context.Background(), actorID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || state.PausedDiary == nil || state.PausedDiary.ID != "diary-retry" || state.PausedDiary.TaskID != string(progress.Actors[0].TaskID) || state.PausedDiary.Output != string(encodedProgress) {
		t.Fatalf("post-sleep checkpoint = %+v; want original diary paused with exact TaskID/progress", state)
	}
	if !state.LogicalSleepUntil.Equal(wakeAt) {
		t.Fatalf("LogicalSleepUntil = %v, want planned wake %v", state.LogicalSleepUntil, wakeAt)
	}
	runs, err := runtime.tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: startedTask.TaskID})
	if err != nil || len(runs) != 1 || runs[0].RunID != startedRun.RunID || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("canonical diary Run after sleep pause = %+v, %v; want the exact Run closed to Waiting", runs, err)
	}
}

func writeRoutineDiaryGameFixture(t *testing.T, path string, events []viewer.GameBridgeEvent) {
	t.Helper()
	var fixture bytes.Buffer
	encoder := json.NewEncoder(&fixture)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatalf("encode GAMES fixture event: %v", err)
		}
	}
	if err := os.WriteFile(path, fixture.Bytes(), 0o600); err != nil {
		t.Fatalf("write GAMES fixture: %v", err)
	}
}
