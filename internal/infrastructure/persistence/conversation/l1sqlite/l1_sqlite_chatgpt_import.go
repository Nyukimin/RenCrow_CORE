package l1sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const ChatGPTL3ArtifactFormat = "rencrow.chatgpt_l3.v1"

type ChatGPTL3ImportRecord struct {
	Format                string
	ExportID              string
	EvidenceID            string
	ConversationID        string
	ConversationTitle     string
	ConversationCreatedAt time.Time
	ConversationUpdatedAt time.Time
	NodeID                string
	ParentNodeID          string
	ChildNodeIDs          []string
	OnCurrentBranch       bool
	MessageID             string
	MessageCreatedAt      time.Time
	Role                  string
	ContentType           string
	Text                  string
	Content               json.RawMessage
	Metadata              json.RawMessage
}

type ChatGPTL3ImportResult struct {
	Validated int `json:"validated"`
	Imported  int `json:"imported"`
	Existing  int `json:"existing"`
	Queued    int `json:"queued_for_projection"`
}

type ChatGPTL3ConfirmResult struct {
	Matched             int `json:"matched"`
	Confirmed           int `json:"confirmed"`
	ProjectionPending   int `json:"projection_pending"`
	ProjectionRunning   int `json:"projection_running"`
	ProjectionRetryWait int `json:"projection_retry_wait"`
	ProjectionFailed    int `json:"projection_failed"`
	ProjectionCompleted int `json:"projection_completed"`
}

func ValidateChatGPTL3ImportRecord(item ChatGPTL3ImportRecord) error {
	if item.Format != ChatGPTL3ArtifactFormat {
		return fmt.Errorf("unsupported ChatGPT L3 artifact format: %q", item.Format)
	}
	if strings.TrimSpace(item.ExportID) == "" || strings.TrimSpace(item.ConversationID) == "" || strings.TrimSpace(item.MessageID) == "" {
		return errors.New("export_id, conversation_id, and message_id are required")
	}
	wantEvidence := "chatgpt_export:" + item.ConversationID + ":" + item.MessageID
	if item.EvidenceID != wantEvidence {
		return fmt.Errorf("evidence_id mismatch: got %q want %q", item.EvidenceID, wantEvidence)
	}
	switch item.Role {
	case "user", "assistant", "system", "tool":
	default:
		return fmt.Errorf("unsupported ChatGPT message role: %q", item.Role)
	}
	if strings.TrimSpace(item.Text) == "" && len(item.Content) == 0 {
		return errors.New("message text or content is required")
	}
	return nil
}

