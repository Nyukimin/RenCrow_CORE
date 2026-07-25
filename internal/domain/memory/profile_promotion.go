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
