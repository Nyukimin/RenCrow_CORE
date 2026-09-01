package dcimigration

import (
	"context"
	"errors"
)

// cutoverServiceManager is the narrow service-owner boundary for D2d.  The
// implementation owns all service commands, listener checks, and readiness
// checks; this package receives only bounded evidence and never accepts an
// arbitrary command, service name, or path.
type cutoverServiceManager interface {
	VerifyRunning(context.Context, string) (cutoverServiceRunningEvidence, error)
	MaskAndStop(context.Context) error
	VerifyStopped(context.Context) (cutoverServiceStoppedEvidence, error)
	UnmaskAndStart(context.Context) error
}

type cutoverServiceRunningEvidence struct {
	Owner           int
	Enabled         int
	Unmasked        int
	Active          int
	MainPIDPositive int
	ListenerOwned   int
	Readiness       int
	RuntimeSHA256   string
}

type cutoverServiceStoppedEvidence struct {
	Masked       int
	Active       int
	MainPIDZero  int
	ListenerZero int
}

func (e cutoverServiceRunningEvidence) valid(expectedSHA256 string) bool {
	return expectedSHA256 != "" && isLowerHexSHA256(expectedSHA256) &&
		e.Owner == 1 && e.Enabled == 1 && e.Unmasked == 1 && e.Active == 1 &&
		e.MainPIDPositive == 1 && e.ListenerOwned == 1 && e.Readiness == 1 &&
		e.RuntimeSHA256 == expectedSHA256
}

func (e cutoverServiceStoppedEvidence) valid() bool {
	return e.Masked == 1 && e.Active == 0 && e.MainPIDZero == 1 && e.ListenerZero == 1
}

type cutoverServiceOptions struct {
	build          cutoverArtifactOptions
	active         cutoverActiveOptions
	manager        cutoverServiceManager
	ServiceReceipt string
}

type cutoverServiceStatus string

const (
	cutoverServiceApplied        cutoverServiceStatus = CutoverStatusApplied
	cutoverServiceBlocked        cutoverServiceStatus = CutoverStatusBlocked
	cutoverServiceRolledBack     cutoverServiceStatus = CutoverStatusRolledBack
	cutoverServiceRollbackFailed cutoverServiceStatus = CutoverStatusRollbackFailed
)

type cutoverServiceResult struct {
	status               cutoverServiceStatus
	receipt              CutoverReceipt
	initialRunning       cutoverServiceRunningEvidence
	stoppedBeforePrepare cutoverServiceStoppedEvidence
	stoppedBeforeApply   cutoverServiceStoppedEvidence
	finalRunning         cutoverServiceRunningEvidence
	// applied remains private on an operational applied result, and on the
	// rollback_failed boundary where the post-apply stop proof itself failed;
	// the latter is the only rollback_failed path that still has a coherent
	// D2c-authorized applied cohort. Other rollback_failed paths may be partial
	// and deliberately carry no reusable authorization state.
	applied *cutoverAppliedState
}

var cutoverServicePrepareBuild = prepareCutoverBuildCohort
var cutoverServicePrepareActive = prepareCutoverActiveCohort
var cutoverServiceStage = stageCutoverCohort
var cutoverServiceApply = applyStagedCutoverOperation
var cutoverServiceRollback = rollbackAppliedCutover

