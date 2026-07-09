package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func (o *MessageOrchestrator) handleExplicitDCI(ctx context.Context, req ProcessMessageRequest, sess *session.Session, t task.Task, jobID task.JobID) (ProcessMessageResponse, bool, error) {
	// スラッシュコマンド（/code3, /analyze 等）はルーティングを最優先。DCI をスキップ。
	if strings.HasPrefix(strings.TrimSpace(req.UserMessage), "/") {
		return ProcessMessageResponse{}, false, nil
	}
	if o.dciSearcher == nil || !o.dciSearcher.ShouldTrigger(req.UserMessage) {
		return ProcessMessageResponse{}, false, nil
	}

	jid := jobID.String()
	o.events.Emit("dci.search.started", "mio", "worker", req.UserMessage, string(routing.RouteRESEARCH), jid, req.SessionID, req.Channel, req.ChatID)
	result, err := o.dciSearcher.Search(ctx, req.UserMessage)
	if err != nil {
		o.events.Emit("dci.search.failed", "worker", "mio", err.Error(), string(routing.RouteRESEARCH), jid, req.SessionID, req.Channel, req.ChatID)
		return ProcessMessageResponse{}, true, fmt.Errorf("dci search failed: %w", err)
	}

	response := formatDCIResponse(result)
	o.events.Emit("dci.search.completed", "worker", "mio", response, string(routing.RouteRESEARCH), jid, req.SessionID, req.Channel, req.ChatID)
	o.events.Emit("agent.response", "worker", "mio", response, string(routing.RouteRESEARCH), jid, req.SessionID, req.Channel, req.ChatID)

	if err := o.saveDCIRecallTrace(ctx, req.SessionID, jid, result); err != nil {
		return ProcessMessageResponse{}, true, err
	}
	if err := o.sessions.SaveCompletedTask(ctx, sess, t); err != nil {
		return ProcessMessageResponse{}, true, err
	}

	decision := routing.NewDecision(routing.RouteRESEARCH, 1.0, "explicit DCI trigger")
	return o.responses.Build(response, decision, jobID), true, nil
}

func (o *MessageOrchestrator) saveDCIRecallTrace(ctx context.Context, sessionID string, responseID string, result domaindci.SearchResult) error {
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

func dciResultToRecallTrace(sessionID string, responseID string, result domaindci.SearchResult) domainconversation.RecallTrace {
	items := make([]domainconversation.RecallTraceItem, 0, len(result.Pack.Evidence)+len(result.Pack.Limitations))
	for i, ev := range result.Pack.Evidence {
		location := ev.FilePath
		if ev.LineStart > 0 {
			location = fmt.Sprintf("%s:%d", ev.FilePath, ev.LineStart)
		}
		items = append(items, domainconversation.RecallTraceItem{
			Layer:       "DCI",
			Kind:        "evidence",
			Summary:     strings.TrimSpace(location + " " + strings.TrimSpace(ev.Snippet)),
			Query:       result.Pack.Query,
			Provider:    "dci",
			SourceURLs:  []string{location},
			RetrievedAt: result.Trace.EndedAt,
			Score:       float32(ev.Confidence),
			Decision:    "included",
			Reason:      "explicit DCI trigger evidence returned to user",
			PromptIndex: i,
		})
	}
	for _, limitation := range result.Pack.Limitations {
		limitation = strings.TrimSpace(limitation)
		if limitation == "" {
			continue
		}
		items = append(items, domainconversation.RecallTraceItem{
			Layer:       "DCI",
			Kind:        "limitation",
			Summary:     limitation,
			Query:       result.Pack.Query,
			Provider:    "dci",
			RetrievedAt: result.Trace.EndedAt,
			Decision:    "excluded",
			Reason:      "DCI search did not find usable evidence for this part",
			PromptIndex: -1,
		})
	}
	createdAt := result.Trace.EndedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return domainconversation.RecallTrace{
		ResponseID: responseID,
		SessionID:  sessionID,
		Role:       "dci",
		Items:      items,
		CreatedAt:  createdAt,
	}
}

func formatDCIResponse(result domaindci.SearchResult) string {
	var b strings.Builder
	pack := result.Pack
	trace := result.Trace
	fmt.Fprintf(&b, "DCI探索結果\n")
	fmt.Fprintf(&b, "event_id: %s\n", pack.EventID)
	fmt.Fprintf(&b, "query: %s\n", pack.Query)
	fmt.Fprintf(&b, "status: %s\n", trace.Status)
	fmt.Fprintf(&b, "evidence_count: %d\n", len(pack.Evidence))
	if len(pack.CorpusScope) > 0 {
		fmt.Fprintf(&b, "scope: %s\n", strings.Join(pack.CorpusScope, ", "))
	}
	for i, ev := range pack.Evidence {
		if i >= 5 {
			fmt.Fprintf(&b, "- ... remaining evidence omitted: %d\n", len(pack.Evidence)-i)
			break
		}
		location := ev.FilePath
		if ev.LineStart > 0 {
			location = fmt.Sprintf("%s:%d", ev.FilePath, ev.LineStart)
		}
		fmt.Fprintf(&b, "- %s\n  %s\n", location, strings.TrimSpace(ev.Snippet))
	}
	for _, limitation := range pack.Limitations {
		if strings.TrimSpace(limitation) == "" {
			continue
		}
		fmt.Fprintf(&b, "limitation: %s\n", limitation)
	}
	return strings.TrimSpace(b.String())
}
