package session

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ErrSessionNotFound はセッションが見つからない場合のエラー
var ErrSessionNotFound = errors.New("session not found")

// CanonicalSessionOwnerStatus is the fail-closed result of resolving an owner
// against a canonical Session record.
type CanonicalSessionOwnerStatus string

const (
	CanonicalSessionOwnerCreated     CanonicalSessionOwnerStatus = "created"
	CanonicalSessionOwnerMatched     CanonicalSessionOwnerStatus = "matched"
	CanonicalSessionOwnerUnavailable CanonicalSessionOwnerStatus = "unavailable"
	CanonicalSessionOwnerConflict    CanonicalSessionOwnerStatus = "conflict"
	CanonicalSessionOwnerNotFound    CanonicalSessionOwnerStatus = "not_found"
)

// CanonicalSessionOwnerResolution carries only a Session whose owner and
// ChannelAddress have both matched. Other statuses intentionally expose no
// Session payload or owner identity.
type CanonicalSessionOwnerResolution struct {
	Session      *Session
	BoundOwnerID string
	Status       CanonicalSessionOwnerStatus
}

// CanonicalSessionPublicationBinding is the current canonical identity used by
// Viewer publication checks. It is a read projection of the Session record,
// never an independently mutable owner source.
type CanonicalSessionPublicationBinding struct {
	sessionID              modulecore.SessionID
	ownerID                string
	channelAddress         conversation.ChannelAddress
	publicationIncarnation string
}

func NewCanonicalSessionPublicationBinding(sess *Session) (CanonicalSessionPublicationBinding, error) {
	if sess == nil {
		return CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session is required")
	}
	binding := CanonicalSessionPublicationBinding{
		sessionID:              modulecore.SessionID(sess.ID()),
		ownerID:                sess.OwnerID(),
		channelAddress:         sess.ChannelAddress(),
		publicationIncarnation: sess.PublicationIncarnation(),
	}
	if err := binding.Validate(); err != nil {
		return CanonicalSessionPublicationBinding{}, err
	}
	return binding, nil
}

// NewReservedV2ErrorPublicationBinding derives the private publication identity
// used for a terminal error on an unmaterialized response-first v2 reservation.
func NewReservedV2ErrorPublicationBinding(sess *Session, ownerID string) (CanonicalSessionPublicationBinding, error) {
	if sess == nil {
		return CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 Session is required")
	}
	if err := modulecore.SessionID(sess.ID()).Validate(); err != nil {
		return CanonicalSessionPublicationBinding{}, fmt.Errorf("invalid reserved v2 SessionID: %w", err)
	}
	if err := ValidateOwnerID(ownerID); err != nil {
		return CanonicalSessionPublicationBinding{}, fmt.Errorf("invalid reserved v2 owner: %w", err)
	}
	if sess.OwnerID() != "" {
		return CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 Session must not have a Session owner binding")
	}
	address := sess.ChannelAddress()
	if err := address.Validate(); err != nil {
		return CanonicalSessionPublicationBinding{}, fmt.Errorf("invalid reserved v2 Session ChannelAddress: %w", err)
	}
	if address.ChannelType() != "viewer" || address.ExternalConversationID() != "cmd:"+ownerID {
		return CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 Session address does not match its command owner")
	}
	binding := CanonicalSessionPublicationBinding{
		sessionID:              modulecore.SessionID(sess.ID()),
		ownerID:                ownerID,
		channelAddress:         address,
		publicationIncarnation: sess.PublicationIncarnation(),
	}
	if err := binding.Validate(); err != nil {
		return CanonicalSessionPublicationBinding{}, err
	}
	return binding, nil
}

