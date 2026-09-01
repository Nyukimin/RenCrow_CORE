package dcimigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	sqlite "modernc.org/sqlite"
)

const maxCaptureManifestBytes = int64(64 << 10)

// CaptureOptions describes the five explicit live sources and the new,
// non-existing directory that will receive an offline snapshot.
type CaptureOptions struct {
	SnapshotDir    string
	LiveDCI        string
	LiveDCIJSONL   string
	LiveEventStore string
	LiveL1         string
	LiveArchive    string
}

// CaptureArtifact is the bounded physical evidence for one captured source.
// SQLite-only fields are pointers so JSONL artifacts omit them while SQLite
// artifacts retain an explicit zero sidecar count.
type CaptureArtifact struct {
	Method      string `json:"method"`
	FileSHA256  string `json:"file_sha256"`
	Bytes       int64  `json:"bytes"`
	PageCount   *int   `json:"page_count,omitempty"`
	QuickCheck  string `json:"quick_check,omitempty"`
	SidecarZero *int   `json:"sidecar_zero,omitempty"`
}

// CaptureReceipt is the path-free receipt for a physical offline snapshot.
// Logical, schema, and classification hashes remain owned by DryRun.
type CaptureReceipt struct {
	SchemaVersion     string                     `json:"schema_version"`
	Mode              string                     `json:"mode"`
	Status            string                     `json:"status"`
	StartedAt         time.Time                  `json:"started_at"`
	CompletedAt       time.Time                  `json:"completed_at"`
	Artifacts         map[string]CaptureArtifact `json:"artifacts"`
	ArtifactSetSHA256 string                     `json:"artifact_set_sha256"`
	ErrorCode         string                     `json:"error_code"`
}

type captureArtifactSpec struct {
	role     string
	filename string
	method   string
	sqlite   bool
}

var captureArtifactSpecs = []captureArtifactSpec{
	{role: "source_dci", filename: "source-dci", method: "sqlite_backup", sqlite: true},
	{role: "source_dci_jsonl", filename: "source-dci-jsonl", method: "byte_copy"},
	{role: "source_event_store", filename: "source-event-store", method: "sqlite_backup", sqlite: true},
	{role: "source_l1", filename: "source-l1", method: "sqlite_backup", sqlite: true},
	{role: "source_archive", filename: "source-archive", method: "sqlite_backup", sqlite: true},
}

type capturePaths struct {
	root    string
	sources map[string]string
}

// These narrow seams make backup progress and receipt durability failures
// testable without replacing the production sqlite driver or filesystem path
// policy.
var captureBackupStep = func(backup *sqlite.Backup, pages int32) (bool, error) {
	return backup.Step(pages)
}

var captureBackupFinish = func(backup *sqlite.Backup) error {
	return backup.Finish()
}

var captureJSONLSourceHash = fileSHA256

var captureReceiptWriter = writeCaptureReceipt

