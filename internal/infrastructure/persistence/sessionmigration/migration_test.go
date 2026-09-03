package sessionmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunDryRunAndApplyMigratesIdentityWithoutChangingHistory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	output := filepath.Join(root, "output")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"id":"viewer","channel":"viewer","chat_id":"viewer-user","history":[{"job_id":"job-1","user_message":"hello","channel":"viewer","chat_id":"viewer-user"}],"memory":{"key":"value"},"created_at":"2026-08-08T09:10:00Z","updated_at":"2026-08-08T09:11:00Z"}`)
	nonSession := []byte(`{"topics":["one","two"]}`)
	// The legacy filename sorts before the non-Session file, while the canonical
	// filename sorts after it. The planned hash must use materialized filename order.
	if err := os.WriteFile(filepath.Join(source, "aaa.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "forecast_topic_stock.json"), nonSession, 0600); err != nil {
		t.Fatal(err)
	}
	dryPath := filepath.Join(root, "dry.json")
	dry, err := Run(context.Background(), Options{Mode: "dry-run", SourceDir: source, ReceiptPath: dryPath})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Status != "ready" || dry.LegacySessions != 1 || dry.NonSessionFiles != 1 || dry.OutputSHA256 == "" || dry.OutputCanonicalSessions != 1 || dry.LegacySessionsRemaining != 0 {
		t.Fatalf("dry receipt = %+v", dry)
	}

	applyPath := filepath.Join(root, "apply.json")
	applied, err := Run(context.Background(), Options{Mode: "apply", SourceDir: source, OutputDir: output, ReceiptPath: applyPath, DryRunReceipt: dryPath})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Status != "ready" || applied.SourceSHA256 != dry.SourceSHA256 || applied.MappingSHA256 != dry.MappingSHA256 || applied.OutputSHA256 == "" {
		t.Fatalf("apply receipt = %+v", applied)
	}
	wantID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "session_files", "id", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(output, wantID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["channel"]; ok {
		t.Fatal("legacy channel remains")
	}
	if _, ok := fields["chat_id"]; ok {
		t.Fatal("legacy chat_id remains")
	}
	var gotID, logicalDate string
	_ = json.Unmarshal(fields["id"], &gotID)
	_ = json.Unmarshal(fields["logical_date"], &logicalDate)
	var compactHistory bytes.Buffer
	if err := json.Compact(&compactHistory, fields["history"]); err != nil {
		t.Fatal(err)
	}
	if gotID != wantID || logicalDate != "2026-08-08" || compactHistory.String() != `[{"job_id":"job-1","user_message":"hello","channel":"viewer","chat_id":"viewer-user"}]` {
		t.Fatalf("migrated fields id=%q date=%q history=%s", gotID, logicalDate, fields["history"])
	}
	repository := sessionpersistence.NewJSONSessionRepository(output)
	loaded, err := repository.Load(context.Background(), wantID)
	if err != nil {
		t.Fatalf("load migrated Session through owner repository: %v", err)
	}
	if loaded.ID() != wantID || loaded.LogicalDate() != "2026-08-08" || loaded.ChannelAddress().Channel != "viewer" || loaded.ChannelAddress().Address != "viewer-user" || loaded.HistoryCount() != 1 {
		t.Fatalf("reconstructed Session id=%q date=%q address=%#v history=%d", loaded.ID(), loaded.LogicalDate(), loaded.ChannelAddress(), loaded.HistoryCount())
	}
	copied, err := os.ReadFile(filepath.Join(output, "forecast_topic_stock.json"))
	if err != nil || string(copied) != string(nonSession) {
		t.Fatalf("non-session changed: %v", err)
	}
}

func TestRunApplyRejectsSourceDriftAndNonFreshOutput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	output := filepath.Join(root, "output")
	_ = os.Mkdir(source, 0700)
	_ = os.Mkdir(output, 0700)
	legacy := `{"id":"one","channel":"line","chat_id":"U1","history":[],"memory":{},"created_at":"2026-09-03T00:00:00Z","updated_at":"2026-09-03T00:00:00Z"}`
	_ = os.WriteFile(filepath.Join(source, "one.json"), []byte(legacy), 0600)
	dryPath := filepath.Join(root, "dry.json")
	if _, err := Run(context.Background(), Options{Mode: "dry-run", SourceDir: source, ReceiptPath: dryPath}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(source, "extra.json"), []byte(`{"x":1}`), 0600)
	if _, err := Run(context.Background(), Options{Mode: "apply", SourceDir: source, OutputDir: output, ReceiptPath: filepath.Join(root, "apply.json"), DryRunReceipt: dryPath}); err == nil {
		t.Fatal("source drift was accepted")
	}
	_ = os.WriteFile(filepath.Join(output, "occupied"), []byte("x"), 0600)
	if _, err := Run(context.Background(), Options{Mode: "apply", SourceDir: source, OutputDir: output, ReceiptPath: filepath.Join(root, "apply2.json"), DryRunReceipt: dryPath}); err == nil {
		t.Fatal("non-fresh output was accepted")
	}
}

func TestRunRejectsReceiptsInsideMigrationDirectories(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	output := filepath.Join(root, "output")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"one","channel":"line","chat_id":"U1","history":[],"memory":{},"created_at":"2026-09-03T00:00:00Z","updated_at":"2026-09-03T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(source, "one.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	insideSource := filepath.Join(source, "dry.json")
	if _, err := Run(context.Background(), Options{Mode: "dry-run", SourceDir: source, ReceiptPath: insideSource}); err == nil {
		t.Fatal("receipt inside source was accepted")
	}
	if _, err := os.Stat(insideSource); !os.IsNotExist(err) {
		t.Fatalf("rejected receipt mutated source: %v", err)
	}

	dryPath := filepath.Join(root, "dry.json")
	if _, err := Run(context.Background(), Options{Mode: "dry-run", SourceDir: source, ReceiptPath: dryPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{Mode: "dry-run", SourceDir: source, ReceiptPath: dryPath}); err == nil {
		t.Fatal("existing receipt target was overwritten")
	}
	if _, err := Run(context.Background(), Options{
		Mode: "apply", SourceDir: source, OutputDir: output,
		ReceiptPath: filepath.Join(output, "apply.json"), DryRunReceipt: dryPath,
	}); err == nil {
		t.Fatal("receipt inside output was accepted")
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected receipt mutated output: %v", entries)
	}
}

func TestRunRejectsPartialSessionAndNonStrictDryRunReceipt(t *testing.T) {
	root := t.TempDir()
	partialSource := filepath.Join(root, "partial")
	if err := os.Mkdir(partialSource, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partialSource, "broken.json"), []byte(`{"id":"legacy","history":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{Mode: "dry-run", SourceDir: partialSource, ReceiptPath: filepath.Join(root, "partial.json")}); err == nil {
		t.Fatal("partial Session was classified as a non-Session file")
	}

	source := filepath.Join(root, "source")
	output := filepath.Join(root, "output")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"one","channel":"line","chat_id":"U1","history":[],"memory":{},"created_at":"2026-09-03T00:00:00Z","updated_at":"2026-09-03T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(source, "one.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	dryPath := filepath.Join(root, "dry.json")
	if _, err := Run(context.Background(), Options{Mode: "dry-run", SourceDir: source, ReceiptPath: dryPath}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dryPath)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["unexpected"] = true
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{
		Mode: "apply", SourceDir: source, OutputDir: output,
		ReceiptPath: filepath.Join(root, "apply.json"), DryRunReceipt: dryPath,
	}); err == nil {
		t.Fatal("dry-run receipt with unknown field was accepted")
	}
}
