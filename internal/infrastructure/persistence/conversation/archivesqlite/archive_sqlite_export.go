package archivesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/parquet-go/parquet-go"
)

// threadSummaryParquetRow は session_thread テーブルの Parquet エクスポート行。
// ts_end / domain / is_novel はスキーマ上 NOT NULL 制約が無いため optional として扱う。
type threadSummaryParquetRow struct {
	ThreadID         string     `parquet:"thread_id"`
	ThreadSeq        int64      `parquet:"thread_seq"`
	ThreadKind       string     `parquet:"thread_kind"`
	SessionID        string     `parquet:"session_id"`
	TsStart          time.Time  `parquet:"ts_start"`
	TsEnd            *time.Time `parquet:"ts_end,optional"`
	Domain           *string    `parquet:"domain,optional"`
	Summary          string     `parquet:"summary"`
	Keywords         string     `parquet:"keywords"`
	Embedding        string     `parquet:"embedding"`
	IsNovel          *bool      `parquet:"is_novel,optional"`
	CreatedAt        time.Time  `parquet:"created_at"`
	SchemaVersion    *string    `parquet:"schema_version,optional"`
	GenerationMode   *string    `parquet:"generation_mode,optional"`
	Provider         *string    `parquet:"provider,optional"`
	FailureCode      *string    `parquet:"failure_code,optional"`
	EvidenceSHA256   *string    `parquet:"evidence_sha256,optional"`
	SourceTurnCount  *int64     `parquet:"source_turn_count,optional"`
	RolesJSON        *string    `parquet:"roles_json,optional"`
	ReceiptCreatedAt *time.Time `parquet:"receipt_created_at,optional"`
}

func (d *ArchiveSQLiteStore) ExportThreadSummariesParquet(ctx context.Context, outputPath string) error {
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return fmt.Errorf("parquet output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("failed to create parquet output directory: %w", err)
	}
	snapshot, err := d.readArchiveParquetSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("failed to read archive snapshot for parquet export: %w", err)
	}
	if err := parquet.WriteFile(outputPath, snapshot.Threads); err != nil {
		return fmt.Errorf("failed to write thread summaries parquet file: %w", err)
	}
	if err := os.Chmod(outputPath, 0o600); err != nil {
		return fmt.Errorf("failed to set thread summaries parquet permissions: %w", err)
	}
	return nil
}

// l1MemoryEventParquetRow は l1_memory_event_archive の Parquet エクスポート行（全カラム NOT NULL）。
type l1MemoryEventParquetRow struct {
	ID          string    `parquet:"id"`
	Namespace   string    `parquet:"namespace"`
	SessionID   string    `parquet:"session_id"`
	ThreadID    string    `parquet:"thread_id"`
	ThreadSeq   int64     `parquet:"thread_seq"`
	ThreadKind  string    `parquet:"thread_kind"`
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

	snapshot, err := d.readArchiveParquetSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read archive snapshot for l1 parquet export: %w", err)
	}
	paths := make(map[string]string, 4)

	memoryPath := filepath.Join(outputDir, "l1_memory_event.parquet")
	if _, err := writeArchiveParquetFile(memoryPath, "l1_memory_event.parquet", snapshot.Memory); err != nil {
		return nil, fmt.Errorf("failed to export %s archive parquet: %w", L1ArchiveMemory, err)
	}
	paths[L1ArchiveMemory] = memoryPath

	newsPath := filepath.Join(outputDir, "l1_news_item.parquet")
	if _, err := writeArchiveParquetFile(newsPath, "l1_news_item.parquet", snapshot.News); err != nil {
		return nil, fmt.Errorf("failed to export %s archive parquet: %w", L1ArchiveNews, err)
	}
	paths[L1ArchiveNews] = newsPath

	knowledgePath := filepath.Join(outputDir, "l1_knowledge_item.parquet")
	if _, err := writeArchiveParquetFile(knowledgePath, "l1_knowledge_item.parquet", snapshot.Knowledge); err != nil {
		return nil, fmt.Errorf("failed to export %s archive parquet: %w", L1ArchiveKnowledge, err)
	}
	paths[L1ArchiveKnowledge] = knowledgePath

	stagingPath := filepath.Join(outputDir, "l1_staging_item.parquet")
	if _, err := writeArchiveParquetFile(stagingPath, "l1_staging_item.parquet", snapshot.Staging); err != nil {
		return nil, fmt.Errorf("failed to export %s archive parquet: %w", L1ArchiveStaging, err)
	}
	paths[L1ArchiveStaging] = stagingPath

	return paths, nil
}

func (d *ArchiveSQLiteStore) exportMemoryEventArchiveParquet(ctx context.Context, outputPath string) error {
	rows, err := d.db.QueryContext(ctx, `
	SELECT id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
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
		var threadID string
		var threadSeq int64
		var threadKind string
		if err := rows.Scan(
			&rec.ID, &rec.Namespace, &rec.SessionID, &threadID, &threadSeq, &threadKind, &rec.Speaker, &rec.Message,
			&rec.MetaJSON, &rec.MemoryState, &rec.Layer, &rec.Source, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return err
		}
		if err := validateArchiveThreadTuple(modulecore.ThreadID(threadID), modulecore.ThreadSeq(threadSeq), modulecore.ThreadKind(threadKind), true); err != nil {
			return fmt.Errorf("invalid archived memory thread identity: %w", err)
		}
		rec.ThreadID = threadID
		rec.ThreadSeq = threadSeq
		rec.ThreadKind = threadKind
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
