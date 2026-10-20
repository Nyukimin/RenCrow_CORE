package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

// messageStreamHook binds the LLM token stream of one turn to event, TTS and
// VTuber forwarding. speaker is the Agent whose model generates the tokens: the
// agent.thinking events carry it as from, so it must equal the from of the
// first agent.response of the same turn (docs/06_Public_API仕様.md).
type messageStreamHook func(ctx context.Context, route routing.Route, speaker, taskID, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle)

// messageTTSPusher voices one finished utterance. speaker is the Agent that
// produced the text (the turn's responding Agent), never inferred from route.
type messageTTSPusher func(ctx context.Context, sessionID string, route routing.Route, speaker, eventType, text string)

// messageSpeechPlanPusher hands one normalized CHAT plan to its TTS owner.
// Conversation session, TTS session, and canonical turn IDs remain explicit.
type messageSpeechPlanPusher func(ctx context.Context, ttsSessionID, conversationSessionID, turnID, speakerID string, plan speech.SpeechPlan) error

type speechPlanRouteObservation struct {
	Event            string `json:"event"`
	TurnID           string `json:"turn_id"`
	SpeakerID        string `json:"speaker_id"`
	SpeechPlanValid  bool   `json:"speech_plan_valid"`
	UtteranceCount   int    `json:"utterance_count"`
	CueCount         int    `json:"cue_count"`
	LLMGenerationMS  *int64 `json:"llm_generation_ms"`
	PlanValidationMS *int64 `json:"plan_validation_ms"`
	FallbackReason   string `json:"fallback_reason"`
}

const (
	speechPlanHandoffUnavailable  = "speech_plan_tts_handoff_unavailable"
	speechPlanHandoffFailed       = "speech_plan_tts_handoff_failed"
	speechPlanDisplayUnavailable  = "speech_plan_display_unavailable"
	speechPlanCallbackUnavailable = "speech_plan_callback_unavailable"
)

type messageRouteDispatcher struct {
	mio               MioAgent
	shiroChat         MioAgent
	shiro             ShiroAgent
	wild              WildAgent
	heavy             HeavyAgent
	speechPlanEnabled bool
	codeExecutor      CodeExecutor
	emit              messageEventEmitter
	withStreamHooks   messageStreamHook
	pushTTS           messageTTSPusher
	pushSpeechPlan    messageSpeechPlanPusher
	executeAutonomous autonomousRouteExecutor
	canonicalEvents   CanonicalEventRecorder
	nativeCoding      NativeCodingAdmission
}

func newMessageRouteDispatcher(
	mio MioAgent,
	shiro ShiroAgent,
	codeExecutor CodeExecutor,
	emit messageEventEmitter,
	withStreamHooks messageStreamHook,
	pushTTS messageTTSPusher,
) *messageRouteDispatcher {
	return &messageRouteDispatcher{
		mio:             mio,
		shiro:           shiro,
		codeExecutor:    codeExecutor,
		emit:            emit,
		withStreamHooks: withStreamHooks,
		pushTTS:         pushTTS,
	}
}

func (d *messageRouteDispatcher) SetWildAgent(wild WildAgent) {
	d.wild = wild
}

func (d *messageRouteDispatcher) SetHeavyAgent(heavy HeavyAgent) {
	d.heavy = heavy
}

func (d *messageRouteDispatcher) SetShiroChatAgent(chat MioAgent) {
	d.shiroChat = chat
}

// SetSpeechPlanPusher installs the typed CHAT-to-TTS handoff. The message
// orchestrator lifecycle owns wiring this seam to its active TTS bridge.
func (d *messageRouteDispatcher) SetSpeechPlanPusher(pusher messageSpeechPlanPusher) {
	d.pushSpeechPlan = pusher
}

func (d *messageRouteDispatcher) SetSpeechPlanEnabled(enabled bool) {
	d.speechPlanEnabled = enabled
}

func (d *messageRouteDispatcher) SetAutonomousExecutor(execute autonomousRouteExecutor) {
	d.executeAutonomous = execute
}

func (d *messageRouteDispatcher) SetCanonicalEventRecorder(recorder CanonicalEventRecorder) {
	d.canonicalEvents = recorder
}

func turnInputMetadata(input domainconversation.TurnInput) (sessionID, channel, chatID string) {
	address := input.ChannelAddress()
	return input.SessionID(), address.ChannelType(), address.ExternalConversationID()
}

