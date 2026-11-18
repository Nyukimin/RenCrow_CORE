package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

func TestBuildDiaryBodySummarizesConfirmedDailyLife(t *testing.T) {
	const actorID, date = "aka", "2026-11-16"
	runtime := newRoutineDiaryTestRuntime(t, nil)
	loc := time.FixedZone("JST", 9*60*60)
	at := func(hour, minute int) time.Time { return time.Date(2026, time.November, 16, hour, minute, 0, 0, loc) }
	transcript, err := json.Marshal([]map[string]string{
		{"actor_id": "aka", "text": "おはようございます。今日は涼しいですね。"},
		{"actor_id": "kin", "text": "涼しいと気持ちいいね。今日は焦らず進めよう。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	facts := []agentroutine.Activity{
		{Key: "sleep", Kind: agentroutine.ActivitySleep, At: at(1, 42), Source: "world_life_event", Confirmed: true, Summary: "WORLDでアカの部屋のベッドで就寝を確認しました。"},
		{Key: "wake", Kind: agentroutine.ActivityWake, At: at(5, 45), Source: "world_life_event", Confirmed: true, Summary: "WORLDで起床を確認しました。"},
		{Key: "relax-a", Kind: agentroutine.ActivityRelax, At: at(9, 6), Source: agentRoutineModuleID, Confirmed: true, Summary: "リビングで静かに過ごしました。"},
		{Key: "relax-b", Kind: agentroutine.ActivityRelax, At: at(9, 12), Source: agentRoutineModuleID, Confirmed: true, Summary: "リビングで静かに過ごしました。"},
		{Key: "move-start", Kind: agentroutine.ActivityRelax, At: at(9, 5), Source: "world_life_event", Confirmed: true, Summary: "WORLDで自宅内のリビングへの移動を開始しました。"},
		{Key: "move-arrive", Kind: agentroutine.ActivityRelax, At: at(9, 6), Source: "world_life_event", Confirmed: true, Summary: "WORLDで自宅内のリビングへの移動完了を確認しました。"},
		{Key: "chat-a", Kind: agentroutine.ActivityMorningChat, At: at(6, 0), Source: agentRoutineModuleID, Confirmed: true, Summary: "朝に短いテキスト会話をしました。", Reference: string(transcript)},
		{Key: "chat-b", Kind: agentroutine.ActivityMorningChat, At: at(6, 1), Source: agentRoutineModuleID, Confirmed: true, Summary: "朝に短いテキスト会話をしました。", Reference: string(transcript)},
		{Key: "game", Kind: agentroutine.ActivityHobby, At: at(14, 0), Source: "canonical_game_session", Confirmed: true, Summary: "GAMESでNetHackを2ターン遊んだ（初回 13:50、最終 14:00 JST）"},
		{Key: "work-start", Kind: agentroutine.ActivityWork, At: at(15, 0), Source: "canonical_run", SourceID: "run-1", Confirmed: true, Summary: "作業を開始した"},
		{Key: "work-done", Kind: agentroutine.ActivityWork, At: at(15, 30), Source: "canonical_run", SourceID: "run-1", Confirmed: true, Summary: "作業を完了した"},
	}
	for i := range facts {
		facts[i].ActorID, facts[i].Date = actorID, date
		if _, err := runtime.store.SaveFact(context.Background(), facts[i]); err != nil {
			t.Fatalf("SaveFact(%s): %v", facts[i].Key, err)
		}
	}

	body, err := runtime.buildDiaryBody(context.Background(), actorID, date)
	if err != nil {
		t.Fatalf("buildDiaryBody: %v", err)
	}
	for _, want := range []string{"1:42", "5:45", "リビング", "キン", "涼しいと気持ちいい", "NetHack", "13:50", "作業を完了"} {
		if !strings.Contains(body, want) {
			t.Errorf("life summary omitted %q: %s", want, body)
		}
	}
	if strings.Contains(body, "WORLD") || strings.Contains(body, "記録の範囲") || strings.Contains(body, "移動を開始") || strings.Contains(body, "移動完了") {
		t.Errorf("life summary exposes implementation/event log wording: %s", body)
	}
	if strings.Count(body, "涼しいと気持ちいい") != 1 {
		t.Errorf("repeated conversation quote was not deduplicated: %s", body)
	}
	if strings.Contains(body, "22:14") || strings.Contains(body, "就寝した") && !strings.Contains(body, "1:42") {
		t.Errorf("life summary invented a sleep time: %s", body)
	}
}

func TestRoutineDiaryMovementFallbackAndSeparateRestEpisodes(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	base := time.Date(2026, time.November, 16, 12, 0, 0, 0, loc)
	moveFacts := []agentroutine.Activity{
		{Key: "move-start", Kind: agentroutine.ActivityRelax, At: base, Source: "world_life_event", Confirmed: true, Summary: "WORLDで自宅内のリビングへの移動を開始しました。"},
		{Key: "move-arrive", Kind: agentroutine.ActivityRelax, At: base.Add(5 * time.Minute), Source: "world_life_event", Confirmed: true, Summary: "WORLDで自宅内のリビングへの移動完了を確認しました。"},
	}
	body := renderRoutineDiary("2026-11-16", moveFacts, loc)
	if strings.Contains(body, "移動を開始") || strings.Count(body, "へ移りました") != 1 || !strings.Contains(body, "リビングへ移りました") {
		t.Fatalf("movement-only fallback = %s; want one concise arrival", body)
	}

	restFacts := []agentroutine.Activity{
		{Key: "rest-1", Kind: agentroutine.ActivityRelax, At: base, Source: agentRoutineModuleID, Confirmed: true, Summary: "リビングで静かに過ごしました。"},
		{Key: "work", Kind: agentroutine.ActivityWork, At: base.Add(10 * time.Minute), Source: "canonical_run", SourceID: "run-1", Confirmed: true, Summary: "作業を完了した"},
		{Key: "rest-2", Kind: agentroutine.ActivityRelax, At: base.Add(20 * time.Minute), Source: agentRoutineModuleID, Confirmed: true, Summary: "リビングで静かに過ごしました。"},
	}
	body = renderRoutineDiary("2026-11-16", restFacts, loc)
	if strings.Count(body, "静かに過ごすひととき") != 2 || strings.Contains(body, "12:00〜12:20") || strings.Contains(body, "その間") {
		t.Fatalf("relax observations should be separate around work without claiming continuous presence: %s", body)
	}
}

func TestRoutineDiaryConversationKeepsLateNightAndAfterMidnightSeparateAndRequiresActor(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	late := time.Date(2026, time.November, 16, 23, 40, 0, 0, loc)
	transcript, err := json.Marshal([]routineDiaryTranscriptLine{
		{ActorID: "aka", Text: "おやすみ。"}, {ActorID: "kin", Text: "また明日。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	facts := []agentroutine.Activity{
		{ActorID: "aka", Kind: agentroutine.ActivityMorningChat, At: late, Source: agentRoutineModuleID, Reference: string(transcript)},
		{ActorID: "aka", Kind: agentroutine.ActivityMorningChat, At: late.Add(10 * time.Minute), Source: agentRoutineModuleID, Reference: string(transcript)},
		{ActorID: "aka", Kind: agentroutine.ActivityMorningChat, At: late.Add(time.Hour), Source: agentRoutineModuleID, Reference: `[{"actor_id":"kin","text":"unscoped line"}]`},
	}
	lines := routineDiaryConversationLines(facts, loc)
	if len(lines) != 2 || !strings.Contains(lines[0], "夜はキン") || !strings.Contains(lines[0], "また明日") || !strings.Contains(lines[1], "深夜に人との会話") {
		t.Fatalf("conversation groups = %v; want one deduplicated late-night quote and a generic scoped record for missing content", lines)
	}
	facts = append(facts, agentroutine.Activity{
		ActorID: "aka", Kind: agentroutine.ActivityMorningChat, At: late.Add(20 * time.Minute), Source: agentRoutineModuleID,
		Reference: `[ {"actor_id":"aka","text":"おはよう"},{"actor_id":"kin","text":"おはようございます"} ]`,
	})
	lines = routineDiaryConversationLines(facts, loc)
	if len(lines) != 2 || !strings.Contains(strings.Join(lines, "\n"), "深夜はキン") || !strings.Contains(strings.Join(lines, "\n"), "夜はキン") {
		t.Fatalf("after-midnight conversation was merged into late night: %v", lines)
	}
}

func TestRoutineDiaryConversationIncludesEachRecordedPeerAndNaturalTimeBands(t *testing.T) {
	peers := []routineDiaryTranscriptLine{
		{ActorID: "aka", Text: "おはよう。"}, {ActorID: "kin", Text: "今日もよろしく。"},
		{ActorID: "kuro", Text: "あとでゲームしよう。"}, {ActorID: "shiro", Text: "いい天気だね。"},
		{ActorID: "mio", Text: "準備しておくね。"}, {ActorID: "gin", Text: "またあとで。"},
		{ActorID: "ao", Text: "楽しみにしてる。"}, {ActorID: "midori", Text: "ゆっくりしよう。"},
	}
	encoded, err := json.Marshal(peers)
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("JST", 9*60*60)
	lines := routineDiaryConversationLines([]agentroutine.Activity{{
		ActorID: "aka", Kind: agentroutine.ActivityMorningChat, Source: agentRoutineModuleID,
		At: time.Date(2026, time.November, 16, 8, 0, 0, 0, loc), Reference: string(encoded),
	}}, loc)
	body := strings.Join(lines, "\n")
	for _, name := range []string{"キン", "クロ", "シロ", "ミオ", "ギン", "アオ", "ミドリ"} {
		if !strings.Contains(body, name) {
			t.Errorf("conversation summary omitted recorded peer %q: %s", name, body)
		}
	}
	for hour, want := range map[int]string{0: "深夜", 4: "深夜", 5: "朝", 18: "夕方", 19: "夜", 23: "夜"} {
		at := time.Date(2026, time.November, 16, hour, 0, 0, 0, loc)
		if got := routineDiaryDaypart(at); got != want {
			t.Errorf("daypart(%02d:00) = %q, want %q", hour, got, want)
		}
	}
}

func TestRoutineDiaryConversationRetainsSameQuoteInDifferentTimeBands(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	transcript, err := json.Marshal([]routineDiaryTranscriptLine{
		{ActorID: "aka", Text: "おはよう。"}, {ActorID: "kin", Text: "今日はよろしく。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	day := func(hour, minute int) time.Time { return time.Date(2026, time.November, 16, hour, minute, 0, 0, loc) }
	facts := []agentroutine.Activity{
		{ActorID: "aka", Kind: agentroutine.ActivityMorningChat, At: day(8, 0), Source: agentRoutineModuleID, Reference: string(transcript)},
		{ActorID: "aka", Kind: agentroutine.ActivityMorningChat, At: day(8, 5), Source: agentRoutineModuleID, Reference: string(transcript)},
		{ActorID: "aka", Kind: agentroutine.ActivityMorningChat, At: day(20, 0), Source: agentRoutineModuleID, Reference: string(transcript)},
		{ActorID: "aka", Kind: agentroutine.ActivityMorningChat, At: day(20, 5), Source: agentRoutineModuleID, Reference: string(transcript)},
	}
	lines := routineDiaryConversationLines(facts, loc)
	joined := strings.Join(lines, "\n")
	if len(lines) != 2 || !strings.Contains(joined, "朝はキン") || !strings.Contains(joined, "夜はキン") {
		t.Fatalf("same peer quote across dayparts = %v; want one representative for both distinct periods", lines)
	}
}

func TestRoutineDiaryDeduplicatesRepeatedHobbyFactsWithinDaypart(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	base := time.Date(2026, time.November, 16, 12, 0, 0, 0, loc)
	facts := make([]agentroutine.Activity, 64)
	for i := range facts {
		facts[i] = agentroutine.Activity{
			Key: fmt.Sprintf("hobby-%02d", i), Kind: agentroutine.ActivityHobby, At: base.Add(time.Duration(i) * time.Minute),
			Source: "routine_hobby", Confirmed: true, Summary: "同じ内容の創作メモを作りました。",
		}
	}
	lines := routineDiaryWorkAndActivityLines(facts, loc)
	if len(lines) != 1 || !strings.Contains(lines[0], "同じ内容の創作メモ") {
		t.Fatalf("repeated hobby facts = %d lines: %v; want one representative within the same daypart", len(lines), lines)
	}
}

func TestRoutineDiaryWorkKeepsLatestRunOutcomeAndSeparateRetry(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	base := time.Date(2026, time.November, 16, 10, 0, 0, 0, loc)
	facts := []agentroutine.Activity{
		{Key: "run-1-start", Kind: agentroutine.ActivityWork, Source: "canonical_run", SourceID: "run-1", At: base, Summary: "作業を開始した"},
		{Key: "run-1-fail", Kind: agentroutine.ActivityWork, Source: "canonical_run", SourceID: "run-1", At: base.Add(5 * time.Minute), Summary: "作業が失敗した"},
		{Key: "run-2-start", Kind: agentroutine.ActivityWork, Source: "canonical_run", SourceID: "run-2", At: base.Add(time.Hour), Summary: "作業を開始した"},
		{Key: "run-2-done", Kind: agentroutine.ActivityWork, Source: "canonical_run", SourceID: "run-2", At: base.Add(90 * time.Minute), Summary: "作業を完了した"},
	}
	lines := routineDiaryWorkAndActivityLines(facts, loc)
	body := strings.Join(lines, "\n")
	if len(lines) != 2 || !strings.Contains(body, "作業が失敗しました") || !strings.Contains(body, "作業を完了しました") || strings.Contains(body, "作業を始めました") {
		t.Fatalf("work outcomes by run = %v; want failure then separate successful retry outcome", lines)
	}
}

func TestRoutineDiaryWorkPrefersTerminalStateOnEqualTimestampIndependentOfInputOrder(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	at := time.Date(2026, time.November, 16, 10, 0, 0, 0, loc)
	terminalSummaries := []string{
		"作業を完了した", "作業が失敗した", "作業を中止した", "作業が待機状態になった", "作業が停止状態になった", "作業が中断された",
	}
	for _, summary := range terminalSummaries {
		for _, reverse := range []bool{false, true} {
			name := summary
			if reverse {
				name += "-reverse"
			}
			t.Run(name, func(t *testing.T) {
				started := agentroutine.Activity{Key: "zz-run:started", Kind: agentroutine.ActivityWork, Source: "canonical_run", SourceID: "run-1", At: at, Summary: "作業を開始した"}
				terminal := agentroutine.Activity{Key: "aa-run:completed", Kind: agentroutine.ActivityWork, Source: "canonical_run", SourceID: "run-1", At: at, Summary: summary}
				facts := []agentroutine.Activity{started, terminal}
				if reverse {
					facts[0], facts[1] = facts[1], facts[0]
				}
				lines := routineDiaryWorkAndActivityLines(facts, loc)
				if len(lines) != 1 || !strings.Contains(lines[0], routineDiaryWorkAction(summary)) || strings.Contains(lines[0], "始めました") {
					t.Fatalf("equal-time state selection = %v; want terminal outcome %q", lines, summary)
				}
			})
		}
	}
}

func TestRoutineDiaryActivityUsesNaturalWorldGameAndMediaDescriptions(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	at := time.Date(2026, time.November, 16, 14, 0, 0, 0, loc)
	mediaReference, err := json.Marshal(routineMediaFactReference{
		ItemID: "clip-1", Kind: "video", StartMS: 65_250, EndMS: 77_250, ElapsedMS: 12_000,
		DurationMS: 180_000, StartedAt: at.Add(-12 * time.Second), FinishedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		fact agentroutine.Activity
		want string
	}{
		{name: "exercise start", fact: agentroutine.Activity{Kind: agentroutine.ActivityHobby, At: at, Source: "world_life_event", Summary: "WORLDで運動の開始を確認しました。"}, want: "14:00ごろ、運動を始めました。"},
		{name: "pose start", fact: agentroutine.Activity{Kind: agentroutine.ActivityHobby, At: at, Source: "world_life_event", Summary: "WORLDで鏡のポーズ「横向き」の練習開始を確認しました。"}, want: "14:00ごろ、鏡の前で「横向き」の練習を始めました。"},
		{name: "pose finish", fact: agentroutine.Activity{Kind: agentroutine.ActivityHobby, At: at, Source: "world_life_event", Summary: "WORLDで鏡のポーズ「横向き」の完了を確認しました。"}, want: "14:00ごろ、鏡の前で「横向き」のポーズを終えました。"},
		{name: "game session", fact: agentroutine.Activity{Kind: agentroutine.ActivityHobby, At: at, Source: "canonical_game_session", Summary: "GAMESでNetHackを2ターン遊んだ（初回 13:50、最終 14:00 JST）"}, want: "14:00ごろ、NetHackを2ターン（13:50〜14:00）遊びました"},
		{name: "media range", fact: agentroutine.Activity{Kind: agentroutine.ActivityHobby, At: at, Source: "routine_media_playback", SourceID: "intent-1:clip-1", Confirmed: true, Summary: "動画「作品タイトル」を合計12.0秒再生（出典: 公式）", Reference: string(mediaReference)}, want: "14:00ごろ、動画「作品タイトル」を合計12.0秒再生（出典: 公式）（再生範囲 00:01:05.250〜00:01:17.250）"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := routineDiaryActivityText(test.fact, loc)
			if got != test.want {
				t.Fatalf("routineDiaryActivityText = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadDiaryAmendmentDetectsLegacyBodyWithoutVersionBump(t *testing.T) {
	const actorID, date = "aka", "2026-11-16"
	runtime := newRoutineDiaryTestRuntime(t, nil)
	loc := runtime.location()
	runtime.now = func() time.Time { return time.Date(2026, time.November, 17, 10, 0, 0, 0, loc) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL: server.URL, TokenSource: func(context.Context) ([]byte, error) { return []byte("test-token"), nil },
		ExpectedConnection: boardcommunication.ConnectionIdentity{PrincipalID: "rencrow-comm", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	runtime.cfg = &config.Config{}
	runtime.deps.boardCommunicationActorBoots = map[string]*boardCommunicationBoot{
		actorID: {composition: &boardCommunicationComposition{boardClient: client}},
	}
	oldBody := date + " の記録\n\n記録の範囲: COREの確定記録と、認証済みWORLDイベント・canonical GAMES sessionで確認できた事実のみです。\n"
	old, err := runtime.store.SaveDiary(context.Background(), agentroutine.Diary{
		ActorID: actorID, Date: date, Body: oldBody, Reflection: "感想",
		ReflectionVersion: agentRoutineDiaryReflectionVersion, Revision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.store.SaveDiary(context.Background(), agentroutine.Diary{
		ActorID: actorID, Date: date, Body: old.Body, Reflection: old.Reflection,
		ReflectionVersion: old.ReflectionVersion, Revision: old.Revision, PostingKey: old.PostingKey,
		Receipt: "old-board-receipt", ReceiptRevision: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.tasks = nil
	changed, err := runtime.readDiaryAmendment(context.Background(), actorID, date)
	if err != nil || !changed {
		t.Fatalf("readDiaryAmendment legacy body with unavailable fact collectors = %t, %v; want cheap legacy detection", changed, err)
	}
	if old.PostingKey == "" {
		t.Fatal("saved diary has no stable posting identity")
	}
}

func TestReadDiaryAmendmentStillDetectsLateFactsForCurrentFormat(t *testing.T) {
	const actorID, date = "aka", "2026-11-16"
	runtime := newRoutineDiaryTestRuntime(t, nil)
	loc := runtime.location()
	runtime.now = func() time.Time { return time.Date(2026, time.November, 17, 10, 0, 0, 0, loc) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL: server.URL, TokenSource: func(context.Context) ([]byte, error) { return []byte("test-token"), nil },
		ExpectedConnection: boardcommunication.ConnectionIdentity{PrincipalID: "rencrow-comm", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	runtime.cfg = &config.Config{}
	runtime.deps.boardCommunicationActorBoots = map[string]*boardCommunicationBoot{
		actorID: {composition: &boardCommunicationComposition{boardClient: client}},
	}
	body, err := runtime.buildDiaryBody(context.Background(), actorID, date)
	if err != nil {
		t.Fatal(err)
	}
	if routineDiaryIsLegacyFormat(date, body) {
		t.Fatal("current renderer output was classified as legacy")
	}
	diary, err := runtime.store.SaveDiary(context.Background(), agentroutine.Diary{
		ActorID: actorID, Date: date, Body: body, Reflection: "感想",
		ReflectionVersion: agentRoutineDiaryReflectionVersion, Revision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.store.SaveDiary(context.Background(), agentroutine.Diary{
		ActorID: actorID, Date: date, Body: diary.Body, Reflection: diary.Reflection,
		ReflectionVersion: diary.ReflectionVersion, Revision: diary.Revision, PostingKey: diary.PostingKey,
		Receipt: "old-board-receipt", ReceiptRevision: "1",
	}); err != nil {
		t.Fatal(err)
	}
	fact := agentroutine.Activity{
		ActorID: actorID, Date: date, Key: "late-confirmed-fact", Kind: agentroutine.ActivityHobby,
		At: time.Date(2026, time.November, 16, 14, 0, 0, 0, loc), Source: agentRoutineModuleID,
		Confirmed: true, Summary: "後から確認された活動です。",
	}
	if _, err := runtime.store.SaveFact(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	changed, err := runtime.readDiaryAmendment(context.Background(), actorID, date)
	if err != nil || !changed {
		t.Fatalf("current-format late fact comparison = %t, %v; want body difference candidate", changed, err)
	}
}

func TestRoutineDiaryLegacyFormatRequiresExactDateAndHeaderPrefix(t *testing.T) {
	const date = "2026-11-16"
	legacy := date + " の記録\n\n" + routineDiaryLegacyHeader + "朝に起きました。\n"
	if !routineDiaryIsLegacyFormat(date, legacy) {
		t.Fatal("exact legacy date header was not recognized")
	}
	for _, body := range []string{
		"別の日 の記録\n\n" + routineDiaryLegacyHeader,
		date + " の記録\n追記\n" + routineDiaryLegacyHeader,
		date + " の記録\n\n本文中に" + routineDiaryLegacyHeader,
		date + " の記録\n\n記録の範囲: COREの確定記録",
	} {
		if routineDiaryIsLegacyFormat(date, body) {
			t.Errorf("malformed or nonprefix old wording recognized as legacy: %q", body)
		}
	}
}

func TestCheckDiaryAmendmentDoesNotCachePositiveDiscovery(t *testing.T) {
	const actorID, date = "aka", "2026-11-16"
	runtime := newRoutineDiaryTestRuntime(t, nil)
	loc := runtime.location()
	now := time.Date(2026, time.November, 17, 10, 0, 0, 0, loc)
	runtime.now = func() time.Time { return now }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL: server.URL, TokenSource: func(context.Context) ([]byte, error) { return []byte("test-token"), nil },
		ExpectedConnection: boardcommunication.ConnectionIdentity{PrincipalID: "rencrow-comm", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	runtime.cfg = &config.Config{}
	runtime.deps.boardCommunicationActorBoots = map[string]*boardCommunicationBoot{
		actorID: {composition: &boardCommunicationComposition{boardClient: client}},
	}
	currentBody, err := runtime.buildDiaryBody(context.Background(), actorID, date)
	if err != nil {
		t.Fatal(err)
	}
	old, err := runtime.store.SaveDiary(context.Background(), agentroutine.Diary{
		ActorID: actorID, Date: date, Body: currentBody + "\n追記前の別内容。", Reflection: "感想",
		ReflectionVersion: agentRoutineDiaryReflectionVersion, Revision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.store.SaveDiary(context.Background(), agentroutine.Diary{
		ActorID: actorID, Date: date, Body: old.Body, Reflection: old.Reflection,
		ReflectionVersion: old.ReflectionVersion, Revision: old.Revision, PostingKey: old.PostingKey,
		Receipt: "old-board-receipt", ReceiptRevision: "1",
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := runtime.checkDiaryAmendment(context.Background(), actorID, date)
	if err != nil || !changed {
		t.Fatalf("first amendment discovery = %t, %v; want positive discovery", changed, err)
	}
	tasks := runtime.tasks
	runtime.tasks = nil
	now = now.Add(time.Minute)
	changed, err = runtime.checkDiaryAmendment(context.Background(), actorID, date)
	if err == nil || changed {
		t.Fatalf("second check after positive discovery = %t, %v; want a fresh read that fails without collectors", changed, err)
	}
	runtime.tasks = tasks
	now = now.Add(time.Minute)
	changed, err = runtime.checkDiaryAmendment(context.Background(), actorID, date)
	if err != nil || !changed {
		t.Fatalf("rediscovery after transient revalidation error = %t, %v; want positive discovery again", changed, err)
	}
}

func TestCheckDiaryAmendmentCachesUnchangedBodyForThirtyMinutes(t *testing.T) {
	const actorID, date = "aka", "2026-11-16"
	runtime := newRoutineDiaryTestRuntime(t, nil)
	loc := runtime.location()
	now := time.Date(2026, time.November, 17, 10, 0, 0, 0, loc)
	runtime.now = func() time.Time { return now }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL: server.URL, TokenSource: func(context.Context) ([]byte, error) { return []byte("test-token"), nil },
		ExpectedConnection: boardcommunication.ConnectionIdentity{PrincipalID: "rencrow-comm", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	runtime.cfg = &config.Config{}
	runtime.deps.boardCommunicationActorBoots = map[string]*boardCommunicationBoot{
		actorID: {composition: &boardCommunicationComposition{boardClient: client}},
	}
	body, err := runtime.buildDiaryBody(context.Background(), actorID, date)
	if err != nil {
		t.Fatal(err)
	}
	diary, err := runtime.store.SaveDiary(context.Background(), agentroutine.Diary{
		ActorID: actorID, Date: date, Body: body, Reflection: "感想",
		ReflectionVersion: agentRoutineDiaryReflectionVersion, Revision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.store.SaveDiary(context.Background(), agentroutine.Diary{
		ActorID: actorID, Date: date, Body: diary.Body, Reflection: diary.Reflection,
		ReflectionVersion: diary.ReflectionVersion, Revision: diary.Revision, PostingKey: diary.PostingKey,
		Receipt: "old-board-receipt", ReceiptRevision: "1",
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := runtime.checkDiaryAmendment(context.Background(), actorID, date)
	if err != nil || changed {
		t.Fatalf("unchanged body check = %t, %v; want no change", changed, err)
	}
	runtime.tasks = nil
	now = now.Add(29 * time.Minute)
	changed, err = runtime.checkDiaryAmendment(context.Background(), actorID, date)
	if err != nil || changed {
		t.Fatalf("cached check at 29 minutes = %t, %v; want cached no change", changed, err)
	}
	now = now.Add(time.Minute)
	changed, err = runtime.checkDiaryAmendment(context.Background(), actorID, date)
	if err == nil || changed {
		t.Fatalf("check at cache expiry = %t, %v; want a fresh read that fails without collectors", changed, err)
	}
}
