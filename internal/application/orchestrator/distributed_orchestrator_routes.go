package orchestrator

import (
	"context"
	"fmt"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type distributedAutonomousExecutor func(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error)
type distributedCodeExecutor func(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID) (string, error)
type distributedRouteToAgent func(route routing.Route) string
type distributedAttributionGuardFunc func(input domainconversation.TurnInput, targetAgent string) domainconversation.TurnInput
type distributedAgentTransportExecutor func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error)
type distributedNoteEmitter func(from, to, content, route, taskID, sessionID, channel, chatID string)

type distributedRouteDispatcher struct {
	mio                 MioAgent
	shiroChat           MioAgent
	wild                WildAgent
	heavy               HeavyAgent
	memory              *session.CentralMemory
	emit                messageEventEmitter
	emitNote            distributedNoteEmitter
	withStreamHooks     messageStreamHook
	pushTTS             messageTTSPusher
	executeAutonomous   distributedAutonomousExecutor
	executeCodeViaShiro distributedCodeExecutor
	routeToAgent        distributedRouteToAgent
	withAttribution     distributedAttributionGuardFunc
	executeToAgent      distributedAgentTransportExecutor
	canonicalEvents     CanonicalEventRecorder
}

func newDistributedRouteDispatcher(
	mio MioAgent,
	memory *session.CentralMemory,
	emit messageEventEmitter,
	emitNote distributedNoteEmitter,
	withStreamHooks messageStreamHook,
	pushTTS messageTTSPusher,
	executeCodeViaShiro distributedCodeExecutor,
	routeToAgent distributedRouteToAgent,
	withAttribution distributedAttributionGuardFunc,
	executeToAgent distributedAgentTransportExecutor,
) *distributedRouteDispatcher {
	return &distributedRouteDispatcher{
		mio:                 mio,
		memory:              memory,
		emit:                emit,
		emitNote:            emitNote,
		withStreamHooks:     withStreamHooks,
		pushTTS:             pushTTS,
		executeCodeViaShiro: executeCodeViaShiro,
		routeToAgent:        routeToAgent,
		withAttribution:     withAttribution,
		executeToAgent:      executeToAgent,
	}
}

func (d *distributedRouteDispatcher) SetWildAgent(wild WildAgent) {
	d.wild = wild
}

func (d *distributedRouteDispatcher) SetHeavyAgent(heavy HeavyAgent) {
	d.heavy = heavy
}

func (d *distributedRouteDispatcher) SetShiroChatAgent(chat MioAgent) {
	d.shiroChat = chat
}

func (d *distributedRouteDispatcher) SetAutonomousExecutor(execute distributedAutonomousExecutor) {
	d.executeAutonomous = execute
}

func (d *distributedRouteDispatcher) SetCanonicalEventRecorder(recorder CanonicalEventRecorder) {
	d.canonicalEvents = recorder
}

func (d *distributedRouteDispatcher) ExecuteTurnInput(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID, ttsSessionID string) (string, error) {
	var err error
	ctx, err = domainexecution.WithIdentity(ctx, taskID, runID, input.TraceID())
	if err != nil {
		return "", err
	}
	input = input.WithRoute(route)
	if route != routing.RouteCHAT {
		return d.executeAutonomous(ctx, input, route, taskID, ttsSessionID)
	}
	return d.ExecuteDirect(ctx, input, route, taskID, runID, ttsSessionID)
}

