package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	AcceptedOPSInputCanonicalVersion  = "rencrow.accepted_ops_input.v1"
	AcceptedOPSInputMaxRequestIDBytes = 128
	AcceptedOPSInputMaxOwnerIDBytes   = 255
	AcceptedOPSInputMaxMessageBytes   = 32 * 1024
)

type AcceptedOPSInputOrigin string

const (
	AcceptedOPSInputOriginAutomation AcceptedOPSInputOrigin = "automation"
	AcceptedOPSInputOriginHuman      AcceptedOPSInputOrigin = "human"
)

type AcceptedOPSInputErrorCode string

const (
	AcceptedOPSInputErrorInvalid     AcceptedOPSInputErrorCode = "invalid"
	AcceptedOPSInputErrorForbidden   AcceptedOPSInputErrorCode = "forbidden"
	AcceptedOPSInputErrorConflict    AcceptedOPSInputErrorCode = "conflict"
	AcceptedOPSInputErrorUnavailable AcceptedOPSInputErrorCode = "unavailable"
)

type AcceptedOPSInputError struct {
	Code AcceptedOPSInputErrorCode
}

func (e *AcceptedOPSInputError) Error() string {
	if e == nil {
		return "accepted OPS input error"
	}
	return "accepted OPS input " + string(e.Code)
}

func (e *AcceptedOPSInputError) Is(target error) bool {
	other, ok := target.(*AcceptedOPSInputError)
	return ok && e != nil && other != nil && e.Code == other.Code
}

var (
	ErrAcceptedOPSInputInvalid     = &AcceptedOPSInputError{Code: AcceptedOPSInputErrorInvalid}
	ErrAcceptedOPSInputForbidden   = &AcceptedOPSInputError{Code: AcceptedOPSInputErrorForbidden}
	ErrAcceptedOPSInputConflict    = &AcceptedOPSInputError{Code: AcceptedOPSInputErrorConflict}
	ErrAcceptedOPSInputUnavailable = &AcceptedOPSInputError{Code: AcceptedOPSInputErrorUnavailable}
)

// AcceptedOPSInputRequest contains authenticated identity and the exact user
// bytes accepted for a Harness operation. RawMessage is never copied into the
// acceptance receipt.
type AcceptedOPSInputRequest struct {
	RequestID      string
	OwnerID        string
	ActorID        string
	SessionID      modulecore.SessionID
	FirstThreadID  modulecore.ThreadID
	TaskID         modulecore.TaskID
	TurnID         modulecore.TurnID
	TraceID        modulecore.TraceID
	UserMessageID  modulecore.MessageID
	AgentMessageID modulecore.MessageID
	RawMessage     string `json:"-"`
	DeclaredOrigin AcceptedOPSInputOrigin
}

type AcceptedOPSInputReceipt struct {
	AcceptanceSequence int64                  `json:"acceptance_sequence"`
	RequestID          string                 `json:"request_id"`
	OwnerID            string                 `json:"owner_id"`
	ActorID            string                 `json:"actor_id"`
	SessionID          modulecore.SessionID   `json:"session_id"`
	ThreadID           modulecore.ThreadID    `json:"thread_id"`
	ThreadSeq          modulecore.ThreadSeq   `json:"thread_seq"`
	ThreadKind         modulecore.ThreadKind  `json:"thread_kind"`
	TaskID             modulecore.TaskID      `json:"task_id"`
	TurnID             modulecore.TurnID      `json:"turn_id"`
	TraceID            modulecore.TraceID     `json:"trace_id"`
	UserMessageID      modulecore.MessageID   `json:"user_message_id"`
	AgentMessageID     modulecore.MessageID   `json:"agent_message_id"`
	DeclaredOrigin     AcceptedOPSInputOrigin `json:"declared_origin"`
	PayloadSHA256      string                 `json:"payload_sha256"`
	RawRecordID        string                 `json:"raw_record_id"`
	ManifestID         string                 `json:"manifest_id"`
	RawSHA256          string                 `json:"raw_sha256"`
	ManifestSHA256     string                 `json:"manifest_sha256"`
	AcceptedAt         time.Time              `json:"accepted_at"`
	IdempotentReplay   bool                   `json:"idempotent_replay,omitempty"`
}

type AcceptedOPSInput struct {
	Receipt    AcceptedOPSInputReceipt `json:"receipt"`
	RawMessage string                  `json:"-"`
}

type AcceptedOPSInputReadRequest struct {
	RequestID string
	OwnerID   string
}

// AcceptedOPSInputStore is the narrow Conversation-owner boundary for
// accepting and exact-reading one private OPS input.
type AcceptedOPSInputStore interface {
	AcceptOPSInput(context.Context, AcceptedOPSInputRequest) (AcceptedOPSInputReceipt, error)
	ReadAcceptedOPSInput(context.Context, AcceptedOPSInputReadRequest) (AcceptedOPSInput, error)
}

