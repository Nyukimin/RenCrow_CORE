// Package worldprojection owns CORE's side of the WORLD v1.4 projection
// contract. It defines the exact WorkSet v2 / LifePolicy payloads and the
// envelope that carries them. It deliberately does not decide which CORE
// policy or Persona fields become a LifePolicy; that decision stays with the
// owning CORE policy source.
package worldprojection

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ContractVersion = "world.contracts/v1"
	RecordType      = "CoreProjectionEnvelope"

	KindWorkSet    = "full_actor_work_set"
	KindLifePolicy = "life_policy"

	WorkSetContract    = "rencrow.world.work-set/v2"
	LifePolicyContract = "world.life-policy/v1"
	SourceOwnerCORE    = "CORE"

	WorkClassAssignedWork = "assigned_work"

	ExecutionQueued  = "queued"
	ExecutionRunning = "running"
	ExecutionWaiting = "waiting"
	ExecutionBlocked = "blocked"

	MaxSourceRevision = uint64(9007199254740991)
	MaxEnvelopeBytes  = 256 * 1024
)

var (
	ErrInvalidProjection = errors.New("invalid CORE world projection")
	ErrEpochChange       = errors.New("source epoch change requires an explicit reconcile")
)

// Target is the exact WORLD destination for one projection. ActorTransportID
// is the already-encoded WORLD path segment; CORE must not normalize or alias
// the canonical ActorID.
type Target struct {
	WorldID          string
	TimelineID       string
	TimelineEpoch    string
	ActorID          string
	ActorInstanceID  string
	ActorTransportID string
}

func (t Target) Validate() error {
	if !validExact(t.WorldID, 256) || !validExact(t.TimelineID, 256) ||
		!validExact(t.TimelineEpoch, 256) || !validExact(t.ActorID, 256) ||
		!validExact(t.ActorInstanceID, 256) || !validExact(t.ActorTransportID, 256) {
		return fmt.Errorf("%w: destination is incomplete", ErrInvalidProjection)
	}
	return nil
}

// WorkItem is one CORE-assigned active work item. A running item must carry
// the confirmed RunID; queued, waiting, and blocked items must not.
type WorkItem struct {
	TaskID                   string `json:"task_id"`
	RunID                    string `json:"run_id,omitempty"`
	ExecutionState           string `json:"execution_state"`
	WorkClass                string `json:"work_class"`
	RequiresHousePresence    bool   `json:"requires_house_presence"`
	ClassificationReasonCode string `json:"classification_reason_code"`
}

// WorkSet is a fresh full-actor work snapshot. ActiveWork is always non-nil so
// an empty set remains distinguishable from a missing set.
type WorkSet struct {
	ContractVersion string     `json:"contract_version"`
	WorldID         string     `json:"world_id"`
	ActorID         string     `json:"actor_id"`
	SourceOwner     string     `json:"source_owner"`
	SourceEpoch     string     `json:"source_epoch"`
	SourceRevision  uint64     `json:"source_revision"`
	ValidUntil      time.Time  `json:"valid_until"`
	ActiveWork      []WorkItem `json:"active_work"`
}

// LifePolicy is the CORE-owned non-cognitive life permission snapshot. The
// payload intentionally carries no Persona body, long-term memory, model, or
// hardware assignment.
type LifePolicy struct {
	ContractVersion                string    `json:"contract_version"`
	WorldID                        string    `json:"world_id"`
	ActorID                        string    `json:"actor_id"`
	SourceOwner                    string    `json:"source_owner"`
	SourceEpoch                    string    `json:"source_epoch"`
	SourceRevision                 uint64    `json:"source_revision"`
	ValidUntil                     time.Time `json:"valid_until"`
	PersonaRevision                string    `json:"persona_revision"`
	AllowedNonCognitiveActivityIDs []string  `json:"allowed_non_cognitive_activity_ids"`
	ExpiredFallbackActivityIDs     []string  `json:"expired_fallback_activity_ids"`
	ConditionProfileRef            string    `json:"condition_profile_ref"`
}

// Envelope is a fully built projection. Payload is the exact JSON carried to
// WORLD; Digest is WORLD's canonical payload SHA-256, so CORE can compare its
// outbox record with the accepted head even when the wire bytes differ in key
// order or whitespace.
type Envelope struct {
	Target  Target
	Kind    string
	Payload json.RawMessage
	Digest  string
}

