package dcimigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const serviceCutoverReceiptTempPattern = ".rencrow-identity-step03-service-cutover-receipt-*.tmp"

// A service receipt is deliberately much smaller than a build or D2c receipt.
// Its only free-form value is a bounded machine error code.
const maxServiceCutoverReceiptBytes = int64(64 << 10)

type serviceCutoverReceiptWriteResult struct {
	temporary *cutoverBoundFile
	final     *cutoverBoundFile
}

type serviceCutoverReceiptWriteError struct{}

func (err *serviceCutoverReceiptWriteError) Error() string {
	return "service cutover receipt write failed"
}

var serviceCutoverReceiptWriter = writeServiceCutoverReceipt
var serviceCutoverReceiptSyncFile = func(file *os.File) error { return file.Sync() }
var serviceCutoverReceiptSyncDirectory = syncDirectory
var serviceCutoverReceiptPublish = os.Link
var serviceCutoverReceiptRemove = os.Remove

type serviceCutoverReceiptOperationResult struct {
	receipt ServiceCutoverReceipt
	applied *cutoverAppliedState
}

type preparedServiceCutoverReceipt struct {
	servicePath string
	cutoverPath string
	seed        ServiceCutoverReceipt
}

// executeServiceCutoverWithReceipt is the bounded D2d-2b facade.  It keeps
// the lifecycle owner in executeServiceCutover and only publishes a service
// subreceipt after that owner has returned a terminal, bounded result.
func executeServiceCutoverWithReceipt(ctx context.Context, options cutoverServiceOptions) (ServiceCutoverReceipt, error) {
	result, err := executeServiceCutoverReceiptOperation(ctx, options)
	return result.receipt, err
}

