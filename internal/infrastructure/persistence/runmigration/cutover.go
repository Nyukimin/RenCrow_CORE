package runmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	RunCutoverSchema         = "rencrow.identity.run-cutover/v1"
	RunCutoverApplied        = "applied"
	RunCutoverBlocked        = "blocked"
	RunCutoverRolledBack     = "rolled_back"
	RunCutoverRollbackFailed = "rollback_failed"
)

var runCutoverRoles = []string{
	"browser.sqlite",
	"checkpoints.json",
	"dialogue.jsonl",
	"events.sqlite",
	"forecast.json",
	"knowledge.sqlite",
	"story.jsonl",
	"superagent.sqlite",
	"tasks/task_context.jsonl",
	"tasks/task_notifications.jsonl",
	"tasks/task_run.jsonl",
	"tasks/task_state.jsonl",
	"word.json",
}

// RunCutoverActiveManifest binds the exact pre-cutover active bytes. Paths are
// derived from the four fixed owner roots and are never accepted per file.
type RunCutoverActiveManifest struct {
	SchemaVersion string            `json:"schema_version"`
	TasksDir      string            `json:"tasks_dir"`
	EventStore    string            `json:"event_store"`
	OpsDir        string            `json:"ops_dir"`
	SessionDir    string            `json:"session_dir"`
	Files         map[string]string `json:"files"`
}

// CutoverOptions is the sole public Step10 production file-cutover contract.
// The service remains stopped after success so deployment can replace the
// runtime before any writer opens the new cohort.
type CutoverOptions struct {
	Cohort                       string
	Snapshot                     string
	Inventory                    Inventory
	PlanReceipt                  Receipt
	ExpectedPlanReceiptSHA256    string
	Active                       RunCutoverActiveManifest
	ExpectedActiveManifestSHA256 string
	RollbackDir                  string
	CutoverReceipt               string
	InstalledRuntime             string
	ExpectedRuntimeSHA256        string
	ActiveConfig                 string
}

type RunCutoverServiceEvidence struct {
	Owner         int    `json:"owner"`
	Masked        int    `json:"masked"`
	Active        int    `json:"active"`
	MainPIDZero   int    `json:"main_pid_zero"`
	ListenerZero  int    `json:"listener_zero"`
	RuntimeSHA256 string `json:"runtime_sha256"`
}

func (e RunCutoverServiceEvidence) valid(expected string) bool {
	return isLowerSHA256(expected) && e.Owner == 1 && e.Masked == 1 && e.Active == 0 &&
		e.MainPIDZero == 1 && e.ListenerZero == 1 && e.RuntimeSHA256 == expected
}

type RunCutoverReceipt struct {
	SchemaVersion        string            `json:"schema_version"`
	Status               string            `json:"status"`
	StartedAt            time.Time         `json:"started_at"`
	CompletedAt          time.Time         `json:"completed_at"`
	InventorySHA256      string            `json:"inventory_sha256"`
	PlanReceiptSHA256    string            `json:"plan_receipt_sha256"`
	ActiveManifestSHA256 string            `json:"active_manifest_sha256"`
	QuarantineMarker     string            `json:"quarantine_marker"`
	QuarantinedEvents    int               `json:"quarantined_events"`
	QuarantinedTaskIDs   int               `json:"quarantined_task_ids"`
	OldActiveSHA256      map[string]string `json:"old_active_sha256"`
	NewActiveSHA256      map[string]string `json:"new_active_sha256"`
	ServiceStopped       int               `json:"service_stopped"`
	RollbackFiles        int               `json:"rollback_files"`
	AppliedFiles         int               `json:"applied_files"`
	ErrorCode            string            `json:"error_code,omitempty"`
}

type runCutoverService interface {
	StopAndVerify(context.Context, string, string) (RunCutoverServiceEvidence, error)
	Restore(context.Context, string) error
}

var runCutoverServiceFactory = newRunCutoverService
var runCutoverReplaceHook func(int) error
var runCutoverReceiptWriter = writeRunCutoverReceipt

