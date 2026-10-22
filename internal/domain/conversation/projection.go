package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const MaxConversationProjectionThreadMessages = 12

var ErrInvalidConversationProjection = errors.New("invalid canonical conversation projection")

// ConversationProjectionSource is a public canonical message selected by the
// EventStore. It intentionally contains no private report or handoff fields.
type ConversationProjectionSource struct {
	SessionID  modulecore.SessionID `json:"session_id"`
	TurnID     modulecore.TurnID    `json:"turn_id"`
	MessageID  modulecore.MessageID `json:"message_id"`
	EventID    modulecore.EventID   `json:"event_id"`
	EventSeq   modulecore.EventSeq  `json:"event_seq"`
	ActorID    string               `json:"actor_id"`
	Speaker    string               `json:"speaker"`
	Text       string               `json:"text"`
	OccurredAt time.Time            `json:"occurred_at"`
	Visibility string               `json:"visibility"`
}

// CanonicalConversationProjection is the deterministic L1 assignment persisted
// by EventStore before the derived L1 apply begins.
type CanonicalConversationProjection struct {
	ConversationProjectionSource
	ThreadID      modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq     modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind    modulecore.ThreadKind `json:"thread_kind"`
	ContentSHA256 string                `json:"content_sha256"`
}

type ConversationProjectionThread struct {
	ID           modulecore.ThreadID
	Sequence     modulecore.ThreadSeq
	MessageCount int
	Closed       bool
}

type ConversationProjectionPackInput struct {
	Active       ConversationProjectionThread
	NextSequence modulecore.ThreadSeq
	NewThreadID  modulecore.ThreadID
	ThreadKind   modulecore.ThreadKind
	Sources      []ConversationProjectionSource
	InitialPair  bool
}

type ConversationProjectionPackResult struct {
	Items  []CanonicalConversationProjection
	Active ConversationProjectionThread
	Closed *ConversationProjectionThread
}

type ConversationProjectionItemReceipt struct {
	MessageID     modulecore.MessageID `json:"message_id"`
	EventID       modulecore.EventID   `json:"event_id"`
	EventSeq      modulecore.EventSeq  `json:"event_seq"`
	ContentSHA256 string               `json:"content_sha256"`
}

type ConversationProjectionApplyReceipt struct {
	SessionID modulecore.SessionID `json:"session_id"`
	// AppliedThrough is the greatest event sequence in this atomic unit. It is
	// not the EventStore's contiguous public-message watermark when units from
	// interleaved Turns have been applied out of event-sequence order.
	AppliedThrough modulecore.EventSeq                 `json:"applied_through"`
	Items          []ConversationProjectionItemReceipt `json:"items"`
}