// executeServiceCutover coordinates the service lifecycle around the
// already-reviewed D2c file cutover.  It performs no service command itself;
// the supplied owner is the sole service-command authority.
func executeServiceCutover(ctx context.Context, options cutoverServiceOptions) (cutoverServiceResult, error) {
	if ctx == nil {
		return cutoverServiceResult{}, cutoverServiceError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceResult{}, err
	}
	if err := validateCutoverServiceOptions(options); err != nil {
		return cutoverServiceResult{status: cutoverServiceBlocked}, cutoverServiceError(errorCode(err, "invalid_options"))
	}

	serviceResult := cutoverServiceResult{}
	oldRuntimeSHA256 := options.build.ExpectedInstalledRuntimeSHA256
	running, err := options.manager.VerifyRunning(ctx, oldRuntimeSHA256)
	serviceResult.initialRunning = running
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return cutoverServiceResult{}, err
		}
		return cutoverServiceResult{status: cutoverServiceBlocked}, cutoverServiceError("service_running")
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceResult{}, err
	}
	if !running.valid(oldRuntimeSHA256) {
		return cutoverServiceResult{status: cutoverServiceBlocked}, cutoverServiceError("service_running")
	}

	if err := options.manager.MaskAndStop(ctx); err != nil {
		return recoverCutoverServiceBeforeMutationWithResult(ctx, options.manager, oldRuntimeSHA256, serviceResult, "service_stop", err)
	}
	stopped, err := observeCutoverServiceStopped(ctx, options.manager)
	serviceResult.stoppedBeforePrepare = stopped
	if err != nil {
		return recoverCutoverServiceBeforeMutationWithResult(ctx, options.manager, oldRuntimeSHA256, serviceResult, "service_stopped", err)
	}

	preparedBuild, err := cutoverServicePrepareBuild(ctx, options.build)
	if err != nil {
		return recoverCutoverServiceBeforeMutationWithResult(ctx, options.manager, oldRuntimeSHA256, serviceResult, errorCode(err, "build_prepare"), err)
	}
	preparedActive, err := cutoverServicePrepareActive(ctx, preparedBuild, options.active)
	if err != nil {
		return recoverCutoverServiceBeforeMutationWithResult(ctx, options.manager, oldRuntimeSHA256, serviceResult, errorCode(err, "active_prepare"), err)
	}
	staged, err := cutoverServiceStage(ctx, preparedActive)
	if err != nil {
		return recoverCutoverServiceBeforeMutationWithResult(ctx, options.manager, oldRuntimeSHA256, serviceResult, errorCode(err, "stage_prepare"), err)
	}
	stopped, err = observeCutoverServiceStopped(ctx, options.manager)
	serviceResult.stoppedBeforeApply = stopped
	if err != nil {
		return recoverCutoverServiceBeforeMutationWithResult(ctx, options.manager, oldRuntimeSHA256, serviceResult, "service_stopped", err)
	}

	appliedResult, applyErr := cutoverServiceApply(ctx, staged)
	serviceResult.status = cutoverServiceStatusForReceipt(appliedResult.receipt)
	serviceResult.receipt = cloneCutoverReceiptValue(appliedResult.receipt)
	serviceResult.applied = appliedResult.applied
	if applyErr != nil || serviceResult.status != cutoverServiceApplied || serviceResult.applied == nil {
		return handleCutoverServiceApplyFailure(ctx, options.manager, oldRuntimeSHA256, serviceResult, applyErr)
	}

	newRuntimeSHA256 := options.build.ExpectedStagedRuntimeSHA256
	if err := ctx.Err(); err != nil {
		return handleCutoverServiceAppliedFailure(ctx, options.manager, oldRuntimeSHA256, serviceResult, "context", err)
	}
	if err := options.manager.UnmaskAndStart(ctx); err != nil {
		return handleCutoverServiceAppliedFailure(ctx, options.manager, oldRuntimeSHA256, serviceResult, "service_start", err)
	}
	running, err = options.manager.VerifyRunning(ctx, newRuntimeSHA256)
	serviceResult.finalRunning = running
	if err != nil {
		return handleCutoverServiceAppliedFailure(ctx, options.manager, oldRuntimeSHA256, serviceResult, "service_readiness", err)
	}
	if err := ctx.Err(); err != nil {
		return handleCutoverServiceAppliedFailure(ctx, options.manager, oldRuntimeSHA256, serviceResult, "context", err)
	}
	if !running.valid(newRuntimeSHA256) {
		return handleCutoverServiceAppliedFailure(ctx, options.manager, oldRuntimeSHA256, serviceResult, "service_readiness", errors.New("new service evidence is invalid"))
	}
	return serviceResult, nil
}

func validateCutoverServiceOptions(options cutoverServiceOptions) error {
	if options.manager == nil {
		return errors.New("service manager is required")
	}
	if err := validateCutoverArtifactOptions(options.build); err != nil {
		return err
	}
	return validateCutoverActiveOptions(options.active)
}

func proveCutoverServiceStopped(ctx context.Context, manager cutoverServiceManager) error {
	_, err := observeCutoverServiceStopped(ctx, manager)
	return err
}

func observeCutoverServiceStopped(ctx context.Context, manager cutoverServiceManager) (cutoverServiceStoppedEvidence, error) {
	if ctx == nil || manager == nil {
		return cutoverServiceStoppedEvidence{}, errors.New("service stopped proof input is invalid")
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceStoppedEvidence{}, err
	}
	evidence, err := manager.VerifyStopped(ctx)
	if err != nil {
		return evidence, err
	}
	if err := ctx.Err(); err != nil {
		return evidence, err
	}
	if !evidence.valid() {
		return evidence, errors.New("service stopped evidence is invalid")
	}
	return evidence, nil
}

func restoreCutoverService(ctx context.Context, manager cutoverServiceManager, oldRuntimeSHA256 string) bool {
	_, ok := restoreCutoverServiceEvidence(ctx, manager, oldRuntimeSHA256)
	return ok
}

func restoreCutoverServiceEvidence(ctx context.Context, manager cutoverServiceManager, oldRuntimeSHA256 string) (cutoverServiceRunningEvidence, bool) {
	if ctx == nil || manager == nil {
		return cutoverServiceRunningEvidence{}, false
	}
	recoveryContext := context.WithoutCancel(ctx)
	_ = manager.UnmaskAndStart(recoveryContext)
	running, err := manager.VerifyRunning(recoveryContext, oldRuntimeSHA256)
	if err == nil && running.valid(oldRuntimeSHA256) {
		return running, true
	}
	_ = forceCutoverServiceStopped(recoveryContext, manager)
	return running, false
}

func forceCutoverServiceStopped(ctx context.Context, manager cutoverServiceManager) bool {
	if ctx == nil || manager == nil {
		return false
	}
	_ = manager.MaskAndStop(ctx)
	return proveCutoverServiceStopped(ctx, manager) == nil
}