// Capture copies all five live sources into one new dedicated snapshot root.
// It never writes to a source or claims that writers were stopped.
func Capture(ctx context.Context, options CaptureOptions) (CaptureReceipt, error) {
	receipt := CaptureReceipt{
		SchemaVersion: CaptureSchemaVersion,
		Mode:          ModeCapture,
		Status:        StatusBlocked,
		StartedAt:     time.Now().UTC(),
		Artifacts:     make(map[string]CaptureArtifact),
	}
	if err := ctx.Err(); err != nil {
		return blockedCaptureReceipt(receipt, "", err)
	}
	paths, err := validateCaptureOptions(options)
	if err != nil {
		return blockedCaptureReceipt(receipt, "", err)
	}
	if err := os.Mkdir(paths.root, 0o700); err != nil {
		return blockedCaptureReceipt(receipt, "", newCodedError("unsafe_path", "create capture root"))
	}
	if err := os.Chmod(paths.root, 0o700); err != nil {
		return blockedCaptureReceipt(receipt, paths.root, newCodedError("capture_root", "set capture root permissions"))
	}

	for _, spec := range captureArtifactSpecs {
		if err := ctx.Err(); err != nil {
			return blockedCaptureReceipt(receipt, paths.root, err)
		}
		source := paths.sources[spec.role]
		destination := filepath.Join(paths.root, spec.filename)
		temporary, err := captureTemporaryPath(paths.root)
		if err != nil {
			return blockedCaptureReceipt(receipt, paths.root, err)
		}
		artifact, captureErr := captureArtifact(ctx, spec, source, temporary)
		if captureErr != nil {
			removeCaptureTemporary(temporary)
			return blockedCaptureReceipt(receipt, paths.root, captureErr)
		}
		if err := ctx.Err(); err != nil {
			removeCaptureTemporary(temporary)
			return blockedCaptureReceipt(receipt, paths.root, err)
		}
		if _, err := os.Lstat(destination); err == nil {
			removeCaptureTemporary(temporary)
			return blockedCaptureReceipt(receipt, paths.root, newCodedError("unsafe_path", "capture destination already exists"))
		} else if !errors.Is(err, os.ErrNotExist) {
			removeCaptureTemporary(temporary)
			return blockedCaptureReceipt(receipt, paths.root, newCodedError("capture_artifact", "inspect capture destination"))
		}
		if err := os.Rename(temporary, destination); err != nil {
			removeCaptureTemporary(temporary)
			return blockedCaptureReceipt(receipt, paths.root, newCodedError("capture_artifact", "install captured artifact"))
		}
		if err := syncDirectory(paths.root); err != nil {
			return blockedCaptureReceipt(receipt, paths.root, newCodedError("capture_sync", "sync capture directory"))
		}
		artifact, err = finalizeCaptureArtifact(destination, artifact)
		if err != nil {
			return blockedCaptureReceipt(receipt, paths.root, err)
		}
		receipt.Artifacts[spec.role] = artifact
	}

	if err := ctx.Err(); err != nil {
		return blockedCaptureReceipt(receipt, paths.root, err)
	}
	receipt.Status = StatusReady
	receipt.CompletedAt = time.Now().UTC()
	receipt.ArtifactSetSHA256 = captureArtifactSetSHA256(receipt.Artifacts)
	if err := captureReceiptWriter(filepath.Join(paths.root, CaptureReceiptFilename), receipt); err != nil {
		return blockedCaptureReceipt(receipt, paths.root, newCodedError("capture_receipt", "write capture receipt"))
	}
	return receipt, nil
}

func validateCaptureOptions(options CaptureOptions) (capturePaths, error) {
	if strings.TrimSpace(options.SnapshotDir) == "" {
		return capturePaths{}, newCodedError("invalid_options", "capture root is required")
	}
	root, err := absolutePath(options.SnapshotDir)
	if err != nil {
		return capturePaths{}, newCodedError("unsafe_path", "resolve capture root")
	}
	if _, err := os.Lstat(root); err == nil {
		return capturePaths{}, newCodedError("unsafe_path", "capture root must not exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return capturePaths{}, newCodedError("unsafe_path", "inspect capture root")
	}
	parent := filepath.Dir(root)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return capturePaths{}, newCodedError("unsafe_path", "capture root parent is missing or unsafe")
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !samePath(parent, filepath.Clean(realParent)) {
		return capturePaths{}, newCodedError("unsafe_path", "capture root parent is not canonical")
	}
	root = filepath.Join(filepath.Clean(realParent), filepath.Base(root))

	rawSources := map[string]string{
		"source_dci":         options.LiveDCI,
		"source_dci_jsonl":   options.LiveDCIJSONL,
		"source_event_store": options.LiveEventStore,
		"source_l1":          options.LiveL1,
		"source_archive":     options.LiveArchive,
	}
	resolved := make(map[string]string, len(captureArtifactSpecs))
	resolvedInfo := make(map[string]os.FileInfo, len(captureArtifactSpecs))
	for _, spec := range captureArtifactSpecs {
		if strings.TrimSpace(rawSources[spec.role]) == "" {
			return capturePaths{}, newCodedError("invalid_options", "all five live source paths are required")
		}
		source, err := resolveCaptureSource(rawSources[spec.role])
		if err != nil {
			return capturePaths{}, err
		}
		resolved[spec.role] = source
		info, err := os.Stat(source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return capturePaths{}, newCodedError("unsafe_path", "resolved live source is unsafe")
		}
		resolvedInfo[spec.role] = info
	}
	for left, leftSpec := range captureArtifactSpecs {
		for _, rightSpec := range captureArtifactSpecs[left+1:] {
			if samePath(resolved[leftSpec.role], resolved[rightSpec.role]) || os.SameFile(resolvedInfo[leftSpec.role], resolvedInfo[rightSpec.role]) {
				return capturePaths{}, newCodedError("unsafe_path", "live source paths must be distinct")
			}
		}
		for _, destinationSpec := range captureArtifactSpecs {
			if samePath(resolved[leftSpec.role], filepath.Join(root, destinationSpec.filename)) {
				return capturePaths{}, newCodedError("unsafe_path", "live source aliases a capture destination")
			}
		}
	}
	return capturePaths{root: root, sources: resolved}, nil
}

func resolveCaptureSource(raw string) (string, error) {
	path, err := absolutePath(raw)
	if err != nil {
		return "", newCodedError("unsafe_path", "resolve live source")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", newCodedError("unsafe_path", "live source is missing or unsafe")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", newCodedError("unsafe_path", "resolve live source")
	}
	realPath = filepath.Clean(realPath)
	realInfo, err := os.Lstat(realPath)
	if err != nil || realInfo.Mode()&os.ModeSymlink != 0 || !realInfo.Mode().IsRegular() {
		return "", newCodedError("unsafe_path", "resolved live source is unsafe")
	}
	return realPath, nil
}

func removeCaptureTemporary(path string) {
	_ = os.Remove(path)
	for _, suffix := range sqliteSidecarSuffixes {
		_ = os.Remove(path + suffix)
	}
}

func captureTemporaryPath(root string) (string, error) {
	temporary, err := os.CreateTemp(root, ".rencrow-capture-*.tmp")
	if err != nil {
		return "", newCodedError("capture_artifact", "create capture temporary path")
	}
	name := temporary.Name()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return "", newCodedError("capture_artifact", "set capture temporary permissions")
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return "", newCodedError("capture_artifact", "close capture temporary path")
	}
	if err := os.Remove(name); err != nil {
		return "", newCodedError("capture_artifact", "prepare capture temporary path")
	}
	return name, nil
}

