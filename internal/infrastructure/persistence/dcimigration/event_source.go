package dcimigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var eventStoreSpecs = map[string]tableSpec{
	"event_envelope": {Columns: []tableColumnSpec{
		{Name: "event_id", Type: "TEXT", NotNull: true, Primary: true},
		{Name: "trace_id", Type: "TEXT", NotNull: true},
		{Name: "schema_version", Type: "TEXT", NotNull: true},
		{Name: "event_type", Type: "TEXT", NotNull: true},
		{Name: "component_id", Type: "TEXT", NotNull: true},
		{Name: "occurred_at", Type: "TEXT", NotNull: true},
		{Name: "envelope_json", Type: "TEXT", NotNull: true},
	}},
	"event_dependency": {Columns: []tableColumnSpec{
		{Name: "event_id", Type: "TEXT", NotNull: true, Primary: true},
		{Name: "dependency_event_id", Type: "TEXT", NotNull: true, Primary: true},
		{Name: "relation_type", Type: "TEXT", NotNull: true},
	}},
}

func loadEventStore(ctx context.Context, path string) (map[string]struct{}, SourceCounts, sourceHashes, error) {
	before, err := fileSHA256(path)
	if err != nil {
		return nil, SourceCounts{}, sourceHashes{}, newCodedError("source_read", "hash canonical Event Store: %v", err)
	}
	db, err := openSQLiteReadOnly(ctx, path)
	if err != nil {
		return nil, SourceCounts{}, sourceHashes{}, newCodedError("source_read", "open canonical Event Store: %v", err)
	}
	ids, count, lines, readErr := queryEventStore(ctx, db)
	var logical logicalHashes
	if readErr == nil {
		logical, readErr = hashSQLiteLogical(ctx, db, nil)
	}
	closeErr := db.Close()
	if readErr != nil {
		return nil, SourceCounts{}, sourceHashes{}, readErr
	}
	if closeErr != nil {
		return nil, SourceCounts{}, sourceHashes{}, newCodedError("source_read", "close canonical Event Store: %v", closeErr)
	}
	after, err := fileSHA256(path)
	if err != nil || after != before {
		return nil, SourceCounts{}, sourceHashes{}, newCodedError("source_changed", "canonical Event Store changed during read")
	}
	return ids, count, sourceHashes{
		DatabaseLogical: logical.Full,
		Schema:          logical.Schema,
		Classification:  hashCanonicalLines(lines),
		NonDCI:          logical.NonDCI,
	}, nil
}

