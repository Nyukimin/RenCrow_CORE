package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestStageCutoverCohortBindsRollbackAndReplacementStages(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	beforeActive := cutoverActiveTestHashes(t, fixture.paths)
	beforeBuild := cutoverBoundTestHashes(t, fixture.build.files)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatalf("prepareCutoverActiveCohort() error = %v", err)
	}
	beforeSourceBindings := append(append([]cutoverBoundFile{}, active.files...), active.build.files...)

	bundle, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatalf("stageCutoverCohort() error = %v", err)
	}
	if !samePath(bundle.rollback, fixture.build.paths.rollbackDir) || len(bundle.rollbackFiles) != 7 || len(bundle.stageFiles) != 5 {
		t.Fatalf("stage bundle shape = %#v", bundle)
	}
	assertCutoverStageEvidenceShape(t, reflect.TypeOf(bundle.evidence))
	if bundle.evidence.RollbackFileCount != 7 || bundle.evidence.ReplacementFileCount != 5 ||
		bundle.evidence.RollbackRootModeOK != 1 || bundle.evidence.ReplacementModeOK != 1 ||
		bundle.evidence.SourceInputsStable != 1 || bundle.evidence.SidecarZero != 1 ||
		bundle.evidence.NonAlias != 1 || bundle.evidence.SyncOK != 1 {
		t.Fatalf("stage health evidence = %#v", bundle.evidence)
	}
	if bundle.evidence.RollbackArtifactSetSHA256 != cutoverStageArtifactSetSHA256(bundle.rollbackFiles, "rollback") ||
		bundle.evidence.ReplacementArtifactSetSHA256 != cutoverStageArtifactSetSHA256(bundle.stageFiles, "replacement") {
		t.Fatalf("stage artifact set evidence = %#v", bundle.evidence)
	}
	if runtime.GOOS != "windows" {
		assertCutoverStageMode(t, bundle.rollback, 0o700)
	}
	assertCutoverStageEntries(t, bundle.rollback, map[string]struct{}{
		cutoverRollbackDCIFilename: {}, cutoverRollbackDCIJSONLFilename: {}, cutoverRollbackEventStoreFilename: {},
		cutoverRollbackL1Filename: {}, cutoverRollbackArchiveFilename: {}, cutoverRollbackInstalledFilename: {}, cutoverRollbackStagedFilename: {},
	})
	rollbackNames := map[string]string{
		"active_dci": cutoverRollbackDCIFilename, "active_dci_jsonl": cutoverRollbackDCIJSONLFilename,
		"active_event_store": cutoverRollbackEventStoreFilename, "active_l1": cutoverRollbackL1Filename,
		"active_archive": cutoverRollbackArchiveFilename, "installed_runtime": cutoverRollbackInstalledFilename,
		"staged_runtime": cutoverRollbackStagedFilename,
	}
	for _, file := range bundle.rollbackFiles {
		want, ok := rollbackNames[file.role]
		if !ok || filepath.Base(file.target.path) != want || !samePath(filepath.Dir(file.target.path), bundle.rollback) {
			t.Fatalf("rollback binding = %#v", file)
		}
		assertCutoverStageCopy(t, file)
	}
	stageNames := map[string]string{
		"replacement_dci": cutoverStageDCIName, "replacement_event_store": cutoverStageEventStoreName,
		"replacement_l1": cutoverStageL1Name, "replacement_archive": cutoverStageArchiveName,
		"replacement_runtime": cutoverStageRuntimeName,
	}
	for _, file := range bundle.stageFiles {
		want, ok := stageNames[file.role]
		if !ok || filepath.Base(file.target.path) != want || samePath(file.target.path, file.source.path) {
			t.Fatalf("replacement binding = %#v", file)
		}
		assertCutoverStageCopy(t, file)
		if file.role != "replacement_runtime" && runtime.GOOS != "windows" && file.target.info.Mode().Perm() != 0o600 {
			t.Fatalf("replacement %s mode = %o, want 600", file.role, file.target.info.Mode().Perm())
		}
		if file.role == "replacement_runtime" && file.target.info.Mode().Perm() != file.source.info.Mode().Perm() {
			t.Fatalf("replacement runtime mode = %o, want %o", file.target.info.Mode().Perm(), file.source.info.Mode().Perm())
		}
	}
	for _, file := range append(append([]cutoverStageBinding{}, bundle.rollbackFiles...), bundle.stageFiles...) {
		if file.source.sqlite {
			for _, suffix := range sqliteSidecarSuffixes {
				if _, err := os.Lstat(file.target.path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("SQLite sidecar %q exists: %v", suffix, err)
				}
			}
		}
	}
	if _, err := os.Lstat(fixture.build.paths.cutoverReceipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("formal cutover receipt exists: %v", err)
	}
	if after := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(beforeActive, after) {
		t.Fatalf("active sources changed: before=%#v after=%#v", beforeActive, after)
	}
	if after := cutoverBoundTestHashes(t, fixture.build.files); !mapsEqual(beforeBuild, after) {
		t.Fatalf("build/runtime cohort changed: before=%#v after=%#v", beforeBuild, after)
	}
	assertCutoverStageSourcesUnchanged(t, beforeSourceBindings)
}

