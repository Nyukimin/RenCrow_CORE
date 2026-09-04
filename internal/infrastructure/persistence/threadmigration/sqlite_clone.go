package threadmigration

// This file owns the physical boundary between an exact legacy SQLite file
// and a disposable migration clone.  It deliberately does not know the
// inventory or materialization contracts: the caller supplies paths, and the
// only output is a path-free bounded receipt.  Source is opened read-only and
// is never written by this operation.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	sqlite "modernc.org/sqlite"
)

const (
	SQLiteCloneReceiptSchemaVersion = "rencrow.threadmigration.sqlite_clone.v1"
	SQLiteCloneMethod               = "sqlite_backup"
	SQLiteCloneQuickCheckOK         = "ok"
)

var sqliteCloneSidecarSuffixes = [...]string{"-wal", "-shm", "-journal"}

// SQLiteCloneInput identifies one exact legacy SQLite file and one new
// disposable destination.  The caller owns both paths and remains
// responsible for closing any database handles it opened elsewhere.
type SQLiteCloneInput struct {
	SourcePath      string
	DestinationPath string
}

// SQLiteCloneReceipt is the bounded physical evidence returned after a
// successful clone.  ReceiptSHA256 is computed over CanonicalJSON, which
// intentionally excludes the self-referential receipt hash.
type SQLiteCloneReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Method        string `json:"method"`
	PageCount     int64  `json:"page_count"`
	SourceSHA256  string `json:"source_sha256"`
	OutputSHA256  string `json:"output_sha256"`
	Bytes         int64  `json:"bytes"`
	QuickCheck    string `json:"quick_check"`
	SidecarZero   int    `json:"sidecar_zero"`
	ReceiptSHA256 string `json:"receipt_sha256"`
}

// SQLiteCloneError is the bounded failure boundary for CloneSQLite.  When
// PostOutputUnusable is true, the destination path was created or otherwise
// touched and must be discarded by the caller; no rollback is implied.
type SQLiteCloneError struct {
	Code               string
	Phase              string
	PostOutputUnusable bool
	cause              error
}

func (err *SQLiteCloneError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.PostOutputUnusable {
		return fmt.Sprintf("SQLite clone %s failed after output creation; destination is unusable", err.Code)
	}
	return fmt.Sprintf("SQLite clone %s failed during %s", err.Code, err.Phase)
}

