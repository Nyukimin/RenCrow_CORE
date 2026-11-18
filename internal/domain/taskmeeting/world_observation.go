package taskmeeting

import (
	"errors"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// WorldActorSnapshot is the allowlisted actor state returned by the
// authenticated WORLD Snapshot client. It is source data, not an attendance
// proof by itself.
type WorldActorSnapshot struct {
	WorldActorID string `json:"world_actor_id"`
	Present      bool   `json:"present"`
	RoomID       string `json:"room_id,omitempty"`
	ActivityID   string `json:"activity_id,omitempty"`
}

type WorldSnapshotObservation struct {
	WorldID       string               `json:"world_id"`
	TimelineID    string               `json:"timeline_id"`
	TimelineEpoch string               `json:"timeline_epoch"`
	StateSeq      uint64               `json:"state_seq"`
	StateHash     string               `json:"state_hash"`
	Actors        []WorldActorSnapshot `json:"actors"`
}

type WorldObservationRequest struct {
	Key                    OccurrenceKey            `json:"occurrence_key"`
	TaskID                 modulecore.TaskID        `json:"task_id"`
	RunID                  modulecore.RunID         `json:"run_id"`
	ConfigRevision         uint64                   `json:"config_revision"`
	ConfigSHA256           string                   `json:"config_sha256"`
	OwnerUserID            string                   `json:"owner_user_id"`
	RegistrationEvidenceID modulecore.EvidenceID    `json:"registration_evidence_id"`
	CoordinatorActorID     string                   `json:"coordinator_actor_id"`
	Session                modulecore.SessionID     `json:"session_id"`
	Thread                 modulecore.ThreadID      `json:"thread_id"`
	Generation             uint64                   `json:"generation"`
	Epoch                  uint64                   `json:"epoch"`
	Audience               []string                 `json:"audience"`
	ActorBindings          []WorldActorBinding      `json:"actor_bindings"`
	Rooms                  RoomRefs                 `json:"rooms"`
	Snapshot               WorldSnapshotObservation `json:"snapshot"`
	ObservedAt             time.Time                `json:"observed_at"`
	ValidUntil             time.Time                `json:"valid_until"`
	RequestID              string                   `json:"request_id"`
}

type WorldObservationReceipt struct {
	EvidenceID modulecore.EvidenceID `json:"evidence_id"`
	Scope      WorldScopeEvidence    `json:"scope"`
	Presence   []PresenceEvidence    `json:"presence"`
	ObservedAt time.Time             `json:"observed_at"`
}

func (snapshot WorldSnapshotObservation) Validate() error {
	if strings.TrimSpace(snapshot.WorldID) == "" || snapshot.WorldID != strings.TrimSpace(snapshot.WorldID) ||
		strings.TrimSpace(snapshot.TimelineID) == "" || snapshot.TimelineID != strings.TrimSpace(snapshot.TimelineID) ||
		strings.TrimSpace(snapshot.TimelineEpoch) == "" || snapshot.TimelineEpoch != strings.TrimSpace(snapshot.TimelineEpoch) ||
		snapshot.StateSeq == 0 || !canonicalWorldHash(snapshot.StateHash) || len(snapshot.Actors) != 8 {
		return errors.New("WORLD snapshot observation identity is incomplete")
	}
	seen := make(map[string]struct{}, len(snapshot.Actors))
	for _, actor := range snapshot.Actors {
		if strings.TrimSpace(actor.WorldActorID) == "" || actor.WorldActorID != strings.TrimSpace(actor.WorldActorID) ||
			strings.ContainsAny(actor.WorldActorID, "\r\n\x00") || (actor.Present && strings.TrimSpace(actor.RoomID) == "") ||
			(!actor.Present && actor.RoomID != "") {
			return errors.New("WORLD snapshot actor observation is invalid")
		}
		if _, exists := seen[actor.WorldActorID]; exists {
			return errors.New("WORLD snapshot contains a duplicate actor")
		}
		seen[actor.WorldActorID] = struct{}{}
	}
	return nil
}

func canonicalWorldHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