func captureArtifact(ctx context.Context, spec captureArtifactSpec, source, destination string) (CaptureArtifact, error) {
	if spec.sqlite {
		return captureSQLiteBackup(ctx, source, destination)
	}
	return captureJSONL(ctx, source, destination)
}

func captureSQLiteBackup(ctx context.Context, source, destination string) (CaptureArtifact, error) {
	if err := ctx.Err(); err != nil {
		return CaptureArtifact{}, err
	}
	db, err := openSQLiteReadOnly(ctx, source)
	if err != nil {
		return CaptureArtifact{}, newCodedError("capture_source", "open live SQLite source")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return CaptureArtifact{}, newCodedError("capture_source", "pin live SQLite source")
	}
	var backup *sqlite.Backup
	var completed bool
	pageCount := 0
	rawErr := conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return newCodedError("capture_backup", "SQLite driver does not provide Backup API")
		}
		var err error
		backup, err = backuper.NewBackup(destination)
		if err != nil {
			return newCodedError("capture_backup", "create SQLite backup")
		}
		var operationErr error
		for {
			if err := ctx.Err(); err != nil {
				operationErr = err
				break
			}
			more, err := captureBackupStep(backup, 256)
			if err != nil {
				operationErr = newCodedError("capture_backup_step", "step SQLite backup")
				break
			}
			if err := ctx.Err(); err != nil {
				operationErr = err
				break
			}
			if more {
				continue
			}
			if backup.Remaining() != 0 {
				operationErr = newCodedError("capture_backup_incomplete", "SQLite backup has remaining pages")
				break
			}
			pageCount = backup.PageCount()
			if pageCount < 0 {
				operationErr = newCodedError("capture_backup", "SQLite backup page count is invalid")
				break
			}
			completed = true
			break
		}
		finishErr := captureBackupFinish(backup)
		if operationErr != nil && finishErr != nil {
			return errors.Join(operationErr, newCodedError("capture_backup_finish", "finish SQLite backup"))
		}
		if operationErr != nil {
			return operationErr
		}
		if finishErr != nil {
			return newCodedError("capture_backup_finish", "finish SQLite backup")
		}
		return nil
	})
	connCloseErr := conn.Close()
	dbCloseErr := db.Close()
	if rawErr != nil || connCloseErr != nil || dbCloseErr != nil {
		var closeErr error
		if connCloseErr != nil {
			closeErr = newCodedError("capture_source", "close pinned SQLite source")
		}
		if dbCloseErr != nil {
			closeErr = errors.Join(closeErr, newCodedError("capture_source", "close live SQLite source"))
		}
		return CaptureArtifact{}, errors.Join(rawErr, closeErr)
	}
	if !completed {
		return CaptureArtifact{}, newCodedError("capture_backup", "SQLite backup did not complete")
	}
	if err := ctx.Err(); err != nil {
		return CaptureArtifact{}, err
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return CaptureArtifact{}, newCodedError("capture_artifact", "set SQLite artifact permissions")
	}
	if err := normalizeCapturedSQLiteJournal(ctx, destination); err != nil {
		return CaptureArtifact{}, err
	}
	if err := syncCaptureFile(destination); err != nil {
		return CaptureArtifact{}, newCodedError("capture_sync", "sync SQLite artifact")
	}
	if err := rejectCapturedSQLiteSidecars(destination); err != nil {
		return CaptureArtifact{}, err
	}
	destinationDB, err := openSQLiteReadOnly(ctx, destination)
	if err != nil {
		return CaptureArtifact{}, newCodedError("capture_quick_check", "open captured SQLite artifact")
	}
	quickErr := captureSQLiteQuickCheck(ctx, destinationDB)
	closeErr := destinationDB.Close()
	if quickErr != nil || closeErr != nil {
		if closeErr != nil {
			closeErr = newCodedError("capture_quick_check", "close captured SQLite artifact")
		}
		return CaptureArtifact{}, errors.Join(quickErr, closeErr)
	}
	if err := rejectCapturedSQLiteSidecars(destination); err != nil {
		return CaptureArtifact{}, err
	}
	pageCountValue := pageCount
	sidecarZero := 0
	return CaptureArtifact{
		Method:      "sqlite_backup",
		PageCount:   &pageCountValue,
		QuickCheck:  "ok",
		SidecarZero: &sidecarZero,
	}, nil
}