type wireEnvelope struct {
	ContractVersion string          `json:"contract_version"`
	RecordType      string          `json:"record_type"`
	WorldID         string          `json:"world_id"`
	TimelineID      string          `json:"timeline_id"`
	TimelineEpoch   string          `json:"timeline_epoch"`
	ActorInstanceID string          `json:"actor_instance_id"`
	ProjectionKind  string          `json:"projection_kind"`
	Payload         json.RawMessage `json:"payload"`
}

// NewWorkSet validates and constructs a full-actor work set. activeWork must
// be non-nil; the caller supplies an empty slice when there is no current work.
func NewWorkSet(worldID, actorID, sourceEpoch string, sourceRevision uint64, validUntil time.Time, activeWork []WorkItem) (WorkSet, error) {
	if activeWork == nil {
		return WorkSet{}, fmt.Errorf("%w: active_work must be a full set", ErrInvalidProjection)
	}
	work := make([]WorkItem, len(activeWork))
	copy(work, activeWork)
	seen := make(map[string]struct{}, len(work))
	for i := range work {
		if err := validateWorkItem(work[i]); err != nil {
			return WorkSet{}, err
		}
		if _, exists := seen[work[i].TaskID]; exists {
			return WorkSet{}, fmt.Errorf("%w: duplicate task_id %q", ErrInvalidProjection, work[i].TaskID)
		}
		seen[work[i].TaskID] = struct{}{}
	}
	sort.Slice(work, func(i, j int) bool {
		if work[i].TaskID == work[j].TaskID {
			return work[i].RunID < work[j].RunID
		}
		return work[i].TaskID < work[j].TaskID
	})
	value := WorkSet{
		ContractVersion: WorkSetContract,
		WorldID:         worldID,
		ActorID:         actorID,
		SourceOwner:     SourceOwnerCORE,
		SourceEpoch:     sourceEpoch,
		SourceRevision:  sourceRevision,
		ValidUntil:      validUntil.UTC(),
		ActiveWork:      work,
	}
	if err := value.Validate(); err != nil {
		return WorkSet{}, err
	}
	return value, nil
}

