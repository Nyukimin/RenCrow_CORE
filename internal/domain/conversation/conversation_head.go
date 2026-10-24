package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const conversationHeadResetCanonicalVersion = "rencrow.conversation_head_reset.v1"

// ConversationHeadMessage is one retained message in an active head snapshot.
// The reset operation never removes messages from the prior thread.
type ConversationHeadMessage struct {
	ID        modulecore.MessageID   `json:"message_id"`
	Speaker   Speaker                `json:"speaker"`
	Message   string                 `json:"message"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// ConversationHeadSnapshot is the owner-scoped view of the durable active
// thread. OwnerID is supplied by the caller after session ownership validation;
// L1 stores thread state by SessionID and does not own session authorization.
type ConversationHeadSnapshot struct {
	SessionID    string                         `json:"session_id"`
	OwnerID      string                         `json:"owner_id"`
	Thread       ConversationTurnThreadIdentity `json:"thread"`
	Domain       string                         `json:"domain"`
	MessageCount int64                          `json:"message_count"`
	Messages     []ConversationHeadMessage      `json:"messages"`
	UpdatedAt    time.Time                      `json:"updated_at"`
}

// ConversationHeadResetRequest changes the active head only if its current
// thread identity still matches the expected ID and sequence. OperationID is
// the durable idempotency key for a caller retry.
type ConversationHeadResetRequest struct {
	OperationID       string               `json:"operation_id"`
	SessionID         string               `json:"session_id"`
	OwnerID           string               `json:"owner_id"`
	ExpectedThreadID  modulecore.ThreadID  `json:"expected_thread_id"`
	ExpectedThreadSeq modulecore.ThreadSeq `json:"expected_thread_seq"`
}

// ConversationHeadResetReceipt records the old and new active thread identity
// committed by one reset operation.
type ConversationHeadResetReceipt struct {
	OperationID      string                         `json:"operation_id"`
	SessionID        string                         `json:"session_id"`
	OwnerID          string                         `json:"owner_id"`
	PreviousThread   ConversationTurnThreadIdentity `json:"previous_thread"`
	ActiveThread     ConversationTurnThreadIdentity `json:"active_thread"`
	Domain           string                         `json:"domain"`
	MessageCount     int64                          `json:"message_count"`
	PayloadSHA256    string                         `json:"payload_sha256"`
	ResetAt          time.Time                      `json:"reset_at"`
	IdempotentReplay bool                           `json:"idempotent_replay,omitempty"`
}

// NormalizeConversationHeadResetRequest validates canonical identity fields
// before hashing or changing durable state.
func NormalizeConversationHeadResetRequest(request ConversationHeadResetRequest) (ConversationHeadResetRequest, error) {
	if !validConversationHeadOperationID(request.OperationID) ||
		request.SessionID == "" || strings.TrimSpace(request.SessionID) != request.SessionID || modulecore.SessionID(request.SessionID).Validate() != nil ||
		ValidateConversationHeadOwnerID(request.OwnerID) != nil ||
		request.ExpectedThreadID.Validate() != nil || request.ExpectedThreadSeq.Validate() != nil {
		return ConversationHeadResetRequest{}, ErrConversationTurnInvalid
	}
	return request, nil
}

// ValidateConversationHeadOwnerID checks the already-authenticated session
// owner identity carried by conversation head operations.
func ValidateConversationHeadOwnerID(value string) error {
	if !validConversationHeadOwnerID(value) {
		return ErrConversationTurnInvalid
	}
	return nil
}

// ConversationHeadResetPayloadSHA256 returns the canonical hash used by L1 to
// reject reuse of an operation ID for a different owner, session, or head.
func ConversationHeadResetPayloadSHA256(request ConversationHeadResetRequest) (string, error) {
	normalized, err := NormalizeConversationHeadResetRequest(request)
	if err != nil {
		return "", err
	}
	canonical := struct {
		Version           string               `json:"version"`
		OperationID       string               `json:"operation_id"`
		SessionID         string               `json:"session_id"`
		OwnerID           string               `json:"owner_id"`
		ExpectedThreadID  modulecore.ThreadID  `json:"expected_thread_id"`
		ExpectedThreadSeq modulecore.ThreadSeq `json:"expected_thread_seq"`
	}{
		Version: conversationHeadResetCanonicalVersion, OperationID: normalized.OperationID,
		SessionID: normalized.SessionID, OwnerID: normalized.OwnerID,
		ExpectedThreadID: normalized.ExpectedThreadID, ExpectedThreadSeq: normalized.ExpectedThreadSeq,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

func validConversationHeadOperationID(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func validConversationHeadOwnerID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && boundedText(value, ConversationTurnMaxIDRunes, "owner_id") == nil
}
