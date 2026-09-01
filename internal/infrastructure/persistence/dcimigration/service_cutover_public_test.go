package dcimigration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func publicCutoverOptionsForTest() CutoverOptions {
	return CutoverOptions{
		BuildRoot:                      "build-root",
		BuildReceipt:                   "build-receipt",
		ExpectedBuildReceiptSHA256:     strings.Repeat("a", 64),
		InstalledRuntime:               "installed-runtime",
		StagedRuntime:                  "staged-runtime",
		ExpectedInstalledRuntimeSHA256: strings.Repeat("b", 64),
		ExpectedStagedRuntimeSHA256:    strings.Repeat("c", 64),
		RollbackDir:                    "rollback-dir",
		CutoverReceipt:                 "cutover-receipt",
		ServiceReceipt:                 "service-receipt",
		ActiveDCI:                      "active-dci",
		ActiveDCIJSONL:                 "active-dci-jsonl",
		ActiveEventStore:               "active-event-store",
		ActiveL1:                       "active-l1",
		ActiveArchive:                  "active-archive",
		ActiveConfig:                   "active-config",
	}
}

func withPublicCutoverSeams(t *testing.T, factory func(string, string) (cutoverServiceManager, error), executor func(context.Context, cutoverServiceOptions) (ServiceCutoverReceipt, error)) {
	t.Helper()
	oldFactory := cutoverPublicManagerFactory
	oldExecutor := cutoverPublicExecutor
	cutoverPublicManagerFactory = factory
	cutoverPublicExecutor = executor
	t.Cleanup(func() {
		cutoverPublicManagerFactory = oldFactory
		cutoverPublicExecutor = oldExecutor
	})
}

type publicCutoverManagerStub struct{}

func (*publicCutoverManagerStub) VerifyRunning(context.Context, string) (cutoverServiceRunningEvidence, error) {
	return cutoverServiceRunningEvidence{}, errors.New("stub manager must not run")
}

func (*publicCutoverManagerStub) MaskAndStop(context.Context) error {
	return errors.New("stub manager must not run")
}

func (*publicCutoverManagerStub) VerifyStopped(context.Context) (cutoverServiceStoppedEvidence, error) {
	return cutoverServiceStoppedEvidence{}, errors.New("stub manager must not run")
}

func publicTerminalReceiptForTest(status string) ServiceCutoverReceipt {
	receipt := validServiceCutoverReceiptForTest()
	receipt.Status = status
	switch status {
	case CutoverStatusBlocked:
		receipt.CutoverSubreceiptSHA256 = ""
		receipt.CutoverSubreceiptStatus = ""
		receipt.CutoverTerminalStatus = ""
		receipt.ErrorCode = "service_running"
		receipt.InitialRunning = ServiceCutoverRunningEvidence{}
		receipt.StoppedBeforePrepare = ServiceCutoverStoppedEvidence{}
		receipt.StoppedBeforeApply = ServiceCutoverStoppedEvidence{}
		receipt.FinalRunning = ServiceCutoverRunningEvidence{}
	case CutoverStatusRolledBack:
		receipt.CutoverTerminalStatus = CutoverStatusRolledBack
		receipt.ErrorCode = CutoverStatusRolledBack
		receipt.FinalRunning = receipt.InitialRunning
	case CutoverStatusRollbackFailed:
		receipt.CutoverTerminalStatus = CutoverStatusRollbackFailed
		receipt.ErrorCode = CutoverStatusRollbackFailed
		receipt.FinalRunning = ServiceCutoverRunningEvidence{}
	}
	return receipt
}

func (*publicCutoverManagerStub) UnmaskAndStart(context.Context) error {
	return errors.New("stub manager must not run")
}

