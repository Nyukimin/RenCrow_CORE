package charimage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	domaincharimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/charimage"
	_ "modernc.org/sqlite"
)

func TestSQLiteStorePersistsHistoryAcrossRestartInNewestFirstOrder(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(privateCharImageTestDir(t), "charimage_history.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	older := historyEntry("img_older", time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC))
	newer := historyEntry("img_newer", time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC))
	if err := store.Save(ctx, older); err != nil {
		t.Fatalf("Save(older) error = %v", err)
	}
	if err := store.Save(ctx, newer); err != nil {
		t.Fatalf("Save(newer) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	restarted, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() after restart error = %v", err)
	}
	defer restarted.Close()
	items, err := restarted.List(ctx, 10)
	if err != nil {
		t.Fatalf("List() after restart error = %v", err)
	}
	if len(items) != 2 || items[0].ID != newer.ID || items[1].ID != older.ID {
		t.Fatalf("history order = %#v, want newer first", items)
	}
	if items[0] != newer {
		t.Fatalf("restored history = %#v, want %#v", items[0], newer)
	}
}

func TestSQLiteStoreCreatesPrivateDatabaseAndRejectsBroadExistingPermissions(t *testing.T) {
	dbPath := filepath.Join(privateCharImageTestDir(t), "charimage_history.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("new database mode = %o, want 600", info.Mode().Perm())
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dbPath, 0o640); err != nil {
			t.Fatal(err)
		}
		if store, err := NewSQLiteStore(dbPath); err == nil {
			_ = store.Close()
			t.Fatal("NewSQLiteStore() accepted an existing database readable by group")
		}
		info, err = os.Stat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("existing database mode changed to %o; existing permissions must be preserved", info.Mode().Perm())
		}
	}
}

func TestSQLiteStoreRejectsWritableParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows confidentiality is enforced through DACLs")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	if store, err := NewSQLiteStore(filepath.Join(parent, "history.db")); err == nil {
		_ = store.Close()
		t.Fatal("NewSQLiteStore() accepted a group/world-writable parent")
	}
}

func TestSQLiteStoreFailsClosedForRelativeOrMissingParentPath(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "relative path", path: "history.db"},
		{name: "missing parent", path: filepath.Join(t.TempDir(), "missing", "history.db")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if store, err := NewSQLiteStore(test.path); err == nil {
				_ = store.Close()
				t.Fatal("NewSQLiteStore() accepted a non-durable path")
			}
		})
	}
}

func TestSQLiteStoreRejectsUnsupportedSchemaAndInvalidEntry(t *testing.T) {
	dbPath := filepath.Join(privateCharImageTestDir(t), "charimage_history.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := setUserVersion(dbPath, 2); err != nil {
		t.Fatal(err)
	}
	if store, err := NewSQLiteStore(dbPath); err == nil {
		_ = store.Close()
		t.Fatal("NewSQLiteStore() accepted a newer schema revision")
	}

	valid, err := NewSQLiteStore(filepath.Join(privateCharImageTestDir(t), "valid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer valid.Close()
	entry := historyEntry("img_invalid", time.Now().UTC())
	entry.ImageSHA256 = strings.Repeat("0", 62)
	if err := valid.Save(context.Background(), entry); err == nil {
		t.Fatal("Save() accepted an invalid image hash")
	}
}

func TestSQLiteStoreEscapesSpecialCharactersInDatabasePath(t *testing.T) {
	dbPath := filepath.Join(privateCharImageTestDir(t), "history?# report.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() with URI-special path: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected database file at configured path %q: %v", dbPath, err)
	}
}

func TestSQLiteStoreRejectsIncompleteSchemaAtStartupAndPreflight(t *testing.T) {
	dbPath := filepath.Join(privateCharImageTestDir(t), "charimage_history.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE charimage_history`); err != nil {
		t.Fatalf("drop history table: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TABLE charimage_history (id TEXT PRIMARY KEY NOT NULL)`); err != nil {
		t.Fatalf("create incomplete history table: %v", err)
	}
	if err := store.Ping(context.Background()); err == nil {
		t.Fatal("Ping() accepted a database whose required history table was removed")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := NewSQLiteStore(dbPath); err == nil {
		_ = reopened.Close()
		t.Fatal("NewSQLiteStore() accepted an incomplete schema with user_version=1")
	}
}

func privateCharImageTestDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func historyEntry(id string, createdAt time.Time) domaincharimage.HistoryEntry {
	return domaincharimage.HistoryEntry{
		ID:                    id,
		CreatedAt:             createdAt,
		CharacterID:           "01_sera",
		RenderMode:            domaincharimage.RenderMode2D,
		SceneVariantText:      "custom input",
		SceneVariantEffective: "custom input",
		FinalPrompt:           "character scene render tail",
		Profile:               domaincharimage.ModelProfileID,
		ImageID:               id,
		ImageSHA256:           strings.Repeat("0", 64),
		ImageSizeBytes:        1234,
		ImageContentType:      "image/png",
		ImageWidth:            768,
		ImageHeight:           768,
		CatalogRevision:       "sha256:" + strings.Repeat("1", 64),
	}
}

func setUserVersion(path string, version int) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version))
	return err
}
