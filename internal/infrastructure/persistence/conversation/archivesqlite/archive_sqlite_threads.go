package archivesqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// SaveThreadSummary は要約とreceiptを同時に要求する新規保存経路である。
func (d *ArchiveSQLiteStore) SaveThreadSummary(ctx context.Context, summary *conversation.ThreadSummary) error {
	if err := validateThreadSummary(summary); err != nil {
		return err
	}
	if summary.Receipt == nil {
		return fmt.Errorf("thread summary receipt is required")
	}
	return d.SaveThreadSummaryWithReceipt(ctx, summary, summary.Receipt)
}

// SaveThreadSummaryWithReceipt atomically persists the summary and its
// provenance receipt. The receipt is never derived from model output.
func (d *ArchiveSQLiteStore) SaveThreadSummaryWithReceipt(ctx context.Context, summary *conversation.ThreadSummary, receipt *conversation.ThreadSummaryReceipt) error {
	if err := validateNewThreadSummary(summary, receipt); err != nil {
		return err
	}
	keywordsJSON, embeddingJSON, err := marshalThreadSummaryPayload(summary)
	if err != nil {
		return err
	}
	rolesJSON, err := json.Marshal(receipt.Roles)
	if err != nil {
		return fmt.Errorf("failed to marshal thread summary roles")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin thread summary transaction")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	storedSummary, summaryExists, err := loadStoredThreadSummary(ctx, tx, summary.ThreadID)
	if err != nil {
		return err
	}
	storedReceipt, receiptExists, err := loadStoredThreadSummaryReceipt(ctx, tx, summary.ThreadID)
	if err != nil {
		return err
	}
	if summaryExists != receiptExists {
		return fmt.Errorf("thread summary and receipt must exist together")
	}
	if summaryExists {
		if !storedThreadSummaryEqual(storedSummary, summary, keywordsJSON, embeddingJSON) || !storedThreadSummaryReceiptEqual(storedReceipt, receipt, string(rolesJSON)) {
			return fmt.Errorf("thread summary receipt is immutable")
		}
		return nil
	}

	if _, err := tx.ExecContext(ctx, threadSummaryInsertQuery(),
		summary.ThreadID,
		summary.SessionID,
		summary.StartTime,
		summary.EndTime,
		summary.Domain,
		summary.Summary,
		keywordsJSON,
		embeddingJSON,
		summary.IsNovel,
	); err != nil {
		return fmt.Errorf("failed to save thread summary to archive sqlite")
	}
	const receiptQuery = `
	INSERT INTO conversation_thread_summary_receipt
		(thread_id, schema_version, generation_mode, provider, failure_code,
		 evidence_sha256, source_turn_count, roles_json, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	if _, err := tx.ExecContext(ctx, receiptQuery,
		summary.ThreadID,
		receipt.SchemaVersion,
		receipt.GenerationMode,
		receipt.Provider,
		receipt.FailureCode,
		receipt.EvidenceSHA256,
		receipt.SourceTurnCount,
		string(rolesJSON),
		receipt.CreatedAt,
	); err != nil {
		return fmt.Errorf("failed to save thread summary receipt")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit thread summary transaction")
	}
	committed = true
	return nil
}

// GetSessionHistory はセッションの履歴を取得（最新limit件）。
func (d *ArchiveSQLiteStore) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]*conversation.ThreadSummary, error) {
	query := threadSummarySelectQuery(`WHERE st.session_id = ?`)
	rows, err := d.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query session history: %w", err)
	}
	defer rows.Close()
	return scanThreadSummaries(rows, limit)
}

// GetThreadSummary returns one exact archived thread, including its immutable
// summary receipt. It is intentionally not a session-history lookup: follower
// replay must bind to the requested thread ID and must not select a neighbor.
func (d *ArchiveSQLiteStore) GetThreadSummary(ctx context.Context, threadID int64) (*conversation.ThreadSummary, error) {
	if d == nil || d.db == nil || threadID <= 0 {
		return nil, conversation.ErrThreadNotFound
	}
	query := threadSummarySelectQuery(`WHERE st.thread_id = ?`)
	rows, err := d.db.QueryContext(ctx, query, threadID, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to query exact thread summary: %w", err)
	}
	defer rows.Close()
	summaries, err := scanThreadSummaries(rows, 1)
	if err != nil {
		return nil, err
	}
	if len(summaries) != 1 || summaries[0] == nil || summaries[0].ThreadID != threadID {
		return nil, conversation.ErrThreadNotFound
	}
	return summaries[0], nil
}

// SearchByDomain はドメインで Thread要約を検索する。
func (d *ArchiveSQLiteStore) SearchByDomain(ctx context.Context, domain string, limit int) ([]*conversation.ThreadSummary, error) {
	query := threadSummarySelectQuery(`WHERE st.domain = ?`)
	rows, err := d.db.QueryContext(ctx, query, domain, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query by domain: %w", err)
	}
	defer rows.Close()
	return scanThreadSummaries(rows, limit)
}

func threadSummaryInsertQuery() string {
	return `
	INSERT INTO session_thread (thread_id, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
}

func marshalThreadSummaryPayload(summary *conversation.ThreadSummary) (string, string, error) {
	keywords := summary.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	embedding := summary.Embedding
	if embedding == nil {
		embedding = []float32{}
	}
	keywordsJSON, err := json.Marshal(keywords)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal keywords")
	}
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal embedding")
	}
	return string(keywordsJSON), string(embeddingJSON), nil
}

