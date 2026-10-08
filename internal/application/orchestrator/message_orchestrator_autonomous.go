package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	autonomousapp "github.com/Nyukimin/RenCrow_CORE/internal/application/autonomous"
	contractapp "github.com/Nyukimin/RenCrow_CORE/internal/application/contract"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domaincontract "github.com/Nyukimin/RenCrow_CORE/internal/domain/contract"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

type autonomousRouteExecutor func(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error)

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

func (c *autonomousExecutionCoordinator) Execute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	if !isAutonomousRoute(route) {
		return "", fmt.Errorf("unknown route: %s", route)
	}
	contract, err := contractapp.NormalizeRequestWithRoute(input.MessageText(), route.String())
	if err != nil {
		return "", err
	}
	sessionID, channel, chatID := turnInputMetadata(input)
	maxRepair := c.maxRepair()
	if input.BackendSelection() != domainconversation.BackendSelectionNone {
		// A turn selected for shiro_native_coding_v1 is delegated once. The repair
		// loop would start a second Harness Task for the same request (after an
		// unknown outcome, a block or a failed check), and resuming is the
		// Harness's own operation with its own limits, so it is not repaired here.
		maxRepair = 0
	}
	result, err := autonomousapp.RunExecutor(ctx, autonomousapp.ExecuteRequest{
		TaskID:     taskID,
		Route:      route.String(),
		Capability: capabilityForRoute(route),
		Contract:   contract,
		MaxRepair:  maxRepair,
		Observe: func(stage autonomousapp.Stage) {
			c.emit("entry.stage", channel, "system", string(stage), route.String(), taskID.String(), sessionID, channel, chatID)
		},
		ReportStore: c.reporter,
		Execute: func(execCtx context.Context, attempt int, failureKind, failureReason string) (autonomousapp.AttemptResult, error) {
			execInput := input
			if attempt > 0 {
				execInput = execInput.WithMessageText(buildExecutorRetryMessage(input.MessageText(), route, failureKind, failureReason, attempt))
			}
			resp, runErr := c.executeDirect(execCtx, execInput, route, taskID, ttsSessionID)
			return autonomousapp.AttemptResult{
				Response:      resp,
				Steps:         routeExecutionSteps(route, runErr == nil),
				FailureKind:   classifyExecutorFailure(runErr),
				FailureReason: errorString(runErr),
			}, runErr
		},
		Verify: func(_ context.Context, c domaincontract.Contract, last autonomousapp.AttemptResult) (bool, string, string, error) {
			if input.BackendSelection() != domainconversation.BackendSelectionNone {
				ok, kind, reason := verifyNativeCodingAttempt(last)
				return ok, kind, reason, nil
			}
			ok, kind, reason := verifyByContract(route, c, last)
			return ok, kind, reason, nil
		},
	})
	if err != nil {
		return result.Response, err
	}
	return result.Response, nil
}

// verifyNativeCodingAttempt is the verification of an attempt of a turn that was
// selected for shiro_native_coding_v1. The acceptance of such a turn is decided
// from the typed RunResult before this point (agent.ShiroAgent.Execute returns
// an error for every Run that is not a completed one, and for a failed
// verification), so the answer text is only required to be present. The
// keyword check of verifyByContract (any "error", "失敗" or "エラー" in the text)
// is a heuristic for the text of a chat or ops answer; applied to the answer of
// a coding Run it would reject an answer that merely talks about an error.
func verifyNativeCodingAttempt(last autonomousapp.AttemptResult) (bool, string, string) {
	if strings.TrimSpace(last.Response) == "" {
		return false, "verification_failed", "empty response"
	}
	return true, "", ""
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
	if agent.IsNativeCodingFailure(err) {
		// Not one of the retryable kinds: a delegation that did not succeed is not
		// repaired by delegating again.
		return "native_coding"
	}
	var terminal *workerResultError
	if errors.As(err, &terminal) {
		return "worker_result_failed"
	}
	var incomplete *coderLoopIncompleteError
	if errors.As(err, &incomplete) {
		return "coder_loop_incomplete"
	}
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