func (s *L1SQLiteStore) ImportChatGPTL3Records(ctx context.Context, records []ChatGPTL3ImportRecord, apply bool) (ChatGPTL3ImportResult, error) {
	result := ChatGPTL3ImportResult{Validated: len(records)}
	if len(records) == 0 {
		return result, errors.New("ChatGPT L3 import batch is empty")
	}
	if len(records) > 100 {
		return result, errors.New("ChatGPT L3 import batch exceeds 100 records")
	}
	for _, item := range records {
		if err := ValidateChatGPTL3ImportRecord(item); err != nil {
			return result, err
		}
	}
	if !apply {
		return result, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	for _, item := range records {
		namespace := chatGPTConversationNamespace(item.ConversationID)
		sessionID := strings.TrimPrefix(namespace, "conv:")
		threadID := chatGPTConversationThreadID(item.ConversationID)
		createdAt := item.MessageCreatedAt.UTC()
		if createdAt.IsZero() {
			createdAt = firstNonZeroTime(item.ConversationCreatedAt.UTC(), time.Now().UTC())
		}
		now := time.Now().UTC()
		message := strings.TrimSpace(item.Text)
		if message == "" {
			message = "[non-text ChatGPT content: " + firstNonEmptyString(item.ContentType, "unknown") + "]"
		}
		contentHash := chatGPTRecordContentHash(item)
		meta := map[string]interface{}{
			"external_source": "chatgpt_export", "export_id": item.ExportID,
			"conversation_id": item.ConversationID, "conversation_title": item.ConversationTitle,
			"node_id": item.NodeID, "parent_node_id": item.ParentNodeID,
			"child_node_ids": item.ChildNodeIDs, "on_current_branch": item.OnCurrentBranch,
			"message_id": item.MessageID, "original_role": item.Role,
			"content_type": item.ContentType, "content_sha256": contentHash,
			"artifact_content": json.RawMessage(item.Content), "artifact_metadata": json.RawMessage(item.Metadata),
		}
		metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal ChatGPT L3 metadata")
		if err != nil {
			return result, rollbackL1Tx(tx, err)
		}
		speaker := domconv.SpeakerSystem
		if item.Role == "user" {
			speaker = domconv.SpeakerUser
		} else if item.Role == "tool" {
			speaker = domconv.SpeakerTool
		}
		inserted, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.EvidenceID, namespace, sessionID, threadID, string(speaker), message, metaJSON,
			MemoryStateObserved, "L3", "chatgpt_export", createdAt, now)
		if err != nil {
			return result, rollbackL1Tx(tx, fmt.Errorf("insert ChatGPT L3 record: %w", err))
		}
		affected, _ := inserted.RowsAffected()
		if affected == 0 {
			var existingMeta string
			if err := tx.QueryRowContext(ctx, `SELECT meta_json FROM l1_memory_event WHERE id = ?`, item.EvidenceID).Scan(&existingMeta); err != nil {
				return result, rollbackL1Tx(tx, err)
			}
			var existing map[string]interface{}
			if json.Unmarshal([]byte(existingMeta), &existing) != nil || strings.TrimSpace(fmt.Sprint(existing["content_sha256"])) != contentHash {
				return result, rollbackL1Tx(tx, fmt.Errorf("ChatGPT L3 evidence conflict: %s", item.EvidenceID))
			}
			result.Existing++
			continue
		}
		result.Imported++
		if item.Role == "user" && item.OnCurrentBranch {
			queued, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_profile_promotion_job (
	evidence_event_id, session_id, thread_id, state, attempt_count,
	lease_token, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, 0, '', '', ?, ?)
`, item.EvidenceID, sessionID, threadID, domainmemory.ProfilePromotionPending, createdAt, now)
			if err != nil {
				return result, rollbackL1Tx(tx, fmt.Errorf("queue ChatGPT L3 projection: %w", err))
			}
			queuedRows, _ := queued.RowsAffected()
			result.Queued += int(queuedRows)
		}
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.chatgpt_l3_imported", "user:ren", "", 0, map[string]interface{}{
		"validated": result.Validated, "imported": result.Imported,
		"existing": result.Existing, "queued_for_projection": result.Queued,
	}, "chatgpt_export_importer"); err != nil {
		return result, rollbackL1Tx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return result, rollbackL1Tx(tx, err)
	}
	return result, nil
}

func (s *L1SQLiteStore) ConfirmChatGPTL3Candidates(ctx context.Context, exportID string, reason string, apply bool) (ChatGPTL3ConfirmResult, error) {
	exportID = strings.TrimSpace(exportID)
	reason = strings.TrimSpace(reason)
	if exportID == "" {
		return ChatGPTL3ConfirmResult{}, errors.New("export_id is required")
	}
	if reason == "" {
		return ChatGPTL3ConfirmResult{}, errors.New("explicit confirmation reason is required")
	}
	result, err := s.chatGPTProjectionStatus(ctx, exportID)
	if err != nil {
		return ChatGPTL3ConfirmResult{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, meta_json FROM l1_memory_event
WHERE namespace = 'user:ren' AND memory_state = ? AND source = 'profile_extractor'
ORDER BY created_at ASC, id ASC
`, MemoryStateCandidate)
	if err != nil {
		return ChatGPTL3ConfirmResult{}, err
	}
	var matched []string
	for rows.Next() {
		var id, metaJSON string
		if err := rows.Scan(&id, &metaJSON); err != nil {
			rows.Close()
			return ChatGPTL3ConfirmResult{}, err
		}
		var meta struct {
			EvidenceEventIDs []string `json:"evidence_event_ids"`
			Sensitivity      string   `json:"sensitivity"`
		}
		if json.Unmarshal([]byte(metaJSON), &meta) != nil || meta.Sensitivity == "sensitive" || len(meta.EvidenceEventIDs) == 0 {
			continue
		}
		allMatch := true
		for _, evidenceID := range meta.EvidenceEventIDs {
			var evidenceMetaJSON string
			if err := s.db.QueryRowContext(ctx, `SELECT meta_json FROM l1_memory_event WHERE id = ? AND source = 'chatgpt_export'`, evidenceID).Scan(&evidenceMetaJSON); err != nil {
				allMatch = false
				break
			}
			var evidenceMeta map[string]interface{}
			if json.Unmarshal([]byte(evidenceMetaJSON), &evidenceMeta) != nil || strings.TrimSpace(fmt.Sprint(evidenceMeta["export_id"])) != exportID {
				allMatch = false
				break
			}
		}
		if allMatch {
			matched = append(matched, id)
		}
	}
	if err := rows.Close(); err != nil {
		return ChatGPTL3ConfirmResult{}, err
	}
	if err := rows.Err(); err != nil {
		return ChatGPTL3ConfirmResult{}, err
	}
	result.Matched = len(matched)
	if !apply {
		return result, nil
	}
	if result.ProjectionPending > 0 || result.ProjectionRunning > 0 || result.ProjectionRetryWait > 0 {
		return result, errors.New("ChatGPT L3 projection is not complete")
	}
	if result.ProjectionFailed > 0 {
		return result, errors.New("ChatGPT L3 projection contains failed jobs")
	}
	if len(matched) == 0 {
		return result, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC()
	for _, id := range matched {
		updated, err := tx.ExecContext(ctx, `UPDATE l1_memory_event SET memory_state = ?, updated_at = ? WHERE id = ? AND memory_state = ?`, MemoryStateConfirmed, now, id, MemoryStateCandidate)
		if err != nil {
			return result, rollbackL1Tx(tx, err)
		}
		affected, _ := updated.RowsAffected()
		result.Confirmed += int(affected)
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.chatgpt_l3_candidates_confirmed", "user:ren", "", 0, map[string]interface{}{
		"export_id": exportID, "reason": reason, "confirmed": result.Confirmed,
	}, "chatgpt_export_importer"); err != nil {
		return result, rollbackL1Tx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return result, rollbackL1Tx(tx, err)
	}
	return result, nil
}

func (s *L1SQLiteStore) chatGPTProjectionStatus(ctx context.Context, exportID string) (ChatGPTL3ConfirmResult, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT j.state, e.meta_json
FROM l1_profile_promotion_job j
JOIN l1_memory_event e ON e.id = j.evidence_event_id
WHERE e.source = 'chatgpt_export'
`)
	if err != nil {
		return ChatGPTL3ConfirmResult{}, err
	}
	defer rows.Close()
	var result ChatGPTL3ConfirmResult
	for rows.Next() {
		var state, metaJSON string
		if err := rows.Scan(&state, &metaJSON); err != nil {
			return result, err
		}
		var meta map[string]interface{}
		if json.Unmarshal([]byte(metaJSON), &meta) != nil || strings.TrimSpace(fmt.Sprint(meta["export_id"])) != exportID {
			continue
		}
		switch state {
		case domainmemory.ProfilePromotionPending:
			result.ProjectionPending++
		case domainmemory.ProfilePromotionRunning:
			result.ProjectionRunning++
		case domainmemory.ProfilePromotionRetryWait:
			result.ProjectionRetryWait++
		case domainmemory.ProfilePromotionFailed:
			result.ProjectionFailed++
		case domainmemory.ProfilePromotionCompleted:
			result.ProjectionCompleted++
		}
	}
	return result, rows.Err()
}

func chatGPTConversationNamespace(conversationID string) string {
	digest := sha256.Sum256([]byte(conversationID))
	return "conv:chatgpt-" + hex.EncodeToString(digest[:8])
}

func chatGPTConversationThreadID(conversationID string) int64 {
	digest := sha256.Sum256([]byte(conversationID))
	value := int64(binary.BigEndian.Uint64(digest[:8]) & 0x7fffffffffffffff)
	if value == 0 {
		return 1
	}
	return value
}

func chatGPTRecordContentHash(item ChatGPTL3ImportRecord) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", item.Role, item.Text, item.Content)
	return hex.EncodeToString(h.Sum(nil))
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Now().UTC()
}
