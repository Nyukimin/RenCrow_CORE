package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	_ "modernc.org/sqlite"
)

func (s *L1SQLiteStore) AppendEvent(ctx context.Context, eventType string, namespace string, sessionID string, threadID int64, payload map[string]interface{}, source string) (*L1EventLogEntry, error) {
	return appendL1EventLog(ctx, s.db, eventType, namespace, sessionID, threadID, payload, source)
}

func (s *L1SQLiteStore) RecentEvents(ctx context.Context, namespace string, limit int) ([]L1EventLogEntry, error) {
	namespace = strings.TrimSpace(namespace)
	if err := ValidateL1Namespace(namespace); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, event_type, namespace, session_id, thread_id, payload_json, source, created_at
FROM l1_event_log
WHERE namespace = ?
ORDER BY created_at DESC
LIMIT ?
`, namespace, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 event log: %w", err)
	}
	defer rows.Close()
	return scanL1EventLogEntries(rows)
}

func (s *L1SQLiteStore) SaveRecallTrace(ctx context.Context, trace domconv.RecallTrace) error {
	if strings.TrimSpace(trace.ResponseID) == "" {
		return errors.New("response_id is required")
	}
	if strings.TrimSpace(trace.SessionID) == "" {
		return errors.New("session_id is required")
	}
	if trace.CreatedAt.IsZero() {
		trace.CreatedAt = time.Now().UTC()
	}
	traceID := RecallTraceID(trace.SessionID, trace.CreatedAt, trace.ResponseID)
	records := TraceItemRecordsFromPack(traceID, trace.Items)
	injectedCount := 0
	totalTokens := 0
	for _, item := range records {
		if item.Injected {
			injectedCount++
			totalTokens += item.TokenCount
		}
	}
	if err := s.StartRecallTrace(ctx, domconv.RecallTraceRecord{
		TraceID:             traceID,
		TurnID:              trace.ResponseID,
		ChatID:              trace.SessionID,
		Persona:             firstNonEmptyString(trace.Role, "mio"),
		UserMessageHash:     HashRecallText(trace.ResponseID),
		QueryTextRedacted:   RedactedRecallQuery(trace.ResponseID),
		CreatedAt:           trace.CreatedAt,
		RecallPolicyVersion: "memory-lifecycle-v1",
		TotalCandidates:     len(records),
		InjectedCount:       injectedCount,
		TotalInjectedTokens: totalTokens,
		Status:              "completed",
	}); err != nil {
		return err
	}
	if err := s.AddRecallTraceItems(ctx, traceID, records); err != nil {
		return err
	}
	if err := s.AddPromptInjectionEvents(ctx, traceID, PromptInjectionEventsFromItems(traceID, records, trace.CreatedAt)); err != nil {
		return err
	}
	payload := map[string]interface{}{
		"response_id": trace.ResponseID,
		"session_id":  trace.SessionID,
		"role":        trace.Role,
		"items":       trace.Items,
		"created_at":  trace.CreatedAt.UTC().Format(time.RFC3339),
	}
	_, err := s.AppendEvent(ctx, "recall.trace", "conv:"+trace.SessionID, trace.SessionID, 0, payload, "recall")
	return err
}

func (s *L1SQLiteStore) RecentRecallTraces(ctx context.Context, sessionID string, limit int) ([]domconv.RecallTrace, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	tableTraces, err := s.recentRecallTracesFromTables(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	if len(tableTraces) > 0 {
		return tableTraces, nil
	}
	var rows *sql.Rows
	if strings.TrimSpace(sessionID) == "" {
		rows, err = s.db.QueryContext(ctx, `
SELECT payload_json, created_at
FROM l1_event_log
WHERE event_type = 'recall.trace'
ORDER BY created_at DESC
LIMIT ?`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
SELECT payload_json, created_at
FROM l1_event_log
WHERE event_type = 'recall.trace' AND session_id = ?
ORDER BY created_at DESC
LIMIT ?`, strings.TrimSpace(sessionID), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var traces []domconv.RecallTrace
	for rows.Next() {
		var payloadJSON string
		var createdAt time.Time
		if err := rows.Scan(&payloadJSON, &createdAt); err != nil {
			return nil, err
		}
		var payload struct {
			ResponseID string                    `json:"response_id"`
			SessionID  string                    `json:"session_id"`
			Role       string                    `json:"role"`
			Items      []domconv.RecallTraceItem `json:"items"`
			CreatedAt  string                    `json:"created_at"`
		}
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return nil, err
		}
		traceCreatedAt := createdAt
		if parsed, err := time.Parse(time.RFC3339, payload.CreatedAt); err == nil {
			traceCreatedAt = parsed
		}
		traces = append(traces, domconv.RecallTrace{
			ResponseID: payload.ResponseID,
			SessionID:  payload.SessionID,
			Role:       payload.Role,
			Items:      payload.Items,
			CreatedAt:  traceCreatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return traces, nil
}

func (s *L1SQLiteStore) recentRecallTracesFromTables(ctx context.Context, sessionID string, limit int) ([]domconv.RecallTrace, error) {
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(sessionID) == "" {
		rows, err = s.db.QueryContext(ctx, `
SELECT trace_id, turn_id, chat_id, persona, created_at
FROM recall_trace
ORDER BY created_at DESC
LIMIT ?`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
SELECT trace_id, turn_id, chat_id, persona, created_at
FROM recall_trace
WHERE chat_id = ?
ORDER BY created_at DESC
LIMIT ?`, strings.TrimSpace(sessionID), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var traces []domconv.RecallTrace
	for rows.Next() {
		var traceID string
		var turnID string
		var chatID string
		var persona string
		var createdAt time.Time
		if err := rows.Scan(&traceID, &turnID, &chatID, &persona, &createdAt); err != nil {
			return nil, err
		}
		items, err := s.recallTraceItems(ctx, traceID)
		if err != nil {
			return nil, err
		}
		traces = append(traces, domconv.RecallTrace{
			ResponseID: turnID,
			SessionID:  chatID,
			Role:       persona,
			Items:      items,
			CreatedAt:  createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return traces, nil
}

func (s *L1SQLiteStore) recallTraceItems(ctx context.Context, traceID string) ([]domconv.RecallTraceItem, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT layer, kind, memory_id, source_id, source_url, source_type, summary, status,
       reason, prompt_section, token_count, score, retrieved_at
FROM recall_trace_item
WHERE trace_id = ?
ORDER BY item_id ASC`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domconv.RecallTraceItem
	for rows.Next() {
		var item domconv.RecallTraceItem
		var sourceURL string
		var status string
		var retrievedAt sql.NullTime
		if err := rows.Scan(&item.Layer, &item.Kind, &item.MemoryID, &item.SourceID, &sourceURL, &item.SourceType,
			&item.Summary, &status, &item.Reason, &item.PromptSection, &item.TokenCount, &item.Score, &retrievedAt); err != nil {
			return nil, err
		}
		item.Status = status
		if status == domconv.TraceStatusInjected {
			item.Decision = "included"
		} else {
			item.Decision = "rejected"
		}
		if sourceURL != "" {
			item.SourceURLs = []string{sourceURL}
		}
		if retrievedAt.Valid {
			item.RetrievedAt = retrievedAt.Time
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
