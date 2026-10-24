package orchestrator

import (
	"context"
	"fmt"
	"strings"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type routeDecisionCoordinator struct {
	mio             MioAgent
	heavyPolicy     domainai.HeavyWorkerPolicy
	canonicalEvents CanonicalEventRecorder
}

type routeDecisionResult struct {
	Decision  routing.Decision
	ChatDraft *domainagent.MioChatDraft
	Context   context.Context
}

func newRouteDecisionCoordinator(mio MioAgent) *routeDecisionCoordinator {
	return &routeDecisionCoordinator{
		mio: mio,
	}
}

func (c *routeDecisionCoordinator) SetHeavyWorkerPolicy(policy domainai.HeavyWorkerPolicy) {
	c.heavyPolicy = policy
}

func (c *routeDecisionCoordinator) SetCanonicalEventRecorder(recorder CanonicalEventRecorder) {
	c.canonicalEvents = recorder
}

func (c *routeDecisionCoordinator) Decide(ctx context.Context, input domainconversation.TurnInput, req ProcessMessageRequest, taskID modulecore.TaskID) (routing.Decision, error) {
	result, err := c.DecideWithChatDraft(ctx, input, req, taskID, false)
	c.releaseChatDraft(ctx, result.ChatDraft)
	return result.Decision, err
}

func (c *routeDecisionCoordinator) DecideWithChatDraft(ctx context.Context, input domainconversation.TurnInput, req ProcessMessageRequest, taskID modulecore.TaskID, allowCombined bool) (routeDecisionResult, error) {
	var draftForRelease *domainagent.MioChatDraft
	transferredDraft := false
	defer func() {
		if !transferredDraft {
			c.releaseChatDraft(ctx, draftForRelease)
		}
	}()
	if recipient, ok := directViewerRecipientForChat(req); ok {
		return routeDecisionResult{Decision: directViewerRecipientDecision(recipient), Context: ctx}, nil
	}
	var decision routing.Decision
	var chatDraft *domainagent.MioChatDraft
	var err error
	if combined, ok := c.mio.(interface {
		DecideActionWithChatDraft(context.Context, domainconversation.TurnInput, bool) (routing.Decision, *domainagent.MioChatDraft, error)
		ReleaseChatDraft(context.Context, *domainagent.MioChatDraft)
	}); ok && allowCombined {
		decision, chatDraft, err = combined.DecideActionWithChatDraft(ctx, input, true)
		draftForRelease = chatDraft
	} else {
		decision, err = c.mio.DecideAction(ctx, input)
	}
	if err != nil {
		return routeDecisionResult{}, fmt.Errorf("routing decision failed: %w", err)
	}
	classifiedRoute := decision.Route
	decision, pinnedViewerRecipient := pinSelectedViewerRecipientDecision(decision, req)
	if pinnedViewerRecipient {
		return routeDecisionResult{Decision: decision, Context: ctx}, nil
	}
	if chatDraft.Failed() {
		transferredDraft = true
		return routeDecisionResult{Decision: decision, ChatDraft: chatDraft, Context: ctx}, nil
	}
	decision, ctx, err = c.applyHeavyWorkerPolicy(ctx, decision, req, taskID)
	if err != nil {
		return routeDecisionResult{}, err
	}
	if decision.Route != routing.RouteCHAT || decision.Route != classifiedRoute {
		chatDraft = nil
	}
	transferredDraft = chatDraft != nil
	return routeDecisionResult{Decision: decision, ChatDraft: chatDraft, Context: ctx}, nil
}

func (c *routeDecisionCoordinator) releaseChatDraft(ctx context.Context, draft *domainagent.MioChatDraft) {
	if c == nil || draft == nil {
		return
	}
	if releaser, ok := c.mio.(interface {
		ReleaseChatDraft(context.Context, *domainagent.MioChatDraft)
	}); ok {
		releaser.ReleaseChatDraft(ctx, draft)
	}
}

// directViewerRecipientForChat returns the explicit Viewer recipient (shiro,
// kuro, or midori) when the user picked an agent other than Mio for a plain
// Viewer chat message. The returned bool is true only when Mio's routing
// should be skipped entirely because the explicit recipient supersedes it per
// the "明示commandは会話相手の選択より優先されます" rule in docs/02_機能仕様.md.
// to=mio keeps routing engaged (Mio is the route owner) and so does a message
// that starts with a slash command.
func directViewerRecipientForChat(req ProcessMessageRequest) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(req.Channel), "viewer") {
		return "", false
	}
	recipient := normalizeProcessViewerRecipient(req.To)
	switch recipient {
	case "shiro", "kuro", "midori":
	default:
		return "", false
	}
	originalMessage := req.originalUserMessage
	if originalMessage == "" {
		originalMessage = req.UserMessage
	}
	if strings.HasPrefix(strings.TrimSpace(originalMessage), "/") {
		return "", false
	}
	return recipient, true
}

// directViewerRootAssignee selects the initial root owner only when the
// request is a plain Viewer chat rather than an existing specialized path.
// Workflows that do not expose a pure matcher keep the legacy Mio-owned root.
func directViewerRootAssignee(req ProcessMessageRequest, workflow DurableStoreWorkflow, dciSearcher DCISearcher) (string, bool) {
	recipient, direct := directViewerRecipientForChat(req)
	if !direct || isDailyNewsBriefRequest(req.UserMessage) || shouldHandleExplicitDCI(dciSearcher, req.UserMessage) {
		return "", false
	}
	if workflow != nil {
		matcher, ok := workflow.(durableStoreIntentMatcher)
		if !ok || matcher.CanHandle(durableStoreInput(req)) {
			return "", false
		}
	}
	return recipient, true
}

