package orchestrator

import (
	"context"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

type distributedAutonomousExecutor func(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error)
type distributedCodeExecutor func(ctx context.Context, t task.Task, route routing.Route, sessionID, jid string) (string, error)
type distributedRouteToAgent func(route routing.Route) string
type distributedAttributionGuardFunc func(t task.Task, targetAgent, sessionID string) task.Task
type distributedAgentTransportExecutor func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error)
type distributedNoteEmitter func(from, to, content, route, jobID, sessionID, channel, chatID string)

type distributedRouteDispatcher struct {
	mio                 MioAgent
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
	workflowEvents      WorkflowEventRecorder
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

func (d *distributedRouteDispatcher) SetAutonomousExecutor(execute distributedAutonomousExecutor) {
	d.executeAutonomous = execute
}

func (d *distributedRouteDispatcher) SetWorkflowEventRecorder(recorder WorkflowEventRecorder) {
	d.workflowEvents = recorder
}

func (d *distributedRouteDispatcher) ExecuteTask(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
	if route != routing.RouteCHAT {
		return d.executeAutonomous(ctx, t, route, sessionID, ttsSessionID)
	}
	return d.ExecuteDirect(ctx, t, route, sessionID, ttsSessionID)
}

func (d *distributedRouteDispatcher) ExecuteDirect(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
	jid := t.JobID().String()
	if isCodeRoute(route) {
		resp, err := d.executeCodeViaShiro(ctx, t, route, sessionID, jid)
		if err == nil {
			d.emit("agent.response", "mio", "user", resp, string(route), jid, sessionID, t.Channel(), t.ChatID())
			d.emitNote("mio", "user", "コード作業の報告をまとめて返したよ。", string(route), jid, sessionID, t.Channel(), t.ChatID())
			d.pushTTS(ctx, ttsSessionID, route, "agent.response", resp)
		}
		return resp, err
	}
	if route == routing.RouteWILD {
		if d.wild == nil {
			return "", fmt.Errorf("no wild agent available")
		}
		work := fmt.Sprintf("route=%s job=%s の創作", route, jid)
		d.emit("agent.delegate", "mio", "wild", formatAgentHandoffSpeech("mio", "wild", work, t.UserMessage()), string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emit("agent.acknowledge", "wild", "mio", formatAgentHandoffReadbackSpeech("mio", "wild", work, t.UserMessage()), string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emit("agent.start", "mio", "wild", "創作中...", string(route), jid, sessionID, t.Channel(), t.ChatID())
		streamCtx, ttsStream := d.withStreamHooks(ctx, route, jid, sessionID, t.Channel(), t.ChatID(), ttsSessionID)
		resp, err := d.wild.Generate(streamCtx, t)
		if err == nil {
			d.emit("agent.response", "wild", "mio", resp, string(route), jid, sessionID, t.Channel(), t.ChatID())
			d.emit("agent.report", "wild", "mio", formatAgentHandoffCompletionSpeech("mio", "wild", resp), string(route), jid, sessionID, t.Channel(), t.ChatID())
			d.emit("agent.response", "mio", "user", resp, string(route), jid, sessionID, t.Channel(), t.ChatID())
			ttsStream.Finalize(ctx, resp)
		} else {
			d.emit("agent.report", "wild", "mio", formatAgentHandoffCompletionSpeech("mio", "wild", "実行失敗: "+err.Error()), string(route), jid, sessionID, t.Channel(), t.ChatID())
		}
		return resp, err
	}
	if route == routing.RouteANALYZE {
		if d.heavy == nil {
			return "", fmt.Errorf("no heavy agent available")
		}
		work := fmt.Sprintf("route=%s job=%s の分析", route, jid)
		d.emit("agent.delegate", "mio", "heavy", formatAgentHandoffSpeech("mio", "heavy", work, t.UserMessage()), string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emit("agent.acknowledge", "heavy", "mio", formatAgentHandoffReadbackSpeech("mio", "heavy", work, t.UserMessage()), string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emit("agent.start", "mio", "heavy", "分析中...", string(route), jid, sessionID, t.Channel(), t.ChatID())
		recordHeavyWorkflowEvent(ctx, d.workflowEvents, "started", "Heavy Worker started", jid)
		streamCtx, ttsStream := d.withStreamHooks(ctx, route, jid, sessionID, t.Channel(), t.ChatID(), ttsSessionID)
		resp, err := d.heavy.Generate(streamCtx, t)
		if err == nil {
			d.emit("agent.response", "heavy", "mio", resp, string(route), jid, sessionID, t.Channel(), t.ChatID())
			d.emit("agent.report", "heavy", "mio", formatAgentHandoffCompletionSpeech("mio", "heavy", resp), string(route), jid, sessionID, t.Channel(), t.ChatID())
			d.emit("agent.response", "mio", "user", resp, string(route), jid, sessionID, t.Channel(), t.ChatID())
			ttsStream.Finalize(ctx, resp)
			recordHeavyWorkflowEvent(ctx, d.workflowEvents, "completed", "Heavy Worker completed", jid)
		} else {
			d.emit("agent.report", "heavy", "mio", formatAgentHandoffCompletionSpeech("mio", "heavy", "実行失敗: "+err.Error()), string(route), jid, sessionID, t.Channel(), t.ChatID())
			recordHeavyWorkflowEvent(ctx, d.workflowEvents, "failed", err.Error(), jid)
		}
		return resp, err
	}
	targetAgent := d.routeToAgent(route)
	if targetAgent == "" {
		return d.executeLocalRoute(ctx, t, route, sessionID, ttsSessionID, jid)
	}
	return d.executeRemoteRoute(ctx, t, route, sessionID, ttsSessionID, jid, targetAgent)
}

func (d *distributedRouteDispatcher) executeLocalRoute(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID, jid string) (string, error) {
	speaker := chatSpeakerForTask(t)
	guardedTask := d.withAttribution(t, "mio", sessionID)
	userMsg := domaintransport.NewMessage("user", speaker, sessionID, jid, t.UserMessage())
	userMsg.Type = domaintransport.MessageTypeTask
	d.memory.RecordMessage(userMsg)

	d.emit("agent.start", speaker, "user", "考え中...", string(route), jid, sessionID, t.Channel(), t.ChatID())
	streamCtx, ttsStream := d.withStreamHooks(ctx, route, jid, sessionID, t.Channel(), t.ChatID(), ttsSessionID)
	resp, err := d.mio.Chat(streamCtx, guardedTask)
	if err == nil {
		respMsg := domaintransport.NewMessage(speaker, "user", sessionID, jid, resp)
		respMsg.Type = domaintransport.MessageTypeResult
		d.memory.RecordMessage(respMsg)
		d.emit("agent.response", speaker, "user", resp, string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emitNote(speaker, "user", "会話処理が終わったよ。", string(route), jid, sessionID, t.Channel(), t.ChatID())
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *distributedRouteDispatcher) executeRemoteRoute(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID, jid, targetAgent string) (string, error) {
	guardedTask := d.withAttribution(t, targetAgent, sessionID)
	msg := domaintransport.NewMessage("mio", targetAgent, sessionID, jid, guardedTask.UserMessage())
	msg.Type = domaintransport.MessageTypeTask
	msg.Context = map[string]interface{}{
		"route":   string(route),
		"channel": t.Channel(),
		"chat_id": t.ChatID(),
	}

	work := fmt.Sprintf("route=%s job=%s の作業", route, jid)
	d.emit("agent.delegate", "mio", targetAgent, formatAgentHandoffSpeech("mio", targetAgent, work, t.UserMessage()), string(route), jid, sessionID, t.Channel(), t.ChatID())
	d.emit("agent.acknowledge", targetAgent, "mio", formatAgentHandoffReadbackSpeech("mio", targetAgent, work, t.UserMessage()), string(route), jid, sessionID, t.Channel(), t.ChatID())
	d.emit("agent.start", "mio", targetAgent, t.UserMessage(), string(route), jid, sessionID, t.Channel(), t.ChatID())
	d.emit("agent.dispatch", "mio", targetAgent, "ルーティング先へ依頼を転送", string(route), jid, sessionID, t.Channel(), t.ChatID())
	d.memory.RecordMessage(msg)

	result, err := d.executeToAgent(ctx, targetAgent, msg)
	if err == nil {
		d.emit("agent.response", targetAgent, "mio", result.Content, string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emit("agent.report", targetAgent, "mio", formatAgentHandoffCompletionSpeech("mio", targetAgent, result.Content), string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emitNote(targetAgent, "mio",
			fmt.Sprintf("%s の作業が終わりました。", displayAgentName(targetAgent)),
			string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emit("agent.response", "mio", "user", result.Content, string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.emitNote("mio", "user", fmt.Sprintf("%sの報告をまとめて返したよ。", displayAgentName(targetAgent)), string(route), jid, sessionID, t.Channel(), t.ChatID())
		d.pushTTS(ctx, ttsSessionID, route, "agent.response", result.Content)
	} else {
		d.emit("agent.report", targetAgent, "mio", formatAgentHandoffCompletionSpeech("mio", targetAgent, "実行失敗: "+err.Error()), string(route), jid, sessionID, t.Channel(), t.ChatID())
	}
	return result.Content, err
}
