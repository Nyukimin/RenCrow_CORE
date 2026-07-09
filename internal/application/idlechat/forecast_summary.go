package idlechat

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

// saveForecastSummary は Coder2 で要約+継続考察テーマを生成して保存する。
func (o *IdleChatOrchestrator) saveForecastSummary(sessionID string, domain ForecastDomain, topic string, transcript []string, startedAt, endedAt time.Time, turns int, loopRestarted bool, loopReason string) string {
	summary := o.summarizeByForecastLLM(domain, topic, transcript)
	summary = annotateLoopSummary(summary, loopRestarted, loopReason)
	fullTopic := fmt.Sprintf("[%s] %s", domain.Name, topic)
	qualityReview, promptGuidance := o.reviewSessionEnd(fullTopic, fmt.Sprintf("forecast/%s", domain.Name), transcript, summary, loopReason)
	title := fmt.Sprintf("%d月%d日の%sの話題まとめ", endedAt.Month(), endedAt.Day(), truncate(fullTopic, 24))
	record := SessionSummary{
		SessionID:       sessionID,
		Title:           title,
		Topic:           fullTopic,
		Category:        TopicCategoryForecast,
		Strategy:        TopicStrategy(fmt.Sprintf("forecast/%s", domain.Name)),
		Summary:         summary,
		QualityReview:   qualityReview,
		PromptGuidance:  promptGuidance,
		StartedAt:       startedAt.Format(time.RFC3339),
		EndedAt:         endedAt.Format(time.RFC3339),
		Turns:           turns,
		LoopRestarted:   loopRestarted,
		LoopReason:      loopReason,
		TopicProvider:   "forecast",
		SummaryProvider: "shiro",
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
			log.Printf("[Forecast] topic store append failed: %v", err)
		}
	}

	// タイムラインに要約を emit
	msg := domaintransport.NewMessage("shiro", "forecast_summary", sessionID, "", title+"\n"+summary)
	msg.Type = domaintransport.MessageTypeIdleChat
	turnIndex := o.nextIdleChatTurnIndex(sessionID)
	messageID := idleChatMessageID(sessionID, turnIndex)
	msg.Context = idleChatMessageContext(messageID, turnIndex)
	o.memory.RecordMessage(msg)
	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.summary",
		From:      "shiro",
		To:        "forecast_summary",
		Content:   title + "\n" + summary,
		SessionID: sessionID,
		MessageID: messageID,
		TurnIndex: turnIndex,
	})
	return summary
}

// summarizeByForecastLLM は Coder2 で未来展望ディスカッションを要約し、継続考察テーマを付与する。
func (o *IdleChatOrchestrator) summarizeByForecastLLM(domain ForecastDomain, topic string, transcript []string) string {
	if len(transcript) == 0 {
		return "会話ログがありません。"
	}
	body := strings.Join(transcript, "\n")
	messages := []llm.Message{
		{Role: "system", Content: "あなたは未来予測・社会分析の専門家です。議論を的確に要約し、さらに深掘りすべき論点を提示してください。"},
		{Role: "user", Content: fmt.Sprintf(`以下は「%s」分野の未来展望ディスカッションです。

話題: %s

%s

以下の形式で要約してください:

## 議論の要約
- 主要な論点と結論を3〜5点で簡潔に

## 注目すべき視点
- 議論の中で特に鋭かった指摘や新しい切り口を1〜2点

## 継続考察テーマ
この議論を踏まえて、次に掘り下げるべきテーマを3つ提案してください:
1. （テーマ名）: 一行説明
2. （テーマ名）: 一行説明
3. （テーマ名）: 一行説明`, domain.Name, topic, body)},
	}
	req := llm.GenerateRequest{Messages: messages, MaxTokens: idleChatShiroSummaryMaxTokens, Temperature: 0.4}
	resp, err := o.providerForSpeaker("shiro").Generate(o.idleRunContext(), req)
	if err != nil || strings.TrimSpace(resp.Content) == "" {
		log.Printf("[Forecast] Summary generation failed (worker): %v", err)
		if err == nil {
			logIdleRaw("forecast.summary.generate", resp.Content)
		}
		return truncate(body, 200)
	}
	logIdleRaw("forecast.summary.generate", resp.Content)
	summary := sanitizeIdleSummaryResponse(resp.Content, topic)
	if summary == "" {
		log.Printf("[Forecast] Summary sanitize failed; raw=%q", truncate(strings.TrimSpace(resp.Content), 180))
		return truncate(body, 200)
	}
	return summary
}

