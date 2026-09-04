package threadmigration

// This file is deliberately a read-only boundary.  It inspects the legacy
// SQLite stores that existed before Step 05 and produces the in-memory Plan
// consumed by a later migration operation.  It must not open a path, start a
// transaction, mutate a PRAGMA, or write a row.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// SQLiteInventoryReceiptSchemaVersion identifies the canonical receipt
	// encoding.  The receipt hash excludes ReceiptSHA256 itself.
	SQLiteInventoryReceiptSchemaVersion = "rencrow.threadmigration.sqlite_inventory.v1"
	SQLiteInventoryReceiptReady         = "ready"

	legacySourceChatGPT = "chatgpt_export"

	l1MemoryEventSurface        = "l1_memory_event"
	l1EventLogSurface           = "l1_event_log"
	l1ProfilePromotionSurface   = "l1_profile_promotion_job"
	activeThreadSurface         = "conversation_active_thread"
	turnReceiptSurface          = "conversation_turn_receipt"
	turnOutboxSurface           = "conversation_turn_outbox"
	sessionThreadSurface        = "session_thread"
	threadSummaryReceiptSurface = "conversation_thread_summary_receipt"
	l1MemoryEventArchiveSurface = "l1_memory_event_archive"
	closedThreadRecordSuffix    = "closed_thread_id"
	turnOutboxTargetRedis       = "redis_projection"
	turnOutboxTargetFollowers   = "thread_followers"
	turnPayloadVersion          = "rencrow.conversation_turn_outbox.v1"
	maxLegacyResultJSONBytes    = 65536
	maxLegacyOutboxPayloadBytes = 8192
)

// SQLiteInventoryInput contains caller-owned database handles.  The handles
// remain owned by the caller and are never closed by the inventory operation.
// RawDB is optional because the legacy l1_raw_record table lived in the L1
// database; callers with a separate raw store may provide it explicitly.
type SQLiteInventoryInput struct {
	L1DB      *sql.DB
	ArchiveDB *sql.DB
	RawDB     *sql.DB
}

// SQLiteInventoryResult is the complete read-only output.  Plan contains the
// mappings; Receipt contains only deterministic counts, schema fingerprints,
// and hashes, not the source rows or their message contents.
type SQLiteInventoryResult struct {
	Plan    Plan                   `json:"plan"`
	Receipt SQLiteInventoryReceipt `json:"receipt"`
}

// SQLiteInventorySurfaceCount is a stable count for one legacy table. Rows is
// the number of source rows and References counts mapped identity references.
// An archive event with legacy thread_id=0 contributes a row but no reference.
type SQLiteInventorySurfaceCount struct {
	Surface    string `json:"surface"`
	Rows       int64  `json:"rows"`
	References int64  `json:"references"`
}

// SQLiteInventoryOptionalZeroCount records allowed zero-valued legacy thread
// identities. The pre-Step05 event and archive surfaces permit an unthreaded
// row; those rows are counted but never emitted as a ThreadMapping fact.
type SQLiteInventoryOptionalZeroCount struct {
	Surface string `json:"surface"`
	Count   int64  `json:"count"`
}

// SQLiteInventorySchemaFingerprint binds the exact source schema descriptor
// observed for one table without retaining arbitrary SQL text or data.
type SQLiteInventorySchemaFingerprint struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	SHA256   string `json:"sha256"`
}

// SQLiteInventoryReceipt is a machine-checkable, deterministic inventory
// receipt. All slices are sorted by their documented keys before hashing.
type SQLiteInventoryReceipt struct {
	SchemaVersion            string                             `json:"schema_version"`
	Status                   string                             `json:"status"`
	SurfaceCounts            []SQLiteInventorySurfaceCount      `json:"surface_counts"`
	OptionalZeroCounts       []SQLiteInventoryOptionalZeroCount `json:"optional_zero_counts"`
	SourceSchemaFingerprints []SQLiteInventorySchemaFingerprint `json:"source_schema_fingerprints"`
	MappingSHA256            string                             `json:"mapping_sha256"`
	ReceiptSHA256            string                             `json:"receipt_sha256"`
}

