package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeCutoverServiceManager struct {
	events []string

	running    bool
	masked     bool
	runtimeSHA string

	maskErrors          []error
	stoppedErrors       []error
	stoppedEvidence     []cutoverServiceStoppedEvidence
	startErrors         []error
	startRuntimeSHA     []string
	runningErrors       []error
	runningEvidence     []cutoverServiceRunningEvidence
	maintenanceErrors   []error
	maintenanceEvidence []cutoverServiceMaintenanceStoppedEvidence
	mutateBeforeStopErr bool

	stopCalls         int
	startCalls        int
	startedRuntimeSHA []string
}

func (m *fakeCutoverServiceManager) VerifyMaintenanceStopped(_ context.Context, _ string) (cutoverServiceMaintenanceStoppedEvidence, error) {
	m.events = append(m.events, "verify_maintenance_stopped")
	if len(m.maintenanceErrors) != 0 {
		err := m.maintenanceErrors[0]
		m.maintenanceErrors = m.maintenanceErrors[1:]
		return cutoverServiceMaintenanceStoppedEvidence{}, err
	}
	if len(m.maintenanceEvidence) != 0 {
		evidence := m.maintenanceEvidence[0]
		m.maintenanceEvidence = m.maintenanceEvidence[1:]
		return evidence, nil
	}
	if m.running || m.masked {
		return cutoverServiceMaintenanceStoppedEvidence{}, nil
	}
	return cutoverServiceMaintenanceStoppedEvidence{
		Owner: 1, Enabled: 1, Unmasked: 1, Active: 0, MainPIDZero: 1,
		ListenerZero: 1, RuntimeSHA256: m.runtimeSHA,
	}, nil
}

func (m *fakeCutoverServiceManager) VerifyRunning(_ context.Context, _ string) (cutoverServiceRunningEvidence, error) {
	m.events = append(m.events, "verify_running")
	if len(m.runningErrors) != 0 {
		err := m.runningErrors[0]
		m.runningErrors = m.runningErrors[1:]
		return cutoverServiceRunningEvidence{}, err
	}
	if len(m.runningEvidence) != 0 {
		evidence := m.runningEvidence[0]
		m.runningEvidence = m.runningEvidence[1:]
		return evidence, nil
	}
	if !m.running {
		return cutoverServiceRunningEvidence{}, nil
	}
	unmasked := 0
	if !m.masked {
		unmasked = 1
	}
	return cutoverServiceRunningEvidence{
		Owner: 1, Enabled: 1, Unmasked: unmasked, Active: 1, MainPIDPositive: 1,
		ListenerOwned: 1, Readiness: 1, RuntimeSHA256: m.runtimeSHA,
	}, nil
}

func (m *fakeCutoverServiceManager) MaskAndStop(_ context.Context) error {
	m.events = append(m.events, "mask_stop")
	m.stopCalls++
	var err error
	if len(m.maskErrors) != 0 {
		err = m.maskErrors[0]
		m.maskErrors = m.maskErrors[1:]
	}
	if err == nil || m.mutateBeforeStopErr {
		m.running = false
		m.masked = true
	}
	return err
}

func (m *fakeCutoverServiceManager) VerifyStopped(_ context.Context) (cutoverServiceStoppedEvidence, error) {
	m.events = append(m.events, "verify_stopped")
	if len(m.stoppedErrors) != 0 {
		err := m.stoppedErrors[0]
		m.stoppedErrors = m.stoppedErrors[1:]
		return cutoverServiceStoppedEvidence{}, err
	}
	if len(m.stoppedEvidence) != 0 {
		evidence := m.stoppedEvidence[0]
		m.stoppedEvidence = m.stoppedEvidence[1:]
		return evidence, nil
	}
	if !m.masked || m.running {
		return cutoverServiceStoppedEvidence{}, nil
	}
	return cutoverServiceStoppedEvidence{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1}, nil
}

func (m *fakeCutoverServiceManager) UnmaskAndStart(_ context.Context) error {
	m.events = append(m.events, "unmask_start")
	var err error
	if len(m.startErrors) != 0 {
		err = m.startErrors[0]
		m.startErrors = m.startErrors[1:]
	}
	if err != nil {
		return err
	}
	m.masked = false
	m.running = true
	if len(m.startRuntimeSHA) != 0 {
		m.runtimeSHA = m.startRuntimeSHA[0]
		m.startRuntimeSHA = m.startRuntimeSHA[1:]
	}
	m.startCalls++
	m.startedRuntimeSHA = append(m.startedRuntimeSHA, m.runtimeSHA)
	return nil
}

func newCutoverServiceManager(oldSHA, newSHA string) *fakeCutoverServiceManager {
	return &fakeCutoverServiceManager{
		running: true, runtimeSHA: oldSHA,
		startRuntimeSHA: []string{newSHA, oldSHA},
	}
}

