package idlechat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

func TestAutomaticIdleChatWaitsForDailyEnrichmentCompletion(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 0, 2, 0.7, nil, "")
	jobCtx, cancel := context.WithCancel(o.ctx)
	defer cancel()
	o.mu.Lock()
	o.lastActivity = time.Now().Add(-time.Minute)
	o.dailyEnrichmentJob = &dailyEnrichmentJob{ctx: jobCtx, cancel: cancel, done: make(chan struct{})}
	o.mu.Unlock()

	o.checkAndStartChat()
	if o.IsChatActive() {
		t.Fatal("automatic IdleChat must not start while Daily enrichment is active")
	}
}

func attachPreparedWordDialogue(t *testing.T, o *IdleChatOrchestrator, topic, seedWord string, turns int) *queuedIdleChatCodexGenerator {
	t.Helper()
	stock := newWordTopicStock("")
	if !stock.push(WordPreparedTopic{
		Category:    TopicCategorySingle,
		Topic:       topic,
		Seed:        TopicSeed{Category: TopicCategorySingle, Genre1: seedWord},
		Axis:        "観察",
		OpeningHook: "店内の具体物から始める",
		Avoid:       "一般論だけで終わる",
	}) {
		t.Fatal("failed to prepare word topic")
	}
	o.wordTopicStock = stock
	utterances := []string{
		"店の赤い印を最初に見た人が黙った場面から、判断の難しさが見えるね。",
		"その赤い印を棚の記録と照合すれば、誰が判断を先送りしたか分かります。",
		"ただ、その記録だけでは夜の店員が迷った理由までは決められないね。",
		"一方、その迷いを引き継ぐ欄があれば、次の担当者は別の判断を選べます。",
		"その欄に時刻だけでなく店内の音も残すと、小さな違和感を拾えそう。",
		"今の音という手がかりなら、機械の警告と人の判断を分けて記録できます。",
		"その分け方で見ると、棚の前で止まった時間にも店員の意図が表れるね。",
		"ただ、その停止時間を責任追及だけに使うと、店で報告しにくくなります。",
		"その怖さを減らすには、失敗を責めず判断材料として共有する場面が要るね。",
		"一方、その共有に期限を設ければ、古い判断が店のルールとして残りません。",
		"その期限を越えた記録は消すのでなく、棚の変化と一緒に見直したいね。",
		"今の見直し方なら、赤い印は警告ではなく判断を更新する合図になります。",
	}
	if turns > len(utterances) {
		t.Fatalf("test dialogue turns=%d exceeds fixtures", turns)
	}
	prepared := make([]DialogueEpisodeTurn, 0, turns)
	for i := 0; i < turns; i++ {
		speaker := "mio"
		if i%2 == 1 {
			speaker = "shiro"
		}
		prepared = append(prepared, DialogueEpisodeTurn{Speaker: speaker, DisplayText: utterances[i], SpeechText: utterances[i]})
	}
	payload, err := json.Marshal(map[string]any{"turns": prepared})
	if err != nil {
		t.Fatal(err)
	}
	generator := &queuedIdleChatCodexGenerator{responses: []string{string(payload)}}
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = turns
	o.SetDialogueInterestingnessConfig(config)
	o.SetDialogueEpisodeService(NewPersistentDialogueEpisodeService("", generator, map[string]string{"mio": "Mio canonical", "shiro": "Shiro canonical"}, config))
	return generator
}

