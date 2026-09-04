// Package turnmigration owns the one-shot migration of historical L1
// conversation-turn identities.  It operates only on an explicitly supplied
// SQLite file; it has no connection to the live conversation manager.
package turnmigration

import (
	"bytes"
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
	"reflect"
	"strings"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

const (
	ManifestSchemaVersion = "rencrow.turn-message-migration/v1"
	ModeDryRun            = "dry-run"
	ModeApply             = "apply"
	StatusReady           = "ready"
	StatusApplied         = "applied"
	StatusNoop            = "noop"
	StatusBlocked         = "blocked"

	OutboxPayloadVersion = "rencrow.conversation_turn_outbox.v2"

	maxResultJSONBytes    = 64 * 1024
	maxOutboxJSONBytes    = 8192
	maxMessageMetaBytes   = 4096
	maxMessageMetaRunes   = 256
	maxManifestErrorRunes = 512
	maxMigrationRows      = 2_000_000
)

// Options is the complete bounded command contract for one migration run.
// DBPath must name an existing SQLite file. ManifestPath is written by both
// modes; PriorDryRunManifestPath is required for apply.
type Options struct {
	DBPath                  string
	ManifestPath            string
	PriorDryRunManifestPath string
	Mode                    string
}

// Counts is the machine-readable row and identity summary in a receipt.
type Counts struct {
	Receipts            int `json:"receipts"`
	Outbox              int `json:"outbox"`
	RecallTraces        int `json:"recall_traces"`
	RecallTraceItems    int `json:"recall_trace_items"`
	PromptInjections    int `json:"prompt_injections"`
	ReceiptLinkedRecall int `json:"receipt_linked_recall"`
	UnlinkedRecall      int `json:"unlinked_recall"`
	ExistingMessageIDs  int `json:"existing_message_ids"`
}

// Receipt is the checksum-bound, machine-readable migration result. Errors
// are deliberately bounded and contain no row payloads.
type Receipt struct {
	SchemaVersion string   `json:"schema_version"`
	Mode          string   `json:"mode"`
	Status        string   `json:"status"`
	InputSHA256   string   `json:"input_sha256"`
	PlanSHA256    string   `json:"plan_sha256"`
	OutputSHA256  string   `json:"output_sha256,omitempty"`
	Before        Counts   `json:"before"`
	After         Counts   `json:"after"`
	Errors        []string `json:"errors,omitempty"`
	ErrorCode     string   `json:"error_code,omitempty"`
}

// Run performs a read-only plan or an explicitly authorized checksum-bound
// apply. Dry-run writes only the requested receipt; apply also replaces the
// five owned tables in the explicitly supplied database.
func Run(ctx context.Context, options Options) (Receipt, error) {
	receipt := Receipt{SchemaVersion: ManifestSchemaVersion, Mode: options.Mode, Status: StatusBlocked}
	if err := validateOptions(options); err != nil {
		return blockedReceipt(receipt, "invalid_options", err), err
	}
	dbPath, manifestPath, err := resolvePaths(options)
	if err != nil {
		// ManifestPath is not safe to open until resolvePaths proves that it
		// cannot alias the database or the required dry-run receipt.
		return blockedReceipt(receipt, "invalid_path", err), err
	}
	inputHash, err := hashDatabaseFile(dbPath)
	if err != nil {
		return writeFailureReceipt(options, receipt, "source_read", err)
	}
	receipt.InputSHA256 = inputHash

	plan, err := readPlan(ctx, dbPath)
	if err != nil {
		receipt.PlanSHA256 = planHash(plan)
		return writeFailureReceiptAt(manifestPath, receipt, "source_invalid", err)
	}
	receipt.Before = plan.before
	receipt.After = plan.after
	receipt.PlanSHA256 = planHash(plan)
	if err := verifyStableDatabaseFile(dbPath, inputHash); err != nil {
		return writeFailureReceiptAt(manifestPath, receipt, "source_changed", err)
	}

	if options.Mode == ModeDryRun {
		receipt.Status = StatusReady
		return writeReceiptAt(manifestPath, receipt)
	}

	prior, err := readReceipt(options.PriorDryRunManifestPath)
	if err != nil {
		return writeFailureReceiptAt(manifestPath, receipt, "dry_run_receipt_invalid", err)
	}
	if err := validatePriorReceipt(prior, receipt); err != nil {
		return writeFailureReceiptAt(manifestPath, receipt, "dry_run_receipt_mismatch", err)
	}
	if err := applyPlan(ctx, dbPath, plan, inputHash); err != nil {
		return writeFailureReceiptAt(manifestPath, receipt, "apply_failed", err)
	}
	outputHash, err := hashDatabaseFile(dbPath)
	if err != nil {
		return writeFailureReceiptAt(manifestPath, receipt, "output_read", err)
	}
	receipt.OutputSHA256 = outputHash
	if plan.changed {
		receipt.Status = StatusApplied
	} else {
		receipt.Status = StatusNoop
	}
	return writeReceiptAt(manifestPath, receipt)
}

func validateOptions(options Options) error {
	if strings.TrimSpace(options.DBPath) == "" {
		return errors.New("--db is required")
	}
	if strings.TrimSpace(options.ManifestPath) == "" {
		return errors.New("--manifest is required")
	}
	if options.Mode != ModeDryRun && options.Mode != ModeApply {
		return fmt.Errorf("--mode must be %q or %q", ModeDryRun, ModeApply)
	}
	if options.Mode == ModeApply && strings.TrimSpace(options.PriorDryRunManifestPath) == "" {
		return errors.New("--dry-run-receipt is required in apply mode")
	}
	if options.Mode == ModeDryRun && strings.TrimSpace(options.PriorDryRunManifestPath) != "" {
		return errors.New("--dry-run-receipt is only valid in apply mode")
	}
	return nil
}

func resolvePaths(options Options) (string, string, error) {
	dbPath, err := filepath.Abs(filepath.Clean(options.DBPath))
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(dbPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("--db must name a regular SQLite file")
	}
	manifestPath, err := filepath.Abs(filepath.Clean(options.ManifestPath))
	if err != nil {
		return "", "", err
	}
	if samePath(dbPath, manifestPath) {
		return "", "", errors.New("--manifest must not replace --db")
	}
	if options.Mode == ModeApply && strings.TrimSpace(options.PriorDryRunManifestPath) != "" {
		prior, err := filepath.Abs(filepath.Clean(options.PriorDryRunManifestPath))
		if err != nil {
			return "", "", err
		}
		if samePath(dbPath, prior) {
			return "", "", errors.New("--dry-run-receipt must not replace --db")
		}
		if samePath(manifestPath, prior) {
			return "", "", errors.New("--manifest must not replace --dry-run-receipt")
		}
	}
	return dbPath, manifestPath, nil
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	if strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs)) {
		return true
	}
	leftInfo, leftErr := os.Stat(leftAbs)
	rightInfo, rightErr := os.Stat(rightAbs)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func hashDatabaseFile(path string) (string, error) {
	for _, suffix := range []string{"-wal", "-journal"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Size() > 0 {
			return "", fmt.Errorf("SQLite snapshot has non-empty %s sidecar", suffix)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect SQLite snapshot sidecar: %w", err)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyStableDatabaseFile(path, expected string) error {
	got, err := hashDatabaseFile(path)
	if err != nil {
		return err
	}
	if got != expected {
		return errors.New("SQLite source changed during migration planning")
	}
	return nil
}

func openReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout%3d5000"}).String()
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
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = db.Close()
		if err == nil {
			err = errors.New("SQLite source is not query-only")
		}
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func openWritable(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func writeFailureReceipt(options Options, receipt Receipt, code string, cause error) (Receipt, error) {
	path, err := filepath.Abs(filepath.Clean(options.ManifestPath))
	if err != nil {
		return receipt, fmt.Errorf("%s: %w", code, cause)
	}
	return writeFailureReceiptAt(path, receipt, code, cause)
}

func blockedReceipt(receipt Receipt, code string, cause error) Receipt {
	receipt.Status = StatusBlocked
	receipt.ErrorCode = code
	receipt.Errors = []string{boundError(cause)}
	return receipt
}

func writeFailureReceiptAt(path string, receipt Receipt, code string, cause error) (Receipt, error) {
	receipt = blockedReceipt(receipt, code, cause)
	if _, writeErr := writeReceiptAt(path, receipt); writeErr != nil {
		return receipt, fmt.Errorf("%s: %w; write receipt: %v", code, cause, writeErr)
	}
	return receipt, cause
}

func writeReceiptAt(path string, receipt Receipt) (Receipt, error) {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return receipt, err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return receipt, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return receipt, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return receipt, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return receipt, err
	}
	if err := file.Close(); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func boundError(err error) string {
	if err == nil {
		return "migration failed"
	}
	value := strings.TrimSpace(err.Error())
	runes := []rune(value)
	if len(runes) > maxManifestErrorRunes {
		value = string(runes[:maxManifestErrorRunes])
	}
	if value == "" {
		return "migration failed"
	}
	return value
}

func readReceipt(path string) (Receipt, error) {
	file, err := os.Open(path)
	if err != nil {
		return Receipt{}, err
	}
	defer file.Close()
	var receipt Receipt
	decoder := json.NewDecoder(io.LimitReader(file, 128*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Receipt{}, errors.New("dry-run receipt has trailing data")
	}
	return receipt, nil
}

func validatePriorReceipt(prior, current Receipt) error {
	if prior.SchemaVersion != ManifestSchemaVersion || prior.Mode != ModeDryRun || prior.Status != StatusReady {
		return errors.New("prior receipt is not a ready dry-run receipt")
	}
	if prior.InputSHA256 == "" || prior.InputSHA256 != current.InputSHA256 {
		return errors.New("prior dry-run input hash does not match database")
	}
	if prior.PlanSHA256 == "" || prior.PlanSHA256 != current.PlanSHA256 {
		return errors.New("prior dry-run plan hash does not match database")
	}
	if prior.OutputSHA256 != "" {
		return errors.New("prior dry-run receipt unexpectedly has output hash")
	}
	if prior.Before != current.Before || prior.After != current.After {
		return errors.New("prior dry-run counts do not match database")
	}
	return nil
}

type migrationPlan struct {
	receipts     []receiptRow
	outbox       []outboxRow
	recalls      []recallRow
	items        []recallItemRow
	injections   []injectionRow
	messages     []messageRow
	messageIDs   map[string]struct{}
	itemIDs      map[string]struct{}
	injectionIDs map[string]struct{}
	before       Counts
	after        Counts
	changed      bool
}

type messageRow struct {
	id            string
	speaker       string
	oldMetaJSON   string
	newMetaJSON   string
	oldTurn       string
	canonicalTurn modulecore.TurnID
}

type identityMapping struct {
	oldTurn  string
	oldTrace string
	turn     modulecore.TurnID
	trace    modulecore.TraceID
	root     modulecore.TaskID
	current  bool
}

func identityPair(turnID, traceID string) string {
	return turnID + "\x00" + traceID
}

type receiptRow struct {
	oldTurn, oldTrace, oldRoot    string
	payloadHash, sessionID        string
	threadID, threadKind          string
	threadSeq                     int64
	closedID, closedKind          string
	closedSeq                     int64
	userMessageID, agentMessageID string
	status, resultJSON            string
	createdAt, updatedAt          string
	mapping                       identityMapping
	rewrittenResultJSON           string
}

type outboxRow struct {
	oldTurn, oldTrace, oldRoot string
	target, sessionID          string
	threadID, threadKind       string
	threadSeq                  int64
	closedID, closedKind       string
	closedSeq                  int64
	payloadHash, payloadJSON   string
	status, leaseToken         string
	leaseExpiresAt             sql.NullString
	attempts                   int64
	lastError, createdAt       string
	updatedAt                  string
	mapping                    identityMapping
	rewrittenPayloadJSON       string
}

type recallRow struct {
	oldTrace, oldTurn, oldRoot                          string
	ownerID, chatID                                     string
	persona, route                                      string
	userMessageHash                                     string
	queryTextRedacted                                   string
	createdAt, modelID                                  string
	promptVersion, policyVersion                        string
	totalCandidates, injectedCount, totalInjectedTokens int64
	status                                              string
	mapping                                             identityMapping
	linked                                              bool
}

type recallItemRow struct {
	itemID, oldTrace                                         string
	layer, memoryID, sourceID, sourceURL, sourceType, status string
	score, relevance, recency, confidence, sourceTrust       float64
	reason                                                   string
	injected                                                 int64
	promptSection                                            string
	tokenCount                                               int64
	sensitivity, memoryState, rawOrSummary                   string
	retrievedAt, publishedAt                                 sql.NullString
	eventID, summary, kind                                   string
	newTrace                                                 string
}

type injectionRow struct {
	injectionID, oldTrace, promptSection string
	orderIndex                           int64
	itemIDs                              string
	tokenCount                           int64
	redactionLevel, createdAt            string
	newTrace                             string
}

type tableColumnSet map[string]struct{}

func (columns tableColumnSet) has(name string) bool {
	_, ok := columns[name]
	return ok
}

func readPlan(ctx context.Context, path string) (migrationPlan, error) {
	db, err := openReadOnly(ctx, path)
	if err != nil {
		return migrationPlan{}, err
	}
	defer db.Close()
	return readPlanFromDB(ctx, db)
}

func readPlanFromDB(ctx context.Context, db *sql.DB) (migrationPlan, error) {
	if db == nil {
		return migrationPlan{}, errors.New("SQLite database is required")
	}
	plan := migrationPlan{
		messageIDs:   make(map[string]struct{}),
		itemIDs:      make(map[string]struct{}),
		injectionIDs: make(map[string]struct{}),
	}
	receiptColumns, err := inspectTable(ctx, db, "conversation_turn_receipt")
	if err != nil {
		return plan, err
	}
	outboxColumns, err := inspectTable(ctx, db, "conversation_turn_outbox")
	if err != nil {
		return plan, err
	}
	recallColumns, err := inspectTable(ctx, db, "recall_trace")
	if err != nil {
		return plan, err
	}
	itemColumns, err := inspectTable(ctx, db, "recall_trace_item")
	if err != nil {
		return plan, err
	}
	injectionColumns, err := inspectTable(ctx, db, "prompt_injection_event")
	if err != nil {
		return plan, err
	}
	if _, err := inspectTable(ctx, db, "l1_memory_event"); err != nil {
		return plan, err
	}

	if err := requireColumns("conversation_turn_receipt", receiptColumns,
		"turn_id", "trace_id", "payload_sha256", "session_id", "thread_id", "thread_seq", "thread_kind",
		"closed_thread_id", "closed_thread_seq", "closed_thread_kind", "user_message_id", "agent_message_id",
		"status", "result_json", "created_at", "updated_at"); err != nil {
		return plan, err
	}
	if err := requireColumns("conversation_turn_outbox", outboxColumns,
		"turn_id", "target", "session_id", "thread_id", "thread_seq", "thread_kind",
		"closed_thread_id", "closed_thread_seq", "closed_thread_kind", "payload_sha256", "payload_json",
		"status", "lease_token", "lease_expires_at", "attempts", "last_error", "created_at", "updated_at"); err != nil {
		return plan, err
	}
	if err := requireColumns("recall_trace", recallColumns,
		"trace_id", "turn_id", "chat_id", "persona", "route", "user_message_hash", "query_text_redacted",
		"created_at", "model_id", "prompt_version", "recall_policy_version", "total_candidates", "injected_count",
		"total_injected_tokens", "status"); err != nil {
		return plan, err
	}
	if err := requireColumns("recall_trace_item", itemColumns,
		"item_id", "trace_id", "layer", "memory_id", "source_id", "source_url", "source_type", "status",
		"score", "relevance", "recency", "confidence", "source_trust", "reason", "injected", "prompt_section",
		"token_count", "sensitivity", "memory_state", "is_raw_or_summary", "retrieved_at", "published_at", "event_id",
		"summary", "kind"); err != nil {
		return plan, err
	}
	if err := requireColumns("prompt_injection_event", injectionColumns,
		"injection_id", "trace_id", "prompt_section", "order_index", "item_ids", "token_count", "redaction_level", "created_at"); err != nil {
		return plan, err
	}

	if err := readReceiptRows(ctx, db, receiptColumns, &plan); err != nil {
		return plan, err
	}
	if err := buildReceiptMappings(&plan); err != nil {
		return plan, err
	}
	if err := readOutboxRows(ctx, db, outboxColumns, &plan); err != nil {
		return plan, err
	}
	if err := readRecallRows(ctx, db, recallColumns, &plan); err != nil {
		return plan, err
	}
	if err := readRecallChildRows(ctx, db, itemColumns, injectionColumns, &plan); err != nil {
		return plan, err
	}
	if err := readExistingMessageIDs(ctx, db, &plan); err != nil {
		return plan, err
	}
	if err := readReceiptOwnedMessageRows(ctx, db, &plan); err != nil {
		return plan, err
	}
	if err := validateMigrationPlan(&plan); err != nil {
		return plan, err
	}
	plan.before = Counts{
		Receipts: len(plan.receipts), Outbox: len(plan.outbox), RecallTraces: len(plan.recalls),
		RecallTraceItems: len(plan.items), PromptInjections: len(plan.injections),
		ReceiptLinkedRecall: countLinkedRecall(plan.recalls), UnlinkedRecall: len(plan.recalls) - countLinkedRecall(plan.recalls),
		ExistingMessageIDs: len(plan.messageIDs),
	}
	plan.after = plan.before
	return plan, nil
}

func inspectTable(ctx context.Context, db *sql.DB, table string) (tableColumnSet, error) {
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 1 {
		return nil, fmt.Errorf("required table %s is missing", table)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := tableColumnSet{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func requireColumns(table string, columns tableColumnSet, names ...string) error {
	for _, name := range names {
		if !columns.has(name) {
			return fmt.Errorf("table %s is missing required column %s", table, name)
		}
	}
	return nil
}

func optionalColumn(columns tableColumnSet, name string) string {
	if columns.has(name) {
		return name
	}
	return "'' AS " + name
}

func boundedRowCount(ctx context.Context, db *sql.DB, table string) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		return 0, err
	}
	if count < 0 || count > maxMigrationRows {
		return 0, fmt.Errorf("table %s exceeds migration row bound", table)
	}
	return count, nil
}

func readReceiptRows(ctx context.Context, db *sql.DB, columns tableColumnSet, plan *migrationPlan) error {
	count, err := boundedRowCount(ctx, db, "conversation_turn_receipt")
	if err != nil {
		return err
	}
	query := `SELECT turn_id, trace_id, ` + optionalColumn(columns, "root_task_id") + `,
	payload_sha256, session_id, thread_id, thread_seq, thread_kind,
	closed_thread_id, closed_thread_seq, closed_thread_kind, user_message_id, agent_message_id,
	status, result_json, created_at, updated_at
FROM conversation_turn_receipt ORDER BY rowid ASC`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	plan.receipts = make([]receiptRow, 0, count)
	for rows.Next() {
		var row receiptRow
		if err := rows.Scan(&row.oldTurn, &row.oldTrace, &row.oldRoot, &row.payloadHash, &row.sessionID,
			&row.threadID, &row.threadSeq, &row.threadKind, &row.closedID, &row.closedSeq, &row.closedKind,
			&row.userMessageID, &row.agentMessageID, &row.status, &row.resultJSON, &row.createdAt, &row.updatedAt); err != nil {
			return err
		}
		if len(row.resultJSON) > maxResultJSONBytes {
			return errors.New("conversation turn result_json exceeds migration bound")
		}
		plan.receipts = append(plan.receipts, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !columns.has("root_task_id") {
		plan.changed = true
	}
	return nil
}

func buildReceiptMappings(plan *migrationPlan) error {
	byOldTurn := make(map[string]*receiptRow, len(plan.receipts))
	byNewTurn := make(map[string]*receiptRow, len(plan.receipts))
	byOldTrace := make(map[string]*receiptRow, len(plan.receipts))
	byNewTrace := make(map[string]*receiptRow, len(plan.receipts))
	byNewRoot := make(map[string]*receiptRow, len(plan.receipts))
	for index := range plan.receipts {
		row := &plan.receipts[index]
		mapping, err := mapReceiptIdentity(row.oldTurn, row.oldTrace, row.oldRoot)
		if err != nil {
			return fmt.Errorf("receipt %q identity: %w", row.oldTurn, err)
		}
		row.mapping = mapping
		if _, exists := byOldTurn[row.oldTurn]; exists {
			return fmt.Errorf("duplicate receipt turn_id %q", row.oldTurn)
		}
		if _, exists := byNewTurn[string(mapping.turn)]; exists {
			return fmt.Errorf("mapped receipt turn_id collision for %q", row.oldTurn)
		}
		if _, exists := byOldTrace[row.oldTrace]; exists {
			return fmt.Errorf("duplicate receipt trace_id %q", row.oldTrace)
		}
		if _, exists := byNewTrace[string(mapping.trace)]; exists {
			return fmt.Errorf("mapped receipt trace_id collision for %q", row.oldTrace)
		}
		if _, exists := byNewRoot[string(mapping.root)]; exists {
			return fmt.Errorf("mapped receipt root_task_id collision for %q", row.oldTurn)
		}
		byOldTurn[row.oldTurn] = row
		byNewTurn[string(mapping.turn)] = row
		byOldTrace[row.oldTrace] = row
		byNewTrace[string(mapping.trace)] = row
		byNewRoot[string(mapping.root)] = row
		row.rewrittenResultJSON, err = rewriteReceiptResult(row.resultJSON, row)
		if err != nil {
			return fmt.Errorf("receipt %q result_json: %w", row.oldTurn, err)
		}
		if !mapping.current || !jsonValuesEqual(row.resultJSON, row.rewrittenResultJSON) {
			plan.changed = true
		}
	}
	return nil
}

func mapReceiptIdentity(oldTurn, oldTrace, oldRoot string) (identityMapping, error) {
	if strings.TrimSpace(oldTurn) == "" || strings.TrimSpace(oldTrace) == "" {
		return identityMapping{}, errors.New("turn_id and trace_id are required")
	}
	turn := modulecore.TurnID(oldTurn)
	trace := modulecore.TraceID(oldTrace)
	root := modulecore.TaskID(oldRoot)
	turnOK := turn.Validate() == nil
	traceOK := trace.Validate() == nil
	rootOK := root.Validate() == nil
	if turnOK && traceOK && rootOK {
		return identityMapping{oldTurn: oldTurn, oldTrace: oldTrace, turn: turn, trace: trace, root: root, current: true}, nil
	}
	if turnOK || traceOK || rootOK {
		return identityMapping{}, errors.New("partially canonical identity cannot be migrated")
	}
	newTurn, err := migrationID(modulecore.CanonicalTurnID, "conversation_turn_receipt", "turn_id", oldTurn)
	if err != nil {
		return identityMapping{}, err
	}
	newTrace, err := migrationID(modulecore.CanonicalTraceID, "conversation_turn_receipt", "trace_id", oldTrace)
	if err != nil {
		return identityMapping{}, err
	}
	newRoot, err := migrationID(modulecore.CanonicalTaskID, "conversation_turn_receipt", "turn_id", oldTurn)
	if err != nil {
		return identityMapping{}, err
	}
	return identityMapping{oldTurn: oldTurn, oldTrace: oldTrace, turn: modulecore.TurnID(newTurn), trace: modulecore.TraceID(newTrace), root: modulecore.TaskID(newRoot)}, nil
}

func migrationID(kind modulecore.CanonicalIDType, table, field, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("legacy identity value is required")
	}
	id, err := modulecore.NewMigrationID(kind, table, field, value)
	if err != nil {
		return "", err
	}
	return id, nil
}

func rewriteReceiptResult(raw string, row *receiptRow) (string, error) {
	object, err := decodeJSONObject(raw, maxResultJSONBytes)
	if err != nil {
		return "", err
	}
	if err := validateResultRelationalFields(object, row); err != nil {
		return "", err
	}
	if err := validateResultMessageIDs(object, row.userMessageID, row.agentMessageID); err != nil {
		return "", err
	}
	setJSONString(object, "turn_id", string(row.mapping.turn))
	setJSONString(object, "trace_id", string(row.mapping.trace))
	setJSONString(object, "root_task_id", string(row.mapping.root))
	setJSONString(object, "session_id", row.sessionID)
	setJSONString(object, "user_message_id", row.userMessageID)
	setJSONString(object, "agent_message_id", row.agentMessageID)
	setJSONString(object, "payload_sha256", row.payloadHash)
	setJSONString(object, "status", row.status)
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxResultJSONBytes {
		return "", errors.New("rewritten result_json exceeds migration bound")
	}
	return string(encoded), nil
}

func validateResultRelationalFields(object map[string]json.RawMessage, row *receiptRow) error {
	if row == nil {
		return errors.New("receipt row is required")
	}
	fields := map[string]string{
		"turn_id":        row.oldTurn,
		"trace_id":       row.oldTrace,
		"session_id":     row.sessionID,
		"payload_sha256": row.payloadHash,
		"status":         row.status,
	}
	for key, expected := range fields {
		var actual string
		if err := json.Unmarshal(object[key], &actual); err != nil || actual != expected {
			return fmt.Errorf("result_json %s does not match receipt", key)
		}
	}
	return nil
}

func decodeJSONObject(raw string, maxBytes int) (map[string]json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" || len(raw) > maxBytes {
		return nil, errors.New("JSON object is missing or exceeds migration bound")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("JSON object is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON object has trailing data")
	}
	return object, nil
}

func jsonValuesEqual(left, right string) bool {
	var leftValue, rightValue any
	if json.Unmarshal([]byte(left), &leftValue) != nil || json.Unmarshal([]byte(right), &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func setJSONString(object map[string]json.RawMessage, key, value string) {
	encoded, _ := json.Marshal(value)
	object[key] = encoded
}

func validateResultMessageIDs(object map[string]json.RawMessage, userID, agentID string) error {
	var existingUser, existingAgent string
	if err := json.Unmarshal(object["user_message_id"], &existingUser); err != nil || existingUser != userID {
		return errors.New("result_json user_message_id does not match receipt")
	}
	if err := json.Unmarshal(object["agent_message_id"], &existingAgent); err != nil || existingAgent != agentID {
		return errors.New("result_json agent_message_id does not match receipt")
	}
	var messageIDs []string
	if err := json.Unmarshal(object["message_ids"], &messageIDs); err != nil || len(messageIDs) != 2 || messageIDs[0] != userID || messageIDs[1] != agentID {
		return errors.New("result_json message_ids do not match receipt")
	}
	if modulecore.MessageID(userID).Validate() != nil || modulecore.MessageID(agentID).Validate() != nil || userID == agentID {
		return errors.New("receipt message IDs are not canonical and distinct")
	}
	return nil
}

func readOutboxRows(ctx context.Context, db *sql.DB, columns tableColumnSet, plan *migrationPlan) error {
	count, err := boundedRowCount(ctx, db, "conversation_turn_outbox")
	if err != nil {
		return err
	}
	query := `SELECT turn_id, ` + optionalColumn(columns, "trace_id") + `, ` + optionalColumn(columns, "root_task_id") + `,
	target, session_id, thread_id, thread_seq, thread_kind,
	closed_thread_id, closed_thread_seq, closed_thread_kind, payload_sha256, payload_json,
	status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at
FROM conversation_turn_outbox ORDER BY rowid ASC`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	plan.outbox = make([]outboxRow, 0, count)
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.oldTurn, &row.oldTrace, &row.oldRoot, &row.target, &row.sessionID,
			&row.threadID, &row.threadSeq, &row.threadKind, &row.closedID, &row.closedSeq, &row.closedKind,
			&row.payloadHash, &row.payloadJSON, &row.status, &row.leaseToken, &row.leaseExpiresAt,
			&row.attempts, &row.lastError, &row.createdAt, &row.updatedAt); err != nil {
			return err
		}
		if len(row.payloadJSON) > maxOutboxJSONBytes {
			return errors.New("conversation turn outbox payload_json exceeds migration bound")
		}
		plan.outbox = append(plan.outbox, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	byOldTurn := make(map[string]*receiptRow, len(plan.receipts))
	byNewTurn := make(map[string]*receiptRow, len(plan.receipts))
	for index := range plan.receipts {
		row := &plan.receipts[index]
		byOldTurn[row.oldTurn] = row
		byNewTurn[string(row.mapping.turn)] = row
	}
	for index := range plan.outbox {
		row := &plan.outbox[index]
		receipt := byOldTurn[row.oldTurn]
		if receipt == nil {
			receipt = byNewTurn[row.oldTurn]
		}
		if receipt == nil {
			return fmt.Errorf("outbox turn_id %q has no receipt", row.oldTurn)
		}
		if columns.has("trace_id") && row.oldTrace != string(receipt.mapping.trace) {
			return fmt.Errorf("outbox %q/%s trace_id does not match receipt", row.oldTurn, row.target)
		}
		if columns.has("root_task_id") && row.oldRoot != string(receipt.mapping.root) {
			return fmt.Errorf("outbox %q/%s root_task_id does not match receipt", row.oldTurn, row.target)
		}
		row.mapping = receipt.mapping
		row.rewrittenPayloadJSON, err = rewriteOutboxPayload(row, receipt)
		if err != nil {
			return fmt.Errorf("outbox %q/%s payload_json: %w", row.oldTurn, row.target, err)
		}
		if row.rewrittenPayloadJSON != row.payloadJSON || row.oldTurn != string(row.mapping.turn) ||
			row.oldTrace != string(row.mapping.trace) || row.oldRoot != string(row.mapping.root) || !columns.has("trace_id") || !columns.has("root_task_id") {
			plan.changed = true
		}
	}
	return nil
}

func rewriteOutboxPayload(row *outboxRow, receipt *receiptRow) (string, error) {
	if row == nil || receipt == nil {
		return "", errors.New("outbox and receipt rows are required")
	}
	if strings.TrimSpace(row.payloadJSON) == "" || len(row.payloadJSON) > maxOutboxJSONBytes {
		return "", errors.New("outbox payload is missing or exceeds migration bound")
	}
	var payload outboxPayloadJSON
	decoder := json.NewDecoder(bytes.NewReader([]byte(row.payloadJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return "", errors.New("outbox payload JSON is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("outbox payload JSON has trailing data")
	}
	if payload.Version != "rencrow.conversation_turn_outbox.v1" && payload.Version != OutboxPayloadVersion {
		return "", errors.New("unsupported outbox payload version")
	}
	if payload.TurnID != row.oldTurn && payload.TurnID != string(receipt.mapping.turn) {
		return "", errors.New("outbox payload turn_id does not match relational row")
	}
	if payload.Version == "rencrow.conversation_turn_outbox.v1" {
		if payload.TraceID != row.oldTurn {
			return "", errors.New("legacy outbox payload trace_id does not match legacy turn_id")
		}
	} else if payload.TraceID != string(receipt.mapping.trace) || payload.RootTaskID != string(receipt.mapping.root) {
		return "", errors.New("canonical outbox payload identities do not match receipt")
	}
	if payload.SessionID != row.sessionID || payload.OwnerID == "" || payload.ThreadID != row.threadID ||
		payload.ThreadSeq != row.threadSeq || payload.ThreadKind != row.threadKind || payload.Target != row.target ||
		payload.PayloadSHA256 != row.payloadHash || payload.UserMessageID != receipt.userMessageID ||
		payload.AgentMessageID != receipt.agentMessageID {
		return "", errors.New("outbox payload does not match relational row")
	}
	if err := validateThreadValues(row.threadID, row.threadSeq, row.threadKind, true); err != nil {
		return "", err
	}
	if err := validateThreadValues(row.closedID, row.closedSeq, row.closedKind, false); err != nil {
		return "", err
	}
	payload.Version = OutboxPayloadVersion
	payload.TurnID = string(receipt.mapping.turn)
	payload.TraceID = string(receipt.mapping.trace)
	payload.RootTaskID = string(receipt.mapping.root)
	payload.UserMessageID = receipt.userMessageID
	payload.AgentMessageID = receipt.agentMessageID
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > maxOutboxJSONBytes {
		return "", errors.New("rewritten outbox payload exceeds migration bound")
	}
	return string(encoded), nil
}

type outboxPayloadJSON struct {
	Version          string `json:"version"`
	TurnID           string `json:"turn_id"`
	TraceID          string `json:"trace_id"`
	RootTaskID       string `json:"root_task_id"`
	SessionID        string `json:"session_id"`
	OwnerID          string `json:"owner_id"`
	ThreadID         string `json:"thread_id"`
	ThreadSeq        int64  `json:"thread_seq"`
	ThreadKind       string `json:"thread_kind"`
	ClosedThreadID   string `json:"closed_thread_id,omitempty"`
	ClosedThreadSeq  int64  `json:"closed_thread_seq,omitempty"`
	ClosedThreadKind string `json:"closed_thread_kind,omitempty"`
	UserMessageID    string `json:"user_message_id"`
	AgentMessageID   string `json:"agent_message_id"`
	Target           string `json:"target"`
	PayloadSHA256    string `json:"payload_sha256"`
}

func readRecallRows(ctx context.Context, db *sql.DB, columns tableColumnSet, plan *migrationPlan) error {
	count, err := boundedRowCount(ctx, db, "recall_trace")
	if err != nil {
		return err
	}
	query := `SELECT trace_id, turn_id, ` + optionalColumn(columns, "root_task_id") + `,
	owner_id, chat_id, persona, route, user_message_hash, query_text_redacted,
	created_at, model_id, prompt_version, recall_policy_version, total_candidates,
	injected_count, total_injected_tokens, status
FROM recall_trace ORDER BY rowid ASC`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	plan.recalls = make([]recallRow, 0, count)
	for rows.Next() {
		var row recallRow
		if err := rows.Scan(&row.oldTrace, &row.oldTurn, &row.oldRoot, &row.ownerID, &row.chatID, &row.persona,
			&row.route, &row.userMessageHash, &row.queryTextRedacted, &row.createdAt, &row.modelID, &row.promptVersion,
			&row.policyVersion, &row.totalCandidates, &row.injectedCount, &row.totalInjectedTokens, &row.status); err != nil {
			return err
		}
		plan.recalls = append(plan.recalls, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !columns.has("root_task_id") {
		plan.changed = true
	}
	byOldTurn := make(map[string]*receiptRow, len(plan.receipts))
	byOldTrace := make(map[string]*receiptRow, len(plan.receipts))
	byNewTurn := make(map[string]*receiptRow, len(plan.receipts))
	byNewTrace := make(map[string]*receiptRow, len(plan.receipts))
	byOldPair := make(map[string]*receiptRow, len(plan.receipts))
	byNewPair := make(map[string]*receiptRow, len(plan.receipts))
	for index := range plan.receipts {
		row := &plan.receipts[index]
		byOldTurn[row.oldTurn] = row
		byOldTrace[row.oldTrace] = row
		byNewTurn[string(row.mapping.turn)] = row
		byNewTrace[string(row.mapping.trace)] = row
		byOldPair[identityPair(row.oldTurn, row.oldTrace)] = row
		byNewPair[identityPair(string(row.mapping.turn), string(row.mapping.trace))] = row
	}
	for index := range plan.recalls {
		row := &plan.recalls[index]
		receipt := byOldPair[identityPair(row.oldTurn, row.oldTrace)]
		if receipt == nil {
			receipt = byNewPair[identityPair(row.oldTurn, row.oldTrace)]
		}
		if receipt != nil {
			if modulecore.TurnID(row.oldTurn).Validate() == nil {
				if row.oldTurn != string(receipt.mapping.turn) || row.oldTrace != string(receipt.mapping.trace) || row.oldRoot != string(receipt.mapping.root) {
					return fmt.Errorf("linked recall %q canonical identity does not match receipt", row.oldTrace)
				}
			} else if modulecore.TraceID(row.oldTrace).Validate() == nil || modulecore.TaskID(row.oldRoot).Validate() == nil {
				return fmt.Errorf("linked recall %q has partially canonical identity", row.oldTrace)
			}
			row.linked = true
			row.mapping = receipt.mapping
			if row.oldTrace != string(row.mapping.trace) || row.oldTurn != string(row.mapping.turn) || row.oldRoot != string(row.mapping.root) {
				plan.changed = true
			}
			continue
		}
		if _, turnMatch := byOldTurn[row.oldTurn]; turnMatch {
			return fmt.Errorf("recall %q matches a receipt turn without its exact trace", row.oldTrace)
		}
		if _, turnMatch := byNewTurn[row.oldTurn]; turnMatch {
			return fmt.Errorf("recall %q matches a canonical receipt turn without its exact trace", row.oldTrace)
		}
		if _, traceMatch := byOldTrace[row.oldTrace]; traceMatch {
			return fmt.Errorf("recall %q matches a receipt trace without its exact turn", row.oldTrace)
		}
		if _, traceMatch := byNewTrace[row.oldTrace]; traceMatch {
			return fmt.Errorf("recall %q matches a canonical receipt trace without its exact turn", row.oldTrace)
		}
		mapping, err := mapUnlinkedRecallIdentity(row.oldTrace, row.oldTurn, row.oldRoot)
		if err != nil {
			return fmt.Errorf("unlinked recall %q identity: %w", row.oldTrace, err)
		}
		row.mapping = mapping
		if !mapping.current {
			plan.changed = true
		}
	}
	return nil
}

func mapUnlinkedRecallIdentity(oldTrace, oldTurn, oldRoot string) (identityMapping, error) {
	if strings.TrimSpace(oldTrace) == "" || strings.TrimSpace(oldTurn) == "" {
		return identityMapping{}, errors.New("trace_id and turn_id are required")
	}
	trace := modulecore.TraceID(oldTrace)
	turn := modulecore.TurnID(oldTurn)
	root := modulecore.TaskID(oldRoot)
	traceOK := trace.Validate() == nil
	turnOK := turn.Validate() == nil
	rootOK := root.Validate() == nil
	if traceOK && turnOK && rootOK {
		return identityMapping{oldTurn: oldTurn, oldTrace: oldTrace, turn: turn, trace: trace, root: root, current: true}, nil
	}
	if traceOK || turnOK || rootOK {
		return identityMapping{}, errors.New("partially canonical identity cannot be migrated")
	}
	newTurn, err := migrationID(modulecore.CanonicalTurnID, "recall_trace", "turn_id", oldTurn)
	if err != nil {
		return identityMapping{}, err
	}
	newTrace, err := migrationID(modulecore.CanonicalTraceID, "recall_trace", "trace_id", oldTrace)
	if err != nil {
		return identityMapping{}, err
	}
	newRoot, err := migrationID(modulecore.CanonicalTaskID, "recall_trace", "turn_id", oldTurn)
	if err != nil {
		return identityMapping{}, err
	}
	return identityMapping{oldTurn: oldTurn, oldTrace: oldTrace, turn: modulecore.TurnID(newTurn), trace: modulecore.TraceID(newTrace), root: modulecore.TaskID(newRoot)}, nil
}

func readRecallChildRows(ctx context.Context, db *sql.DB, itemColumns, injectionColumns tableColumnSet, plan *migrationPlan) error {
	itemCount, err := boundedRowCount(ctx, db, "recall_trace_item")
	if err != nil {
		return err
	}
	itemRows, err := db.QueryContext(ctx, `SELECT item_id, trace_id, layer, memory_id, source_id, source_url, source_type, status,
	score, relevance, recency, confidence, source_trust, reason, injected, prompt_section, token_count,
	sensitivity, memory_state, is_raw_or_summary, retrieved_at, published_at, event_id, summary, kind
FROM recall_trace_item ORDER BY rowid ASC`)
	if err != nil {
		return err
	}
	defer itemRows.Close()
	plan.items = make([]recallItemRow, 0, itemCount)
	for itemRows.Next() {
		var row recallItemRow
		if err := itemRows.Scan(&row.itemID, &row.oldTrace, &row.layer, &row.memoryID, &row.sourceID, &row.sourceURL,
			&row.sourceType, &row.status, &row.score, &row.relevance, &row.recency, &row.confidence, &row.sourceTrust,
			&row.reason, &row.injected, &row.promptSection, &row.tokenCount, &row.sensitivity, &row.memoryState,
			&row.rawOrSummary, &row.retrievedAt, &row.publishedAt, &row.eventID, &row.summary, &row.kind); err != nil {
			return err
		}
		plan.items = append(plan.items, row)
	}
	if err := itemRows.Err(); err != nil {
		return err
	}
	injectionCount, err := boundedRowCount(ctx, db, "prompt_injection_event")
	if err != nil {
		return err
	}
	injectionRows, err := db.QueryContext(ctx, `SELECT injection_id, trace_id, prompt_section, order_index, item_ids,
	token_count, redaction_level, created_at FROM prompt_injection_event ORDER BY rowid ASC`)
	if err != nil {
		return err
	}
	defer injectionRows.Close()
	plan.injections = make([]injectionRow, 0, injectionCount)
	for injectionRows.Next() {
		var row injectionRow
		if err := injectionRows.Scan(&row.injectionID, &row.oldTrace, &row.promptSection, &row.orderIndex, &row.itemIDs,
			&row.tokenCount, &row.redactionLevel, &row.createdAt); err != nil {
			return err
		}
		plan.injections = append(plan.injections, row)
	}
	if err := injectionRows.Err(); err != nil {
		return err
	}
	traceMap := make(map[string]string, len(plan.recalls)*2)
	for index := range plan.recalls {
		row := &plan.recalls[index]
		traceMap[row.oldTrace] = string(row.mapping.trace)
		traceMap[string(row.mapping.trace)] = string(row.mapping.trace)
	}
	for index := range plan.items {
		row := &plan.items[index]
		newTrace, ok := traceMap[row.oldTrace]
		if !ok {
			return fmt.Errorf("recall_trace_item %q references missing trace %q", row.itemID, row.oldTrace)
		}
		row.newTrace = newTrace
		if row.oldTrace != newTrace {
			plan.changed = true
		}
	}
	for index := range plan.injections {
		row := &plan.injections[index]
		newTrace, ok := traceMap[row.oldTrace]
		if !ok {
			return fmt.Errorf("prompt_injection_event %q references missing trace %q", row.injectionID, row.oldTrace)
		}
		row.newTrace = newTrace
		if row.oldTrace != newTrace {
			plan.changed = true
		}
	}
	if !itemColumns.has("trace_id") || !injectionColumns.has("trace_id") {
		return errors.New("recall child tables are missing required trace_id")
	}
	return nil
}

type messageMetadata struct {
	domain, messageID, turnID, speaker, from, to string
}

func readExistingMessageIDs(ctx context.Context, db *sql.DB, plan *migrationPlan) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM l1_memory_event WHERE id LIKE 'msg\_%' ESCAPE '\' ORDER BY id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if _, exists := plan.messageIDs[id]; exists {
			return fmt.Errorf("duplicate existing message ID %q", id)
		}
		if modulecore.MessageID(id).Validate() != nil {
			return fmt.Errorf("existing message ID %q is not canonical", id)
		}
		plan.messageIDs[id] = struct{}{}
	}
	return rows.Err()
}

func readReceiptOwnedMessageRows(ctx context.Context, db *sql.DB, plan *migrationPlan) error {
	seenIDs := make(map[string]string, len(plan.receipts)*2)
	plan.messages = make([]messageRow, 0, len(plan.receipts)*2)
	for index := range plan.receipts {
		receipt := &plan.receipts[index]
		user, err := readReceiptOwnedMessage(ctx, db, receiptMessageOwner{messageID: receipt.userMessageID, oldTurn: receipt.oldTurn, canonicalTurn: receipt.mapping.turn})
		if err != nil {
			return fmt.Errorf("receipt %q user message: %w", receipt.oldTurn, err)
		}
		agent, err := readReceiptOwnedMessage(ctx, db, receiptMessageOwner{messageID: receipt.agentMessageID, oldTurn: receipt.oldTurn, canonicalTurn: receipt.mapping.turn})
		if err != nil {
			return fmt.Errorf("receipt %q agent message: %w", receipt.oldTurn, err)
		}
		if err := validateReceiptMessagePair(user, agent); err != nil {
			return fmt.Errorf("receipt %q message metadata: %w", receipt.oldTurn, err)
		}
		for _, row := range []messageRow{user, agent} {
			if previous, exists := seenIDs[row.id]; exists {
				return fmt.Errorf("message ID %q is owned by receipts %q and %q", row.id, previous, receipt.oldTurn)
			}
			seenIDs[row.id] = receipt.oldTurn
			plan.messages = append(plan.messages, row)
			if _, exists := plan.messageIDs[row.id]; !exists {
				return fmt.Errorf("receipt %q references missing canonical message ID %q", receipt.oldTurn, row.id)
			}
			if row.oldMetaJSON != row.newMetaJSON {
				plan.changed = true
			}
		}
	}
	return nil
}

type receiptMessageOwner struct {
	messageID     string
	oldTurn       string
	canonicalTurn modulecore.TurnID
}

func readReceiptOwnedMessage(ctx context.Context, db *sql.DB, owner receiptMessageOwner) (messageRow, error) {
	if modulecore.MessageID(owner.messageID).Validate() != nil || owner.messageID == "" {
		return messageRow{}, errors.New("message ID is not canonical")
	}
	rows, err := db.QueryContext(ctx, `SELECT id, speaker, meta_json FROM l1_memory_event WHERE id = ?`, owner.messageID)
	if err != nil {
		return messageRow{}, err
	}
	defer rows.Close()
	var row messageRow
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return messageRow{}, errors.New("message ID has duplicate rows")
		}
		if err := rows.Scan(&row.id, &row.speaker, &row.oldMetaJSON); err != nil {
			return messageRow{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return messageRow{}, err
	}
	if count != 1 {
		return messageRow{}, errors.New("message row is missing")
	}
	if len(row.oldMetaJSON) > maxMessageMetaBytes {
		return messageRow{}, errors.New("message metadata exceeds migration bound")
	}
	metadata, err := decodeMessageMetadata(row.oldMetaJSON, row.id, owner.oldTurn)
	if err != nil {
		return messageRow{}, err
	}
	if row.speaker != metadata.speaker {
		return messageRow{}, errors.New("message speaker does not match metadata")
	}
	row.oldTurn = owner.oldTurn
	row.canonicalTurn = owner.canonicalTurn
	if metadata.turnID == string(owner.canonicalTurn) {
		row.newMetaJSON = row.oldMetaJSON
		return row, nil
	}
	metadata.turnID = string(owner.canonicalTurn)
	value := map[string]string{
		"domain": metadata.domain, "message_id": metadata.messageID, "turn_id": metadata.turnID,
		"speaker": metadata.speaker, "from": metadata.from, "to": metadata.to,
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxMessageMetaBytes {
		return messageRow{}, errors.New("rewritten message metadata exceeds migration bound")
	}
	row.newMetaJSON = string(encoded)
	return row, nil
}

func decodeMessageMetadata(raw, rowID, oldTurn string) (messageMetadata, error) {
	if strings.TrimSpace(raw) == "" || len(raw) > maxMessageMetaBytes {
		return messageMetadata{}, errors.New("message metadata is missing or exceeds migration bound")
	}
	object, err := decodeStrictMessageObject(raw)
	if err != nil {
		return messageMetadata{}, err
	}
	keys := []string{"domain", "message_id", "turn_id", "speaker", "from", "to"}
	if len(object) != len(keys) {
		return messageMetadata{}, errors.New("message metadata must contain exactly six fields")
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		rawValue, ok := object[key]
		if !ok {
			return messageMetadata{}, fmt.Errorf("message metadata field %s is missing", key)
		}
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil || value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") || len([]rune(value)) > maxMessageMetaRunes {
			return messageMetadata{}, fmt.Errorf("message metadata field %s is invalid", key)
		}
		values[key] = value
	}
	if values["message_id"] != rowID || modulecore.MessageID(rowID).Validate() != nil {
		return messageMetadata{}, errors.New("message metadata message_id does not match row ID")
	}
	if values["turn_id"] != oldTurn {
		return messageMetadata{}, errors.New("message metadata turn_id does not match receipt")
	}
	return messageMetadata{domain: values["domain"], messageID: values["message_id"], turnID: values["turn_id"], speaker: values["speaker"], from: values["from"], to: values["to"]}, nil
}

func decodeStrictMessageObject(raw string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	start, err := decoder.Token()
	if err != nil {
		return nil, errors.New("message metadata is not a JSON object")
	}
	delim, ok := start.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("message metadata is not a JSON object")
	}
	object := make(map[string]json.RawMessage, 6)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return nil, errors.New("message metadata object key is invalid")
		}
		if _, exists := object[key]; exists {
			return nil, fmt.Errorf("message metadata field %s is duplicated", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("message metadata field value is invalid")
		}
		object[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, errors.New("message metadata object is incomplete")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("message metadata has trailing data")
	}
	return object, nil
}

func validateReceiptMessagePair(user, agent messageRow) error {
	userMeta, err := decodeMessageMetadata(user.oldMetaJSON, user.id, user.oldTurn)
	if err != nil {
		return err
	}
	agentMeta, err := decodeMessageMetadata(agent.oldMetaJSON, agent.id, agent.oldTurn)
	if err != nil {
		return err
	}
	if userMeta.speaker != string(domconv.SpeakerUser) || userMeta.from != string(domconv.SpeakerUser) {
		return errors.New("user message speaker shape is invalid")
	}
	canonicalAgent, ok := domconv.CanonicalChatAgentSpeaker(domconv.Speaker(agentMeta.speaker))
	if !ok || canonicalAgent != domconv.Speaker(agentMeta.speaker) || agentMeta.from != agentMeta.speaker || agentMeta.to != string(domconv.SpeakerUser) {
		return errors.New("agent message speaker shape is invalid")
	}
	if userMeta.to != agentMeta.speaker || userMeta.domain != agentMeta.domain {
		return errors.New("message pair metadata is inconsistent")
	}
	return nil
}

func validateMigrationPlan(plan *migrationPlan) error {
	if plan == nil {
		return errors.New("migration plan is required")
	}
	receiptsByOldTurn := make(map[string]*receiptRow, len(plan.receipts))
	receiptsByNewTurn := make(map[string]*receiptRow, len(plan.receipts))
	messageOwners := make(map[string]string, len(plan.receipts)*2)
	for index := range plan.receipts {
		row := &plan.receipts[index]
		if row.mapping.turn.Validate() != nil || row.mapping.trace.Validate() != nil || row.mapping.root.Validate() != nil ||
			row.mapping.turn == "" || row.mapping.trace == "" || row.mapping.root == "" {
			return fmt.Errorf("receipt %q mapped identity is invalid", row.oldTurn)
		}
		if modulecore.SessionID(row.sessionID).Validate() != nil {
			return fmt.Errorf("receipt %q session_id is not canonical", row.oldTurn)
		}
		if modulecore.MessageID(row.userMessageID).Validate() != nil || modulecore.MessageID(row.agentMessageID).Validate() != nil || row.userMessageID == row.agentMessageID {
			return fmt.Errorf("receipt %q message IDs are invalid", row.oldTurn)
		}
		for _, messageID := range []string{row.userMessageID, row.agentMessageID} {
			if _, exists := plan.messageIDs[messageID]; !exists {
				return fmt.Errorf("receipt %q references missing message ID %q", row.oldTurn, messageID)
			}
			if owner, exists := messageOwners[messageID]; exists {
				return fmt.Errorf("message ID %q is reused by receipts %q and %q", messageID, owner, row.oldTurn)
			}
			messageOwners[messageID] = row.oldTurn
		}
		if err := validateThreadValues(row.threadID, row.threadSeq, row.threadKind, true); err != nil {
			return fmt.Errorf("receipt %q thread: %w", row.oldTurn, err)
		}
		if err := validateThreadValues(row.closedID, row.closedSeq, row.closedKind, false); err != nil {
			return fmt.Errorf("receipt %q closed thread: %w", row.oldTurn, err)
		}
		if err := validateStatus(row.status, "receipt"); err != nil {
			return fmt.Errorf("receipt %q: %w", row.oldTurn, err)
		}
		if !validSHA256(row.payloadHash) {
			return fmt.Errorf("receipt %q payload hash is invalid", row.oldTurn)
		}
		if err := validateRewrittenResult(row.rewrittenResultJSON, row); err != nil {
			return fmt.Errorf("receipt %q result_json: %w", row.oldTurn, err)
		}
		receiptsByOldTurn[row.oldTurn] = row
		receiptsByNewTurn[string(row.mapping.turn)] = row
	}
	seenOutbox := make(map[string]struct{}, len(plan.outbox))
	for index := range plan.outbox {
		row := &plan.outbox[index]
		receipt := receiptsByOldTurn[row.oldTurn]
		if receipt == nil {
			receipt = receiptsByNewTurn[row.oldTurn]
		}
		if receipt == nil || row.mapping.turn != receipt.mapping.turn || row.mapping.trace != receipt.mapping.trace || row.mapping.root != receipt.mapping.root {
			return fmt.Errorf("outbox %q/%s is not bound to its receipt", row.oldTurn, row.target)
		}
		key := string(row.mapping.turn) + "\x00" + row.target
		if _, exists := seenOutbox[key]; exists {
			return fmt.Errorf("mapped outbox collision for %q/%s", row.oldTurn, row.target)
		}
		seenOutbox[key] = struct{}{}
		if row.target != "redis_projection" && row.target != "thread_followers" {
			return fmt.Errorf("outbox %q has unsupported target %q", row.oldTurn, row.target)
		}
		if err := validateStatus(row.status, "outbox"); err != nil {
			return fmt.Errorf("outbox %q/%s: %w", row.oldTurn, row.target, err)
		}
		if !validSHA256(row.payloadHash) {
			return fmt.Errorf("outbox %q/%s payload hash is invalid", row.oldTurn, row.target)
		}
	}
	seenRecallTrace := make(map[string]struct{}, len(plan.recalls))
	traceByOld := make(map[string]string, len(plan.recalls)*2)
	for index := range plan.recalls {
		row := &plan.recalls[index]
		if row.mapping.turn.Validate() != nil || row.mapping.trace.Validate() != nil || row.mapping.root.Validate() != nil {
			return fmt.Errorf("recall %q mapped identity is invalid", row.oldTrace)
		}
		if _, exists := seenRecallTrace[string(row.mapping.trace)]; exists {
			return fmt.Errorf("mapped recall trace collision for %q", row.oldTrace)
		}
		seenRecallTrace[string(row.mapping.trace)] = struct{}{}
		traceByOld[row.oldTrace] = string(row.mapping.trace)
		traceByOld[string(row.mapping.trace)] = string(row.mapping.trace)
	}
	seenItems := make(map[string]struct{}, len(plan.items))
	for index := range plan.items {
		row := &plan.items[index]
		if _, exists := seenItems[row.itemID]; exists {
			return fmt.Errorf("duplicate recall item_id %q", row.itemID)
		}
		seenItems[row.itemID] = struct{}{}
		if _, ok := seenRecallTrace[row.newTrace]; !ok {
			return fmt.Errorf("recall item %q maps to missing trace %q", row.itemID, row.newTrace)
		}
	}
	plan.itemIDs = seenItems
	seenInjections := make(map[string]struct{}, len(plan.injections))
	for index := range plan.injections {
		row := &plan.injections[index]
		if _, exists := seenInjections[row.injectionID]; exists {
			return fmt.Errorf("duplicate injection_id %q", row.injectionID)
		}
		seenInjections[row.injectionID] = struct{}{}
		if _, ok := seenRecallTrace[row.newTrace]; !ok {
			return fmt.Errorf("injection %q maps to missing trace %q", row.injectionID, row.newTrace)
		}
		var itemIDs []string
		if err := json.Unmarshal([]byte(row.itemIDs), &itemIDs); err != nil {
			return fmt.Errorf("injection %q item_ids JSON is invalid", row.injectionID)
		}
		for _, itemID := range itemIDs {
			if _, ok := seenItems[itemID]; !ok {
				return fmt.Errorf("injection %q references missing item %q", row.injectionID, itemID)
			}
		}
	}
	plan.injectionIDs = seenInjections
	return nil
}

func countLinkedRecall(recalls []recallRow) int {
	count := 0
	for _, row := range recalls {
		if row.linked {
			count++
		}
	}
	return count
}

func validateThreadValues(id string, seq int64, kind string, required bool) error {
	if id == "" {
		if required || seq != 0 || kind != "" {
			return errors.New("thread tuple is incomplete")
		}
		return nil
	}
	if modulecore.ThreadID(id).Validate() != nil || modulecore.ThreadSeq(seq).Validate() != nil || modulecore.ThreadKind(kind).Validate() != nil {
		return errors.New("thread tuple is not canonical")
	}
	return nil
}

func validateStatus(status, table string) error {
	switch table {
	case "receipt":
		switch status {
		case "completed", "partial", "failed":
			return nil
		}
	case "outbox":
		switch status {
		case "pending", "running", "completed", "failed":
			return nil
		}
	}
	return fmt.Errorf("%s status %q is invalid", table, status)
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateRewrittenResult(raw string, row *receiptRow) error {
	object, err := decodeJSONObject(raw, maxResultJSONBytes)
	if err != nil {
		return err
	}
	fields := map[string]string{}
	for _, key := range []string{"turn_id", "trace_id", "root_task_id", "session_id", "user_message_id", "agent_message_id", "payload_sha256", "status"} {
		var value string
		if err := json.Unmarshal(object[key], &value); err != nil {
			return fmt.Errorf("field %s is invalid", key)
		}
		fields[key] = value
	}
	if fields["turn_id"] != string(row.mapping.turn) || fields["trace_id"] != string(row.mapping.trace) || fields["root_task_id"] != string(row.mapping.root) ||
		fields["session_id"] != row.sessionID || fields["user_message_id"] != row.userMessageID || fields["agent_message_id"] != row.agentMessageID ||
		fields["payload_sha256"] != row.payloadHash || fields["status"] != row.status {
		return errors.New("result identity or relational binding mismatch")
	}
	return validateResultMessageIDs(object, row.userMessageID, row.agentMessageID)
}

type planFingerprint struct {
	Receipts   []receiptFingerprint `json:"receipts"`
	Outbox     []outboxFingerprint  `json:"outbox"`
	Recalls    []recallFingerprint  `json:"recalls"`
	Items      []childFingerprint   `json:"items"`
	Injections []childFingerprint   `json:"injections"`
	Messages   []messageFingerprint `json:"messages"`
}

type receiptFingerprint struct {
	OldTurn, OldTrace string
	Turn, Trace, Root string
	Result            string
}

type outboxFingerprint struct {
	OldTurn, Target   string
	Turn, Trace, Root string
	Payload           string
}

type recallFingerprint struct {
	OldTrace, OldTurn, OldRoot string
	Trace, Turn, Root          string
	Linked                     bool
}

type childFingerprint struct {
	ID, OldTrace, NewTrace, JSON string
}

type messageFingerprint struct {
	ID, OldMeta, NewMeta, OldTurn, CanonicalTurn string
}

func planHash(plan migrationPlan) string {
	fingerprint := planFingerprint{
		Receipts:   make([]receiptFingerprint, 0, len(plan.receipts)),
		Outbox:     make([]outboxFingerprint, 0, len(plan.outbox)),
		Recalls:    make([]recallFingerprint, 0, len(plan.recalls)),
		Items:      make([]childFingerprint, 0, len(plan.items)),
		Injections: make([]childFingerprint, 0, len(plan.injections)),
		Messages:   make([]messageFingerprint, 0, len(plan.messages)),
	}
	for _, row := range plan.receipts {
		fingerprint.Receipts = append(fingerprint.Receipts, receiptFingerprint{
			OldTurn: row.oldTurn, OldTrace: row.oldTrace, Turn: string(row.mapping.turn), Trace: string(row.mapping.trace),
			Root: string(row.mapping.root), Result: row.rewrittenResultJSON,
		})
	}
	for _, row := range plan.outbox {
		fingerprint.Outbox = append(fingerprint.Outbox, outboxFingerprint{
			OldTurn: row.oldTurn, Target: row.target, Turn: string(row.mapping.turn), Trace: string(row.mapping.trace),
			Root: string(row.mapping.root), Payload: row.rewrittenPayloadJSON,
		})
	}
	for _, row := range plan.recalls {
		fingerprint.Recalls = append(fingerprint.Recalls, recallFingerprint{
			OldTrace: row.oldTrace, OldTurn: row.oldTurn, OldRoot: row.oldRoot, Trace: string(row.mapping.trace),
			Turn: string(row.mapping.turn), Root: string(row.mapping.root), Linked: row.linked,
		})
	}
	for _, row := range plan.items {
		fingerprint.Items = append(fingerprint.Items, childFingerprint{ID: row.itemID, OldTrace: row.oldTrace, NewTrace: row.newTrace})
	}
	for _, row := range plan.injections {
		fingerprint.Injections = append(fingerprint.Injections, childFingerprint{ID: row.injectionID, OldTrace: row.oldTrace, NewTrace: row.newTrace, JSON: row.itemIDs})
	}
	for _, row := range plan.messages {
		fingerprint.Messages = append(fingerprint.Messages, messageFingerprint{ID: row.id, OldMeta: row.oldMetaJSON, NewMeta: row.newMetaJSON, OldTurn: row.oldTurn, CanonicalTurn: string(row.canonicalTurn)})
	}
	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func applyPlan(ctx context.Context, path string, plan migrationPlan, expectedHash string) error {
	if expectedHash != "" {
		if err := verifyStableDatabaseFile(path, expectedHash); err != nil {
			return err
		}
	}
	db, err := openWritable(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	if plan.changed {
		if err := rebuildCanonicalTables(ctx, tx, &plan); err != nil {
			return rollback(err)
		}
	}
	if err := applyMessageMetadataUpdates(ctx, tx, plan); err != nil {
		return rollback(err)
	}
	if err := verifyMigratedTransaction(ctx, tx, plan); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	return db.Close()
}

func applyMessageMetadataUpdates(ctx context.Context, tx *sql.Tx, plan migrationPlan) error {
	for _, row := range plan.messages {
		if row.oldMetaJSON == row.newMetaJSON {
			continue
		}
		result, err := tx.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = ? WHERE id = ? AND meta_json = ?`, row.newMetaJSON, row.id, row.oldMetaJSON)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("message %q metadata update affected %d rows", row.id, affected)
		}
	}
	return nil
}

var migrationTables = []string{
	"prompt_injection_event",
	"recall_trace_item",
	"conversation_turn_outbox",
	"recall_trace",
	"conversation_turn_receipt",
}

func rebuildCanonicalTables(ctx context.Context, tx *sql.Tx, plan *migrationPlan) error {
	backups := make(map[string]string, len(migrationTables))
	for _, table := range migrationTables {
		backup := table + "__turn_migration_old"
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", backup).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("migration staging table %s already exists", backup)
		}
		backups[table] = backup
	}
	for _, table := range migrationTables {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" RENAME TO "+backups[table]); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, canonicalReceiptDDL); err != nil {
		return err
	}
	for _, row := range plan.receipts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_turn_receipt(
	turn_id, payload_sha256, session_id, trace_id, root_task_id, thread_id, thread_seq, thread_kind,
	closed_thread_id, closed_thread_seq, closed_thread_kind, user_message_id, agent_message_id,
	status, result_json, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			row.mapping.turn, row.payloadHash, row.sessionID, row.mapping.trace, row.mapping.root, row.threadID, row.threadSeq, row.threadKind,
			row.closedID, row.closedSeq, row.closedKind, row.userMessageID, row.agentMessageID, row.status, row.rewrittenResultJSON, row.createdAt, row.updatedAt); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, canonicalRecallDDL); err != nil {
		return err
	}
	for _, row := range plan.recalls {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recall_trace(
	trace_id, owner_id, turn_id, root_task_id, chat_id, persona, route, user_message_hash, query_text_redacted,
	created_at, model_id, prompt_version, recall_policy_version, total_candidates, injected_count,
	total_injected_tokens, status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			row.mapping.trace, row.ownerID, row.mapping.turn, row.mapping.root, row.chatID, row.persona, row.route,
			row.userMessageHash, row.queryTextRedacted, row.createdAt, row.modelID, row.promptVersion, row.policyVersion,
			row.totalCandidates, row.injectedCount, row.totalInjectedTokens, row.status); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, canonicalOutboxDDL); err != nil {
		return err
	}
	for _, row := range plan.outbox {
		var lease any
		if row.leaseExpiresAt.Valid {
			lease = row.leaseExpiresAt.String
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_turn_outbox(
	turn_id, trace_id, root_task_id, target, session_id, thread_id, thread_seq, thread_kind,
	closed_thread_id, closed_thread_seq, closed_thread_kind, payload_sha256, payload_json, status,
	lease_token, lease_expires_at, attempts, last_error, created_at, updated_at)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			row.mapping.turn, row.mapping.trace, row.mapping.root, row.target, row.sessionID, row.threadID, row.threadSeq, row.threadKind,
			row.closedID, row.closedSeq, row.closedKind, row.payloadHash, row.rewrittenPayloadJSON, row.status,
			row.leaseToken, lease, row.attempts, row.lastError, row.createdAt, row.updatedAt); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, canonicalRecallItemDDL); err != nil {
		return err
	}
	for _, row := range plan.items {
		var retrieved, published any
		if row.retrievedAt.Valid {
			retrieved = row.retrievedAt.String
		}
		if row.publishedAt.Valid {
			published = row.publishedAt.String
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recall_trace_item(
	item_id, trace_id, layer, memory_id, source_id, source_url, source_type, status,
	score, relevance, recency, confidence, source_trust, reason, injected, prompt_section,
	token_count, sensitivity, memory_state, is_raw_or_summary, retrieved_at, published_at,
	event_id, summary, kind) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			row.itemID, row.newTrace, row.layer, row.memoryID, row.sourceID, row.sourceURL, row.sourceType, row.status,
			row.score, row.relevance, row.recency, row.confidence, row.sourceTrust, row.reason, row.injected, row.promptSection,
			row.tokenCount, row.sensitivity, row.memoryState, row.rawOrSummary, retrieved, published, row.eventID, row.summary, row.kind); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, canonicalInjectionDDL); err != nil {
		return err
	}
	for _, row := range plan.injections {
		if _, err := tx.ExecContext(ctx, `INSERT INTO prompt_injection_event(
	injection_id, trace_id, prompt_section, order_index, item_ids, token_count, redaction_level, created_at)
	VALUES(?,?,?,?,?,?,?,?)`, row.injectionID, row.newTrace, row.promptSection, row.orderIndex, row.itemIDs, row.tokenCount, row.redactionLevel, row.createdAt); err != nil {
			return err
		}
	}

	for _, table := range migrationTables {
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+backups[table]); err != nil {
			return err
		}
	}
	for _, index := range canonicalIndexes {
		if _, err := tx.ExecContext(ctx, index); err != nil {
			return err
		}
	}
	return nil
}

const canonicalReceiptDDL = `CREATE TABLE conversation_turn_receipt (
	turn_id TEXT PRIMARY KEY CHECK(length(turn_id) > 0),
	payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
	session_id TEXT NOT NULL CHECK(length(session_id) > 0),
	trace_id TEXT NOT NULL CHECK(length(trace_id) > 0),
	root_task_id TEXT NOT NULL CHECK(length(root_task_id) > 0),
	thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
	thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
	thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
	closed_thread_id TEXT NOT NULL DEFAULT '',
	closed_thread_seq INTEGER NOT NULL DEFAULT 0,
	closed_thread_kind TEXT NOT NULL DEFAULT '',
	user_message_id TEXT NOT NULL CHECK(length(user_message_id) > 0),
	agent_message_id TEXT NOT NULL CHECK(length(agent_message_id) > 0),
	status TEXT NOT NULL CHECK(status IN ('completed', 'partial', 'failed')),
	result_json TEXT NOT NULL CHECK(length(result_json) <= 65536),
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	CHECK((closed_thread_id = '' AND closed_thread_seq = 0 AND closed_thread_kind = '') OR
	(closed_thread_id <> '' AND closed_thread_seq > 0 AND closed_thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')))
)`

const canonicalRecallDDL = `CREATE TABLE recall_trace (
	trace_id TEXT PRIMARY KEY,
	owner_id TEXT NOT NULL DEFAULT '',
	turn_id TEXT NOT NULL,
	root_task_id TEXT NOT NULL CHECK(length(root_task_id) > 0),
	chat_id TEXT NOT NULL,
	persona TEXT NOT NULL,
	route TEXT NOT NULL DEFAULT '',
	user_message_hash TEXT NOT NULL DEFAULT '',
	query_text_redacted TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	model_id TEXT NOT NULL DEFAULT '',
	prompt_version TEXT NOT NULL DEFAULT '',
	recall_policy_version TEXT NOT NULL DEFAULT '',
	total_candidates INTEGER NOT NULL DEFAULT 0,
	injected_count INTEGER NOT NULL DEFAULT 0,
	total_injected_tokens INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT ''
)`

const canonicalOutboxDDL = `CREATE TABLE conversation_turn_outbox (
	turn_id TEXT NOT NULL CHECK(length(turn_id) > 0),
	trace_id TEXT NOT NULL CHECK(length(trace_id) > 0),
	root_task_id TEXT NOT NULL CHECK(length(root_task_id) > 0),
	target TEXT NOT NULL CHECK(target IN ('redis_projection', 'thread_followers')),
	session_id TEXT NOT NULL CHECK(length(session_id) > 0),
	thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
	thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
	thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
	closed_thread_id TEXT NOT NULL DEFAULT '',
	closed_thread_seq INTEGER NOT NULL DEFAULT 0,
	closed_thread_kind TEXT NOT NULL DEFAULT '',
	payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
	payload_json TEXT NOT NULL CHECK(length(payload_json) <= 8192),
	status TEXT NOT NULL CHECK(status IN ('pending', 'running', 'completed', 'failed')),
	lease_token TEXT NOT NULL DEFAULT '',
	lease_expires_at TIMESTAMP,
	attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
	last_error TEXT NOT NULL DEFAULT '' CHECK(last_error IN ('', 'invalid', 'conflict', 'unavailable', 'internal')),
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	PRIMARY KEY(turn_id, target),
	FOREIGN KEY(turn_id) REFERENCES conversation_turn_receipt(turn_id),
	CHECK((closed_thread_id = '' AND closed_thread_seq = 0 AND closed_thread_kind = '') OR
	(closed_thread_id <> '' AND closed_thread_seq > 0 AND closed_thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')))
)`

const canonicalRecallItemDDL = `CREATE TABLE recall_trace_item (
	item_id TEXT PRIMARY KEY,
	trace_id TEXT NOT NULL,
	layer TEXT NOT NULL,
	memory_id TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	source_url TEXT NOT NULL DEFAULT '',
	source_type TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	score REAL NOT NULL DEFAULT 0,
	relevance REAL NOT NULL DEFAULT 0,
	recency REAL NOT NULL DEFAULT 0,
	confidence REAL NOT NULL DEFAULT 0,
	source_trust REAL NOT NULL DEFAULT 0,
	reason TEXT NOT NULL DEFAULT '',
	injected INTEGER NOT NULL DEFAULT 0,
	prompt_section TEXT NOT NULL DEFAULT '',
	token_count INTEGER NOT NULL DEFAULT 0,
	sensitivity TEXT NOT NULL DEFAULT '',
	memory_state TEXT NOT NULL DEFAULT '',
	is_raw_or_summary TEXT NOT NULL DEFAULT '',
	retrieved_at TIMESTAMP,
	published_at TIMESTAMP,
	event_id TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '',
	FOREIGN KEY(trace_id) REFERENCES recall_trace(trace_id)
)`

const canonicalInjectionDDL = `CREATE TABLE prompt_injection_event (
	injection_id TEXT PRIMARY KEY,
	trace_id TEXT NOT NULL,
	prompt_section TEXT NOT NULL,
	order_index INTEGER NOT NULL,
	item_ids TEXT NOT NULL DEFAULT '[]',
	token_count INTEGER NOT NULL DEFAULT 0,
	redaction_level TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	FOREIGN KEY(trace_id) REFERENCES recall_trace(trace_id)
)`

var canonicalIndexes = []string{
	`CREATE INDEX idx_conversation_turn_receipt_session_created ON conversation_turn_receipt(session_id, created_at DESC)`,
	`CREATE INDEX idx_conversation_turn_outbox_claim ON conversation_turn_outbox(status, lease_expires_at, created_at, turn_id, target)`,
	`CREATE INDEX idx_recall_trace_chat_created ON recall_trace(chat_id, created_at DESC)`,
	`CREATE INDEX idx_recall_trace_owner_created ON recall_trace(owner_id, created_at DESC)`,
	`CREATE INDEX idx_recall_trace_item_trace ON recall_trace_item(trace_id)`,
	`CREATE INDEX idx_recall_trace_item_status ON recall_trace_item(status)`,
	`CREATE INDEX idx_prompt_injection_event_trace ON prompt_injection_event(trace_id, order_index)`,
}

func verifyMigratedTransaction(ctx context.Context, tx *sql.Tx, plan migrationPlan) error {
	for table, want := range map[string]int{
		"conversation_turn_receipt": len(plan.receipts), "conversation_turn_outbox": len(plan.outbox),
		"recall_trace": len(plan.recalls), "recall_trace_item": len(plan.items), "prompt_injection_event": len(plan.injections),
	} {
		var got int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("table %s row count changed from %d to %d", table, want, got)
		}
	}

	receipts := make(map[string]*receiptRow, len(plan.receipts))
	for index := range plan.receipts {
		receipts[string(plan.receipts[index].mapping.turn)] = &plan.receipts[index]
	}
	rows, err := tx.QueryContext(ctx, `SELECT turn_id, trace_id, root_task_id, user_message_id, agent_message_id, result_json FROM conversation_turn_receipt ORDER BY rowid ASC`)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(plan.receipts))
	for rows.Next() {
		var turnID, traceID, rootTaskID, userID, agentID, resultJSON string
		if err := rows.Scan(&turnID, &traceID, &rootTaskID, &userID, &agentID, &resultJSON); err != nil {
			rows.Close()
			return err
		}
		row := receipts[turnID]
		if row == nil || traceID != string(row.mapping.trace) || rootTaskID != string(row.mapping.root) || userID != row.userMessageID || agentID != row.agentMessageID || resultJSON != row.rewrittenResultJSON {
			rows.Close()
			return fmt.Errorf("migrated receipt %q does not match migration plan", turnID)
		}
		if err := validateRewrittenResult(resultJSON, row); err != nil {
			rows.Close()
			return err
		}
		seen[turnID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(seen) != len(receipts) {
		return errors.New("migrated receipt set is incomplete")
	}

	outboxes := make(map[string]*outboxRow, len(plan.outbox))
	for index := range plan.outbox {
		row := &plan.outbox[index]
		outboxes[string(row.mapping.turn)+"\x00"+row.target] = row
	}
	rows, err = tx.QueryContext(ctx, `SELECT turn_id, trace_id, root_task_id, target, payload_json FROM conversation_turn_outbox ORDER BY rowid ASC`)
	if err != nil {
		return err
	}
	seen = make(map[string]struct{}, len(plan.outbox))
	for rows.Next() {
		var turnID, traceID, rootTaskID, target, payloadJSON string
		if err := rows.Scan(&turnID, &traceID, &rootTaskID, &target, &payloadJSON); err != nil {
			rows.Close()
			return err
		}
		key := turnID + "\x00" + target
		row := outboxes[key]
		if row == nil || traceID != string(row.mapping.trace) || rootTaskID != string(row.mapping.root) || payloadJSON != row.rewrittenPayloadJSON {
			rows.Close()
			return fmt.Errorf("migrated outbox %q does not match migration plan", key)
		}
		seen[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(seen) != len(outboxes) {
		return errors.New("migrated outbox set is incomplete")
	}

	recalls := make(map[string]*recallRow, len(plan.recalls))
	for index := range plan.recalls {
		recalls[string(plan.recalls[index].mapping.trace)] = &plan.recalls[index]
	}
	rows, err = tx.QueryContext(ctx, `SELECT trace_id, turn_id, root_task_id FROM recall_trace ORDER BY rowid ASC`)
	if err != nil {
		return err
	}
	seen = make(map[string]struct{}, len(plan.recalls))
	for rows.Next() {
		var traceID, turnID, rootTaskID string
		if err := rows.Scan(&traceID, &turnID, &rootTaskID); err != nil {
			rows.Close()
			return err
		}
		row := recalls[traceID]
		if row == nil || turnID != string(row.mapping.turn) || rootTaskID != string(row.mapping.root) {
			rows.Close()
			return fmt.Errorf("migrated recall %q does not match migration plan", traceID)
		}
		seen[traceID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(seen) != len(recalls) {
		return errors.New("migrated recall set is incomplete")
	}

	rows, err = tx.QueryContext(ctx, `SELECT item_id, trace_id FROM recall_trace_item ORDER BY rowid ASC`)
	if err != nil {
		return err
	}
	seen = make(map[string]struct{}, len(plan.items))
	for rows.Next() {
		var itemID, traceID string
		if err := rows.Scan(&itemID, &traceID); err != nil {
			rows.Close()
			return err
		}
		found := false
		for _, row := range plan.items {
			if row.itemID == itemID {
				if row.newTrace != traceID {
					rows.Close()
					return fmt.Errorf("migrated recall item %q has wrong trace", itemID)
				}
				found = true
				break
			}
		}
		if !found {
			rows.Close()
			return fmt.Errorf("unexpected migrated recall item %q", itemID)
		}
		seen[itemID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(seen) != len(plan.items) {
		return errors.New("migrated recall item set is incomplete")
	}

	rows, err = tx.QueryContext(ctx, `SELECT injection_id, trace_id, item_ids FROM prompt_injection_event ORDER BY rowid ASC`)
	if err != nil {
		return err
	}
	seen = make(map[string]struct{}, len(plan.injections))
	for rows.Next() {
		var injectionID, traceID, itemIDs string
		if err := rows.Scan(&injectionID, &traceID, &itemIDs); err != nil {
			rows.Close()
			return err
		}
		found := false
		for _, row := range plan.injections {
			if row.injectionID == injectionID {
				if row.newTrace != traceID || row.itemIDs != itemIDs {
					rows.Close()
					return fmt.Errorf("migrated injection %q does not match migration plan", injectionID)
				}
				found = true
				break
			}
		}
		if !found {
			rows.Close()
			return fmt.Errorf("unexpected migrated injection %q", injectionID)
		}
		seen[injectionID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(seen) != len(plan.injections) {
		return errors.New("migrated injection set is incomplete")
	}

	if err := verifyMessageMetadataRows(ctx, tx, plan); err != nil {
		return err
	}
	if err := verifyStringSet(ctx, tx, `SELECT id FROM l1_memory_event WHERE id LIKE 'msg\_%' ESCAPE '\'`, plan.messageIDs); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, tx); err != nil {
		return err
	}
	return verifyIntegrity(ctx, tx)
}

func verifyMessageMetadataRows(ctx context.Context, tx *sql.Tx, plan migrationPlan) error {
	seen := make(map[string]struct{}, len(plan.messages))
	for _, row := range plan.messages {
		var speaker, metaJSON string
		if err := tx.QueryRowContext(ctx, `SELECT speaker, meta_json FROM l1_memory_event WHERE id = ?`, row.id).Scan(&speaker, &metaJSON); err != nil {
			return fmt.Errorf("migrated message %q is missing: %w", row.id, err)
		}
		if speaker != row.speaker || metaJSON != row.newMetaJSON {
			return fmt.Errorf("migrated message %q metadata does not match migration plan", row.id)
		}
		seen[row.id] = struct{}{}
	}
	if len(seen) != len(plan.messages) {
		return errors.New("migrated receipt-owned message set is incomplete")
	}
	return nil
}

func verifyStringSet(ctx context.Context, tx *sql.Tx, query string, expected map[string]struct{}) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(expected))
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		seen[value] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("preserved canonical message identity set changed")
	}
	for value := range expected {
		if _, ok := seen[value]; !ok {
			return fmt.Errorf("preserved canonical message identity %q is missing", value)
		}
	}
	return nil
}

func verifyForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign_key_check failed")
	}
	return rows.Err()
}

func verifyIntegrity(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	var result string
	if !rows.Next() || rows.Scan(&result) != nil || strings.ToLower(strings.TrimSpace(result)) != "ok" {
		return errors.New("integrity_check failed")
	}
	return nil
}
