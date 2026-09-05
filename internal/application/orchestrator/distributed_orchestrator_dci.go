package orchestrator

import (
	"context"
	"fmt"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func (o *DistributedOrchestrator) SetDCISearcher(searcher DCISearcher) {
	o.dciSearcher = searcher
}

func (o *DistributedOrchestrator) SetRecallTraceStore(store RecallTraceStore) {
	o.recallTrace = store
}

func (o *DistributedOrchestrator) handleExplicitDCI(ctx context.Context, req ProcessMessageRequest, sess *session.Session, input domainconversation.TurnInput, taskID modulecore.TaskID, triggered bool) (ProcessMessageResponse, bool, error) {
	if !triggered {
		return ProcessMessageResponse{}, false, nil
	}

	taskIDText := taskID.String()
	result, err := o.dciSearcher.Search(ctx, req.UserMessage)
	if err != nil {
		return ProcessMessageResponse{}, true, fmt.Errorf("dci search failed: %w", err)
	}

	response := formatDCIResponse(result)
	if err := o.saveDCIRecallTrace(ctx, input, result); err != nil {
		return ProcessMessageResponse{}, true, err
	}
	routedInput := input.WithRoute(routing.RouteRESEARCH)
	if err := o.sessions.SaveCompletedTurnInput(ctx, sess, routedInput); err != nil {
		return ProcessMessageResponse{}, true, fmt.Errorf("failed to save session: %w", err)
	}
	o.emit("agent.response", "shiro", "mio", response, string(routing.RouteRESEARCH), taskIDText, req.SessionID, req.Channel, req.ChatID)

	return ProcessMessageResponse{
		Response:   response,
		Route:      routing.RouteRESEARCH,
		Confidence: 1.0,
		TaskID:     taskIDText,
	}, true, nil
}

func (o *DistributedOrchestrator) saveDCIRecallTrace(ctx context.Context, input domainconversation.TurnInput, result domaindci.SearchResult) error {
	if o.recallTrace == nil {
		return nil
	}
	trace := dciResultToRecallTrace(input, result)
	if len(trace.Items) == 0 {
		return nil
	}
	if err := o.recallTrace.SaveRecallTrace(ctx, trace); err != nil {
		return fmt.Errorf("failed to save dci recall trace: %w", err)
	}
	return nil
}
