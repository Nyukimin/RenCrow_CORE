package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validServiceCutoverReceiptForTest() ServiceCutoverReceipt {
	oldSHA := strings.Repeat("a", 64)
	newSHA := strings.Repeat("b", 64)
	now := time.Now().UTC()
	return ServiceCutoverReceipt{
		SchemaVersion: ServiceCutoverSchemaVersion, Mode: ModeCutover, Status: CutoverStatusApplied,
		InitialState: ServiceCutoverInitialRunning,
		StartedAt:    now, CompletedAt: now,
		CutoverSubreceiptSHA256: strings.Repeat("c", 64), CutoverSubreceiptStatus: CutoverStatusApplied,
		CutoverTerminalStatus: CutoverStatusApplied,
		BuildReceiptSHA256:    strings.Repeat("d", 64), CaptureReceiptSHA256: strings.Repeat("e", 64),
		DryRunManifestSHA256: strings.Repeat("f", 64), CaptureArtifactSetSHA256: strings.Repeat("0", 64),
		OldRuntimeSHA256: oldSHA, NewRuntimeSHA256: newSHA,
		InitialRunning: ServiceCutoverRunningEvidence{
			Owner: 1, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1,
			ListenerOwned: 1, Readiness: 1, RuntimeSHA256: oldSHA,
		},
		StoppedBeforePrepare: ServiceCutoverStoppedEvidence{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1},
		ActiveSourcesQuiesced: ServiceCutoverQuiesceEvidence{
			SQLiteSources: 4, BusyZero: 1, JournalModeDelete: 1, SameFile: 1, SidecarZero: 1,
		},
		StoppedBeforeApply: ServiceCutoverStoppedEvidence{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1},
		FinalRunning: ServiceCutoverRunningEvidence{
			Owner: 1, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1,
			ListenerOwned: 1, Readiness: 1, RuntimeSHA256: newSHA,
		},
	}
}

func validMaintenanceStoppedServiceCutoverReceiptForTest() ServiceCutoverReceipt {
	receipt := validServiceCutoverReceiptForTest()
	receipt.InitialState = ServiceCutoverInitialMaintenanceStopped
	receipt.InitialRunning = ServiceCutoverRunningEvidence{}
	receipt.InitialMaintenanceStopped = ServiceCutoverMaintenanceStoppedEvidence{
		Owner: 1, Enabled: 1, Unmasked: 1, Active: 0, MainPIDZero: 1,
		ListenerZero: 1, RuntimeSHA256: receipt.OldRuntimeSHA256,
	}
	return receipt
}

func TestServiceCutoverReceiptWriterRoundTripFreshOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	want := validServiceCutoverReceiptForTest()
	gotWrite, err := writeServiceCutoverReceipt(path, want)
	if err != nil || gotWrite.final == nil {
		t.Fatalf("write service receipt = %#v, err=%v", gotWrite, err)
	}
	got, err := readServiceCutoverReceipt(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read service receipt = %#v, err=%v, want %#v", got, err, want)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || runtimeGOOSNotWindows() && info.Mode().Perm() != 0o600 || !cutoverKnownFileIsUnaliased(path, info) {
		t.Fatalf("service receipt binding = mode=%v links-safe=%v", info.Mode(), cutoverKnownFileIsUnaliased(path, info))
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("service receipt directory entries = %#v, err=%v", entries, err)
	}
	if err := writeServiceCutoverReceiptFreshOnlyMustFail(path, want); err == nil {
		t.Fatal("existing service receipt was overwritten")
	}
}

