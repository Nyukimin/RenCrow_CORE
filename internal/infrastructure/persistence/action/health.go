package action

import "time"

// StoreHealth is what the Action store says about itself for /health (design
// IU-6). It is counts, sizes, segment names and a fixed reason, so it is safe to
// hand to monitoring: it never carries an ID, the content of a record, a path or
// the text of an error.
//
// It is read without the OS lock and writes nothing: the in-memory part comes
// from the snapshot's own lock, and the part about the files is
// InspectActionLayout, which only stats the logs and reads the small LAYOUT and
// SEALED records. Its cost is a few system calls per segment and does not depend
// on how much the logs hold. A probe can therefore never wait for the OS lock a
// writer holds (it does take the snapshot's own lock for reading, which the
// absorb of a commit holds for microseconds).
type StoreHealth struct {
	// Indexed says the store keeps the snapshot. A store on layout v2 always does.
	Indexed bool

	// Layout is "v1" or "v2" as the files say it; empty when they could not be read
	// (InspectFailed).
	Layout string
	// InspectFailed is true when the layout files could not be read. The reason is
	// not reported (the error carries a path); `rencrow actions layout show` and
	// `actions verify` say why.
	InspectFailed bool
	// Segments is how many segments the store has, and CurrentSegment the one
	// writes go to ("legacy" on layout v1, <YYYYMMDD>-<NN> on v2). With the
	// snapshot they are the ones it follows; without it, the ones on disk.
	Segments       int
	CurrentSegment string
	// Segment0ReadOnly and Segment0MatchesLayout are about layout v2: whether the
	// files of the frozen segment 0 are read-only and still the size LAYOUT
	// recorded. Both are false on layout v1.
	Segment0ReadOnly      bool
	Segment0MatchesLayout bool
	// CurrentSegmentSealed is true on layout v2 when the newest segment on disk is
	// sealed: a rotation seals a segment before it makes the next one, so this is a
	// rotation that stopped between the two. Writes wait for the successor, which
	// the next rotation or the next open makes. It is brief in a rotation that is
	// running and lasting when the successor cannot be made.
	CurrentSegmentSealed bool
	// RecordMismatches is how many closed segments (segment 0 included) have files
	// of another size than the one recorded for them, or no record. The segment
	// writes go to is not counted: it grows. A store that finds one at open does
	// not start; seeing one here means the files were touched while it runs.
	RecordMismatches int

	// Actions and Attempts are the records the snapshot holds, ActionBytes and
	// AttemptBytes the size of the logs it covers (all segments), BuildDuration
	// how long the last build or rebuild took. All are zero without the snapshot.
	Actions       int
	Attempts      int
	ActionBytes   int64
	AttemptBytes  int64
	BuildDuration time.Duration
	// Rebuilds, LastRebuildReason and LastRebuildAt are IndexStats'. The count
	// starts again at zero with the process.
	Rebuilds          int64
	LastRebuildReason string
	LastRebuildAt     time.Time
	// Dirty and Broken are the snapshot's marks. Both are short states the next
	// operation settles (a commit is in flight, or one ended without a known
	// boundary), not faults by themselves.
	Dirty  bool
	Broken bool
}

// Health reports the state of the store. See StoreHealth.
func (s *JSONLStore) Health() StoreHealth {
	var h StoreHealth
	if s == nil {
		return h
	}
	if stats, ok := s.IndexStats(); ok {
		h.Indexed = true
		h.Segments = stats.Segments
		h.CurrentSegment = stats.CurrentSegment
		h.Actions = stats.Actions
		h.Attempts = stats.Attempts
		h.ActionBytes = stats.ActionBytes
		h.AttemptBytes = stats.AttemptBytes
		h.BuildDuration = stats.BuildDuration
		h.Rebuilds = stats.Rebuilds
		h.LastRebuildReason = stats.LastRebuildReason
		h.LastRebuildAt = stats.LastRebuildAt
		h.Dirty = stats.Dirty
		h.Broken = stats.Broken
	}
	report, err := InspectActionLayout(s.root)
	if err != nil {
		h.InspectFailed = true
		return h
	}
	h.Layout = report.Layout
	if !h.Indexed && len(report.Segments) > 0 {
		h.Segments = len(report.Segments)
		h.CurrentSegment = report.Segments[len(report.Segments)-1].Name
	}
	h.Segment0ReadOnly = report.Segment0ReadOnly
	h.Segment0MatchesLayout = report.Segment0MatchesLayout
	for _, seg := range report.Segments {
		if seg.Sealed == "mismatch" || seg.Sealed == "missing" {
			h.RecordMismatches++
		}
	}
	if report.Layout == "v2" && len(report.Segments) > 0 {
		h.CurrentSegmentSealed = report.Segments[len(report.Segments)-1].Sealed != "open"
	}
	return h
}
