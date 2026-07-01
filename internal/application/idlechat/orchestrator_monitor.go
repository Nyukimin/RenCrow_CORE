package idlechat

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
	modulechat "github.com/Nyukimin/picoclaw_multiLLM/modules/chat"
)

func (o *IdleChatOrchestrator) monitorLoop() {
	defer o.wg.Done()

	ticker := time.NewTicker(idleCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
			go o.checkAndStartChat()
		}
	}
}

func (o *IdleChatOrchestrator) checkAndStartChat() {
	o.mu.Lock()
	idleDuration := time.Since(o.lastActivity)
	threshold := o.interval
	now := time.Now()
	nextTopicAt := o.nextTopicAt
	chatBusy := o.chatBusy
	workerBusy := o.workerBusy
	manualMode := o.manualMode
	disabled := o.disabled
	externalLLMBusy := false
	if o.externalLLMBusy != nil {
		externalLLMBusy = o.externalLLMBusy()
	}
	if disabled || externalLLMBusy || o.chatActive || chatBusy || workerBusy || (!nextTopicAt.IsZero() && now.Before(nextTopicAt)) || (!manualMode && idleDuration < threshold) {
		o.mu.Unlock()
		return
	}
	o.chatActive = true
	plan := o.nextIdleSessionPlanLocked()
	o.sessionMode = plan.mode
	generation := o.beginIdleRunLocked()
	o.mu.Unlock()

	log.Printf("[IdleChat] Idle for %v, starting %s session generation=%d", idleDuration.Round(time.Second), plan.mode, generation)
	switch plan.mode {
	case "forecast":
		if plan.domain == nil {
			log.Printf("[Forecast] Missing domain in session plan, skipping")
		} else {
			o.runForecastDomainSession(*plan.domain)
		}
	case "story-simple":
		o.RunSimpleStorySession()
	default:
		o.runChatSession(plan.strategy)
	}

	o.mu.Lock()
	if o.activeGeneration == generation {
		o.chatActive = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.activeSessionID = ""
	}
	o.lastActivity = time.Now() // セッション終了でアイドル計測をリセット
	o.mu.Unlock()
	o.cancelIdleRunIfGeneration(generation)
}

