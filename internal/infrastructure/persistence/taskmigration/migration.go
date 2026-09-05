package taskmigration

import (
	"bufio"
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
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const ContractVersion = "rencrow-task-store-migration/v1"

const (
	ModeDryRun = "dry-run"
	ModeApply  = "apply"

	stateFilename         = "job_state.jsonl"
	contextFilename       = "job_context.jsonl"
	notificationsFilename = "job_notifications.jsonl"
	taskStateFilename     = "task_state.jsonl"
	taskContextFilename   = "task_context.jsonl"
	taskNotifyFilename    = "task_notifications.jsonl"
	maxReceiptBytes       = 64 << 10
	maxLineBytes          = 8 << 20
)

// Options controls the writer-stopped, one-shot migration. Source is never
// modified; apply writes only to a fresh output directory.
type Options struct {
	Mode          string
	SourceDir     string
	OutputDir     string
	ReceiptPath   string
	DryRunReceipt string
}

// Receipt is a hash-bound plan/result. It intentionally contains no paths or
// legacy identifiers.
type Receipt struct {
	ContractVersion        string `json:"contract_version"`
	Status                 string `json:"status"`
	Mode                   string `json:"mode"`
	SourceSHA256           string `json:"source_sha256"`
	MappingSHA256          string `json:"mapping_sha256"`
	OutputSHA256           string `json:"output_sha256"`
	SourceFiles            int    `json:"source_files"`
	OutputFiles            int    `json:"output_files"`
	LegacyStateRows        int    `json:"legacy_state_rows"`
	TaskStateRows          int    `json:"task_state_rows"`
	LegacyContextRows      int    `json:"legacy_context_rows"`
	TaskContextRows        int    `json:"task_context_rows"`
	LegacyNotificationRows int    `json:"legacy_notification_rows"`
	TaskNotificationRows   int    `json:"task_notification_rows"`
	LegacyTaskIDs          int    `json:"legacy_task_ids"`
	CanonicalTaskIDs       int    `json:"canonical_task_ids"`
	LegacyRowsRemaining    int    `json:"legacy_rows_remaining"`
	ErrorCode              string `json:"error_code,omitempty"`
}

type migrationError struct {
	code  string
	cause error
}

func (e *migrationError) Error() string { return e.code }
func (e *migrationError) Unwrap() error { return e.cause }

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
	mode          string
	sourceDir     string
	outputDir     string
	receiptPath   string
	dryRunReceipt string
}

type sourceFile struct {
	name       string
	path       string
	permission fs.FileMode
}

type plannedFile struct {
	name       string
	permission fs.FileMode
	data       []byte
}

type preparedPlan struct {
	options resolvedOptions
	files   []plannedFile
	receipt Receipt
}

// legacyState mirrors the historical Job JSONL schema. The parent/dependency
// fields are accepted only as legacy references and are rewritten to TaskID.
type legacyState struct {
	JobID                string                     `json:"job_id"`
	Title                string                     `json:"title"`
	ModuleID             string                     `json:"module_id,omitempty"`
	ModuleRoot           string                     `json:"module_root,omitempty"`
	Route                domaintask.Route           `json:"route"`
	Assignee             string                     `json:"assignee,omitempty"`
	CoderRoles           []string                   `json:"coder_roles,omitempty"`
	Status               string                     `json:"status"`
	Priority             domaintask.Priority        `json:"priority"`
	CreatedBy            string                     `json:"created_by,omitempty"`
	ParentConversationID string                     `json:"parent_conversation_id,omitempty"`
	ParentMessageID      string                     `json:"parent_message_id,omitempty"`
	ParentJobID          string                     `json:"parent_job_id,omitempty"`
	DependencyJobIDs     []string                   `json:"dependency_job_ids,omitempty"`
	InterruptPolicy      domaintask.InterruptPolicy `json:"interrupt_policy"`
	SupersedesJobID      string                     `json:"supersedes_job_id,omitempty"`
	ReadOnly             bool                       `json:"read_only"`
	CreatedAt            time.Time                  `json:"created_at"`
	UpdatedAt            time.Time                  `json:"updated_at"`
	StartedAt            *time.Time                 `json:"started_at,omitempty"`
	FinishedAt           *time.Time                 `json:"finished_at,omitempty"`
	Summary              string                     `json:"summary,omitempty"`
	NextActions          []string                   `json:"next_actions,omitempty"`
	Evidence             []string                   `json:"evidence,omitempty"`
	Artifacts            []string                   `json:"artifacts,omitempty"`
}