func directViewerExecutionActor(req ProcessMessageRequest, route routing.Route, actor string) bool {
	recipient, direct := directViewerRecipientForChat(req)
	return direct && route == routing.RouteCHAT && actor == recipient
}

// directViewerRecipientDecision builds the pinned CHAT decision used when the
// user explicitly selected shiro/kuro/midori for a plain Viewer chat message.
// The decision is constructed without invoking Mio.DecideAction, so the extra
// LLM round-trip is avoided.
func directViewerRecipientDecision(recipient string) routing.Decision {
	return routing.Decision{
		Route:      routing.RouteCHAT,
		Confidence: 1,
		Reason:     fmt.Sprintf("explicit Viewer recipient (%s) without a route command", recipient),
		Evidence: []routing.DecisionEvidence{{
			Source:     "viewer_recipient",
			Matched:    true,
			Route:      routing.RouteCHAT,
			Confidence: 1,
			Reason:     fmt.Sprintf("explicit %s Viewer recipient without a route command", recipient),
		}},
	}
}

// shouldPinSelectedViewerRecipientToChat preserves the earlier midori-only
// safety net. The pre-check in directViewerRecipientForChat already covers
// shiro/kuro/midori before Mio.DecideAction runs, so this function now only
// catches cases where a code path still invokes Mio and the result needs to
// honor an explicit Viewer recipient.
func shouldPinSelectedViewerRecipientToChat(req ProcessMessageRequest) bool {
	_, ok := directViewerRecipientForChat(req)
	return ok
}

func pinSelectedViewerRecipientDecision(decision routing.Decision, req ProcessMessageRequest) (routing.Decision, bool) {
	recipient, ok := directViewerRecipientForChat(req)
	if !ok {
		return decision, false
	}
	decision.Route = routing.RouteCHAT
	decision.Confidence = 1
	decision.Reason = fmt.Sprintf("explicit Viewer recipient (%s) without a route command", recipient)
	decision.Evidence = append(decision.Evidence, routing.DecisionEvidence{
		Source:     "viewer_recipient",
		Matched:    true,
		Route:      routing.RouteCHAT,
		Confidence: 1,
		Reason:     fmt.Sprintf("explicit %s Viewer recipient without a route command", recipient),
	})
	return decision, true
}

func preserveOriginalUserMessage(req *ProcessMessageRequest) {
	if req != nil && req.originalUserMessage == "" {
		req.originalUserMessage = req.UserMessage
	}
}

func (c *routeDecisionCoordinator) applyHeavyWorkerPolicy(ctx context.Context, decision routing.Decision, req ProcessMessageRequest, taskID modulecore.TaskID) (routing.Decision, context.Context, error) {
	if !canHeavyPolicyElevate(decision.Route) {
		return decision, ctx, nil
	}
	heavyReq := heavyWorkerRequestFromMessage(taskID.String(), req.UserMessage)
	if !heavyReq.UserRequestedDeepDive {
		return decision, ctx, nil
	}
	evaluated := domainai.EvaluateHeavyWorker(heavyReq, c.heavyPolicy)
	if evaluated.Status != domainai.HeavyWorkerStatusRequested {
		return decision, ctx, nil
	}
	var err error
	ctx, err = synchronousPersistenceBeforeEffect(ctx, "heavy worker policy event")
	if err != nil {
		return decision, ctx, err
	}
	recordHeavyCanonicalEvent(ctx, c.canonicalEvents, "requested", strings.Join(evaluated.Reasons, "; "), taskID.String())
	elevated := decision
	elevated.Route = routing.RouteANALYZE
	if elevated.Confidence < 0.95 {
		elevated.Confidence = 0.95
	}
	if elevated.Reason == "" {
		elevated.Reason = "heavy worker policy requested ANALYZE"
	} else {
		elevated.Reason += "; heavy worker policy requested ANALYZE"
	}
	elevated.Evidence = append(elevated.Evidence, routing.DecisionEvidence{
		Source:     "heavy_worker_policy",
		Matched:    true,
		Route:      routing.RouteANALYZE,
		Confidence: elevated.Confidence,
		Reason:     strings.Join(evaluated.Reasons, "; "),
	})
	return elevated, ctx, nil
}

func canHeavyPolicyElevate(route routing.Route) bool {
	switch route {
	case routing.RouteCHAT, routing.RoutePLAN, routing.RouteRESEARCH:
		return true
	default:
		return false
	}
}

func heavyWorkerRequestFromMessage(eventID, message string) domainai.HeavyWorkerRequest {
	lower := strings.ToLower(message)
	keywords := []string{
		"深掘り",
		"深く調べ",
		"詳しく分析",
		"詳細分析",
		"徹底調査",
		"heavy",
		"deep dive",
		"deep-dive",
	}
	requested := false
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			requested = true
			break
		}
	}
	reason := ""
	if requested {
		reason = "user requested deep dive"
	}
	return domainai.HeavyWorkerRequest{
		EventID:               eventID,
		Agent:                 "Mio",
		UserRequestedDeepDive: requested,
		Reason:                reason,
	}
}

func routeDecisionEvidenceSummary(evidence []routing.DecisionEvidence) string {
	if len(evidence) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(evidence))
	for _, ev := range evidence {
		state := "miss"
		if ev.Matched {
			state = "matched"
		}
		route := string(ev.Route)
		if route == "" {
			route = "-"
		}
		parts = append(parts, fmt.Sprintf("%s:%s:%s", ev.Source, state, route))
	}
	return strings.Join(parts, ",")
}
