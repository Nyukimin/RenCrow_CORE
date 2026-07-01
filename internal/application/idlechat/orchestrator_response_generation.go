package idlechat

import (
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
)

func (o *IdleChatOrchestrator) generateResponse(speaker, target, sessionID string, turn int, segmentTurns int, topic string) (string, error) {
	response, _, err := o.generateResponseWithRaw(speaker, target, sessionID, turn, segmentTurns, topic)
	return response, err
}

func (o *IdleChatOrchestrator) generateResponseWithRaw(speaker, target, sessionID string, turn int, segmentTurns int, topic string) (string, string, error) {
	topic = o.resolveDialogueTopic(sessionID, speaker, topic)
	systemPrompt := o.getSystemPrompt(speaker)
	temp := o.temperatureForSpeaker(speaker)

	// 履歴は浅めにして、古いテンプレが自己強化しないようにする。
	recentEntries := o.memory.GetUnifiedView(12)
	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
	}
	selfCtx, otherCtx := splitSpeakerContexts(recentEntries, sessionID, speaker, 2)
	latestOther := latestOtherUtterance(recentEntries, sessionID, speaker)
	latestSelf := latestSelfUtterance(recentEntries, sessionID, speaker)

	// OpenAI互換サーバによっては system message が先頭以外にあると拒否するため、
	// 追加の system 文脈は履歴や user 指示より前に集約する。
	o.mu.Lock()
	sc := o.sessionContext
	dialoguePrompt, dialoguePlan, dialogueState, dialogueConfig := o.dialoguePromptContextLocked(topic, speaker, latestOther, latestSelf, turn)
	o.mu.Unlock()
	if sc != "" {
		messages[0].Content += "\n\n" + sc
	}
	if dialoguePrompt != "" {
		messages[0].Content += "\n\n" + dialoguePrompt
	}
	if o.recentTopics != nil {
		if glossaryTopics, err := o.recentTopics(o.ctx, 5); err != nil {
			log.Printf("[IdleChat] glossary context failed: %v", err)
		} else if len(glossaryTopics) > 0 {
			messages[0].Content += "\n\n最近語彙メモ:\n- " + strings.Join(glossaryTopics, "\n- ") + "\n最近語彙は会話の種としてだけ使い、詳細断言はしないでください。"
		}
	}

	sessionEntries := make([]session.ConversationEntry, 0, 4)
	for i := len(recentEntries) - 1; i >= 0 && len(sessionEntries) < 4; i-- {
		if recentEntries[i].Message.SessionID == sessionID {
			sessionEntries = append(sessionEntries, recentEntries[i])
		}
	}
	for i := len(sessionEntries) - 1; i >= 0; i-- {
		entry := sessionEntries[i]
		role := "assistant"
		if entry.Message.From != speaker {
			role = "user"
		}
		messages = append(messages, llm.Message{
			Role:    role,
			Content: fmt.Sprintf("[%s]: %s", entry.Message.From, entry.Message.Content),
		})
	}

	messages = append(messages, llm.Message{
		Role:    "user",
		Content: buildIdleResponseGuardPrompt(speaker, selfCtx, otherCtx),
	})
	if isMovieTopicPrompt(topic) {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: "これは架空映画の妄想会話です。実在作品として扱わず、『聞いたことがある』『前に見た』『有名作だ』のような既知前提は禁止。抽象論より、主人公・事件・場面・対立・反転を早めに一つ出してください。",
		})
	}

	if turn == 0 {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: buildIdleTurnPrompt(topic, speaker, "", "", turn, segmentTurns, true),
		})
	} else {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: buildIdleTurnPrompt(topic, speaker, latestOther, latestSelf, turn, segmentTurns, false),
		})
	}

	messageID := idleChatMessageID(sessionID, turn+1)
	o.mu.Lock()
	prefetchEmitter := o.emitTTSPrefetch
	o.mu.Unlock()
	req := llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   idleMaxTokensForSpeaker(speaker, idleChatResponseMaxTokens),
		Temperature: temp,
	}
	if prefetchEmitter != nil {
		req.OnToken = func(token string) {
			token = strings.TrimSpace(token)
			if token == "" {
				return
			}
			prefetchEmitter(TTSPrefetchEvent{
				SessionID: sessionID,
				MessageID: messageID,
				From:      speaker,
				To:        target,
				TurnIndex: turn + 1,
				Token:     token,
			})
		}
	}

	provider := o.providerForSpeaker(speaker)
	resp, err := o.generateIdleLLM(provider, req)
	if err != nil {
		log.Printf("[IdleChat] LLM generate primary failed (%s turn=%d): %v", speaker, turn, err)
		return "", "", fmt.Errorf("idlechat dialogue generation failed: speaker=%s turn=%d: %w", speaker, turn, err)
	}
	logIdleRaw(fmt.Sprintf("dialogue.primary speaker=%s turn=%d", speaker, turn), resp.Content)
	firstRaw := strings.TrimSpace(resp.Content)
	first := sanitizeIdleResponseForSpeaker(resp.Content, topic, speaker)
	firstTruncated := finishReasonLooksTruncated(resp.FinishReason)
	if firstTruncated {
		log.Printf("[IdleChat] primary truncated (%s turn=%d): finish=%q max_tokens=%d", speaker, turn, resp.FinishReason, req.MaxTokens)
	}
	if firstRaw == "" && strings.TrimSpace(first) == "" {
		log.Printf("[IdleChat] empty content rejected without fallback (%s turn=%d)", speaker, turn)
		return "", firstRaw, fmt.Errorf("%w: speaker=%s turn=%d empty_content=true", errIdleInvalidResponse, speaker, turn)
	}
	if !firstTruncated && shouldRejectShiroDialogueImmediately(speaker, firstRaw, first) {
		log.Printf("[IdleChat] shiro dialogue rejected without fallback (turn=%d): raw=%q sanitized=%q", turn, truncate(firstRaw, 180), truncate(first, 180))
		return "", firstRaw, fmt.Errorf("%w: speaker=%s turn=%d", errIdleInvalidResponse, speaker, turn)
	}
	if shouldGenerateIdleFunCandidate(speaker) && !firstTruncated && !unusableIdleResponse(firstRaw, first) {
		secondMessages := append([]llm.Message{}, messages...)
		secondMessages = append(secondMessages, llm.Message{
			Role:    "assistant",
			Content: first,
		})
		secondMessages = append(secondMessages, llm.Message{
			Role:    "user",
			Content: "今の発話とは別候補を1つだけ出してください。前候補と同じ書き出し・同じ比喩・同じ結論を避け、読者の楽しさが上がるように、具体物・選択・秘密・感情の反転のどれかを一つだけ入れてください。英語だけの応答、英語の見出し、英語での説明は禁止です。候補番号、評価文、説明、ルール確認は書かず、発話として読める自然な日本語だけを1-2文で返してください。",
		})
		respSecond, errSecond := o.generateIdleLLM(provider, llm.GenerateRequest{
			Messages:    secondMessages,
			MaxTokens:   idleMaxTokensForSpeaker(speaker, idleChatRetryMaxTokens),
			Temperature: temp,
		})
		if errSecond != nil {
			log.Printf("[IdleChat] fun candidate B failed (%s turn=%d): %v", speaker, turn, errSecond)
		} else {
			logIdleRaw(fmt.Sprintf("dialogue.candidate_b speaker=%s turn=%d", speaker, turn), respSecond.Content)
			secondRaw := strings.TrimSpace(respSecond.Content)
			second := sanitizeIdleResponseForSpeaker(respSecond.Content, topic, speaker)
			if finishReasonLooksTruncated(respSecond.FinishReason) || unusableIdleResponse(secondRaw, second) {
				log.Printf("[IdleChat] fun candidate B unusable (%s turn=%d): raw=%q sanitized=%q", speaker, turn, truncate(secondRaw, 180), truncate(second, 180))
			} else {
				firstScore := idleFunScorePercent(first, latestOther, latestSelf, topic)
				secondScore := idleFunScorePercent(second, latestOther, latestSelf, topic)
				log.Printf("[IdleChat] fun candidate scores (%s turn=%d): A=%d%% B=%d%%", speaker, turn, firstScore, secondScore)
				if secondScore > firstScore {
					firstRaw = secondRaw
					first = second
					firstTruncated = false
				}
			}
		}
	}
	if firstTruncated || unusableIdleResponse(firstRaw, first) {
		retryInvalid := buildIdleCompactRetryMessages(speaker, topic, latestOther, firstTurnLabel(turn))
		respInvalid, errInvalid := o.generateIdleLLM(provider, llm.GenerateRequest{
			Messages:    retryInvalid,
			MaxTokens:   idleMaxTokensForSpeaker(speaker, idleChatRetryMaxTokens),
			Temperature: temp,
		})
		if errInvalid != nil {
			log.Printf("[IdleChat] retryInvalid failed (%s turn=%d): %v", speaker, turn, errInvalid)
		}
		if errInvalid == nil && strings.TrimSpace(respInvalid.Content) != "" {
			logIdleRaw(fmt.Sprintf("dialogue.retry_invalid speaker=%s turn=%d", speaker, turn), respInvalid.Content)
			first = sanitizeIdleResponseForSpeaker(respInvalid.Content, topic, speaker)
			firstRaw = strings.TrimSpace(respInvalid.Content)
			firstTruncated = finishReasonLooksTruncated(respInvalid.FinishReason)
		}
	}
	if needsIdleStyleRetry(speaker, first, latestOther, latestSelf, topic) {
		retryStyle := append([]llm.Message{}, messages...)
		retryStyle = append(retryStyle, llm.Message{
			Role:    "user",
			Content: "評価や言い直し宣言は書かず、別の手で自然な日本語だけで返してください。英語だけの応答、英語の見出し、英語での説明は禁止です。直前の言い回しをなぞらず、1文目で相手の論点に反応し、2文目で具体物・選択・秘密・感情の反転のどれかを一つだけ足してください。",
		})
		respStyle, errStyle := o.generateIdleLLM(provider, llm.GenerateRequest{
			Messages:    retryStyle,
			MaxTokens:   idleMaxTokensForSpeaker(speaker, idleChatRetryMaxTokens),
			Temperature: temp,
		})
		if errStyle != nil {
			log.Printf("[IdleChat] retryStyle failed (%s turn=%d): %v", speaker, turn, errStyle)
		}
		if errStyle == nil && strings.TrimSpace(respStyle.Content) != "" {
			logIdleRaw(fmt.Sprintf("dialogue.retry_style speaker=%s turn=%d", speaker, turn), respStyle.Content)
			styleRaw := strings.TrimSpace(respStyle.Content)
			style := sanitizeIdleResponseForSpeaker(respStyle.Content, topic, speaker)
			styleTruncated := finishReasonLooksTruncated(respStyle.FinishReason)
			if styleTruncated || unusableIdleResponse(styleRaw, style) {
				log.Printf("[IdleChat] retryStyle unusable (%s turn=%d): truncated=%t raw=%q sanitized=%q", speaker, turn, styleTruncated, truncate(styleRaw, 180), truncate(style, 180))
			} else {
				first = style
				firstRaw = styleRaw
				firstTruncated = false
			}
		}
	}
	if hasPromptLeak(firstRaw) || hasPromptLeak(first) || hasInternalReasoningLeak(firstRaw) || hasInternalReasoningLeak(first) {
		retryLeak := buildIdleCompactRetryMessages(speaker, topic, latestOther, "内部推論を出さずに本文だけで再生成")
		respLeak, errLeak := o.generateIdleLLM(provider, llm.GenerateRequest{
			Messages:    retryLeak,
			MaxTokens:   idleMaxTokensForSpeaker(speaker, idleChatRetryMaxTokens),
			Temperature: temp,
		})
		if errLeak != nil {
			log.Printf("[IdleChat] retryLeak failed (%s turn=%d): %v", speaker, turn, errLeak)
		}
		if errLeak == nil && strings.TrimSpace(respLeak.Content) != "" {
			logIdleRaw(fmt.Sprintf("dialogue.retry_leak speaker=%s turn=%d", speaker, turn), respLeak.Content)
			first = sanitizeIdleResponseForSpeaker(respLeak.Content, topic, speaker)
			firstRaw = strings.TrimSpace(respLeak.Content)
			firstTruncated = finishReasonLooksTruncated(respLeak.FinishReason)
		}
	}
	if violatesAttribution(first, latestOther) {
		retry := append([]llm.Message{}, messages...)
		retry = append(retry, llm.Message{
			Role:    "user",
			Content: "発言帰属が曖昧です。相手の案を受けてから、自分の新しい具体物・選択・秘密・感情の反転を一つだけ足し、自然な日本語1-2文で言い直してください。英語だけの応答、英語の見出し、英語での説明は禁止です。",
		})
		resp2, err2 := o.generateIdleLLM(provider, llm.GenerateRequest{
			Messages:    retry,
			MaxTokens:   idleMaxTokensForSpeaker(speaker, idleChatRetryMaxTokens),
			Temperature: temp,
		})
		if err2 != nil {
			log.Printf("[IdleChat] retryAttribution failed (%s turn=%d): %v", speaker, turn, err2)
		}
		if err2 == nil && strings.TrimSpace(resp2.Content) != "" {
			logIdleRaw(fmt.Sprintf("dialogue.retry_attribution speaker=%s turn=%d", speaker, turn), resp2.Content)
			candidateRaw := strings.TrimSpace(resp2.Content)
			candidate := sanitizeIdleResponseForSpeaker(resp2.Content, topic, speaker)
			if finishReasonLooksTruncated(resp2.FinishReason) || unusableIdleResponse(candidateRaw, candidate) {
				log.Printf("[IdleChat] retryAttribution unusable (%s turn=%d): raw=%q sanitized=%q", speaker, turn, truncate(candidateRaw, 180), truncate(candidate, 180))
				return "", candidateRaw, fmt.Errorf("idlechat dialogue retry_attribution unusable: speaker=%s turn=%d", speaker, turn)
			}
			if canonical := o.applyPersonaCanonicalResponse(speaker, sessionID, candidate); canonical != "" {
				return canonical, candidateRaw, nil
			}
			return candidate, candidateRaw, nil
		}
	}

	if firstTruncated || unusableIdleResponse(firstRaw, first) {
		log.Printf("[IdleChat] unusable response rejected (%s turn=%d): truncated=%t raw=%q sanitized=%q", speaker, turn, firstTruncated, truncate(firstRaw, 180), truncate(first, 180))
		return "", firstRaw, fmt.Errorf("%w: speaker=%s turn=%d truncated=%t", errIdleInvalidResponse, speaker, turn, firstTruncated)
	}
	if dialogueConfig.Enabled && dialoguePlan != nil && dialogueState != nil {
		first, firstRaw, err = o.ensureDialogueQuality(provider, messages, speaker, sessionID, turn, topic, latestOther, latestSelf, first, firstRaw, *dialoguePlan, *dialogueState, dialogueConfig, temp)
		if err != nil {
			return "", firstRaw, err
		}
	}

	if canonical := o.applyPersonaCanonicalResponse(speaker, sessionID, first); canonical != "" {
		return canonical, firstRaw, nil
	}
	return first, firstRaw, nil
}