func cutoverServiceOptionsForFixture(t *testing.T, fixture cutoverActiveCohortTestFixture, manager cutoverServiceManager) cutoverServiceOptions {
	t.Helper()
	installed, ok := findCutoverBoundFile(fixture.build.files, fixture.build.paths.installedRuntime)
	if !ok {
		t.Fatal("installed runtime binding missing")
	}
	staged, ok := findCutoverBoundFile(fixture.build.files, fixture.build.paths.stagedRuntime)
	if !ok {
		t.Fatal("staged runtime binding missing")
	}
	return cutoverServiceOptions{
		build: cutoverArtifactOptions{
			BuildRoot: fixture.build.paths.buildRoot, BuildReceipt: fixture.build.paths.buildReceipt,
			ExpectedBuildReceiptSHA256: fixture.build.buildReceiptSHA256,
			InstalledRuntime:           fixture.build.paths.installedRuntime, StagedRuntime: fixture.build.paths.stagedRuntime,
			ExpectedInstalledRuntimeSHA256: installed.sha256, ExpectedStagedRuntimeSHA256: staged.sha256,
			RollbackDir: fixture.build.paths.rollbackDir, CutoverReceipt: fixture.build.paths.cutoverReceipt,
		},
		active: fixture.options, manager: manager,
	}
}

func syntheticCutoverServiceOptions(t *testing.T, manager cutoverServiceManager, oldSHA, newSHA string) cutoverServiceOptions {
	t.Helper()
	original := cutoverServiceQuiesceActive
	cutoverServiceQuiesceActive = func(context.Context, cutoverActiveOptions) (cutoverActiveQuiesceEvidence, error) {
		return cutoverActiveQuiesceEvidence{SQLiteSources: 4, BusyZero: 1, JournalModeDelete: 1, SameFile: 1, SidecarZero: 1}, nil
	}
	t.Cleanup(func() { cutoverServiceQuiesceActive = original })
	return cutoverServiceOptions{
		build: cutoverArtifactOptions{
			BuildRoot: "build-root", BuildReceipt: "build-receipt",
			ExpectedBuildReceiptSHA256: strings.Repeat("c", 64),
			InstalledRuntime:           "installed-runtime", StagedRuntime: "staged-runtime",
			ExpectedInstalledRuntimeSHA256: oldSHA, ExpectedStagedRuntimeSHA256: newSHA,
			RollbackDir: "rollback-root", CutoverReceipt: "cutover-receipt",
		},
		active: cutoverActiveOptions{
			SourceDCI: "active-dci", SourceDCIJSONL: "active-dci-jsonl",
			SourceEventStore: "active-event-store", SourceL1: "active-l1", SourceArchive: "active-archive",
		},
		manager: manager,
	}
}

func recordCutoverServicePipeline(t *testing.T, manager *fakeCutoverServiceManager) {
	t.Helper()
	originalBuild := cutoverServicePrepareBuild
	originalQuiesce := cutoverServiceQuiesceActive
	originalActive := cutoverServicePrepareActive
	originalStage := cutoverServiceStage
	originalApply := cutoverServiceApply
	cutoverServicePrepareBuild = func(ctx context.Context, options cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		if !manager.masked || manager.running {
			t.Error("build preparation ran before stopped proof")
		}
		manager.events = append(manager.events, "prepare_build")
		return originalBuild(ctx, options)
	}
	cutoverServiceQuiesceActive = func(ctx context.Context, options cutoverActiveOptions) (cutoverActiveQuiesceEvidence, error) {
		if !manager.masked || manager.running {
			t.Error("active quiesce ran before stopped proof")
		}
		manager.events = append(manager.events, "quiesce_active")
		return originalQuiesce(ctx, options)
	}
	cutoverServicePrepareActive = func(ctx context.Context, prepared preparedCutoverArtifacts, options cutoverActiveOptions) (preparedCutoverActiveCohort, error) {
		if !manager.masked || manager.running {
			t.Error("active preparation ran before stopped proof")
		}
		manager.events = append(manager.events, "prepare_active")
		return originalActive(ctx, prepared, options)
	}
	cutoverServiceStage = func(ctx context.Context, prepared preparedCutoverActiveCohort) (preparedCutoverStage, error) {
		if !manager.masked || manager.running {
			t.Error("staging ran before stopped proof")
		}
		manager.events = append(manager.events, "stage")
		return originalStage(ctx, prepared)
	}
	cutoverServiceApply = func(ctx context.Context, staged preparedCutoverStage) (cutoverApplyOperationResult, error) {
		if !manager.masked || manager.running {
			t.Error("apply ran before stopped proof")
		}
		manager.events = append(manager.events, "apply")
		return originalApply(ctx, staged)
	}
	t.Cleanup(func() {
		cutoverServicePrepareBuild = originalBuild
		cutoverServiceQuiesceActive = originalQuiesce
		cutoverServicePrepareActive = originalActive
		cutoverServiceStage = originalStage
		cutoverServiceApply = originalApply
	})
}

func TestExecuteServiceCutoverHappyPathOrdersStopBeforeRealD2C(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := fixture.build.buildReceipt.OutputArtifacts[buildOutputDCIRole].FileSHA256
	manager := newCutoverServiceManager(mustFileSHA256(t, fixture.build.paths.installedRuntime), mustFileSHA256(t, fixture.build.paths.stagedRuntime))
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	if oldSHA == "" || options.build.ExpectedInstalledRuntimeSHA256 == "" {
		t.Fatal("fixture runtime binding is incomplete")
	}
	recordCutoverServicePipeline(t, manager)

	result, err := executeServiceCutover(context.Background(), options)
	if err != nil || result.status != cutoverServiceApplied || result.applied == nil {
		t.Fatalf("service cutover = %#v, err=%v", result, err)
	}
	want := []string{"verify_running", "mask_stop", "verify_stopped", "quiesce_active", "prepare_build", "prepare_active", "stage", "verify_stopped", "apply", "unmask_start", "verify_running"}
	if !reflect.DeepEqual(manager.events, want) {
		t.Fatalf("service manager/order = %#v, want %#v", manager.events, want)
	}
	if _, err := os.Stat(options.build.CutoverReceipt); err != nil {
		t.Fatalf("D2c receipt missing after service cutover: %v", err)
	}
}