func queryEventStore(ctx context.Context, db *sql.DB) (map[string]struct{}, SourceCounts, []string, error) {
	if err := checkUserVersion(ctx, db, 0); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("unknown_schema", "canonical Event Store schema version: %v", err)
	}
	tables, err := schemaUserTables(ctx, db)
	if err != nil {
		return nil, SourceCounts{}, nil, newCodedError("unknown_schema", "inspect canonical Event Store tables: %v", err)
	}
	if err := requireTableSet(tables, []string{"event_envelope", "event_dependency"}, true); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("unknown_schema", "%v", err)
	}
	if err := inspectTable(ctx, db, "event_envelope", eventStoreSpecs["event_envelope"]); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("unknown_schema", "canonical Event Store envelope schema: %v", err)
	}
	if err := inspectCompositePrimaryKey(ctx, db, "event_dependency", []string{"event_id", "dependency_event_id"}); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("unknown_schema", "canonical Event Store dependency schema: %v", err)
	}
	if err := inspectTableWithoutPrimaryValidation(ctx, db, "event_dependency", eventStoreSpecs["event_dependency"]); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("unknown_schema", "canonical Event Store dependency schema: %v", err)
	}
	if err := inspectEventDependencyForeignKeys(ctx, db); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("unknown_schema", "%v", err)
	}
	ids := make(map[string]struct{})
	traces := make(map[string]string)
	events := make([]modulecore.EventEnvelope, 0)
	expectedDependencies := make(map[string]map[string]string)
	rows, err := db.QueryContext(ctx, `SELECT event_id, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json FROM event_envelope ORDER BY rowid`)
	if err != nil {
		return nil, SourceCounts{}, nil, newCodedError("source_read", "read canonical Event Store envelopes: %v", err)
	}
	var lines []string
	count := SourceCounts{}
	for rows.Next() {
		values := make([]any, 7)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			_ = rows.Close()
			return nil, SourceCounts{}, nil, newCodedError("source_read", "scan canonical Event Store envelope: %v", err)
		}
		eventID := readText(values[0])
		traceID := readText(values[1])
		envelope, err := validateExistingEventIdentity(eventID, traceID, readText(values[2]), readText(values[3]), readText(values[4]), readText(values[5]), readText(values[6]))
		if err != nil {
			_ = rows.Close()
			return nil, SourceCounts{}, nil, newCodedError(errorCode(err, "malformed_source"), "canonical Event Store envelope: %v", err)
		}
		if _, exists := ids[eventID]; exists {
			_ = rows.Close()
			return nil, SourceCounts{}, nil, newCodedError("malformed_source", "canonical Event Store has duplicate event_id")
		}
		ids[eventID] = struct{}{}
		traces[eventID] = traceID
		if strings.HasPrefix(envelope.EventType, "dci.") {
			_ = rows.Close()
			return nil, SourceCounts{}, nil, newCodedError("partial_dci_history", "canonical Event Store already contains DCI history")
		}
		events = append(events, envelope)
		dependencies := make(map[string]string, 1+len(envelope.DependencyEventIDs))
		if envelope.CausationEventID != "" {
			dependencies[string(envelope.CausationEventID)] = "causation"
		}
		for _, dependencyID := range envelope.DependencyEventIDs {
			dependencies[string(dependencyID)] = "dependency"
		}
		expectedDependencies[eventID] = dependencies
		count.EventStore++
		canonicalJSON, _ := json.Marshal(envelope)
		line, _ := json.Marshal([]any{"envelope", eventID, traceID, readText(values[2]), readText(values[3]), readText(values[4]), readText(values[5]), string(canonicalJSON)})
		lines = append(lines, string(line))
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, SourceCounts{}, nil, newCodedError("source_read", "iterate canonical Event Store envelopes: %v", err)
	}
	if err := rows.Close(); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("source_read", "close canonical Event Store envelopes: %v", err)
	}
	dependencyRows, err := db.QueryContext(ctx, `SELECT event_id, dependency_event_id, relation_type FROM event_dependency ORDER BY rowid`)
	if err != nil {
		return nil, SourceCounts{}, nil, newCodedError("source_read", "read canonical Event Store dependencies: %v", err)
	}
	actualDependencies := make(map[string]map[string]string)
	for dependencyRows.Next() {
		var eventID, dependencyID, relation string
		if err := dependencyRows.Scan(&eventID, &dependencyID, &relation); err != nil {
			_ = dependencyRows.Close()
			return nil, SourceCounts{}, nil, newCodedError("source_read", "scan canonical Event Store dependency: %v", err)
		}
		if eventID == "" || dependencyID == "" || (relation != "causation" && relation != "dependency") {
			_ = dependencyRows.Close()
			return nil, SourceCounts{}, nil, newCodedError("malformed_source", "canonical Event Store dependency is invalid")
		}
		if _, exists := ids[eventID]; !exists {
			_ = dependencyRows.Close()
			return nil, SourceCounts{}, nil, newCodedError("malformed_source", "canonical Event Store dependency references a missing event")
		}
		if _, exists := ids[dependencyID]; !exists {
			_ = dependencyRows.Close()
			return nil, SourceCounts{}, nil, newCodedError("malformed_source", "canonical Event Store dependency references a missing event")
		}
		byEvent := actualDependencies[eventID]
		if byEvent == nil {
			byEvent = make(map[string]string)
			actualDependencies[eventID] = byEvent
		}
		if prior, exists := byEvent[dependencyID]; exists && prior != relation {
			_ = dependencyRows.Close()
			return nil, SourceCounts{}, nil, newCodedError("event_dependency_mismatch", "canonical Event Store dependency relation is duplicated with another type")
		}
		byEvent[dependencyID] = relation
		if err := modulecore.EventID(eventID).Validate(); err != nil {
			_ = dependencyRows.Close()
			return nil, SourceCounts{}, nil, newCodedError("malformed_source", "canonical Event Store dependency event_id: %v", err)
		}
		if err := modulecore.EventID(dependencyID).Validate(); err != nil {
			_ = dependencyRows.Close()
			return nil, SourceCounts{}, nil, newCodedError("malformed_source", "canonical Event Store dependency dependency_event_id: %v", err)
		}
		if traces[eventID] != traces[dependencyID] {
			_ = dependencyRows.Close()
			return nil, SourceCounts{}, nil, newCodedError("malformed_source", "canonical Event Store dependency crosses traces")
		}
		line, _ := json.Marshal([]any{"dependency", eventID, dependencyID, relation})
		lines = append(lines, string(line))
	}
	if err := dependencyRows.Err(); err != nil {
		_ = dependencyRows.Close()
		return nil, SourceCounts{}, nil, newCodedError("source_read", "iterate canonical Event Store dependencies: %v", err)
	}
	if err := dependencyRows.Close(); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("source_read", "close canonical Event Store dependencies: %v", err)
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		return nil, SourceCounts{}, nil, newCodedError("event_graph_invalid", "canonical Event Store event graph: %v", err)
	}
	if !equalEventDependencies(expectedDependencies, actualDependencies) {
		return nil, SourceCounts{}, nil, newCodedError("event_dependency_mismatch", "canonical Event Store dependencies do not match envelope references")
	}
	return ids, count, lines, nil
}