func TestCutoverMapsOptionsAndCallsPrivateOwnerOnce(t *testing.T) {
	options := publicCutoverOptionsForTest()
	manager := &publicCutoverManagerStub{}
	wantReceipt := validServiceCutoverReceiptForTest()
	var gotManagerRuntime, gotManagerConfig string
	var gotOptions cutoverServiceOptions
	factoryCalls, executorCalls := 0, 0
	withPublicCutoverSeams(t,
		func(runtime, config string) (cutoverServiceManager, error) {
			factoryCalls++
			gotManagerRuntime, gotManagerConfig = runtime, config
			return manager, nil
		},
		func(_ context.Context, got cutoverServiceOptions) (ServiceCutoverReceipt, error) {
			executorCalls++
			gotOptions = got
			return wantReceipt, nil
		},
	)

	got, err := Cutover(context.Background(), options)
	if err != nil {
		t.Fatalf("Cutover() error = %v", err)
	}
	if !reflect.DeepEqual(got, wantReceipt) {
		t.Fatalf("Cutover() receipt = %#v, want owner receipt", got)
	}
	if factoryCalls != 1 || executorCalls != 1 {
		t.Fatalf("factory/executor calls = %d/%d, want 1/1", factoryCalls, executorCalls)
	}
	if gotManagerRuntime != options.InstalledRuntime || gotManagerConfig != options.ActiveConfig {
		t.Fatalf("factory inputs = %q/%q, want %q/%q", gotManagerRuntime, gotManagerConfig, options.InstalledRuntime, options.ActiveConfig)
	}
	want := cutoverServiceOptions{
		build: cutoverArtifactOptions{
			BuildRoot:                      options.BuildRoot,
			BuildReceipt:                   options.BuildReceipt,
			ExpectedBuildReceiptSHA256:     options.ExpectedBuildReceiptSHA256,
			InstalledRuntime:               options.InstalledRuntime,
			StagedRuntime:                  options.StagedRuntime,
			ExpectedInstalledRuntimeSHA256: options.ExpectedInstalledRuntimeSHA256,
			ExpectedStagedRuntimeSHA256:    options.ExpectedStagedRuntimeSHA256,
			RollbackDir:                    options.RollbackDir,
			CutoverReceipt:                 options.CutoverReceipt,
		},
		active: cutoverActiveOptions{
			SourceDCI:        options.ActiveDCI,
			SourceDCIJSONL:   options.ActiveDCIJSONL,
			SourceEventStore: options.ActiveEventStore,
			SourceL1:         options.ActiveL1,
			SourceArchive:    options.ActiveArchive,
		},
		manager:        manager,
		ServiceReceipt: options.ServiceReceipt,
	}
	if !reflect.DeepEqual(gotOptions, want) {
		t.Fatalf("private owner options = %#v, want %#v", gotOptions, want)
	}
}

func TestCutoverInvalidInputsReturnSchemaValidBlockedReceiptWithoutOwner(t *testing.T) {
	options := publicCutoverOptionsForTest()
	options.ExpectedBuildReceiptSHA256 = strings.ToUpper(options.ExpectedBuildReceiptSHA256)
	factoryCalls, executorCalls := 0, 0
	withPublicCutoverSeams(t,
		func(string, string) (cutoverServiceManager, error) {
			factoryCalls++
			return &publicCutoverManagerStub{}, nil
		},
		func(context.Context, cutoverServiceOptions) (ServiceCutoverReceipt, error) {
			executorCalls++
			return ServiceCutoverReceipt{}, nil
		},
	)

	got, err := Cutover(context.Background(), options)
	if err == nil || errorCode(err, "") != "invalid_options" {
		t.Fatalf("invalid input error = %v, code=%q", err, errorCode(err, ""))
	}
	if validateErr := validateServiceCutoverReceipt(got); validateErr != nil {
		t.Fatalf("invalid input receipt is not schema-valid: %v; receipt=%#v", validateErr, got)
	}
	if got.BuildReceiptSHA256 != "" || got.OldRuntimeSHA256 != options.ExpectedInstalledRuntimeSHA256 || got.NewRuntimeSHA256 != options.ExpectedStagedRuntimeSHA256 {
		t.Fatalf("invalid input hash retention = %#v", got)
	}
	if factoryCalls != 0 || executorCalls != 0 {
		t.Fatalf("owner calls after invalid input = %d/%d", factoryCalls, executorCalls)
	}
}