func TestExecuteServiceCutoverQuiescesPersistentWALBeforeActiveBinding(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	for _, path := range []string{
		fixture.options.SourceDCI,
		fixture.options.SourceEventStore,
		fixture.options.SourceL1,
		fixture.options.SourceArchive,
	} {
		forceCutoverPersistentWALWithoutLogicalChange(t, path)
	}
	manager := newCutoverServiceManager(
		mustFileSHA256(t, fixture.build.paths.installedRuntime),
		mustFileSHA256(t, fixture.build.paths.stagedRuntime),
	)
	options := cutoverServiceOptionsForFixture(t, fixture, manager)

	result, err := executeServiceCutover(context.Background(), options)
	if err != nil || result.status != cutoverServiceApplied || result.applied == nil {
		t.Fatalf("persistent-WAL service cutover = %#v, err=%v", result, err)
	}
}

func TestExecuteServiceCutoverQuiesceFailureRestoresOldRuntime(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	manager := newCutoverServiceManager(oldSHA, mustFileSHA256(t, fixture.build.paths.stagedRuntime))
	manager.startRuntimeSHA = []string{oldSHA}
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	originalQuiesce := cutoverServiceQuiesceActive
	originalBuild := cutoverServicePrepareBuild
	buildCalled := false
	cutoverServiceQuiesceActive = func(context.Context, cutoverActiveOptions) (cutoverActiveQuiesceEvidence, error) {
		return cutoverActiveQuiesceEvidence{}, newCodedError("active_quiesce", "fixture is busy")
	}
	cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		buildCalled = true
		return preparedCutoverArtifacts{}, errors.New("must not prepare")
	}
	t.Cleanup(func() {
		cutoverServiceQuiesceActive = originalQuiesce
		cutoverServicePrepareBuild = originalBuild
	})

	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked || errorCode(err, "") != "active_quiesce" {
		t.Fatalf("quiesce failure = %#v, err=%v", result, err)
	}
	if buildCalled || !result.finalRunning.valid(oldSHA) || result.activeSourcesQuiesced.valid() {
		t.Fatalf("quiesce failure crossed boundary = called=%v result=%#v", buildCalled, result)
	}
	want := []string{"verify_running", "mask_stop", "verify_stopped", "unmask_start", "verify_running"}
	if !reflect.DeepEqual(manager.events, want) {
		t.Fatalf("quiesce recovery order = %#v, want %#v", manager.events, want)
	}
}

func TestExecuteServiceCutoverMaintenanceStoppedHappyPath(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	manager.running = false
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	options.initialServiceStopped = true
	recordCutoverServicePipeline(t, manager)

	result, err := executeServiceCutover(context.Background(), options)
	if err != nil || result.status != cutoverServiceApplied || result.applied == nil {
		t.Fatalf("maintenance-stopped cutover = %#v, err=%v", result, err)
	}
	want := []string{"verify_maintenance_stopped", "mask_stop", "verify_stopped", "quiesce_active", "prepare_build", "prepare_active", "stage", "verify_stopped", "apply", "unmask_start", "verify_running"}
	if !reflect.DeepEqual(manager.events, want) {
		t.Fatalf("maintenance-stopped order = %#v, want %#v", manager.events, want)
	}
	if result.initialState != ServiceCutoverInitialMaintenanceStopped || !result.initialMaintenanceStopped.valid(oldSHA) || result.initialRunning != (cutoverServiceRunningEvidence{}) {
		t.Fatalf("maintenance-stopped initial evidence = %#v", result)
	}
}

func TestExecuteServiceCutoverMaintenanceStoppedRejectsInvalidProofBeforeMutation(t *testing.T) {
	oldSHA := strings.Repeat("a", 64)
	manager := newCutoverServiceManager(oldSHA, strings.Repeat("b", 64))
	manager.running = false
	manager.maintenanceEvidence = []cutoverServiceMaintenanceStoppedEvidence{
		{Owner: 1, Enabled: 1, Unmasked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 0, RuntimeSHA256: oldSHA},
	}
	options := syntheticCutoverServiceOptions(t, manager, oldSHA, strings.Repeat("b", 64))
	options.initialServiceStopped = true

	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked || errorCode(err, "") != "service_maintenance_stopped" {
		t.Fatalf("invalid maintenance proof = %#v, err=%v", result, err)
	}
	if !reflect.DeepEqual(manager.events, []string{"verify_maintenance_stopped"}) || manager.stopCalls != 0 || manager.startCalls != 0 {
		t.Fatalf("invalid maintenance proof mutated service: events=%#v stops=%d starts=%d", manager.events, manager.stopCalls, manager.startCalls)
	}
}