func validateExistingEventIdentity(eventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON string) (modulecore.EventEnvelope, error) {
	if err := modulecore.EventID(eventID).Validate(); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("event_id: %v", err)
	}
	if err := modulecore.TraceID(traceID).Validate(); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("trace_id: %v", err)
	}
	if schemaVersion != modulecore.EventEnvelopeSchemaVersion || eventType == "" || componentID == "" || occurredAt == "" || envelopeJSON == "" {
		return modulecore.EventEnvelope{}, fmt.Errorf("required envelope field is empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, occurredAt); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("occurred_at is not RFC3339: %w", err)
	}
	var envelope modulecore.EventEnvelope
	if err := decodeStrictJSON([]byte(envelopeJSON), &envelope, nil); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("envelope_json is not a canonical EventEnvelope: %w", err)
	}
	if err := modulecore.ValidateEventEnvelope(envelope); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("envelope_json validation: %w", err)
	}
	if string(envelope.EventID) != eventID || string(envelope.TraceID) != traceID || envelope.SchemaVersion != schemaVersion || envelope.EventType != eventType || envelope.ComponentID != componentID || envelope.OccurredAt.UTC().Format(time.RFC3339Nano) != occurredAt {
		return modulecore.EventEnvelope{}, newCodedError("event_envelope_mismatch", "envelope_json does not match canonical columns")
	}
	return envelope, nil
}

func equalEventDependencies(expected, actual map[string]map[string]string) bool {
	for eventID, expectedSet := range expected {
		actualSet, ok := actual[eventID]
		if !ok {
			if len(expectedSet) == 0 {
				continue
			}
			return false
		}
		if len(expectedSet) != len(actualSet) {
			return false
		}
		for dependencyID, relation := range expectedSet {
			if actualSet[dependencyID] != relation {
				return false
			}
		}
	}
	for eventID := range actual {
		if _, ok := expected[eventID]; !ok {
			return false
		}
	}
	return true
}

func inspectTableWithoutPrimaryValidation(ctx context.Context, db *sql.DB, table string, spec tableSpec) error {
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
	}
	return nil
}

func inspectCompositePrimaryKey(ctx context.Context, db *sql.DB, table string, expected []string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info('"+strings.ReplaceAll(table, "'", "''")+"')")
	if err != nil {
		return err
	}
	defer rows.Close()
	actual := make([]string, len(expected))
	for rows.Next() {
		var cid, notNull, primary int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primary); err != nil {
			return err
		}
		if primary > 0 && primary <= len(expected) {
			actual[primary-1] = name
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return fmt.Errorf("SQLite table %s primary key must be (%s)", table, strings.Join(expected, ", "))
		}
	}
	return nil
}

func inspectEventDependencyForeignKeys(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list('event_dependency')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	seenFrom := make(map[string]struct{}, 2)
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return err
		}
		count++
		if table != "event_envelope" || (from != "event_id" && from != "dependency_event_id") || to != "event_id" || onUpdate != "RESTRICT" || onDelete != "RESTRICT" || match != "NONE" {
			return fmt.Errorf("event_dependency foreign key is invalid")
		}
		if _, exists := seenFrom[from]; exists {
			return fmt.Errorf("event_dependency foreign keys duplicate column %s", from)
		}
		seenFrom[from] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 2 || len(seenFrom) != 2 {
		return fmt.Errorf("event_dependency must have two foreign keys")
	}
	return nil
}
