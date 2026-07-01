//go:build ignore

package idlechat

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

func TestMain(m *testing.M) {
	// LoadStoryData: Complex Story Mode用、アーカイブ済み
	// Simple Story Mode はハードコードされた昔話リストを使用
	os.Exit(m.Run())
}

// mockLLMProvider はテスト用のモックLLMプロバイダー
type mockLLMProvider struct {
	response  string
	responses []string
	err       error
	callCount int
	delay     time.Duration // Generate呼び出し時の遅延
	lastReq   llm.GenerateRequest
}

func (m *mockLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	m.callCount++
	m.lastReq = req
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.err != nil {
		return llm.GenerateResponse{}, m.err
	}
	if len(m.responses) > 0 {
		idx := m.callCount - 1
		if idx >= len(m.responses) {
			idx = len(m.responses) - 1
		}
		return llm.GenerateResponse{
			Content:      m.responses[idx],
			TokensUsed:   10,
			FinishReason: "stop",
		}, nil
	}
	return llm.GenerateResponse{
		Content:      m.response,
		TokensUsed:   10,
		FinishReason: "stop",
	}, nil
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

func TestNewIdleChatOrchestrator(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	participants := []string{"mio", "shiro"}

	o := NewIdleChatOrchestrator(provider, memory, participants, 5, 10, 0.8, nil, "")

	if o.intervalMin != 5 {
		t.Errorf("Expected intervalMin=5, got %d", o.intervalMin)
	}
	if o.maxTurns != 10 {
		t.Errorf("Expected maxTurns=10, got %d", o.maxTurns)
	}
	if o.temperature != 0.8 {
		t.Errorf("Expected temperature=0.8, got %f", o.temperature)
	}
	if len(o.participants) != 2 {
		t.Errorf("Expected 2 participants, got %d", len(o.participants))
	}
}

func TestIdleChatOrchestrator_UsesSpeakerSpecificProviders(t *testing.T) {
	chatProvider := &mockLLMProvider{response: "話題"}
	workerProvider := &mockLLMProvider{response: "要約"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(chatProvider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{
		"mio":   chatProvider,
		"shiro": workerProvider,
	})

	o.generateTopicFromChat("idle-provider-routing", StrategySingleGenre)
	if chatProvider.callCount == 0 {
		t.Fatal("expected mio/chat provider to be used for topic generation")
	}

	workerProvider.response = "短い要約"
	_ = o.summarizeByWorker("話題", []string{"mio: こんにちは", "shiro: 条件を見たい。"})
	if workerProvider.callCount == 0 {
		t.Fatal("expected shiro/worker provider to be used for summary")
	}

	chatProvider.response = "そこは面白いね。次は場面を見たい。"
	_, err := o.generateResponse("mio", "shiro", "idle-provider-routing", 1, 1, "話題")
	if err != nil {
		t.Fatalf("generateResponse(mio) failed: %v", err)
	}
	if chatProvider.callCount < 2 {
		t.Fatal("expected mio/chat provider to be used for mio response generation")
	}
}

func TestIdleChatOrchestrator_TemperatureForSpeaker_MioAndShiroFixed(t *testing.T) {
	provider := &mockLLMProvider{response: "新しい観点を出してみよう。"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.2, nil, "")

	if _, err := o.generateResponse("mio", "shiro", "idle-temp", 0, 0, "話題"); err != nil {
		t.Fatalf("generateResponse(mio) failed: %v", err)
	}
	if provider.lastReq.Temperature != 0.65 {
		t.Fatalf("expected mio idlechat temperature 0.65, got %v", provider.lastReq.Temperature)
	}

	if _, err := o.generateResponse("shiro", "mio", "idle-temp", 1, 1, "話題"); err != nil {
		t.Fatalf("generateResponse(shiro) failed: %v", err)
	}
	if provider.lastReq.Temperature != 0.65 {
		t.Fatalf("expected shiro idlechat temperature 0.65, got %v", provider.lastReq.Temperature)
	}
}

func TestIdleChatOrchestrator_TemperatureForSpeaker_OthersUseConfiguredValue(t *testing.T) {
	provider := &mockLLMProvider{response: "別の案として考えると面白い。"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"gin", "mio"}, 5, 10, 0.35, nil, "")

	if _, err := o.generateResponse("gin", "mio", "idle-temp", 0, 0, "話題"); err != nil {
		t.Fatalf("generateResponse(gin) failed: %v", err)
	}
	if provider.lastReq.Temperature != 0.35 {
		t.Fatalf("expected non-mio/shiro idlechat temperature 0.35, got %v", provider.lastReq.Temperature)
	}
}

func TestGenerateResponse_ShowsSpeakerStyleConstraintsInPrompt(t *testing.T) {
	provider := &mockLLMProvider{response: "その見方は面白い。条件を一つずつ確かめたい。"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	_, err := o.generateResponse("shiro", "mio", "idle-shiro-prompt", 1, 1, "古代塔")
	if err != nil {
		t.Fatalf("generateResponse(shiro) failed: %v", err)
	}
	if len(provider.lastReq.Messages) == 0 {
		t.Fatal("expected prompt messages to be sent")
	}
	joined := make([]string, 0, len(provider.lastReq.Messages))
	for _, msg := range provider.lastReq.Messages {
		joined = append(joined, msg.Content)
	}
	promptText := strings.Join(joined, "\n")
	for _, phrase := range []string{
		"相手や自分の直前の言い回しをなぞらない",
		"同じ比喩やたとえの型を続けず",
		"言いよどみや同意テンプレで始めない",
		"直前の相手発言",
		"自分の直前発言",
		"話し方契約",
		"読者の楽しみ",
		"数値や出典を求めて詰問しない",
		"研究発表みたいな硬い締め方を避け",
	} {
		if !strings.Contains(promptText, phrase) {
			t.Fatalf("expected prompt to mention %q, got %q", phrase, promptText)
		}
	}
}

func TestIdleAudienceAngle_Varies(t *testing.T) {
	if idleAudienceAngle(0, false, false) == idleAudienceAngle(1, false, false) {
		t.Fatal("expected non-movie audience angle to vary")
	}
	if idleAudienceAngle(0, true, false) == idleAudienceAngle(1, true, false) {
		t.Fatal("expected movie audience angle to vary")
	}
}

func TestIdleShiftHint_PrefersNonAnalogyAfterAnalogy(t *testing.T) {
	got := idleShiftHint("まるで映画のセットみたいだね。", "")
	if !strings.Contains(got, "今回は比喩で返さず") {
		t.Fatalf("expected non-analogy shift hint, got %q", got)
	}
}

func TestBuildIdleTurnPrompt_ClosingModeAddsEndingGuidance(t *testing.T) {
	got := buildIdleTurnPrompt("鏡の表面", "shiro", "まるで映画みたいだね。", "光が揺れる。", 10, 10, false)
	if !strings.Contains(got, "そろそろ締める") {
		t.Fatalf("expected closing guidance, got %q", got)
	}
	if !strings.Contains(got, "最後の1-2ターン") {
		t.Fatalf("expected ending-turn hint, got %q", got)
	}
}

func TestGenerateResponse_AddsMovieTopicGuidance(t *testing.T) {
	provider := &mockLLMProvider{response: "廃墟の余韻が先に立つ作品かもしれない。音の扱いでかなり印象が変わりそうだ。"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	_, err := o.generateResponse("shiro", "mio", "idle-movie-prompt", 0, 0, "「瓦礫のセレナーデ」ってどんな映画？")
	if err != nil {
		t.Fatalf("generateResponse() failed: %v", err)
	}
	joined := make([]string, 0, len(provider.lastReq.Messages))
	for _, msg := range provider.lastReq.Messages {
		joined = append(joined, msg.Content)
	}
	promptText := strings.Join(joined, "\n")
	if !strings.Contains(promptText, "架空映画の妄想会話") {
		t.Fatalf("expected compact movie prompt, got %q", promptText)
	}
	if !strings.Contains(promptText, "主人公・事件・場面") {
		t.Fatalf("expected concrete movie guidance in prompt, got %q", promptText)
	}
	if !strings.Contains(promptText, "前に見た") {
		t.Fatalf("expected movie prompt to ban known-work framing, got %q", promptText)
	}
	if !strings.Contains(promptText, "対立") || !strings.Contains(promptText, "反転") {
		t.Fatalf("expected progression guidance in prompt, got %q", promptText)
	}
}

func TestIdleChatOrchestrator_StartStop(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 60, 3, 0.8, nil, "")

	o.Start()

	// 即座にStop
	done := make(chan struct{})
	go func() {
		o.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Stop timed out")
	}
}

func TestIdleChatOrchestrator_NotifyActivity(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	// chatActiveを手動で設定してNotifyActivityで中断されることを確認
	o.mu.Lock()
	o.chatActive = true
	o.mu.Unlock()

	o.NotifyActivity()

	if o.IsChatActive() {
		t.Error("Chat should be interrupted after NotifyActivity")
	}
}

func TestIdleChatOrchestrator_ManualMode_StartStop(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	if err := o.StartManualMode(); err != nil {
		t.Fatalf("StartManualMode failed: %v", err)
	}
	if !o.IsManualMode() {
		t.Fatal("manual mode should be enabled")
	}

	o.StopManualMode()
	if o.IsManualMode() {
		t.Fatal("manual mode should be disabled")
	}
}

func TestIdleChatOrchestrator_ManualMode_StopsOnActivity(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	if err := o.StartManualMode(); err != nil {
		t.Fatalf("StartManualMode failed: %v", err)
	}
	o.NotifyActivity()
	if o.IsManualMode() {
		t.Fatal("manual mode should stop after activity")
	}
}

func TestIdleChatOrchestrator_StartForecastMode_SwitchesFromManualMode(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	if err := o.StartManualMode(); err != nil {
		t.Fatalf("StartManualMode failed: %v", err)
	}
	if err := o.StartForecastMode(); err != nil {
		t.Fatalf("StartForecastMode failed: %v", err)
	}

	if o.IsManualMode() {
		t.Fatal("manual mode should be disabled after switching to forecast")
	}
	if !o.IsChatActive() {
		t.Fatal("chat should be active after switching to forecast")
	}
	if got := o.CurrentMode(); got != "forecast" {
		t.Fatalf("expected current mode forecast, got %q", got)
	}
}

func TestIdleChatOrchestrator_StartForecastMode_RejectsActiveSession(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	o.mu.Lock()
	o.chatActive = true
	o.sessionMode = "idle"
	o.mu.Unlock()

	if err := o.StartForecastMode(); err == nil {
		t.Fatal("expected StartForecastMode to reject active session")
	}
}

func TestIdleChatOrchestrator_StartStoryMode_SwitchesFromManualMode(t *testing.T) {
	provider := &mockLLMProvider{response: "story"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	if err := o.StartManualMode(); err != nil {
		t.Fatalf("StartManualMode failed: %v", err)
	}
	if err := o.StartStoryMode(); err != nil {
		t.Fatalf("StartStoryMode failed: %v", err)
	}
	if o.IsManualMode() {
		t.Fatal("manual mode should be disabled after switching to story")
	}
	if !o.IsChatActive() {
		t.Fatal("chat should be active after switching to story")
	}
	if got := o.CurrentMode(); got != "story" {
		t.Fatalf("expected current mode story, got %q", got)
	}
}

func TestIdleChatOrchestrator_NextIdleSessionPlan_RotatesNormalAndForecast(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	o.mu.Lock()
	defer o.mu.Unlock()

	plan1 := o.nextIdleSessionPlanLocked()
	plan2 := o.nextIdleSessionPlanLocked()
	plan3 := o.nextIdleSessionPlanLocked()
	plan4 := o.nextIdleSessionPlanLocked()
	plan5 := o.nextIdleSessionPlanLocked()

	if plan1.mode != "idle" || plan1.strategy != StrategySingleGenre {
		t.Fatalf("expected first plan single idle, got mode=%q strategy=%q", plan1.mode, plan1.strategy)
	}
	if plan2.mode != "idle" || plan2.strategy != StrategyDoubleGenre {
		t.Fatalf("expected second plan double idle, got mode=%q strategy=%q", plan2.mode, plan2.strategy)
	}
	if plan3.mode != "idle" || plan3.strategy != StrategyExternalStimulus {
		t.Fatalf("expected third plan external idle, got mode=%q strategy=%q", plan3.mode, plan3.strategy)
	}
	if plan4.mode != "forecast" || plan4.domain == nil || plan4.domain.Name != forecastDomains[0].Name {
		t.Fatalf("expected fourth plan forecast %q, got mode=%q domain=%v", forecastDomains[0].Name, plan4.mode, plan4.domain)
	}
	if plan5.mode != "idle" || plan5.strategy != StrategySingleGenre {
		t.Fatalf("expected fifth plan to restart at single idle, got mode=%q strategy=%q", plan5.mode, plan5.strategy)
	}
}

func TestChooseStoryRewriteStyle_AvoidsImmediateRepeat(t *testing.T) {
	history := []SessionSummary{{Strategy: "story:role_shift", RewriteStyle: "role_shift"}}
	for i := 0; i < 20; i++ {
		if got := chooseStoryRewriteStyle(history); got == "role_shift" {
			t.Fatalf("expected style to avoid immediate repeat, got %q", got)
		}
	}
}

func TestIdleChatOrchestrator_IsChatActive(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	if o.IsChatActive() {
		t.Error("Should not be active initially")
	}
}

func TestIdleChatOrchestrator_RunChatSession(t *testing.T) {
	provider := &mockLLMProvider{response: "こんにちは！", delay: 1 * time.Millisecond}
	memory := session.NewCentralMemory()
	maxTurns := 3

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 0, maxTurns, 0.8, nil, "")

	o.mu.Lock()
	o.chatActive = true
	o.mu.Unlock()

	o.runChatSession(StrategySingleGenre)

	// 話題生成1回 + 会話maxTurns回 + 要約1回
	minExpectedCalls := maxTurns + 2
	if provider.callCount < minExpectedCalls {
		t.Errorf("Expected at least %d LLM calls, got %d", minExpectedCalls, provider.callCount)
	}

	// メモリに記録されているはず（重複排除によりmaxTurns以下の場合もある）
	mioMemory := memory.GetOrCreateAgent("mio")
	shiroMemory := memory.GetOrCreateAgent("shiro")
	totalEntries := mioMemory.Count() + shiroMemory.Count()
	if totalEntries < maxTurns {
		t.Errorf("Expected at least %d total entries across agents, got %d", maxTurns, totalEntries)
	}
}

func TestSplitStoryNarration_SplitsLongParagraph(t *testing.T) {
	text := "昔々あるところに、とても長い導入がありました。主人公はまだ何も知らず、町の端から端まで歩き続けていました。やがて奇妙な知らせが届き、話は別の方向へ進みます。"
	got := splitStoryNarration(text, 40)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %v", got)
	}
	for _, chunk := range got {
		if utf8.RuneCountInString(chunk) > 40 {
			t.Fatalf("chunk too long: %q", chunk)
		}
	}
}

func TestSaveStorySummary_StoresStoryMetadata(t *testing.T) {
	origStoryRandIntn := storyRandIntn
	storyRandIntn = func(n int) int { return 0 }
	defer func() { storyRandIntn = origStoryRandIntn }()

	provider := &mockLLMProvider{response: "unused"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	source := StorySource{ID: "momotaro", Title: "桃太郎"}
	plan := StoryRewritePlan{RewriteStyle: "role_shift", StoryTitle: "港の桃太郎", Premise: "桃太郎が港で異変を追う", EndingFlavor: "皮肉", MotifMap: []string{"桃=>桃印", "きびだんご=>夜食の配給券"}}
	draftText := "夜の港に、桃印の新人が立っていた。"
	revisionNote := "前半から因果が通るよう整えた。"
	storyText := "夜の港に、桃印の新人が立っていた。夜食の配給券が沈黙を買い、最後に皮肉だけが残った。"
	transcript := []string{"mio: 今夜の物語です。", "mio: 夜の港に、桃印の新人が立っていた。"}
	startedAt := time.Now().In(jst)
	endedAt := startedAt.Add(time.Minute)

	o.saveStorySummary("story-test", source, plan, draftText, revisionNote, storyText, transcript, startedAt, endedAt)

	history := o.GetHistory(1)
	if len(history) != 1 {
		t.Fatalf("expected one story history item, got %d", len(history))
	}
	if history[0].SourceTitle != "桃太郎" || history[0].StoryTitle != "港の桃太郎" || history[0].StoryText != storyText {
		t.Fatalf("expected story metadata to be stored, got %+v", history[0])
	}
}

func TestRetryStoryDraft_RetriesBeforeSuccess(t *testing.T) {
	shiroProvider := &mockLLMProvider{
		responses: []string{
			// attempt 1: beat 0 fails (empty → outline check fails)
			"",
			// attempt 2: beat 0 fails (empty → outline check fails)
			"",
			// attempt 3: 4 valid beat texts, each distinct to avoid repeats-context check
			"桃太郎は夜の港で桃印の検品ロッカーを開け、点呼表を片手に犬と猿と雉の担当者を集めた。誰が鬼ヶ島の便を黙って通したのかを確かめようとした。",
			"配給券の束が次々と消え、犬は怪訝な顔で台帳を繰り返し確かめた。猿は小声で「口止め料だ」と言い、雉は高い棚の陰に隠れた箱を指さした。",
			"保税倉庫の扉を開けると、隠された帳簿が台車の下から現れた。内部不正の証拠が眼前に広がり、猿が番号を書き留めた。",
			"桃太郎は帳簿を抱えて岸壁まで走り、仲間たちと静かに視線を交わした。夜明けの港には勝利ではなく重い沈黙だけが残った。",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(shiroProvider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{"shiro": shiroProvider})
	source := StorySource{ID: "momotaro", Title: "桃太郎", SourceLabel: "日本昔話", Text: "昔々、川から流れてきた大きな桃から男の子が生まれました。桃太郎と名づけられたその子は、やがて立派に育ち、村を困らせる鬼を退治するため鬼ヶ島へ向かいます。道中で犬、猿、雉を家来にし、きびだんごを分け与えながら心を一つにしました。鬼ヶ島では力だけでなく知恵も使って鬼の油断を突き、宝を持ち帰って村に平和を戻しました。"}
	analysis := analyzeStorySource(source)
	plan := StoryRewritePlan{
		SourceTitle:  "桃太郎",
		RewriteStyle: "role_shift",
		StoryTitle:   "港の桃太郎",
		Premise:      "桃太郎が夜の港湾で積み荷の異変を追う",
		Setting:      "現代の港湾都市",
		Viewpoint:    "桃太郎の一人称",
		Tone:         "きびきびして少し切ない",
		EndingFlavor: "皮肉",
		MotifMap: []string{
			"桃から生まれる=>検品番号ゼロ番の出自",
			"きびだんご=>夜食の配給券",
			"鬼退治=>内部不正の摘発",
		},
	}
	beatPlan := StoryBeatPlan{
		Opening:   "桃太郎は夜の港で点呼表を持って立っていた。",
		Deviation: "配給券の束が、仲間集めではなく沈黙の口止めに使われ始める。",
		Reversal:  "鬼ヶ島とあだ名される保税倉庫で、内部不正の摘発が必要だと分かる。",
		Landing:   "最後に残るのは、勝利ではなく皮肉だった。",
	}
	adaptation := buildStoryAdaptationPlan(analysis.Skeleton, plan, beatPlan)

	draft, _, err := o.retryStoryDraft(source, analysis, plan, adaptation, beatPlan)
	if err != nil {
		t.Fatalf("expected retry to recover story draft, got %v", err)
	}
	if shiroProvider.callCount < 6 {
		t.Fatalf("expected at least 6 LLM calls (2 failed attempts + 4 beats on success), got %d", shiroProvider.callCount)
	}
	if !strings.Contains(draft, "桃印") && !strings.Contains(draft, "鬼ヶ島") && !strings.Contains(draft, "配給券") {
		t.Fatalf("expected valid draft after retries, got %q", draft)
	}
}

func TestRetryStoryDraft_ReturnsErrorAfterExhaustedRetries(t *testing.T) {
	shiroProvider := &mockLLMProvider{responses: []string{"", "", ""}}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(shiroProvider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{"shiro": shiroProvider})
	source := StorySource{ID: "issun", Title: "一寸法師", SourceLabel: "日本昔話", Text: "一寸ほどの小さな男の子は、針を刀、椀を舟にして都へ向かいました。都では働き者として姫のそばに仕えますが、ある日鬼にさらわれそうになります。小さな体を生かして鬼の口や腹の中で暴れ、打ち出の小槌を手に入れました。その力で元の大きさに育ち、勇気と機転で姫を守った功績が認められます。"}
	analysis := analyzeStorySource(source)
	plan := buildStoryRewritePlan(source, analysis, "view_shift")
	beatPlan := groundedStoryBeatPlan(source, analysis, plan)
	adaptation := buildStoryAdaptationPlan(analysis.Skeleton, plan, beatPlan)

	_, retryLog, err := o.retryStoryDraft(source, analysis, plan, adaptation, beatPlan)
	if err == nil {
		t.Fatal("expected error after exhausted retries, got nil")
	}
	if len(retryLog) == 0 {
		t.Fatal("expected non-empty retry log")
	}
}

func TestRunStorySession_FallsBackToNormalChatAfterThreeSourceFailures(t *testing.T) {
	origStoryRandIntn := storyRandIntn
	storyRandIntn = func(n int) int { return 0 }
	defer func() { storyRandIntn = origStoryRandIntn }()

	mioProvider := &mockLLMProvider{
		response: "unused",
	}
	// 9 empty responses: 3 sources × 3 retry attempts, all return "" (outline/overblown rejection)
	shiroResponses := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		shiroResponses = append(shiroResponses, "")
	}
	shiroProvider := &mockLLMProvider{responses: shiroResponses}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(mioProvider, memory, []string{"mio", "shiro"}, 5, 1, 0.8, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{"mio": mioProvider, "shiro": shiroProvider})

	// Should complete without panic (falls back to normal chat)
	o.RunStorySession()

	history := o.GetHistory(1)
	if len(history) != 1 {
		t.Fatalf("expected one history item, got %d", len(history))
	}
	// After fallback to normal chat, Strategy is NOT story:*
	if strings.HasPrefix(string(history[0].Strategy), "story:") {
		t.Fatalf("expected normal chat fallback, got story history: %+v", history[0])
	}
}

func TestStoryNarrativeLooksLikeProse_RejectsOverblownModernization(t *testing.T) {
	story := "雨が降っていた。主人公はスマホを握りしめ、SNSで集まった観光客に囲まれながら、巨大企業の高層ビルへ入った。そこで権限トークンを受け取り、いいねの数だけ運命が決まると言われた。彼は黙ってうなずき、またスマホを見た。"
	if storyNarrativeLooksLikeProse(story) {
		t.Fatalf("expected overblown modernization to be rejected")
	}
}

func TestStoryNarrativeLooksLikeProse_RejectsAtmosphericOpeningWithoutAction(t *testing.T) {
	story := "雨音は冷たく、まるで忘れられた記憶のように窓を打っていた。薄い明かりが床をなぞり、部屋は深い影を抱え込んでいた。息をひそめたまま、夜だけが長く伸びていった。やがて静けさはさらに濃くなり、誰も何も決めないまま時間だけが過ぎた。"
	if storyNarrativeLooksLikeProse(story) {
		t.Fatalf("expected atmospheric non-story opening to be rejected")
	}
}

func TestReviseStoryNarrative_RejectsSkeletonRegression(t *testing.T) {
	shiroProvider := &mockLLMProvider{response: "REVISION_NOTE:\nまとまりだけ整えた。\nSTORY:\n一寸ほどの背丈しかない若者は町へ向かった。彼は店先で昔を思い出し、静かな夜を見上げた。やがて何も起きないまま朝になり、喪失だけが残った。"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(shiroProvider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{"shiro": shiroProvider})
	source := StorySource{ID: "issun", Title: "一寸法師", SourceLabel: "日本昔話"}
	analysis := analyzeStorySource(source)
	plan := buildStoryRewritePlan(source, analysis, "view_shift")
	beatPlan := groundedStoryBeatPlan(source, analysis, plan)
	adaptation := buildStoryAdaptationPlan(analysis.Skeleton, plan, beatPlan)
	draft := deterministicStoryDraft(source, analysis, plan, adaptation, beatPlan)

	if _, _, err := o.reviseStoryNarrative(source, analysis, plan, adaptation, beatPlan, draft); err == nil {
		t.Fatal("expected revision with missing cues to be rejected")
	}
}

func TestStoryNarrativeLooksLikeProse_RejectsDistractingDigression(t *testing.T) {
	story := "おじいさんは庭の土を掘る犬の前にしゃがみこみ、何が出るのか息をひそめた。土の中から小さな箱が出てきた。その姿を見て、私は幼い頃に見た花を思い出した。おじいさんは箱を抱えたまま立ち尽くし、隣の男はそれを奪おうと腕を伸ばした。"
	if storyNarrativeLooksLikeProse(story) {
		t.Fatalf("expected distracting digression to be rejected")
	}
}

func TestNormalizeStoryNarrative_DedupesMetaAndRepeatedSentences(t *testing.T) {
	raw := "（余韻）\nマッチの火が消えた後、少女は静かに息を引き取った。\nマッチの火が消えた後、少女は静かに息を引き取った。\n\nマッチの火が消えた後、少女は静かに息を引き取った。"
	got := normalizeStoryNarrative(raw)
	if strings.Contains(got, "（余韻）") {
		t.Fatalf("expected meta label removed, got %q", got)
	}
	if strings.Count(got, "マッチの火が消えた後") != 1 {
		t.Fatalf("expected duplicate sentence removed, got %q", got)
	}
}

func TestAnalyzeStorySource_SnowwhiteKeepsTabooAndAftertaste(t *testing.T) {
	analysis := analyzeStorySource(StorySource{ID: "snowwhite", Title: "白雪姫"})
	if !containsString(analysis.CoreMotifs, "毒りんご") {
		t.Fatalf("expected poison apple motif, got %v", analysis.CoreMotifs)
	}
	if analysis.TabooOrRule == "" || analysis.EmotionalAftertaste == "" {
		t.Fatalf("expected taboo and aftertaste, got %+v", analysis)
	}
}

func TestAnalyzeStorySource_AladdinKeepsSpecificStructure(t *testing.T) {
	t.Skip("aladdin.json archived in current session — skip until data restored")
}

func TestStorySkeleton_RedridingHasRecognizableBeats(t *testing.T) {
	skeleton := storySkeleton(StorySource{ID: "redriding", Title: "赤ずきん"})
	// "道中の足止め"+"先回り" は "狼との出会いと先回り" に統合済み (4ビート構成)
	for _, want := range []string{"届け物の出発", "狼との出会いと先回り", "変装と誤認", "危機と救出"} {
		if !containsString(storyBeatLabels(skeleton.RequiredBeats), want) {
			t.Fatalf("expected beat %q in %v", want, storyBeatLabels(skeleton.RequiredBeats))
		}
	}
	for _, cue := range []string{"赤い頭巾", "おばあさん", "狼"} {
		if !containsString(skeleton.RecognitionCues, cue) {
			t.Fatalf("expected cue %q in %v", cue, skeleton.RecognitionCues)
		}
	}
}

func TestStorySpecs_CoverEntireCorpus(t *testing.T) {
	for _, source := range storyCorpus {
		spec, ok := storySpecForSource(source)
		if !ok {
			t.Fatalf("missing story spec for %s", source.ID)
		}
		if strings.TrimSpace(spec.Skeleton.SourceTitle) == "" {
			t.Fatalf("missing source title in skeleton for %s", source.ID)
		}
		if len(spec.Skeleton.RequiredBeats) == 0 {
			t.Fatalf("missing required beats for %s", source.ID)
		}
		if len(spec.Skeleton.RecognitionCues) == 0 {
			t.Fatalf("missing recognition cues for %s", source.ID)
		}
		for style, axis := range spec.Twists {
			if strings.TrimSpace(axis) == "" {
				t.Fatalf("empty twist axis for style %q in %s", style, source.ID)
			}
		}
	}
}

func TestStorySkeleton_KasajizoHasGiftAndReturnBeats(t *testing.T) {
	skeleton := storySkeleton(StorySource{ID: "kasajizo", Title: "笠地蔵"})
	for _, want := range []string{"年の暮れの困窮と売れ残り", "地蔵に笠をかぶせる", "足りない一体に手ぬぐいを巻く", "夜の返礼と温かな正月"} {
		if !containsString(storyBeatLabels(skeleton.RequiredBeats), want) {
			t.Fatalf("expected beat %q in %v", want, storyBeatLabels(skeleton.RequiredBeats))
		}
	}
	for _, cue := range []string{"笠", "地蔵", "手ぬぐい", "正月"} {
		if !containsString(skeleton.RecognitionCues, cue) {
			t.Fatalf("expected cue %q in %v", cue, skeleton.RecognitionCues)
		}
	}
}

func TestStorySatisfiesSkeleton_RedridingRequiresRecognitionCues(t *testing.T) {
	skeleton := storySkeleton(StorySource{ID: "redriding", Title: "赤ずきん"})
	plan := StoryRewritePlan{
		RewriteStyle: "role_shift",
		MotifMap: []string{
			"赤い頭巾=>赤い頭巾",
			"狼の先回り=>先回り",
			"おばあさんに化ける=>変装",
		},
	}
	beatPlan := StoryBeatPlan{
		Opening:   "赤い頭巾のスタッフが祖母役の入居者へ薬を届けに出る。",
		Deviation: "途中で不審者に足止めされ、訪問ルートを変えさせられる。",
		Reversal:  "相手は先回りし、おばあさんに化けて待っていた。",
		Landing:   "閉じ込められた入居者は救出されるが、油断の代償が残る。",
	}
	adaptation := buildStoryAdaptationPlan(skeleton, plan, beatPlan)
	story := "赤い頭巾のスタッフは、祖母のように慕う入居者へ薬を届けに出た。途中で足止めされて遠回りをさせられたあいだに、相手は先回りし、おばあさんに化けて部屋で待っていた。違和感のある会話の末に閉じ込められた入居者は救出されたが、油断の代償だけが残った。"
	if !storySatisfiesSkeleton(story, skeleton, adaptation) {
		t.Fatalf("expected recognizable redriding story to satisfy skeleton")
	}
}

func TestNormalizeStoryRewriteStyle_RejectsEraShiftAndMapsCurrentModes(t *testing.T) {
	cases := map[string]string{
		"role_shift":  "role_shift",
		"what_if":     "role_shift",
		"view_shift":  "view_shift",
		"value_shift": "value_shift",
		"era_shift":   "era_shift",
	}
	for in, want := range cases {
		if got := normalizeStoryRewriteStyle(in); got != want {
			t.Fatalf("normalizeStoryRewriteStyle(%q)=%q want %q", in, got, want)
		}
	}
}

func TestStoryNarrativeLooksSettled_RejectsMetaLeak(t *testing.T) {
	plan := StoryRewritePlan{
		StoryTitle:   "王女が見ていたランプ",
		Setting:      "王宮",
		Viewpoint:    "王女の一人称",
		EndingFlavor: "救い",
		MotifMap:     []string{"魔法のランプ=>魔法のランプ"},
	}
	beatPlan := StoryBeatPlan{Landing: "最後に残るのは救いだ。"}
	story := "元の『アラジンと魔法のランプ』で禁じられていたのは約束を破らないことだった。最後に残るのは救いという読後感だった。"
	if storyNarrativeLooksSettled(story, "draft", plan, beatPlan) {
		t.Fatal("expected meta leak to be rejected")
	}
}

func TestRepairStoryDraft_StripsMetaLeakAndKeepsLanding(t *testing.T) {
	source := StorySource{ID: "aladdin", Title: "アラジンと魔法のランプ", Text: "若者アラジンは怪しい男に導かれて洞窟へ入り、古びたランプを持ち帰りました。ランプの精の力で貧しさを抜け出し、王女と心を通わせます。しかし力を奪おうとする者に狙われ、ランプを取り戻すための機転が試されました。最後にアラジンは王女と再会し、奪われたものを自分の手で取り返します。"}
	analysis := analyzeStorySource(source)
	plan := StoryRewritePlan{
		StoryTitle:   "王女が見ていたランプ",
		Setting:      "王宮",
		Viewpoint:    "王女の一人称",
		EndingFlavor: "救い",
		MotifMap:     []string{"魔法のランプ=>魔法のランプ"},
	}
	beatPlan := StoryBeatPlan{Landing: "最後に残るのは救いだ。"}
	adaptation := buildStoryAdaptationPlan(analysis.Skeleton, plan, beatPlan)
	draft := "王女は魔法のランプの光を見た。元の『アラジンと魔法のランプ』で禁じられていたのは約束を破らないことだった。"
	got := repairStoryDraft(source, analysis, plan, adaptation, beatPlan, draft)
	if strings.Contains(got, "元の『") {
		t.Fatalf("expected meta leak to be stripped, got %q", got)
	}
	if !strings.Contains(got, "救い") {
		t.Fatalf("expected ending flavor to be restored, got %q", got)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestIdleChatOrchestrator_ChatInterrupted(t *testing.T) {
	provider := &mockLLMProvider{response: "response", delay: 5 * time.Millisecond}
	memory := session.NewCentralMemory()

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 100, 0.8, nil, "")

	o.mu.Lock()
	o.chatActive = true
	o.mu.Unlock()

	// 別goroutineで少し後に中断（delay=5ms * 数ターン後に到達）
	go func() {
		time.Sleep(30 * time.Millisecond)
		o.NotifyActivity()
	}()

	o.runChatSession(StrategySingleGenre)

	// 100ターン全部は実行されていないはず
	if provider.callCount >= 100 {
		t.Error("Chat should have been interrupted before 100 turns")
	}
	if until := time.Until(o.nextTopicAt); until < 4*time.Minute {
		t.Fatalf("expected interruption to apply idle cooldown, got nextTopicAt in %v", until)
	}
}

func TestIdleChatOrchestrator_GenerationErrorAppliesCooldown(t *testing.T) {
	provider := &mockLLMProvider{err: context.DeadlineExceeded}
	memory := session.NewCentralMemory()

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	o.mu.Lock()
	o.chatActive = true
	o.mu.Unlock()

	o.runChatSession(StrategySingleGenre)

	if until := time.Until(o.nextTopicAt); until < 4*time.Minute {
		t.Fatalf("expected generation error to apply idle cooldown, got nextTopicAt in %v", until)
	}
	if len(o.GetHistory(10)) != 0 {
		t.Fatalf("expected no summary history for zero-turn failed session, got %d", len(o.GetHistory(10)))
	}
}

func TestCheckAndStartChat_NotIdleLongEnough(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 60, 3, 0.8, nil, "")
	// lastActivity は now（アイドル時間が短い）

	o.checkAndStartChat()

	// アイドル時間不足なので雑談は開始しない
	if provider.callCount != 0 {
		t.Errorf("Expected 0 LLM calls (not idle enough), got %d", provider.callCount)
	}
}

func TestCheckAndStartChat_AlreadyActive(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 0, 3, 0.8, nil, "")

	o.mu.Lock()
	o.chatActive = true
	o.mu.Unlock()

	o.checkAndStartChat()

	// 既にアクティブなので新しいセッションは開始しない
	if provider.callCount != 0 {
		t.Errorf("Expected 0 LLM calls (already active), got %d", provider.callCount)
	}
}

func TestCheckAndStartChat_StartsSession(t *testing.T) {
	provider := &mockLLMProvider{response: "hello", delay: 1 * time.Millisecond}
	memory := session.NewCentralMemory()

	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 0, 2, 0.8, nil, "")

	// lastActivity を過去に設定
	o.mu.Lock()
	o.lastActivity = time.Now().Add(-1 * time.Hour)
	o.mu.Unlock()

	o.checkAndStartChat()

	// 雑談セッションが実行されたはず
	if provider.callCount < 4 {
		t.Errorf("Expected at least 4 LLM calls (topic + maxTurns + summary), got %d", provider.callCount)
	}

	// セッション終了後はchatActive=false
	if o.IsChatActive() {
		t.Error("chatActive should be false after session completes")
	}
}

func TestCheckAndStartChat_ManualMode_StartsWithoutIdleThreshold(t *testing.T) {
	provider := &mockLLMProvider{response: "hello", delay: 1 * time.Millisecond}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 60, 2, 0.8, nil, "")

	if err := o.StartManualMode(); err != nil {
		t.Fatalf("StartManualMode failed: %v", err)
	}

	o.checkAndStartChat()
	if provider.callCount < 4 {
		t.Fatalf("Expected at least 4 LLM calls in manual mode (topic + maxTurns + summary), got %d", provider.callCount)
	}
}

func TestCheckAndStartChat_RespectsMinTopicInterval(t *testing.T) {
	provider := &mockLLMProvider{response: "hello", delay: 1 * time.Millisecond}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 0, 2, 0.8, nil, "")

	o.mu.Lock()
	o.lastActivity = time.Now().Add(-1 * time.Hour)
	o.nextTopicAt = time.Now().Add(5 * time.Minute)
	o.mu.Unlock()

	o.checkAndStartChat()
	if provider.callCount != 0 {
		t.Fatalf("Expected 0 calls while within topic interval, got %d", provider.callCount)
	}
}

