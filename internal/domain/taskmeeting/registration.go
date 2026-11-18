package taskmeeting

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// TaskMeetingRegistrationKey names the one owner-managed daily meeting
// declaration. It is stable across config revisions and process restarts.
const TaskMeetingRegistrationKey = "heartbeat.routine.daily_report_meetings"

// TaskMeetingDeclaration is the complete non-secret owner declaration stored
// in an EventStore registration receipt. It deliberately contains neither an
// issued ScheduleID nor an issued RegistrationEvidenceID.
type TaskMeetingDeclaration struct {
	RegistrationKey          string                `json:"registration_key"`
	OwnerUserID              string                `json:"owner_user_id"`
	Revision                 uint64                `json:"revision"`
	DataScope                string                `json:"data_scope"`
	CoordinatorActorID       string                `json:"coordinator_actor_id"`
	Audience                 []string              `json:"audience"`
	WorldActorBindings       []WorldActorBinding   `json:"world_actor_bindings"`
	WorldBaseURL             string                `json:"world_base_url"`
	WorldCredentialEnv       string                `json:"world_credential_env"`
	WorldID                  string                `json:"world_id"`
	TimelineID               string                `json:"timeline_id"`
	TimelineEpoch            string                `json:"timeline_epoch"`
	Rooms                    RoomRefs              `json:"rooms"`
	Timing                   modulecore.TaskTiming `json:"timing"`
	MeetingDeadlineAfterSecs int64                 `json:"meeting_deadline_after_seconds"`
}

// Validate checks the declaration's canonical identity and exact ordered
// roster before it is admitted to the durable registration owner.
func (declaration TaskMeetingDeclaration) Validate() error {
	if declaration.RegistrationKey != TaskMeetingRegistrationKey ||
		!canonicalOwnerValue(declaration.OwnerUserID) || declaration.Revision == 0 || declaration.DataScope != "user" ||
		!canonicalOwnerValue(declaration.CoordinatorActorID) || len(declaration.Audience) != 8 ||
		len(declaration.WorldActorBindings) != len(declaration.Audience) || !validTaskMeetingWorldEndpoint(declaration.WorldBaseURL) ||
		!validTaskMeetingCredentialEnvName(declaration.WorldCredentialEnv) || !canonicalOwnerValue(declaration.WorldID) ||
		!canonicalOwnerValue(declaration.TimelineID) || !canonicalOwnerValue(declaration.TimelineEpoch) {
		return errors.New("TaskMeeting registration declaration identity is incomplete")
	}
	if err := ValidateDailyMeetingTiming(declaration.Timing); err != nil {
		return fmt.Errorf("TaskMeeting registration timing: %w", err)
	}
	if err := declaration.Rooms.Validate(); err != nil || declaration.Rooms.Living == "" || declaration.Rooms.Dining == "" {
		return errors.New("TaskMeeting registration requires two exact owner-declared venue references")
	}
	if declaration.MeetingDeadlineAfterSecs < 60 || declaration.MeetingDeadlineAfterSecs > 3*60*60 {
		return errors.New("TaskMeeting registration deadline must be between 60 seconds and 3 hours")
	}
	seenCore := make(map[string]struct{}, len(declaration.Audience))
	seenWorld := make(map[string]struct{}, len(declaration.WorldActorBindings))
	coordinatorPresent := false
	for index, actorID := range declaration.Audience {
		if !canonicalOwnerValue(actorID) || actorID != strings.ToLower(actorID) {
			return errors.New("TaskMeeting registration audience contains a noncanonical actor")
		}
		if _, exists := seenCore[actorID]; exists {
			return errors.New("TaskMeeting registration audience contains a duplicate actor")
		}
		seenCore[actorID] = struct{}{}
		binding := declaration.WorldActorBindings[index]
		if binding.CoreActorID != actorID || !canonicalOwnerValue(binding.WorldActorID) {
			return errors.New("TaskMeeting registration bindings must match the exact ordered audience")
		}
		if _, exists := seenWorld[binding.WorldActorID]; exists {
			return errors.New("TaskMeeting registration has duplicate WORLD actor bindings")
		}
		seenWorld[binding.WorldActorID] = struct{}{}
		coordinatorPresent = coordinatorPresent || actorID == declaration.CoordinatorActorID
	}
	if !coordinatorPresent {
		return errors.New("TaskMeeting registration coordinator must belong to its exact audience")
	}
	return nil
}

// CanonicalTaskMeetingDeclarationJSON is the only supported declaration
// serializer. Its bytes are shared by runtime, TaskManager, and EventStore.
func CanonicalTaskMeetingDeclarationJSON(declaration TaskMeetingDeclaration) ([]byte, string, error) {
	if err := declaration.Validate(); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(declaration)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

// DecodeCanonicalTaskMeetingDeclaration rejects unknown fields, trailing
// values, and noncanonical byte representations before owner admission.
func DecodeCanonicalTaskMeetingDeclaration(encoded []byte) (TaskMeetingDeclaration, string, error) {
	var declaration TaskMeetingDeclaration
	if len(encoded) == 0 || len(encoded) > 64<<10 {
		return declaration, "", errors.New("TaskMeeting registration declaration size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&declaration); err != nil {
		return TaskMeetingDeclaration{}, "", fmt.Errorf("decode TaskMeeting registration declaration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return TaskMeetingDeclaration{}, "", errors.New("TaskMeeting registration declaration contains trailing data")
	}
	canonical, digest, err := CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		return TaskMeetingDeclaration{}, "", err
	}
	if !bytes.Equal(encoded, canonical) {
		return TaskMeetingDeclaration{}, "", errors.New("TaskMeeting registration declaration is not canonical JSON")
	}
	return declaration, digest, nil
}

func canonicalOwnerValue(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func validTaskMeetingWorldEndpoint(value string) bool {
	if !canonicalOwnerValue(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validTaskMeetingCredentialEnvName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	for index, char := range value {
		valid := char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char == '_' || index > 0 && char >= '0' && char <= '9'
		if !valid {
			return false
		}
	}
	return true
}
