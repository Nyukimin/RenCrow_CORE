package orchestrator

import (
	"context"
	"log"

	autonomousapp "github.com/Nyukimin/RenCrow_CORE/internal/application/autonomous"
	contractapp "github.com/Nyukimin/RenCrow_CORE/internal/application/contract"
	domaincontract "github.com/Nyukimin/RenCrow_CORE/internal/domain/contract"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type distributedDirectExecutor func(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error)

type distributedAutonomousCoordinator struct {
	reporter      ReportStore
	maxRepair     func() int
	emit          messageEventEmitter
	executeDirect distributedDirectExecutor
}

func newDistributedAutonomousCoordinator(
	reporter ReportStore,
	maxRepair func() int,
	emit messageEventEmitter,
	executeDirect distributedDirectExecutor,
) *distributedAutonomousCoordinator {
	return &distributedAutonomousCoordinator{
		reporter:      reporter,
		maxRepair:     maxRepair,
		emit:          emit,
		executeDirect: executeDirect,
	}
}

func (c *distributedAutonomousCoordinator) SetReportStore(reporter ReportStore) {
	c.reporter = reporter
}

func (c *distributedAutonomousCoordinator) Execute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
	contract, err := contractapp.NormalizeRequestWithRoute(input.MessageText(), route.String())
	if err != nil {
		return "", err
	}
	sessionID, channel, chatID := turnInputMetadata(input)
	result, err := autonomousapp.RunExecutor(ctx, autonomousapp.ExecuteRequest{
		JobID:      jobID.String(),
		Route:      route.String(),
		Capability: capabilityForRoute(route),
		Contract:   contract,
		MaxRepair:  c.maxRepair(),
		Observe: func(stage autonomousapp.Stage) {
			log.Printf("[AutonomousExecutor] entry.stage=%s route=%s job=%s", stage, route.String(), jobID.String())
			c.emit("entry.stage", channel, "system", string(stage), route.String(), jobID.String(), sessionID, channel, chatID)
		},
		ReportStore: c.reporter,
		Execute: func(execCtx context.Context, attempt int, failureKind, failureReason string) (autonomousapp.AttemptResult, error) {
			log.Printf("[AutonomousExecutor] execute start route=%s job=%s attempt=%d failure_kind=%q", route.String(), jobID.String(), attempt, failureKind)
			execInput := input
			if attempt > 0 {
				execInput = execInput.WithMessageText(buildExecutorRetryMessage(input.MessageText(), route, failureKind, failureReason, attempt))
			}
			resp, runErr := c.executeDirect(execCtx, execInput, route, jobID, ttsSessionID)
			resultKind := classifyExecutorFailure(runErr)
			log.Printf("[AutonomousExecutor] execute complete route=%s job=%s attempt=%d success=%t failure_kind=%q", route.String(), jobID.String(), attempt, runErr == nil, resultKind)
			return autonomousapp.AttemptResult{
				Response:      resp,
				Steps:         routeExecutionSteps(route, runErr == nil),
				FailureKind:   resultKind,
				FailureReason: errorString(runErr),
			}, runErr
		},
		Verify: func(_ context.Context, c domaincontract.Contract, last autonomousapp.AttemptResult) (bool, string, string, error) {
			ok, kind, reason := verifyByContract(route, c, last)
			log.Printf("[AutonomousExecutor] verify route=%s job=%s passed=%t failure_kind=%q reason=%q", route.String(), jobID.String(), ok, kind, reason)
			return ok, kind, reason, nil
		},
	})
	if err != nil {
		return result.Response, err
	}
	return result.Response, nil
}
