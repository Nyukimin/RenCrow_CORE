package orchestrator

import (
	"context"
	"fmt"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type messageStreamHook func(ctx context.Context, route routing.Route, jid, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle)

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

func (d *messageRouteDispatcher) ExecuteTurnInput(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	input = input.WithRoute(route)
	if route != routing.RouteCHAT {
		if shouldTraceShiroDelegation(route) {
			sessionID, channel, chatID := turnInputMetadata(input)
			d.emit("agent.delegate", "mio", "shiro", formatMioToShiroInstruction(input, route, jobID.String()), route.String(), jobID.String(), sessionID, channel, chatID)
			d.emit("agent.acknowledge", "shiro", "mio", formatShiroReadbackToMio(input, route, jobID.String()), route.String(), jobID.String(), sessionID, channel, chatID)
		}
		return d.executeAutonomous(ctx, input, route, jobID, ttsSessionID)
	}

	return d.executeChatRoute(ctx, input, jobID, ttsSessionID)
}

func (d *messageRouteDispatcher) ExecuteDirect(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	input = input.WithRoute(route)
	switch route {
	case routing.RouteOPS:
		return d.executeOPSRoute(ctx, input, jobID, ttsSessionID)
	case routing.RouteCODE, routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4:
		return d.executeCodeRoute(ctx, input, route, jobID, ttsSessionID)
	case routing.RouteWILD:
		return d.executeWildRoute(ctx, input, jobID, ttsSessionID)
	case routing.RoutePLAN:
		return d.executePlanRoute(ctx, input, jobID, ttsSessionID)
	case routing.RouteANALYZE:
		return d.executeAnalyzeRoute(ctx, input, jobID, ttsSessionID)
	case routing.RouteRESEARCH:
		return d.executeResearchRoute(ctx, input, jobID, ttsSessionID)
	default:
		return "", fmt.Errorf("unsupported autonomous route: %s", route)
	}
}

func (d *messageRouteDispatcher) executeChatRoute(ctx context.Context, input domainconversation.TurnInput, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	jid := jobID.String()
	speaker := chatSpeakerForTurnInput(input)
	d.emit("agent.start", speaker, "user", "考え中...", "CHAT", jid, sessionID, channel, chatID)
	streamCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteCHAT, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.generateChatResponse(streamCtx, input, speaker)
	if err == nil {
		d.emit("agent.response", speaker, "user", resp, "CHAT", jid, sessionID, channel, chatID)
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

func (d *messageRouteDispatcher) executeOPSRoute(ctx context.Context, input domainconversation.TurnInput, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	jid := jobID.String()
	shiroCtx, err := domaintool.DeriveAgentToolExecutionScope(ctx, jid, "shiro", "worker", "ops", true)
	if err != nil {
		return "", err
	}
	d.emit("agent.start", "mio", "shiro", "タスクを実行依頼", "OPS", jid, sessionID, channel, chatID)
	resp, err := d.shiro.Execute(shiroCtx, input)
	if err == nil {
		d.emit("agent.response", "shiro", "mio", resp, "OPS", jid, sessionID, channel, chatID)
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, jid, resp), "OPS", jid, sessionID, channel, chatID)
		d.emit("agent.response", "mio", "user", resp, "OPS", jid, sessionID, channel, chatID)
		d.pushTTS(ctx, ttsSessionID, routing.RouteOPS, "agent.response", resp)
	} else {
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, jid, "実行失敗: "+err.Error()), "OPS", jid, sessionID, channel, chatID)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeRoute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	resp, err := d.executeCodeViaShiro(ctx, input, route, jobID)
	if err == nil {
		d.pushTTS(ctx, ttsSessionID, route, "agent.response", resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeWildRoute(ctx context.Context, input domainconversation.TurnInput, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	if d.wild == nil {
		return "", fmt.Errorf("no wild agent available")
	}
	jid := jobID.String()
	work := fmt.Sprintf("route=%s job=%s の創作", routing.RouteWILD.String(), jid)
	d.emit("agent.delegate", "mio", "wild", formatAgentHandoffSpeech("mio", "wild", work, input.MessageText()), "WILD", jid, sessionID, channel, chatID)
	d.emit("agent.acknowledge", "wild", "mio", formatAgentHandoffReadbackSpeech("mio", "wild", work, input.MessageText()), "WILD", jid, sessionID, channel, chatID)
	d.emit("agent.start", "mio", "wild", "創作中...", "WILD", jid, sessionID, channel, chatID)
	streamCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteWILD, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.wild.Generate(streamCtx, input)
	if err == nil {
		d.emit("agent.response", "wild", "mio", resp, "WILD", jid, sessionID, channel, chatID)
		d.emit("agent.report", "wild", "mio", formatAgentHandoffCompletionSpeech("mio", "wild", resp), "WILD", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	} else {
		d.emit("agent.report", "wild", "mio", formatAgentHandoffCompletionSpeech("mio", "wild", "実行失敗: "+err.Error()), "WILD", jid, sessionID, channel, chatID)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executePlanRoute(ctx context.Context, input domainconversation.TurnInput, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	jid := jobID.String()
	d.emit("agent.start", "mio", "user", "計画を検討中...", "PLAN", jid, sessionID, channel, chatID)
	planCtx, ttsStream := d.withStreamHooks(ctx, routing.RoutePLAN, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(planCtx, input)
	if err == nil {
		d.emit("agent.response", "mio", "user", resp, "PLAN", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeAnalyzeRoute(ctx context.Context, input domainconversation.TurnInput, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	jid := jobID.String()
	if d.heavy == nil {
		return "", fmt.Errorf("no heavy agent available")
	}
	work := fmt.Sprintf("route=%s job=%s の分析", routing.RouteANALYZE.String(), jid)
	d.emit("agent.delegate", "mio", "heavy", formatAgentHandoffSpeech("mio", "heavy", work, input.MessageText()), "ANALYZE", jid, sessionID, channel, chatID)
	d.emit("agent.acknowledge", "heavy", "mio", formatAgentHandoffReadbackSpeech("mio", "heavy", work, input.MessageText()), "ANALYZE", jid, sessionID, channel, chatID)
	d.emit("agent.start", "mio", "heavy", "分析中...", "ANALYZE", jid, sessionID, channel, chatID)
	recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "started", "Heavy Worker started", jid)
	analyzeCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteANALYZE, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.heavy.Generate(analyzeCtx, input)
	if err == nil {
		d.emit("agent.response", "heavy", "mio", resp, "ANALYZE", jid, sessionID, channel, chatID)
		d.emit("agent.report", "heavy", "mio", formatAgentHandoffCompletionSpeech("mio", "heavy", resp), "ANALYZE", jid, sessionID, channel, chatID)
		d.emit("agent.response", "mio", "user", resp, "ANALYZE", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
		recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "completed", "Heavy Worker completed", jid)
	} else {
		d.emit("agent.report", "heavy", "mio", formatAgentHandoffCompletionSpeech("mio", "heavy", "実行失敗: "+err.Error()), "ANALYZE", jid, sessionID, channel, chatID)
		recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "failed", err.Error(), jid)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeResearchRoute(ctx context.Context, input domainconversation.TurnInput, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	jid := jobID.String()
	d.emit("agent.start", "mio", "user", "調査中...", "RESEARCH", jid, sessionID, channel, chatID)
	researchCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteRESEARCH, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(researchCtx, input)
	if err == nil {
		d.emit("agent.response", "mio", "user", resp, "RESEARCH", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeViaShiro(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID) (string, error) {
	req := CodeExecutionRequest{
		Input: input.WithRoute(route),
		JobID: jobID.String(),
	}
	resp, err := d.codeExecutor.ExecuteCode(ctx, req)
	return resp.Response, err
}
