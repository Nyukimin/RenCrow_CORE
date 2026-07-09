package idlechat

import (
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

// simpleStoryTales は簡易版物語生成で使う昔話リスト。
var simpleStoryTales = []struct {
	title    string
	synopsis string
}{
	{"桃太郎", "川から桃が流れてきて生まれた子が、犬・猿・雉を仲間に鬼ヶ島へ鬼退治に行く"},
	{"一寸法師", "親指ほどの小さな武士が針を刀に都へ上り、鬼を倒して打ち出の小槌で大きくなる"},
	{"浦島太郎", "亀を助けた漁師が竜宮城へ招かれ、帰ると何百年も経っていて老人になる"},
	{"かぐや姫", "竹から生まれた娘が貴族たちの求婚を難題で退け、月へ帰っていく"},
	{"鶴の恩返し", "助けた鶴が娘に化けて機を織るが、見ることを禁じられた部屋を覗かれて去る"},
	{"舌切り雀", "親切な翁が舌を切られた雀を助け、意地悪な婆が欲張って痛い目に遭う"},
	{"花咲かじいさん", "犬の教えで金を掘り当てた翁が、灰で枯れ木に花を咲かせて殿様に褒められる"},
	{"さるかに合戦", "猿に騙されたカニの子が栗・蜂・臼と協力して仇を討つ"},
	{"笠地蔵", "雪の中の地蔵に笠をかぶせた翁夫婦の元へ、夜中に宝物が届く"},
	{"金太郎", "山で熊と相撲を取って育った怪力の子が、坂田金時として武将に仕える"},
}

const simpleStorySystemPrompt = `あなたは昔話リメイク作家です。ユーザーの指示に従って、笑えるくらい大袈裟で面白い短編を書いてください。`

// simpleStoryUserPrompt は1回のLLM呼び出しで物語全文を生成するプロンプト。
func simpleStoryUserPrompt(tale struct {
	title    string
	synopsis string
}, protagonist string) string {
	return fmt.Sprintf(`昔話「%s」を、主人公を「%s」に置き換えてリメイクしてください。

元の話のあらすじ: %s

条件:
- 主人公が「%s」になったことで、世界設定・社会の常識・登場人物の反応もすべて大胆に変わる
- 元の話の骨格（事件 → 解決 → オチ）は残す
- テンポよく、会話と描写を交えて
- 大げさなくらい面白く仕上げる（笑えるくらいでよい）
- 2000文字前後
- タイトルは1行目に「【タイトル】」形式で書く
- 本文のみ出力（解説・メタ発言不要）`, tale.title, protagonist, tale.synopsis, protagonist)
}

// protagonistOptions は主人公改変の候補リスト。
var protagonistOptions = []string{
	"AIロボット",
	"サラリーマン",
	"宅配業者",
	"YouTuber",
	"コンビニ店員",
	"定年退職したおじいさん",
	"高校生",
	"宇宙人",
	"忍者",
	"猫",
	"ドラゴン",
	"魔法使い見習い",
	"探偵",
	"料理人",
}

// StartSimpleStoryMode は簡易版物語モードを手動起動する。
func (o *IdleChatOrchestrator) StartSimpleStoryMode() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.participants) < 1 {
		return fmt.Errorf("idlechat requires at least 1 participant")
	}
	if o.chatActive {
		return fmt.Errorf("chat session already active")
	}
	o.disabled = false
	o.manualMode = false
	o.chatActive = true
	o.sessionMode = "story-simple"
	o.currentTopic = idleChatPendingTopic("story-simple")
	o.beginIdleRunLocked()
	o.lastActivity = time.Now()
	log.Println("[SimpleStory] Simple story mode started")
	return nil
}