func recoverCutoverServiceBeforeMutation(ctx context.Context, manager cutoverServiceManager, oldRuntimeSHA256 string, receipt CutoverReceipt, code string, cause error) (cutoverServiceResult, error) {
	return recoverCutoverServiceBeforeMutationWithResult(ctx, manager, oldRuntimeSHA256, cutoverServiceResult{receipt: receipt}, code, cause)
}

func recoverCutoverServiceBeforeMutationWithResult(ctx context.Context, manager cutoverServiceManager, oldRuntimeSHA256 string, result cutoverServiceResult, code string, cause error) (cutoverServiceResult, error) {
	if running, ok := restoreCutoverServiceEvidence(ctx, manager, oldRuntimeSHA256); ok {
		result.status = cutoverServiceBlocked
		result.receipt = cloneCutoverReceiptValue(result.receipt)
		result.finalRunning = running
		return result, cutoverServiceFailure(code, cause)
	}
	result.status = cutoverServiceRollbackFailed
	result.receipt = cloneCutoverReceiptValue(result.receipt)
	return result, cutoverServiceError(CutoverStatusRollbackFailed)
}

func handleCutoverServiceApplyFailure(ctx context.Context, manager cutoverServiceManager, oldRuntimeSHA256 string, result cutoverServiceResult, cause error) (cutoverServiceResult, error) {
	if result.status == cutoverServiceRollbackFailed {
		_ = forceCutoverServiceStopped(context.WithoutCancel(ctx), manager)
		result.status = cutoverServiceRollbackFailed
		result.receipt = cloneCutoverReceiptValue(result.receipt)
		return result, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	if result.status == cutoverServiceRolledBack {
		if running, ok := restoreCutoverServiceEvidence(ctx, manager, oldRuntimeSHA256); ok {
			result.status = cutoverServiceRolledBack
			result.receipt = cloneCutoverReceiptValue(result.receipt)
			result.applied = nil
			result.finalRunning = running
			return result, cutoverServiceFailure(CutoverStatusRolledBack, cause)
		}
		result.status = cutoverServiceRollbackFailed
		result.receipt = cloneCutoverReceiptValue(result.receipt)
		result.applied = nil
		return result, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	if result.applied == nil {
		return recoverCutoverServiceBeforeMutationWithResult(ctx, manager, oldRuntimeSHA256, result, errorCode(cause, "apply"), cause)
	}
	return handleCutoverServiceAppliedFailure(ctx, manager, oldRuntimeSHA256, result, "apply", cause)
}

func handleCutoverServiceAppliedFailure(ctx context.Context, manager cutoverServiceManager, oldRuntimeSHA256 string, result cutoverServiceResult, code string, cause error) (cutoverServiceResult, error) {
	recoveryContext := context.WithoutCancel(ctx)
	if !forceCutoverServiceStopped(recoveryContext, manager) {
		result.status = cutoverServiceRollbackFailed
		result.receipt = cutoverRollbackFailedReceipt(result.applied)
		// Keep the applied authorization only when the second stop proof was
		// not established; a later owner may still recover it safely.
		return result, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	if result.applied == nil {
		result.status = cutoverServiceRollbackFailed
		result.receipt = cloneCutoverReceiptValue(result.receipt)
		return result, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	rolledBack, err := cutoverServiceRollback(recoveryContext, result.applied)
	if err != nil || rolledBack.Status != CutoverStatusRolledBack {
		result.status = cutoverServiceRollbackFailed
		result.receipt = cutoverRollbackFailedReceipt(result.applied)
		result.applied = nil
		return result, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	running, ok := restoreCutoverServiceEvidence(recoveryContext, manager, oldRuntimeSHA256)
	if !ok {
		result.status = cutoverServiceRollbackFailed
		result.receipt = cutoverRollbackFailedReceipt(result.applied)
		result.applied = nil
		return result, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	result.status = cutoverServiceRolledBack
	result.receipt = cloneCutoverReceiptValue(rolledBack)
	result.applied = nil
	result.finalRunning = running
	return result, cutoverServiceFailure(CutoverStatusRolledBack, errors.New(code))
}

func cutoverServiceStatusForReceipt(receipt CutoverReceipt) cutoverServiceStatus {
	switch receipt.Status {
	case CutoverStatusApplied:
		return cutoverServiceApplied
	case CutoverStatusBlocked:
		return cutoverServiceBlocked
	case CutoverStatusRolledBack:
		return cutoverServiceRolledBack
	case CutoverStatusRollbackFailed:
		return cutoverServiceRollbackFailed
	default:
		return ""
	}
}

func cutoverServiceError(code string) error {
	if code == "" || !validErrorCode(code) {
		code = "service_cutover"
	}
	return newCodedError(code, "service-managed DCI cutover failed")
}

func cutoverServiceFailure(code string, cause error) error {
	if code == "" || !validErrorCode(code) {
		code = "service_cutover"
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return newCodedError(code, "service-managed DCI cutover failed: %w", cause)
	}
	return cutoverServiceError(code)
}
