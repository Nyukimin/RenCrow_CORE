package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	migration "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/runmigration"
)

func TestCommandMapsExactCutoverOwnerInputs(t *testing.T) {
	root := t.TempDir()
	write := func(name string, value any) string {
		t.Helper()
		path := filepath.Join(root, name)
		data, err := json.Marshal(value)
		if err != nil || os.WriteFile(path, data, 0o600) != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	inventory := write("inventory.json", migration.Inventory{SchemaVersion: migration.Schema})
	plan := write("plan.json", migration.Receipt{SchemaVersion: migration.Schema, Status: "quarantined"})
	active := write("active.json", migration.RunCutoverActiveManifest{SchemaVersion: migration.RunCutoverSchema})
	old := cutoverOperation
	defer func() { cutoverOperation = old }()
	var got migration.CutoverOptions
	cutoverOperation = func(_ context.Context, options migration.CutoverOptions) (migration.RunCutoverReceipt, error) {
		got = options
		return migration.RunCutoverReceipt{SchemaVersion: migration.RunCutoverSchema, Status: migration.RunCutoverApplied}, nil
	}
	args := []string{
		"--mode", "cutover", "--snapshot", "snapshot", "--cohort", "cohort", "--inventory", inventory,
		"--quarantine-receipt", plan, "--expected-quarantine-receipt-sha256", strings.Repeat("a", 64),
		"--active-manifest", active, "--expected-active-manifest-sha256", strings.Repeat("b", 64),
		"--rollback-dir", "rollback", "--cutover-receipt", "cutover.json", "--installed-runtime", "rencrow",
		"--expected-runtime-sha256", strings.Repeat("c", 64), "--active-config", "core.yaml",
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("cutover exit=%d stderr=%s", code, stderr.String())
	}
	if got.Cohort != "cohort" || got.Snapshot != "snapshot" || got.RollbackDir != "rollback" || got.CutoverReceipt != "cutover.json" ||
		got.ExpectedPlanReceiptSHA256 != strings.Repeat("a", 64) || got.ExpectedActiveManifestSHA256 != strings.Repeat("b", 64) ||
		got.ExpectedRuntimeSHA256 != strings.Repeat("c", 64) {
		t.Fatalf("cutover mapping = %#v", got)
	}
}

func TestCommandRejectsAmbiguousInventory(t *testing.T) {
	for _, raw := range []string{`{"schema_version":"one","schema_version":"two"}`, `{"unknown":true}`, `{} {}`} {
		p := filepath.Join(t.TempDir(), "inventory.json")
		if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"--inventory", p}, &stdout, &stderr); code != 2 {
			t.Fatalf("ambiguous inventory exit=%d", code)
		}
		if stdout.Len() != 0 {
			t.Fatal("invalid inventory emitted success receipt")
		}
	}
}

func TestCommandOfflineDryRunApplyAndNoop(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "tasks.jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(nil)
	inv := migration.Inventory{SchemaVersion: migration.Schema, SnapshotAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), Files: map[string]string{"tasks.jsonl": hex.EncodeToString(hash[:])}, Roles: map[string]string{}}
	for _, role := range []string{"tasks", "runs", "contexts", "notifications", "events", "superagent", "browser", "knowledge", "word", "forecast", "story", "dialogue", "checkpoints"} {
		inv.Roles[role] = ""
	}
	inv.Roles["tasks"] = "tasks.jsonl"
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	ip := filepath.Join(root, "inventory.json")
	if err = os.WriteFile(ip, b, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--inventory", ip, "--snapshot", snapshot, "--output", filepath.Join(root, "output")}
	var out, stderr bytes.Buffer
	if code := run(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatalf("dry-run exit=%d: %s", code, stderr.String())
	}
	var receipt migration.Receipt
	if err = json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.Status != "ready" {
		t.Fatalf("dry-run receipt: %s %v", out.String(), err)
	}
	rp := filepath.Join(root, "ready.json")
	if err = os.WriteFile(rp, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "--mode", "apply", "--dry-run-receipt", rp)
	for _, want := range []string{"applied", "noop"} {
		out.Reset()
		stderr.Reset()
		if code := run(context.Background(), args, &out, &stderr); code != 0 {
			t.Fatalf("apply exit=%d: %s", code, stderr.String())
		}
		if err = json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.Status != want {
			t.Fatalf("receipt: %s %v", out.String(), err)
		}
	}
}

func TestCommandHelpAndUnknownFlag(t *testing.T) {
	var out, err bytes.Buffer
	if code := run(context.Background(), []string{"--unknown"}, &out, &err); code != 2 {
		t.Fatalf("unknown flag exit=%d", code)
	}
}

func TestCommandRejectsReceiptForWrongMode(t *testing.T) {
	root := t.TempDir()
	receipt := filepath.Join(root, "receipt.json")
	if err := os.WriteFile(receipt, []byte(`{"schema_version":"rencrow.identity.run-migration/v1","status":"quarantined"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--mode", "dry-run", "--quarantine-receipt", receipt}, &out, &stderr); code != 2 {
		t.Fatalf("wrong-mode receipt exit=%d stderr=%s", code, stderr.String())
	}
}

func TestCommandRejectsCutoverOwnerFlagOutsideCutover(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--mode", "dry-run", "--rollback-dir", "private"}, &out, &stderr); code != 2 {
		t.Fatalf("wrong-mode cutover flag exit=%d stderr=%s", code, stderr.String())
	}
}
