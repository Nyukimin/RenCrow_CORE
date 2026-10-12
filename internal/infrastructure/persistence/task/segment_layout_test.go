package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLayoutRoundTripsAndRefusesAnythingElse(t *testing.T) {
	frozen := [sizedFileCount]int64{10, 0, 3, 4, 5, 6}
	data := formatLayout(frozen)
	if !strings.HasPrefix(string(data), "tasks-layout/v2\n") || !strings.HasSuffix(string(data), "\nend\n") {
		t.Fatalf("LAYOUT text = %q", data)
	}
	info, err := parseLayout(data)
	if err != nil || !info.v2 || info.frozen != frozen {
		t.Fatalf("parseLayout = %+v, %v", info, err)
	}
	for name, bad := range map[string]string{
		"empty":            "",
		"other version":    "tasks-layout/v3\n",
		"version only":     "tasks-layout/v2\n",
		"no end":           strings.TrimSuffix(string(data), "end\n"),
		"no final newline": strings.TrimSuffix(string(data), "\n"),
		"wrong file":       strings.Replace(string(data), "task_run.jsonl", "task_runs.jsonl", 1),
		"negative size":    strings.Replace(string(data), "task_state.jsonl 10", "task_state.jsonl -1", 1),
		"extra word":       strings.Replace(string(data), "task_state.jsonl 10", "task_state.jsonl 10 x", 1),
		"not a number":     strings.Replace(string(data), "task_state.jsonl 10", "task_state.jsonl ten", 1),
	} {
		if _, err := parseLayout([]byte(bad)); err == nil {
			t.Errorf("parseLayout accepted %s", name)
		}
	}
}

func TestReadLayoutOfAStoreWithoutLayoutIsV1(t *testing.T) {
	info, err := readLayout(t.TempDir())
	if err != nil || info.v2 {
		t.Fatalf("readLayout of a plain store = %+v, %v", info, err)
	}
}

func TestWriteLayoutIsWholeOrAbsent(t *testing.T) {
	root := t.TempDir()
	frozen := [sizedFileCount]int64{1, 2, 3, 4, 5, 6}
	if err := writeLayout(root, frozen, nil); err != nil {
		t.Fatal(err)
	}
	info, err := readLayout(root)
	if err != nil || info.frozen != frozen {
		t.Fatalf("readLayout = %+v, %v", info, err)
	}
	if _, err := os.Stat(layoutPath(root) + sidecarTmpSuffix); err == nil {
		t.Fatal("temporary LAYOUT file left behind")
	}
}

func TestSegmentNamesAndOrder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"20261014-01", "20261013-02", "20261013-01", "notasegment", "2026101-1"} {
		if err := os.MkdirAll(filepath.Join(logsDir(root), name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(logsDir(root), "20270101-01"), nil, 0o600); err != nil { // a file, not a segment
		t.Fatal(err)
	}
	names, err := listSegmentDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"20261013-01", "20261013-02", "20261014-01"}
	if len(names) != len(want) {
		t.Fatalf("segments = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("segments = %v, want %v", names, want)
		}
	}
	refs, err := listSegments(root, true)
	if err != nil || len(refs) != 4 || refs[0].name != segmentLegacyName || refs[0].dir != root || refs[3].ord != 3 || refs[3].name != "20261014-01" {
		t.Fatalf("listSegments = %+v, %v", refs, err)
	}
	if segmentDay("20261013-02") != "20261013" || segmentSequence("20261013-02") != 2 {
		t.Fatal("segment name parsing")
	}
	if utcDay(time.Date(2026, 10, 13, 23, 59, 59, 0, time.FixedZone("JST", 9*3600))) != "20261013" {
		t.Fatal("utcDay must convert to UTC (23:59 JST is 14:59 UTC)")
	}
}

func TestADirectoryWithDataButNoLayoutIsRefused(t *testing.T) {
	root := t.TempDir()
	dir := segmentDir(root, "20261013-01")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if refs, err := listSegments(root, false); err != nil || len(refs) != 1 {
		t.Fatalf("an empty orphan segment directory must be tolerated: %+v, %v", refs, err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFilename), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := listSegments(root, false); err == nil {
		t.Fatal("a segment directory that holds data was ignored although there is no LAYOUT")
	}
}
