package boardcommunication

import (
	"time"
)

// PageRequest selects one bounded BOARD page.
type PageRequest struct {
	Limit  int
	Cursor string
}

// ThreadPage is one canonical BOARD thread response page.
type ThreadPage struct {
	Thread     BoardThread `json:"thread"`
	NextCursor string      `json:"next_cursor"`
}

// ThreadListPage contains one canonical BOARD thread-summary page.
type ThreadListPage struct {
	Threads    []ThreadSummary `json:"threads"`
	NextCursor string          `json:"next_cursor"`
}

// ThreadSummary is one canonical BOARD thread-list row.
type ThreadSummary struct {
	ThreadID  string `json:"thread_id"`
	ChannelID string `json:"channel_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

// BoardThread preserves the BOARD Topic JSON shape and string IDs.
type BoardThread struct {
	ID           string         `json:"id"`
	Space        string         `json:"space"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Status       string         `json:"status"`
	Decision     string         `json:"decision"`
	Participants []string       `json:"participants"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	Messages     []BoardMessage `json:"messages"`
}

// BoardMessage preserves the BOARD Message JSON shape and string IDs.
type BoardMessage struct {
	ID          string   `json:"id"`
	Seq         int64    `json:"seq"`
	By          string   `json:"by"`
	At          string   `json:"at"`
	Body        string   `json:"body"`
	Kind        string   `json:"kind,omitempty"`
	Mentions    []string `json:"mentions"`
	Recipients  []string `json:"recipients"`
	ChannelID   string   `json:"channel_id,omitempty"`
	ThreadID    string   `json:"thread_id,omitempty"`
	ReplyTo     string   `json:"reply_to,omitempty"`
	CausationID string   `json:"causation_id,omitempty"`
	HopCount    int      `json:"hop_count,omitempty"`
}

// InboxPage contains the canonical flat inbox rows and the continuation cursor.
type InboxPage struct {
	Items      []InboxItem `json:"items"`
	NextCursor string      `json:"next_cursor"`
}

// InboxItem is the exact flat BOARD inbox projection. AckReceipt is private
// protocol material for explicit caller-owned ACK or claim operations.
type InboxItem struct {
	ItemID      string    `json:"item_id"`
	MessageID   string    `json:"message_id"`
	ChannelID   string    `json:"channel_id"`
	ThreadID    string    `json:"thread_id"`
	SenderID    string    `json:"sender_id"`
	CreatedAt   time.Time `json:"created_at"`
	Body        string    `json:"body"`
	Kind        string    `json:"kind,omitempty"`
	ReplyTo     string    `json:"reply_to,omitempty"`
	CausationID string    `json:"causation_id,omitempty"`
	HopCount    int       `json:"hop_count,omitempty"`
	Mentions    []string  `json:"mentions"`
	Recipients  []string  `json:"recipients"`
	AckReceipt  string    `json:"ack_receipt"`
}

// BoardEvent is one public BOARD SSE hint. Its sequence is a hint cursor and
// does not imply inbox acknowledgement or complete delivery.
type BoardEvent struct {
	Seq        int64    `json:"seq"`
	Type       string   `json:"type"`
	TopicID    string   `json:"topic_id,omitempty"`
	SpaceID    string   `json:"space_id,omitempty"`
	MessageID  string   `json:"message_id,omitempty"`
	Sender     string   `json:"sender,omitempty"`
	Mentions   []string `json:"mentions"`
	Recipients []string `json:"recipients"`
	Time       string   `json:"time"`
}

// InboxReceipt contains the caller-owned receipt for explicit ACK or claim.
type InboxReceipt struct {
	ItemID     string `json:"item_id"`
	MessageID  string `json:"message_id"`
	AckReceipt string `json:"ack_receipt"`
}

// ClaimRequest is one explicit caller-owned processing claim attempt.
type ClaimRequest struct {
	Receipt      string `json:"receipt"`
	ClaimID      string `json:"claim_id,omitempty"`
	LeaseSeconds int    `json:"lease_seconds,omitempty"`
}

// ClaimResult is the canonical result of one processing claim attempt.
// ClaimReceipt is secret protocol material for a later caller-owned send.
type ClaimResult struct {
	MessageID    string     `json:"message_id"`
	ClaimID      string     `json:"claim_id"`
	ClaimReceipt string     `json:"claim_receipt,omitempty"`
	Status       string     `json:"status"`
	LeaseUntil   *time.Time `json:"lease_until,omitempty"`
	CanProcess   bool       `json:"can_process,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// BoardMessageStatus preserves BOARD recipient status fields.
type BoardMessageStatus struct {
	MessageID               string                            `json:"message_id"`
	PrincipalID             string                            `json:"principal_id"`
	StoredAt                time.Time                         `json:"stored_at"`
	NotificationAttemptedAt *time.Time                        `json:"notification_attempted_at"`
	NotificationAcceptedAt  *time.Time                        `json:"notification_accepted_at"`
	BodyReadAt              *time.Time                        `json:"body_read_at"`
	ProcessingStartedAt     *time.Time                        `json:"processing_started_at"`
	ReplyObservedAt         *time.Time                        `json:"reply_observed_at"`
	NotificationRecovery    *BoardRecoveryStatus              `json:"notification_recovery,omitempty"`
	NotificationDeliveries  []BoardNotificationDeliveryStatus `json:"notification_deliveries,omitempty"`
}

// BoardNotificationDeliveryStatus preserves the safe external delivery observation fields.
type BoardNotificationDeliveryStatus struct {
	DeliveryID     string     `json:"delivery_id"`
	EventID        string     `json:"event_id"`
	SubscriptionID string     `json:"subscription_id"`
	Provider       string     `json:"provider"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	AttemptedAt    *time.Time `json:"attempted_at,omitempty"`
	AcceptedAt     *time.Time `json:"accepted_at,omitempty"`
	LastStatusCode int        `json:"last_status_code,omitempty"`
	LastResultAt   *time.Time `json:"last_result_at,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	OutcomeUnknown bool       `json:"outcome_unknown"`
}

// BoardRecoveryStatus preserves the optional accepted-delivery recovery view.
type BoardRecoveryStatus struct {
	Status          string     `json:"status"`
	Reason          string     `json:"reason,omitempty"`
	FirstAcceptedAt *time.Time `json:"first_accepted_at,omitempty"`
	DeadlineAt      *time.Time `json:"deadline_at,omitempty"`
	Attempts        int        `json:"attempts"`
	MaxAttempts     int        `json:"max_attempts"`
	NextAttemptAt   *time.Time `json:"next_attempt_at,omitempty"`
	LastResult      string     `json:"last_result,omitempty"`
}
