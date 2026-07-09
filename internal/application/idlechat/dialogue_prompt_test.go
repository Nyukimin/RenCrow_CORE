package idlechat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

type capturingIdleProvider struct {
	response      string
	block         bool
	requests      []llm.GenerateRequest
	responses     []string
	finishReasons []string
}

func (p *capturingIdleProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	if p.block {
		<-ctx.Done()
		return llm.GenerateResponse{}, ctx.Err()
	}
	if len(p.responses) > 0 {
		response := p.responses[0]
		p.responses = p.responses[1:]
		finish := ""
		if len(p.finishReasons) > 0 {
			finish = p.finishReasons[0]
			p.finishReasons = p.finishReasons[1:]
		}
		return llm.GenerateResponse{Content: response, FinishReason: finish}, nil
	}
	return llm.GenerateResponse{Content: p.response}, nil
}

func (p *capturingIdleProvider) Name() string { return "capturing-idle" }

func TestGenerateResponseFirstTurnUsesActualSpeaker(t *testing.T) {
	provider := &capturingIdleProvider{response: "郵便配達員が古書店の棚で宛先不明の手紙を見つける入口がよさそう。しろなら、誰が隠したと思う？"}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	_, err := o.generateResponse("mio", "shiro", "idle-dialogue-first", 0, 0, "郵便と古書店")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(provider.requests))
	}
	last := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	if !strings.Contains(last, "mioとして、会話の最初の発話を1〜2文") {
		t.Fatalf("first turn prompt should use actual speaker mio:\n%s", last)
	}
	if strings.Contains(last, "shiroとして、会話の最初の発話") {
		t.Fatalf("first turn prompt leaked target as speaker:\n%s", last)
	}
}

func TestGenerateResponseSendsExplicitThinkOptionPerIdleSpeaker(t *testing.T) {
	chatProvider := &capturingIdleProvider{response: "郵便配達員が古書店で手紙を見つける入口、すごく気になる。しろなら、その手紙を開ける？"}
	workerProvider := &capturingIdleProvider{response: "開ける前に、宛名の消え方を見るべきです。封筒の端だけ濡れているなら、隠した人の癖が残ります。"}
	o := NewIdleChatOrchestrator(chatProvider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{
		"mio":   chatProvider,
		"shiro": workerProvider,
	})

	if _, err := o.generateResponse("mio", "shiro", "idle-think-mio", 0, 0, "郵便と古書店"); err != nil {
		t.Fatalf("mio generateResponse() error = %v", err)
	}
	if len(chatProvider.requests) == 0 {
		t.Fatal("expected mio request")
	}
	if got, ok := chatProvider.requests[0].ProviderOptions["think"].(bool); !ok || got {
		t.Fatalf("mio think option = %#v, want false", chatProvider.requests[0].ProviderOptions["think"])
	}
	if system := chatProvider.requests[0].Messages[0].Content; !strings.Contains(system, "/no_think") {
		t.Fatalf("mio system prompt should use /no_think:\n%s", system)
	}

	if _, err := o.generateResponse("shiro", "mio", "idle-think-shiro", 1, 1, "郵便と古書店"); err != nil {
		t.Fatalf("shiro generateResponse() error = %v", err)
	}
	if len(workerProvider.requests) == 0 {
		t.Fatal("expected shiro request")
	}
	if got, ok := workerProvider.requests[0].ProviderOptions["think"].(bool); !ok || got {
		t.Fatalf("shiro think option = %#v, want false", workerProvider.requests[0].ProviderOptions["think"])
	}
	if system := workerProvider.requests[0].Messages[0].Content; !strings.Contains(system, "/no_think") {
		t.Fatalf("shiro system prompt should use /no_think:\n%s", system)
	}
}

