package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type fakeXBookmarkCollectionProcess struct {
	coreRecords []map[string]any
	report      map[string]any
	err         error
	outputDir   string
}

func (p *fakeXBookmarkCollectionProcess) Collect(_ context.Context, outputDir string) error {
	p.outputDir = outputDir
	if p.err != nil {
		return p.err
	}
	for name, value := range map[string]any{
		"rencrow_core.jsonl": p.coreRecords,
		"report.json":        p.report,
	} {
		path := filepath.Join(outputDir, name)
		var data []byte
		if name == "rencrow_core.jsonl" {
			for _, record := range value.([]map[string]any) {
				line, _ := json.Marshal(record)
				data = append(data, append(line, '\n')...)
			}
		} else {
			data, _ = json.Marshal(value)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func TestXBookmarkHeartbeatRunnerCollectsAndImportsPendingRecords(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	process := &fakeXBookmarkCollectionProcess{
		coreRecords: []map[string]any{
			{"domain": "general", "id": "x:1", "raw_text": "one", "source_id": "x:bookmarks_browser"},
			{"domain": "general", "id": "x:2", "raw_text": "two", "source_id": "x:bookmarks_browser"},
		},
		report: map[string]any{"status": "completed", "collected_count": 2, "external_fetch_succeeded": 1},
	}
	runner := newXBookmarkHeartbeatRunner(process, store, filepath.Join(t.TempDir(), "exports"))
	report, err := runner.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if report.Collected != 2 || report.Imported != 2 || report.ExternalFetched != 1 {
		t.Fatalf("report=%+v", report)
	}
	if process.outputDir == "" || filepath.Dir(process.outputDir) == process.outputDir {
		t.Fatalf("unique output directory was not provided: %q", process.outputDir)
	}
	items, err := store.RecentStagingItems(context.Background(), l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("pending items=%d, want 2", len(items))
	}
	for _, item := range items {
		if item.SourceID != "x:bookmarks_browser" || item.ValidationStatus != l1sqlite.L1StagingStatusPending {
			t.Fatalf("unexpected staging item: %+v", item)
		}
	}
}

func TestXBookmarkHeartbeatRunnerDoesNotImportAfterCollectorFailure(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := newXBookmarkHeartbeatRunner(&fakeXBookmarkCollectionProcess{err: errors.New("browser unavailable")}, store, filepath.Join(t.TempDir(), "exports"))
	if _, err := runner.Collect(context.Background()); err == nil {
		t.Fatal("collector failure must be returned")
	}
	items, err := store.RecentStagingItems(context.Background(), l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("collector failure must not import records: %+v", items)
	}
}

func TestXBookmarkCLIArgsAreExplicitAndHeadless(t *testing.T) {
	got := xBookmarkCLIArgs("/safe/export/run-1", 100)
	want := []string{"--headless", "--output-dir", "/safe/export/run-1", "--max-scrolls", "100"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%q, want %q", got, want)
	}
}
