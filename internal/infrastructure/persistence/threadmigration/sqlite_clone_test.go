package threadmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	sqlite "modernc.org/sqlite"
)

type sqliteCloneFixture struct {
	directory string
	source    string
}

func newSQLiteCloneFixture(t *testing.T) sqliteCloneFixture {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "legacy.sqlite")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatalf("open clone source: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE clone_fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL); INSERT INTO clone_fixture(id, value) VALUES (1, 'alpha'), (2, 'beta');`); err != nil {
		_ = db.Close()
		t.Fatalf("create clone source: %v", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		_ = db.Close()
		t.Fatalf("normalize clone source journal: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close clone source: %v", err)
	}
	return sqliteCloneFixture{directory: directory, source: source}
}

func TestCloneSQLiteCreatesDeterministicReadOnlyVerifiedClone(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	destination := filepath.Join(fixture.directory, "clone-a.sqlite")
	sourceBefore := mustSQLiteCloneBytes(t, fixture.source)

	receipt, err := CloneSQLite(context.Background(), SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination})
	if err != nil {
		t.Fatalf("CloneSQLite() error = %v", err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("clone receipt Validate() error = %v", err)
	}
	if receipt.Method != SQLiteCloneMethod || receipt.PageCount <= 0 || receipt.QuickCheck != SQLiteCloneQuickCheckOK || receipt.SidecarZero != 0 {
		t.Fatalf("unexpected clone receipt = %+v", receipt)
	}
	if sourceAfter := mustSQLiteCloneBytes(t, fixture.source); !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatal("source bytes changed during clone")
	}
	outputBytes := mustSQLiteCloneBytes(t, destination)
	if receipt.Bytes != int64(len(outputBytes)) || receipt.OutputSHA256 != sqliteCloneSHA256(outputBytes) || receipt.SourceSHA256 != sqliteCloneSHA256(sourceBefore) {
		t.Fatalf("clone receipt does not bind file evidence: receipt=%+v bytes=%d", receipt, len(outputBytes))
	}
	assertSQLiteCloneOutput(t, destination)

	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	canonicalAgain, err := receipt.CanonicalJSON()
	if err != nil || !reflect.DeepEqual(canonical, canonicalAgain) {
		t.Fatalf("clone receipt JSON is not deterministic: %q / %q (err=%v)", canonical, canonicalAgain, err)
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil || computed != receipt.ReceiptSHA256 {
		t.Fatalf("clone receipt hash = %q, computed=%q, err=%v", receipt.ReceiptSHA256, computed, err)
	}
}

func TestCloneSQLiteProducesSameOutputForSameSource(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	first, err := CloneSQLite(context.Background(), SQLiteCloneInput{
		SourcePath: fixture.source, DestinationPath: filepath.Join(fixture.directory, "clone-first.sqlite"),
	})
	if err != nil {
		t.Fatalf("first CloneSQLite() error = %v", err)
	}
	second, err := CloneSQLite(context.Background(), SQLiteCloneInput{
		SourcePath: fixture.source, DestinationPath: filepath.Join(fixture.directory, "clone-second.sqlite"),
	})
	if err != nil {
		t.Fatalf("second CloneSQLite() error = %v", err)
	}
	if first.OutputSHA256 != second.OutputSHA256 || first.Bytes != second.Bytes || first.PageCount != second.PageCount || first.ReceiptSHA256 != second.ReceiptSHA256 {
		t.Fatalf("same source produced nondeterministic clone evidence: first=%+v second=%+v", first, second)
	}
}

func TestCloneSQLiteHandlesFilesystemNamesRequiringURIEscaping(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	destination := filepath.Join(fixture.directory, "clone ? # %.sqlite")
	receipt, err := CloneSQLite(context.Background(), SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination})
	if err != nil {
		t.Fatalf("CloneSQLite() with escaped destination name error = %v", err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("escaped-name receipt Validate() error = %v", err)
	}
	assertSQLiteCloneOutput(t, destination)
}