type preparedRunCutover struct {
	activePaths map[string]string
	activeBytes map[string][]byte
	outputBytes map[string][]byte
	receipt     RunCutoverReceipt
}

type stagedRunCutoverFile struct{ role, active, stage, old string }

type runCutoverFailure struct{ code string }

func (e runCutoverFailure) Error() string { return e.code }

// Cutover stops the fixed CORE writer, creates a durable rollback cohort, and
// replaces all Step10 files as one rollback-protected operation. On success it
// intentionally leaves the service stopped for the following deployment step.
func Cutover(ctx context.Context, options CutoverOptions) (RunCutoverReceipt, error) {
	now := time.Now().UTC()
	receipt := RunCutoverReceipt{SchemaVersion: RunCutoverSchema, Status: RunCutoverBlocked, StartedAt: now, CompletedAt: now}
	prepared, err := prepareRunCutover(ctx, options, receipt)
	if err != nil {
		return finishRunCutoverReceipt(prepared.receipt, RunCutoverBlocked, codeForRunCutover(err, "preflight"), err)
	}
	receipt = prepared.receipt
	service, err := runCutoverServiceFactory(options.InstalledRuntime, options.ActiveConfig)
	if err != nil || service == nil {
		return finishRunCutoverReceipt(receipt, RunCutoverBlocked, codeForRunCutover(err, "service_owner"), runCutoverFailure{codeForRunCutover(err, "service_owner")})
	}
	evidence, err := service.StopAndVerify(ctx, options.ExpectedRuntimeSHA256, options.ActiveConfig)
	if err != nil || !evidence.valid(options.ExpectedRuntimeSHA256) {
		if err == nil {
			err = runCutoverFailure{"service_stopped"}
		}
		_ = service.Restore(context.WithoutCancel(ctx), options.ExpectedRuntimeSHA256)
		return finishRunCutoverReceipt(receipt, RunCutoverBlocked, codeForRunCutover(err, "service_stopped"), err)
	}
	receipt.ServiceStopped = 1
	if err = revalidateStoppedRunCutover(prepared); err != nil {
		_ = service.Restore(context.WithoutCancel(ctx), options.ExpectedRuntimeSHA256)
		return finishRunCutoverReceipt(receipt, RunCutoverBlocked, codeForRunCutover(err, "stopped_revalidation"), err)
	}

	if err = createRunCutoverRollback(options.RollbackDir, prepared); err != nil {
		_ = service.Restore(context.WithoutCancel(ctx), options.ExpectedRuntimeSHA256)
		return finishRunCutoverReceipt(receipt, RunCutoverBlocked, codeForRunCutover(err, "rollback_prepare"), err)
	}
	receipt.RollbackFiles = len(runCutoverRoles)

	applied, rollbackOK, err := applyRunCutover(prepared)
	receipt.AppliedFiles = applied
	if err != nil {
		status := RunCutoverRolledBack
		code := codeForRunCutover(err, "apply")
		if !rollbackOK {
			rollbackOK = restoreRunCutoverFromBytes(prepared)
		}
		if !rollbackOK {
			status = RunCutoverRollbackFailed
			code = "rollback_failed"
		} else if restoreErr := service.Restore(context.WithoutCancel(ctx), options.ExpectedRuntimeSHA256); restoreErr != nil {
			status = RunCutoverRollbackFailed
			code = "service_restore"
		}
		return finishAndWriteRunCutoverReceipt(options.CutoverReceipt, receipt, status, code, err)
	}
	appliedReceipt, _ := finishRunCutoverReceipt(receipt, RunCutoverApplied, "", nil)
	if err := runCutoverReceiptWriter(options.CutoverReceipt, appliedReceipt); err != nil {
		status, code := RunCutoverRolledBack, "receipt_write"
		if !restoreRunCutoverFromBytes(prepared) {
			status, code = RunCutoverRollbackFailed, "rollback_failed"
		} else if restoreErr := service.Restore(context.WithoutCancel(ctx), options.ExpectedRuntimeSHA256); restoreErr != nil {
			status, code = RunCutoverRollbackFailed, "service_restore"
		}
		return finishRunCutoverReceipt(receipt, status, code, runCutoverFailure{"receipt_write"})
	}
	return appliedReceipt, nil
}