// RunSimpleStorySession はCoder2（forecastProvider）を使った簡易版物語生成セッション。
// ワンプロンプトで昔話の主人公改変物語を生成し、Viewer に段落単位で配信する。
func (o *IdleChatOrchestrator) RunSimpleStorySession() {
	sessionID := fmt.Sprintf("story-simple-%d", time.Now().Unix())
	startedAt := time.Now().In(jst)

	o.mu.Lock()
	o.chatActive = true
	o.sessionMode = "story-simple"
	generation := o.beginIdleRunLocked()
	o.activeSessionID = sessionID
	o.mu.Unlock()

	defer func() {
		o.mu.Lock()
		if o.activeGeneration == generation {
			o.chatActive = false
			o.sessionMode = ""
			o.currentTopic = ""
			o.activeSessionID = ""
		}
		o.lastActivity = time.Now()
		o.mu.Unlock()
		o.cancelIdleRunIfGeneration(generation)
	}()

	// 昔話と主人公をランダム選択
	tale := simpleStoryTales[rand.Intn(len(simpleStoryTales))]
	protagonist := protagonistOptions[rand.Intn(len(protagonistOptions))]

	log.Printf("[SimpleStory] Generating: %s × %s", tale.title, protagonist)

	storyTopicResult := buildSimpleStoryTopicResult(tale.title, protagonist)
	storyTopic := storyTopicResult.Topic
	o.mu.Lock()
	o.currentTopic = storyTopic
	o.sessionContext = formatTopicGenerationContext(storyTopicResult)
	copiedStoryTopic := storyTopicResult
	o.currentTopicResult = &copiedStoryTopic
	o.mu.Unlock()

	// LLM生成が長くても、Viewer には開始直後に状態を見せる。
	intro := fmt.Sprintf("今夜の物語です。『%s』を、主人公を%sに置き換えたら——", tale.title, protagonist)
	transcript := []string{"mio: " + intro}
	storyUtteranceSeq := 0
	o.emitStoryParagraph(sessionID, intro, &storyUtteranceSeq)

	messages := []llm.Message{
		{Role: "system", Content: simpleStorySystemPrompt},
		{Role: "user", Content: simpleStoryUserPrompt(tale, protagonist)},
	}

	provider := o.forecastLLM()
	resp, err := provider.Generate(o.idleRunContext(), llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   2500,
		Temperature: 0.9,
	})
	if err != nil {
		log.Printf("[SimpleStory] generation failed: %v", err)
		o.saveSimpleStoryReview(sessionID, storyTopic, tale.title, protagonist, "", "", transcript, startedAt, "generation_error")
		return
	}
	if !o.isIdleSessionActive(sessionID, generation) {
		log.Printf("[SimpleStory] response discarded after interrupt: session=%s", sessionID)
		return
	}
	logIdleRaw("story_simple.generate", resp.Content)

	raw := strings.TrimSpace(resp.Content)
	if raw == "" {
		log.Printf("[SimpleStory] empty response")
		o.saveSimpleStoryReview(sessionID, storyTopic, tale.title, protagonist, "", "", transcript, startedAt, "invalid_response")
		return
	}

	// タイトル行と本文を分離
	titleLine := ""
	bodyLines := make([]string, 0)
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i == 0 && (strings.HasPrefix(line, "【") || strings.HasPrefix(line, "#")) {
			titleLine = strings.TrimPrefix(strings.TrimPrefix(line, "#"), " ")
			titleLine = strings.Trim(titleLine, "【】")
		} else {
			bodyLines = append(bodyLines, line)
		}
	}
	body := strings.Join(bodyLines, "\n")

	if titleLine != "" {
		titleSpeech := fmt.Sprintf("改題は『%s』。", titleLine)
		transcript = append(transcript, "mio: "+titleSpeech)
		o.emitStoryParagraph(sessionID, titleSpeech, &storyUtteranceSeq)
	}

	// 本文を段落単位でViewerに配信
	for _, para := range groupStoryIntoViewerParagraphs(body, 150) {
		transcript = append(transcript, "mio: "+para)
		o.emitStoryParagraph(sessionID, para, &storyUtteranceSeq)
	}

	// 締め
	closing := fmt.Sprintf("『%s』を下敷きにした、主人公%sのお話でした。", tale.title, protagonist)
	transcript = append(transcript, "mio: "+closing)
	o.emitStoryParagraph(sessionID, closing, &storyUtteranceSeq)
	o.saveSimpleStoryReview(sessionID, storyTopic, tale.title, protagonist, titleLine, body, transcript, startedAt, "")

	log.Printf("[SimpleStory] Session complete: %s × %s", tale.title, protagonist)
}

