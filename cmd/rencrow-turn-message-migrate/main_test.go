package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/turnmigration"
)

func TestRunCLIRejectsPositionalArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"unexpected"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code=%d, want 2", code)
	}
	if stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunCLIEmitsBoundedFailureReceipt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	manifestPath := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"--db", dbPath, "--manifest", manifestPath, "--mode", turnmigration.ModeDryRun}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code=%d, want 1; stderr=%q", code, stderr.String())
	}
	var receipt turnmigration.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("stdout receipt=%q: %v", stdout.String(), err)
	}
	if receipt.Status != turnmigration.StatusBlocked || receipt.ErrorCode != "source_invalid" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestRunCLIHelpSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code=%d stderr=%q", code, stderr.String())
	}
}