func normalizeCapturedSQLiteJournal(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return newCodedError("capture_artifact", "open captured SQLite artifact for journal normalization")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var mode string
	queryErr := db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode)
	closeErr := db.Close()
	if queryErr != nil || closeErr != nil {
		if queryErr != nil {
			queryErr = newCodedError("capture_artifact", "normalize captured SQLite journal")
		}
		if closeErr != nil {
			closeErr = newCodedError("capture_artifact", "close captured SQLite artifact after journal normalization")
		}
		return errors.Join(queryErr, closeErr)
	}
	if !strings.EqualFold(mode, "delete") {
		return newCodedError("capture_artifact", "captured SQLite journal mode is not delete")
	}
	return nil
}

func captureSQLiteQuickCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return newCodedError("capture_quick_check", "run SQLite quick_check")
	}
	count := 0
	result := ""
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()
			return newCodedError("capture_quick_check", "read SQLite quick_check")
		}
		count++
		if count == 1 {
			result = value
		}
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil || closeErr != nil {
		return newCodedError("capture_quick_check", "finish SQLite quick_check")
	}
	if count != 1 || result != "ok" {
		return newCodedError("capture_quick_check", "SQLite quick_check did not return ok")
	}
	return nil
}

func captureJSONL(ctx context.Context, source, destination string) (CaptureArtifact, error) {
	if err := ctx.Err(); err != nil {
		return CaptureArtifact{}, err
	}
	info, err := os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return CaptureArtifact{}, newCodedError("unsafe_path", "live JSONL source is unsafe")
	}
	if info.Size() > maxJSONLBytes {
		return CaptureArtifact{}, newCodedError("oversized_jsonl", "live JSONL source exceeds the size bound")
	}
	before, err := captureJSONLSourceHash(source)
	if err != nil {
		return CaptureArtifact{}, newCodedError("capture_source", "hash live JSONL source")
	}
	in, err := os.Open(source)
	if err != nil {
		return CaptureArtifact{}, newCodedError("capture_source", "open live JSONL source")
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = in.Close()
		return CaptureArtifact{}, newCodedError("capture_artifact", "create JSONL artifact")
	}
	copyBytes, copyErr := copyCaptureBytes(ctx, out, in, maxJSONLBytes)
	inCloseErr := in.Close()
	syncErr := out.Sync()
	outCloseErr := out.Close()
	if copyErr != nil || inCloseErr != nil || syncErr != nil || outCloseErr != nil {
		return CaptureArtifact{}, errors.Join(copyErr, inCloseErr, syncErr, outCloseErr)
	}
	if copyBytes > maxJSONLBytes {
		return CaptureArtifact{}, newCodedError("oversized_jsonl", "live JSONL source exceeds the size bound")
	}
	after, err := captureJSONLSourceHash(source)
	if err != nil || after != before {
		return CaptureArtifact{}, newCodedError("source_changed", "live JSONL source changed during capture")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		return CaptureArtifact{}, newCodedError("capture_artifact", "read captured JSONL artifact")
	}
	if int64(len(data)) > maxJSONLBytes || !utf8.Valid(data) {
		return CaptureArtifact{}, newCodedError("malformed_jsonl", "captured JSONL is invalid")
	}
	if _, _, _, _, err := loadLegacyJSONL(ctx, destination); err != nil {
		return CaptureArtifact{}, err
	}
	return CaptureArtifact{Method: "byte_copy"}, nil
}

