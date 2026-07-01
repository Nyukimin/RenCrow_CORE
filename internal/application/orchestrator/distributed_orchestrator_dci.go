package orchestrator

import (
	"context"
	"fmt"
	"strings"

	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

func (o *DistributedOrchestrator) SetDCISearcher(searcher DCISearcher) {
	o.dciSearcher = searcher
}

func (o *DistributedOrchestrator) SetRecallTraceStore(store RecallTraceStore) {
	o.recallTrace = store
}

func (o *DistributedOrchestrator) handleExplicitDCI(ctx context.Context, req ProcessMessageRequest, sess *session.Session, t task.Task, jobID task.JobID) (ProcessMessageResponse, bool, error) {
	// スラッシュコマンド（/code3, /analyze 等）はルーティングを最優先。DCI をスキップ。
	if strings.HasPrefix(strings.TrimSpace(req.UserMessage), "/") {
		return ProcessMessageResponse{}, false, nil
	}
	if o.dciSearcher == nil || !o.dciSearcher.ShouldTrigger(req.UserMessage) {
		return ProcessMessageResponse{}, false, nil
	}

	jid := jobID.String()
	o.emit("dci.search.started", "mio", "worker", req.UserMessage, string(routing.RouteRESEARCH), jid, req.SessionID, req.Channel, req.ChatID)
	result, err := o.dciSearcher.Search(ctx, req.UserMessage)
	if err != nil {
		o.emit("dci.search.failed", "worker", "mio", err.Error(), string(routing.RouteRESEARCH), jid, req.SessionID, req.Channel, req.ChatID)
		return ProcessMessageResponse{}, true, fmt.Errorf("dci search failed: %w", err)
	}

	response := formatDCIResponse(result)
	o.emit("dci.search.completed", "worker", "mio", response, string(routing.RouteRESEARCH), jid, req.SessionID, req.Channel, req.ChatID)
	o.emit("agent.response", "worker", "mio", response, string(routing.RouteRESEARCH), jid, req.SessionID, req.Channel, req.ChatID)

	if err := o.saveDCIRecallTrace(ctx, req.SessionID, jid, result); err != nil {
		return ProcessMessageResponse{}, true, err
	}
	routedTask := t.WithRoute(routing.RouteRESEARCH)
	if err := o.sessions.SaveCompletedTask(ctx, sess, routedTask); err != nil {
		return ProcessMessageResponse{}, true, fmt.Errorf("failed to save session: %w", err)
	}

	return ProcessMessageResponse{
		Response:   response,
		Route:      routing.RouteRESEARCH,
		Confidence: 1.0,
		JobID:      jid,
	}, true, nil
}

func (o *DistributedOrchestrator) saveDCIRecallTrace(ctx context.Context, sessionID string, responseID string, result domaindci.SearchResult) error {
	if o.recallTrace == nil {
		return nil
	}
	trace := dciResultToRecallTrace(sessionID, responseID, result)
	if len(trace.Items) == 0 {
		return nil
	}
	if err := o.recallTrace.SaveRecallTrace(ctx, trace); err != nil {
		return fmt.Errorf("failed to save dci recall trace: %w", err)
	}
	return nil
}
