package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecuteCutoverRequiresImplementation(t *testing.T) {
	if _, err := executeCutover(context.Background(), preparedCutoverPreflight{}, preparedCutoverRestoreStages{}); err == nil {
		t.Fatal("empty cutover execution unexpectedly succeeded")
	}
}

func TestExecuteCutoverSuccessAndPrivateRollback(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	beforeActive := cutoverActiveTestHashes(t, fixture.paths)
	beforeRuntime := mustFileSHA256(t, preflight.active.build.paths.installedRuntime)
	result, err := executeCutover(context.Background(), preflight, restore)
	if err != nil {
		t.Fatalf("executeCutover() error = %v code=%s receipt=%#v", err, errorCode(err, ""), result.receipt)
	}
	if result.applied == nil || result.receipt.Status != CutoverStatusApplied || result.receipt.ErrorCode != "" {
		t.Fatalf("applied result = %#v", result)
	}
	if err := validateCutoverReceipt(result.receipt); err != nil {
		t.Fatalf("applied receipt validation error = %v", err)
	}
	encodedApplied, err := marshalCutoverReceipt(result.receipt)
	if err != nil || len(encodedApplied) == 0 || int64(len(encodedApplied)) > maxCutoverReceiptBytes {
		t.Fatalf("applied receipt marshal = %d bytes, err=%v", len(encodedApplied), err)
	}
	for _, private := range []string{fixture.paths.dci, fixture.paths.dciJSONL, fixture.paths.eventStore, fixture.paths.l1, fixture.paths.archive, fixture.build.paths.buildRoot, "legacy-search-1", "legacy-evidence-1", "raw-error-secret"} {
		if strings.Contains(string(encodedApplied), private) {
			t.Fatalf("applied receipt leaked private value %q", private)
		}
	}
	buildHash := preflight.active.build.buildReceipt.SourceDatabaseLogicalSHA256["source_dci"]
	result.receipt.SourceDatabaseLogicalSHA256["source_dci"] = "bad"
	if preflight.active.build.buildReceipt.SourceDatabaseLogicalSHA256["source_dci"] != buildHash {
		t.Fatal("applied receipt mutation changed the build receipt map")
	}
	result.receipt.SourceDatabaseLogicalSHA256["source_dci"] = buildHash
	if result.receipt.ActiveBeforeArtifactSetSHA256 == "" || result.receipt.ActiveAfterArtifactSetSHA256 == "" || result.receipt.JSONLRetired != 1 {
		t.Fatalf("applied receipt claims = %#v", result.receipt)
	}
	for role, activePath := range map[string]string{
		buildOutputDCIRole: fixture.paths.dci, buildOutputEventStoreRole: fixture.paths.eventStore,
		buildOutputL1Role: fixture.paths.l1, buildOutputArchiveRole: fixture.paths.archive,
	} {
		artifact := preflight.active.build.buildReceipt.OutputArtifacts[role]
		got := mustFileSHA256(t, activePath)
		info, err := os.Lstat(activePath)
		if err != nil {
			t.Fatal(err)
		}
		if got != artifact.FileSHA256 || info.Size() != artifact.Bytes {
			t.Fatalf("active output %s = %q, want %q", role, got, artifact.FileSHA256)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("active output %s mode = %o", role, info.Mode().Perm())
		}
	}
	if got := mustFileSHA256(t, fixture.paths.dci); got != preflight.active.build.buildReceipt.OutputArtifacts[buildOutputDCIRole].FileSHA256 {
		t.Fatalf("active DCI hash = %q", got)
	}
	if got := mustFileSHA256(t, preflight.active.build.paths.stagedRuntime); result.receipt.NewRuntimeSHA256 != got {
		t.Fatalf("new runtime hash = %q, want %q", result.receipt.NewRuntimeSHA256, got)
	}
	if _, err := os.Lstat(fixture.paths.dciJSONL); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active JSONL remains: %v", err)
	}
	if _, err := os.Lstat(preflight.retiredJSONL); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired JSONL remains: %v", err)
	}
	for _, file := range restore.files {
		if _, err := os.Lstat(file.target.path); err != nil {
			t.Fatalf("restore stage %s missing after applied cutover: %v", file.role, err)
		}
	}
	if _, err := os.Lstat(preflight.active.build.paths.cutoverReceipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cutover receipt was written: %v", err)
	}

	drifted, err := os.OpenFile(fixture.paths.dci, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drifted.Write([]byte("owner-write")); err != nil {
		_ = drifted.Close()
		t.Fatal(err)
	}
	if err := drifted.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rolledBack, err := rollbackAppliedCutover(ctx, result.applied)
	if err != nil {
		t.Fatalf("rollbackAppliedCutover() error = %v", err)
	}
	if rolledBack.Status != CutoverStatusRolledBack || rolledBack.RestoredArtifactSetSHA256 != rolledBack.ActiveBeforeArtifactSetSHA256 || rolledBack.JSONLRestored != 1 {
		t.Fatalf("rolled-back receipt = %#v", rolledBack)
	}
	if err := validateCutoverReceipt(rolledBack); err != nil {
		t.Fatalf("rolled-back receipt validation error = %v", err)
	}
	encodedRollback, err := marshalCutoverReceipt(rolledBack)
	if err != nil || len(encodedRollback) == 0 || int64(len(encodedRollback)) > maxCutoverReceiptBytes {
		t.Fatalf("rolled-back receipt marshal = %d bytes, err=%v", len(encodedRollback), err)
	}
	for _, private := range []string{fixture.paths.dci, fixture.paths.dciJSONL, fixture.paths.eventStore, fixture.paths.l1, fixture.paths.archive, fixture.build.paths.buildRoot, "legacy-search-1", "legacy-evidence-1", "raw-error-secret"} {
		if strings.Contains(string(encodedRollback), private) {
			t.Fatalf("rolled-back receipt leaked private value %q", private)
		}
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(beforeActive, got) {
		t.Fatalf("active cohort was not restored: before=%#v after=%#v", beforeActive, got)
	}
	if got := mustFileSHA256(t, preflight.active.build.paths.installedRuntime); got != beforeRuntime {
		t.Fatalf("installed runtime was not restored: %q != %q", got, beforeRuntime)
	}
	if _, err := os.Lstat(fixture.paths.dciJSONL); err != nil {
		t.Fatalf("active JSONL was not restored: %v", err)
	}
	assertCutoverRestoreStagesAbsent(t, preflight)
}

func TestRollbackAppliedCutoverRejectsUnsafeCurrentBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture cutoverActiveCohortTestFixture, state *cutoverAppliedState)
	}{
		{name: "different inode", mutate: func(t *testing.T, fixture cutoverActiveCohortTestFixture, _ *cutoverAppliedState) {
			copyPath := filepath.Join(t.TempDir(), "substitute.db")
			originalInfo, err := os.Lstat(fixture.paths.dci)
			if err != nil {
				t.Fatal(err)
			}
			copyFile(t, fixture.paths.dci, copyPath)
			substituteInfo, err := os.Lstat(copyPath)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(originalInfo, substituteInfo) {
				t.Fatal("substitute unexpectedly aliases the pre-mutation active file")
			}
			if err := os.Remove(fixture.paths.dci); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(copyPath, fixture.paths.dci); err != nil {
				t.Fatal(err)
			}
			activeInfo, err := os.Lstat(fixture.paths.dci)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(activeInfo, substituteInfo) || os.SameFile(activeInfo, originalInfo) {
				t.Fatal("renamed active file did not preserve a distinct substitute inode")
			}
		}},
		{name: "wrong mode", mutate: func(t *testing.T, fixture cutoverActiveCohortTestFixture, _ *cutoverAppliedState) {
			if err := os.Chmod(fixture.paths.dci, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "SQLite sidecar", mutate: func(t *testing.T, fixture cutoverActiveCohortTestFixture, _ *cutoverAppliedState) {
			if err := os.WriteFile(fixture.paths.dci+sqliteSidecarSuffixes[0], []byte("sidecar-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "modified runtime", mutate: func(t *testing.T, fixture cutoverActiveCohortTestFixture, _ *cutoverAppliedState) {
			file, err := os.OpenFile(fixture.build.paths.installedRuntime, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte("sqlite-owner-update")); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "JSONL present", mutate: func(t *testing.T, fixture cutoverActiveCohortTestFixture, _ *cutoverAppliedState) {
			if err := os.WriteFile(fixture.paths.dciJSONL, []byte("jsonl-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preflight, fixture, result := executeCutoverFixture(t)
			tt.mutate(t, fixture, result.applied)
			beforeRuntime := mustFileSHA256(t, preflight.active.build.paths.installedRuntime)
			receipt, err := rollbackAppliedCutover(context.Background(), result.applied)
			if err == nil || receipt.SchemaVersion != "" {
				t.Fatalf("unsafe %s rollback = %#v, err=%v", tt.name, receipt, err)
			}
			if got := mustFileSHA256(t, preflight.active.build.paths.installedRuntime); got != beforeRuntime {
				t.Fatalf("unsafe %s changed runtime despite rejected rollback", tt.name)
			}
		})
	}
}

func TestRollbackAppliedCutoverRejectsUnknownSymlinkAndHardlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and hardlink substitutions are Unix-specific")
	}
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			_, fixture, result := executeCutoverFixture(t)
			if err := os.Remove(fixture.paths.dci); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(fixture.paths.l1, fixture.paths.dci); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Link(fixture.paths.l1, fixture.paths.dci); err != nil {
				t.Fatal(err)
			}
			if _, err := rollbackAppliedCutover(context.Background(), result.applied); err == nil {
				t.Fatal("unsafe substituted target was accepted")
			}
			info, err := os.Lstat(fixture.paths.dci)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("symlink substitution was removed")
				}
			} else {
				l1Info, err := os.Lstat(fixture.paths.l1)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(info, l1Info) {
					t.Fatal("hardlink substitution was removed")
				}
			}
		})
	}
}