func (err *SQLiteCloneError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

type sqliteClonePaths struct {
	source      string
	destination string
}

// These seams keep the production path on the modernc Backup API while
// allowing bounded failure tests without introducing a second database
// driver or a fake filesystem.
var sqliteCloneBackupStep = func(backup *sqlite.Backup, pages int32) (bool, error) {
	return backup.Step(pages)
}

var sqliteCloneBackupFinish = func(backup *sqlite.Backup) error {
	return backup.Finish()
}

// CloneSQLite creates one physical SQLite clone using bounded Backup.Step
// calls.  It never closes a caller-owned handle because it opens all handles
// internally from the explicit source and destination paths.
func CloneSQLite(ctx context.Context, input SQLiteCloneInput) (SQLiteCloneReceipt, error) {
	if ctx == nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("invalid_input", "preflight", false, errors.New("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("context_canceled", "preflight", false, err)
	}
	paths, err := validateSQLiteClonePaths(ctx, input)
	if err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("preflight", "preflight", false, err)
	}

	sourceSHA256, _, err := hashSQLiteCloneFile(ctx, paths.source)
	if err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("source_hash", "preflight", false, err)
	}
	if err := rejectSQLiteCloneSidecars(paths.source, true); err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("source_sidecar", "preflight", false, err)
	}

	sourceDB, err := openSQLiteCloneReadOnly(ctx, paths.source)
	if err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("source_open", "preflight", false, err)
	}
	closeSource := func() error {
		return sourceDB.Close()
	}

	created, err := reserveSQLiteCloneDestination(paths.destination)
	if err != nil {
		closeErr := closeSource()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return SQLiteCloneReceipt{}, newSQLiteCloneError("destination_create", "preflight", created, err)
	}
	outputStarted := true

	pageCount, backupErr := runSQLiteCloneBackup(ctx, sourceDB, paths.destination)
	closeErr := closeSource()
	if backupErr != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError(cloneErrorCode(backupErr, "backup"), "backup", outputStarted, backupErr)
	}
	if closeErr != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("source_close", "backup", outputStarted, closeErr)
	}
	if err := ctx.Err(); err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("context_canceled", "backup", outputStarted, err)
	}

	if err := rejectSQLiteCloneSidecars(paths.source, true); err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("source_changed", "backup", outputStarted, err)
	}
	sourceAfter, _, err := hashSQLiteCloneFile(ctx, paths.source)
	if err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("source_changed", "backup", outputStarted, err)
	}
	if sourceAfter != sourceSHA256 {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("source_changed", "backup", outputStarted, errors.New("source hash changed during backup"))
	}
	if err := rejectSQLiteCloneSidecars(paths.source, true); err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("source_changed", "backup", outputStarted, err)
	}

	if err := finalizeSQLiteClone(ctx, paths.destination); err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError(cloneErrorCode(err, "output_validation"), "output", outputStarted, err)
	}
	outputSHA256, outputBytes, err := hashSQLiteCloneFile(ctx, paths.destination)
	if err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("output_hash", "output", outputStarted, err)
	}
	if err := rejectSQLiteCloneSidecars(paths.destination, false); err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("output_sidecar", "output", outputStarted, err)
	}

	receipt := SQLiteCloneReceipt{
		SchemaVersion: SQLiteCloneReceiptSchemaVersion,
		Method:        SQLiteCloneMethod,
		PageCount:     pageCount,
		SourceSHA256:  sourceSHA256,
		OutputSHA256:  outputSHA256,
		Bytes:         outputBytes,
		QuickCheck:    SQLiteCloneQuickCheckOK,
		SidecarZero:   0,
	}
	receiptHash, err := receipt.ComputeSHA256()
	if err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("receipt_hash", "receipt", outputStarted, err)
	}
	receipt.ReceiptSHA256 = receiptHash
	if err := receipt.Validate(); err != nil {
		return SQLiteCloneReceipt{}, newSQLiteCloneError("receipt_validation", "receipt", outputStarted, err)
	}
	return receipt, nil
}

func newSQLiteCloneError(code, phase string, postOutputUnusable bool, cause error) error {
	if code == "" {
		code = "clone"
	}
	if phase == "" {
		phase = "operation"
	}
	return &SQLiteCloneError{Code: code, Phase: phase, PostOutputUnusable: postOutputUnusable, cause: cause}
}

func cloneErrorCode(err error, fallback string) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context_canceled"
	}
	var typed *SQLiteCloneError
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	if fallback == "" {
		return "clone"
	}
	return fallback
}

