package taskcenter

import (
	"context"
	"time"

	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// DailyRead is the separate read-model port for owner-owned daily work.
// Daily definitions are not native Goal Items and never receive a Goal ID.
type DailyRead interface {
	ListDaily(context.Context, DailyQuery) (DailyListResult, error)
}

type DailyQuery struct {
	KindID string `json:"kind_id,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

type DailyValueState string

const (
	// DailyMaxNativeRefs bounds each native reference collection in a daily projection.
	DailyMaxNativeRefs = 64
	// DailyMaxOccurrences bounds occurrences returned for one daily definition.
	DailyMaxOccurrences = 50
)

const (
	DailyValueKnown       DailyValueState = "known"
	DailyValueNone        DailyValueState = "none"
	DailyValueUnknown     DailyValueState = "unknown"
	DailyValueUnsupported DailyValueState = "unsupported"
)

// DailyValue preserves the difference between an explicitly empty value and
// a field that is absent, unknown, or unsupported by its native owner.
type DailyValue[T any] struct {
	State      DailyValueState `json:"state"`
	SourceRef  string          `json:"source_ref"`
	ReasonCode string          `json:"reason_code,omitempty"`
	Value      *T              `json:"value"`
}

func (v DailyValue[T]) Known() bool { return v.State == DailyValueKnown && v.Value != nil }

type DailyTimingProjection struct {
	Timezone                 DailyValue[string]                      `json:"timezone"`
	ScheduledStartAt         DailyValue[time.Time]                   `json:"scheduled_start_at"`
	NotBefore                DailyValue[time.Time]                   `json:"not_before"`
	AllowedWindows           DailyValue[[]modulecore.TaskTimeWindow] `json:"allowed_windows"`
	DeadlineAt               DailyValue[time.Time]                   `json:"deadline_at"`
	Recurrence               DailyValue[modulecore.TaskRecurrence]   `json:"recurrence"`
	EstimatedDurationSeconds DailyValue[int64]                       `json:"estimated_duration_seconds"`
}

type DailyAxisState string

const (
	DailyImplementationImplemented   DailyAxisState = "implemented"
	DailyImplementationUnimplemented DailyAxisState = "unimplemented"
	DailyImplementationUnsupported   DailyAxisState = "unsupported"
	DailyImplementationUnknown       DailyAxisState = "unknown"

	DailyConfiguredConfigured   DailyAxisState = "configured"
	DailyConfiguredUnconfigured DailyAxisState = "unconfigured"
	DailyConfiguredDisabled     DailyAxisState = "disabled"
	DailyConfiguredUnsupported  DailyAxisState = "unsupported"
	DailyConfiguredUnknown      DailyAxisState = "unknown"

	DailyEffectiveEnabled     DailyAxisState = "enabled"
	DailyEffectiveDisabled    DailyAxisState = "disabled"
	DailyEffectiveBlocked     DailyAxisState = "blocked"
	DailyEffectiveUnavailable DailyAxisState = "unavailable"
	DailyEffectiveUnsupported DailyAxisState = "unsupported"
	DailyEffectiveUnknown     DailyAxisState = "unknown"

	DailyObservedAvailable   DailyAxisState = "available"
	DailyObservedUnavailable DailyAxisState = "unavailable"
	DailyObservedStale       DailyAxisState = "stale"
	DailyObservedNotObserved DailyAxisState = "not_observed"
	DailyObservedUnsupported DailyAxisState = "unsupported"
	DailyObservedUnknown     DailyAxisState = "unknown"
)

type DailyAxis struct {
	State      DailyAxisState `json:"state"`
	SourceRef  string         `json:"source_ref"`
	ReasonCode string         `json:"reason_code,omitempty"`
}

type DailyResourceRef struct {
	SourceID     string `json:"source_id,omitempty"`
	OwnerRef     string `json:"owner_ref"`
	ResourceType string `json:"resource_type"`
	Reference    string `json:"reference,omitempty"`
	NativeID     string `json:"native_id,omitempty"`
	DataScope    string `json:"data_scope,omitempty"`
	UserID       string `json:"-"`
	Revision     string `json:"revision,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	Filename     string `json:"filename,omitempty"`
	LinkRef      string `json:"link_ref,omitempty"`
}

type DailyOutcome string

const (
	DailyOutcomeSuccess DailyOutcome = "success"
	DailyOutcomeFailure DailyOutcome = "failure"
	DailyOutcomePartial DailyOutcome = "partial"
	DailyOutcomeRunning DailyOutcome = "running"
	DailyOutcomeMissed  DailyOutcome = "missed"
	DailyOutcomeUnknown DailyOutcome = "unknown"
)

type DailyEvidence struct {
	State         DailyValueState          `json:"state"`
	LastAttemptAt DailyValue[time.Time]    `json:"last_attempt_at"`
	LastSuccessAt DailyValue[time.Time]    `json:"last_success_at"`
	LastResult    DailyValue[DailyOutcome] `json:"last_result"`
	Refs          []DailyResourceRef       `json:"refs"`
}

type DailyOccurrenceState string

const (
	DailyOccurrenceScheduled   DailyOccurrenceState = "scheduled"
	DailyOccurrenceRunning     DailyOccurrenceState = "running"
	DailyOccurrenceSucceeded   DailyOccurrenceState = "succeeded"
	DailyOccurrenceFailed      DailyOccurrenceState = "failed"
	DailyOccurrencePartial     DailyOccurrenceState = "partial"
	DailyOccurrenceMissed      DailyOccurrenceState = "missed"
	DailyOccurrenceUnknown     DailyOccurrenceState = "unknown"
	DailyOccurrenceUnavailable DailyOccurrenceState = "unavailable"
)

type DailyOccurrence struct {
	OccurrenceID string               `json:"occurrence_id"`
	State        DailyOccurrenceState `json:"state"`
	// ScheduledAt is the original scheduled slot for this occurrence. Nil means
	// the owner did not report this field; consumers must not infer it.
	ScheduledAt   *DailyValue[time.Time]   `json:"scheduled_at,omitempty"`
	NextRunAt     DailyValue[time.Time]    `json:"next_run_at"`
	LastAttemptAt DailyValue[time.Time]    `json:"last_attempt_at"`
	LastSuccessAt DailyValue[time.Time]    `json:"last_success_at"`
	Result        DailyValue[DailyOutcome] `json:"result"`
	NativeRefs    []DailyResourceRef       `json:"native_refs"`
	// MeetingProgress is optional for non-meeting sources and legacy meeting
	// rows. Missing owner counters stay absent instead of becoming zeroes.
	MeetingProgress *DailyMeetingProgress `json:"meeting_progress,omitempty"`
}

// DailyProgressCount preserves both the observed count and its denominator as
// separate owner-reported values. A known total never makes an unknown count
// look like zero.
type DailyProgressCount struct {
	Current DailyValue[int] `json:"current"`
	Total   DailyValue[int] `json:"total"`
}

// DailyMeetingProgress is a read-only checkpoint projection. Concern counts
// describe concerns reported so far; they are not a forecast of concerns from
// participants who have not reported yet. Minutes contains a reference only.
type DailyMeetingProgress struct {
	Attendance         DailyProgressCount           `json:"attendance"`
	DeliveredReports   DailyProgressCount           `json:"delivered_reports"`
	AnsweredConcerns   DailyProgressCount           `json:"answered_concerns"`
	UnresolvedConcerns DailyValue[int]              `json:"unresolved_concerns"`
	Minutes            DailyValue[DailyResourceRef] `json:"minutes"`
}

type DailyIssueCode string

const (
	DailyIssueSourceUnavailable     DailyIssueCode = "source_unavailable"
	DailyIssueInvalidProjection     DailyIssueCode = "invalid_projection"
	DailyIssueObservationMissing    DailyIssueCode = "observation_missing"
	DailyIssueSourceReported        DailyIssueCode = "source_reported_partial"
	DailyIssueKindFilterUnavailable DailyIssueCode = "kind_filter_unavailable"
)

type DailyIssue struct {
	SourceID string         `json:"source_id"`
	OwnerRef string         `json:"owner_ref,omitempty"`
	Code     DailyIssueCode `json:"code"`
}

type DailyAvailability string

const (
	DailyAvailabilityAvailable   DailyAvailability = "available"
	DailyAvailabilityPartial     DailyAvailability = "partial"
	DailyAvailabilityUnavailable DailyAvailability = "unavailable"
)

type DailyDefinitionDescriptor struct {
	SourceID       string             `json:"source_id"`
	DefinitionID   string             `json:"definition_id"`
	KindID         string             `json:"kind_id"`
	Label          string             `json:"label"`
	OwnerRef       string             `json:"owner_ref"`
	ResourceType   string             `json:"resource_type"`
	DataScope      string             `json:"data_scope"`
	UserID         string             `json:"-"`
	Implementation DailyAxis          `json:"implementation"`
	SourceRefs     []DailyResourceRef `json:"source_refs"`
}

type DailySourceDescriptor struct {
	SourceID    string                      `json:"source_id"`
	Definitions []DailyDefinitionDescriptor `json:"definitions"`
}

// DailySource describes stable owner metadata separately from a fresh,
// scope-specific read. Snapshot must not run work, repair, retry, or recovery.
// Every non-reference value and every known count must be safe for the exact
// supplied scope. NativeRefCount counts typed DailyResourceRef values, while
// OccurrenceCount counts occurrence records. Known counts are in-scope totals
// before payload bounds, never payload lengths or cross-scope totals. When a
// payload is truncated, its total may be unknown or unsupported if the owner
// cannot provide it. A none count is valid only for an untruncated empty payload.
type DailySource interface {
	Descriptor() DailySourceDescriptor
	Snapshot(context.Context, tool.ToolExecutionScope) (DailySourceSnapshot, error)
}

type DailySourceSnapshot struct {
	ObservedAt   time.Time          `json:"observed_at"`
	Observations []DailyObservation `json:"observations"`
	Issues       []DailyIssue       `json:"issues"`
}

type DailyObservation struct {
	DefinitionID         string                `json:"definition_id"`
	Configured           DailyAxis             `json:"configured"`
	Effective            DailyAxis             `json:"effective"`
	Observed             DailyAxis             `json:"observed"`
	Timing               DailyTimingProjection `json:"timing"`
	NextRunAt            DailyValue[time.Time] `json:"next_run_at"`
	TargetCount          DailyValue[int]       `json:"target_count"`
	SourceRefs           []DailyResourceRef    `json:"source_refs"`
	NativeRefs           []DailyResourceRef    `json:"native_refs"`
	NativeRefCount       DailyValue[int]       `json:"native_ref_count"`
	NativeRefsTruncated  bool                  `json:"native_refs_truncated"`
	LatestEvidence       DailyEvidence         `json:"latest_evidence"`
	Occurrences          []DailyOccurrence     `json:"occurrences"`
	OccurrenceCount      DailyValue[int]       `json:"occurrence_count"`
	OccurrencesTruncated bool                  `json:"occurrences_truncated"`
	Issues               []DailyIssue          `json:"issues"`
}

type DailyDefinition struct {
	DailyDefinitionDescriptor
	Configured           DailyAxis             `json:"configured"`
	Effective            DailyAxis             `json:"effective"`
	Observed             DailyAxis             `json:"observed"`
	SourceObservedAt     DailyValue[time.Time] `json:"source_observed_at"`
	Timing               DailyTimingProjection `json:"timing"`
	NextRunAt            DailyValue[time.Time] `json:"next_run_at"`
	TargetCount          DailyValue[int]       `json:"target_count"`
	NativeRefs           []DailyResourceRef    `json:"native_refs"`
	NativeRefCount       DailyValue[int]       `json:"native_ref_count"`
	NativeRefsTruncated  bool                  `json:"native_refs_truncated"`
	LatestEvidence       DailyEvidence         `json:"latest_evidence"`
	Occurrences          []DailyOccurrence     `json:"occurrences"`
	OccurrenceCount      DailyValue[int]       `json:"occurrence_count"`
	OccurrencesTruncated bool                  `json:"occurrences_truncated"`
	Issues               []DailyIssue          `json:"issues"`
}

type DailyListResult struct {
	Definitions  []DailyDefinition `json:"definitions"`
	NextCursor   string            `json:"next_cursor,omitempty"`
	Partial      bool              `json:"partial"`
	Availability DailyAvailability `json:"availability"`
	Issues       []DailyIssue      `json:"issues"`
	ObservedAt   time.Time         `json:"observed_at"`
}