func TestExecuteCutoverRollsBackAfterEveryForwardBoundary(t *testing.T) {
	roles := []string{"replacement_dci", "replacement_event_store", "replacement_l1", "replacement_archive", "replacement_runtime", "retire_jsonl"}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			preflight, fixture := newCutoverRestorePreflightFixture(t)
			restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
			if err != nil {
				t.Fatal(err)
			}
			before := cutoverActiveTestHashes(t, fixture.paths)
			beforeRuntime := mustFileSHA256(t, preflight.active.build.paths.installedRuntime)
			original := cutoverExecuteForwardReplace
			cutoverExecuteForwardReplace = func(got, source, target string) error {
				if err := original(got, source, target); err != nil {
					return err
				}
				if got == role {
					return errors.New("injected mutate-then-error")
				}
				return nil
			}
			t.Cleanup(func() { cutoverExecuteForwardReplace = original })
			result, err := executeCutover(context.Background(), preflight, restore)
			if err == nil || result.receipt.Status != CutoverStatusRolledBack {
				t.Fatalf("forward %s result = %#v, err=%v", role, result.receipt, err)
			}
			if receiptErr := validateCutoverReceipt(result.receipt); receiptErr != nil {
				t.Fatalf("rolled-back receipt validation error = %v", receiptErr)
			}
			if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
				t.Fatalf("active files not restored after %s: before=%#v after=%#v", role, before, got)
			}
			if got := mustFileSHA256(t, preflight.active.build.paths.installedRuntime); got != beforeRuntime {
				t.Fatalf("runtime not restored after %s", role)
			}
			if _, err := os.Lstat(fixture.paths.dciJSONL); err != nil {
				t.Fatalf("JSONL not restored after %s: %v", role, err)
			}
			assertCutoverRestoreStagesAbsent(t, preflight)
			assertCutoverReplacementStagesAbsent(t, preflight)
		})
	}
}