func validateSQLiteClonePaths(ctx context.Context, input SQLiteCloneInput) (sqliteClonePaths, error) {
	if strings.TrimSpace(input.SourcePath) == "" || strings.TrimSpace(input.DestinationPath) == "" {
		return sqliteClonePaths{}, errors.New("source and destination paths are required")
	}
	if err := ctx.Err(); err != nil {
		return sqliteClonePaths{}, err
	}

	source, err := filepath.Abs(input.SourcePath)
	if err != nil {
		return sqliteClonePaths{}, errors.New("source path cannot be resolved")
	}
	source = filepath.Clean(source)
	if err := rejectSQLiteCloneSidecars(source, true); err != nil {
		return sqliteClonePaths{}, err
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil || sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
		return sqliteClonePaths{}, errors.New("source must be an existing regular non-symlink file")
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return sqliteClonePaths{}, errors.New("source path cannot be canonicalized")
	}
	resolvedSource = filepath.Clean(resolvedSource)
	resolvedSourceInfo, err := os.Lstat(resolvedSource)
	if err != nil || resolvedSourceInfo.Mode()&os.ModeSymlink != 0 || !resolvedSourceInfo.Mode().IsRegular() {
		return sqliteClonePaths{}, errors.New("resolved source is not a regular non-symlink file")
	}

	destination, err := filepath.Abs(input.DestinationPath)
	if err != nil {
		return sqliteClonePaths{}, errors.New("destination path cannot be resolved")
	}
	destination = filepath.Clean(destination)
	if err := rejectSQLiteCloneSidecars(destination, false); err != nil {
		return sqliteClonePaths{}, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return sqliteClonePaths{}, errors.New("destination must not already exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return sqliteClonePaths{}, errors.New("destination path cannot be inspected")
	}
	parent := filepath.Dir(destination)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return sqliteClonePaths{}, errors.New("destination parent must be an existing real directory")
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !sameSQLiteClonePath(parent, filepath.Clean(resolvedParent)) {
		return sqliteClonePaths{}, errors.New("destination parent must be canonical")
	}
	destination = filepath.Join(filepath.Clean(resolvedParent), filepath.Base(destination))
	if _, err := os.Lstat(destination); err == nil {
		return sqliteClonePaths{}, errors.New("destination must not already exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return sqliteClonePaths{}, errors.New("destination path cannot be inspected")
	}
	if err := rejectSQLiteCloneSidecars(destination, false); err != nil {
		return sqliteClonePaths{}, err
	}
	if sameSQLiteClonePath(resolvedSource, destination) {
		return sqliteClonePaths{}, errors.New("source and destination must be distinct")
	}
	return sqliteClonePaths{source: resolvedSource, destination: destination}, nil
}

func sameSQLiteClonePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	return strings.EqualFold(left, right) && filepath.VolumeName(left) != ""
}

func rejectSQLiteCloneSidecars(path string, source bool) error {
	base := filepath.Base(path)
	for _, suffix := range sqliteCloneSidecarSuffixes {
		if strings.HasSuffix(strings.ToLower(base), suffix) {
			return errors.New("SQLite path uses a reserved sidecar suffix")
		}
		candidate := path + suffix
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("SQLite sidecar path cannot be inspected")
		}
		if source {
			return errors.New("SQLite source sidecar exists")
		}
		return errors.New("SQLite destination sidecar already exists")
	}
	return nil
}

func reserveSQLiteCloneDestination(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return true, err
	}
	return true, file.Close()
}

func openSQLiteCloneReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteCloneDSN(path, "mode=ro&_pragma=busy_timeout%3d5000"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	var queryOnly int64
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		_ = db.Close()
		return nil, err
	}
	if queryOnly != 1 {
		_ = db.Close()
		return nil, errors.New("SQLite source query_only pragma is disabled")
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func sqliteCloneDSN(path, rawQuery string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: rawQuery}).String()
}

func runSQLiteCloneBackup(ctx context.Context, sourceDB *sql.DB, destination string) (int64, error) {
	conn, err := sourceDB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	var pageCount int64
	err = conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver does not expose the Backup API")
		}
		backup, err := backuper.NewBackup(sqliteCloneDSN(destination, ""))
		if err != nil {
			return err
		}
		var operationErr error
		for {
			if err := ctx.Err(); err != nil {
				operationErr = err
				break
			}
			more, err := sqliteCloneBackupStep(backup, 256)
			if err != nil {
				operationErr = err
				break
			}
			if err := ctx.Err(); err != nil {
				operationErr = err
				break
			}
			if more {
				continue
			}
			if remaining := backup.Remaining(); remaining != 0 {
				operationErr = errors.New("SQLite Backup completed with remaining pages")
				break
			}
			pageCount = int64(backup.PageCount())
			if pageCount <= 0 {
				operationErr = errors.New("SQLite Backup page count is invalid")
			}
			break
		}
		finishErr := sqliteCloneBackupFinish(backup)
		if operationErr != nil && finishErr != nil {
			return errors.Join(operationErr, finishErr)
		}
		if operationErr != nil {
			return operationErr
		}
		if finishErr != nil {
			return finishErr
		}
		return nil
	})
	connCloseErr := conn.Close()
	if err != nil {
		if connCloseErr != nil {
			return pageCount, errors.Join(err, connCloseErr)
		}
		return pageCount, err
	}
	if connCloseErr != nil {
		return pageCount, connCloseErr
	}
	return pageCount, nil
}