// PackConversationProjectionUnit assigns one indivisible public output unit to
// the current thread, closing it first when the unit would exceed twelve
// messages. A two-message initial pair is never split, even at an odd boundary.
func PackConversationProjectionUnit(input ConversationProjectionPackInput) (ConversationProjectionPackResult, error) {
	if len(input.Sources) == 0 || len(input.Sources) > 2 || input.InitialPair != (len(input.Sources) == 2) {
		return ConversationProjectionPackResult{}, ErrInvalidConversationProjection
	}
	if input.ThreadKind.Validate() != nil {
		return ConversationProjectionPackResult{}, ErrInvalidConversationProjection
	}
	for _, source := range input.Sources {
		if err := validateConversationProjectionSource(source); err != nil {
			return ConversationProjectionPackResult{}, err
		}
	}
	for i := range input.Sources {
		if input.Sources[i].SessionID != input.Sources[0].SessionID || input.Sources[i].TurnID != input.Sources[0].TurnID ||
			(i > 0 && (input.Sources[i].EventSeq <= input.Sources[i-1].EventSeq || input.Sources[i].MessageID == input.Sources[i-1].MessageID || input.Sources[i].EventID == input.Sources[i-1].EventID)) {
			return ConversationProjectionPackResult{}, ErrInvalidConversationProjection
		}
	}
	if input.InitialPair && (input.Sources[0].Speaker != "user" || input.Sources[1].Speaker == "user") {
		return ConversationProjectionPackResult{}, ErrInvalidConversationProjection
	}

	result := ConversationProjectionPackResult{Items: make([]CanonicalConversationProjection, 0, len(input.Sources))}
	thread := input.Active
	if thread.ID != "" && (thread.ID.Validate() != nil || thread.Sequence.Validate() != nil || thread.MessageCount < 1 || thread.MessageCount > MaxConversationProjectionThreadMessages || thread.Closed) {
		return ConversationProjectionPackResult{}, ErrInvalidConversationProjection
	}
	if thread.ID != "" && thread.MessageCount+len(input.Sources) > MaxConversationProjectionThreadMessages {
		closed := thread
		closed.Closed = true
		result.Closed = &closed
		thread = ConversationProjectionThread{}
	}
	if thread.ID == "" {
		if input.NewThreadID.Validate() != nil || input.NextSequence.Validate() != nil {
			return ConversationProjectionPackResult{}, ErrInvalidConversationProjection
		}
		thread = ConversationProjectionThread{ID: input.NewThreadID, Sequence: input.NextSequence}
	}

	for i, source := range input.Sources {
		item := CanonicalConversationProjection{
			ConversationProjectionSource: source,
			ThreadID:                     thread.ID,
			ThreadSeq:                    modulecore.ThreadSeq(thread.MessageCount + i + 1),
			ThreadKind:                   input.ThreadKind,
		}
		hash, err := ConversationProjectionContentHash(item)
		if err != nil {
			return ConversationProjectionPackResult{}, err
		}
		item.ContentSHA256 = hash
		result.Items = append(result.Items, item)
	}
	thread.MessageCount += len(input.Sources)
	if thread.MessageCount == MaxConversationProjectionThreadMessages {
		closed := thread
		closed.Closed = true
		result.Closed = &closed
		result.Active = ConversationProjectionThread{}
		return result, nil
	}
	result.Active = thread
	return result, nil
}

func validateConversationProjectionSource(source ConversationProjectionSource) error {
	if source.SessionID.Validate() != nil || source.TurnID.Validate() != nil || source.MessageID.Validate() != nil || source.EventID.Validate() != nil || source.EventSeq.Validate() != nil ||
		strings.TrimSpace(source.ActorID) == "" || strings.TrimSpace(source.ActorID) != source.ActorID ||
		strings.TrimSpace(source.Speaker) == "" || strings.TrimSpace(source.Speaker) != source.Speaker || strings.TrimSpace(source.Text) == "" || source.Visibility != "public" || source.OccurredAt.IsZero() {
		return ErrInvalidConversationProjection
	}
	return nil
}

// ConversationProjectionContentHash binds canonical identity, public content,
// occurrence time, and the persisted thread assignment for exact retry checks.
func ConversationProjectionContentHash(item CanonicalConversationProjection) (string, error) {
	if err := validateConversationProjectionSource(item.ConversationProjectionSource); err != nil || item.ThreadID.Validate() != nil || item.ThreadSeq.Validate() != nil || item.ThreadKind.Validate() != nil {
		return "", ErrInvalidConversationProjection
	}
	canonical := struct {
		SessionID  modulecore.SessionID  `json:"session_id"`
		TurnID     modulecore.TurnID     `json:"turn_id"`
		MessageID  modulecore.MessageID  `json:"message_id"`
		EventID    modulecore.EventID    `json:"event_id"`
		EventSeq   modulecore.EventSeq   `json:"event_seq"`
		ActorID    string                `json:"actor_id"`
		Speaker    string                `json:"speaker"`
		Text       string                `json:"text"`
		OccurredAt string                `json:"occurred_at"`
		Visibility string                `json:"visibility"`
		ThreadID   modulecore.ThreadID   `json:"thread_id"`
		ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq"`
		ThreadKind modulecore.ThreadKind `json:"thread_kind"`
	}{
		SessionID: item.SessionID, TurnID: item.TurnID, MessageID: item.MessageID, EventID: item.EventID,
		EventSeq: item.EventSeq, ActorID: item.ActorID, Speaker: item.Speaker, Text: item.Text,
		OccurredAt: item.OccurredAt.UTC().Format(time.RFC3339Nano), Visibility: item.Visibility,
		ThreadID: item.ThreadID, ThreadSeq: item.ThreadSeq, ThreadKind: item.ThreadKind,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal canonical conversation projection hash: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
