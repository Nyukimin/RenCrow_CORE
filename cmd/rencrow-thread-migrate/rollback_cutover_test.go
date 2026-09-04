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

func TestPrepareExplicitRollbackAndRestore(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	specs := cutoverTestSpecs(t, directory)
	buildHash := strings.Repeat("e", 64)
	if err := prepareCutoverSwaps(context.Background(), specs, buildHash); err != nil {
		t.Fatal(err)
	}
	if _, err := applyCutoverSwaps(specs); err != nil {
		t.Fatal(err)
	}
	if err := prepareExplicitRollback(context.Background(), specs, buildHash); err != nil {
		t.Fatal(err)
	}
	if err := rollbackCutoverSwaps(specs); err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		assertCutoverFileContent(t, spec.target, "old-"+spec.role)
		assertCutoverFileContent(t, spec.candidate, "new-"+spec.role)
		if _, err := os.Lstat(spec.rollback); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback artifact %s remains: %v", spec.role, err)
		}
	}
}

func TestPrepareExplicitRollbackAcceptsMutableDriftAndPreservesDisplacedActive(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	specs := cutoverTestSpecs(t, directory)
	buildHash := strings.Repeat("e", 64)
	if err := prepareCutoverSwaps(context.Background(), specs, buildHash); err != nil {
		t.Fatal(err)
	}
	if _, err := applyCutoverSwaps(specs); err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs[:3] {
		if err := os.WriteFile(spec.target, []byte("drift-"+spec.role), spec.mode.Perm()); err != nil {
			t.Fatalf("drift %s: %v", spec.role, err)
		}
	}
	preparation, err := prepareExplicitRollbackState(context.Background(), specs, buildHash)
	if err != nil {
		t.Fatalf("prepareExplicitRollbackState() error = %v", err)
	}
	for _, spec := range specs[:3] {
		wantHash := cutoverSHA256([]byte("drift-" + spec.role))
		if preparation.observedMutableTargetHashes[spec.target] != wantHash {
			t.Fatalf("observed %s hash = %q, want %q", spec.role, preparation.observedMutableTargetHashes[spec.target], wantHash)
		}
	}
	if err := rollbackCutoverSwaps(specs); err != nil {
		t.Fatal(err)
	}
	if err := postcheckExplicitRollback(context.Background(), specs, preparation); err != nil {
		t.Fatalf("postcheckExplicitRollback() error = %v", err)
	}
	for _, spec := range specs {
		assertCutoverFileContent(t, spec.target, "old-"+spec.role)
		wantCandidate := "new-" + spec.role
		if isMutableRollbackRole(spec.role) {
			wantCandidate = "drift-" + spec.role
		}
		assertCutoverFileContent(t, spec.candidate, wantCandidate)
		if _, err := os.Lstat(spec.rollback); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback artifact %s remains: %v", spec.role, err)
		}
	}
}

func TestPrepareExplicitRollbackRejectsChangedGenerationAndOccupiedCandidate(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, specs []cutoverSwapSpec)
	}{
		{name: "changed config target", mutate: func(t *testing.T, specs []cutoverSwapSpec) {
			if err := os.WriteFile(specs[3].target, []byte("changed-config"), specs[3].mode.Perm()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed runtime target", mutate: func(t *testing.T, specs []cutoverSwapSpec) {
			if err := os.WriteFile(specs[4].target, []byte("changed-runtime"), specs[4].mode.Perm()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "occupied candidate", mutate: func(t *testing.T, specs []cutoverSwapSpec) {
			if err := os.WriteFile(specs[0].candidate, []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := canonicalThreadConfigTestDir(t)
			specs := cutoverTestSpecs(t, directory)
			buildHash := strings.Repeat("f", 64)
			if err := prepareCutoverSwaps(context.Background(), specs, buildHash); err != nil {
				t.Fatal(err)
			}
			if _, err := applyCutoverSwaps(specs); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, specs)
			if err := prepareExplicitRollback(context.Background(), specs, buildHash); err == nil {
				t.Fatal("unsafe explicit rollback was accepted")
			}
		})
	}
}

func TestRunRollbackCutoverWritesCanonicalPathFreeReceipt(t *testing.T) {
	receipt := validRollbackCutoverReceipt(t)
	var got rollbackCutoverOptions
	var stdout bytes.Buffer
	args := []string{
		"--build-dir", "/private/build", "--stage-dir", "/private/stage", "--l1-target", "/private/l1",
		"--archive-target", "/private/archive", "--topic-target", "/private/topic", "--config-target", "/private/config",
		"--runtime-candidate", "/private/runtime-new", "--runtime-target", "/private/runtime-old",
	}
	code := runRollbackCutover(args, &stdout, func(_ context.Context, options rollbackCutoverOptions) (rollbackCutoverReceipt, error) {
		got = options
		return receipt, nil
	})
	if code != 0 || got.L1Target != "/private/l1" {
		t.Fatalf("runRollbackCutover() = %d, %+v", code, got)
	}
	encoded, _ := json.Marshal(receipt)
	if stdout.String() != string(append(encoded, '\n')) || strings.Contains(stdout.String(), "/private") {
		t.Fatalf("rollback stdout = %q", stdout.String())
	}
}

func TestRunRollbackCutoverInvalidArgumentsFailClosed(t *testing.T) {
	var stdout bytes.Buffer
	if code := runRollbackCutover([]string{"--build-dir", "only"}, &stdout, nil); code == 0 {
		t.Fatal("invalid rollback arguments succeeded")
	}
	var receipt rollbackCutoverReceipt
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &receipt); err != nil || receipt.validate() != nil || receipt.Status != rollbackCutoverStatusBlocked {
		t.Fatalf("blocked receipt = %+v, decode=%v validate=%v", receipt, err, receipt.validate())
	}
}

func TestWriteRollbackCutoverReceiptFileIsFreshAndPrivate(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	receipt := validRollbackCutoverReceipt(t)
	if err := writeRollbackCutoverReceiptFile(directory, receipt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, rollbackCutoverReceiptFilename)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt metadata = %v, %v", info, err)
	}
	if err := writeRollbackCutoverReceiptFile(directory, receipt); err == nil {
		t.Fatal("existing rollback receipt was overwritten")
	}
}

func validRollbackCutoverReceipt(t *testing.T) rollbackCutoverReceipt {
	t.Helper()
	receipt := rollbackCutoverReceipt{
		SchemaVersion: rollbackCutoverSchemaVersion, CutoverReceiptSHA256: strings.Repeat("1", 64),
		BuildReceiptSHA256: strings.Repeat("2", 64), StageReceiptSHA256: strings.Repeat("3", 64),
		MappingSHA256: strings.Repeat("4", 64), OldGenerationRestored: true,
	}
	receipt = sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusApplied, "")
	if err := receipt.validate(); err != nil {
		t.Fatal(err)
	}
	return receipt
}
