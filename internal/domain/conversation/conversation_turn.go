package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	ConversationTurnCanonicalVersion   = "rencrow.conversation_turn.v1"
	ConversationTurnMaxIDRunes         = 256
	ConversationTurnMaxTextRunes       = 64 * 1024
	ConversationTurnMaxReasonRunes     = 1024
	ConversationTurnMaxTraceItems      = 256
	ConversationTurnMaxTraceRunes      = 8192
	// ConversationTurnMaxTraceTotalRunes bounds the whole trace, not one item.
	// A realistic RecallPack trace holds up to 256 items of a few hundred
	// runes each; reusing the per-item bound here rejected every real turn.
	ConversationTurnMaxTraceTotalRunes = 256 * 1024
	ConversationTurnMaxTraceSourceURLs = 256
	ConversationTurnMaxTargets         = 2
	ConversationTurnMaxOutboxAttempts  = 3
)

type ConversationTurnStatus string

const (
	ConversationTurnCompleted ConversationTurnStatus = "completed"
	ConversationTurnPartial   ConversationTurnStatus = "partial"
	ConversationTurnFailed    ConversationTurnStatus = "failed"
)

type ConversationTurnErrorCode string

const (
	ConversationTurnErrorInvalid     ConversationTurnErrorCode = "invalid"
	ConversationTurnErrorConflict    ConversationTurnErrorCode = "conflict"
	ConversationTurnErrorUnavailable ConversationTurnErrorCode = "unavailable"
	ConversationTurnErrorInternal    ConversationTurnErrorCode = "internal"
)

type ConversationTurnError struct{ Code ConversationTurnErrorCode }

func (e *ConversationTurnError) Error() string {
	if e == nil {
		return "conversation turn error"
	}
	return "conversation turn " + string(e.Code)
}

func (e *ConversationTurnError) Is(target error) bool {
	t, ok := target.(*ConversationTurnError)
	return ok && e != nil && t != nil && e.Code == t.Code
}

var (
	ErrConversationTurnInvalid     = &ConversationTurnError{Code: ConversationTurnErrorInvalid}
	ErrConversationTurnConflict    = &ConversationTurnError{Code: ConversationTurnErrorConflict}
	ErrConversationTurnUnavailable = &ConversationTurnError{Code: ConversationTurnErrorUnavailable}
	ErrConversationTurnInternal    = &ConversationTurnError{Code: ConversationTurnErrorInternal}
)

func ConversationTurnErrorCodeOf(err error) ConversationTurnErrorCode {
	var typed *ConversationTurnError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	return ""
}

type ConversationTurnTarget string

const (
	ConversationTurnTargetRedisProjection ConversationTurnTarget = "redis_projection"
	ConversationTurnTargetThreadFollowers ConversationTurnTarget = "thread_followers"
)

type ConversationTurnOutboxStatus string

const (
	ConversationTurnOutboxPending   ConversationTurnOutboxStatus = "pending"
	ConversationTurnOutboxRunning   ConversationTurnOutboxStatus = "running"
	ConversationTurnOutboxCompleted ConversationTurnOutboxStatus = "completed"
	ConversationTurnOutboxFailed    ConversationTurnOutboxStatus = "failed"
)

// ConversationTurnRequest is the one canonical EndTurn input shape. TurnID
// is the root job and trace identity; no caller-provided message IDs exist.
type ConversationTurnRequest struct {
	TurnID           string
	SessionID        string
	OwnerID          string
	Domain           string
	UserMessage      string
	AgentMessage     string
	AgentSpeaker     Speaker
	RecallTraceItems []RecallTraceItem
	Boundary         bool
	BoundaryReason   string
	Targets          []ConversationTurnTarget
}

type ConversationTurnResult struct {
	TurnID           string                    `json:"turn_id"`
	TraceID          string                    `json:"trace_id"`
	SessionID        string                    `json:"session_id"`
	ThreadID         int64                     `json:"thread_id"`
	ClosedThreadID   int64                     `json:"closed_thread_id,omitempty"`
	UserMessageID    string                    `json:"user_message_id"`
	AgentMessageID   string                    `json:"agent_message_id"`
	MessageIDs       []string                  `json:"message_ids"`
	PayloadSHA256    string                    `json:"payload_sha256"`
	Status           ConversationTurnStatus    `json:"status"`
	ErrorCode        ConversationTurnErrorCode `json:"error_code,omitempty"`
	RequestedTargets []string                  `json:"requested_targets,omitempty"`
	PendingTargets   []string                  `json:"pending_targets,omitempty"`
	CompletedTargets []string                  `json:"completed_targets,omitempty"`
	IdempotentReplay bool                      `json:"idempotent_replay,omitempty"`
}

