package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	appsubagent "github.com/Nyukimin/RenCrow_CORE/internal/application/subagent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// ProcessMessage は既存MessageOrchestratorと同じシグネチャでメッセージを処理
// 分散環境ではTransport経由でAgent間通信を行う
func (o *DistributedOrchestrator) ProcessMessage(ctx context.Context, req ProcessMessageRequest) (ProcessMessageResponse, error) {
	jobID := resolveProcessMessageJobID(req.JobID)
	req.JobID = jobID.String()
	log.Printf("[DistributedOrch] ProcessMessage START: jobID=%s sessionID=%s channel=%s chatID=%s message=%q",
		jobID.String(), req.SessionID, req.Channel, req.ChatID, req.UserMessage)
	startedAt := time.Now().UTC()

	if o.idleNotifier != nil {
		o.idleNotifier.NotifyActivity()
		o.idleNotifier.SetChatBusy(true)
		defer o.idleNotifier.SetChatBusy(false)
	}

	// 1. セッションをロードまたは作成
	sess, err := o.sessions.LoadForRequest(ctx, req)
	if err != nil {
		return ProcessMessageResponse{}, fmt.Errorf("failed to load or create session: %w", err)
	}

	recipient := normalizeProcessViewerRecipient(req.To)
	o.emit("message.received", "user", recipient, req.UserMessage, "", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	if expandedReq, handled, err := o.expandRegisteredSlashCommand(ctx, req); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		req = expandedReq
	}

	// 2. タスクを作成
	t := task.NewTask(jobID, req.UserMessage, req.Channel, req.ChatID).WithViewerRecipient(normalizeProcessViewerRecipient(req.To))
	if resp, handled, err := o.handleExplicitDCI(ctx, req, sess, t, jobID); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		return resp, nil
	}

	// 3. mio がルーティング決定
	decision, err := o.mio.DecideAction(ctx, t)
	if err != nil {
		o.saveExecutionReport(ctx, jobID.String(), req.UserMessage, "", startedAt, time.Now().UTC(), err)
		return ProcessMessageResponse{}, fmt.Errorf("routing decision failed: %w", err)
	}
	log.Printf("[DistributedOrch] routing decision: route=%s confidence=%.2f reason=%q",
		decision.Route, decision.Confidence, decision.Reason)

	o.emit("routing.decision", "mio", "",
		fmt.Sprintf("confidence %.0f%%", decision.Confidence*100),
		string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	if canHeavyPolicyElevate(decision.Route) {
		heavyReq := heavyWorkerRequestFromMessage(jobID.String(), req.UserMessage)
		if heavyReq.UserRequestedDeepDive {
			evaluated := domainai.EvaluateHeavyWorker(heavyReq, o.heavyPolicy)
			if evaluated.Status == domainai.HeavyWorkerStatusRequested {
				recordHeavyWorkflowEvent(ctx, o.workflowEvents, "requested", strings.Join(evaluated.Reasons, "; "), jobID.String())
				decision.Route = routing.RouteANALYZE
				if decision.Confidence < 0.95 {
					decision.Confidence = 0.95
				}
				if decision.Reason == "" {
					decision.Reason = "heavy worker policy requested ANALYZE"
				} else {
					decision.Reason += "; heavy worker policy requested ANALYZE"
				}
				o.emit("routing.decision", "ai_workflow", "",
					fmt.Sprintf("heavy worker policy elevated route to ANALYZE: %s", strings.Join(evaluated.Reasons, "; ")),
					string(routing.RouteANALYZE), jobID.String(), req.SessionID, req.Channel, req.ChatID)
			}
		}
	}
	o.emitNote("mio", "user",
		fmt.Sprintf("%s", routeNoticeText(decision.Route, req.UserMessage)),
		string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID)

	t = t.WithRoute(decision.Route)
	if err := recordRouteSkillBootstrap(ctx, o.skillBootstrap, req, decision.Route); err != nil {
		return ProcessMessageResponse{}, err
	}
	ttsSessionID := o.ttsLifecycle.StartSessionForRoute(ctx, req, jobID, decision)
	runStartedAt, err := recordLeadAgentRunStarted(ctx, o.superAgentRuns, req, jobID, decision.Route)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	leadRunID := leadAgentRunID(jobID)
	if o.superAgentRunController != nil {
		var unregister func()
		ctx, unregister = o.superAgentRunController.RegisterRun(ctx, leadRunID)
		defer unregister()
	}
	ctx = appsubagent.WithSuperAgentRuntime(ctx, leadRunID, []string{"session:" + req.SessionID, "route:" + string(decision.Route)}, nil, "return summary-only subagent result to Lead Agent")

	workerMarkedBusy := false
	if o.idleNotifier != nil && decision.Route != routing.RouteCHAT {
		o.idleNotifier.SetWorkerBusy(true)
		workerMarkedBusy = true
	}
	if workerMarkedBusy {
		defer o.idleNotifier.SetWorkerBusy(false)
	}

	// 4. ルートに応じてTransport経由で実行
	response, err := o.executeDistributed(ctx, t, decision.Route, sess.ID(), ttsSessionID)
	if err != nil {
		if o.superAgentRunController != nil && o.superAgentRunController.IsPauseRequested(leadRunID) {
			_ = recordLeadAgentRunFinished(context.Background(), o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "paused", "pause requested; distributed execution canceled")
		} else {
			_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "failed", err.Error())
		}
		if decision.Route == routing.RouteCHAT {
			o.saveExecutionReport(ctx, jobID.String(), req.UserMessage, string(decision.Route), startedAt, time.Now().UTC(), err)
		}
		return ProcessMessageResponse{}, fmt.Errorf("distributed execution failed: %w", err)
	}
	o.ttsLifecycle.EndSession(ctx, ttsSessionID)

	// 5. タスクを履歴に追加し、セッションを保存
	if err := o.sessions.SaveCompletedTask(ctx, sess, t); err != nil {
		_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "failed", err.Error())
		return ProcessMessageResponse{}, fmt.Errorf("failed to save session: %w", err)
	}
	if err := recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "completed", "Lead Agent completed"); err != nil {
		return ProcessMessageResponse{}, err
	}

	log.Printf("[DistributedOrch] ProcessMessage COMPLETE: jobID=%s route=%s response_len=%d",
		jobID.String(), decision.Route, len(response))
	if decision.Route == routing.RouteCHAT {
		o.saveExecutionReport(ctx, jobID.String(), req.UserMessage, string(decision.Route), startedAt, time.Now().UTC(), nil)
	}

	return ProcessMessageResponse{
		Response:   response,
		Route:      decision.Route,
		Confidence: decision.Confidence,
		JobID:      jobID.String(),
	}, nil
}