func TestServiceCutoverReceiptWriterPreservesUnknownFinal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	unknown := []byte("unknown-final")
	if err := os.WriteFile(path, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := writeServiceCutoverReceipt(path, validServiceCutoverReceiptForTest())
	if err == nil {
		t.Fatal("writer accepted existing final")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || !reflect.DeepEqual(got, unknown) {
		t.Fatalf("unknown final changed: %q, err=%v", got, readErr)
	}
}

func TestServiceCutoverReceiptWriterPublishErrorCleansOwnedTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	original := serviceCutoverReceiptPublish
	serviceCutoverReceiptPublish = func(string, string) error { return errors.New("publish failed") }
	t.Cleanup(func() { serviceCutoverReceiptPublish = original })
	_, err := writeServiceCutoverReceipt(path, validServiceCutoverReceiptForTest())
	if err == nil {
		t.Fatal("publish failure was accepted")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("publish failure left files = %#v, err=%v", entries, readErr)
	}
}

func TestServiceCutoverReceiptWriterPrePublicationFailureCleansOwnedTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	original := serviceCutoverReceiptSyncFile
	serviceCutoverReceiptSyncFile = func(file *os.File) error {
		if err := file.Truncate(0); err != nil {
			return err
		}
		return errors.New("sync failed after partial write")
	}
	t.Cleanup(func() { serviceCutoverReceiptSyncFile = original })
	_, err := writeServiceCutoverReceipt(path, validServiceCutoverReceiptForTest())
	if err == nil {
		t.Fatal("pre-publication sync failure was accepted")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("pre-publication failure left files = %#v, err=%v", entries, readErr)
	}
}

func TestServiceCutoverReceiptWriterPreservesRacingFinalAfterPublishError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	original := serviceCutoverReceiptPublish
	unknown := []byte("racing-final")
	serviceCutoverReceiptPublish = func(_, final string) error {
		if err := os.WriteFile(final, unknown, 0o600); err != nil {
			return err
		}
		return errors.New("publish race")
	}
	t.Cleanup(func() { serviceCutoverReceiptPublish = original })
	_, err := writeServiceCutoverReceipt(path, validServiceCutoverReceiptForTest())
	if err == nil {
		t.Fatal("racing final was accepted")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || !reflect.DeepEqual(got, unknown) {
		t.Fatalf("racing final changed: %q, err=%v", got, readErr)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("racing final cleanup = %#v, err=%v", entries, readErr)
	}
}