func (d *messageRouteDispatcher) ExecuteTurnInput(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID, ttsSessionID string) (string, error) {
	var err error
	ctx, err = bindRouteExecutionContext(ctx, input, route, taskID, runID)
	if err != nil {
		return "", err
	}
	input = input.WithRoute(route)
	if route != routing.RouteCHAT {
		// shiro_native_coding_v1: the admission selects a new OPS turn before it
		// enters the autonomous coordinator, so the selection travels with the
		// input through every attempt. A refusal ends the turn here.
		input, err = AdmitNativeCoding(ctx, d.nativeCoding, input, route)
		if err != nil {
			return "", err
		}
		if shouldTraceShiroDelegation(route) {
			sessionID, channel, chatID := turnInputMetadata(input)
			d.emit("agent.delegate", "mio", "shiro", formatMioToShiroInstruction(input, route, taskID.String()), route.String(), taskID.String(), sessionID, channel, chatID)
			d.emit("agent.acknowledge", "shiro", "mio", formatShiroReadbackToMio(input, route, taskID.String()), route.String(), taskID.String(), sessionID, channel, chatID)
		}
		return d.executeAutonomous(ctx, input, route, taskID, ttsSessionID)
	}

	return d.executeChatRoute(ctx, input, taskID, ttsSessionID)
}

func (d *messageRouteDispatcher) ExecuteDirect(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID, ttsSessionID string) (string, error) {
	var err error
	ctx, err = bindRouteExecutionContext(ctx, input, route, taskID, runID)
	if err != nil {
		return "", err
	}
	input = input.WithRoute(route)
	switch route {
	case routing.RouteOPS:
		return d.executeOPSRoute(ctx, input, taskID, ttsSessionID)
	case routing.RouteCODE, routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4:
		return d.executeCodeRoute(ctx, input, route, taskID, ttsSessionID)
	case routing.RouteWILD:
		return d.executeWildRoute(ctx, input, taskID, ttsSessionID)
	case routing.RoutePLAN:
		return d.executePlanRoute(ctx, input, taskID, ttsSessionID)
	case routing.RouteANALYZE:
		return d.executeAnalyzeRoute(ctx, input, taskID, ttsSessionID)
	case routing.RouteRESEARCH:
		return d.executeResearchRoute(ctx, input, taskID, ttsSessionID)
	default:
		return "", fmt.Errorf("unsupported autonomous route: %s", route)
	}
}

// executeDirectForContext is the adapter used by the bounded autonomous
// coordinator. The coordinator retries with the same context, so it must
// recover the exact owner-selected RunID rather than issue or derive one.
func (d *messageRouteDispatcher) executeDirectForContext(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return "", err
	}
	if identity.TaskID != taskID {
		return "", fmt.Errorf("execution task identity mismatch: context=%s argument=%s", identity.TaskID, taskID)
	}
	return d.ExecuteDirect(ctx, input, route, taskID, identity.RunID, ttsSessionID)
}

func (d *messageRouteDispatcher) executeChatRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	speaker := chatSpeakerForTurnInput(input)
	d.emit("agent.start", speaker, "user", "考え中...", "CHAT", taskIDText, sessionID, channel, chatID)
	streamHookCtx := ctx
	if d.speechPlanEnabled {
		streamHookCtx = withSpeechPlanStreamMode(streamHookCtx)
	}
	streamCtx, ttsStream := d.withStreamHooks(streamHookCtx, routing.RouteCHAT, speaker, taskIDText, sessionID, channel, chatID, ttsSessionID)
	var speechPlanResult *speech.Result
	if d.speechPlanEnabled {
		streamCtx = domainagent.ContextWithSpeechPlanCallback(streamCtx, func(result speech.Result) {
			captured := result
			speechPlanResult = &captured
		})
	}
	resp, err := d.generateChatResponse(streamCtx, input, speaker)
	if err != nil {
		if speechPlanResult != nil {
			d.logSpeechPlanObservation(string(input.TurnID()), speaker, *speechPlanResult, "")
		} else if d.speechPlanEnabled {
			ttsStream.Finalize(ctx, "")
			d.logSpeechPlanObservation(string(input.TurnID()), speaker, speech.Result{FallbackReason: speechPlanCallbackUnavailable}, "")
		}
		return resp, err
	}
	d.emit("agent.response", speaker, "user", resp, "CHAT", taskIDText, sessionID, channel, chatID)
	if speechPlanResult == nil {
		if d.speechPlanEnabled {
			ttsStream.Finalize(ctx, "")
			d.logSpeechPlanObservation(string(input.TurnID()), speaker, speech.Result{FallbackReason: speechPlanCallbackUnavailable}, "")
			return resp, nil
		}
		// SpeechPlan is disabled, so deterministic shortcuts keep the existing
		// text-only stream behavior.
		ttsStream.Finalize(ctx, resp)
		return resp, nil
	}

	// Close and drain legacy forwarders without sending the JSON plan or its
	// display text through the old TTS/Avatar text path.
	ttsStream.Finalize(ctx, "")
	result := *speechPlanResult
	routeFallbackReason := ""
	if result.Valid {
		if strings.TrimSpace(result.DisplayText) == "" {
			routeFallbackReason = speechPlanDisplayUnavailable
		} else if d.pushSpeechPlan == nil {
			routeFallbackReason = speechPlanHandoffUnavailable
		} else if pushErr := d.pushSpeechPlan(ctx, ttsSessionID, sessionID, string(input.TurnID()), speaker, result.Plan); pushErr != nil {
			routeFallbackReason = speechPlanHandoffFailed
		}
	}
	d.logSpeechPlanObservation(string(input.TurnID()), speaker, result, routeFallbackReason)
	return resp, nil
}