type ConversationTurnOutbox struct {
	TurnID         string                       `json:"turn_id"`
	Target         string                       `json:"target"`
	SessionID      string                       `json:"session_id"`
	ThreadID       int64                        `json:"thread_id"`
	ClosedThreadID int64                        `json:"closed_thread_id,omitempty"`
	PayloadSHA256  string                       `json:"payload_sha256"`
	PayloadJSON    string                       `json:"payload_json"`
	Status         ConversationTurnOutboxStatus `json:"status"`
	LeaseToken     string                       `json:"-"`
	LeaseExpiresAt time.Time                    `json:"lease_expires_at,omitempty"`
	Attempts       int                          `json:"attempts"`
	LastError      ConversationTurnErrorCode    `json:"last_error,omitempty"`
	CreatedAt      time.Time                    `json:"created_at"`
	UpdatedAt      time.Time                    `json:"updated_at"`
}

func NormalizeConversationTurnRequest(request ConversationTurnRequest) (ConversationTurnRequest, error) {
	turnID, err := boundedRequired(request.TurnID, ConversationTurnMaxIDRunes, "turn_id")
	if err != nil {
		return ConversationTurnRequest{}, err
	}
	sessionID, err := boundedRequired(request.SessionID, ConversationTurnMaxIDRunes, "session_id")
	if err != nil {
		return ConversationTurnRequest{}, err
	}
	ownerID, err := boundedRequired(request.OwnerID, ConversationTurnMaxIDRunes, "owner_id")
	if err != nil {
		return ConversationTurnRequest{}, err
	}
	domain := strings.TrimSpace(request.Domain)
	if domain == "" {
		domain = "general"
	}
	if err := boundedText(domain, ConversationTurnMaxIDRunes, "domain"); err != nil {
		return ConversationTurnRequest{}, err
	}
	if err := boundedRequiredText(request.UserMessage, ConversationTurnMaxTextRunes, "user_message"); err != nil {
		return ConversationTurnRequest{}, err
	}
	if err := boundedRequiredText(request.AgentMessage, ConversationTurnMaxTextRunes, "agent_message"); err != nil {
		return ConversationTurnRequest{}, err
	}
	canonicalSpeaker, ok := CanonicalChatAgentSpeaker(request.AgentSpeaker)
	if !ok || canonicalSpeaker != request.AgentSpeaker {
		return ConversationTurnRequest{}, invalidConversationTurn("agent speaker is not canonical")
	}
	items := append([]RecallTraceItem(nil), request.RecallTraceItems...)
	if len(items) > ConversationTurnMaxTraceItems {
		return ConversationTurnRequest{}, invalidConversationTurn("recall trace item count exceeds bound")
	}
	traceTotalRunes := 0
	for i := range items {
		if err := validateConversationTurnTraceItem(items[i]); err != nil {
			return ConversationTurnRequest{}, err
		}
		traceRunes := conversationTurnTraceRunes(items[i])
		if traceRunes > ConversationTurnMaxTraceRunes {
			return ConversationTurnRequest{}, invalidConversationTurn("recall trace text exceeds bound")
		}
		traceTotalRunes += traceRunes
		if traceTotalRunes > ConversationTurnMaxTraceTotalRunes {
			return ConversationTurnRequest{}, invalidConversationTurn("recall trace total text exceeds bound")
		}
	}
	targets, err := normalizeConversationTurnTargets(request.Targets)
	if err != nil {
		return ConversationTurnRequest{}, err
	}
	reason := request.BoundaryReason
	if request.Boundary {
		if err := boundedRequiredText(reason, ConversationTurnMaxReasonRunes, "boundary_reason"); err != nil {
			return ConversationTurnRequest{}, err
		}
	} else if strings.TrimSpace(reason) != "" {
		return ConversationTurnRequest{}, invalidConversationTurn("boundary_reason requires boundary")
	}
	return ConversationTurnRequest{
		TurnID: turnID, SessionID: sessionID, OwnerID: ownerID, Domain: domain,
		UserMessage: request.UserMessage, AgentMessage: request.AgentMessage, AgentSpeaker: request.AgentSpeaker,
		RecallTraceItems: items, Boundary: request.Boundary, BoundaryReason: reason, Targets: targets,
	}, nil
}

