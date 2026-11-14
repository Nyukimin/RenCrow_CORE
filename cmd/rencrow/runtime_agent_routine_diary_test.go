package main

import (
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
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestExecuteDiaryKeepsMixedLongDayAndBoundsReflectionInput(t *testing.T) {
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
	if len(boardWrite.Body) <= 24<<10 {
		t.Fatalf("BOARD diary body = %d bytes; want a complete body beyond the former 24 KiB cut", len(boardWrite.Body))
	}
	for index := 0; index < activityCount; index++ {
		if marker := fmt.Sprintf("ACTIVITY-%02d", index); !strings.Contains(boardWrite.Body, marker) {
			t.Errorf("BOARD diary omitted confirmed activity %s", marker)
		}
	}
	gameSummary := "GAMESでNetHackを2ターン遊んだ"
	if !strings.Contains(boardWrite.Body, gameSummary) {
		t.Fatalf("BOARD diary omitted final canonical game activity %q", gameSummary)
	}
	if strings.Contains(boardWrite.Body, "残り") || len(boardWrite.Body) > 120<<10 {
		t.Fatalf("BOARD diary body has an omission marker or exceeds CORE's safe body limit: %d bytes", len(boardWrite.Body))
	}
	if len(worker.prompt) > 24<<10 {
		t.Fatalf("reflection provider input = %d bytes, want at most 24 KiB", len(worker.prompt))
	}
	if !strings.Contains(worker.prompt, "中略") || !strings.Contains(worker.prompt, gameSummary) {
		t.Fatalf("bounded reflection input should mark omitted middle facts and retain the final activity: %s", worker.prompt[len(worker.prompt)-min(500, len(worker.prompt)):])
	}
	if boardWrite.Body != progress.DiaryBody || boardWrite.Reflection != progress.DiaryReflection || boardWrite.Revision != 1 {
		t.Fatal("BOARD did not receive the exact complete saved diary revision")
	}
	diary, err := runtime.store.GetDiary(ctx, "mio", date)
	if err != nil || diary.Receipt != correlationID || diary.Revision != 1 || diary.Body != boardWrite.Body {
		t.Fatalf("saved diary receipt=%+v err=%v, want exact one-row revision receipt", diary, err)
	}
}

func TestBuildDiaryBodyErrorsInsteadOfPublishingAboveBOARDTextBudget(t *testing.T) {
	const date = "2026-11-13"
	runtime := newRoutineDiaryTestRuntime(t, nil)
	for index := 0; index < 64; index++ {
		fact := agentroutine.Activity{
			ActorID: "mio", Date: date, Key: fmt.Sprintf("oversized-%02d", index), Kind: agentroutine.ActivityWork,
			Source: "diary-test", Confirmed: true, Summary: strings.Repeat("事", 1200),
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

func writeRoutineDiaryGameFixture(t *testing.T, path string, events []viewer.GameBridgeEvent) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create GAMES fixture: %v", err)
	}
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			_ = file.Close()
			t.Fatalf("encode GAMES fixture event: %v", err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close GAMES fixture: %v", err)
	}
}
