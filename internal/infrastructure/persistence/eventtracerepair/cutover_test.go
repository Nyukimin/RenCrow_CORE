package eventtracerepair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type applyFixture struct {
	dir       string
	source    string
	output    string
	build     string
	active    string
	rollback  string
	installed string
	staged    string
	receipt   string
}

func newApplyFixture(t *testing.T) applyFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := applyFixture{
		dir:       dir,
		source:    filepath.Join(dir, "source.db"),
		output:    filepath.Join(dir, "repaired.db"),
		build:     filepath.Join(dir, "build.json"),
		active:    filepath.Join(dir, "active.db"),
		rollback:  filepath.Join(dir, "rollback"),
		installed: filepath.Join(dir, "runtime.old"),
		staged:    filepath.Join(dir, "runtime.new"),
		receipt:   filepath.Join(dir, "cutover.json"),
	}
	jobID := "job-cutover"
	writeStore(t, fixture.source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "agent.response", map[string]any{"job_id": jobID}),
	})
	dryManifest := filepath.Join(dir, "dry-run.json")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: dir, SourceStore: fixture.source, OutputStore: fixture.output,
		Manifest: dryManifest, Mode: ModeDryRun,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{
		SnapshotDir: dir, SourceStore: fixture.source, OutputStore: fixture.output,
		Manifest: fixture.build, DryRunManifest: dryManifest, Mode: ModeBuild,
	}); err != nil {
		t.Fatal(err)
	}
	copyFileForTest(t, fixture.source, fixture.active, 0600)
	if err := os.WriteFile(fixture.installed, []byte("installed-runtime"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.staged, []byte("staged-runtime"), 0750); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newUnresolvedApplyFixture(t *testing.T) applyFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := applyFixture{
		dir:       dir,
		source:    filepath.Join(dir, "source.db"),
		output:    filepath.Join(dir, "repaired.db"),
		build:     filepath.Join(dir, "build.json"),
		active:    filepath.Join(dir, "active.db"),
		rollback:  filepath.Join(dir, "rollback"),
		installed: filepath.Join(dir, "runtime.old"),
		staged:    filepath.Join(dir, "runtime.new"),
		receipt:   filepath.Join(dir, "cutover.json"),
	}
	jobID := "job-unresolved-cutover"
	writeStore(t, fixture.source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "unknown.started", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "unknown.finished", map[string]any{"job_id": jobID}),
	})
	dryManifest := filepath.Join(dir, "dry-run.json")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: dir, SourceStore: fixture.source, OutputStore: fixture.output,
		Manifest: dryManifest, Mode: ModeDryRun,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{
		SnapshotDir: dir, SourceStore: fixture.source, OutputStore: fixture.output,
		Manifest: fixture.build, DryRunManifest: dryManifest, Mode: ModeBuild,
	}); err != nil {
		t.Fatal(err)
	}
	copyFileForTest(t, fixture.source, fixture.active, 0600)
	if err := os.WriteFile(fixture.installed, []byte("installed-runtime"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.staged, []byte("staged-runtime"), 0750); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f applyFixture) options(t *testing.T) ApplyOptions {
	t.Helper()
	return ApplyOptions{
		SnapshotDir:                    f.dir,
		SourceStore:                    f.source,
		OutputStore:                    f.output,
		BuildManifest:                  f.build,
		ExpectedBuildManifestSHA256:    sha256FileForTest(t, f.build),
		ActiveStore:                    f.active,
		RollbackDir:                    f.rollback,
		InstalledRuntimeBinary:         f.installed,
		StagedRuntimeBinary:            f.staged,
		ExpectedInstalledRuntimeSHA256: sha256FileForTest(t, f.installed),
		ExpectedStagedRuntimeSHA256:    sha256FileForTest(t, f.staged),
		Manifest:                       f.receipt,
	}
}

