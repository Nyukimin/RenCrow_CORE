package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const dailyNewsBriefIntent = "daily_news_brief"

// isDailyNewsBriefRequest is intentionally deterministic. A request for the
// prepared morning brief must not depend on the general LLM route classifier.
func isDailyNewsBriefRequest(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" || strings.HasPrefix(message, "/") {
		return false
	}
	for _, qualifier := range []string{"検索", "調べて", "調査して", "最新", "速報", "今起きている", "今起きてる"} {
		if strings.Contains(message, qualifier) {
			return false
		}
	}
	for _, phrase := range []string{
		"今朝のニュース",
		"朝のニュース",
		"今日のニュース",
		"きょうのニュース",
		"ニュース一覧",
		"ニュースを一覧",
		"ニュースの続き",
		"朝刊の続き",
		"ニュースを続けて",
		"番を詳しく",
		"番を説明",
		"このニュースを詳しく",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func dailyNewsBriefRouteDecision() routing.Decision {
	return routing.NewDecision(routing.RouteCHAT, 1.0, dailyNewsBriefIntent)
}

func (o *MessageOrchestrator) SetDailyNewsBriefReader(reader domainnews.DailyNewsBriefReader) {
	o.dailyNewsBriefReader = reader
}

// SetDailyNewsBriefCollector injects the Worker/tool boundary used only when
// the prepared morning cache is unavailable.
func (o *MessageOrchestrator) SetDailyNewsBriefCollector(collector domainnews.DailyNewsBriefCollector) {
	o.dailyNewsBriefCollector = collector
}

func (o *DistributedOrchestrator) SetDailyNewsBriefReader(reader domainnews.DailyNewsBriefReader) {
	o.dailyNewsBriefReader = reader
}

// SetDailyNewsBriefCollector injects the Worker/tool boundary used only when
// the prepared morning cache is unavailable.
func (o *DistributedOrchestrator) SetDailyNewsBriefCollector(collector domainnews.DailyNewsBriefCollector) {
	o.dailyNewsBriefCollector = collector
}

type dailyNewsBriefResponder struct {
	name  string
	agent MioAgent
}

func dailyNewsBriefResponders(recipient string, mio MioAgent, shiro MioAgent) []dailyNewsBriefResponder {
	// The morning brief is a Mio-owned Chat intent. Shiro is only a fallback
	// when Mio is unavailable; the selected avatar must not silently change the
	// factual-news ownership contract.
	_ = recipient
	responders := make([]dailyNewsBriefResponder, 0, 2)
	if mio != nil {
		responders = append(responders, dailyNewsBriefResponder{name: "mio", agent: mio})
	}
	if shiro != nil {
		responders = append(responders, dailyNewsBriefResponder{name: "shiro", agent: shiro})
	}
	return responders
}

func (o *MessageOrchestrator) handleDailyNewsBrief(
	ctx context.Context,
	req ProcessMessageRequest,
	sess *session.Session,
	t task.Task,
	jobID task.JobID,
	ttsSessionID string,
) (ProcessMessageResponse, bool, error) {
	if !isDailyNewsBriefRequest(req.UserMessage) {
		return ProcessMessageResponse{}, false, nil
	}
	return o.respondWithDailyNewsBrief(ctx, req, sess, t, jobID, o.dailyNewsBriefReader, o.dailyNewsBriefCollector, o.shiroChat, ttsSessionID)
}

func (o *DistributedOrchestrator) handleDailyNewsBrief(
	ctx context.Context,
	req ProcessMessageRequest,
	sess *session.Session,
	t task.Task,
	jobID task.JobID,
) (ProcessMessageResponse, bool, error) {
	if !isDailyNewsBriefRequest(req.UserMessage) {
		return ProcessMessageResponse{}, false, nil
	}
	return o.respondWithDailyNewsBrief(ctx, req, sess, t, jobID, o.dailyNewsBriefReader, o.dailyNewsBriefCollector, o.shiroChat)
}

func (o *MessageOrchestrator) respondWithDailyNewsBrief(
	ctx context.Context,
	req ProcessMessageRequest,
	sess *session.Session,
	t task.Task,
	jobID task.JobID,
	reader domainnews.DailyNewsBriefReader,
	collector domainnews.DailyNewsBriefCollector,
	shiroChat MioAgent,
	ttsSessionID string,
) (ProcessMessageResponse, bool, error) {
	now := time.Now()
	decision := dailyNewsBriefRouteDecision()
	o.ttsLifecycle.StartSessionForRoute(ctx, req, jobID, decision, ttsSessionID)
	o.events.Emit("news.brief.requested", "user", "mio", "intent="+dailyNewsBriefIntent, "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	brief, readerErr := readDailyNewsBrief(ctx, reader, now)
	if readerErr != nil {
		o.events.Emit("news.brief.cache_miss", "system", "mio", readerErr.Error(), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	} else if brief.IsUsable(now) {
		o.events.Emit("news.brief.cache_hit", "system", "mio", dailyNewsBriefObservation(brief), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	} else {
		o.events.Emit("news.brief.cache_miss", "system", "mio", dailyNewsBriefObservation(brief), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	resp, handled, err := respondWithDailyNewsBrief(
		ctx,
		req,
		sess,
		t,
		jobID,
		now,
		brief,
		readerErr,
		collector,
		o.mio,
		shiroChat,
		o.events.Emit,
		o.sessions.SaveCompletedTask,
		o.responses.Build,
	)
	if err == nil && handled {
		o.ttsLifecycle.Push(ctx, ttsSessionID, decision.Route, "agent.response", resp.Response)
	}
	o.ttsLifecycle.EndSession(ctx, ttsSessionID)
	return resp, handled, err
}

func (o *DistributedOrchestrator) respondWithDailyNewsBrief(
	ctx context.Context,
	req ProcessMessageRequest,
	sess *session.Session,
	t task.Task,
	jobID task.JobID,
	reader domainnews.DailyNewsBriefReader,
	collector domainnews.DailyNewsBriefCollector,
	shiroChat MioAgent,
) (ProcessMessageResponse, bool, error) {
	now := time.Now()
	decision := dailyNewsBriefRouteDecision()
	ttsSessionID := o.ttsLifecycle.StartSessionForRoute(ctx, req, jobID, decision)
	o.events.Emit("news.brief.requested", "user", "mio", "intent="+dailyNewsBriefIntent, "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	brief, readerErr := readDailyNewsBrief(ctx, reader, now)
	if readerErr != nil {
		o.events.Emit("news.brief.cache_miss", "system", "mio", readerErr.Error(), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	} else if brief.IsUsable(now) {
		o.events.Emit("news.brief.cache_hit", "system", "mio", dailyNewsBriefObservation(brief), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	} else {
		o.events.Emit("news.brief.cache_miss", "system", "mio", dailyNewsBriefObservation(brief), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	resp, handled, err := respondWithDailyNewsBrief(
		ctx,
		req,
		sess,
		t,
		jobID,
		now,
		brief,
		readerErr,
		collector,
		o.mio,
		shiroChat,
		o.events.Emit,
		o.sessions.SaveCompletedTask,
		func(response string, decision routing.Decision, jid task.JobID) ProcessMessageResponse {
			return ProcessMessageResponse{
				Response:   response,
				Route:      decision.Route,
				Confidence: decision.Confidence,
				JobID:      jid.String(),
			}
		},
	)
	if err == nil && handled {
		o.ttsLifecycle.Push(ctx, ttsSessionID, decision.Route, "agent.response", resp.Response)
	}
	o.ttsLifecycle.EndSession(ctx, ttsSessionID)
	return resp, handled, err
}

type dailyNewsBriefEventEmitter func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string)
type dailyNewsBriefTaskSaver func(context.Context, *session.Session, task.Task) error
type dailyNewsBriefResponseBuilder func(string, routing.Decision, task.JobID) ProcessMessageResponse

func readDailyNewsBrief(ctx context.Context, reader domainnews.DailyNewsBriefReader, now time.Time) (domainnews.DailyNewsBrief, error) {
	if reader == nil {
		return domainnews.DailyNewsBrief{Status: domainnews.StatusEmpty, Items: []domainnews.Item{}}, nil
	}
	return reader.Read(ctx, now)
}

func respondWithDailyNewsBrief(
	ctx context.Context,
	req ProcessMessageRequest,
	sess *session.Session,
	t task.Task,
	jobID task.JobID,
	now time.Time,
	brief domainnews.DailyNewsBrief,
	readerErr error,
	collector domainnews.DailyNewsBriefCollector,
	mioChat MioAgent,
	shiroChat MioAgent,
	emit dailyNewsBriefEventEmitter,
	save dailyNewsBriefTaskSaver,
	build dailyNewsBriefResponseBuilder,
) (ProcessMessageResponse, bool, error) {
	usable := readerErr == nil && brief.IsUsable(now)
	requestCtx := ctx
	responseBrief := brief
	hasResponseBrief := usable
	if usable {
		requestCtx = domainagent.WithDailyNewsBrief(requestCtx, brief)
	} else {
		emit("news.brief.fallback_started", "system", "mio", "source=live_news_search reason="+dailyNewsBriefFallbackReason(brief, readerErr), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
		emitDailyNewsFallbackHandoff(emit, req, jobID)
		if collector == nil {
			err := fmt.Errorf("daily news brief collector is unavailable")
			emitDailyNewsFallbackProgress(emit, req, jobID, "収集Toolが未接続のため、朝刊を取得できませんでした。")
			emit("news.brief.failed", "system", "user", err.Error(), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
			return ProcessMessageResponse{}, true, err
		}
		emitDailyNewsFallbackProgress(emit, req, jobID, "まずニュース検索源を確認し、候補記事を集めます。")
		liveBrief, err := collector.Collect(ctx, req.UserMessage, now)
		if err != nil {
			emitDailyNewsFallbackProgress(emit, req, jobID, "検索・本文取得に失敗しました。事実データは確定できません。")
			emit("news.brief.failed", "system", "user", err.Error(), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
			return ProcessMessageResponse{}, true, err
		}
		responseBrief = liveBrief
		hasResponseBrief = len(liveBrief.Items) > 0
		requestCtx = domainagent.WithDailyNewsBrief(requestCtx, liveBrief)
		emitDailyNewsFallbackProgress(emit, req, jobID, "候補記事の本文を取得し、重複を確認して整理しました。")
		emitDailyNewsFallbackProgress(emit, req, jobID, "調べてきました。収集結果はデータとしてMioへ渡します。")
	}

	responders := dailyNewsBriefResponders(t.ViewerRecipient(), mioChat, shiroChat)
	if len(responders) == 0 {
		return ProcessMessageResponse{}, true, fmt.Errorf("daily news brief has no Chat responder")
	}
	var response string
	var responseAgent string
	var lastErr error
	for _, responder := range responders {
		if responder.agent == nil {
			continue
		}
		response, lastErr = responder.agent.Chat(requestCtx, t.WithRoute(routing.RouteCHAT))
		if lastErr == nil && strings.TrimSpace(response) != "" {
			responseAgent = responder.name
			break
		}
	}
	if strings.TrimSpace(response) == "" {
		if hasResponseBrief {
			response = formatDailyNewsBriefFallback(responseBrief)
			responseAgent = "mio"
			lastErr = nil
		} else if lastErr != nil {
			emit("news.brief.failed", responseAgentOrSystem(responseAgent), "user", lastErr.Error(), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
			return ProcessMessageResponse{}, true, lastErr
		} else {
			lastErr = fmt.Errorf("daily news brief responder returned an empty response")
			emit("news.brief.failed", "system", "user", lastErr.Error(), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
			return ProcessMessageResponse{}, true, lastErr
		}
	}

	emit("news.brief.responded", responseAgentOrSystem(responseAgent), "user", dailyNewsBriefResponseObservation(responseBrief, usable, now), "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	emit("agent.response", responseAgentOrSystem(responseAgent), "user", response, "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	if err := save(ctx, sess, t.WithRoute(routing.RouteCHAT)); err != nil {
		return ProcessMessageResponse{}, true, err
	}
	return build(response, dailyNewsBriefRouteDecision(), jobID), true, nil
}

func responseAgentOrSystem(agent string) string {
	if strings.TrimSpace(agent) == "" {
		return "system"
	}
	return agent
}

func emitDailyNewsFallbackHandoff(emit dailyNewsBriefEventEmitter, req ProcessMessageRequest, jobID task.JobID) {
	route := string(routing.RouteCHAT)
	emit("agent.progress", "mio", "shiro", "Shiro、今日の朝刊データがまだ届いていないみたい。ニュース収集Workerで調べてきて。", route, jobID.String(), req.SessionID, req.Channel, req.ChatID)
	emit("agent.progress", "shiro", "mio", "Mio、了解。検索源を確認して、候補記事の本文取得と重複確認まで進めます。", route, jobID.String(), req.SessionID, req.Channel, req.ChatID)
}

func emitDailyNewsFallbackProgress(emit dailyNewsBriefEventEmitter, req ProcessMessageRequest, jobID task.JobID, content string) {
	emit("agent.progress", "shiro", "mio", content, string(routing.RouteCHAT), jobID.String(), req.SessionID, req.Channel, req.ChatID)
}

func dailyNewsBriefObservation(brief domainnews.DailyNewsBrief) string {
	fetchedAt := ""
	if !brief.FetchedAt.IsZero() {
		fetchedAt = brief.FetchedAt.Format(time.RFC3339)
	}
	return fmt.Sprintf("intent=%s source=%s date=%s fetched_at=%s status=%s enrichment=%s items=%d", dailyNewsBriefIntent, brief.Source, brief.Date, fetchedAt, brief.Status, brief.EnrichmentStatus, len(brief.Items))
}

func dailyNewsBriefFallbackReason(brief domainnews.DailyNewsBrief, readerErr error) string {
	if readerErr != nil {
		return "reader_error"
	}
	if brief.Status == domainnews.StatusStale {
		return "stale"
	}
	if brief.Status == domainnews.StatusPending || brief.Status == domainnews.StatusEnriching {
		return brief.Status
	}
	return "empty_or_unusable"
}

func dailyNewsBriefResponseObservation(brief domainnews.DailyNewsBrief, usable bool, now time.Time) string {
	if usable || brief.Source == domainnews.SourceScheduled || brief.Source == domainnews.SourcePersistent {
		fetchedAt := ""
		if !brief.FetchedAt.IsZero() {
			fetchedAt = brief.FetchedAt.Format(time.RFC3339)
		}
		source := brief.Source
		if source == "" {
			source = domainnews.SourceScheduled
		}
		return fmt.Sprintf("intent=%s source=%s date=%s fetched_at=%s items=%d", dailyNewsBriefIntent, source, brief.Date, fetchedAt, len(brief.Items))
	}
	return fmt.Sprintf("intent=%s source=live_news_search searched_at=%s brief_date=%s items=%d", dailyNewsBriefIntent, now.Format(time.RFC3339), brief.Date, len(brief.Items))
}

func formatDailyNewsBriefFallback(brief domainnews.DailyNewsBrief) string {
	var b strings.Builder
	if brief.Source == domainnews.SourceLiveSearch {
		fetchedAt := "不明"
		if !brief.FetchedAt.IsZero() {
			fetchedAt = brief.FetchedAt.In(jst).Format("2006-01-02 15:04 JST")
		}
		fmt.Fprintf(&b, "最新ニュース（%s 取得）\n\n", fetchedAt)
	} else {
		fmt.Fprintf(&b, "今朝のニュース（%s 04:00 JST）\n\n", brief.Date)
	}
	maxItems := len(brief.Items)
	if maxItems > 5 {
		maxItems = 5
	}
	for index, item := range brief.Items[:maxItems] {
		fmt.Fprintf(&b, "%d. %s（%s）\n", index+1, item.Title, item.Source)
		if summary := firstNonEmptyNewsBriefText(item.Summary, item.TranslatedBody); summary != "" {
			fmt.Fprintf(&b, "   %s\n", truncateNewsBriefText(summary, 260))
		}
		if item.URL != "" {
			fmt.Fprintf(&b, "   %s\n", item.URL)
		}
	}
	if maxItems == 0 {
		return "04:00 JSTの朝刊ニュースはまだ準備されていません。"
	}
	return strings.TrimSpace(b.String())
}

func firstNonEmptyNewsBriefText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncateNewsBriefText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