type storedThreadSummary struct {
	SessionID string
	StartTime sql.NullTime
	EndTime   sql.NullTime
	Domain    string
	Summary   string
	Keywords  string
	Embedding string
	IsNovel   bool
}

func loadStoredThreadSummary(ctx context.Context, tx *sql.Tx, threadID int64) (*storedThreadSummary, bool, error) {
	var stored storedThreadSummary
	err := tx.QueryRowContext(ctx, `
	SELECT session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel
	FROM session_thread WHERE thread_id = ?
	`, threadID).Scan(
		&stored.SessionID,
		&stored.StartTime,
		&stored.EndTime,
		&stored.Domain,
		&stored.Summary,
		&stored.Keywords,
		&stored.Embedding,
		&stored.IsNovel,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to inspect existing thread summary")
	}
	return &stored, true, nil
}

type storedThreadSummaryReceipt struct {
	SchemaVersion   string
	GenerationMode  string
	Provider        string
	FailureCode     string
	EvidenceSHA256  string
	SourceTurnCount int64
	RolesJSON       string
	CreatedAt       sql.NullTime
}

func loadStoredThreadSummaryReceipt(ctx context.Context, tx *sql.Tx, threadID int64) (*storedThreadSummaryReceipt, bool, error) {
	var stored storedThreadSummaryReceipt
	err := tx.QueryRowContext(ctx, `
	SELECT schema_version, generation_mode, provider, failure_code,
	       evidence_sha256, source_turn_count, roles_json, created_at
	FROM conversation_thread_summary_receipt WHERE thread_id = ?
	`, threadID).Scan(
		&stored.SchemaVersion,
		&stored.GenerationMode,
		&stored.Provider,
		&stored.FailureCode,
		&stored.EvidenceSHA256,
		&stored.SourceTurnCount,
		&stored.RolesJSON,
		&stored.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to inspect existing thread summary receipt")
	}
	return &stored, true, nil
}

func storedThreadSummaryEqual(stored *storedThreadSummary, summary *conversation.ThreadSummary, keywordsJSON string, embeddingJSON string) bool {
	if stored == nil || summary == nil {
		return false
	}
	return stored.SessionID == summary.SessionID &&
		timeValueEqual(summary.StartTime, stored.StartTime) &&
		timeValueEqual(summary.EndTime, stored.EndTime) &&
		stored.Domain == summary.Domain &&
		stored.Summary == summary.Summary &&
		stored.Keywords == keywordsJSON &&
		stored.Embedding == embeddingJSON &&
		stored.IsNovel == summary.IsNovel
}

func storedThreadSummaryReceiptEqual(stored *storedThreadSummaryReceipt, receipt *conversation.ThreadSummaryReceipt, rolesJSON string) bool {
	if stored == nil || receipt == nil {
		return false
	}
	return stored.SchemaVersion == receipt.SchemaVersion &&
		stored.GenerationMode == receipt.GenerationMode &&
		stored.Provider == receipt.Provider &&
		stored.FailureCode == receipt.FailureCode &&
		stored.EvidenceSHA256 == receipt.EvidenceSHA256 &&
		stored.SourceTurnCount == int64(receipt.SourceTurnCount) &&
		stored.RolesJSON == rolesJSON &&
		timeValueEqual(receipt.CreatedAt, stored.CreatedAt)
}

func timeValueEqual(want time.Time, stored sql.NullTime) bool {
	if want.IsZero() {
		return !stored.Valid || stored.Time.IsZero()
	}
	return stored.Valid && stored.Time.Equal(want)
}

func validateNewThreadSummary(summary *conversation.ThreadSummary, receipt *conversation.ThreadSummaryReceipt) error {
	if err := validateThreadSummary(summary); err != nil {
		return err
	}
	if receipt == nil {
		return fmt.Errorf("thread summary receipt is required")
	}
	if err := receipt.ValidateForWrite(); err != nil {
		return err
	}
	if err := conversation.ValidateSummaryResidual(conversation.SummaryResidual{
		Summary:  summary.Summary,
		Keywords: summary.Keywords,
		Provider: receipt.Provider,
	}); err != nil {
		return fmt.Errorf("invalid thread summary residual: %w", err)
	}
	if len(summary.Roles) != len(receipt.Roles) {
		return fmt.Errorf("thread summary roles do not match receipt")
	}
	for i := range summary.Roles {
		if summary.Roles[i] != receipt.Roles[i] {
			return fmt.Errorf("thread summary roles do not match receipt")
		}
	}
	return nil
}