func TestApplySwapsDatabaseAndRuntimeWithBoundedReceipt(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	beforeRuntime := readFileForTest(t, fixture.installed)
	activeMode := modeForTest(t, fixture.active)
	installedMode := modeForTest(t, fixture.installed)
	receipt, err := Apply(context.Background(), options)
	if err != nil {
		t.Fatalf("apply: %+v err=%v", receipt, err)
	}
	if receipt.SchemaVersion != CutoverManifestSchemaVersion || receipt.Status != CutoverStatusApplied || receipt.BuildManifestSHA256 == "" {
		t.Fatalf("unexpected applied receipt: %+v", receipt)
	}
	if got := readFileForTest(t, fixture.installed); bytes.Equal(got, beforeRuntime) {
		t.Fatal("installed runtime was not replaced")
	}
	if got := sha256FileForTest(t, fixture.installed); got != options.ExpectedStagedRuntimeSHA256 {
		t.Fatalf("installed runtime hash=%s want=%s", got, options.ExpectedStagedRuntimeSHA256)
	}
	if got := modeForTest(t, fixture.active); got != activeMode || modeForTest(t, fixture.installed) != installedMode {
		t.Fatalf("target permissions changed: db=%o runtime=%o", got, modeForTest(t, fixture.installed))
	}
	if _, err := os.Stat(filepath.Join(fixture.rollback, rollbackDatabaseName)); err != nil {
		t.Fatalf("database rollback copy missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.rollback, rollbackRuntimeName)); err != nil {
		t.Fatalf("runtime rollback copy missing: %v", err)
	}
	info, err := os.Stat(fixture.rollback)
	if err != nil {
		t.Fatalf("rollback directory missing: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("rollback directory mode=%o", info.Mode().Perm())
	}
	manifest := readCutoverManifestForTest(t, fixture.receipt)
	if manifest.Status != CutoverStatusApplied || manifest.AfterDBSHA256 == "" || manifest.AfterRuntimeSHA256 != options.ExpectedStagedRuntimeSHA256 {
		t.Fatalf("unexpected persisted receipt: %+v", manifest)
	}
}

func TestApplyRejectsUnresolvedBuildBeforeRollbackOrTargetMutation(t *testing.T) {
	fixture := newUnresolvedApplyFixture(t)
	options := fixture.options(t)
	activeBefore := readFileForTest(t, fixture.active)
	runtimeBefore := readFileForTest(t, fixture.installed)
	receipt, err := Apply(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "build_manifest_unresolved" {
		t.Fatalf("unresolved build must block: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created before unresolved rejection: %v", err)
	}
	if !bytes.Equal(readFileForTest(t, fixture.active), activeBefore) || !bytes.Equal(readFileForTest(t, fixture.installed), runtimeBefore) {
		t.Fatal("targets changed on unresolved rejection")
	}
}

func TestApplyRejectsUnknownOrTrailingBuildManifestJSON(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "unknown field",
			mutate: func(content []byte) []byte {
				trimmed := bytes.TrimSpace(content)
				return append(append(trimmed[:len(trimmed)-1], []byte(`,"unexpected":true}`)...), '\n')
			},
		},
		{
			name: "trailing json",
			mutate: func(content []byte) []byte {
				return append(append([]byte{}, content...), []byte(`{}`)...)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			content := readFileForTest(t, fixture.build)
			if err := os.WriteFile(fixture.build, test.mutate(content), 0600); err != nil {
				t.Fatal(err)
			}
			receipt, err := Apply(context.Background(), fixture.options(t))
			if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "build_manifest_invalid" {
				t.Fatalf("malformed build manifest must block: %+v err=%v", receipt, err)
			}
			if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
				t.Fatalf("rollback directory was created for malformed build manifest: %v", err)
			}
		})
	}
}

func TestApplyBlocksWhenActiveLogicalSnapshotGainsEvent(t *testing.T) {
	fixture := newApplyFixture(t)
	store, err := eventstore.NewSQLiteStore(fixture.active)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), eventFixture(modulecore.NewTraceID(), "orchestrator", "heartbeat.skip", nil)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	receipt, err := Apply(context.Background(), fixture.options(t))
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "active_mismatch" {
		t.Fatalf("active logical drift must block: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created on pre-swap drift: %v", err)
	}
}

func TestApplyBlocksWhenOutputIsTampered(t *testing.T) {
	fixture := newApplyFixture(t)
	if err := os.Remove(fixture.output); err != nil {
		t.Fatal(err)
	}
	copyFileForTest(t, fixture.source, fixture.output, 0600)
	receipt, err := Apply(context.Background(), fixture.options(t))
	if err == nil || receipt.Status != CutoverStatusBlocked {
		t.Fatalf("tampered output must block: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created for tampered output: %v", err)
	}
}

func TestReadOnlySQLiteDSNEscapesSpecialPath(t *testing.T) {
	for _, name := range []string{"snapshot space .db", "snapshot?.db", "snapshot#.db", "snapshot ?#.db"} {
		directory := t.TempDir()
		path := filepath.Join(directory, name)
		seedPath := filepath.Join(directory, "seed.db")
		writeStore(t, seedPath, []modulecore.EventEnvelope{
			eventFixture(modulecore.NewTraceID(), "orchestrator", "heartbeat.skip", nil),
		})
		if err := os.Rename(seedPath, path); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(readOnlySQLiteDSN(path), name) {
			t.Fatalf("SQLite DSN did not escape special path %q: %q", name, readOnlySQLiteDSN(path))
		}
		if events, _, err := readSnapshot(context.Background(), path); err != nil || len(events) != 1 {
			t.Fatalf("read snapshot %q: events=%d err=%v", name, len(events), err)
		}
		if err := quickCheck(context.Background(), path); err != nil {
			t.Fatalf("quick check %q: %v", name, err)
		}
	}
}

