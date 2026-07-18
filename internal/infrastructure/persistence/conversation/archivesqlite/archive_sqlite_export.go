package archivesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
)

// threadSummaryParquetRow は session_thread テーブルの Parquet エクスポート行。
// ts_end / domain / is_novel はスキーマ上 NOT NULL 制約が無いため optional として扱う。
type threadSummaryParquetRow struct {
	ThreadID  int64      `parquet:"thread_id"`
	SessionID string     `parquet:"session_id"`
	TsStart   time.Time  `parquet:"ts_start"`
	TsEnd     *time.Time `parquet:"ts_end,optional"`
	Domain    *string    `parquet:"domain,optional"`
	Summary   string     `parquet:"summary"`
	Keywords  string     `parquet:"keywords"`
	Embedding string     `parquet:"embedding"`
	IsNovel   *bool      `parquet:"is_novel,optional"`
	CreatedAt time.Time  `parquet:"created_at"`
}

func (d *ArchiveSQLiteStore) ExportThreadSummariesParquet(ctx context.Context, outputPath string) error {
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return fmt.Errorf("parquet output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("failed to create parquet output directory: %w", err)
	}

	rows, err := d.db.QueryContext(ctx, `
	SELECT thread_id, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel, created_at
	FROM session_thread
	ORDER BY ts_start ASC, thread_id ASC
	`)
	if err != nil {
		return fmt.Errorf("failed to query thread summaries for parquet export: %w", err)
	}
	defer rows.Close()

	records := make([]threadSummaryParquetRow, 0)
	for rows.Next() {
		var rec threadSummaryParquetRow
		var tsEnd sql.NullTime
		var domain sql.NullString
		var isNovel sql.NullBool

		if err := rows.Scan(
			&rec.ThreadID,
			&rec.SessionID,
			&rec.TsStart,
			&tsEnd,
			&domain,
			&rec.Summary,
			&rec.Keywords,
			&rec.Embedding,
			&isNovel,
			&rec.CreatedAt,
		); err != nil {
			return fmt.Errorf("failed to scan thread summary row for parquet export: %w", err)
		}
		if tsEnd.Valid {
			t := tsEnd.Time
			rec.TsEnd = &t
		}
		if domain.Valid {
			v := domain.String
			rec.Domain = &v
		}
		if isNovel.Valid {
			v := isNovel.Bool
			rec.IsNovel = &v
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error while exporting thread summaries: %w", err)
	}

	if err := parquet.WriteFile(outputPath, records); err != nil {
		return fmt.Errorf("failed to write thread summaries parquet file: %w", err)
	}
	return nil
}

// l1MemoryEventParquetRow は l1_memory_event_archive の Parquet エクスポート行（全カラム NOT NULL）。
type l1MemoryEventParquetRow struct {
	ID          string    `parquet:"id"`
	Namespace   string    `parquet:"namespace"`
	SessionID   string    `parquet:"session_id"`
	ThreadID    int64     `parquet:"thread_id"`
	Speaker     string    `parquet:"speaker"`
	Message     string    `parquet:"message"`
	MetaJSON    string    `parquet:"meta_json"`
	MemoryState string    `parquet:"memory_state"`
	Layer       string    `parquet:"layer"`
	Source      string    `parquet:"source"`
	CreatedAt   time.Time `parquet:"created_at"`
	UpdatedAt   time.Time `parquet:"updated_at"`
}

// l1NewsItemParquetRow は l1_news_item_archive の Parquet エクスポート行。published_at のみ NULL 許容。
type l1NewsItemParquetRow struct {
	ID           string     `parquet:"id"`
	StagingID    string     `parquet:"staging_id"`
	Category     string     `parquet:"category"`
	SourceID     string     `parquet:"source_id"`
	SourceURL    string     `parquet:"source_url"`
	PublishedAt  *time.Time `parquet:"published_at,optional"`
	FetchedAt    time.Time  `parquet:"fetched_at"`
	RawText      string     `parquet:"raw_text"`
	RawHash      string     `parquet:"raw_hash"`
	SummaryDraft string     `parquet:"summary_draft"`
	KeywordsJSON string     `parquet:"keywords_json"`
	LicenseNote  string     `parquet:"license_note"`
	MetaJSON     string     `parquet:"meta_json"`
	CreatedAt    time.Time  `parquet:"created_at"`
	UpdatedAt    time.Time  `parquet:"updated_at"`
}

// l1KnowledgeItemParquetRow は l1_knowledge_item_archive の Parquet エクスポート行（全カラム NOT NULL）。
type l1KnowledgeItemParquetRow struct {
	ID           string    `parquet:"id"`
	StagingID    string    `parquet:"staging_id"`
	Domain       string    `parquet:"domain"`
	Title        string    `parquet:"title"`
	SourceID     string    `parquet:"source_id"`
	SourceURL    string    `parquet:"source_url"`
	RawText      string    `parquet:"raw_text"`
	RawHash      string    `parquet:"raw_hash"`
	SummaryDraft string    `parquet:"summary_draft"`
	KeywordsJSON string    `parquet:"keywords_json"`
	LicenseNote  string    `parquet:"license_note"`
	MetaJSON     string    `parquet:"meta_json"`
	CreatedAt    time.Time `parquet:"created_at"`
	UpdatedAt    time.Time `parquet:"updated_at"`
}

// l1StagingItemParquetRow は l1_staging_item_archive の Parquet エクスポート行。published_at のみ NULL 許容。
type l1StagingItemParquetRow struct {
	ID               string     `parquet:"id"`
	Kind             string     `parquet:"kind"`
	Namespace        string     `parquet:"namespace"`
	EventID          string     `parquet:"event_id"`
	SourceID         string     `parquet:"source_id"`
	SourceURL        string     `parquet:"source_url"`
	FetchedAt        time.Time  `parquet:"fetched_at"`
	PublishedAt      *time.Time `parquet:"published_at,optional"`
	RawText          string     `parquet:"raw_text"`
	RawHash          string     `parquet:"raw_hash"`
	SummaryDraft     string     `parquet:"summary_draft"`
	KeywordsJSON     string     `parquet:"keywords_json"`
	LicenseNote      string     `parquet:"license_note"`
	ValidationStatus string     `parquet:"validation_status"`
	MetaJSON         string     `parquet:"meta_json"`
	CreatedAt        time.Time  `parquet:"created_at"`
	UpdatedAt        time.Time  `parquet:"updated_at"`
}

func (d *ArchiveSQLiteStore) ExportL1ArchivesParquet(ctx context.Context, outputDir string) (map[string]string, error) {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return nil, fmt.Errorf("parquet output directory is required")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create l1 archive output directory: %w", err)
	}

	paths := make(map[string]string, 4)

	memoryPath := filepath.Join(outputDir, "l1_memory_event.parquet")
	if err := d.exportMemoryEventArchiveParquet(ctx, memoryPath); err != nil {
		return nil, fmt.Errorf("failed to export %s archive parquet: %w", L1ArchiveMemory, err)
	}
	paths[L1ArchiveMemory] = memoryPath

	newsPath := filepath.Join(outputDir, "l1_news_item.parquet")
	if err := d.exportNewsItemArchiveParquet(ctx, newsPath); err != nil {
		return nil, fmt.Errorf("failed to export %s archive parquet: %w", L1ArchiveNews, err)
	}
	paths[L1ArchiveNews] = newsPath

	knowledgePath := filepath.Join(outputDir, "l1_knowledge_item.parquet")
	if err := d.exportKnowledgeItemArchiveParquet(ctx, knowledgePath); err != nil {
		return nil, fmt.Errorf("failed to export %s archive parquet: %w", L1ArchiveKnowledge, err)
	}
	paths[L1ArchiveKnowledge] = knowledgePath

	stagingPath := filepath.Join(outputDir, "l1_staging_item.parquet")
	if err := d.exportStagingItemArchiveParquet(ctx, stagingPath); err != nil {
		return nil, fmt.Errorf("failed to export %s archive parquet: %w", L1ArchiveStaging, err)
	}
	paths[L1ArchiveStaging] = stagingPath

	return paths, nil
}

