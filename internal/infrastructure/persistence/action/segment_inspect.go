package action

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
	// Bytes is the size of its two logs together.
	Bytes int64 `json:"bytes"`
	// Files lists the size of each log, in the order of logFilenames (the Action
	// log, then the Attempt log).
	Files []int64 `json:"files"`
	// Sealed says what the record of a closed segment shows: "open" (the segment
	// writes go to), "recorded" (closed, and its files are the recorded size),
	// "mismatch" (closed, and its files are not), "missing" (closed with no
	// record). Segment 0 is judged against LAYOUT.
	Sealed string `json:"sealed"`
}

// LayoutReport says what layout a store is on and what it is made of. It names
// segments and counts bytes; it never carries the content of a record.
type LayoutReport struct {
	// Layout is "v1" (the logs in the store root) or "v2" (segment 0 frozen in
	// the store root and a segment per day under logs/).
	Layout   string          `json:"layout"`
	Segments []LayoutSegment `json:"segments"`
	// Segment0ReadOnly and Segment0MatchesLayout describe segment 0 of a layout v2
	// store: whether its files are read-only, and whether their sizes are those
	// LAYOUT recorded when it froze them (anything else is a history that has been
	// written to since).
	Segment0ReadOnly      bool `json:"segment0_read_only,omitempty"`
	Segment0MatchesLayout bool `json:"segment0_matches_layout,omitempty"`
}

// InspectActionLayout reports the layout of the store at root without opening it:
// no lock is taken and nothing is written.
func InspectActionLayout(root string) (LayoutReport, error) {
	var report LayoutReport
	root = filepath.Clean(root)
	if info, err := os.Stat(root); err != nil {
		return report, fmt.Errorf("action store %q: %w", root, err)
	} else if !info.IsDir() {
		return report, fmt.Errorf("action store %q is not a directory", root)
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
	for i, ref := range refs {
		sz, err := segmentFileSizes(ref.dir)
		if err != nil {
			return report, err
		}
		seg := LayoutSegment{Ordinal: ref.ord, Name: ref.name, Files: append([]int64(nil), sz[:]...), Sealed: "open"}
		for _, v := range sz {
			seg.Bytes += v
		}
		if layout.v2 {
			record, known, err := recordOfSegment(layout, ref)
			if err != nil {
				return report, err
			}
			switch {
			case i == len(refs)-1 && !known:
				seg.Sealed = "open"
			case !known:
				seg.Sealed = "missing"
			case record != sz:
				seg.Sealed = "mismatch"
			default:
				seg.Sealed = "recorded"
			}
		}
		report.Segments = append(report.Segments, seg)
	}
	if layout.v2 {
		report.Segment0ReadOnly = segmentZeroFrozen(root)
		report.Segment0MatchesLayout = verifyFrozenSegmentZero(root, layout.frozen) == nil
	}
	return report, nil
}

// recordOfSegment is the size record of a segment of a store on layout v2:
// LAYOUT's for segment 0, SEALED's for the others (known is false when it has
// none).
func recordOfSegment(layout layoutInfo, ref segmentRef) (record sizes, known bool, err error) {
	if ref.ord == 0 {
		return layout.frozen, true, nil
	}
	return readSealed(ref.dir)
}
