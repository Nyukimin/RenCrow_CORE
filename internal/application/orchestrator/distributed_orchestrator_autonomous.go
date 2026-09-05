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

type distributedDirectExecutor func(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error)

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

func (c *distributedAutonomousCoordinator) Execute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	if err := validateDistributedTaskID(taskID); err != nil {
		return "", err
	}
	contract, err := contractapp.NormalizeRequestWithRoute(input.MessageText(), route.String())
	if err != nil {
		return "", err
	}
	sessionID, channel, chatID := turnInputMetadata(input)
	result, err := autonomousapp.RunExecutor(ctx, autonomousapp.ExecuteRequest{
		TaskID:     taskID,
		Route:      route.String(),
		Capability: capabilityForRoute(route),
		Contract:   contract,
		MaxRepair:  c.maxRepair(),
		Observe: func(stage autonomousapp.Stage) {
			log.Printf("[AutonomousExecutor] entry.stage=%s route=%s task=%s", stage, route.String(), taskID.String())
			c.emit("entry.stage", channel, "system", string(stage), route.String(), taskID.String(), sessionID, channel, chatID)
		},
		ReportStore: c.reporter,
		Execute: func(execCtx context.Context, attempt int, failureKind, failureReason string) (autonomousapp.AttemptResult, error) {
			log.Printf("[AutonomousExecutor] execute start route=%s task=%s attempt=%d failure_kind=%q", route.String(), taskID.String(), attempt, failureKind)
			execInput := input
			if attempt > 0 {
				execInput = execInput.WithMessageText(buildExecutorRetryMessage(input.MessageText(), route, failureKind, failureReason, attempt))
			}
			resp, runErr := c.executeDirect(execCtx, execInput, route, taskID, ttsSessionID)
			resultKind := classifyExecutorFailure(runErr)
			log.Printf("[AutonomousExecutor] execute complete route=%s task=%s attempt=%d success=%t failure_kind=%q", route.String(), taskID.String(), attempt, runErr == nil, resultKind)
			return autonomousapp.AttemptResult{
				Response:      resp,
				Steps:         routeExecutionSteps(route, runErr == nil),
				FailureKind:   resultKind,
				FailureReason: errorString(runErr),
			}, runErr
		},
		Verify: func(_ context.Context, c domaincontract.Contract, last autonomousapp.AttemptResult) (bool, string, string, error) {
			ok, kind, reason := verifyByContract(route, c, last)
			log.Printf("[AutonomousExecutor] verify route=%s task=%s passed=%t failure_kind=%q reason=%q", route.String(), taskID.String(), ok, kind, reason)
			return ok, kind, reason, nil
		},
	})
	if err != nil {
		return result.Response, err
	}
	return result.Response, nil
}