// executeServiceCutoverReceiptOperation retains the private applied state on
// success for the later D2d owner.  It never exposes that state through the
// public receipt.
func executeServiceCutoverReceiptOperation(ctx context.Context, options cutoverServiceOptions) (serviceCutoverReceiptOperationResult, error) {
	if ctx == nil {
		return serviceCutoverReceiptOperationResult{}, cutoverServiceError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return serviceCutoverReceiptOperationResult{}, err
	}
	prepared, err := prepareServiceCutoverReceipt(ctx, options)
	if err != nil {
		return serviceCutoverReceiptOperationResult{}, serviceCutoverError(errorCode(err, "receipt_prepare"))
	}

	serviceResult, executeErr := executeServiceCutover(ctx, options)
	if serviceResult.status == "" {
		// No lifecycle terminal state was reached (normally cancellation or a
		// precondition failure before the service owner was called).  No receipt
		// is authorized for an unobserved state.
		if executeErr == nil {
			executeErr = serviceCutoverError("service_cutover")
		}
		return serviceCutoverReceiptOperationResult{}, boundedServiceCutoverError(executeErr)
	}

	subreceiptSHA, subreceiptStatus, subreceiptErr := serviceCutoverSubreceiptBinding(prepared)
	if serviceResult.status == cutoverServiceApplied && subreceiptErr != nil {
		// The file-swap owner reported success but its durable subreceipt cannot
		// be re-read.  Treat this as a post-apply failure and use the same
		// detached recovery path as a readiness failure.
		serviceResult, executeErr = handleCutoverServiceAppliedFailure(
			context.WithoutCancel(ctx), options.manager, options.build.ExpectedInstalledRuntimeSHA256,
			serviceResult, "cutover_receipt", subreceiptErr,
		)
		subreceiptSHA, subreceiptStatus = "", ""
	}
	if serviceResult.status != cutoverServiceApplied && subreceiptErr != nil {
		serviceResult.status = cutoverServiceRollbackFailed
		serviceResult.receipt = CutoverReceipt{Status: CutoverStatusRollbackFailed}
		executeErr = serviceCutoverError(CutoverStatusRollbackFailed)
		subreceiptSHA, subreceiptStatus = "", ""
	}

	seed := prepared.seed
	serviceReceipt := serviceCutoverReceiptFromResult(seed, serviceResult, subreceiptSHA, subreceiptStatus, errorCode(executeErr, "service_cutover"))
	if serviceResult.status != cutoverServiceApplied {
		if executeErr == nil {
			executeErr = serviceCutoverError(string(serviceResult.status))
		}
		written, writeErr := serviceCutoverReceiptWriter(prepared.servicePath, serviceReceipt)
		if writeErr != nil {
			_ = cleanupServiceCutoverReceiptResult(prepared.servicePath, serviceReceipt, written)
			return serviceCutoverReceiptOperationResult{receipt: serviceReceipt}, boundedServiceCutoverError(writeErr)
		}
		return serviceCutoverReceiptOperationResult{receipt: serviceReceipt}, boundedServiceCutoverError(executeErr)
	}

	// The D2c receipt must bind exactly to the build input prepared before the
	// service was stopped.  A seam or tampered result is not an alternate
	// source of truth; recover the applied cohort instead.
	if !serviceCutoverD2CBindingMatches(prepared.seed, serviceResult.receipt) {
		serviceResult, executeErr = handleCutoverServiceAppliedFailure(
			context.WithoutCancel(ctx), options.manager, options.build.ExpectedInstalledRuntimeSHA256,
			serviceResult, "cutover_binding", errors.New("cutover binding is invalid"),
		)
		serviceReceipt = serviceCutoverReceiptFromResult(seed, serviceResult, subreceiptSHA, subreceiptStatus, errorCode(executeErr, "cutover_binding"))
		return publishRecoveredServiceCutover(ctx, prepared, serviceReceipt, serviceResult, executeErr)
	}

	written, writeErr := serviceCutoverReceiptWriter(prepared.servicePath, serviceReceipt)
	if writeErr == nil && written.final != nil && (ctx.Err() == nil) {
		return serviceCutoverReceiptOperationResult{receipt: serviceReceipt, applied: serviceResult.applied}, nil
	}

	// Publication, readback, cleanup, or caller cancellation after publication
	// is a post-apply failure.  Cleanup is attempted only against the writer's
	// exact private binding; an unknown final file is never removed.
	cleanupErr := cleanupServiceCutoverReceiptResult(prepared.servicePath, serviceReceipt, written)
	serviceResult, recoveryErr := handleCutoverServiceAppliedFailure(
		context.WithoutCancel(ctx), options.manager, options.build.ExpectedInstalledRuntimeSHA256,
		serviceResult, "service_receipt", firstServiceCutoverError(writeErr, cleanupErr),
	)
	terminalCode := "service_receipt"
	if serviceResult.status == cutoverServiceRollbackFailed || cleanupErr != nil {
		serviceResult.status = cutoverServiceRollbackFailed
		terminalCode = CutoverStatusRollbackFailed
	}
	terminal := serviceCutoverReceiptFromResult(seed, serviceResult, subreceiptSHA, subreceiptStatus, terminalCode)
	if serviceResult.status == cutoverServiceRolledBack && cleanupErr == nil && recoveryErr != nil {
		terminal.ErrorCode = "service_receipt"
	}
	if cleanupErr != nil && serviceResult.status == cutoverServiceRolledBack {
		markServiceReceiptRollbackFailed(&terminal)
	}
	if terminal.Status == CutoverStatusRollbackFailed {
		markServiceReceiptRollbackFailed(&terminal)
	}
	terminalWritten, terminalWriteErr := serviceCutoverReceiptWriter(prepared.servicePath, terminal)
	if terminalWriteErr != nil {
		_ = cleanupServiceCutoverReceiptResult(prepared.servicePath, terminal, terminalWritten)
		markServiceReceiptRollbackFailed(&terminal)
		return serviceCutoverReceiptOperationResult{receipt: terminal}, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	if terminal.Status == CutoverStatusRollbackFailed {
		return serviceCutoverReceiptOperationResult{receipt: terminal}, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	return serviceCutoverReceiptOperationResult{receipt: terminal}, cutoverServiceFailure(CutoverStatusRolledBack, errors.New("service receipt publication failed"))
}

// publishRecoveredServiceCutover publishes the terminal receipt after a
// post-apply binding failure.  The path is still fresh unless an unknown file
// was introduced, in which case the writer fails closed.
func publishRecoveredServiceCutover(ctx context.Context, prepared preparedServiceCutoverReceipt, receipt ServiceCutoverReceipt, result cutoverServiceResult, cause error) (serviceCutoverReceiptOperationResult, error) {
	if result.status == cutoverServiceRollbackFailed {
		receipt.Status = CutoverStatusRollbackFailed
		receipt.ErrorCode = CutoverStatusRollbackFailed
		receipt.FinalRunning = ServiceCutoverRunningEvidence{}
	}
	written, err := serviceCutoverReceiptWriter(prepared.servicePath, receipt)
	if err != nil {
		_ = cleanupServiceCutoverReceiptResult(prepared.servicePath, receipt, written)
		markServiceReceiptRollbackFailed(&receipt)
		return serviceCutoverReceiptOperationResult{receipt: receipt}, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	if ctx != nil && ctx.Err() != nil {
		return serviceCutoverReceiptOperationResult{receipt: receipt}, cutoverServiceError(CutoverStatusRollbackFailed)
	}
	if cause == nil {
		cause = errors.New("service cutover failed")
	}
	return serviceCutoverReceiptOperationResult{receipt: receipt}, cutoverServiceFailure(receipt.Status, cause)
}

func prepareServiceCutoverReceipt(ctx context.Context, options cutoverServiceOptions) (preparedServiceCutoverReceipt, error) {
	if err := validateCutoverServiceOptions(options); err != nil {
		return preparedServiceCutoverReceipt{}, err
	}
	if strings.TrimSpace(options.ServiceReceipt) == "" {
		return preparedServiceCutoverReceipt{}, errors.New("service receipt path is required")
	}
	paths, err := resolveCutoverArtifactPaths(options.build)
	if err != nil {
		return preparedServiceCutoverReceipt{}, err
	}
	servicePath, err := resolveCutoverFreshPath(options.ServiceReceipt)
	if err != nil {
		return preparedServiceCutoverReceipt{}, err
	}
	active, err := resolveCutoverActivePaths(options.active)
	if err != nil {
		return preparedServiceCutoverReceipt{}, err
	}
	if err := validateServiceCutoverReceiptAliases(paths, active, servicePath); err != nil {
		return preparedServiceCutoverReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return preparedServiceCutoverReceipt{}, err
	}
	build, buildSHA, binding, err := readCutoverBuildReceipt(paths.buildReceipt)
	if err != nil || buildSHA != options.build.ExpectedBuildReceiptSHA256 || !samePath(binding.path, paths.buildReceipt) {
		return preparedServiceCutoverReceipt{}, errors.New("build receipt binding is invalid")
	}
	seed := newServiceCutoverReceiptSeed(build, buildSHA, options.build.ExpectedInstalledRuntimeSHA256, options.build.ExpectedStagedRuntimeSHA256, options.initialServiceStopped)
	if err := validateServiceCutoverReceipt(seed); err != nil {
		return preparedServiceCutoverReceipt{}, err
	}
	return preparedServiceCutoverReceipt{servicePath: servicePath, cutoverPath: paths.cutoverReceipt, seed: seed}, nil
}

func validateServiceCutoverReceiptAliases(paths cutoverArtifactPaths, active cutoverActivePaths, servicePath string) error {
	values := []string{
		paths.buildRoot, paths.buildReceipt, paths.installedRuntime, paths.stagedRuntime,
		paths.rollbackDir, paths.cutoverReceipt, active.dci, active.dciJSONL,
		active.eventStore, active.l1, active.archive, servicePath,
	}
	for _, output := range buildOutputTargets(paths.buildRoot) {
		values = append(values, output.path)
	}
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if samePath(values[left], values[right]) || pathWithinOrRoot(values[left], values[right]) || pathWithinOrRoot(values[right], values[left]) {
				if samePath(values[left], servicePath) || samePath(values[right], servicePath) {
					return errors.New("service receipt path aliases cutover artifacts")
				}
			}
		}
	}
	return nil
}

func newServiceCutoverReceiptSeed(build BuildReceipt, buildSHA, oldSHA, newSHA string, initialServiceStopped bool) ServiceCutoverReceipt {
	now := time.Now().UTC()
	receipt := ServiceCutoverReceipt{
		SchemaVersion: ServiceCutoverSchemaVersion, Mode: ModeCutover, Status: CutoverStatusBlocked,
		StartedAt: now, CompletedAt: now,
		BuildReceiptSHA256: buildSHA, CaptureReceiptSHA256: build.CaptureReceiptSHA256,
		DryRunManifestSHA256: build.DryRunManifestSHA256, CaptureArtifactSetSHA256: build.CaptureArtifactSetSHA256,
		OldRuntimeSHA256: oldSHA, NewRuntimeSHA256: newSHA,
		InitialState: ServiceCutoverInitialRunning, ErrorCode: "service_cutover",
	}
	if initialServiceStopped {
		receipt.InitialState = ServiceCutoverInitialMaintenanceStopped
	}
	return receipt
}

func serviceCutoverReceiptFromResult(seed ServiceCutoverReceipt, result cutoverServiceResult, subreceiptSHA, subreceiptStatus, code string) ServiceCutoverReceipt {
	receipt := seed
	receipt.Status = string(result.status)
	receipt.CompletedAt = time.Now().UTC()
	receipt.CutoverSubreceiptSHA256 = subreceiptSHA
	receipt.CutoverSubreceiptStatus = subreceiptStatus
	receipt.CutoverTerminalStatus = string(result.receipt.Status)
	receipt.InitialRunning = serviceRunningProjection(result.initialRunning, receipt.OldRuntimeSHA256)
	receipt.InitialMaintenanceStopped = serviceMaintenanceStoppedProjection(result.initialMaintenanceStopped, receipt.OldRuntimeSHA256)
	receipt.StoppedBeforePrepare = serviceStoppedProjection(result.stoppedBeforePrepare)
	receipt.StoppedBeforeApply = serviceStoppedProjection(result.stoppedBeforeApply)
	finalExpected := receipt.OldRuntimeSHA256
	if result.status == cutoverServiceApplied {
		finalExpected = receipt.NewRuntimeSHA256
	}
	receipt.FinalRunning = serviceRunningProjection(result.finalRunning, finalExpected)
	if receipt.Status == CutoverStatusApplied {
		receipt.ErrorCode = ""
	} else if receipt.Status == CutoverStatusRollbackFailed {
		receipt.ErrorCode = CutoverStatusRollbackFailed
	} else {
		if !validErrorCode(code) {
			code = "service_cutover"
		}
		receipt.ErrorCode = code
	}
	return receipt
}

func serviceMaintenanceStoppedProjection(evidence cutoverServiceMaintenanceStoppedEvidence, expectedSHA256 string) ServiceCutoverMaintenanceStoppedEvidence {
	if !evidence.valid(expectedSHA256) {
		return ServiceCutoverMaintenanceStoppedEvidence{}
	}
	return ServiceCutoverMaintenanceStoppedEvidence{
		Owner: evidence.Owner, Enabled: evidence.Enabled, Unmasked: evidence.Unmasked,
		Active: evidence.Active, MainPIDZero: evidence.MainPIDZero,
		ListenerZero: evidence.ListenerZero, RuntimeSHA256: evidence.RuntimeSHA256,
	}
}

func serviceRunningProjection(evidence cutoverServiceRunningEvidence, expectedSHA256 string) ServiceCutoverRunningEvidence {
	if !evidence.valid(expectedSHA256) {
		return ServiceCutoverRunningEvidence{}
	}
	return ServiceCutoverRunningEvidence{
		Owner: evidence.Owner, Enabled: evidence.Enabled, Unmasked: evidence.Unmasked,
		Active: evidence.Active, MainPIDPositive: evidence.MainPIDPositive,
		ListenerOwned: evidence.ListenerOwned, Readiness: evidence.Readiness,
		RuntimeSHA256: evidence.RuntimeSHA256,
	}
}

func serviceStoppedProjection(evidence cutoverServiceStoppedEvidence) ServiceCutoverStoppedEvidence {
	if !evidence.valid() {
		return ServiceCutoverStoppedEvidence{}
	}
	return ServiceCutoverStoppedEvidence{Masked: evidence.Masked, Active: evidence.Active, MainPIDZero: evidence.MainPIDZero, ListenerZero: evidence.ListenerZero}
}

func serviceCutoverD2CBindingMatches(seed ServiceCutoverReceipt, receipt CutoverReceipt) bool {
	return receipt.Status == CutoverStatusApplied &&
		receipt.BuildReceiptSHA256 == seed.BuildReceiptSHA256 &&
		receipt.CaptureReceiptSHA256 == seed.CaptureReceiptSHA256 &&
		receipt.DryRunManifestSHA256 == seed.DryRunManifestSHA256 &&
		receipt.CaptureArtifactSetSHA256 == seed.CaptureArtifactSetSHA256
}

func serviceCutoverSubreceiptBinding(prepared preparedServiceCutoverReceipt) (string, string, error) {
	// The D2c path was bound before the lifecycle began and is never copied
	// into the public receipt.  It remains readable after D2c rollback because
	// that immutable applied subreceipt is historical evidence.
	path := prepared.cutoverPath
	if path == "" {
		return "", "", errors.New("cutover subreceipt path is invalid")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	} else if err != nil {
		return "", "", errors.New("cutover subreceipt is unavailable")
	}
	data, err := readBuildInputBytes(path, maxCutoverReceiptBytes)
	if err != nil {
		return "", "", err
	}
	receipt, err := readCutoverReceipt(path)
	if err != nil || receipt.Status != CutoverStatusApplied {
		return "", "", errors.New("cutover subreceipt is invalid")
	}
	if !serviceCutoverD2CBindingMatches(prepared.seed, receipt) {
		return "", "", errors.New("cutover subreceipt binding is invalid")
	}
	binding, err := bindCutoverFile(path, true, false)
	if err != nil || binding.sha256 != buildInputBytesSHA256(data) {
		return "", "", errors.New("cutover subreceipt file changed")
	}
	return binding.sha256, CutoverStatusApplied, nil
}

func validateServiceCutoverReceipt(receipt ServiceCutoverReceipt) error {
	if receipt.SchemaVersion != ServiceCutoverSchemaVersion || receipt.Mode != ModeCutover {
		return errors.New("service receipt schema is invalid")
	}
	if receipt.Status != CutoverStatusApplied && receipt.Status != CutoverStatusBlocked && receipt.Status != CutoverStatusRolledBack && receipt.Status != CutoverStatusRollbackFailed {
		return errors.New("service receipt status is invalid")
	}
	if receipt.StartedAt.IsZero() || receipt.CompletedAt.IsZero() || receipt.StartedAt.Location() != time.UTC || receipt.CompletedAt.Location() != time.UTC || receipt.CompletedAt.Before(receipt.StartedAt) {
		return errors.New("service receipt timestamps are invalid")
	}
	for name, value := range map[string]string{
		"cutover_subreceipt_sha256":   receipt.CutoverSubreceiptSHA256,
		"build_receipt_sha256":        receipt.BuildReceiptSHA256,
		"capture_receipt_sha256":      receipt.CaptureReceiptSHA256,
		"dry_run_manifest_sha256":     receipt.DryRunManifestSHA256,
		"capture_artifact_set_sha256": receipt.CaptureArtifactSetSHA256,
		"old_runtime_sha256":          receipt.OldRuntimeSHA256,
		"new_runtime_sha256":          receipt.NewRuntimeSHA256,
	} {
		if value != "" && !isLowerHexSHA256(value) {
			return errors.New("service receipt hash is invalid")
		}
		_ = name
	}
	if receipt.CutoverSubreceiptSHA256 == "" {
		if receipt.CutoverSubreceiptStatus != "" {
			return errors.New("service subreceipt status is invalid")
		}
	} else if receipt.CutoverSubreceiptStatus != CutoverStatusApplied {
		return errors.New("service subreceipt status is invalid")
	}
	if receipt.CutoverTerminalStatus != "" && receipt.CutoverTerminalStatus != CutoverStatusApplied && receipt.CutoverTerminalStatus != CutoverStatusBlocked && receipt.CutoverTerminalStatus != CutoverStatusRolledBack && receipt.CutoverTerminalStatus != CutoverStatusRollbackFailed {
		return errors.New("service terminal status is invalid")
	}
	if err := validateServiceRunningProjection(receipt.InitialRunning); err != nil {
		return err
	}
	if err := validateServiceRunningProjection(receipt.FinalRunning); err != nil {
		return err
	}
	if err := validateServiceMaintenanceStoppedProjection(receipt.InitialMaintenanceStopped); err != nil {
		return err
	}
	if err := validateServiceStoppedProjection(receipt.StoppedBeforePrepare); err != nil {
		return err
	}
	if err := validateServiceStoppedProjection(receipt.StoppedBeforeApply); err != nil {
		return err
	}
	if receipt.ErrorCode != "" && !validErrorCode(receipt.ErrorCode) {
		return errors.New("service receipt error code is invalid")
	}
	if !serviceInitialEvidenceCoherent(receipt, false) {
		return errors.New("service initial evidence is invalid")
	}
	switch receipt.Status {
	case CutoverStatusApplied:
		if receipt.ErrorCode != "" || receipt.CutoverTerminalStatus != CutoverStatusApplied || receipt.CutoverSubreceiptSHA256 == "" || receipt.CutoverSubreceiptStatus != CutoverStatusApplied || !serviceReceiptHashesComplete(receipt) || !serviceInitialEvidenceCoherent(receipt, true) || !serviceStoppedProjectionValid(receipt.StoppedBeforePrepare) || !serviceStoppedProjectionValid(receipt.StoppedBeforeApply) || !serviceRunningProjectionMatches(receipt.FinalRunning, receipt.NewRuntimeSHA256) {
			return errors.New("service applied claims are incomplete")
		}
	case CutoverStatusBlocked:
		if receipt.ErrorCode == "" || receipt.CutoverTerminalStatus == CutoverStatusApplied || receipt.CutoverSubreceiptSHA256 != "" {
			return errors.New("service blocked claims are invalid")
		}
		if !serviceStoppedProjectionZero(receipt.StoppedBeforePrepare) && !serviceRunningProjectionMatches(receipt.FinalRunning, receipt.OldRuntimeSHA256) {
			return errors.New("service blocked restoration is unproven")
		}
		if !serviceStoppedProjectionZero(receipt.StoppedBeforeApply) && !serviceRunningProjectionMatches(receipt.FinalRunning, receipt.OldRuntimeSHA256) {
			return errors.New("service blocked restoration is unproven")
		}
	case CutoverStatusRolledBack:
		if receipt.ErrorCode == "" || receipt.CutoverTerminalStatus != CutoverStatusRolledBack || !serviceReceiptHashesComplete(receipt) || !serviceInitialEvidenceCoherent(receipt, true) || !serviceStoppedProjectionValid(receipt.StoppedBeforePrepare) || !serviceStoppedProjectionValid(receipt.StoppedBeforeApply) || !serviceRunningProjectionMatches(receipt.FinalRunning, receipt.OldRuntimeSHA256) {
			return errors.New("service rolled-back claims are incomplete")
		}
	case CutoverStatusRollbackFailed:
		if receipt.ErrorCode != CutoverStatusRollbackFailed || receipt.CutoverTerminalStatus == CutoverStatusRolledBack {
			return errors.New("service rollback-failed claims are invalid")
		}
	}
	return nil
}

func serviceReceiptHashesComplete(receipt ServiceCutoverReceipt) bool {
	return isLowerHexSHA256(receipt.BuildReceiptSHA256) && isLowerHexSHA256(receipt.CaptureReceiptSHA256) && isLowerHexSHA256(receipt.DryRunManifestSHA256) && isLowerHexSHA256(receipt.CaptureArtifactSetSHA256) && isLowerHexSHA256(receipt.OldRuntimeSHA256) && isLowerHexSHA256(receipt.NewRuntimeSHA256)
}

func validateServiceRunningProjection(evidence ServiceCutoverRunningEvidence) error {
	fields := []int{evidence.Owner, evidence.Enabled, evidence.Unmasked, evidence.Active, evidence.MainPIDPositive, evidence.ListenerOwned, evidence.Readiness}
	allZero := evidence.RuntimeSHA256 == ""
	for _, value := range fields {
		if value != 0 && value != 1 {
			return errors.New("service running evidence is invalid")
		}
		if value != 0 {
			allZero = false
		}
	}
	if evidence.RuntimeSHA256 != "" && !isLowerHexSHA256(evidence.RuntimeSHA256) {
		return errors.New("service running hash is invalid")
	}
	if !allZero && evidence.RuntimeSHA256 == "" {
		return errors.New("service running evidence is incomplete")
	}
	return nil
}

func validateServiceMaintenanceStoppedProjection(evidence ServiceCutoverMaintenanceStoppedEvidence) error {
	fields := []int{evidence.Owner, evidence.Enabled, evidence.Unmasked, evidence.Active, evidence.MainPIDZero, evidence.ListenerZero}
	allZero := evidence.RuntimeSHA256 == ""
	for _, value := range fields {
		if value != 0 && value != 1 {
			return errors.New("service maintenance-stopped evidence is invalid")
		}
		if value != 0 {
			allZero = false
		}
	}
	if evidence.RuntimeSHA256 != "" && !isLowerHexSHA256(evidence.RuntimeSHA256) {
		return errors.New("service maintenance-stopped hash is invalid")
	}
	if !allZero && evidence.RuntimeSHA256 == "" {
		return errors.New("service maintenance-stopped evidence is incomplete")
	}
	return nil
}

func serviceInitialEvidenceCoherent(receipt ServiceCutoverReceipt, required bool) bool {
	switch receipt.InitialState {
	case ServiceCutoverInitialRunning:
		if receipt.InitialMaintenanceStopped != (ServiceCutoverMaintenanceStoppedEvidence{}) {
			return false
		}
		return serviceRunningProjectionMatches(receipt.InitialRunning, receipt.OldRuntimeSHA256) || (!required && receipt.InitialRunning == (ServiceCutoverRunningEvidence{}))
	case ServiceCutoverInitialMaintenanceStopped:
		if receipt.InitialRunning != (ServiceCutoverRunningEvidence{}) {
			return false
		}
		return serviceMaintenanceStoppedProjectionMatches(receipt.InitialMaintenanceStopped, receipt.OldRuntimeSHA256) || (!required && receipt.InitialMaintenanceStopped == (ServiceCutoverMaintenanceStoppedEvidence{}))
	default:
		return false
	}
}

func serviceMaintenanceStoppedProjectionMatches(evidence ServiceCutoverMaintenanceStoppedEvidence, expected string) bool {
	return expected != "" && evidence.Owner == 1 && evidence.Enabled == 1 && evidence.Unmasked == 1 && evidence.Active == 0 && evidence.MainPIDZero == 1 && evidence.ListenerZero == 1 && evidence.RuntimeSHA256 == expected
}

func validateServiceStoppedProjection(evidence ServiceCutoverStoppedEvidence) error {
	for _, value := range []int{evidence.Masked, evidence.Active, evidence.MainPIDZero, evidence.ListenerZero} {
		if value != 0 && value != 1 {
			return errors.New("service stopped evidence is invalid")
		}
	}
	return nil
}

func serviceRunningProjectionMatches(evidence ServiceCutoverRunningEvidence, expected string) bool {
	return expected != "" && evidence.Owner == 1 && evidence.Enabled == 1 && evidence.Unmasked == 1 && evidence.Active == 1 && evidence.MainPIDPositive == 1 && evidence.ListenerOwned == 1 && evidence.Readiness == 1 && evidence.RuntimeSHA256 == expected
}

func serviceStoppedProjectionValid(evidence ServiceCutoverStoppedEvidence) bool {
	return evidence.Masked == 1 && evidence.Active == 0 && evidence.MainPIDZero == 1 && evidence.ListenerZero == 1
}

func serviceStoppedProjectionZero(evidence ServiceCutoverStoppedEvidence) bool {
	return evidence == (ServiceCutoverStoppedEvidence{})
}

func markServiceReceiptRollbackFailed(receipt *ServiceCutoverReceipt) {
	if receipt == nil {
		return
	}
	receipt.Status = CutoverStatusRollbackFailed
	receipt.CutoverTerminalStatus = CutoverStatusRollbackFailed
	receipt.ErrorCode = CutoverStatusRollbackFailed
	receipt.FinalRunning = ServiceCutoverRunningEvidence{}
}

func readServiceCutoverReceipt(path string) (ServiceCutoverReceipt, error) {
	absolute, err := absolutePath(path)
	if err != nil {
		return ServiceCutoverReceipt{}, serviceCutoverError("receipt_read")
	}
	resolved, err := resolveCutoverExistingPath(absolute)
	if err != nil || !samePath(resolved, absolute) {
		return ServiceCutoverReceipt{}, serviceCutoverError("receipt_read")
	}
	info, err := inspectCutoverFile(resolved, true, false)
	if err != nil || info.Size() > maxServiceCutoverReceiptBytes {
		return ServiceCutoverReceipt{}, serviceCutoverError("receipt_read")
	}
	data, err := readBuildInputBytes(resolved, maxServiceCutoverReceiptBytes)
	if err != nil || rejectDuplicateJSONKeys(data) != nil {
		return ServiceCutoverReceipt{}, serviceCutoverError("receipt_read")
	}
	var receipt ServiceCutoverReceipt
	if err := decodeOneBuildInputObject(data, &receipt); err != nil || validateServiceCutoverReceipt(receipt) != nil {
		return ServiceCutoverReceipt{}, serviceCutoverError("receipt_read")
	}
	return receipt, nil
}

func marshalServiceCutoverReceipt(receipt ServiceCutoverReceipt) ([]byte, error) {
	if err := validateServiceCutoverReceipt(receipt); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || int64(len(encoded))+1 > maxServiceCutoverReceiptBytes {
		return nil, errors.New("service receipt exceeds size bound")
	}
	return append(encoded, '\n'), nil
}

func writeServiceCutoverReceipt(path string, receipt ServiceCutoverReceipt) (serviceCutoverReceiptWriteResult, error) {
	encoded, err := marshalServiceCutoverReceipt(receipt)
	if err != nil {
		return serviceCutoverReceiptWriteResult{}, &serviceCutoverReceiptWriteError{}
	}
	absolute, err := absolutePath(path)
	if err != nil {
		return serviceCutoverReceiptWriteResult{}, &serviceCutoverReceiptWriteError{}
	}
	if _, err := resolveCutoverFreshPath(absolute); err != nil {
		return serviceCutoverReceiptWriteResult{}, &serviceCutoverReceiptWriteError{}
	}
	parent := filepath.Dir(absolute)
	file, err := os.CreateTemp(parent, serviceCutoverReceiptTempPattern)
	if err != nil {
		return serviceCutoverReceiptWriteResult{}, &serviceCutoverReceiptWriteError{}
	}
	temporaryName := file.Name()
	initialInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return serviceCutoverReceiptWriteResult{}, &serviceCutoverReceiptWriteError{}
	}
	initialBinding := cutoverBoundFile{path: temporaryName, info: initialInfo, require0600: true}
	closeFile := true
	cleanupInitial := func(result serviceCutoverReceiptWriteResult) (serviceCutoverReceiptWriteResult, error) {
		if closeFile {
			_ = file.Close()
			closeFile = false
		}
		if result.temporary == nil {
			if current, bindErr := bindCutoverFile(temporaryName, true, false); bindErr == nil &&
				os.SameFile(current.info, initialInfo) && cutoverKnownFileIsUnaliased(temporaryName, current.info) {
				// Bind the bytes that actually remain in the original inode.  An
				// early write/sync failure may leave a partial file, so the final
				// receipt hash is not an ownership proof for this cleanup path.
				result.temporary = &current
			} else {
				// Keep the original inode proof when rebinding fails; the cleanup
				// path will fail closed and preserve any substituted entry.
				result.temporary = &initialBinding
			}
		}
		_ = cleanupServiceCutoverReceiptResult(absolute, receipt, result)
		return result, &serviceCutoverReceiptWriteError{}
	}
	if err := file.Chmod(0o600); err != nil {
		return cleanupInitial(serviceCutoverReceiptWriteResult{})
	}
	if written, err := file.Write(encoded); err != nil || written != len(encoded) {
		return cleanupInitial(serviceCutoverReceiptWriteResult{})
	}
	if err := serviceCutoverReceiptSyncFile(file); err != nil {
		return cleanupInitial(serviceCutoverReceiptWriteResult{})
	}
	if err := file.Close(); err != nil {
		closeFile = false
		return cleanupInitial(serviceCutoverReceiptWriteResult{})
	}
	closeFile = false
	temporaryBinding, err := bindCutoverFile(temporaryName, true, false)
	if err != nil {
		return cleanupInitial(serviceCutoverReceiptWriteResult{})
	}
	result := serviceCutoverReceiptWriteResult{temporary: &temporaryBinding}
	if err := serviceCutoverReceiptPublish(temporaryName, absolute); err != nil {
		_, finalErr := os.Lstat(absolute)
		if errors.Is(finalErr, os.ErrNotExist) {
			return cleanupInitial(result)
		}
		if finalErr != nil {
			_ = cleanupServiceCutoverReceiptTemporary(result, absolute, false)
			return result, &serviceCutoverReceiptWriteError{}
		}
		final, bindErr := bindCutoverFile(absolute, true, false)
		if bindErr != nil || !os.SameFile(final.info, temporaryBinding.info) {
			// The final name is unknown or a racing substitution.  Preserve it,
			// but remove only the owned temporary inode when it is still safe.
			_ = cleanupServiceCutoverReceiptTemporary(result, absolute, false)
			return serviceCutoverReceiptWriteResult{temporary: result.temporary}, &serviceCutoverReceiptWriteError{}
		}
		result.final = &final
		_ = cleanupServiceCutoverReceiptResult(absolute, receipt, result)
		return result, &serviceCutoverReceiptWriteError{}
	}
	final, err := bindCutoverFile(absolute, true, false)
	if err != nil || !os.SameFile(final.info, temporaryBinding.info) {
		_ = cleanupServiceCutoverReceiptResult(absolute, receipt, result)
		return result, &serviceCutoverReceiptWriteError{}
	}
	result.final = &final
	if err := serviceCutoverReceiptSyncDirectory(parent); err != nil {
		_ = cleanupServiceCutoverReceiptResult(absolute, receipt, result)
		return result, &serviceCutoverReceiptWriteError{}
	}
	if err := cleanupServiceCutoverReceiptTemporary(result, absolute, true); err != nil {
		_ = cleanupServiceCutoverReceiptResult(absolute, receipt, result)
		return result, &serviceCutoverReceiptWriteError{}
	}
	if err := serviceCutoverReceiptSyncDirectory(parent); err != nil {
		_ = cleanupServiceCutoverReceiptResult(absolute, receipt, result)
		return result, &serviceCutoverReceiptWriteError{}
	}
	actual, err := readServiceCutoverReceipt(absolute)
	if err != nil || !reflect.DeepEqual(actual, receipt) {
		_ = cleanupServiceCutoverReceiptResult(absolute, receipt, result)
		return result, &serviceCutoverReceiptWriteError{}
	}
	final, err = bindCutoverFile(absolute, true, false)
	if err != nil || !cutoverKnownFileIsUnaliased(absolute, final.info) || !os.SameFile(final.info, temporaryBinding.info) {
		_ = cleanupServiceCutoverReceiptResult(absolute, receipt, result)
		return result, &serviceCutoverReceiptWriteError{}
	}
	result.final = &final
	return result, nil
}

