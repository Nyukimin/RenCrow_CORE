package eventtaskmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

type fileIdentity struct {
	size    int64
	modTime time.Time
	mode    os.FileMode
	sha256  string
}

type sourceEvent struct {
	event modulecore.EventEnvelope
}

type dependencyEdge struct {
	eventID, dependencyID modulecore.EventID
	relation              string
}

func prepare(ctx context.Context, paths resolvedPaths) (prepared, error) {
	eventIdentity, err := inspectSource(paths.sourceEventStore)
	if err != nil {
		return prepared{}, coded("source_changed", "inspect event store snapshot: %v", err)
	}
	l1Identity, err := inspectSource(paths.sourceConversationL1)
	if err != nil {
		return prepared{}, coded("source_changed", "inspect conversation L1 snapshot: %v", err)
	}
	reportIdentity, err := inspectSource(paths.sourceExecutionReports)
	if err != nil {
		return prepared{}, coded("source_changed", "inspect execution report snapshot: %v", err)
	}
	if err := rejectSQLiteSidecars(paths.sourceEventStore); err != nil {
		return prepared{}, coded("source_sidecar", "event store snapshot: %v", err)
	}
	if err := rejectSQLiteSidecars(paths.sourceConversationL1); err != nil {
		return prepared{}, coded("source_sidecar", "conversation L1 snapshot: %v", err)
	}

	receipts, err := loadReceiptMappings(ctx, paths.sourceConversationL1)
	if err != nil {
		return prepared{}, err
	}
	legacy, edges, err := loadLegacyEvents(ctx, paths.sourceEventStore)
	if err != nil {
		return prepared{}, err
	}
	if err := verifySourceIdentity(paths.sourceEventStore, eventIdentity); err != nil {
		return prepared{}, coded("source_changed", "event store snapshot changed during read: %v", err)
	}
	if err := verifySourceIdentity(paths.sourceConversationL1, l1Identity); err != nil {
		return prepared{}, coded("source_changed", "conversation L1 snapshot changed during read: %v", err)
	}

	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		TotalEvents:   len(legacy), Dependencies: len(edges),
		SourceEventStoreSHA256:     eventIdentity.sha256,
		SourceConversationL1SHA256: l1Identity.sha256,
	}
	events := make([]modulecore.EventEnvelope, len(legacy))
	derived := make(map[string]modulecore.TaskID)
	eventTasksByLegacyJob := make(map[string]modulecore.TaskID)
	for index, item := range legacy {
		event := item.event
		event.EventSeq = modulecore.EventSeq(index + 1)
		if event.ComponentID == "orchestrator" {
			manifest.OrchestratorEvents++
			if event.Payload == nil {
				event.Payload = make(map[string]any)
			}
			legacyJob, _ := event.Payload["job_id"].(string)
			mapped, method, err := migrateOrchestratorPayload(event.TraceID, event.EventSeq, event.Payload, receipts, derived)
			if err != nil {
				return prepared{}, coded("payload_invalid", "event %q: %v", event.EventID, err)
			}
			if mapped != "" {
				event.TaskID = mapped
				if existing, ok := eventTasksByLegacyJob[legacyJob]; ok && existing != mapped {
					return prepared{}, coded("event_job_ambiguous", "legacy orchestrator job_id maps to multiple TaskID values")
				}
				eventTasksByLegacyJob[legacyJob] = mapped
			}
			switch method {
			case "receipt":
				manifest.MappedByReceipt++
			case "derived":
				manifest.MappedDerived++
			}
		}
		if event.TaskID == "" {
			manifest.NoTaskEvents++
		}
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return prepared{}, coded("envelope_invalid", "event %q after migration: %v", event.EventID, err)
		}
		events[index] = event
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		return prepared{}, coded("graph_invalid", "%v", err)
	}
	digest, err := canonicalOutputSHA(events)
	if err != nil {
		return prepared{}, coded("output_hash", "%v", err)
	}
	manifest.CanonicalOutputSetSHA256 = digest
	reports, reportCounts, reportDigest, err := loadAndMigrateExecutionReports(ctx, paths.sourceExecutionReports, eventTasksByLegacyJob)
	if err != nil {
		return prepared{}, err
	}
	if err := verifySourceIdentity(paths.sourceExecutionReports, reportIdentity); err != nil {
		return prepared{}, coded("source_changed", "execution report snapshot changed during read: %v", err)
	}
	reportTasks, err := executionReportTaskMappings(reports)
	if err != nil {
		return prepared{}, err
	}
	resiliencePlan, resilienceIdentity, err := loadAndMigrateResilience(paths.sourceResilienceRoot, reportTasks)
	if err != nil {
		return prepared{}, err
	}
	if err := verifyResilienceSourceIdentity(paths.sourceResilienceRoot, resilienceIdentity); err != nil {
		return prepared{}, coded("source_changed", "resilience snapshot changed during read: %v", err)
	}
	if err := verifySourceIdentity(paths.sourceEventStore, eventIdentity); err != nil {
		return prepared{}, coded("source_changed", "event store snapshot changed before plan completion: %v", err)
	}
	if err := verifySourceIdentity(paths.sourceConversationL1, l1Identity); err != nil {
		return prepared{}, coded("source_changed", "conversation L1 snapshot changed before plan completion: %v", err)
	}
	if err := verifySourceIdentity(paths.sourceExecutionReports, reportIdentity); err != nil {
		return prepared{}, coded("source_changed", "execution report snapshot changed before plan completion: %v", err)
	}
	if err := verifyResilienceSourceIdentity(paths.sourceResilienceRoot, resilienceIdentity); err != nil {
		return prepared{}, coded("source_changed", "resilience snapshot changed before plan completion: %v", err)
	}
	manifest.SourceExecutionReportsSHA256 = reportIdentity.sha256
	manifest.CanonicalExecutionReportsSHA256 = reportDigest
	manifest.ExecutionReportRows = len(reports)
	manifest.MappedReportByEvent = reportCounts.byEvent
	manifest.MappedReportDerived = reportCounts.derived
	manifest.SourceResilienceSHA256 = resilienceIdentity.sha256
	manifest.CanonicalResilienceSHA256 = resiliencePlan.sha256
	manifest.ResilienceFiles = len(resiliencePlan.files)
	manifest.ResilienceIncidents = resiliencePlan.incidents
	manifest.MappedRepairByReport = resiliencePlan.mappedByReport
	return prepared{events: events, reports: reports, resilience: resiliencePlan, manifest: manifest}, nil
}

