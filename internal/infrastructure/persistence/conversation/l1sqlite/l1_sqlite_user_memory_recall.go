package l1sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func ownerScopeForRecall(ctx context.Context, requestID, ownerID string) (domaintool.ToolExecutionScope, error) {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil {
		return domaintool.ToolExecutionScope{}, domainmemory.ErrUserMemoryOwnerForbidden
	}
	if strings.TrimSpace(requestID) != "" && scope.RequestID != strings.TrimSpace(requestID) {
		return domaintool.ToolExecutionScope{}, domainmemory.ErrUserMemoryOwnerForbidden
	}
	if strings.TrimSpace(ownerID) == "" || scope.AuthenticatedUserID != strings.TrimSpace(ownerID) || !scope.Allows(domaintool.DataScopeUser) {
		return domaintool.ToolExecutionScope{}, domainmemory.ErrUserMemoryOwnerForbidden
	}
	return scope, nil
}

// OwnerRecallUserMemories performs the owner-only deterministic recall and
// persists its bounded decision trace before returning the owner projection.
func (s *L1SQLiteStore) OwnerRecallUserMemories(ctx context.Context, requestID, ownerID, query string, limit int) (domainmemory.UserMemoryOwnerRecallResult, error) {
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	query = strings.TrimSpace(query)
	if requestID == "" || ownerID == "" || query == "" || len([]rune(query)) > 512 {
		return domainmemory.UserMemoryOwnerRecallResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if limit <= 0 {
		limit = domainmemory.UserMemoryRecallDefaultLimit
	}
	if limit > domainmemory.UserMemoryRecallMaxLimit {
		return domainmemory.UserMemoryOwnerRecallResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if _, err := ownerScopeForRecall(ctx, requestID, ownerID); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, err
	}

	now := time.Now().UTC()
	traceID := OwnerRecallTraceID(requestID, ownerID, query)
	trace := domconv.RecallTraceRecord{
		TraceID:             traceID,
		OwnerID:             ownerID,
		TurnID:              traceID,
		ChatID:              ownerID,
		Persona:             "owner",
		Route:               "conversation_l1/user_memory/recall",
		UserMessageHash:     HashRecallText(query),
		QueryTextRedacted:   RedactedRecallQuery(query),
		CreatedAt:           now,
		RecallPolicyVersion: domainmemory.UserMemoryOwnerPolicyRevision,
		Status:              "started",
	}
	if err := s.StartRecallTrace(ctx, trace); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to start owner recall trace: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}

	memories, err := s.OwnerListUserMemories(ctx, ownerID, "", true, domainmemory.UserMemoryRecallMaxScan)
	if err != nil {
		failure := domconv.RecallTraceItemRecord{
			ItemID:        traceID + ":source-failure",
			TraceID:       traceID,
			Layer:         "L1",
			Kind:          "user_memory_source_failure",
			Status:        domainmemory.UserMemoryRecallStatusSourceFailure,
			Reason:        "user memory source unavailable",
			PromptSection: domconv.PromptSectionUserMemory,
		}
		if addErr := s.AddRecallTraceItems(ctx, traceID, []domconv.RecallTraceItemRecord{failure}); addErr == nil {
			_, _ = s.db.ExecContext(ctx, `UPDATE recall_trace SET total_candidates = 0 WHERE trace_id = ?`, traceID)
			_ = s.FinishRecallTrace(ctx, traceID, "partial", 0, 0)
		}
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to read owner memories: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	for _, item := range memories {
		if item.UserID != ownerID || item.Namespace != NamespaceKindUser+":"+ownerID {
			return domainmemory.UserMemoryOwnerRecallResult{}, domainmemory.ErrUserMemoryOwnerForbidden
		}
	}

	decisions := domainmemory.RankUserMemoriesForRecall(query, memories, limit)
	selected := make([]domainmemory.UserMemoryOwnerRecallItem, 0, limit)
	traceItems := make([]domconv.RecallTraceItemRecord, 0, len(decisions))
	for i, decision := range decisions {
		traceItem := domconv.RecallTraceItemRecord{
			ItemID:         fmt.Sprintf("%s:item:%04d", traceID, i),
			TraceID:        traceID,
			Layer:          "L1",
			Kind:           "user_memory",
			MemoryID:       decision.Item.ID,
			SourceType:     "user_memory",
			Status:         decision.Status,
			Score:          decision.Score,
			Reason:         decision.Reason,
			Injected:       decision.Selected,
			PromptSection:  domconv.PromptSectionUserMemory,
			MemoryState:    decision.Item.State,
			Sensitivity:    decision.Item.Sensitivity,
			IsRawOrSummary: "summary",
		}
		if len(decision.Item.EvidenceEventIDs) > 0 {
			traceItem.SourceID = decision.Item.EvidenceEventIDs[0]
		}
		if decision.Selected {
			traceItem.Summary = redactOwnerTraceSummary(decision.Item.Statement, decision.Item.Sensitivity, traceItem.Kind)
			traceItem.TokenCount = ownerTraceTokenCount(traceItem.Summary)
			selected = append(selected, domainmemory.UserMemoryOwnerRecallItem{
				UserMemoryOwnerView: domainmemory.UserMemoryOwnerViewFromMemory(decision.Item),
				Score:               decision.Score,
			})
		}
		traceItems = append(traceItems, traceItem)
	}
	trace.TotalCandidates = len(decisions)
	selectedCount := 0
	for _, decision := range decisions {
		if decision.Selected {
			selectedCount++
		}
	}
	trace.InjectedCount = selectedCount
	if _, err := s.db.ExecContext(ctx, `UPDATE recall_trace SET total_candidates = ? WHERE trace_id = ?`, trace.TotalCandidates, traceID); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to update owner recall trace totals: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if err := s.AddRecallTraceItems(ctx, traceID, traceItems); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to save owner recall trace items: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if err := s.AddPromptInjectionEvents(ctx, traceID, PromptInjectionEventsFromItems(traceID, traceItems, now)); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to save owner recall injection events: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if err := s.FinishRecallTrace(ctx, traceID, "completed", selectedCount, sumTraceTokens(traceItems)); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to finish owner recall trace: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}

	return domainmemory.UserMemoryOwnerRecallResult{
		Items: selected,
		Trace: domainmemory.UserMemoryRecallTrace{
			ID:                traceID,
			Status:            "completed",
			QueryTextRedacted: trace.QueryTextRedacted,
			TotalCandidates:   trace.TotalCandidates,
			SelectedCount:     selectedCount,
			CreatedAt:         now,
		},
		Receipt: newOwnerReadReceipt(requestID, domainmemory.UserMemoryOwnerOperationRecall, traceID, len(decisions), len(selected), now),
	}, nil
}

