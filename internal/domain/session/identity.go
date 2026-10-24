package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const logicalDateLayout = "2006-01-02"

func ValidateLogicalDate(value string) error {
	parsed, err := time.Parse(logicalDateLayout, value)
	if err != nil || parsed.Format(logicalDateLayout) != value {
		return fmt.Errorf("logical_date must use YYYY-MM-DD")
	}
	return nil
}

// NewPublicationIncarnation creates an opaque 128-bit generation token for a
// canonical Session publication lifecycle.
func NewPublicationIncarnation() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate publication incarnation: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

func ValidatePublicationIncarnation(value string) error {
	if len(value) != 32 || strings.ToLower(value) != value {
		return fmt.Errorf("publication_incarnation must be 32 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("publication_incarnation must be 32 lowercase hexadecimal characters")
	}
	return nil
}

// NewCanonicalSession constructs a Session whose opaque identity and lookup
// attributes are separate canonical values.
func NewCanonicalSession(id modulecore.SessionID, logicalDate string, address conversation.ChannelAddress, createdAt time.Time) (*Session, error) {
	incarnation, err := NewPublicationIncarnation()
	if err != nil {
		return nil, err
	}
	return ReconstructCanonicalSessionWithPublicationIncarnation(id, logicalDate, address, nil, nil, createdAt, createdAt, incarnation)
}

// ReconstructCanonicalSession restores a persisted canonical session without
// changing its timestamps while history and memory are hydrated.
func ReconstructCanonicalSession(id modulecore.SessionID, logicalDate string, address conversation.ChannelAddress, history []conversation.TurnInput, memory map[string]interface{}, createdAt, updatedAt time.Time) (*Session, error) {
	incarnation, err := NewPublicationIncarnation()
	if err != nil {
		return nil, err
	}
	return ReconstructCanonicalSessionWithPublicationIncarnation(id, logicalDate, address, history, memory, createdAt, updatedAt, incarnation)
}

// ReconstructCanonicalSessionWithPublicationIncarnation restores a canonical
// Session using the exact generation persisted by its owner.
func ReconstructCanonicalSessionWithPublicationIncarnation(id modulecore.SessionID, logicalDate string, address conversation.ChannelAddress, history []conversation.TurnInput, memory map[string]interface{}, createdAt, updatedAt time.Time, publicationIncarnation string) (*Session, error) {
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("invalid session_id: %w", err)
	}
	if err := ValidatePublicationIncarnation(publicationIncarnation); err != nil {
		return nil, err
	}
	if err := ValidateLogicalDate(logicalDate); err != nil {
		return nil, err
	}
	if err := address.Validate(); err != nil {
		return nil, fmt.Errorf("invalid channel address: %w", err)
	}
	if createdAt.IsZero() {
		return nil, fmt.Errorf("created_at is required")
	}
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		return nil, fmt.Errorf("updated_at must not precede created_at")
	}
	createdAt = createdAt.UTC()
	updatedAt = updatedAt.UTC()
	storedHistory := append([]conversation.TurnInput(nil), history...)
	storedMemory := make(map[string]interface{}, len(memory))
	for key, value := range memory {
		storedMemory[key] = value
	}
	return &Session{
		id:                     string(id),
		publicationIncarnation: publicationIncarnation,
		logicalDate:            logicalDate,
		channelAddress:         address,
		history:                storedHistory,
		memory:                 storedMemory,
		createdAt:              createdAt,
		updatedAt:              updatedAt,
	}, nil
}
