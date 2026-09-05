package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	appsubagent "github.com/Nyukimin/RenCrow_CORE/internal/application/subagent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ProcessMessage は既存MessageOrchestratorと同じシグネチャでメッセージを処理
// 分散環境ではTransport経由でAgent間通信を行う
func (o *DistributedOrchestrator) ProcessMessage(ctx context.Context, req ProcessMessageRequest) (resp ProcessMessageResponse, err error) {
	startedAt := time.Now().UTC()
	if err := ensureProcessRequestIdentity(&req); err != nil {
		return ProcessMessageResponse{}, err
	}
	sess, req, err := o.sessions.ResolveForRequest(ctx, req, startedAt)
	if err != nil {
		return ProcessMessageResponse{}, fmt.Errorf("failed to resolve session: %w", err)
	}
	defer func() {
		if err == nil {
			resp.SessionID = req.SessionID
		}
	}()
	jobID := modulecore.TaskID(req.JobID)
	traceID := modulecore.TraceID(req.TraceID)
	ctx = contextWithCanonicalTrace(ctx, traceID)
	o.events.BindTrace(jobID.String(), traceID)
	o.events.BindResponseMessageID(jobID.String(), modulecore.MessageID(req.AgentMessageID))
	defer func() {
		o.events.ReleaseTrace(jobID.String())
		o.events.ReleaseResponseMessageID(jobID.String())
	}()
	ctx, cancel := context.WithCancelCause(ctx)
	publicationFailures := o.events.publicationFail
	if publicationFailures != nil {
		publicationFailures.Begin(traceID, cancel)
	}
	defer func() {
		var publicationErr error
		if publicationFailures != nil {
			publicationErr = publicationFailures.End(traceID)
		}
		if publicationErr == nil {
			cancel(nil)
			return
		}
		cancel(publicationErr)
		wrapped := fmt.Errorf("canonical event publication failed: %w", publicationErr)
		resp = ProcessMessageResponse{}
		if err == nil {
			err = wrapped
		} else if !errors.Is(err, publicationErr) {
			err = errors.Join(err, wrapped)
		}
	}()
	preserveOriginalUserMessage(&req)
	log.Printf("[DistributedOrch] ProcessMessage START: jobID=%s traceID=%s messageID=%s sessionID=%s channel=%s chatID=%s message=%q",
		jobID.String(), req.TraceID, req.MessageID, req.SessionID, req.Channel, req.ChatID, req.UserMessage)
	if err := o.events.EmitMessageReceived(req, jobID.String()); err != nil {
		return ProcessMessageResponse{}, err
	}
	if o.idleNotifier != nil {
		o.idleNotifier.NotifyActivity()
		o.idleNotifier.SetChatBusy(true)
		defer o.idleNotifier.SetChatBusy(false)
	}

	if expandedReq, handled, err := o.expandRegisteredSlashCommand(ctx, req); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		req = expandedReq
	}
	if o.visionRequests != nil {
		processed, err := o.visionRequests.Process(ctx, req, o.events.Emit)
		if err != nil {
			return ProcessMessageResponse{}, err
		}
		req = processed
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
	}

	// 2. Reconstruct the canonical conversation input once at ingress.
	input, err := buildTurnInputFromProcessRequest(req)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	if resp, handled, err := o.handleDailyNewsBrief(ctx, req, sess, input, jobID); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		return ensureProcessResponseIdentity(resp, jobID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID), nil
	}
	if resp, handled, err := o.handleExplicitDCI(ctx, req, sess, input, jobID); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		return ensureProcessResponseIdentity(resp, jobID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID), nil
	}
	if resp, handled, err := o.handleDurableStore(ctx, req, sess, input, jobID); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		return ensureProcessResponseIdentity(resp, jobID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID), nil
	}

	// 3. mio がルーティング決定
	decision, err := o.mio.DecideAction(ctx, input)
	if err != nil {
		o.saveExecutionReport(ctx, jobID.String(), req.UserMessage, "", startedAt, time.Now().UTC(), err)
		return ProcessMessageResponse{}, fmt.Errorf("routing decision failed: %w", err)
	}
	decision, pinnedViewerRecipient := pinSelectedViewerRecipientDecision(decision, req)
	log.Printf("[DistributedOrch] routing decision: route=%s confidence=%.2f reason=%q",
		decision.Route, decision.Confidence, decision.Reason)

	o.emit("routing.decision", "mio", "",
		fmt.Sprintf("confidence %.0f%% evidence=%s", decision.Confidence*100, routeDecisionEvidenceSummary(decision.Evidence)),
		string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	if !pinnedViewerRecipient && canHeavyPolicyElevate(decision.Route) {
		heavyReq := heavyWorkerRequestFromMessage(jobID.String(), req.UserMessage)
		if heavyReq.UserRequestedDeepDive {
			evaluated := domainai.EvaluateHeavyWorker(heavyReq, o.heavyPolicy)
			if evaluated.Status == domainai.HeavyWorkerStatusRequested {
				recordHeavyCanonicalEvent(ctx, o.canonicalEvents, "requested", strings.Join(evaluated.Reasons, "; "), jobID.String())
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
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}

	input = input.WithRoute(decision.Route)
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
	response, err := o.executeDistributed(ctx, input, decision.Route, jobID, ttsSessionID)
	if publicationErr := o.events.PublicationError(traceID); publicationErr != nil {
		if err != nil {
			return ProcessMessageResponse{}, errors.Join(err, publicationErr)
		}
		return ProcessMessageResponse{}, publicationErr
	}
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
	if err := o.sessions.SaveCompletedTurnInput(ctx, sess, input); err != nil {
		_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "failed", err.Error())
		return ProcessMessageResponse{}, fmt.Errorf("failed to save session: %w", err)
	}
	if err := recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "completed", "Lead Agent completed"); err != nil {
		return ProcessMessageResponse{}, err
	}

	if decision.Route == routing.RouteCHAT {
		o.saveExecutionReport(ctx, jobID.String(), req.UserMessage, string(decision.Route), startedAt, time.Now().UTC(), nil)
	}

	resp = ensureProcessResponseIdentity(ProcessMessageResponse{
		Response:   response,
		Route:      decision.Route,
		Confidence: decision.Confidence,
		JobID:      jobID.String(),
	}, jobID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID)
	log.Printf("[DistributedOrch] ProcessMessage COMPLETE: jobID=%s traceID=%s messageID=%s route=%s response_len=%d",
		jobID.String(), resp.TraceID, resp.MessageID, decision.Route, len(response))
	return resp, nil
}