type legacyContext struct {
	JobID         string    `json:"job_id"`
	UserIntent    string    `json:"user_intent,omitempty"`
	ModuleID      string    `json:"module_id,omitempty"`
	ModuleRoot    string    `json:"module_root,omitempty"`
	RelevantFiles []string  `json:"relevant_files,omitempty"`
	Decisions     []string  `json:"decisions,omitempty"`
	Constraints   []string  `json:"constraints,omitempty"`
	CurrentPlan   string    `json:"current_plan,omitempty"`
	LatestStatus  string    `json:"latest_status,omitempty"`
	Artifacts     []string  `json:"artifacts,omitempty"`
	HandoffNotes  string    `json:"handoff_notes,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type legacyNotification struct {
	Type        string                       `json:"type"`
	Level       domaintask.NotificationLevel `json:"level"`
	JobID       string                       `json:"job_id"`
	Title       string                       `json:"title"`
	Assignee    string                       `json:"assignee,omitempty"`
	Route       domaintask.Route             `json:"route,omitempty"`
	ModuleID    string                       `json:"module_id,omitempty"`
	Status      string                       `json:"status"`
	Summary     string                       `json:"summary,omitempty"`
	NextActions []string                     `json:"next_actions,omitempty"`
	Interrupt   bool                         `json:"interrupt"`
	CreatedAt   time.Time                    `json:"created_at"`
}

type parsedSource struct {
	files         []sourceFile
	state         []legacyState
	contexts      []legacyContext
	notifications []legacyNotification
	stateBytes    [][]byte
}

// Run performs a dry-run or applies a matching dry-run receipt.
func Run(ctx context.Context, options Options) (Receipt, error) {
	receipt := Receipt{ContractVersion: ContractVersion, Status: "blocked", Mode: options.Mode}
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
	if err := ensureSourceHash(resolved.sourceDir, plan.receipt.SourceSHA256); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := ensureEmptyDirectory(resolved.outputDir); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := materialize(ctx, plan); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := verifyOutput(ctx, plan); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	if err := ensureSourceHash(resolved.sourceDir, plan.receipt.SourceSHA256); err != nil {
		receipt.ErrorCode = errorCode(err)
		return receipt, err
	}
	receipt = plan.receipt
	receipt.Status = "applied"
	receipt.Mode = ModeApply
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
	resolved := resolvedOptions{mode: options.Mode, sourceDir: sourceDir}
	forbidden := []string{sourceDir}
	if options.Mode == ModeApply {
		outputDir, outputErr := resolveDirectory(options.OutputDir, "invalid_output")
		if outputErr != nil {
			return resolvedOptions{}, outputErr
		}
		if outputDir == sourceDir || pathWithin(outputDir, sourceDir) || pathWithin(sourceDir, outputDir) {
			return resolvedOptions{}, blocked("invalid_output", errors.New("source and output must differ"))
		}
		if outputErr = ensureEmptyDirectory(outputDir); outputErr != nil {
			return resolvedOptions{}, outputErr
		}
		resolved.outputDir = outputDir
		forbidden = append(forbidden, outputDir)
	}
	receiptPath, err := resolveFreshPath(options.ReceiptPath, forbidden, "invalid_receipt")
	if err != nil {
		return resolvedOptions{}, err
	}
	resolved.receiptPath = receiptPath
	if options.Mode == ModeApply {
		dryPath, dryErr := resolveExistingReceipt(options.DryRunReceipt, forbidden, "invalid_dry_run_receipt")
		if dryErr != nil {
			return resolvedOptions{}, dryErr
		}
		resolved.dryRunReceipt = dryPath
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
	if _, err := os.Lstat(resolved); err == nil || !errors.Is(err, os.ErrNotExist) {
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
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxReceiptBytes || info.Mode().Perm() != 0o600 {
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

func snapshotSource(dir string) ([]sourceFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, blocked("source_read_failed", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]sourceFile, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, blocked("source_read_failed", statErr)
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, blocked("source_invalid", errors.New("source entries must be top-level regular files"))
		}
		result = append(result, sourceFile{name: entry.Name(), path: path, permission: info.Mode().Perm()})
	}
	return result, nil
}

func parseSource(ctx context.Context, dir string) (parsedSource, string, error) {
	files, err := snapshotSource(dir)
	if err != nil {
		return parsedSource{}, "", err
	}
	want := map[string]bool{stateFilename: false, contextFilename: false, notificationsFilename: false}
	for _, file := range files {
		if _, ok := want[file.name]; !ok {
			return parsedSource{}, "", blocked("source_invalid", errors.New("unknown task store file"))
		}
		want[file.name] = true
	}
	for name, present := range want {
		if !present {
			return parsedSource{}, "", blocked("source_invalid", fmt.Errorf("missing legacy task store file %s", name))
		}
	}
	source := parsedSource{files: files}
	for _, file := range files {
		data, readErr := os.ReadFile(file.path)
		if readErr != nil {
			return parsedSource{}, "", blocked("source_read_failed", readErr)
		}
		switch file.name {
		case stateFilename:
			rows, rawRows, parseErr := readJSONL[legacyState](ctx, data)
			if parseErr != nil {
				return parsedSource{}, "", blocked("state_schema_invalid", parseErr)
			}
			source.state, source.stateBytes = rows, rawRows
		case contextFilename:
			rows, _, parseErr := readJSONL[legacyContext](ctx, data)
			if parseErr != nil {
				return parsedSource{}, "", blocked("context_schema_invalid", parseErr)
			}
			source.contexts = rows
		case notificationsFilename:
			rows, _, parseErr := readJSONL[legacyNotification](ctx, data)
			if parseErr != nil {
				return parsedSource{}, "", blocked("notification_schema_invalid", parseErr)
			}
			source.notifications = rows
		}
	}
	if err := validateLegacySource(source); err != nil {
		return parsedSource{}, "", err
	}
	hash, err := hashFiles(files)
	if err != nil {
		return parsedSource{}, "", err
	}
	return source, hash, nil
}

func readJSONL[T any](ctx context.Context, data []byte) ([]T, [][]byte, error) {
	if len(data) == 0 {
		return []T{}, [][]byte{}, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	values := make([]T, 0)
	rawRows := make([][]byte, 0)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			return nil, nil, errors.New("blank JSONL row is invalid")
		}
		var value T
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return nil, nil, errors.New("JSONL row contains trailing value")
			}
			return nil, nil, err
		}
		values = append(values, value)
		rawRows = append(rawRows, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return values, rawRows, nil
}

func validateLegacySource(source parsedSource) error {
	seen := make(map[string]struct{})
	for index, row := range source.state {
		if strings.TrimSpace(row.JobID) == "" {
			return blocked("state_schema_invalid", fmt.Errorf("state row %d has no job_id", index))
		}
		if _, exists := seen[row.JobID]; !exists {
			seen[row.JobID] = struct{}{}
		}
		if strings.TrimSpace(row.Title) == "" || !domaintask.ValidStatus(domaintask.Status(row.Status)) || row.Status == "waiting_user" {
			if row.Status == "waiting_user" {
				return blocked("waiting_user_forbidden", errors.New("waiting_user is not a Task status"))
			}
			return blocked("state_schema_invalid", errors.New("legacy task state is invalid"))
		}
		if !domaintask.ValidPriority(row.Priority) || !domaintask.ValidRoute(row.Route) {
			return blocked("state_schema_invalid", errors.New("legacy task priority or route is invalid"))
		}
		if row.InterruptPolicy != domaintask.InterruptNotifyDoneOrBlocked && row.InterruptPolicy != domaintask.InterruptSilent {
			return blocked("state_schema_invalid", errors.New("legacy interrupt policy is invalid"))
		}
		if row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
			return blocked("state_schema_invalid", errors.New("legacy timestamps are invalid"))
		}
		if row.ParentConversationID != "" {
			return blocked("origin_unsupported", errors.New("parent_conversation_id has no canonical origin evidence"))
		}
		if row.ParentMessageID != "" && modulecore.MessageID(row.ParentMessageID).Validate() != nil {
			return blocked("origin_invalid", errors.New("parent_message_id is not canonical"))
		}
		if row.Status == string(domaintask.StatusWaiting) && strings.TrimSpace(row.Summary) == "" {
			return blocked("state_schema_invalid", errors.New("waiting state has no machine reason"))
		}
	}
	for index, row := range source.contexts {
		if strings.TrimSpace(row.JobID) == "" || row.UpdatedAt.IsZero() {
			return blocked("context_schema_invalid", fmt.Errorf("context row %d is invalid", index))
		}
	}
	for index, row := range source.notifications {
		if strings.TrimSpace(row.JobID) == "" || strings.TrimSpace(row.Type) == "" || row.CreatedAt.IsZero() {
			return blocked("notification_schema_invalid", fmt.Errorf("notification row %d is invalid", index))
		}
		if row.Status == "waiting_user" {
			return blocked("waiting_user_forbidden", errors.New("waiting_user notification is not a Task notification"))
		}
	}
	return nil
}

func prepare(ctx context.Context, options resolvedOptions) (preparedPlan, error) {
	source, sourceHash, err := parseSource(ctx, options.sourceDir)
	if err != nil {
		return preparedPlan{}, err
	}
	mappings := make(map[string]modulecore.TaskID)
	reverseMappings := make(map[modulecore.TaskID]string)
	legacyIDs := make([]string, 0)
	for _, row := range source.state {
		if _, exists := mappings[row.JobID]; exists {
			continue
		}
		mapped, mapErr := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "job_state", "job_id", row.JobID)
		if mapErr != nil {
			return preparedPlan{}, blocked("mapping_failed", mapErr)
		}
		taskID := modulecore.TaskID(mapped)
		if taskID.Validate() != nil {
			return preparedPlan{}, blocked("mapping_failed", errors.New("mapped TaskID is invalid"))
		}
		if other, collision := reverseMappings[taskID]; collision && other != row.JobID {
			return preparedPlan{}, blocked("mapping_collision", errors.New("multiple legacy identities map to one TaskID"))
		}
		mappings[row.JobID] = taskID
		reverseMappings[taskID] = row.JobID
		legacyIDs = append(legacyIDs, row.JobID)
	}
	sort.Strings(legacyIDs)
	if err := validateReferences(source, mappings); err != nil {
		return preparedPlan{}, err
	}
	plans, counts, mappingHash, err := convertRows(source, mappings)
	if err != nil {
		return preparedPlan{}, err
	}
	if err := ensureSourceHash(options.sourceDir, sourceHash); err != nil {
		return preparedPlan{}, err
	}
	outputHash := hashPlannedFiles(plans)
	receipt := Receipt{
		ContractVersion: ContractVersion, Status: "ready", Mode: ModeDryRun,
		SourceSHA256: sourceHash, MappingSHA256: mappingHash, OutputSHA256: outputHash,
		SourceFiles: len(source.files), OutputFiles: len(plans), LegacyStateRows: len(source.state), TaskStateRows: counts.state,
		LegacyContextRows: len(source.contexts), TaskContextRows: counts.context,
		LegacyNotificationRows: len(source.notifications), TaskNotificationRows: counts.notifications,
		LegacyTaskIDs: len(mappings), CanonicalTaskIDs: len(mappings), LegacyRowsRemaining: 0,
	}
	return preparedPlan{options: options, files: plans, receipt: receipt}, nil
}

func validateReferences(source parsedSource, mappings map[string]modulecore.TaskID) error {
	for _, row := range source.state {
		for _, ref := range append([]string{row.ParentJobID, row.SupersedesJobID}, row.DependencyJobIDs...) {
			if ref == "" {
				continue
			}
			if _, ok := mappings[ref]; !ok {
				return blocked("dangling_reference", errors.New("legacy task reference has no state mapping"))
			}
		}
		if row.ParentJobID == row.JobID || row.SupersedesJobID == row.JobID {
			return blocked("invalid_relationship", errors.New("legacy task relationship points to itself"))
		}
		seen := make(map[string]struct{}, len(row.DependencyJobIDs))
		for _, dependency := range row.DependencyJobIDs {
			if dependency == row.JobID {
				return blocked("invalid_relationship", errors.New("legacy dependency points to itself"))
			}
			if _, exists := seen[dependency]; exists {
				return blocked("invalid_relationship", errors.New("duplicate legacy dependency"))
			}
			seen[dependency] = struct{}{}
		}
	}
	for _, row := range source.contexts {
		if _, ok := mappings[row.JobID]; !ok {
			return blocked("dangling_reference", errors.New("context references unknown job"))
		}
	}
	for _, row := range source.notifications {
		if _, ok := mappings[row.JobID]; !ok {
			return blocked("dangling_reference", errors.New("notification references unknown job"))
		}
	}
	return validateCurrentRelationshipGraph(source.state)
}

func validateCurrentRelationshipGraph(rows []legacyState) error {
	latest := make(map[string]legacyState, len(rows))
	for _, row := range rows {
		latest[row.JobID] = row
	}
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(latest))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return blocked("relationship_cycle", errors.New("legacy Task relationship graph contains a cycle"))
		case visited:
			return nil
		}
		state[id] = visiting
		row := latest[id]
		references := append([]string{row.ParentJobID, row.SupersedesJobID}, row.DependencyJobIDs...)
		for _, reference := range references {
			if reference != "" {
				if err := visit(reference); err != nil {
					return err
				}
			}
		}
		state[id] = visited
		return nil
	}
	keys := make([]string, 0, len(latest))
	for id := range latest {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

type rowCounts struct{ state, context, notifications int }

func convertRows(source parsedSource, mappings map[string]modulecore.TaskID) ([]plannedFile, rowCounts, string, error) {
	stateRows := make([][]byte, 0, len(source.state))
	mappingRecords := make([]string, 0, len(mappings))
	for _, oldID := range sortedMappingKeys(mappings) {
		mappingRecords = append(mappingRecords, fmt.Sprintf("%d:%s:%s", len(oldID), oldID, mappings[oldID]))
	}
	for _, row := range source.state {
		mapped := domaintask.Task{
			TaskID: mappings[row.JobID], Title: row.Title, ModuleID: row.ModuleID, ModuleRoot: row.ModuleRoot,
			Route: row.Route, OwnerID: row.CreatedBy, Assignee: row.Assignee, CoderRoles: append([]string(nil), row.CoderRoles...),
			Status: domaintask.Status(row.Status), Priority: row.Priority, InterruptPolicy: row.InterruptPolicy, ReadOnly: row.ReadOnly,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
			Summary: row.Summary, NextActions: append([]string(nil), row.NextActions...), Evidence: append([]string(nil), row.Evidence...), Artifacts: append([]string(nil), row.Artifacts...),
		}
		if row.ParentJobID != "" {
			mapped.ParentTaskID = mappings[row.ParentJobID]
		}
		for _, dependency := range row.DependencyJobIDs {
			mapped.DependencyTaskIDs = append(mapped.DependencyTaskIDs, mappings[dependency])
		}
		if row.ParentMessageID != "" {
			mapped.OriginMessageID = modulecore.MessageID(row.ParentMessageID)
		}
		if row.SupersedesJobID != "" {
			mapped.SupersedesTaskID = mappings[row.SupersedesJobID]
		}
		if mapped.Status == domaintask.StatusWaiting {
			mapped.WaitingReason = strings.TrimSpace(mapped.Summary)
		}
		if err := mapped.Validate(); err != nil {
			return nil, rowCounts{}, "", blocked("mapped_state_invalid", err)
		}
		data, err := json.Marshal(mapped)
		if err != nil {
			return nil, rowCounts{}, "", blocked("mapping_failed", err)
		}
		stateRows = append(stateRows, data)
	}
	contextRows := make([][]byte, 0, len(source.contexts))
	for _, row := range source.contexts {
		value := domaintask.SharedRoleContext{TaskID: mappings[row.JobID], UserIntent: row.UserIntent, ModuleID: row.ModuleID, ModuleRoot: row.ModuleRoot, RelevantFiles: append([]string(nil), row.RelevantFiles...), Decisions: append([]string(nil), row.Decisions...), Constraints: append([]string(nil), row.Constraints...), CurrentPlan: row.CurrentPlan, LatestStatus: row.LatestStatus, Artifacts: append([]string(nil), row.Artifacts...), HandoffNotes: row.HandoffNotes, UpdatedAt: row.UpdatedAt}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, rowCounts{}, "", blocked("mapping_failed", err)
		}
		contextRows = append(contextRows, data)
	}
	notificationRows := make([][]byte, 0, len(source.notifications))
	for _, row := range source.notifications {
		status := domaintask.Status(row.Status)
		if !domaintask.ValidStatus(status) {
			return nil, rowCounts{}, "", blocked("notification_schema_invalid", errors.New("notification status is invalid"))
		}
		value := domaintask.Notification{Type: "task.notification", Level: row.Level, TaskID: mappings[row.JobID], Title: row.Title, Assignee: row.Assignee, Route: row.Route, ModuleID: row.ModuleID, Status: status, Summary: row.Summary, NextActions: append([]string(nil), row.NextActions...), Interrupt: row.Interrupt, CreatedAt: row.CreatedAt}
		if value.Level == "" {
			value.Level = domaintask.NotificationLevelForStatus(value.Status, domaintask.PriorityNormal)
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, rowCounts{}, "", blocked("mapping_failed", err)
		}
		notificationRows = append(notificationRows, data)
	}
	plans := []plannedFile{
		{name: taskStateFilename, permission: permissionFor(source.files, stateFilename), data: joinJSONL(stateRows)},
		{name: taskContextFilename, permission: permissionFor(source.files, contextFilename), data: joinJSONL(contextRows)},
		{name: taskNotifyFilename, permission: permissionFor(source.files, notificationsFilename), data: joinJSONL(notificationRows)},
	}
	return plans, rowCounts{state: len(stateRows), context: len(contextRows), notifications: len(notificationRows)}, writeHashStrings(mappingRecords), nil
}

func permissionFor(files []sourceFile, name string) fs.FileMode {
	for _, file := range files {
		if file.name == name {
			return file.permission
		}
	}
	return 0o644
}

func sortedMappingKeys(mapping map[string]modulecore.TaskID) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinJSONL(rows [][]byte) []byte {
	if len(rows) == 0 {
		return nil
	}
	var buffer bytes.Buffer
	for _, row := range rows {
		buffer.Write(row)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

func materialize(ctx context.Context, plan preparedPlan) error {
	if err := ensureEmptyDirectory(plan.options.outputDir); err != nil {
		return err
	}
	ordered := append([]plannedFile(nil), plan.files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })
	for _, file := range ordered {
		if err := ctx.Err(); err != nil {
			return blocked("context_canceled", err)
		}
		if err := writeAtomic(filepath.Join(plan.options.outputDir, file.name), file.permission, file.data); err != nil {
			return err
		}
	}
	return nil
}

func verifyOutput(ctx context.Context, plan preparedPlan) error {
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
	hash, err := hashFiles(entries)
	if err != nil {
		return err
	}
	if hash != plan.receipt.OutputSHA256 {
		return blocked("output_drift", errors.New("output hash differs from plan"))
	}
	store, err := taskpersistence.NewJSONLStore(plan.options.outputDir)
	if err != nil {
		return blocked("output_invalid", err)
	}
	if _, err := store.ListTasks(ctx, domaintask.Filter{Limit: 1 << 30}); err != nil {
		return blocked("output_invalid", err)
	}
	for _, file := range plan.files {
		data, err := os.ReadFile(filepath.Join(plan.options.outputDir, file.name))
		if err != nil {
			return blocked("output_invalid", err)
		}
		switch file.name {
		case taskContextFilename:
			contexts, _, err := readJSONL[domaintask.SharedRoleContext](ctx, data)
			if err != nil {
				return blocked("output_invalid", err)
			}
			for _, item := range contexts {
				if err := item.TaskID.Validate(); err != nil {
					return blocked("output_invalid", err)
				}
				if _, err := store.GetContext(ctx, item.TaskID); err != nil {
					return blocked("output_invalid", err)
				}
			}
		case taskNotifyFilename:
			notifications, _, err := readJSONL[domaintask.Notification](ctx, data)
			if err != nil {
				return blocked("output_invalid", err)
			}
			for _, item := range notifications {
				if err := item.TaskID.Validate(); err != nil || item.Type != "task.notification" || !domaintask.ValidStatus(item.Status) {
					return blocked("output_invalid", errors.New("Task notification is invalid"))
				}
			}
			loaded, err := store.ListNotifications(ctx, 1<<30, false)
			if err != nil || len(loaded) != len(notifications) {
				return blocked("output_invalid", errors.New("Task notification owner reload differs"))
			}
		}
	}
	return nil
}

func ensureSourceHash(dir, expected string) error {
	files, err := snapshotSource(dir)
	if err != nil {
		return err
	}
	actual, err := hashFiles(files)
	if err != nil {
		return err
	}
	if actual != expected {
		return blocked("source_drift", errors.New("source changed during migration"))
	}
	return nil
}

func ensureEmptyDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return blocked("invalid_output", err)
	}
	if len(entries) != 0 {
		return blocked("invalid_output", errors.New("output directory must be empty"))
	}
	return nil
}

func hashFiles(files []sourceFile) (string, error) {
	ordered := append([]sourceFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })
	hash := sha256.New()
	for _, file := range ordered {
		input, err := os.Open(file.path)
		if err != nil {
			return "", blocked("source_read_failed", err)
		}
		if err := hashRecord(hash, file.name, file.permission, input); err != nil {
			_ = input.Close()
			return "", blocked("source_read_failed", err)
		}
		if err := input.Close(); err != nil {
			return "", blocked("source_read_failed", err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashPlannedFiles(files []plannedFile) string {
	ordered := append([]plannedFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })
	hash := sha256.New()
	for _, file := range ordered {
		_ = hashRecord(hash, file.name, file.permission, bytes.NewReader(file.data))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hashRecord(output io.Writer, name string, permission fs.FileMode, input io.Reader) error {
	if _, err := fmt.Fprintf(output, "%d:%s:%o\x00", len(name), name, permission.Perm()); err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	_, err := io.WriteString(output, "\x00")
	return err
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

func writeFreshReceipt(path string, receipt Receipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return blocked("receipt_write_failed", err)
	}
	if len(data) > maxReceiptBytes {
		return blocked("receipt_write_failed", errors.New("receipt exceeds size limit"))
	}
	return writeAtomic(path, 0o600, data)
}

func writeAtomic(path string, permission fs.FileMode, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".rencrow-task-migrate-*")
	if err != nil {
		return blocked("output_write_failed", err)
	}
	temporary := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(permission); err != nil {
		return blocked("output_write_failed", err)
	}
	if _, err := file.Write(data); err != nil {
		return blocked("output_write_failed", err)
	}
	if err := file.Sync(); err != nil {
		return blocked("output_write_failed", err)
	}
	if err := file.Close(); err != nil {
		return blocked("output_write_failed", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return blocked("output_write_failed", err)
	}
	committed = true
	return nil
}

func compareDryRunPlan(prior, current Receipt) error {
	if prior.ContractVersion != ContractVersion || prior.Status != "ready" || prior.Mode != ModeDryRun || prior.ErrorCode != "" {
		return blocked("dry_run_receipt_invalid", errors.New("dry-run receipt status is invalid"))
	}
	if prior.SourceSHA256 != current.SourceSHA256 || prior.MappingSHA256 != current.MappingSHA256 || prior.OutputSHA256 != current.OutputSHA256 ||
		prior.SourceFiles != current.SourceFiles || prior.OutputFiles != current.OutputFiles || prior.LegacyStateRows != current.LegacyStateRows || prior.TaskStateRows != current.TaskStateRows ||
		prior.LegacyContextRows != current.LegacyContextRows || prior.TaskContextRows != current.TaskContextRows || prior.LegacyNotificationRows != current.LegacyNotificationRows || prior.TaskNotificationRows != current.TaskNotificationRows ||
		prior.LegacyTaskIDs != current.LegacyTaskIDs || prior.CanonicalTaskIDs != current.CanonicalTaskIDs || prior.LegacyRowsRemaining != current.LegacyRowsRemaining {
		return blocked("dry_run_mismatch", errors.New("dry-run receipt does not match current plan"))
	}
	return nil
}

func readStrictReceipt(path string) (Receipt, error) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxReceiptBytes {
		return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("dry-run receipt is unavailable"))
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil {
		return Receipt{}, blocked("dry_run_receipt_invalid", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("dry-run receipt has trailing data"))
	}
	allowed := map[string]bool{
		"contract_version": true, "status": true, "mode": true, "source_sha256": true, "mapping_sha256": true, "output_sha256": true,
		"source_files": true, "output_files": true, "legacy_state_rows": true, "task_state_rows": true, "legacy_context_rows": true, "task_context_rows": true,
		"legacy_notification_rows": true, "task_notification_rows": true, "legacy_task_ids": true, "canonical_task_ids": true, "legacy_rows_remaining": true, "error_code": true,
	}
	for key := range fields {
		if !allowed[key] {
			return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("unknown dry-run receipt field"))
		}
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, blocked("dry_run_receipt_invalid", err)
	}
	if receipt.ContractVersion == "" || receipt.Status == "" || receipt.Mode == "" || receipt.SourceSHA256 == "" || receipt.MappingSHA256 == "" || receipt.OutputSHA256 == "" {
		return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("required receipt field is missing"))
	}
	if receipt.SourceFiles < 0 || receipt.OutputFiles < 0 || receipt.LegacyStateRows < 0 || receipt.TaskStateRows < 0 || receipt.LegacyContextRows < 0 || receipt.TaskContextRows < 0 || receipt.LegacyNotificationRows < 0 || receipt.TaskNotificationRows < 0 || receipt.LegacyTaskIDs < 0 || receipt.CanonicalTaskIDs < 0 || receipt.LegacyRowsRemaining < 0 {
		return Receipt{}, blocked("dry_run_receipt_invalid", errors.New("receipt count is invalid"))
	}
	return receipt, nil
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