func inspectSource(path string) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fileIdentity{}, errors.New("source is not a regular non-symlink file")
	}
	digest, err := hashFile(path)
	if err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{size: info.Size(), modTime: info.ModTime(), mode: info.Mode(), sha256: digest}, nil
}

func verifySourceIdentity(path string, before fileIdentity) error {
	after, err := inspectSource(path)
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("size, metadata, or SHA256 changed")
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func rejectSQLiteSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-journal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != 0 {
			return fmt.Errorf("non-empty or unsafe %s sidecar; checkpoint the writer-stopped snapshot", suffix)
		}
	}
	return nil
}

func openReadOnly(path string) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&immutable=1&_pragma=query_only%3d1&_pragma=busy_timeout%3d5000"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var queryOnly int
	if err := db.QueryRow(`PRAGMA query_only`).Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = db.Close()
		return nil, errors.New("sqlite snapshot is not query-only")
	}
	return db, nil
}

func requireColumns(ctx context.Context, db *sql.DB, table string, required []string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := make(map[string]bool)
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			return err
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, column := range required {
		if !found[column] {
			return fmt.Errorf("table %s is missing required column %s", table, column)
		}
	}
	return nil
}

func loadReceiptMappings(ctx context.Context, path string) (map[modulecore.TraceID]modulecore.TaskID, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, coded("receipt_store_invalid", "open conversation L1 snapshot: %v", err)
	}
	defer db.Close()
	if err := requireColumns(ctx, db, "conversation_turn_receipt", []string{"trace_id", "root_task_id"}); err != nil {
		return nil, coded("receipt_schema_invalid", "%v", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT trace_id, root_task_id FROM conversation_turn_receipt ORDER BY rowid ASC`)
	if err != nil {
		return nil, coded("receipt_store_invalid", "%v", err)
	}
	defer rows.Close()
	result := make(map[modulecore.TraceID]modulecore.TaskID)
	for rows.Next() {
		var traceRaw, taskRaw string
		if err := rows.Scan(&traceRaw, &taskRaw); err != nil {
			return nil, coded("receipt_store_invalid", "%v", err)
		}
		traceID, taskID := modulecore.TraceID(traceRaw), modulecore.TaskID(taskRaw)
		if err := traceID.Validate(); err != nil {
			return nil, coded("receipt_identity_invalid", "trace_id: %v", err)
		}
		if err := taskID.Validate(); err != nil {
			return nil, coded("receipt_identity_invalid", "root_task_id: %v", err)
		}
		if existing, ok := result[traceID]; ok && existing != taskID {
			return nil, coded("receipt_ambiguous", "trace_id %q maps to multiple root_task_id values", traceID)
		}
		result[traceID] = taskID
	}
	if err := rows.Err(); err != nil {
		return nil, coded("receipt_store_invalid", "%v", err)
	}
	return result, nil
}

func loadLegacyEvents(ctx context.Context, path string) ([]sourceEvent, []dependencyEdge, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, nil, coded("event_store_invalid", "open event store snapshot: %v", err)
	}
	defer db.Close()
	if err := requireColumns(ctx, db, "event_envelope", []string{"event_id", "trace_id", "schema_version", "event_type", "component_id", "occurred_at", "envelope_json"}); err != nil {
		return nil, nil, coded("event_schema_invalid", "%v", err)
	}
	if present, err := hasColumn(ctx, db, "event_envelope", "event_seq"); err != nil {
		return nil, nil, coded("event_schema_invalid", "%v", err)
	} else if present {
		return nil, nil, coded("event_schema_invalid", "source event_envelope already has event_seq")
	}
	if err := requireColumns(ctx, db, "event_dependency", []string{"event_id", "dependency_event_id", "relation_type"}); err != nil {
		return nil, nil, coded("event_schema_invalid", "%v", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT rowid, event_id, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json FROM event_envelope ORDER BY rowid ASC`)
	if err != nil {
		return nil, nil, coded("event_store_invalid", "%v", err)
	}
	events := make([]sourceEvent, 0)
	for rows.Next() {
		var rowID int64
		var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON string
		if err := rows.Scan(&rowID, &eventID, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &envelopeJSON); err != nil {
			_ = rows.Close()
			return nil, nil, coded("event_store_invalid", "%v", err)
		}
		var event modulecore.EventEnvelope
		decoder := json.NewDecoder(strings.NewReader(envelopeJSON))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			_ = rows.Close()
			return nil, nil, coded("envelope_invalid", "rowid %d JSON: %v", rowID, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			_ = rows.Close()
			return nil, nil, coded("envelope_invalid", "rowid %d has trailing JSON", rowID)
		}
		parsedOccurredAt, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			_ = rows.Close()
			return nil, nil, coded("index_mismatch", "rowid %d occurred_at is invalid", rowID)
		}
		if string(event.EventID) != eventID || string(event.TraceID) != traceID || event.SchemaVersion != schemaVersion || event.EventType != eventType || event.ComponentID != componentID || event.OccurredAt.Format(time.RFC3339Nano) != parsedOccurredAt.Format(time.RFC3339Nano) || occurredAt != event.OccurredAt.Format(time.RFC3339Nano) {
			_ = rows.Close()
			return nil, nil, coded("index_mismatch", "rowid %d indexed columns differ from envelope JSON", rowID)
		}
		if event.EventSeq != 0 {
			_ = rows.Close()
			return nil, nil, coded("event_schema_invalid", "source is not a legacy pre-Step09 Event Store")
		}
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			_ = rows.Close()
			return nil, nil, coded("envelope_invalid", "rowid %d: %v", rowID, err)
		}
		events = append(events, sourceEvent{event: event})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, coded("event_store_invalid", "%v", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, coded("event_store_invalid", "%v", err)
	}
	edges, err := loadAndVerifyEdges(ctx, db, events)
	if err != nil {
		return nil, nil, err
	}
	return events, edges, nil
}