// InventorySQLite validates and inventories the exact legacy SQLite schema
// and rows. It performs only QueryContext calls (including read-only PRAGMA
// table_info calls) on the caller-owned handles.
func InventorySQLite(ctx context.Context, input SQLiteInventoryInput) (SQLiteInventoryResult, error) {
	if ctx == nil {
		return SQLiteInventoryResult{}, errors.New("sqlite inventory context is nil")
	}
	if err := ctx.Err(); err != nil {
		return SQLiteInventoryResult{}, err
	}
	if input.L1DB == nil {
		return SQLiteInventoryResult{}, errors.New("sqlite inventory L1 database is nil")
	}
	if input.ArchiveDB == nil {
		return SQLiteInventoryResult{}, errors.New("sqlite inventory archive database is nil")
	}

	builder := sqliteInventoryBuilder{
		ctx:                ctx,
		l1DB:               input.L1DB,
		archiveDB:          input.ArchiveDB,
		rawDB:              input.RawDB,
		chatGPTByTuple:     make(map[legacyTuple]string),
		tupleByChatGPT:     make(map[string]legacyTuple),
		facts:              make(map[compactFactKey]LegacyThreadFact),
		pendingChatGPTLogs: make(map[legacyTuple]pendingChatGPTLog),
		surfaceCounts:      make(map[string]*SQLiteInventorySurfaceCount, len(legacyTableNames)),
		optionalZeroCounts: make(map[string]int64),
		schemaFingerprints: make([]SQLiteInventorySchemaFingerprint, 0, len(legacyTableNames)),
	}
	if builder.rawDB == nil {
		builder.rawDB = builder.l1DB
	}

	if err := builder.validateSchemas(); err != nil {
		return SQLiteInventoryResult{}, err
	}
	if err := builder.inventoryL1(); err != nil {
		return SQLiteInventoryResult{}, err
	}
	if err := builder.inventoryArchive(); err != nil {
		return SQLiteInventoryResult{}, err
	}
	if err := builder.validatePendingChatGPTRawBindings(); err != nil {
		return SQLiteInventoryResult{}, err
	}
	if err := builder.resolvePendingChatGPTEventLogs(); err != nil {
		return SQLiteInventoryResult{}, err
	}
	facts, err := builder.classifiedFacts()
	if err != nil {
		return SQLiteInventoryResult{}, err
	}
	plan, err := BuildPlan(facts)
	if err != nil {
		return SQLiteInventoryResult{}, fmt.Errorf("build SQLite legacy thread mapping plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return SQLiteInventoryResult{}, fmt.Errorf("validate SQLite legacy thread mapping plan: %w", err)
	}

	receipt := SQLiteInventoryReceipt{
		SchemaVersion:            SQLiteInventoryReceiptSchemaVersion,
		Status:                   SQLiteInventoryReceiptReady,
		SurfaceCounts:            builder.sortedSurfaceCounts(),
		OptionalZeroCounts:       builder.sortedOptionalZeroCounts(),
		SourceSchemaFingerprints: builder.sortedSchemaFingerprints(),
		MappingSHA256:            plan.MappingSHA256,
	}
	receiptHash, err := receipt.ComputeSHA256()
	if err != nil {
		return SQLiteInventoryResult{}, fmt.Errorf("hash SQLite inventory receipt: %w", err)
	}
	receipt.ReceiptSHA256 = receiptHash
	result := SQLiteInventoryResult{Plan: plan, Receipt: receipt}
	if err := result.Validate(); err != nil {
		return SQLiteInventoryResult{}, fmt.Errorf("validate SQLite inventory result: %w", err)
	}
	return result, nil
}

// Validate checks both the plan and receipt, including the binding between the
// receipt's mapping digest and the plan digest.
func (result SQLiteInventoryResult) Validate() error {
	if err := result.Plan.Validate(); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if err := result.Receipt.Validate(); err != nil {
		return fmt.Errorf("receipt: %w", err)
	}
	if result.Receipt.MappingSHA256 != result.Plan.MappingSHA256 {
		return fmt.Errorf("receipt mapping SHA256 does not match plan")
	}
	return nil
}

// CanonicalJSON returns the receipt payload that is hashed. ReceiptSHA256 is
// excluded to avoid hashing a value that contains its own digest.
func (receipt SQLiteInventoryReceipt) CanonicalJSON() ([]byte, error) {
	payload := struct {
		SchemaVersion            string                             `json:"schema_version"`
		Status                   string                             `json:"status"`
		SurfaceCounts            []SQLiteInventorySurfaceCount      `json:"surface_counts"`
		OptionalZeroCounts       []SQLiteInventoryOptionalZeroCount `json:"optional_zero_counts"`
		SourceSchemaFingerprints []SQLiteInventorySchemaFingerprint `json:"source_schema_fingerprints"`
		MappingSHA256            string                             `json:"mapping_sha256"`
	}{
		SchemaVersion:            receipt.SchemaVersion,
		Status:                   receipt.Status,
		SurfaceCounts:            canonicalSurfaceCounts(receipt.SurfaceCounts),
		OptionalZeroCounts:       canonicalOptionalZeroCounts(receipt.OptionalZeroCounts),
		SourceSchemaFingerprints: canonicalSchemaFingerprints(receipt.SourceSchemaFingerprints),
		MappingSHA256:            receipt.MappingSHA256,
	}
	return json.Marshal(payload)
}

// ComputeSHA256 computes the lowercase SHA-256 of CanonicalJSON.
func (receipt SQLiteInventoryReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Validate checks receipt shape, sorting, digest syntax, and the canonical
// digest. Counts and fingerprints are intentionally visible but bounded to
// their owning table names by uniqueness and nonnegative checks.
func (receipt SQLiteInventoryReceipt) Validate() error {
	if receipt.SchemaVersion != SQLiteInventoryReceiptSchemaVersion {
		return fmt.Errorf("unsupported receipt schema version %q", receipt.SchemaVersion)
	}
	if receipt.Status != SQLiteInventoryReceiptReady {
		return fmt.Errorf("invalid receipt status %q", receipt.Status)
	}
	if err := validateSHA256Hex(receipt.MappingSHA256, "mapping SHA256"); err != nil {
		return err
	}
	if err := validateSHA256Hex(receipt.ReceiptSHA256, "receipt SHA256"); err != nil {
		return err
	}
	if err := validateSurfaceCounts(receipt.SurfaceCounts); err != nil {
		return err
	}
	if err := validateOptionalZeroCounts(receipt.OptionalZeroCounts); err != nil {
		return err
	}
	if err := validateCountRelationships(receipt.SurfaceCounts, receipt.OptionalZeroCounts); err != nil {
		return err
	}
	if err := validateSchemaFingerprints(receipt.SourceSchemaFingerprints); err != nil {
		return err
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return fmt.Errorf("compute receipt SHA256: %w", err)
	}
	if computed != receipt.ReceiptSHA256 {
		return fmt.Errorf("receipt SHA256 does not match canonical JSON")
	}
	return nil
}

// SurfaceCount returns one count by exact surface name.
func (receipt SQLiteInventoryReceipt) SurfaceCount(surface string) (SQLiteInventorySurfaceCount, bool) {
	for _, count := range receipt.SurfaceCounts {
		if count.Surface == surface {
			return count, true
		}
	}
	return SQLiteInventorySurfaceCount{}, false
}

// OptionalZeroCount returns one allowed-zero count by exact surface name.
func (receipt SQLiteInventoryReceipt) OptionalZeroCount(surface string) (SQLiteInventoryOptionalZeroCount, bool) {
	for _, count := range receipt.OptionalZeroCounts {
		if count.Surface == surface {
			return count, true
		}
	}
	return SQLiteInventoryOptionalZeroCount{}, false
}

type legacyTableColumn struct {
	Name       string
	Type       string
	NotNull    int64
	Default    sql.NullString
	PrimaryKey int64
}

type legacyTableDescriptor struct {
	Database string
	Name     string
	Columns  []legacyColumnDescriptor
}

type legacyColumnDescriptor struct {
	Name       string
	Type       string
	NotNull    int64
	Default    *string
	PrimaryKey int64
}

var legacyL1Tables = []legacyTableDescriptor{
	{Database: "l1", Name: l1MemoryEventSurface, Columns: []legacyColumnDescriptor{
		{Name: "id", Type: "TEXT", PrimaryKey: 1},
		{Name: "namespace", Type: "TEXT", NotNull: 1},
		{Name: "session_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_id", Type: "INTEGER", NotNull: 1},
		{Name: "speaker", Type: "TEXT", NotNull: 1},
		{Name: "message", Type: "TEXT", NotNull: 1},
		{Name: "meta_json", Type: "TEXT", NotNull: 1, Default: stringPointer("'{}'")},
		{Name: "memory_state", Type: "TEXT", NotNull: 1},
		{Name: "layer", Type: "TEXT", NotNull: 1},
		{Name: "source", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: l1EventLogSurface, Columns: []legacyColumnDescriptor{
		{Name: "id", Type: "TEXT", PrimaryKey: 1},
		{Name: "event_type", Type: "TEXT", NotNull: 1},
		{Name: "namespace", Type: "TEXT", NotNull: 1},
		{Name: "session_id", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "thread_id", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "payload_json", Type: "TEXT", NotNull: 1, Default: stringPointer("'{}'")},
		{Name: "source", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: l1ProfilePromotionSurface, Columns: []legacyColumnDescriptor{
		{Name: "evidence_event_id", Type: "TEXT", PrimaryKey: 1},
		{Name: "session_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_id", Type: "INTEGER", NotNull: 1},
		{Name: "state", Type: "TEXT", NotNull: 1},
		{Name: "attempt_count", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "lease_token", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "lease_expires_at", Type: "TIMESTAMP"},
		{Name: "next_attempt_at", Type: "TIMESTAMP"},
		{Name: "last_error", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: activeThreadSurface, Columns: []legacyColumnDescriptor{
		{Name: "session_id", Type: "TEXT", PrimaryKey: 1},
		{Name: "thread_id", Type: "INTEGER", NotNull: 1},
		{Name: "domain", Type: "TEXT", NotNull: 1},
		{Name: "message_count", Type: "INTEGER", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: turnReceiptSurface, Columns: []legacyColumnDescriptor{
		{Name: "turn_id", Type: "TEXT", PrimaryKey: 1},
		{Name: "payload_sha256", Type: "TEXT", NotNull: 1},
		{Name: "session_id", Type: "TEXT", NotNull: 1},
		{Name: "trace_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_id", Type: "INTEGER", NotNull: 1},
		{Name: "closed_thread_id", Type: "INTEGER"},
		{Name: "user_message_id", Type: "TEXT", NotNull: 1},
		{Name: "agent_message_id", Type: "TEXT", NotNull: 1},
		{Name: "status", Type: "TEXT", NotNull: 1},
		{Name: "result_json", Type: "TEXT", NotNull: 1},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: turnOutboxSurface, Columns: []legacyColumnDescriptor{
		{Name: "turn_id", Type: "TEXT", NotNull: 1, PrimaryKey: 1},
		{Name: "target", Type: "TEXT", NotNull: 1, PrimaryKey: 2},
		{Name: "session_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_id", Type: "INTEGER", NotNull: 1},
		{Name: "closed_thread_id", Type: "INTEGER"},
		{Name: "payload_sha256", Type: "TEXT", NotNull: 1},
		{Name: "payload_json", Type: "TEXT", NotNull: 1},
		{Name: "status", Type: "TEXT", NotNull: 1},
		{Name: "lease_token", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "lease_expires_at", Type: "TIMESTAMP"},
		{Name: "attempts", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "last_error", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
}

var legacyArchiveTables = []legacyTableDescriptor{
	{Database: "archive", Name: sessionThreadSurface, Columns: []legacyColumnDescriptor{
		{Name: "thread_id", Type: "BIGINT", PrimaryKey: 1},
		{Name: "session_id", Type: "VARCHAR", NotNull: 1},
		{Name: "ts_start", Type: "TIMESTAMP", NotNull: 1},
		{Name: "ts_end", Type: "TIMESTAMP"},
		{Name: "domain", Type: "VARCHAR"},
		{Name: "summary", Type: "TEXT"},
		{Name: "keywords", Type: "TEXT"},
		{Name: "embedding", Type: "TEXT"},
		{Name: "is_novel", Type: "BOOLEAN"},
		{Name: "created_at", Type: "TIMESTAMP", Default: stringPointer("CURRENT_TIMESTAMP")},
	}},
	{Database: "archive", Name: threadSummaryReceiptSurface, Columns: []legacyColumnDescriptor{
		{Name: "thread_id", Type: "BIGINT", PrimaryKey: 1},
		{Name: "schema_version", Type: "TEXT", NotNull: 1},
		{Name: "generation_mode", Type: "TEXT", NotNull: 1},
		{Name: "provider", Type: "TEXT", NotNull: 1},
		{Name: "failure_code", Type: "TEXT", NotNull: 1},
		{Name: "evidence_sha256", Type: "TEXT", NotNull: 1},
		{Name: "source_turn_count", Type: "INTEGER", NotNull: 1},
		{Name: "roles_json", Type: "TEXT", NotNull: 1},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "archive", Name: l1MemoryEventArchiveSurface, Columns: []legacyColumnDescriptor{
		{Name: "id", Type: "VARCHAR", PrimaryKey: 1},
		{Name: "namespace", Type: "VARCHAR", NotNull: 1},
		{Name: "session_id", Type: "VARCHAR", NotNull: 1},
		{Name: "thread_id", Type: "BIGINT", NotNull: 1},
		{Name: "speaker", Type: "VARCHAR", NotNull: 1},
		{Name: "message", Type: "TEXT", NotNull: 1},
		{Name: "meta_json", Type: "TEXT", NotNull: 1},
		{Name: "memory_state", Type: "VARCHAR", NotNull: 1},
		{Name: "layer", Type: "VARCHAR", NotNull: 1},
		{Name: "source", Type: "VARCHAR", NotNull: 1},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
}

var legacyTableNames = []string{
	l1MemoryEventSurface,
	l1EventLogSurface,
	l1ProfilePromotionSurface,
	activeThreadSurface,
	turnReceiptSurface,
	turnOutboxSurface,
	sessionThreadSurface,
	threadSummaryReceiptSurface,
	l1MemoryEventArchiveSurface,
}

var legacyOptionalZeroSurfaces = []string{
	l1EventLogSurface,
	l1MemoryEventSurface,
	l1MemoryEventArchiveSurface,
}

func stringPointer(value string) *string { return &value }

type sqliteInventoryBuilder struct {
	ctx       context.Context
	l1DB      *sql.DB
	archiveDB *sql.DB
	rawDB     *sql.DB

	facts              map[compactFactKey]LegacyThreadFact
	chatGPTByTuple     map[legacyTuple]string
	tupleByChatGPT     map[string]legacyTuple
	surfaceCounts      map[string]*SQLiteInventorySurfaceCount
	optionalZeroCounts map[string]int64
	schemaFingerprints []SQLiteInventorySchemaFingerprint
	receipts           map[string]legacyReceiptRow
	pendingChatGPTLogs map[legacyTuple]pendingChatGPTLog
	pendingRawBindings []pendingChatGPTRawBinding

	rawTableChecked bool
	rawTableExists  bool
}

type legacyTuple struct {
	sessionID string
	threadID  int64
}

// compactFactKey identifies one semantic identity on one source surface. A
// surface is retained in the plan so qualified lookup remains useful, while
// repeated rows on that surface collapse to one deterministic representative.
type compactFactKey struct {
	surface               string
	chatGPTConversationID string
	sessionID             string
	legacyThreadID        int64
}

type pendingChatGPTLog struct {
	recordKey string
	sessionID string
	threadID  int64
}

type pendingChatGPTRawBinding struct {
	surface        string
	recordKey      string
	conversationID string
}

func (builder *sqliteInventoryBuilder) validateSchemas() error {
	for _, descriptor := range legacyL1Tables {
		fingerprint, err := inspectLegacySchema(builder.ctx, builder.l1DB, descriptor)
		if err != nil {
			return err
		}
		builder.schemaFingerprints = append(builder.schemaFingerprints, fingerprint)
	}
	for _, descriptor := range legacyArchiveTables {
		fingerprint, err := inspectLegacySchema(builder.ctx, builder.archiveDB, descriptor)
		if err != nil {
			return err
		}
		builder.schemaFingerprints = append(builder.schemaFingerprints, fingerprint)
	}
	return nil
}

func inspectLegacySchema(ctx context.Context, db *sql.DB, descriptor legacyTableDescriptor) (SQLiteInventorySchemaFingerprint, error) {
	if err := contextError(ctx); err != nil {
		return SQLiteInventorySchemaFingerprint{}, err
	}
	var objectType string
	err := db.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name = ?`, descriptor.Name).Scan(&objectType)
	if errors.Is(err, sql.ErrNoRows) {
		return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("legacy %s.%s schema is missing", descriptor.Database, descriptor.Name)
	}
	if err != nil {
		return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("inspect legacy %s.%s schema object: %w", descriptor.Database, descriptor.Name, err)
	}
	if objectType != "table" {
		return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("legacy %s.%s schema object is %q, want table", descriptor.Database, descriptor.Name, objectType)
	}

	quotedName := quoteSQLiteIdentifier(descriptor.Name)
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quotedName+`)`)
	if err != nil {
		return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("inspect legacy %s.%s columns: %w", descriptor.Database, descriptor.Name, err)
	}
	defer rows.Close()
	columns := make([]legacyTableColumn, 0, len(descriptor.Columns))
	for rows.Next() {
		var cid, notNull, primaryKey int64
		var name, declaredType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
			return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("scan legacy %s.%s column: %w", descriptor.Database, descriptor.Name, err)
		}
		if cid != int64(len(columns)) {
			return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("legacy %s.%s column cid %d is not stable", descriptor.Database, descriptor.Name, cid)
		}
		columns = append(columns, legacyTableColumn{Name: name, Type: declaredType, NotNull: notNull, Default: defaultValue, PrimaryKey: primaryKey})
	}
	if err := rows.Err(); err != nil {
		return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("iterate legacy %s.%s columns: %w", descriptor.Database, descriptor.Name, err)
	}
	if err := rows.Close(); err != nil {
		return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("close legacy %s.%s columns: %w", descriptor.Database, descriptor.Name, err)
	}
	if len(columns) != len(descriptor.Columns) {
		return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("legacy %s.%s has %d columns, want exact %d-column legacy shape", descriptor.Database, descriptor.Name, len(columns), len(descriptor.Columns))
	}
	for index, expected := range descriptor.Columns {
		actual := columns[index]
		if actual.Name != expected.Name || normalizeDeclaredType(actual.Type) != normalizeDeclaredType(expected.Type) || actual.NotNull != expected.NotNull || actual.PrimaryKey != expected.PrimaryKey || !sameDefaultValue(actual.Default, expected.Default) {
			return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("legacy %s.%s column %d %q shape mismatch (type=%q notnull=%d pk=%d default=%v)", descriptor.Database, descriptor.Name, index, actual.Name, actual.Type, actual.NotNull, actual.PrimaryKey, nullableDefault(actual.Default))
		}
	}

	// The old outbox contract had a row-level foreign key. Checking its
	// metadata catches a mixed schema even when foreign-key enforcement was
	// disabled on the caller's connection.
	if descriptor.Name == turnOutboxSurface {
		if err := validateLegacyOutboxForeignKey(ctx, db); err != nil {
			return SQLiteInventorySchemaFingerprint{}, err
		}
	}

	fingerprintPayload := struct {
		Database string                `json:"database"`
		Table    string                `json:"table"`
		Columns  []legacyColumnPayload `json:"columns"`
	}{Database: descriptor.Database, Table: descriptor.Name, Columns: make([]legacyColumnPayload, len(columns))}
	for index, column := range columns {
		fingerprintPayload.Columns[index] = legacyColumnPayload{
			Name: column.Name, Type: normalizeDeclaredType(column.Type), NotNull: column.NotNull,
			Default: nullableDefault(column.Default), PrimaryKey: column.PrimaryKey,
		}
	}
	encoded, err := json.Marshal(fingerprintPayload)
	if err != nil {
		return SQLiteInventorySchemaFingerprint{}, fmt.Errorf("marshal legacy %s.%s schema fingerprint: %w", descriptor.Database, descriptor.Name, err)
	}
	digest := sha256.Sum256(encoded)
	return SQLiteInventorySchemaFingerprint{Database: descriptor.Database, Table: descriptor.Name, SHA256: hex.EncodeToString(digest[:])}, nil
}

type legacyColumnPayload struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	NotNull    int64   `json:"notnull"`
	Default    *string `json:"default"`
	PrimaryKey int64   `json:"pk"`
}

func validateLegacyOutboxForeignKey(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list("conversation_turn_outbox")`)
	if err != nil {
		return fmt.Errorf("inspect legacy conversation_turn_outbox foreign key: %w", err)
	}
	defer rows.Close()
	type foreignKey struct {
		id, sequence                               int64
		table, from, to, onUpdate, onDelete, match string
	}
	keys := make([]foreignKey, 0, 1)
	for rows.Next() {
		var key foreignKey
		if err := rows.Scan(&key.id, &key.sequence, &key.table, &key.from, &key.to, &key.onUpdate, &key.onDelete, &key.match); err != nil {
			return fmt.Errorf("scan legacy conversation_turn_outbox foreign key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy conversation_turn_outbox foreign key: %w", err)
	}
	if len(keys) != 1 || keys[0].table != turnReceiptSurface || keys[0].from != "turn_id" || keys[0].to != "turn_id" || keys[0].onUpdate != "NO ACTION" || keys[0].onDelete != "NO ACTION" {
		return fmt.Errorf("legacy conversation_turn_outbox foreign key does not exactly reference conversation_turn_receipt(turn_id)")
	}
	return nil
}

func (builder *sqliteInventoryBuilder) inventoryL1() error {
	if err := builder.inventoryL1MemoryEvents(); err != nil {
		return err
	}
	if err := builder.inventoryL1EventLog(); err != nil {
		return err
	}
	if err := builder.inventoryL1ProfileJobs(); err != nil {
		return err
	}
	if err := builder.inventoryActiveThreads(); err != nil {
		return err
	}
	if err := builder.inventoryTurnReceipts(); err != nil {
		return err
	}
	return builder.inventoryTurnOutbox()
}

func (builder *sqliteInventoryBuilder) inventoryL1MemoryEvents() error {
	rows, err := builder.l1DB.QueryContext(builder.ctx, `
SELECT id, session_id, thread_id, typeof(thread_id), source, meta_json
FROM l1_memory_event
ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", l1MemoryEventSurface, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, sessionID, threadType, source, metaJSON string
		var threadID int64
		if err := rows.Scan(&id, &sessionID, &threadID, &threadType, &source, &metaJSON); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", l1MemoryEventSurface, err)
		}
		if err := auditJSONWithoutIdentity(metaJSON, l1MemoryEventSurface, id); err != nil {
			return err
		}
		if err := builder.contextAndIdentity(l1MemoryEventSurface, id, sessionID, threadID, threadType, true); err != nil {
			return err
		}
		if threadID == 0 {
			if source == legacySourceChatGPT {
				return fmt.Errorf("legacy %s row %q claims ChatGPT source with optional-zero thread_id", l1MemoryEventSurface, id)
			}
			builder.addRow(l1MemoryEventSurface)
			builder.addOptionalZero(l1MemoryEventSurface)
			continue
		}
		builder.addRowReference(l1MemoryEventSurface)
		if source == legacySourceChatGPT {
			conversationID, err := parseChatGPTConversationMetadata(metaJSON)
			if err != nil {
				return fmt.Errorf("legacy %s row %q ChatGPT metadata: %w", l1MemoryEventSurface, id, err)
			}
			if err := builder.registerChatGPT(sessionID, threadID, conversationID); err != nil {
				return fmt.Errorf("legacy %s row %q ChatGPT identity: %w", l1MemoryEventSurface, id, err)
			}
			builder.pendingRawBindings = append(builder.pendingRawBindings, pendingChatGPTRawBinding{surface: l1MemoryEventSurface, recordKey: id, conversationID: conversationID})
			if err := builder.addChatGPTFact(l1MemoryEventSurface, id, conversationID); err != nil {
				return err
			}
			continue
		}
		if err := builder.addGenericFact(l1MemoryEventSurface, id, sessionID, threadID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", l1MemoryEventSurface, err)
	}
	return nil
}

func (builder *sqliteInventoryBuilder) inventoryL1EventLog() error {
	rows, err := builder.l1DB.QueryContext(builder.ctx, `
	SELECT id, session_id, thread_id, typeof(thread_id), source, payload_json
FROM l1_event_log
ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", l1EventLogSurface, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, sessionID, threadType, source, payloadJSON string
		var threadID int64
		if err := rows.Scan(&id, &sessionID, &threadID, &threadType, &source, &payloadJSON); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", l1EventLogSurface, err)
		}
		if err := auditJSONWithoutIdentity(payloadJSON, l1EventLogSurface, id); err != nil {
			return err
		}
		if err := builder.contextAndIdentity(l1EventLogSurface, id, sessionID, threadID, threadType, true); err != nil {
			return err
		}
		if threadID == 0 {
			if source == legacySourceChatGPT {
				return fmt.Errorf("legacy %s row %q claims ChatGPT source with optional-zero thread_id", l1EventLogSurface, id)
			}
			builder.addRow(l1EventLogSurface)
			builder.addOptionalZero(l1EventLogSurface)
			continue
		}
		builder.addRowReference(l1EventLogSurface)
		if source == legacySourceChatGPT {
			// Resolve only after both stores have been scanned: the exact
			// ChatGPT metadata may be carried by an archive event rather than a
			// current L1 event. Keep one compact representative per tuple while
			// cursors are open. The owner Raw binding is intentionally not
			// required here; source_record_id binds memory/archive evidence.
			tuple := legacyTuple{sessionID: sessionID, threadID: threadID}
			previous, exists := builder.pendingChatGPTLogs[tuple]
			if !exists || id < previous.recordKey {
				builder.pendingChatGPTLogs[tuple] = pendingChatGPTLog{recordKey: id, sessionID: sessionID, threadID: threadID}
			}
			continue
		}
		if err := builder.addGenericFact(l1EventLogSurface, id, sessionID, threadID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", l1EventLogSurface, err)
	}
	return nil
}

func (builder *sqliteInventoryBuilder) resolvePendingChatGPTEventLogs() error {
	if err := contextError(builder.ctx); err != nil {
		return err
	}
	pending := make([]pendingChatGPTLog, 0, len(builder.pendingChatGPTLogs))
	for _, row := range builder.pendingChatGPTLogs {
		pending = append(pending, row)
	}
	sort.Slice(pending, func(left, right int) bool {
		if pending[left].sessionID != pending[right].sessionID {
			return pending[left].sessionID < pending[right].sessionID
		}
		if pending[left].threadID != pending[right].threadID {
			return pending[left].threadID < pending[right].threadID
		}
		return pending[left].recordKey < pending[right].recordKey
	})
	for _, row := range pending {
		conversationID, ok := builder.chatGPTByTuple[legacyTuple{sessionID: row.sessionID, threadID: row.threadID}]
		if !ok {
			return fmt.Errorf("legacy %s row %q claims ChatGPT source but its tuple has no exact ChatGPT metadata binding", l1EventLogSurface, row.recordKey)
		}
		if err := builder.addChatGPTFact(l1EventLogSurface, row.recordKey, conversationID); err != nil {
			return err
		}
	}
	return nil
}

func (builder *sqliteInventoryBuilder) validatePendingChatGPTRawBindings() error {
	for _, pending := range builder.pendingRawBindings {
		if err := builder.validateRawBinding(pending.recordKey, pending.conversationID); err != nil {
			return fmt.Errorf("legacy %s row %q Raw binding: %w", pending.surface, pending.recordKey, err)
		}
	}
	return nil
}

func (builder *sqliteInventoryBuilder) inventoryL1ProfileJobs() error {
	rows, err := builder.l1DB.QueryContext(builder.ctx, `
SELECT j.evidence_event_id, j.session_id, j.thread_id, typeof(j.thread_id),
       e.id, e.session_id, e.thread_id, typeof(e.thread_id), e.source, e.meta_json
FROM l1_profile_promotion_job AS j
LEFT JOIN l1_memory_event AS e ON e.id = j.evidence_event_id
ORDER BY j.evidence_event_id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", l1ProfilePromotionSurface, err)
	}
	defer rows.Close()
	for rows.Next() {
		var evidenceID, jobSessionID, jobThreadType string
		var jobThreadID int64
		var eventID, eventSessionID, eventThreadType, eventSource, eventMeta sql.NullString
		var eventThreadID sql.NullInt64
		if err := rows.Scan(&evidenceID, &jobSessionID, &jobThreadID, &jobThreadType, &eventID, &eventSessionID, &eventThreadID, &eventThreadType, &eventSource, &eventMeta); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", l1ProfilePromotionSurface, err)
		}
		if strings.TrimSpace(evidenceID) == "" {
			return fmt.Errorf("legacy %s has an empty evidence_event_id", l1ProfilePromotionSurface)
		}
		if !eventID.Valid || !eventSessionID.Valid || !eventThreadID.Valid || !eventThreadType.Valid || !eventSource.Valid || !eventMeta.Valid || eventID.String == "" {
			return fmt.Errorf("legacy %s row %q is orphaned from l1_memory_event", l1ProfilePromotionSurface, evidenceID)
		}
		if err := builder.contextAndIdentity(l1ProfilePromotionSurface, evidenceID, jobSessionID, jobThreadID, jobThreadType, false); err != nil {
			return err
		}
		if eventID.String != evidenceID || eventThreadType.String != "integer" || eventThreadID.Int64 <= 0 || eventSessionID.String == "" {
			return fmt.Errorf("legacy %s row %q evidence identity is malformed", l1ProfilePromotionSurface, evidenceID)
		}
		if jobSessionID != eventSessionID.String || jobThreadID != eventThreadID.Int64 {
			return fmt.Errorf("legacy %s row %q does not exactly match its evidence event tuple", l1ProfilePromotionSurface, evidenceID)
		}
		builder.addRowReference(l1ProfilePromotionSurface)
		if eventSource.String == legacySourceChatGPT {
			conversationID, err := parseChatGPTConversationMetadata(eventMeta.String)
			if err != nil {
				return fmt.Errorf("legacy %s row %q evidence ChatGPT metadata: %w", l1ProfilePromotionSurface, evidenceID, err)
			}
			if err := builder.registerChatGPT(eventSessionID.String, eventThreadID.Int64, conversationID); err != nil {
				return fmt.Errorf("legacy %s row %q ChatGPT identity: %w", l1ProfilePromotionSurface, evidenceID, err)
			}
		}
		if err := builder.addGenericFact(l1ProfilePromotionSurface, evidenceID, jobSessionID, jobThreadID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", l1ProfilePromotionSurface, err)
	}
	return nil
}

func (builder *sqliteInventoryBuilder) inventoryActiveThreads() error {
	rows, err := builder.l1DB.QueryContext(builder.ctx, `
SELECT session_id, thread_id, typeof(thread_id), domain, message_count
FROM conversation_active_thread
ORDER BY session_id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", activeThreadSurface, err)
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, threadType, domain string
		var threadID, messageCount int64
		if err := rows.Scan(&sessionID, &threadID, &threadType, &domain, &messageCount); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", activeThreadSurface, err)
		}
		if err := builder.contextAndIdentity(activeThreadSurface, sessionID, sessionID, threadID, threadType, false); err != nil {
			return err
		}
		if strings.TrimSpace(domain) == "" || messageCount < 0 || messageCount > 12 {
			return fmt.Errorf("legacy %s row %q has invalid domain or message_count", activeThreadSurface, sessionID)
		}
		builder.addRowReference(activeThreadSurface)
		if err := builder.addGenericFact(activeThreadSurface, sessionID, sessionID, threadID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", activeThreadSurface, err)
	}
	return nil
}

type legacyReceiptRow struct {
	turnID       string
	payloadHash  string
	sessionID    string
	traceID      string
	threadID     int64
	closedID     int64
	closed       bool
	userMessage  string
	agentMessage string
	status       string
}

func (builder *sqliteInventoryBuilder) inventoryTurnReceipts() error {
	rows, err := builder.l1DB.QueryContext(builder.ctx, `
SELECT turn_id, payload_sha256, session_id, trace_id, thread_id, typeof(thread_id),
       closed_thread_id, typeof(closed_thread_id), user_message_id, agent_message_id,
       status, result_json
FROM conversation_turn_receipt
ORDER BY turn_id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", turnReceiptSurface, err)
	}
	defer rows.Close()
	receipts := make(map[string]legacyReceiptRow)
	for rows.Next() {
		var row legacyReceiptRow
		var threadType, closedType, resultJSON string
		var closedID sql.NullInt64
		if err := rows.Scan(&row.turnID, &row.payloadHash, &row.sessionID, &row.traceID, &row.threadID, &threadType, &closedID, &closedType, &row.userMessage, &row.agentMessage, &row.status, &resultJSON); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", turnReceiptSurface, err)
		}
		if err := builder.contextAndIdentity(turnReceiptSurface, row.turnID, row.sessionID, row.threadID, threadType, false); err != nil {
			return err
		}
		if err := validateCanonicalSessionForTurn(row.sessionID); err != nil {
			return fmt.Errorf("legacy %s row %q: %w", turnReceiptSurface, row.turnID, err)
		}
		if row.turnID == "" || row.traceID == "" || row.traceID != row.turnID || row.userMessage == "" || row.agentMessage == "" || !validTurnStatus(row.status) {
			return fmt.Errorf("legacy %s row %q has an invalid required field", turnReceiptSurface, row.turnID)
		}
		if err := validatePayloadHash(row.payloadHash); err != nil {
			return fmt.Errorf("legacy %s row %q: %w", turnReceiptSurface, row.turnID, err)
		}
		if err := validateClosedSQLTuple(closedID, closedType); err != nil {
			return fmt.Errorf("legacy %s row %q closed thread: %w", turnReceiptSurface, row.turnID, err)
		}
		row.closed = closedID.Valid
		row.closedID = closedID.Int64
		if len(resultJSON) == 0 || len(resultJSON) > maxLegacyResultJSONBytes {
			return fmt.Errorf("legacy %s row %q result_json exceeds legacy bound", turnReceiptSurface, row.turnID)
		}
		if err := auditTypedTurnJSON(resultJSON, turnReceiptSurface, row.turnID); err != nil {
			return err
		}
		decoded, err := decodeLegacyTurnResult(resultJSON)
		if err != nil {
			return fmt.Errorf("legacy %s row %q result_json: %w", turnReceiptSurface, row.turnID, err)
		}
		if err := decoded.validate(row); err != nil {
			return fmt.Errorf("legacy %s row %q result_json identity: %w", turnReceiptSurface, row.turnID, err)
		}
		if _, exists := receipts[row.turnID]; exists {
			return fmt.Errorf("legacy %s has duplicate turn_id %q", turnReceiptSurface, row.turnID)
		}
		receipts[row.turnID] = row
		builder.addRowReference(turnReceiptSurface)
		if err := builder.addGenericFact(turnReceiptSurface, row.turnID, row.sessionID, row.threadID); err != nil {
			return err
		}
		if row.closed {
			builder.addReference(turnReceiptSurface, 1)
			if err := builder.addGenericFact(turnReceiptSurface, closedThreadKey(row.turnID), row.sessionID, row.closedID); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", turnReceiptSurface, err)
	}
	builder.receipts = receipts
	return nil
}

func (builder *sqliteInventoryBuilder) inventoryTurnOutbox() error {
	rows, err := builder.l1DB.QueryContext(builder.ctx, `
SELECT turn_id, target, session_id, thread_id, typeof(thread_id),
       closed_thread_id, typeof(closed_thread_id), payload_sha256, payload_json,
       status, attempts, last_error
FROM conversation_turn_outbox
ORDER BY turn_id ASC, target ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", turnOutboxSurface, err)
	}
	defer rows.Close()
	for rows.Next() {
		var turnID, target, sessionID, threadType, closedType, payloadHash, payloadJSON, status, lastError string
		var threadID, attempts int64
		var closedID sql.NullInt64
		if err := rows.Scan(&turnID, &target, &sessionID, &threadID, &threadType, &closedID, &closedType, &payloadHash, &payloadJSON, &status, &attempts, &lastError); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", turnOutboxSurface, err)
		}
		receipt, exists := builder.receipts[turnID]
		if !exists {
			return fmt.Errorf("legacy %s row %q/%q has no conversation_turn_receipt", turnOutboxSurface, turnID, target)
		}
		if err := builder.contextAndIdentity(turnOutboxSurface, outboxRecordKey(turnID, target), sessionID, threadID, threadType, false); err != nil {
			return err
		}
		if err := validateCanonicalSessionForTurn(sessionID); err != nil {
			return fmt.Errorf("legacy %s row %q/%q: %w", turnOutboxSurface, turnID, target, err)
		}
		if !validOutboxTarget(target) || !validOutboxStatus(status) || attempts < 0 || !validOutboxLastError(lastError) {
			return fmt.Errorf("legacy %s row %q/%q has invalid status, target, attempts, or last_error", turnOutboxSurface, turnID, target)
		}
		if err := validatePayloadHash(payloadHash); err != nil {
			return fmt.Errorf("legacy %s row %q/%q: %w", turnOutboxSurface, turnID, target, err)
		}
		if payloadHash != receipt.payloadHash || sessionID != receipt.sessionID || threadID != receipt.threadID {
			return fmt.Errorf("legacy %s row %q/%q SQL identity does not match its receipt", turnOutboxSurface, turnID, target)
		}
		if err := validateClosedSQLTuple(closedID, closedType); err != nil {
			return fmt.Errorf("legacy %s row %q/%q closed thread: %w", turnOutboxSurface, turnID, target, err)
		}
		if closedID.Valid != receipt.closed || (closedID.Valid && closedID.Int64 != receipt.closedID) {
			return fmt.Errorf("legacy %s row %q/%q closed thread does not match its receipt", turnOutboxSurface, turnID, target)
		}
		if len(payloadJSON) == 0 || len(payloadJSON) > maxLegacyOutboxPayloadBytes {
			return fmt.Errorf("legacy %s row %q/%q payload_json exceeds legacy bound", turnOutboxSurface, turnID, target)
		}
		if err := auditTypedTurnJSON(payloadJSON, turnOutboxSurface, outboxRecordKey(turnID, target)); err != nil {
			return err
		}
		decoded, err := decodeLegacyOutboxPayload(payloadJSON)
		if err != nil {
			return fmt.Errorf("legacy %s row %q/%q payload_json: %w", turnOutboxSurface, turnID, target, err)
		}
		if err := decoded.validate(turnID, target, sessionID, threadID, closedID, payloadHash, receipt); err != nil {
			return fmt.Errorf("legacy %s row %q/%q payload_json identity: %w", turnOutboxSurface, turnID, target, err)
		}
		builder.addRowReference(turnOutboxSurface)
		if err := builder.addGenericFact(turnOutboxSurface, outboxRecordKey(turnID, target), sessionID, threadID); err != nil {
			return err
		}
		if closedID.Valid {
			builder.addReference(turnOutboxSurface, 1)
			if err := builder.addGenericFact(turnOutboxSurface, closedThreadKey(outboxRecordKey(turnID, target)), sessionID, closedID.Int64); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", turnOutboxSurface, err)
	}
	return nil
}

// receipts is populated only after all receipt rows have been checked. It is
// intentionally not part of the public result; it exists solely to enforce
// the outbox foreign-key and identity contract.
func (builder *sqliteInventoryBuilder) classifiedFacts() ([]LegacyThreadFact, error) {
	if err := contextError(builder.ctx); err != nil {
		return nil, err
	}
	// Source rows are audited one at a time, but the plan only needs a
	// deterministic representative for each semantic identity and surface.
	// BuildPlan retains every fact it receives, so this compact set prevents a
	// high-volume event table from turning mapping Sources into an unbounded
	// copy of the database. Later transforms resolve rows by LookupGeneric or
	// LookupChatGPT and use the receipt counts for the complete row population.
	compact := make(map[compactFactKey]LegacyThreadFact, len(builder.facts))
	for _, fact := range builder.facts {
		candidate := fact
		if fact.ChatGPTConversationID == "" {
			if conversationID, ok := builder.chatGPTByTuple[legacyTuple{sessionID: fact.SessionID, threadID: fact.LegacyThreadID}]; ok {
				candidate = LegacyThreadFact{Surface: fact.Surface, RecordKey: fact.RecordKey, ChatGPTConversationID: conversationID}
			}
		}
		key, err := compactFactKeyFor(candidate)
		if err != nil {
			return nil, err
		}
		retainCompactFact(compact, key, candidate)
	}
	classified := make([]LegacyThreadFact, 0, len(compact))
	for _, fact := range compact {
		classified = append(classified, fact)
	}
	sort.Slice(classified, func(left, right int) bool { return legacyFactLess(classified[left], classified[right]) })
	return classified, nil
}

func (builder *sqliteInventoryBuilder) inventoryArchive() error {
	if err := builder.inventorySessionThreads(); err != nil {
		return err
	}
	if err := builder.inventorySummaryReceipts(); err != nil {
		return err
	}
	return builder.inventoryArchiveEvents()
}

func (builder *sqliteInventoryBuilder) inventorySessionThreads() error {
	rows, err := builder.archiveDB.QueryContext(builder.ctx, `
SELECT thread_id, typeof(thread_id), session_id
FROM session_thread
ORDER BY thread_id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", sessionThreadSurface, err)
	}
	defer rows.Close()
	for rows.Next() {
		var threadID int64
		var threadType, sessionID string
		if err := rows.Scan(&threadID, &threadType, &sessionID); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", sessionThreadSurface, err)
		}
		if err := builder.contextAndIdentity(sessionThreadSurface, strconv.FormatInt(threadID, 10), sessionID, threadID, threadType, false); err != nil {
			return err
		}
		builder.addRowReference(sessionThreadSurface)
		if err := builder.addGenericFact(sessionThreadSurface, strconv.FormatInt(threadID, 10), sessionID, threadID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", sessionThreadSurface, err)
	}
	return nil
}

func (builder *sqliteInventoryBuilder) inventorySummaryReceipts() error {
	rows, err := builder.archiveDB.QueryContext(builder.ctx, `
SELECT r.thread_id, typeof(r.thread_id), s.session_id
FROM conversation_thread_summary_receipt AS r
LEFT JOIN session_thread AS s ON s.thread_id = r.thread_id
ORDER BY r.thread_id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", threadSummaryReceiptSurface, err)
	}
	defer rows.Close()
	for rows.Next() {
		var threadID int64
		var threadType string
		var sessionID sql.NullString
		if err := rows.Scan(&threadID, &threadType, &sessionID); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", threadSummaryReceiptSurface, err)
		}
		if !sessionID.Valid || strings.TrimSpace(sessionID.String) == "" {
			return fmt.Errorf("legacy %s row %d is orphaned from session_thread", threadSummaryReceiptSurface, threadID)
		}
		recordKey := strconv.FormatInt(threadID, 10)
		if err := builder.contextAndIdentity(threadSummaryReceiptSurface, recordKey, sessionID.String, threadID, threadType, false); err != nil {
			return err
		}
		builder.addRowReference(threadSummaryReceiptSurface)
		if err := builder.addGenericFact(threadSummaryReceiptSurface, recordKey, sessionID.String, threadID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", threadSummaryReceiptSurface, err)
	}
	return nil
}

func (builder *sqliteInventoryBuilder) inventoryArchiveEvents() error {
	rows, err := builder.archiveDB.QueryContext(builder.ctx, `
SELECT id, session_id, thread_id, typeof(thread_id), source, meta_json
FROM l1_memory_event_archive
ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", l1MemoryEventArchiveSurface, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, sessionID, threadType, source, metaJSON string
		var threadID int64
		if err := rows.Scan(&id, &sessionID, &threadID, &threadType, &source, &metaJSON); err != nil {
			return fmt.Errorf("scan legacy %s row: %w", l1MemoryEventArchiveSurface, err)
		}
		if err := auditJSONWithoutIdentity(metaJSON, l1MemoryEventArchiveSurface, id); err != nil {
			return err
		}
		if err := builder.contextAndIdentity(l1MemoryEventArchiveSurface, id, sessionID, threadID, threadType, true); err != nil {
			return err
		}
		if threadID == 0 {
			if source == legacySourceChatGPT {
				return fmt.Errorf("legacy %s row %q claims ChatGPT source with optional-zero thread_id", l1MemoryEventArchiveSurface, id)
			}
			builder.addRow(l1MemoryEventArchiveSurface)
			builder.addOptionalZero(l1MemoryEventArchiveSurface)
			continue
		}
		builder.addRowReference(l1MemoryEventArchiveSurface)
		if source == legacySourceChatGPT {
			conversationID, err := parseChatGPTConversationMetadata(metaJSON)
			if err != nil {
				return fmt.Errorf("legacy %s row %q ChatGPT metadata: %w", l1MemoryEventArchiveSurface, id, err)
			}
			if err := builder.registerChatGPT(sessionID, threadID, conversationID); err != nil {
				return fmt.Errorf("legacy %s row %q ChatGPT identity: %w", l1MemoryEventArchiveSurface, id, err)
			}
			builder.pendingRawBindings = append(builder.pendingRawBindings, pendingChatGPTRawBinding{surface: l1MemoryEventArchiveSurface, recordKey: id, conversationID: conversationID})
			if err := builder.addChatGPTFact(l1MemoryEventArchiveSurface, id, conversationID); err != nil {
				return err
			}
			continue
		}
		if err := builder.addGenericFact(l1MemoryEventArchiveSurface, id, sessionID, threadID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy %s: %w", l1MemoryEventArchiveSurface, err)
	}
	return nil
}

func (builder *sqliteInventoryBuilder) addGenericFact(surface, recordKey, sessionID string, threadID int64) error {
	fact := LegacyThreadFact{
		Surface: surface, RecordKey: recordKey, SessionID: sessionID,
		LegacyThreadID: threadID, KindHint: "user_conversation",
	}
	key, err := compactFactKeyFor(fact)
	if err != nil {
		return err
	}
	retainCompactFact(builder.facts, key, fact)
	return nil
}

func (builder *sqliteInventoryBuilder) addChatGPTFact(surface, recordKey, conversationID string) error {
	fact := LegacyThreadFact{Surface: surface, RecordKey: recordKey, ChatGPTConversationID: conversationID}
	key, err := compactFactKeyFor(fact)
	if err != nil {
		return err
	}
	retainCompactFact(builder.facts, key, fact)
	return nil
}

func compactFactKeyFor(fact LegacyThreadFact) (compactFactKey, error) {
	surface := strings.TrimSpace(fact.Surface)
	if surface == "" {
		return compactFactKey{}, errors.New("compact legacy fact surface is empty")
	}
	if conversationID := fact.ChatGPTConversationID; conversationID != "" {
		return compactFactKey{surface: surface, chatGPTConversationID: conversationID}, nil
	}
	if fact.LegacyThreadID <= 0 || strings.TrimSpace(fact.SessionID) == "" {
		return compactFactKey{}, errors.New("compact generic fact identity is incomplete")
	}
	canonicalSessionID, err := canonicalGenericSessionID(fact.SessionID)
	if err != nil {
		return compactFactKey{}, fmt.Errorf("canonicalize compact generic session: %w", err)
	}
	return compactFactKey{surface: surface, sessionID: canonicalSessionID, legacyThreadID: fact.LegacyThreadID}, nil
}

func retainCompactFact(facts map[compactFactKey]LegacyThreadFact, key compactFactKey, candidate LegacyThreadFact) {
	if previous, exists := facts[key]; exists && !legacyFactLess(candidate, previous) {
		return
	}
	facts[key] = candidate
}

func legacyFactLess(left, right LegacyThreadFact) bool {
	if left.Surface != right.Surface {
		return left.Surface < right.Surface
	}
	if left.RecordKey != right.RecordKey {
		return left.RecordKey < right.RecordKey
	}
	if left.SessionID != right.SessionID {
		return left.SessionID < right.SessionID
	}
	if left.LegacyThreadID != right.LegacyThreadID {
		return left.LegacyThreadID < right.LegacyThreadID
	}
	if left.ChatGPTConversationID != right.ChatGPTConversationID {
		return left.ChatGPTConversationID < right.ChatGPTConversationID
	}
	return left.KindHint < right.KindHint
}

func (builder *sqliteInventoryBuilder) addReference(surface string, amount int64) {
	count := builder.ensureSurfaceCount(surface)
	count.References += amount
}

func (builder *sqliteInventoryBuilder) addRow(surface string) {
	builder.ensureSurfaceCount(surface).Rows++
}

func (builder *sqliteInventoryBuilder) addRowReference(surface string) {
	builder.addRow(surface)
	builder.addReference(surface, 1)
}

func (builder *sqliteInventoryBuilder) addOptionalZero(surface string) {
	builder.optionalZeroCounts[surface]++
}

func (builder *sqliteInventoryBuilder) ensureSurfaceCount(surface string) *SQLiteInventorySurfaceCount {
	if count := builder.surfaceCounts[surface]; count != nil {
		return count
	}
	count := &SQLiteInventorySurfaceCount{Surface: surface}
	builder.surfaceCounts[surface] = count
	return count
}

func (builder *sqliteInventoryBuilder) contextAndIdentity(surface, recordKey, sessionID string, threadID int64, threadType string, allowZero bool) error {
	if err := contextError(builder.ctx); err != nil {
		return err
	}
	allowEmptySession := allowZero && threadID == 0 && (surface == l1MemoryEventSurface || surface == l1EventLogSurface)
	if strings.TrimSpace(recordKey) == "" || (strings.TrimSpace(sessionID) == "" && !allowEmptySession) {
		return fmt.Errorf("legacy %s row has an incomplete identity tuple", surface)
	}
	if threadType != "integer" {
		return fmt.Errorf("legacy %s row %q thread_id has SQLite type %q, want integer", surface, recordKey, threadType)
	}
	if threadID < 0 || (!allowZero && threadID == 0) {
		return fmt.Errorf("legacy %s row %q has invalid legacy thread_id %d", surface, recordKey, threadID)
	}
	return nil
}

func (builder *sqliteInventoryBuilder) registerChatGPT(sessionID string, threadID int64, conversationID string) error {
	if conversationID == "" {
		return errors.New("ChatGPT conversation ID is empty")
	}
	expectedSession, expectedThread, err := chatGPTLegacyTuple(conversationID)
	if err != nil {
		return err
	}
	if sessionID != expectedSession || threadID != expectedThread {
		return fmt.Errorf("legacy tuple %q/%d does not match ChatGPT conversation %q (want %q/%d)", sessionID, threadID, conversationID, expectedSession, expectedThread)
	}
	tuple := legacyTuple{sessionID: sessionID, threadID: threadID}
	if previous, exists := builder.chatGPTByTuple[tuple]; exists && previous != conversationID {
		return fmt.Errorf("legacy tuple %q/%d maps to multiple ChatGPT conversations %q and %q", sessionID, threadID, previous, conversationID)
	}
	if previous, exists := builder.tupleByChatGPT[conversationID]; exists && previous != tuple {
		return fmt.Errorf("ChatGPT conversation %q maps to multiple legacy tuples %q/%d and %q/%d", conversationID, previous.sessionID, previous.threadID, sessionID, threadID)
	}
	builder.chatGPTByTuple[tuple] = conversationID
	builder.tupleByChatGPT[conversationID] = tuple
	return nil
}

func (builder *sqliteInventoryBuilder) validateRawBinding(sourceRecordID, conversationID string) error {
	if err := contextError(builder.ctx); err != nil {
		return err
	}
	if !builder.rawTableChecked {
		exists, err := sqliteTableExists(builder.ctx, builder.rawDB, "l1_raw_record")
		if err != nil {
			return err
		}
		builder.rawTableChecked = true
		builder.rawTableExists = exists
	}
	if !builder.rawTableExists {
		return errors.New("l1_raw_record table is required for a ChatGPT source binding")
	}
	if err := validateRawColumns(builder.ctx, builder.rawDB); err != nil {
		return err
	}
	rows, err := builder.rawDB.QueryContext(builder.ctx, `
SELECT source_type, thread_id, typeof(thread_id)
FROM l1_raw_record
WHERE source_record_id = ?
ORDER BY source_type ASC, thread_id ASC`, sourceRecordID)
	if err != nil {
		return fmt.Errorf("read l1_raw_record for source_record_id %q: %w", sourceRecordID, err)
	}
	defer rows.Close()
	matches := 0
	for rows.Next() {
		matches++
		var sourceType, threadID, threadType string
		if err := rows.Scan(&sourceType, &threadID, &threadType); err != nil {
			return fmt.Errorf("scan l1_raw_record for source_record_id %q: %w", sourceRecordID, err)
		}
		if sourceType != legacySourceChatGPT || threadID != conversationID || threadType != "text" {
			return fmt.Errorf("l1_raw_record for source_record_id %q does not preserve exact ChatGPT source/thread", sourceRecordID)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate l1_raw_record for source_record_id %q: %w", sourceRecordID, err)
	}
	if matches != 1 {
		return fmt.Errorf("l1_raw_record for source_record_id %q has %d matching rows, want exactly one", sourceRecordID, matches)
	}
	return nil
}

func sqliteTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var objectType string
	err := db.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name = ?`, table).Scan(&objectType)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect optional %s table: %w", table, err)
	}
	if objectType != "table" {
		return false, fmt.Errorf("optional %s object is %q, want table", table, objectType)
	}
	return true, nil
}

func validateRawColumns(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info("l1_raw_record")`)
	if err != nil {
		return fmt.Errorf("inspect l1_raw_record columns: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, 3)
	for rows.Next() {
		var cid, notNull, pk int64
		var name, declaredType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("scan l1_raw_record columns: %w", err)
		}
		if name == "source_record_id" || name == "source_type" || name == "thread_id" {
			seen[name] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate l1_raw_record columns: %w", err)
	}
	for _, required := range []string{"source_record_id", "source_type", "thread_id"} {
		if _, ok := seen[required]; !ok {
			return fmt.Errorf("l1_raw_record is missing required column %q", required)
		}
	}
	return nil
}

func (builder *sqliteInventoryBuilder) sortedSurfaceCounts() []SQLiteInventorySurfaceCount {
	result := make([]SQLiteInventorySurfaceCount, 0, len(legacyTableNames))
	for _, surface := range legacyTableNames {
		if count := builder.surfaceCounts[surface]; count != nil {
			result = append(result, *count)
		} else {
			result = append(result, SQLiteInventorySurfaceCount{Surface: surface})
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Surface < result[right].Surface })
	return result
}

func (builder *sqliteInventoryBuilder) sortedOptionalZeroCounts() []SQLiteInventoryOptionalZeroCount {
	result := make([]SQLiteInventoryOptionalZeroCount, 0, len(legacyOptionalZeroSurfaces))
	for _, surface := range legacyOptionalZeroSurfaces {
		result = append(result, SQLiteInventoryOptionalZeroCount{Surface: surface, Count: builder.optionalZeroCounts[surface]})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Surface < result[right].Surface })
	return result
}

func (builder *sqliteInventoryBuilder) sortedSchemaFingerprints() []SQLiteInventorySchemaFingerprint {
	result := append([]SQLiteInventorySchemaFingerprint(nil), builder.schemaFingerprints...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].Database != result[right].Database {
			return result[left].Database < result[right].Database
		}
		return result[left].Table < result[right].Table
	})
	return result
}

func canonicalSurfaceCounts(values []SQLiteInventorySurfaceCount) []SQLiteInventorySurfaceCount {
	result := append([]SQLiteInventorySurfaceCount(nil), values...)
	sort.Slice(result, func(left, right int) bool { return result[left].Surface < result[right].Surface })
	if result == nil {
		return []SQLiteInventorySurfaceCount{}
	}
	return result
}

func canonicalOptionalZeroCounts(values []SQLiteInventoryOptionalZeroCount) []SQLiteInventoryOptionalZeroCount {
	result := append([]SQLiteInventoryOptionalZeroCount(nil), values...)
	sort.Slice(result, func(left, right int) bool { return result[left].Surface < result[right].Surface })
	if result == nil {
		return []SQLiteInventoryOptionalZeroCount{}
	}
	return result
}

func canonicalSchemaFingerprints(values []SQLiteInventorySchemaFingerprint) []SQLiteInventorySchemaFingerprint {
	result := append([]SQLiteInventorySchemaFingerprint(nil), values...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].Database != result[right].Database {
			return result[left].Database < result[right].Database
		}
		return result[left].Table < result[right].Table
	})
	if result == nil {
		return []SQLiteInventorySchemaFingerprint{}
	}
	return result
}

func validateSurfaceCounts(values []SQLiteInventorySurfaceCount) error {
	if len(values) != len(legacyTableNames) {
		return fmt.Errorf("surface counts contain %d surfaces, want %d", len(values), len(legacyTableNames))
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.TrimSpace(value.Surface) == "" || value.Rows < 0 || value.References < 0 || value.References > value.Rows*2 {
			return fmt.Errorf("invalid surface count at index %d", index)
		}
		if _, exists := seen[value.Surface]; exists {
			return fmt.Errorf("duplicate surface count %q", value.Surface)
		}
		seen[value.Surface] = struct{}{}
		if index > 0 && values[index-1].Surface >= value.Surface {
			return fmt.Errorf("surface counts are not sorted")
		}
	}
	for _, surface := range legacyTableNames {
		if _, exists := seen[surface]; !exists {
			return fmt.Errorf("surface count %q is missing", surface)
		}
	}
	return nil
}

func validateOptionalZeroCounts(values []SQLiteInventoryOptionalZeroCount) error {
	if len(values) != len(legacyOptionalZeroSurfaces) {
		return fmt.Errorf("optional-zero counts contain %d surfaces, want %d", len(values), len(legacyOptionalZeroSurfaces))
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.TrimSpace(value.Surface) == "" || value.Count < 0 {
			return fmt.Errorf("invalid optional-zero count at index %d", index)
		}
		if _, exists := seen[value.Surface]; exists {
			return fmt.Errorf("duplicate optional-zero count %q", value.Surface)
		}
		seen[value.Surface] = struct{}{}
		if index > 0 && values[index-1].Surface >= value.Surface {
			return fmt.Errorf("optional-zero counts are not sorted")
		}
	}
	for _, surface := range legacyOptionalZeroSurfaces {
		if _, exists := seen[surface]; !exists {
			return fmt.Errorf("optional-zero count %q is missing", surface)
		}
	}
	return nil
}

func validateCountRelationships(surfaceCounts []SQLiteInventorySurfaceCount, optionalZeroCounts []SQLiteInventoryOptionalZeroCount) error {
	rowsBySurface := make(map[string]SQLiteInventorySurfaceCount, len(surfaceCounts))
	optionalSurfaces := make(map[string]struct{}, len(optionalZeroCounts))
	for _, count := range surfaceCounts {
		rowsBySurface[count.Surface] = count
	}
	for _, zero := range optionalZeroCounts {
		optionalSurfaces[zero.Surface] = struct{}{}
		count, ok := rowsBySurface[zero.Surface]
		if !ok {
			return fmt.Errorf("optional-zero surface %q has no row count", zero.Surface)
		}
		if zero.Count > count.Rows {
			return fmt.Errorf("optional-zero count for %q exceeds its row count", zero.Surface)
		}
		if count.References != count.Rows-zero.Count {
			return fmt.Errorf("reference count for %q does not match its positive thread rows", zero.Surface)
		}
	}
	for surface, count := range rowsBySurface {
		if _, optional := optionalSurfaces[surface]; optional {
			continue
		}
		switch surface {
		case turnReceiptSurface, turnOutboxSurface:
			if count.References < count.Rows || count.References > count.Rows*2 {
				return fmt.Errorf("reference count for %q does not match its required and optional thread rows", surface)
			}
		default:
			if count.References != count.Rows {
				return fmt.Errorf("reference count for %q does not match its required thread rows", surface)
			}
		}
	}
	return nil
}

func validateSchemaFingerprints(values []SQLiteInventorySchemaFingerprint) error {
	if len(values) != len(legacyTableNames) {
		return fmt.Errorf("schema fingerprints contain %d tables, want %d", len(values), len(legacyTableNames))
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.TrimSpace(value.Database) == "" || strings.TrimSpace(value.Table) == "" {
			return fmt.Errorf("invalid schema fingerprint at index %d", index)
		}
		key := value.Database + "\x00" + value.Table
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate schema fingerprint %s/%s", value.Database, value.Table)
		}
		seen[key] = struct{}{}
		if index > 0 {
			previous := values[index-1]
			if previous.Database > value.Database || (previous.Database == value.Database && previous.Table >= value.Table) {
				return fmt.Errorf("schema fingerprints are not sorted")
			}
		}
		if err := validateSHA256Hex(value.SHA256, "schema fingerprint SHA256"); err != nil {
			return err
		}
	}
	expected := make(map[string]struct{}, len(legacyTableNames))
	for _, descriptor := range legacyL1Tables {
		expected[descriptor.Database+"\x00"+descriptor.Name] = struct{}{}
	}
	for _, descriptor := range legacyArchiveTables {
		expected[descriptor.Database+"\x00"+descriptor.Name] = struct{}{}
	}
	for key := range expected {
		if _, exists := seen[key]; !exists {
			return fmt.Errorf("schema fingerprint %q is missing", key)
		}
	}
	return nil
}

func normalizeDeclaredType(value string) string { return strings.ToUpper(strings.TrimSpace(value)) }

func sameDefaultValue(actual sql.NullString, expected *string) bool {
	if expected == nil {
		return !actual.Valid
	}
	return actual.Valid && strings.TrimSpace(actual.String) == strings.TrimSpace(*expected)
}

func nullableDefault(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := strings.TrimSpace(value.String)
	return &result
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("sqlite inventory context is nil")
	}
	return ctx.Err()
}

func closedThreadKey(base string) string { return base + "\x00" + closedThreadRecordSuffix }

func outboxRecordKey(turnID, target string) string { return turnID + "\x00" + target }

func validateCanonicalSessionForTurn(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session_id is required")
	}
	// Avoid importing a persistence owner: the canonical ID validator is the
	// module identity owner and accepts UUIDv5 migration IDs as well as UUIDv7.
	if err := validateCanonicalSessionID(sessionID); err != nil {
		return err
	}
	return nil
}

func validateCanonicalSessionID(value string) error {
	// The core identity package is the only owner of the canonical ID grammar.
	if err := modulecore.SessionID(value).Validate(); err != nil {
		return fmt.Errorf("session_id %q is not canonical: %w", value, err)
	}
	return nil
}

func validatePayloadHash(value string) error {
	if err := validateSHA256Hex(value, "payload SHA256"); err != nil {
		return err
	}
	// The owner helper hashes the canonical request before persistence. Legacy
	// rows retain only this opaque digest, so this inventory proves the exact
	// lower-case SHA-256 contract and cross-row equality without rewriting or
	// inventing omitted request fields.
	return nil
}

func validateSHA256Hex(value, label string) error {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return fmt.Errorf("%s is not lowercase hexadecimal SHA-256", label)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s is not lowercase hexadecimal SHA-256: %w", label, err)
	}
	return nil
}

func validateClosedSQLTuple(value sql.NullInt64, declaredType string) error {
	if !value.Valid {
		if declaredType != "null" {
			return fmt.Errorf("NULL value has SQLite type %q", declaredType)
		}
		return nil
	}
	if declaredType != "integer" || value.Int64 <= 0 {
		return fmt.Errorf("closed_thread_id must be a positive SQLite integer")
	}
	return nil
}

func validTurnStatus(value string) bool {
	switch value {
	case "completed", "partial", "failed":
		return true
	default:
		return false
	}
}

func validOutboxTarget(value string) bool {
	return value == turnOutboxTargetRedis || value == turnOutboxTargetFollowers
}

func validOutboxStatus(value string) bool {
	switch value {
	case "pending", "running", "completed", "failed":
		return true
	default:
		return false
	}
}

func validOutboxLastError(value string) bool {
	switch value {
	case "", "invalid", "conflict", "unavailable", "internal":
		return true
	default:
		return false
	}
}

type legacyTurnResultPayload struct {
	TurnID           string          `json:"turn_id"`
	TraceID          string          `json:"trace_id"`
	SessionID        string          `json:"session_id"`
	ThreadID         json.RawMessage `json:"thread_id"`
	ClosedThreadID   json.RawMessage `json:"closed_thread_id"`
	UserMessageID    string          `json:"user_message_id"`
	AgentMessageID   string          `json:"agent_message_id"`
	MessageIDs       []string        `json:"message_ids"`
	PayloadSHA256    string          `json:"payload_sha256"`
	Status           string          `json:"status"`
	ErrorCode        string          `json:"error_code"`
	RequestedTargets []string        `json:"requested_targets"`
	PendingTargets   []string        `json:"pending_targets"`
	CompletedTargets []string        `json:"completed_targets"`
	IdempotentReplay bool            `json:"idempotent_replay"`
}

func decodeLegacyTurnResult(encoded string) (legacyTurnResultPayload, error) {
	var payload legacyTurnResultPayload
	if err := decodeStrictObject(encoded, &payload); err != nil {
		return legacyTurnResultPayload{}, err
	}
	return payload, nil
}

func (payload legacyTurnResultPayload) validate(row legacyReceiptRow) error {
	threadID, err := parseRequiredJSONInteger(payload.ThreadID, "thread_id")
	if err != nil {
		return err
	}
	closedID, closed, err := parseOptionalJSONInteger(payload.ClosedThreadID, "closed_thread_id")
	if err != nil {
		return err
	}
	if payload.TurnID != row.turnID || payload.TraceID != row.traceID || payload.SessionID != row.sessionID || threadID != row.threadID || payload.UserMessageID != row.userMessage || payload.AgentMessageID != row.agentMessage || payload.PayloadSHA256 != row.payloadHash || payload.Status != row.status {
		return errors.New("embedded turn identity does not match SQL identity")
	}
	if closed != row.closed || (closed && closedID != row.closedID) {
		return errors.New("embedded closed thread does not match SQL identity")
	}
	if len(payload.TurnID) == 0 || len(payload.TraceID) == 0 || len(payload.SessionID) == 0 || len(payload.UserMessageID) == 0 || len(payload.AgentMessageID) == 0 {
		return errors.New("embedded required identity is empty")
	}
	if len(payload.MessageIDs) != 2 || payload.MessageIDs[0] != payload.UserMessageID || payload.MessageIDs[1] != payload.AgentMessageID {
		return errors.New("embedded message_ids do not match SQL message IDs")
	}
	if err := validatePayloadHash(payload.PayloadSHA256); err != nil {
		return err
	}
	if payload.ErrorCode != "" && !validTurnErrorCode(payload.ErrorCode) {
		return fmt.Errorf("unknown error_code %q", payload.ErrorCode)
	}
	for name, targets := range map[string][]string{
		"requested_targets": payload.RequestedTargets,
		"pending_targets":   payload.PendingTargets,
		"completed_targets": payload.CompletedTargets,
	} {
		if err := validateTargetList(name, targets); err != nil {
			return err
		}
	}
	return nil
}

func validTurnErrorCode(value string) bool {
	switch value {
	case "invalid", "conflict", "unavailable", "internal":
		return true
	default:
		return false
	}
}

func validateTargetList(name string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validOutboxTarget(value) {
			return fmt.Errorf("%s contains invalid target %q", name, value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate target %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

type legacyOutboxPayload struct {
	Version        string          `json:"version"`
	TurnID         string          `json:"turn_id"`
	TraceID        string          `json:"trace_id"`
	SessionID      string          `json:"session_id"`
	OwnerID        string          `json:"owner_id"`
	ThreadID       json.RawMessage `json:"thread_id"`
	ClosedThreadID json.RawMessage `json:"closed_thread_id"`
	UserMessageID  string          `json:"user_message_id"`
	AgentMessageID string          `json:"agent_message_id"`
	Target         string          `json:"target"`
	PayloadSHA256  string          `json:"payload_sha256"`
}

func decodeLegacyOutboxPayload(encoded string) (legacyOutboxPayload, error) {
	var payload legacyOutboxPayload
	if err := decodeStrictObject(encoded, &payload); err != nil {
		return legacyOutboxPayload{}, err
	}
	return payload, nil
}

func (payload legacyOutboxPayload) validate(turnID, target, sessionID string, threadID int64, closedID sql.NullInt64, payloadHash string, receipt legacyReceiptRow) error {
	embeddedThreadID, err := parseRequiredJSONInteger(payload.ThreadID, "thread_id")
	if err != nil {
		return err
	}
	embeddedClosedID, embeddedClosed, err := parseOptionalJSONInteger(payload.ClosedThreadID, "closed_thread_id")
	if err != nil {
		return err
	}
	if payload.Version != turnPayloadVersion || payload.TurnID != turnID || payload.TraceID != turnID || payload.SessionID != sessionID || payload.OwnerID == "" || embeddedThreadID != threadID || payload.UserMessageID != receipt.userMessage || payload.AgentMessageID != receipt.agentMessage || payload.Target != target || payload.PayloadSHA256 != payloadHash {
		return errors.New("embedded outbox identity does not match SQL/receipt identity")
	}
	if embeddedClosed != closedID.Valid || (embeddedClosed && embeddedClosedID != closedID.Int64) {
		return errors.New("embedded outbox closed thread does not match SQL identity")
	}
	if err := validatePayloadHash(payload.PayloadSHA256); err != nil {
		return err
	}
	return nil
}

func decodeStrictObject(encoded string, destination interface{}) error {
	trimmed := bytes.TrimSpace([]byte(encoded))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("JSON value must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains more than one value")
		}
		return fmt.Errorf("JSON trailing value: %w", err)
	}
	return nil
}

// auditJSONWithoutIdentity is used for legacy JSON columns whose schema does
// not define an identity payload. The strict shared auditor still validates
// one bounded UTF-8 JSON value, duplicate object keys, and trailing input;
// this boundary then rejects every identity-key occurrence so no nested or
// renamed projection can pass unnoticed.
func auditJSONWithoutIdentity(encoded, surface, recordKey string) error {
	receipt, err := AuditJSONIdentity([]byte(encoded))
	if err != nil {
		return fmt.Errorf("legacy %s row %q JSON identity audit: %w", surface, recordKey, err)
	}
	if receipt.OccurrenceCount != 0 {
		occurrence := receipt.Occurrences[0]
		return fmt.Errorf("legacy %s row %q contains unsupported JSON identity %q at %s", surface, recordKey, occurrence.Key, occurrence.Pointer)
	}
	return nil
}

// auditTypedTurnJSON permits only the two numeric identity fields that the
// typed legacy turn decoders consume. It intentionally does not retain the
// audit receipt or any payload value in the public inventory result.
func auditTypedTurnJSON(encoded, surface, recordKey string) error {
	receipt, err := AuditJSONIdentity([]byte(encoded))
	if err != nil {
		return fmt.Errorf("legacy %s row %q JSON identity audit: %w", surface, recordKey, err)
	}
	for _, occurrence := range receipt.Occurrences {
		switch occurrence.Pointer {
		case "/thread_id":
			if occurrence.Key != JSONIdentityKeyThreadID || occurrence.ValueKind != JSONIdentityValueInteger {
				return fmt.Errorf("legacy %s row %q has invalid typed JSON thread_id", surface, recordKey)
			}
		case "/closed_thread_id":
			if occurrence.Key != JSONIdentityKeyClosedThreadID || (occurrence.ValueKind != JSONIdentityValueInteger && occurrence.ValueKind != JSONIdentityValueNull) {
				return fmt.Errorf("legacy %s row %q has invalid typed JSON closed_thread_id", surface, recordKey)
			}
		default:
			return fmt.Errorf("legacy %s row %q contains unsupported JSON identity %q at %s", surface, recordKey, occurrence.Key, occurrence.Pointer)
		}
	}
	return nil
}

func parseRequiredJSONInteger(raw json.RawMessage, field string) (int64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("%s is required", field)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || strings.ContainsAny(string(trimmed), `".eE`) {
		return 0, fmt.Errorf("%s must be an integer JSON number", field)
	}
	value, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer JSON number: %w", field, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", field)
	}
	return value, nil
}

func parseOptionalJSONInteger(raw json.RawMessage, field string) (int64, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false, nil
	}
	value, err := parseRequiredJSONInteger(raw, field)
	if err != nil {
		return 0, false, err
	}
	return value, true, nil
}

func parseChatGPTConversationMetadata(metaJSON string) (string, error) {
	// AuditJSONIdentity walks every object, not only identity keys, and thus
	// rejects duplicate keys, malformed/trailing input, and invalid UTF-8
	// before map decoding could silently apply a last-value-wins rule.
	if _, err := AuditJSONIdentity([]byte(metaJSON)); err != nil {
		return "", fmt.Errorf("meta_json failed strict JSON audit: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metaJSON), &object); err != nil {
		return "", errors.New("meta_json is not valid JSON")
	}
	raw, ok := object["conversation_id"]
	if !ok {
		return "", errors.New("meta_json lacks conversation_id")
	}
	var conversationID string
	if err := json.Unmarshal(raw, &conversationID); err != nil || strings.TrimSpace(conversationID) == "" {
		return "", errors.New("conversation_id must be an exact nonempty JSON string")
	}
	return conversationID, nil
}

func chatGPTLegacyTuple(conversationID string) (string, int64, error) {
	if conversationID == "" {
		return "", 0, errors.New("ChatGPT conversation ID is empty")
	}
	digest := sha256.Sum256([]byte(conversationID))
	sessionID := "chatgpt-" + hex.EncodeToString(digest[:8])
	threadID := int64(binary.BigEndian.Uint64(digest[:8]) & math.MaxInt64)
	if threadID == 0 {
		threadID = 1
	}
	return sessionID, threadID, nil
}