func (w WorkSet) Validate() error {
	if w.ContractVersion != WorkSetContract || w.SourceOwner != SourceOwnerCORE ||
		!validExact(w.WorldID, 256) || !validExact(w.ActorID, 256) ||
		!validExact(w.SourceEpoch, 256) || !validRevision(w.SourceRevision) ||
		w.ValidUntil.IsZero() || w.ActiveWork == nil {
		return fmt.Errorf("%w: invalid work set header", ErrInvalidProjection)
	}
	for _, item := range w.ActiveWork {
		if err := validateWorkItem(item); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkItem(item WorkItem) error {
	if !validExact(item.TaskID, 256) || (item.RunID != "" && !validExact(item.RunID, 256)) ||
		!validExact(item.ClassificationReasonCode, 256) ||
		item.WorkClass != WorkClassAssignedWork {
		return fmt.Errorf("%w: invalid work item", ErrInvalidProjection)
	}
	switch item.ExecutionState {
	case ExecutionQueued, ExecutionWaiting, ExecutionBlocked:
		if item.RunID != "" {
			return fmt.Errorf("%w: only running work may carry run_id", ErrInvalidProjection)
		}
	case ExecutionRunning:
		if item.RunID == "" {
			return fmt.Errorf("%w: running work requires run_id", ErrInvalidProjection)
		}
	default:
		return fmt.Errorf("%w: invalid execution_state %q", ErrInvalidProjection, item.ExecutionState)
	}
	return nil
}

// NewLifePolicy validates and constructs a LifePolicy snapshot.
func NewLifePolicy(worldID, actorID, sourceEpoch string, sourceRevision uint64, validUntil time.Time,
	personaRevision, conditionProfileRef string, allowed, expiredFallback []string) (LifePolicy, error) {
	if allowed == nil || expiredFallback == nil {
		return LifePolicy{}, fmt.Errorf("%w: allowed and fallback activity lists must be full sets", ErrInvalidProjection)
	}
	allowedCopy, err := validatedActivityIDs(allowed, 128)
	if err != nil {
		return LifePolicy{}, err
	}
	fallbackCopy, err := validatedActivityIDs(expiredFallback, 16)
	if err != nil {
		return LifePolicy{}, err
	}
	allowedSet := make(map[string]struct{}, len(allowedCopy))
	for _, id := range allowedCopy {
		allowedSet[id] = struct{}{}
	}
	for _, id := range fallbackCopy {
		if _, ok := allowedSet[id]; !ok {
			return LifePolicy{}, fmt.Errorf("%w: expired fallback activity %q is not allowed", ErrInvalidProjection, id)
		}
	}
	value := LifePolicy{
		ContractVersion:                LifePolicyContract,
		WorldID:                        worldID,
		ActorID:                        actorID,
		SourceOwner:                    SourceOwnerCORE,
		SourceEpoch:                    sourceEpoch,
		SourceRevision:                 sourceRevision,
		ValidUntil:                     validUntil.UTC(),
		PersonaRevision:                personaRevision,
		AllowedNonCognitiveActivityIDs: allowedCopy,
		ExpiredFallbackActivityIDs:     fallbackCopy,
		ConditionProfileRef:            conditionProfileRef,
	}
	if err := value.Validate(); err != nil {
		return LifePolicy{}, err
	}
	return value, nil
}

func (p LifePolicy) Validate() error {
	if p.ContractVersion != LifePolicyContract || p.SourceOwner != SourceOwnerCORE ||
		!validExact(p.WorldID, 256) || !validExact(p.ActorID, 256) ||
		!validExact(p.SourceEpoch, 256) || !validRevision(p.SourceRevision) ||
		p.ValidUntil.IsZero() || p.AllowedNonCognitiveActivityIDs == nil ||
		p.ExpiredFallbackActivityIDs == nil ||
		!validExact(p.PersonaRevision, 256) || !validExact(p.ConditionProfileRef, 256) {
		return fmt.Errorf("%w: invalid life policy header", ErrInvalidProjection)
	}
	allowed, err := validatedActivityIDs(p.AllowedNonCognitiveActivityIDs, 128)
	if err != nil {
		return err
	}
	fallback, err := validatedActivityIDs(p.ExpiredFallbackActivityIDs, 16)
	if err != nil {
		return err
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	for _, id := range fallback {
		if _, ok := allowedSet[id]; !ok {
			return fmt.Errorf("%w: expired fallback activity %q is not allowed", ErrInvalidProjection, id)
		}
	}
	return nil
}

func validatedActivityIDs(ids []string, max int) ([]string, error) {
	if len(ids) > max {
		return nil, fmt.Errorf("%w: too many activity ids", ErrInvalidProjection)
	}
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if !validExact(id, 256) {
			return nil, fmt.Errorf("%w: invalid activity id", ErrInvalidProjection)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: duplicate activity id %q", ErrInvalidProjection, id)
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

// NewEnvelope builds one wire envelope from a typed payload. The payload's
// world and Actor must match the destination, so a caller cannot cross-post an
// Actor's projection to another Actor's route.
func NewEnvelope(target Target, kind string, payload any) (Envelope, error) {
	if err := target.Validate(); err != nil {
		return Envelope{}, err
	}
	var (
		raw       []byte
		payloadID string
	)
	switch value := payload.(type) {
	case WorkSet:
		if value.WorldID != target.WorldID || value.ActorID != target.ActorID {
			return Envelope{}, fmt.Errorf("%w: work set destination mismatch", ErrInvalidProjection)
		}
		if err := value.Validate(); err != nil {
			return Envelope{}, err
		}
		raw, _ = json.Marshal(value)
		payloadID = KindWorkSet
	case LifePolicy:
		if value.WorldID != target.WorldID || value.ActorID != target.ActorID {
			return Envelope{}, fmt.Errorf("%w: life policy destination mismatch", ErrInvalidProjection)
		}
		if err := value.Validate(); err != nil {
			return Envelope{}, err
		}
		raw, _ = json.Marshal(value)
		payloadID = KindLifePolicy
	default:
		return Envelope{}, fmt.Errorf("%w: unsupported payload type", ErrInvalidProjection)
	}
	if kind != payloadID {
		return Envelope{}, fmt.Errorf("%w: payload does not match projection kind", ErrInvalidProjection)
	}
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes {
		return Envelope{}, fmt.Errorf("%w: payload size is invalid", ErrInvalidProjection)
	}
	digest, err := PayloadDigest(raw)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		Target:  target,
		Kind:    kind,
		Payload: append(json.RawMessage(nil), raw...),
		Digest:  digest,
	}, nil
}

// Marshal returns the exact envelope bytes sent to WORLD.
func (e Envelope) Marshal() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(wireEnvelope{
		ContractVersion: ContractVersion,
		RecordType:      RecordType,
		WorldID:         e.Target.WorldID,
		TimelineID:      e.Target.TimelineID,
		TimelineEpoch:   e.Target.TimelineEpoch,
		ActorInstanceID: e.Target.ActorInstanceID,
		ProjectionKind:  e.Kind,
		Payload:         e.Payload,
	})
}

func (e Envelope) Validate() error {
	if err := e.Target.Validate(); err != nil {
		return err
	}
	if e.Kind != KindWorkSet && e.Kind != KindLifePolicy {
		return fmt.Errorf("%w: unsupported projection kind", ErrInvalidProjection)
	}
	if len(e.Payload) == 0 || len(e.Payload) > MaxEnvelopeBytes {
		return fmt.Errorf("%w: payload size is invalid", ErrInvalidProjection)
	}
	digest, err := PayloadDigest(e.Payload)
	if err != nil {
		return err
	}
	if e.Digest != digest {
		return fmt.Errorf("%w: payload digest mismatch", ErrInvalidProjection)
	}
	switch e.Kind {
	case KindWorkSet:
		var value WorkSet
		if err := json.Unmarshal(e.Payload, &value); err != nil {
			return fmt.Errorf("%w: work set payload is invalid", ErrInvalidProjection)
		}
		if value.WorldID != e.Target.WorldID || value.ActorID != e.Target.ActorID {
			return fmt.Errorf("%w: work set destination mismatch", ErrInvalidProjection)
		}
		return value.Validate()
	case KindLifePolicy:
		var value LifePolicy
		if err := json.Unmarshal(e.Payload, &value); err != nil {
			return fmt.Errorf("%w: life policy payload is invalid", ErrInvalidProjection)
		}
		if value.WorldID != e.Target.WorldID || value.ActorID != e.Target.ActorID {
			return fmt.Errorf("%w: life policy destination mismatch", ErrInvalidProjection)
		}
		return value.Validate()
	default:
		return fmt.Errorf("%w: unsupported projection kind", ErrInvalidProjection)
	}
}

// SourceRevision is CORE's durable per-Actor/projection ordering state.
type SourceRevision struct {
	Epoch    string
	Revision uint64
}

func (s SourceRevision) Validate() error {
	if !validExact(s.Epoch, 256) || !validRevision(s.Revision) {
		return fmt.Errorf("%w: invalid source revision", ErrInvalidProjection)
	}
	return nil
}

// NextSourceRevision advances an existing epoch. An epoch change is rejected
// here because it is an explicit reconcile decision, not a normal publish.
func NextSourceRevision(current SourceRevision, epoch string) (SourceRevision, error) {
	if !validExact(epoch, 256) {
		return SourceRevision{}, fmt.Errorf("%w: invalid source epoch", ErrInvalidProjection)
	}
	if current.Epoch == "" && current.Revision == 0 {
		return SourceRevision{Epoch: epoch, Revision: 1}, nil
	}
	if err := current.Validate(); err != nil {
		return SourceRevision{}, err
	}
	if current.Epoch != epoch {
		return SourceRevision{}, fmt.Errorf("%w: %q -> %q", ErrEpochChange, current.Epoch, epoch)
	}
	if current.Revision >= MaxSourceRevision {
		return SourceRevision{}, fmt.Errorf("%w: source revision exhausted", ErrInvalidProjection)
	}
	return SourceRevision{Epoch: epoch, Revision: current.Revision + 1}, nil
}

// ResetSourceRevision is the only explicit epoch switch. The caller must have
// completed the authenticated WORLD reconcile before using the new epoch.
func ResetSourceRevision(epoch string) (SourceRevision, error) {
	if !validExact(epoch, 256) {
		return SourceRevision{}, fmt.Errorf("%w: invalid source epoch", ErrInvalidProjection)
	}
	return SourceRevision{Epoch: epoch, Revision: 1}, nil
}

func validRevision(value uint64) bool {
	return value >= 1 && value <= MaxSourceRevision
}

func validExact(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
