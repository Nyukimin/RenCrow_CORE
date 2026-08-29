package eventmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	identitymigration "github.com/Nyukimin/RenCrow_CORE/internal/application/identitymigration"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

func TestRunJSONLSnapshotDryRunApplyAndSecondRunNoop(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(snapshot, "trace-events.jsonl")
	writeTestFile(t, source, strings.Join([]string{
		`{"event_id":"child","run_id":"run-1","created_at":"2026-08-29T01:00:01Z","payload":{"parent_event_id":"root","event_type":"child","status":"done"}}`,
		`{"event_id":"root","run_id":"run-1","created_at":"2026-08-29T01:00:00Z","payload":{"event_type":"root","status":"started"}}`,
		"",
	}, "\n"))

	eventStorePath := filepath.Join(root, "target", "event-store.sqlite")
	dryManifestPath := filepath.Join(root, "dry-run.json")
	dryManifest, err := Run(context.Background(), Options{
		SnapshotDir:     snapshot,
		SuperagentJSONL: source,
		EventStore:      eventStorePath,
		Manifest:        dryManifestPath,
		Mode:            ModeDryRun,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dryManifest.Status != StatusReady || dryManifest.Mode != ModeDryRun {
		t.Fatalf("dry-run manifest = %#v", dryManifest)
	}
	if dryManifest.InputCount != 2 || dryManifest.ConvertedCount != 2 || dryManifest.DroppedRunAsParentCount != 0 {
		t.Fatalf("dry-run counts = %#v", dryManifest)
	}
	if _, err := os.Stat(eventStorePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run target stat error = %v, want not exist", err)
	}
	if got := readManifestFile(t, dryManifestPath); got.CanonicalEventSetSHA256 != dryManifest.CanonicalEventSetSHA256 {
		t.Fatalf("manifest file hash = %q, receipt hash = %q", got.CanonicalEventSetSHA256, dryManifest.CanonicalEventSetSHA256)
	}
	if _, ok := readManifestJSON(t, dryManifestPath)["events"]; ok {
		t.Fatal("manifest must not expose converted event data")
	}

	applyManifestPath := filepath.Join(root, "apply.json")
	applyManifest, err := Run(context.Background(), Options{
		SnapshotDir:     snapshot,
		SuperagentJSONL: source,
		EventStore:      eventStorePath,
		Manifest:        applyManifestPath,
		DryRunManifest:  dryManifestPath,
		Mode:            ModeApply,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applyManifest.Status != StatusApplied || applyManifest.Mode != ModeApply {
		t.Fatalf("apply manifest = %#v", applyManifest)
	}
	store, err := eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("open applied event store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	converted, err := loadAndConvert(context.Background(), SourceOptions{SuperagentJSONL: source})
	if err != nil {
		t.Fatalf("load converted events: %v", err)
	}
	for _, event := range converted.events {
		got, found, getErr := store.GetByID(context.Background(), event.EventID)
		if getErr != nil || !found {
			t.Fatalf("event %s found=%t err=%v", event.EventID, found, getErr)
		}
		wantJSON, _ := json.Marshal(event)
		gotJSON, _ := json.Marshal(got)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("event %s JSON = %s, want %s", event.EventID, gotJSON, wantJSON)
		}
	}

	noopManifestPath := filepath.Join(root, "noop.json")
	noopManifest, err := Run(context.Background(), Options{
		SnapshotDir:     snapshot,
		SuperagentJSONL: source,
		EventStore:      eventStorePath,
		Manifest:        noopManifestPath,
		DryRunManifest:  dryManifestPath,
		Mode:            ModeApply,
	})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if noopManifest.Status != StatusNoop {
		t.Fatalf("second apply status = %q, want %q", noopManifest.Status, StatusNoop)
	}
}

func TestRunAISQLiteChildBeforeParentAndDryRunDoesNotCreateTarget(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(snapshot, "ai-workflow.sqlite")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ai_workflow_event (
		event_id TEXT PRIMARY KEY,
		parent_event_id TEXT,
		run_id TEXT,
		workstream_id TEXT,
		event_type TEXT,
		created_at TEXT,
		payload TEXT NOT NULL
	)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	insert := `INSERT INTO ai_workflow_event(event_id,parent_event_id,run_id,workstream_id,event_type,created_at,payload) VALUES(?,?,?,?,?,?,?)`
	for _, row := range [][]any{
		{"child", "root", "run-ai", "ws-ai", "completed", "2026-08-29T02:00:01Z", `{"status":"done"}`},
		{"root", "", "run-ai", "ws-ai", "started", "2026-08-29T02:00:00Z", `{"status":"running"}`},
	} {
		if _, err := db.Exec(insert, row...); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(root, "missing", "event-store.sqlite")
	manifestPath := filepath.Join(root, "ai-dry-run.json")
	manifest, err := Run(context.Background(), Options{
		SnapshotDir: snapshot,
		AISQLite:    source,
		EventStore:  target,
		Manifest:    manifestPath,
		Mode:        ModeDryRun,
	})
	if err != nil {
		t.Fatalf("AI SQLite dry-run: %v", err)
	}
	if manifest.InputCount != 2 || manifest.ConvertedCount != 2 {
		t.Fatalf("AI SQLite counts = %#v", manifest)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AI SQLite dry-run target stat error = %v, want not exist", err)
	}
}

func TestRunRejectsNonEmptySQLiteSnapshotSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-journal"} {
		suffix := suffix
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			snapshot := filepath.Join(root, "snapshot")
			if err := os.Mkdir(snapshot, 0o700); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(snapshot, "source.sqlite")
			db, err := sql.Open("sqlite", original)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE ai_workflow_event (
				event_id TEXT PRIMARY KEY,
				parent_event_id TEXT,
				run_id TEXT,
				workstream_id TEXT,
				event_type TEXT,
				created_at TEXT,
				payload TEXT NOT NULL
			)`); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO ai_workflow_event(event_id,created_at,payload) VALUES(?,?,?)`, "root", "2026-08-29T03:00:00Z", `{}`); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			source := filepath.Join(snapshot, "source snapshot.sqlite")
			if err := os.Rename(original, source); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, source+suffix, "pending journal data")
			target := filepath.Join(root, "target", "event-store.sqlite")
			_, err = Run(context.Background(), Options{
				SnapshotDir: snapshot,
				AISQLite:    source,
				EventStore:  target,
				Manifest:    filepath.Join(root, "blocked.json"),
				Mode:        ModeDryRun,
			})
			if err == nil || !strings.Contains(err.Error(), "sidecar") {
				t.Fatalf("non-empty %s error = %v, want sidecar rejection", suffix, err)
			}
			if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("sidecar rejection target stat error = %v, want not exist", statErr)
			}
		})
	}
}

