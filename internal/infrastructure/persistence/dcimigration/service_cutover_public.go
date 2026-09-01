package dcimigration

import (
	"context"
	"strings"
	"time"
)

// CutoverOptions is the public owner-operation contract for the service-managed
// DCI cutover.  Service identity, command order, readiness, and rollback
// behavior remain private to this package and its fixed platform owner.
type CutoverOptions struct {
	InitialServiceStopped          bool
	BuildRoot                      string
	BuildReceipt                   string
	ExpectedBuildReceiptSHA256     string
	InstalledRuntime               string
	StagedRuntime                  string
	ExpectedInstalledRuntimeSHA256 string
	ExpectedStagedRuntimeSHA256    string
	RollbackDir                    string
	CutoverReceipt                 string
	ServiceReceipt                 string
	ActiveDCI                      string
	ActiveDCIJSONL                 string
	ActiveEventStore               string
	ActiveL1                       string
	ActiveArchive                  string
	ActiveConfig                   string
}

// These package-local seams exist only so unit tests can exercise the public
// boundary without invoking a production service manager.  Production values
// are fixed platform owner and private lifecycle functions.
var (
	cutoverPublicManagerFactory = newPlatformCutoverServiceManager
	cutoverPublicExecutor       = executeServiceCutoverWithReceipt
)

// Cutover is the sole public entry point for the service-managed DCI cutover.
// It maps public paths and hashes exactly once to the private owner options;
// all lifecycle, file swap, rollback, and durable receipt behavior remains in
// executeServiceCutoverWithReceipt.
func Cutover(ctx context.Context, options CutoverOptions) (ServiceCutoverReceipt, error) {
	if ctx == nil {
		return blockedServiceCutoverReceipt(options, "invalid_context")
	}

	build := cutoverArtifactOptions{
		BuildRoot:                      options.BuildRoot,
		BuildReceipt:                   options.BuildReceipt,
		ExpectedBuildReceiptSHA256:     options.ExpectedBuildReceiptSHA256,
		InstalledRuntime:               options.InstalledRuntime,
		StagedRuntime:                  options.StagedRuntime,
		ExpectedInstalledRuntimeSHA256: options.ExpectedInstalledRuntimeSHA256,
		ExpectedStagedRuntimeSHA256:    options.ExpectedStagedRuntimeSHA256,
		RollbackDir:                    options.RollbackDir,
		CutoverReceipt:                 options.CutoverReceipt,
	}
	active := cutoverActiveOptions{
		SourceDCI:        options.ActiveDCI,
		SourceDCIJSONL:   options.ActiveDCIJSONL,
		SourceEventStore: options.ActiveEventStore,
		SourceL1:         options.ActiveL1,
		SourceArchive:    options.ActiveArchive,
	}

	if err := validateCutoverArtifactOptions(build); err != nil {
		return blockedServiceCutoverReceipt(options, errorCode(err, "invalid_options"))
	}
	if err := validateCutoverActiveOptions(active); err != nil {
		return blockedServiceCutoverReceipt(options, errorCode(err, "invalid_options"))
	}
	if strings.TrimSpace(options.ActiveConfig) == "" || strings.TrimSpace(options.ServiceReceipt) == "" {
		return blockedServiceCutoverReceipt(options, "invalid_options")
	}

	if cutoverPublicManagerFactory == nil {
		return blockedServiceCutoverReceipt(options, "service_manager_unavailable")
	}
	manager, err := cutoverPublicManagerFactory(options.InstalledRuntime, options.ActiveConfig)
	if err != nil || manager == nil {
		return blockedServiceCutoverReceipt(options, errorCode(err, "service_manager_unavailable"))
	}

	serviceOptions := cutoverServiceOptions{
		build:                 build,
		active:                active,
		manager:               manager,
		initialServiceStopped: options.InitialServiceStopped,
		ServiceReceipt:        options.ServiceReceipt,
	}
	if cutoverPublicExecutor == nil {
		return blockedServiceCutoverReceipt(options, "service_cutover")
	}
	receipt, executeErr := cutoverPublicExecutor(ctx, serviceOptions)
	if isTerminalServiceCutoverReceipt(receipt) {
		if executeErr != nil {
			return receipt, boundedServiceCutoverError(executeErr)
		}
		if receipt.Status != CutoverStatusApplied {
			code := receipt.ErrorCode
			if !validErrorCode(code) {
				code = string(receipt.Status)
			}
			return receipt, serviceCutoverError(code)
		}
		return receipt, nil
	}
	return blockedServiceCutoverReceipt(options, errorCode(executeErr, "service_cutover"))
}

func isTerminalServiceCutoverReceipt(receipt ServiceCutoverReceipt) bool {
	if validateServiceCutoverReceipt(receipt) != nil {
		return false
	}
	switch receipt.Status {
	case CutoverStatusApplied, CutoverStatusBlocked, CutoverStatusRolledBack, CutoverStatusRollbackFailed:
		return true
	default:
		return false
	}
}

func blockedServiceCutoverReceipt(options CutoverOptions, code string) (ServiceCutoverReceipt, error) {
	if !validErrorCode(code) {
		code = "service_cutover"
	}
	now := time.Now().UTC()
	receipt := ServiceCutoverReceipt{
		SchemaVersion: ServiceCutoverSchemaVersion,
		Mode:          ModeCutover,
		Status:        CutoverStatusBlocked,
		InitialState:  ServiceCutoverInitialRunning,
		StartedAt:     now,
		CompletedAt:   now,
		ErrorCode:     code,
	}
	if options.InitialServiceStopped {
		receipt.InitialState = ServiceCutoverInitialMaintenanceStopped
	}
	// A blocked in-memory receipt may retain only caller-provided hashes that
	// are already valid lowercase SHA-256 values.  Invalid input is omitted.
	if isLowerHexSHA256(options.ExpectedBuildReceiptSHA256) {
		receipt.BuildReceiptSHA256 = options.ExpectedBuildReceiptSHA256
	}
	if isLowerHexSHA256(options.ExpectedInstalledRuntimeSHA256) {
		receipt.OldRuntimeSHA256 = options.ExpectedInstalledRuntimeSHA256
	}
	if isLowerHexSHA256(options.ExpectedStagedRuntimeSHA256) {
		receipt.NewRuntimeSHA256 = options.ExpectedStagedRuntimeSHA256
	}
	return receipt, serviceCutoverError(code)
}
