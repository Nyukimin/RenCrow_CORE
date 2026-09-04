package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareApplyAndRollbackCutoverSwapsPreservesBothGenerations(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	specs := cutoverTestSpecs(t, directory)
	if err := prepareCutoverSwaps(context.Background(), specs, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	completed, err := applyCutoverSwaps(specs)
	if err != nil || len(completed) != len(specs) {
		t.Fatalf("applyCutoverSwaps() = %d, %v", len(completed), err)
	}
	for _, spec := range specs {
		assertCutoverFileContent(t, spec.target, "new-"+spec.role)
		assertCutoverFileContent(t, spec.rollback, "old-"+spec.role)
		if _, err := os.Lstat(spec.candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("candidate %s still exists: %v", spec.role, err)
		}
	}
	if err := rollbackCutoverSwaps(completed); err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		assertCutoverFileContent(t, spec.target, "old-"+spec.role)
		assertCutoverFileContent(t, spec.candidate, "new-"+spec.role)
		if _, err := os.Lstat(spec.rollback); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback %s remains after restore: %v", spec.role, err)
		}
	}
}

func TestApplyCutoverSwapsMidFailureCanRollbackCompletedPairs(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	specs := cutoverTestSpecs(t, directory)
	if err := prepareCutoverSwaps(context.Background(), specs, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	originalRename := cutoverRename
	calls := 0
	cutoverRename = func(oldPath, newPath string) error {
		calls++
		if calls == 4 {
			return errors.New("injected rename failure")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() { cutoverRename = originalRename })
	completed, err := applyCutoverSwaps(specs)
	if err == nil || len(completed) != 1 {
		t.Fatalf("mid-failure result = %d, %v", len(completed), err)
	}
	if err := rollbackCutoverSwaps(completed); err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		assertCutoverFileContent(t, spec.target, "old-"+spec.role)
		assertCutoverFileContent(t, spec.candidate, "new-"+spec.role)
	}
}

func TestPrepareCutoverSwapsRejectsLaterDifferentFilesystemBeforeApply(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	specs := cutoverTestSpecs(t, directory)
	buildHash := strings.Repeat("b", 64)
	originalSameFilesystem := cutoverSameFilesystem
	originalRename := cutoverRename
	checks := 0
	renameCalls := 0
	cutoverSameFilesystem = func(candidatePath, targetPath string, candidateInfo, targetInfo os.FileInfo) bool {
		checks++
		return checks != len(specs)
	}
	cutoverRename = func(oldPath, newPath string) error {
		renameCalls++
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		cutoverSameFilesystem = originalSameFilesystem
		cutoverRename = originalRename
	})

	if err := prepareCutoverSwaps(context.Background(), specs, buildHash); err == nil {
		t.Fatal("different filesystem pair was accepted")
	}
	if checks != len(specs) {
		t.Fatalf("filesystem checks = %d, want %d", checks, len(specs))
	}
	if renameCalls != 0 {
		t.Fatalf("cutover renames = %d, want 0", renameCalls)
	}
	for _, spec := range specs {
		assertCutoverFileContent(t, spec.candidate, "new-"+spec.role)
		assertCutoverFileContent(t, spec.target, "old-"+spec.role)
		if _, err := os.Lstat(spec.target + ".pre-threadid-" + buildHash[:12]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback %s was created: %v", spec.role, err)
		}
	}
}

