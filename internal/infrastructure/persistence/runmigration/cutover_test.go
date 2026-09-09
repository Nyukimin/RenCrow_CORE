package runmigration

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeRunCutoverService struct {
	events   []string
	stopped  bool
	restore  bool
	stopHook func()
}

func (s *fakeRunCutoverService) StopAndVerify(context.Context, string, string) (RunCutoverServiceEvidence, error) {
	s.events = append(s.events, "stop_verify")
	s.stopped = true
	if s.stopHook != nil {
		s.stopHook()
	}
	return RunCutoverServiceEvidence{Owner: 1, Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1, RuntimeSHA256: testSHA("runtime")}, nil
}

func TestRunCutoverRejectsActiveDriftAfterWriterStop(t *testing.T) {
	fixture := newRunCutoverFixture(t)
	service := &fakeRunCutoverService{stopHook: func() {
		_ = os.WriteFile(fixture.activePaths["word.json"], []byte("writer drift\n"), 0o600)
	}}
	withRunCutoverSeams(t, service, nil)

	receipt, err := Cutover(context.Background(), fixture.options)
	if err == nil || receipt.Status != RunCutoverBlocked || receipt.ErrorCode != "active_drift" {
		t.Fatalf("stopped drift = %#v err=%v", receipt, err)
	}
	if !reflect.DeepEqual(service.events, []string{"stop_verify", "restore"}) || !service.restore {
		t.Fatalf("service recovery = %#v", service.events)
	}
	if _, err := os.Stat(fixture.options.RollbackDir); !os.IsNotExist(err) {
		t.Fatalf("rollback created before stopped revalidation: %v", err)
	}
}

func (s *fakeRunCutoverService) Restore(context.Context, string) error {
	s.events = append(s.events, "restore")
	s.restore = true
	s.stopped = false
	return nil
}

func TestRunCutoverAppliesFixedCohortAndRetainsRecoveryEvidence(t *testing.T) {
	fixture := newRunCutoverFixture(t)
	service := &fakeRunCutoverService{}
	withRunCutoverSeams(t, service, nil)

	receipt, err := Cutover(context.Background(), fixture.options)
	if err != nil || receipt.Status != RunCutoverApplied {
		t.Fatalf("cutover = %#v err=%v", receipt, err)
	}
	if !reflect.DeepEqual(service.events, []string{"stop_verify"}) || !service.stopped || service.restore {
		t.Fatalf("service lifecycle = %#v stopped=%v restored=%v", service.events, service.stopped, service.restore)
	}
	if receipt.QuarantineMarker != "retain_and_quarantine/v1" || receipt.QuarantinedEvents != 5 || receipt.QuarantinedTaskIDs != 2 {
		t.Fatalf("quarantine binding = %#v", receipt)
	}
	for role, active := range fixture.activePaths {
		got, readErr := os.ReadFile(active)
		if readErr != nil || string(got) != string(fixture.outputBytes[role]) {
			t.Fatalf("active %s = %q err=%v", role, got, readErr)
		}
		rollback := filepath.Join(fixture.options.RollbackDir, filepath.FromSlash(role))
		old, readErr := os.ReadFile(rollback)
		if readErr != nil || string(old) != string(fixture.activeBytes[role]) {
			t.Fatalf("rollback %s = %q err=%v", role, old, readErr)
		}
	}
	if _, err := os.Stat(fixture.options.CutoverReceipt); err != nil {
		t.Fatalf("durable receipt missing: %v", err)
	}
	if source, err := os.ReadFile(filepath.Join(fixture.options.Snapshot, "tasks.source")); err != nil || string(source) != string(fixture.activeBytes["tasks/task_state.jsonl"]) {
		t.Fatalf("source changed: %q err=%v", source, err)
	}
}