func NormalizeAcceptedOPSInputRequest(request AcceptedOPSInputRequest) (AcceptedOPSInputRequest, error) {
	if !validAcceptedOPSIdentity(request.RequestID, AcceptedOPSInputMaxRequestIDBytes) ||
		!validAcceptedOPSIdentity(request.OwnerID, AcceptedOPSInputMaxOwnerIDBytes) ||
		!validAcceptedOPSIdentity(request.ActorID, AcceptedOPSInputMaxOwnerIDBytes) {
		return AcceptedOPSInputRequest{}, ErrAcceptedOPSInputInvalid
	}
	if request.SessionID.Validate() != nil ||
		request.FirstThreadID.Validate() != nil ||
		request.TaskID.Validate() != nil ||
		request.TurnID.Validate() != nil ||
		request.TraceID.Validate() != nil ||
		request.UserMessageID.Validate() != nil ||
		request.AgentMessageID.Validate() != nil ||
		request.UserMessageID == request.AgentMessageID {
		return AcceptedOPSInputRequest{}, ErrAcceptedOPSInputInvalid
	}
	if request.RawMessage == "" || len([]byte(request.RawMessage)) > AcceptedOPSInputMaxMessageBytes ||
		!utf8.ValidString(request.RawMessage) || strings.IndexByte(request.RawMessage, 0) >= 0 {
		return AcceptedOPSInputRequest{}, ErrAcceptedOPSInputInvalid
	}
	if request.DeclaredOrigin == "" {
		request.DeclaredOrigin = AcceptedOPSInputOriginAutomation
	}
	if request.DeclaredOrigin != AcceptedOPSInputOriginAutomation && request.DeclaredOrigin != AcceptedOPSInputOriginHuman {
		return AcceptedOPSInputRequest{}, ErrAcceptedOPSInputInvalid
	}
	return request, nil
}

func NormalizeAcceptedOPSInputReadRequest(request AcceptedOPSInputReadRequest) (AcceptedOPSInputReadRequest, error) {
	if !validAcceptedOPSIdentity(request.RequestID, AcceptedOPSInputMaxRequestIDBytes) ||
		!validAcceptedOPSIdentity(request.OwnerID, AcceptedOPSInputMaxOwnerIDBytes) {
		return AcceptedOPSInputReadRequest{}, ErrAcceptedOPSInputInvalid
	}
	return request, nil
}

// CanonicalAcceptedOPSInputPayload excludes all allocated identities and
// timestamps. The owner/request key selects the receipt; only exact message
// bytes and declared origin determine whether a retry is the same input.
func CanonicalAcceptedOPSInputPayload(request AcceptedOPSInputRequest) ([]byte, error) {
	normalized, err := NormalizeAcceptedOPSInputRequest(request)
	if err != nil {
		return nil, err
	}
	// Automation keeps the established v1 bytes. Human adds its declaration so
	// changing only the origin under an existing request key is a conflict.
	declaredOrigin := AcceptedOPSInputOrigin("")
	if normalized.DeclaredOrigin == AcceptedOPSInputOriginHuman {
		declaredOrigin = AcceptedOPSInputOriginHuman
	}
	return json.Marshal(struct {
		Version        string                 `json:"version"`
		DeclaredOrigin AcceptedOPSInputOrigin `json:"declared_origin,omitempty"`
		RawMessage     string                 `json:"raw_message"`
	}{
		Version:        AcceptedOPSInputCanonicalVersion,
		DeclaredOrigin: declaredOrigin,
		RawMessage:     normalized.RawMessage,
	})
}

func AcceptedOPSInputPayloadSHA256(request AcceptedOPSInputRequest) (string, error) {
	payload, err := CanonicalAcceptedOPSInputPayload(request)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

func (receipt AcceptedOPSInputReceipt) Validate() error {
	if receipt.AcceptanceSequence <= 0 ||
		!validAcceptedOPSIdentity(receipt.RequestID, AcceptedOPSInputMaxRequestIDBytes) ||
		!validAcceptedOPSIdentity(receipt.OwnerID, AcceptedOPSInputMaxOwnerIDBytes) ||
		receipt.ActorID != receipt.OwnerID ||
		receipt.SessionID.Validate() != nil ||
		receipt.ThreadID.Validate() != nil ||
		receipt.ThreadSeq != modulecore.ThreadSeq(1) ||
		receipt.ThreadKind != modulecore.ThreadKindUserConversation ||
		receipt.TaskID.Validate() != nil ||
		receipt.TurnID.Validate() != nil ||
		receipt.TraceID.Validate() != nil ||
		receipt.UserMessageID.Validate() != nil ||
		receipt.AgentMessageID.Validate() != nil ||
		receipt.UserMessageID == receipt.AgentMessageID ||
		(receipt.DeclaredOrigin != AcceptedOPSInputOriginAutomation && receipt.DeclaredOrigin != AcceptedOPSInputOriginHuman) ||
		!validSHA256(receipt.PayloadSHA256) ||
		!validAcceptedOPSIdentity(receipt.RawRecordID, 256) ||
		!validAcceptedOPSIdentity(receipt.ManifestID, 256) ||
		!validSHA256(receipt.RawSHA256) ||
		!validSHA256(receipt.ManifestSHA256) ||
		receipt.AcceptedAt.IsZero() {
		return ErrAcceptedOPSInputInvalid
	}
	return nil
}

func validAcceptedOPSIdentity(value string, maxBytes int) bool {
	return value != "" && len([]byte(value)) <= maxBytes && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func IsAcceptedOPSInputError(err error, code AcceptedOPSInputErrorCode) bool {
	var typed *AcceptedOPSInputError
	return errors.As(err, &typed) && typed != nil && typed.Code == code
}
