package dcimigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReadBuildCaptureReceiptReturnsReadyReceiptAndExactSHA256(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	receipt := buildInputReadyCaptureReceipt()
	data := buildInputJSON(t, receipt)
	path := filepath.Join(root, CaptureReceiptFilename)
	buildInputWriteFile(t, path, data)

	got, gotHash, err := readBuildCaptureReceipt(path)
	if err != nil {
		t.Fatalf("readBuildCaptureReceipt() error = %v", err)
	}
	if got.Status != StatusReady || got.SchemaVersion != CaptureSchemaVersion || got.Mode != ModeCapture {
		t.Fatalf("readBuildCaptureReceipt() = %#v, want ready capture receipt", got)
	}
	wantHash := buildInputSHA256(data)
	if gotHash != wantHash {
		t.Fatalf("readBuildCaptureReceipt() hash = %q, want %q", gotHash, wantHash)
	}
}

func TestReadBuildCaptureReceiptRejectsUnsafeAndOversizedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	validPath := filepath.Join(root, CaptureReceiptFilename)
	buildInputWriteFile(t, validPath, buildInputJSON(t, buildInputReadyCaptureReceipt()))

	tests := []struct {
		name string
		path string
		data []byte
	}{
		{name: "missing", path: filepath.Join(root, "missing.json")},
		{name: "directory", path: filepath.Join(root, "directory")},
		{name: "oversized", path: filepath.Join(root, "oversized.json"), data: bytes.Repeat([]byte("x"), int(maxCaptureManifestBytes)+1)},
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.data != nil {
				buildInputWriteFile(t, tt.path, tt.data)
			}
			_, _, err := readBuildCaptureReceipt(tt.path)
			if err == nil {
				t.Fatalf("readBuildCaptureReceipt(%q) unexpectedly succeeded", tt.name)
			}
			buildInputAssertBoundedError(t, err, tt.name)
		})
	}

	if runtime.GOOS == "windows" {
		return
	}
	symlinkPath := filepath.Join(root, "receipt-link.json")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := readBuildCaptureReceipt(symlinkPath); err == nil {
		t.Fatal("readBuildCaptureReceipt() unexpectedly followed symlink")
	} else {
		buildInputAssertBoundedError(t, err, "symlink")
	}
}

func TestReadBuildCaptureReceiptRejectsUnknownTrailingAndNonReadyHeaders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := buildInputReadyCaptureReceipt()
	baseData := buildInputJSON(t, base)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: buildInputAddField(t, baseData, `"unexpected_secret":"capture-sensitive-id"`)},
		{name: "trailing object", data: append(append([]byte{}, baseData...), []byte("{}")...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-")+".json")
			buildInputWriteFile(t, path, tt.data)
			_, _, err := readBuildCaptureReceipt(path)
			if err == nil {
				t.Fatalf("readBuildCaptureReceipt(%q) unexpectedly succeeded", tt.name)
			}
			buildInputAssertBoundedError(t, err, "capture-sensitive-id")
		})
	}

	for _, tt := range []struct {
		name   string
		mutate func(*CaptureReceipt)
	}{
		{name: "blocked status", mutate: func(value *CaptureReceipt) { value.Status = StatusBlocked; value.ErrorCode = "capture_failed" }},
		{name: "wrong schema", mutate: func(value *CaptureReceipt) { value.SchemaVersion = "wrong-schema" }},
		{name: "wrong mode", mutate: func(value *CaptureReceipt) { value.Mode = ModeDryRun }},
		{name: "wrong status", mutate: func(value *CaptureReceipt) { value.Status = "unexpected" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := base
			tt.mutate(&candidate)
			path := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-")+".json")
			buildInputWriteFile(t, path, buildInputJSON(t, candidate))
			_, _, err := readBuildCaptureReceipt(path)
			if err == nil {
				t.Fatalf("readBuildCaptureReceipt(%q) unexpectedly succeeded", tt.name)
			}
			buildInputAssertBoundedError(t, err, "wrong-schema")
		})
	}
}

func TestReadBuildManifestReturnsReadyManifestAndExactSHA256(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manifest := buildInputReadyManifest()
	data := buildInputJSON(t, manifest)
	path := filepath.Join(root, "dry-run.json")
	buildInputWriteFile(t, path, data)

	got, gotHash, err := readBuildManifest(path)
	if err != nil {
		t.Fatalf("readBuildManifest() error = %v", err)
	}
	if got.Status != StatusReady || got.SchemaVersion != ManifestSchemaVersion || got.Mode != ModeDryRun {
		t.Fatalf("readBuildManifest() = %#v, want ready dry-run manifest", got)
	}
	wantHash := buildInputSHA256(data)
	if gotHash != wantHash {
		t.Fatalf("readBuildManifest() hash = %q, want %q", gotHash, wantHash)
	}
}