func (o *IdleChatOrchestrator) runChatSession(strategy TopicStrategy) {
	sessionID := fmt.Sprintf("idle-%d", time.Now().Unix())
	startedAt := time.Now().In(jst)
	turnLimit := o.idleChatTurnLimit()
	segmentID := fmt.Sprintf("%s-topic-00", sessionID)
	generation := o.activateIdleSession(segmentID)
	o.markWatchdogStage("session_start", fmt.Sprintf("strategy=%s", strategy), TimelineEvent{SessionID: segmentID})
	o.markWatchdogStage("topic_generation", fmt.Sprintf("strategy=%s", strategy), TimelineEvent{SessionID: segmentID})
	topic, strategy := o.generateTopicFromChat(segmentID, strategy)
	if !o.isIdleSessionActive(segmentID, generation) {
		log.Printf("[IdleChat] Topic generation discarded after interrupt: session=%s", segmentID)
		return
	}
	o.markWatchdogStage("topic_ready", fmt.Sprintf("strategy=%s", strategy), TimelineEvent{SessionID: segmentID})
	o.mu.Lock()
	o.currentTopic = topic
	topicResult := o.dialogueTopicResultLocked(topic, strategy)
	dialogueConfig := o.dialogueConfig
	o.mu.Unlock()
	director := NewDialogueDirector(dialogueConfig)
	arcPlan := director.BuildArcPlan(topicResult)
	arcState := director.NewArcState(segmentID, topicResult, arcPlan)
	director.LogArcCreated(segmentID, arcPlan)
	o.mu.Lock()
	o.currentDialoguePlan = &arcPlan
	o.currentDialogueState = &arcState
	o.mu.Unlock()
	log.Printf("[IdleChat] Topic: %s (%s, session=%s)", topic, strategy, segmentID)
	ttsDrain := make([]<-chan struct{}, 0, turnLimit+2)
	if ttsDone := o.emitTopicToTimeline(segmentID, topic, strategy); ttsDone != nil {
		ttsDrain = append(ttsDrain, ttsDone)
	}

	segmentTurns := 0
	loopReason := ""
	loopWarningReason := ""
	sessionInterrupted := false
	generationFailed := false
	transcript := make([]string, 0, turnLimit)
	currentSpeaker := o.chatSpeakerIndex()

	for turn := 0; turn < turnLimit; turn++ {
		select {
		case <-o.idleRunContext().Done():
			return
		default:
		}

		o.mu.Lock()
		if !o.chatActive {
			o.mu.Unlock()
			log.Printf("[IdleChat] Session interrupted at turn %d", turn)
			sessionInterrupted = true
			loopReason = "interrupted"
			break
		}
		o.mu.Unlock()

		speaker := o.participants[currentSpeaker]
		nextSpeaker := o.participants[(currentSpeaker+1)%len(o.participants)]

		o.markWatchdogStage("response_generation", fmt.Sprintf("%s->%s turn=%d", speaker, nextSpeaker, turn+1), TimelineEvent{
			From:      speaker,
			To:        nextSpeaker,
			SessionID: segmentID,
			TurnIndex: turn + 1,
		})
		response, rawResponse, err := o.generateResponseWithRaw(speaker, nextSpeaker, segmentID, turn, segmentTurns, topic)
		if err != nil {
			log.Printf("[IdleChat] Generation error: %v", err)
			generationFailed = true
			if errors.Is(err, errIdleInvalidResponse) {
				loopReason = "invalid_response"
			} else {
				loopReason = "generation_error"
			}
			o.recordGenerationErrorToTimeline(speaker, nextSpeaker, segmentID, loopReason, turn+1)
			break
		}
		if !o.isIdleSessionActive(segmentID, generation) {
			log.Printf("[IdleChat] Response discarded after interrupt: session=%s turn=%d", segmentID, turn)
			sessionInterrupted = true
			loopReason = "interrupted"
			break
		}
		if isResponseTooSimilar(response, transcript) {
			loopWarningReason = "pre_emit_similarity"
			log.Printf("[IdleChat] Repetitive response detected before emit, continuing current session topic: session=%s turn=%d reason=%s", segmentID, turn, loopWarningReason)
		}

		response = ensureTrailingPeriod(response)
		o.mu.Lock()
		currentQuality := o.lastDialogueQuality
		o.mu.Unlock()
		turnPlan := dialogueTurnPlanForIndex(arcPlan, turn)
		arcState = director.UpdateArcState(arcState, response, turnPlan, currentQuality)
		o.mu.Lock()
		updatedState := arcState
		o.currentDialogueState = &updatedState
		o.mu.Unlock()

		turnIndex := turn + 1
		messageID := idleChatMessageID(segmentID, turnIndex)
		o.markWatchdogStage("message_record", fmt.Sprintf("%s->%s turn=%d", speaker, nextSpeaker, turnIndex), TimelineEvent{
			From:      speaker,
			To:        nextSpeaker,
			SessionID: segmentID,
			MessageID: messageID,
			TurnIndex: turnIndex,
		})
		msg := domaintransport.NewMessage(speaker, nextSpeaker, segmentID, "", response)
		msg.Type = domaintransport.MessageTypeIdleChat
		msg.Context = idleChatMessageContext(messageID, turnIndex)
		o.memory.RecordMessage(msg)
		ttsDone := o.emitTimelineEvent(TimelineEvent{
			Type:       "idlechat.message",
			From:       speaker,
			To:         nextSpeaker,
			Content:    response,
			RawContent: rawResponse,
			SessionID:  segmentID,
			MessageID:  messageID,
			TurnIndex:  turnIndex,
		})
		if ttsDone != nil {
			ttsDrain = append(ttsDrain, ttsDone)
		}
		o.markWatchdogStage("message_emitted", fmt.Sprintf("%s->%s turn=%d", speaker, nextSpeaker, turnIndex), TimelineEvent{
			From:      speaker,
			To:        nextSpeaker,
			SessionID: segmentID,
			MessageID: messageID,
			TurnIndex: turnIndex,
		})
		transcript = append(transcript, fmt.Sprintf("%s: %s", speaker, response))
		segmentTurns++

		log.Printf("[IdleChat] [Turn %d] %s→%s: %s", turn, speaker, nextSpeaker, truncate(response, 80))
		o.waitForTTSDoneForEvent(TimelineEvent{
			Type:      "idlechat.message",
			From:      speaker,
			To:        nextSpeaker,
			Content:   response,
			SessionID: segmentID,
			MessageID: messageID,
			TurnIndex: turnIndex,
		}, ttsDone)
		o.waitBreak(speakerBreak)

		if reason := detectLoopReason(transcript); reason != "" {
			loopWarningReason = reason
			log.Printf("[IdleChat] Loop/repetition warning, continuing current session topic: session=%s turn=%d reason=%s", segmentID, turn, reason)
		}
		currentSpeaker = (currentSpeaker + 1) % len(o.participants)
	}

	endedAt := time.Now().In(jst)
	if segmentTurns > 0 && o.isIdleSessionActive(segmentID, generation) {
		if loopWarningReason != "" {
			log.Printf("[IdleChat] Session %s reached summary after loop warning: reason=%s turns=%d", segmentID, loopWarningReason, segmentTurns)
		}
		displayStrategy := TopicStrategy(fmt.Sprintf("%s: %s", strategy, truncate(topic, 30)))
		endedEarly := sessionInterrupted || generationFailed
		o.markWatchdogStage("summary_generation", fmt.Sprintf("turns=%d ended_early=%t", segmentTurns, endedEarly), TimelineEvent{SessionID: segmentID})
		summary := o.saveSummary(segmentID, topic, displayStrategy, transcript, startedAt, endedAt, segmentTurns, endedEarly, loopReason)
		o.markWatchdogStage("summary_tts", fmt.Sprintf("turns=%d", segmentTurns), TimelineEvent{SessionID: segmentID})
		if ttsDone := o.speakSummary(segmentID, summary); ttsDone != nil {
			ttsDrain = append(ttsDrain, ttsDone)
		}
	}
	o.markWatchdogStage("session_drain", fmt.Sprintf("channels=%d", len(ttsDrain)), TimelineEvent{SessionID: segmentID})
	o.waitForTTSSessionDrain(segmentID, ttsDrain)
	cooldown := topicBreak
	if sessionInterrupted || generationFailed {
		idleCooldown := o.interval
		if idleCooldown > cooldown {
			cooldown = idleCooldown
		}
	}
	o.mu.Lock()
	o.nextTopicAt = endedAt.Add(cooldown)
	o.mu.Unlock()

	log.Printf("[IdleChat] Session %s completed (%d turns)", sessionID, segmentTurns)
	o.markWatchdogStage("session_completed", fmt.Sprintf("turns=%d", segmentTurns), TimelineEvent{SessionID: segmentID})
}