func (b CanonicalSessionPublicationBinding) Validate() error {
	if err := b.sessionID.Validate(); err != nil {
		return fmt.Errorf("invalid publication SessionID: %w", err)
	}
	if err := b.channelAddress.Validate(); err != nil {
		return fmt.Errorf("invalid publication ChannelAddress: %w", err)
	}
	if b.ownerID != "" {
		if err := ValidateOwnerID(b.ownerID); err != nil {
			return fmt.Errorf("invalid publication owner: %w", err)
		}
	}
	if err := ValidatePublicationIncarnation(b.publicationIncarnation); err != nil {
		return fmt.Errorf("invalid publication incarnation: %w", err)
	}
	return nil
}

func (b CanonicalSessionPublicationBinding) SessionID() modulecore.SessionID { return b.sessionID }
func (b CanonicalSessionPublicationBinding) OwnerID() string                 { return b.ownerID }
func (b CanonicalSessionPublicationBinding) Audience() conversation.SessionAudience {
	if b.ownerID == "" {
		return conversation.SessionAudiencePublic
	}
	return conversation.SessionAudiencePrivate
}
func (b CanonicalSessionPublicationBinding) ChannelAddress() conversation.ChannelAddress {
	return b.channelAddress
}
func (b CanonicalSessionPublicationBinding) PublicationIncarnation() string {
	return b.publicationIncarnation
}

func (b CanonicalSessionPublicationBinding) SameIdentity(other CanonicalSessionPublicationBinding) bool {
	return b.sessionID == other.sessionID && b.ownerID == other.ownerID &&
		b.channelAddress == other.channelAddress && b.publicationIncarnation == other.publicationIncarnation
}

// ValidateOwnerID verifies the canonical owner value stored with an owned
// Session. Empty values represent legacy sessions and must not be treated as
// an owner binding.
func ValidateOwnerID(ownerID string) error {
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
		return fmt.Errorf("owner_id is required and normalized")
	}
	return nil
}

// Session はユーザーセッションを表すエンティティ
// 日次カットオーバーで切り替わり、会話履歴とメモリを保持
type Session struct {
	id                     string // 不透明なCanonical SessionID。日付やrouting情報を埋め込まない。
	ownerID                string // immutable authenticated owner binding; empty only for legacy sessions.
	publicationIncarnation string // opaque generation for public event visibility; changed only when this Session binding is replaced.
	logicalDate            string
	channelAddress         conversation.ChannelAddress
	history                []conversation.TurnInput // 会話履歴
	memory                 map[string]interface{}   // セッションメモリ
	createdAt              time.Time                // セッション作成時刻
	updatedAt              time.Time                // 最終更新時刻
}

// NewCanonicalSessionWithOwner creates a canonical Session with its
// authenticated owner bound at creation time.
func NewCanonicalSessionWithOwner(id modulecore.SessionID, ownerID, logicalDate string, address conversation.ChannelAddress, createdAt time.Time) (*Session, error) {
	if err := ValidateOwnerID(ownerID); err != nil {
		return nil, err
	}
	sess, err := NewCanonicalSession(id, logicalDate, address, createdAt)
	if err != nil {
		return nil, err
	}
	sess.ownerID = ownerID
	return sess, nil
}

// ReconstructCanonicalSessionWithOwner restores a persisted owner-bound
// Session without changing the binding or timestamps.
func ReconstructCanonicalSessionWithOwner(id modulecore.SessionID, ownerID, logicalDate string, address conversation.ChannelAddress, history []conversation.TurnInput, memory map[string]interface{}, createdAt, updatedAt time.Time) (*Session, error) {
	if err := ValidateOwnerID(ownerID); err != nil {
		return nil, err
	}
	sess, err := ReconstructCanonicalSession(id, logicalDate, address, history, memory, createdAt, updatedAt)
	if err != nil {
		return nil, err
	}
	sess.ownerID = ownerID
	return sess, nil
}