func TestExecuteServiceCutoverMaintenanceStoppedRecoveryRestartsOldRuntime(t *testing.T) {
	oldSHA := strings.Repeat("a", 64)
	newSHA := strings.Repeat("b", 64)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	manager.running = false
	manager.startRuntimeSHA = []string{oldSHA}
	options := syntheticCutoverServiceOptions(t, manager, oldSHA, newSHA)
	options.initialServiceStopped = true
	original := cutoverServicePrepareBuild
	cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		return preparedCutoverArtifacts{}, errors.New("prepare failed")
	}
	t.Cleanup(func() { cutoverServicePrepareBuild = original })

	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked || !result.finalRunning.valid(oldSHA) {
		t.Fatalf("maintenance recovery = %#v, err=%v", result, err)
	}
	want := []string{"verify_maintenance_stopped", "mask_stop", "verify_stopped", "unmask_start", "verify_running"}
	if !reflect.DeepEqual(manager.events, want) || !reflect.DeepEqual(manager.startedRuntimeSHA, []string{oldSHA}) {
		t.Fatalf("maintenance recovery events=%#v starts=%#v", manager.events, manager.startedRuntimeSHA)
	}
}

func TestExecuteServiceCutoverRejectsInvalidInitialRunningEvidence(t *testing.T) {
	oldSHA := strings.Repeat("a", 64)
	newSHA := strings.Repeat("b", 64)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	manager.runningEvidence = []cutoverServiceRunningEvidence{{Owner: 1, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1, ListenerOwned: 1, Readiness: 0, RuntimeSHA256: oldSHA}}
	options := syntheticCutoverServiceOptions(t, manager, oldSHA, newSHA)
	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked || errorCode(err, "") != "service_running" {
		t.Fatalf("invalid running evidence result = %#v, err=%v", result, err)
	}
	if !reflect.DeepEqual(manager.events, []string{"verify_running"}) {
		t.Fatalf("manager call sequence for invalid running evidence: %#v", manager.events)
	}
}

func TestExecuteServiceCutoverRejectsBadRunningEvidenceBeforeMask(t *testing.T) {
	manager := newCutoverServiceManager(strings.Repeat("a", 64), strings.Repeat("b", 64))
	fixture := newCutoverActiveCohortTestFixture(t)
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	manager.runningEvidence = []cutoverServiceRunningEvidence{{Owner: 1, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1, ListenerOwned: 1, Readiness: 0, RuntimeSHA256: options.build.ExpectedInstalledRuntimeSHA256}}
	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked || errorCode(err, "") != "service_running" {
		t.Fatalf("bad running evidence = %#v, err=%v", result, err)
	}
	if !reflect.DeepEqual(manager.events, []string{"verify_running"}) {
		t.Fatalf("service calls after bad running evidence = %#v", manager.events)
	}
}

func TestExecuteServiceCutoverRequiresStoppedProofBeforePreparation(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	manager := newCutoverServiceManager(mustFileSHA256(t, fixture.build.paths.installedRuntime), mustFileSHA256(t, fixture.build.paths.stagedRuntime))
	manager.stoppedEvidence = []cutoverServiceStoppedEvidence{{Masked: 1, Active: 1, MainPIDZero: 1, ListenerZero: 1}}
	manager.startRuntimeSHA = []string{mustFileSHA256(t, fixture.build.paths.installedRuntime)}
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	before := cutoverActiveTestHashes(t, fixture.paths)
	called := false
	original := cutoverServicePrepareBuild
	cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		called = true
		return preparedCutoverArtifacts{}, errors.New("must not prepare")
	}
	t.Cleanup(func() { cutoverServicePrepareBuild = original })
	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked {
		t.Fatalf("stopped proof failure = %#v, err=%v", result, err)
	}
	if called || !reflect.DeepEqual(manager.events, []string{"verify_running", "mask_stop", "verify_stopped", "unmask_start", "verify_running"}) {
		t.Fatalf("preparation/service calls = called=%v events=%#v", called, manager.events)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatal("active cohort changed before stopped proof")
	}
}