func TestCutoverNilContextReturnsSchemaValidBlockedReceiptWithOnlyValidHashes(t *testing.T) {
	options := publicCutoverOptionsForTest()
	options.ExpectedInstalledRuntimeSHA256 = "invalid-runtime-hash"
	got, err := Cutover(nil, options)
	if err == nil || errorCode(err, "") != "invalid_context" {
		t.Fatalf("nil context error = %v, code=%q", err, errorCode(err, ""))
	}
	if validateErr := validateServiceCutoverReceipt(got); validateErr != nil {
		t.Fatalf("nil context receipt is not schema-valid: %v; receipt=%#v", validateErr, got)
	}
	if got.BuildReceiptSHA256 != options.ExpectedBuildReceiptSHA256 || got.OldRuntimeSHA256 != "" || got.NewRuntimeSHA256 != options.ExpectedStagedRuntimeSHA256 {
		t.Fatalf("nil context hash retention = %#v", got)
	}
	if got.StartedAt.Location() != time.UTC || got.CompletedAt.Location() != time.UTC || got.CompletedAt.Before(got.StartedAt) {
		t.Fatalf("nil context timestamps = %v/%v", got.StartedAt, got.CompletedAt)
	}
}

func TestCutoverPlatformErrorAndZeroOwnerReceiptAreBounded(t *testing.T) {
	options := publicCutoverOptionsForTest()
	managerCalls, executorCalls := 0, 0
	withPublicCutoverSeams(t,
		func(string, string) (cutoverServiceManager, error) {
			managerCalls++
			return nil, newCodedError("service_manager_unavailable", "private/path/secret")
		},
		func(context.Context, cutoverServiceOptions) (ServiceCutoverReceipt, error) {
			executorCalls++
			return ServiceCutoverReceipt{}, errors.New("private/path/secret")
		},
	)

	got, err := Cutover(context.Background(), options)
	if err == nil || errorCode(err, "") != "service_manager_unavailable" {
		t.Fatalf("platform error = %v, code=%q", err, errorCode(err, ""))
	}
	if got.Status != CutoverStatusBlocked || got.ErrorCode != "service_manager_unavailable" {
		t.Fatalf("platform receipt = %#v", got)
	}
	if managerCalls != 1 || executorCalls != 0 {
		t.Fatalf("platform owner calls = %d/%d", managerCalls, executorCalls)
	}

	withPublicCutoverSeams(t,
		func(string, string) (cutoverServiceManager, error) {
			return &publicCutoverManagerStub{}, nil
		},
		func(context.Context, cutoverServiceOptions) (ServiceCutoverReceipt, error) {
			return ServiceCutoverReceipt{}, errors.New("private/path/secret")
		},
	)
	got, err = Cutover(context.Background(), options)
	if err == nil || errorCode(err, "") != "service_cutover" {
		t.Fatalf("zero owner receipt error = %v, code=%q", err, errorCode(err, ""))
	}
	if got.Status != CutoverStatusBlocked || got.ErrorCode != "service_cutover" {
		t.Fatalf("zero owner receipt = %#v", got)
	}
	if strings.Contains(err.Error(), "private/path/secret") {
		t.Fatalf("zero owner error leaked private input: %v", err)
	}
}

