package eventmigration

import (
	"bufio"
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
	"strings"
	"time"

	identitymigration "github.com/Nyukimin/RenCrow_CORE/internal/application/identitymigration"
	_ "modernc.org/sqlite"
)

const (
	legacyAIWorkflowTable = "ai_workflow_event"
	legacyTraceEventTable = "trace_event"
	maxJSONLRecordBytes   = 16 << 20
)

func loadAndConvert(ctx context.Context, options SourceOptions) (preparedMigration, error) {
	if err := validateSourceOptions(options); err != nil {
		return preparedMigration{}, err
	}
	sources := make([]sourceDescriptor, 0, 2)
	if strings.TrimSpace(options.AISQLite) != "" {
		sources = append(sources, sourceDescriptor{
			name: "ai_workflow", format: "sqlite", table: legacyAIWorkflowTable, componentID: "ai_workflow", path: options.AISQLite,
		})
	} else if strings.TrimSpace(options.AIJSONL) != "" {
		sources = append(sources, sourceDescriptor{
			name: "ai_workflow", format: "jsonl", table: legacyAIWorkflowTable, componentID: "ai_workflow", path: options.AIJSONL,
		})
	}
	if strings.TrimSpace(options.SuperagentSQLite) != "" {
		sources = append(sources, sourceDescriptor{
			name: "superagent", format: "sqlite", table: legacyTraceEventTable, componentID: "superagent", path: options.SuperagentSQLite,
		})
	} else if strings.TrimSpace(options.SuperagentJSONL) != "" {
		sources = append(sources, sourceDescriptor{
			name: "superagent", format: "jsonl", table: legacyTraceEventTable, componentID: "superagent", path: options.SuperagentJSONL,
		})
	}
	return prepare(ctx, sources)
}

func validateSourceOptions(options SourceOptions) error {
	if nonEmptyCount(options.AISQLite, options.AIJSONL) > 1 {
		return fmt.Errorf("exactly zero or one AI source may be set")
	}
	if nonEmptyCount(options.SuperagentSQLite, options.SuperagentJSONL) > 1 {
		return fmt.Errorf("exactly zero or one SuperAgent source may be set")
	}
	if nonEmptyCount(options.AISQLite, options.AIJSONL, options.SuperagentSQLite, options.SuperagentJSONL) == 0 {
		return fmt.Errorf("at least one legacy event source is required")
	}
	return nil
}

func loadSource(ctx context.Context, source sourceDescriptor) ([]identitymigration.LegacyEvent, string, error) {
	if strings.TrimSpace(source.path) == "" {
		return nil, "", fmt.Errorf("source path is required")
	}
	switch source.format {
	case "jsonl":
		return loadJSONLSource(ctx, source)
	case "sqlite":
		return loadSQLiteSource(ctx, source)
	default:
		return nil, "", fmt.Errorf("unsupported source format")
	}
}

func loadJSONLSource(ctx context.Context, source sourceDescriptor) ([]identitymigration.LegacyEvent, string, error) {
	file, err := os.Open(source.path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()

	hash := sha256.New()
	reader := io.TeeReader(file, hash)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxJSONLRecordBytes)
	legacy := make([]identitymigration.LegacyEvent, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		item, err := parseJSONRecord(line, source.table)
		if err != nil {
			return nil, "", fmt.Errorf("JSONL record %d: %w", lineNumber, err)
		}
		legacy = append(legacy, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, "", err
	}
	return legacy, hex.EncodeToString(hash.Sum(nil)), nil
}

func loadSQLiteSource(ctx context.Context, source sourceDescriptor) ([]identitymigration.LegacyEvent, string, error) {
	if err := ensureSQLiteSnapshotSidecars(source.path); err != nil {
		return nil, "", err
	}
	beforeHash, err := hashRegularFile(source.path)
	if err != nil {
		return nil, "", err
	}
	db, err := openSQLiteReadOnly(ctx, source.path)
	if err != nil {
		return nil, "", err
	}
	legacy, queryErr := querySQLiteSource(ctx, db, source)
	closeErr := db.Close()
	if queryErr != nil {
		return nil, "", queryErr
	}
	if closeErr != nil {
		return nil, "", closeErr
	}
	if err := ensureSQLiteSnapshotSidecars(source.path); err != nil {
		return nil, "", err
	}
	afterHash, err := hashRegularFile(source.path)
	if err != nil {
		return nil, "", err
	}
	if beforeHash != afterHash {
		return nil, "", fmt.Errorf("source snapshot changed during read")
	}
	return legacy, beforeHash, nil
}

func ensureSQLiteSnapshotSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-journal"} {
		sidecarPath := path + suffix
		info, err := os.Lstat(sidecarPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect sqlite snapshot sidecar %s: %w", suffix, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("sqlite snapshot sidecar %s is not a regular file", suffix)
		}
		if info.Size() > 0 {
			return fmt.Errorf("sqlite snapshot has non-empty %s sidecar; checkpoint the snapshot before migration", suffix)
		}
	}
	return nil
}

func hashRegularFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("source snapshot is missing or not a regular file")
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

func openSQLiteReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The resolved path has already been checked as a regular snapshot file.
	// URL escaping keeps spaces, '?', '#', and Windows path syntax in the file
	// component while mode=ro prevents SQLite from creating or mutating it.
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
		return nil, fmt.Errorf("sqlite source is not query-only")
	}
	return db, nil
}

func sqliteReadOnlyDSN(path string) string {
	return (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(path),
		RawQuery: "mode=ro&_pragma=busy_timeout%3d5000",
	}).String()
}

func querySQLiteSource(ctx context.Context, db *sql.DB, source sourceDescriptor) ([]identitymigration.LegacyEvent, error) {
	columns, err := sqliteTableColumns(ctx, db, source.table)
	if err != nil {
		return nil, err
	}
	if !columns["event_id"] || !columns["payload"] {
		return nil, fmt.Errorf("legacy table %s is missing event_id or payload", source.table)
	}
	selectColumns := []string{"event_id", "parent_event_id", "run_id", "workstream_id", "event_type", "created_at", "payload"}
	expressions := make([]string, 0, len(selectColumns))
	for _, column := range selectColumns {
		if columns[column] {
			expressions = append(expressions, column)
		} else {
			expressions = append(expressions, "NULL AS "+column)
		}
	}
	query := "SELECT " + strings.Join(expressions, ", ") + " FROM " + source.table + " ORDER BY rowid ASC"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	legacy := make([]identitymigration.LegacyEvent, 0)
	for rows.Next() {
		values := make([]any, len(selectColumns))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		item, err := parseSQLiteRecord(source.table, selectColumns, values)
		if err != nil {
			return nil, err
		}
		legacy = append(legacy, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return legacy, nil
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("legacy table %s is missing", table)
	}
	return columns, nil
}

func parseSQLiteRecord(sourceTable string, columns []string, values []any) (identitymigration.LegacyEvent, error) {
	fields := make(map[string]any, len(columns))
	for index, column := range columns {
		fields[column] = values[index]
	}
	payload, err := decodePayloadValue(fields["payload"])
	if err != nil {
		return identitymigration.LegacyEvent{}, fmt.Errorf("payload: %w", err)
	}
	eventID := firstString(fields["event_id"])
	parentEventID := firstString(fields["parent_event_id"])
	runID := firstString(fields["run_id"])
	workstreamID := firstString(fields["workstream_id"])
	eventType := firstString(fields["event_type"])
	occurredAt, err := parseTimeValue(fields["created_at"])
	if err != nil {
		return identitymigration.LegacyEvent{}, fmt.Errorf("created_at: %w", err)
	}
	applyPayloadFallbacks(payload, &eventID, &parentEventID, &runID, &workstreamID, &eventType, &occurredAt)
	return identitymigration.LegacyEvent{
		SourceTable: sourceTable, EventID: eventID, ParentEventID: parentEventID,
		RunID: runID, WorkstreamID: workstreamID, EventType: eventType,
		OccurredAt: occurredAt, Payload: payload,
	}, nil
}

func parseJSONRecord(line []byte, sourceTable string) (identitymigration.LegacyEvent, error) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(line))
	if err := decoder.Decode(&raw); err != nil {
		return identitymigration.LegacyEvent{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return identitymigration.LegacyEvent{}, fmt.Errorf("record has trailing JSON")
		}
		return identitymigration.LegacyEvent{}, fmt.Errorf("record has trailing data")
	}
	if raw == nil {
		return identitymigration.LegacyEvent{}, fmt.Errorf("record must be a JSON object")
	}
	payload, err := decodePayloadRaw(raw["payload"])
	if err != nil {
		return identitymigration.LegacyEvent{}, fmt.Errorf("payload: %w", err)
	}
	if payload == nil && raw["payload"] == nil {
		payload = make(map[string]any, len(raw))
		for key, encoded := range raw {
			if key == "event_id" || key == "parent_event_id" || key == "run_id" || key == "workstream_id" || key == "event_type" || key == "created_at" || key == "occurred_at" {
				continue
			}
			var value any
			if err := json.Unmarshal(encoded, &value); err != nil {
				return identitymigration.LegacyEvent{}, err
			}
			payload[key] = value
		}
	}
	eventID := rawString(raw, "event_id")
	parentEventID := rawString(raw, "parent_event_id")
	runID := rawString(raw, "run_id")
	workstreamID := rawString(raw, "workstream_id")
	eventType := rawString(raw, "event_type")
	occurredAt, err := parseTimeRaw(firstRaw(raw, "created_at", "occurred_at"))
	if err != nil {
		return identitymigration.LegacyEvent{}, fmt.Errorf("created_at: %w", err)
	}
	applyPayloadFallbacks(payload, &eventID, &parentEventID, &runID, &workstreamID, &eventType, &occurredAt)
	return identitymigration.LegacyEvent{
		SourceTable: sourceTable, EventID: eventID, ParentEventID: parentEventID,
		RunID: runID, WorkstreamID: workstreamID, EventType: eventType,
		OccurredAt: occurredAt, Payload: payload,
	}, nil
}

