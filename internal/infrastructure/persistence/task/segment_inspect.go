package task

import (
	"fmt"
	"os"
	"path/filepath"
)

// LayoutSegment is one segment of the store: where its writes went and how much
// it holds.
type LayoutSegment struct {
	Ordinal int    `json:"ordinal"`
	Name    string `json:"name"`
	// Bytes is the size of its six log files together.
	Bytes int64 `json:"bytes"`
	// Files lists the size of each log file, in the order of sizedFilenames
	// (state, run, context, notifications, receipts, fences).
	Files []int64 `json:"files"`
}

// LayoutReport says what layout a store is on and what it is made of. It names
// segments and counts bytes; it never carries the content of a record.
type LayoutReport struct {
	// Layout is "v1" (the log files in the store root) or "v2" (segment 0 frozen
	// in the store root and a segment per day under logs/).
	Layout   string          `json:"layout"`
	Segments []LayoutSegment `json:"segments"`
	// Segment0ReadOnly and Segment0MatchesLayout describe segment 0 of a layout v2
	// store: whether its files are read-only, and whether their sizes are those
	// LAYOUT recorded when it froze them (anything else is a history that has
	// been written to since).
	Segment0ReadOnly      bool `json:"segment0_read_only,omitempty"`
	Segment0MatchesLayout bool `json:"segment0_matches_layout,omitempty"`
	// Sidecar is "present" or "missing": the index sidecar of the layout.
	Sidecar string `json:"sidecar"`
}

// InspectTaskLayout reports the layout of the store at root without opening it:
// no lock is taken and nothing is written.
func InspectTaskLayout(root string) (LayoutReport, error) {
	var report LayoutReport
	root = filepath.Clean(root)
	if info, err := os.Stat(root); err != nil {
		return report, fmt.Errorf("task store %q: %w", root, err)
	} else if !info.IsDir() {
		return report, fmt.Errorf("task store %q is not a directory", root)
	}
	layout, err := readLayout(root)
	if err != nil {
		return report, err
	}
	refs, err := listSegments(root, layout.v2)
	if err != nil {
		return report, err
	}
	report.Layout = "v1"
	if layout.v2 {
		report.Layout = "v2"
	}
	for _, ref := range refs {
		sizes, err := segmentFileSizes(ref.dir)
		if err != nil {
			return report, err
		}
		seg := LayoutSegment{Ordinal: int(ref.ord), Name: ref.name, Files: sizes[:]}
		for _, v := range sizes {
			seg.Bytes += v
		}
		report.Segments = append(report.Segments, seg)
	}
	if layout.v2 {
		report.Segment0ReadOnly = segmentZeroFrozen(root)
		report.Segment0MatchesLayout = verifyFrozenSegmentZero(root, layout.frozen) == nil
	}
	report.Sidecar = "missing"
	if _, err := os.Stat(sidecarPathFor(root, layout.v2)); err == nil {
		report.Sidecar = "present"
	}
	return report, nil
}
