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
	rootTaskID := modulecore.TaskID(req.RootTaskID)
	taskID := rootTaskID
	executionTaskID := rootTaskID
	lifecycleCreated := false
	traceID := modulecore.TraceID(req.TraceID)
	ctx = contextWithCanonicalTrace(ctx, traceID)
	ctx = withOrchestrationLLMObservation(ctx, rootTaskID, traceID, req.SessionID, "orchestrator.distributed")
	o.events.BindTrace(rootTaskID.String(), traceID)
	o.events.BindResponseMessageID(rootTaskID.String(), modulecore.MessageID(req.AgentMessageID))
	defer func() {
		o.events.ReleaseTrace(rootTaskID.String())
		o.events.ReleaseResponseMessageID(rootTaskID.String())
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
	defer func() {
		if !lifecycleCreated || o.taskLifecycle == nil {
			return
		}
		lifecycleErr := err
		if publicationErr := publicationFailures.Current(traceID); publicationErr != nil {
			if lifecycleErr == nil {
				lifecycleErr = publicationErr
			} else {
				lifecycleErr = errors.Join(lifecycleErr, publicationErr)
			}
		}
		if finalizeErr := o.taskLifecycle.finish(context.WithoutCancel(ctx), rootTaskID, executionTaskID, resp.Response, lifecycleErr); finalizeErr != nil {
			if err == nil {
				err = finalizeErr
			} else {
				err = errors.Join(err, finalizeErr)
			}
			resp = ProcessMessageResponse{}
		}
	}()
	if o.taskLifecycle != nil {
		if _, err := o.taskLifecycle.createRoot(ctx, req); err != nil {
			return ProcessMessageResponse{}, err
		}
		lifecycleCreated = true
	}
	taskActivation, cleanupTaskActivation := newTaskLifecycleActivation(o.taskLifecycle, o.events, req)
	defer cleanupTaskActivation()
	activateConfiguredTask := func(route routing.Route, actor, content string) (modulecore.TaskID, error) {
		if o.taskLifecycle == nil {
			return rootTaskID, nil
		}
		activatedTaskID, activateErr := taskActivation.Activate(ctx, route, actor, content)
		if activatedTaskID != "" {
			executionTaskID = activatedTaskID
			taskID = activatedTaskID
			ctx = withOrchestrationLLMTask(ctx, activatedTaskID)
		}
		return activatedTaskID, activateErr
	}
	preserveOriginalUserMessage(&req)
	log.Printf("[DistributedOrch] ProcessMessage START: taskID=%s traceID=%s messageID=%s sessionID=%s channel=%s chatID=%s message=%q",
		taskID.String(), req.TraceID, req.MessageID, req.SessionID, req.Channel, req.ChatID, req.UserMessage)
	if err := o.events.EmitMessageReceived(req, taskID.String()); err != nil {
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
	dailyBriefRequested := isDailyNewsBriefRequest(req.UserMessage)
	if dailyBriefRequested {
		if _, err := activateConfiguredTask(routing.RouteCHAT, taskLifecycleMio, "confidence 100% evidence=daily news brief intent"); err != nil {
			return ProcessMessageResponse{}, err
		}
	}
	activateDailyShiro := func(activateCtx context.Context) (modulecore.TaskID, error) {
		if o.taskLifecycle == nil {
			return rootTaskID, nil
		}
		activatedTaskID, activateErr := taskActivation.Activate(activateCtx, routing.RouteCHAT, taskLifecycleShiro, "confidence 100% evidence=daily news brief intent")
		if activatedTaskID != "" {
			executionTaskID = activatedTaskID
			taskID = activatedTaskID
			ctx = withOrchestrationLLMTask(activateCtx, activatedTaskID)
		}
		return activatedTaskID, activateErr
	}
	if resp, handled, err := o.handleDailyNewsBrief(ctx, req, sess, input, rootTaskID, activateDailyShiro); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		return ensureProcessResponseIdentity(resp, taskID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID), nil
	}
	explicitDCI := shouldHandleExplicitDCI(o.dciSearcher, req.UserMessage)
	if explicitDCI {
		if _, err := activateConfiguredTask(routing.RouteRESEARCH, taskLifecycleShiro, "confidence 100% evidence=explicit DCI trigger"); err != nil {
			return ProcessMessageResponse{}, err
		}
	}
	if resp, handled, err := o.handleExplicitDCI(ctx, req, sess, input, taskID, explicitDCI); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		return ensureProcessResponseIdentity(resp, taskID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID), nil
	}
	durableResult, durableHandled, durableErr := evaluateDurableStore(ctx, o.durableStoreWorkflow, req)
	if durableHandled {
		if _, err := activateConfiguredTask(routing.RouteCHAT, taskLifecycleMio, "confidence 100% evidence=durable store workflow"); err != nil {
			return ProcessMessageResponse{}, err
		}
		if durableErr != nil {
			return ProcessMessageResponse{}, durableErr
		}
		resp, err := o.completeDurableStore(ctx, req, sess, input, taskID, durableResult)
		if err != nil {
			return ProcessMessageResponse{}, err
		}
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		return ensureProcessResponseIdentity(resp, taskID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID), nil
	}
	if durableErr != nil {
		return ProcessMessageResponse{}, durableErr
	}

	// 3. mio がルーティング決定
	decision, err := o.mio.DecideAction(ctx, input)
	if err != nil {
		o.saveExecutionReport(ctx, taskID, req.UserMessage, "", startedAt, time.Now().UTC(), err)
		return ProcessMessageResponse{}, fmt.Errorf("routing decision failed: %w", err)
	}
	decision, pinnedViewerRecipient := pinSelectedViewerRecipientDecision(decision, req)
	log.Printf("[DistributedOrch] routing decision: route=%s confidence=%.2f reason=%q",
		decision.Route, decision.Confidence, decision.Reason)

	if !pinnedViewerRecipient && canHeavyPolicyElevate(decision.Route) {
		heavyReq := heavyWorkerRequestFromMessage(taskID.String(), req.UserMessage)
		if heavyReq.UserRequestedDeepDive {
			evaluated := domainai.EvaluateHeavyWorker(heavyReq, o.heavyPolicy)
			if evaluated.Status == domainai.HeavyWorkerStatusRequested {
				recordHeavyCanonicalEvent(ctx, o.canonicalEvents, "requested", strings.Join(evaluated.Reasons, "; "), taskID.String())
				decision.Route = routing.RouteANALYZE
				if decision.Confidence < 0.95 {
					decision.Confidence = 0.95
				}
				if decision.Reason == "" {
					decision.Reason = "heavy worker policy requested ANALYZE"
				} else {
					decision.Reason += "; heavy worker policy requested ANALYZE"
				}
			}
		}
	}
	actor, err := actualCoreActorForRequest(decision.Route, req)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	preparedTaskID, err := taskActivation.Activate(ctx, decision.Route, actor, fmt.Sprintf("confidence %.0f%% evidence=%s", decision.Confidence*100, routeDecisionEvidenceSummary(decision.Evidence)))
	if preparedTaskID != "" {
		executionTaskID = preparedTaskID
		taskID = preparedTaskID
		ctx = withOrchestrationLLMTask(ctx, preparedTaskID)
	}
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	leadRunID, err := resolveLeadAgentRun(ctx, o.taskLifecycle, taskID, o.superAgentRuns, o.superAgentRunController)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	o.emitNote("mio", "user",
		fmt.Sprintf("%s", routeNoticeText(decision.Route, req.UserMessage)),
		string(decision.Route), taskID.String(), req.SessionID, req.Channel, req.ChatID)
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}

	input = input.WithRoute(decision.Route)
	if err := recordRouteSkillBootstrap(ctx, o.skillBootstrap, req, decision.Route); err != nil {
		return ProcessMessageResponse{}, err
	}
	ttsSessionID := o.ttsLifecycle.StartSessionForRoute(ctx, req, taskID, decision)
	runStartedAt, err := recordLeadAgentRunStarted(ctx, o.superAgentRuns, req, taskID, leadRunID, actor, decision.Route)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	if o.superAgentRunController != nil {
		var unregister func()
		ctx, unregister = o.superAgentRunController.RegisterRun(ctx, string(leadRunID))
		defer unregister()
	}
	if leadRunID != "" {
		ctx = appsubagent.WithSuperAgentRuntime(ctx, taskID, leadRunID, actor, runStartedAt.TraceID, runStartedAt.StartedEventID, []string{"session:" + req.SessionID, "route:" + string(decision.Route)}, nil, "return summary-only subagent result to Lead Agent")
	}

	workerMarkedBusy := false
	if o.idleNotifier != nil && decision.Route != routing.RouteCHAT {
		o.idleNotifier.SetWorkerBusy(true)
		workerMarkedBusy = true
	}
	if workerMarkedBusy {
		defer o.idleNotifier.SetWorkerBusy(false)
	}

	// 4. ルートに応じてTransport経由で実行
	response, err := o.executeDistributed(ctx, input, decision.Route, taskID, ttsSessionID)
	if publicationErr := o.events.PublicationError(traceID); publicationErr != nil {
		if err != nil {
			return ProcessMessageResponse{}, errors.Join(err, publicationErr)
		}
		return ProcessMessageResponse{}, publicationErr
	}
	if err != nil {
		if o.superAgentRunController != nil && o.superAgentRunController.IsPauseRequested(string(leadRunID)) {
			_ = recordLeadAgentRunFinished(context.Background(), o.superAgentRuns, req, taskID, leadRunID, actor, decision.Route, runStartedAt, "paused", "pause requested; distributed execution canceled")
		} else {
			_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, taskID, leadRunID, actor, decision.Route, runStartedAt, "failed", err.Error())
		}
		if decision.Route == routing.RouteCHAT {
			o.saveExecutionReport(ctx, taskID, req.UserMessage, string(decision.Route), startedAt, time.Now().UTC(), err)
		}
		return ProcessMessageResponse{}, fmt.Errorf("distributed execution failed: %w", err)
	}
	o.ttsLifecycle.EndSession(ctx, ttsSessionID)

	// 5. タスクを履歴に追加し、セッションを保存
	if err := o.sessions.SaveCompletedTurnInput(ctx, sess, input); err != nil {
		_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, taskID, leadRunID, actor, decision.Route, runStartedAt, "failed", err.Error())
		return ProcessMessageResponse{}, fmt.Errorf("failed to save session: %w", err)
	}
	if err := recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, taskID, leadRunID, actor, decision.Route, runStartedAt, "completed", "Lead Agent completed"); err != nil {
		return ProcessMessageResponse{}, err
	}

	if decision.Route == routing.RouteCHAT {
		o.saveExecutionReport(ctx, taskID, req.UserMessage, string(decision.Route), startedAt, time.Now().UTC(), nil)
	}

	resp = ensureProcessResponseIdentity(ProcessMessageResponse{
		Response:   response,
		Route:      decision.Route,
		Confidence: decision.Confidence,
		TaskID:     taskID.String(),
	}, taskID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID)
	log.Printf("[DistributedOrch] ProcessMessage COMPLETE: taskID=%s traceID=%s messageID=%s route=%s response_len=%d",
		taskID.String(), resp.TraceID, resp.MessageID, decision.Route, len(response))
	return resp, nil
}
