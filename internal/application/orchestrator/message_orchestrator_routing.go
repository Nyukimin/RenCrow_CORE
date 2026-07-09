package orchestrator

import (
	"context"
	"fmt"
	"strings"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type routeDecisionCoordinator struct {
	mio            MioAgent
	emit           messageEventEmitter
	heavyPolicy    domainai.HeavyWorkerPolicy
	workflowEvents WorkflowEventRecorder
}

func newRouteDecisionCoordinator(mio MioAgent, emit messageEventEmitter) *routeDecisionCoordinator {
	return &routeDecisionCoordinator{
		mio:  mio,
		emit: emit,
	}
}

func (c *routeDecisionCoordinator) SetHeavyWorkerPolicy(policy domainai.HeavyWorkerPolicy) {
	c.heavyPolicy = policy
}

func (c *routeDecisionCoordinator) SetWorkflowEventRecorder(recorder WorkflowEventRecorder) {
	c.workflowEvents = recorder
}

func (c *routeDecisionCoordinator) Decide(ctx context.Context, t task.Task, req ProcessMessageRequest, jobID task.JobID) (routing.Decision, error) {
	decision, err := c.mio.DecideAction(ctx, t)
	if err != nil {
		return routing.Decision{}, fmt.Errorf("routing decision failed: %w", err)
	}
	c.emit("routing.decision", "mio", "",
		fmt.Sprintf("confidence %.0f%% evidence=%s", decision.Confidence*100, routeDecisionEvidenceSummary(decision.Evidence)),
		string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	decision = c.applyHeavyWorkerPolicy(ctx, decision, req, jobID)
	return decision, nil
}

func (c *routeDecisionCoordinator) applyHeavyWorkerPolicy(ctx context.Context, decision routing.Decision, req ProcessMessageRequest, jobID task.JobID) routing.Decision {
	if !canHeavyPolicyElevate(decision.Route) {
		return decision
	}
	heavyReq := heavyWorkerRequestFromMessage(jobID.String(), req.UserMessage)
	if !heavyReq.UserRequestedDeepDive {
		return decision
	}
	evaluated := domainai.EvaluateHeavyWorker(heavyReq, c.heavyPolicy)
	if evaluated.Status != domainai.HeavyWorkerStatusRequested {
		return decision
	}
	recordHeavyWorkflowEvent(ctx, c.workflowEvents, "requested", strings.Join(evaluated.Reasons, "; "), jobID.String())
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
	c.emit("routing.decision", "ai_workflow", "",
		fmt.Sprintf("heavy worker policy elevated route to ANALYZE: %s", strings.Join(evaluated.Reasons, "; ")),
		string(routing.RouteANALYZE), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	return elevated
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
