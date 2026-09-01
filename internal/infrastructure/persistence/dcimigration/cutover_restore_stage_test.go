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

func TestPrepareCutoverRestoreStagesRequiresImplementation(t *testing.T) {
	if _, err := prepareCutoverRestoreStages(context.Background(), preparedCutoverPreflight{}); err == nil {
		t.Fatal("empty restore-stage preparation unexpectedly succeeded")
	}
}

func TestPrepareCutoverRestoreStagesCreatesExactSixStagesReadOnly(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	beforeActive := cutoverActiveTestHashes(t, fixture.paths)
	beforeBuild := cutoverBoundTestHashes(t, preflight.active.build.files)
	beforeStage := cutoverStageTestHashes(t, preflight.staged)

	prepared, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatalf("prepareCutoverRestoreStages() error = %v", err)
	}
	if len(prepared.files) != 6 || prepared.evidence.RestoreFileCount != 6 || prepared.evidence.SyncOK != 1 || prepared.evidence.SidecarZero != 1 || prepared.evidence.NonAlias != 1 || prepared.evidence.SourceInputsStable != 1 {
		t.Fatalf("restore evidence = %#v", prepared.evidence)
	}
	if !isLowerHexSHA256(prepared.evidence.RestoreArtifactSetSHA256) || prepared.evidence.RestoreArtifactSetSHA256 != cutoverStageArtifactSetSHA256(cutoverRestoreArtifactBindings(prepared.files), "restore") {
		t.Fatalf("restore artifact set = %q", prepared.evidence.RestoreArtifactSetSHA256)
	}
	if err := verifyCutoverRestoreStages(preflight, prepared.files); err != nil {
		t.Fatalf("verifyCutoverRestoreStages() error = %v", err)
	}
	wantNames := map[string]string{
		"restore_dci":         cutoverRestoreDCIName,
		"restore_event_store": cutoverRestoreEventStoreName,
		"restore_l1":          cutoverRestoreL1Name,
		"restore_archive":     cutoverRestoreArchiveName,
		"restore_runtime":     cutoverRestoreRuntimeName,
		"restore_dci_jsonl":   cutoverRestoreDCIJSONLName,
	}
	wantRollbackRoles := map[string]string{
		"restore_dci":         "active_dci",
		"restore_event_store": "active_event_store",
		"restore_l1":          "active_l1",
		"restore_archive":     "active_archive",
		"restore_runtime":     "installed_runtime",
		"restore_dci_jsonl":   "active_dci_jsonl",
	}
	wantActivePaths := map[string]string{
		"restore_dci":         preflight.active.paths.dci,
		"restore_event_store": preflight.active.paths.eventStore,
		"restore_l1":          preflight.active.paths.l1,
		"restore_archive":     preflight.active.paths.archive,
		"restore_runtime":     preflight.active.build.paths.installedRuntime,
		"restore_dci_jsonl":   preflight.active.paths.dciJSONL,
	}
	specs, err := cutoverRestoreStageSpecs(preflight)
	if err != nil {
		t.Fatal(err)
	}
	specByRole := make(map[string]cutoverRestoreStageSpec, len(specs))
	for _, spec := range specs {
		specByRole[spec.role] = spec
	}
	activeSourceByRole := make(map[string]cutoverBoundFile, len(wantActivePaths))
	for role, path := range wantActivePaths {
		var activeSource cutoverBoundFile
		var activeOK bool
		if role == "restore_runtime" {
			activeSource, activeOK = findCutoverBoundFile(preflight.active.build.files, path)
		} else {
			activeSource, activeOK = findCutoverBoundFile(preflight.active.files, path)
		}
		if !activeOK {
			t.Fatalf("active source binding missing for %s", role)
		}
		activeSourceByRole[role] = activeSource
	}
	seen := make(map[string]struct{}, len(prepared.files))
	for _, file := range prepared.files {
		name, ok := wantNames[file.role]
		if !ok || filepath.Base(file.target.path) != name {
			t.Fatalf("unexpected restore stage role/path: %#v", file)
		}
		rollback, ok := findCutoverStageRole(preflight.staged.rollbackFiles, wantRollbackRoles[file.role])
		spec, specOK := specByRole[file.role]
		activeSource := activeSourceByRole[file.role]
		if !ok || !specOK || !samePath(file.source.path, rollback.target.path) || !os.SameFile(file.source.info, rollback.target.info) || os.SameFile(file.source.info, activeSource.info) || !samePath(spec.activePath, wantActivePaths[file.role]) || !samePath(filepath.Dir(file.target.path), filepath.Dir(spec.activePath)) {
			t.Fatalf("restore stage source/target binding is invalid: %#v", file)
		}
		seen[file.role] = struct{}{}
		info, err := os.Lstat(file.target.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || os.SameFile(info, file.source.info) {
			t.Fatalf("restore target type/alias = %v", info.Mode())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != file.source.info.Mode().Perm() {
			t.Fatalf("restore target mode = %o, source = %o", info.Mode().Perm(), file.source.info.Mode().Perm())
		}
		if got := mustFileSHA256(t, file.target.path); got != file.source.sha256 {
			t.Fatalf("restore target hash = %q, source = %q", got, file.source.sha256)
		}
		if file.source.sqlite {
			for _, suffix := range sqliteSidecarSuffixes {
				if _, err := os.Lstat(file.target.path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("restore target sidecar %q = %v", suffix, err)
				}
			}
		}
	}
	if len(seen) != 6 {
		t.Fatalf("restore stage roles = %#v", seen)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(beforeActive, got) {
		t.Fatalf("active files changed: before=%#v after=%#v", beforeActive, got)
	}
	if got := cutoverBoundTestHashes(t, preflight.active.build.files); !mapsEqual(beforeBuild, got) {
		t.Fatalf("build files changed: before=%#v after=%#v", beforeBuild, got)
	}
	if got := cutoverStageTestHashes(t, preflight.staged); !mapsEqual(beforeStage, got) {
		t.Fatalf("rollback/replacement stages changed: before=%#v after=%#v", beforeStage, got)
	}
	if _, err := os.Lstat(preflight.retiredJSONL); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired JSONL stage exists: %v", err)
	}
	if _, err := os.Lstat(preflight.active.build.paths.cutoverReceipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cutover receipt exists: %v", err)
	}
}

func TestPrepareCutoverRestoreStagesRejectsInvalidContextAndCancellation(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	if _, err := prepareCutoverRestoreStages(nil, preflight); errorCode(err, "") != "invalid_context" {
		t.Fatalf("nil context error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareCutoverRestoreStages(ctx, preflight); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
}

func TestPrepareCutoverRestoreStagesRejectsExistingSymlinkAndHardlinkTargets(t *testing.T) {
	tests := []struct {
		name   string
		create func(t *testing.T, path, source string)
	}{
		{name: "existing", create: func(t *testing.T, path, _ string) {
			if err := os.WriteFile(path, []byte("preexisting"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests,
			struct {
				name   string
				create func(*testing.T, string, string)
			}{name: "symlink", create: func(t *testing.T, path, source string) {
				if err := os.Symlink(source, path); err != nil {
					t.Fatal(err)
				}
			}},
			struct {
				name   string
				create func(*testing.T, string, string)
			}{name: "hardlink", create: func(t *testing.T, path, source string) {
				if err := os.Link(source, path); err != nil {
					t.Fatal(err)
				}
			}},
		)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preflight, fixture := newCutoverRestorePreflightFixture(t)
			target := filepath.Join(filepath.Dir(preflight.active.paths.dci), cutoverRestoreDCIName)
			tt.create(t, target, preflight.active.paths.dci)
			before := cutoverActiveTestHashes(t, fixture.paths)
			if _, err := prepareCutoverRestoreStages(context.Background(), preflight); err == nil {
				t.Fatal("unsafe restore target was accepted")
			} else if containsCutoverActivePrivateValue(err, fixture) {
				t.Fatalf("restore error leaked private value: %v", err)
			}
			if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
				t.Fatalf("active files changed: before=%#v after=%#v", before, got)
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatalf("preexisting restore target was removed: %v", err)
			}
		})
	}
}

func TestPrepareCutoverRestoreStagesCleansPartialCopiesAfterEachRoleFailure(t *testing.T) {
	roles := []string{"restore_dci", "restore_event_store", "restore_l1", "restore_archive", "restore_runtime", "restore_dci_jsonl"}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			preflight, fixture := newCutoverRestorePreflightFixture(t)
			before := cutoverActiveTestHashes(t, fixture.paths)
			original := cutoverRestoreAfterCopy
			cutoverRestoreAfterCopy = func(got string) error {
				if got == role {
					return errors.New("injected restore failure")
				}
				return nil
			}
			t.Cleanup(func() { cutoverRestoreAfterCopy = original })
			if _, err := prepareCutoverRestoreStages(context.Background(), preflight); err == nil {
				t.Fatal("injected restore failure was ignored")
			}
			assertCutoverRestoreStagesAbsent(t, preflight)
			if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
				t.Fatalf("active files changed: before=%#v after=%#v", before, got)
			}
		})
	}
}

