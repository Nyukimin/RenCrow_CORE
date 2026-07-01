package memory

import (
	"fmt"
	"strings"
	"time"
)

const (
	NamespaceKindConversation = "conv"
	NamespaceKindUser         = "user"
	NamespaceKindCharacter    = "char"
	NamespaceKindKnowledge    = "kb"
)

const (
	MemoryStateObserved  = "observed"
	MemoryStateCandidate = "candidate"
	MemoryStateConfirmed = "confirmed"
	MemoryStatePinned    = "pinned"
)

const (
	UserMemoryTypeProfile      = "profile"
	UserMemoryTypePreference   = "preference"
	UserMemoryTypeProject      = "project"
	UserMemoryTypeConstraint   = "constraint"
	UserMemoryTypeRelationship = "relationship"
	UserMemoryTypeEpisode      = "episode"
	UserMemoryTypeSkill        = "skill"
	UserMemoryTypeSensitive    = "sensitive"
)

type UserMemory struct {
	ID               string    `json:"id"`
	Namespace        string    `json:"namespace"`
	UserID           string    `json:"user_id"`
	Type             string    `json:"type"`
	Statement        string    `json:"statement"`
	EvidenceEventIDs []string  `json:"evidence_event_ids"`
	Confidence       float64   `json:"confidence"`
	Sensitivity      string    `json:"sensitivity"`
	State            string    `json:"state"`
	Scope            string    `json:"scope"`
	Active           bool      `json:"active"`
	LifecycleStatus  string    `json:"lifecycle_status,omitempty"`
	DecayScore       float64   `json:"decay_score,omitempty"`
	SupersededBy     string    `json:"superseded_by,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CreateUserMemoryInput struct {
	UserID           string
	Type             string
	Statement        string
	State            string
	EvidenceEventIDs []string
	Confidence       float64
	Sensitivity      string
	Scope            string
	Source           string
}

type RecallPackView struct {
	SessionID string           `json:"session_id"`
	UserID    string           `json:"user_id"`
	Items     []RecallPackItem `json:"items"`
	CreatedAt time.Time        `json:"created_at"`
}

type RecallPackItem struct {
	Layer       string   `json:"layer"`
	Namespace   string   `json:"namespace"`
	MemoryID    string   `json:"memory_id"`
	Kind        string   `json:"kind"`
	Summary     string   `json:"summary"`
	Score       float64  `json:"score"`
	State       string   `json:"state"`
	SourceID    string   `json:"source_id,omitempty"`
	EventIDs    []string `json:"event_ids,omitempty"`
	Sensitivity string   `json:"sensitivity,omitempty"`
}

func BuildNamespace(kind string, id string) (string, error) {
	kind = strings.TrimSpace(kind)
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("memory namespace id is required")
	}
	switch kind {
	case NamespaceKindConversation, NamespaceKindUser, NamespaceKindCharacter, NamespaceKindKnowledge:
		return kind + ":" + id, nil
	default:
		return "", fmt.Errorf("invalid memory namespace kind: %s", kind)
	}
}

func ValidateNamespace(namespace string) error {
	namespace = strings.TrimSpace(namespace)
	parts := strings.SplitN(namespace, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("invalid memory namespace: %s", namespace)
	}
	switch parts[0] {
	case NamespaceKindConversation, NamespaceKindUser, NamespaceKindCharacter, NamespaceKindKnowledge:
		return nil
	default:
		return fmt.Errorf("invalid memory namespace kind: %s", parts[0])
	}
}

func ValidateMemoryState(state string) error {
	switch strings.TrimSpace(state) {
	case MemoryStateObserved, MemoryStateCandidate, MemoryStateConfirmed, MemoryStatePinned:
		return nil
	default:
		return fmt.Errorf("invalid memory state: %s", state)
	}
}

func ValidateUserMemoryType(memoryType string) error {
	switch strings.TrimSpace(memoryType) {
	case UserMemoryTypeProfile, UserMemoryTypePreference, UserMemoryTypeProject, UserMemoryTypeConstraint,
		UserMemoryTypeRelationship, UserMemoryTypeEpisode, UserMemoryTypeSkill, UserMemoryTypeSensitive:
		return nil
	default:
		return fmt.Errorf("invalid user memory type: %s", memoryType)
	}
}

func CanPromoteUserMemory(state string, evidenceEventIDs []string, sensitivity string, reason string) error {
	state = strings.TrimSpace(state)
	reason = strings.TrimSpace(reason)
	sensitivity = strings.TrimSpace(sensitivity)
	if err := ValidateMemoryState(state); err != nil {
		return err
	}
	if state == MemoryStateObserved || state == MemoryStateCandidate {
		return nil
	}
	if len(evidenceEventIDs) == 0 {
		return fmt.Errorf("%s memory requires evidence_event_ids", state)
	}
	if sensitivity == UserMemoryTypeSensitive || sensitivity == "sensitive" {
		return fmt.Errorf("sensitive memory cannot be auto-promoted to %s", state)
	}
	if state == MemoryStatePinned && reason == "" {
		return fmt.Errorf("pinned memory requires explicit reason")
	}
	return nil
}

func IsUserMemoryPromptInjectable(mem UserMemory, persona string) bool {
	if !mem.Active {
		return false
	}
	if strings.TrimSpace(mem.SupersededBy) != "" {
		return false
	}
	if strings.TrimSpace(mem.LifecycleStatus) == "decayed" {
		return false
	}
	switch strings.TrimSpace(mem.State) {
	case MemoryStateConfirmed, MemoryStatePinned:
	default:
		return false
	}
	if strings.TrimSpace(mem.Sensitivity) != "" && strings.TrimSpace(mem.Sensitivity) != "normal" {
		return false
	}
	scope := strings.TrimSpace(mem.Scope)
	if scope == "" || scope == "all_personas" || scope == "all" || scope == "global" {
		return true
	}
	persona = strings.ToLower(strings.TrimSpace(persona))
	scope = strings.ToLower(scope)
	return persona != "" && (scope == persona || scope == persona+"_only")
}