// ReconstructCanonicalSessionWithOwnerAndPublicationIncarnation restores the
// persisted owner and publication lifecycle without issuing replacement IDs.
func ReconstructCanonicalSessionWithOwnerAndPublicationIncarnation(id modulecore.SessionID, ownerID, logicalDate string, address conversation.ChannelAddress, history []conversation.TurnInput, memory map[string]interface{}, createdAt, updatedAt time.Time, publicationIncarnation string) (*Session, error) {
	if err := ValidateOwnerID(ownerID); err != nil {
		return nil, err
	}
	sess, err := ReconstructCanonicalSessionWithPublicationIncarnation(id, logicalDate, address, history, memory, createdAt, updatedAt, publicationIncarnation)
	if err != nil {
		return nil, err
	}
	sess.ownerID = ownerID
	return sess, nil
}

// ID はセッションIDを返す
func (s *Session) ID() string {
	return s.id
}

// OwnerID returns the immutable authenticated owner binding. Empty means the
// canonical Session predates owner binding or came from a non-owner flow.
func (s *Session) OwnerID() string {
	return s.ownerID
}

// PublicationIncarnation returns the opaque publication generation stored
// with this canonical Session. It remains stable across ordinary turn changes.
func (s *Session) PublicationIncarnation() string {
	if s == nil {
		return ""
	}
	return s.publicationIncarnation
}

// RenewPublicationIncarnation starts a new canonical publication lifecycle.
// The persistence owner uses it only when creating or replacing the current
// Session binding; ordinary Save and EndTurn preserve the existing value.
func (s *Session) RenewPublicationIncarnation() error {
	if s == nil {
		return fmt.Errorf("canonical Session is required")
	}
	incarnation, err := NewPublicationIncarnation()
	if err != nil {
		return err
	}
	s.publicationIncarnation = incarnation
	return nil
}

// RestorePublicationIncarnation applies the value verified from the current
// canonical Session record to a stale in-memory object after owner resolution.
func (s *Session) RestorePublicationIncarnation(incarnation string) error {
	if s == nil {
		return fmt.Errorf("canonical Session is required")
	}
	if err := ValidatePublicationIncarnation(incarnation); err != nil {
		return err
	}
	s.publicationIncarnation = incarnation
	return nil
}

// LogicalDate returns the explicit calendar partition used to find a session.
func (s *Session) LogicalDate() string {
	return s.logicalDate
}

// ChannelAddress returns routing information kept outside the opaque SessionID.
func (s *Session) ChannelAddress() conversation.ChannelAddress {
	return s.channelAddress
}

// CreatedAt は作成時刻を返す
func (s *Session) CreatedAt() time.Time {
	return s.createdAt
}

// UpdatedAt は最終更新時刻を返す
func (s *Session) UpdatedAt() time.Time {
	return s.updatedAt
}

// AddTurnInput は会話入力を履歴に追加
func (s *Session) AddTurnInput(input conversation.TurnInput) {
	s.history = append(s.history, input)
	s.updatedAt = time.Now()
}

// GetHistory は会話履歴を返す
func (s *Session) GetHistory() []conversation.TurnInput {
	return append([]conversation.TurnInput(nil), s.history...)
}

// GetRecentHistory は最近N件の履歴を返す
func (s *Session) GetRecentHistory(n int) []conversation.TurnInput {
	if len(s.history) <= n {
		return s.GetHistory()
	}
	return append([]conversation.TurnInput(nil), s.history[len(s.history)-n:]...)
}

// SetMemory はメモリに値を設定
func (s *Session) SetMemory(key string, value interface{}) {
	s.memory[key] = value
	s.updatedAt = time.Now()
}

// GetMemory はメモリから値を取得
func (s *Session) GetMemory(key string) (interface{}, bool) {
	value, ok := s.memory[key]
	return value, ok
}

// GetAllMemory はメモリのコピーを返す
func (s *Session) GetAllMemory() map[string]interface{} {
	result := make(map[string]interface{}, len(s.memory))
	for k, v := range s.memory {
		result[k] = v
	}
	return result
}

// ClearMemory はメモリをクリア
func (s *Session) ClearMemory() {
	s.memory = make(map[string]interface{})
	s.updatedAt = time.Now()
}

// HistoryCount は履歴の件数を返す
func (s *Session) HistoryCount() int {
	return len(s.history)
}