func TestRunChatSessionDoesNotSwitchTopicWithinSingleIdleSession(t *testing.T) {
	responses := []string{
		"一番面白かったのは、古書店の棚に残った手紙を同じ謎として追えた点です。二人が手紙の意味を少しずつ具体化したことで話が前に進みました。次は差出人の選択へ広げられます。",
		"QUALITY: pass\nBORING_CAUSE: 大きな損耗は検出されませんでした。\nINTEREST_HOOK: 古書店の棚に残った手紙\nMISSED_TURN: 手紙を誰が置いたかに絞る余地がありました。\nPROMPT_FIX: INTEREST_HOOKを一つ選び、場面・選択・秘密へ変換する。\nLENGTH_CONTROL: 2文以内。",
	}
	provider := &capturingIdleProvider{
		response:  "追加の話題へ切り替えないための既定応答です。",
		responses: responses,
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 4, 0.7, nil, "")
	dialogueGenerator := attachPreparedWordDialogue(t, o, "郵便と古書店に残る、宛先不明の手紙の扱い方", "郵便", 4)
	o.mu.Lock()
	o.chatActive = true
	o.beginIdleRunLocked()
	o.mu.Unlock()
	defer o.cancelIdleRun()

	o.runChatSession(StrategySingleGenre)
	if len(o.history) != 1 {
		t.Fatalf("history summaries = %d, want 1; records=%+v", len(o.history), o.history)
	}
	if strings.Contains(o.history[0].SessionID, "topic-01") {
		t.Fatalf("single idle session switched topic: %s", o.history[0].SessionID)
	}
	if got := countTopicGenerationRequests(provider.requests); got != 0 {
		t.Fatalf("Worker topic generation requests = %d, want 0", got)
	}
	if len(dialogueGenerator.requests) != 1 || !strings.Contains(dialogueGenerator.requests[0], "店内の具体物から始める") || !strings.Contains(dialogueGenerator.requests[0], "一般論だけで終わる") {
		t.Fatalf("topic guidance was not injected into CodexExe dialogue prompt: %+v", dialogueGenerator.requests)
	}
}

func TestRunChatSessionPlaysValidatedEpisodeToTurnLimit(t *testing.T) {
	responses := []string{
		"一番面白かったのは、映画館に残った鍵を最後まで同じ話題として追えた点です。二人が客席と映写機の手がかりを順に重ねました。",
		"QUALITY: pass\nBORING_CAUSE: 大きな損耗は検出されませんでした。\nINTEREST_HOOK: 映画館に残った鍵\nMISSED_TURN: なし\nPROMPT_FIX: \nLENGTH_CONTROL: 2文以内。",
	}
	provider := &capturingIdleProvider{
		response:  "もし鍵が古い映写機を開ける合図だったら、二人は暗い客席で同じ場面をもう一度見ることになります。",
		responses: responses,
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, maxTurnsPerTopic, 0.7, nil, "")
	attachPreparedWordDialogue(t, o, "映画館に残った鍵の使い道", "鍵", maxTurnsPerTopic)
	o.mu.Lock()
	o.chatActive = true
	o.beginIdleRunLocked()
	o.mu.Unlock()
	defer o.cancelIdleRun()

	o.runChatSession(StrategySingleGenre)

	if len(o.history) != 1 {
		t.Fatalf("history summaries = %d, want 1; records=%+v", len(o.history), o.history)
	}
	if got := o.history[0].Turns; got != maxTurnsPerTopic {
		t.Fatalf("turns = %d, want %d", got, maxTurnsPerTopic)
	}
	if o.history[0].LoopRestarted {
		t.Fatalf("validated episode should not be marked as restarted: reason=%q", o.history[0].LoopReason)
	}
}

