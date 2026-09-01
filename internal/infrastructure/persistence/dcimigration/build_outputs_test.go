package dcimigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMaterializeBuildOutputsCreatesAllFixedArtifactsAndEvidence(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	before := buildOutputsInputHashes(t, prepared)

	evidence, err := materializeBuildOutputs(context.Background(), prepared)
	if err != nil {
		t.Fatalf("materializeBuildOutputs() error = %v", err)
	}
	if evidence.BuildRootModeOK != 1 || evidence.SidecarZero != 1 || evidence.SourceInputsStable != 1 {
		t.Fatalf("health evidence = %#v", evidence)
	}
	if evidence.DCI.QuickCheckOK != 1 || evidence.DCI.SidecarZero != 1 || evidence.EventStore.QuickCheckOK != 1 || evidence.EventStore.SidecarZero != 1 || evidence.L1.QuickCheckOK != 1 || evidence.L1.SidecarZero != 1 || evidence.Archive.QuickCheckOK != 1 || evidence.Archive.SidecarZero != 1 {
		t.Fatalf("owner health evidence = %#v", evidence)
	}
	if !isLowerHexSHA256(evidence.ArtifactSetSHA256) {
		t.Fatalf("artifact set hash = %q", evidence.ArtifactSetSHA256)
	}

	targets := buildOutputTargets(prepared.paths.buildDir)
	files := make(map[string]buildOutputFile, len(targets))
	for _, target := range targets {
		info, err := os.Lstat(target.path)
		if err != nil {
			t.Fatalf("output %s missing: %v", target.name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			t.Fatalf("output %s is not a regular non-symlink file", target.name)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("output %s permissions = %o, want 600", target.name, info.Mode().Perm())
		}
		sha256, bytes, err := hashBuildFile(target.path)
		if err != nil {
			t.Fatalf("hash output %s: %v", target.name, err)
		}
		target.sha256 = sha256
		target.bytes = bytes
		target.quickCheckOK = 1
		target.sidecarZero = 1
		files[target.role] = target
		for _, suffix := range sqliteSidecarSuffixes {
			if _, err := os.Lstat(target.path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("output %s sidecar %q exists: %v", target.name, suffix, err)
			}
		}
	}
	entries, err := os.ReadDir(prepared.paths.buildDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(targets) {
		t.Fatalf("build root entries = %d, want %d", len(entries), len(targets))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(prepared.paths.buildDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("build root permissions = %o, want 700", info.Mode().Perm())
		}
	}
	if got := buildOutputArtifactSetSHA256(files); got != evidence.ArtifactSetSHA256 {
		t.Fatalf("artifact set hash = %q, recomputed %q", evidence.ArtifactSetSHA256, got)
	}
	if evidence.TargetDCISHA256 != files[buildOutputDCIRole].sha256 || evidence.TargetDCIBytes != files[buildOutputDCIRole].bytes ||
		evidence.TargetEventStoreSHA256 != files[buildOutputEventStoreRole].sha256 || evidence.TargetEventStoreBytes != files[buildOutputEventStoreRole].bytes ||
		evidence.TargetL1SHA256 != files[buildOutputL1Role].sha256 || evidence.TargetL1Bytes != files[buildOutputL1Role].bytes ||
		evidence.TargetArchiveSHA256 != files[buildOutputArchiveRole].sha256 || evidence.TargetArchiveBytes != files[buildOutputArchiveRole].bytes {
		t.Fatalf("file evidence does not match outputs: %#v", evidence)
	}
	if after := buildOutputsInputHashes(t, prepared); !mapsEqual(before, after) {
		t.Fatalf("prepared inputs changed: before=%#v after=%#v", before, after)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{prepared.paths.snapshotDir, prepared.paths.buildDir, "legacy-search-1", "legacy-evidence-1", "spec.md"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("output evidence contains forbidden value %q: %s", forbidden, encoded)
		}
	}
}

func TestMaterializeBuildOutputsRejectsInvalidOrNonFreshRootWithoutMutation(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	before := buildOutputsInputHashes(t, prepared)
	if err := os.Mkdir(prepared.paths.buildDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(prepared.paths.buildDir, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeBuildOutputs(context.Background(), prepared); err == nil {
		t.Fatal("materializeBuildOutputs() accepted an existing build root")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("existing build root was modified: %v", err)
	}
	if after := buildOutputsInputHashes(t, prepared); !mapsEqual(before, after) {
		t.Fatalf("prepared inputs changed after invalid-root rejection: before=%#v after=%#v", before, after)
	}
}

func TestMaterializeBuildOutputsRejectsCanceledContextWithoutCreatingRoot(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := materializeBuildOutputs(ctx, prepared); !errors.Is(err, context.Canceled) {
		t.Fatalf("materializeBuildOutputs(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := os.Lstat(prepared.paths.buildDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("build root was created for canceled output: %v", err)
	}
}

func TestMaterializeBuildOutputsCleansEveryPartialOutputAndKeepsEmptyRoot(t *testing.T) {
	roles := []struct {
		name string
		file string
	}{
		{name: buildOutputDCIRole, file: buildOutputDCIFilename},
		{name: buildOutputEventStoreRole, file: buildOutputEventStoreFilename},
		{name: buildOutputL1Role, file: buildOutputL1Filename},
		{name: buildOutputArchiveRole, file: buildOutputArchiveFilename},
	}
	for _, role := range roles {
		t.Run(role.name, func(t *testing.T) {
			prepared := newBuildOutputsPreparedFixture(t)
			before := buildOutputsInputHashes(t, prepared)
			original := buildOutputsAfterOutput
			buildOutputsAfterOutput = func(path string) error {
				if filepath.Base(path) != role.file {
					return nil
				}
				if err := os.WriteFile(path+"-wal", []byte("injected sidecar"), 0o600); err != nil {
					return err
				}
				return errors.New("injected output failure")
			}
			t.Cleanup(func() { buildOutputsAfterOutput = original })

			if _, err := materializeBuildOutputs(context.Background(), prepared); err == nil {
				t.Fatal("materializeBuildOutputs() unexpectedly succeeded after injected failure")
			}
			entries, err := os.ReadDir(prepared.paths.buildDir)
			if err != nil {
				t.Fatalf("failed build root missing: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed build root is not empty: %#v", entries)
			}
			if after := buildOutputsInputHashes(t, prepared); !mapsEqual(before, after) {
				t.Fatalf("prepared inputs changed after injected failure: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestMaterializeBuildOutputsRejectsSourceMutationAfterAnOutputAndCleansRoot(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	original := buildOutputsAfterOutput
	buildOutputsAfterOutput = func(path string) error {
		if filepath.Base(path) != buildOutputDCIFilename {
			return nil
		}
		file, err := os.OpenFile(prepared.paths.sources.dci, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if _, err := file.Write([]byte("source mutation")); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
	t.Cleanup(func() { buildOutputsAfterOutput = original })

	_, err := materializeBuildOutputs(context.Background(), prepared)
	if err == nil || errorCode(err, "unknown") != "source_changed" {
		t.Fatalf("source mutation result = %v, code=%s, want source_changed", err, errorCode(err, "unknown"))
	}
	entries, err := os.ReadDir(prepared.paths.buildDir)
	if err != nil {
		t.Fatalf("failed build root missing: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed build root is not empty after source mutation: %#v", entries)
	}
}

func TestMaterializeBuildOutputsRejectsMalformedPreparedBuildBeforeCreatingRoot(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	prepared.artifactHashes = nil
	_, err := materializeBuildOutputs(context.Background(), prepared)
	if err == nil || errorCode(err, "unknown") != "invalid_input" {
		t.Fatalf("malformed prepared result = %v, code=%s, want invalid_input", err, errorCode(err, "unknown"))
	}
	if _, err := os.Lstat(prepared.paths.buildDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("build root was created for malformed prepared input: %v", err)
	}
	if strings.Contains(err.Error(), prepared.paths.snapshotDir) || strings.Contains(err.Error(), "legacy-search-1") {
		t.Fatalf("malformed prepared error leaked private value: %v", err)
	}
}

func newBuildOutputsPreparedFixture(t *testing.T) preparedBuild {
	t.Helper()
	sourceRoot := makeTestSnapshot(t, "build-outputs-source")
	eventStorePath := filepath.Join(sourceRoot, "source-event-store")
	if err := os.Remove(eventStorePath); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatal(err)
	}
	root := modulecore.NewRootEventEnvelope("orchestrator", "conversation.message.received", time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC), map[string]any{"kind": "root"})
	child := modulecore.NewEventEnvelope(root.TraceID, root.EventID, nil, "orchestrator", "conversation.message.completed", root.OccurredAt.Add(time.Second), map[string]any{"kind": "child"})
	if err := store.AppendBatch(context.Background(), []modulecore.EventEnvelope{root, child}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot := filepath.Join(t.TempDir(), "captured")
	if _, err := Capture(context.Background(), CaptureOptions{
		SnapshotDir: snapshot, LiveDCI: filepath.Join(sourceRoot, "source-dci"), LiveDCIJSONL: filepath.Join(sourceRoot, "source-dci-jsonl"),
		LiveEventStore: eventStorePath, LiveL1: filepath.Join(sourceRoot, "source-l1"), LiveArchive: filepath.Join(sourceRoot, "source-archive"),
	}); err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if _, err := DryRun(context.Background(), Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: "dry-run.json",
		Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	}); err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	prepared, err := prepareBuild(context.Background(), buildOptions{
		SnapshotDir: snapshot, BuildDir: filepath.Join(t.TempDir(), "build-output"), CaptureReceipt: CaptureReceiptFilename, DryRunManifest: "dry-run.json", AgentIDs: append([]string(nil), testAgentIDs...),
	})
	if err != nil {
		t.Fatalf("prepareBuild() error = %v", err)
	}
	return prepared
}

func buildOutputsInputHashes(t *testing.T, prepared preparedBuild) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(captureArtifactSpecs)+2)
	for _, spec := range captureArtifactSpecs {
		path := filepath.Join(prepared.paths.sources.root, spec.filename)
		hash, err := fileSHA256(path)
		if err != nil {
			t.Fatalf("hash prepared artifact %s: %v", spec.role, err)
		}
		hashes[spec.role] = hash
	}
	for name, path := range map[string]string{
		"capture":  prepared.paths.captureReceipt,
		"manifest": prepared.paths.dryRunManifest,
	} {
		hash, err := fileSHA256(path)
		if err != nil {
			t.Fatalf("hash prepared input %s: %v", name, err)
		}
		hashes[name] = hash
	}
	return hashes
}