func (o *IdleChatOrchestrator) dialogueTopicResultLocked(topic string, strategy TopicStrategy) TopicGenerationResult {
	if o.currentTopicResult != nil && strings.TrimSpace(o.currentTopicResult.Topic) == strings.TrimSpace(topic) {
		return *o.currentTopicResult
	}
	category, _ := modulechat.NormalizeTopicCategory(string(strategy))
	return TopicGenerationResult{
		Topic:               topic,
		Category:            category,
		Strategy:            string(strategy),
		InterestingnessAxis: modulechat.ExpectedAxisByCategory[category],
	}
}

func (o *IdleChatOrchestrator) idleChatTurnLimit() int {
	o.mu.Lock()
	dialogueMax := normalizeDialogueInterestingnessConfig(o.dialogueConfig).MaxTurnsPerTopic
	o.mu.Unlock()
	limit := maxTurnsPerTopic
	if dialogueMax > 0 && dialogueMax < limit {
		limit = dialogueMax
	}
	if o.maxTurns <= 0 || o.maxTurns > limit {
		return limit
	}
	return o.maxTurns
}

// waitForTTSDone はTTS完了チャネルを短時間だけ待つ。TTS未完了は音声系エラーとして扱い、会話進行は止めない。

func (o *IdleChatOrchestrator) waitForTTSDone(ch <-chan struct{}) {
	o.waitForTTSDoneForEvent(TimelineEvent{}, ch)
}