func TestGenerateResponseShiroSkipsFunCandidateAndUsesCompactMaxTokens(t *testing.T) {
	workerProvider := &capturingIdleProvider{response: "その役割は、外から来た人が古いルールの穴を見つけることです。最初に困る場所を一つ決めると見えます。"}
	o := NewIdleChatOrchestrator(workerProvider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("shiro", "mio", "idle-shiro-single-candidate", 1, 1, "異世界転移者の役割")
	if err != nil {
		t.Fatalf("shiro generateResponse() error = %v", err)
	}
	if got == "" {
		t.Fatal("shiro response should not be empty")
	}
	if len(workerProvider.requests) != 1 {
		t.Fatalf("shiro should emit after primary response without fun candidate request; requests = %d, want 1", len(workerProvider.requests))
	}
	if got := workerProvider.requests[0].MaxTokens; got != idleChatShiroResponseMaxTokens {
		t.Fatalf("shiro max tokens = %d, want %d", got, idleChatShiroResponseMaxTokens)
	}
}

func TestGenerateResponseSelectsMoreFunCandidate(t *testing.T) {
	provider := &capturingIdleProvider{responses: []string{
		"その話題は構造を考えると面白いですね。もう少し整理できそうです。",
		"雨の古書店で宛先不明の手紙が棚から落ちたら、隠した人の嘘まで濡れて見えそう。しろなら、その封筒を開ける？",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-fun-candidate", 0, 0, "郵便と古書店")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(provider.requests))
	}
	secondPrompt := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content
	if !strings.Contains(secondPrompt, "別候補") || !strings.Contains(secondPrompt, "楽しさ") {
		t.Fatalf("second candidate prompt does not request a fun alternative:\n%s", secondPrompt)
	}
	if !strings.Contains(secondPrompt, "英語だけの応答") {
		t.Fatalf("second candidate prompt does not ban English-only responses:\n%s", secondPrompt)
	}
	if len(provider.requests[1].Messages) < 2 || provider.requests[1].Messages[len(provider.requests[1].Messages)-2].Role != "assistant" {
		t.Fatalf("second candidate request should include first candidate as assistant context: %+v", provider.requests[1].Messages)
	}
	if !strings.Contains(got, "宛先不明の手紙") || !strings.Contains(got, "封筒を開ける") {
		t.Fatalf("more fun candidate was not selected: %q", got)
	}
}

func TestGenerateResponseWithRawReturnsUneditedSelectedOutput(t *testing.T) {
	provider := &capturingIdleProvider{responses: []string{
		"Mio: 雨の古書店で宛先不明の手紙が棚から落ちたら、隠した人の嘘まで濡れて見えそう。",
		"その話題は構造を考えると面白いですね。もう少し整理できそうです。",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, raw, err := o.generateResponseWithRaw("mio", "shiro", "idle-raw-response", 0, 0, "郵便と古書店")
	if err != nil {
		t.Fatalf("generateResponseWithRaw() error = %v", err)
	}
	if strings.Contains(got, "Mio:") {
		t.Fatalf("response was not sanitized: %q", got)
	}
	if raw != "Mio: 雨の古書店で宛先不明の手紙が棚から落ちたら、隠した人の嘘まで濡れて見えそう。" {
		t.Fatalf("raw response = %q", raw)
	}
}

func TestGenerateResponseSelectsCurrentSpeakerLineFromScriptOutput(t *testing.T) {
	provider := &capturingIdleProvider{responses: []string{
		"Mio: その封筒を開けた瞬間、棚の奥の雨音まで変わりそう。\nShiro: 開ける前に、宛名の消え方を見たほうがいい。",
		"その話題は構造を考えると面白いですね。もう少し整理できそうです。",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, raw, err := o.generateResponseWithRaw("mio", "shiro", "idle-script-response", 0, 0, "郵便と古書店")
	if err != nil {
		t.Fatalf("generateResponseWithRaw() error = %v", err)
	}
	if got != "その封筒を開けた瞬間、棚の奥の雨音まで変わりそう。" {
		t.Fatalf("response should keep only current speaker line: %q", got)
	}
	if !strings.Contains(raw, "Shiro:") {
		t.Fatalf("raw response should preserve original script output: %q", raw)
	}
}

func TestSanitizeIdleResponseForSpeakerSelectsShiroLine(t *testing.T) {
	raw := "mio: その封筒を開けた瞬間、棚の奥の雨音まで変わりそう。\nshiro: 開ける前に、宛名の消え方を見たほうがいい。"

	got := sanitizeIdleResponseForSpeaker(raw, "郵便と古書店", "shiro")
	want := "開ける前に、宛名の消え方を見たほうがいい。"
	if got != want {
		t.Fatalf("sanitizeIdleResponseForSpeaker() = %q, want %q", got, want)
	}
}

func TestPromptInstructionLeakIsRejectedAsUnusableIdleResponse(t *testing.T) {
	raw := "直前と違う入り口、具体物・理由・問いのどれか一つを足してください。"
	sanitized := sanitizeIdleResponse(raw, "文化交流")
	if !unusableIdleResponse(raw, sanitized) {
		t.Fatalf("prompt instruction leak should be unusable: raw=%q sanitized=%q", raw, sanitized)
	}
}

func TestSanitizeIdleResponseExtractsStraightQuotedPossibleResponse(t *testing.T) {
	raw := `Possible response: "雨上がりの空気は、薄い青色だったような気がする。でも、古い傘の柄にまだ水滴が残っていた。"`

	got := sanitizeIdleResponseForSpeaker(raw, "雨上がりの縁側", "shiro")
	want := "雨上がりの空気は、薄い青色だったような気がする。でも、古い傘の柄にまだ水滴が残っていた。"
	if got != want {
		t.Fatalf("sanitizeIdleResponseForSpeaker() = %q, want %q", got, want)
	}
}

func TestGenerateResponseUsesDialogueMaxTokensForShiro(t *testing.T) {
	provider := &capturingIdleProvider{responses: []string{
		"その点は、封筒を開ける前に誰が見ていたかで意味が変わる。",
		"その点は、封筒を戻す選択にも小さな嘘が残る。",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	_, err := o.generateResponse("shiro", "mio", "idle-shiro-token-limit", 1, 1, "郵便と古書店")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("no LLM requests captured")
	}
	if got := provider.requests[0].MaxTokens; got != idleChatShiroResponseMaxTokens {
		t.Fatalf("shiro primary max tokens = %d, want %d", got, idleChatShiroResponseMaxTokens)
	}
}

func TestGenerateResponseErrorsShiroDialogueWithoutDefaultProviderFallback(t *testing.T) {
	chatProvider := &capturingIdleProvider{response: "開ける前に、宛名の消え方を見たほうがいい。封筒の端だけ濡れているなら、隠した人の癖が残ります。"}
	workerProvider := &capturingIdleProvider{responses: []string{"", ""}}
	o := NewIdleChatOrchestrator(chatProvider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{
		"shiro": workerProvider,
	})

	got, err := o.generateResponse("shiro", "mio", "idle-shiro-recovery", 1, 1, "郵便と古書店")
	if err == nil {
		t.Fatalf("generateResponse() returned fallback instead of error: %q", got)
	}
	if len(workerProvider.requests) < 1 {
		t.Fatal("expected worker provider to be tried first")
	}
	if len(chatProvider.requests) != 0 {
		t.Fatalf("default provider fallback must not be used, requests = %d", len(chatProvider.requests))
	}
}

func TestGenerateResponseErrorsEmptyContentForAnySpeaker(t *testing.T) {
	provider := &capturingIdleProvider{responses: []string{"", "この応答は使われないはずです。"}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-empty-content-error", 0, 0, "郵便と古書店")
	if err == nil {
		t.Fatalf("generateResponse() returned retry/fallback instead of error: %q", got)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("empty content must fail without retry, requests=%d", len(provider.requests))
	}
}

func TestGenerateResponseErrorsShiroReasoningLeakWithoutDefaultProviderFallback(t *testing.T) {
	chatProvider := &capturingIdleProvider{response: "古い録音機材の部屋なら、最初に残ったノイズの正体を決めると話が締まります。みおなら、その音を誰の記憶にしますか？"}
	workerProvider := &capturingIdleProvider{responses: []string{
		"Okay, the user is asking me to respond as Shiro in Japanese. The task is to give one or two sentences, but first I need to analyze the context and decide what kind of concrete hook to add.",
		"この応答は使われないはずです。",
	}}
	o := NewIdleChatOrchestrator(chatProvider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{
		"shiro": workerProvider,
	})

	got, err := o.generateResponse("shiro", "mio", "idle-shiro-reasoning-recovery", 1, 1, "古い録音機材の部屋")
	if err == nil {
		t.Fatalf("generateResponse() returned fallback instead of error: %q", got)
	}
	if len(workerProvider.requests) != 1 {
		t.Fatalf("worker requests = %d, want 1", len(workerProvider.requests))
	}
	if len(chatProvider.requests) != 0 {
		t.Fatalf("default provider fallback must not be used, requests = %d", len(chatProvider.requests))
	}
}

func TestGenerateResponseErrorsShiroQuotedReasoningLeakWithoutDefaultProviderFallback(t *testing.T) {
	chatProvider := &capturingIdleProvider{response: "金属の肌が彼の選択を先に語ってしまうなら、工房の隅に残った酸化銅がいちばん正直な証人かもしれません。"}
	workerProvider := &capturingIdleProvider{responses: []string{
		`Okay, let's see. The user wants me to respond as Shiro to Mio's latest message.

Let's look at the previous response. Shiro said: "その通りかもしれないね。あの金属の肌は、もはや彼の意志を超えた素材として彼の表現を押し上げているように見える。"

Need a concise Japanese final answer, but first I need to choose one hook.`,
		"この応答は使われないはずです。",
	}}
	o := NewIdleChatOrchestrator(chatProvider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{
		"shiro": workerProvider,
	})

	got, err := o.generateResponse("shiro", "mio", "idle-shiro-quoted-reasoning-recovery", 7, 7, "金属の肌を持つアーティスト")
	if err == nil {
		t.Fatalf("generateResponse() returned fallback instead of error: %q", got)
	}
	if len(workerProvider.requests) != 1 {
		t.Fatalf("worker requests = %d, want 1", len(workerProvider.requests))
	}
	if len(chatProvider.requests) != 0 {
		t.Fatalf("default provider fallback must not be used, requests = %d", len(chatProvider.requests))
	}
}

func TestGenerateResponseTenThemesDoNotUseFallback(t *testing.T) {
	topics := []string{
		"郵便と古書店",
		"雨の文化祭前夜",
		"港町の倉庫街",
		"壊れたオルゴール",
		"深夜の自動販売機",
		"古い団地の掲示板",
		"地下鉄の忘れ物",
		"夏祭りの裏通り",
		"閉館後の図書室",
		"朝の市場と手紙",
	}
	responses := make([]string, 0, len(topics)*2)
	for _, topic := range topics {
		responses = append(responses,
			topic+"なら、最初に小さな違和感を一つ置くと話が入りやすいね。誰がそれに気づくかで空気が変わりそう。",
			topic+"で濡れた封筒が一つだけ残っていたら、隠した人の嘘まで見えてきそう。開けるか戻すか、そこで性格が出るよね。",
		)
	}
	provider := &capturingIdleProvider{responses: responses}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	for i, topic := range topics {
		got, err := o.generateResponse("mio", "shiro", "idle-ten-themes", i, i, topic)
		if err != nil {
			t.Fatalf("theme %d %q generateResponse() error = %v", i, topic, err)
		}
		if !strings.Contains(got, topic) {
			t.Fatalf("theme %d %q lost topic context: %q", i, topic, got)
		}
	}
	if len(provider.requests) != len(topics)*2 {
		t.Fatalf("requests = %d, want %d", len(provider.requests), len(topics)*2)
	}
}

func TestIdleFunScorePercentRanksConcreteHooksHigher(t *testing.T) {
	dull := "その話題は構造を考えると面白いですね。もう少し整理できそうです。"
	fun := "雨の古書店で宛先不明の手紙が棚から落ちたら、隠した人の嘘まで濡れて見えそう。しろなら、その封筒を開ける？"

	dullScore := idleFunScorePercent(dull, "", "", "郵便と古書店")
	funScore := idleFunScorePercent(fun, "", "", "郵便と古書店")

	if funScore <= dullScore {
		t.Fatalf("fun score should prefer concrete hook: fun=%d dull=%d", funScore, dullScore)
	}
	if funScore < 70 {
		t.Fatalf("concrete hook should score as clearly fun, got %d", funScore)
	}
}

func TestIdleCompactRetryMessagesBanEnglishOutput(t *testing.T) {
	messages := buildIdleCompactRetryMessages("mio", "郵便と古書店", "", "会話の最初の発話")
	joined := ""
	for _, msg := range messages {
		joined += msg.Content + "\n"
	}
	for _, want := range []string{"自然な日本語", "英語だけの応答", "英語の見出し", "英語での説明"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("compact retry messages do not contain %q:\n%s", want, joined)
		}
	}
}

func TestGenerateResponseRecoversEmptyTopicFromSessionMemory(t *testing.T) {
	provider := &capturingIdleProvider{response: "郵便と古書店なら、宛先不明の手紙を最初の手がかりにすると入れそう。しろなら、その手紙を誰が隠したと思う？"}
	memory := session.NewCentralMemory()
	memory.RecordMessage(domaintransport.NewMessage("user", "mio", "idle-empty-topic", "", "今日のお題（external）: 郵便と古書店"))
	o := NewIdleChatOrchestrator(provider, memory, []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	_, err := o.generateResponse("mio", "shiro", "idle-empty-topic", 0, 0, "")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("provider was not called")
	}
	last := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	if !strings.Contains(last, "話題: 郵便と古書店") {
		t.Fatalf("empty topic was not recovered from memory:\n%s", last)
	}
	if strings.Contains(last, "話題: \n") {
		t.Fatalf("empty topic reached prompt:\n%s", last)
	}
}

func TestGenerateResponseNeverPassesEmptyTopicToProvider(t *testing.T) {
	provider := &capturingIdleProvider{response: "この会話の現在のお題なら、まず具体的な入口を一つ決めると話しやすいです。みおなら、どの場面から始めますか？"}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	_, err := o.generateResponse("shiro", "mio", "idle-empty-topic-fallback", 0, 0, "")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("provider was not called")
	}
	last := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	if !strings.Contains(last, "話題: この会話の現在のお題") {
		t.Fatalf("fallback topic was not injected:\n%s", last)
	}
	if strings.Contains(last, "話題: \n") {
		t.Fatalf("empty topic reached prompt:\n%s", last)
	}
}

func TestGenerateResponseRejectsInternalReasoningLeak(t *testing.T) {
	provider := &capturingIdleProvider{
		responses: []string{
			"channel>thought\nユーザーは私（Mio）に対して、会話の制約を課している。\n1. **キャラクター**: Mio\n2. **目標**: 自然な返答。",
			"えー、その映写室で音が少し遅れて聞こえる瞬間、秘密の入口っぽくて気になるよ。shiroなら、そのズレを誰が仕込んだと思う？",
		},
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-leak", 0, 0, "小さな映画館の音響空間")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if strings.Contains(got, "channel>thought") || strings.Contains(got, "ユーザーは私") || strings.Contains(got, "制約") {
		t.Fatalf("internal reasoning leaked into response: %q", got)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected retry after leaked response, requests=%d", len(provider.requests))
	}
}

func TestGenerateResponseErrorsOnShiroEnglishReasoningLeak(t *testing.T) {
	provider := &capturingIdleProvider{
		responses: []string{
			"Okay, let's see. The user is asking me to respond as Shiro in two sentences. The task is to acknowledge the point and add a concrete example.",
			"その見方なら、道具の傷は使い手の失敗も直した回数も一緒に残している感じがあります。例えば修理屋の定規なら、削れた端だけで前の持ち主の癖まで見えてきそうです。",
		},
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("shiro", "mio", "idle-english-leak", 3, 3, "使い古された道具の魂")
	if err == nil {
		t.Fatalf("generateResponse() returned fallback/retry response instead of error: %q", got)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("shiro reasoning leak must fail without retry, requests=%d", len(provider.requests))
	}
}

func TestUnusableIdleResponseRejectsEnglishDominantReasoningFragment(t *testing.T) {
	raw := `Okay, let's see. The user is asking about what happens if the signal is missing.

The user's question: "その合図がなかったら、私たちの体はどう反応しちゃうんだろう？" So Shiro should explain the immune reaction.`
	sanitized := `The user's question: "その合図がなかったら、私たちの体はどう反応しちゃうんだろう？" So Shiro should explain the immune reaction.`

	if !unusableIdleResponse(raw, sanitized) {
		t.Fatalf("English-dominant reasoning fragment should be unusable: raw=%q sanitized=%q", raw, sanitized)
	}
}

func TestHasInternalReasoningLeakDetectsEnglishReasoning(t *testing.T) {
	raw := "Okay, let's see. The user is asking me to respond as Shiro in two sentences. The task is to acknowledge the point and add a concrete example."
	if !hasInternalReasoningLeak(raw) {
		t.Fatalf("English reasoning leak was not detected: %q", raw)
	}
}

func TestHasInternalReasoningLeakDetectsChineseReasoning(t *testing.T) {
	raw := "好的，我现在需要处理用户的请求。首先，需要确认Shiro的规则。可能的回应：「市場の端に置かれた手紙なら、午前のざわめきが宛先を隠していそうです。」"
	if !hasInternalReasoningLeak(raw) {
		t.Fatalf("Chinese reasoning leak was not detected: %q", raw)
	}
}

func TestSanitizeIdleResponseExtractsJapaneseQuoteFromChineseReasoning(t *testing.T) {
	raw := "好的，我现在需要处理用户的请求。可能的回应：「市場の端に置かれた手紙なら、午前のざわめきが宛先を隠していそうです。」"
	got := sanitizeIdleResponse(raw, "朝の市場と手紙")
	want := "市場の端に置かれた手紙なら、午前のざわめきが宛先を隠していそうです。"
	if got != want {
		t.Fatalf("sanitizeIdleResponse() = %q, want %q", got, want)
	}
}

func TestUnusableIdleResponseAllowsSanitizedJapaneseFromReasoningRaw(t *testing.T) {
	raw := "好的，我现在需要处理用户的请求。可能的回应：「市場の端に置かれた手紙なら、午前のざわめきが宛先を隠していそうです。」"
	sanitized := sanitizeIdleResponse(raw, "朝の市場と手紙")
	if unusableIdleResponse(raw, sanitized) {
		t.Fatalf("sanitized Japanese candidate should be usable: raw=%q sanitized=%q", raw, sanitized)
	}
}

func TestUnusableIdleResponseRejectsReasoningFragmentWithoutSentenceEnd(t *testing.T) {
	raw := "好的，我现在需要处理用户的请求。例えば「安全対策で警備員の数が限られている」"
	sanitized := sanitizeIdleResponse(raw, "夏祭りの裏通り")
	if !unusableIdleResponse(raw, sanitized) {
		t.Fatalf("reasoning fragment should be unusable: raw=%q sanitized=%q", raw, sanitized)
	}
}

func TestUnusableIdleResponseRejectsPromptInstructionCandidate(t *testing.T) {
	raw := "好的，我现在需要处理用户的请求。例えば「相手の案を整理し、次に起きそうな場面を一つ置く。」"
	sanitized := sanitizeIdleResponse(raw, "壊れたオルゴール")
	if !unusableIdleResponse(raw, sanitized) {
		t.Fatalf("prompt instruction candidate should be unusable: raw=%q sanitized=%q", raw, sanitized)
	}
}

func TestInvalidIdleResponseRejectsUnexpectedScript(t *testing.T) {
	got := invalidIdleResponse("えー、鍵とかも一緒かな？持って行ったって सोचाがんばってるんだよね。")
	if !got {
		t.Fatal("expected unexpected script response to be invalid")
	}
}

func TestInvalidIdleResponseRejectsShortUnfinishedJapaneseFragment(t *testing.T) {
	got := invalidIdleResponse("昨日、玄")
	if !got {
		t.Fatal("expected short unfinished Japanese fragment to be invalid")
	}
}

func TestInvalidIdleResponseRejectsLongUnfinishedJapaneseFragment(t *testing.T) {
	cases := []string{
		"商標登録の鍵がノートに隠されているなら、その鍵の所有者は発明者の死後にノートを引き継いだ人物かもしれない。それは、誰が書いたかという秘密が、実は誰かの利益になるという事実が、現",
		"全国の地域医療機関でAI診断が保険適用され",
		"保険適用拡大が実現すれば、地域医療の経営基盤が強化される。ただ、その結果として、医師",
	}
	for _, input := range cases {
		if !invalidIdleResponse(input) {
			t.Fatalf("expected unfinished response to be invalid: %q", input)
		}
	}
}

func TestGenerateResponseDoesNotAcceptUnfinishedShiroStyleRetry(t *testing.T) {
	provider := &capturingIdleProvider{
		responses: []string{
			"非常に興味深いですね。封筒の価値は、誰が最後に開けるかで変わる。",
			"商標登録の鍵がノートに隠されているなら、その鍵の所有者は発明者の死後にノートを引き継いだ人物かもしれない。それは、誰が書いたかという秘密が、実は誰かの利益になるという事実が、現",
		},
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("shiro", "mio", "idle-shiro-unfinished-style", 1, 1, "発明者の観測ノート")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if strings.Contains(got, "という事実が、現") {
		t.Fatalf("unfinished retry_style response was accepted: %q", got)
	}
	if got != "非常に興味深いですね。封筒の価値は、誰が最後に開けるかで変わる。" {
		t.Fatalf("valid primary should remain when style retry is unusable, got %q", got)
	}
}

func TestGenerateResponseErrorsWhenInternalReasoningPersists(t *testing.T) {
	provider := &capturingIdleProvider{
		response: "channel>thought\nユーザーは私（Mio）に対して、会話の制約を課している。\n1. **キャラクター**: Mio\n2. **目標**: 自然な返答。",
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-leak-error", 0, 0, "小さな映画館の音響空間")
	if err == nil {
		t.Fatalf("generateResponse() returned fallback instead of error: %q", got)
	}
}

func TestGenerateResponseDoesNotFallbackAfterRetryCycle(t *testing.T) {
	provider := &capturingIdleProvider{
		response: "channel>thought\nユーザーは私（Mio）に対して、会話の制約を課している。",
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-leak-stop", 8, 8, "映画館の映写担当")
	if err == nil {
		t.Fatalf("generateResponse() returned fallback instead of error: %q", got)
	}
}

func TestGenerateResponseRetriesTruncatedFinishReason(t *testing.T) {
	provider := &capturingIdleProvider{
		responses: []string{
			"小さな映画館の映写室で、音のズレに気づいた主人公が",
			"その音のズレ、映写室の床下に誰かが隠した古いスピーカーのせいかもね。shiroなら、最初にどこを調べる？",
		},
		finishReasons: []string{"length", "stop"},
	}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-truncated", 0, 0, "小さな映画館の音響空間")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if strings.Contains(got, "主人公が") {
		t.Fatalf("truncated response was accepted: %q", got)
	}
	if !strings.Contains(got, "古いスピーカー") {
		t.Fatalf("retry response was not used: %q", got)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected retry after truncated response, requests=%d", len(provider.requests))
	}
}

func TestNormalizeIdleTopicRejectsInternalReasoningLeak(t *testing.T) {
	raw := "channel>thought\nユーザーは私（Mio）に対して、お題生成の制約を確認している。"
	if got := normalizeIdleTopic(raw, false); got != "" {
		t.Fatalf("normalizeIdleTopic() = %q, want empty", got)
	}
}

func TestNormalizeIdleTopicExtractsFinalChannel(t *testing.T) {
	raw := "<|channel>thought\nまず候補を考える。\n<|channel>final\n古書店の棚に残る時間の質感"
	if got := normalizeIdleTopic(raw, false); got != "古書店の棚に残る時間の質感" {
		t.Fatalf("normalizeIdleTopic() = %q", got)
	}
}

func TestNormalizeIdleTopicRejectsTruncatedTopic(t *testing.T) {
	raw := "壊れたオルゴールの音色と麻雀卓の静寂が紡ぐ、取り"
	if got := normalizeIdleTopic(raw, true); got != "" {
		t.Fatalf("normalizeIdleTopic() = %q, want empty", got)
	}
}

func TestSanitizeIdleResponseExtractsFinalChannel(t *testing.T) {
	raw := "<|channel>thought\n制約を確認する。\n<|channel>final\nえー、その古書の紙の匂いから前の持ち主が見えてくる感じ、すごくいいね。shiroなら、最初にどの本を開く？"
	got := sanitizeIdleResponse(raw, "古書店")
	if strings.Contains(got, "thought") || strings.Contains(got, "制約") {
		t.Fatalf("reasoning leaked after sanitize: %q", got)
	}
	if !strings.Contains(got, "紙の匂い") {
		t.Fatalf("final answer was not extracted: %q", got)
	}
}

func TestSanitizeIdleResponseExtractsFinalAnswerAfterEnglishThink(t *testing.T) {
	raw := "Okay, let's think this through step by step.\nThe user wants a short Japanese reply.\nFinal answer: それ、閉館間際の温室で温度計だけが先に嘘をつく瞬間かも。shiroなら、その嘘を最初に誰が見破ると思う？"
	got := sanitizeIdleResponse(raw, "閉館間際の温室")
	if strings.Contains(strings.ToLower(got), "okay, let's") || strings.Contains(strings.ToLower(got), "final answer") {
		t.Fatalf("reasoning leaked after sanitize: %q", got)
	}
	if !strings.Contains(got, "温度計") {
		t.Fatalf("final answer was not extracted: %q", got)
	}
}

func TestSanitizeIdleResponseExtractsTrailingJapaneseBlock(t *testing.T) {
	raw := "Okay, let's reason carefully.\nThe user asks for a concise response.\n\nその鍵穴、雨で膨らんだ木が一晩だけ塞いでいたのかも。mioなら、朝一番でどこを触って確かめる？"
	got := sanitizeIdleResponse(raw, "古書店")
	if strings.Contains(strings.ToLower(got), "okay, let's") {
		t.Fatalf("reasoning leaked after sanitize: %q", got)
	}
	if !strings.Contains(got, "鍵穴") {
		t.Fatalf("trailing japanese block was not extracted: %q", got)
	}
}

func TestSanitizeIdleSummaryResponseExtractsFinalAnswer(t *testing.T) {
	raw := "Okay, let's see. I need to summarize.\nFinal answer: 一番面白かったのは、フィルムと筐体の記号が秘密の合図みたいに同期していた点です。記号の時代背景を軸にすると、次の論点が自然に広がります。"
	got := sanitizeIdleSummaryResponse(raw, "映写")
	if strings.Contains(strings.ToLower(got), "okay, let's") || strings.Contains(strings.ToLower(got), "final answer") {
		t.Fatalf("reasoning leaked after summary sanitize: %q", got)
	}
	if !strings.Contains(got, "一番面白かった") {
		t.Fatalf("summary body was not extracted: %q", got)
	}
}

func TestSanitizeIdleSummaryResponseRejectsReasoningOnly(t *testing.T) {
	raw := "Okay, let's see. The user wants me to summarize in Japanese with three points."
	got := sanitizeIdleSummaryResponse(raw, "映写")
	if got != "" {
		t.Fatalf("expected empty summary for reasoning-only output, got %q", got)
	}
}

func TestSanitizeIdleSummaryResponseDropsLeadingReasoningParagraphs(t *testing.T) {
	raw := "Okay, so the user wants me to summarize this idle chat in three points.\n\n一番面白かったのは、フィルムと筐体の記号同期が秘密の合図みたいに見えた点です。記号の時代背景を追うと、次の議論が自然に広がりそうです。"
	got := sanitizeIdleSummaryResponse(raw, "映写")
	if strings.Contains(strings.ToLower(got), "okay, so") {
		t.Fatalf("leading reasoning paragraph remained: %q", got)
	}
	if !strings.Contains(got, "一番面白かった") {
		t.Fatalf("summary body was not preserved: %q", got)
	}
}

func TestSanitizeIdleSummaryResponseDropsEnglishMetaReasoningAndKeepsBody(t *testing.T) {
	raw := "Wait, the user provided an IdleChat log and wants me to evaluate it. First, I need to understand the output format and the required fields.\n\n1. 錆びた鍵に隠された秘密のドアが気になる。"
	got := sanitizeIdleSummaryResponse(raw, "路地")
	if strings.Contains(strings.ToLower(got), "the user provided") || strings.Contains(strings.ToLower(got), "output format") {
		t.Fatalf("english meta reasoning remained: %q", got)
	}
	if !strings.Contains(got, "錆びた鍵") {
		t.Fatalf("summary body was not preserved: %q", got)
	}
}

func TestBuildIdleTurnPromptRequiresDialogueResponse(t *testing.T) {
	got := buildIdleTurnPrompt("郵便と古書店", "shiro", "古書店に届く宛先不明の手紙って、誰かの記憶みたいだね。", "配達記録が鍵になりそうです。", 1, 1, false)

	for _, want := range []string{
		"直前の相手発言",
		"直前の相手発言を受けて1〜2文",
		"具体物・理由・問い",
		"自然な日本語だけ",
		"英語や説明は書かない",
		"話者名・相手名",
		"メタ発言",
		"要約コピー",
		"文末は必ず完結",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt does not contain %q:\n%s", want, got)
		}
	}
}

func TestBuildIdleTurnPromptFinalTurnClosesWithoutNewQuestion(t *testing.T) {
	got := buildIdleTurnPrompt("郵便と古書店", "shiro", "古書店に届く宛先不明の手紙って、誰かの記憶みたいだね。", "配達記録が鍵になりそうです。", 11, 11, false)

	for _, want := range []string{
		"最後の発話",
		"問いを増やさず",
		"新しい問い",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("final turn prompt does not contain %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{
		"最後に残る問いを一つ置く",
		"余韻のある問いか感想",
		"具体物・理由・問いのどれか",
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("final turn prompt still allows a new question via %q:\n%s", banned, got)
		}
	}
}

func TestBuildIdleResponseGuardPromptBansEnglishOutput(t *testing.T) {
	got := buildIdleResponseGuardPrompt("mio", nil, nil)
	for _, want := range []string{"自然な日本語", "英語だけの応答", "英語の見出し", "英語での説明", "話者名・相手名", "言い直すと", "直前文の言い換えコピー"} {
		if !strings.Contains(got, want) {
			t.Fatalf("guard prompt does not contain %q:\n%s", want, got)
		}
	}
}

func TestSystemPromptKeepsOutputContractWithoutToneContract(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	got := o.getSystemPrompt("shiro")

	for _, want := range []string{
		"表示本文だけ",
		"自然な日本語",
		"英語の見出し",
		"IdleChat出力契約",
		"2〜3文まで",
		"一つの論点だけ前に進める",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt does not contain %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{
		"話し方契約",
		"語尾",
		"タメ口",
		"敬語テンプレ",
		"礼儀テンプレ",
		"賞賛",
		"非常に興味深いですね",
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("system prompt still contains tone contract %q:\n%s", banned, got)
		}
	}
}

func TestMioSystemPromptForcesIdleChatGalStyle(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	got := o.getSystemPrompt("mio")

	for _, want := range []string{
		"Mio IdleChat話し方契約",
		"最優先",
		"濃いギャル口調",
		"文頭を固定しない",
		"同じ開始表現を連続で使わず",
		"おけ",
		"それな",
		"ガチで",
		"めっちゃ",
		"〜じゃん",
		"〜っぽい",
		"出力前に本文だけを書き直す",
		"気がする",
		"かしら",
		"は禁止",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mio system prompt does not contain %q:\n%s", want, got)
		}
	}

	shiro := o.getSystemPrompt("shiro")
	if strings.Contains(shiro, "Mio IdleChat話し方契約") || strings.Contains(shiro, "濃いギャル口調") {
		t.Fatalf("shiro system prompt should not include Mio gal style contract:\n%s", shiro)
	}
	if strings.Contains(got, "文頭を「おけ、」「それな、」「ガチで」「めっちゃ」「やば、」「まじで」のいずれかにする") {
		t.Fatalf("mio system prompt should not force a tiny fixed set of sentence openings:\n%s", got)
	}
}

func TestGetSystemPromptPutsRuntimePolicyBeforeCommonPrompt(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, map[string]string{
		"shiro": "COMMON SHIRO SYSTEM PROMPT\n\n---\n\n# IdleChat補正\n\nIDLECHAT SHIRO CORRECTION",
	}, "")

	got := o.getSystemPrompt("shiro")
	commonIdx := strings.Index(got, "COMMON SHIRO SYSTEM PROMPT")
	correctionIdx := strings.Index(got, "IDLECHAT SHIRO CORRECTION")
	runtimeIdx := strings.Index(got, "この会話はidleChatです")
	if commonIdx < 0 || correctionIdx < 0 || runtimeIdx < 0 {
		t.Fatalf("system prompt missing common, idle correction, or runtime policy:\n%s", got)
	}
	if !(runtimeIdx < commonIdx && commonIdx < correctionIdx) {
		t.Fatalf("system prompt should be runtime policy first, then common Shiro prompt, then IdleChat correction:\n%s", got)
	}
}

func TestGenerateResponseKeepsOnlyFirstMessageAsSystem(t *testing.T) {
	provider := &capturingIdleProvider{responses: []string{
		"古書店の棚から宛先不明の手紙が落ちるなら、まず隠した人の癖が見えます。封筒を開ける前に、誰の字かだけ決めたいですね。",
		"雨でにじんだ宛名だけ先に読めるなら、隠した人より受け取るはずだった人が気になります。そこを一人に絞ると話が動きますね。",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, map[string]string{
		"shiro": "COMMON SHIRO SYSTEM PROMPT\n\n# IdleChat補正\n\nIDLECHAT SHIRO CORRECTION",
	}, "")
	o.sessionContext = "SESSION CONTEXT SHOULD STAY IN FIRST SYSTEM"
	o.SetRecentTopicProvider(func(context.Context, int) ([]string, error) {
		return []string{"RECENT TOPIC SHOULD STAY IN FIRST SYSTEM"}, nil
	})

	_, err := o.generateResponse("shiro", "mio", "idle-system-role-layout", 1, 1, "郵便と古書店")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(provider.requests))
	}
	for reqIndex, req := range provider.requests {
		for msgIndex, msg := range req.Messages {
			if msgIndex == 0 {
				if msg.Role != "system" {
					t.Fatalf("request %d first role = %q, want system", reqIndex, msg.Role)
				}
				if !strings.Contains(msg.Content, "COMMON SHIRO SYSTEM PROMPT") ||
					!strings.Contains(msg.Content, "IDLECHAT SHIRO CORRECTION") ||
					!strings.Contains(msg.Content, "SESSION CONTEXT SHOULD STAY IN FIRST SYSTEM") ||
					!strings.Contains(msg.Content, "RECENT TOPIC SHOULD STAY IN FIRST SYSTEM") {
					t.Fatalf("request %d first system did not contain all persistent context:\n%s", reqIndex, msg.Content)
				}
				continue
			}
			if msg.Role == "system" {
				t.Fatalf("request %d message %d should not be system: %#v", reqIndex, msgIndex, msg)
			}
		}
	}
}

func TestLatestOtherUtteranceUsesPreviousSpeakerLine(t *testing.T) {
	memory := session.NewCentralMemory()
	memory.RecordMessage(domaintransport.NewMessage("user", "mio", "idle-dialogue-context", "", "今日のお題（external）: 郵便と古書店"))
	memory.RecordMessage(domaintransport.NewMessage("mio", "shiro", "idle-dialogue-context", "", "古書店に届く宛先不明の手紙って、誰かの記憶みたいだね。"))

	got := latestOtherUtterance(memory.GetUnifiedView(10), "idle-dialogue-context", "shiro")
	if got != "古書店に届く宛先不明の手紙って、誰かの記憶みたいだね。" {
		t.Fatalf("latestOtherUtterance() = %q", got)
	}
}

func TestGenerateResponseErrorsWhenIdleLLMTimesOut(t *testing.T) {
	provider := &capturingIdleProvider{block: true}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	old := idleChatLLMGenerateTimeout
	idleChatLLMGenerateTimeout = 10 * time.Millisecond
	defer func() { idleChatLLMGenerateTimeout = old }()

	got, err := o.generateResponse("shiro", "mio", "idle-dialogue-timeout", 1, 1, "郵便と古書店")
	if err == nil {
		t.Fatalf("generateResponse() returned fallback instead of error: %q", got)
	}
}