func TestExecuteServiceCutoverRequiresSecondStoppedProofBeforeApply(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	manager := newCutoverServiceManager(oldSHA, mustFileSHA256(t, fixture.build.paths.stagedRuntime))
	manager.startRuntimeSHA = []string{oldSHA}
	manager.stoppedEvidence = []cutoverServiceStoppedEvidence{
		{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1},
		{Masked: 1, Active: 1, MainPIDZero: 1, ListenerZero: 1},
	}
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	before := cutoverActiveTestHashes(t, fixture.paths)
	originalBuild := cutoverServicePrepareBuild
	originalActive := cutoverServicePrepareActive
	originalStage := cutoverServiceStage
	originalApply := cutoverServiceApply
	cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		return preparedCutoverArtifacts{}, nil
	}
	cutoverServicePrepareActive = func(context.Context, preparedCutoverArtifacts, cutoverActiveOptions) (preparedCutoverActiveCohort, error) {
		return preparedCutoverActiveCohort{}, nil
	}
	cutoverServiceStage = func(context.Context, preparedCutoverActiveCohort) (preparedCutoverStage, error) {
		return preparedCutoverStage{}, nil
	}
	applyCalls := 0
	cutoverServiceApply = func(context.Context, preparedCutoverStage) (cutoverApplyOperationResult, error) {
		applyCalls++
		return cutoverApplyOperationResult{}, nil
	}
	t.Cleanup(func() {
		cutoverServicePrepareBuild = originalBuild
		cutoverServicePrepareActive = originalActive
		cutoverServiceStage = originalStage
		cutoverServiceApply = originalApply
	})

	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked || errorCode(err, "") != "service_stopped" {
		t.Fatalf("second stopped proof = %#v, err=%v", result, err)
	}
	if applyCalls != 0 {
		t.Fatalf("apply ran after stale stopped proof: %d", applyCalls)
	}
	wantEvents := []string{"verify_running", "mask_stop", "verify_stopped", "verify_stopped", "unmask_start", "verify_running"}
	if !reflect.DeepEqual(manager.events, wantEvents) {
		t.Fatalf("second stopped proof calls = %#v, want %#v", manager.events, wantEvents)
	}
	if !reflect.DeepEqual(manager.startedRuntimeSHA, []string{oldSHA}) {
		t.Fatalf("old service restoration starts = %#v", manager.startedRuntimeSHA)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatal("active cohort changed before apply")
	}
}

func TestExecuteServiceCutoverPreMutationFailuresRestoreOldService(t *testing.T) {
	tests := []struct {
		name string
		set  func(*testing.T)
	}{
		{name: "mask", set: func(t *testing.T) {
			original := cutoverServicePrepareBuild
			cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
				t.Fatal("build preparation should not run")
				return preparedCutoverArtifacts{}, nil
			}
			t.Cleanup(func() { cutoverServicePrepareBuild = original })
		}},
		{name: "build", set: func(t *testing.T) {
			original := cutoverServicePrepareBuild
			cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
				return preparedCutoverArtifacts{}, errors.New("/secret/build legacy-id payload")
			}
			t.Cleanup(func() { cutoverServicePrepareBuild = original })
		}},
		{name: "active", set: func(t *testing.T) {
			original := cutoverServicePrepareActive
			cutoverServicePrepareActive = func(context.Context, preparedCutoverArtifacts, cutoverActiveOptions) (preparedCutoverActiveCohort, error) {
				return preparedCutoverActiveCohort{}, errors.New("active path legacy-id payload")
			}
			t.Cleanup(func() { cutoverServicePrepareActive = original })
		}},
		{name: "stage", set: func(t *testing.T) {
			original := cutoverServiceStage
			cutoverServiceStage = func(context.Context, preparedCutoverActiveCohort) (preparedCutoverStage, error) {
				return preparedCutoverStage{}, errors.New("stage path legacy-id payload")
			}
			t.Cleanup(func() { cutoverServiceStage = original })
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
			manager := newCutoverServiceManager(oldSHA, mustFileSHA256(t, fixture.build.paths.stagedRuntime))
			manager.startRuntimeSHA = []string{oldSHA}
			options := cutoverServiceOptionsForFixture(t, fixture, manager)
			before := cutoverActiveTestHashes(t, fixture.paths)
			if tt.name == "mask" {
				manager.maskErrors = []error{errors.New("stop failed")}
			}
			tt.set(t)
			result, err := executeServiceCutover(context.Background(), options)
			if err == nil || result.status != cutoverServiceBlocked {
				t.Fatalf("%s failure = %#v, err=%v", tt.name, result, err)
			}
			if manager.startCalls != 1 || manager.runtimeSHA != oldSHA {
				t.Fatalf("old runtime was not restored: successful starts=%d sha=%q", manager.startCalls, manager.runtimeSHA)
			}
			if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
				t.Fatalf("active files changed on %s failure", tt.name)
			}
			if strings.Contains(err.Error(), "legacy-id") || strings.Contains(err.Error(), "payload") || strings.Contains(err.Error(), fixture.paths.dci) {
				t.Fatalf("bounded error leaked private value: %v", err)
			}
		})
	}
}

func TestExecuteServiceCutoverReturnsRollbackFailedWhenOldRestoreCannotBeProven(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	manager := newCutoverServiceManager(oldSHA, mustFileSHA256(t, fixture.build.paths.stagedRuntime))
	manager.startRuntimeSHA = []string{oldSHA}
	manager.startErrors = []error{errors.New("old start failed"), errors.New("safe stop only")}
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	original := cutoverServicePrepareBuild
	cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		return preparedCutoverArtifacts{}, errors.New("build failed")
	}
	t.Cleanup(func() { cutoverServicePrepareBuild = original })
	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceRollbackFailed || errorCode(err, "") != CutoverStatusRollbackFailed {
		t.Fatalf("old restore failure = %#v, err=%v", result, err)
	}
	if manager.startCalls != 0 {
		t.Fatalf("failed old restoration reported a start: %d", manager.startCalls)
	}
}