func hasColumn(ctx context.Context, db *sql.DB, table, wanted string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == wanted {
			return true, nil
		}
	}
	return false, rows.Err()
}

func loadAndVerifyEdges(ctx context.Context, db *sql.DB, events []sourceEvent) ([]dependencyEdge, error) {
	rows, err := db.QueryContext(ctx, `SELECT event_id, dependency_event_id, relation_type FROM event_dependency ORDER BY event_id, dependency_event_id, relation_type`)
	if err != nil {
		return nil, coded("edge_invalid", "%v", err)
	}
	defer rows.Close()
	edges := make([]dependencyEdge, 0)
	actual := make(map[string]int)
	for rows.Next() {
		var eventRaw, dependencyRaw, relation string
		if err := rows.Scan(&eventRaw, &dependencyRaw, &relation); err != nil {
			return nil, coded("edge_invalid", "%v", err)
		}
		eventID, dependencyID := modulecore.EventID(eventRaw), modulecore.EventID(dependencyRaw)
		if err := eventID.Validate(); err != nil {
			return nil, coded("edge_invalid", "event_id: %v", err)
		}
		if err := dependencyID.Validate(); err != nil {
			return nil, coded("edge_invalid", "dependency_event_id: %v", err)
		}
		if relation != "causation" && relation != "dependency" {
			return nil, coded("edge_invalid", "invalid relation_type %q", relation)
		}
		key := edgeKey(eventID, dependencyID, relation)
		actual[key]++
		if actual[key] != 1 {
			return nil, coded("edge_invalid", "duplicate event dependency edge")
		}
		edges = append(edges, dependencyEdge{eventID, dependencyID, relation})
	}
	if err := rows.Err(); err != nil {
		return nil, coded("edge_invalid", "%v", err)
	}
	expected := make(map[string]int)
	for _, item := range events {
		event := item.event
		if event.CausationEventID != "" {
			expected[edgeKey(event.EventID, event.CausationEventID, "causation")]++
		}
		for _, dependencyID := range event.DependencyEventIDs {
			expected[edgeKey(event.EventID, dependencyID, "dependency")]++
		}
	}
	if len(actual) != len(expected) {
		return nil, coded("edge_mismatch", "event_dependency table differs from envelope JSON")
	}
	for key, count := range expected {
		if count != 1 || actual[key] != 1 {
			return nil, coded("edge_mismatch", "event_dependency table differs from envelope JSON")
		}
	}
	return edges, nil
}

