//go:build !((linux && amd64) || (darwin && arm64))

package duckdb

import (
	"context"
	"fmt"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

// DuckDBStore is unavailable on platforms where the bundled DuckDB driver does not build.
type DuckDBStore struct{}

const (
	L1ArchiveMemory    = "memory"
	L1ArchiveNews      = "news"
	L1ArchiveKnowledge = "knowledge"
	L1ArchiveStaging   = "staging"
)

func NewDuckDBStore(_ string) (*DuckDBStore, error) {
	return nil, fmt.Errorf("duckdb archive is not supported on this platform")
}

func (d *DuckDBStore) SaveThreadSummary(context.Context, *conversation.ThreadSummary) error {
	return unsupportedDuckDBArchive()
}

func (d *DuckDBStore) GetSessionHistory(context.Context, string, int) ([]*conversation.ThreadSummary, error) {
	return nil, unsupportedDuckDBArchive()
}

func (d *DuckDBStore) SearchByDomain(context.Context, string, int) ([]*conversation.ThreadSummary, error) {
	return nil, unsupportedDuckDBArchive()
}

func (d *DuckDBStore) SearchKnowledgeArchiveFTS(context.Context, string, string, int) ([]l1sqlite.L1KnowledgeItem, error) {
	return nil, unsupportedDuckDBArchive()
}

func (d *DuckDBStore) ArchiveL1MemoryEvents(context.Context, []l1sqlite.L1MemoryEvent) error {
	return unsupportedDuckDBArchive()
}

func (d *DuckDBStore) ArchiveL1NewsItems(context.Context, []l1sqlite.L1NewsItem) error {
	return unsupportedDuckDBArchive()
}

func (d *DuckDBStore) ArchiveL1KnowledgeItems(context.Context, []l1sqlite.L1KnowledgeItem) error {
	return unsupportedDuckDBArchive()
}

func (d *DuckDBStore) ArchiveL1StagingItems(context.Context, []l1sqlite.L1StagingItem) error {
	return unsupportedDuckDBArchive()
}

func (d *DuckDBStore) ExportThreadSummariesParquet(context.Context, string) error {
	return unsupportedDuckDBArchive()
}

func (d *DuckDBStore) ExportL1ArchivesParquet(context.Context, string) (map[string]string, error) {
	return nil, unsupportedDuckDBArchive()
}

func (d *DuckDBStore) CleanupOldRecords(context.Context) (int64, error) {
	return 0, unsupportedDuckDBArchive()
}

func (d *DuckDBStore) Close() error {
	return nil
}

func unsupportedDuckDBArchive() error {
	return fmt.Errorf("duckdb archive is not supported on this platform")
}
