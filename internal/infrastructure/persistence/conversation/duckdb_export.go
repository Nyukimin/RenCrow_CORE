//go:build linux && amd64

package conversation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb"
)

func (d *DuckDBStore) ExportThreadSummariesParquet(ctx context.Context, outputPath string) error {
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return fmt.Errorf("parquet output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("failed to create parquet output directory: %w", err)
	}
	escapedPath := strings.ReplaceAll(outputPath, "'", "''")
	query := fmt.Sprintf(`
COPY (
	SELECT thread_id, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel, created_at
	FROM session_thread
	ORDER BY ts_start ASC, thread_id ASC
) TO '%s' (FORMAT PARQUET)
`, escapedPath)
	if _, err := d.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("failed to export thread summaries to parquet: %w", err)
	}
	return nil
}

func (d *DuckDBStore) ExportL1ArchivesParquet(ctx context.Context, outputDir string) (map[string]string, error) {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return nil, fmt.Errorf("parquet output directory is required")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create l1 archive output directory: %w", err)
	}
	targets := map[string]struct {
		table string
		order string
		file  string
	}{
		L1ArchiveMemory: {
			table: "l1_memory_event_archive",
			order: "created_at ASC, id ASC",
			file:  "l1_memory_event.parquet",
		},
		L1ArchiveNews: {
			table: "l1_news_item_archive",
			order: "COALESCE(published_at, fetched_at) ASC, id ASC",
			file:  "l1_news_item.parquet",
		},
		L1ArchiveKnowledge: {
			table: "l1_knowledge_item_archive",
			order: "updated_at ASC, id ASC",
			file:  "l1_knowledge_item.parquet",
		},
		L1ArchiveStaging: {
			table: "l1_staging_item_archive",
			order: "created_at ASC, id ASC",
			file:  "l1_staging_item.parquet",
		},
	}
	paths := make(map[string]string, len(targets))
	for kind, target := range targets {
		path := filepath.Join(outputDir, target.file)
		if err := d.exportTableParquet(ctx, target.table, target.order, path); err != nil {
			return nil, fmt.Errorf("failed to export %s archive parquet: %w", kind, err)
		}
		paths[kind] = path
	}
	return paths, nil
}

func (d *DuckDBStore) exportTableParquet(ctx context.Context, table string, order string, outputPath string) error {
	escapedPath := strings.ReplaceAll(outputPath, "'", "''")
	query := fmt.Sprintf(`
COPY (
	SELECT *
	FROM %s
	ORDER BY %s
) TO '%s' (FORMAT PARQUET)
`, table, order, escapedPath)
	if _, err := d.db.ExecContext(ctx, query); err != nil {
		return err
	}
	return nil
}

// CleanupOldRecords は7日以上経過したレコードを削除
func (d *DuckDBStore) CleanupOldRecords(ctx context.Context) (int64, error) {
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
