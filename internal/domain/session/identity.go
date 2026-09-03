package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const logicalDateLayout = "2006-01-02"

// ChannelAddress identifies the authenticated address within one channel.
// It is routing data and must never be encoded into SessionID.
type ChannelAddress struct {
	Channel string `json:"channel"`
	Address string `json:"address"`
}

func NewChannelAddress(channel, address string) (ChannelAddress, error) {
	value := ChannelAddress{
		Channel: strings.ToLower(strings.TrimSpace(channel)),
		Address: strings.TrimSpace(address),
	}
	if value.Channel == "" {
		return ChannelAddress{}, fmt.Errorf("channel is required")
	}
	if value.Address == "" {
		return ChannelAddress{}, fmt.Errorf("channel address is required")
	}
	return value, nil
}

func (a ChannelAddress) Validate() error {
	if a.Channel == "" || a.Address == "" {
		return fmt.Errorf("channel and channel address are required")
	}
	if strings.ToLower(strings.TrimSpace(a.Channel)) != a.Channel || strings.TrimSpace(a.Address) != a.Address {
		return fmt.Errorf("channel address is not normalized")
	}
	return nil
}

func ValidateLogicalDate(value string) error {
	parsed, err := time.Parse(logicalDateLayout, value)
	if err != nil || parsed.Format(logicalDateLayout) != value {
		return fmt.Errorf("logical_date must use YYYY-MM-DD")
	}
	return nil
}

// NewCanonicalSession constructs a Session whose opaque identity and lookup
// attributes are separate canonical values.
func NewCanonicalSession(id modulecore.SessionID, logicalDate string, address ChannelAddress, createdAt time.Time) (*Session, error) {
	return ReconstructCanonicalSession(id, logicalDate, address, nil, nil, createdAt, createdAt)
}

// ReconstructCanonicalSession restores a persisted canonical session without
// changing its timestamps while history and memory are hydrated.
func ReconstructCanonicalSession(id modulecore.SessionID, logicalDate string, address ChannelAddress, history []task.Task, memory map[string]interface{}, createdAt, updatedAt time.Time) (*Session, error) {
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("invalid session_id: %w", err)
	}
	if err := ValidateLogicalDate(logicalDate); err != nil {
		return nil, err
	}
	normalizedAddress, err := NewChannelAddress(address.Channel, address.Address)
	if err != nil {
		return nil, err
	}
	if createdAt.IsZero() {
		return nil, fmt.Errorf("created_at is required")
	}
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		return nil, fmt.Errorf("updated_at must not precede created_at")
	}
	createdAt = createdAt.UTC()
	updatedAt = updatedAt.UTC()
	storedHistory := append([]task.Task(nil), history...)
	storedMemory := make(map[string]interface{}, len(memory))
	for key, value := range memory {
		storedMemory[key] = value
	}
	return &Session{
		id:             string(id),
		logicalDate:    logicalDate,
		channelAddress: normalizedAddress,
		channel:        normalizedAddress.Channel,
		chatID:         normalizedAddress.Address,
		history:        storedHistory,
		memory:         storedMemory,
		createdAt:      createdAt,
		updatedAt:      updatedAt,
	}, nil
}
