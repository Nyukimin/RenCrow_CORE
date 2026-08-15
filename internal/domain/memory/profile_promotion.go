package memory

import "time"

const (
	ProfilePromotionPending   = "pending"
	ProfilePromotionRunning   = "running"
	ProfilePromotionRetryWait = "retry_wait"
	ProfilePromotionCompleted = "completed"
	ProfilePromotionFailed    = "failed"
)

type ProfilePromotionJob struct {
	EvidenceEventID string    `json:"evidence_event_id"`
	SessionID       string    `json:"session_id"`
	ThreadID        int64     `json:"thread_id"`
	State           string    `json:"state"`
	AttemptCount    int       `json:"attempt_count"`
	LeaseToken      string    `json:"-"`
	LeaseExpiresAt  time.Time `json:"lease_expires_at,omitempty"`
	NextAttemptAt   time.Time `json:"next_attempt_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// L1DBPoolStats is the cumulative sql.DB pool snapshot exposed with
// ProfilePromotion diagnostics. Wait counters are cumulative for the life of
// the database handle, as reported by database/sql.DB.Stats.
type L1DBPoolStats struct {
	Max                int   `json:"max"`
	Open               int   `json:"open"`
	InUse              int   `json:"in_use"`
	Idle               int   `json:"idle"`
	PoolWaitCount      int64 `json:"pool_wait_count"`
	PoolWaitDurationMS int64 `json:"pool_wait_duration_ms"`
}

// ProfilePromotionDiagnostics describes all persisted promotion jobs, while
// the handler may still return a limited page of job details.
type ProfilePromotionDiagnostics struct {
	StateCounts                map[string]int `json:"state_counts"`
	FailedCount                int            `json:"failed_count"`
	RetryableFailedCount       int            `json:"retryable_failed_count"`
	MissingEvidenceFailedCount int            `json:"missing_evidence_failed_count"`
	DBPoolStats                L1DBPoolStats  `json:"db_pool_stats"`
}

// ProfilePromotionRetryResult is the result of an explicit retry request.
// Missing-evidence rows are reported but remain terminal and untouched.
type ProfilePromotionRetryResult struct {
	RequeuedCount        int `json:"requeued_count"`
	MissingEvidenceCount int `json:"missing_evidence_count"`
}

type ProfilePromotionMessage struct {
	EventID   string
	SessionID string
	ThreadID  int64
	Text      string
	CreatedAt time.Time
}

type ProfilePromotionBatch struct {
	LeaseToken string
	SessionID  string
	ThreadID   int64
	Messages   []ProfilePromotionMessage
}

type ProfileCandidate struct {
	Type        string
	Statement   string
	Confidence  float64
	Sensitivity string
	Scope       string
}