func assertCutoverStageEvidenceShape(t *testing.T, typ reflect.Type) {
	t.Helper()
	expected := map[string]reflect.Type{
		"RollbackArtifactSetSHA256": reflect.TypeOf(""), "ReplacementArtifactSetSHA256": reflect.TypeOf(""),
		"RollbackFileCount": reflect.TypeOf(0), "ReplacementFileCount": reflect.TypeOf(0),
		"RollbackRootModeOK": reflect.TypeOf(0), "ReplacementModeOK": reflect.TypeOf(0),
		"SourceInputsStable": reflect.TypeOf(0), "SidecarZero": reflect.TypeOf(0),
		"NonAlias": reflect.TypeOf(0), "SyncOK": reflect.TypeOf(0),
	}
	if typ.NumField() != len(expected) {
		t.Fatalf("stage evidence fields = %d, want %d", typ.NumField(), len(expected))
	}
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if want, ok := expected[field.Name]; !ok || field.Type != want {
			t.Fatalf("unexpected stage evidence field %q (%v)", field.Name, field.Type)
		}
	}
}

func assertCutoverStageEntries(t *testing.T, root string, expected map[string]struct{}) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read stage directory %q: %v", root, err)
	}
	if len(expected) != 0 && len(entries) != len(expected) {
		t.Fatalf("stage directory entries = %d, want %d", len(entries), len(expected))
	}
	for _, entry := range entries {
		if len(expected) != 0 {
			if _, ok := expected[entry.Name()]; !ok {
				t.Fatalf("unexpected stage entry %q", entry.Name())
			}
		}
	}
}

func assertCutoverStageMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}

func assertCutoverStageCopy(t *testing.T, file cutoverStageBinding) {
	t.Helper()
	if file.target.info == nil || file.source.info == nil || os.SameFile(file.source.info, file.target.info) {
		t.Fatalf("stage copy aliases source: %#v", file)
	}
	if file.target.sha256 != file.source.sha256 || file.target.bytes != file.source.bytes {
		t.Fatalf("stage copy differs from source: %#v", file)
	}
}

func TestStageCutoverCohortCleansEveryPartialCopy(t *testing.T) {
	roles := []string{
		"active_dci", "active_dci_jsonl", "active_event_store", "active_l1", "active_archive",
		"installed_runtime", "staged_runtime", "replacement_dci", "replacement_event_store",
		"replacement_l1", "replacement_archive", "replacement_runtime",
	}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			beforeActive := cutoverActiveTestHashes(t, fixture.paths)
			beforeBuild := cutoverBoundTestHashes(t, fixture.build.files)
			original := cutoverStageAfterCopy
			cutoverStageAfterCopy = func(got string) error {
				if got == role {
					return errors.New("injected copy failure")
				}
				return nil
			}
			t.Cleanup(func() { cutoverStageAfterCopy = original })
			if _, err := stageCutoverCohort(context.Background(), active); err == nil {
				t.Fatal("stageCutoverCohort() unexpectedly succeeded")
			} else if containsCutoverStagePrivateValue(err, active) {
				t.Fatalf("stage error leaked private value: %v", err)
			}
			assertCutoverStageArtifactsAbsent(t, active)
			if after := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(beforeActive, after) {
				t.Fatalf("active source changed: before=%#v after=%#v", beforeActive, after)
			}
			if after := cutoverBoundTestHashes(t, fixture.build.files); !mapsEqual(beforeBuild, after) {
				t.Fatalf("build/runtime cohort changed: before=%#v after=%#v", beforeBuild, after)
			}
		})
	}
}