func TestRunChatSessionRecordsGenerationErrorInConversationHistory(t *testing.T) {
	provider := &capturingIdleProvider{
		responses: []string{
			topicCandidatesJSON("郵便と古書店に残る、宛先不明の手紙の扱い方", "観察"),
			topicJudgeJSON("郵便と古書店に残る、宛先不明の手紙の扱い方"),
			"",
			"",
			"",
		},
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	o.mu.Lock()
	o.chatActive = true
	o.beginIdleRunLocked()
	o.mu.Unlock()
	defer o.cancelIdleRun()

	o.runChatSession(StrategySingleGenre)

	entries := o.memory.GetUnifiedView(20)
	found := false
	for _, entry := range entries {
		if strings.Contains(entry.Message.Content, "生成エラー") {
			found = true
			if !strings.Contains(entry.Message.Content, "応答生成に失敗") {
				t.Fatalf("generation error history is not explicit: %q", entry.Message.Content)
			}
		}
	}
	if !found {
		t.Fatalf("generation error was not recorded in conversation history: %+v", entries)
	}
}

func TestActiveSessionTranscriptReturnsCurrentIdleSessionInOrder(t *testing.T) {
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(nil, memory, []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	o.mu.Lock()
	o.activeSessionID = "idle-current"
	o.mu.Unlock()

	old := domaintransport.NewMessage("shiro", "mio", "idle-old", "", "古いセッションです。")
	old.Type = domaintransport.MessageTypeIdleChat
	memory.RecordMessage(old)
	first := domaintransport.NewMessage("mio", "shiro", "idle-current", "", "最初はMioです。")
	first.Type = domaintransport.MessageTypeIdleChat
	first.Context = map[string]interface{}{"message_id": "idle-current:msg:0001", "turn_index": 1}
	memory.RecordMessage(first)
	second := domaintransport.NewMessage("shiro", "mio", "idle-current", "", "次はShiroです。")
	second.Type = domaintransport.MessageTypeIdleChat
	second.Context = map[string]interface{}{"message_id": "idle-current:msg:0002", "turn_index": 2}
	memory.RecordMessage(second)

	sessionID, transcript := o.ActiveSessionTranscript(10)
	if sessionID != "idle-current" {
		t.Fatalf("sessionID = %q, want idle-current", sessionID)
	}
	if len(transcript) != 2 {
		t.Fatalf("transcript len = %d, want 2: %+v", len(transcript), transcript)
	}
	if transcript[0].From != "mio" || transcript[0].Content != "最初はMioです。" {
		t.Fatalf("first transcript entry = %+v", transcript[0])
	}
	if transcript[0].TurnIndex != 1 || transcript[0].MessageID != "idle-current:msg:0001" {
		t.Fatalf("first transcript identity = %+v", transcript[0])
	}
	if transcript[1].From != "shiro" || transcript[1].Content != "次はShiroです。" {
		t.Fatalf("second transcript entry = %+v", transcript[1])
	}
	if transcript[1].TurnIndex != 2 || transcript[1].MessageID != "idle-current:msg:0002" {
		t.Fatalf("second transcript identity = %+v", transcript[1])
	}
}

func TestEmitTopicUsesTopicEventOutsideConversationTurns(t *testing.T) {
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(nil, memory, []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	var emitted []TimelineEvent
	o.SetEventEmitter(func(ev TimelineEvent) <-chan struct{} {
		emitted = append(emitted, ev)
		return nil
	})

	o.emitTopicToTimeline("idle-topic-contract", "記憶と風景の関係", StrategyExternalStimulus)

	if len(emitted) != 1 {
		t.Fatalf("emitted len = %d, want 1", len(emitted))
	}
	if emitted[0].Type != "idlechat.topic" {
		t.Fatalf("topic event type = %q, want idlechat.topic", emitted[0].Type)
	}
	if !strings.HasPrefix(emitted[0].MessageID, "msg_") || emitted[0].TurnIndex != 0 {
		t.Fatalf("topic identity = %+v", emitted[0])
	}
	if emitted[0].Category != TopicCategoryExternal || emitted[0].Strategy != StrategyExternalStimulus {
		t.Fatalf("topic trace fields = category=%q strategy=%q", emitted[0].Category, emitted[0].Strategy)
	}
	o.mu.Lock()
	o.activeSessionID = "idle-topic-contract"
	o.mu.Unlock()
	_, transcript := o.ActiveSessionTranscript(10)
	if len(transcript) != 0 {
		t.Fatalf("topic should not be included in active transcript: %+v", transcript)
	}
}

func countTopicGenerationRequests(requests []llm.GenerateRequest) int {
	count := 0
	for _, req := range requests {
		if len(req.Messages) > 0 && req.Messages[0].Role == "system" && req.Messages[0].Content == topicGeneratorSystemPrompt() {
			count++
		}
	}
	return count
}

func topicCandidatesJSON(topic, axis string) string {
	return fmt.Sprintf(`{"candidates":[{"topic":%q,"interestingness_axis":%q,"opening_hook":"最初に具体物の扱いを拾う","avoid":"抽象論だけで終わらせない","rationale":"二人の見方が分かれる"}]}`, topic, axis)
}

func topicJudgeJSON(topic string) string {
	return fmt.Sprintf(`{"winner_topic":%q,"scores":[{"topic":%q,"category_fit":5,"concreteness":5,"curiosity":5,"conversation_potential":5,"axis_strength":5,"novelty":5,"safety":5,"present_day_relevance":5,"total":40,"reason":"会話が続く"}],"reject_reason_summary":""}`, topic, topic)
}

func containsRequestSystemPrompt(requests []llm.GenerateRequest, text string) bool {
	for _, req := range requests {
		if len(req.Messages) == 0 {
			continue
		}
		if req.Messages[0].Role == "system" && strings.Contains(req.Messages[0].Content, text) {
			return true
		}
	}
	return false
}