func cleanupServiceCutoverReceiptTemporary(result serviceCutoverReceiptWriteResult, final string, published bool) error {
	if result.temporary == nil {
		return nil
	}
	path := result.temporary.path
	current, err := bindCutoverFile(path, true, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(current.info, result.temporary.info) || current.sha256 != result.temporary.sha256 || current.bytes != result.temporary.bytes {
		return errors.New("service receipt temporary binding is unknown")
	}
	if published {
		info, err := os.Lstat(final)
		if err != nil || !os.SameFile(info, current.info) {
			return errors.New("service receipt temporary alias is unknown")
		}
	} else if !cutoverKnownFileIsUnaliased(path, current.info) {
		return errors.New("service receipt temporary is aliased")
	}
	if err := serviceCutoverReceiptRemove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("service receipt temporary cleanup failed")
	}
	if err := ensureCutoverAbsent(path); err != nil {
		return err
	}
	return serviceCutoverReceiptSyncDirectory(filepath.Dir(path))
}

func cleanupServiceCutoverReceiptResult(path string, receipt ServiceCutoverReceipt, result serviceCutoverReceiptWriteResult) error {
	if result.temporary != nil {
		if _, err := os.Lstat(result.temporary.path); err == nil {
			published := result.final != nil
			if err := cleanupServiceCutoverReceiptTemporary(result, path, published); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("service receipt temporary cleanup is unproven")
		}
	}
	if result.final == nil {
		if _, err := os.Lstat(path); err == nil {
			return errors.New("service receipt final binding is unknown")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("service receipt final cleanup is unproven")
		}
		return nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !os.SameFile(info, result.final.info) || !cutoverKnownFileIsUnaliased(path, info) {
		return errors.New("service receipt final binding is unknown")
	}
	data, err := marshalServiceCutoverReceipt(receipt)
	if err != nil {
		return errors.New("service receipt cleanup binding is invalid")
	}
	binding, err := bindCutoverFile(path, true, false)
	if err != nil || binding.sha256 != buildInputBytesSHA256(data) || binding.bytes != int64(len(data)) {
		return errors.New("service receipt final binding is unknown")
	}
	if err := serviceCutoverReceiptRemove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("service receipt cleanup failed")
	}
	if err := ensureCutoverAbsent(path); err != nil {
		return err
	}
	return serviceCutoverReceiptSyncDirectory(filepath.Dir(path))
}

func serviceCutoverError(code string) error {
	if !validErrorCode(code) {
		code = "service_cutover"
	}
	return newCodedError(code, "service cutover receipt operation failed")
}

func boundedServiceCutoverError(err error) error {
	if err == nil {
		return nil
	}
	return serviceCutoverError(errorCode(err, "service_cutover"))
}

func firstServiceCutoverError(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