func TestExecuteServiceCutoverHandlesD2cRolledBackAndRollbackFailed(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantStatus cutoverServiceStatus
		wantStart  int
	}{
		{name: "rolled back", status: CutoverStatusRolledBack, wantStatus: cutoverServiceRolledBack, wantStart: 1},
		{name: "rollback failed", status: CutoverStatusRollbackFailed, wantStatus: cutoverServiceRollbackFailed, wantStart: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
			newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
			manager := newCutoverServiceManager(oldSHA, newSHA)
			manager.startRuntimeSHA = []string{oldSHA}
			options := cutoverServiceOptionsForFixture(t, fixture, manager)
			originalBuild := cutoverServicePrepareBuild
			originalApply := cutoverServiceApply
			cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
				return preparedCutoverArtifacts{}, nil
			}
			cutoverServiceApply = func(context.Context, preparedCutoverStage) (cutoverApplyOperationResult, error) {
				return cutoverApplyOperationResult{receipt: CutoverReceipt{Status: tt.status}, applied: nil}, errors.New("d2c result")
			}
			t.Cleanup(func() {
				cutoverServicePrepareBuild = originalBuild
				cutoverServiceApply = originalApply
			})
			// The active/stage seams are not reached because the build seam returns
			// an empty prepared value only for this state-boundary test.
			originalActive := cutoverServicePrepareActive
			originalStage := cutoverServiceStage
			cutoverServicePrepareActive = func(context.Context, preparedCutoverArtifacts, cutoverActiveOptions) (preparedCutoverActiveCohort, error) {
				return preparedCutoverActiveCohort{}, nil
			}
			cutoverServiceStage = func(context.Context, preparedCutoverActiveCohort) (preparedCutoverStage, error) {
				return preparedCutoverStage{}, nil
			}
			t.Cleanup(func() {
				cutoverServicePrepareActive = originalActive
				cutoverServiceStage = originalStage
			})
			result, err := executeServiceCutover(context.Background(), options)
			if err == nil || result.status != tt.wantStatus {
				t.Fatalf("D2c %s = %#v, err=%v", tt.name, result, err)
			}
			if manager.startCalls != tt.wantStart {
				t.Fatalf("D2c %s successful service starts = %d, want %d; events=%#v", tt.name, manager.startCalls, tt.wantStart, manager.events)
			}
			if tt.status == CutoverStatusRolledBack && !reflect.DeepEqual(manager.startedRuntimeSHA, []string{oldSHA}) {
				t.Fatalf("D2c rolled-back path did not start the old runtime: %#v", manager.startedRuntimeSHA)
			}
			if tt.status == CutoverStatusRollbackFailed && len(manager.startedRuntimeSHA) != 0 {
				t.Fatalf("D2c rollback-failed path started a service: %#v", manager.startedRuntimeSHA)
			}
		})
	}
}

func TestExecuteServiceCutoverPostApplyFailureStopsRollsBackAndRestoresOld(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeCutoverServiceManager)
	}{
		{name: "start error", setup: func(manager *fakeCutoverServiceManager) {
			manager.startErrors = []error{errors.New("new start failed")}
			manager.startRuntimeSHA = []string{strings.Repeat("a", 64)}
		}},
		{name: "new runtime mismatch", setup: func(manager *fakeCutoverServiceManager) {
			manager.startRuntimeSHA = []string{"wrong-runtime", strings.Repeat("a", 64)}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldSHA := strings.Repeat("a", 64)
			newSHA := strings.Repeat("b", 64)
			manager := newCutoverServiceManager(oldSHA, newSHA)
			tt.setup(manager)
			options := syntheticCutoverServiceOptions(t, manager, oldSHA, newSHA)
			applied := &cutoverAppliedState{receipt: CutoverReceipt{Status: CutoverStatusApplied}}
			originalBuild := cutoverServicePrepareBuild
			originalActive := cutoverServicePrepareActive
			originalStage := cutoverServiceStage
			originalApply := cutoverServiceApply
			originalRollback := cutoverServiceRollback
			cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
				return preparedCutoverArtifacts{}, nil
			}
			cutoverServicePrepareActive = func(context.Context, preparedCutoverArtifacts, cutoverActiveOptions) (preparedCutoverActiveCohort, error) {
				return preparedCutoverActiveCohort{}, nil
			}
			cutoverServiceStage = func(context.Context, preparedCutoverActiveCohort) (preparedCutoverStage, error) {
				return preparedCutoverStage{}, nil
			}
			cutoverServiceApply = func(context.Context, preparedCutoverStage) (cutoverApplyOperationResult, error) {
				return cutoverApplyOperationResult{receipt: CutoverReceipt{Status: CutoverStatusApplied}, applied: applied}, nil
			}
			rollbackCalls := 0
			cutoverServiceRollback = func(context.Context, *cutoverAppliedState) (CutoverReceipt, error) {
				rollbackCalls++
				return CutoverReceipt{Status: CutoverStatusRolledBack}, nil
			}
			t.Cleanup(func() {
				cutoverServicePrepareBuild = originalBuild
				cutoverServicePrepareActive = originalActive
				cutoverServiceStage = originalStage
				cutoverServiceApply = originalApply
				cutoverServiceRollback = originalRollback
			})
			result, err := executeServiceCutover(context.Background(), options)
			if err == nil || result.status != cutoverServiceRolledBack || result.applied != nil || rollbackCalls != 1 {
				t.Fatalf("post-apply %s = %#v, err=%v rollbackCalls=%d", tt.name, result, err, rollbackCalls)
			}
			wantStarts := 2
			if tt.name == "start error" {
				wantStarts = 1
			}
			if manager.stopCalls != 2 || manager.startCalls != wantStarts {
				t.Fatalf("post-apply %s manager recovery = stops=%d starts=%d events=%#v", tt.name, manager.stopCalls, manager.startCalls, manager.events)
			}
		})
	}
}

