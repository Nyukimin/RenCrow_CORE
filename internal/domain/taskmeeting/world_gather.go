package taskmeeting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type WorldGatherCommandPhase string

var ErrWorldGatherCommandConflict = errors.New("TaskMeeting World gather stable key conflicts with immutable command")

const (
	WorldGatherPhaseStart  WorldGatherCommandPhase = "start"
	WorldGatherPhaseFinish WorldGatherCommandPhase = "finish"
)

// WorldGatherCommandIntent is the immutable CORE-owned intent for one WORLD
// movement command. Scope carries the frozen TaskMeeting root binding. Its
// observation request ID and refreshed snapshot timestamps are deliberately
// excluded from the command's stable identity.
type WorldGatherCommandIntent struct {
	Scope          WorldObservationRequest `json:"scope"`
	ActorID        string                  `json:"actor_id"`
	WorldActorID   string                  `json:"world_actor_id"`
	TargetRoomID   string                  `json:"target_room_id"`
	Phase          WorldGatherCommandPhase `json:"phase"`
	ActivityID     string                  `json:"activity_id,omitempty"`
	RouteID        string                  `json:"route_id,omitempty"`
	SceneSHA256    string                  `json:"scene_sha256,omitempty"`
	DueAt          time.Time               `json:"due_at,omitempty"`
	Payload        json.RawMessage         `json:"payload"`
	StableKey      string                  `json:"stable_key"`
	StartStableKey string                  `json:"start_stable_key,omitempty"`
}

// WorldGatherCommandReceipt is the typed, verified WORLD lifecycle evidence
// retained by CORE. WORLD identifiers remain opaque strings at this boundary.
type WorldGatherCommandReceipt struct {
	ReceiptID    string    `json:"receipt_id"`
	EventID      string    `json:"event_id"`
	EventSeq     uint64    `json:"event_seq"`
	ActivityID   string    `json:"activity_id"`
	RouteID      string    `json:"route_id"`
	SceneSHA256  string    `json:"scene_sha256"`
	TargetRoomID string    `json:"target_room_id"`
	DueAt        time.Time `json:"due_at"`
}

type WorldGatherCommandOutcome struct {
	StableKey string                    `json:"stable_key"`
	Phase     WorldGatherCommandPhase   `json:"phase"`
	Receipt   WorldGatherCommandReceipt `json:"receipt"`
}

// WorldGatherCommandPayload mirrors the stable fields accepted by the WORLD
// start and finish command routes.
type WorldGatherCommandPayload struct {
	ActorID    string `json:"actor_id"`
	RoomID     string `json:"room_id,omitempty"`
	SlotID     string `json:"slot_id,omitempty"`
	ActivityID string `json:"activity_id,omitempty"`
	PoseID     string `json:"pose_id,omitempty"`
}

type TaskMeetingWorldGatherAuthorizationCheck func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, WorldGatherCommandIntent) error

// TaskMeetingWorldGatherStore persists command intent before external effects,
// and records only a verified WORLD outcome. Prepare returns whether this call
// created the immutable intent; a replay returns the original intent and uses
// its same stable key for WORLD idempotency.
type TaskMeetingWorldGatherStore interface {
	FindTaskMeetingWorldGather(context.Context, WorldGatherCommandIntent, domaintask.TaskMeetingInvocationReader, domaintask.TaskMeetingReadAuthorizationCheck) (WorldGatherCommandIntent, WorldGatherCommandOutcome, bool, error)
	PrepareTaskMeetingWorldGather(context.Context, WorldGatherCommandIntent, domaintask.TaskMeetingInvocationReader, domaintask.TaskMeetingReadAuthorizationCheck, TaskMeetingWorldGatherAuthorizationCheck) (WorldGatherCommandIntent, bool, error)
	RecordTaskMeetingWorldGatherOutcome(context.Context, WorldGatherCommandIntent, WorldGatherCommandOutcome, domaintask.TaskMeetingInvocationReader, domaintask.TaskMeetingReadAuthorizationCheck, TaskMeetingWorldGatherAuthorizationCheck) error
}