func TestCloneSQLiteRejectsUnsafePathsBeforeCreatingDestination(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	tests := []struct {
		name string
		make func(t *testing.T) (SQLiteCloneInput, string)
	}{
		{
			name: "missing source",
			make: func(t *testing.T) (SQLiteCloneInput, string) {
				destination := filepath.Join(fixture.directory, "missing-source.sqlite")
				return SQLiteCloneInput{SourcePath: filepath.Join(fixture.directory, "missing.sqlite"), DestinationPath: destination}, destination
			},
		},
		{
			name: "source sidecar suffix",
			make: func(t *testing.T) (SQLiteCloneInput, string) {
				destination := filepath.Join(fixture.directory, "source-sidecar-suffix.sqlite")
				return SQLiteCloneInput{SourcePath: fixture.source + "-wal", DestinationPath: destination}, destination
			},
		},
		{
			name: "source sidecar exists",
			make: func(t *testing.T) (SQLiteCloneInput, string) {
				sidecar := fixture.source + "-wal"
				if err := os.WriteFile(sidecar, []byte("sidecar"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(sidecar) })
				destination := filepath.Join(fixture.directory, "source-sidecar.sqlite")
				return SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination}, destination
			},
		},
		{
			name: "destination exists",
			make: func(t *testing.T) (SQLiteCloneInput, string) {
				destination := filepath.Join(fixture.directory, "existing.sqlite")
				if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				return SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination}, destination
			},
		},
		{
			name: "destination sidecar exists",
			make: func(t *testing.T) (SQLiteCloneInput, string) {
				destination := filepath.Join(fixture.directory, "destination-sidecar.sqlite")
				if err := os.WriteFile(destination+"-wal", []byte("sidecar"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(destination + "-wal") })
				return SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination}, destination
			},
		},
		{
			name: "destination sidecar suffix",
			make: func(t *testing.T) (SQLiteCloneInput, string) {
				destination := filepath.Join(fixture.directory, "destination.sqlite-wal")
				return SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination}, destination
			},
		},
		{
			name: "destination parent missing",
			make: func(t *testing.T) (SQLiteCloneInput, string) {
				destination := filepath.Join(fixture.directory, "missing-parent", "clone.sqlite")
				return SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination}, destination
			},
		},
		{
			name: "same path",
			make: func(t *testing.T) (SQLiteCloneInput, string) {
				return SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: fixture.source}, fixture.source
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, destination := test.make(t)
			before, exists := sqliteCloneOptionalBytes(destination)
			_, err := CloneSQLite(context.Background(), input)
			if err == nil {
				t.Fatal("unsafe clone path was accepted")
			}
			assertSQLiteCloneError(t, err, false)
			after, afterExists := sqliteCloneOptionalBytes(destination)
			if exists != afterExists || !reflect.DeepEqual(before, after) {
				t.Fatalf("preflight failure mutated destination: before=%v/%v after=%v/%v", before, exists, after, afterExists)
			}
		})
	}
}

func TestCloneSQLiteRejectsSymlinkSourceAndDestinationParent(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	link := filepath.Join(fixture.directory, "source-link.sqlite")
	if err := os.Symlink(fixture.source, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	destination := filepath.Join(fixture.directory, "symlink-source-destination.sqlite")
	_, err := CloneSQLite(context.Background(), SQLiteCloneInput{SourcePath: link, DestinationPath: destination})
	if err == nil {
		t.Fatal("symlink source was accepted")
	}
	assertSQLiteCloneError(t, err, false)

	parent := filepath.Join(fixture.directory, "parent-link")
	if err := os.Symlink(fixture.directory, parent); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(parent) })
	destination = filepath.Join(parent, "clone.sqlite")
	_, err = CloneSQLite(context.Background(), SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination})
	if err == nil {
		t.Fatal("symlink destination parent was accepted")
	}
	assertSQLiteCloneError(t, err, false)
}

func TestCloneSQLiteRejectsMalformedSourceAndCanceledContext(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	malformed := filepath.Join(fixture.directory, "malformed.sqlite")
	if err := os.WriteFile(malformed, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fixture.directory, "malformed-clone.sqlite")
	_, err := CloneSQLite(context.Background(), SQLiteCloneInput{SourcePath: malformed, DestinationPath: destination})
	if err == nil {
		t.Fatal("malformed SQLite source was accepted")
	}
	assertSQLiteCloneError(t, err, false)
	if _, exists := sqliteCloneOptionalBytes(destination); exists {
		t.Fatal("malformed source created a destination")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	destination = filepath.Join(fixture.directory, "canceled-clone.sqlite")
	_, err = CloneSQLite(canceled, SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled clone error = %v, want context.Canceled", err)
	}
	assertSQLiteCloneError(t, err, false)
	if _, exists := sqliteCloneOptionalBytes(destination); exists {
		t.Fatal("canceled clone created a destination")
	}
}

func TestCloneSQLiteBackupStepFailureMarksOutputUnusableAndBoundsError(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	destination := filepath.Join(fixture.directory, "step-failure.sqlite")
	original := sqliteCloneBackupStep
	sqliteCloneBackupStep = func(_ *sqlite.Backup, _ int32) (bool, error) {
		return false, fmt.Errorf("private source path %s and secret payload", fixture.source)
	}
	t.Cleanup(func() { sqliteCloneBackupStep = original })
	_, err := CloneSQLite(context.Background(), SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination})
	if err == nil {
		t.Fatal("injected Backup.Step failure was not returned")
	}
	assertSQLiteCloneError(t, err, true)
	if strings.Contains(err.Error(), fixture.source) || strings.Contains(err.Error(), "secret payload") || len(err.Error()) > 256 {
		t.Fatalf("clone error leaked or exceeded bound: %q", err.Error())
	}
	if _, exists := sqliteCloneOptionalBytes(destination); !exists {
		t.Fatal("post-output failure did not leave an explicitly unusable destination")
	}
}