func (d *messageRouteDispatcher) generateChatResponse(ctx context.Context, input domainconversation.TurnInput, speaker string) (string, error) {
	switch speaker {
	case string(modulechat.ViewerRecipientMio):
		return d.mio.Chat(ctx, input)
	case string(modulechat.ViewerRecipientShiro):
		if d.shiroChat == nil {
			return "", fmt.Errorf("no ChatWorker agent available for Shiro CHAT")
		}
		return d.shiroChat.Chat(ctx, input)
	case string(modulechat.ViewerRecipientMidori):
		if d.wild == nil {
			return "", fmt.Errorf("no Wild agent available for Midori CHAT")
		}
		return d.wild.Generate(ctx, input)
	case string(modulechat.ViewerRecipientKuro):
		if d.heavy == nil {
			return "", fmt.Errorf("no heavy agent available for Kuro CHAT")
		}
		return d.heavy.Generate(ctx, input)
	default:
		return "", fmt.Errorf("unsupported CHAT recipient %q", speaker)
	}
}

func chatSpeakerForTurnInput(input domainconversation.TurnInput) string {
	recipient := normalizeProcessViewerRecipient(input.ViewerRecipient())
	if recipient == "" {
		return string(modulechat.DefaultViewerRecipient)
	}
	return recipient
}

func (d *messageRouteDispatcher) logSpeechPlanObservation(turnID, speakerID string, result speech.Result, routeFallbackReason string) {
	fallbackReason := routeFallbackReason
	if fallbackReason == "" {
		fallbackReason = result.FallbackReason
	}
	fallbackReason = boundedSpeechPlanFallbackReason(fallbackReason, result.Valid)

	observation := speechPlanRouteObservation{
		Event:            "speech.plan.observed",
		TurnID:           turnID,
		SpeakerID:        speakerID,
		SpeechPlanValid:  result.Valid,
		UtteranceCount:   boundedSpeechPlanUtteranceCount(result.Plan),
		CueCount:         boundedSpeechPlanCueCount(result.Plan),
		LLMGenerationMS:  result.LLMGenerationMS,
		PlanValidationMS: result.PlanValidationMS,
		FallbackReason:   fallbackReason,
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return
	}
	log.New(log.Writer(), "", 0).Print(string(encoded))
}

func boundedSpeechPlanFallbackReason(reason string, valid bool) string {
	switch reason {
	case "":
		if valid {
			return ""
		}
		return "speech_plan_unavailable"
	case speech.FallbackSpeechPlanLimitExceeded,
		speech.FallbackUnknownField,
		speech.FallbackInvalidJSON,
		speech.FallbackPlainText,
		speech.FallbackInvalidSchema,
		speech.FallbackInvalidStructure,
		speech.FallbackInvalidSegmentText,
		speech.FallbackUnsupportedEmoji,
		speech.FallbackGenerationFailed,
		speechPlanHandoffUnavailable,
		speechPlanHandoffFailed,
		speechPlanDisplayUnavailable,
		speechPlanCallbackUnavailable:
		return reason
	default:
		return "speech_plan_unavailable"
	}
}

func boundedSpeechPlanUtteranceCount(plan speech.SpeechPlan) int {
	if len(plan.Utterances) > speech.MaxUtterances {
		return speech.MaxUtterances
	}
	return len(plan.Utterances)
}

func boundedSpeechPlanCueCount(plan speech.SpeechPlan) int {
	count := 0
	for utteranceIndex, utterance := range plan.Utterances {
		if utteranceIndex == speech.MaxUtterances {
			break
		}
		for segmentIndex, segment := range utterance.Segments {
			if segmentIndex == speech.MaxSegmentsPerUtterance {
				break
			}
			count += min(len(segment.Before), speech.MaxCuesPerPosition)
			count += min(len(segment.After), speech.MaxCuesPerPosition)
		}
	}
	return count
}

