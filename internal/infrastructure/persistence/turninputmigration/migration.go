package turninputmigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
)

const ContractVersion = "rencrow-turn-input-migration/v1"

const (
	ModeDryRun = "dry-run"
	ModeApply  = "apply"

	maxReceiptBytes = 64 << 10
)

// Options controls the one-shot, writer-stopped migration.
type Options struct {
	Mode               string
	SourceDir          string
	EventDBPath        string
	ConversationDBPath string
	OutputDir          string
	ReceiptPath        string
	DryRunReceipt      string
}

// Receipt is the machine-readable migration plan and result. It deliberately
// contains hashes and counts only; source paths, source content, and legacy
// identifiers never appear in a receipt.
type Receipt struct {
	ContractVersion            string `json:"contract_version"`
	Status                     string `json:"status"`
	Mode                       string `json:"mode"`
	SourceSHA256               string `json:"source_sha256"`
	EventEvidenceSHA256        string `json:"event_evidence_sha256"`
	ConversationEvidenceSHA256 string `json:"conversation_evidence_sha256"`
	MappingSHA256              string `json:"mapping_sha256"`
	OutputSHA256               string `json:"output_sha256"`
	SourceFiles                int    `json:"source_files"`
	CanonicalSessionFiles      int    `json:"canonical_session_files"`
	NonSessionFiles            int    `json:"non_session_files"`
	LegacyHistoryRows          int    `json:"legacy_history_rows"`
	CanonicalHistoryRows       int    `json:"canonical_history_rows"`
	ReceiptLinkedRows          int    `json:"receipt_linked_rows"`
	DeterministicRows          int    `json:"deterministic_rows"`
	OutputHistoryRows          int    `json:"output_history_rows"`
	LegacyHistoryRowsRemaining int    `json:"legacy_history_rows_remaining"`
	ErrorCode                  string `json:"error_code,omitempty"`
}

type migrationError struct {
	code  string
	cause error
}

func (e *migrationError) Error() string {
	return e.code
}

func (e *migrationError) Unwrap() error {
	return e.cause
}

func blocked(code string, cause error) error {
	if code == "" {
		code = "migration_failed"
	}
	return &migrationError{code: code, cause: cause}
}

func errorCode(err error) string {
	var migrationErr *migrationError
	if errors.As(err, &migrationErr) && migrationErr.code != "" {
		return migrationErr.code
	}
	return "migration_failed"
}

type resolvedOptions struct {
	mode               string
	sourceDir          string
	eventDBPath        string
	conversationDBPath string
	outputDir          string
	receiptPath        string
	dryRunReceipt      string
}

type preparedPlan struct {
	options    resolvedOptions
	files      []plannedFile
	receipt    Receipt
	sessionIDs []string
}

type plannedFile struct {
	name        string
	sourcePath  string
	permission  fs.FileMode
	data        []byte
	materialize bool
	sessionID   string
	historyRows int
}

// Run performs a dry-run plan or applies a previously accepted dry-run plan.
// It never mutates the source directory or either SQLite database.
func Run(ctx context.Context, options Options) (Receipt, error) {
	receipt := Receipt{
		ContractVersion: ContractVersion,
		Status:          "blocked",
		Mode:            options.Mode,
	}
	if err := ctx.Err(); err != nil {
		receipt.ErrorCode = "context_canceled"
		return receipt, blocked(receipt.ErrorCode, err)
	}

	resolved, err := resolveOptions(options)
	if err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	plan, err := prepare(ctx, resolved)
	if err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}

	if resolved.mode == ModeDryRun {
		receipt = plan.receipt
		receipt.Status = "ready"
		receipt.Mode = ModeDryRun
		if err := writeFreshReceipt(resolved.receiptPath, receipt); err != nil {
			receipt.Status = "blocked"
			receipt.ErrorCode = errorCode(err)
			return receipt, err
		}
		return receipt, nil
	}

	prior, err := readStrictReceipt(resolved.dryRunReceipt)
	if err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := compareDryRunPlan(prior, plan.receipt); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := ensureSourceUnchanged(ctx, resolved.sourceDir, plan.receipt.SourceSHA256); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := ensureEmptyOutput(resolved.outputDir); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}

	if err := materialize(ctx, plan); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	outputHash, err := hashDirectory(resolved.outputDir)
	if err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if outputHash != plan.receipt.OutputSHA256 {
		err = blocked("output_drift", errors.New("materialized output hash differs from plan"))
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := verifyOutput(ctx, plan, outputHash); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := ensureSourceUnchanged(ctx, resolved.sourceDir, plan.receipt.SourceSHA256); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}

	receipt = plan.receipt
	receipt.Status = "applied"
	receipt.Mode = ModeApply
	receipt.OutputSHA256 = outputHash
	if err := writeFreshReceipt(resolved.receiptPath, receipt); err != nil {
		receipt.Status = "blocked"
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	return receipt, nil
}