func TestPrepareCutoverRestoreStagesCleansAfterInjectedCopyFailure(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	original := cutoverRestoreCopyFile
	calls := 0
	cutoverRestoreCopyFile = func(ctx context.Context, source cutoverBoundFile, target string, targetMode os.FileMode, require0600, sqlite, executable bool) (cutoverBoundFile, bool, error) {
		calls++
		if calls == 2 {
			return cutoverBoundFile{}, false, errors.New("injected restore copy failure")
		}
		return original(ctx, source, target, targetMode, require0600, sqlite, executable)
	}
	t.Cleanup(func() { cutoverRestoreCopyFile = original })
	if _, err := prepareCutoverRestoreStages(context.Background(), preflight); err == nil {
		t.Fatal("injected copy failure was ignored")
	}
	if calls != 2 {
		t.Fatalf("copy calls = %d, want 2", calls)
	}
	assertCutoverRestoreStagesAbsent(t, preflight)
}

func TestPrepareCutoverRestoreStagesCleansAfterInjectedSyncFailure(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	original := cutoverStageSyncFile
	calls := 0
	cutoverStageSyncFile = func(*os.File) error {
		calls++
		return errors.New("injected restore sync failure")
	}
	t.Cleanup(func() { cutoverStageSyncFile = original })
	if _, err := prepareCutoverRestoreStages(context.Background(), preflight); err == nil {
		t.Fatal("injected sync failure was ignored")
	}
	if calls == 0 {
		t.Fatal("sync seam was not called")
	}
	assertCutoverRestoreStagesAbsent(t, preflight)
}

