package health

import (
	"context"
	"fmt"
	"time"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// ActionStoreReporter is what the check needs from the Action store.
type ActionStoreReporter interface {
	Health() actionpersistence.StoreHealth
}

// ActionStoreCheck reports the state of the canonical Action store: its layout,
// its segments and the in-memory snapshot (design: ActionStore全件読み IU-6).
//
// The status is always ok. IsReady is "every check is ok", and /ready is what the
// backup waits on after it starts CORE again and what core-verify reads; the
// store serves from its own snapshot whatever these facts say. A dirty or broken
// snapshot and a rebuild are states the store settles by itself. A closed segment
// that is not the size recorded for it is a fact for a person (run
// `rencrow actions verify`); a newest segment that is sealed with no successor is
// a rotation that stopped between the two (writes wait for it). Both are named in
// "attention" and do not change the status. Making a check able to turn /health down without turning /ready down is
// a change to HealthService, not to this check.
//
// The details carry counts, sizes, segment names and fixed words only: no ID, no
// record text and no path or error text.
type ActionStoreCheck struct {
	store ActionStoreReporter
}

// NewActionStoreCheck makes the check for store, which must not be nil.
func NewActionStoreCheck(store ActionStoreReporter) *ActionStoreCheck {
	return &ActionStoreCheck{store: store}
}

// Name is the name the check has in /health.
func (c *ActionStoreCheck) Name() string { return "action_store" }

// Run reads the state of the store.
func (c *ActionStoreCheck) Run(_ context.Context) domainhealth.CheckResult {
	started := time.Now()
	h := c.store.Health()

	layout := h.Layout
	if layout == "" {
		layout = "unknown"
	}
	reason := h.LastRebuildReason
	if reason == "" {
		reason = "none"
	}
	attention := []string{}
	if h.InspectFailed {
		attention = append(attention, "layout_unreadable")
	}
	if h.RecordMismatches > 0 {
		attention = append(attention, "record_mismatch")
	}
	if h.CurrentSegmentSealed {
		attention = append(attention, "current_segment_sealed")
	}

	details := map[string]any{
		"indexed":             h.Indexed,
		"layout":              layout,
		"inspect_failed":      h.InspectFailed,
		"segments":            h.Segments,
		"current_segment":     h.CurrentSegment,
		"record_mismatches":   h.RecordMismatches,
		"actions":             h.Actions,
		"attempts":            h.Attempts,
		"action_bytes":        h.ActionBytes,
		"attempt_bytes":       h.AttemptBytes,
		"build_ms":            h.BuildDuration.Milliseconds(),
		"rebuilds":            h.Rebuilds,
		"last_rebuild_reason": reason,
		"dirty":               h.Dirty,
		"broken":              h.Broken,
		"attention":           attention,
	}
	if layout == "v2" {
		details["segment0_read_only"] = h.Segment0ReadOnly
		details["segment0_matches_layout"] = h.Segment0MatchesLayout
		details["current_segment_sealed"] = h.CurrentSegmentSealed
	}
	if !h.LastRebuildAt.IsZero() {
		details["last_rebuild_at"] = h.LastRebuildAt.UTC().Format(time.RFC3339)
	}

	message := fmt.Sprintf("layout=%s segments=%d current=%s", layout, h.Segments, h.CurrentSegment)
	for _, item := range attention {
		message += " attention=" + item
	}
	return domainhealth.CheckResult{
		Name: c.Name(), Status: domainhealth.StatusOK,
		Message: message, Duration: time.Since(started), Details: details,
	}
}