func validateThreadSummary(summary *conversation.ThreadSummary) error {
	if summary == nil {
		return fmt.Errorf("thread summary is required")
	}
	if summary.ThreadID <= 0 {
		return fmt.Errorf("thread summary thread_id must be > 0")
	}
	if strings.TrimSpace(summary.Summary) == "" {
		return fmt.Errorf("thread summary summary is required")
	}
	return nil
}

type threadSummaryScanner interface {
	Scan(dest ...any) error
}

func scanThreadSummaries(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}, limit int) ([]*conversation.ThreadSummary, error) {
	summaries := make([]*conversation.ThreadSummary, 0, limit)
	for rows.Next() {
		summary, err := scanThreadSummaryRow(rows)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return summaries, nil
}

func scanThreadSummaryRow(row threadSummaryScanner) (*conversation.ThreadSummary, error) {
	var summary conversation.ThreadSummary
	var keywordsJSON, embeddingJSON string
	var schemaVersion, generationMode, provider, failureCode, evidenceSHA256, rolesJSON sql.NullString
	var sourceTurnCount sql.NullInt64
	var createdAt sql.NullTime
	if err := row.Scan(
		&summary.ThreadID,
		&summary.SessionID,
		&summary.StartTime,
		&summary.EndTime,
		&summary.Domain,
		&summary.Summary,
		&keywordsJSON,
		&embeddingJSON,
		&summary.IsNovel,
		&schemaVersion,
		&generationMode,
		&provider,
		&failureCode,
		&evidenceSHA256,
		&sourceTurnCount,
		&rolesJSON,
		&createdAt,
	); err != nil {
		return nil, fmt.Errorf("failed to scan row: %w", err)
	}
	if err := json.Unmarshal([]byte(keywordsJSON), &summary.Keywords); err != nil {
		return nil, fmt.Errorf("failed to unmarshal keywords: %w", err)
	}
	if err := json.Unmarshal([]byte(embeddingJSON), &summary.Embedding); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embedding: %w", err)
	}
	if schemaVersion.Valid || generationMode.Valid || provider.Valid || failureCode.Valid || evidenceSHA256.Valid || sourceTurnCount.Valid || rolesJSON.Valid || createdAt.Valid {
		if !schemaVersion.Valid || !generationMode.Valid || !provider.Valid || !failureCode.Valid || !evidenceSHA256.Valid || !sourceTurnCount.Valid || !rolesJSON.Valid || !createdAt.Valid {
			return nil, fmt.Errorf("incomplete thread summary receipt")
		}
		var roles []string
		if err := json.Unmarshal([]byte(rolesJSON.String), &roles); err != nil {
			return nil, fmt.Errorf("failed to unmarshal thread summary roles")
		}
		receipt := &conversation.ThreadSummaryReceipt{
			SchemaVersion:   schemaVersion.String,
			GenerationMode:  generationMode.String,
			Provider:        provider.String,
			FailureCode:     failureCode.String,
			EvidenceSHA256:  evidenceSHA256.String,
			SourceTurnCount: int(sourceTurnCount.Int64),
			Roles:           roles,
		}
		receipt.CreatedAt = createdAt.Time
		if receipt.GenerationMode == conversation.ThreadSummaryGenerationLegacyUnverified {
			return nil, fmt.Errorf("stored legacy thread summary receipt is invalid")
		}
		if err := receipt.ValidateForWrite(); err != nil {
			return nil, err
		}
		if err := conversation.ValidateSummaryResidual(conversation.SummaryResidual{
			Summary:  summary.Summary,
			Keywords: summary.Keywords,
			Provider: receipt.Provider,
		}); err != nil {
			return nil, fmt.Errorf("invalid stored thread summary residual: %w", err)
		}
		summary.Receipt = receipt
		summary.Roles = append([]string(nil), roles...)
	} else {
		summary.Receipt = &conversation.ThreadSummaryReceipt{GenerationMode: conversation.ThreadSummaryGenerationLegacyUnverified}
	}
	if err := validateThreadSummary(&summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

func threadSummarySelectQuery(where string) string {
	return `
	SELECT st.thread_id, st.session_id, st.ts_start, st.ts_end, st.domain, st.summary,
	       st.keywords, st.embedding, st.is_novel,
	       r.schema_version, r.generation_mode, r.provider, r.failure_code,
	       r.evidence_sha256, r.source_turn_count, r.roles_json, r.created_at
	FROM session_thread st
	LEFT JOIN conversation_thread_summary_receipt r ON r.thread_id = st.thread_id
	` + where + `
	ORDER BY st.ts_start DESC, st.rowid DESC
	LIMIT ?
	`
}