// extractCoveredThemes は直近のトランスクリプトから新たに出た論点をキーワードリストとして抽出する。
func (o *IdleChatOrchestrator) extractCoveredThemes(domain ForecastDomain, topic string, transcript []string, existingThemes []string) []string {
	if len(transcript) < forecastCheckpointInterval {
		return nil
	}
	window := transcript
	if len(window) > forecastCheckpointInterval {
		window = window[len(window)-forecastCheckpointInterval:]
	}
	body := strings.Join(window, "\n")

	existingSection := ""
	if len(existingThemes) > 0 {
		existingSection = fmt.Sprintf("\n\n既に記録済みの論点（繰り返し禁止）:\n- %s", strings.Join(existingThemes, "\n- "))
	}

	messages := []llm.Message{
		{Role: "system", Content: "あなたは議論の進行管理者です。既出論点を正確に記録します。"},
		{Role: "user", Content: fmt.Sprintf(`以下は「%s」の議論（直近%dターン）です。

話題: %s
%s

会話ログ:
%s

この区間で新たに出た論点・主張を、1行1項目の箇条書きで抽出してください。
- 各項目は10〜20文字程度の短いキーワード/フレーズ
- 既に記録済みの論点と重複するものは除外
- 最大5項目
- 箇条書き（「- 」始まり）のみ出力、それ以外の文は不要`, domain.Name, len(window), topic, existingSection, body)},
	}
	resp, err := o.providerForSpeaker("shiro").Generate(o.idleRunContext(), llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   idleChatQualityReviewMaxTokens,
		Temperature: 0.3,
	})
	if err != nil {
		log.Printf("[Forecast] Theme extraction failed (worker): %v", err)
		return nil
	}
	logIdleRaw("forecast.theme_extract.generate", resp.Content)

	var themes []string
	for _, line := range strings.Split(resp.Content, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "・")
		line = strings.TrimSpace(line)
		if line != "" && len(themes) < 5 {
			themes = append(themes, line)
		}
	}
	return themes
}

// updateForecastSessionContext は蓄積された既出テーマを sessionContext に反映する。
// これにより generateResponse の全ターンで既出テーマが LLM に見える。
func (o *IdleChatOrchestrator) updateForecastSessionContext(domain ForecastDomain, topic string, coveredThemes []string) {
	if len(coveredThemes) == 0 {
		return
	}
	ctx := fmt.Sprintf(`【%s 議論ガード】話題: %s
以下は既に議論済みの論点です。これらの繰り返しや言い換えは厳禁です。必ず新しい視点・具体例・反論で議論を前に進めてください。

既出論点:
- %s

禁止: 上記の論点を別の言葉で言い直すこと、同じ結論に戻ること。
必須: 毎回、直前の発言に対して「新しい事実」「別の立場からの反論」「具体的な数字や事例」のいずれかを加えること。`,
		domain.Name, topic, strings.Join(coveredThemes, "\n- "))

	o.mu.Lock()
	o.sessionContext = ctx
	o.mu.Unlock()
}

// forecastLLM は未来展望セッション用の LLM を返す。forecastProvider があればそれを、なければ mio を使う。
func (o *IdleChatOrchestrator) forecastLLM() llm.LLMProvider {
	p, _ := o.forecastLLMInfo()
	return p
}

func (o *IdleChatOrchestrator) forecastLLMInfo() (llm.LLMProvider, string) {
	if p, label := o.forecastPrimaryLLMInfo(); p != nil {
		return p, label
	}
	p := o.providerForSpeaker("mio")
	label := "mio"
	if p != nil && strings.TrimSpace(p.Name()) != "" {
		label = "mio " + strings.TrimSpace(p.Name())
	}
	return p, label
}

func (o *IdleChatOrchestrator) forecastPrimaryLLMInfo() (llm.LLMProvider, string) {
	o.mu.Lock()
	p := o.forecastProvider
	label := strings.TrimSpace(o.forecastProviderLabel)
	o.mu.Unlock()
	if p != nil {
		if label == "" {
			label = strings.TrimSpace(p.Name())
		}
		if label == "" {
			label = "forecastProvider"
		}
		return p, label
	}
	return nil, ""
}