func (d *distributedRouteDispatcher) ExecuteDirect(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID, ttsSessionID string) (string, error) {
	var err error
	ctx, err = domainexecution.WithIdentity(ctx, taskID, runID, input.TraceID())
	if err != nil {
		return "", err
	}
	input = input.WithRoute(route)
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	if isCodeRoute(route) {
		resp, err := d.executeCodeViaShiro(ctx, input, route, taskID)
		if err == nil {
			d.emit("agent.response", "mio", "user", resp, string(route), taskIDText, sessionID, channel, chatID)
			d.emitNote("mio", "user", "コード作業の報告をまとめて返したよ。", string(route), taskIDText, sessionID, channel, chatID)
			d.pushTTS(ctx, ttsSessionID, route, "agent.response", resp)
		}
		return resp, err
	}
	if route == routing.RouteWILD {
		if d.wild == nil {
			return "", fmt.Errorf("no wild agent available")
		}
		work := fmt.Sprintf("route=%s task=%s の創作", route, taskIDText)
		d.emit("agent.delegate", "mio", "midori", formatAgentHandoffSpeech("mio", "midori", work, input.MessageText()), string(route), taskIDText, sessionID, channel, chatID)
		d.emit("agent.acknowledge", "midori", "mio", formatAgentHandoffReadbackSpeech("mio", "midori", work, input.MessageText()), string(route), taskIDText, sessionID, channel, chatID)
		d.emit("agent.start", "mio", "midori", "創作中...", string(route), taskIDText, sessionID, channel, chatID)
		streamCtx, ttsStream := d.withStreamHooks(ctx, route, taskIDText, sessionID, channel, chatID, ttsSessionID)
		resp, err := d.wild.Generate(streamCtx, input)
		if err == nil {
			d.emit("agent.response", "midori", "mio", resp, string(route), taskIDText, sessionID, channel, chatID)
			d.emit("agent.report", "midori", "mio", formatAgentHandoffCompletionSpeech("mio", "midori", resp), string(route), taskIDText, sessionID, channel, chatID)
			d.emit("agent.response", "mio", "user", resp, string(route), taskIDText, sessionID, channel, chatID)
			ttsStream.Finalize(ctx, resp)
		} else {
			d.emit("agent.report", "midori", "mio", formatAgentHandoffCompletionSpeech("mio", "midori", "実行失敗: "+err.Error()), string(route), taskIDText, sessionID, channel, chatID)
		}
		return resp, err
	}
	if route == routing.RouteANALYZE {
		if d.heavy == nil {
			return "", fmt.Errorf("no heavy agent available")
		}
		work := fmt.Sprintf("route=%s task=%s の分析", route, taskIDText)
		d.emit("agent.delegate", "mio", "kuro", formatAgentHandoffSpeech("mio", "kuro", work, input.MessageText()), string(route), taskIDText, sessionID, channel, chatID)
		d.emit("agent.acknowledge", "kuro", "mio", formatAgentHandoffReadbackSpeech("mio", "kuro", work, input.MessageText()), string(route), taskIDText, sessionID, channel, chatID)
		d.emit("agent.start", "mio", "kuro", "分析中...", string(route), taskIDText, sessionID, channel, chatID)
		recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "started", "Heavy Worker started", taskIDText)
		streamCtx, ttsStream := d.withStreamHooks(ctx, route, taskIDText, sessionID, channel, chatID, ttsSessionID)
		resp, err := d.heavy.Generate(streamCtx, input)
		if err == nil {
			d.emit("agent.response", "kuro", "mio", resp, string(route), taskIDText, sessionID, channel, chatID)
			d.emit("agent.report", "kuro", "mio", formatAgentHandoffCompletionSpeech("mio", "kuro", resp), string(route), taskIDText, sessionID, channel, chatID)
			d.emit("agent.response", "mio", "user", resp, string(route), taskIDText, sessionID, channel, chatID)
			ttsStream.Finalize(ctx, resp)
			recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "completed", "Heavy Worker completed", taskIDText)
		} else {
			d.emit("agent.report", "kuro", "mio", formatAgentHandoffCompletionSpeech("mio", "kuro", "実行失敗: "+err.Error()), string(route), taskIDText, sessionID, channel, chatID)
			recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "failed", err.Error(), taskIDText)
		}
		return resp, err
	}
	targetAgent := d.routeToAgent(route)
	if targetAgent == "" {
		return d.executeLocalRoute(ctx, input, route, taskID, ttsSessionID)
	}
	return d.executeRemoteRoute(ctx, input, route, taskID, ttsSessionID, targetAgent)
}

// executeDirectForContext is the adapter used by the bounded autonomous
// coordinator. Retries retain the owner-selected execution context; no RunID
// is generated or derived at this boundary.
func (d *distributedRouteDispatcher) executeDirectForContext(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return "", err
	}
	if identity.TaskID != taskID {
		return "", fmt.Errorf("execution task identity mismatch: context=%s argument=%s", identity.TaskID, taskID)
	}
	return d.ExecuteDirect(ctx, input, route, taskID, identity.RunID, ttsSessionID)
}