func finalizeSQLiteClone(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("SQLite output is not a regular non-symlink file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteCloneDSN(path, "_pragma=busy_timeout%3d5000"))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var mode string
	journalErr := db.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&mode)
	closeErr := db.Close()
	if journalErr != nil {
		return journalErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !strings.EqualFold(mode, "delete") {
		return errors.New("SQLite output journal mode is not DELETE")
	}
	if err := rejectSQLiteCloneSidecars(path, false); err != nil {
		return err
	}
	if err := syncSQLiteCloneFile(ctx, path); err != nil {
		return err
	}
	if err := rejectSQLiteCloneSidecars(path, false); err != nil {
		return err
	}
	db, err = openSQLiteCloneReadOnly(ctx, path)
	if err != nil {
		return err
	}
	quickErr := quickCheckSQLiteClone(ctx, db)
	closeErr = db.Close()
	if quickErr != nil {
		return quickErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := rejectSQLiteCloneSidecars(path, false); err != nil {
		return err
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		return errors.New("SQLite output file metadata is invalid")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return errors.New("SQLite output permissions are not 0600")
	}
	return nil
}

func quickCheckSQLiteClone(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	count := 0
	result := ""
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		var value string
		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()
			return err
		}
		count++
		if count == 1 {
			result = value
		}
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	if count != 1 || result != SQLiteCloneQuickCheckOK {
		return errors.New("SQLite quick_check did not return exactly one ok")
	}
	return nil
}

func syncSQLiteCloneFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func hashSQLiteCloneFile(ctx context.Context, path string) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, errors.New("SQLite file is not a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return "", total, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if _, err := hash.Write(buffer[:read]); err != nil {
				_ = file.Close()
				return "", total, err
			}
			total += int64(read)
			if total < 0 {
				_ = file.Close()
				return "", 0, errors.New("SQLite file size overflow")
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return "", total, readErr
		}
	}
	closeErr := file.Close()
	if closeErr != nil {
		return "", total, closeErr
	}
	if total <= 0 {
		return "", total, errors.New("SQLite file is empty")
	}
	digest := hash.Sum(nil)
	return hex.EncodeToString(digest), total, nil
}

func (receipt SQLiteCloneReceipt) CanonicalJSON() ([]byte, error) {
	type canonicalSQLiteCloneReceipt struct {
		SchemaVersion string `json:"schema_version"`
		Method        string `json:"method"`
		PageCount     int64  `json:"page_count"`
		SourceSHA256  string `json:"source_sha256"`
		OutputSHA256  string `json:"output_sha256"`
		Bytes         int64  `json:"bytes"`
		QuickCheck    string `json:"quick_check"`
		SidecarZero   int    `json:"sidecar_zero"`
	}
	return json.Marshal(canonicalSQLiteCloneReceipt{
		SchemaVersion: receipt.SchemaVersion,
		Method:        receipt.Method,
		PageCount:     receipt.PageCount,
		SourceSHA256:  receipt.SourceSHA256,
		OutputSHA256:  receipt.OutputSHA256,
		Bytes:         receipt.Bytes,
		QuickCheck:    receipt.QuickCheck,
		SidecarZero:   receipt.SidecarZero,
	})
}

func (receipt SQLiteCloneReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (receipt SQLiteCloneReceipt) Validate() error {
	if receipt.SchemaVersion != SQLiteCloneReceiptSchemaVersion || receipt.Method != SQLiteCloneMethod {
		return errors.New("SQLite clone receipt schema or method is invalid")
	}
	if receipt.PageCount <= 0 || receipt.Bytes <= 0 {
		return errors.New("SQLite clone receipt size evidence is invalid")
	}
	if err := validateSQLiteCloneSHA256(receipt.SourceSHA256, "SQLite clone source SHA256"); err != nil {
		return err
	}
	if err := validateSQLiteCloneSHA256(receipt.OutputSHA256, "SQLite clone output SHA256"); err != nil {
		return err
	}
	if receipt.QuickCheck != SQLiteCloneQuickCheckOK || receipt.SidecarZero != 0 {
		return errors.New("SQLite clone receipt integrity evidence is invalid")
	}
	if err := validateSQLiteCloneSHA256(receipt.ReceiptSHA256, "SQLite clone receipt SHA256"); err != nil {
		return err
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return err
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("SQLite clone receipt SHA256 does not match canonical JSON")
	}
	return nil
}

func validateSQLiteCloneSHA256(value, label string) error {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return fmt.Errorf("%s is not lowercase hexadecimal SHA-256", label)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s is not lowercase hexadecimal SHA-256", label)
	}
	return nil
}