func TestCutoverRejectsMalformedTerminalOwnerReceipts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ServiceCutoverReceipt)
	}{
		{
			name: "malformed applied",
			mutate: func(receipt *ServiceCutoverReceipt) {
				receipt.CutoverSubreceiptSHA256 = ""
				receipt.CutoverSubreceiptStatus = ""
			},
		},
		{
			name: "malformed blocked",
			mutate: func(receipt *ServiceCutoverReceipt) {
				receipt.Status = CutoverStatusBlocked
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			options := publicCutoverOptionsForTest()
			ownerReceipt := validServiceCutoverReceiptForTest()
			tt.mutate(&ownerReceipt)
			withPublicCutoverSeams(t,
				func(string, string) (cutoverServiceManager, error) {
					return &publicCutoverManagerStub{}, nil
				},
				func(context.Context, cutoverServiceOptions) (ServiceCutoverReceipt, error) {
					return ownerReceipt, nil
				},
			)

			got, err := Cutover(context.Background(), options)
			if err == nil || errorCode(err, "") != "service_cutover" {
				t.Fatalf("malformed owner error = %v, code=%q", err, errorCode(err, ""))
			}
			if validateErr := validateServiceCutoverReceipt(got); validateErr != nil {
				t.Fatalf("wrapped receipt is not schema-valid: %v; receipt=%#v", validateErr, got)
			}
			if got.Status != CutoverStatusBlocked || got.ErrorCode != "service_cutover" {
				t.Fatalf("malformed owner receipt escaped as %#v", got)
			}
			if reflect.DeepEqual(got, ownerReceipt) {
				t.Fatalf("malformed owner receipt was preserved: %#v", got)
			}
		})
	}
}

func TestCutoverNonAppliedTerminalWithoutExecutorErrorReturnsBoundedError(t *testing.T) {
	for _, status := range []string{CutoverStatusBlocked, CutoverStatusRolledBack, CutoverStatusRollbackFailed} {
		t.Run(status, func(t *testing.T) {
			options := publicCutoverOptionsForTest()
			ownerReceipt := publicTerminalReceiptForTest(status)
			if validateErr := validateServiceCutoverReceipt(ownerReceipt); validateErr != nil {
				t.Fatalf("test owner receipt is not schema-valid: %v; receipt=%#v", validateErr, ownerReceipt)
			}
			withPublicCutoverSeams(t,
				func(string, string) (cutoverServiceManager, error) {
					return &publicCutoverManagerStub{}, nil
				},
				func(context.Context, cutoverServiceOptions) (ServiceCutoverReceipt, error) {
					return ownerReceipt, nil
				},
			)

			got, err := Cutover(context.Background(), options)
			if !reflect.DeepEqual(got, ownerReceipt) {
				t.Fatalf("terminal receipt changed: got=%#v want=%#v", got, ownerReceipt)
			}
			if err == nil || errorCode(err, "") != ownerReceipt.ErrorCode || !validErrorCode(errorCode(err, "")) {
				t.Fatalf("non-applied terminal error = %v, code=%q, want %q", err, errorCode(err, ""), ownerReceipt.ErrorCode)
			}
		})
	}
}

func TestCutoverPreservesTerminalOwnerReceipts(t *testing.T) {
	statuses := []string{CutoverStatusApplied, CutoverStatusBlocked, CutoverStatusRolledBack, CutoverStatusRollbackFailed}
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			options := publicCutoverOptionsForTest()
			ownerReceipt := publicTerminalReceiptForTest(status)
			withPublicCutoverSeams(t,
				func(string, string) (cutoverServiceManager, error) { return &publicCutoverManagerStub{}, nil },
				func(context.Context, cutoverServiceOptions) (ServiceCutoverReceipt, error) {
					return ownerReceipt, errors.New("owner/path/secret")
				},
			)
			got, err := Cutover(context.Background(), options)
			if !reflect.DeepEqual(got, ownerReceipt) {
				t.Fatalf("terminal receipt changed: got=%#v want=%#v", got, ownerReceipt)
			}
			if err == nil || !validErrorCode(errorCode(err, "")) {
				t.Fatalf("terminal error is not bounded: %v", err)
			}
		})
	}
}