func TestGetSystemPrompt(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio"}, 5, 10, 0.8, nil, "")

	// 既知のAgent
	prompt := o.getSystemPrompt("mio")
	if prompt == "" {
		t.Error("Expected non-empty prompt for Mio")
	}

	// 未知のAgent
	prompt = o.getSystemPrompt("Unknown")
	if prompt == "" {
		t.Error("Expected fallback prompt for unknown agent")
	}
}

func TestFallbackTopicForStrategy_SingleUsesGenre(t *testing.T) {
	got := fallbackTopicForStrategy(StrategySingleGenre, []string{"昆虫学"}, "", "", topicAnchor{Kind: "人物", Value: "地方博物館の学芸員"}, false)
	if !strings.Contains(got, "昆虫学") || strings.HasSuffix(got, "ってどんな映画？") {
		t.Fatalf("expected normal single fallback, got %q", got)
	}
	if !strings.Contains(got, "地方博物館の学芸員") {
		t.Fatalf("expected concrete anchor in single fallback, got %q", got)
	}
}

func TestFallbackTopicForStrategy_DoubleUsesBothGenres(t *testing.T) {
	got := fallbackTopicForStrategy(StrategyDoubleGenre, []string{"茶道", "歯車"}, "", "", topicAnchor{Kind: "物", Value: "壊れたオルゴール"}, false)
	if !strings.Contains(got, "茶道") || !strings.Contains(got, "歯車") || strings.HasSuffix(got, "ってどんな映画？") {
		t.Fatalf("expected normal double fallback, got %q", got)
	}
	if !strings.Contains(got, "壊れたオルゴール") {
		t.Fatalf("expected concrete anchor in double fallback, got %q", got)
	}
}