func (request ConversationTurnRequest) Validate() error {
	_, err := NormalizeConversationTurnRequest(request)
	return err
}

func ValidateConversationTurnRequest(request ConversationTurnRequest) error {
	return request.Validate()
}

func CanonicalConversationTurnPayload(request ConversationTurnRequest) ([]byte, error) {
	normalized, err := NormalizeConversationTurnRequest(request)
	if err != nil {
		return nil, err
	}
	type canonicalMessage struct {
		Speaker string `json:"speaker"`
		Text    string `json:"text"`
	}
	type canonicalTraceItem struct {
		Layer         string   `json:"layer"`
		Kind          string   `json:"kind"`
		MemoryID      string   `json:"memory_id"`
		SourceID      string   `json:"source_id"`
		SourceType    string   `json:"source_type"`
		Summary       string   `json:"summary"`
		Query         string   `json:"query"`
		Provider      string   `json:"provider"`
		SourceURLs    []string `json:"source_urls"`
		Score         float32  `json:"score"`
		Decision      string   `json:"decision"`
		Status        string   `json:"status"`
		Reason        string   `json:"reason"`
		MemoryState   string   `json:"memory_state"`
		Sensitivity   string   `json:"sensitivity"`
		PromptSection string   `json:"prompt_section"`
		TokenCount    int      `json:"token_count"`
		PromptIndex   int      `json:"prompt_index"`
		RetrievedAt   string   `json:"retrieved_at"`
	}
	items := make([]canonicalTraceItem, 0, len(normalized.RecallTraceItems))
	for _, item := range normalized.RecallTraceItems {
		items = append(items, canonicalTraceItem{
			Layer: item.Layer, Kind: item.Kind, MemoryID: item.MemoryID, SourceID: item.SourceID, SourceType: item.SourceType,
			Summary: item.Summary, Query: item.Query, Provider: item.Provider, SourceURLs: append([]string(nil), item.SourceURLs...),
			Score: item.Score, Decision: item.Decision, Status: item.Status, Reason: item.Reason, MemoryState: item.MemoryState,
			Sensitivity: item.Sensitivity, PromptSection: item.PromptSection, TokenCount: item.TokenCount, PromptIndex: item.PromptIndex,
			RetrievedAt: canonicalTurnTime(item.RetrievedAt),
		})
	}
	canonical := struct {
		Version        string               `json:"version"`
		SessionID      string               `json:"session_id"`
		OwnerID        string               `json:"owner_id"`
		Domain         string               `json:"domain"`
		AgentSpeaker   string               `json:"agent_speaker"`
		Messages       []canonicalMessage   `json:"messages"`
		RecallItems    []canonicalTraceItem `json:"recall_trace_items"`
		Boundary       bool                 `json:"boundary"`
		BoundaryReason string               `json:"boundary_reason"`
		Targets        []string             `json:"requested_targets"`
	}{
		Version: ConversationTurnCanonicalVersion, SessionID: normalized.SessionID, OwnerID: normalized.OwnerID,
		Domain: normalized.Domain, AgentSpeaker: string(normalized.AgentSpeaker),
		Messages:    []canonicalMessage{{Speaker: string(SpeakerUser), Text: normalized.UserMessage}, {Speaker: string(normalized.AgentSpeaker), Text: normalized.AgentMessage}},
		RecallItems: items, Boundary: normalized.Boundary, BoundaryReason: normalized.BoundaryReason,
		Targets: targetStrings(normalized.Targets),
	}
	return json.Marshal(canonical)
}