func TestPrepareCutoverRestoreStagesCancelsDuringCopyAndCleans(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	original := cutoverRestoreAfterCopy
	cutoverRestoreAfterCopy = func(string) error {
		cancel()
		return nil
	}
	t.Cleanup(func() {
		cutoverRestoreAfterCopy = original
		cancel()
	})
	if _, err := prepareCutoverRestoreStages(ctx, preflight); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-copy cancellation error = %v", err)
	}
	assertCutoverRestoreStagesAbsent(t, preflight)
}

func TestPrepareCutoverRestoreStagesRejectsSourceDriftBeforeCopy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, preparedCutoverPreflight)
	}{
		{name: "active", mutate: func(t *testing.T, preflight preparedCutoverPreflight) {
			path := preflight.active.paths.dci
			if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "rollback", mutate: func(t *testing.T, preflight preparedCutoverPreflight) {
			path := preflight.staged.rollbackFiles[0].target.path
			if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replacement", mutate: func(t *testing.T, preflight preparedCutoverPreflight) {
			path := preflight.staged.stageFiles[0].target.path
			if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "build", mutate: func(t *testing.T, preflight preparedCutoverPreflight) {
			path := filepath.Join(preflight.active.build.paths.buildRoot, buildOutputDCIFilename)
			if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preflight, fixture := newCutoverRestorePreflightFixture(t)
			tt.mutate(t, preflight)
			before := cutoverActiveTestHashes(t, fixture.paths)
			if _, err := prepareCutoverRestoreStages(context.Background(), preflight); err == nil {
				t.Fatal("source drift was accepted")
			} else if containsCutoverActivePrivateValue(err, fixture) {
				t.Fatalf("restore error leaked private value: %v", err)
			}
			assertCutoverRestoreStagesAbsent(t, preflight)
			if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
				t.Fatalf("active files changed: before=%#v after=%#v", before, got)
			}
		})
	}
}

func TestPrepareCutoverRestoreStagesRejectsFinalSourceMutationAndCleans(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	original := cutoverRestoreFinalRevalidate
	cutoverRestoreFinalRevalidate = func(ctx context.Context, current preparedCutoverPreflight, files []cutoverRestoreStageBinding) error {
		path := current.active.paths.dci
		if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
			return err
		}
		return original(ctx, current, files)
	}
	t.Cleanup(func() {
		cutoverRestoreFinalRevalidate = original
		fixture.restore(t)
	})
	if _, err := prepareCutoverRestoreStages(context.Background(), preflight); err == nil {
		t.Fatal("final source mutation was accepted")
	}
	assertCutoverRestoreStagesAbsent(t, preflight)
}