func TestRunCutoverRollsBackAllAppliedFilesAndRestoresService(t *testing.T) {
	fixture := newRunCutoverFixture(t)
	service := &fakeRunCutoverService{}
	withRunCutoverSeams(t, service, func(index int) error {
		if index == 3 {
			return os.ErrPermission
		}
		return nil
	})

	receipt, err := Cutover(context.Background(), fixture.options)
	if err == nil || receipt.Status != RunCutoverRolledBack {
		t.Fatalf("cutover failure = %#v err=%v", receipt, err)
	}
	if !reflect.DeepEqual(service.events, []string{"stop_verify", "restore"}) || !service.restore {
		t.Fatalf("service recovery = %#v", service.events)
	}
	for role, active := range fixture.activePaths {
		got, readErr := os.ReadFile(active)
		if readErr != nil || string(got) != string(fixture.activeBytes[role]) {
			t.Fatalf("active rollback %s = %q err=%v", role, got, readErr)
		}
	}
}

func TestRunCutoverRollsBackWhenDurableReceiptCannotBeWritten(t *testing.T) {
	fixture := newRunCutoverFixture(t)
	service := &fakeRunCutoverService{}
	withRunCutoverSeams(t, service, nil)
	oldWriter := runCutoverReceiptWriter
	runCutoverReceiptWriter = func(string, RunCutoverReceipt) error { return os.ErrPermission }
	t.Cleanup(func() { runCutoverReceiptWriter = oldWriter })

	receipt, err := Cutover(context.Background(), fixture.options)
	if err == nil || receipt.Status != RunCutoverRolledBack || receipt.ErrorCode != "receipt_write" {
		t.Fatalf("receipt failure = %#v err=%v", receipt, err)
	}
	if !reflect.DeepEqual(service.events, []string{"stop_verify", "restore"}) || !service.restore {
		t.Fatalf("service recovery = %#v", service.events)
	}
	for role, active := range fixture.activePaths {
		got, readErr := os.ReadFile(active)
		if readErr != nil || string(got) != string(fixture.activeBytes[role]) {
			t.Fatalf("active rollback %s = %q err=%v", role, got, readErr)
		}
	}
}

