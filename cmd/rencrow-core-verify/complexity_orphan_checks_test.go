package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	complexitypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/complexity"
	_ "modernc.org/sqlite"
)

func readComplexityOrphanEvidence(t *testing.T, evidenceDir string, receipt verifierReceipt) map[string]any {
	t.Helper()
	if len(receipt.EvidenceRefs) != 1 || !strings.HasPrefix(receipt.EvidenceRefs[0], "relative:") {
		t.Fatalf("evidence refs = %#v", receipt.EvidenceRefs)
	}
	raw, err := os.ReadFile(filepath.Join(evidenceDir, strings.TrimPrefix(receipt.EvidenceRefs[0], "relative:")))
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode evidence: %v raw=%s", err, raw)
	}
	return value
}

func writeComplexityOrphanFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	store, err := complexitypersistence.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(%q) error = %v", path, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close fixture store: %v", err)
	}
	return path
}

func TestVerifierComplexityOrphanManifestUsesOnlyFixedOwnerInput(t *testing.T) {
	manifest, err := loadOwnerManifest(filepath.Join("..", "..", "config", "checks", "core.json"))
	if err != nil {
		t.Fatalf("load current CORE manifest: %v", err)
	}
	var check manifestCheck
	for _, candidate := range manifest.Checks {
		if candidate.CheckID == "core_complexity_identity_orphan" {
			check = candidate
		}
	}
	if check.CheckID == "" {
		t.Fatalf("current CORE manifest does not declare the complexity orphan check")
	}
	if check.Executor.Kind != "owner_cli" || check.Executor.CommandID != complexityOrphanCommandID {
		t.Fatalf("complexity orphan executor = %+v", check.Executor)
	}
	if check.Owner != verifierOwner || check.ReceiptSchema != verifierReceiptSchema {
		t.Fatalf("complexity orphan contract = %q %q", check.Owner, check.ReceiptSchema)
	}
	acquisition := check.Executor.Acquisition
	if acquisition == nil || acquisition.Mode != "owner_self_collect" {
		t.Fatalf("complexity orphan acquisition = %+v", acquisition)
	}
	if acquisition.VerificationSafe == nil || *acquisition.VerificationSafe {
		t.Fatalf("complexity orphan read-only observation must declare verification_safe=false")
	}
	if len(acquisition.Inputs) != 1 {
		t.Fatalf("complexity orphan inputs = %+v", acquisition.Inputs)
	}
	input := acquisition.Inputs[0]
	if input.ID != "complexity_hotspot_db" || input.Class != "external_prerequisite" ||
		input.Source != "owner_external_artifact" || input.Required == nil || !*input.Required {
		t.Fatalf("complexity orphan input = %+v", input)
	}
}

func TestVerifierComplexityOrphanCleanDatabasePassesWithBoundedEvidence(t *testing.T) {
	dbPath := writeComplexityOrphanFixture(t, "complexity_hotspot.db")
	manifest := testManifest(t, complexityOrphanCommandID)
	evidenceDir := t.TempDir()
	out, errOut := &strings.Builder{}, &strings.Builder{}
	code := runVerifierCLI(context.Background(), append(
		verifierArgs(manifest, "core_complexity_identity_orphan", evidenceDir),
		"--complexity-db", dbPath,
	), out, errOut, defaultVerifierDependencies())
	if code != verifierExitPassed {
		t.Fatalf("exit = %d want %d, stderr=%s", code, verifierExitPassed, errOut)
	}
	receipt := decodeVerifierReceipt(t, []byte(out.String()))
	if receipt.Status != "passed" || receipt.CheckID != "core_complexity_identity_orphan" {
		t.Fatalf("receipt = %+v", receipt)
	}
	raw := readComplexityOrphanEvidence(t, evidenceDir, receipt)
	for _, token := range []string{
		"hotspots_missing_scan", "hotspots_invalid_index", "evidence_missing_hotspot",
		"evidence_invalid_index", "artifacts_missing_scan", "artifacts_invalid_index",
		"identity_orphans", "read_only_observation",
	} {
		if _, ok := raw[token]; !ok {
			t.Fatalf("evidence is missing %s: %#v", token, raw)
		}
	}
	if raw["identity_orphans"] != float64(0) {
		t.Fatalf("identity_orphans = %#v want 0", raw["identity_orphans"])
	}
	if raw["db_name"] != "complexity_hotspot.db" {
		t.Fatalf("evidence db_name = %#v", raw["db_name"])
	}
	blob := out.String() + errOut.String()
	if strings.Contains(blob, dbPath) {
		t.Fatalf("receipt leaked the raw database path")
	}
}