func (o *IdleChatOrchestrator) waitForTTSDoneForEvent(ev TimelineEvent, ch <-chan struct{}) {
	if ch == nil {
		return
	}
	if ev.Type != "" || ev.SessionID != "" || ev.MessageID != "" {
		o.markWatchdogStage("tts_wait", idleWatchdogEventDetail(ev), ev)
	}
	timeout := idleChatTTSWaitTimeout
	if timeout <= 0 {
		select {
		case <-o.idleRunContext().Done():
		case <-ch:
			o.markWatchdogStage("tts_done", idleWatchdogEventDetail(ev), ev)
		}
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-o.idleRunContext().Done():
		return
	case <-ch:
		o.markWatchdogStage("tts_done", idleWatchdogEventDetail(ev), ev)
	case <-timer.C:
		o.markWatchdogStage("tts_timeout", idleWatchdogEventDetail(ev), ev)
		log.Printf("[IdleChat] TTS completion wait timed out after %s; continuing conversation (tts_error=true tts_error_kind=timeout session=%s message_id=%s turn_index=%d)", timeout, ev.SessionID, ev.MessageID, ev.TurnIndex)
		o.reportTTSTimeoutEvent(TTSTimeoutEvent{
			Kind:      "timeout",
			SessionID: ev.SessionID,
			MessageID: ev.MessageID,
			TurnIndex: ev.TurnIndex,
		})
	}
}

func (o *IdleChatOrchestrator) waitForTTSSessionDrain(sessionID string, channels []<-chan struct{}) {
	if len(channels) == 0 {
		return
	}
	o.markWatchdogStage("tts_session_drain", fmt.Sprintf("channels=%d", len(channels)), TimelineEvent{SessionID: sessionID})
	timeout := idleChatTTSSessionDrainTimeout
	if timeout <= 0 {
		timeout = idleChatTTSWaitTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for idx, ch := range channels {
		if ch == nil {
			continue
		}
		select {
		case <-o.idleRunContext().Done():
			return
		case <-ch:
		case <-timer.C:
			o.markWatchdogStage("tts_session_drain_timeout", fmt.Sprintf("remaining=%d/%d", idx+1, len(channels)), TimelineEvent{SessionID: sessionID})
			log.Printf("[IdleChat] TTS session drain timed out after %s; continuing next session (session=%s remaining_index=%d/%d session_audio_timeout=true)", timeout, sessionID, idx+1, len(channels))
			o.reportTTSTimeoutEvent(TTSTimeoutEvent{
				Kind:           "session_audio_timeout",
				SessionID:      sessionID,
				RemainingIndex: idx + 1,
				RemainingCount: len(channels),
			})
			return
		}
	}
	o.markWatchdogStage("tts_session_drain_done", fmt.Sprintf("channels=%d", len(channels)), TimelineEvent{SessionID: sessionID})
}

func (o *IdleChatOrchestrator) reportTTSTimeoutEvent(ev TTSTimeoutEvent) {
	if o == nil {
		return
	}
	o.mu.Lock()
	report := o.reportTTSTimeout
	o.mu.Unlock()
	if report != nil {
		report(ev)
	}
}

// waitBreak はTTS完了後の沈黙を待つ。

func (o *IdleChatOrchestrator) waitBreak(d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-o.idleRunContext().Done():
		return
	case <-timer.C:
	}
}

// ensureTrailingPeriod はセリフ末尾に句読点がなければ「。」を追記する。

func ensureTrailingPeriod(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	last, _ := utf8.DecodeLastRuneInString(s)
	switch last {
	case '。', '！', '？', '!', '?', '…':
		return s
	}
	return s + "。"
}

func (o *IdleChatOrchestrator) nextIdleSessionPlanLocked() idleSessionPlan {
	normalStrategies := []TopicStrategy{
		StrategySingleGenre,
		StrategyDoubleGenre,
		StrategyExternalStimulus,
		StrategyMovie,
		StrategyNews,
	}
	if o.autoStep < len(normalStrategies) {
		plan := idleSessionPlan{
			mode:     "idle",
			strategy: normalStrategies[o.autoStep],
		}
		o.autoStep++
		return plan
	}
	if o.autoStep == len(normalStrategies) {
		domain := forecastDomains[o.forecastStep%len(forecastDomains)]
		o.forecastStep = (o.forecastStep + 1) % len(forecastDomains)
		o.autoStep++
		return idleSessionPlan{
			mode:   "forecast",
			domain: &domain,
		}
	}
	o.autoStep = 0
	return idleSessionPlan{
		mode: "story-simple",
	}
}

func (o *IdleChatOrchestrator) chatSpeakerIndex() int {
	for i, p := range o.participants {
		if strings.EqualFold(p, "mio") {
			return i
		}
	}
	return 0
}