func (s *L1SQLiteStore) OwnerListRecallTraces(ctx context.Context, ownerID string, limit int) ([]domainmemory.UserMemoryTraceSummary, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if _, err := ownerScopeForRecall(ctx, "", ownerID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = domainmemory.UserMemoryTraceDefaultLimit
	}
	if limit > domainmemory.UserMemoryTraceMaxLimit {
		return nil, domainmemory.ErrUserMemoryOwnerInvalid
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT trace_id, status, route, persona, total_candidates, injected_count,
       total_injected_tokens, created_at
FROM recall_trace
WHERE owner_id = ?
ORDER BY created_at DESC, trace_id ASC
LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to list owner recall traces: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	defer rows.Close()
	items := make([]domainmemory.UserMemoryTraceSummary, 0)
	for rows.Next() {
		var item domainmemory.UserMemoryTraceSummary
		if err := rows.Scan(&item.ID, &item.Status, &item.Route, &item.Persona, &item.TotalCandidates, &item.InjectedCount, &item.TotalInjectedTokens, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("%w: failed to scan owner recall trace: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: failed to iterate owner recall traces: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	return items, nil
}

func (s *L1SQLiteStore) OwnerFindRecallTrace(ctx context.Context, ownerID, id string) (domainmemory.UserMemoryTraceDetail, error) {
	ownerID = strings.TrimSpace(ownerID)
	id = strings.TrimSpace(id)
	if ownerID == "" || id == "" {
		return domainmemory.UserMemoryTraceDetail{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if _, err := ownerScopeForRecall(ctx, "", ownerID); err != nil {
		return domainmemory.UserMemoryTraceDetail{}, err
	}
	var detail domainmemory.UserMemoryTraceDetail
	if err := s.db.QueryRowContext(ctx, `
SELECT trace_id, status, route, persona, total_candidates, injected_count,
       total_injected_tokens, created_at, query_text_redacted
FROM recall_trace
WHERE trace_id = ? AND owner_id = ?`, id, ownerID).Scan(
		&detail.ID, &detail.Status, &detail.Route, &detail.Persona, &detail.TotalCandidates,
		&detail.InjectedCount, &detail.TotalInjectedTokens, &detail.CreatedAt, &detail.QueryTextRedacted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainmemory.UserMemoryTraceDetail{}, domainmemory.ErrUserMemoryOwnerNotFound
		}
		return domainmemory.UserMemoryTraceDetail{}, fmt.Errorf("%w: failed to find owner recall trace: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT item_id, memory_id, kind, source_id, source_type, summary, score,
       status, reason, prompt_section, token_count, memory_state, sensitivity
FROM recall_trace_item
WHERE trace_id = ?
ORDER BY item_id ASC`, id)
	if err != nil {
		return domainmemory.UserMemoryTraceDetail{}, fmt.Errorf("%w: failed to read owner recall trace items: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var item domainmemory.UserMemoryTraceItem
		if err := rows.Scan(&item.ItemID, &item.MemoryID, &item.Kind, &item.SourceID, &item.SourceType, &item.Summary, &item.Score, &item.Status, &item.Reason, &item.PromptSection, &item.TokenCount, &item.MemoryState, &item.Sensitivity); err != nil {
			return domainmemory.UserMemoryTraceDetail{}, fmt.Errorf("%w: failed to scan owner recall trace item: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
		item.Summary = redactOwnerTraceSummary(item.Summary, item.Sensitivity, item.Kind)
		if item.Summary == "" {
			item.TokenCount = 0
		}
		detail.Items = append(detail.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domainmemory.UserMemoryTraceDetail{}, fmt.Errorf("%w: failed to iterate owner recall trace items: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if detail.Items == nil {
		detail.Items = []domainmemory.UserMemoryTraceItem{}
	}
	return detail, nil
}

func newOwnerReadReceipt(requestID, operation, auditReference string, inputCount, outputCount int, completedAt time.Time) domainmemory.UserMemoryOwnerReceipt {
	return domainmemory.UserMemoryOwnerReceipt{
		RequestID: requestID, Operation: operation, Status: "completed",
		OwnerRoute:     "conversation_l1/user_memory/" + operation,
		PolicyRevision: domainmemory.UserMemoryOwnerPolicyRevision,
		IdempotencyKey: requestID, Warnings: []string{}, InputCount: inputCount, OutputCount: outputCount,
		AuditReference: auditReference, CompletedAt: completedAt,
	}
}

func redactOwnerTraceSummary(summary, sensitivity, kind string) string {
	if strings.TrimSpace(sensitivity) != "" && !strings.EqualFold(strings.TrimSpace(sensitivity), "normal") {
		return ""
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "short_context" || kind == "rolling_summary" || kind == "full_transcript" || kind == "conversation_full_transcript" || kind == "transcript" {
		return ""
	}
	runes := []rune(summary)
	if len(runes) > 240 {
		runes = runes[:240]
	}
	return string(runes)
}

func ownerTraceTokenCount(summary string) int {
	if strings.TrimSpace(summary) == "" {
		return 0
	}
	return len([]rune(summary))/4 + 1
}

func sumTraceTokens(items []domconv.RecallTraceItemRecord) int {
	total := 0
	for _, item := range items {
		if item.Injected {
			total += item.TokenCount
		}
	}
	return total
}
