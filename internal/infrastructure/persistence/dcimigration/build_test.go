package dcimigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuildCreatesFixedOfflineOutputsAndBoundedReceipt(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	before := buildOutputsInputHashes(t, prepared)

	receipt, err := Build(context.Background(), BuildOptions{
		SnapshotDir: prepared.paths.snapshotDir, BuildDir: prepared.paths.buildDir,
		CaptureReceipt: prepared.paths.captureReceipt, DryRunManifest: prepared.paths.dryRunManifest,
		AgentIDs: append([]string(nil), testAgentIDs...),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if receipt.Status != StatusReady || receipt.ErrorCode != "" || receipt.SchemaVersion != BuildSchemaVersion || receipt.Mode != ModeBuild {
		t.Fatalf("Build receipt = %#v", receipt)
	}
	if err := validateBuildReceipt(receipt); err != nil {
		t.Fatalf("validateBuildReceipt() error = %v", err)
	}
	if len(receipt.OutputArtifacts) != 4 || !isLowerHexSHA256(receipt.OutputArtifactSetSHA256) || receipt.BuildRootModeOK != 1 || receipt.SidecarZero != 1 || receipt.SourceInputsStable != 1 {
		t.Fatalf("output evidence = %#v", receipt)
	}
	for role, artifact := range receipt.OutputArtifacts {
		if artifact.FileSHA256 == "" || artifact.Bytes < 0 || artifact.QuickCheckOK != 1 || artifact.ForeignKeyViolations != 0 || artifact.SidecarZero != 1 {
			t.Fatalf("output role %q evidence = %#v", role, artifact)
		}
	}

	wantNames := map[string]struct{}{
		BuildReceiptFilename: {}, buildOutputDCIFilename: {}, buildOutputEventStoreFilename: {}, buildOutputL1Filename: {}, buildOutputArchiveFilename: {},
	}
	entries, err := os.ReadDir(prepared.paths.buildDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(wantNames) {
		t.Fatalf("build root entries = %d, want %d", len(entries), len(wantNames))
	}
	for _, entry := range entries {
		if _, ok := wantNames[entry.Name()]; !ok {
			t.Fatalf("unexpected build root entry %q", entry.Name())
		}
	}
	if runtime.GOOS != "windows" {
		rootInfo, err := os.Lstat(prepared.paths.buildDir)
		if err != nil {
			t.Fatal(err)
		}
		if rootInfo.Mode().Perm() != 0o700 {
			t.Fatalf("build root mode = %o, want 700", rootInfo.Mode().Perm())
		}
	}
	files := make(map[string]buildOutputFile, 4)
	for _, target := range buildOutputTargets(prepared.paths.buildDir) {
		info, err := os.Lstat(target.path)
		if err != nil {
			t.Fatalf("output %s: %v", target.name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			t.Fatalf("output %s mode/type = %v", target.name, info.Mode())
		}
		target.sha256, target.bytes, err = hashBuildFile(target.path)
		if err != nil {
			t.Fatalf("hash output %s: %v", target.name, err)
		}
		artifact := receipt.OutputArtifacts[target.role]
		if artifact.FileSHA256 != target.sha256 || artifact.Bytes != target.bytes {
			t.Fatalf("output %s does not match receipt: %#v", target.name, artifact)
		}
		target.quickCheckOK, target.sidecarZero = artifact.QuickCheckOK, artifact.SidecarZero
		files[target.role] = target
		for _, suffix := range sqliteSidecarSuffixes {
			if _, err := os.Lstat(target.path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("output %s sidecar %q exists: %v", target.name, suffix, err)
			}
		}
	}
	if got := buildOutputArtifactSetSHA256(files); got != receipt.OutputArtifactSetSHA256 {
		t.Fatalf("output artifact set = %q, recomputed %q", receipt.OutputArtifactSetSHA256, got)
	}
	diskBytes, err := readBuildInputBytes(filepath.Join(prepared.paths.buildDir, BuildReceiptFilename), maxBuildReceiptBytes)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(diskBytes)) > maxBuildReceiptBytes {
		t.Fatalf("receipt bytes = %d, want <= %d", len(diskBytes), maxBuildReceiptBytes)
	}
	var disk BuildReceipt
	if err := decodeOneBuildInputObject(diskBytes, &disk); err != nil {
		t.Fatalf("decode disk build receipt: %v", err)
	}
	if !reflect.DeepEqual(disk, receipt) {
		t.Fatal("disk build receipt differs from returned receipt")
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{prepared.paths.snapshotDir, prepared.paths.buildDir, "legacy-search-1", "legacy-evidence-1", "spec.md"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("build receipt contains forbidden value %q", forbidden)
		}
	}
	if after := buildOutputsInputHashes(t, prepared); !mapsEqual(before, after) {
		t.Fatalf("build inputs changed: before=%#v after=%#v", before, after)
	}
}

func TestBuildFailureAfterEachOutputLeavesOnlyBlockedReceipt(t *testing.T) {
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
				if filepath.Base(path) == role.file {
					return errors.New("injected output failure")
				}
				return nil
			}
			t.Cleanup(func() { buildOutputsAfterOutput = original })

			receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
			if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode == "" {
				t.Fatalf("Build() result = receipt=%#v err=%v", receipt, err)
			}
			entries, readErr := os.ReadDir(prepared.paths.buildDir)
			if readErr != nil {
				t.Fatalf("blocked build root missing: %v", readErr)
			}
			if len(entries) != 1 || entries[0].Name() != BuildReceiptFilename {
				t.Fatalf("blocked build root entries = %#v", entries)
			}
			blockedBytes, readErr := readBuildInputBytes(filepath.Join(prepared.paths.buildDir, BuildReceiptFilename), maxBuildReceiptBytes)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var blocked BuildReceipt
			if decodeErr := decodeOneBuildInputObject(blockedBytes, &blocked); decodeErr != nil || blocked.Status != StatusBlocked || blocked.OutputArtifactSetSHA256 != "" || len(blocked.OutputArtifacts) != 0 {
				t.Fatalf("blocked on-disk receipt = %#v decode=%v", blocked, decodeErr)
			}
			if after := buildOutputsInputHashes(t, prepared); !mapsEqual(before, after) {
				t.Fatalf("inputs changed after blocked build: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestBuildSourceMutationAfterOutputIsBlockedAndCleaned(t *testing.T) {
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

	receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "source_changed" {
		t.Fatalf("source mutation result = receipt=%#v err=%v", receipt, err)
	}
	entries, err := os.ReadDir(prepared.paths.buildDir)
	if err != nil {
		t.Fatalf("build root missing: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != BuildReceiptFilename {
		t.Fatalf("blocked build root entries = %#v", entries)
	}
}

func TestBuildRepeatedIndependentRootsRetainTheSamePlanBindings(t *testing.T) {
	first := newBuildOutputsPreparedFixture(t)
	second := newBuildOutputsPreparedFixture(t)
	firstReceipt, err := Build(context.Background(), buildOptionsFromPrepared(first))
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	secondReceipt, err := Build(context.Background(), buildOptionsFromPrepared(second))
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	for name, pair := range map[string][2]string{
		"mapping":      {firstReceipt.MappingSHA256, secondReceipt.MappingSHA256},
		"action set":   {firstReceipt.ActionSetSHA256, secondReceipt.ActionSetSHA256},
		"trace set":    {firstReceipt.TraceSetSHA256, secondReceipt.TraceSetSHA256},
		"evidence set": {firstReceipt.EvidenceSetSHA256, secondReceipt.EvidenceSetSHA256},
		"event set":    {firstReceipt.EventSetSHA256, secondReceipt.EventSetSHA256},
		"event plan":   {firstReceipt.EventPlanSHA256, secondReceipt.EventPlanSHA256},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("%s differs across roots: %q != %q", name, pair[0], pair[1])
		}
	}
}

func TestBuildReceiptWriterFailureDoesNotClaimReady(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	originalWriter := buildReceiptWriter
	callCount := 0
	buildReceiptWriter = func(path string, receipt BuildReceipt) error {
		callCount++
		if callCount == 1 {
			return errors.New("injected receipt write failure")
		}
		return originalWriter(path, receipt)
	}
	t.Cleanup(func() { buildReceiptWriter = originalWriter })

	receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "receipt_write" || callCount != 2 {
		t.Fatalf("single-use writer failure = receipt=%#v err=%v calls=%d", receipt, err, callCount)
	}
	entries, err := os.ReadDir(prepared.paths.buildDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != BuildReceiptFilename {
		t.Fatalf("blocked root entries = %#v", entries)
	}
}

func TestBuildPersistentReceiptWriterFailureLeavesEmptyRoot(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	originalWriter := buildReceiptWriter
	buildReceiptWriter = func(string, BuildReceipt) error { return errors.New("persistent receipt write failure") }
	t.Cleanup(func() { buildReceiptWriter = originalWriter })

	receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "receipt_write" {
		t.Fatalf("persistent writer failure = receipt=%#v err=%v", receipt, err)
	}
	entries, readErr := os.ReadDir(prepared.paths.buildDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("persistent writer failure left entries = %#v", entries)
	}
}

func TestBuildRejectsCanceledOrNonFreshRootWithoutCreatingOutput(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipt, err := Build(ctx, buildOptionsFromPrepared(prepared))
	if !errors.Is(err, context.Canceled) || receipt.Status != StatusBlocked || receipt.ErrorCode != "context_canceled" {
		t.Fatalf("canceled Build() = receipt=%#v err=%v", receipt, err)
	}
	if _, err := os.Lstat(prepared.paths.buildDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled Build created root: %v", err)
	}

	prepared = newBuildOutputsPreparedFixture(t)
	if err := os.Mkdir(prepared.paths.buildDir, 0o700); err != nil {
		t.Fatal(err)
	}
	receipt, err = Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "unsafe_path" {
		t.Fatalf("nonfresh Build() = receipt=%#v err=%v", receipt, err)
	}
	entries, err := os.ReadDir(prepared.paths.buildDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("nonfresh root modified: %#v", entries)
	}
}

func TestBuildReceiptValidationRejectsCrossBindingAndArtifactSetDrift(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err != nil {
		t.Fatal(err)
	}
	mutated := receipt
	mutated.OutputArtifacts = cloneBuildOutputArtifactMap(receipt.OutputArtifacts)
	mutated.OutputArtifactSetSHA256 = strings.Repeat("a", 64)
	if err := validateBuildReceipt(mutated); err == nil {
		t.Fatal("validateBuildReceipt accepted artifact-set drift")
	}
	mutated = receipt
	mutated.OutputArtifacts = cloneBuildOutputArtifactMap(receipt.OutputArtifacts)
	mutated.OutputArtifacts[buildOutputDCIRole] = receipt.OutputArtifacts[buildOutputEventStoreRole]
	if err := validateBuildReceipt(mutated); err == nil {
		t.Fatal("validateBuildReceipt accepted output/owner cross-binding drift")
	}
}

func TestBuildSourceMutationAfterReadyReceiptWriteIsBlocked(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	originalWriter := buildReceiptWriter
	callCount := 0
	buildReceiptWriter = func(path string, receipt BuildReceipt) error {
		callCount++
		if callCount == 1 {
			if err := originalWriter(path, receipt); err != nil {
				return err
			}
			file, err := os.OpenFile(prepared.paths.sources.dci, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			if _, err := file.Write([]byte("source mutation after receipt")); err != nil {
				_ = file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			return nil
		}
		return originalWriter(path, receipt)
	}
	t.Cleanup(func() { buildReceiptWriter = originalWriter })

	receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err == nil || receipt.Status != StatusBlocked || callCount != 2 {
		t.Fatalf("post-receipt source mutation result = receipt=%#v err=%v calls=%d", receipt, err, callCount)
	}
	entries, err := os.ReadDir(prepared.paths.buildDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != BuildReceiptFilename {
		t.Fatalf("post-receipt mutation root entries = %#v", entries)
	}
	blocked, err := readBuildInputBytes(filepath.Join(prepared.paths.buildDir, BuildReceiptFilename), maxBuildReceiptBytes)
	if err != nil {
		t.Fatal(err)
	}
	var blockedReceipt BuildReceipt
	if err := decodeOneBuildInputObject(blocked, &blockedReceipt); err != nil || blockedReceipt.Status != StatusBlocked {
		t.Fatalf("post-receipt mutation blocked receipt = %#v decode=%v", blockedReceipt, err)
	}
}

func TestValidateBlockedBuildReceiptRejectsAnyReadyClaim(t *testing.T) {
	receipt := newBaseBuildReceipt(time.Now().UTC())
	receipt.CompletedAt = receipt.StartedAt
	receipt.ErrorCode = "build_blocked"
	if err := validateBuildReceipt(receipt); err != nil {
		t.Fatalf("valid blocked receipt rejected: %v", err)
	}
	mutations := []func(*BuildReceipt){
		func(value *BuildReceipt) { value.BuildRootModeOK = 1 },
		func(value *BuildReceipt) { value.SidecarZero = 1 },
		func(value *BuildReceipt) { value.SourceInputsStable = 1 },
		func(value *BuildReceipt) { value.DCI.QuickCheckOK = 1 },
		func(value *BuildReceipt) { value.EventStore.OutputEnvelopeCount = 1 },
		func(value *BuildReceipt) { value.L1.CanonicalStagingRows = 1 },
		func(value *BuildReceipt) { value.Archive.SidecarZero = 1 },
	}
	for index, mutate := range mutations {
		candidate := receipt
		mutate(&candidate)
		if err := validateBuildReceipt(candidate); err == nil {
			t.Fatalf("blocked receipt mutation %d claimed ready evidence", index)
		}
	}
}

func TestBuildRejectsMalformedBoundInputBeforeCreatingRoot(t *testing.T) {
	for _, input := range []struct {
		name string
		path func(preparedBuild) string
		code string
	}{
		{name: "capture receipt", path: func(value preparedBuild) string { return value.paths.captureReceipt }, code: "capture_receipt"},
		{name: "dry-run manifest", path: func(value preparedBuild) string { return value.paths.dryRunManifest }, code: "dryrun_manifest"},
	} {
		t.Run(input.name, func(t *testing.T) {
			prepared := newBuildOutputsPreparedFixture(t)
			path := input.path(prepared)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("{}")...)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
			if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != input.code {
				t.Fatalf("malformed %s result = receipt=%#v err=%v", input.name, receipt, err)
			}
			if _, statErr := os.Lstat(prepared.paths.buildDir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("malformed %s created build root: %v", input.name, statErr)
			}
			if strings.Contains(err.Error(), prepared.paths.snapshotDir) || strings.Contains(err.Error(), "legacy-search-1") {
				t.Fatalf("malformed %s error leaked private value: %v", input.name, err)
			}
		})
	}
}

func buildOptionsFromPrepared(prepared preparedBuild) BuildOptions {
	return BuildOptions{
		SnapshotDir: prepared.paths.snapshotDir, BuildDir: prepared.paths.buildDir,
		CaptureReceipt: prepared.paths.captureReceipt, DryRunManifest: prepared.paths.dryRunManifest,
		AgentIDs: append([]string(nil), testAgentIDs...),
	}
}

func cloneBuildOutputArtifactMap(input map[string]BuildOutputArtifact) map[string]BuildOutputArtifact {
	output := make(map[string]BuildOutputArtifact, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
