package l1sqlite

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

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func (s *L1SQLiteStore) StartRecallTrace(ctx context.Context, trace domconv.RecallTraceRecord) error {
	if trace.TraceID.Validate() != nil {
		return errors.New("canonical trace_id is required")
	}
	if trace.TurnID.Validate() != nil {
		return errors.New("canonical turn_id is required")
	}
	if trace.RootTaskID.Validate() != nil {
		return errors.New("canonical root_task_id is required")
	}
	if strings.TrimSpace(trace.ChatID) == "" {
		return errors.New("chat_id is required")
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
INSERT INTO recall_trace (
	trace_id, owner_id, turn_id, root_task_id, chat_id, persona, route, user_message_hash, query_text_redacted,
	created_at, model_id, prompt_version, recall_policy_version, total_candidates,
	injected_count, total_injected_tokens, status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, trace.TraceID, trace.OwnerID, trace.TurnID, trace.RootTaskID, trace.ChatID, trace.Persona, trace.Route, trace.UserMessageHash, trace.QueryTextRedacted,
		trace.CreatedAt.UTC(), trace.ModelID, trace.PromptVersion, trace.RecallPolicyVersion, trace.TotalCandidates,
		trace.InjectedCount, trace.TotalInjectedTokens, trace.Status)
	if err != nil {
		return fmt.Errorf("failed to start recall trace: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) AddRecallTraceItems(ctx context.Context, traceID string, items []domconv.RecallTraceItemRecord) error {
	traceID = strings.TrimSpace(traceID)
	if modulecore.TraceID(traceID).Validate() != nil {
		return errors.New("canonical trace_id is required")
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
		if modulecore.TraceID(item.TraceID).Validate() != nil || item.TraceID != traceID {
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
	prompt_section, token_count, sensitivity, memory_state, is_raw_or_summary, retrieved_at,
	published_at, event_id, summary, kind
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.ItemID, item.TraceID, item.Layer, item.MemoryID, item.SourceID, item.SourceURL, item.SourceType, item.Status,
			item.Score, item.Relevance, item.Recency, item.Confidence, item.SourceTrust, item.Reason, injected,
			item.PromptSection, item.TokenCount, item.Sensitivity, item.MemoryState, item.IsRawOrSummary, retrievedAt,
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
	if modulecore.TraceID(traceID).Validate() != nil {
		return errors.New("canonical trace_id is required")
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
		if modulecore.TraceID(event.TraceID).Validate() != nil || event.TraceID != traceID {
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
	if modulecore.TraceID(traceID).Validate() != nil {
		return errors.New("canonical trace_id is required")
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

func HashRecallText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func RedactedRecallQuery(text string) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= 160 {
		return text
	}
	runes := []rune(text)
	return string(runes[:160])
}

func TraceItemRecordsFromPack(traceID string, items []domconv.RecallTraceItem) []domconv.RecallTraceItemRecord {
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
			MemoryState:    item.MemoryState,
			Sensitivity:    item.Sensitivity,
			IsRawOrSummary: "summary",
			RetrievedAt:    item.RetrievedAt,
			Summary:        item.Summary,
			Kind:           item.Kind,
		})
	}
	return out
}

func PromptInjectionEventsFromItems(traceID string, records []domconv.RecallTraceItemRecord, createdAt time.Time) []domconv.PromptInjectionEventRecord {
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
				// injection_id is the table primary key; leaving it empty
				// collides on the second section of the same trace.
				InjectionID:   fmt.Sprintf("%s:injection:%04d", traceID, len(order)),
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
