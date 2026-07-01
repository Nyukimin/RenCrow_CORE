package archive

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type ParquetExportStore interface {
	ExportThreadSummariesParquet(ctx context.Context, outputPath string) error
	ExportL1ArchivesParquet(ctx context.Context, outputDir string) (map[string]string, error)
}

type ParquetExportOptions struct {
	OutputDir string
	Interval  time.Duration
	Now       func() time.Time
}

type ParquetExportResult struct {
	StartedAt           time.Time
	ThreadSummariesPath string
	L1ArchivePaths      map[string]string
	Error               error
}

type ParquetExportJob struct {
	store ParquetExportStore
	opts  ParquetExportOptions
}

func NewParquetExportJob(store ParquetExportStore, opts ParquetExportOptions) *ParquetExportJob {
	return &ParquetExportJob{store: store, opts: opts}
}

func (j *ParquetExportJob) RunOnce(ctx context.Context) (ParquetExportResult, error) {
	if j == nil || j.store == nil {
		return ParquetExportResult{}, errors.New("parquet export store is required")
	}
	outputDir := strings.TrimSpace(j.opts.OutputDir)
	if outputDir == "" {
		return ParquetExportResult{}, errors.New("parquet export output dir is required")
	}
	now := time.Now().UTC()
	if j.opts.Now != nil {
		now = j.opts.Now().UTC()
	}
	runDir := filepath.Join(outputDir, now.Format("20060102T150405Z"))
	threadPath := filepath.Join(runDir, "thread_summaries.parquet")
	result := ParquetExportResult{
		StartedAt:           now,
		ThreadSummariesPath: threadPath,
		L1ArchivePaths:      map[string]string{},
	}
	if err := j.store.ExportThreadSummariesParquet(ctx, threadPath); err != nil {
		return result, err
	}
	archivePaths, err := j.store.ExportL1ArchivesParquet(ctx, filepath.Join(runDir, "l1"))
	if err != nil {
		return result, err
	}
	result.L1ArchivePaths = archivePaths
	return result, nil
}

func (j *ParquetExportJob) Start(ctx context.Context) <-chan ParquetExportResult {
	out := make(chan ParquetExportResult, 1)
	interval := j.opts.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		defer close(out)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result, err := j.RunOnce(ctx)
				result.Error = err
				select {
				case out <- result:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