func TestStageCutoverCohortRejectsMalformedNonFreshAndUnsafeInputs(t *testing.T) {
	t.Run("malformed prepared", func(t *testing.T) {
		if _, err := stageCutoverCohort(context.Background(), preparedCutoverActiveCohort{}); err == nil {
			t.Fatal("malformed prepared cohort was accepted")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := stageCutoverCohort(ctx, active); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled stage error = %v", err)
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
	t.Run("existing stage", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		paths, err := resolveCutoverStagePaths(active)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.dci, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := mustReadFile(t, paths.dci)
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("existing stage was accepted")
		}
		if after := mustReadFile(t, paths.dci); !reflect.DeepEqual(before, after) {
			t.Fatalf("existing stage changed: before=%q after=%q", before, after)
		}
		if _, err := os.Lstat(active.build.paths.rollbackDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback root created after existing-stage rejection: %v", err)
		}
	})
	t.Run("existing rollback root", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(active.build.paths.rollbackDir, 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(active.build.paths.rollbackDir, "keep")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("existing rollback root was accepted")
		}
		if got := string(mustReadFile(t, sentinel)); got != "keep" {
			t.Fatalf("existing rollback root was changed: %q", got)
		}
	})
	t.Run("sidecar", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture.paths.eventStore+"-wal", []byte("sidecar"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("active sidecar was accepted")
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
	if runtime.GOOS != "windows" {
		t.Run("runtime not executable", func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(active.build.paths.installedRuntime, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := stageCutoverCohort(context.Background(), active); err == nil {
				t.Fatal("non-executable runtime was accepted")
			}
			assertCutoverStageArtifactsAbsent(t, active)
		})
		t.Run("runtime group writable", func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(active.build.paths.stagedRuntime, 0o720); err != nil {
				t.Fatal(err)
			}
			if _, err := stageCutoverCohort(context.Background(), active); err == nil {
				t.Fatal("writable runtime was accepted")
			}
			assertCutoverStageArtifactsAbsent(t, active)
		})
		t.Run("stage symlink", func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			paths, err := resolveCutoverStagePaths(active)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(active.paths.dci, paths.dci); err != nil {
				t.Fatal(err)
			}
			if _, err := stageCutoverCohort(context.Background(), active); err == nil {
				t.Fatal("symlink stage was accepted")
			}
			if _, err := os.Lstat(active.build.paths.rollbackDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rollback root created after symlink-stage rejection: %v", err)
			}
		})
		t.Run("stage hardlink", func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			paths, err := resolveCutoverStagePaths(active)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Link(active.paths.dci, paths.dci); err != nil {
				t.Fatal(err)
			}
			if _, err := stageCutoverCohort(context.Background(), active); err == nil {
				t.Fatal("hardlink stage was accepted")
			}
			if _, err := os.Lstat(active.build.paths.rollbackDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rollback root created after hardlink-stage rejection: %v", err)
			}
		})
	}
}

func TestStageCutoverCohortRejectsBoundModeDriftBeforeCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bound mode contract is exercised on Unix")
	}
	tests := []struct {
		name string
		path func(cutoverActiveCohortTestFixture, preparedCutoverActiveCohort) string
		mode os.FileMode
	}{
		{name: "active DCI", path: func(fixture cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort) string {
			return fixture.paths.dci
		}, mode: 0o644},
		{name: "active JSONL", path: func(fixture cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort) string {
			return fixture.paths.dciJSONL
		}, mode: 0o644},
		{name: "build output", path: func(_ cutoverActiveCohortTestFixture, active preparedCutoverActiveCohort) string {
			return active.build.outputFiles[buildOutputDCIRole].path
		}, mode: 0o644},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			path := tt.path(fixture, active)
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := stageCutoverCohort(context.Background(), active); err == nil {
				t.Fatal("bound mode drift was accepted")
			}
			assertCutoverStageArtifactsAbsent(t, active)
		})
	}
}

