package orchestrator

import (
	"context"
	"fmt"

	autonomousapp "github.com/Nyukimin/RenCrow_CORE/internal/application/autonomous"
	contractapp "github.com/Nyukimin/RenCrow_CORE/internal/application/contract"
	domaincontract "github.com/Nyukimin/RenCrow_CORE/internal/domain/contract"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

type autonomousRouteExecutor func(ctx context.Context, t task.Task, route routing.Route, sessionID, channel, chatID, ttsSessionID string) (string, error)

type autonomousExecutionCoordinator struct {
	reporter      ReportStore
	maxRepair     func() int
	emit          messageEventEmitter
	executeDirect autonomousRouteExecutor
}

func newAutonomousExecutionCoordinator(
	reporter ReportStore,
	maxRepair func() int,
	emit messageEventEmitter,
	executeDirect autonomousRouteExecutor,
) *autonomousExecutionCoordinator {
	return &autonomousExecutionCoordinator{
		reporter:      reporter,
		maxRepair:     maxRepair,
		emit:          emit,
		executeDirect: executeDirect,
	}
}

func (c *autonomousExecutionCoordinator) SetReportStore(reporter ReportStore) {
	c.reporter = reporter
}

func (c *autonomousExecutionCoordinator) Execute(ctx context.Context, t task.Task, route routing.Route, sessionID, channel, chatID, ttsSessionID string) (string, error) {
	if !isAutonomousRoute(route) {
		return "", fmt.Errorf("unknown route: %s", route)
	}
	contract, err := contractapp.NormalizeRequestWithRoute(t.UserMessage(), route.String())
	if err != nil {
		return "", err
	}
	result, err := autonomousapp.RunExecutor(ctx, autonomousapp.ExecuteRequest{
		JobID:      t.JobID().String(),
		Route:      route.String(),
		Capability: capabilityForRoute(route),
		Contract:   contract,
		MaxRepair:  c.maxRepair(),
		Observe: func(stage autonomousapp.Stage) {
			c.emit("entry.stage", channel, "system", string(stage), route.String(), t.JobID().String(), sessionID, channel, chatID)
		},
		ReportStore: c.reporter,
		Execute: func(execCtx context.Context, attempt int, failureKind, failureReason string) (autonomousapp.AttemptResult, error) {
			execTask := t
			if attempt > 0 {
				execTask = execTask.WithUserMessage(buildExecutorRetryMessage(t.UserMessage(), route, failureKind, failureReason, attempt))
			}
			resp, runErr := c.executeDirect(execCtx, execTask, route, sessionID, channel, chatID, ttsSessionID)
			return autonomousapp.AttemptResult{
				Response:      resp,
				Steps:         routeExecutionSteps(route, runErr == nil),
				FailureKind:   classifyExecutorFailure(runErr),
				FailureReason: errorString(runErr),
			}, runErr
		},
		Verify: func(_ context.Context, c domaincontract.Contract, last autonomousapp.AttemptResult) (bool, string, string, error) {
			ok, kind, reason := verifyByContract(route, c, last)
			return ok, kind, reason, nil
		},
	})
	if err != nil {
		return result.Response, err
	}
	return result.Response, nil
}

func capabilityForRoute(route routing.Route) autonomousapp.CapabilityPack {
	return autonomousapp.CapabilityPack(moduleworker.CapabilityForRoute(route.String()))
}

func isAutonomousRoute(route routing.Route) bool {
	return moduleworker.IsAutonomousRoute(route.String())
}

func routeExecutionSteps(route routing.Route, ok bool) []string {
	return moduleworker.RouteExecutionSteps(route.String(), ok)
}

func classifyExecutorFailure(err error) string {
	return moduleworker.ClassifyExecutorFailure(err)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func responseLooksLikeFailure(content string) bool {
	return moduleworker.ResponseLooksLikeFailure(content)
}

func shortFailureReason(content string) string {
	return moduleworker.ShortFailureReason(content)
}

// verifyByContract はルートと実行契約に基づいて AttemptResult を検証する。
// verifyAutonomousRouteResponse の後継。
func verifyByContract(
	route routing.Route,
	c domaincontract.Contract,
	last autonomousapp.AttemptResult,
) (bool, string, string) {
	return moduleworker.VerifyAutonomousAttempt(route.String(), autonomousContractFromDomain(c), autonomousAttemptFromApp(last))
}

// isTTSCapability は契約の Acceptance フィールドから TTS CapabilityPack かどうかを判定する。
func isTTSCapability(c domaincontract.Contract) bool {
	return moduleworker.IsTTSCapability(autonomousContractFromDomain(c))
}

// verifyTTSResult は TTS CapabilityPack の E2E 検証を行う。
// PlaybackCode/TTSAudioFile が未設定の場合は暫定フォールバック（レスポンス文字列チェック）。
func verifyTTSResult(last autonomousapp.AttemptResult) (bool, string, string) {
	return moduleworker.VerifyTTSAttempt(autonomousAttemptFromApp(last))
}

// looksLikeNonExecutable は Coder の出力が設計文書のみで実行可能形式を含まないかを判定する。
func looksLikeNonExecutable(response string) bool {
	return moduleworker.LooksLikeNonExecutable(response)
}

func buildExecutorRetryMessage(userMessage string, route routing.Route, failureKind, failureReason string, attempt int) string {
	return moduleworker.BuildExecutorRetryMessage(userMessage, route.String(), failureKind, failureReason, attempt)
}

func autonomousContractFromDomain(c domaincontract.Contract) moduleworker.AutonomousContract {
	return moduleworker.AutonomousContract{Acceptance: append([]string(nil), c.Acceptance...)}
}

func autonomousAttemptFromApp(last autonomousapp.AttemptResult) moduleworker.AutonomousAttemptResult {
	return moduleworker.AutonomousAttemptResult{
		Response:     last.Response,
		TTSAudioFile: last.TTSAudioFile,
		PlaybackCode: last.PlaybackCode,
	}
}
