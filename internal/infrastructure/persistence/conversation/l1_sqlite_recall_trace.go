package conversation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

func (s *L1SQLiteStore) StartRecallTrace(ctx context.Context, trace domconv.RecallTraceRecord) error {
	trace.TraceID = strings.TrimSpace(trace.TraceID)
	if trace.TraceID == "" {
		return errors.New("trace_id is required")
	}
	if strings.TrimSpace(trace.ChatID) == "" {
		return errors.New("chat_id is required")
	}
	if strings.TrimSpace(trace.TurnID) == "" {
		trace.TurnID = trace.TraceID
	}
	if strings.TrimSpace(trace.Persona) == "" {
		trace.Persona = "mio"
	}
	if trace.CreatedAt.IsZero() {
		trace.CreatedAt = time.Now().UTC()
	}
	if strings.TrimSpace(trace.Status) == "" {
		trace.Status = "started"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT OR REPLACE INTO recall_trace (
	trace_id, turn_id, chat_id, persona, route, user_message_hash, query_text_redacted,
	created_at, model_id, prompt_version, recall_policy_version, total_candidates,
	injected_count, total_injected_tokens, status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, trace.TraceID, trace.TurnID, trace.ChatID, trace.Persona, trace.Route, trace.UserMessageHash, trace.QueryTextRedacted,
		trace.CreatedAt.UTC(), trace.ModelID, trace.PromptVersion, trace.RecallPolicyVersion, trace.TotalCandidates,
		trace.InjectedCount, trace.TotalInjectedTokens, trace.Status)
	if err != nil {
		return fmt.Errorf("failed to start recall trace: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) AddRecallTraceItems(ctx context.Context, traceID string, items []domconv.RecallTraceItemRecord) error {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return errors.New("trace_id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, item := range items {
		if strings.TrimSpace(item.ItemID) == "" {
			item.ItemID = fmt.Sprintf("%s:item:%04d", traceID, i)
		}
		if strings.TrimSpace(item.TraceID) == "" {
			item.TraceID = traceID
		}
		if item.TraceID != traceID {
			return fmt.Errorf("trace item %s belongs to different trace %s", item.ItemID, item.TraceID)
		}
		injected := 0
		if item.Injected {
			injected = 1
		}
		var retrievedAt any
		if !item.RetrievedAt.IsZero() {
			retrievedAt = item.RetrievedAt.UTC()
		}
		var publishedAt any
		if !item.PublishedAt.IsZero() {
			publishedAt = item.PublishedAt.UTC()
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO recall_trace_item (
	item_id, trace_id, layer, memory_id, source_id, source_url, source_type, status,
	score, relevance, recency, confidence, source_trust, reason, injected,
	prompt_section, token_count, sensitivity, is_raw_or_summary, retrieved_at,
	published_at, event_id, summary, kind
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.ItemID, item.TraceID, item.Layer, item.MemoryID, item.SourceID, item.SourceURL, item.SourceType, item.Status,
			item.Score, item.Relevance, item.Recency, item.Confidence, item.SourceTrust, item.Reason, injected,
			item.PromptSection, item.TokenCount, item.Sensitivity, item.IsRawOrSummary, retrievedAt,
			publishedAt, item.EventID, item.Summary, item.Kind); err != nil {
			return fmt.Errorf("failed to insert recall trace item: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *L1SQLiteStore) AddPromptInjectionEvents(ctx context.Context, traceID string, events []domconv.PromptInjectionEventRecord) error {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return errors.New("trace_id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, event := range events {
		if strings.TrimSpace(event.InjectionID) == "" {
			event.InjectionID = fmt.Sprintf("%s:injection:%04d", traceID, i)
		}
		if strings.TrimSpace(event.TraceID) == "" {
			event.TraceID = traceID
		}
		if event.TraceID != traceID {
			return fmt.Errorf("prompt injection event %s belongs to different trace %s", event.InjectionID, event.TraceID)
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		itemIDs, err := json.Marshal(event.ItemIDs)
		if err != nil {
			return fmt.Errorf("failed to marshal prompt injection item ids: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO prompt_injection_event (
	injection_id, trace_id, prompt_section, order_index, item_ids, token_count, redaction_level, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, event.InjectionID, event.TraceID, event.PromptSection, event.OrderIndex, string(itemIDs), event.TokenCount, event.RedactionLevel, event.CreatedAt.UTC()); err != nil {
			return fmt.Errorf("failed to insert prompt injection event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *L1SQLiteStore) FinishRecallTrace(ctx context.Context, traceID string, status string, injectedCount int, totalTokens int) error {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return errors.New("trace_id is required")
	}
	if strings.TrimSpace(status) == "" {
		status = "completed"
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE recall_trace
SET status = ?, injected_count = ?, total_injected_tokens = ?
WHERE trace_id = ?
`, status, injectedCount, totalTokens, traceID)
	if err != nil {
		return fmt.Errorf("failed to finish recall trace: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func recallTraceID(sessionID string, createdAt time.Time, userMessage string) string {
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	sum := sha256.Sum256([]byte(sessionID + "\n" + createdAt.UTC().Format(time.RFC3339Nano) + "\n" + userMessage))
	return "trace:" + safeRecallIDPart(sessionID) + ":" + createdAt.UTC().Format("20060102150405.000000000") + ":" + hex.EncodeToString(sum[:])[:12]
}

func hashRecallText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func redactedRecallQuery(text string) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= 160 {
		return text
	}
	runes := []rune(text)
	return string(runes[:160])
}

func safeRecallIDPart(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unknown"
	}
	if len(out) > 48 {
		return out[:48]
	}
	return out
}

func traceItemRecordsFromPack(traceID string, items []domconv.RecallTraceItem) []domconv.RecallTraceItemRecord {
	out := make([]domconv.RecallTraceItemRecord, 0, len(items))
	for i, item := range items {
		status := strings.TrimSpace(item.Status)
		if status == "" {
			status = domconv.TraceStatusRetrieved
			if item.Decision == "included" {
				status = domconv.TraceStatusInjected
			}
		}
		sourceURL := ""
		if len(item.SourceURLs) > 0 {
			sourceURL = item.SourceURLs[0]
		}
		out = append(out, domconv.RecallTraceItemRecord{
			ItemID:         fmt.Sprintf("%s:item:%04d", traceID, i),
			TraceID:        traceID,
			Layer:          item.Layer,
			MemoryID:       item.MemoryID,
			SourceID:       item.SourceID,
			SourceURL:      sourceURL,
			SourceType:     item.SourceType,
			Status:         status,
			Score:          float64(item.Score),
			Reason:         item.Reason,
			Injected:       status == domconv.TraceStatusInjected || item.Decision == "included",
			PromptSection:  item.PromptSection,
			TokenCount:     item.TokenCount,
			IsRawOrSummary: "summary",
			RetrievedAt:    item.RetrievedAt,
			Summary:        item.Summary,
			Kind:           item.Kind,
		})
	}
	return out
}

func promptInjectionEventsFromItems(traceID string, records []domconv.RecallTraceItemRecord, createdAt time.Time) []domconv.PromptInjectionEventRecord {
	bySection := map[string]*domconv.PromptInjectionEventRecord{}
	var order []string
	for _, item := range records {
		if !item.Injected {
			continue
		}
		section := strings.TrimSpace(item.PromptSection)
		if section == "" {
			section = domconv.PromptSectionConversation
		}
		event, ok := bySection[section]
		if !ok {
			event = &domconv.PromptInjectionEventRecord{
				TraceID:       traceID,
				PromptSection: section,
				OrderIndex:    len(order),
				CreatedAt:     createdAt,
			}
			bySection[section] = event
			order = append(order, section)
		}
		event.ItemIDs = append(event.ItemIDs, item.ItemID)
		event.TokenCount += item.TokenCount
	}
	out := make([]domconv.PromptInjectionEventRecord, 0, len(order))
	for _, section := range order {
		event := *bySection[section]
		out = append(out, event)
	}
	return out
}