func prepareRunCutover(ctx context.Context, options CutoverOptions, receipt RunCutoverReceipt) (preparedRunCutover, error) {
	prepared := preparedRunCutover{receipt: receipt}
	if ctx == nil {
		return prepared, runCutoverFailure{"invalid_context"}
	}
	if err := ctx.Err(); err != nil {
		return prepared, err
	}
	if !isLowerSHA256(options.ExpectedRuntimeSHA256) {
		return prepared, runCutoverFailure{"invalid_runtime_hash"}
	}
	runtimeBytes, err := readRunCutoverFile(options.InstalledRuntime)
	if err != nil || digest(runtimeBytes) != options.ExpectedRuntimeSHA256 {
		return prepared, runCutoverFailure{"runtime_mismatch"}
	}
	if _, err = readRunCutoverFile(options.ActiveConfig); err != nil {
		return prepared, runCutoverFailure{"active_config"}
	}
	if options.PlanReceipt.SchemaVersion != Schema || options.PlanReceipt.Status != "quarantined" ||
		options.PlanReceipt.QuarantineMarker != "retain_and_quarantine/v1" || options.PlanReceipt.ErrorCode != "" ||
		options.PlanReceipt.Counts["quarantined_events"] <= 0 || options.PlanReceipt.Counts["quarantined_task_ids"] <= 0 {
		return prepared, runCutoverFailure{"quarantine_receipt"}
	}
	planJSON, err := canonicalJSON(options.PlanReceipt)
	if err != nil || !isLowerSHA256(options.ExpectedPlanReceiptSHA256) || digest(planJSON) != options.ExpectedPlanReceiptSHA256 {
		return prepared, runCutoverFailure{"quarantine_receipt_mismatch"}
	}
	inventoryJSON, err := canonicalJSON(options.Inventory)
	if err != nil {
		return prepared, runCutoverFailure{"inventory"}
	}
	if digest(inventoryJSON) != options.PlanReceipt.InventorySHA256 {
		return prepared, runCutoverFailure{"inventory_mismatch"}
	}
	snapshotFiles, err := readSnapshotFiles(options.Snapshot, options.Inventory.Files)
	if err != nil {
		return prepared, runCutoverFailure{"snapshot_mismatch"}
	}
	if len(options.PlanReceipt.Outputs) != len(runCutoverRoles)+1 || options.PlanReceipt.Outputs["tasks/.jsonlbatch.lock"] != digest(nil) {
		return prepared, runCutoverFailure{"cohort_manifest"}
	}
	outputs, err := readSnapshotFiles(options.Cohort, options.PlanReceipt.Outputs)
	if err != nil {
		return prepared, runCutoverFailure{"cohort_mismatch"}
	}
	activePaths, err := resolveRunCutoverActivePaths(options.Active)
	if err != nil {
		return prepared, err
	}
	activeJSON, err := canonicalJSON(options.Active)
	if err != nil || !isLowerSHA256(options.ExpectedActiveManifestSHA256) || digest(activeJSON) != options.ExpectedActiveManifestSHA256 {
		return prepared, runCutoverFailure{"active_manifest_mismatch"}
	}
	if err = validateFreshRunCutoverTargets(options, activePaths); err != nil {
		return prepared, err
	}
	activeBytes := make(map[string][]byte, len(runCutoverRoles))
	for _, role := range runCutoverRoles {
		value, readErr := readRunCutoverFile(activePaths[role])
		if readErr != nil || digest(value) != options.Active.Files[role] {
			return prepared, runCutoverFailure{"active_mismatch"}
		}
		sourcePath, ok := options.Inventory.Roles[runCutoverSourceRole(role)]
		if !ok || sourcePath == "" || !bytes.Equal(value, snapshotFiles[sourcePath]) {
			return prepared, runCutoverFailure{"active_snapshot_mismatch"}
		}
		activeBytes[role] = value
	}
	prepared.activePaths = activePaths
	prepared.activeBytes = activeBytes
	prepared.outputBytes = make(map[string][]byte, len(runCutoverRoles))
	prepared.receipt.InventorySHA256 = options.PlanReceipt.InventorySHA256
	prepared.receipt.PlanReceiptSHA256 = digest(planJSON)
	prepared.receipt.ActiveManifestSHA256 = digest(activeJSON)
	prepared.receipt.QuarantineMarker = options.PlanReceipt.QuarantineMarker
	prepared.receipt.QuarantinedEvents = options.PlanReceipt.Counts["quarantined_events"]
	prepared.receipt.QuarantinedTaskIDs = options.PlanReceipt.Counts["quarantined_task_ids"]
	prepared.receipt.OldActiveSHA256 = cloneStringMap(options.Active.Files)
	prepared.receipt.NewActiveSHA256 = map[string]string{}
	for _, role := range runCutoverRoles {
		prepared.outputBytes[role] = outputs[role]
		prepared.receipt.NewActiveSHA256[role] = options.PlanReceipt.Outputs[role]
	}
	return prepared, nil
}