func TestSQLiteReadOnlyDSNEscapesPortablePaths(t *testing.T) {
	paths := []string{
		filepath.Join(t.TempDir(), "snapshot with spaces", "events.sqlite"),
		filepath.Join(t.TempDir(), "snapshot", "events?part.sqlite"),
		`C:/snapshot with spaces/events?part.sqlite`,
	}
	for _, path := range paths {
		dsn := sqliteReadOnlyDSN(path)
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse DSN for %q: %v", path, err)
		}
		if parsed.Scheme != "file" {
			t.Fatalf("DSN scheme for %q = %q, want file", path, parsed.Scheme)
		}
		if parsed.Query().Get("mode") != "ro" {
			t.Fatalf("DSN mode for %q = %q, want ro", path, parsed.Query().Get("mode"))
		}
		if strings.Contains(dsn, " ") || strings.Contains(dsn, "?part") {
			t.Fatalf("DSN contains unescaped path characters for %q: %s", path, dsn)
		}
	}
}

func TestRunRejectsChangedInputAndPartialTarget(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(snapshot, "trace-events.jsonl")
	writeTestFile(t, source, strings.Join([]string{
		`{"event_id":"child","run_id":"run-1","created_at":"2026-08-29T01:00:01Z","payload":{"parent_event_id":"root","event_type":"child"}}`,
		`{"event_id":"root","run_id":"run-1","created_at":"2026-08-29T01:00:00Z","payload":{"event_type":"root"}}`,
		"",
	}, "\n"))
	dryManifestPath := filepath.Join(root, "dry.json")
	if _, err := Run(context.Background(), Options{
		SnapshotDir:     snapshot,
		SuperagentJSONL: source,
		EventStore:      filepath.Join(root, "changed-target.sqlite"),
		Manifest:        dryManifestPath,
		Mode:            ModeDryRun,
	}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if err := os.WriteFile(source, []byte(strings.Join([]string{
		`{"event_id":"child","run_id":"run-1","created_at":"2026-08-29T01:00:01Z","payload":{"parent_event_id":"root","event_type":"child","changed":true}}`,
		`{"event_id":"root","run_id":"run-1","created_at":"2026-08-29T01:00:00Z","payload":{"event_type":"root"}}`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	changedTarget := filepath.Join(root, "changed-target.sqlite")
	if _, err := Run(context.Background(), Options{
		SnapshotDir:     snapshot,
		SuperagentJSONL: source,
		EventStore:      changedTarget,
		Manifest:        filepath.Join(root, "changed-apply.json"),
		DryRunManifest:  dryManifestPath,
		Mode:            ModeApply,
	}); err == nil {
		t.Fatal("changed input apply error = nil")
	}
	if _, err := os.Stat(changedTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed input target stat error = %v, want not exist", err)
	}

	// Restore the exact source bytes used by the dry-run and seed only its root.
	writeTestFile(t, source, strings.Join([]string{
		`{"event_id":"child","run_id":"run-1","created_at":"2026-08-29T01:00:01Z","payload":{"parent_event_id":"root","event_type":"child"}}`,
		`{"event_id":"root","run_id":"run-1","created_at":"2026-08-29T01:00:00Z","payload":{"event_type":"root"}}`,
		"",
	}, "\n"))
	converted, err := loadAndConvert(context.Background(), SourceOptions{SuperagentJSONL: source})
	if err != nil {
		t.Fatalf("load restored source: %v", err)
	}
	partialTarget := filepath.Join(root, "partial-target.sqlite")
	store, err := eventstore.NewSQLiteStore(partialTarget)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), converted.events[1]); err != nil {
		store.Close()
		t.Fatalf("seed root: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{
		SnapshotDir:     snapshot,
		SuperagentJSONL: source,
		EventStore:      partialTarget,
		Manifest:        filepath.Join(root, "partial-apply.json"),
		DryRunManifest:  dryManifestPath,
		Mode:            ModeApply,
	}); err == nil {
		t.Fatal("partial target apply error = nil")
	}
	store, err = eventstore.NewSQLiteStore(partialTarget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, found, err := store.GetByID(context.Background(), converted.events[0].EventID); err != nil || found {
		t.Fatalf("partial target child found=%t err=%v", found, err)
	}
}

func TestOptionsRejectSourceOutsideSnapshotAndConflictingFormats(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.jsonl")
	writeTestFile(t, outside, "{}\n")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SuperagentJSONL: outside,
		EventStore: filepath.Join(root, "store.sqlite"), Manifest: filepath.Join(root, "manifest.json"), Mode: ModeDryRun,
	}); err == nil {
		t.Fatal("outside source error = nil")
	}
	inside := filepath.Join(snapshot, "source.jsonl")
	writeTestFile(t, inside, "{}\n")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, AISQLite: inside, AIJSONL: inside,
		EventStore: filepath.Join(root, "store.sqlite"), Manifest: filepath.Join(root, "manifest.json"), Mode: ModeDryRun,
	}); err == nil {
		t.Fatal("conflicting AI formats error = nil")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readManifestFile(t *testing.T, path string) Manifest {
	t.Helper()
	var value Manifest
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func readManifestJSON(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCanonicalEventSetHashIsStableAcrossInputOrder(t *testing.T) {
	at := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	legacy := []identitymigration.LegacyEvent{
		{SourceTable: "trace_event", EventID: "child", ParentEventID: "root", RunID: "run-1", EventType: "child", OccurredAt: at.Add(time.Second)},
		{SourceTable: "trace_event", EventID: "root", RunID: "run-1", EventType: "root", OccurredAt: at},
	}
	converted, err := identitymigration.ConvertLegacyEvents("superagent", legacy)
	if err != nil {
		t.Fatal(err)
	}
	reversed := append([]modulecore.EventEnvelope(nil), converted.Events...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	first, err := canonicalEventSetSHA256(converted.Events)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalEventSetSHA256(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("canonical event set hash changed with input order: %q != %q", first, second)
	}
}

func TestDryRunManifestRecordsDroppedRunAsParentReason(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(snapshot, "trace-events.jsonl")
	writeTestFile(t, source, `{"event_id":"legacy-event","run_id":"legacy-run","parent_event_id":"legacy-run","event_type":"started","created_at":"2026-08-29T00:00:00Z","payload":{}}`+"\n")
	manifest, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SuperagentJSONL: source,
		EventStore: filepath.Join(root, "event-store.sqlite"),
		Manifest:   filepath.Join(root, "manifest.json"), Mode: ModeDryRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.DroppedRunAsParentCount != 1 || manifest.DroppedRunAsParentReason != "legacy_parent_event_id_referenced_run_id" {
		t.Fatalf("dropped parent receipt = %#v", manifest)
	}
}
