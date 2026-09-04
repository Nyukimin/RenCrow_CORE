package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	adapterconfig "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	_ "modernc.org/sqlite"
)

const (
	quiesceSQLiteSchemaVersion = "rencrow.threadmigration.sqlite_quiesce.v1"
	quiesceSQLiteStatusReady   = "quiesced_not_snapshot_bound"
	quiesceSQLiteStatusBlocked = "blocked"
)

var errWriterStoppedProofUnavailable = errors.New("fixed CORE writer stopped proof is unavailable")

type quiesceSQLiteReceipt struct {
	SchemaVersion     string `json:"schema_version"`
	Status            string `json:"status"`
	SQLiteSources     int    `json:"sqlite_sources"`
	BusyZero          bool   `json:"busy_zero"`
	JournalModeDelete bool   `json:"journal_mode_delete"`
	SameFile          bool   `json:"same_file"`
	SidecarZero       bool   `json:"sidecar_zero"`
	L1SHA256          string `json:"l1_sha256"`
	ArchiveSHA256     string `json:"archive_sha256"`
	ReceiptSHA256     string `json:"receipt_sha256"`
	ErrorCode         string `json:"error_code"`
}

func (receipt quiesceSQLiteReceipt) canonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt quiesceSQLiteReceipt) computeSHA256() (string, error) {
	data, err := receipt.canonicalJSON()
	if err != nil {
		return "", err
	}
	return cutoverSHA256(data), nil
}

func (receipt quiesceSQLiteReceipt) validate() error {
	if receipt.SchemaVersion != quiesceSQLiteSchemaVersion || !validCutoverSHA256(receipt.ReceiptSHA256) {
		return errors.New("SQLite quiesce receipt schema or self hash is invalid")
	}
	want, err := receipt.computeSHA256()
	if err != nil || want != receipt.ReceiptSHA256 {
		return errors.New("SQLite quiesce receipt self hash does not match")
	}
	if receipt.Status == quiesceSQLiteStatusReady {
		if receipt.ErrorCode != "" || receipt.SQLiteSources != 2 || !receipt.BusyZero || !receipt.JournalModeDelete || !receipt.SameFile || !receipt.SidecarZero || !validCutoverSHA256(receipt.L1SHA256) || !validCutoverSHA256(receipt.ArchiveSHA256) {
			return errors.New("SQLite quiesce success state is invalid")
		}
		return nil
	}
	if receipt.Status != quiesceSQLiteStatusBlocked || receipt.ErrorCode == "" || receipt.SQLiteSources != 0 || receipt.BusyZero || receipt.JournalModeDelete || receipt.SameFile || receipt.SidecarZero || receipt.L1SHA256 != "" || receipt.ArchiveSHA256 != "" {
		return errors.New("SQLite quiesce blocked state is invalid")
	}
	return nil
}

func runQuiesceSQLite(args []string, stdout io.Writer, operation quiesceSQLiteOperation) int {
	if stdout == nil {
		return 1
	}
	flags := flag.NewFlagSet("rencrow-thread-migrate quiesce-sqlite", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "active CORE config path")
	initialServiceStopped := flags.Bool("initial-service-stopped", false, "require the fixed CORE service to already be maintenance-stopped")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*configPath) == "" || !*initialServiceStopped || operation == nil {
		return writeQuiesceSQLiteReceipt(stdout, newBlockedQuiesceSQLiteReceipt("invalid_arguments"), 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), externalOperationTimeout)
	defer cancel()
	receipt, err := operation(ctx, *configPath)
	if err != nil || receipt.Status != quiesceSQLiteStatusReady || receipt.validate() != nil {
		if receipt.validate() != nil {
			receipt = newBlockedQuiesceSQLiteReceipt("quiesce_failed")
		}
		return writeQuiesceSQLiteReceipt(stdout, receipt, 1)
	}
	return writeQuiesceSQLiteReceipt(stdout, receipt, 0)
}

var proveThreadWriterStopped = proveFixedThreadWriterStopped

func quiesceSQLite(ctx context.Context, configPath string) (quiesceSQLiteReceipt, error) {
	receipt := newBlockedQuiesceSQLiteReceipt("preflight")
	if ctx == nil || ctx.Err() != nil {
		return receipt, errors.New("ThreadID SQLite quiesce blocked: context")
	}
	if err := proveThreadWriterStopped(ctx); err != nil {
		if errors.Is(err, errWriterStoppedProofUnavailable) {
			return sealQuiesceSQLiteReceipt(receipt, quiesceSQLiteStatusBlocked, "writer_stopped_proof_unavailable"), errors.New("ThreadID SQLite quiesce blocked: writer_stopped_proof_unavailable")
		}
		return sealQuiesceSQLiteReceipt(receipt, quiesceSQLiteStatusBlocked, "writer_not_stopped"), errors.New("ThreadID SQLite quiesce blocked: writer_not_stopped")
	}
	cfg, err := adapterconfig.LoadConfig(configPath)
	if err != nil {
		return sealQuiesceSQLiteReceipt(receipt, quiesceSQLiteStatusBlocked, "config_read"), errors.New("ThreadID SQLite quiesce blocked: config_read")
	}
	paths := []string{cfg.Storage.Databases.ConversationL1, cfg.Storage.Databases.ConversationArchive}
	bindings := make([]quiesceSQLiteBinding, 0, len(paths))
	for _, path := range paths {
		binding, err := bindQuiesceSQLiteFile(ctx, path, false)
		if err != nil {
			return sealQuiesceSQLiteReceipt(receipt, quiesceSQLiteStatusBlocked, "source_bind"), errors.New("ThreadID SQLite quiesce blocked: source_bind")
		}
		for _, previous := range bindings {
			if os.SameFile(previous.info, binding.info) {
				return sealQuiesceSQLiteReceipt(receipt, quiesceSQLiteStatusBlocked, "source_alias"), errors.New("ThreadID SQLite quiesce blocked: source_alias")
			}
		}
		bindings = append(bindings, binding)
	}
	post := make([]quiesceSQLiteBinding, 0, len(bindings))
	for _, binding := range bindings {
		quiesced, err := quiesceSQLiteFile(ctx, binding)
		if err != nil {
			return sealQuiesceSQLiteReceipt(receipt, quiesceSQLiteStatusBlocked, "checkpoint"), errors.New("ThreadID SQLite quiesce blocked: checkpoint")
		}
		post = append(post, quiesced)
	}
	receipt.SQLiteSources = 2
	receipt.BusyZero = true
	receipt.JournalModeDelete = true
	receipt.SameFile = true
	receipt.SidecarZero = true
	receipt.L1SHA256 = post[0].sha256
	receipt.ArchiveSHA256 = post[1].sha256
	receipt = sealQuiesceSQLiteReceipt(receipt, quiesceSQLiteStatusReady, "")
	if err := receipt.validate(); err != nil {
		return newBlockedQuiesceSQLiteReceipt("receipt_invalid"), errors.New("ThreadID SQLite quiesce blocked: receipt_invalid")
	}
	return receipt, nil
}

