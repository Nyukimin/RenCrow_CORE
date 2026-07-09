package idlechat

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func detectLoopReason(transcript []string) string {
	if reason := detectShortLoopReason(transcript); reason != "" {
		return reason
	}
	if len(transcript) < 6 {
		return ""
	}
	norm := normalizeLoopText
	last := norm(transcript[len(transcript)-1])
	if last == "" {
		return ""
	}
	count := 0
	for i := len(transcript) - 4; i < len(transcript)-1; i++ {
		if i >= 0 && norm(transcript[i]) == last {
			count++
		}
	}
	if count >= 1 {
		return "exact_repeat"
	}
	if hasAlternatingLoop(transcript) {
		return "alternating_repeat"
	}
	if hasSpeakerTemplateLoop(transcript) {
		return "template_repeat"
	}
	if hasHighSimilarityLoop(transcript) {
		return "high_similarity"
	}
	if isWhatIfRepetition(transcript) {
		return "what_if_repeat"
	}
	return ""
}

func detectShortLoopReason(transcript []string) string {
	if len(transcript) < 4 {
		return ""
	}
	if hasShortAlternatingLoop(transcript) {
		return "short_alternating_repeat"
	}
	if hasShortSpeakerTemplateLoop(transcript) {
		return "short_template_repeat"
	}
	if hasShortHighSimilarityLoop(transcript) {
		return "short_high_similarity"
	}
	return ""
}

func isWhatIfRepetition(transcript []string) bool {
	if len(transcript) < 6 {
		return false
	}
	start := len(transcript) - 8
	if start < 0 {
		start = 0
	}
	repeated := 0
	for i := start; i < len(transcript); i++ {
		line := strings.ToLower(transcript[i])
		if strings.Contains(line, "もし") && (strings.Contains(line, "だったら") || strings.Contains(line, "なら")) {
			repeated++
		}
	}
	// 直近発話の半数以上が「もし〜だったら/なら」ならループとみなす。
	window := len(transcript) - start
	return repeated >= 4 && repeated*2 >= window
}

func (o *IdleChatOrchestrator) speakSummary(sessionID, summary string) <-chan struct{} {
	if strings.TrimSpace(summary) == "" {
		return nil
	}
	o.waitBreak(topicBreak)
	spokenSummary := "今回のまとめです。\n" + strings.TrimSpace(summary)
	turnIndex := o.nextIdleChatTurnIndex(sessionID)
	messageID := idleChatMessageID(sessionID, turnIndex)
	msg := domaintransport.NewMessage("mio", "user", sessionID, "", spokenSummary)
	msg.Type = domaintransport.MessageTypeIdleChat
	msg.Context = idleChatMessageContext(messageID, turnIndex)
	o.memory.RecordMessage(msg)
	ttsDone := o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "user",
		Content:   spokenSummary,
		SessionID: sessionID,
		MessageID: messageID,
		TurnIndex: turnIndex,
	})
	log.Printf("[IdleChat] Mio reading summary: %s", truncate(spokenSummary, 80))
	o.waitForTTSDoneForEvent(TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "user",
		Content:   spokenSummary,
		SessionID: sessionID,
		MessageID: messageID,
		TurnIndex: turnIndex,
	}, ttsDone)
	o.waitBreak(topicBreak)
	return ttsDone
}

func annotateLoopSummary(summary string, loopRestarted bool, loopReason string) string {
	if !loopRestarted || strings.TrimSpace(loopReason) == "" {
		return summary
	}
	note := loopReasonLabel(loopReason)
	if note == "" {
		return summary
	}
	if strings.TrimSpace(summary) == "" {
		return "注記: " + note
	}
	return "注記: " + note + "\n\n" + summary
}

func loopReasonLabel(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	switch reason {
	case "short_template_repeat":
		return "短周期テンプレ反復で即打ち切り"
	case "short_alternating_repeat":
		return "短周期の交互反復で即打ち切り"
	case "short_high_similarity":
		return "短周期の類似反復で即打ち切り"
	case "template_repeat":
		return "テンプレ反復で打ち切り"
	case "alternating_repeat":
		return "交互反復で打ち切り"
	case "exact_repeat", "high_similarity", "pre_emit_similarity":
		return "類似発話の反復で打ち切り"
	case "what_if_repeat":
		return "仮定表現の反復で打ち切り"
	case "topic_turn_limit":
		return ""
	case "interrupted":
		return "中断で終了"
	case "generation_error":
		return "生成エラーで終了"
	case "invalid_response":
		return "返答崩れで終了"
	default:
		return "反復検知で打ち切り"
	}
}

func (o *IdleChatOrchestrator) formatHintsFromLatestSession(entries []session.ConversationEntry, match func(domaintransport.Message) bool, fallback string) string {
	parts := collectLatestSessionSnippets(entries, match, 3)
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, " / ")
}

func (o *IdleChatOrchestrator) isLooping(transcript []string) bool {
	return detectLoopReason(transcript) != ""
}