func TestValidateServiceCutoverReceiptStrictClaims(t *testing.T) {
	valid := validServiceCutoverReceiptForTest()
	cases := []struct {
		name string
		edit func(*ServiceCutoverReceipt)
	}{
		{name: "schema", edit: func(r *ServiceCutoverReceipt) { r.SchemaVersion = "wrong" }},
		{name: "status", edit: func(r *ServiceCutoverReceipt) { r.Status = "unknown" }},
		{name: "error", edit: func(r *ServiceCutoverReceipt) { r.ErrorCode = "/private" }},
		{name: "missing subreceipt", edit: func(r *ServiceCutoverReceipt) { r.CutoverSubreceiptSHA256 = ""; r.CutoverSubreceiptStatus = "" }},
		{name: "initial state", edit: func(r *ServiceCutoverReceipt) { r.InitialState = "unknown" }},
		{name: "bad running", edit: func(r *ServiceCutoverReceipt) { r.InitialRunning.Owner = 2 }},
		{name: "bad stopped", edit: func(r *ServiceCutoverReceipt) { r.StoppedBeforePrepare.Active = 2 }},
		{name: "bad quiesce", edit: func(r *ServiceCutoverReceipt) { r.ActiveSourcesQuiesced.SidecarZero = 0 }},
		{name: "non UTC", edit: func(r *ServiceCutoverReceipt) { r.StartedAt = r.StartedAt.In(time.FixedZone("x", 0)) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := valid
			tt.edit(&got)
			if err := validateServiceCutoverReceipt(got); err == nil {
				t.Fatal("malformed service receipt accepted")
			}
		})
	}

	blocked := validServiceCutoverReceiptForTest()
	blocked.Status = CutoverStatusBlocked
	blocked.CutoverSubreceiptSHA256 = ""
	blocked.CutoverSubreceiptStatus = ""
	blocked.CutoverTerminalStatus = ""
	blocked.ErrorCode = "service_running"
	blocked.InitialRunning = ServiceCutoverRunningEvidence{}
	blocked.StoppedBeforePrepare = ServiceCutoverStoppedEvidence{}
	blocked.ActiveSourcesQuiesced = ServiceCutoverQuiesceEvidence{}
	blocked.StoppedBeforeApply = ServiceCutoverStoppedEvidence{}
	blocked.FinalRunning = ServiceCutoverRunningEvidence{}
	if err := validateServiceCutoverReceipt(blocked); err != nil {
		t.Fatalf("valid blocked receipt rejected: %v", err)
	}
	maintenance := validMaintenanceStoppedServiceCutoverReceiptForTest()
	if err := validateServiceCutoverReceipt(maintenance); err != nil {
		t.Fatalf("valid maintenance-stopped receipt rejected: %v", err)
	}
	maintenanceCases := []struct {
		name string
		edit func(*ServiceCutoverReceipt)
	}{
		{name: "also running", edit: func(r *ServiceCutoverReceipt) { r.InitialRunning = valid.InitialRunning }},
		{name: "active", edit: func(r *ServiceCutoverReceipt) { r.InitialMaintenanceStopped.Active = 1 }},
		{name: "wrong runtime", edit: func(r *ServiceCutoverReceipt) { r.InitialMaintenanceStopped.RuntimeSHA256 = r.NewRuntimeSHA256 }},
	}
	for _, test := range maintenanceCases {
		t.Run("maintenance-"+test.name, func(t *testing.T) {
			got := maintenance
			test.edit(&got)
			if err := validateServiceCutoverReceipt(got); err == nil {
				t.Fatal("invalid maintenance-stopped receipt accepted")
			}
		})
	}
	blocked.CutoverSubreceiptSHA256 = strings.Repeat("a", 64)
	if err := validateServiceCutoverReceipt(blocked); err == nil {
		t.Fatal("blocked receipt claimed a D2c subreceipt")
	}
}

func TestServiceCutoverReceiptProjectsActiveSourceQuiesceEvidence(t *testing.T) {
	seed := validServiceCutoverReceiptForTest()
	seed.ActiveSourcesQuiesced = ServiceCutoverQuiesceEvidence{}
	result := cutoverServiceResult{
		status: cutoverServiceApplied,
		activeSourcesQuiesced: cutoverActiveQuiesceEvidence{
			SQLiteSources: 4, BusyZero: 1, JournalModeDelete: 1, SameFile: 1, SidecarZero: 1,
		},
	}
	receipt := serviceCutoverReceiptFromResult(seed, result, strings.Repeat("c", 64), CutoverStatusApplied, "")
	if receipt.ActiveSourcesQuiesced != (ServiceCutoverQuiesceEvidence{
		SQLiteSources: 4, BusyZero: 1, JournalModeDelete: 1, SameFile: 1, SidecarZero: 1,
	}) {
		t.Fatalf("active source quiesce projection = %#v", receipt.ActiveSourcesQuiesced)
	}
}

func TestServiceCutoverReceiptStrictJSON(t *testing.T) {
	dir := t.TempDir()
	valid := validServiceCutoverReceiptForTest()
	encoded, err := marshalServiceCutoverReceipt(valid)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"unknown":  append(append([]byte{}, encoded[:len(encoded)-1]...), []byte(",\"private\":true}\n")...),
		"trailing": append(append([]byte{}, encoded...), []byte("{}\n")...),
		"oversize": append([]byte("{\"schema_version\":\""), bytesRepeat('x', int(maxServiceCutoverReceiptBytes))...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readServiceCutoverReceipt(path); err == nil {
				t.Fatal("malformed JSON accepted")
			}
		})
	}
}