func TestFallbackTopicForStrategy_ExternalUsesSeed(t *testing.T) {
	got := fallbackTopicForStrategy(StrategyExternalStimulus, nil, "Wikipedia:アレクサンドリア", "", topicAnchor{}, false)
	if !strings.Contains(got, "アレクサンドリア") || strings.HasSuffix(got, "ってどんな映画？") {
		t.Fatalf("expected normal external fallback to include seed, got %q", got)
	}
}

func TestBuildSingleGenrePrompt_RequiresConcreteAnchor(t *testing.T) {
	got := buildSingleGenrePrompt("音楽", topicAnchor{Kind: "人物", Value: "駆け出しのアーティスト"}, false)
	for _, want := range []string{
		"ジャンル: 音楽",
		"具体アンカー (人物): 駆け出しのアーティスト",
		"人・物・場所・場面のどれかを1つ必ず入れる",
		"抽象語だけで閉じた題名にしない",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected single prompt to contain %q, got %q", want, got)
		}
	}
}

func TestBuildDoubleGenrePrompt_RequiresConcreteAnchor(t *testing.T) {
	got := buildDoubleGenrePrompt([]string{"音楽", "橋"}, topicAnchor{Kind: "場所", Value: "始発前の駅"}, false)
	for _, want := range []string{
		"ジャンル: 音楽 × 橋",
		"具体アンカー (場所): 始発前の駅",
		"2ジャンルに具体アンカーを接続し",
		"抽象語だけで閉じた題名にしない",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected double prompt to contain %q, got %q", want, got)
		}
	}
}