func TestCloneSQLiteContextCancellationDuringBackupMarksOutputUnusable(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	destination := filepath.Join(fixture.directory, "cancel-during-backup.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	original := sqliteCloneBackupStep
	sqliteCloneBackupStep = func(backup *sqlite.Backup, pages int32) (bool, error) {
		cancel()
		return original(backup, pages)
	}
	t.Cleanup(func() { sqliteCloneBackupStep = original })
	_, err := CloneSQLite(ctx, SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled backup error = %v, want context.Canceled", err)
	}
	assertSQLiteCloneError(t, err, true)
	if _, exists := sqliteCloneOptionalBytes(destination); !exists {
		t.Fatal("canceled backup did not leave an explicitly unusable destination")
	}
}

func TestCloneSQLiteSourceDriftIsRejectedAfterBackup(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	destination := filepath.Join(fixture.directory, "source-drift.sqlite")
	writer, err := sql.Open("sqlite", fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	original := sqliteCloneBackupStep
	changed := false
	sqliteCloneBackupStep = func(backup *sqlite.Backup, pages int32) (bool, error) {
		if !changed {
			changed = true
			if _, err := writer.Exec(`UPDATE clone_fixture SET value = 'changed-during-backup' WHERE id = 1`); err != nil {
				return false, err
			}
		}
		return original(backup, pages)
	}
	t.Cleanup(func() { sqliteCloneBackupStep = original })
	_, err = CloneSQLite(context.Background(), SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination})
	if err == nil {
		t.Fatal("source drift was accepted")
	}
	assertSQLiteCloneError(t, err, true)
	if typed := new(SQLiteCloneError); !errors.As(err, &typed) || typed.Code != "source_changed" {
		t.Fatalf("source drift error = %v", err)
	}
}

func TestCloneSQLiteBackupFinishFailureMarksOutputUnusable(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	destination := filepath.Join(fixture.directory, "finish-failure.sqlite")
	original := sqliteCloneBackupFinish
	sqliteCloneBackupFinish = func(backup *sqlite.Backup) error {
		_ = original(backup)
		return errors.New("injected finish failure with private path")
	}
	t.Cleanup(func() { sqliteCloneBackupFinish = original })
	_, err := CloneSQLite(context.Background(), SQLiteCloneInput{SourcePath: fixture.source, DestinationPath: destination})
	if err == nil {
		t.Fatal("injected Backup.Finish failure was not returned")
	}
	assertSQLiteCloneError(t, err, true)
	if _, exists := sqliteCloneOptionalBytes(destination); !exists {
		t.Fatal("finish failure did not leave an explicitly unusable destination")
	}
}

func TestSQLiteCloneReceiptRejectsRehashedIntegrityTamper(t *testing.T) {
	fixture := newSQLiteCloneFixture(t)
	receipt, err := CloneSQLite(context.Background(), SQLiteCloneInput{
		SourcePath: fixture.source, DestinationPath: filepath.Join(fixture.directory, "receipt.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := receipt
	tampered.SidecarZero = 1
	tampered.ReceiptSHA256 = mustSQLiteCloneReceiptHash(t, tampered)
	if err := tampered.Validate(); err == nil {
		t.Fatal("rehashed sidecar integrity tamper was accepted")
	}

	tampered = receipt
	tampered.QuickCheck = "not-ok"
	tampered.ReceiptSHA256 = mustSQLiteCloneReceiptHash(t, tampered)
	if err := tampered.Validate(); err == nil {
		t.Fatal("rehashed quick-check tamper was accepted")
	}
}

func assertSQLiteCloneOutput(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat SQLite clone: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		t.Fatalf("SQLite clone file metadata = %#v", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("SQLite clone permissions = %o, want 600", info.Mode().Perm())
	}
	for _, suffix := range sqliteCloneSidecarSuffixes {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("SQLite clone sidecar %s exists: %v", suffix, err)
		}
	}
	db, err := sql.Open("sqlite", sqliteCloneDSN(path, "mode=ro"))
	if err != nil {
		t.Fatalf("open SQLite clone: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || !strings.EqualFold(mode, "delete") {
		t.Fatalf("SQLite clone journal mode = %q, err=%v", mode, err)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM clone_fixture WHERE id = 1`).Scan(&value); err != nil || value != "alpha" {
		t.Fatalf("SQLite clone row = %q, err=%v", value, err)
	}
}

func assertSQLiteCloneError(t *testing.T, err error, postOutput bool) {
	t.Helper()
	var typed *SQLiteCloneError
	if !errors.As(err, &typed) {
		t.Fatalf("clone error type = %T, want *SQLiteCloneError", err)
	}
	if typed.PostOutputUnusable != postOutput {
		t.Fatalf("clone post-output marker = %v, want %v", typed.PostOutputUnusable, postOutput)
	}
	if len(err.Error()) > 256 {
		t.Fatalf("clone bounded error length = %d", len(err.Error()))
	}
}

func mustSQLiteCloneBytes(t *testing.T, path string) []byte {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SQLite clone file: %v", err)
	}
	return bytes
}

func sqliteCloneOptionalBytes(path string) ([]byte, bool) {
	bytes, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		return []byte(err.Error()), true
	}
	return bytes, true
}

func sqliteCloneSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func mustSQLiteCloneReceiptHash(t *testing.T, receipt SQLiteCloneReceipt) string {
	t.Helper()
	hash, err := receipt.ComputeSHA256()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
