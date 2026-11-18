package workstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// MeetingOccurrenceRecord is the exact owner payload returned by the store.
// PayloadHash is a CAS token over the persisted JSON bytes, not an occurrence ID.
type MeetingOccurrenceRecord = domainmeeting.OccurrenceRecord

type meetingOccurrenceEnvelope struct {
	Key     domainmeeting.OccurrenceKey `json:"key"`
	Access  domainmeeting.StoreAccess   `json:"access"`
	Meeting domainmeeting.Meeting       `json:"meeting"`
}

// FindMeetingOccurrence returns the exact stored occurrence for a previously
// authorized owner scope. It never creates a managed Workstream or occurrence.
func (s *SQLiteStore) FindMeetingOccurrence(ctx context.Context, access domainmeeting.StoreAccess, key domainmeeting.OccurrenceKey) (domainmeeting.Meeting, bool, error) {
	if err := validateMeetingStoreCall(ctx, access, key); err != nil {
		return domainmeeting.Meeting{}, false, err
	}
	if s == nil || s.db == nil {
		return domainmeeting.Meeting{}, false, errors.New("meeting occurrence store unavailable")
	}
	workstreamID, exists, err := findTaskCenterWorkstream(ctx, s.db, domainworkstream.TaskPlanDataScopeUser, access.OwnerUserID)
	if err != nil {
		return domainmeeting.Meeting{}, false, err
	}
	if !exists {
		return domainmeeting.Meeting{}, false, nil
	}
	stored, _, found, err := findMeetingOccurrence(ctx, s.db, key, workstreamID)
	if err != nil {
		return domainmeeting.Meeting{}, false, err
	}
	foreign, err := meetingOccurrenceExistsOutsideWorkstream(ctx, s.db, key, workstreamID)
	if err != nil {
		return domainmeeting.Meeting{}, false, err
	}
	if foreign {
		return domainmeeting.Meeting{}, false, domainmeeting.ErrConflict
	}
	if !found {
		return domainmeeting.Meeting{}, false, nil
	}
	if !sameMeetingStoreAccess(stored.Access, access) {
		return domainmeeting.Meeting{}, false, domainmeeting.ErrConflict
	}
	return stored.Meeting, true, nil
}

