package memory

import (
	"errors"
	"time"
)

const UserMemoryOwnerPolicyRevision = "memory-owner/v1"

const (
	UserMemoryOwnerOperationList      = "list"
	UserMemoryOwnerOperationShow      = "show"
	UserMemoryOwnerOperationPropose   = "propose"
	UserMemoryOwnerOperationConfirm   = "confirm"
	UserMemoryOwnerOperationPin       = "pin"
	UserMemoryOwnerOperationForget    = "forget"
	UserMemoryOwnerOperationSupersede = "supersede"
)

var (
	ErrUserMemoryOwnerInvalid   = errors.New("invalid user memory owner request")
	ErrUserMemoryOwnerNotFound  = errors.New("user memory owner item not found")
	ErrUserMemoryOwnerForbidden = errors.New("user memory owner item is not owned by the authenticated user")
	ErrUserMemoryOwnerConflict  = errors.New("user memory owner request conflicts with existing state")
)

// UserMemoryOwnerView is the bounded projection returned by the CMD owner API.
// Storage identity (user_id, namespace, source and metadata) is intentionally
// not part of this type.
type UserMemoryOwnerView struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	Statement        string    `json:"statement"`
	EvidenceEventIDs []string  `json:"evidence_event_ids,omitempty"`
	Confidence       float64   `json:"confidence"`
	Sensitivity      string    `json:"sensitivity"`
	State            string    `json:"state"`
	PersonaScope     string    `json:"persona_scope"`
	Active           bool      `json:"active"`
	SupersededBy     string    `json:"superseded_by,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// UserMemoryOwnerReceipt closes one owner operation at the CORE boundary.
type UserMemoryOwnerReceipt struct {
	RequestID        string    `json:"request_id"`
	Operation        string    `json:"operation"`
	Status           string    `json:"status"`
	OwnerRoute       string    `json:"owner_route"`
	PolicyRevision   string    `json:"policy_revision"`
	IdempotencyKey   string    `json:"idempotency_key"`
	IdempotentReplay bool      `json:"idempotent_replay"`
	InputCount       int       `json:"input_count"`
	OutputCount      int       `json:"output_count"`
	Warnings         []string  `json:"warnings"`
	AuditReference   string    `json:"audit_reference"`
	CompletedAt      time.Time `json:"completed_at"`
}

type UserMemoryOwnerResult struct {
	Item    UserMemoryOwnerView    `json:"item"`
	Receipt UserMemoryOwnerReceipt `json:"receipt"`
}

func UserMemoryOwnerViewFromMemory(item UserMemory) UserMemoryOwnerView {
	return UserMemoryOwnerView{
		ID:               item.ID,
		Type:             item.Type,
		Statement:        item.Statement,
		EvidenceEventIDs: append([]string(nil), item.EvidenceEventIDs...),
		Confidence:       item.Confidence,
		Sensitivity:      item.Sensitivity,
		State:            item.State,
		PersonaScope:     item.Scope,
		Active:           item.Active,
		SupersededBy:     item.SupersededBy,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}
