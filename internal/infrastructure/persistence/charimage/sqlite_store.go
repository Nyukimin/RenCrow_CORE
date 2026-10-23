package charimage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	domaincharimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/charimage"
	_ "modernc.org/sqlite"
)

const (
	schemaVersion = 1
	maxHistory    = 100
	timeFormat    = time.RFC3339Nano
)

var historyImageIDPattern = regexp.MustCompile(`^img_[0-9A-Za-z_-]+$`)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("CharImage history sqlite path must be absolute")
	}
	parent := filepath.Dir(path)
	if err := validatePrivateDatabaseDirectory(parent); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		if err := secureCreatedPrivateDatabase(path, file); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close new CharImage history database: %w", err)
		}
	} else if os.IsExist(err) {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, fmt.Errorf("inspect existing CharImage history database: %w", statErr)
		}
		if err := validateExistingPrivateDatabase(path, info); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("create CharImage history database: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteDatabaseURI(path))
	if err != nil {
		return nil, fmt.Errorf("open CharImage history sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("CharImage history sqlite store is closed")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read CharImage history schema version: %w", err)
	}
	if version != schemaVersion {
		return fmt.Errorf("CharImage history schema version %d is unsupported (current %d)", version, schemaVersion)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, created_at, character_id, render_mode, scene_variant_text,
		scene_variant_effective, final_prompt, profile, image_id, image_sha256,
		image_size_bytes, image_content_type, image_width, image_height, catalog_revision
		FROM charimage_history LIMIT 0`)
	if err != nil {
		return fmt.Errorf("validate CharImage history schema: %w", err)
	}
	return rows.Close()
}

func (s *SQLiteStore) Save(ctx context.Context, entry domaincharimage.HistoryEntry) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("CharImage history sqlite store is closed")
	}
	if err := validateEntry(entry); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO charimage_history (
		id, created_at, character_id, render_mode, scene_variant_text,
		scene_variant_effective, final_prompt, profile, image_id, image_sha256,
		image_size_bytes, image_content_type, image_width, image_height, catalog_revision
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.CreatedAt.UTC().Format(timeFormat),
		entry.CharacterID,
		string(entry.RenderMode),
		entry.SceneVariantText,
		entry.SceneVariantEffective,
		entry.FinalPrompt,
		entry.Profile,
		entry.ImageID,
		entry.ImageSHA256,
		entry.ImageSizeBytes,
		entry.ImageContentType,
		entry.ImageWidth,
		entry.ImageHeight,
		entry.CatalogRevision,
	)
	if err != nil {
		return fmt.Errorf("save CharImage history: %w", err)
	}
	return nil
}

func (s *SQLiteStore) List(ctx context.Context, limit int) ([]domaincharimage.HistoryEntry, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("CharImage history sqlite store is closed")
	}
	if limit < 1 {
		return []domaincharimage.HistoryEntry{}, nil
	}
	if limit > maxHistory {
		limit = maxHistory
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, created_at, character_id, render_mode, scene_variant_text,
		scene_variant_effective, final_prompt, profile, image_id, image_sha256,
		image_size_bytes, image_content_type, image_width, image_height, catalog_revision
		FROM charimage_history ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read CharImage history: %w", err)
	}
	defer rows.Close()
	items := make([]domaincharimage.HistoryEntry, 0, limit)
	for rows.Next() {
		var entry domaincharimage.HistoryEntry
		var createdAt, renderMode string
		if err := rows.Scan(
			&entry.ID, &createdAt, &entry.CharacterID, &renderMode, &entry.SceneVariantText,
			&entry.SceneVariantEffective, &entry.FinalPrompt, &entry.Profile, &entry.ImageID, &entry.ImageSHA256,
			&entry.ImageSizeBytes, &entry.ImageContentType, &entry.ImageWidth, &entry.ImageHeight, &entry.CatalogRevision,
		); err != nil {
			return nil, fmt.Errorf("scan CharImage history: %w", err)
		}
		entry.RenderMode = domaincharimage.RenderMode(renderMode)
		entry.CreatedAt, err = time.Parse(timeFormat, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse CharImage history timestamp: %w", err)
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read CharImage history rows: %w", err)
	}
	return items, nil
}

func (s *SQLiteStore) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read CharImage history schema version: %w", err)
	}
	if version < 0 || version > schemaVersion {
		return fmt.Errorf("CharImage history schema version %d is unsupported (current %d)", version, schemaVersion)
	}
	if version == schemaVersion {
		return s.validateSchema()
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin CharImage history migration: %w", err)
	}
	defer tx.Rollback()
	if version == 0 {
		if _, err := tx.Exec(`CREATE TABLE charimage_history (
			id TEXT PRIMARY KEY NOT NULL,
			created_at TEXT NOT NULL,
			character_id TEXT NOT NULL,
			render_mode TEXT NOT NULL CHECK (render_mode IN ('2d', '3d')),
			scene_variant_text TEXT NOT NULL,
			scene_variant_effective TEXT NOT NULL,
			final_prompt TEXT NOT NULL,
			profile TEXT NOT NULL,
			image_id TEXT NOT NULL,
			image_sha256 TEXT NOT NULL,
			image_size_bytes INTEGER NOT NULL CHECK (image_size_bytes > 0),
			image_content_type TEXT NOT NULL,
			image_width INTEGER NOT NULL CHECK (image_width > 0),
			image_height INTEGER NOT NULL CHECK (image_height > 0),
			catalog_revision TEXT NOT NULL
		)`); err != nil {
			return fmt.Errorf("create CharImage history schema: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX charimage_history_created_at_idx ON charimage_history(created_at DESC)`); err != nil {
			return fmt.Errorf("create CharImage history index: %w", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("write CharImage history schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit CharImage history migration: %w", err)
	}
	return s.validateSchema()
}

func (s *SQLiteStore) validateSchema() error {
	var integrity string
	if err := s.db.QueryRow("PRAGMA quick_check(1)").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("CharImage history database integrity check failed: %s", integrity)
	}
	type columnSpec struct {
		name       string
		typeName   string
		primaryKey int
	}
	expected := []columnSpec{
		{name: "id", typeName: "TEXT", primaryKey: 1},
		{name: "created_at", typeName: "TEXT"},
		{name: "character_id", typeName: "TEXT"},
		{name: "render_mode", typeName: "TEXT"},
		{name: "scene_variant_text", typeName: "TEXT"},
		{name: "scene_variant_effective", typeName: "TEXT"},
		{name: "final_prompt", typeName: "TEXT"},
		{name: "profile", typeName: "TEXT"},
		{name: "image_id", typeName: "TEXT"},
		{name: "image_sha256", typeName: "TEXT"},
		{name: "image_size_bytes", typeName: "INTEGER"},
		{name: "image_content_type", typeName: "TEXT"},
		{name: "image_width", typeName: "INTEGER"},
		{name: "image_height", typeName: "INTEGER"},
		{name: "catalog_revision", typeName: "TEXT"},
	}
	rows, err := s.db.Query("PRAGMA table_info(charimage_history)")
	if err != nil {
		return fmt.Errorf("inspect CharImage history schema: %w", err)
	}
	index := 0
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typeName string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read CharImage history schema: %w", err)
		}
		if index >= len(expected) || name != expected[index].name || strings.ToUpper(typeName) != expected[index].typeName || primaryKey != expected[index].primaryKey || notNull != 1 {
			_ = rows.Close()
			return fmt.Errorf("CharImage history schema does not match revision %d", schemaVersion)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read CharImage history schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close CharImage history schema inspection: %w", err)
	}
	if index != len(expected) {
		return fmt.Errorf("CharImage history schema is incomplete: found %d of %d columns", index, len(expected))
	}
	var indexCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='charimage_history_created_at_idx' AND tbl_name='charimage_history'`).Scan(&indexCount); err != nil || indexCount != 1 {
		return fmt.Errorf("CharImage history timestamp index is missing")
	}
	var rowCount int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM charimage_history").Scan(&rowCount); err != nil {
		return fmt.Errorf("read CharImage history rows: %w", err)
	}
	items, err := s.List(context.Background(), maxHistory)
	if err != nil {
		return fmt.Errorf("read CharImage history sample: %w", err)
	}
	wantItems := rowCount
	if wantItems > maxHistory {
		wantItems = maxHistory
	}
	if wantItems < 0 || int64(len(items)) != wantItems {
		return fmt.Errorf("CharImage history sample count does not match readback")
	}
	for _, item := range items {
		if err := validateEntry(item); err != nil {
			return fmt.Errorf("CharImage history sample is invalid: %w", err)
		}
	}
	return nil
}

func sqliteDatabaseURI(path string) string {
	query := url.Values{}
	query.Set("_pragma", "busy_timeout=5000")
	query.Set("_time_format", "sqlite")
	slashPath := filepath.ToSlash(path)
	uri := url.URL{Scheme: "file", RawQuery: query.Encode()}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(slashPath, "//") {
			unc := strings.TrimPrefix(slashPath, "//")
			if host, filePath, ok := strings.Cut(unc, "/"); ok {
				uri.Host = host
				uri.Path = "/" + filePath
			} else {
				uri.Path = "/" + unc
			}
		} else {
			uri.Path = "/" + slashPath
		}
	} else {
		uri.Path = slashPath
	}
	return uri.String()
}

func validateEntry(entry domaincharimage.HistoryEntry) error {
	digest, err := hex.DecodeString(entry.ImageSHA256)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != entry.ImageSHA256 {
		return fmt.Errorf("CharImage history image hash is invalid")
	}
	if !historyImageIDPattern.MatchString(entry.ID) || entry.ImageID != entry.ID ||
		entry.CreatedAt.IsZero() || !validCharacterID(entry.CharacterID) ||
		(entry.RenderMode != domaincharimage.RenderMode2D && entry.RenderMode != domaincharimage.RenderMode3D) ||
		entry.FinalPrompt == "" || entry.SceneVariantEffective == "" ||
		entry.Profile != domaincharimage.ModelProfileID || entry.ImageContentType != "image/png" ||
		entry.ImageSizeBytes <= 0 || entry.ImageWidth <= 0 || entry.ImageHeight <= 0 ||
		!strings.HasPrefix(entry.CatalogRevision, "sha256:") {
		return fmt.Errorf("CharImage history entry is invalid")
	}
	return nil
}

func validCharacterID(id string) bool {
	if len(id) < 4 || len(id) > 32 || id[2] != '_' {
		return false
	}
	for i, r := range id {
		if i == 2 {
			continue
		}
		if i < 2 {
			if r < '0' || r > '9' {
				return false
			}
		} else if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