func TestExecuteCutoverJSONLAndFinalVerificationFailuresRollback(t *testing.T) {
	tests := []struct {
		name       string
		wantStatus string
		setup      func(t *testing.T)
	}{
		{name: "remove", wantStatus: CutoverStatusRolledBack, setup: func(t *testing.T) {
			original := cutoverExecuteRemoveFile
			cutoverExecuteRemoveFile = func(role string, binding cutoverBoundFile) error {
				if role == "retire_jsonl" {
					if err := original(role, binding); err != nil {
						return err
					}
					return errors.New("injected JSONL remove error")
				}
				return original(role, binding)
			}
			t.Cleanup(func() { cutoverExecuteRemoveFile = original })
		}},
		{name: "directory sync", wantStatus: CutoverStatusRolledBack, setup: func(t *testing.T) {
			original := cutoverExecuteSyncDirectory
			cutoverExecuteSyncDirectory = func(role, path string) error {
				if role == "jsonl_retire" {
					return errors.New("injected JSONL sync error")
				}
				return original(role, path)
			}
			t.Cleanup(func() { cutoverExecuteSyncDirectory = original })
		}},
		{name: "directory sync after JSONL removal", wantStatus: CutoverStatusRolledBack, setup: func(t *testing.T) {
			original := cutoverExecuteSyncDirectory
			cutoverExecuteSyncDirectory = func(role, path string) error {
				if role == "jsonl_remove" {
					return errors.New("injected post-remove directory sync error")
				}
				return original(role, path)
			}
			t.Cleanup(func() { cutoverExecuteSyncDirectory = original })
		}},
		{name: "final verify", wantStatus: CutoverStatusRolledBack, setup: func(t *testing.T) {
			original := cutoverExecuteFinalVerify
			cutoverExecuteFinalVerify = func(context.Context, cutoverExecutionVerification) error {
				return errors.New("injected final verification error")
			}
			t.Cleanup(func() { cutoverExecuteFinalVerify = original })
		}},
		{name: "JSONL restore mutate then error", wantStatus: CutoverStatusRollbackFailed, setup: func(t *testing.T) {
			originalFinal := cutoverExecuteFinalVerify
			cutoverExecuteFinalVerify = func(context.Context, cutoverExecutionVerification) error {
				return errors.New("injected post-retirement verification error")
			}
			originalRollback := cutoverExecuteRollbackReplace
			cutoverExecuteRollbackReplace = func(role, source, target string) error {
				if err := originalRollback(role, source, target); err != nil {
					return err
				}
				if role == "restore_dci_jsonl" {
					return errors.New("injected JSONL restore durability error")
				}
				return nil
			}
			t.Cleanup(func() {
				cutoverExecuteFinalVerify = originalFinal
				cutoverExecuteRollbackReplace = originalRollback
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preflight, fixture := newCutoverRestorePreflightFixture(t)
			restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
			if err != nil {
				t.Fatal(err)
			}
			before := cutoverActiveTestHashes(t, fixture.paths)
			tt.setup(t)
			result, err := executeCutover(context.Background(), preflight, restore)
			if err == nil || result.receipt.Status != tt.wantStatus {
				t.Fatalf("failure result = %#v, err=%v", result.receipt, err)
			}
			if receiptErr := validateCutoverReceipt(result.receipt); receiptErr != nil {
				t.Fatalf("receipt validation error = %v", receiptErr)
			}
			if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
				t.Fatalf("active files not restored: before=%#v after=%#v", before, got)
			}
			assertCutoverRestoreStagesAbsent(t, preflight)
		})
	}
}

func TestExecuteCutoverCancellationAfterMutationRollsBackWithoutCancel(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	before := cutoverActiveTestHashes(t, fixture.paths)
	ctx, cancel := context.WithCancel(context.Background())
	original := cutoverExecuteAfterForwardReplace
	cutoverExecuteAfterForwardReplace = func(role string) error {
		if role == "replacement_dci" {
			cancel()
		}
		return original(role)
	}
	t.Cleanup(func() { cutoverExecuteAfterForwardReplace = original })
	result, err := executeCutover(ctx, preflight, restore)
	if err == nil || result.receipt.Status != CutoverStatusRolledBack {
		t.Fatalf("canceled cutover result = %#v, err=%v", result.receipt, err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("test context was not canceled")
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatalf("active files not restored after cancellation")
	}
	assertCutoverRestoreStagesAbsent(t, preflight)
}

func TestExecuteCutoverRejectsPreMutationDriftAndCleansRestoreStages(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	stage, ok := findCutoverStageRole(preflight.staged.stageFiles, "replacement_dci")
	if !ok {
		t.Fatal("replacement stage missing")
	}
	if err := os.WriteFile(stage.target.path, append(mustReadFile(t, stage.target.path), 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	before := cutoverActiveTestHashes(t, fixture.paths)
	result, err := executeCutover(context.Background(), preflight, restore)
	if err == nil || result.receipt.Status != CutoverStatusBlocked {
		t.Fatalf("drift result = %#v, err=%v", result.receipt, err)
	}
	if result.receipt.ActiveBeforeArtifactSetSHA256 != "" || result.receipt.ActiveAfterArtifactSetSHA256 != "" {
		t.Fatalf("blocked receipt claims mutation: %#v", result.receipt)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatalf("active files changed on blocked drift")
	}
	assertCutoverRestoreStagesAbsent(t, preflight)
}

func TestExecuteCutoverLaterBoundaryDriftRollsBackAfterMutation(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	before := cutoverActiveTestHashes(t, fixture.paths)
	original := cutoverExecuteAfterForwardReplace
	cutoverExecuteAfterForwardReplace = func(role string) error {
		if role == "replacement_dci" {
			if err := os.Remove(fixture.paths.eventStore); err != nil {
				t.Fatal(err)
			}
		}
		return original(role)
	}
	t.Cleanup(func() { cutoverExecuteAfterForwardReplace = original })

	result, err := executeCutover(context.Background(), preflight, restore)
	if err == nil || result.receipt.Status != CutoverStatusRolledBack {
		t.Fatalf("later-boundary drift result = %#v, err=%v; drift after first replace must roll back", result.receipt, err)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatalf("active cohort was not restored after later-boundary drift: before=%#v after=%#v", before, got)
	}
	assertCutoverRestoreStagesAbsent(t, preflight)
	assertCutoverReplacementStagesAbsent(t, preflight)
}

func TestExecuteCutoverRejectsHardlinkActiveTargetDuringRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hardlink replacement test is Unix-specific")
	}
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	l1Info, err := os.Lstat(fixture.paths.l1)
	if err != nil {
		t.Fatal(err)
	}
	original := cutoverExecuteAfterForwardReplace
	cutoverExecuteAfterForwardReplace = func(role string) error {
		if role == "replacement_dci" {
			if err := os.Remove(fixture.paths.eventStore); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(fixture.paths.l1, fixture.paths.eventStore); err != nil {
				t.Fatal(err)
			}
		}
		return original(role)
	}
	t.Cleanup(func() { cutoverExecuteAfterForwardReplace = original })

	result, err := executeCutover(context.Background(), preflight, restore)
	if err == nil || result.receipt.Status != CutoverStatusRollbackFailed {
		t.Fatalf("hardlink target result = %#v, err=%v; unknown target must not be overwritten", result.receipt, err)
	}
	info, err := os.Lstat(fixture.paths.eventStore)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, l1Info) {
		t.Fatal("unknown hardlink target was not preserved")
	}
}

func TestExecuteCutoverRollbackFailureContinuesAllRestoreRoles(t *testing.T) {
	roles := []string{"restore_dci", "restore_event_store", "restore_l1", "restore_archive", "restore_runtime", "restore_dci_jsonl"}
	for _, failedRole := range roles {
		t.Run(failedRole, func(t *testing.T) {
			preflight, _ := newCutoverRestorePreflightFixture(t)
			restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
			if err != nil {
				t.Fatal(err)
			}
			originalAfter := cutoverExecuteAfterForwardReplace
			cutoverExecuteAfterForwardReplace = func(role string) error {
				if role == "replacement_dci" {
					return errors.New("start rollback")
				}
				return originalAfter(role)
			}
			originalRollback := cutoverExecuteRollbackReplace
			calls := make([]string, 0, 6)
			cutoverExecuteRollbackReplace = func(role, source, target string) error {
				calls = append(calls, role)
				if err := originalRollback(role, source, target); err != nil {
					return err
				}
				if role == failedRole {
					return errors.New("injected rollback durability error")
				}
				return nil
			}
			originalRemove := cutoverExecuteRemoveFile
			jsonlCalled := false
			cutoverExecuteRemoveFile = func(role string, binding cutoverBoundFile) error {
				if role == "restore_dci_jsonl" {
					jsonlCalled = true
					if failedRole == "restore_dci_jsonl" {
						if err := originalRemove(role, binding); err != nil {
							return err
						}
						return errors.New("injected rollback JSONL durability error")
					}
				}
				return originalRemove(role, binding)
			}
			t.Cleanup(func() {
				cutoverExecuteAfterForwardReplace = originalAfter
				cutoverExecuteRollbackReplace = originalRollback
				cutoverExecuteRemoveFile = originalRemove
			})
			result, err := executeCutover(context.Background(), preflight, restore)
			if err == nil || result.receipt.Status != CutoverStatusRollbackFailed || result.receipt.ErrorCode != CutoverStatusRollbackFailed {
				t.Fatalf("rollback failure result = %#v, err=%v", result.receipt, err)
			}
			if receiptErr := validateCutoverReceipt(result.receipt); receiptErr != nil {
				t.Fatalf("rollback-failed receipt validation error = %v", receiptErr)
			}
			if result.receipt.RestoredArtifactSetSHA256 != "" || result.receipt.JSONLRestored != 0 {
				t.Fatalf("rollback-failed receipt claims restoration: %#v", result.receipt)
			}
			if len(calls) != 5 || !jsonlCalled {
				t.Fatalf("rollback calls = %#v JSONL called=%v, want all six restore roles", calls, jsonlCalled)
			}
		})
	}
}

func TestExecuteCutoverPreservesUnknownCleanupSubstitution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink cleanup substitution is Unix-specific")
	}
	preflight, _ := newCutoverRestorePreflightFixture(t)
	restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	stage, ok := findCutoverStageRole(preflight.staged.stageFiles, "replacement_event_store")
	if !ok {
		t.Fatal("event replacement stage missing")
	}
	originalAfter := cutoverExecuteAfterForwardReplace
	cutoverExecuteAfterForwardReplace = func(role string) error {
		if role == "replacement_dci" {
			if err := os.Remove(stage.target.path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(preflight.active.paths.eventStore, stage.target.path); err != nil {
				t.Fatal(err)
			}
			return errors.New("start rollback with substituted stage")
		}
		return originalAfter(role)
	}
	t.Cleanup(func() { cutoverExecuteAfterForwardReplace = originalAfter })
	result, err := executeCutover(context.Background(), preflight, restore)
	if err == nil || result.receipt.Status != CutoverStatusRollbackFailed {
		t.Fatalf("substitution result = %#v, err=%v", result.receipt, err)
	}
	if _, err := os.Lstat(stage.target.path); err != nil {
		t.Fatalf("substituted unknown stage was removed: %v", err)
	}
}