func TestPrepareServiceCutoverReceiptRejectsFinalAliasesBeforeManager(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, cutoverServiceOptions) (string, func())
	}{
		{
			name: "same cutover artifact",
			setup: func(_ *testing.T, options cutoverServiceOptions) (string, func()) {
				return options.build.CutoverReceipt, func() {}
			},
		},
		{
			name: "ancestor build root",
			setup: func(_ *testing.T, options cutoverServiceOptions) (string, func()) {
				return options.build.BuildRoot, func() {}
			},
		},
		{
			name: "descendant build root",
			setup: func(_ *testing.T, options cutoverServiceOptions) (string, func()) {
				return filepath.Join(options.build.BuildRoot, "service.json"), func() {}
			},
		},
		{
			name: "symlink final",
			setup: func(t *testing.T, _ cutoverServiceOptions) (string, func()) {
				parent := t.TempDir()
				final := filepath.Join(parent, "service.json")
				target := filepath.Join(parent, "target")
				if err := os.WriteFile(target, []byte("preserve-symlink-target"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, final); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				return final, func() {
					if got, err := os.ReadFile(target); err != nil || string(got) != "preserve-symlink-target" {
						t.Errorf("symlink target changed: %q, err=%v", got, err)
					}
				}
			},
		},
		{
			name: "hardlink final",
			setup: func(t *testing.T, _ cutoverServiceOptions) (string, func()) {
				parent := t.TempDir()
				final := filepath.Join(parent, "service.json")
				target := filepath.Join(parent, "target")
				if err := os.WriteFile(target, []byte("preserve-hardlink-target"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, final); err != nil {
					t.Skipf("hardlink unavailable: %v", err)
				}
				return final, func() {
					if got, err := os.ReadFile(target); err != nil || string(got) != "preserve-hardlink-target" {
						t.Errorf("hardlink target changed: %q, err=%v", got, err)
					}
					finalInfo, finalErr := os.Stat(final)
					targetInfo, targetErr := os.Stat(target)
					if finalErr != nil || targetErr != nil || !os.SameFile(finalInfo, targetInfo) {
						t.Errorf("hardlink final was replaced: final=%v target=%v", finalErr, targetErr)
					}
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
			newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
			manager := newCutoverServiceManager(oldSHA, newSHA)
			options := cutoverServiceOptionsForFixture(t, fixture, manager)
			servicePath, preserve := tt.setup(t, options)
			t.Cleanup(preserve)
			options.ServiceReceipt = servicePath
			beforeEntries, err := os.ReadDir(fixture.build.paths.buildRoot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executeServiceCutoverWithReceipt(context.Background(), options); err == nil {
				t.Fatal("service receipt alias was accepted")
			}
			if len(manager.events) != 0 {
				t.Fatalf("service manager called before alias rejection: %#v", manager.events)
			}
			afterEntries, err := os.ReadDir(fixture.build.paths.buildRoot)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeEntries, afterEntries) {
				t.Fatalf("build cohort changed on alias rejection: before=%#v after=%#v", beforeEntries, afterEntries)
			}
		})
	}
}

func TestServiceCutoverReceiptTerminalValuesAndErrorsStayPrivate(t *testing.T) {
	fixturePath := filepath.Join(t.TempDir(), "fixture-secret-path")
	markers := []string{
		fixturePath, "SELECT secret FROM private_table", "raw-payload-secret", "query-secret",
		"legacy-search-1", "legacy-evidence-1", "canonical-action-1",
	}
	checks := []struct {
		name string
		make func() ServiceCutoverReceipt
	}{
		{name: "applied", make: func() ServiceCutoverReceipt { return validServiceCutoverReceiptForTest() }},
		{name: "rolled_back", make: func() ServiceCutoverReceipt {
			receipt := validServiceCutoverReceiptForTest()
			receipt.Status = CutoverStatusRolledBack
			receipt.CutoverTerminalStatus = CutoverStatusRolledBack
			receipt.ErrorCode = "service_receipt"
			receipt.FinalRunning = receipt.InitialRunning
			receipt.FinalRunning.RuntimeSHA256 = receipt.OldRuntimeSHA256
			return receipt
		}},
		{name: "rollback_failed", make: func() ServiceCutoverReceipt {
			receipt := validServiceCutoverReceiptForTest()
			receipt.Status = CutoverStatusRollbackFailed
			receipt.CutoverTerminalStatus = CutoverStatusRollbackFailed
			receipt.ErrorCode = CutoverStatusRollbackFailed
			receipt.FinalRunning = ServiceCutoverRunningEvidence{}
			return receipt
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			receipt := check.make()
			encoded, err := marshalServiceCutoverReceipt(receipt)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range markers {
				if strings.Contains(string(encoded), marker) {
					t.Fatalf("receipt leaked private marker %q: %s", marker, encoded)
				}
			}
			raw := errors.New(strings.Join(markers, " "))
			bounded := boundedServiceCutoverError(raw)
			if bounded == nil {
				t.Fatal("bounded error was nil")
			}
			for _, marker := range markers {
				if strings.Contains(bounded.Error(), marker) {
					t.Fatalf("bounded error leaked private marker %q: %v", marker, bounded)
				}
			}
		})
	}
}

func TestExecuteServiceCutoverWithReceiptBlocksBeforeMutation(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	manager.runningEvidence = []cutoverServiceRunningEvidence{{Owner: 1, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1, ListenerOwned: 1, Readiness: 0, RuntimeSHA256: oldSHA}}
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	options.ServiceReceipt = filepath.Join(filepath.Dir(options.build.CutoverReceipt), "service.json")
	receipt, err := executeServiceCutoverWithReceipt(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusBlocked {
		t.Fatalf("blocked service operation = %#v, err=%v", receipt, err)
	}
	if _, err := readServiceCutoverReceipt(options.ServiceReceipt); err != nil {
		t.Fatalf("blocked service receipt unreadable: %v", err)
	}
	if len(manager.events) != 1 || manager.events[0] != "verify_running" {
		t.Fatalf("service mutated before initial proof: %#v", manager.events)
	}
}

func TestExecuteServiceCutoverWithReceiptAppliedBindsD2C(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	options.ServiceReceipt = filepath.Join(filepath.Dir(options.build.CutoverReceipt), "service.json")
	receipt, err := executeServiceCutoverWithReceipt(context.Background(), options)
	if err != nil || receipt.Status != CutoverStatusApplied {
		t.Fatalf("applied service operation = %#v, err=%v", receipt, err)
	}
	if err := validateServiceCutoverReceipt(receipt); err != nil {
		t.Fatalf("applied service receipt invalid: %v", err)
	}
	onDisk, err := readServiceCutoverReceipt(options.ServiceReceipt)
	if err != nil || !reflect.DeepEqual(onDisk, receipt) {
		t.Fatalf("on-disk service receipt = %#v, err=%v, returned=%#v", onDisk, err, receipt)
	}
	subreceipt, err := readCutoverReceipt(options.build.CutoverReceipt)
	if err != nil || subreceipt.Status != CutoverStatusApplied || receipt.CutoverSubreceiptSHA256 != mustFileSHA256(t, options.build.CutoverReceipt) {
		t.Fatalf("D2c subreceipt binding = %#v, err=%v", subreceipt, err)
	}
	if receipt.CutoverSubreceiptStatus != CutoverStatusApplied || receipt.CutoverTerminalStatus != CutoverStatusApplied {
		t.Fatalf("D2c status projection = %#v", receipt)
	}
	if !serviceQuiesceProjectionValid(receipt.ActiveSourcesQuiesced) {
		t.Fatalf("active source quiesce projection = %#v", receipt.ActiveSourcesQuiesced)
	}
}

func TestExecuteServiceCutoverWithReceiptQuiesceFailureIsBlockedAndRecovered(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	manager := newCutoverServiceManager(oldSHA, mustFileSHA256(t, fixture.build.paths.stagedRuntime))
	manager.startRuntimeSHA = []string{oldSHA}
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	options.ServiceReceipt = filepath.Join(filepath.Dir(options.build.CutoverReceipt), "service.json")
	original := cutoverServiceQuiesceActive
	cutoverServiceQuiesceActive = func(context.Context, cutoverActiveOptions) (cutoverActiveQuiesceEvidence, error) {
		return cutoverActiveQuiesceEvidence{}, newCodedError("active_quiesce", "fixture is busy")
	}
	t.Cleanup(func() { cutoverServiceQuiesceActive = original })

	receipt, err := executeServiceCutoverWithReceipt(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "active_quiesce" {
		t.Fatalf("quiesce blocked receipt = %#v, err=%v", receipt, err)
	}
	if !serviceQuiesceProjectionZero(receipt.ActiveSourcesQuiesced) || !serviceRunningProjectionMatches(receipt.FinalRunning, oldSHA) {
		t.Fatalf("quiesce blocked receipt overclaimed = %#v", receipt)
	}
	if err := validateServiceCutoverReceipt(receipt); err != nil {
		t.Fatalf("quiesce blocked receipt invalid: %v", err)
	}
	onDisk, err := readServiceCutoverReceipt(options.ServiceReceipt)
	if err != nil || !reflect.DeepEqual(onDisk, receipt) {
		t.Fatalf("quiesce blocked receipt not durable: %#v, err=%v", onDisk, err)
	}
}

func TestExecuteServiceCutoverWithReceiptRollbackRetainsAppliedD2CSubreceipt(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	manager.startErrors = []error{errors.New("new readiness failed")}
	manager.startRuntimeSHA = []string{oldSHA}
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	options.ServiceReceipt = filepath.Join(filepath.Dir(options.build.CutoverReceipt), "service.json")
	before := cutoverActiveTestHashes(t, fixture.paths)
	receipt, err := executeServiceCutoverWithReceipt(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusRolledBack {
		t.Fatalf("rolled-back service operation = %#v, err=%v", receipt, err)
	}
	if err := validateServiceCutoverReceipt(receipt); err != nil {
		t.Fatalf("rolled-back service receipt invalid: %v", err)
	}
	if receipt.CutoverSubreceiptSHA256 == "" || receipt.CutoverSubreceiptStatus != CutoverStatusApplied || receipt.CutoverTerminalStatus != CutoverStatusRolledBack {
		t.Fatalf("D2c historical subreceipt projection = %#v", receipt)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatal("active cohort was not restored after readiness failure")
	}
	if _, err := readServiceCutoverReceipt(options.ServiceReceipt); err != nil {
		t.Fatalf("rolled-back service receipt not durable: %v", err)
	}
}

func TestExecuteServiceCutoverWithReceiptWriterFailureRollsBackAndPublishesTerminal(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	options.ServiceReceipt = filepath.Join(filepath.Dir(options.build.CutoverReceipt), "service.json")
	before := cutoverActiveTestHashes(t, fixture.paths)
	originalWriter := serviceCutoverReceiptWriter
	calls := 0
	serviceCutoverReceiptWriter = func(path string, receipt ServiceCutoverReceipt) (serviceCutoverReceiptWriteResult, error) {
		calls++
		if calls == 1 {
			return serviceCutoverReceiptWriteResult{}, errors.New("first receipt writer failure")
		}
		return originalWriter(path, receipt)
	}
	t.Cleanup(func() { serviceCutoverReceiptWriter = originalWriter })
	receipt, err := executeServiceCutoverWithReceipt(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusRolledBack || calls != 2 {
		t.Fatalf("writer failure operation = %#v, err=%v, calls=%d", receipt, err, calls)
	}
	if err := validateServiceCutoverReceipt(receipt); err != nil {
		t.Fatalf("rolled-back writer failure receipt invalid: %v", err)
	}
	if receipt.CutoverSubreceiptSHA256 == "" || receipt.CutoverSubreceiptStatus != CutoverStatusApplied || receipt.CutoverTerminalStatus != CutoverStatusRolledBack {
		t.Fatalf("D2c durable subreceipt was not retained: %#v", receipt)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatal("active cohort was not restored after receipt writer failure")
	}
	if _, err := readServiceCutoverReceipt(options.ServiceReceipt); err != nil {
		t.Fatalf("terminal service receipt missing: %v", err)
	}
}

func TestExecuteServiceCutoverWithReceiptPersistentWriterFailureIsRollbackFailed(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	options.ServiceReceipt = filepath.Join(filepath.Dir(options.build.CutoverReceipt), "service.json")
	before := cutoverActiveTestHashes(t, fixture.paths)
	originalWriter := serviceCutoverReceiptWriter
	serviceCutoverReceiptWriter = func(string, ServiceCutoverReceipt) (serviceCutoverReceiptWriteResult, error) {
		return serviceCutoverReceiptWriteResult{}, errors.New("persistent writer failure")
	}
	t.Cleanup(func() { serviceCutoverReceiptWriter = originalWriter })
	receipt, err := executeServiceCutoverWithReceipt(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusRollbackFailed {
		t.Fatalf("persistent writer operation = %#v, err=%v", receipt, err)
	}
	if err := validateServiceCutoverReceipt(receipt); err != nil {
		t.Fatalf("rollback-failed receipt invalid: %v", err)
	}
	if receipt.FinalRunning != (ServiceCutoverRunningEvidence{}) || receipt.CutoverTerminalStatus == CutoverStatusRolledBack {
		t.Fatalf("rollback-failed receipt claims restoration: %#v", receipt)
	}
	if _, statErr := os.Lstat(options.ServiceReceipt); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("persistent writer left service receipt: %v", statErr)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatal("active cohort was not restored after persistent writer failure")
	}
}

func TestExecuteServiceCutoverWithReceiptCancellationAfterPublishRollsBack(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	oldSHA := mustFileSHA256(t, fixture.build.paths.installedRuntime)
	newSHA := mustFileSHA256(t, fixture.build.paths.stagedRuntime)
	manager := newCutoverServiceManager(oldSHA, newSHA)
	options := cutoverServiceOptionsForFixture(t, fixture, manager)
	options.ServiceReceipt = filepath.Join(filepath.Dir(options.build.CutoverReceipt), "service.json")
	before := cutoverActiveTestHashes(t, fixture.paths)
	ctx, cancel := context.WithCancel(context.Background())
	originalWriter := serviceCutoverReceiptWriter
	calls := 0
	serviceCutoverReceiptWriter = func(path string, receipt ServiceCutoverReceipt) (serviceCutoverReceiptWriteResult, error) {
		calls++
		result, err := originalWriter(path, receipt)
		if calls == 1 {
			cancel()
		}
		return result, err
	}
	t.Cleanup(func() { serviceCutoverReceiptWriter = originalWriter })
	receipt, err := executeServiceCutoverWithReceipt(ctx, options)
	if err == nil || receipt.Status != CutoverStatusRolledBack || calls != 2 {
		t.Fatalf("cancellation after receipt publication = %#v, err=%v, calls=%d", receipt, err, calls)
	}
	if err := validateServiceCutoverReceipt(receipt); err != nil {
		t.Fatalf("canceled rolled-back receipt invalid: %v", err)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatal("active cohort was not restored after cancellation")
	}
}

func writeServiceCutoverReceiptFreshOnlyMustFail(path string, receipt ServiceCutoverReceipt) error {
	_, err := writeServiceCutoverReceipt(path, receipt)
	return err
}

func bytesRepeat(value byte, count int) []byte {
	return []byte(strings.Repeat(string([]byte{value}), count))
}

func runtimeGOOSNotWindows() bool {
	return os.PathSeparator == '/'
}