func ConversationTurnPayloadSHA256(request ConversationTurnRequest) (string, error) {
	payload, err := CanonicalConversationTurnPayload(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (request ConversationTurnRequest) CanonicalPayload() ([]byte, error) {
	return CanonicalConversationTurnPayload(request)
}

func (request ConversationTurnRequest) PayloadSHA256() (string, error) {
	return ConversationTurnPayloadSHA256(request)
}

func ConversationTurnMessageIDs(turnID string) (userID, agentID string, err error) {
	turnID, err = boundedRequired(turnID, ConversationTurnMaxIDRunes, "turn_id")
	if err != nil {
		return "", "", err
	}
	derive := func(label string) string {
		digest := sha256.Sum256([]byte("rencrow.conversation-turn.message.v1\x00" + turnID + "\x00" + label))
		value := uuid.UUID(digest[:16])
		value[6] = (value[6] & 0x0f) | 0x50
		value[8] = (value[8] & 0x3f) | 0x80
		return "msg_" + value.String()
	}
	return derive("user"), derive("agent"), nil
}

func normalizeConversationTurnTargets(values []ConversationTurnTarget) ([]ConversationTurnTarget, error) {
	if len(values) > ConversationTurnMaxTargets {
		return nil, invalidConversationTurn("too many conversation turn targets")
	}
	seen := make(map[ConversationTurnTarget]struct{}, len(values))
	result := make([]ConversationTurnTarget, 0, len(values))
	for _, value := range values {
		if value != ConversationTurnTargetRedisProjection && value != ConversationTurnTargetThreadFollowers {
			return nil, invalidConversationTurn("unknown conversation turn target")
		}
		if _, ok := seen[value]; ok {
			return nil, invalidConversationTurn("duplicate conversation turn target")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func targetStrings(values []ConversationTurnTarget) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func validateConversationTurnTraceItem(item RecallTraceItem) error {
	for name, value := range map[string]string{
		"trace.layer": item.Layer, "trace.kind": item.Kind, "trace.memory_id": item.MemoryID,
		"trace.source_id": item.SourceID, "trace.source_type": item.SourceType, "trace.summary": item.Summary,
		"trace.query": item.Query, "trace.provider": item.Provider, "trace.decision": item.Decision,
		"trace.status": item.Status, "trace.reason": item.Reason, "trace.memory_state": item.MemoryState,
		"trace.sensitivity": item.Sensitivity, "trace.prompt_section": item.PromptSection,
	} {
		if err := boundedText(value, ConversationTurnMaxTraceRunes, name); err != nil {
			return err
		}
	}
	if math.IsNaN(float64(item.Score)) || math.IsInf(float64(item.Score), 0) {
		return invalidConversationTurn("trace score is not finite")
	}
	for _, sourceURL := range item.SourceURLs {
		if err := boundedText(sourceURL, ConversationTurnMaxTraceRunes, "trace.source_url"); err != nil {
			return err
		}
	}
	if len(item.SourceURLs) > ConversationTurnMaxTraceSourceURLs {
		return invalidConversationTurn("trace source URL count exceeds bound")
	}
	if item.TokenCount < 0 || item.TokenCount > ConversationTurnMaxTraceRunes || item.PromptIndex < -1 || item.PromptIndex > ConversationTurnMaxTraceItems {
		return invalidConversationTurn("trace numeric field is out of bounds")
	}
	return nil
}

func conversationTurnTraceRunes(item RecallTraceItem) int {
	total := 0
	for _, value := range []string{
		item.Layer, item.Kind, item.MemoryID, item.SourceID, item.SourceType,
		item.Summary, item.Query, item.Provider, item.Decision, item.Status,
		item.Reason, item.MemoryState, item.Sensitivity, item.PromptSection,
	} {
		total += utf8.RuneCountInString(value)
	}
	for _, value := range item.SourceURLs {
		total += utf8.RuneCountInString(value)
	}
	return total
}

func boundedRequiredText(value string, max int, field string) error {
	if strings.TrimSpace(value) == "" {
		return invalidConversationTurn(field + " is required")
	}
	return boundedText(value, max, field)
}

func boundedRequired(value string, max int, field string) (string, error) {
	value = strings.TrimSpace(value)
	if err := boundedRequiredText(value, max, field); err != nil {
		return "", err
	}
	return value, nil
}

func boundedText(value string, max int, field string) error {
	if !utf8.ValidString(value) {
		return invalidConversationTurn(field + " must be valid UTF-8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return invalidConversationTurn(field + " contains NUL")
	}
	if len([]rune(value)) > max {
		return invalidConversationTurn(field + " exceeds bound")
	}
	return nil
}

// invalidConversationTurn keeps the typed invalid identity (errors.Is /
// ConversationTurnErrorCodeOf) while preserving the static reason for
// operational logs. Reasons are fixed field labels, never message text.
func invalidConversationTurn(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrConversationTurnInvalid
	}
	return fmt.Errorf("%w: %s", ErrConversationTurnInvalid, reason)
}

func canonicalTurnTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
