package dcimigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareBuildValidatesCaptureAndManifestWithoutCreatingBuildRoot(t *testing.T) {
	snapshot, _, options := newBuildPrepareFixture(t)
	before := buildPrepareSourceHashes(t, snapshot)

	prepared, err := prepareBuild(context.Background(), options)
	if err != nil {
		t.Fatalf("prepareBuild() error = %v", err)
	}
	if prepared.captureReceiptSHA256 == "" || prepared.dryRunManifestSHA256 == "" {
		t.Fatalf("build preparation did not retain receipt hashes: %#v", prepared)
	}
	if prepared.plan.Events == nil || prepared.snapshot.Searches == nil {
		t.Fatalf("build preparation did not retain classified snapshot and plan: %#v", prepared)
	}
	if _, err := os.Lstat(options.BuildDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("build root was created: %v", err)
	}
	if after := buildPrepareSourceHashes(t, snapshot); !mapsEqual(before, after) {
		t.Fatalf("source hashes changed: before=%#v after=%#v", before, after)
	}
}

func TestPrepareBuildRejectsBlockedWrongUnknownTrailingAndOversizedCaptureReceipts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, path string, receipt CaptureReceipt)
	}{
		{
			name: "blocked",
			mutate: func(t *testing.T, path string, receipt CaptureReceipt) {
				receipt.Status = StatusBlocked
				receipt.ErrorCode = "capture_failed"
				writeBuildPrepareJSON(t, path, receipt)
			},
		},
		{
			name: "wrong header",
			mutate: func(t *testing.T, path string, receipt CaptureReceipt) {
				receipt.Mode = ModeDryRun
				writeBuildPrepareJSON(t, path, receipt)
			},
		},
		{
			name: "unknown field",
			mutate: func(t *testing.T, path string, receipt CaptureReceipt) {
				encoded, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				trimmed := bytes.TrimSpace(encoded)
				encoded = append(append(trimmed[:len(trimmed)-1], []byte(`,"unexpected":true}`)...), '\n')
				if err := os.WriteFile(path, encoded, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "trailing JSON",
			mutate: func(t *testing.T, path string, receipt CaptureReceipt) {
				encoded, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				encoded = append(encoded, '\n')
				encoded = append(encoded, []byte(`{}`)...)
				if err := os.WriteFile(path, encoded, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "oversized",
			mutate: func(t *testing.T, path string, _ CaptureReceipt) {
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), int(maxCaptureManifestBytes)+1), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, options := newBuildPrepareFixture(t)
			receipt := readBuildPrepareCaptureReceipt(t, filepath.Join(options.SnapshotDir, CaptureReceiptFilename))
			tt.mutate(t, filepath.Join(options.SnapshotDir, CaptureReceiptFilename), receipt)
			if _, err := prepareBuild(context.Background(), options); err == nil {
				t.Fatal("prepareBuild() unexpectedly accepted malformed capture receipt")
			}
		})
	}
}

func TestPrepareBuildRejectsCaptureArtifactDriftAndSetMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, snapshot string, receipt CaptureReceipt)
	}{
		{
			name: "physical size and hash drift",
			mutate: func(t *testing.T, snapshot string, _ CaptureReceipt) {
				path := filepath.Join(snapshot, "source-dci")
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("drift")); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "receipt artifact hash drift",
			mutate: func(t *testing.T, snapshot string, receipt CaptureReceipt) {
				artifact := receipt.Artifacts["source_dci"]
				artifact.FileSHA256 = strings.Repeat("0", 64)
				receipt.Artifacts["source_dci"] = artifact
				receipt.ArtifactSetSHA256 = captureArtifactSetSHA256(receipt.Artifacts)
				writeBuildPrepareJSON(t, filepath.Join(snapshot, CaptureReceiptFilename), receipt)
			},
		},
		{
			name: "artifact set hash drift",
			mutate: func(t *testing.T, snapshot string, receipt CaptureReceipt) {
				receipt.ArtifactSetSHA256 = strings.Repeat("0", 64)
				writeBuildPrepareJSON(t, filepath.Join(snapshot, CaptureReceiptFilename), receipt)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, options := newBuildPrepareFixture(t)
			receipt := readBuildPrepareCaptureReceipt(t, filepath.Join(options.SnapshotDir, CaptureReceiptFilename))
			tt.mutate(t, options.SnapshotDir, receipt)
			if _, err := prepareBuild(context.Background(), options); err == nil {
				t.Fatal("prepareBuild() unexpectedly accepted capture drift")
			}
		})
	}
}