func (o *IdleChatOrchestrator) dialoguePromptContextLocked(topic, speaker, latestOther, latestSelf string, turn int) (string, *DialogueTurnPlan, *DialogueArcState, DialogueInterestingnessConfig) {
	config := normalizeDialogueInterestingnessConfig(o.dialogueConfig)
	if !config.Enabled || o.currentDialoguePlan == nil || o.currentDialogueState == nil {
		return "", nil, nil, config
	}
	plan := *o.currentDialoguePlan
	state := *o.currentDialogueState
	topicResult := TopicGenerationResult{
		Topic:               topic,
		Category:            plan.Category,
		Strategy:            plan.Strategy,
		InterestingnessAxis: plan.InterestingnessAxis,
	}
	if o.currentTopicResult != nil {
		topicResult = *o.currentTopicResult
	}
	turnPlan := dialogueTurnPlanForIndex(plan, turn)
	prompt := BuildDialoguePrompt(DialoguePromptInput{
		Result:             topicResult,
		Plan:               plan,
		State:              state,
		TurnPlan:           turnPlan,
		Speaker:            speaker,
		PreviousUtterances: []string{latestOther, latestSelf},
		Config:             config,
	})
	return prompt, &turnPlan, &state, config
}

func (o *IdleChatOrchestrator) ensureDialogueQuality(provider llm.LLMProvider, baseMessages []llm.Message, speaker, sessionID string, turn int, topic, latestOther, latestSelf, candidate, candidateRaw string, plan DialogueTurnPlan, state DialogueArcState, config DialogueInterestingnessConfig, temp float64) (string, string, error) {
	checker := NewDialogueQualityChecker(config)
	quality := checker.Check(DialogueQualityInput{
		Category:    state.Category,
		Utterance:   candidate,
		LatestOther: latestOther,
		LatestSelf:  latestSelf,
		State:       state,
		TurnPlan:    plan,
		Config:      config,
	})
	retryCount := 0
	if !quality.OK {
		maxRetries := config.MaxQualityRetries
		if maxRetries <= 0 {
			maxRetries = 1
		}
		for retryAttempt := 1; retryAttempt <= maxRetries && !quality.OK; retryAttempt++ {
			logDialogueTurnRetry(sessionID, speaker, state.Category, quality, retryAttempt)
			retryMessages := append([]llm.Message{}, baseMessages...)
			retryMessages = append(retryMessages, llm.Message{Role: "assistant", Content: candidate})
			retryMessages = append(retryMessages, llm.Message{Role: "user", Content: BuildDialogueRetryPrompt(plan, quality)})
			resp, err := o.generateIdleLLM(provider, llm.GenerateRequest{
				Messages:    retryMessages,
				MaxTokens:   idleMaxTokensForSpeaker(speaker, idleChatRetryMaxTokens),
				Temperature: temp,
			})
			if err != nil || strings.TrimSpace(resp.Content) == "" {
				continue
			}
			logIdleRaw(fmt.Sprintf("dialogue.retry_quality speaker=%s turn=%d attempt=%d", speaker, turn, retryAttempt), resp.Content)
			retryRaw := strings.TrimSpace(resp.Content)
			retry := sanitizeIdleResponseForSpeaker(resp.Content, topic, speaker)
			if finishReasonLooksTruncated(resp.FinishReason) || unusableIdleResponse(retryRaw, retry) {
				continue
			}
			retryQuality := checker.Check(DialogueQualityInput{
				Category:    state.Category,
				Utterance:   retry,
				LatestOther: latestOther,
				LatestSelf:  latestSelf,
				State:       state,
				TurnPlan:    plan,
				Config:      config,
			})
			retryCount = retryAttempt
			if retryQuality.OK || retryQuality.Score >= quality.Score {
				candidate = retry
				candidateRaw = retryRaw
				quality = retryQuality
			}
		}
	}
	logDialogueTurnQuality(sessionID, speaker, state.Category, plan, quality, retryCount)
	o.mu.Lock()
	o.lastDialogueQuality = quality
	o.mu.Unlock()
	if !quality.OK {
		return "", candidateRaw, dialogueQualityError(quality)
	}
	return candidate, candidateRaw, nil
}

func dialogueTurnPlanForIndex(plan DialogueArcPlan, zeroBasedTurn int) DialogueTurnPlan {
	if len(plan.TurnPlans) == 0 {
		return DialogueTurnPlan{TurnIndex: zeroBasedTurn + 1, Phase: dialoguePhaseForTurn(zeroBasedTurn), RequiredMove: "直前発話を受け、新しい貢献を一つ足す"}
	}
	if zeroBasedTurn < 0 {
		zeroBasedTurn = 0
	}
	if zeroBasedTurn >= len(plan.TurnPlans) {
		return plan.TurnPlans[len(plan.TurnPlans)-1]
	}
	return plan.TurnPlans[zeroBasedTurn]
}

func shouldGenerateIdleFunCandidate(speaker string) bool {
	return !strings.EqualFold(strings.TrimSpace(speaker), "shiro")
}

func shouldRejectShiroDialogueImmediately(speaker, raw, sanitized string) bool {
	if !strings.EqualFold(strings.TrimSpace(speaker), "shiro") {
		return false
	}
	raw = strings.TrimSpace(raw)
	sanitized = strings.TrimSpace(sanitized)
	if hasInternalReasoningLeak(raw) {
		return true
	}
	return false
}