func copyCaptureBytes(ctx context.Context, destination io.Writer, source io.Reader, limit int64) (int64, error) {
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, err := source.Read(buffer)
		if read > 0 {
			if total+int64(read) > limit {
				return total + int64(read), newCodedError("oversized_jsonl", "JSONL source exceeds the size bound")
			}
			written, writeErr := destination.Write(buffer[:read])
			if writeErr != nil {
				return total + int64(written), newCodedError("capture_artifact", "write JSONL artifact")
			}
			if written != read {
				return total + int64(written), io.ErrShortWrite
			}
			total += int64(read)
			if err := ctx.Err(); err != nil {
				return total, err
			}
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, newCodedError("capture_source", "read JSONL source")
		}
	}
}

func finalizeCaptureArtifact(path string, artifact CaptureArtifact) (CaptureArtifact, error) {
	if artifact.Method == "sqlite_backup" {
		if err := rejectCapturedSQLiteSidecars(path); err != nil {
			return CaptureArtifact{}, err
		}
	}
	hash, err := fileSHA256(path)
	if err != nil {
		return CaptureArtifact{}, newCodedError("capture_artifact", "hash captured artifact")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return CaptureArtifact{}, newCodedError("capture_artifact", "inspect captured artifact")
	}
	if info.Size() < 0 {
		return CaptureArtifact{}, newCodedError("capture_artifact", "captured artifact size is invalid")
	}
	artifact.FileSHA256 = hash
	artifact.Bytes = info.Size()
	return artifact, nil
}

func rejectCapturedSQLiteSidecars(path string) error {
	for _, suffix := range sqliteSidecarSuffixes {
		candidate := path + suffix
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return newCodedError("capture_sidecar", "captured SQLite sidecar is unsafe")
		}
		return newCodedError("capture_sidecar", "captured SQLite sidecar exists: %s", suffix)
	}
	return nil
}

func syncCaptureFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

type captureArtifactHashRecord struct {
	Role        string `json:"role"`
	Method      string `json:"method"`
	FileSHA256  string `json:"file_sha256"`
	Bytes       int64  `json:"bytes"`
	PageCount   *int   `json:"page_count,omitempty"`
	QuickCheck  string `json:"quick_check,omitempty"`
	SidecarZero *int   `json:"sidecar_zero,omitempty"`
}

func captureArtifactSetSHA256(artifacts map[string]CaptureArtifact) string {
	lines := make([]string, 0, len(artifacts))
	for role, artifact := range artifacts {
		encoded, _ := json.Marshal(captureArtifactHashRecord{
			Role: role, Method: artifact.Method, FileSHA256: artifact.FileSHA256, Bytes: artifact.Bytes,
			PageCount: artifact.PageCount, QuickCheck: artifact.QuickCheck, SidecarZero: artifact.SidecarZero,
		})
		lines = append(lines, string(encoded))
	}
	if len(lines) == 0 {
		return ""
	}
	return hashCanonicalLines(lines)
}

func blockedCaptureReceipt(receipt CaptureReceipt, root string, cause error) (CaptureReceipt, error) {
	receipt.Status = StatusBlocked
	receipt.CompletedAt = time.Now().UTC()
	receipt.ErrorCode = errorCode(cause, "capture_failed")
	receipt.ArtifactSetSHA256 = captureArtifactSetSHA256(receipt.Artifacts)
	if root == "" {
		return receipt, cause
	}
	writeErr := captureReceiptWriter(filepath.Join(root, CaptureReceiptFilename), receipt)
	if writeErr == nil {
		return receipt, cause
	}
	cleanupCaptureRoot(root)
	receipt.ErrorCode = "capture_receipt"
	return receipt, errors.Join(cause, newCodedError("capture_receipt", "write capture receipt"))
}

func cleanupCaptureRoot(root string) {
	for _, spec := range captureArtifactSpecs {
		path := filepath.Join(root, spec.filename)
		_ = os.Remove(path)
		for _, suffix := range sqliteSidecarSuffixes {
			_ = os.Remove(path + suffix)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".rencrow-capture-") {
			_ = os.Remove(filepath.Join(root, name))
		}
	}
}