func TestExecuteCutoverPreservesHardlinkToConsumedStage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hardlink cleanup substitution is Unix-specific")
	}
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	stage, ok := findCutoverStageRole(preflight.staged.stageFiles, "replacement_dci")
	if !ok {
		t.Fatal("DCI replacement stage missing")
	}
	var consumedInfo os.FileInfo
	original := cutoverExecuteAfterForwardReplace
	cutoverExecuteAfterForwardReplace = func(role string) error {
		if role == "replacement_dci" {
			consumedInfo, err = os.Lstat(fixture.paths.dci)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Link(fixture.paths.dci, stage.target.path); err != nil {
				t.Fatal(err)
			}
			return errors.New("start rollback with consumed-stage hardlink")
		}
		return original(role)
	}
	t.Cleanup(func() { cutoverExecuteAfterForwardReplace = original })
	result, err := executeCutover(context.Background(), preflight, restore)
	if err == nil || result.receipt.Status != CutoverStatusRollbackFailed {
		t.Fatalf("consumed-stage hardlink result = %#v, err=%v", result.receipt, err)
	}
	current, err := os.Lstat(stage.target.path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(current, consumedInfo) || cutoverKnownFileIsUnaliased(stage.target.path, current) {
		t.Fatal("consumed-stage hardlink was not preserved as an unsafe artifact")
	}
}