func TestPrepareCutoverRestoreStagesCleanupRefusesSymlinkAndPreservesUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink substitution test requires Unix semantics")
	}
	preflight, _ := newCutoverRestorePreflightFixture(t)
	target := filepath.Join(filepath.Dir(preflight.active.paths.dci), cutoverRestoreDCIName)
	unknown := filepath.Join(filepath.Dir(target), "unrelated-restore-entry")
	original := cutoverRestoreAfterCopy
	cutoverRestoreAfterCopy = func(role string) error {
		if role != "restore_dci" {
			return nil
		}
		if err := os.Remove(target); err != nil {
			return err
		}
		if err := os.Symlink(preflight.active.paths.dci, target); err != nil {
			return err
		}
		if err := os.WriteFile(unknown, []byte("preserve"), 0o600); err != nil {
			return err
		}
		return errors.New("injected cleanup substitution")
	}
	t.Cleanup(func() {
		cutoverRestoreAfterCopy = original
		_ = os.Remove(target)
		_ = os.Remove(unknown)
	})
	if _, err := prepareCutoverRestoreStages(context.Background(), preflight); errorCode(err, "") != "cleanup" {
		t.Fatalf("cleanup substitution error = %v", err)
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("cleanup removed or changed substituted symlink: info=%v err=%v", info, err)
	}
	data, err := os.ReadFile(unknown)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("unknown cleanup entry = %q, %v", data, err)
	}
}

func TestVerifyCutoverRestoreStagesRejectsMalformedBindings(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	prepared, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]cutoverRestoreStageBinding) []cutoverRestoreStageBinding
	}{
		{name: "missing", mutate: func(files []cutoverRestoreStageBinding) []cutoverRestoreStageBinding { return files[:5] }},
		{name: "duplicate role", mutate: func(files []cutoverRestoreStageBinding) []cutoverRestoreStageBinding {
			files[1].role = files[0].role
			return files
		}},
		{name: "wrong source", mutate: func(files []cutoverRestoreStageBinding) []cutoverRestoreStageBinding {
			files[0].source = files[1].source
			return files
		}},
		{name: "wrong target", mutate: func(files []cutoverRestoreStageBinding) []cutoverRestoreStageBinding {
			files[0].target.path = files[1].target.path
			return files
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := append([]cutoverRestoreStageBinding(nil), prepared.files...)
			if err := verifyCutoverRestoreStages(preflight, tt.mutate(files)); err == nil {
				t.Fatal("malformed restore bindings were accepted")
			}
		})
	}
}

func newCutoverRestorePreflightFixture(t *testing.T) (preparedCutoverPreflight, cutoverActiveCohortTestFixture) {
	t.Helper()
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	preflight, _, err := preflightStagedCutover(context.Background(), stage)
	if err != nil {
		t.Fatal(err)
	}
	return preflight, fixture
}

func assertCutoverRestoreStagesAbsent(t *testing.T, preflight preparedCutoverPreflight) {
	t.Helper()
	specs := []struct {
		role   string
		path   string
		sqlite bool
	}{
		{role: "restore_dci", path: filepath.Join(filepath.Dir(preflight.active.paths.dci), cutoverRestoreDCIName), sqlite: true},
		{role: "restore_event_store", path: filepath.Join(filepath.Dir(preflight.active.paths.eventStore), cutoverRestoreEventStoreName), sqlite: true},
		{role: "restore_l1", path: filepath.Join(filepath.Dir(preflight.active.paths.l1), cutoverRestoreL1Name), sqlite: true},
		{role: "restore_archive", path: filepath.Join(filepath.Dir(preflight.active.paths.archive), cutoverRestoreArchiveName), sqlite: true},
		{role: "restore_runtime", path: filepath.Join(filepath.Dir(preflight.active.build.paths.installedRuntime), cutoverRestoreRuntimeName)},
		{role: "restore_dci_jsonl", path: filepath.Join(filepath.Dir(preflight.active.paths.dciJSONL), cutoverRestoreDCIJSONLName)},
	}
	for _, spec := range specs {
		if _, err := os.Lstat(spec.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restore stage %s remains: %v", spec.role, err)
		}
		if spec.sqlite {
			for _, suffix := range sqliteSidecarSuffixes {
				if _, err := os.Lstat(spec.path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("restore stage %s sidecar remains: %v", spec.role, err)
				}
			}
		}
	}
}

func TestPrepareCutoverRestoreStagesErrorsRemainBounded(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	original := cutoverRestoreAfterCopy
	cutoverRestoreAfterCopy = func(string) error { return errors.New("/private/path legacy-search-1 payload") }
	t.Cleanup(func() { cutoverRestoreAfterCopy = original })
	_, err := prepareCutoverRestoreStages(context.Background(), preflight)
	if err == nil || strings.Contains(err.Error(), fixture.paths.dci) || strings.Contains(err.Error(), "legacy-search-1") || strings.Contains(err.Error(), "payload") {
		t.Fatalf("restore error leaked private data: %v", err)
	}
}