func validateCaptureReceipt(receipt CaptureReceipt) error {
	if receipt.SchemaVersion != CaptureSchemaVersion || receipt.Mode != ModeCapture {
		return fmt.Errorf("capture receipt header is invalid")
	}
	if receipt.Status != StatusReady && receipt.Status != StatusBlocked {
		return fmt.Errorf("capture receipt status is invalid")
	}
	if receipt.StartedAt.IsZero() || receipt.CompletedAt.IsZero() || receipt.CompletedAt.Before(receipt.StartedAt) {
		return fmt.Errorf("capture receipt timestamps are invalid")
	}
	if len(receipt.Artifacts) > len(captureArtifactSpecs) {
		return fmt.Errorf("capture receipt contains too many artifacts")
	}
	specs := make(map[string]captureArtifactSpec, len(captureArtifactSpecs))
	for _, spec := range captureArtifactSpecs {
		specs[spec.role] = spec
	}
	for role, artifact := range receipt.Artifacts {
		spec, ok := specs[role]
		if !ok {
			return fmt.Errorf("capture receipt contains an unknown artifact role")
		}
		if artifact.Method != spec.method || !isLowerHexSHA256(artifact.FileSHA256) || artifact.Bytes < 0 {
			return fmt.Errorf("capture receipt artifact is invalid")
		}
		if spec.sqlite {
			if artifact.PageCount == nil || *artifact.PageCount < 0 || artifact.QuickCheck != "ok" || artifact.SidecarZero == nil || *artifact.SidecarZero != 0 {
				return fmt.Errorf("capture receipt SQLite evidence is invalid")
			}
		} else if artifact.PageCount != nil || artifact.QuickCheck != "" || artifact.SidecarZero != nil {
			return fmt.Errorf("capture receipt JSONL evidence contains SQLite fields")
		}
	}
	if receipt.Status == StatusReady {
		if len(receipt.Artifacts) != len(captureArtifactSpecs) || !isLowerHexSHA256(receipt.ArtifactSetSHA256) || receipt.ErrorCode != "" {
			return fmt.Errorf("ready capture receipt is incomplete")
		}
		for _, spec := range captureArtifactSpecs {
			if _, ok := receipt.Artifacts[spec.role]; !ok {
				return fmt.Errorf("ready capture receipt is missing an artifact")
			}
		}
		if receipt.ArtifactSetSHA256 != captureArtifactSetSHA256(receipt.Artifacts) {
			return fmt.Errorf("ready capture artifact set hash does not match")
		}
	} else {
		if !validErrorCode(receipt.ErrorCode) {
			return fmt.Errorf("blocked capture receipt error_code is invalid")
		}
		wantArtifactSetHash := captureArtifactSetSHA256(receipt.Artifacts)
		if receipt.ArtifactSetSHA256 != wantArtifactSetHash || (wantArtifactSetHash != "" && !isLowerHexSHA256(receipt.ArtifactSetSHA256)) {
			return fmt.Errorf("blocked capture artifact set hash is invalid")
		}
	}
	return nil
}

func writeCaptureReceipt(path string, receipt CaptureReceipt) error {
	if err := validateCaptureReceipt(receipt); err != nil {
		return newCodedError("capture_receipt", "validate capture receipt")
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return newCodedError("capture_receipt", "encode capture receipt")
	}
	if int64(len(encoded))+1 > maxCaptureManifestBytes {
		return newCodedError("capture_receipt", "capture receipt exceeds the size bound")
	}
	encoded = append(encoded, '\n')
	if _, err := os.Lstat(path); err == nil {
		return newCodedError("capture_receipt", "capture receipt already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return newCodedError("capture_receipt", "inspect capture receipt path")
	}
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".rencrow-capture-receipt-*.tmp")
	if err != nil {
		return newCodedError("capture_receipt", "create capture receipt temporary file")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return newCodedError("capture_receipt", "set capture receipt permissions")
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return newCodedError("capture_receipt", "write capture receipt")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return newCodedError("capture_receipt", "sync capture receipt")
	}
	if err := temporary.Close(); err != nil {
		return newCodedError("capture_receipt", "close capture receipt")
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return newCodedError("capture_receipt", "install capture receipt")
	}
	if err := syncDirectory(parent); err != nil {
		_ = os.Remove(path)
		return newCodedError("capture_receipt", "sync capture receipt directory")
	}
	return nil
}
