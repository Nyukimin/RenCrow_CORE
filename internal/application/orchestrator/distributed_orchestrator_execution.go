package orchestrator

import (
	"context"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

func (o *DistributedOrchestrator) saveExecutionReport(ctx context.Context, jobID, goal, route string, startedAt, finishedAt time.Time, runErr error) {
	o.evidence.Save(ctx, jobID, goal, route, startedAt, finishedAt, runErr)
}

// executeDistributed はルートに応じてTransport経由でAgent間通信
func (o *DistributedOrchestrator) executeDistributed(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
	return o.routes.ExecuteTask(ctx, t, route, sessionID, ttsSessionID)
}

func (o *DistributedOrchestrator) executeAutonomousDistributed(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
	return o.autonomous.Execute(ctx, t, route, sessionID, ttsSessionID)
}

func (o *DistributedOrchestrator) executeDistributedDirect(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
	return o.routes.ExecuteDirect(ctx, t, route, sessionID, ttsSessionID)
}

func (o *DistributedOrchestrator) withStreamHooks(
	ctx context.Context,
	route routing.Route,
	jid, sessionID, channel, chatID, ttsSessionID string,
) (context.Context, *streamBundle) {
	return o.ttsLifecycle.WithStreamHooks(ctx, route, jid, sessionID, channel, chatID, ttsSessionID)
}

func (o *DistributedOrchestrator) pushTTS(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {
	o.ttsLifecycle.Push(ctx, sessionID, route, eventType, text)
}

func (o *DistributedOrchestrator) executeCodeViaShiro(
	ctx context.Context,
	t task.Task,
	route routing.Route,
	sessionID, jid string,
) (string, error) {
	return o.codeExecution.Execute(ctx, t, route, sessionID, jid)
}

// executeViaSSH はSSH Transport経由でリモートAgentと通信
// SSHTransportは1:1接続のため、同一transport上でSend→Receiveする
func (o *DistributedOrchestrator) executeViaSSH(ctx context.Context, sshTransport domaintransport.Transport, targetAgent string, msg domaintransport.Message) (string, error) {
	return o.transports.ExecuteViaSSH(ctx, sshTransport, targetAgent, msg)
}

func (o *DistributedOrchestrator) executeToAgent(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
	return o.transports.ExecuteToAgent(ctx, targetAgent, msg)
}

func (o *DistributedOrchestrator) executeToAgentViaMailbox(ctx context.Context, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error) {
	return o.transports.ExecuteToAgentViaMailbox(ctx, targetAgent, msg, receiveOnAgent)
}

// executeViaLocal はMessageRouter経由でローカルAgentと通信
func (o *DistributedOrchestrator) executeViaLocal(ctx context.Context, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error) {
	return o.transports.ExecuteViaLocal(ctx, targetAgent, msg, receiveOnAgent)
}

func (o *DistributedOrchestrator) distributedWaitTimeout(targetAgent string, msg domaintransport.Message) time.Duration {
	if isCoderAgent(targetAgent) {
		return o.coderTimeoutOrDefault()
	}
	return distributedWaitTimeout(targetAgent, msg)
}

// routeToAgent はルートをAgent名にマッピング
func (o *DistributedOrchestrator) routeToAgent(route routing.Route) string {
	switch route {
	case routing.RouteOPS:
		return "shiro"
	case routing.RouteCODE, routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4:
		return "shiro"
	case routing.RouteCHAT, routing.RoutePLAN, routing.RouteANALYZE, routing.RouteRESEARCH:
		return "" // mio がローカル処理
	default:
		return ""
	}
}

func (o *DistributedOrchestrator) routeToCoder(route routing.Route) string {
	return o.coderSelector.RouteToCoder(route)
}

func (o *DistributedOrchestrator) routeToCoderForMessage(route routing.Route, userMessage string) string {
	return o.coderSelector.RouteToCoderForMessage(route, userMessage)
}

func (o *DistributedOrchestrator) isCoderConnected(agent string) bool {
	return o.coderSelector.IsCoderConnected(agent)
}

func isCodeRoute(route routing.Route) bool {
	switch route {
	case routing.RouteCODE, routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3:
		return true
	default:
		return false
	}
}

func (o *DistributedOrchestrator) withAttributionGuard(t task.Task, targetAgent, sessionID string) task.Task {
	return o.attribution.Apply(t, targetAgent, sessionID)
}

func (o *DistributedOrchestrator) buildAttributionGuardedMessage(userMessage, targetAgent, sessionID string) string {
	return o.attribution.BuildMessage(userMessage, targetAgent, sessionID)
}