func revalidateStoppedRunCutover(prepared preparedRunCutover) error {
	for _, role := range runCutoverRoles {
		path := prepared.activePaths[role]
		value, err := readRunCutoverFile(path)
		if err != nil || !bytes.Equal(value, prepared.activeBytes[role]) {
			return runCutoverFailure{"active_drift"}
		}
		if isRunCutoverSQLite(role) && (pathExists(path+"-wal") || pathExists(path+"-shm")) {
			return runCutoverFailure{"active_sidecar"}
		}
	}
	return nil
}

func runCutoverSourceRole(outputRole string) string {
	switch outputRole {
	case "tasks/task_state.jsonl":
		return "tasks"
	case "tasks/task_run.jsonl":
		return "runs"
	case "tasks/task_context.jsonl":
		return "contexts"
	case "tasks/task_notifications.jsonl":
		return "notifications"
	case "events.sqlite":
		return "events"
	case "superagent.sqlite":
		return "superagent"
	case "browser.sqlite":
		return "browser"
	case "knowledge.sqlite":
		return "knowledge"
	case "word.json":
		return "word"
	case "forecast.json":
		return "forecast"
	case "story.jsonl":
		return "story"
	case "dialogue.jsonl":
		return "dialogue"
	case "checkpoints.json":
		return "checkpoints"
	default:
		return ""
	}
}

func resolveRunCutoverActivePaths(active RunCutoverActiveManifest) (map[string]string, error) {
	if active.SchemaVersion != RunCutoverSchema || len(active.Files) != len(runCutoverRoles) {
		return nil, runCutoverFailure{"active_manifest"}
	}
	paths := map[string]string{
		"browser.sqlite":                 filepath.Join(active.OpsDir, "browser_trace_to_api.db"),
		"checkpoints.json":               filepath.Join(active.SessionDir, "idlechat_generation_checkpoints.json"),
		"dialogue.jsonl":                 filepath.Join(active.SessionDir, "idlechat_dialogue_episodes.jsonl"),
		"events.sqlite":                  active.EventStore,
		"forecast.json":                  filepath.Join(active.SessionDir, "forecast_topic_stock.json"),
		"knowledge.sqlite":               filepath.Join(active.OpsDir, "knowledge_memory.db"),
		"story.jsonl":                    filepath.Join(active.SessionDir, "idlechat_story_episodes.jsonl"),
		"superagent.sqlite":              filepath.Join(active.OpsDir, "superagent_harness.db"),
		"tasks/task_context.jsonl":       filepath.Join(active.TasksDir, "task_context.jsonl"),
		"tasks/task_notifications.jsonl": filepath.Join(active.TasksDir, "task_notifications.jsonl"),
		"tasks/task_run.jsonl":           filepath.Join(active.TasksDir, "task_run.jsonl"),
		"tasks/task_state.jsonl":         filepath.Join(active.TasksDir, "task_state.jsonl"),
		"word.json":                      filepath.Join(active.SessionDir, "word_topic_stock.json"),
	}
	seen := map[string]bool{}
	for _, role := range runCutoverRoles {
		if !isLowerSHA256(active.Files[role]) {
			return nil, runCutoverFailure{"active_manifest"}
		}
		path, err := filepath.Abs(paths[role])
		if err != nil || strings.TrimSpace(paths[role]) == "" || seen[path] {
			return nil, runCutoverFailure{"active_path"}
		}
		seen[path] = true
		paths[role] = filepath.Clean(path)
	}
	return paths, nil
}