func TestExecuteServiceCutoverStoppedProofFailureSkipsRollback(t *testing.T) {
	oldSHA := strings.Repeat("a", 64)
	newSHA := strings.Repeat("b", 64)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	manager.stoppedEvidence = []cutoverServiceStoppedEvidence{
		{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1},
		{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1},
		{Masked: 1, Active: 1, MainPIDZero: 1, ListenerZero: 1},
	}
	manager.startRuntimeSHA = []string{"wrong-runtime", oldSHA}
	options := syntheticCutoverServiceOptions(t, manager, oldSHA, newSHA)
	prepared := &cutoverAppliedState{receipt: CutoverReceipt{Status: CutoverStatusApplied}}
	originalBuild := cutoverServicePrepareBuild
	originalActive := cutoverServicePrepareActive
	originalStage := cutoverServiceStage
	originalApply := cutoverServiceApply
	originalRollback := cutoverServiceRollback
	cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		return preparedCutoverArtifacts{}, nil
	}
	cutoverServicePrepareActive = func(context.Context, preparedCutoverArtifacts, cutoverActiveOptions) (preparedCutoverActiveCohort, error) {
		return preparedCutoverActiveCohort{}, nil
	}
	cutoverServiceStage = func(context.Context, preparedCutoverActiveCohort) (preparedCutoverStage, error) {
		return preparedCutoverStage{}, nil
	}
	cutoverServiceApply = func(context.Context, preparedCutoverStage) (cutoverApplyOperationResult, error) {
		return cutoverApplyOperationResult{receipt: CutoverReceipt{Status: CutoverStatusApplied}, applied: prepared}, nil
	}
	rollbackCalls := 0
	cutoverServiceRollback = func(context.Context, *cutoverAppliedState) (CutoverReceipt, error) {
		rollbackCalls++
		return CutoverReceipt{Status: CutoverStatusRolledBack}, nil
	}
	t.Cleanup(func() {
		cutoverServicePrepareBuild = originalBuild
		cutoverServicePrepareActive = originalActive
		cutoverServiceStage = originalStage
		cutoverServiceApply = originalApply
		cutoverServiceRollback = originalRollback
	})
	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceRollbackFailed || result.applied != prepared || rollbackCalls != 0 {
		t.Fatalf("stop proof failure = %#v, err=%v rollbackCalls=%d", result, err, rollbackCalls)
	}
	if !reflect.DeepEqual(manager.startedRuntimeSHA, []string{"wrong-runtime"}) {
		t.Fatalf("old service was started after stop proof failure; successful starts=%#v", manager.startedRuntimeSHA)
	}
}

func TestExecuteServiceCutoverRollbackFailureNeverStartsOldService(t *testing.T) {
	oldSHA := strings.Repeat("a", 64)
	newSHA := strings.Repeat("b", 64)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	manager.startRuntimeSHA = []string{"wrong-runtime", oldSHA}
	options := syntheticCutoverServiceOptions(t, manager, oldSHA, newSHA)
	prepared := &cutoverAppliedState{receipt: CutoverReceipt{Status: CutoverStatusApplied}}
	originalBuild := cutoverServicePrepareBuild
	originalActive := cutoverServicePrepareActive
	originalStage := cutoverServiceStage
	originalApply := cutoverServiceApply
	originalRollback := cutoverServiceRollback
	cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		return preparedCutoverArtifacts{}, nil
	}
	cutoverServicePrepareActive = func(context.Context, preparedCutoverArtifacts, cutoverActiveOptions) (preparedCutoverActiveCohort, error) {
		return preparedCutoverActiveCohort{}, nil
	}
	cutoverServiceStage = func(context.Context, preparedCutoverActiveCohort) (preparedCutoverStage, error) {
		return preparedCutoverStage{}, nil
	}
	cutoverServiceApply = func(context.Context, preparedCutoverStage) (cutoverApplyOperationResult, error) {
		return cutoverApplyOperationResult{receipt: CutoverReceipt{Status: CutoverStatusApplied}, applied: prepared}, nil
	}
	cutoverServiceRollback = func(context.Context, *cutoverAppliedState) (CutoverReceipt, error) {
		return CutoverReceipt{}, errors.New("rollback failed")
	}
	t.Cleanup(func() {
		cutoverServicePrepareBuild = originalBuild
		cutoverServicePrepareActive = originalActive
		cutoverServiceStage = originalStage
		cutoverServiceApply = originalApply
		cutoverServiceRollback = originalRollback
	})
	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceRollbackFailed {
		t.Fatalf("rollback failure = %#v, err=%v", result, err)
	}
	if !reflect.DeepEqual(manager.startedRuntimeSHA, []string{"wrong-runtime"}) {
		t.Fatalf("old service was started despite rollback failure; successful starts=%#v", manager.startedRuntimeSHA)
	}
}