// LoadOrCreateMeetingOccurrence creates the initial occurrence only after the
// reservation INSERT acquires SQLite's writer lock. Replays return the exact
// stored aggregate and never call Resume.
func (s *SQLiteStore) LoadOrCreateMeetingOccurrence(ctx context.Context, access domainmeeting.StoreAccess, key domainmeeting.OccurrenceKey, initial domainmeeting.Meeting) (MeetingOccurrenceRecord, error) {
	if err := validateMeetingStoreCall(ctx, access, key); err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if initial.Key != key || initial.Validate() != nil || !sameActorAudience(access.Audience, initial.Participants) {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrConflict
	}
	if s == nil || s.db == nil {
		return MeetingOccurrenceRecord{}, errors.New("meeting occurrence store unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	defer tx.Rollback()
	reservationID := string(modulecore.NewArtifactID())
	reservation := domainworkstream.Artifact{
		ArtifactID: reservationID, Type: taskMeetingOccurrenceReservationArtifactType, Status: "reserved",
	}
	reservationPayload, err := json.Marshal(reservation)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, '', CURRENT_TIMESTAMP, ?)`, reservationID, string(reservationPayload)); err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	workstreamID, err := findOrCreateTaskCenterWorkstream(ctx, tx, domainworkstream.TaskPlanDataScopeUser, access.OwnerUserID, initial.ScheduledAt)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	stored, raw, found, err := findMeetingOccurrence(ctx, tx, key, workstreamID)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	foreign, err := meetingOccurrenceExistsOutsideWorkstream(ctx, tx, key, workstreamID)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if foreign {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrConflict
	}
	if found {
		if !sameMeetingStoreAccess(stored.Access, access) {
			return MeetingOccurrenceRecord{}, domainmeeting.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifact WHERE artifact_id = ?`, reservationID); err != nil {
			return MeetingOccurrenceRecord{}, err
		}
		if err := tx.Commit(); err != nil {
			return MeetingOccurrenceRecord{}, fmt.Errorf("meeting occurrence replay commit outcome unknown: %w", err)
		}
		return MeetingOccurrenceRecord{Meeting: stored.Meeting, PayloadHash: meetingPayloadHash(raw)}, nil
	}
	envelope := meetingOccurrenceEnvelope{Key: key, Access: cloneMeetingStoreAccess(access), Meeting: initial}
	artifact, payload, err := meetingOccurrenceArtifact(reservationID, workstreamID, envelope)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE artifact SET workstream_id = ?, payload = ?, created_at = CURRENT_TIMESTAMP WHERE artifact_id = ? AND workstream_id = ''`, workstreamID, string(payload), artifact.ArtifactID)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return MeetingOccurrenceRecord{}, fmt.Errorf("meeting occurrence create outcome unknown: %w", err)
	}
	return MeetingOccurrenceRecord{Meeting: initial, PayloadHash: meetingPayloadHash(payload), Created: true}, nil
}

// SaveMeetingOccurrence performs exact-key, guard, and payload CAS. It does not
// call external owners; callers must gather immutable evidence before entry.
func (s *SQLiteStore) SaveMeetingOccurrence(ctx context.Context, access domainmeeting.StoreAccess, key domainmeeting.OccurrenceKey, expected domainmeeting.Guard, expectedPayloadHash string, next domainmeeting.Meeting) (MeetingOccurrenceRecord, error) {
	if err := validateMeetingStoreCall(ctx, access, key); err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if next.Key != key || next.Validate() != nil || !sameActorAudience(access.Audience, next.Participants) || next.Guard().ProgressionRevision != expected.ProgressionRevision+1 ||
		next.Guard().Generation < expected.Generation || next.Guard().Epoch < expected.Epoch || expectedPayloadHash == "" {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrConflict
	}
	if s == nil || s.db == nil {
		return MeetingOccurrenceRecord{}, errors.New("meeting occurrence store unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	defer tx.Rollback()
	reservationID := string(modulecore.NewArtifactID())
	reservation := domainworkstream.Artifact{ArtifactID: reservationID, Type: taskMeetingOccurrenceReservationArtifactType, Status: "reserved"}
	reservationPayload, err := json.Marshal(reservation)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, '', CURRENT_TIMESTAMP, ?)`, reservationID, string(reservationPayload)); err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	workstreamID, err := findOrCreateTaskCenterWorkstream(ctx, tx, domainworkstream.TaskPlanDataScopeUser, access.OwnerUserID, next.ScheduledAt)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	current, raw, found, err := findMeetingOccurrence(ctx, tx, key, workstreamID)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	foreign, err := meetingOccurrenceExistsOutsideWorkstream(ctx, tx, key, workstreamID)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if foreign {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrConflict
	}
	if !found || !sameMeetingStoreAccess(current.Access, access) || current.Meeting.Guard() != expected || meetingPayloadHash(raw) != expectedPayloadHash {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrStale
	}
	if err := requireManagedWorkstream(ctx, tx, workstreamID, domainworkstream.TaskPlanDataScopeUser, access.OwnerUserID); err != nil {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrConflict
	}
	if err := validateMeetingCASProjection(current.Meeting, next); err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if err := validateMeetingDowngradeAgainstMinutesTx(ctx, tx, key, modulecore.WorkstreamID(workstreamID), current.Meeting, next); err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifact WHERE artifact_id = ?`, reservationID); err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	artifactID, artifactWorkstreamID, artifactPayload, err := findMeetingArtifactID(raw)
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if artifactWorkstreamID != workstreamID {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrConflict
	}
	_, updatedPayload, err := meetingOccurrenceArtifact(artifactID, workstreamID, meetingOccurrenceEnvelope{Key: key, Access: cloneMeetingStoreAccess(access), Meeting: next})
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE artifact SET payload = ?, created_at = CURRENT_TIMESTAMP WHERE artifact_id = ? AND payload = ?`, string(updatedPayload), artifactID, string(artifactPayload))
	if err != nil {
		return MeetingOccurrenceRecord{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return MeetingOccurrenceRecord{}, domainmeeting.ErrStale
	}
	if err := tx.Commit(); err != nil {
		return MeetingOccurrenceRecord{}, fmt.Errorf("meeting occurrence CAS outcome unknown: %w", err)
	}
	return MeetingOccurrenceRecord{Meeting: next, PayloadHash: meetingPayloadHash(updatedPayload)}, nil
}