type simpleStoryValidationResult struct {
	Valid  bool
	Reason string
}

func validateSimpleStoryDraft(sourceTitle, protagonist, storyTitle, storyText string) simpleStoryValidationResult {
	body := strings.TrimSpace(storyText)
	if utf8.RuneCountInString(body) < 120 {
		return simpleStoryValidationResult{Reason: "too_short"}
	}
	if !strings.Contains(body, protagonist) && !strings.Contains(storyTitle, protagonist) {
		return simpleStoryValidationResult{Reason: "missing_protagonist"}
	}
	if containsHypotheticalFrame(body) || containsHypotheticalFrame(storyTitle) {
		return simpleStoryValidationResult{Reason: "hypothetical_frame"}
	}
	if containsAnyStoryMetaText(body) {
		return simpleStoryValidationResult{Reason: "meta_text"}
	}
	if strings.TrimSpace(sourceTitle) == "" {
		return simpleStoryValidationResult{Reason: "missing_source"}
	}
	return simpleStoryValidationResult{Valid: true}
}

func containsHypotheticalFrame(text string) bool {
	normalized := strings.ReplaceAll(text, " ", "")
	return strings.Contains(normalized, "もし") &&
		(strings.Contains(normalized, "だったら") || strings.Contains(normalized, "なら"))
}

func containsAnyStoryMetaText(text string) bool {
	return strings.Contains(text, "本文のみ") ||
		strings.Contains(text, "解説") ||
		strings.Contains(text, "メタ発言") ||
		strings.Contains(text, "条件:")
}

func buildSimpleStoryTopicResult(sourceTitle, protagonist string) TopicGenerationResult {
	sourceTitle = strings.TrimSpace(sourceTitle)
	protagonist = strings.TrimSpace(protagonist)
	topic := fmt.Sprintf("%sを、主人公%sの視点で語り直す物語", sourceTitle, protagonist)
	candidate := TopicCandidate{
		Topic:               topic,
		InterestingnessAxis: modulechat.ExpectedAxisByCategory[TopicCategoryStory],
		OpeningHook:         fmt.Sprintf("%sの骨格を残しつつ、%sなら何を見落とさないかを拾う", sourceTitle, protagonist),
		Avoid:               "元話のあらすじ紹介だけで終わらせない",
	}
	seed := TopicSeed{
		Category:       TopicCategoryStory,
		StoryBase:      sourceTitle,
		StoryTransform: protagonist,
	}
	if err := modulechat.ValidateTopicCandidate(TopicCategoryStory, seed, candidate); err != nil {
		log.Printf("[SimpleStory] story topic contract violation: %v", err)
	}
	return TopicGenerationResult{
		Topic:               candidate.Topic,
		Category:            TopicCategoryStory,
		Strategy:            modulechat.StrategyFromTopicCategory(TopicCategoryStory),
		InterestingnessAxis: candidate.InterestingnessAxis,
		OpeningHook:         candidate.OpeningHook,
		Avoid:               candidate.Avoid,
		Seed:                seed,
		Candidates:          []TopicCandidate{candidate},
		Provider:            "story-simple",
	}
}

func (o *IdleChatOrchestrator) saveSimpleStoryReview(sessionID, topic, sourceTitle, protagonist, storyTitle, storyText string, transcript []string, startedAt time.Time, loopReason string) {
	endedAt := time.Now().In(jst)
	summary := fmt.Sprintf("物語モード: %sを主人公%sでリメイク。", sourceTitle, protagonist)
	qualityReview, promptGuidance := o.reviewSessionEnd(topic, "story-simple", transcript, summary, loopReason)
	record := SessionSummary{
		SessionID:       sessionID,
		Title:           fmt.Sprintf("%d月%d日の%s", endedAt.Month(), endedAt.Day(), topic),
		Topic:           topic,
		Category:        TopicCategoryStory,
		Strategy:        TopicStrategy("story-simple"),
		Summary:         summary,
		QualityReview:   qualityReview,
		PromptGuidance:  promptGuidance,
		SourceTitle:     sourceTitle,
		StoryTitle:      storyTitle,
		StoryText:       storyText,
		StartedAt:       startedAt.Format(time.RFC3339),
		EndedAt:         endedAt.Format(time.RFC3339),
		Turns:           len(transcript),
		LoopRestarted:   loopReason != "",
		LoopReason:      loopReason,
		TopicProvider:   "story-simple",
		SummaryProvider: "quality-review",
		Transcript:      append([]string(nil), transcript...),
	}
	o.mu.Lock()
	o.history = append(o.history, record)
	if len(o.history) > 200 {
		o.history = o.history[len(o.history)-200:]
	}
	o.addPromptGuideLocked(promptGuidance)
	store := o.topicStore
	o.mu.Unlock()
	if store != nil {
		if err := store.Append(record); err != nil {
			log.Printf("[SimpleStory] topic store append failed: %v", err)
		}
	}
}

