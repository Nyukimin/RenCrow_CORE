package jsonlutil

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testRecord struct {
	ID int `json:"id"`
}

func TestListLatestReadsTailNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.jsonl")
	for i := 1; i <= 5; i++ {
		if err := Append(path, testRecord{ID: i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	items, err := ListLatest[testRecord](path, 3)
	if err != nil {
		t.Fatalf("ListLatest() error = %v", err)
	}
	if got := []int{items[0].ID, items[1].ID, items[2].ID}; got[0] != 5 || got[1] != 4 || got[2] != 3 {
		t.Fatalf("latest IDs = %v, want [5 4 3]", got)
	}
}

func TestAppendBoundedCompactsWhenFileExceedsMaxBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.jsonl")
	for i := 1; i <= 6; i++ {
		if err := AppendBounded(path, testRecord{ID: i}, BoundOptions{MaxRecords: 3, MaxBytes: 1}); err != nil {
			t.Fatalf("append bounded %d: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compacted file: %v", err)
	}
	lines := splitNonEmptyLines(data)
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3; data=%s", len(lines), data)
	}
	var ids []int
	for _, line := range lines {
		var rec testRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal compacted line: %v", err)
		}
		ids = append(ids, rec.ID)
	}
	if ids[0] != 4 || ids[1] != 5 || ids[2] != 6 {
		t.Fatalf("compacted IDs = %v, want [4 5 6]", ids)
	}
	archives, err := filepath.Glob(filepath.Join(filepath.Dir(path), "archive", "records.compacted.*.jsonl.gz"))
	if err != nil {
		t.Fatalf("glob archive: %v", err)
	}
	if len(archives) == 0 {
		t.Fatal("expected compacted archive")
	}
	archiveLines := readJSONLGzipLines(t, archives[len(archives)-1])
	if len(archiveLines) == 0 || !strings.Contains(archiveLines[0], `"id":`) {
		t.Fatalf("unexpected archive lines: %q", archiveLines)
	}
}

func TestCompactLatestRecordsArchivesDroppedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.jsonl")
	for i := 1; i <= 5; i++ {
		if err := Append(path, testRecord{ID: i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := CompactLatestRecords(path, 2); err != nil {
		t.Fatalf("CompactLatestRecords() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compacted file: %v", err)
	}
	lines := splitNonEmptyLines(data)
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2; data=%s", len(lines), data)
	}
	var keptIDs []int
	for _, line := range lines {
		var rec testRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal compacted line: %v", err)
		}
		keptIDs = append(keptIDs, rec.ID)
	}
	if keptIDs[0] != 4 || keptIDs[1] != 5 {
		t.Fatalf("kept IDs = %v, want [4 5]", keptIDs)
	}
	archives, err := filepath.Glob(filepath.Join(filepath.Dir(path), "archive", "records.compacted.*.jsonl.gz"))
	if err != nil {
		t.Fatalf("glob archive: %v", err)
	}
	if len(archives) != 1 {
		t.Fatalf("archive count = %d, want 1", len(archives))
	}
	archiveLines := readJSONLGzipLines(t, archives[0])
	if len(archiveLines) != 3 {
		t.Fatalf("archive line count = %d, want 3: %q", len(archiveLines), archiveLines)
	}
	var droppedIDs []int
	for _, line := range archiveLines {
		var rec testRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshal archive line: %v", err)
		}
		droppedIDs = append(droppedIDs, rec.ID)
	}
	if droppedIDs[0] != 1 || droppedIDs[1] != 2 || droppedIDs[2] != 3 {
		t.Fatalf("dropped archive IDs = %v, want [1 2 3]", droppedIDs)
	}
}

func readJSONLGzipLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open gzip archive: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("new gzip reader: %v", err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip archive: %v", err)
	}
	raw := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(raw) == 1 && raw[0] == "" {
		return []string{}
	}
	return raw
}

func splitNonEmptyLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}
		if i > start {
			out = append(out, data[start:i])
		}
		start = i + 1
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
