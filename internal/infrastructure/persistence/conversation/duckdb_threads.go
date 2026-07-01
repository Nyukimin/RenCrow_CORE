//go:build linux && amd64

package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	_ "github.com/marcboeker/go-duckdb"
)

// SaveThreadSummary はThread要約をDuckDBに保存
func (d *DuckDBStore) SaveThreadSummary(ctx context.Context, summary *conversation.ThreadSummary) error {
	if summary == nil {
		return fmt.Errorf("thread summary is required")
	}
	if err := validateThreadSummary(summary); err != nil {
		return err
	}
	keywords := summary.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	embedding := summary.Embedding
	if embedding == nil {
		embedding = []float32{}
	}
	// keywords と embedding を JSON 化（DuckDB の配列型として保存）
	keywordsJSON, err := json.Marshal(keywords)
	if err != nil {
		return fmt.Errorf("failed to marshal keywords: %w", err)
	}

	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("failed to marshal embedding: %w", err)
	}

	query := `
	INSERT INTO session_thread (thread_id, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel)
	VALUES (?, ?, ?, ?, ?, ?, ?::VARCHAR[], ?::FLOAT[], ?)
	ON CONFLICT (thread_id) DO UPDATE SET
		summary = excluded.summary,
		keywords = excluded.keywords,
		embedding = excluded.embedding,
		is_novel = excluded.is_novel
	`

	_, err = d.db.ExecContext(ctx, query,
		summary.ThreadID,
		summary.SessionID,
		summary.StartTime,
		summary.EndTime,
		summary.Domain,
		summary.Summary,
		string(keywordsJSON),
		string(embeddingJSON),
		summary.IsNovel,
	)
	if err != nil {
		return fmt.Errorf("failed to save thread summary to duckdb: %w", err)
	}

	return nil
}

// GetSessionHistory はセッションの履歴を取得（最新limit件）
func (d *DuckDBStore) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]*conversation.ThreadSummary, error) {
	query := `
	SELECT thread_id, session_id, ts_start, ts_end, domain, summary,
	       CAST(to_json(keywords) AS VARCHAR), CAST(to_json(embedding) AS VARCHAR), is_novel
	FROM session_thread
	WHERE session_id = ?
	ORDER BY ts_start DESC
	LIMIT ?
	`

	rows, err := d.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query session history: %w", err)
	}
	defer rows.Close()

	summaries := make([]*conversation.ThreadSummary, 0, limit)
	for rows.Next() {
		var summary conversation.ThreadSummary
		var keywordsJSON, embeddingJSON string

		if err := rows.Scan(
			&summary.ThreadID,
			&summary.SessionID,
			&summary.StartTime,
			&summary.EndTime,
			&summary.Domain,
			&summary.Summary,
			&keywordsJSON,
			&embeddingJSON,
			&summary.IsNovel,
		); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// JSON → []string, []float32
		if err := json.Unmarshal([]byte(keywordsJSON), &summary.Keywords); err != nil {
			return nil, fmt.Errorf("failed to unmarshal keywords: %w", err)
		}
		if err := json.Unmarshal([]byte(embeddingJSON), &summary.Embedding); err != nil {
			return nil, fmt.Errorf("failed to unmarshal embedding: %w", err)
		}
		if err := validateThreadSummary(&summary); err != nil {
			return nil, err
		}

		summaries = append(summaries, &summary)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return summaries, nil
}

// SearchByDomain はドメインで Thread要約を検索
func (d *DuckDBStore) SearchByDomain(ctx context.Context, domain string, limit int) ([]*conversation.ThreadSummary, error) {
	query := `
	SELECT thread_id, session_id, ts_start, ts_end, domain, summary,
	       CAST(to_json(keywords) AS VARCHAR), CAST(to_json(embedding) AS VARCHAR), is_novel
	FROM session_thread
	WHERE domain = ?
	ORDER BY ts_start DESC
	LIMIT ?
	`

	rows, err := d.db.QueryContext(ctx, query, domain, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query by domain: %w", err)
	}
	defer rows.Close()

	summaries := make([]*conversation.ThreadSummary, 0, limit)
	for rows.Next() {
		var summary conversation.ThreadSummary
		var keywordsJSON, embeddingJSON string

		if err := rows.Scan(
			&summary.ThreadID,
			&summary.SessionID,
			&summary.StartTime,
			&summary.EndTime,
			&summary.Domain,
			&summary.Summary,
			&keywordsJSON,
			&embeddingJSON,
			&summary.IsNovel,
		); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// JSON → []string, []float32
		if err := json.Unmarshal([]byte(keywordsJSON), &summary.Keywords); err != nil {
			return nil, fmt.Errorf("failed to unmarshal keywords: %w", err)
		}
		if err := json.Unmarshal([]byte(embeddingJSON), &summary.Embedding); err != nil {
			return nil, fmt.Errorf("failed to unmarshal embedding: %w", err)
		}
		if err := validateThreadSummary(&summary); err != nil {
			return nil, err
		}

		summaries = append(summaries, &summary)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return summaries, nil
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