func TestVerifierComplexityOrphanOrphanRowFails(t *testing.T) {
	dbPath := writeComplexityOrphanFixture(t, "complexity_hotspot.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open fixture writable: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `INSERT INTO complexity_hotspot_evidence
		(evidence_id, hotspot_id, created_at, payload) VALUES (?, ?, ?, ?)`,
		"evd_verifier_orphan", "hotspot_absent", "2026-09-17T00:00:00Z",
		`{"evidence_id":"evd_verifier_orphan","hotspot_id":"hotspot_absent","file_path":"src/app.go","created_at":"2026-09-17T00:00:00Z"}`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert orphan row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture writable: %v", err)
	}
	manifest := testManifest(t, complexityOrphanCommandID)
	evidenceDir := t.TempDir()
	out, errOut := &strings.Builder{}, &strings.Builder{}
	code := runVerifierCLI(context.Background(), append(
		verifierArgs(manifest, "core_complexity_identity_orphan", evidenceDir),
		"--complexity-db", dbPath,
	), out, errOut, defaultVerifierDependencies())
	if code != verifierExitFailed {
		t.Fatalf("exit = %d want %d, stderr=%s", code, verifierExitFailed, errOut)
	}
	receipt := decodeVerifierReceipt(t, []byte(out.String()))
	if receipt.Status != "failed" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !strings.Contains(receipt.FailureBoundary, "orphan") {
		t.Fatalf("failure boundary = %q", receipt.FailureBoundary)
	}
}

func TestVerifierComplexityOrphanMissingInputIsBlocked(t *testing.T) {
	manifest := testManifest(t, complexityOrphanCommandID)
	evidenceDir := t.TempDir()
	out, errOut := &strings.Builder{}, &strings.Builder{}
	code := runVerifierCLI(context.Background(),
		verifierArgs(manifest, "core_complexity_identity_orphan", evidenceDir),
		out, errOut, defaultVerifierDependencies())
	if code != verifierExitBlocked {
		t.Fatalf("exit = %d want %d, stderr=%s", code, verifierExitBlocked, errOut)
	}
	receipt := decodeVerifierReceipt(t, []byte(out.String()))
	if receipt.Status != "blocked" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !strings.Contains(receipt.FailureBoundary, "complexity database path is required") {
		t.Fatalf("failure boundary = %q", receipt.FailureBoundary)
	}
	if strings.Contains(out.String()+errOut.String(), "fallback") {
		t.Fatalf("blocked path must not mention a fallback route: %s", out)
	}
}

func TestVerifierComplexityOrphanAbsentDatabaseIsBlockedWithoutDiscovery(t *testing.T) {
	manifest := testManifest(t, complexityOrphanCommandID)
	evidenceDir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "absent_complexity.db")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("fixture path unexpectedly exists: %v", err)
	}
	out, errOut := &strings.Builder{}, &strings.Builder{}
	code := runVerifierCLI(context.Background(), append(
		verifierArgs(manifest, "core_complexity_identity_orphan", evidenceDir),
		"--complexity-database", missing,
	), out, errOut, defaultVerifierDependencies())
	if code != verifierExitBlocked {
		t.Fatalf("exit = %d want %d, stderr=%s", code, verifierExitBlocked, errOut)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("blocked observation must not create the database: %v", err)
	}
	receipt := decodeVerifierReceipt(t, []byte(out.String()))
	if receipt.Status != "blocked" {
		t.Fatalf("receipt = %+v", receipt)
	}
}
