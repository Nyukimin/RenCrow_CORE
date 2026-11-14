package agentroutine

import "time"

const JST = "Asia/Tokyo"

type ActivityKind string

const (
	ActivityWait         ActivityKind = "wait"
	ActivitySleep        ActivityKind = "sleep"
	ActivityWake         ActivityKind = "wake"
	ActivityMorningChat  ActivityKind = "morning_chat"
	ActivityHobby        ActivityKind = "hobby"
	ActivityRelax        ActivityKind = "relax"
	ActivityDiary        ActivityKind = "diary"
	ActivityDaytime      ActivityKind = "daytime_delegate"
	ActivityWork         ActivityKind = "work"
	ActivityConversation ActivityKind = "conversation"
)

type Availability uint8

const (
	AvailabilityUnknown Availability = iota
	AvailabilityUnavailable
	AvailabilityAvailable
)

// ActorState contains caller-owned identity and confirmed WORLD facts used by policy.
// ActualWakeAt must be zero unless the adapter has a confirmed wake observation.
type ActorState struct {
	ID              string
	HobbyConfigured bool
	ActualWakeAt    time.Time
}

// Presence is an adapter snapshot. Unknown is intentionally distinct from unavailable.
type Presence struct {
	SharedRoom       Availability
	OwnRoom          Availability
	ChatParticipants []string
}

type RelaxRoomScope uint8

const (
	RelaxRoomUnknown RelaxRoomScope = iota
	RelaxRoomPrivate
	RelaxRoomShared
)

// State is the actor's bounded, durable routine checkpoint. LogicalSleepUntil is a
// CORE eligibility window; it is not evidence that WORLD moved or put the actor to sleep.
type State struct {
	ActorID              string    `json:"actor_id"`
	Revision             int64     `json:"revision"`
	ActivatedOn          string    `json:"activated_on,omitempty"`
	ScheduleDate         string    `json:"schedule_date,omitempty"`
	PlannedSleep         time.Time `json:"planned_sleep,omitempty"`
	PlannedWake          time.Time `json:"planned_wake,omitempty"`
	LogicalSleepUntil    time.Time `json:"logical_sleep_until,omitempty"`
	ChoiceIndex          uint64    `json:"choice_index"`
	NextDiaryDate        string    `json:"next_diary_date,omitempty"`
	CooldownUntil        time.Time `json:"cooldown_until,omitempty"`
	Pending              *Intent   `json:"pending,omitempty"`
	PausedDiary          *Intent   `json:"paused_diary,omitempty"`
	LastFinishedIntentID string    `json:"last_finished_intent_id,omitempty"`
}

type Plan struct {
	Kind         ActivityKind
	RelaxRoom    RelaxRoomScope
	Date         string
	ScheduleDate string
	PlannedSleep time.Time
	PlannedWake  time.Time
	Participants []string
	CountChoice  bool
	Resuming     bool
	IntentID     string
	HobbyIndex   uint64
	LogicalSleep bool
	Reason       string
}

// Intent is one actor's resumable routine step, linked to a TaskID owned by the caller.
type Intent struct {
	ID              string       `json:"id"`
	ActorID         string       `json:"actor_id"`
	Date            string       `json:"date"`
	TaskID          string       `json:"task_id,omitempty"`
	Kind            ActivityKind `json:"kind"`
	Label           string       `json:"label,omitempty"`
	Participants    []string     `json:"participants,omitempty"`
	ChoiceEligible  bool         `json:"choice_eligible,omitempty"`
	ChoiceCommitted bool         `json:"choice_committed,omitempty"`
	Step            string       `json:"step,omitempty"`
	Output          string       `json:"output,omitempty"`
	Digest          string       `json:"digest,omitempty"`
	Attempt         int          `json:"attempt,omitempty"`
	RetryAfter      time.Time    `json:"retry_after,omitempty"`
	CreatedAt       time.Time    `json:"created_at,omitempty"`
}

// Activity is a caller-supplied ledger fact. A zero At means the source supplied no
// confirmed timestamp; persistence never substitutes the current time.
type Activity struct {
	ActorID   string       `json:"actor_id"`
	Date      string       `json:"date"`
	Key       string       `json:"key"`
	Kind      ActivityKind `json:"kind"`
	At        time.Time    `json:"at,omitempty"`
	Source    string       `json:"source"`
	SourceID  string       `json:"source_id,omitempty"`
	Confirmed bool         `json:"confirmed"`
	Summary   string       `json:"summary,omitempty"`
	Reference string       `json:"reference,omitempty"`
}

// Diary stores one revisioned draft for one actor and JST calendar date.
type Diary struct {
	ActorID         string    `json:"actor_id"`
	Date            string    `json:"date"`
	Body            string    `json:"body"`
	Reflection      string    `json:"reflection"`
	Revision        int64     `json:"revision"`
	PostingKey      string    `json:"posting_key"`
	Receipt         string    `json:"receipt,omitempty"`
	ReceiptRevision string    `json:"receipt_revision,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}
