package dcimigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sqlite "modernc.org/sqlite"
)

type captureTestSources struct {
	dci        string
	dciJSONL   string
	eventStore string
	l1         string
	archive    string
	dbs        []*sql.DB
}

func TestCaptureCopiesWALVisibleStateAndWritesBoundedReceipt(t *testing.T) {
	sources := makeCaptureTestSources(t, true)
	defer closeCaptureTestSources(t, sources)
	root := filepath.Join(t.TempDir(), "captured")
	receipt, err := Capture(context.Background(), CaptureOptions{
		SnapshotDir: root, LiveDCI: sources.dci, LiveDCIJSONL: sources.dciJSONL,
		LiveEventStore: sources.eventStore, LiveL1: sources.l1, LiveArchive: sources.archive,
	})
	if err != nil {
		t.Fatalf("Capture() error = %v, receipt = %#v", err, receipt)
	}
	if receipt.SchemaVersion != CaptureSchemaVersion || receipt.Mode != ModeCapture || receipt.Status != StatusReady {
		t.Fatalf("capture receipt header = %#v", receipt)
	}
	if receipt.ErrorCode != "" || receipt.ArtifactSetSHA256 == "" || len(receipt.ArtifactSetSHA256) != 64 {
		t.Fatalf("capture receipt completion = %#v", receipt)
	}
	wantRoles := map[string]struct{}{
		"source_dci": {}, "source_dci_jsonl": {}, "source_event_store": {}, "source_l1": {}, "source_archive": {},
	}
	if len(receipt.Artifacts) != len(wantRoles) {
		t.Fatalf("capture artifacts = %#v", receipt.Artifacts)
	}
	for role := range receipt.Artifacts {
		if _, ok := wantRoles[role]; !ok {
			t.Fatalf("capture artifact role = %q, want exact role set %#v", role, wantRoles)
		}
	}
	for role, artifact := range receipt.Artifacts {
		if artifact.Method != "sqlite_backup" && role != "source_dci_jsonl" {
			t.Fatalf("SQLite artifact %s method = %q", role, artifact.Method)
		}
		if role == "source_dci_jsonl" {
			if artifact.Method != "byte_copy" || artifact.PageCount != nil || artifact.QuickCheck != "" || artifact.SidecarZero != nil {
				t.Fatalf("JSONL artifact = %#v", artifact)
			}
			continue
		}
		if artifact.PageCount == nil || *artifact.PageCount < 0 || artifact.QuickCheck != "ok" || artifact.SidecarZero == nil || *artifact.SidecarZero != 0 {
			t.Fatalf("SQLite artifact %s receipt = %#v", role, artifact)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, CaptureReceiptFilename))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > maxCaptureManifestBytes {
		t.Fatalf("capture receipt size = %d, exceeds %d", len(data), maxCaptureManifestBytes)
	}
	encoded := string(data)
	if strings.Contains(encoded, root) || strings.Contains(encoded, sources.dci) || strings.Contains(encoded, "wal-visible") {
		t.Fatalf("capture receipt leaked path or content: %s", encoded)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(root, CaptureReceiptFilename)); err != nil {
			t.Fatalf("capture receipt stat error = %v", err)
		} else if info.Mode().Perm() != 0o600 {
			t.Fatalf("capture receipt mode = %o", info.Mode().Perm())
		}
		info, err := os.Stat(root)
		if err != nil {
			t.Fatalf("capture root stat error = %v", err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("capture root mode = %v", info.Mode().Perm())
		}
	}
	for _, role := range []string{"source-dci", "source-dci-jsonl", "source-event-store", "source-l1", "source-archive"} {
		path := filepath.Join(root, role)
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("captured artifact %s missing: %v", role, err)
		} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("captured artifact %s mode = %o", role, info.Mode().Perm())
		}
		for _, suffix := range sqliteSidecarSuffixes {
			if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("captured artifact %s has sidecar %s: %v", role, suffix, err)
			}
		}
	}

	db, err := sql.Open("sqlite", filepath.Join(root, "source-dci"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got string
	if err := db.QueryRow("SELECT value FROM wal_probe WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("captured WAL row missing: %v", err)
	}
	if got != "wal-visible" {
		t.Fatalf("captured WAL row = %q, want wal-visible", got)
	}
}

func TestCaptureRejectsExistingRootMissingSourceAndAliases(t *testing.T) {
	sources := makeCaptureTestSources(t, false)
	defer closeCaptureTestSources(t, sources)
	base := CaptureOptions{
		SnapshotDir: filepath.Join(t.TempDir(), "new-root"), LiveDCI: sources.dci, LiveDCIJSONL: sources.dciJSONL,
		LiveEventStore: sources.eventStore, LiveL1: sources.l1, LiveArchive: sources.archive,
	}
	existing := base
	existing.SnapshotDir = filepath.Join(t.TempDir(), "existing-root")
	if err := os.Mkdir(existing.SnapshotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	receipt, err := Capture(context.Background(), existing)
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "unsafe_path" {
		t.Fatalf("existing root result = %#v, err=%v", receipt, err)
	}
	aliased := base
	aliased.LiveEventStore = sources.dci
	receipt, err = Capture(context.Background(), aliased)
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "unsafe_path" {
		t.Fatalf("aliased source result = %#v, err=%v", receipt, err)
	}
	missing := base
	missing.LiveArchive = filepath.Join(t.TempDir(), "missing.db")
	receipt, err = Capture(context.Background(), missing)
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "unsafe_path" {
		t.Fatalf("missing source result = %#v, err=%v", receipt, err)
	}
}

func TestCaptureCanceledContextDoesNotCreateRoot(t *testing.T) {
	sources := makeCaptureTestSources(t, false)
	defer closeCaptureTestSources(t, sources)
	root := filepath.Join(t.TempDir(), "canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipt, err := Capture(ctx, CaptureOptions{
		SnapshotDir: root, LiveDCI: sources.dci, LiveDCIJSONL: sources.dciJSONL,
		LiveEventStore: sources.eventStore, LiveL1: sources.l1, LiveArchive: sources.archive,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "context_canceled" {
		t.Fatalf("canceled result = %#v, err=%v", receipt, err)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled capture root stat = %v, want not exist", statErr)
	}
}

func TestCaptureStepAndFinishFailuresStayBlocked(t *testing.T) {
	sources := makeCaptureTestSources(t, false)
	defer closeCaptureTestSources(t, sources)
	originalStep := captureBackupStep
	originalFinish := captureBackupFinish
	t.Cleanup(func() {
		captureBackupStep = originalStep
		captureBackupFinish = originalFinish
	})
	finishCalls := 0
	captureBackupStep = func(_ *sqlite.Backup, _ int32) (bool, error) {
		return false, errors.New("injected step failure")
	}
	captureBackupFinish = func(backup *sqlite.Backup) error {
		finishCalls++
		realErr := originalFinish(backup)
		return errors.Join(realErr, errors.New("injected finish failure"))
	}
	root := filepath.Join(t.TempDir(), "step-failure")
	receipt, err := Capture(context.Background(), CaptureOptions{
		SnapshotDir: root, LiveDCI: sources.dci, LiveDCIJSONL: sources.dciJSONL,
		LiveEventStore: sources.eventStore, LiveL1: sources.l1, LiveArchive: sources.archive,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode == "" || finishCalls == 0 {
		t.Fatalf("injected backup failure result = %#v, err=%v finishCalls=%d", receipt, err, finishCalls)
	}
	data, readErr := os.ReadFile(filepath.Join(root, CaptureReceiptFilename))
	if readErr != nil {
		t.Fatalf("blocked receipt missing after injected failure: %v", readErr)
	}
	var persisted CaptureReceipt
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusBlocked || persisted.ErrorCode == "" {
		t.Fatalf("persisted injected-failure receipt = %#v", persisted)
	}
}

func TestCaptureRejectsSymlinkSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not portable on Windows")
	}
	sources := makeCaptureTestSources(t, false)
	defer closeCaptureTestSources(t, sources)
	link := filepath.Join(t.TempDir(), "dci-link.db")
	if err := os.Symlink(sources.dci, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root := filepath.Join(t.TempDir(), "symlink-root")
	receipt, err := Capture(context.Background(), CaptureOptions{
		SnapshotDir: root, LiveDCI: link, LiveDCIJSONL: sources.dciJSONL,
		LiveEventStore: sources.eventStore, LiveL1: sources.l1, LiveArchive: sources.archive,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "unsafe_path" {
		t.Fatalf("symlink source result = %#v, err=%v", receipt, err)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("symlink rejection root stat = %v, want not exist", statErr)
	}
}

func TestCaptureRejectsHardlinkSourceAlias(t *testing.T) {
	sources := makeCaptureTestSources(t, false)
	defer closeCaptureTestSources(t, sources)
	link := filepath.Join(t.TempDir(), "event-store-link.db")
	if err := os.Link(sources.dci, link); err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "not supported") || strings.Contains(message, "not implemented") {
			t.Skipf("hardlink unsupported: %v", err)
		}
		t.Fatalf("hardlink creation failed: %v", err)
	}
	root := filepath.Join(t.TempDir(), "hardlink-root")
	receipt, err := Capture(context.Background(), CaptureOptions{
		SnapshotDir: root, LiveDCI: sources.dci, LiveDCIJSONL: sources.dciJSONL,
		LiveEventStore: link, LiveL1: sources.l1, LiveArchive: sources.archive,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "unsafe_path" {
		t.Fatalf("hardlink source result = %#v, err=%v", receipt, err)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hardlink rejection root stat = %v, want not exist", statErr)
	}
}

func TestCaptureRejectsMalformedJSONLBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		write func(*testing.T, string)
		code  string
	}{
		{
			name: "invalid utf8",
			write: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte{0xff, '\n'}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			code: "malformed_jsonl",
		},
		{
			name: "unknown schema key",
			write: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`{"event_id":"unknown-key","unknown":true}`+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			code: "malformed_jsonl",
		},
		{
			name: "truncated record",
			write: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`{"event_id":"truncated"`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			code: "malformed_jsonl",
		},
		{
			name: "oversized source",
			write: func(t *testing.T, path string) {
				t.Helper()
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				err = file.Truncate(maxJSONLBytes + 1)
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Skipf("sparse/truncate unavailable: truncate=%v close=%v", err, closeErr)
				}
			},
			code: "oversized_jsonl",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sources := makeCaptureTestSources(t, false)
			defer closeCaptureTestSources(t, sources)
			test.write(t, sources.dciJSONL)
			root := filepath.Join(t.TempDir(), "capture-root")
			receipt, err := Capture(context.Background(), captureTestOptions(root, sources))
			if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != test.code {
				t.Fatalf("capture result = %#v, err=%v", receipt, err)
			}
			data, readErr := os.ReadFile(filepath.Join(root, CaptureReceiptFilename))
			if readErr != nil {
				t.Fatalf("blocked receipt missing: %v", readErr)
			}
			var persisted CaptureReceipt
			if err := json.Unmarshal(data, &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted.Status != StatusBlocked || persisted.ErrorCode != test.code {
				t.Fatalf("persisted capture result = %#v", persisted)
			}
		})
	}
}

func TestCaptureRejectsJSONLSourceMutation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.jsonl")
	destination := filepath.Join(root, "captured.jsonl")
	if err := os.WriteFile(source, []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalHash := captureJSONLSourceHash
	t.Cleanup(func() { captureJSONLSourceHash = originalHash })
	calls := 0
	captureJSONLSourceHash = func(string) (string, error) {
		calls++
		if calls == 1 {
			return strings.Repeat("a", 64), nil
		}
		return strings.Repeat("b", 64), nil
	}
	_, err := captureJSONL(context.Background(), source, destination)
	if err == nil || errorCode(err, "") != "source_changed" {
		t.Fatalf("source mutation result = %v, code=%q", err, errorCode(err, ""))
	}
	if calls != 2 {
		t.Fatalf("source hash calls = %d, want 2", calls)
	}
}

func TestCaptureReceiptFailureCannotClaimReady(t *testing.T) {
	sources := makeCaptureTestSources(t, false)
	defer closeCaptureTestSources(t, sources)
	originalWriter := captureReceiptWriter
	t.Cleanup(func() { captureReceiptWriter = originalWriter })
	captureReceiptWriter = func(string, CaptureReceipt) error { return errors.New("injected receipt failure") }
	receipt, err := Capture(context.Background(), CaptureOptions{
		SnapshotDir: filepath.Join(t.TempDir(), "receipt-failure"), LiveDCI: sources.dci, LiveDCIJSONL: sources.dciJSONL,
		LiveEventStore: sources.eventStore, LiveL1: sources.l1, LiveArchive: sources.archive,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "capture_receipt" {
		t.Fatalf("injected receipt failure result = %#v, err=%v", receipt, err)
	}
}

func captureTestOptions(root string, sources captureTestSources) CaptureOptions {
	return CaptureOptions{
		SnapshotDir: root, LiveDCI: sources.dci, LiveDCIJSONL: sources.dciJSONL,
		LiveEventStore: sources.eventStore, LiveL1: sources.l1, LiveArchive: sources.archive,
	}
}

func makeCaptureTestSources(t *testing.T, wal bool) captureTestSources {
	t.Helper()
	root := t.TempDir()
	paths := captureTestSources{
		dci: filepath.Join(root, "dci.db"), dciJSONL: filepath.Join(root, "dci.jsonl"),
		eventStore: filepath.Join(root, "event-store.db"), l1: filepath.Join(root, "l1.db"), archive: filepath.Join(root, "archive.db"),
	}
	if err := os.WriteFile(paths.dciJSONL, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for index, path := range []string{paths.dci, paths.eventStore, paths.l1, paths.archive} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		paths.dbs = append(paths.dbs, db)
		if wal && index == 0 {
			if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE wal_probe (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO wal_probe(id, value) VALUES (1, 'wal-visible')"); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("CREATE TABLE probe_%d (id INTEGER PRIMARY KEY)", index)); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func closeCaptureTestSources(t *testing.T, sources captureTestSources) {
	t.Helper()
	for _, db := range sources.dbs {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