func TestPrepareCutoverSwapsRejectsOccupiedRollbackAndSQLiteSidecar(t *testing.T) {
	t.Run("occupied rollback", func(t *testing.T) {
		directory := canonicalThreadConfigTestDir(t)
		specs := cutoverTestSpecs(t, directory)
		occupied := specs[0].target + ".pre-threadid-" + strings.Repeat("c", 12)
		if err := os.WriteFile(occupied, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := prepareCutoverSwaps(context.Background(), specs, strings.Repeat("c", 64)); err == nil {
			t.Fatal("occupied rollback was accepted")
		}
		assertCutoverFileContent(t, occupied, "preserve")
	})
	t.Run("SQLite sidecar", func(t *testing.T) {
		directory := canonicalThreadConfigTestDir(t)
		specs := cutoverTestSpecs(t, directory)
		if err := os.WriteFile(specs[0].target+"-wal", []byte("writer-active"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := prepareCutoverSwaps(context.Background(), specs, strings.Repeat("d", 64)); err == nil {
			t.Fatal("SQLite sidecar was accepted")
		}
	})
}

func TestRunCutoverWritesCanonicalSuccessReceiptAndArguments(t *testing.T) {
	want := validCutoverReceipt(t)
	var got cutoverOptions
	var stdout bytes.Buffer
	args := []string{
		"--build-dir", "build", "--stage-dir", "stage", "--l1-target", "l1",
		"--archive-target", "archive", "--topic-target", "topic", "--config-target", "config",
		"--runtime-candidate", "runtime-new", "--runtime-target", "runtime-old",
	}
	code := runCutover(args, &stdout, func(_ context.Context, options cutoverOptions) (cutoverReceipt, error) {
		got = options
		return want, nil
	})
	if code != 0 || got.RuntimeCandidate != "runtime-new" || got.RuntimeTarget != "runtime-old" || got.L1Target != "l1" {
		t.Fatalf("runCutover() = code %d options %+v output %q", code, got, stdout.String())
	}
	encoded, _ := json.Marshal(want)
	if stdout.String() != string(append(encoded, '\n')) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "runtime-new") || strings.Contains(stdout.String(), `"build"`) {
		t.Fatal("cutover receipt leaked a path")
	}
}

func TestCutoverReceiptTamperAndInvalidArgumentsFailClosed(t *testing.T) {
	receipt := validCutoverReceipt(t)
	receipt.MappingSHA256 = strings.Repeat("f", 64)
	if err := receipt.validate(); err == nil {
		t.Fatal("tampered cutover receipt was accepted")
	}
	var stdout bytes.Buffer
	if code := runCutover([]string{"--build-dir", "only"}, &stdout, nil); code == 0 {
		t.Fatal("invalid cutover arguments succeeded")
	}
	var blocked cutoverReceipt
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &blocked); err != nil || blocked.validate() != nil || blocked.Status != cutoverStatusBlocked {
		t.Fatalf("blocked receipt = %+v, decode=%v validate=%v", blocked, err, blocked.validate())
	}
}

func TestWriteCutoverReceiptFilePublishesPrivateCanonicalReceipt(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	receipt := validCutoverReceipt(t)
	if err := writeCutoverReceiptFile(directory, receipt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, cutoverReceiptFilename)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt metadata = %v, %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(receipt)
	want = append(want, '\n')
	if !bytes.Equal(data, want) {
		t.Fatalf("receipt = %q, want %q", data, want)
	}
}

func cutoverTestSpecs(t *testing.T, directory string) []cutoverSwapSpec {
	t.Helper()
	specs := make([]cutoverSwapSpec, 0, 5)
	for index, role := range []string{"l1", "archive", "topic", "config", "runtime"} {
		mode := os.FileMode(0o600)
		executable := false
		if role == "runtime" {
			mode = 0o755
			executable = true
		}
		candidate := filepath.Join(directory, role+".candidate")
		target := filepath.Join(directory, role+".active")
		if err := os.WriteFile(candidate, []byte("new-"+role), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("old-"+role), mode); err != nil {
			t.Fatal(err)
		}
		specs = append(specs, cutoverSwapSpec{
			role: role, candidate: candidate, target: target,
			oldHash: cutoverSHA256([]byte("old-" + role)), newHash: cutoverSHA256([]byte("new-" + role)),
			mode: mode, executable: executable, sqlite: index < 2,
		})
	}
	return specs
}

func validCutoverReceipt(t *testing.T) cutoverReceipt {
	t.Helper()
	hashes := []string{
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64),
		strings.Repeat("6", 64), strings.Repeat("7", 64), strings.Repeat("8", 64), strings.Repeat("9", 64), strings.Repeat("a", 64),
		strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64),
	}
	receipt := cutoverReceipt{
		SchemaVersion: cutoverSchemaVersion, BuildReceiptSHA256: hashes[0], StageReceiptSHA256: hashes[1], MappingSHA256: hashes[2],
		L1OldSHA256: hashes[3], L1NewSHA256: hashes[4], ArchiveOldSHA256: hashes[5], ArchiveNewSHA256: hashes[6],
		TopicOldSHA256: hashes[7], TopicNewSHA256: hashes[8], ConfigOldSHA256: hashes[9], ConfigNewSHA256: hashes[10],
		RuntimeOldSHA256: hashes[11], RuntimeNewSHA256: hashes[12], RollbackArtifactsRetained: true,
	}
	receipt = sealCutoverReceipt(receipt, cutoverStatusApplied, "")
	if err := receipt.validate(); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func assertCutoverFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, %v; want %q", filepath.Base(path), data, err, want)
	}
}
