package worldprojection

import (
	"context"
	"errors"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

const (
	OutboxPending    = "pending"
	OutboxInFlight   = "in_flight"
	OutboxSent       = "sent"
	OutboxFailed     = "failed"
	OutboxSuperseded = "superseded"
)

var (
	ErrRevisionConflict             = errors.New("world projection outbox: revision conflicts with an existing payload")
	ErrStaleRevision                = errors.New("world projection outbox: source revision is older than the current head")
	ErrEpochChangeRequiresReconcile = errors.New("world projection outbox: epoch change requires an authenticated reconcile")
	ErrOutboxRecordNotFound         = errors.New("world projection outbox: record not found")
)

// OutboxRecord is one durable projection delivery. Envelope is the exact wire
// payload; Target and Kind are stored separately so a retry cannot be posted to
// a different Actor or projection type.
type OutboxRecord struct {
	ID             string
	Target         domainprojection.Target
	Kind           string
	Envelope       []byte
	Digest         string
	SourceEpoch    string
	SourceRevision uint64
	Reconcile      bool
	Status         string
	Attempts       int
	NextAttemptAt  time.Time
	LastError      string
	AckStatus      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	SentAt         time.Time
}

// Outbox is CORE's durable delivery boundary. Enqueue is idempotent for the
// same destination/kind/epoch/revision and digest; it rejects a conflicting
// digest and a stale revision. An epoch change is accepted only with
// Reconcile=true, which supersedes unfinished records from the old epoch.
type Outbox interface {
	Enqueue(context.Context, OutboxRecord, time.Time) error
	Claim(context.Context, time.Time, int) ([]OutboxRecord, error)
	Complete(context.Context, string, PublishResult, time.Time) error
	Fail(context.Context, string, time.Time, bool, string, time.Time) error
}
