package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuiesceSQLiteCheckpointsConfiguredPersistentWAL(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	l1 := filepath.Join(directory, "l1.db")
	archive := filepath.Join(directory, "archive.db")
	seedThreadPersistentWAL(t, l1)
	seedThreadPersistentWAL(t, archive)
	configData, err := os.ReadFile(filepath.Join("..", "..", "config", "config.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	configText := strings.Replace(string(configData), `conversation_l1: "./data/l1_memory.db"`, `conversation_l1: "`+l1+`"`, 1)
	configText = strings.Replace(configText, `conversation_archive: "./data/memory_archive.db"`, `conversation_archive: "`+archive+`"`, 1)
	configPath := filepath.Join(directory, "core.yaml")
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	originalProof := proveThreadWriterStopped
	proveThreadWriterStopped = func(context.Context) error { return nil }
	t.Cleanup(func() { proveThreadWriterStopped = originalProof })

	receipt, err := quiesceSQLite(context.Background(), configPath)
	if err != nil || receipt.validate() != nil || receipt.Status != quiesceSQLiteStatusReady {
		t.Fatalf("quiesceSQLite() = %+v, %v", receipt, err)
	}
	for _, path := range []string{l1, archive} {
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("sidecar remains: %s%s: %v", path, suffix, err)
			}
		}
		db, err := sql.Open("sqlite", path+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var mode string
		var count int
		modeErr := db.QueryRow("PRAGMA journal_mode").Scan(&mode)
		countErr := db.QueryRow("SELECT count(*) FROM quiesce_fixture").Scan(&count)
		closeErr := db.Close()
		if modeErr != nil || countErr != nil || closeErr != nil || !strings.EqualFold(mode, "delete") || count != 1 {
			t.Fatalf("quiesced DB mode=%q count=%d errors=%v/%v/%v", mode, count, modeErr, countErr, closeErr)
		}
	}
}

func TestQuiesceSQLiteRejectsRunningWriterBeforeConfigRead(t *testing.T) {
	originalProof := proveThreadWriterStopped
	proveThreadWriterStopped = func(context.Context) error { return errors.New("running") }
	t.Cleanup(func() { proveThreadWriterStopped = originalProof })
	receipt, err := quiesceSQLite(context.Background(), "/private/missing-config")
	if err == nil || receipt.validate() != nil || receipt.ErrorCode != "writer_not_stopped" {
		t.Fatalf("quiesceSQLite() = %+v, %v", receipt, err)
	}
}

func TestQuiesceSQLiteReportsUnavailableWriterProof(t *testing.T) {
	originalProof := proveThreadWriterStopped
	proveThreadWriterStopped = func(context.Context) error { return errWriterStoppedProofUnavailable }
	t.Cleanup(func() { proveThreadWriterStopped = originalProof })
	receipt, err := quiesceSQLite(context.Background(), "/private/missing-config")
	if err == nil || receipt.validate() != nil || receipt.ErrorCode != "writer_stopped_proof_unavailable" {
		t.Fatalf("quiesceSQLite() = %+v, %v", receipt, err)
	}
}

func TestRunQuiesceSQLiteRequiresStoppedAssertionAndWritesPathFreeReceipt(t *testing.T) {
	want := quiesceSQLiteReceipt{
		SchemaVersion: quiesceSQLiteSchemaVersion, SQLiteSources: 2, BusyZero: true, JournalModeDelete: true,
		SameFile: true, SidecarZero: true, L1SHA256: strings.Repeat("1", 64), ArchiveSHA256: strings.Repeat("2", 64),
	}
	want = sealQuiesceSQLiteReceipt(want, quiesceSQLiteStatusReady, "")
	var stdout bytes.Buffer
	called := false
	code := runQuiesceSQLite([]string{"--config", "/private/core.yaml", "--initial-service-stopped"}, &stdout, func(_ context.Context, config string) (quiesceSQLiteReceipt, error) {
		called = config == "/private/core.yaml"
		return want, nil
	})
	encoded, _ := json.Marshal(want)
	if code != 0 || !called || stdout.String() != string(append(encoded, '\n')) || strings.Contains(stdout.String(), "/private") {
		t.Fatalf("runQuiesceSQLite() = %d called=%v stdout=%q", code, called, stdout.String())
	}
	stdout.Reset()
	called = false
	if code := runQuiesceSQLite([]string{"--config", "/private/core.yaml"}, &stdout, func(context.Context, string) (quiesceSQLiteReceipt, error) {
		called = true
		return want, nil
	}); code == 0 || called {
		t.Fatal("quiesce accepted invocation without stopped assertion")
	}
}

func seedThreadPersistentWAL(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(driverConn any) error {
		control, ok := driverConn.(interface {
			FileControlPersistWAL(string, int) (int, error)
		})
		if !ok {
			return errors.New("SQLite driver does not expose persistent WAL control")
		}
		mode, err := control.FileControlPersistWAL("main", 1)
		if err != nil || mode != 1 {
			return errors.New("persistent WAL control failed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "CREATE TABLE quiesce_fixture(value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "INSERT INTO quiesce_fixture(value) VALUES ('retained')"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