func (d *distributedRouteDispatcher) executeLocalRoute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	speaker := chatSpeakerForTurnInput(input)
	guardedInput := d.withAttribution(input, speaker)
	userMsg, err := domaintransport.NewTurnInputMessage("user", speaker, taskID, guardedInput)
	if err != nil {
		return "", fmt.Errorf("build local user turn message: %w", err)
	}
	userMsg.Type = domaintransport.MessageTypeTask
	d.memory.RecordMessage(userMsg)

	d.emit("agent.start", speaker, "user", "考え中...", string(route), taskIDText, sessionID, channel, chatID)
	streamCtx, ttsStream := d.withStreamHooks(ctx, route, taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.generateLocalChatResponse(streamCtx, guardedInput, speaker)
	if err == nil {
		respMsg := domaintransport.NewMessage(speaker, "user", sessionID, taskID, resp)
		respMsg.Type = domaintransport.MessageTypeResult
		d.memory.RecordMessage(respMsg)
		d.emit("agent.response", speaker, "user", resp, string(route), taskIDText, sessionID, channel, chatID)
		d.emitNote(speaker, "user", "会話処理が終わったよ。", string(route), taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *distributedRouteDispatcher) generateLocalChatResponse(ctx context.Context, input domainconversation.TurnInput, speaker string) (string, error) {
	switch speaker {
	case "mio":
		return d.mio.Chat(ctx, input)
	case "shiro":
		if d.shiroChat == nil {
			return "", fmt.Errorf("no ChatWorker agent available for Shiro CHAT")
		}
		return d.shiroChat.Chat(ctx, input)
	case "midori":
		if d.wild == nil {
			return "", fmt.Errorf("no Wild agent available for Midori CHAT")
		}
		return d.wild.Generate(ctx, input)
	case "kuro":
		if d.heavy == nil {
			return "", fmt.Errorf("no heavy agent available for Kuro CHAT")
		}
		return d.heavy.Generate(ctx, input)
	default:
		return "", fmt.Errorf("unsupported CHAT recipient %q", speaker)
	}
}

func (d *distributedRouteDispatcher) executeRemoteRoute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID, targetAgent string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	guardedInput := d.withAttribution(input, targetAgent)
	msg, err := domaintransport.NewTurnInputMessage("mio", targetAgent, taskID, guardedInput)
	if err != nil {
		return "", fmt.Errorf("build remote user turn message: %w", err)
	}
	msg.Type = domaintransport.MessageTypeTask
	msg.Context = map[string]interface{}{
		"route":   string(route),
		"channel": channel,
		"chat_id": chatID,
	}

	work := fmt.Sprintf("route=%s task=%s の作業", route, taskIDText)
	d.emit("agent.delegate", "mio", targetAgent, formatAgentHandoffSpeech("mio", targetAgent, work, input.MessageText()), string(route), taskIDText, sessionID, channel, chatID)
	d.emit("agent.acknowledge", targetAgent, "mio", formatAgentHandoffReadbackSpeech("mio", targetAgent, work, input.MessageText()), string(route), taskIDText, sessionID, channel, chatID)
	d.emit("agent.start", "mio", targetAgent, input.MessageText(), string(route), taskIDText, sessionID, channel, chatID)
	d.emit("agent.dispatch", "mio", targetAgent, "ルーティング先へ依頼を転送", string(route), taskIDText, sessionID, channel, chatID)
	d.memory.RecordMessage(msg)

	result, err := d.executeToAgent(ctx, targetAgent, msg)
	if err == nil {
		d.emit("agent.response", targetAgent, "mio", result.Content, string(route), taskIDText, sessionID, channel, chatID)
		d.emit("agent.report", targetAgent, "mio", formatAgentHandoffCompletionSpeech("mio", targetAgent, result.Content), string(route), taskIDText, sessionID, channel, chatID)
		d.emitNote(targetAgent, "mio", fmt.Sprintf("%s の作業が終わりました。", displayAgentName(targetAgent)), string(route), taskIDText, sessionID, channel, chatID)
		d.emit("agent.response", "mio", "user", result.Content, string(route), taskIDText, sessionID, channel, chatID)
		d.emitNote("mio", "user", fmt.Sprintf("%sの報告をまとめて返したよ。", displayAgentName(targetAgent)), string(route), taskIDText, sessionID, channel, chatID)
		d.pushTTS(ctx, ttsSessionID, route, "agent.response", result.Content)
	} else {
		d.emit("agent.report", targetAgent, "mio", formatAgentHandoffCompletionSpeech("mio", targetAgent, "実行失敗: "+err.Error()), string(route), taskIDText, sessionID, channel, chatID)
	}
	return result.Content, err
}
