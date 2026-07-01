package orchestrator

import (
	"context"
	"fmt"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	modulechat "github.com/Nyukimin/picoclaw_multiLLM/modules/chat"
)

type messageStreamHook func(ctx context.Context, route routing.Route, jid, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle)

type messageTTSPusher func(ctx context.Context, sessionID string, route routing.Route, eventType, text string)

type messageRouteDispatcher struct {
	mio               MioAgent
	shiro             ShiroAgent
	wild              WildAgent
	heavy             HeavyAgent
	codeExecutor      CodeExecutor
	emit              messageEventEmitter
	withStreamHooks   messageStreamHook
	pushTTS           messageTTSPusher
	executeAutonomous autonomousRouteExecutor
	workflowEvents    WorkflowEventRecorder
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

func (d *messageRouteDispatcher) SetAutonomousExecutor(execute autonomousRouteExecutor) {
	d.executeAutonomous = execute
}

func (d *messageRouteDispatcher) SetWorkflowEventRecorder(recorder WorkflowEventRecorder) {
	d.workflowEvents = recorder
}

func (d *messageRouteDispatcher) ExecuteTask(ctx context.Context, t task.Task, route routing.Route, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	if route != routing.RouteCHAT {
		if shouldTraceShiroDelegation(route) {
			d.emit("agent.delegate", "mio", "shiro", formatMioToShiroInstruction(t, route), route.String(), t.JobID().String(), sessionID, channel, chatID)
		}
		return d.executeAutonomous(ctx, t, route, sessionID, channel, chatID, ttsSessionID)
	}

	return d.executeChatRoute(ctx, t, sessionID, channel, chatID, ttsSessionID)
}

func (d *messageRouteDispatcher) ExecuteDirect(ctx context.Context, t task.Task, route routing.Route, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	switch route {
	case routing.RouteOPS:
		return d.executeOPSRoute(ctx, t, sessionID, channel, chatID, ttsSessionID)
	case routing.RouteCODE, routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4:
		return d.executeCodeRoute(ctx, t, route, sessionID, channel, chatID, ttsSessionID)
	case routing.RouteWILD:
		return d.executeWildRoute(ctx, t, sessionID, channel, chatID, ttsSessionID)
	case routing.RoutePLAN:
		return d.executePlanRoute(ctx, t, sessionID, channel, chatID, ttsSessionID)
	case routing.RouteANALYZE:
		return d.executeAnalyzeRoute(ctx, t, sessionID, channel, chatID, ttsSessionID)
	case routing.RouteRESEARCH:
		return d.executeResearchRoute(ctx, t, sessionID, channel, chatID, ttsSessionID)
	default:
		return "", fmt.Errorf("unsupported autonomous route: %s", route)
	}
}

func (d *messageRouteDispatcher) executeChatRoute(ctx context.Context, t task.Task, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	jid := t.JobID().String()
	speaker := chatSpeakerForTask(t)
	d.emit("agent.start", speaker, "user", "考え中...", "CHAT", jid, sessionID, channel, chatID)
	streamCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteCHAT, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(streamCtx, t)
	if err == nil {
		d.emit("agent.response", speaker, "user", resp, "CHAT", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func chatSpeakerForTask(t task.Task) string {
	recipient := normalizeProcessViewerRecipient(t.ViewerRecipient())
	if recipient == "" {
		return string(modulechat.DefaultViewerRecipient)
	}
	return recipient
}

func (d *messageRouteDispatcher) executeOPSRoute(ctx context.Context, t task.Task, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	jid := t.JobID().String()
	d.emit("agent.start", "mio", "shiro", "タスクを実行依頼", "OPS", jid, sessionID, channel, chatID)
	resp, err := d.shiro.Execute(ctx, t)
	if err == nil {
		d.emit("agent.response", "shiro", "mio", resp, "OPS", jid, sessionID, channel, chatID)
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, jid, resp), "OPS", jid, sessionID, channel, chatID)
		d.pushTTS(ctx, ttsSessionID, routing.RouteOPS, "agent.response", resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeRoute(ctx context.Context, t task.Task, route routing.Route, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	resp, err := d.executeCodeViaShiro(ctx, t, route, sessionID, channel, chatID)
	if err == nil {
		d.pushTTS(ctx, ttsSessionID, route, "agent.response", resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeWildRoute(ctx context.Context, t task.Task, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	if d.wild == nil {
		return "", fmt.Errorf("no wild agent available")
	}
	jid := t.JobID().String()
	d.emit("agent.start", "mio", "wild", "創作中...", "WILD", jid, sessionID, channel, chatID)
	streamCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteWILD, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.wild.Generate(streamCtx, t)
	if err == nil {
		d.emit("agent.response", "wild", "mio", resp, "WILD", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executePlanRoute(ctx context.Context, t task.Task, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	jid := t.JobID().String()
	d.emit("agent.start", "mio", "user", "計画を検討中...", "PLAN", jid, sessionID, channel, chatID)
	planCtx, ttsStream := d.withStreamHooks(ctx, routing.RoutePLAN, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(planCtx, t)
	if err == nil {
		d.emit("agent.response", "mio", "user", resp, "PLAN", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeAnalyzeRoute(ctx context.Context, t task.Task, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	jid := t.JobID().String()
	if d.heavy == nil {
		return "", fmt.Errorf("no heavy agent available")
	}
	d.emit("agent.start", "mio", "heavy", "分析中...", "ANALYZE", jid, sessionID, channel, chatID)
	recordHeavyWorkflowEvent(ctx, d.workflowEvents, "started", "Heavy Worker started", jid)
	analyzeCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteANALYZE, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.heavy.Generate(analyzeCtx, t)
	if err == nil {
		d.emit("agent.response", "heavy", "mio", resp, "ANALYZE", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
		recordHeavyWorkflowEvent(ctx, d.workflowEvents, "completed", "Heavy Worker completed", jid)
	} else {
		recordHeavyWorkflowEvent(ctx, d.workflowEvents, "failed", err.Error(), jid)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeResearchRoute(ctx context.Context, t task.Task, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	jid := t.JobID().String()
	d.emit("agent.start", "mio", "user", "調査中...", "RESEARCH", jid, sessionID, channel, chatID)
	researchCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteRESEARCH, jid, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(researchCtx, t)
	if err == nil {
		d.emit("agent.response", "mio", "user", resp, "RESEARCH", jid, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeViaShiro(
	ctx context.Context,
	t task.Task,
	route routing.Route,
	sessionID, channel, chatID string,
) (string, error) {
	// Phase 1リファクタリング: CodeExecutorに委譲
	req := CodeExecutionRequest{
		Task:      t,
		Route:     route,
		SessionID: sessionID,
		Channel:   channel,
		ChatID:    chatID,
		JobID:     t.JobID().String(),
	}
	resp, err := d.codeExecutor.ExecuteCode(ctx, req)
	return resp.Response, err
}