func validateMeetingDowngradeAgainstMinutesTx(ctx context.Context, tx *sql.Tx, key domainmeeting.OccurrenceKey, workstreamID modulecore.WorkstreamID, current, next domainmeeting.Meeting) error {
	if current.Status != domainmeeting.StatusFinishing || next.Status != domainmeeting.StatusPartial && next.Status != domainmeeting.StatusMissed {
		return nil
	}
	minutes, found, err := findTaskMeetingMinutesTx(ctx, tx, key, workstreamID)
	if err != nil || !found {
		return err
	}
	var artifact TaskMeetingMinutesArtifact
	if json.Unmarshal(minutes.Artifact.Payload, &artifact) != nil || validateTaskMeetingMinutesArtifact(artifact) != nil {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	if artifact.Transcript.Outcome == domainmeeting.StatusCompleted && artifact.Transcript.Guard == current.Guard() && artifact.Transcript.TranscriptRequestID == current.TranscriptRequestID {
		return domainmeeting.ErrConflict
	}
	return nil
}

func validateMeetingStoreCall(ctx context.Context, access domainmeeting.StoreAccess, key domainmeeting.OccurrenceKey) error {
	if ctx == nil || key.Validate() != nil || access.CoordinatorActorID == "" || access.OwnerUserID == "" || access.AuthorityEvidence.Validate() != nil || len(access.Audience) != 8 {
		return domainmeeting.ErrConflict
	}
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != tool.ActorKindAgent || scope.ActorID != access.CoordinatorActorID ||
		scope.AuthenticatedUserID != access.OwnerUserID || !scope.Allows(tool.DataScopeUser) ||
		scope.AuthenticationSource != tool.AuthenticationSourceAgentOrchestrator {
		return domainmeeting.ErrConflict
	}
	seen := map[string]bool{}
	for _, actor := range access.Audience {
		if actor == "" || actor != strings.TrimSpace(actor) || seen[actor] {
			return domainmeeting.ErrConflict
		}
		seen[actor] = true
	}
	return nil
}

func validateMeetingCASProjection(current, next domainmeeting.Meeting) error {
	if current.Key != next.Key || current.ScheduleRevision != next.ScheduleRevision || !sameCanonicalMeetingTiming(current.Timing, next.Timing) ||
		!reflect.DeepEqual(current.Participants, next.Participants) || !reflect.DeepEqual(current.WorldActorBindings, next.WorldActorBindings) ||
		current.RegistrationEvidenceID != next.RegistrationEvidenceID || current.Rooms != next.Rooms ||
		current.WorldBaseURL != next.WorldBaseURL || current.WorldCredentialEnv != next.WorldCredentialEnv || current.WorldID != next.WorldID ||
		current.TimelineID != next.TimelineID || current.TimelineEpoch != next.TimelineEpoch ||
		current.MeetingDeadlineAfterSecs != next.MeetingDeadlineAfterSecs || !current.ScheduledAt.Equal(next.ScheduledAt) || !current.DeadlineAt.Equal(next.DeadlineAt) {
		return domainmeeting.ErrConflict
	}
	if current.TaskRef == nil && current.RunRef == nil && current.TaskManagerEvidenceID == "" {
		if next.TaskRef != nil || next.RunRef != nil || next.TaskManagerEvidenceID != "" {
			if next.TaskRef == nil || next.RunRef == nil || next.TaskRef.Validate() != nil || next.RunRef.Validate() != nil || next.TaskManagerEvidenceID.Validate() != nil {
				return domainmeeting.ErrConflict
			}
		}
	} else if !sameNativeReference(current.TaskRef, next.TaskRef) || !sameNativeReference(current.RunRef, next.RunRef) || current.TaskManagerEvidenceID != next.TaskManagerEvidenceID {
		return domainmeeting.ErrConflict
	}
	generationDelta := next.Generation - current.Generation
	epochDelta := next.Epoch - current.Epoch
	if generationDelta == 0 && epochDelta == 0 {
		return nil
	}
	if generationDelta != 1 || epochDelta != 1 {
		return domainmeeting.ErrConflict
	}
	priorClaims := make(map[modulecore.EvidenceID]bool, len(current.EvidenceClaims))
	for _, claim := range current.EvidenceClaims {
		priorClaims[claim.EvidenceID] = true
	}
	resumeClaims := 0
	for _, claim := range next.EvidenceClaims {
		if !priorClaims[claim.EvidenceID] && claim.Kind == "resume" {
			resumeClaims++
		}
	}
	if resumeClaims != 1 {
		return domainmeeting.ErrConflict
	}
	return nil
}

func sameCanonicalMeetingTiming(left, right modulecore.TaskTiming) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func sameNativeReference[T ~string](left, right *T) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type meetingOccurrenceQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func findMeetingOccurrence(ctx context.Context, queryer meetingOccurrenceQueryer, key domainmeeting.OccurrenceKey, workstreamID string) (meetingOccurrenceEnvelope, []byte, bool, error) {
	query := `SELECT payload FROM artifact WHERE workstream_id = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.artifact_type') END = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.payload.key.schedule_id') END = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.payload.key.local_date') END = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.payload.key.slot') END = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.payload.key.timezone') END = ?`
	rows, err := queryer.QueryContext(ctx, query, workstreamID, taskMeetingOccurrenceArtifactType, string(key.ScheduleID), key.LocalDate, key.Slot, key.Timezone)
	if err != nil {
		return meetingOccurrenceEnvelope{}, nil, false, err
	}
	defer rows.Close()
	var selected meetingOccurrenceEnvelope
	var selectedRaw []byte
	found := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return meetingOccurrenceEnvelope{}, nil, false, err
		}
		var artifact domainworkstream.Artifact
		if err := json.Unmarshal(raw, &artifact); err != nil {
			return meetingOccurrenceEnvelope{}, nil, false, err
		}
		var envelope meetingOccurrenceEnvelope
		if err := json.Unmarshal(artifact.Payload, &envelope); err != nil {
			return meetingOccurrenceEnvelope{}, nil, false, err
		}
		if envelope.Key == key {
			if found {
				return meetingOccurrenceEnvelope{}, nil, false, domainmeeting.ErrConflict
			}
			if artifact.Type != taskMeetingOccurrenceArtifactType || artifact.WorkstreamID != workstreamID || modulecore.ArtifactID(artifact.ArtifactID).Validate() != nil ||
				modulecore.WorkstreamID(artifact.WorkstreamID).Validate() != nil || envelope.Meeting.Key != key ||
				envelope.Meeting.Validate() != nil || !sameActorAudience(envelope.Access.Audience, envelope.Meeting.Participants) {
				return meetingOccurrenceEnvelope{}, nil, false, domainmeeting.ErrConflict
			}
			selected, selectedRaw, found = envelope, append([]byte(nil), raw...), true
		}
	}
	if err := rows.Err(); err != nil {
		return meetingOccurrenceEnvelope{}, nil, false, err
	}
	return selected, selectedRaw, found, nil
}

type meetingOccurrenceQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func meetingOccurrenceExistsOutsideWorkstream(ctx context.Context, queryer meetingOccurrenceQueryRower, key domainmeeting.OccurrenceKey, workstreamID string) (bool, error) {
	query := `SELECT 1 FROM artifact WHERE COALESCE(workstream_id, '') <> ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.artifact_type') END = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.payload.key.schedule_id') END = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.payload.key.local_date') END = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.payload.key.slot') END = ? AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.payload.key.timezone') END = ? LIMIT 1`
	var one int
	err := queryer.QueryRowContext(ctx, query, workstreamID, taskMeetingOccurrenceArtifactType, string(key.ScheduleID), key.LocalDate, key.Slot, key.Timezone).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func findMeetingArtifactID(raw []byte) (string, string, []byte, error) {
	var artifact domainworkstream.Artifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		return "", "", nil, err
	}
	if modulecore.ArtifactID(artifact.ArtifactID).Validate() != nil || modulecore.WorkstreamID(artifact.WorkstreamID).Validate() != nil {
		return "", "", nil, domainmeeting.ErrConflict
	}
	return artifact.ArtifactID, artifact.WorkstreamID, raw, nil
}

func meetingOccurrenceArtifact(id, workstreamID string, envelope meetingOccurrenceEnvelope) (domainworkstream.Artifact, []byte, error) {
	inner, err := json.Marshal(envelope)
	if err != nil {
		return domainworkstream.Artifact{}, nil, err
	}
	artifact := domainworkstream.Artifact{
		ArtifactID: id, WorkstreamID: workstreamID, Type: taskMeetingOccurrenceArtifactType,
		Title: "Scheduled task meeting occurrence", Status: envelope.Meeting.Status,
		Payload: inner, CreatedAt: envelope.Meeting.ScheduledAt, UpdatedAt: envelope.Meeting.ScheduledAt,
	}
	payload, err := json.Marshal(artifact)
	return artifact, payload, err
}

func meetingPayloadHash(payload []byte) string {
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func cloneMeetingStoreAccess(access domainmeeting.StoreAccess) domainmeeting.StoreAccess {
	access.Audience = append([]string(nil), access.Audience...)
	return access
}

func sameMeetingStoreAccess(left, right domainmeeting.StoreAccess) bool {
	left, right = cloneMeetingStoreAccess(left), cloneMeetingStoreAccess(right)
	sort.Strings(left.Audience)
	sort.Strings(right.Audience)
	// Authority evidence is a fresh proof for the current call. The immutable
	// owner/coordinator/audience binding is what must match the stored record.
	left.AuthorityEvidence, right.AuthorityEvidence = modulecore.EvidenceID(""), modulecore.EvidenceID("")
	return reflect.DeepEqual(left, right)
}

func sameActorAudience(left, right []string) bool {
	left, right = append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return reflect.DeepEqual(left, right)
}