func TestStageCutoverCohortRejectsBoundModeDriftDuringCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bound mode contract is exercised on Unix")
	}
	tests := []struct {
		name string
		role string
		path func(cutoverActiveCohortTestFixture, preparedCutoverActiveCohort) string
		mode os.FileMode
	}{
		{name: "active DCI", role: "active_dci", path: func(fixture cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort) string {
			return fixture.paths.dci
		}, mode: 0o644},
		{name: "active JSONL", role: "active_dci_jsonl", path: func(fixture cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort) string {
			return fixture.paths.dciJSONL
		}, mode: 0o644},
		{name: "build output", role: "replacement_dci", path: func(_ cutoverActiveCohortTestFixture, active preparedCutoverActiveCohort) string {
			return active.build.outputFiles[buildOutputDCIRole].path
		}, mode: 0o644},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			path := tt.path(fixture, active)
			originalInfo, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			original := cutoverStageAfterCopy
			cutoverStageAfterCopy = func(role string) error {
				if role == tt.role {
					return os.Chmod(path, tt.mode)
				}
				return nil
			}
			t.Cleanup(func() {
				cutoverStageAfterCopy = original
				if err := os.Chmod(path, originalInfo.Mode().Perm()); err != nil {
					t.Errorf("restore mode: %v", err)
				}
			})
			if _, err := stageCutoverCohort(context.Background(), active); err == nil {
				t.Fatal("bound mode drift was accepted")
			}
			assertCutoverStageArtifactsAbsent(t, active)
		})
	}
}

func TestStageCutoverCohortFinalRevalidationRejectsActiveMutation(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	original := cutoverPrepareActiveAfterBind
	calls := 0
	cutoverPrepareActiveAfterBind = func() error {
		calls++
		if calls != 3 {
			return nil
		}
		data := mustReadFile(t, fixture.paths.dciJSONL)
		return os.WriteFile(fixture.paths.dciJSONL, append(data, 'x'), 0o600)
	}
	t.Cleanup(func() {
		cutoverPrepareActiveAfterBind = original
		fixture.restore(t)
	})
	if _, err := stageCutoverCohort(context.Background(), active); err == nil {
		t.Fatal("active mutation during final revalidation was accepted")
	}
	if calls != 3 {
		t.Fatalf("active-after-bind seam calls = %d, want 3", calls)
	}
	assertCutoverStageArtifactsAbsent(t, active)
}

func TestStageCutoverCohortFinalRevalidationRejectsBuildAndRuntimeMutation(t *testing.T) {
	tests := []struct {
		name string
		path func(preparedCutoverActiveCohort) string
	}{
		{name: "build output", path: func(active preparedCutoverActiveCohort) string {
			return active.build.outputFiles[buildOutputDCIRole].path
		}},
		{name: "installed runtime", path: func(active preparedCutoverActiveCohort) string { return active.build.paths.installedRuntime }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			path := tt.path(active)
			originalData := mustReadFile(t, path)
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			originalMode := info.Mode().Perm()
			originalAfterBind := cutoverPrepareActiveAfterBind
			calls := 0
			cutoverPrepareActiveAfterBind = func() error {
				calls++
				if calls == 3 {
					return os.WriteFile(path, append(append([]byte(nil), originalData...), 'x'), originalMode)
				}
				return nil
			}
			t.Cleanup(func() {
				cutoverPrepareActiveAfterBind = originalAfterBind
				if err := os.WriteFile(path, originalData, originalMode); err != nil {
					t.Errorf("restore mutation: %v", err)
				}
			})
			if _, err := stageCutoverCohort(context.Background(), active); err == nil {
				t.Fatal("build/runtime mutation during final revalidation was accepted")
			}
			if calls != 3 {
				t.Fatalf("active-after-bind seam calls = %d, want 3", calls)
			}
			assertCutoverStageArtifactsAbsent(t, active)
		})
	}
}