func TestRunCutoverRejectsStaleSnapshotEvenWithFreshActiveManifest(t *testing.T) {
	fixture := newRunCutoverFixture(t)
	role := "word.json"
	updated := []byte("new production write\n")
	if err := os.WriteFile(fixture.activePaths[role], updated, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.options.Active.Files[role] = digest(updated)
	activeJSON, err := canonicalJSON(fixture.options.Active)
	if err != nil {
		t.Fatal(err)
	}
	fixture.options.ExpectedActiveManifestSHA256 = digest(activeJSON)
	service := &fakeRunCutoverService{}
	withRunCutoverSeams(t, service, nil)

	receipt, err := Cutover(context.Background(), fixture.options)
	if err == nil || receipt.Status != RunCutoverBlocked || receipt.ErrorCode != "active_snapshot_mismatch" {
		t.Fatalf("stale snapshot = %#v err=%v", receipt, err)
	}
	if len(service.events) != 0 {
		t.Fatalf("service stopped for stale snapshot: %#v", service.events)
	}
}

func TestRunCutoverRejectsReceiptOrActiveDriftBeforeStoppingService(t *testing.T) {
	for _, mutate := range []func(*runCutoverFixture){
		func(f *runCutoverFixture) { f.options.PlanReceipt.Counts["quarantined_events"]++ },
		func(f *runCutoverFixture) {
			_ = os.WriteFile(f.activePaths[runCutoverRoles[0]], []byte("drift\n"), 0o600)
		},
	} {
		fixture := newRunCutoverFixture(t)
		mutate(&fixture)
		service := &fakeRunCutoverService{}
		withRunCutoverSeams(t, service, nil)
		receipt, err := Cutover(context.Background(), fixture.options)
		if err == nil || receipt.Status != RunCutoverBlocked || len(service.events) != 0 {
			t.Fatalf("preflight = %#v err=%v events=%#v", receipt, err, service.events)
		}
	}
}

type runCutoverFixture struct {
	options     CutoverOptions
	activePaths map[string]string
	activeBytes map[string][]byte
	outputBytes map[string][]byte
}

func newRunCutoverFixture(t *testing.T) runCutoverFixture {
	t.Helper()
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	cohort := filepath.Join(root, "cohort")
	activeRoot := filepath.Join(root, "active")
	if err := os.MkdirAll(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	inventory := Inventory{
		SchemaVersion: Schema, SnapshotAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
		Files: map[string]string{}, Roles: map[string]string{},
	}
	plan := Receipt{
		SchemaVersion: Schema, Status: "quarantined", QuarantineMarker: "retain_and_quarantine/v1",
		Counts: map[string]int{"quarantined_events": 5, "quarantined_task_ids": 2}, Outputs: map[string]string{},
	}
	activeManifest := RunCutoverActiveManifest{SchemaVersion: RunCutoverSchema, Files: map[string]string{}}
	activeManifest.TasksDir = filepath.Join(activeRoot, "tasks")
	activeManifest.EventStore = filepath.Join(activeRoot, "event_store.db")
	activeManifest.OpsDir = filepath.Join(activeRoot, "ops")
	activeManifest.SessionDir = filepath.Join(activeRoot, "sessions")
	fixture := runCutoverFixture{activePaths: map[string]string{}, activeBytes: map[string][]byte{}, outputBytes: map[string][]byte{}}
	resolvedPaths, err := resolveRunCutoverActivePathsForFixture(activeManifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range runCutoverRoles {
		output := []byte("new:" + role + "\n")
		old := []byte("old:" + role + "\n")
		outputPath := filepath.Join(cohort, filepath.FromSlash(role))
		activePath := resolvedPaths[role]
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(activePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outputPath, output, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(activePath, old, 0o600); err != nil {
			t.Fatal(err)
		}
		sourceRole := runCutoverSourceRole(role)
		sourcePath := sourceRole + ".source"
		if err := os.WriteFile(filepath.Join(snapshot, sourcePath), old, 0o600); err != nil {
			t.Fatal(err)
		}
		inventory.Roles[sourceRole] = sourcePath
		inventory.Files[sourcePath] = digest(old)
		plan.Outputs[role] = digest(output)
		activeManifest.Files[role] = digest(old)
		fixture.activePaths[role] = activePath
		fixture.activeBytes[role] = old
		fixture.outputBytes[role] = output
	}
	plan.Outputs["tasks/.jsonlbatch.lock"] = digest(nil)
	if err := os.WriteFile(filepath.Join(cohort, "tasks", ".jsonlbatch.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	encodedInventory, err := canonicalJSON(inventory)
	if err != nil {
		t.Fatal(err)
	}
	plan.InventorySHA256 = digest(encodedInventory)
	fixture.options = CutoverOptions{
		Cohort: cohort, Snapshot: snapshot, Inventory: inventory, PlanReceipt: plan,
		Active: activeManifest, RollbackDir: filepath.Join(root, "rollback"),
		CutoverReceipt: filepath.Join(root, "cutover-receipt.json"), InstalledRuntime: filepath.Join(root, "rencrow"),
		ExpectedRuntimeSHA256: testSHA("runtime"), ActiveConfig: filepath.Join(root, "core.yaml"),
	}
	planJSON, err := canonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	activeJSON, err := canonicalJSON(activeManifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.options.ExpectedPlanReceiptSHA256 = digest(planJSON)
	fixture.options.ExpectedActiveManifestSHA256 = digest(activeJSON)
	if err := os.WriteFile(fixture.options.InstalledRuntime, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.options.ActiveConfig, []byte("config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func resolveRunCutoverActivePathsForFixture(active RunCutoverActiveManifest) (map[string]string, error) {
	files := make(map[string]string, len(runCutoverRoles))
	for _, role := range runCutoverRoles {
		files[role] = testSHA("placeholder")
	}
	active.Files = files
	return resolveRunCutoverActivePaths(active)
}

func testSHA(value string) string { return digest([]byte(value)) }

func withRunCutoverSeams(t *testing.T, service runCutoverService, replaceFailure func(int) error) {
	t.Helper()
	oldFactory, oldHook := runCutoverServiceFactory, runCutoverReplaceHook
	runCutoverServiceFactory = func(string, string) (runCutoverService, error) { return service, nil }
	runCutoverReplaceHook = replaceFailure
	t.Cleanup(func() {
		runCutoverServiceFactory = oldFactory
		runCutoverReplaceHook = oldHook
	})
}