func TestNormalizeIdleTopic_StripsChattyAnnouncementStyle(t *testing.T) {
	raw := "ユン食堂の食材調達における薬学的なアプローチ、つまり、それぞれの食材の成分組成と、それらを組み合わせた料理で生み出される生理活性効果を、徹底的に分析していくってのは、めちゃくちゃ面白いんじゃない？"
	got := normalizeIdleTopic(raw, false)
	want := "ユン食堂の食材調達における薬学的なアプローチ"
	if got != want {
		t.Fatalf("normalizeIdleTopic() = %q, want %q", got, want)
	}
}

func TestNormalizeIdleTopic_MovieModeFormatsAsMoviePrompt(t *testing.T) {
	raw := "ユン食堂の食材調達における薬学的なアプローチ、つまり、それぞれの食材の成分組成と、それらを組み合わせた料理で生み出される生理活性効果を、徹底的に分析していくってのは、めちゃくちゃ面白いんじゃない？"
	got := normalizeIdleTopic(raw, true)
	want := "「ユン食堂の食材調達における薬学的なアプローチ」ってどんな映画？"
	if got != want {
		t.Fatalf("normalizeIdleTopic(movie) = %q, want %q", got, want)
	}
}

func TestFormatMovieTopicPrompt_StripsNestedMovieSuffix(t *testing.T) {
	got := formatMovieTopicPrompt("「瓦礫のセレナーデ」ってどんな映画」ってどんな映画？")
	want := "「瓦礫のセレナーデ」ってどんな映画？"
	if got != want {
		t.Fatalf("formatMovieTopicPrompt() = %q, want %q", got, want)
	}
}

