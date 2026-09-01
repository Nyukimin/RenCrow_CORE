package dcimigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestApplyStagedCutoverRequiresImplementation(t *testing.T) {
	if _, err := applyStagedCutover(context.Background(), preparedCutoverStage{}); err == nil {
		t.Fatal("empty staged cutover unexpectedly succeeded")
	}
}

func TestPreflightStagedCutoverIsReadOnlyAndReturnsBlockedSeed(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	beforeActive := cutoverActiveTestHashes(t, fixture.paths)
	beforeBuild := cutoverBoundTestHashes(t, active.build.files)
	beforeStage := cutoverStageTestHashes(t, stage)
	retired := filepath.Join(filepath.Dir(active.paths.dciJSONL), cutoverJSONLRetiredStageName)
	if _, err := os.Lstat(retired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired JSONL stage exists before preflight: %v", err)
	}

	prepared, seed, err := preflightStagedCutover(context.Background(), stage)
	if err != nil {
		t.Fatalf("preflightStagedCutover() error = %v", err)
	}
	if !sameCutoverActiveCohort(stage.active, prepared.active) || !sameCutoverActiveCohort(stage.active, prepared.staged.active) {
		t.Fatal("preflight did not retain the bound cohort")
	}
	if seed.Status != CutoverStatusBlocked || seed.ErrorCode != "cutover_preflight" {
		t.Fatalf("preflight seed = %#v", seed)
	}
	if err := validateCutoverReceipt(seed); err != nil {
		t.Fatalf("preflight seed validation error = %v", err)
	}
	if len(seed.OutputArtifacts) != 0 || seed.OutputArtifactSetSHA256 != "" || seed.RollbackArtifactSetSHA256 != "" || seed.ReplacementArtifactSetSHA256 != "" {
		t.Fatalf("blocked seed contains mutation claims: %#v", seed)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(beforeActive, got) {
		t.Fatalf("preflight changed active files: before=%#v after=%#v", beforeActive, got)
	}
	if got := cutoverBoundTestHashes(t, active.build.files); !mapsEqual(beforeBuild, got) {
		t.Fatalf("preflight changed build files: before=%#v after=%#v", beforeBuild, got)
	}
	if got := cutoverStageTestHashes(t, stage); !mapsEqual(beforeStage, got) {
		t.Fatalf("preflight changed stage files: before=%#v after=%#v", beforeStage, got)
	}
	if _, err := os.Lstat(retired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight created retired JSONL stage: %v", err)
	}
	if _, err := os.Lstat(active.build.paths.cutoverReceipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight created cutover receipt: %v", err)
	}
}

func TestCutoverReceiptSeedRetainsLegacyActorLabelCounts(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	activeRoot := t.TempDir()
	activePaths := cutoverActiveCohortTestFiles{
		dci: filepath.Join(activeRoot, "source-dci"), dciJSONL: filepath.Join(activeRoot, "source-dci-jsonl"),
		eventStore: filepath.Join(activeRoot, "source-event-store"), l1: filepath.Join(activeRoot, "source-l1"),
		archive: filepath.Join(activeRoot, "source-archive"),
	}
	for _, item := range []struct{ source, target string }{
		{prepared.paths.sources.dci, activePaths.dci}, {prepared.paths.sources.dciJSONL, activePaths.dciJSONL},
		{prepared.paths.sources.eventStore, activePaths.eventStore}, {prepared.paths.sources.l1, activePaths.l1},
		{prepared.paths.sources.archive, activePaths.archive},
	} {
		copyFile(t, item.source, item.target)
	}
	activeOptions := cutoverActiveOptions{
		SourceDCI: activePaths.dci, SourceDCIJSONL: activePaths.dciJSONL, SourceEventStore: activePaths.eventStore,
		SourceL1: activePaths.l1, SourceArchive: activePaths.archive,
	}
	legacyAgentIDs := []string{"mio", "kuro", "midori"}
	legacyManifestName := "legacy-label-dry-run.json"
	_, err := DryRun(context.Background(), Options{
		SnapshotDir: prepared.paths.snapshotDir,
		SourceDCI:   "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store",
		SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: legacyManifestName,
		Expected: prepared.dryRunManifest.ExpectedCounts, AgentIDs: legacyAgentIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyBuildRoot := filepath.Join(t.TempDir(), "legacy-label-build")
	buildReceipt, err := Build(context.Background(), BuildOptions{
		SnapshotDir: prepared.paths.snapshotDir, BuildDir: legacyBuildRoot,
		CaptureReceipt: CaptureReceiptFilename, DryRunManifest: legacyManifestName, AgentIDs: legacyAgentIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	buildOptions, _ := newCutoverBuildCohortFixture(t, legacyBuildRoot, buildReceipt)
	build, err := prepareCutoverBuildCohort(context.Background(), buildOptions)
	if err != nil {
		t.Fatal(err)
	}
	active, err := prepareCutoverActiveCohort(context.Background(), build, activeOptions)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	labels := active.build.buildReceipt.LegacyActorLabelCounts
	if len(labels) != 1 || labels["shiro"] != 1 {
		t.Fatalf("legacy actor labels = %#v, want shiro aggregate", labels)
	}
	want := cloneBuildIntMap(labels)

	seed := newCutoverReceiptSeed(active)
	if !reflect.DeepEqual(seed.LegacyActorLabelCounts, want) {
		t.Fatalf("seed legacy actor labels = %#v, want %#v", seed.LegacyActorLabelCounts, want)
	}
	valid := newValidCutoverReceiptTestValue(active, stage)
	if !reflect.DeepEqual(valid.LegacyActorLabelCounts, seed.LegacyActorLabelCounts) {
		t.Fatalf("applied receipt legacy actor labels = %#v, want %#v", valid.LegacyActorLabelCounts, seed.LegacyActorLabelCounts)
	}
	encoded, err := marshalCutoverReceipt(valid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"legacy_actor_label_counts":{"shiro":1}`) {
		t.Fatalf("encoded receipt lost bounded legacy actor label aggregate: %s", encoded)
	}
	for _, private := range []string{"legacy-search-1", "legacy-evidence-1", activePaths.dci, legacyBuildRoot} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("encoded receipt leaked private value %q", private)
		}
	}
	labels["shiro"] = 2
	if !reflect.DeepEqual(seed.LegacyActorLabelCounts, want) {
		t.Fatal("seed legacy actor labels share the build receipt map")
	}
	seed.LegacyActorLabelCounts["shiro"] = 3
	if !reflect.DeepEqual(active.build.buildReceipt.LegacyActorLabelCounts, map[string]int{"shiro": 2}) {
		t.Fatal("seed mutation changed the build receipt labels")
	}
}

func TestApplyStagedCutoverExecutesForwardCutover(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := applyStagedCutover(context.Background(), stage)
	if err != nil {
		t.Fatalf("applyStagedCutover() error = %v", err)
	}
	if receipt.Status != CutoverStatusApplied || receipt.ErrorCode != "" {
		t.Fatalf("applied receipt = %#v", receipt)
	}
	if err := validateCutoverReceipt(receipt); err != nil {
		t.Fatalf("applied receipt validation error = %v", err)
	}
	if got := mustFileSHA256(t, fixture.paths.dci); got != active.build.buildReceipt.OutputArtifacts[buildOutputDCIRole].FileSHA256 {
		t.Fatalf("active DCI hash = %q", got)
	}
	if _, err := os.Lstat(fixture.paths.dciJSONL); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active JSONL remains: %v", err)
	}
	for _, path := range []string{
		filepath.Join(filepath.Dir(active.paths.dci), cutoverRestoreDCIName),
		filepath.Join(filepath.Dir(active.paths.eventStore), cutoverRestoreEventStoreName),
		filepath.Join(filepath.Dir(active.paths.l1), cutoverRestoreL1Name),
		filepath.Join(filepath.Dir(active.paths.archive), cutoverRestoreArchiveName),
		filepath.Join(filepath.Dir(active.build.paths.installedRuntime), cutoverRestoreRuntimeName),
		filepath.Join(filepath.Dir(active.paths.dciJSONL), cutoverRestoreDCIJSONLName),
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("restore stage %q is missing: %v", filepath.Base(path), err)
		}
	}
}

func cutoverStageTestHashes(t *testing.T, stage preparedCutoverStage) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(stage.rollbackFiles)+len(stage.stageFiles))
	for _, file := range append(append([]cutoverStageBinding{}, stage.rollbackFiles...), stage.stageFiles...) {
		hashes[file.target.path] = mustFileSHA256(t, file.target.path)
	}
	return hashes
}

func TestPreflightStagedCutoverRejectsInvalidContextAndCancellation(t *testing.T) {
	if _, _, err := preflightStagedCutover(nil, preparedCutoverStage{}); errorCode(err, "") != "invalid_context" {
		t.Fatalf("nil context error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := preflightStagedCutover(ctx, preparedCutoverStage{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
}

func TestPreflightStagedCutoverRejectsBoundAndProspectiveDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture cutoverActiveCohortTestFixture, active preparedCutoverActiveCohort, stage *preparedCutoverStage)
	}{
		{name: "active source", mutate: func(t *testing.T, fixture cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort, _ *preparedCutoverStage) {
			path := fixture.paths.dci
			if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "build output", mutate: func(t *testing.T, _ cutoverActiveCohortTestFixture, active preparedCutoverActiveCohort, _ *preparedCutoverStage) {
			path := filepath.Join(active.build.paths.buildRoot, buildOutputDCIFilename)
			if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "rollback binding", mutate: func(t *testing.T, _ cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort, stage *preparedCutoverStage) {
			path := stage.rollbackFiles[0].target.path
			if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replacement binding", mutate: func(t *testing.T, _ cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort, stage *preparedCutoverStage) {
			path := stage.stageFiles[0].target.path
			if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stage evidence", mutate: func(_ *testing.T, _ cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort, stage *preparedCutoverStage) {
			stage.evidence.NonAlias = 0
		}},
		{name: "receipt already exists", mutate: func(t *testing.T, fixture cutoverActiveCohortTestFixture, _ preparedCutoverActiveCohort, _ *preparedCutoverStage) {
			if err := os.WriteFile(fixture.build.paths.cutoverReceipt, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retired stage already exists", mutate: func(t *testing.T, fixture cutoverActiveCohortTestFixture, active preparedCutoverActiveCohort, _ *preparedCutoverStage) {
			path := filepath.Join(filepath.Dir(active.paths.dciJSONL), cutoverJSONLRetiredStageName)
			if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			_ = fixture
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
			if err != nil {
				t.Fatal(err)
			}
			stage, err := stageCutoverCohort(context.Background(), active)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, fixture, active, &stage)
			beforeStage := cutoverStageTestHashes(t, stage)
			if _, _, err := preflightStagedCutover(context.Background(), stage); err == nil {
				t.Fatal("drifted staged cohort was accepted")
			} else if containsCutoverActivePrivateValue(err, fixture) {
				t.Fatalf("preflight error leaked private value: %v", err)
			}
			if got := cutoverStageTestHashes(t, stage); !mapsEqual(beforeStage, got) {
				t.Fatalf("preflight changed staged files: before=%#v after=%#v", beforeStage, got)
			}
		})
	}
}

func TestValidateCutoverReceiptStatusClaims(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	valid := newValidCutoverReceiptTestValue(active, stage)
	for _, tt := range []struct {
		name  string
		value func(CutoverReceipt) CutoverReceipt
		valid bool
	}{
		{name: "applied", value: func(value CutoverReceipt) CutoverReceipt { return value }, valid: true},
		{name: "rolled back", value: func(value CutoverReceipt) CutoverReceipt {
			value.Status = CutoverStatusRolledBack
			value.ErrorCode = "rollback_complete"
			value.ActiveAfterArtifactSetSHA256 = ""
			value.RestoredArtifactSetSHA256 = value.ActiveBeforeArtifactSetSHA256
			value.JSONLRetired = 0
			value.JSONLRestored = 1
			return value
		}, valid: true},
		{name: "rollback failed", value: func(value CutoverReceipt) CutoverReceipt {
			value.Status = CutoverStatusRollbackFailed
			value.ErrorCode = CutoverStatusRollbackFailed
			value.ActiveAfterArtifactSetSHA256 = ""
			value.RestoredArtifactSetSHA256 = ""
			value.JSONLRestored = 0
			return value
		}, valid: true},
		{name: "blocked claims output", value: func(value CutoverReceipt) CutoverReceipt {
			value.Status = CutoverStatusBlocked
			value.ErrorCode = "blocked"
			return value
		}, valid: false},
		{name: "rollback failed claims restored", value: func(value CutoverReceipt) CutoverReceipt {
			value.Status = CutoverStatusRollbackFailed
			value.ErrorCode = CutoverStatusRollbackFailed
			value.ActiveAfterArtifactSetSHA256 = ""
			value.RestoredArtifactSetSHA256 = strings.Repeat("c", 64)
			value.JSONLRestored = 0
			return value
		}, valid: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			value := tt.value(valid)
			if got := validateCutoverReceipt(value) == nil; got != tt.valid {
				t.Fatalf("validateCutoverReceipt() valid = %v, want %v", got, tt.valid)
			}
		})
	}
}

func newValidCutoverReceiptTestValue(active preparedCutoverActiveCohort, stage preparedCutoverStage) CutoverReceipt {
	value := newCutoverReceiptSeed(active)
	build := active.build.buildReceipt
	value.Status = CutoverStatusApplied
	value.ErrorCode = ""
	value.OutputArtifacts = cloneCutoverOutputArtifactsForTest(build.OutputArtifacts)
	value.OutputArtifactSetSHA256 = build.OutputArtifactSetSHA256
	value.RollbackArtifactSetSHA256 = stage.evidence.RollbackArtifactSetSHA256
	value.ReplacementArtifactSetSHA256 = stage.evidence.ReplacementArtifactSetSHA256
	value.ActiveBeforeArtifactSetSHA256 = strings.Repeat("a", 64)
	value.ActiveAfterArtifactSetSHA256 = strings.Repeat("b", 64)
	value.OldRuntimeSHA256 = cutoverRuntimeHashForTest(active.build.files, active.build.paths.installedRuntime)
	value.NewRuntimeSHA256 = cutoverRuntimeHashForTest(active.build.files, active.build.paths.stagedRuntime)
	value.RollbackFileCount = 7
	value.ReplacementFileCount = 5
	value.ActiveFileCount = 5
	value.JSONLRetired = 1
	value.QuickCheckOK = 1
	value.SidecarZero = 1
	value.SourceInputsStable = 1
	value.DCI = build.DCI
	value.EventStore = build.EventStore
	value.L1 = build.L1
	value.Archive = build.Archive
	return value
}

func cutoverRuntimeHashForTest(files []cutoverBoundFile, path string) string {
	for _, file := range files {
		if samePath(file.path, path) {
			return file.sha256
		}
	}
	return ""
}

func TestCutoverReceiptStrictEncodingAndNoPrivateValues(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	receipt := newValidCutoverReceiptTestValue(active, stage)
	encoded, err := marshalCutoverReceipt(receipt)
	if err != nil {
		t.Fatalf("marshalCutoverReceipt() error = %v", err)
	}
	if int64(len(encoded)) > maxCutoverReceiptBytes {
		t.Fatalf("encoded receipt size = %d", len(encoded))
	}
	for _, private := range []string{fixture.paths.dci, fixture.paths.dciJSONL, fixture.paths.eventStore, fixture.paths.l1, fixture.paths.archive, fixture.build.paths.buildRoot, "legacy-search-1", "legacy-evidence-1"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("receipt leaked private value %q", private)
		}
	}
	path := filepath.Join(t.TempDir(), "cutover.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := readCutoverReceipt(path)
	if err != nil || !reflect.DeepEqual(read, receipt) {
		t.Fatalf("readCutoverReceipt() = %#v, %v", read, err)
	}
	if err := os.WriteFile(path, append(encoded, []byte("{}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCutoverReceipt(path); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	if err := os.WriteFile(path, make([]byte, maxCutoverReceiptBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCutoverReceipt(path); err == nil {
		t.Fatal("oversized receipt was accepted")
	}
	unknown := strings.TrimSuffix(string(encoded), "\n")
	unknown = strings.TrimSuffix(unknown, "}") + `,"unexpected":"secret"}` + "\n"
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCutoverReceipt(path); err == nil {
		t.Fatal("unknown JSON field was accepted")
	}
}

func TestValidateCutoverReceiptRejectsMalformedClaims(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	base := newValidCutoverReceiptTestValue(active, stage)
	tests := []struct {
		name   string
		mutate func(*CutoverReceipt)
	}{
		{name: "missing source map key", mutate: func(value *CutoverReceipt) {
			value.SourceSchemaSHA256 = cloneBuildStringMap(value.SourceSchemaSHA256)
			delete(value.SourceSchemaSHA256, "source_dci")
		}},
		{name: "extra source map key", mutate: func(value *CutoverReceipt) {
			value.SourceSchemaSHA256 = cloneBuildStringMap(value.SourceSchemaSHA256)
			value.SourceSchemaSHA256["unexpected"] = strings.Repeat("a", 64)
		}},
		{name: "uppercase plan hash", mutate: func(value *CutoverReceipt) {
			value.MappingSHA256 = strings.ToUpper(value.MappingSHA256)
		}},
		{name: "negative count", mutate: func(value *CutoverReceipt) {
			value.ExpectedCounts.Searches = -1
		}},
		{name: "owner count exceeds bound", mutate: func(value *CutoverReceipt) {
			value.DCI.TraceRows = maxLogicalRows + 1
		}},
		{name: "non-UTC timestamp", mutate: func(value *CutoverReceipt) {
			value.StartedAt = time.Date(2026, 9, 1, 0, 0, 0, 0, time.FixedZone("offset", 0))
		}},
		{name: "backwards timestamp", mutate: func(value *CutoverReceipt) {
			value.CompletedAt = value.StartedAt.Add(-time.Second)
		}},
		{name: "unknown status", mutate: func(value *CutoverReceipt) {
			value.Status = "partial"
		}},
		{name: "invalid error code", mutate: func(value *CutoverReceipt) {
			value.Status = CutoverStatusRolledBack
			value.ErrorCode = "contains secret"
		}},
		{name: "rollback failed error code", mutate: func(value *CutoverReceipt) {
			value.Status = CutoverStatusRollbackFailed
			value.ErrorCode = "partial_restore"
		}},
		{name: "extra output role", mutate: func(value *CutoverReceipt) {
			value.OutputArtifacts = cloneCutoverOutputArtifactsForTest(value.OutputArtifacts)
			value.OutputArtifacts["unexpected"] = BuildOutputArtifact{}
		}},
		{name: "output artifact set mismatch", mutate: func(value *CutoverReceipt) {
			value.OutputArtifactSetSHA256 = strings.Repeat("c", 64)
		}},
		{name: "invalid schema", mutate: func(value *CutoverReceipt) {
			value.SchemaVersion = "other/v1"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := base
			tt.mutate(&value)
			if err := validateCutoverReceipt(value); err == nil {
				t.Fatal("malformed receipt was accepted")
			}
		})
	}
}

func TestValidateCutoverReceiptRejectsMalformedLegacyActorLabels(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	active, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageCutoverCohort(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	base := newValidCutoverReceiptTestValue(active, stage)
	base.LegacyActorLabelCounts = map[string]int{"Worker": 1}
	tests := []struct {
		name   string
		mutate func(*CutoverReceipt)
	}{
		{name: "empty label", mutate: func(value *CutoverReceipt) {
			value.LegacyActorLabelCounts = map[string]int{"": 1}
		}},
		{name: "oversized label", mutate: func(value *CutoverReceipt) {
			value.LegacyActorLabelCounts = map[string]int{strings.Repeat("x", maxActorLabel+1): 1}
		}},
		{name: "negative count", mutate: func(value *CutoverReceipt) {
			value.LegacyActorLabelCounts = map[string]int{"Worker": -1}
		}},
		{name: "oversized count map", mutate: func(value *CutoverReceipt) {
			value.LegacyActorLabelCounts = make(map[string]int, maxActorLabels+1)
			for index := 0; index <= maxActorLabels; index++ {
				value.LegacyActorLabelCounts[fmt.Sprintf("actor-%d", index)] = 1
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := base
			tt.mutate(&value)
			if err := validateCutoverReceipt(value); err == nil {
				t.Fatal("malformed legacy actor labels were accepted")
			}
		})
	}
}

func cloneCutoverOutputArtifactsForTest(input map[string]BuildOutputArtifact) map[string]BuildOutputArtifact {
	output := make(map[string]BuildOutputArtifact, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