func (intent WorldGatherCommandIntent) Validate() error {
	if err := validateWorldGatherScope(intent.Scope); err != nil {
		return err
	}
	if validateActorID(intent.ActorID) != nil || strings.TrimSpace(intent.WorldActorID) == "" || intent.WorldActorID != strings.TrimSpace(intent.WorldActorID) ||
		strings.TrimSpace(intent.TargetRoomID) == "" || intent.TargetRoomID != strings.TrimSpace(intent.TargetRoomID) ||
		len(intent.StableKey) == 0 || len(intent.StableKey) > 128 || strings.TrimSpace(intent.StableKey) != intent.StableKey || strings.ContainsAny(intent.StableKey, "\r\n\x00") ||
		len(intent.Payload) == 0 || !json.Valid(intent.Payload) {
		return errors.New("TaskMeeting World gather command intent is incomplete")
	}
	matched := false
	for _, binding := range intent.Scope.ActorBindings {
		if binding.CoreActorID == intent.ActorID && binding.WorldActorID == intent.WorldActorID {
			matched = true
			break
		}
	}
	if !matched || (intent.TargetRoomID != intent.Scope.Rooms.Living && intent.TargetRoomID != intent.Scope.Rooms.Dining) {
		return errors.New("TaskMeeting World gather command actor or destination is outside the frozen scope")
	}
	canonicalPayload, payload, err := canonicalWorldGatherCommandPayload(intent.Payload)
	if err != nil || payload.ActorID != intent.WorldActorID {
		return errors.New("TaskMeeting World gather command payload does not match its actor")
	}
	if string(canonicalPayload) != string(intent.Payload) {
		return errors.New("TaskMeeting World gather command payload is not canonical JSON")
	}
	switch intent.Phase {
	case WorldGatherPhaseStart:
		if intent.ActivityID != "" || intent.RouteID != "" || intent.SceneSHA256 != "" || !intent.DueAt.IsZero() || intent.StartStableKey != "" || payload.RoomID != intent.TargetRoomID || payload.SlotID != "" || payload.ActivityID != "" || payload.PoseID != "" {
			return errors.New("TaskMeeting World gather start intent must not claim started lifecycle evidence")
		}
	case WorldGatherPhaseFinish:
		if !validOpaqueWorldValue(intent.ActivityID) || !validOpaqueWorldValue(intent.RouteID) || !canonicalWorldHash(intent.SceneSHA256) || intent.DueAt.IsZero() ||
			!validOpaqueWorldValue(intent.StartStableKey) || payload.ActivityID != intent.ActivityID || payload.RoomID != "" || payload.SlotID != "" || payload.PoseID != "" {
			return errors.New("TaskMeeting World gather finish intent must bind verified start lifecycle evidence")
		}
	default:
		return errors.New("TaskMeeting World gather phase is unsupported")
	}
	wantStableKey, err := WorldGatherCommandStableKey(intent)
	if err != nil || intent.StableKey != wantStableKey {
		return ErrWorldGatherCommandConflict
	}
	return nil
}