func resolveOptions(options Options) (resolvedOptions, error) {
	if options.Mode != ModeDryRun && options.Mode != ModeApply {
		return resolvedOptions{}, blocked("invalid_mode", errors.New("mode must be dry-run or apply"))
	}
	sourceDir, err := resolveDirectory(options.SourceDir, "invalid_source")
	if err != nil {
		return resolvedOptions{}, err
	}
	eventDBPath, err := resolveRegularFile(options.EventDBPath, "invalid_event_database")
	if err != nil {
		return resolvedOptions{}, err
	}
	conversationDBPath, err := resolveRegularFile(options.ConversationDBPath, "invalid_conversation_database")
	if err != nil {
		return resolvedOptions{}, err
	}
	if eventDBPath == conversationDBPath {
		return resolvedOptions{}, blocked("invalid_database_layout", errors.New("event and conversation databases must differ"))
	}
	if pathWithin(eventDBPath, sourceDir) || pathWithin(conversationDBPath, sourceDir) {
		return resolvedOptions{}, blocked("invalid_database_layout", errors.New("databases must be outside source"))
	}

	resolved := resolvedOptions{
		mode:               options.Mode,
		sourceDir:          sourceDir,
		eventDBPath:        eventDBPath,
		conversationDBPath: conversationDBPath,
	}
	if options.Mode == ModeApply {
		outputDir, err := resolveDirectory(options.OutputDir, "invalid_output")
		if err != nil {
			return resolvedOptions{}, err
		}
		if outputDir == sourceDir {
			return resolvedOptions{}, blocked("invalid_output", errors.New("source and output must differ"))
		}
		if err := ensureEmptyOutput(outputDir); err != nil {
			return resolvedOptions{}, err
		}
		if pathWithin(eventDBPath, outputDir) || pathWithin(conversationDBPath, outputDir) {
			return resolvedOptions{}, blocked("invalid_database_layout", errors.New("databases must be outside output"))
		}
		resolved.outputDir = outputDir
	}

	forbidden := []string{sourceDir}
	if resolved.outputDir != "" {
		forbidden = append(forbidden, resolved.outputDir)
	}
	receiptPath, err := resolveFreshPath(options.ReceiptPath, forbidden, "invalid_receipt")
	if err != nil {
		return resolvedOptions{}, err
	}
	resolved.receiptPath = receiptPath
	if options.Mode == ModeApply {
		dryReceipt, err := resolveExistingReceipt(options.DryRunReceipt, forbidden, "invalid_dry_run_receipt")
		if err != nil {
			return resolvedOptions{}, err
		}
		resolved.dryRunReceipt = dryReceipt
	}
	return resolved, nil
}

func resolveDirectory(raw, code string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", blocked(code, errors.New("directory is required"))
	}
	abs, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return "", blocked(code, err)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", blocked(code, errors.New("directory is unavailable"))
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", blocked(code, err)
	}
	return filepath.Clean(resolved), nil
}

func resolveRegularFile(raw, code string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", blocked(code, errors.New("file is required"))
	}
	abs, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return "", blocked(code, err)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", blocked(code, errors.New("file is unavailable"))
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", blocked(code, err)
	}
	return filepath.Clean(resolved), nil
}