func validateFreshRunCutoverTargets(options CutoverOptions, active map[string]string) error {
	rollback, err := filepath.Abs(options.RollbackDir)
	if err != nil || strings.TrimSpace(options.RollbackDir) == "" || pathExists(rollback) {
		return runCutoverFailure{"rollback_path"}
	}
	receipt, err := filepath.Abs(options.CutoverReceipt)
	if err != nil || strings.TrimSpace(options.CutoverReceipt) == "" || pathExists(receipt) {
		return runCutoverFailure{"receipt_path"}
	}
	cohort, _ := filepath.Abs(options.Cohort)
	snapshot, _ := filepath.Abs(options.Snapshot)
	if pathContains(cohort, rollback) || pathContains(snapshot, rollback) || pathContains(rollback, cohort) || pathContains(rollback, snapshot) {
		return runCutoverFailure{"rollback_path"}
	}
	for _, path := range active {
		if pathContains(filepath.Dir(path), rollback) || pathContains(rollback, path) || pathContains(cohort, path) || pathContains(snapshot, path) {
			return runCutoverFailure{"path_overlap"}
		}
		if pathExists(runCutoverOldPath(path)) {
			return runCutoverFailure{"stale_stage"}
		}
	}
	return nil
}

func createRunCutoverRollback(root string, prepared preparedRunCutover) error {
	if err := os.Mkdir(root, 0o700); err != nil {
		return runCutoverFailure{"rollback_prepare"}
	}
	for _, role := range runCutoverRoles {
		if err := writeRunCutoverFile(filepath.Join(root, filepath.FromSlash(role)), prepared.activeBytes[role], 0o600); err != nil {
			return runCutoverFailure{"rollback_write"}
		}
	}
	return syncRunCutoverDir(root)
}

func applyRunCutover(prepared preparedRunCutover) (int, bool, error) {
	staged := make([]stagedRunCutoverFile, 0, len(runCutoverRoles))
	cleanup := func() {
		for _, item := range staged {
			_ = os.Remove(item.stage)
		}
	}
	defer cleanup()
	for _, role := range runCutoverRoles {
		active := prepared.activePaths[role]
		stage, err := writeRunCutoverStage(active, prepared.outputBytes[role])
		if err != nil {
			return 0, true, runCutoverFailure{"stage_write"}
		}
		staged = append(staged, stagedRunCutoverFile{role: role, active: active, stage: stage, old: runCutoverOldPath(active)})
	}
	applied := 0
	for index, item := range staged {
		if runCutoverReplaceHook != nil {
			if err := runCutoverReplaceHook(index); err != nil {
				return applied, rollbackRunCutover(staged[:applied]), runCutoverFailure{"replace"}
			}
		}
		if err := os.Rename(item.active, item.old); err != nil {
			return applied, rollbackRunCutover(staged[:applied]), runCutoverFailure{"replace"}
		}
		if err := os.Rename(item.stage, item.active); err != nil {
			_ = os.Rename(item.old, item.active)
			return applied, rollbackRunCutover(staged[:applied]), runCutoverFailure{"replace"}
		}
		applied++
	}
	for _, item := range staged {
		if err := os.Remove(item.old); err != nil {
			return applied, false, runCutoverFailure{"old_cleanup"}
		}
		if err := syncRunCutoverDir(filepath.Dir(item.active)); err != nil {
			return applied, false, runCutoverFailure{"directory_sync"}
		}
	}
	return applied, true, nil
}