func (d *ArchiveSQLiteStore) exportMemoryEventArchiveParquet(ctx context.Context, outputPath string) error {
	rows, err := d.db.QueryContext(ctx, `
	SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
	       memory_state, layer, source, created_at, updated_at
	FROM l1_memory_event_archive
	ORDER BY created_at ASC, id ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	records := make([]l1MemoryEventParquetRow, 0)
	for rows.Next() {
		var rec l1MemoryEventParquetRow
		if err := rows.Scan(
			&rec.ID, &rec.Namespace, &rec.SessionID, &rec.ThreadID, &rec.Speaker, &rec.Message,
			&rec.MetaJSON, &rec.MemoryState, &rec.Layer, &rec.Source, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return parquet.WriteFile(outputPath, records)
}

func (d *ArchiveSQLiteStore) exportNewsItemArchiveParquet(ctx context.Context, outputPath string) error {
	rows, err := d.db.QueryContext(ctx, `
	SELECT id, staging_id, category, source_id, source_url, published_at, fetched_at,
	       raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
	       created_at, updated_at
	FROM l1_news_item_archive
	ORDER BY COALESCE(published_at, fetched_at) ASC, id ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	records := make([]l1NewsItemParquetRow, 0)
	for rows.Next() {
		var rec l1NewsItemParquetRow
		var publishedAt sql.NullTime
		if err := rows.Scan(
			&rec.ID, &rec.StagingID, &rec.Category, &rec.SourceID, &rec.SourceURL,
			&publishedAt, &rec.FetchedAt, &rec.RawText, &rec.RawHash, &rec.SummaryDraft,
			&rec.KeywordsJSON, &rec.LicenseNote, &rec.MetaJSON, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return err
		}
		if publishedAt.Valid {
			t := publishedAt.Time
			rec.PublishedAt = &t
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return parquet.WriteFile(outputPath, records)
}

func (d *ArchiveSQLiteStore) exportKnowledgeItemArchiveParquet(ctx context.Context, outputPath string) error {
	rows, err := d.db.QueryContext(ctx, `
	SELECT id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash,
	       summary_draft, keywords_json, license_note, meta_json, created_at, updated_at
	FROM l1_knowledge_item_archive
	ORDER BY updated_at ASC, id ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	records := make([]l1KnowledgeItemParquetRow, 0)
	for rows.Next() {
		var rec l1KnowledgeItemParquetRow
		if err := rows.Scan(
			&rec.ID, &rec.StagingID, &rec.Domain, &rec.Title, &rec.SourceID, &rec.SourceURL,
			&rec.RawText, &rec.RawHash, &rec.SummaryDraft, &rec.KeywordsJSON, &rec.LicenseNote,
			&rec.MetaJSON, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return parquet.WriteFile(outputPath, records)
}

func (d *ArchiveSQLiteStore) exportStagingItemArchiveParquet(ctx context.Context, outputPath string) error {
	rows, err := d.db.QueryContext(ctx, `
	SELECT id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
	       raw_text, raw_hash, summary_draft, keywords_json, license_note,
	       validation_status, meta_json, created_at, updated_at
	FROM l1_staging_item_archive
	ORDER BY created_at ASC, id ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	records := make([]l1StagingItemParquetRow, 0)
	for rows.Next() {
		var rec l1StagingItemParquetRow
		var publishedAt sql.NullTime
		if err := rows.Scan(
			&rec.ID, &rec.Kind, &rec.Namespace, &rec.EventID, &rec.SourceID, &rec.SourceURL,
			&rec.FetchedAt, &publishedAt, &rec.RawText, &rec.RawHash, &rec.SummaryDraft,
			&rec.KeywordsJSON, &rec.LicenseNote, &rec.ValidationStatus, &rec.MetaJSON,
			&rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return err
		}
		if publishedAt.Valid {
			t := publishedAt.Time
			rec.PublishedAt = &t
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return parquet.WriteFile(outputPath, records)
}

// CleanupOldRecords は7日以上経過したレコードを削除
func (d *ArchiveSQLiteStore) CleanupOldRecords(ctx context.Context) (int64, error) {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)

	query := `DELETE FROM session_thread WHERE ts_start < ?`

	result, err := d.db.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old records: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return rowsAffected, nil
}
