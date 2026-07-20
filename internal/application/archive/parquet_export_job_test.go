package archive

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type fakeParquetExportStore struct {
	threadPath string
	archiveDir string
}

func (s *fakeParquetExportStore) ExportThreadSummariesParquet(_ context.Context, outputPath string) error {
	s.threadPath = outputPath
	return nil
}

func (s *fakeParquetExportStore) ExportL1ArchivesParquet(_ context.Context, outputDir string) (map[string]string, error) {
	s.archiveDir = outputDir
	return map[string]string{"memory": filepath.Join(outputDir, "l1_memory_event.parquet")}, nil
}

func TestParquetExportJobRunOnce(t *testing.T) {
	store := &fakeParquetExportStore{}
	job := NewParquetExportJob(store, ParquetExportOptions{
		OutputDir: "/tmp/rencrow-archive",
		Now: func() time.Time {
			return time.Date(2026, 5, 5, 10, 30, 0, 0, time.UTC)
		},
	})

	result, err := job.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if store.threadPath != filepath.Join("/tmp/rencrow-archive", "20260505T103000Z", "thread_summaries.parquet") {
		t.Fatalf("unexpected thread export path: %s", store.threadPath)
	}
	if store.archiveDir != filepath.Join("/tmp/rencrow-archive", "20260505T103000Z", "l1") {
		t.Fatalf("unexpected l1 archive dir: %s", store.archiveDir)
	}
	if result.ThreadSummariesPath != store.threadPath || result.L1ArchivePaths["memory"] == "" {
		t.Fatalf("unexpected export result: %+v", result)
	}
}

func TestParquetExportJobStartRunsOnTicker(t *testing.T) {
	store := &fakeParquetExportStore{}
	job := NewParquetExportJob(store, ParquetExportOptions{
		OutputDir: "/tmp/rencrow-archive",
		Interval:  10 * time.Millisecond,
		Now:       func() time.Time { return time.Date(2026, 5, 5, 10, 30, 0, 0, time.UTC) },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	results := job.Start(ctx)

	select {
	case result := <-results:
		if result.Error != nil {
			t.Fatalf("unexpected export error: %v", result.Error)
		}
		if store.threadPath == "" {
			t.Fatal("expected scheduled export to call store")
		}
	case <-ctx.Done():
		t.Fatal("scheduled export did not run")
	}
}