func rollbackRunCutover(items []stagedRunCutoverFile) bool {
	ok := true
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		failed := item.active + ".rencrow-step10-failed"
		if err := os.Rename(item.active, failed); err != nil {
			ok = false
			continue
		}
		if err := os.Rename(item.old, item.active); err != nil {
			_ = os.Rename(failed, item.active)
			ok = false
			continue
		}
		_ = os.Remove(failed)
		if err := syncRunCutoverDir(filepath.Dir(item.active)); err != nil {
			ok = false
		}
	}
	return ok
}

func restoreRunCutoverFromBytes(prepared preparedRunCutover) bool {
	ok := true
	for index := len(runCutoverRoles) - 1; index >= 0; index-- {
		role := runCutoverRoles[index]
		active := prepared.activePaths[role]
		stage, err := writeRunCutoverStage(active, prepared.activeBytes[role])
		if err != nil {
			ok = false
			continue
		}
		failed := active + ".rencrow-step10-failed"
		if err = os.Rename(active, failed); err != nil {
			_ = os.Remove(stage)
			ok = false
			continue
		}
		if err = os.Rename(stage, active); err != nil {
			_ = os.Rename(failed, active)
			_ = os.Remove(stage)
			ok = false
			continue
		}
		_ = os.Remove(failed)
		if err = syncRunCutoverDir(filepath.Dir(active)); err != nil {
			ok = false
		}
	}
	return ok
}

func writeRunCutoverStage(active string, data []byte) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(active), ".rencrow-step10-stage-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func writeRunCutoverFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		ok = true
	}
	return err
}

func readRunCutoverFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSnapshotFileBytes {
		return nil, errors.New("unsafe file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSnapshotFileBytes+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || int64(len(data)) > maxSnapshotFileBytes || !os.SameFile(info, opened) || opened.Size() != int64(len(data)) {
		return nil, errors.New("file changed")
	}
	return data, nil
}

func finishRunCutoverReceipt(receipt RunCutoverReceipt, status, code string, cause error) (RunCutoverReceipt, error) {
	receipt.Status = status
	receipt.ErrorCode = code
	receipt.CompletedAt = time.Now().UTC()
	if cause == nil && code != "" {
		cause = runCutoverFailure{code}
	}
	if cause != nil {
		return receipt, runCutoverFailure{code}
	}
	return receipt, nil
}

func finishAndWriteRunCutoverReceipt(path string, receipt RunCutoverReceipt, status, code string, cause error) (RunCutoverReceipt, error) {
	receipt, resultErr := finishRunCutoverReceipt(receipt, status, code, cause)
	if err := runCutoverReceiptWriter(path, receipt); err != nil {
		return receipt, runCutoverFailure{"receipt_write"}
	}
	return receipt, resultErr
}

func writeRunCutoverReceipt(path string, receipt RunCutoverReceipt) error {
	data, err := canonicalJSON(receipt)
	if err != nil {
		return err
	}
	return writeRunCutoverFile(path, append(data, '\n'), 0o600)
}

func canonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func codeForRunCutover(err error, fallback string) string {
	var coded runCutoverFailure
	if errors.As(err, &coded) && coded.code != "" {
		return coded.code
	}
	return fallback
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func isRunCutoverSQLite(role string) bool { return strings.HasSuffix(role, ".sqlite") }
func runCutoverOldPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".rencrow-step10-old")
}
func pathExists(path string) bool { _, err := os.Lstat(path); return err == nil || !os.IsNotExist(err) }

func syncRunCutoverDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	return err
}
