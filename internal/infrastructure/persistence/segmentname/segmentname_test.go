package segmentname_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/segmentname"
)

// These are golden tests: the Task store's on-disk segment directory names are
// a persisted contract (an older binary must find the directories a newer one
// made), so every expected string below is the literal the Task store produced
// before this rule moved out of it. A change to any of them is a format change.

func TestFormatIsTheDayAndATwoDigitSequence(t *testing.T) {
	cases := []struct {
		day      string
		sequence int
		want     string
	}{
		{"20261013", 1, "20261013-01"},
		{"20261013", 9, "20261013-09"},
		{"20261013", 10, "20261013-10"},
		{"20261013", 99, "20261013-99"},
		{"20260101", 2, "20260101-02"},
		// Three digits is not a valid name; Format does not hide that. The caller
		// bounds the sequence (the Task store stops at its per-day limit).
		{"20261013", 100, "20261013-100"},
	}
	for _, tc := range cases {
		if got := segmentname.Format(tc.day, tc.sequence); got != tc.want {
			t.Errorf("Format(%q, %d) = %q, want %q", tc.day, tc.sequence, got, tc.want)
		}
	}
}

func TestDayAndSequenceReadAName(t *testing.T) {
	cases := []struct {
		name     string
		day      string
		sequence int
	}{
		{"20261013-01", "20261013", 1},
		{"20261013-02", "20261013", 2},
		{"20261231-99", "20261231", 99},
		{"20260101-10", "20260101", 10},
	}
	for _, tc := range cases {
		if got := segmentname.Day(tc.name); got != tc.day {
			t.Errorf("Day(%q) = %q, want %q", tc.name, got, tc.day)
		}
		if got := segmentname.Sequence(tc.name); got != tc.sequence {
			t.Errorf("Sequence(%q) = %d, want %d", tc.name, got, tc.sequence)
		}
		if got := segmentname.Format(segmentname.Day(tc.name), segmentname.Sequence(tc.name)); got != tc.name {
			t.Errorf("Format(Day, Sequence) of %q = %q, want a round trip", tc.name, got)
		}
	}
}

func TestUTCDayConvertsToUTC(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 13, 23, 59, 59, 0, jst), "20261013"}, // 14:59 UTC the same day
		{time.Date(2026, 10, 14, 8, 59, 59, 0, jst), "20261013"},  // 23:59 UTC the day before
		{time.Date(2026, 10, 14, 9, 0, 0, 0, jst), "20261014"},    // 00:00 UTC
		{time.Date(2026, 10, 13, 23, 59, 59, 999, time.UTC), "20261013"},
		{time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), "20261014"},
		{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "20270101"},
	}
	for _, tc := range cases {
		if got := segmentname.UTCDay(tc.at); got != tc.want {
			t.Errorf("UTCDay(%s) = %q, want %q", tc.at.Format(time.RFC3339Nano), got, tc.want)
		}
	}
}

func TestIsNameAcceptsOnlyEightDigitsDashTwoDigits(t *testing.T) {
	good := []string{"20261013-01", "20261013-99", "00000000-00", "20261231-10"}
	bad := []string{
		"", "notasegment", "2026101-1", "2026101-01", "20261013-1", "20261013-001", "20261013_01",
		"20261013-0a", "a0261013-01", " 20261013-01", "20261013-01 ", "20261013-01\n", "logs", "LAYOUT", "tasks-20261013",
	}
	for _, name := range good {
		if !segmentname.IsName(name) {
			t.Errorf("IsName(%q) = false, want true", name)
		}
	}
	for _, name := range bad {
		if segmentname.IsName(name) {
			t.Errorf("IsName(%q) = true, want false", name)
		}
	}
}

func TestListDirsReturnsSegmentDirectoriesInOrdinalOrder(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"20261014-01", "20261013-02", "20261013-01", "notasegment", "2026101-1"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A regular file with a segment-shaped name is not a segment.
	if err := os.WriteFile(filepath.Join(base, "20270101-01"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := segmentname.ListDirs(base)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"20261013-01", "20261013-02", "20261014-01"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListDirs = %v, want %v", got, want)
	}
}

func TestListDirsOfAMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	got, err := segmentname.ListDirs(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(got) != 0 {
		t.Fatalf("ListDirs of a missing directory = %v, %v; want no names and no error", got, err)
	}
}

func TestListDirsOfAFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := segmentname.ListDirs(path); err == nil {
		t.Fatal("ListDirs of a regular file succeeded")
	}
}

func TestListDirsOrdersByDayThenSequenceAcrossDigits(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"20261013-10", "20261013-02", "20261012-99", "20261013-01"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := segmentname.ListDirs(base)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"20261012-99", "20261013-01", "20261013-02", "20261013-10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListDirs = %v, want %v (lexical order is ordinal order)", got, want)
	}
}