func resolveFreshPath(raw string, forbidden []string, code string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", blocked(code, errors.New("receipt path is required"))
	}
	abs, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return "", blocked(code, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", blocked(code, err)
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", blocked(code, errors.New("receipt parent is unavailable"))
	}
	resolved := filepath.Join(filepath.Clean(parent), filepath.Base(abs))
	if _, err := os.Lstat(resolved); err == nil || !os.IsNotExist(err) {
		return "", blocked(code, errors.New("receipt target must be fresh"))
	}
	for _, root := range forbidden {
		if pathWithin(resolved, root) {
			return "", blocked(code, errors.New("receipt must be outside protected directories"))
		}
	}
	return resolved, nil
}

func resolveExistingReceipt(raw string, forbidden []string, code string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", blocked(code, errors.New("dry-run receipt is required"))
	}
	abs, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return "", blocked(code, err)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxReceiptBytes || info.Mode().Perm() != 0600 {
		return "", blocked(code, errors.New("dry-run receipt is unavailable"))
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", blocked(code, err)
	}
	for _, root := range forbidden {
		if pathWithin(resolved, root) {
			return "", blocked(code, errors.New("dry-run receipt must be outside protected directories"))
		}
	}
	return filepath.Clean(resolved), nil
}

type sourceEntry struct {
	name       string
	path       string
	permission fs.FileMode
}

func snapshotSource(dir string) ([]sourceEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, blocked("source_read_failed", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]sourceEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.Name()]; exists {
			return nil, blocked("source_invalid", errors.New("duplicate source filename"))
		}
		seen[entry.Name()] = struct{}{}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, blocked("source_read_failed", err)
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, blocked("source_invalid", errors.New("source entries must be top-level regular files"))
		}
		result = append(result, sourceEntry{name: entry.Name(), path: path, permission: info.Mode().Perm()})
	}
	return result, nil
}

func hashDirectory(dir string) (string, error) {
	entries, err := snapshotSource(dir)
	if err != nil {
		return "", err
	}
	return hashSourceEntries(entries)
}