type quiesceSQLiteBinding struct {
	path   string
	info   os.FileInfo
	sha256 string
}

func bindQuiesceSQLiteFile(ctx context.Context, raw string, requireSidecarZero bool) (quiesceSQLiteBinding, error) {
	if ctx == nil || ctx.Err() != nil || strings.TrimSpace(raw) == "" || strings.IndexByte(raw, 0) >= 0 {
		return quiesceSQLiteBinding{}, errors.New("SQLite path is invalid")
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return quiesceSQLiteBinding{}, err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		return quiesceSQLiteBinding{}, errors.New("SQLite metadata is invalid")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !canonicalThreadConfigSamePath(absolute, resolved) {
		return quiesceSQLiteBinding{}, errors.New("SQLite path is not canonical")
	}
	if requireSidecarZero {
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(absolute + suffix); err == nil || !errors.Is(err, os.ErrNotExist) {
				return quiesceSQLiteBinding{}, errors.New("SQLite sidecar remains")
			}
		}
	}
	_, verifiedInfo, hash, err := inspectCutoverFile(ctx, absolute, 0o600, false)
	if err != nil || !os.SameFile(info, verifiedInfo) {
		return quiesceSQLiteBinding{}, errors.New("SQLite content binding failed")
	}
	return quiesceSQLiteBinding{path: absolute, info: info, sha256: hash}, nil
}

func quiesceSQLiteFile(ctx context.Context, before quiesceSQLiteBinding) (quiesceSQLiteBinding, error) {
	current, err := os.Lstat(before.path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(before.info, current) {
		return quiesceSQLiteBinding{}, errors.New("SQLite source changed before checkpoint")
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(before.path), RawQuery: "mode=rw&_pragma=busy_timeout%3d0"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return quiesceSQLiteBinding{}, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return quiesceSQLiteBinding{}, err
	}
	var busy, logFrames, checkpointedFrames int
	operationErr := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames)
	if operationErr == nil && busy != 0 {
		operationErr = errors.New("SQLite source is busy")
	}
	var mode string
	if operationErr == nil {
		operationErr = conn.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode)
	}
	if operationErr == nil && !strings.EqualFold(mode, "delete") {
		operationErr = errors.New("SQLite journal mode is not delete")
	}
	closeErr := errors.Join(conn.Close(), db.Close())
	if operationErr != nil {
		return quiesceSQLiteBinding{}, operationErr
	}
	if closeErr != nil {
		return quiesceSQLiteBinding{}, closeErr
	}
	after, err := bindQuiesceSQLiteFile(ctx, before.path, true)
	if err != nil || !os.SameFile(before.info, after.info) || (runtime.GOOS != "windows" && before.info.Mode().Perm() != after.info.Mode().Perm()) {
		return quiesceSQLiteBinding{}, errors.New("SQLite source changed during checkpoint")
	}
	return after, nil
}

func sealQuiesceSQLiteReceipt(receipt quiesceSQLiteReceipt, status, code string) quiesceSQLiteReceipt {
	receipt.Status = status
	receipt.ErrorCode = code
	if status != quiesceSQLiteStatusReady {
		receipt.SQLiteSources = 0
		receipt.BusyZero = false
		receipt.JournalModeDelete = false
		receipt.SameFile = false
		receipt.SidecarZero = false
		receipt.L1SHA256 = ""
		receipt.ArchiveSHA256 = ""
	}
	receipt.ReceiptSHA256 = ""
	receipt.ReceiptSHA256, _ = receipt.computeSHA256()
	return receipt
}

func newBlockedQuiesceSQLiteReceipt(code string) quiesceSQLiteReceipt {
	return sealQuiesceSQLiteReceipt(quiesceSQLiteReceipt{SchemaVersion: quiesceSQLiteSchemaVersion}, quiesceSQLiteStatusBlocked, code)
}

func writeQuiesceSQLiteReceipt(stdout io.Writer, receipt quiesceSQLiteReceipt, code int) int {
	if receipt.validate() != nil {
		receipt = newBlockedQuiesceSQLiteReceipt("quiesce_failed")
		code = 1
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return 1
	}
	data = append(data, '\n')
	written, err := stdout.Write(data)
	if err != nil || written != len(data) {
		return 1
	}
	return code
}