func (d *messageRouteDispatcher) executeOPSRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	d.emit("agent.start", "mio", "shiro", "タスクを実行依頼", "OPS", taskIDText, sessionID, channel, chatID)
	resp, err := d.shiro.Execute(ctx, input)
	if err == nil {
		d.emit("agent.response", "shiro", "mio", resp, "OPS", taskIDText, sessionID, channel, chatID)
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, taskIDText, resp), "OPS", taskIDText, sessionID, channel, chatID)
		d.emit("agent.response", "mio", "user", resp, "OPS", taskIDText, sessionID, channel, chatID)
		d.pushTTS(ctx, ttsSessionID, routing.RouteOPS, moduletts.AgentShiro, "agent.response", resp)
	} else {
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, taskIDText, "実行失敗: "+err.Error()), "OPS", taskIDText, sessionID, channel, chatID)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeRoute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	resp, err := d.executeCodeViaShiro(ctx, input, route, taskID)
	if err == nil {
		d.pushTTS(ctx, ttsSessionID, route, moduletts.AgentShiro, "agent.response", resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeWildRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	if d.wild == nil {
		return "", fmt.Errorf("no wild agent available")
	}
	taskIDText := taskID.String()
	work := fmt.Sprintf("route=%s task=%s の創作", routing.RouteWILD.String(), taskIDText)
	d.emit("agent.delegate", "mio", "midori", formatAgentHandoffSpeech("mio", "midori", work, input.MessageText()), "WILD", taskIDText, sessionID, channel, chatID)
	d.emit("agent.acknowledge", "midori", "mio", formatAgentHandoffReadbackSpeech("mio", "midori", work, input.MessageText()), "WILD", taskIDText, sessionID, channel, chatID)
	d.emit("agent.start", "mio", "midori", "創作中...", "WILD", taskIDText, sessionID, channel, chatID)
	streamCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteWILD, "midori", taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.wild.Generate(streamCtx, input)
	if err == nil {
		d.emit("agent.response", "midori", "mio", resp, "WILD", taskIDText, sessionID, channel, chatID)
		d.emit("agent.report", "midori", "mio", formatAgentHandoffCompletionSpeech("mio", "midori", resp), "WILD", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	} else {
		d.emit("agent.report", "midori", "mio", formatAgentHandoffCompletionSpeech("mio", "midori", "実行失敗: "+err.Error()), "WILD", taskIDText, sessionID, channel, chatID)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executePlanRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	d.emit("agent.start", "mio", "user", "計画を検討中...", "PLAN", taskIDText, sessionID, channel, chatID)
	planCtx, ttsStream := d.withStreamHooks(ctx, routing.RoutePLAN, "mio", taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(planCtx, input)
	if err == nil {
		d.emit("agent.response", "mio", "user", resp, "PLAN", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeAnalyzeRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	if d.heavy == nil {
		return "", fmt.Errorf("no heavy agent available")
	}
	work := fmt.Sprintf("route=%s task=%s の分析", routing.RouteANALYZE.String(), taskIDText)
	d.emit("agent.delegate", "mio", "kuro", formatAgentHandoffSpeech("mio", "kuro", work, input.MessageText()), "ANALYZE", taskIDText, sessionID, channel, chatID)
	d.emit("agent.acknowledge", "kuro", "mio", formatAgentHandoffReadbackSpeech("mio", "kuro", work, input.MessageText()), "ANALYZE", taskIDText, sessionID, channel, chatID)
	d.emit("agent.start", "mio", "kuro", "分析中...", "ANALYZE", taskIDText, sessionID, channel, chatID)
	recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "started", "Heavy Worker started", taskIDText)
	analyzeCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteANALYZE, "kuro", taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.heavy.Generate(analyzeCtx, input)
	if err == nil {
		d.emit("agent.response", "kuro", "mio", resp, "ANALYZE", taskIDText, sessionID, channel, chatID)
		d.emit("agent.report", "kuro", "mio", formatAgentHandoffCompletionSpeech("mio", "kuro", resp), "ANALYZE", taskIDText, sessionID, channel, chatID)
		d.emit("agent.response", "mio", "user", resp, "ANALYZE", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
		recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "completed", "Heavy Worker completed", taskIDText)
	} else {
		d.emit("agent.report", "kuro", "mio", formatAgentHandoffCompletionSpeech("mio", "kuro", "実行失敗: "+err.Error()), "ANALYZE", taskIDText, sessionID, channel, chatID)
		recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "failed", err.Error(), taskIDText)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeResearchRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	d.emit("agent.start", "mio", "user", "調査中...", "RESEARCH", taskIDText, sessionID, channel, chatID)
	researchCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteRESEARCH, "mio", taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(researchCtx, input)
	if err == nil {
		d.emit("agent.response", "mio", "user", resp, "RESEARCH", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeViaShiro(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID) (string, error) {
	req := CodeExecutionRequest{
		Input:  input.WithRoute(route),
		TaskID: taskID,
	}
	resp, err := d.codeExecutor.ExecuteCode(ctx, req)
	return resp.Response, err
}