func edgeKey(eventID, dependencyID modulecore.EventID, relation string) string {
	return string(eventID) + "\x00" + string(dependencyID) + "\x00" + relation
}

func migrateOrchestratorPayload(traceID modulecore.TraceID, seq modulecore.EventSeq, payload map[string]any, receipts map[modulecore.TraceID]modulecore.TaskID, derived map[string]modulecore.TaskID) (modulecore.TaskID, string, error) {
	if payload == nil {
		payload = make(map[string]any)
	}
	legacyJob, hasJob := payload["job_id"]
	if _, exists := payload["event_seq"]; exists {
		return "", "", errors.New("payload already contains event_seq")
	}
	if _, exists := payload["task_id"]; exists {
		return "", "", errors.New("payload already contains task_id")
	}
	if legacySeq, exists := payload["seq"]; exists {
		numeric, ok := legacySeq.(float64)
		if !ok || numeric <= 0 || math.Trunc(numeric) != numeric {
			return "", "", errors.New("seq must be a positive integer when present")
		}
	}
	delete(payload, "seq")
	payload["event_seq"] = int64(seq)
	if !hasJob {
		return "", "", nil
	}
	jobID, ok := legacyJob.(string)
	if !ok {
		return "", "", errors.New("job_id must be a string")
	}
	delete(payload, "job_id")
	if jobID == "" {
		return "", "", nil
	}
	if taskID, ok := receipts[traceID]; ok {
		payload["task_id"] = string(taskID)
		return taskID, "receipt", nil
	}
	key := string(traceID) + "\x00" + jobID
	taskID, ok := derived[key]
	if !ok {
		raw, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "event_envelope", "trace_id+job_id", key)
		if err != nil {
			return "", "", err
		}
		taskID = modulecore.TaskID(raw)
		if err := taskID.Validate(); err != nil {
			return "", "", err
		}
		derived[key] = taskID
	}
	payload["task_id"] = string(taskID)
	return taskID, "derived", nil
}