func TestPrepareBuildRejectsManifestSemanticMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "expected count", mutate: func(manifest *Manifest) { manifest.ExpectedCounts.Searches++ }},
		{name: "logical hash", mutate: func(manifest *Manifest) { manifest.SourceDatabaseLogicalSHA256["source_dci"] = strings.Repeat("0", 64) }},
		{name: "classification hash", mutate: func(manifest *Manifest) {
			manifest.SourceDCIClassificationSHA256["source_dci"] = strings.Repeat("0", 64)
		}},
		{name: "normalization count", mutate: func(manifest *Manifest) { manifest.ExpectedCounts.NormalizedTextValues++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, manifest, options := newBuildPrepareFixture(t)
			tt.mutate(&manifest)
			writeBuildPrepareJSON(t, filepath.Join(options.SnapshotDir, options.DryRunManifest), manifest)
			if _, err := prepareBuild(context.Background(), options); err == nil {
				t.Fatal("prepareBuild() unexpectedly accepted manifest mismatch")
			}
		})
	}
}

func TestPrepareBuildRejectsOutsideOrSymlinkReceiptAndExistingOrSymlinkBuildRoot(t *testing.T) {
	_, _, options := newBuildPrepareFixture(t)
	outside := filepath.Join(t.TempDir(), "capture.json")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideOptions := options
	outsideOptions.CaptureReceipt = outside
	if _, err := prepareBuild(context.Background(), outsideOptions); err == nil {
		t.Fatal("outside capture receipt was accepted")
	}

	existingOptions := options
	if err := os.Mkdir(existingOptions.BuildDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareBuild(context.Background(), existingOptions); err == nil {
		t.Fatal("existing build root was accepted")
	}

	if runtime.GOOS == "windows" {
		return
	}
	_, _, symlinkOptions := newBuildPrepareFixture(t)
	symlinkReceipt := filepath.Join(t.TempDir(), "capture-link.json")
	if err := os.Symlink(filepath.Join(symlinkOptions.SnapshotDir, CaptureReceiptFilename), symlinkReceipt); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	symlinkReceiptOptions := symlinkOptions
	symlinkReceiptOptions.CaptureReceipt = symlinkReceipt
	if _, err := prepareBuild(context.Background(), symlinkReceiptOptions); err == nil {
		t.Fatal("symlink capture receipt was accepted")
	}

	_, _, symlinkBuildOptions := newBuildPrepareFixture(t)
	symlinkBuild := filepath.Join(t.TempDir(), "build-link")
	if err := os.Symlink(symlinkBuildOptions.SnapshotDir, symlinkBuild); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	symlinkBuildOptions.BuildDir = symlinkBuild
	if _, err := prepareBuild(context.Background(), symlinkBuildOptions); err == nil {
		t.Fatal("symlink build root was accepted")
	}
}

func TestPrepareBuildRejectsNonCanonicalSnapshotRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink parent semantics are not portable on Windows")
	}
	_, _, options := newBuildPrepareFixture(t)
	before := buildPrepareSourceHashes(t, options.SnapshotDir)
	realSnapshot := options.SnapshotDir

	finalLink := filepath.Join(t.TempDir(), "snapshot-link")
	if err := os.Symlink(realSnapshot, finalLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	finalOptions := options
	finalOptions.SnapshotDir = finalLink
	if _, err := prepareBuild(context.Background(), finalOptions); err == nil {
		t.Fatal("final snapshot symlink was accepted")
	}

	parentLink := filepath.Join(t.TempDir(), "parent-link")
	if err := os.Symlink(filepath.Dir(realSnapshot), parentLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	parentOptions := options
	parentOptions.SnapshotDir = filepath.Join(parentLink, filepath.Base(realSnapshot))
	if _, err := prepareBuild(context.Background(), parentOptions); err == nil {
		t.Fatal("snapshot through a symlinked parent was accepted")
	}
	if after := buildPrepareSourceHashes(t, realSnapshot); !mapsEqual(before, after) {
		t.Fatalf("source hashes changed after canonical-root rejection: before=%#v after=%#v", before, after)
	}
	if _, err := os.Lstat(options.BuildDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("build root was created after canonical-root rejection: %v", err)
	}
}

func TestPrepareBuildClassifiesSnapshotExactlyOnce(t *testing.T) {
	_, _, options := newBuildPrepareFixture(t)
	original := buildPrepareClassifySnapshot
	calls := 0
	buildPrepareClassifySnapshot = func(ctx context.Context, paths sourcePaths, options Options) (classificationReport, error) {
		calls++
		return original(ctx, paths, options)
	}
	t.Cleanup(func() { buildPrepareClassifySnapshot = original })
	if _, err := prepareBuild(context.Background(), options); err != nil {
		t.Fatalf("prepareBuild() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("classifySnapshot call count = %d, want 1", calls)
	}
}

func TestPrepareBuildRejectsCanceledContextWithoutMutation(t *testing.T) {
	_, _, options := newBuildPrepareFixture(t)
	before := buildPrepareSourceHashes(t, options.SnapshotDir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareBuild(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepareBuild(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := os.Lstat(options.BuildDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("build root was created for canceled preparation: %v", err)
	}
	if after := buildPrepareSourceHashes(t, options.SnapshotDir); !mapsEqual(before, after) {
		t.Fatalf("source hashes changed for canceled preparation: before=%#v after=%#v", before, after)
	}
}

func TestPrepareBuildRejectsWrongAgentIDsAfterOneClassification(t *testing.T) {
	_, _, options := newBuildPrepareFixture(t)
	original := buildPrepareClassifySnapshot
	calls := 0
	buildPrepareClassifySnapshot = func(ctx context.Context, paths sourcePaths, classifierOptions Options) (classificationReport, error) {
		calls++
		return original(ctx, paths, classifierOptions)
	}
	t.Cleanup(func() { buildPrepareClassifySnapshot = original })
	options.AgentIDs = []string{"mio"}
	if _, err := prepareBuild(context.Background(), options); err == nil {
		t.Fatal("prepareBuild() accepted wrong AgentIDs")
	}
	if calls != 1 {
		t.Fatalf("classifySnapshot call count = %d, want 1", calls)
	}
}

func newBuildPrepareFixture(t *testing.T) (string, Manifest, buildOptions) {
	t.Helper()
	source := makeTestSnapshot(t, "build-prepare-source")
	snapshot := filepath.Join(t.TempDir(), "captured")
	_, err := Capture(context.Background(), CaptureOptions{
		SnapshotDir: snapshot, LiveDCI: filepath.Join(source, "source-dci"), LiveDCIJSONL: filepath.Join(source, "source-dci-jsonl"),
		LiveEventStore: filepath.Join(source, "source-event-store"), LiveL1: filepath.Join(source, "source-l1"), LiveArchive: filepath.Join(source, "source-archive"),
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	manifest, err := DryRun(context.Background(), Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: "dry-run.json",
		Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	})
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	return snapshot, manifest, buildOptions{
		SnapshotDir: snapshot, BuildDir: filepath.Join(snapshot, "build-output"),
		CaptureReceipt: CaptureReceiptFilename, DryRunManifest: "dry-run.json", AgentIDs: append([]string(nil), testAgentIDs...),
	}
}

func readBuildPrepareCaptureReceipt(t *testing.T, path string) CaptureReceipt {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt CaptureReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func writeBuildPrepareJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func buildPrepareSourceHashes(t *testing.T, snapshot string) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(captureArtifactSpecs))
	for _, spec := range captureArtifactSpecs {
		if spec.role == "source_dci_jsonl" {
			hashes[spec.role], _ = fileSHA256(filepath.Join(snapshot, spec.filename))
			continue
		}
		hashes[spec.role], _ = fileSHA256(filepath.Join(snapshot, spec.filename))
	}
	return hashes
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