func TestStageCutoverCohortSupportsFailureSeamsAndMidLoopCancellation(t *testing.T) {
	t.Run("copy", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		original := cutoverStageCopyFile
		calls := 0
		cutoverStageCopyFile = func(ctx context.Context, source cutoverBoundFile, target string, mode os.FileMode, require0600, sqlite, executable bool) (cutoverBoundFile, bool, error) {
			calls++
			if calls == 3 {
				return cutoverBoundFile{}, false, errors.New("injected copy failure")
			}
			return original(ctx, source, target, mode, require0600, sqlite, executable)
		}
		t.Cleanup(func() { cutoverStageCopyFile = original })
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("copy seam failure was ignored")
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
	t.Run("file sync", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		original := cutoverStageSyncFile
		cutoverStageSyncFile = func(*os.File) error { return errors.New("injected file sync failure") }
		t.Cleanup(func() { cutoverStageSyncFile = original })
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("file sync seam failure was ignored")
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
	t.Run("directory sync", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		original := cutoverStageSyncDirectory
		cutoverStageSyncDirectory = func(string) error { return errors.New("injected directory sync failure") }
		t.Cleanup(func() { cutoverStageSyncDirectory = original })
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("directory sync seam failure was ignored")
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
	t.Run("rename", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		original := cutoverStageRename
		cutoverStageRename = func(string, string) error { return errors.New("injected rename failure") }
		t.Cleanup(func() { cutoverStageRename = original })
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("rename seam failure was ignored")
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
	t.Run("rename reports after install", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		original := cutoverStageRename
		cutoverStageRename = func(from, to string) error {
			if err := os.Rename(from, to); err != nil {
				return err
			}
			return errors.New("injected post-install rename failure")
		}
		t.Cleanup(func() { cutoverStageRename = original })
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("post-install rename failure was ignored")
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
	t.Run("final revalidation", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		original := cutoverStageFinalRevalidate
		cutoverStageFinalRevalidate = func(context.Context, preparedCutoverActiveCohort, []cutoverStageBinding, []cutoverStageBinding) error {
			return errors.New("injected final verification failure")
		}
		t.Cleanup(func() { cutoverStageFinalRevalidate = original })
		if _, err := stageCutoverCohort(context.Background(), active); err == nil {
			t.Fatal("final revalidation seam failure was ignored")
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
	t.Run("mid-loop cancellation", func(t *testing.T) {
		fixture := newCutoverActiveCohortTestFixture(t)
		active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		original := cutoverStageAfterCopy
		cutoverStageAfterCopy = func(role string) error {
			if role == "active_dci" {
				cancel()
			}
			return nil
		}
		t.Cleanup(func() {
			cutoverStageAfterCopy = original
			cancel()
		})
		if _, err := stageCutoverCohort(ctx, active); !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-loop cancellation error = %v", err)
		}
		assertCutoverStageArtifactsAbsent(t, active)
	})
}

func TestStageCutoverCohortRejectsSourceMutationBeforeFinalize(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	original := cutoverStageAfterCopy
	cutoverStageAfterCopy = func(role string) error {
		if role == "replacement_runtime" {
			file, openErr := os.OpenFile(fixture.paths.dciJSONL, os.O_APPEND|os.O_WRONLY, 0)
			if openErr != nil {
				return openErr
			}
			if _, writeErr := file.Write([]byte("mutation")); writeErr != nil {
				_ = file.Close()
				return writeErr
			}
			return file.Close()
		}
		return nil
	}
	t.Cleanup(func() {
		cutoverStageAfterCopy = original
		fixture.restore(t)
	})
	if _, err := stageCutoverCohort(context.Background(), active); err == nil {
		t.Fatal("source mutation was accepted")
	}
	assertCutoverStageArtifactsAbsent(t, active)
}

func TestStageCutoverCohortRejectsBuildMutationBeforeFinalize(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	path := active.build.outputFiles[buildOutputDCIRole].path
	originalData := mustReadFile(t, path)
	originalInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	originalAfterCopy := cutoverStageAfterCopy
	cutoverStageAfterCopy = func(role string) error {
		if role == "replacement_runtime" {
			if err := os.WriteFile(path, append(append([]byte(nil), originalData...), 'x'), 0o600); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() {
		cutoverStageAfterCopy = originalAfterCopy
		if err := os.WriteFile(path, originalData, originalInfo.Mode().Perm()); err != nil {
			t.Errorf("restore build output: %v", err)
		}
	})
	if _, err := stageCutoverCohort(context.Background(), active); err == nil {
		t.Fatal("build mutation was accepted")
	}
	assertCutoverStageArtifactsAbsent(t, active)
}

func TestStageCutoverCohortDoesNotDeleteUnknownRollbackEntries(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	original := cutoverStageAfterCopy
	unknownPath := ""
	cutoverStageAfterCopy = func(role string) error {
		if role != "active_dci" {
			return nil
		}
		entries, readErr := os.ReadDir(filepath.Dir(active.build.paths.rollbackDir))
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), cutoverRollbackTempPrefix) {
				unknownPath = filepath.Join(filepath.Dir(active.build.paths.rollbackDir), entry.Name(), "unknown")
				if writeErr := os.WriteFile(unknownPath, []byte("unknown"), 0o600); writeErr != nil {
					return writeErr
				}
				return errors.New("injected failure with unknown entry")
			}
		}
		return errors.New("rollback temporary root not found")
	}
	t.Cleanup(func() { cutoverStageAfterCopy = original })
	_, err = stageCutoverCohort(context.Background(), active)
	if err == nil || errorCode(err, "unknown") != "cleanup" {
		t.Fatalf("unknown-entry cleanup result = %v, code=%s, want cleanup", err, errorCode(err, "unknown"))
	}
	if unknownPath == "" {
		t.Fatal("unknown rollback entry was not created")
	}
	if _, err := os.Lstat(unknownPath); err != nil {
		t.Fatalf("unknown rollback entry was deleted: %v", err)
	}
	if _, err := os.Lstat(active.build.paths.rollbackDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final rollback root exists after cleanup failure: %v", err)
	}
}

func TestStageCutoverCohortCleansInjectedStageSidecar(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := resolveCutoverStagePaths(active)
	if err != nil {
		t.Fatal(err)
	}
	original := cutoverStageAfterCopy
	cutoverStageAfterCopy = func(role string) error {
		if role != "replacement_event_store" {
			return nil
		}
		if err := os.WriteFile(paths.eventStore+"-wal", []byte("sidecar"), 0o600); err != nil {
			return err
		}
		return errors.New("injected sidecar failure")
	}
	t.Cleanup(func() { cutoverStageAfterCopy = original })
	if _, err := stageCutoverCohort(context.Background(), active); err == nil {
		t.Fatal("injected sidecar failure was ignored")
	}
	assertCutoverStageArtifactsAbsent(t, active)
	if _, err := os.Lstat(paths.eventStore + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("injected stage sidecar remains: %v", err)
	}
}

func containsCutoverStagePrivateValue(err error, active preparedCutoverActiveCohort) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, value := range []string{
		active.paths.dci, active.paths.dciJSONL, active.paths.eventStore, active.paths.l1, active.paths.archive,
		active.build.paths.buildRoot, active.build.paths.buildReceipt, active.build.paths.rollbackDir,
		active.build.paths.cutoverReceipt, "legacy-search-1", "legacy-evidence-1",
	} {
		if value != "" && strings.Contains(message, value) {
			return true
		}
	}
	return false
}

func assertCutoverStageArtifactsAbsent(t *testing.T, active preparedCutoverActiveCohort) {
	t.Helper()
	if _, err := os.Lstat(active.build.paths.rollbackDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback root remains after failed stage: %v", err)
	}
	paths, err := resolveCutoverStagePaths(active)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.dci, paths.eventStore, paths.l1, paths.archive, paths.runtime} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stage path %q remains after failed stage: %v", path, err)
		}
	}
	parent := filepath.Dir(active.build.paths.rollbackDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), cutoverRollbackTempPrefix) {
			t.Fatalf("rollback temporary entry %q remains", entry.Name())
		}
	}
}

func assertCutoverStageSourcesUnchanged(t *testing.T, sources []cutoverBoundFile) {
	t.Helper()
	for _, source := range sources {
		info, err := os.Lstat(source.path)
		if err != nil {
			t.Fatalf("source %q disappeared: %v", source.path, err)
		}
		if !os.SameFile(source.info, info) || info.Mode().Perm() != source.info.Mode().Perm() {
			t.Fatalf("source binding changed: before=%#v after=%#v", source, info)
		}
		hash, bytes, err := hashBuildFile(source.path)
		if err != nil || hash != source.sha256 || bytes != source.bytes {
			t.Fatalf("source content changed: binding=%#v hash=%q bytes=%d err=%v", source, hash, bytes, err)
		}
	}
}