func TestReadBuildManifestRejectsUnsafeAndOversizedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	validPath := filepath.Join(root, "dry-run.json")
	buildInputWriteFile(t, validPath, buildInputJSON(t, buildInputReadyManifest()))

	tests := []struct {
		name string
		path string
		data []byte
	}{
		{name: "missing", path: filepath.Join(root, "missing.json")},
		{name: "directory", path: filepath.Join(root, "directory")},
		{name: "oversized", path: filepath.Join(root, "oversized.json"), data: bytes.Repeat([]byte("x"), int(maxManifestBytes)+1)},
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.data != nil {
				buildInputWriteFile(t, tt.path, tt.data)
			}
			_, _, err := readBuildManifest(tt.path)
			if err == nil {
				t.Fatalf("readBuildManifest(%q) unexpectedly succeeded", tt.name)
			}
			buildInputAssertBoundedError(t, err, tt.name)
		})
	}

	if runtime.GOOS == "windows" {
		return
	}
	symlinkPath := filepath.Join(root, "manifest-link.json")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := readBuildManifest(symlinkPath); err == nil {
		t.Fatal("readBuildManifest() unexpectedly followed symlink")
	} else {
		buildInputAssertBoundedError(t, err, "symlink")
	}
}

func TestReadBuildManifestRejectsUnknownTrailingAndNonReadyHeaders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := buildInputReadyManifest()
	baseData := buildInputJSON(t, base)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: buildInputAddField(t, baseData, `"unexpected_secret":"manifest-sensitive-id"`)},
		{name: "trailing object", data: append(append([]byte{}, baseData...), []byte("{}")...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-")+".json")
			buildInputWriteFile(t, path, tt.data)
			_, _, err := readBuildManifest(path)
			if err == nil {
				t.Fatalf("readBuildManifest(%q) unexpectedly succeeded", tt.name)
			}
			buildInputAssertBoundedError(t, err, "manifest-sensitive-id")
		})
	}

	for _, tt := range []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "blocked status", mutate: func(value *Manifest) { value.Status = StatusBlocked; value.ErrorCode = "manifest_failed" }},
		{name: "wrong schema", mutate: func(value *Manifest) { value.SchemaVersion = "wrong-schema" }},
		{name: "wrong mode", mutate: func(value *Manifest) { value.Mode = ModeCapture }},
		{name: "wrong status", mutate: func(value *Manifest) { value.Status = "unexpected" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := base
			tt.mutate(&candidate)
			path := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-")+".json")
			buildInputWriteFile(t, path, buildInputJSON(t, candidate))
			_, _, err := readBuildManifest(path)
			if err == nil {
				t.Fatalf("readBuildManifest(%q) unexpectedly succeeded", tt.name)
			}
			buildInputAssertBoundedError(t, err, "wrong-schema")
		})
	}
}

func buildInputReadyCaptureReceipt() CaptureReceipt {
	zero := 0
	pageCount := 1
	artifacts := make(map[string]CaptureArtifact, len(captureArtifactSpecs))
	for _, spec := range captureArtifactSpecs {
		artifact := CaptureArtifact{Method: spec.method, FileSHA256: strings.Repeat("a", 64), Bytes: 1}
		if spec.sqlite {
			artifact.PageCount = &pageCount
			artifact.QuickCheck = "ok"
			artifact.SidecarZero = &zero
		}
		artifacts[spec.role] = artifact
	}
	receipt := CaptureReceipt{
		SchemaVersion: CaptureSchemaVersion,
		Mode:          ModeCapture,
		Status:        StatusReady,
		StartedAt:     time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		CompletedAt:   time.Date(2026, 8, 31, 0, 0, 1, 0, time.UTC),
		Artifacts:     artifacts,
	}
	receipt.ArtifactSetSHA256 = captureArtifactSetSHA256(receipt.Artifacts)
	return receipt
}

func buildInputReadyManifest() Manifest {
	hash := strings.Repeat("b", 64)
	manifest := newBaseManifest(ExpectedCounts{})
	manifest.Status = StatusReady
	manifest.SourceDatabaseLogicalSHA256 = map[string]string{
		"source_dci": hash, "source_event_store": hash, "source_l1": hash, "source_archive": hash,
	}
	manifest.SourceSchemaSHA256 = map[string]string{
		"source_dci": hash, "source_event_store": hash, "source_l1": hash, "source_archive": hash,
	}
	manifest.SourceDCIClassificationSHA256 = map[string]string{
		"source_dci": hash, "source_dci_jsonl": hash, "source_event_store": hash, "source_l1": hash, "source_archive": hash,
	}
	manifest.SourceFileSHA256 = map[string]string{"source_dci_jsonl": hash}
	manifest.SourceNonDCILogicalSHA256 = map[string]string{
		"source_event_store": hash, "source_l1": hash, "source_archive": hash,
	}
	manifest.MappingSHA256 = hash
	manifest.ActionSetSHA256 = hash
	manifest.TraceSetSHA256 = hash
	manifest.EvidenceSetSHA256 = hash
	manifest.EventSetSHA256 = hash
	manifest.EventPlanSHA256 = hash
	return manifest
}

func buildInputJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func buildInputAddField(t *testing.T, data []byte, field string) []byte {
	t.Helper()
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[len(trimmed)-1] != '}' {
		t.Fatalf("JSON fixture is not an object: %q", trimmed)
	}
	result := append([]byte{}, trimmed[:len(trimmed)-1]...)
	result = append(result, ',')
	result = append(result, []byte(field)...)
	result = append(result, '}')
	return append(result, '\n')
}

func buildInputWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func buildInputSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func buildInputAssertBoundedError(t *testing.T, err error, forbidden string) {
	t.Helper()
	if strings.Contains(err.Error(), forbidden) {
		t.Fatalf("error contains forbidden path/content/ID %q: %v", forbidden, err)
	}
}