func TestExecuteServiceCutoverCancellationUsesDetachedRecovery(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	manager := newCutoverServiceManager(oldSHA, mustFileSHA256(t, fixture.build.paths.stagedRuntime))
	manager.startRuntimeSHA = []string{oldSHA}
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	original := cutoverServicePrepareBuild
	cutoverServicePrepareBuild = func(ctx context.Context, _ cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		if ctx.Err() == nil {
			return preparedCutoverArtifacts{}, context.Canceled
		}
		return preparedCutoverArtifacts{}, context.Canceled
	}
	t.Cleanup(func() { cutoverServicePrepareBuild = original })
	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked || errorCode(err, "") != "context_canceled" {
		t.Fatalf("canceled service cutover = %#v, err=%v", result, err)
	}
	if manager.startCalls != 1 || manager.runtimeSHA != oldSHA {
		t.Fatalf("detached old-service restoration failed: starts=%d sha=%q", manager.startCalls, manager.runtimeSHA)
	}
	if !reflect.DeepEqual(manager.events, []string{"verify_running", "mask_stop", "verify_stopped", "unmask_start", "verify_running"}) {
		t.Fatalf("cancellation manager calls = %#v", manager.events)
	}
}

func TestExecuteServiceCutoverPreservesRollbackFailedBoundary(t *testing.T) {
	oldSHA := strings.Repeat("a", 64)
	newSHA := strings.Repeat("b", 64)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	options := syntheticCutoverServiceOptions(t, manager, oldSHA, newSHA)
	manager.startRuntimeSHA = []string{oldSHA}
	originalBuild := cutoverServicePrepareBuild
	cutoverServicePrepareBuild = func(context.Context, cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
		return preparedCutoverArtifacts{}, errors.New("fixture/path legacy-id payload secret")
	}
	t.Cleanup(func() { cutoverServicePrepareBuild = originalBuild })
	result, err := executeServiceCutover(context.Background(), options)
	if err == nil || result.status != cutoverServiceBlocked {
		t.Fatalf("bounded service error = %#v, err=%v", result, err)
	}
	for _, private := range []string{"fixture/path", "legacy-id", "payload", "secret"} {
		if strings.Contains(err.Error(), private) {
			t.Fatalf("service error leaked %q: %v", private, err)
		}
	}
}

func TestCutoverServiceEvidenceValidation(t *testing.T) {
	sha := strings.Repeat("a", 64)
	if !(cutoverServiceRunningEvidence{Owner: 1, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1, ListenerOwned: 1, Readiness: 1, RuntimeSHA256: sha}).valid(sha) {
		t.Fatal("valid running evidence rejected")
	}
	for _, evidence := range []cutoverServiceRunningEvidence{
		{Owner: 0, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1, ListenerOwned: 1, Readiness: 1, RuntimeSHA256: sha},
		{Owner: 1, Enabled: 1, Unmasked: 0, Active: 1, MainPIDPositive: 1, ListenerOwned: 1, Readiness: 1, RuntimeSHA256: sha},
		{Owner: 1, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1, ListenerOwned: 1, Readiness: 1, RuntimeSHA256: strings.ToUpper(sha)},
	} {
		if evidence.valid(sha) {
			t.Fatalf("invalid running evidence accepted: %#v", evidence)
		}
	}
	if !(cutoverServiceStoppedEvidence{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1}).valid() {
		t.Fatal("valid stopped evidence rejected")
	}
	if (cutoverServiceStoppedEvidence{Masked: 1, Active: 1, MainPIDZero: 1, ListenerZero: 1}).valid() {
		t.Fatal("active service accepted as stopped")
	}
}

func TestExecuteServiceCutoverContextNilFailsClosed(t *testing.T) {
	manager := newCutoverServiceManager(strings.Repeat("a", 64), strings.Repeat("b", 64))
	result, err := executeServiceCutover(nil, cutoverServiceOptions{manager: manager})
	if err == nil || result.status != "" || errorCode(err, "") != "invalid_context" {
		t.Fatalf("nil context result = %#v, err=%v", result, err)
	}
}

func TestCutoverServiceOptionsDoNotExposePrivateValuesInError(t *testing.T) {
	secretDir := filepath.Join(t.TempDir(), "private-secret")
	manager := newCutoverServiceManager(strings.Repeat("a", 64), strings.Repeat("b", 64))
	result, err := executeServiceCutover(context.Background(), cutoverServiceOptions{
		build: cutoverArtifactOptions{
			BuildRoot: secretDir, BuildReceipt: filepath.Join(secretDir, "receipt-secret.json"),
			ExpectedBuildReceiptSHA256: strings.Repeat("a", 64), InstalledRuntime: filepath.Join(secretDir, "old-secret"), StagedRuntime: filepath.Join(secretDir, "new-secret"),
			ExpectedInstalledRuntimeSHA256: strings.Repeat("a", 64), ExpectedStagedRuntimeSHA256: strings.Repeat("b", 64), RollbackDir: filepath.Join(secretDir, "rollback-secret"), CutoverReceipt: filepath.Join(secretDir, "receipt-secret"),
		},
		manager: manager,
	})
	if err == nil || result.status != cutoverServiceBlocked {
		t.Fatalf("private invalid options = %#v, err=%v", result, err)
	}
	if strings.Contains(err.Error(), secretDir) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("private option leaked: %v", err)
	}
}