func hashSourceEntries(entries []sourceEntry) (string, error) {
	hash := sha256.New()
	for _, entry := range entries {
		file, err := os.Open(entry.path)
		if err != nil {
			return "", blocked("source_read_failed", err)
		}
		if err := hashRecord(hash, entry.name, entry.permission, file); err != nil {
			_ = file.Close()
			return "", blocked("source_read_failed", err)
		}
		if err := file.Close(); err != nil {
			return "", blocked("source_read_failed", err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashPlannedFiles(files []plannedFile) (string, error) {
	ordered := append([]plannedFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })
	hash := sha256.New()
	for _, file := range ordered {
		if file.materialize {
			if err := hashRecord(hash, file.name, file.permission, bytes.NewReader(file.data)); err != nil {
				return "", blocked("output_plan_failed", err)
			}
			continue
		}
		input, err := os.Open(file.sourcePath)
		if err != nil {
			return "", blocked("source_read_failed", err)
		}
		err = hashRecord(hash, file.name, file.permission, input)
		closeErr := input.Close()
		if err != nil || closeErr != nil {
			if err == nil {
				err = closeErr
			}
			return "", blocked("output_plan_failed", err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashRecord(hash io.Writer, name string, permission fs.FileMode, input io.Reader) error {
	if _, err := fmt.Fprintf(hash, "%d:%s:%o\x00", len(name), name, permission.Perm()); err != nil {
		return err
	}
	if _, err := io.Copy(hash, input); err != nil {
		return err
	}
	_, err := io.WriteString(hash, "\x00")
	return err
}

func ensureSourceUnchanged(ctx context.Context, dir, expected string) error {
	if err := ctx.Err(); err != nil {
		return blocked("context_canceled", err)
	}
	actual, err := hashDirectory(dir)
	if err != nil {
		return err
	}
	if actual != expected {
		return blocked("source_drift", errors.New("source changed during migration"))
	}
	return nil
}

func ensureEmptyOutput(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return blocked("invalid_output", err)
	}
	if len(entries) != 0 {
		return blocked("invalid_output", errors.New("output directory must be empty"))
	}
	return nil
}

func materialize(ctx context.Context, plan preparedPlan) error {
	if err := ensureEmptyOutput(plan.options.outputDir); err != nil {
		return err
	}
	ordered := append([]plannedFile(nil), plan.files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })
	for _, file := range ordered {
		if err := ctx.Err(); err != nil {
			return blocked("context_canceled", err)
		}
		path := filepath.Join(plan.options.outputDir, file.name)
		var input io.Reader
		var close func() error
		if file.materialize {
			input = bytes.NewReader(file.data)
			close = func() error { return nil }
		} else {
			source, err := os.Open(file.sourcePath)
			if err != nil {
				return blocked("source_read_failed", err)
			}
			input = source
			close = source.Close
		}
		err := writeAtomicStream(path, file.permission, input)
		closeErr := close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return blocked("output_write_failed", closeErr)
		}
	}
	return nil
}

func verifyOutput(ctx context.Context, plan preparedPlan, outputHash string) error {
	if outputHash != plan.receipt.OutputSHA256 {
		return blocked("output_drift", errors.New("output hash differs from plan"))
	}
	entries, err := snapshotSource(plan.options.outputDir)
	if err != nil {
		return blocked("output_invalid", err)
	}
	if len(entries) != len(plan.files) {
		return blocked("output_invalid", errors.New("output file count differs from plan"))
	}
	want := make(map[string]plannedFile, len(plan.files))
	for _, file := range plan.files {
		want[file.name] = file
	}
	for _, entry := range entries {
		file, ok := want[entry.name]
		if !ok || entry.permission != file.permission {
			return blocked("output_invalid", errors.New("output file set differs from plan"))
		}
	}
	repository := sessionpersistence.NewJSONSessionRepository(plan.options.outputDir)
	seen := make(map[string]struct{}, len(plan.sessionIDs))
	historyRows := 0
	for _, id := range plan.sessionIDs {
		if err := ctx.Err(); err != nil {
			return blocked("context_canceled", err)
		}
		if _, exists := seen[id]; exists {
			return blocked("output_invalid", errors.New("duplicate output Session ID"))
		}
		seen[id] = struct{}{}
		loaded, err := repository.Load(ctx, id)
		if err != nil {
			return blocked("output_invalid", err)
		}
		for _, file := range plan.files {
			if file.sessionID == id && loaded.HistoryCount() != file.historyRows {
				return blocked("output_invalid", errors.New("output history count differs from plan"))
			}
		}
		historyRows += loaded.HistoryCount()
	}
	if len(seen) != len(plan.sessionIDs) || historyRows != plan.receipt.OutputHistoryRows {
		return blocked("output_invalid", errors.New("output Session history count differs from plan"))
	}
	return nil
}

func writeAtomicStream(path string, permission fs.FileMode, input io.Reader) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".turninput-*")
	if err != nil {
		return blocked("output_write_failed", err)
	}
	temporaryPath := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(permission); err != nil {
		return blocked("output_write_failed", err)
	}
	if _, err := io.Copy(file, input); err != nil {
		return blocked("output_write_failed", err)
	}
	if err := file.Sync(); err != nil {
		return blocked("output_write_failed", err)
	}
	if err := file.Close(); err != nil {
		return blocked("output_write_failed", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return blocked("output_write_failed", err)
	}
	committed = true
	return nil
}

func writeFreshReceipt(path string, receipt Receipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return blocked("receipt_write_failed", err)
	}
	if len(data) > maxReceiptBytes {
		return blocked("receipt_write_failed", errors.New("receipt exceeds size limit"))
	}
	return writeAtomicBytes(path, 0600, data)
}

func writeAtomicBytes(path string, permission fs.FileMode, data []byte) error {
	return writeAtomicStream(path, permission, bytes.NewReader(data))
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func compareDryRunPlan(prior, current Receipt) error {
	if prior.ContractVersion != ContractVersion || prior.Status != "ready" || prior.Mode != ModeDryRun || prior.ErrorCode != "" {
		return blocked("dry_run_receipt_invalid", errors.New("dry-run receipt status is invalid"))
	}
	if prior.SourceSHA256 != current.SourceSHA256 ||
		prior.EventEvidenceSHA256 != current.EventEvidenceSHA256 ||
		prior.ConversationEvidenceSHA256 != current.ConversationEvidenceSHA256 ||
		prior.MappingSHA256 != current.MappingSHA256 ||
		prior.OutputSHA256 != current.OutputSHA256 ||
		prior.SourceFiles != current.SourceFiles ||
		prior.CanonicalSessionFiles != current.CanonicalSessionFiles ||
		prior.NonSessionFiles != current.NonSessionFiles ||
		prior.LegacyHistoryRows != current.LegacyHistoryRows ||
		prior.CanonicalHistoryRows != current.CanonicalHistoryRows ||
		prior.ReceiptLinkedRows != current.ReceiptLinkedRows ||
		prior.DeterministicRows != current.DeterministicRows ||
		prior.OutputHistoryRows != current.OutputHistoryRows ||
		prior.LegacyHistoryRowsRemaining != current.LegacyHistoryRowsRemaining {
		return blocked("dry_run_mismatch", errors.New("dry-run receipt does not match current plan"))
	}
	return nil
}

func readStrictReceipt(path string) (Receipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, blocked("dry_run_receipt_invalid", err)
	}
	if len(data) > maxReceiptBytes {
		return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("dry-run receipt exceeds size limit"))
	}
	fields, err := strictObject(data)
	if err != nil {
		return Receipt{}, blocked("dry_run_receipt_invalid", err)
	}
	allowed := map[string]bool{
		"contract_version": true, "status": true, "mode": true,
		"source_sha256": true, "event_evidence_sha256": true, "conversation_evidence_sha256": true,
		"mapping_sha256": true, "output_sha256": true,
		"source_files": true, "canonical_session_files": true, "non_session_files": true,
		"legacy_history_rows": true, "canonical_history_rows": true, "receipt_linked_rows": true,
		"deterministic_rows": true, "output_history_rows": true, "legacy_history_rows_remaining": true,
		"error_code": true,
	}
	for key := range fields {
		if !allowed[key] {
			return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("unknown dry-run receipt field"))
		}
	}
	required := []string{"contract_version", "status", "mode", "source_sha256", "event_evidence_sha256", "conversation_evidence_sha256", "mapping_sha256", "output_sha256", "source_files", "canonical_session_files", "non_session_files", "legacy_history_rows", "canonical_history_rows", "receipt_linked_rows", "deterministic_rows", "output_history_rows", "legacy_history_rows_remaining"}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("missing dry-run receipt field"))
		}
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, blocked("dry_run_receipt_invalid", err)
	}
	for _, key := range []string{"contract_version", "status", "mode", "source_sha256", "event_evidence_sha256", "conversation_evidence_sha256", "mapping_sha256", "output_sha256"} {
		value, err := decodeString(fields[key])
		if err != nil || value == "" {
			return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("dry-run receipt string field is invalid"))
		}
	}
	for _, key := range []string{"source_files", "canonical_session_files", "non_session_files", "legacy_history_rows", "canonical_history_rows", "receipt_linked_rows", "deterministic_rows", "output_history_rows", "legacy_history_rows_remaining"} {
		trimmed := bytes.TrimSpace(fields[key])
		if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
			return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("dry-run receipt count field is invalid"))
		}
		var value int
		if err := json.Unmarshal(fields[key], &value); err != nil || value < 0 {
			return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("dry-run receipt count field is invalid"))
		}
	}
	if raw, ok := fields["error_code"]; ok {
		if _, err := decodeString(raw); err != nil {
			return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("dry-run receipt error_code is invalid"))
		}
	}
	return receipt, nil
}

func writeHashStrings(values []string) string {
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	hash := sha256.New()
	for _, value := range ordered {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:%s\x00", len(value), value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
