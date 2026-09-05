package orchestrator

import (
	"context"
	"fmt"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type messageStreamHook func(ctx context.Context, route routing.Route, taskID, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle)

type messageTTSPusher func(ctx context.Context, sessionID string, route routing.Route, eventType, text string)

type messageRouteDispatcher struct {
	mio               MioAgent
	shiroChat         MioAgent
	shiro             ShiroAgent
	wild              WildAgent
	heavy             HeavyAgent
	codeExecutor      CodeExecutor
	emit              messageEventEmitter
	withStreamHooks   messageStreamHook
	pushTTS           messageTTSPusher
	executeAutonomous autonomousRouteExecutor
	canonicalEvents   CanonicalEventRecorder
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

func (d *messageRouteDispatcher) ExecuteTurnInput(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	var err error
	ctx, err = domainexecution.WithIdentity(ctx, taskID, input.TraceID())
	if err != nil {
		return "", err
	}
	input = input.WithRoute(route)
	if route != routing.RouteCHAT {
		if shouldTraceShiroDelegation(route) {
			sessionID, channel, chatID := turnInputMetadata(input)
			d.emit("agent.delegate", "mio", "shiro", formatMioToShiroInstruction(input, route, taskID.String()), route.String(), taskID.String(), sessionID, channel, chatID)
			d.emit("agent.acknowledge", "shiro", "mio", formatShiroReadbackToMio(input, route, taskID.String()), route.String(), taskID.String(), sessionID, channel, chatID)
		}
		return d.executeAutonomous(ctx, input, route, taskID, ttsSessionID)
	}

	return d.executeChatRoute(ctx, input, taskID, ttsSessionID)
}

func (d *messageRouteDispatcher) ExecuteDirect(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	var err error
	ctx, err = domainexecution.WithIdentity(ctx, taskID, input.TraceID())
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

func (d *messageRouteDispatcher) executeChatRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	speaker := chatSpeakerForTurnInput(input)
	d.emit("agent.start", speaker, "user", "考え中...", "CHAT", taskIDText, sessionID, channel, chatID)
	streamCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteCHAT, taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.generateChatResponse(streamCtx, input, speaker)
	if err == nil {
		d.emit("agent.response", speaker, "user", resp, "CHAT", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
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

func (d *messageRouteDispatcher) executeOPSRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	shiroCtx, err := domaintool.DeriveAgentToolExecutionScope(ctx, taskIDText, "shiro", "worker", "ops", true)
	if err != nil {
		return "", err
	}
	d.emit("agent.start", "mio", "shiro", "タスクを実行依頼", "OPS", taskIDText, sessionID, channel, chatID)
	resp, err := d.shiro.Execute(shiroCtx, input)
	if err == nil {
		d.emit("agent.response", "shiro", "mio", resp, "OPS", taskIDText, sessionID, channel, chatID)
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, taskIDText, resp), "OPS", taskIDText, sessionID, channel, chatID)
		d.emit("agent.response", "mio", "user", resp, "OPS", taskIDText, sessionID, channel, chatID)
		d.pushTTS(ctx, ttsSessionID, routing.RouteOPS, "agent.response", resp)
	} else {
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, taskIDText, "実行失敗: "+err.Error()), "OPS", taskIDText, sessionID, channel, chatID)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeRoute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	resp, err := d.executeCodeViaShiro(ctx, input, route, taskID)
	if err == nil {
		d.pushTTS(ctx, ttsSessionID, route, "agent.response", resp)
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
	streamCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteWILD, taskIDText, sessionID, channel, chatID, ttsSessionID)
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
	planCtx, ttsStream := d.withStreamHooks(ctx, routing.RoutePLAN, taskIDText, sessionID, channel, chatID, ttsSessionID)
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
	analyzeCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteANALYZE, taskIDText, sessionID, channel, chatID, ttsSessionID)
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
	researchCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteRESEARCH, taskIDText, sessionID, channel, chatID, ttsSessionID)
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