// WorldGatherCommandStableKey returns the only accepted idempotency key for
// an immutable movement command. Mutable observation refresh and guard epochs
// are excluded so recovery reuses the original WORLD key.
func WorldGatherCommandStableKey(intent WorldGatherCommandIntent) (string, error) {
	canonicalPayload, _, err := canonicalWorldGatherCommandPayload(intent.Payload)
	if err != nil {
		return "", err
	}
	identity := struct {
		Occurrence   string                  `json:"occurrence"`
		TaskID       string                  `json:"task_id"`
		RunID        string                  `json:"run_id"`
		ConfigRev    uint64                  `json:"config_revision"`
		ConfigSHA    string                  `json:"config_sha256"`
		Owner        string                  `json:"owner_user_id"`
		Registration string                  `json:"registration_evidence_id"`
		Coordinator  string                  `json:"coordinator_actor_id"`
		Session      string                  `json:"session_id"`
		Thread       string                  `json:"thread_id"`
		Audience     []string                `json:"audience"`
		Bindings     []WorldActorBinding     `json:"actor_bindings"`
		Rooms        RoomRefs                `json:"rooms"`
		ActorID      string                  `json:"actor_id"`
		WorldActorID string                  `json:"world_actor_id"`
		TargetRoom   string                  `json:"target_room_id"`
		Phase        WorldGatherCommandPhase `json:"phase"`
		ActivityID   string                  `json:"activity_id,omitempty"`
		RouteID      string                  `json:"route_id,omitempty"`
		SceneSHA     string                  `json:"scene_sha256,omitempty"`
		DueAt        time.Time               `json:"due_at,omitempty"`
		StartKey     string                  `json:"start_stable_key,omitempty"`
		Payload      json.RawMessage         `json:"payload"`
	}{
		Occurrence: intent.Scope.Key.String(), TaskID: string(intent.Scope.TaskID), RunID: string(intent.Scope.RunID),
		ConfigRev: intent.Scope.ConfigRevision, ConfigSHA: intent.Scope.ConfigSHA256, Owner: intent.Scope.OwnerUserID,
		Registration: string(intent.Scope.RegistrationEvidenceID), Coordinator: intent.Scope.CoordinatorActorID,
		Session: string(intent.Scope.Session), Thread: string(intent.Scope.Thread), Audience: intent.Scope.Audience,
		Bindings: intent.Scope.ActorBindings, Rooms: intent.Scope.Rooms, ActorID: intent.ActorID, WorldActorID: intent.WorldActorID,
		TargetRoom: intent.TargetRoomID, Phase: intent.Phase, ActivityID: intent.ActivityID, RouteID: intent.RouteID,
		SceneSHA: intent.SceneSHA256, DueAt: intent.DueAt.UTC(), StartKey: intent.StartStableKey, Payload: canonicalPayload,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "taskmeeting-world-gather:" + hex.EncodeToString(digest[:]), nil
}

func canonicalWorldGatherCommandPayload(raw json.RawMessage) (json.RawMessage, WorldGatherCommandPayload, error) {
	var payload WorldGatherCommandPayload
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, WorldGatherCommandPayload{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, WorldGatherCommandPayload{}, errors.New("WORLD gather payload has trailing JSON")
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, WorldGatherCommandPayload{}, err
	}
	return canonical, payload, nil
}

func validateWorldGatherScope(scope WorldObservationRequest) error {
	if scope.Key.Validate() != nil || scope.TaskID.Validate() != nil || scope.RunID.Validate() != nil || scope.RegistrationEvidenceID.Validate() != nil ||
		scope.Session.Validate() != nil || scope.Thread.Validate() != nil || scope.Generation == 0 || scope.Epoch == 0 || scope.ConfigRevision == 0 ||
		!canonicalWorldHash(scope.ConfigSHA256) || strings.TrimSpace(scope.OwnerUserID) == "" || scope.OwnerUserID != strings.TrimSpace(scope.OwnerUserID) ||
		validateActorID(scope.CoordinatorActorID) != nil || scope.Rooms.Validate() != nil || scope.Rooms.Living == "" || scope.Rooms.Dining == "" {
		return errors.New("TaskMeeting World gather scope binding is incomplete")
	}
	audienceHash, err := domaintask.TaskMeetingAudienceSHA256(scope.Audience)
	if err != nil || len(scope.ActorBindings) != domaintask.TaskMeetingAudienceSize {
		return errors.New("TaskMeeting World gather scope audience is invalid")
	}
	seenWorld := make(map[string]struct{}, len(scope.ActorBindings))
	for i, binding := range scope.ActorBindings {
		if binding.CoreActorID != scope.Audience[i] || binding.WorldActorID == "" || binding.WorldActorID != strings.TrimSpace(binding.WorldActorID) || strings.ContainsAny(binding.WorldActorID, "\r\n\x00") {
			return errors.New("TaskMeeting World gather scope actor bindings are invalid")
		}
		if _, exists := seenWorld[binding.WorldActorID]; exists {
			return errors.New("TaskMeeting World gather scope has duplicate WORLD actors")
		}
		seenWorld[binding.WorldActorID] = struct{}{}
	}
	if audienceHash == "" {
		return errors.New("TaskMeeting World gather scope audience is invalid")
	}
	return nil
}

func (outcome WorldGatherCommandOutcome) ValidateFor(intent WorldGatherCommandIntent) error {
	receipt := outcome.Receipt
	if outcome.StableKey != intent.StableKey || outcome.Phase != intent.Phase || !validOpaqueWorldValue(receipt.ReceiptID) || !validOpaqueWorldValue(receipt.EventID) ||
		receipt.EventSeq == 0 || !validOpaqueWorldValue(receipt.ActivityID) || !validOpaqueWorldValue(receipt.RouteID) || !canonicalWorldHash(receipt.SceneSHA256) ||
		!validOpaqueWorldValue(receipt.TargetRoomID) || receipt.DueAt.IsZero() || receipt.TargetRoomID != intent.TargetRoomID {
		return errors.New("TaskMeeting World gather outcome does not contain matching verified lifecycle evidence")
	}
	if intent.Phase == WorldGatherPhaseFinish && (receipt.ActivityID != intent.ActivityID || receipt.RouteID != intent.RouteID || receipt.SceneSHA256 != intent.SceneSHA256 || !receipt.DueAt.Equal(intent.DueAt)) {
		return ErrWorldGatherCommandConflict
	}
	return nil
}

func validOpaqueWorldValue(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00")
}