func (o *IdleChatOrchestrator) saveSummary(sessionID, topic string, strategy TopicStrategy, transcript []string, startedAt, endedAt time.Time, turns int, loopRestarted bool, loopReason string) string {
	summary := o.summarizeByWorker(topic, transcript)
	summary = annotateLoopSummary(summary, loopRestarted, loopReason)
	qualityReview, promptGuidance := o.reviewSessionEnd(topic, string(strategy), transcript, summary, loopReason)
	title := fmt.Sprintf("%d月%d日の%sの話題まとめ", endedAt.Month(), endedAt.Day(), truncate(topic, 24))
	category, _ := modulechat.NormalizeTopicCategory(string(strategy))
	record := SessionSummary{
		SessionID:       sessionID,
		Title:           title,
		Topic:           topic,
		Category:        category,
		Strategy:        strategy,
		Summary:         summary,
		QualityReview:   qualityReview,
		PromptGuidance:  promptGuidance,
		StartedAt:       startedAt.Format(time.RFC3339),
		EndedAt:         endedAt.Format(time.RFC3339),
		Turns:           turns,
		LoopRestarted:   loopRestarted,
		LoopReason:      loopReason,
		TopicProvider:   "mio",
		SummaryProvider: "shiro",
		Transcript:      append([]string(nil), transcript...),
	}
	o.mu.Lock()
	o.history = append(o.history, record)
	if len(o.history) > 200 {
		o.history = o.history[len(o.history)-200:]
	}
	store := o.topicStore
	o.mu.Unlock()
	if store != nil {
		if err := store.Append(record); err != nil {
			log.Printf("[IdleChat] topic store append failed: %v", err)
		}
	}

	msg := domaintransport.NewMessage("shiro", "idlechat_summary", sessionID, "", title+"\n"+summary)
	msg.Type = domaintransport.MessageTypeIdleChat
	turnIndex := o.nextIdleChatTurnIndex(sessionID)
	messageID := idleChatMessageID(sessionID, turnIndex)
	msg.Context = idleChatMessageContext(messageID, turnIndex)
	o.memory.RecordMessage(msg)
	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.summary",
		From:      "shiro",
		To:        "idlechat_summary",
		Content:   title + "\n" + summary,
		SessionID: sessionID,
		MessageID: messageID,
		TurnIndex: turnIndex,
	})
	return summary
}

// speakSummary は Mio にまとめを読み上げさせる。

func (o *IdleChatOrchestrator) summarizeByWorker(topic string, transcript []string) string {
	if len(transcript) == 0 {
		return "会話ログがありません。"
	}
	body := strings.Join(transcript, "\n")
	summaryContext := o.dialogueSummaryContext()
	if summaryContext != "" {
		summaryContext = "\n\n対話の内部メタ（要約の観点として使い、内部用語は出さない）:\n" + summaryContext
	}
	messages := []llm.Message{
		{Role: "system", Content: o.getSystemPrompt("shiro")},
		{Role: "user", Content: fmt.Sprintf("次のidleChatを要約してください。硬い報告書ではなく、読んで雰囲気が分かる短い要約にしてください。1. いちばん面白かった点 2. 何が話を前に進めたか 3. 次に広がりそうな観点、の順で自然にまとめてください。\n話題: %s%s\n\n%s", topic, summaryContext, body)},
	}
	req := llm.GenerateRequest{Messages: messages, MaxTokens: 800, Temperature: 0.4}
	req.MaxTokens = idleChatShiroSummaryMaxTokens
	resp, err := o.providerForSpeaker("shiro").Generate(o.idleRunContext(), req)
	if err != nil || strings.TrimSpace(resp.Content) == "" {
		if err == nil {
			logIdleRaw("summary.generate", resp.Content)
		}
		return truncate(body, 200)
	}
	logIdleRaw("summary.generate", resp.Content)
	summary := sanitizeIdleSummaryResponse(resp.Content, topic)
	if summary == "" {
		log.Printf("[IdleChat] summary sanitize failed; raw=%q", truncate(strings.TrimSpace(resp.Content), 180))
		return truncate(body, 200)
	}
	return summary
}

func (o *IdleChatOrchestrator) dialogueSummaryContext() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.currentDialogueState == nil || o.currentDialoguePlan == nil {
		return ""
	}
	state := *o.currentDialogueState
	plan := *o.currentDialoguePlan
	var parts []string
	if plan.InterestingnessAxis != "" {
		parts = append(parts, "interestingness_axis="+plan.InterestingnessAxis)
	}
	if len(state.UsedMoves) > 0 {
		parts = append(parts, "used_moves="+strings.Join(state.UsedMoves, " / "))
	}
	if len(state.TensionPoints) > 0 {
		parts = append(parts, "tension_points="+strings.Join(state.TensionPoints, " / "))
	}
	if len(state.ConcreteAnchors) > 0 {
		parts = append(parts, "concrete_anchors="+strings.Join(state.ConcreteAnchors, " / "))
	}
	return strings.Join(parts, "\n")
}
