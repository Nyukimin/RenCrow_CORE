package action

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// layout show and verify on a store with segments: they read the same history
// the writer does, every segment of it.

func TestLayoutShowReportsEverySegment(t *testing.T) {
	root, _, _ := segmentedStore(t)
	report, err := InspectActionLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Layout != "v2" || len(report.Segments) != 4 {
		t.Fatalf("report = %+v, want layout v2 with legacy and three segments", report)
	}
	if !report.Segment0MatchesLayout {
		t.Error("segment 0 does not match LAYOUT")
	}
	if !report.Segment0ReadOnly {
		t.Error("segment 0 is not read-only")
	}
	wantSealed := []string{"recorded", "recorded", "recorded", "open"}
	var total int64
	for i, seg := range report.Segments {
		if seg.Ordinal != i || seg.Sealed != wantSealed[i] {
			t.Errorf("segment %d = %+v, want sealed=%q", i, seg, wantSealed[i])
		}
		total += seg.Bytes
	}
	if report.Segments[0].Name != segmentLegacyName {
		t.Errorf("segment 0 is named %q", report.Segments[0].Name)
	}
	var onDisk int64
	for _, ref := range append([]segmentRef{{dir: root}}, segmentRefsOf(t, root)...) {
		sz, err := segmentFileSizes(ref.dir)
		if err != nil {
			t.Fatal(err)
		}
		onDisk += sz[fileAction] + sz[fileAttempt]
	}
	if total != onDisk {
		t.Errorf("the report counts %d bytes, the files hold %d", total, onDisk)
	}
}

func segmentRefsOf(t testing.TB, root string) []segmentRef {
	t.Helper()
	var refs []segmentRef
	for i, name := range segmentNames(t, root) {
		refs = append(refs, segmentRef{ord: i + 1, name: name, dir: segmentDir(root, name)})
	}
	return refs
}

func TestLayoutShowOnAPlainStore(t *testing.T) {
	root := t.TempDir()
	plainStore(t, root, makePairs(t, 3, rotStart), 3)
	report, err := InspectActionLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Layout != "v1" || len(report.Segments) != 1 || report.Segments[0].Name != segmentLegacyName || report.Segments[0].Bytes == 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestLayoutShowFlagsAChangedStore(t *testing.T) {
	base, _, _ := segmentedStore(t)
	t.Run("a closed segment that grew", func(t *testing.T) {
		root := copyOf(t, base)
		mutateFile(t, filepath.Join(root, logsDirName, "20261013-01", actionFilename), func(d []byte) []byte { return append(d, lastLine(d)...) })
		report, err := InspectActionLayout(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.Segments[1].Sealed != "mismatch" {
			t.Fatalf("segment 1 = %+v, want mismatch", report.Segments[1])
		}
	})
	t.Run("a closed segment without its record", func(t *testing.T) {
		root := copyOf(t, base)
		if err := os.Remove(sealedPath(filepath.Join(root, logsDirName, "20261014-01"))); err != nil {
			t.Fatal(err)
		}
		report, err := InspectActionLayout(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.Segments[2].Sealed != "missing" {
			t.Fatalf("segment 2 = %+v, want missing", report.Segments[2])
		}
	})
	t.Run("segment 0 that grew", func(t *testing.T) {
		root := copyOf(t, base)
		mutateFile(t, filepath.Join(root, actionFilename), func(d []byte) []byte { return append(d, lastLine(d)...) })
		report, err := InspectActionLayout(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.Segment0MatchesLayout {
			t.Fatal("segment 0 was reported as matching LAYOUT after it grew")
		}
	})
}

func TestVerifyCoversEverySegment(t *testing.T) {
	root, pairs, _ := segmentedStore(t)
	report, err := VerifyActionStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Mismatches != 0 {
		t.Fatalf("verify = %+v", report)
	}
	if report.Layout != "v2" || report.Segments != 4 {
		t.Fatalf("verify saw layout %q with %d segments, want v2 with 4", report.Layout, report.Segments)
	}
	if report.Actions != len(pairs) || report.Attempts != len(pairs) {
		t.Fatalf("verify counted %d Actions and %d Attempts, want %d of each", report.Actions, report.Attempts, len(pairs))
	}
	var actionBytes, attemptBytes int64
	for _, dir := range append([]string{root}, segmentDirs(t, root)...) {
		sz, err := segmentFileSizes(dir)
		if err != nil {
			t.Fatal(err)
		}
		actionBytes += sz[fileAction]
		attemptBytes += sz[fileAttempt]
	}
	if report.ActionBytes != actionBytes || report.AttemptBytes != attemptBytes {
		t.Fatalf("verify counted %d and %d bytes, the files hold %d and %d", report.ActionBytes, report.AttemptBytes, actionBytes, attemptBytes)
	}
}

func segmentDirs(t testing.TB, root string) []string {
	t.Helper()
	var dirs []string
	for _, name := range segmentNames(t, root) {
		dirs = append(dirs, segmentDir(root, name))
	}
	return dirs
}

func TestVerifyReportsAClosedSegmentThatChanged(t *testing.T) {
	base, _, _ := segmentedStore(t)
	root := copyOf(t, base)
	mutateFile(t, filepath.Join(root, logsDirName, "20261013-01", attemptFilename), func(d []byte) []byte { return d[:len(d)-1] })
	report, err := VerifyActionStore(context.Background(), root)
	if err != nil {
		t.Fatalf("a changed closed segment is a finding, not a failure to run: %v", err)
	}
	if report.OK || report.Mismatches == 0 {
		t.Fatalf("verify = %+v, want a mismatch", report)
	}
}

func TestVerifyOnAPlainStoreStillReportsLayoutV1(t *testing.T) {
	root := t.TempDir()
	plainStore(t, root, makePairs(t, 3, rotStart), 3)
	report, err := VerifyActionStore(context.Background(), root)
	if err != nil || !report.OK {
		t.Fatalf("verify = %+v, %v", report, err)
	}
	if report.Layout != "v1" || report.Segments != 1 {
		t.Fatalf("verify saw layout %q with %d segments, want v1 with 1", report.Layout, report.Segments)
	}
}
