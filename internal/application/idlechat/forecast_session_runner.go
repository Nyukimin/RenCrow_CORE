package idlechat

import (
	"fmt"
	"log"
	"time"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

// RunForecastSession は6ドメインを順に回す未来展望セッションを実行する。
func (o *IdleChatOrchestrator) RunForecastSession() {
	sessionID := fmt.Sprintf("forecast-%d", time.Now().Unix())
	startedAt := time.Now().In(jst)
	sessionDomains := append([]ForecastDomain(nil), forecastDomains...)

	log.Printf("[Forecast] Session %s started (%d domains, max %d turns/domain)", sessionID, len(sessionDomains), forecastTurnsPerDomain)

	o.mu.Lock()
	o.chatActive = true
	o.sessionMode = "forecast"
	generation := o.beginIdleRunLocked()
	o.activeSessionID = sessionID
	o.mu.Unlock()

	totalTurns := o.runForecastSessionDomains(sessionID, generation, startedAt, sessionDomains)

	o.mu.Lock()
	if o.activeGeneration == generation {
		o.chatActive = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.sessionContext = ""
		o.activeSessionID = ""
	}
	o.lastActivity = time.Now()
	o.mu.Unlock()
	o.cancelIdleRunIfGeneration(generation)
	log.Printf("[Forecast] Session %s completed (%d total turns)", sessionID, totalTurns)
}

func (o *IdleChatOrchestrator) runForecastDomainSession(domain ForecastDomain) {
	sessionID := fmt.Sprintf("forecast-%d", time.Now().Unix())
	generation := o.activateIdleSession(sessionID)
	startedAt := time.Now().In(jst)
	totalTurns := o.runForecastSessionDomains(sessionID, generation, startedAt, []ForecastDomain{domain})
	log.Printf("[Forecast] Session %s completed (%d total turns)", sessionID, totalTurns)
}

func (o *IdleChatOrchestrator) runForecastSessionDomains(sessionID string, generation uint64, startedAt time.Time, sessionDomains []ForecastDomain) int {
	totalTurns := 0

	for domainIdx, domain := range sessionDomains {
		ttsDrain := make([]<-chan struct{}, 0, forecastTurnsPerDomain+3)
		select {
		case <-o.idleRunContext().Done():
			return totalTurns
		default:
		}

		o.mu.Lock()
		if !o.chatActive {
			o.mu.Unlock()
			log.Printf("[Forecast] Session interrupted before domain %s", domain.Name)
			return totalTurns
		}
		o.mu.Unlock()

		// ドメインアナウンス
		announce := fmt.Sprintf("%sのテーマの時間です。", domain.Name)
		log.Printf("[Forecast] [Domain %d/%d] %s", domainIdx+1, len(sessionDomains), domain.Name)
		announceMessageID := fmt.Sprintf("%s:domain:%04d", sessionID, domainIdx)

		announceMsg := domaintransport.NewMessage("user", "mio", sessionID, "", announce)
		announceMsg.Type = domaintransport.MessageTypeIdleChat
		announceMsg.Context = idleChatMessageContext(announceMessageID, 0)
		o.memory.RecordMessage(announceMsg)
		announceEvent := TimelineEvent{
			Type:      "idlechat.message",
			From:      "user",
			To:        "mio",
			Content:   announce,
			SessionID: sessionID,
			MessageID: announceMessageID,
			TurnIndex: 0,
		}
		ttsDone := o.emitTimelineEvent(announceEvent)
		if ttsDone != nil {
			ttsDrain = append(ttsDrain, ttsDone)
		}
		o.waitForTTSDoneForEvent(announceEvent, ttsDone)

		// ドメイン特化トピック生成: ストックから取得（空ならインライン生成）
		displayTopic, seeds := o.popForecastTopic(domain)
		llmTopic := buildForecastLLMTopic(domain, displayTopic, seeds)
		if !o.isIdleSessionActive(sessionID, generation) {
			log.Printf("[Forecast] Topic discarded after interrupt: session=%s domain=%s", sessionID, domain.Name)
			return totalTurns
		}

		o.mu.Lock()
		o.currentTopic = fmt.Sprintf("[%s] %s", domain.Name, displayTopic)
		forecastTopicResult := TopicGenerationResult{
			Topic:               displayTopic,
			Category:            TopicCategoryForecast,
			Strategy:            string(StrategyForecast),
			InterestingnessAxis: modulechat.ExpectedAxisByCategory[TopicCategoryForecast],
			OpeningHook:         "現在の兆しを生活・仕事・創作・制度のどれかに置く",
			Avoid:               "未来を断定せず、複数の分岐として扱う",
		}
		dialogueConfig := o.dialogueConfig
		o.mu.Unlock()
		director := NewDialogueDirector(dialogueConfig)
		arcPlan := director.BuildArcPlan(forecastTopicResult)
		arcState := director.NewArcState(sessionID, forecastTopicResult, arcPlan)
		director.LogArcCreated(sessionID, arcPlan)
		o.mu.Lock()
		o.currentTopicResult = &forecastTopicResult
		o.currentDialoguePlan = &arcPlan
		o.currentDialogueState = &arcState
		o.mu.Unlock()

		// Viewer/TTS には通常 IdleChat と同じ topic イベント契約で表示する。
		topicAnnounce := fmt.Sprintf("今日のお題（%s）: %s", StrategyForecast, displayTopic)
		topicMessageID := fmt.Sprintf("%s:topic:%04d", sessionID, domainIdx)
		topicMsg := domaintransport.NewMessage("user", "mio", sessionID, "", topicAnnounce)
		topicMsg.Type = domaintransport.MessageTypeIdleChat
		topicMsg.Context = idleChatMessageContext(topicMessageID, 0)
		o.memory.RecordMessage(topicMsg)
		topicEvent := TimelineEvent{
			Type:      "idlechat.topic",
			From:      "user",
			To:        "mio",
			Content:   topicAnnounce,
			SessionID: sessionID,
			MessageID: topicMessageID,
			TurnIndex: 0,
			Category:  TopicCategoryForecast,
			Strategy:  StrategyForecast,
		}
		ttsDone = o.emitTimelineEvent(topicEvent)
		if ttsDone != nil {
			ttsDrain = append(ttsDrain, ttsDone)
		}
		o.waitForTTSDoneForEvent(topicEvent, ttsDone)
		o.waitBreak(topicBreak)

		// ドメイン内ターンループ（generateResponse には詳細版 llmTopic を渡す）
		topic := displayTopic // saveSummary 用
		transcript := make([]string, 0, forecastTurnsPerDomain)
		coveredThemes := make([]string, 0, 8)
		currentSpeaker := o.chatSpeakerIndex()
		segmentTurns := 0
		loopReason := ""
		interrupted := false
		genFailed := false

		// ドメイン開始時に sessionContext をクリア
		o.mu.Lock()
		o.sessionContext = ""
		o.mu.Unlock()

		for turn := 0; turn < forecastTurnsPerDomain; turn++ {
			select {
			case <-o.idleRunContext().Done():
				return totalTurns
			default:
			}

			o.mu.Lock()
			if !o.chatActive {
				o.mu.Unlock()
				interrupted = true
				loopReason = "interrupted"
				break
			}
			o.mu.Unlock()

			speaker := o.participants[currentSpeaker]
			nextSpeaker := o.participants[(currentSpeaker+1)%len(o.participants)]

			// チェックポイント: 既出テーマを蓄積し sessionContext に反映
			if segmentTurns > 0 && segmentTurns%forecastCheckpointInterval == 0 {
				newThemes := o.extractCoveredThemes(domain, displayTopic, transcript, coveredThemes)
				if len(newThemes) > 0 {
					coveredThemes = append(coveredThemes, newThemes...)
					o.updateForecastSessionContext(domain, displayTopic, coveredThemes)
					log.Printf("[Forecast] Checkpoint at turn %d: covered themes now %d", segmentTurns, len(coveredThemes))
				}
			}

			// LLM には詳細な背景情報付きトピックを渡す
			response, err := o.generateResponse(speaker, nextSpeaker, sessionID, totalTurns+turn, segmentTurns, llmTopic)
			if err != nil {
				log.Printf("[Forecast] Generation error: %v", err)
				genFailed = true
				loopReason = "generation_error"
				o.recordGenerationErrorToTimeline(speaker, nextSpeaker, sessionID, loopReason, totalTurns+1)
				break
			}
			if !o.isIdleSessionActive(sessionID, generation) {
				log.Printf("[Forecast] Response discarded after interrupt: session=%s turn=%d", sessionID, turn)
				interrupted = true
				loopReason = "interrupted"
				break
			}
			if isResponseTooSimilar(response, transcript) {
				loopReason = "pre_emit_similarity"
				log.Printf("[Forecast] Repetitive response, moving to next domain")
				break
			}

			response = ensureTrailingPeriod(response)
			o.mu.Lock()
			currentQuality := o.lastDialogueQuality
			o.mu.Unlock()
			turnPlan := dialogueTurnPlanForIndex(arcPlan, segmentTurns)
			arcState = director.UpdateArcState(arcState, response, turnPlan, currentQuality)
			o.mu.Lock()
			updatedState := arcState
			o.currentDialogueState = &updatedState
			o.mu.Unlock()

			turnIndex := totalTurns + 1
			messageID := idleChatMessageID(sessionID, turnIndex)
			msg := domaintransport.NewMessage(speaker, nextSpeaker, sessionID, "", response)
			msg.Type = domaintransport.MessageTypeIdleChat
			msg.Context = idleChatMessageContext(messageID, turnIndex)
			o.memory.RecordMessage(msg)
			turnEvent := TimelineEvent{
				Type:      "idlechat.message",
				From:      speaker,
				To:        nextSpeaker,
				Content:   response,
				SessionID: sessionID,
				MessageID: messageID,
				TurnIndex: turnIndex,
			}
			ttsDone := o.emitTimelineEvent(turnEvent)
			if ttsDone != nil {
				ttsDrain = append(ttsDrain, ttsDone)
			}
			transcript = append(transcript, fmt.Sprintf("%s: %s", speaker, response))
			segmentTurns++
			totalTurns++

			log.Printf("[Forecast] [%s Turn %d] %s→%s: %s", domain.Name, turn, speaker, nextSpeaker, truncate(response, 80))
			o.waitForTTSDoneForEvent(turnEvent, ttsDone)
			o.waitBreak(speakerBreak)

			if reason := detectLoopReason(transcript); reason != "" {
				loopReason = reason
				log.Printf("[Forecast] Loop detected in %s, moving to next domain", domain.Name)
				break
			}
			currentSpeaker = (currentSpeaker + 1) % len(o.participants)
		}

		// ドメイン要約保存（Coder2で要約 + 継続考察テーマ付与）
		endedAt := time.Now().In(jst)
		if segmentTurns > 0 && o.isIdleSessionActive(sessionID, generation) {
			summary := o.saveForecastSummary(sessionID, domain, topic, transcript, startedAt, endedAt, segmentTurns,
				interrupted || genFailed || loopReason != "", loopReason)
			if ttsDone := o.speakSummary(sessionID, summary); ttsDone != nil {
				ttsDrain = append(ttsDrain, ttsDone)
			}
		}
		o.waitForTTSSessionDrain(sessionID, ttsDrain)

		if interrupted {
			return totalTurns
		}

		// ドメイン間ブレイク（最後のドメイン以外）
		if domainIdx < len(sessionDomains)-1 {
			o.waitBreak(topicBreak)
		}
	}

	return totalTurns
}