func TestCutoverExecutionArtifactSetRequiresExactRoleSets(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	all := cutoverExecutionActiveBindings(preflight.active)
	if _, err := cutoverExecutionArtifactSet(all, "active_before"); err != nil {
		t.Fatalf("valid active-before artifact set rejected: %v", err)
	}
	activeAfter := make([]cutoverStageBinding, 0, len(all)-1)
	for _, binding := range all {
		if binding.role != "active_dci_jsonl" {
			activeAfter = append(activeAfter, binding)
		}
	}
	if _, err := cutoverExecutionArtifactSet(activeAfter, "active_after"); err != nil {
		t.Fatalf("valid active-after artifact set rejected: %v", err)
	}
	for _, mutate := range []func([]cutoverStageBinding) []cutoverStageBinding{
		func(bindings []cutoverStageBinding) []cutoverStageBinding { return bindings[:len(bindings)-1] },
		func(bindings []cutoverStageBinding) []cutoverStageBinding {
			return append(append([]cutoverStageBinding(nil), bindings...), bindings[0])
		},
		func(bindings []cutoverStageBinding) []cutoverStageBinding {
			copyOf := append([]cutoverStageBinding(nil), bindings...)
			copyOf[0].role = "unexpected"
			return copyOf
		},
	} {
		if _, err := cutoverExecutionArtifactSet(mutate(all), "active_before"); err == nil {
			t.Fatal("malformed active-before role set was accepted")
		}
	}
}

func assertCutoverReplacementStagesAbsent(t *testing.T, preflight preparedCutoverPreflight) {
	t.Helper()
	for _, file := range preflight.staged.stageFiles {
		if _, err := os.Lstat(file.target.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement stage %s remains: %v", file.role, err)
		}
	}
}

func executeCutoverFixture(t *testing.T) (preparedCutoverPreflight, cutoverActiveCohortTestFixture, cutoverExecutionResult) {
	t.Helper()
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	restore, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeCutover(context.Background(), preflight, restore)
	if err != nil || result.applied == nil || result.receipt.Status != CutoverStatusApplied {
		t.Fatalf("fixture cutover = %#v, err=%v", result.receipt, err)
	}
	return preflight, fixture, result
}
