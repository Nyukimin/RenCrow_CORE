package dcimigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuiesceCutoverActiveSQLiteSourcesCheckpointsPersistentWAL(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "dci.db"),
		filepath.Join(root, "event-store.db"),
		filepath.Join(root, "l1.db"),
		filepath.Join(root, "archive.db"),
	}
	before := make([]os.FileInfo, len(paths))
	for index, path := range paths {
		seedCutoverPersistentWAL(t, path)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[index] = info
		if _, err := os.Lstat(path + "-wal"); err != nil {
			t.Fatalf("persistent WAL fixture missing for %s: %v", filepath.Base(path), err)
		}
	}
	jsonl := filepath.Join(root, "dci.jsonl")
	if err := os.WriteFile(jsonl, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	evidence, err := quiesceCutoverActiveSQLiteSources(context.Background(), cutoverActiveOptions{
		SourceDCI: paths[0], SourceDCIJSONL: jsonl, SourceEventStore: paths[1],
		SourceL1: paths[2], SourceArchive: paths[3],
	})
	if err != nil || !evidence.valid() {
		t.Fatalf("quiesce evidence=%#v err=%v", evidence, err)
	}
	for index, path := range paths {
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(before[index], after) {
			t.Fatalf("active source identity changed for %s: %v", filepath.Base(path), err)
		}
		for _, suffix := range sqliteSidecarSuffixes {
			if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("sidecar remains for %s%s: %v", filepath.Base(path), suffix, err)
			}
		}
		db, err := openSQLiteReadOnly(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		var mode string
		var count int
		modeErr := db.QueryRow("PRAGMA journal_mode").Scan(&mode)
		countErr := db.QueryRow("SELECT count(*) FROM quiesce_fixture").Scan(&count)
		closeErr := db.Close()
		if modeErr != nil || countErr != nil || closeErr != nil || !strings.EqualFold(mode, "delete") || count != 1 {
			t.Fatalf("quiesced %s mode=%q count=%d errors=%v/%v/%v", filepath.Base(path), mode, count, modeErr, countErr, closeErr)
		}
	}
}

func TestQuiesceCutoverActiveSQLiteSourcesRejectsBusyAndUnsafeSources(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "dci.db"), filepath.Join(root, "event-store.db"),
		filepath.Join(root, "l1.db"), filepath.Join(root, "archive.db"),
	}
	for _, path := range paths {
		seedCutoverPersistentWAL(t, path)
	}
	jsonl := filepath.Join(root, "dci.jsonl")
	if err := os.WriteFile(jsonl, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := cutoverActiveOptions{
		SourceDCI: paths[0], SourceDCIJSONL: jsonl, SourceEventStore: paths[1],
		SourceL1: paths[2], SourceArchive: paths[3],
	}

	busy, err := sql.Open("sqlite", paths[0]+"?_pragma=busy_timeout%3d0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	tx, err := busy.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO quiesce_fixture(value) VALUES ('busy')"); err != nil {
		t.Fatal(err)
	}
	if evidence, err := quiesceCutoverActiveSQLiteSources(context.Background(), options); err == nil || evidence.valid() {
		t.Fatalf("busy source accepted: evidence=%#v err=%v", evidence, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := busy.Close(); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "dci-link.db")
	if err := os.Symlink(paths[0], link); err != nil {
		t.Fatal(err)
	}
	unsafe := options
	unsafe.SourceDCI = link
	if evidence, err := quiesceCutoverActiveSQLiteSources(context.Background(), unsafe); err == nil || evidence.valid() {
		t.Fatalf("symlink source accepted: evidence=%#v err=%v", evidence, err)
	}

	hardlink := filepath.Join(root, "event-store-alias.db")
	if err := os.Link(paths[0], hardlink); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	aliased := options
	aliased.SourceEventStore = hardlink
	if evidence, err := quiesceCutoverActiveSQLiteSources(context.Background(), aliased); err == nil || evidence.valid() {
		t.Fatalf("hardlink alias accepted: evidence=%#v err=%v", evidence, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if evidence, err := quiesceCutoverActiveSQLiteSources(canceled, options); !errors.Is(err, context.Canceled) || evidence.valid() {
		t.Fatalf("canceled quiesce evidence=%#v err=%v", evidence, err)
	}
}

func seedCutoverPersistentWAL(t *testing.T, path string) {
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
		if err != nil {
			return err
		}
		if mode != 1 {
			return errors.New("persistent WAL control was not enabled")
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
}

func forceCutoverPersistentWALWithoutLogicalChange(t *testing.T, path string) {
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
		if err != nil {
			return err
		}
		if mode != 1 {
			return errors.New("persistent WAL control was not enabled")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var userVersion int
	if err := conn.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA user_version=%d", userVersion)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path + "-wal"); err != nil {
		t.Fatalf("persistent WAL fixture missing for %s: %v", filepath.Base(path), err)
	}
}