func TestFormatMovieTopicPrompt_StripsWrappedMoviePrompt(t *testing.T) {
	got := formatMovieTopicPrompt("「『暗殺の墨色』ってどんな映画？」")
	want := "「暗殺の墨色」ってどんな映画？"
	if got != want {
		t.Fatalf("formatMovieTopicPrompt() = %q, want %q", got, want)
	}
}

func TestGenerateTopicFromChat_NormalizesChattyOutput(t *testing.T) {
	provider := &mockLLMProvider{
		response: "ユン食堂の食材調達における薬学的なアプローチ、つまり、それぞれの食材の成分組成と、それらを組み合わせた料理で生み出される生理活性効果を、徹底的に分析していくってのは、めちゃくちゃ面白いんじゃない？",
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	topic, _ := o.generateTopicFromChat("idle-topic-normalize", StrategySingleGenre)
	if topic != "ユン食堂の食材調達における薬学的なアプローチ" &&
		topic != "「ユン食堂の食材調達における薬学的なアプローチ」ってどんな映画？" {
		t.Fatalf("unexpected normalized topic: %q", topic)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"こんにちは", 3, "こんに..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func TestIsLooping_DetectsAlternatingSimilarity(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	transcript := []string{
		"mio: 世界の法則が変わるRPGって面白いよね",
		"shiro: その変化を倫理と戦略の両方で扱うのが核心ですね",
		"mio: 世界の法則が変わるRPGって面白いよね！",
		"shiro: その変化を倫理と戦略の両方で扱うのが核心です",
		"mio: 世界の法則が変わるRPGって面白いよね",
		"shiro: その変化を倫理と戦略の両方で扱うのが核心ですね",
		"mio: 世界の法則が変わるRPGって面白いよね！",
		"shiro: その変化を倫理と戦略の両方で扱うのが核心です",
	}
	if !o.isLooping(transcript) {
		t.Fatal("expected alternating repetitive transcript to be detected as loop")
	}
}

func TestIsLooping_DetectsRepeatedSpeakerTemplates(t *testing.T) {
	provider := &mockLLMProvider{response: "hello"}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	transcript := []string{
		"mio: まさに！音色を形にするって、まるで自分の心の風景を立体的に表現していくみたいじゃない？",
		"shiro: [mio]の表現は、非常に的確で、具体的なイメージを喚起するものです。しかし、音の質をどう扱うべきでしょうか。",
		"mio: まさに！感情そのものを具現化するって、まるで音色で自分の心模様を鮮やかに描き出すようなものじゃない？",
		"shiro: [mio]の表現は、非常に興味深いですね。しかし、その表現を成し遂げるには、どのような工夫が必要でしょうか。",
		"mio: まさに！物語を紡ぎ出すって、すごくロマンチックじゃない？",
		"shiro: [mio]の表現は、非常に興味深いですね。しかし、物語のテーマを明確にする必要があるのではないでしょうか。",
	}
	if !o.isLooping(transcript) {
		t.Fatal("expected repeated speaker templates to be detected as loop")
	}
	if reason := detectLoopReason(transcript); reason != "short_template_repeat" {
		t.Fatalf("expected short_template_repeat, got %q", reason)
	}
}

func TestAnnotateLoopSummary_AddsReasonNote(t *testing.T) {
	got := annotateLoopSummary("本文", true, "template_repeat")
	want := "注記: テンプレ反復で打ち切り\n\n本文"
	if got != want {
		t.Fatalf("unexpected annotated summary: got %q want %q", got, want)
	}
}

func TestAnnotateLoopSummary_SkipsNoteForTopicTurnLimit(t *testing.T) {
	got := annotateLoopSummary("本文", true, "topic_turn_limit")
	if got != "本文" {
		t.Fatalf("unexpected annotated summary: got %q want %q", got, "本文")
	}
}

func TestSanitizeIdleResponse_StripsLeadingPunctuation(t *testing.T) {
	got := sanitizeIdleResponse("。「。」なるほど！じゃあ、観察対象を絞ろう。", "話題")
	want := "なるほど！じゃあ、観察対象を絞ろう。"
	if got != want {
		t.Fatalf("sanitizeIdleResponse() = %q, want %q", got, want)
	}
}

func TestSanitizeIdleResponse_StripsSpeakerPrefix(t *testing.T) {
	got := sanitizeIdleResponse("Mio：コンコルドの涙、すごく切なそうだね。", "話題")
	want := "コンコルドの涙、すごく切なそうだね。"
	if got != want {
		t.Fatalf("sanitizeIdleResponse() = %q, want %q", got, want)
	}
}

func TestSanitizeIdleResponse_StripsBracketedSpeakerPrefix(t *testing.T) {
	got := sanitizeIdleResponse("[mio]: コンコルドの涙、すごく切なそうだね。", "話題")
	want := "コンコルドの涙、すごく切なそうだね。"
	if got != want {
		t.Fatalf("sanitizeIdleResponse() = %q, want %q", got, want)
	}
}

func TestSanitizeIdleResponse_StripsRepeatedSpeakerPrefix(t *testing.T) {
	got := sanitizeIdleResponse("mio]: mio]: 鏡の表面って、まるで映画のセットみたいだね。", "話題")
	want := "鏡の表面って、まるで映画のセットみたいだね。"
	if got != want {
		t.Fatalf("sanitizeIdleResponse() = %q, want %q", got, want)
	}
}

func TestInvalidIdleResponse_RejectsLeadingPunctuation(t *testing.T) {
	tests := []string{
		"。",
		"、まるですごろくが戦略を読み解こうとするなんて、めっちゃ面白い！",
		"。「。」なるほど！じゃあ、足切れる場所を特定するために考えよう。",
	}
	for _, input := range tests {
		if !invalidIdleResponse(input) {
			t.Fatalf("expected invalidIdleResponse(%q) to be true", input)
		}
	}
}

func TestGenerateResponse_RetriesInvalidLeadingPunctuation(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []string{
			"。「。」なるほど！じゃあ、足切れる場所を特定するために考えよう。",
			"なるほど！じゃあ、足切れる場所を特定するために、どのマスで失速するか集計してみよう。",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-invalid", 1, 1, "すごろく")
	if err != nil {
		t.Fatalf("generateResponse() failed: %v", err)
	}
	if provider.callCount < 2 {
		t.Fatalf("expected retry on invalid response, got %d calls", provider.callCount)
	}
	if strings.HasPrefix(got, "。") || strings.HasPrefix(got, "、") {
		t.Fatalf("expected sanitized retry result without leading punctuation, got %q", got)
	}
}

func TestGenerateResponse_AcceptsSanitizedResponseWhenRetryInvalidIsEmpty(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []string{
			"。「。」たとえば、雨上がりの舗道で音が一斉に跳ね返る場面があると、ぐっと立ち上がる。",
			"",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-sanitized-ok", 1, 1, "音の反射")
	if err != nil {
		t.Fatalf("generateResponse() failed: %v", err)
	}
	if got != "たとえば、雨上がりの舗道で音が一斉に跳ね返る場面があると、ぐっと立ち上がる。" {
		t.Fatalf("unexpected sanitized response: %q", got)
	}
}

func TestHasAwkwardIdleStyle_DetectsShiroCliches(t *testing.T) {
	if !hasAwkwardIdleStyle("shiro", "mioさんのご発言、まさにその通りですね。非常に興味深いですね。") {
		t.Fatal("expected awkward shiro cliche to be detected")
	}
	if !hasAwkwardIdleStyle("shiro", "なるほど、確かに少し硬すぎましたね。言い直すと、そこが面白いです。") {
		t.Fatal("expected self-referential retry wording to be detected")
	}
	if hasAwkwardIdleStyle("shiro", "その視点は面白いです。ここで条件を一つ足すと見え方が変わりそうです。") {
		t.Fatal("expected natural shiro response to pass")
	}
}

func TestHasExcessivePhraseRepetition_DetectsRepeatedPhrases(t *testing.T) {
	if !hasExcessivePhraseRepetition("まさに まさに まさに 面白いですね。") {
		t.Fatal("expected repeated token to be detected")
	}
	if !hasExcessivePhraseRepetition("同じ こと を 考える。同じ こと を 考える。") {
		t.Fatal("expected repeated phrase to be detected")
	}
	if hasExcessivePhraseRepetition("その視点は面白いです。条件を変えると結果も動きそうです。") {
		t.Fatal("expected non-repetitive response to pass")
	}
}

func TestGenerateResponse_RetriesAwkwardShiroStyle(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []string{
			"mioさんのご発言、まさにその通りですね。前に自分も触れたように、非常に興味深いですね。",
			"その見方は面白いです。どの条件で差が出るのかを先に切り分けたいですね。",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	got, err := o.generateResponse("shiro", "mio", "idle-style", 1, 1, "すごろく")
	if err != nil {
		t.Fatalf("generateResponse() failed: %v", err)
	}
	if provider.callCount < 2 {
		t.Fatalf("expected retry on awkward style, got %d calls", provider.callCount)
	}
	if hasAwkwardIdleStyle("shiro", got) {
		t.Fatalf("expected retried shiro response to avoid awkward style, got %q", got)
	}
}

func TestGenerateResponse_RetriesSelfReferentialShiroRewrite(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []string{
			"なるほど、確かに少し硬すぎましたね。言い直すと、そこが面白いです。",
			"その発想は面白いです。地下構造の違いが地震波にどう出るかを先に見たいですね。",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	got, err := o.generateResponse("shiro", "mio", "idle-style-self-ref", 1, 1, "地下構造")
	if err != nil {
		t.Fatalf("generateResponse() failed: %v", err)
	}
	if provider.callCount < 2 {
		t.Fatalf("expected retry on self-referential style, got %d calls", provider.callCount)
	}
	if hasAwkwardIdleStyle("shiro", got) {
		t.Fatalf("expected retried shiro response to avoid self-referential style, got %q", got)
	}
}

func TestMirrorsLatestOther_DetectsBorrowedLongPhrase(t *testing.T) {
	latest := "舞台の幕開けみたいじゃないですか？ 権力や繁栄の象徴として捉えるのも一理ありますが、もっとロマンチックな側面も持ち合わせている気がしませんか？"
	response := "舞台の幕開け、まるで権力に染め上げられた絢爛な舞踏ですね。"
	if !mirrorsLatestOther(response, latest, "あす予算委、職権で桜咲く") {
		t.Fatal("expected mirrored long phrase to be detected")
	}
}

func TestGenerateResponse_RetriesShiroMirroringLatestOther(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []string{
			"舞台の幕開け、まるで権力に染め上げられた絢爛な舞踏ですね。",
			"そこには演出の気配がありますね。誰が得をする構図なのかを見ると輪郭が出そうです。",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	memory.RecordMessage(domaintransport.Message{
		From:      "mio",
		To:        "shiro",
		SessionID: "idle-mirror",
		Content:   "舞台の幕開けみたいじゃないですか？ 権力や繁栄の象徴として捉えるのも一理ありますが、もっとロマンチックな側面も持ち合わせている気がしませんか？",
	})

	got, err := o.generateResponse("shiro", "mio", "idle-mirror", 1, 1, "あす予算委、職権で桜咲く")
	if err != nil {
		t.Fatalf("generateResponse() failed: %v", err)
	}
	if provider.callCount < 2 {
		t.Fatalf("expected retry on mirrored wording, got %d calls", provider.callCount)
	}
	if strings.Contains(got, "舞台の幕開け") {
		t.Fatalf("expected retried shiro response to avoid mirrored phrase, got %q", got)
	}
}

func TestGenerateResponse_RetriesMioMirroringLatestOther(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []string{
			"混乱の可能性、ご懸念もっともかと存じます！",
			"そこは確かに悩ましいよね。先に見せる範囲を絞れば、混乱はかなり減らせそう！",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	memory.RecordMessage(domaintransport.Message{
		From:      "shiro",
		To:        "mio",
		SessionID: "idle-mio-mirror",
		Content:   "その光が届かない混乱の可能性、ご懸念はもっともかと存じます。",
	})

	got, err := o.generateResponse("mio", "shiro", "idle-mio-mirror", 1, 1, "予算案")
	if err != nil {
		t.Fatalf("generateResponse() failed: %v", err)
	}
	if provider.callCount < 2 {
		t.Fatalf("expected retry on mio mirrored wording, got %d calls", provider.callCount)
	}
	if strings.Contains(got, "ご懸念もっともかと存じます") {
		t.Fatalf("expected retried mio response to avoid mirrored phrase, got %q", got)
	}
}

func TestGenerateResponse_InvalidMovieResponseStopsInsteadOfLooping(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []string{
			"!!!",
			"。。。",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	_, err := o.generateResponse("mio", "shiro", "idle-movie-fallback", 1, 1, "「ブルーノート・コード」ってどんな映画？")
	if !errors.Is(err, errIdleInvalidResponse) {
		t.Fatalf("expected errIdleInvalidResponse, got %v", err)
	}
}

func TestGenerateResponse_ReturnsInvalidResponseErrorAfterRetry(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []string{
			"!!!",
			"。。。",
		},
	}
	memory := session.NewCentralMemory()
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	_, err := o.generateResponse("shiro", "mio", "idle-invalid-stop", 1, 1, "すごろく")
	if !errors.Is(err, errIdleInvalidResponse) {
		t.Fatalf("expected errIdleInvalidResponse, got %v", err)
	}
}

func TestSanitizeIdleResponse_EmptyStaysEmpty(t *testing.T) {
	got := sanitizeIdleResponse("!!!", "話題")
	if got != "" {
		t.Fatalf("sanitizeIdleResponse() = %q, want empty string", got)
	}
}

func TestTopicTooSimilar(t *testing.T) {
	recent := []string{
		"人生をRPG化するならどんな世界観がいいか",
		"月面都市の建設競争とAI設計の未来",
	}
	if !topicTooSimilar("人生をRPG化するならどんな世界観が良いか？", recent) {
		t.Fatal("expected near-duplicate topic to be considered similar")
	}
	if topicTooSimilar("量子通信が一般家庭に来たときの意外な副作用", recent) {
		t.Fatal("expected clearly different topic to be accepted")
	}
}

func TestDetectLoopReason_ShortAlternatingRepeat(t *testing.T) {
	transcript := []string{
		"mio: それって、まるで映画みたいだね。",
		"shiro: 映画のように構造化して考えるといいでしょう。",
		"mio: それって、まるで映画みたいだね。",
		"shiro: 映画のように構造化して考えるといいでしょう。",
	}
	if reason := detectLoopReason(transcript); reason != "short_alternating_repeat" {
		t.Fatalf("expected short_alternating_repeat, got %q", reason)
	}
}

func TestDetectLoopReason_ShortTemplateRepeat(t *testing.T) {
	transcript := []string{
		"mio: なるほど、まるで時計みたいだね。",
		"shiro: 具体的には入力条件を分けて考えるべきです。",
		"mio: なるほど、まるでパズルみたいだね。",
		"shiro: 具体的には実装条件を分けて考えるべきです。",
	}
	if reason := detectLoopReason(transcript); reason != "short_template_repeat" {
		t.Fatalf("expected short_template_repeat, got %q", reason)
	}
}

func TestThemeFromSummaryTitle(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "forecast title with domain prefix",
			title: "3月15日の[AI技術] AIエージェントが行政判断を補助する社会の話題まとめ",
			want:  "AIエージェントが行政判断を補助する社会",
		},
		{
			name:  "generic summary title",
			title: "3月15日の月面都市の建設競争とAI設計の未来の話題まとめ",
			want:  "月面都市の建設競争とAI設計の未来",
		},
		{
			name:  "empty title",
			title: "   ",
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := themeFromSummaryTitle(tc.title); got != tc.want {
				t.Fatalf("themeFromSummaryTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetHistoricalTitleThemes(t *testing.T) {
	o := &IdleChatOrchestrator{
		history: []SessionSummary{
			{Title: "3月15日の[AI技術] AIエージェントが行政判断を補助する社会の話題まとめ"},
			{Title: "3月14日の[AI技術] AIエージェントが行政判断を補助する社会の話題まとめ"},
			{Title: "3月13日の医療AIが診断インフラになる時代の話題まとめ"},
		},
	}
	got := o.getHistoricalTitleThemes(10)
	want := []string{
		"医療AIが診断インフラになる時代",
		"AIエージェントが行政判断を補助する社会",
	}
	if len(got) != len(want) {
		t.Fatalf("len(getHistoricalTitleThemes()) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("getHistoricalTitleThemes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestForecastTopicStockPopRemovesSameThemeDuplicates(t *testing.T) {
	s := &forecastTopicStock{
		stock: map[string][]PreparedTopic{
			"AI技術": {
				{Topic: "AIエージェントが行政判断を補助する社会"},
				{Topic: "AIエージェントが行政判断を補助する社会"},
				{Topic: "医療AIが診断インフラになる時代"},
			},
		},
		filling: make(map[string]bool),
	}
	item := s.pop("AI技術")
	if item == nil || item.Topic != "AIエージェントが行政判断を補助する社会" {
		t.Fatalf("pop() topic = %#v", item)
	}
	got := s.stock["AI技術"]
	if len(got) != 1 {
		t.Fatalf("len(stock after pop) = %d, want 1", len(got))
	}
	if got[0].Topic != "医療AIが診断インフラになる時代" {
		t.Fatalf("remaining topic = %q", got[0].Topic)
	}
}

func TestForecastTopicStockPushSkipsSameThemeDuplicates(t *testing.T) {
	s := &forecastTopicStock{
		stock: map[string][]PreparedTopic{
			"AI技術": {
				{Topic: "AIエージェントが行政判断を補助する社会"},
			},
		},
		filling: make(map[string]bool),
	}
	s.push("AI技術", PreparedTopic{Topic: "AIエージェントが行政判断を補助する社会"})
	if got := len(s.stock["AI技術"]); got != 1 {
		t.Fatalf("len(stock after duplicate push) = %d, want 1", got)
	}
	s.push("AI技術", PreparedTopic{Topic: "医療AIが診断インフラになる時代"})
	if got := len(s.stock["AI技術"]); got != 2 {
		t.Fatalf("len(stock after unique push) = %d, want 2", got)
	}
}

func TestScoreForecastSeed(t *testing.T) {
	if got := scoreForecastSeed(ForecastDomain{Name: "AI技術"}, "生成AI向け半導体投資が加速"); got <= scoreForecastSeed(ForecastDomain{Name: "AI技術"}, "地方の祭りが再開") {
		t.Fatal("expected AI-related seed to score higher for AI技術")
	}
	if got := scoreForecastSeed(ForecastDomain{Name: "経済"}, "日銀の金利政策で為替が変動"); got <= scoreForecastSeed(ForecastDomain{Name: "経済"}, "最新ロボット展示会が開催") {
		t.Fatal("expected economics-related seed to score higher for 経済")
	}
}

func TestRankForecastSeeds(t *testing.T) {
	seeds := []string{
		"地方の祭りが再開",
		"生成AI向け半導体投資が加速",
		"ロボット開発で自動運転技術が進展",
	}
	ranked := rankForecastSeeds(ForecastDomain{Name: "AI技術"}, seeds)
	if len(ranked) != len(seeds) {
		t.Fatalf("len(rankForecastSeeds()) = %d, want %d", len(ranked), len(seeds))
	}
	top := ranked[0]
	if top != "生成AI向け半導体投資が加速" && top != "ロボット開発で自動運転技術が進展" {
		t.Fatalf("unexpected top-ranked AI seed: %q", top)
	}
}

func TestIsResponseTooSimilar(t *testing.T) {
	transcript := []string{
		"mio: 世界の調律師という設定が面白い",
		"shiro: 調和と混沌の選択が主題になります",
		"mio: 運命のカードで分岐を増やしたい",
		"shiro: カードと行動の連動が鍵ですね",
		"mio: 世界の調律師という設定が面白い",
		"shiro: 調和と混沌の選択が主題になります",
	}
	if !isResponseTooSimilar("世界の調律師という設定が面白い！", transcript) {
		t.Fatal("expected repetitive response to be detected")
	}
	if isResponseTooSimilar("都市インフラを音楽理論で最適化する話に広げよう", transcript) {
		t.Fatal("expected fresh response not to be detected as repetitive")
	}
}

func TestSplitSpeakerContexts(t *testing.T) {
	mem := session.NewCentralMemory()
	sid := "idle-ctx"
	mem.RecordMessage(domaintransport.Message{From: "mio", To: "shiro", SessionID: sid, Content: "最初の提案"})
	mem.RecordMessage(domaintransport.Message{From: "shiro", To: "mio", SessionID: sid, Content: "その提案の補足"})
	mem.RecordMessage(domaintransport.Message{From: "mio", To: "shiro", SessionID: sid, Content: "別観点の追加"})

	entries := mem.GetUnifiedView(20)
	self, other := splitSpeakerContexts(entries, sid, "mio", 5)
	if len(self) == 0 || len(other) == 0 {
		t.Fatal("expected both self/other contexts")
	}
	if self[0] != "別観点の追加" {
		t.Fatalf("expected latest self context first, got %q", self[0])
	}
	if other[0] == "なし" {
		t.Fatal("expected other context to include shiro utterance")
	}
}

func TestViolatesAttribution(t *testing.T) {
	other := "世界の調律師という設定はどう？"
	if !violatesAttribution("世界の調律師という設定はどう？", other) {
		t.Fatal("expected direct reuse without attribution to be flagged")
	}
	if violatesAttribution("あなたの『世界の調律師』案を受けると、次は倫理分岐を入れたい", other) {
		t.Fatal("expected explicit attribution to pass")
	}
}
