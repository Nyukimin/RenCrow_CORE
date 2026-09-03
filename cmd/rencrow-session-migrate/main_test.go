package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsIncompleteContractWithBoundedOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"--mode", "dry-run", "--source-dir", filepath.Join(t.TempDir(), "missing")}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run error = nil")
	}
	if strings.Contains(stdout.String(), filepath.Dir(filepath.Dir(t.TempDir()))) {
		t.Fatal("stdout exposed an arbitrary path")
	}
	if !strings.Contains(stdout.String(), `"status":"blocked"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