func decodePayloadRaw(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if stringValue, ok := value.(string); ok {
		return decodePayloadValue(stringValue)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload must be a JSON object")
	}
	return object, nil
}

func decodePayloadValue(value any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed, nil
	case []byte:
		return decodePayloadRaw(json.RawMessage(typed))
	case string:
		return decodePayloadRaw(json.RawMessage(typed))
	default:
		return nil, fmt.Errorf("payload must contain a JSON object")
	}
}

func applyPayloadFallbacks(payload map[string]any, eventID, parentEventID, runID, workstreamID, eventType *string, occurredAt *time.Time) {
	if payload == nil {
		return
	}
	if *eventID == "" {
		*eventID = mapString(payload, "event_id")
	}
	if *parentEventID == "" {
		*parentEventID = mapString(payload, "parent_event_id")
	}
	if *runID == "" {
		*runID = mapString(payload, "run_id")
	}
	if *workstreamID == "" {
		*workstreamID = mapString(payload, "workstream_id")
	}
	if *eventType == "" {
		*eventType = mapString(payload, "event_type")
	}
	if occurredAt.IsZero() {
		if value, ok := payload["created_at"]; ok {
			if parsed, err := parseTimeValue(value); err == nil {
				*occurredAt = parsed
			}
		} else if value, ok := payload["occurred_at"]; ok {
			if parsed, err := parseTimeValue(value); err == nil {
				*occurredAt = parsed
			}
		}
	}
}

func mapString(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok {
		return ""
	}
	return firstString(value)
}

func rawString(raw map[string]json.RawMessage, key string) string {
	value, ok := raw[key]
	if !ok {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return ""
	}
	return firstString(decoded)
}

func firstRaw(raw map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			return value
		}
	}
	return nil
}

func firstString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func parseTimeRaw(raw json.RawMessage) (time.Time, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return time.Time{}, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return time.Time{}, err
	}
	return parseTimeValue(value)
}

func parseTimeValue(value any) (time.Time, error) {
	if value == nil {
		return time.Time{}, nil
	}
	if typed, ok := value.(time.Time); ok {
		return typed.UTC(), nil
	}
	if number, ok := value.(json.Number); ok {
		seconds, err := number.Int64()
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(seconds, 0).UTC(), nil
	}
	if number, ok := value.(int64); ok {
		return time.Unix(number, 0).UTC(), nil
	}
	text := strings.TrimSpace(firstString(value))
	if text == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp")
}
