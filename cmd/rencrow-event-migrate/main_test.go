package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunEmitsJSONReceiptAndValidatesFlagContract(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(snapshot, "events.jsonl")
	if err := os.WriteFile(source, []byte(`{"event_id":"root","event_type":"started","created_at":"2026-08-29T00:00:00Z","payload":{"safe":true}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "missing", "event-store.sqlite")
	manifestPath := filepath.Join(root, "receipt.json")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"--snapshot-dir", snapshot,
		"--superagent-jsonl", source,
		"--event-store", storePath,
		"--manifest", manifestPath,
		"--mode", "dry-run",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run dry-run: %v stderr=%s", err, stderr.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("stdout is not JSON: %v (%s)", err, stdout.String())
	}
	if receipt["mode"] != "dry-run" || receipt["status"] != "ready" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run target stat error = %v, want not exist", err)
	}
}

func TestRunRejectsConflictingSourceFlagsAndMissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--mode=dry-run"}, &stdout, &stderr); err == nil {
		t.Fatal("missing required flags error = nil")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "snapshot"), 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "snapshot", "source.jsonl")
	if err := os.WriteFile(inside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"--snapshot-dir", filepath.Join(root, "snapshot"),
		"--ai-jsonl", inside, "--ai-sqlite", inside,
		"--event-store", filepath.Join(root, "store.sqlite"),
		"--manifest", filepath.Join(root, "manifest.json"), "--mode", "dry-run",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "exactly zero or one") {
		t.Fatalf("conflicting source error = %v, want contract error", err)
	}
}