func TestApplyRejectsHardlinkAliasesBeforeMutation(t *testing.T) {
	fixture := newApplyFixture(t)
	if err := os.Remove(fixture.output); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(fixture.source, fixture.output); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	activeBefore := readFileForTest(t, fixture.active)
	runtimeBefore := readFileForTest(t, fixture.installed)
	receipt, err := Apply(context.Background(), fixture.options(t))
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "invalid_path" {
		t.Fatalf("hardlink alias must block: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created for hardlink alias: %v", err)
	}
	if !bytes.Equal(readFileForTest(t, fixture.active), activeBefore) || !bytes.Equal(readFileForTest(t, fixture.installed), runtimeBefore) {
		t.Fatal("targets changed on hardlink alias rejection")
	}
}

func TestApplyBlocksWhenActiveSidecarExists(t *testing.T) {
	fixture := newApplyFixture(t)
	if err := os.WriteFile(fixture.active+"-wal", nil, 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := Apply(context.Background(), fixture.options(t))
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "active_sidecar" {
		t.Fatalf("active sidecar must block: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created with active sidecar: %v", err)
	}
}

func TestApplyBlocksWhenExpectedRuntimeSHA256DoesNotMatch(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	options.ExpectedInstalledRuntimeSHA256 = strings.Repeat("0", 64)
	receipt, err := Apply(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "installed_runtime_sha256" {
		t.Fatalf("runtime hash gate must block: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created on runtime hash mismatch: %v", err)
	}
}

func TestApplyBlocksWhenExpectedBuildManifestSHA256DoesNotMatch(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	options.ExpectedBuildManifestSHA256 = strings.Repeat("0", 64)
	receipt, err := Apply(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "build_manifest_sha256" {
		t.Fatalf("build manifest hash gate must block: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created on build manifest hash mismatch: %v", err)
	}
}

func TestFileModeReturnsErrorWhenTargetIsMissing(t *testing.T) {
	if _, err := fileMode(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing target mode must return an error")
	}
}

func TestApplySurfacesReceiptWriteFailure(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	options.ExpectedInstalledRuntimeSHA256 = strings.Repeat("0", 64)
	originalWriter := cutoverReceiptWriter
	t.Cleanup(func() { cutoverReceiptWriter = originalWriter })
	receiptErr := errors.New("injected blocked receipt failure")
	cutoverReceiptWriter = func(string, CutoverManifest) error { return receiptErr }
	receipt, err := Apply(context.Background(), options)
	if err == nil || !errors.Is(err, receiptErr) || receipt.Status != CutoverStatusBlocked {
		t.Fatalf("receipt write failure must be surfaced: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created on receipt write failure: %v", err)
	}
}

func TestApplyRollsBackBothTargetsWhenSecondReplaceFails(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	activeBefore := readFileForTest(t, fixture.active)
	runtimeBefore := readFileForTest(t, fixture.installed)
	originalReplace := replaceTargetFile
	t.Cleanup(func() { replaceTargetFile = originalReplace })
	calls := 0
	replaceTargetFile = func(source, target string) error {
		calls++
		if calls == 2 {
			return errors.New("injected second replacement failure")
		}
		return atomicReplaceFile(source, target)
	}
	receipt, err := Apply(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusRolledBack {
		t.Fatalf("second replacement must roll back: %+v err=%v", receipt, err)
	}
	if !bytes.Equal(readFileForTest(t, fixture.active), activeBefore) || !bytes.Equal(readFileForTest(t, fixture.installed), runtimeBefore) {
		t.Fatal("rollback did not restore both targets")
	}
	assertRollbackCopiesRemain(t, fixture)
}

func TestApplyRollsBackBothTargetsWhenFirstReplaceReportsFailure(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	activeBefore := readFileForTest(t, fixture.active)
	runtimeBefore := readFileForTest(t, fixture.installed)
	originalReplace := replaceTargetFile
	t.Cleanup(func() { replaceTargetFile = originalReplace })
	calls := 0
	replaceTargetFile = func(source, target string) error {
		calls++
		if calls == 1 {
			return errors.New("injected first replacement failure")
		}
		return atomicReplaceFile(source, target)
	}
	receipt, err := Apply(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusRolledBack || receipt.ErrorCode != "db_replace" {
		t.Fatalf("first replacement failure must roll back: %+v err=%v", receipt, err)
	}
	if !bytes.Equal(readFileForTest(t, fixture.active), activeBefore) || !bytes.Equal(readFileForTest(t, fixture.installed), runtimeBefore) {
		t.Fatal("first replacement failure rollback did not preserve both targets")
	}
	assertRollbackCopiesRemain(t, fixture)
}

func TestApplyRollsBackBothTargetsWhenPostReplaceVerificationFails(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	activeBefore := readFileForTest(t, fixture.active)
	runtimeBefore := readFileForTest(t, fixture.installed)
	originalHook := cutoverPostReplaceHook
	t.Cleanup(func() { cutoverPostReplaceHook = originalHook })
	cutoverPostReplaceHook = func() error { return errors.New("injected post-replace verification failure") }
	receipt, err := Apply(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusRolledBack {
		t.Fatalf("post-replace failure must roll back: %+v err=%v", receipt, err)
	}
	if !bytes.Equal(readFileForTest(t, fixture.active), activeBefore) || !bytes.Equal(readFileForTest(t, fixture.installed), runtimeBefore) {
		t.Fatal("post-replace rollback did not restore both targets")
	}
	assertRollbackCopiesRemain(t, fixture)
}

func TestApplyRollsBackBothTargetsWhenReceiptWriteFails(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	activeBefore := readFileForTest(t, fixture.active)
	runtimeBefore := readFileForTest(t, fixture.installed)
	originalWriter := cutoverReceiptWriter
	t.Cleanup(func() { cutoverReceiptWriter = originalWriter })
	calls := 0
	cutoverReceiptWriter = func(path string, manifest CutoverManifest) error {
		calls++
		if calls == 1 {
			return errors.New("injected receipt write failure")
		}
		return writeCutoverReceipt(path, manifest)
	}
	receipt, err := Apply(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusRolledBack {
		t.Fatalf("receipt write failure must roll back: %+v err=%v", receipt, err)
	}
	if !bytes.Equal(readFileForTest(t, fixture.active), activeBefore) || !bytes.Equal(readFileForTest(t, fixture.installed), runtimeBefore) {
		t.Fatal("receipt failure rollback did not restore both targets")
	}
	assertRollbackCopiesRemain(t, fixture)
	persisted := readCutoverManifestForTest(t, fixture.receipt)
	if persisted.Status != CutoverStatusRolledBack {
		t.Fatalf("rollback receipt was not persisted: %+v", persisted)
	}
}

func TestApplyRejectsPathEscape(t *testing.T) {
	fixture := newApplyFixture(t)
	options := fixture.options(t)
	options.SourceStore = filepath.Join(filepath.Dir(fixture.dir), "outside-source.db")
	receipt, err := Apply(context.Background(), options)
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "invalid_path" {
		t.Fatalf("path escape must block: %+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(fixture.rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback directory was created on path escape: %v", err)
	}
}

func TestApplyRejectsSymlinkedStagedRuntime(t *testing.T) {
	fixture := newApplyFixture(t)
	original := fixture.staged
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.installed, original); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	receipt, err := Apply(context.Background(), fixture.options(t))
	if err == nil || receipt.Status != CutoverStatusBlocked || receipt.ErrorCode != "invalid_path" {
		t.Fatalf("symlinked staged runtime must block: %+v err=%v", receipt, err)
	}
}

func assertRollbackCopiesRemain(t *testing.T, fixture applyFixture) {
	t.Helper()
	for _, name := range []string{rollbackDatabaseName, rollbackRuntimeName} {
		info, err := os.Stat(filepath.Join(fixture.rollback, name))
		if err != nil {
			t.Fatalf("rollback copy %s missing: %v", name, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("rollback copy %s is not regular", name)
		}
	}
}

func copyFileForTest(t *testing.T, source, target string, mode os.FileMode) {
	t.Helper()
	content := readFileForTest(t, source)
	if err := os.WriteFile(target, content, mode); err != nil {
		t.Fatal(err)
	}
}

func readFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func sha256FileForTest(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256(readFileForTest(t, path))
	return hex.EncodeToString(sum[:])
}

func modeForTest(t *testing.T, path string) os.FileMode {
	t.Helper()
	mode, err := fileMode(path)
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

func readCutoverManifestForTest(t *testing.T, path string) CutoverManifest {
	t.Helper()
	content := readFileForTest(t, path)
	var manifest CutoverManifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("decode cutover receipt: %v", err)
	}
	return manifest
}
