package dcimigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	maxJSONLBytes    = int64(16 << 20)
	maxJSONLLine     = 256 << 10
	maxJSONLRecords  = 10000
	maxManifestBytes = int64(1 << 20)
	maxActorLabels   = 64
	maxActorLabel    = 128
)

var sqliteSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

type codedError struct {
	code string
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }

func (e *codedError) Unwrap() error { return e.err }

func newCodedError(code, format string, args ...any) error {
	return &codedError{code: code, err: fmt.Errorf(format, args...)}
}

func errorCode(err error, fallback string) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context_canceled"
	}
	var coded *codedError
	if errors.As(err, &coded) && coded.code != "" {
		return coded.code
	}
	return fallback
}

func validateOptions(options Options) error {
	if strings.TrimSpace(options.SnapshotDir) == "" {
		return newCodedError("invalid_options", "--snapshot-dir is required")
	}
	paths := []string{options.SourceDCI, options.SourceDCIJSONL, options.SourceEventStore, options.SourceL1, options.SourceArchive}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return newCodedError("invalid_options", "all five DCI snapshot source paths are required")
		}
	}
	if strings.TrimSpace(options.Manifest) == "" {
		return newCodedError("invalid_options", "--manifest is required")
	}
	if err := validateExpectedCounts(options.Expected); err != nil {
		return err
	}
	if len(options.AgentIDs) == 0 {
		return newCodedError("invalid_options", "canonical agent ID set is required")
	}
	seen := make(map[string]struct{}, len(options.AgentIDs))
	for _, id := range options.AgentIDs {
		if id == "" || strings.TrimSpace(id) != id {
			return newCodedError("invalid_options", "canonical agent IDs must be nonblank and canonical")
		}
		if _, exists := seen[id]; exists {
			return newCodedError("invalid_options", "canonical agent ID set contains a duplicate")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateExpectedCounts(expected ExpectedCounts) error {
	if expected.Searches < 0 || expected.ReadEvents < 0 || expected.EvidenceEvents < 0 || expected.TotalEvents < 0 || expected.LegacyLimitSteps < 0 || expected.NormalizedTextValues < 0 || expected.InvalidUTF8Bytes < 0 {
		return newCodedError("invalid_options", "expected counts must be non-negative")
	}
	return nil
}

func resolvePaths(options Options) (sourcePaths, error) {
	root, err := absolutePath(options.SnapshotDir)
	if err != nil {
		return sourcePaths{}, newCodedError("unsafe_path", "resolve snapshot directory: %v", err)
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return sourcePaths{}, newCodedError("unsafe_path", "snapshot directory is missing or unsafe")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return sourcePaths{}, newCodedError("unsafe_path", "resolve snapshot directory: %v", err)
	}
	realRoot = filepath.Clean(realRoot)

	resolveSource := func(raw string, sqlite bool) (string, error) {
		path, err := resolveRelativeOrAbsolute(realRoot, raw)
		if err != nil {
			return "", newCodedError("unsafe_path", "resolve source path: %v", err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", newCodedError("unsafe_path", "source is missing or not a regular non-symlink file")
		}
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", newCodedError("unsafe_path", "resolve source path: %v", err)
		}
		realPath = filepath.Clean(realPath)
		if !pathWithin(realRoot, realPath) {
			return "", newCodedError("unsafe_path", "source is outside the snapshot directory")
		}
		if !samePath(filepath.Dir(realPath), realRoot) {
			return "", newCodedError("unsafe_path", "snapshot sources must be direct children of the dedicated offline snapshot directory")
		}
		if sqlite {
			if err := rejectSQLiteSidecars(realPath); err != nil {
				return "", err
			}
		}
		return realPath, nil
	}

	dci, err := resolveSource(options.SourceDCI, true)
	if err != nil {
		return sourcePaths{}, err
	}
	jsonl, err := resolveSource(options.SourceDCIJSONL, false)
	if err != nil {
		return sourcePaths{}, err
	}
	eventStore, err := resolveSource(options.SourceEventStore, true)
	if err != nil {
		return sourcePaths{}, err
	}
	l1, err := resolveSource(options.SourceL1, true)
	if err != nil {
		return sourcePaths{}, err
	}
	archive, err := resolveSource(options.SourceArchive, true)
	if err != nil {
		return sourcePaths{}, err
	}
	manifest, err := resolveOutputPath(realRoot, options.Manifest)
	if err != nil {
		return sourcePaths{}, newCodedError("unsafe_path", "resolve manifest path: %v", err)
	}
	all := []string{dci, jsonl, eventStore, l1, archive}
	for _, source := range all {
		if samePath(source, manifest) {
			return sourcePaths{}, newCodedError("unsafe_path", "manifest must not replace a source snapshot")
		}
	}
	for left := 0; left < len(all); left++ {
		for right := left + 1; right < len(all); right++ {
			if samePath(all[left], all[right]) {
				return sourcePaths{}, newCodedError("unsafe_path", "snapshot source paths must be distinct")
			}
		}
	}
	return sourcePaths{root: realRoot, dci: dci, dciJSONL: jsonl, eventStore: eventStore, l1: l1, archive: archive, manifest: manifest}, nil
}

func absolutePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("path is invalid")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func resolveRelativeOrAbsolute(root, raw string) (string, error) {
	if filepath.IsAbs(raw) {
		return absolutePath(raw)
	}
	return absolutePath(filepath.Join(root, raw))
}

func resolveOutputPath(root, raw string) (string, error) {
	path, err := resolveRelativeOrAbsolute(root, raw)
	if err != nil {
		return "", err
	}
	if !pathWithinOrRoot(root, path) {
		return "", fmt.Errorf("manifest must be inside snapshot directory")
	}
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("manifest path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return "", fmt.Errorf("manifest parent is missing or unsafe")
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !pathWithinOrRoot(root, filepath.Clean(realParent)) {
		return "", fmt.Errorf("manifest parent is outside snapshot directory")
	}
	return path, nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return false
	}
	parent := ".." + string(filepath.Separator)
	return relative != ".." && !strings.HasPrefix(relative, parent)
}

func pathWithinOrRoot(root, target string) bool {
	return samePath(root, target) || pathWithin(root, target)
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	return strings.EqualFold(left, right) && filepath.VolumeName(left) != ""
}

func rejectSQLiteSidecars(path string) error {
	base := filepath.Base(path)
	for _, suffix := range sqliteSidecarSuffixes {
		if strings.HasSuffix(base, suffix) {
			return newCodedError("unsafe_path", "SQLite source path is a sidecar")
		}
		candidate := path + suffix
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return newCodedError("unsafe_path", "SQLite source sidecar is unsafe")
		}
		return newCodedError("unsafe_path", "SQLite source has a %s sidecar", suffix)
	}
	return nil
}

func openSQLiteReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dsn := sqliteReadOnlyDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	var queryOnly int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		_ = db.Close()
		return nil, err
	}
	if queryOnly != 1 {
		_ = db.Close()
		return nil, fmt.Errorf("SQLite source is not query-only")
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func sqliteReadOnlyDSN(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout%3d5000"}).String()
}

func fileSHA256(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("source file is missing or not regular")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashCanonicalLines(lines []string) string {
	copyLines := append([]string(nil), lines...)
	sort.Strings(copyLines)
	hash := sha256.New()
	for _, line := range copyLines {
		_, _ = io.WriteString(hash, line)
		_, _ = io.WriteString(hash, "\n")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type tableColumn struct {
	CID        int
	Name       string
	Type       string
	NotNull    int
	PrimaryKey int
}

type tableSpec struct {
	Columns []tableColumnSpec
}

type tableColumnSpec struct {
	Name    string
	Type    string
	NotNull bool
	Primary bool
}

func schemaUserTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func requireTableSet(actual []string, expected []string, exact bool) error {
	set := make(map[string]struct{}, len(actual))
	for _, name := range actual {
		set[name] = struct{}{}
	}
	for _, name := range expected {
		if _, ok := set[name]; !ok {
			return fmt.Errorf("required SQLite table %s is missing", name)
		}
	}
	if exact && len(actual) != len(expected) {
		return fmt.Errorf("SQLite tables do not match required schema")
	}
	return nil
}

func inspectTable(ctx context.Context, db *sql.DB, table string, spec tableSpec) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info('"+strings.ReplaceAll(table, "'", "''")+"')")
	if err != nil {
		return err
	}
	defer rows.Close()
	actual := make([]tableColumn, 0, len(spec.Columns))
	for rows.Next() {
		var column tableColumn
		var defaultValue any
		if err := rows.Scan(&column.CID, &column.Name, &column.Type, &column.NotNull, &defaultValue, &column.PrimaryKey); err != nil {
			return err
		}
		actual = append(actual, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(actual) != len(spec.Columns) {
		return fmt.Errorf("SQLite table %s columns do not match required schema", table)
	}
	for index, expected := range spec.Columns {
		column := actual[index]
		if column.CID != index || column.Name != expected.Name || strings.ToUpper(strings.TrimSpace(column.Type)) != strings.ToUpper(expected.Type) {
			return fmt.Errorf("SQLite table %s column %s does not match required declaration", table, expected.Name)
		}
		if expected.NotNull && column.NotNull != 1 && column.PrimaryKey != 1 {
			return fmt.Errorf("SQLite table %s column %s must be NOT NULL", table, expected.Name)
		}
		if !expected.NotNull && column.NotNull != 0 && column.PrimaryKey == 0 {
			return fmt.Errorf("SQLite table %s column %s must remain nullable", table, expected.Name)
		}
		if expected.Primary && column.PrimaryKey != 1 {
			return fmt.Errorf("SQLite table %s column %s must be the primary key", table, expected.Name)
		}
		if !expected.Primary && column.PrimaryKey != 0 {
			return fmt.Errorf("SQLite table %s has unexpected primary key column %s", table, column.Name)
		}
	}
	return nil
}

func checkUserVersion(ctx context.Context, db *sql.DB, want int) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != want {
		return fmt.Errorf("SQLite schema version %d is not the required version %d", version, want)
	}
	return nil
}

func readText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func readInt(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < float64(-1<<63) || typed >= float64(1<<63) {
			return 0, fmt.Errorf("integer value is not a bounded integer")
		}
		return int64(typed), nil
	case float32:
		return readInt(float64(typed))
	case []byte:
		return parseIntString(string(typed))
	case string:
		return parseIntString(typed)
	case nil:
		return 0, fmt.Errorf("integer value is NULL")
	default:
		return 0, fmt.Errorf("integer value has type %T", value)
	}
}

func parseIntString(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

func readFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case []byte:
		return parseFloatString(string(typed))
	case string:
		return parseFloatString(typed)
	case nil:
		return 0, fmt.Errorf("float value is NULL")
	default:
		return 0, fmt.Errorf("float value has type %T", value)
	}
}

func parseFloatString(raw string) (float64, error) {
	var value float64
	if _, err := fmt.Sscan(strings.TrimSpace(raw), &value); err != nil {
		return 0, err
	}
	return value, nil
}