// emitStoryParagraph は段落をViewer + TTSに配信する（story_mode.goから移植）
func (o *IdleChatOrchestrator) emitStoryParagraph(sessionID, paragraph string, utteranceSeq *int) {
	paragraph = strings.TrimSpace(paragraph)
	if paragraph == "" {
		return
	}
	// memory に段落単位で記録
	msg := domaintransport.NewMessage("mio", "user", sessionID, "", paragraph)
	msg.Type = domaintransport.MessageTypeIdleChat
	o.memory.RecordMessage(msg)
	// Viewer に段落全体を1件送る（TTS なし）
	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.viewer",
		From:      "mio",
		To:        "user",
		Content:   paragraph,
		SessionID: sessionID,
	})
	// TTS に文節単位で送る（Viewer には表示しない）
	for _, sentence := range splitStorySentences(paragraph) {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		if utteranceSeq != nil {
			(*utteranceSeq)++
		}
		turnIndex := 1
		if utteranceSeq != nil && *utteranceSeq > 0 {
			turnIndex = *utteranceSeq
		}
		messageID := fmt.Sprintf("%s:story:%04d", sessionID, turnIndex)
		ttsEvent := TimelineEvent{
			Type:      "idlechat.tts",
			From:      "mio",
			To:        "user",
			Content:   sentence,
			SessionID: sessionID,
			MessageID: messageID,
			TurnIndex: turnIndex,
		}
		ttsDone := o.emitTimelineEvent(ttsEvent)
		o.waitForTTSDoneForEvent(ttsEvent, ttsDone)
		o.waitBreak(speakerBreak)
	}
}

// groupStoryIntoViewerParagraphs はストーリーテキストを指定文字数で段落に分割（story_mode.goから移植）
func groupStoryIntoViewerParagraphs(text string, targetRunes int) []string {
	sentences := splitStorySentences(strings.TrimSpace(text))
	var out []string
	var buf strings.Builder
	for _, s := range sentences {
		sLen := utf8.RuneCountInString(s)
		bufLen := utf8.RuneCountInString(buf.String())
		if buf.Len() > 0 && bufLen+sLen > targetRunes {
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
		}
		buf.WriteString(s)
	}
	if buf.Len() > 0 {
		out = append(out, strings.TrimSpace(buf.String()))
	}
	return out
}

// splitStorySentences は文節区切りで分割（story_mode.goから移植）
func splitStorySentences(story string) []string {
	runes := []rune(story)
	n := len(runes)
	var sentences []string
	start := 0
	for i := 0; i < n; i++ {
		switch runes[i] {
		case '。', '！', '？', '\n':
			end := i + 1
			// 直後の行頭禁則文字を前の文節に含める（禁則処理）
			for end < n && isStoryLineHeadForbidden(runes[end]) {
				end++
			}
			part := strings.TrimSpace(string(runes[start:end]))
			if part != "" {
				sentences = append(sentences, part)
			}
			start = end
			i = end - 1
		}
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		sentences = append(sentences, tail)
	}
	return sentences
}

// isStoryLineHeadForbidden は行頭禁則文字かどうかを返す（story_mode.goから移植）
func isStoryLineHeadForbidden(r rune) bool {
	switch r {
	case '、', '。', '！', '？', '」', '』', '）', ')', '…', '‥', '・', '：', '；', 'ー', '～', '〜':
		return true
	}
	return false
}
