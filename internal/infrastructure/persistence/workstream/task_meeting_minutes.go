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
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskMeetingMinutesSchemaVersion = 2
const taskMeetingMinutesMaxBytes = 2 << 20

// TaskMeetingMinutesMessageProof pins a canonical message to the EventStore
// delivery receipt and all eight TaskManager consumption proofs. It contains
// references and hashes only; message bodies appear only in the transcript.
type TaskMeetingMinutesMessageProof struct {
	DeliveryState     string                                  `json:"delivery_state"`
	MessageID         modulecore.MessageID                    `json:"message_id"`
	Reference         domaintask.TaskMeetingMessageReference  `json:"reference"`
	SenderActorID     string                                  `json:"sender_actor_id"`
	SenderTaskID      modulecore.TaskID                       `json:"sender_task_id"`
	SenderRunID       modulecore.RunID                        `json:"sender_run_id"`
	Delivery          domainmeeting.DeliveryEvidence          `json:"delivery"`
	ConsumptionProofs []domaintask.TaskMeetingConsumedMessage `json:"consumption_proofs"`
}

// TaskMeetingMinutesRequest is verified against the stored occurrence and
// actual EventStore receipts before the derived transcript is written.
type TaskMeetingMinutesRequest struct {
	WorkstreamID           modulecore.WorkstreamID          `json:"workstream_id"`
	OwnerUserID            string                           `json:"owner_user_id"`
	CoordinatorActorID     string                           `json:"coordinator_actor_id"`
	RegistrationEvidenceID modulecore.EvidenceID            `json:"registration_evidence_id"`
	ConfigSHA256           string                           `json:"config_sha256"`
	Meeting                domainmeeting.Meeting            `json:"meeting"`
	Messages               []TaskMeetingMinutesMessageProof `json:"messages"`
}

type TaskMeetingMinutesTranscript struct {
	WorkstreamID           modulecore.WorkstreamID          `json:"workstream_id"`
	OwnerUserID            string                           `json:"owner_user_id"`
	CoordinatorActorID     string                           `json:"coordinator_actor_id"`
	OccurrenceKey          domainmeeting.OccurrenceKey      `json:"occurrence_key"`
	TranscriptRequestID    string                           `json:"transcript_request_id"`
	RegistrationEvidenceID modulecore.EvidenceID            `json:"registration_evidence_id"`
	ConfigRevision         uint64                           `json:"config_revision"`
	ConfigSHA256           string                           `json:"config_sha256"`
	Audience               []string                         `json:"audience"`
	Guard                  domainmeeting.Guard              `json:"guard"`
	ScheduledAt            time.Time                        `json:"scheduled_at"`
	Outcome                string                           `json:"outcome"`
	ReasonCode             string                           `json:"reason_code,omitempty"`
	Unresolved             []string                         `json:"unresolved,omitempty"`
	MessageOrder           []modulecore.MessageID           `json:"message_order"`
	Reports                []TaskMeetingMinutesReport       `json:"reports"`
	Answers                []TaskMeetingMinutesAnswer       `json:"answers"`
	Messages               []TaskMeetingMinutesMessageProof `json:"messages"`
}

type TaskMeetingMinutesReport struct {
	MessageID          modulecore.MessageID      `json:"message_id"`
	ActorID            string                    `json:"actor_id"`
	Body               string                    `json:"body"`
	Current            domainmeeting.ReportField `json:"current"`
	Next               domainmeeting.ReportField `json:"next"`
	ConcernDeclaration string                    `json:"concern_declaration"`
	Concerns           []domainmeeting.Concern   `json:"concerns"`
	SpeakerEvidenceID  modulecore.EvidenceID     `json:"speaker_evidence_id"`
	SpeakerEvidenceSHA string                    `json:"speaker_evidence_sha256"`
	SourceTaskID       modulecore.TaskID         `json:"source_task_id"`
	SourceRunID        modulecore.RunID          `json:"source_run_id"`
}

type TaskMeetingMinutesAnswer struct {
	MessageID          modulecore.MessageID    `json:"message_id"`
	ActorID            string                  `json:"actor_id"`
	Body               string                  `json:"body"`
	ConcernID          domainmeeting.ConcernID `json:"concern_id"`
	ReporterID         string                  `json:"reporter_id"`
	Kind               string                  `json:"kind"`
	SpeakerEvidenceID  modulecore.EvidenceID   `json:"speaker_evidence_id"`
	SpeakerEvidenceSHA string                  `json:"speaker_evidence_sha256"`
	SourceTaskID       modulecore.TaskID       `json:"source_task_id"`
	SourceRunID        modulecore.RunID        `json:"source_run_id"`
}

type TaskMeetingMinutesArtifact struct {
	SchemaVersion int                          `json:"schema_version"`
	Transcript    TaskMeetingMinutesTranscript `json:"transcript"`
	InputSHA256   string                       `json:"input_sha256"`
}

type TaskMeetingMinutesReference struct {
	ArtifactID             modulecore.ArtifactID   `json:"artifact_id"`
	WorkstreamID           modulecore.WorkstreamID `json:"workstream_id"`
	OwnerUserID            string                  `json:"owner_user_id"`
	CoordinatorActorID     string                  `json:"coordinator_actor_id"`
	OccurrenceKey          string                  `json:"occurrence_key"`
	RegistrationEvidenceID modulecore.EvidenceID   `json:"registration_evidence_id"`
	ConfigRevision         uint64                  `json:"config_revision"`
	ConfigSHA256           string                  `json:"config_sha256"`
	Audience               []string                `json:"audience"`
	AudienceSHA256         string                  `json:"audience_sha256"`
	Guard                  domainmeeting.Guard     `json:"guard"`
	TranscriptSHA256       string                  `json:"transcript_sha256"`
	CreatedAt              time.Time               `json:"created_at"`
}

type TaskMeetingMinutesProofCheck func(context.Context, TaskMeetingMinutesRequest) error
type TaskMeetingMinutesReadAuthorizationCheck func(context.Context, TaskMeetingMinutesReference) error
type TaskMeetingMinutesFindAuthorizationCheck func(context.Context, string, domainmeeting.OccurrenceKey, string, domainmeeting.Guard, *TaskMeetingMinutesReference) error

type TaskMeetingOccurrenceSummary struct {
	Key                    domainmeeting.OccurrenceKey  `json:"occurrence_key"`
	WorkstreamID           modulecore.WorkstreamID      `json:"workstream_id"`
	OwnerUserID            string                       `json:"owner_user_id"`
	CoordinatorActorID     string                       `json:"coordinator_actor_id"`
	Audience               []string                     `json:"audience"`
	ScheduleRevision       uint64                       `json:"schedule_revision"`
	RegistrationEvidenceID modulecore.EvidenceID        `json:"registration_evidence_id"`
	Status                 string                       `json:"status"`
	ScheduledAt            time.Time                    `json:"scheduled_at"`
	DeadlineAt             time.Time                    `json:"deadline_at"`
	LastCheckpointAt       time.Time                    `json:"last_checkpoint_at"`
	FinishProofPersistedAt *time.Time                   `json:"finish_proof_persisted_at,omitempty"`
	PartialObservedAt      *time.Time                   `json:"partial_observed_at,omitempty"`
	Guard                  domainmeeting.Guard          `json:"guard"`
	TaskRef                *modulecore.TaskID           `json:"task_ref,omitempty"`
	RunRef                 *modulecore.RunID            `json:"run_ref,omitempty"`
	ReportCount            int                          `json:"report_count"`
	AnswerCount            int                          `json:"answer_count"`
	DeliveredMessageCount  int                          `json:"delivered_message_count"`
	AttendanceCount        *int                         `json:"attendance_count,omitempty"`
	AttendanceScopeID      modulecore.EvidenceID        `json:"attendance_scope_evidence_id,omitempty"`
	AttendanceObservedAt   *time.Time                   `json:"attendance_observed_at,omitempty"`
	DeliveredReportCount   int                          `json:"delivered_report_count"`
	ReportedConcernCount   int                          `json:"reported_concern_count"`
	AnsweredConcernCount   int                          `json:"answered_concern_count"`
	UnresolvedConcernCount int                          `json:"unresolved_concern_count"`
	Minutes                *TaskMeetingMinutesReference `json:"minutes,omitempty"`
}

type TaskMeetingOccurrenceList struct {
	Occurrences []TaskMeetingOccurrenceSummary `json:"occurrences"`
	TotalCount  int                            `json:"total_count"`
	HasMore     bool                           `json:"has_more"`
}

type TaskMeetingOccurrenceListAuthorizationCheck func(context.Context, modulecore.WorkstreamID, string, modulecore.ScheduleID, *TaskMeetingOccurrenceSummary) error

// PersistTaskMeetingMinutes commits an immutable derived transcript to the
// existing Workstream artifact table. Proof verification runs outside SQL.
func (s *SQLiteStore) PersistTaskMeetingMinutes(ctx context.Context, request TaskMeetingMinutesRequest, verifyProofs TaskMeetingMinutesProofCheck) (TaskMeetingMinutesReference, error) {
	if s == nil || s.db == nil || ctx == nil || verifyProofs == nil {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanUnavailable
	}
	if request.OwnerUserID == "" || request.OwnerUserID != strings.TrimSpace(request.OwnerUserID) || request.Meeting.Key.Validate() != nil {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanInvalid
	}
	if request.Meeting.TranscriptRequestID == "" || request.Meeting.TranscriptRequestID != strings.TrimSpace(request.Meeting.TranscriptRequestID) {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanInvalid
	}
	actualWorkstreamID, err := findTaskMeetingOccurrenceWorkstream(ctx, s.db, request.OwnerUserID, request.Meeting.Key)
	if err != nil {
		return TaskMeetingMinutesReference{}, err
	}
	if request.WorkstreamID != "" && request.WorkstreamID != actualWorkstreamID {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanConflict
	}
	request.WorkstreamID = actualWorkstreamID
	transcript, err := projectTaskMeetingMinutes(request)
	if err != nil {
		return TaskMeetingMinutesReference{}, err
	}
	if err := verifyProofs(ctx, cloneTaskMeetingMinutesRequest(request)); err != nil {
		return TaskMeetingMinutesReference{}, err
	}
	transcriptJSON, err := json.Marshal(transcript)
	if err != nil || len(transcriptJSON) > taskMeetingMinutesMaxBytes {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanInvalid
	}
	inputHash := sha256Hex(transcriptJSON)
	encoded, err := json.Marshal(TaskMeetingMinutesArtifact{SchemaVersion: taskMeetingMinutesSchemaVersion, Transcript: transcript, InputSHA256: inputHash})
	if err != nil || len(encoded) > taskMeetingMinutesMaxBytes {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskMeetingMinutesReference{}, classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	if err := validateTaskMeetingMinutesOccurrenceTx(ctx, tx, request); err != nil {
		return TaskMeetingMinutesReference{}, err
	}
	if current, found, err := findTaskMeetingMinutesTx(ctx, tx, request.Meeting.Key, request.WorkstreamID); err != nil {
		return TaskMeetingMinutesReference{}, err
	} else if found {
		if current.InputSHA256 != inputHash || !bytes.Equal(current.TranscriptJSON, transcriptJSON) {
			return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanConflict
		}
		if err := tx.Commit(); err != nil {
			return TaskMeetingMinutesReference{}, classifyTaskPlanStorageError(err)
		}
		return taskMeetingMinutesReference(current.Artifact, current.CreatedAt), nil
	}
	// A finished transcript may be created only while the frozen occurrence is
	// still before its deadline. Existing canonical artifacts above remain
	// replayable after the deadline so the owner can roll forward Finish.
	if request.Meeting.Status == domainmeeting.StatusFinishing && !s.taskMeetingNowUTC().Before(request.Meeting.DeadlineAt) {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanConflict
	}
	createdAt := s.taskMeetingNowUTC()
	if request.Meeting.Status == domainmeeting.StatusFinishing && !createdAt.Before(request.Meeting.DeadlineAt) {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanConflict
	}
	reservationID := string(modulecore.NewArtifactID())
	reservation := domainworkstream.Artifact{ArtifactID: reservationID, Type: taskMeetingMinutesReservationArtifactType, Status: "reserved"}
	reservationJSON, err := json.Marshal(reservation)
	if err != nil {
		return TaskMeetingMinutesReference{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact (artifact_id,workstream_id,created_at,payload) VALUES (?,'',?,?)`, reservationID, createdAt.Format(timeFormatRFC3339Nano), string(reservationJSON)); err != nil {
		return TaskMeetingMinutesReference{}, classifyTaskPlanStorageError(err)
	}
	artifact := domainworkstream.Artifact{ArtifactID: reservationID, WorkstreamID: string(request.WorkstreamID), Type: taskMeetingMinutesArtifactType,
		Title: "TaskMeeting minutes", Status: "stored", Payload: encoded, CreatedAt: createdAt}
	if err := domainworkstream.ValidateArtifact(artifact); err != nil {
		return TaskMeetingMinutesReference{}, err
	}
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		return TaskMeetingMinutesReference{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE artifact SET workstream_id=?,created_at=?,payload=? WHERE artifact_id=? AND workstream_id='' AND json_extract(payload,'$.artifact_type')=?`, request.WorkstreamID, createdAt.Format(timeFormatRFC3339Nano), string(artifactJSON), reservationID, taskMeetingMinutesReservationArtifactType)
	if err != nil {
		return TaskMeetingMinutesReference{}, classifyTaskPlanStorageError(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return TaskMeetingMinutesReference{}, domainworkstream.ErrTaskPlanConflict
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingMinutesReference{}, fmt.Errorf("TaskMeeting minutes artifact commit outcome unknown: %w", classifyTaskPlanStorageError(err))
	}
	return taskMeetingMinutesReference(artifact, createdAt), nil
}

// ReadTaskMeetingMinutes authenticates the exact owner/audience reference
// before querying the private body, then rechecks it after canonical decoding.
func (s *SQLiteStore) ReadTaskMeetingMinutes(ctx context.Context, expected TaskMeetingMinutesReference, authorize TaskMeetingMinutesReadAuthorizationCheck) (TaskMeetingMinutesArtifact, error) {
	if s == nil || s.db == nil || ctx == nil || authorize == nil || validateTaskMeetingMinutesReference(expected) != nil {
		return TaskMeetingMinutesArtifact{}, domainworkstream.ErrTaskPlanForbidden
	}
	if err := authorize(ctx, expected); err != nil {
		return TaskMeetingMinutesArtifact{}, err
	}
	var rowPayload, createdAt string
	if err := s.db.QueryRowContext(ctx, `SELECT payload,created_at FROM artifact WHERE artifact_id=?`, string(expected.ArtifactID)).Scan(&rowPayload, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TaskMeetingMinutesArtifact{}, domainworkstream.ErrTaskPlanNotFound
		}
		return TaskMeetingMinutesArtifact{}, classifyTaskPlanStorageError(err)
	}
	var row domainworkstream.Artifact
	var artifact TaskMeetingMinutesArtifact
	if json.Unmarshal([]byte(rowPayload), &row) != nil || row.ArtifactID != string(expected.ArtifactID) || row.Type != taskMeetingMinutesArtifactType ||
		json.Unmarshal(row.Payload, &artifact) != nil || validateTaskMeetingMinutesArtifact(artifact) != nil {
		return TaskMeetingMinutesArtifact{}, domainworkstream.ErrTaskPlanNotFound
	}
	transcriptJSON, err := json.Marshal(artifact.Transcript)
	if err != nil || sha256Hex(transcriptJSON) != artifact.InputSHA256 {
		return TaskMeetingMinutesArtifact{}, domainworkstream.ErrTaskPlanUnavailable
	}
	storedAt, err := time.Parse(timeFormatRFC3339Nano, createdAt)
	if err != nil {
		return TaskMeetingMinutesArtifact{}, domainworkstream.ErrTaskPlanUnavailable
	}
	actual := taskMeetingMinutesReference(row, storedAt)
	if !sameTaskMeetingMinutesReference(expected, actual) {
		return TaskMeetingMinutesArtifact{}, domainworkstream.ErrTaskPlanNotFound
	}
	var current string
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM artifact WHERE artifact_id=?`, string(expected.ArtifactID)).Scan(&current); err != nil || current != rowPayload {
		return TaskMeetingMinutesArtifact{}, domainworkstream.ErrTaskPlanConflict
	}
	if err := authorize(ctx, actual); err != nil {
		return TaskMeetingMinutesArtifact{}, err
	}
	return artifact, nil
}

// FindTaskMeetingMinutes resolves an exact pre-authorized occurrence/request
// receipt without requiring the caller to already know the owner-issued
// ArtifactID. The first authorization runs before transcript bytes are read;
// the second binds the exact persisted artifact reference before it returns.
func (s *SQLiteStore) FindTaskMeetingMinutes(ctx context.Context, ownerUserID string, key domainmeeting.OccurrenceKey, requestID string, guard domainmeeting.Guard, authorize TaskMeetingMinutesFindAuthorizationCheck) (TaskMeetingMinutesReference, bool, error) {
	if s == nil || s.db == nil || ctx == nil || ownerUserID == "" || ownerUserID != strings.TrimSpace(ownerUserID) || key.Validate() != nil ||
		requestID == "" || requestID != strings.TrimSpace(requestID) || guard.Generation == 0 || guard.Epoch == 0 || guard.ProgressionRevision == 0 || authorize == nil {
		return TaskMeetingMinutesReference{}, false, domainworkstream.ErrTaskPlanForbidden
	}
	if err := authorize(ctx, ownerUserID, key, requestID, guard, nil); err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	workstreamID, err := findTaskMeetingOccurrenceWorkstream(ctx, s.db, ownerUserID, key)
	if err != nil {
		if errors.Is(err, domainworkstream.ErrTaskPlanNotFound) {
			return TaskMeetingMinutesReference{}, false, nil
		}
		return TaskMeetingMinutesReference{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingMinutesReference{}, false, classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	stored, found, err := findTaskMeetingMinutesTx(ctx, tx, key, workstreamID)
	if err != nil || !found {
		return TaskMeetingMinutesReference{}, false, err
	}
	occurrence, _, occurrenceFound, err := findMeetingOccurrence(ctx, tx, key, string(workstreamID))
	if err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	if !occurrenceFound || occurrence.Access.OwnerUserID != ownerUserID || occurrence.Meeting.Guard() != guard || occurrence.Meeting.TranscriptRequestID != requestID {
		return TaskMeetingMinutesReference{}, false, domainworkstream.ErrTaskPlanNotFound
	}
	var artifact TaskMeetingMinutesArtifact
	if json.Unmarshal(stored.Artifact.Payload, &artifact) != nil || validateTaskMeetingMinutesArtifact(artifact) != nil ||
		artifact.Transcript.OwnerUserID != ownerUserID || artifact.Transcript.TranscriptRequestID != requestID || artifact.Transcript.Guard != guard || artifact.Transcript.OccurrenceKey != key ||
		artifact.Transcript.RegistrationEvidenceID != occurrence.Meeting.RegistrationEvidenceID || artifact.Transcript.ConfigRevision != occurrence.Meeting.ScheduleRevision ||
		artifact.Transcript.Outcome == domainmeeting.StatusCompleted && occurrence.Meeting.Status != domainmeeting.StatusFinishing && occurrence.Meeting.Status != domainmeeting.StatusCompleted ||
		artifact.Transcript.Outcome == domainmeeting.StatusPartial && occurrence.Meeting.Status != domainmeeting.StatusPartial ||
		artifact.Transcript.Outcome == domainmeeting.StatusMissed && occurrence.Meeting.Status != domainmeeting.StatusMissed {
		return TaskMeetingMinutesReference{}, false, domainworkstream.ErrTaskPlanNotFound
	}
	ref := taskMeetingMinutesReference(stored.Artifact, stored.CreatedAt)
	if err := authorize(ctx, ownerUserID, key, requestID, guard, &ref); err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingMinutesReference{}, false, classifyTaskPlanStorageError(err)
	}
	return ref, true, nil
}

// ListTaskMeetingOccurrenceSummaries returns a bounded owner-scoped index of
// persisted occurrences and real minutes references. It never includes bodies
// or reads across the supplied WorkstreamID.
func (s *SQLiteStore) ListTaskMeetingOccurrenceSummaries(ctx context.Context, workstreamID modulecore.WorkstreamID, ownerUserID string, scheduleID modulecore.ScheduleID, limit int, authorize TaskMeetingOccurrenceListAuthorizationCheck) (TaskMeetingOccurrenceList, error) {
	if s == nil || s.db == nil || ctx == nil || workstreamID.Validate() != nil || ownerUserID == "" || ownerUserID != strings.TrimSpace(ownerUserID) || scheduleID.Validate() != nil || limit < 1 || limit > 50 || authorize == nil {
		return TaskMeetingOccurrenceList{}, domainworkstream.ErrTaskPlanInvalid
	}
	if err := authorize(ctx, workstreamID, ownerUserID, scheduleID, nil); err != nil {
		return TaskMeetingOccurrenceList{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingOccurrenceList{}, classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	if err := requireManagedWorkstream(ctx, tx, string(workstreamID), domainworkstream.TaskPlanDataScopeUser, ownerUserID); err != nil {
		return TaskMeetingOccurrenceList{}, domainworkstream.ErrTaskPlanForbidden
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact WHERE workstream_id=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.artifact_type') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.access.owner_user_id') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.key.schedule_id') END=?`, string(workstreamID), taskMeetingOccurrenceArtifactType, ownerUserID, string(scheduleID)).Scan(&total); err != nil {
		return TaskMeetingOccurrenceList{}, classifyTaskPlanStorageError(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT artifact_id,created_at,payload FROM artifact WHERE workstream_id=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.artifact_type') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.access.owner_user_id') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.key.schedule_id') END=? ORDER BY json_extract(payload,'$.payload.meeting.scheduled_at') DESC,artifact_id DESC LIMIT ?`, string(workstreamID), taskMeetingOccurrenceArtifactType, ownerUserID, string(scheduleID), limit+1)
	if err != nil {
		return TaskMeetingOccurrenceList{}, classifyTaskPlanStorageError(err)
	}
	defer rows.Close()
	type storedOccurrence struct{ id, storedAt, raw string }
	storedRows := make([]storedOccurrence, 0, min(limit+1, total))
	for rows.Next() {
		var item storedOccurrence
		if err := rows.Scan(&item.id, &item.storedAt, &item.raw); err != nil {
			return TaskMeetingOccurrenceList{}, err
		}
		storedRows = append(storedRows, item)
	}
	if err := rows.Err(); err != nil {
		return TaskMeetingOccurrenceList{}, err
	}
	if err := rows.Close(); err != nil {
		return TaskMeetingOccurrenceList{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingOccurrenceList{}, classifyTaskPlanStorageError(err)
	}
	result := TaskMeetingOccurrenceList{Occurrences: make([]TaskMeetingOccurrenceSummary, 0, min(limit, total)), TotalCount: total, HasMore: total > limit}
	for _, storedRow := range storedRows {
		var artifact domainworkstream.Artifact
		var envelope meetingOccurrenceEnvelope
		if json.Unmarshal([]byte(storedRow.raw), &artifact) != nil || json.Unmarshal(artifact.Payload, &envelope) != nil || artifact.ArtifactID != storedRow.id || artifact.Type != taskMeetingOccurrenceArtifactType || artifact.WorkstreamID != string(workstreamID) ||
			envelope.Access.OwnerUserID != ownerUserID || envelope.Key.ScheduleID != scheduleID || envelope.Meeting.Key != envelope.Key || envelope.Meeting.Validate() != nil {
			return TaskMeetingOccurrenceList{}, domainworkstream.ErrTaskPlanUnavailable
		}
		checkpointAt, err := parseTaskMeetingArtifactTimestamp(storedRow.storedAt)
		if err != nil {
			return TaskMeetingOccurrenceList{}, err
		}
		summary := taskMeetingOccurrenceSummary(envelope, workstreamID, checkpointAt.UTC())
		if err := authorize(ctx, workstreamID, ownerUserID, scheduleID, &summary); err != nil {
			return TaskMeetingOccurrenceList{}, err
		}
		if minutes, found, err := findTaskMeetingMinutesReference(ctx, s.db, workstreamID, summary, envelope.Meeting); err != nil {
			return TaskMeetingOccurrenceList{}, err
		} else if found {
			summary.Minutes = &minutes
		}
		result.Occurrences = append(result.Occurrences, summary)
		if len(result.Occurrences) == limit {
			break
		}
	}
	return result, nil
}

func taskMeetingOccurrenceSummary(envelope meetingOccurrenceEnvelope, workstreamID modulecore.WorkstreamID, checkpointAt time.Time) TaskMeetingOccurrenceSummary {
	meeting := envelope.Meeting
	summary := TaskMeetingOccurrenceSummary{Key: envelope.Key, WorkstreamID: workstreamID, OwnerUserID: envelope.Access.OwnerUserID, CoordinatorActorID: envelope.Access.CoordinatorActorID,
		Audience:         append([]string(nil), envelope.Access.Audience...),
		ScheduleRevision: meeting.ScheduleRevision, RegistrationEvidenceID: meeting.RegistrationEvidenceID, Status: meeting.Status, ScheduledAt: meeting.ScheduledAt, DeadlineAt: meeting.DeadlineAt,
		LastCheckpointAt: checkpointAt, Guard: meeting.Guard(), TaskRef: cloneModuleTaskID(meeting.TaskRef), RunRef: cloneModuleRunID(meeting.RunRef),
		ReportCount: len(meeting.Reports), AnswerCount: len(meeting.Answers), DeliveredMessageCount: deliveredMeetingMessageCount(meeting)}
	if meeting.FinishEvidence != nil {
		value := meeting.FinishEvidence.PersistedAt
		summary.FinishProofPersistedAt = &value
	}
	if meeting.PartialEvidence != nil {
		value := meeting.PartialEvidence.ObservedAt
		summary.PartialObservedAt = &value
	}
	summary.AttendanceCount, summary.AttendanceScopeID, summary.AttendanceObservedAt = taskMeetingObservedAttendance(meeting)
	summary.DeliveredReportCount, summary.ReportedConcernCount, summary.AnsweredConcernCount, summary.UnresolvedConcernCount = taskMeetingConcernCounts(meeting)
	return summary
}

// taskMeetingObservedAttendance reports a historical checkpoint count only
// when every configured participant has an observation attached to the exact
// current scope. It never infers presence from roster membership.
func taskMeetingObservedAttendance(meeting domainmeeting.Meeting) (*int, modulecore.EvidenceID, *time.Time) {
	if meeting.ScopeEvidence == nil || meeting.ScopeEvidence.EvidenceID.Validate() != nil || len(meeting.Participants) == 0 {
		return nil, "", nil
	}
	observed := make(map[string]bool, len(meeting.Participants))
	for _, attendance := range meeting.Attendance {
		evidence := attendance.Evidence
		if evidence.ScopeEvidenceID != meeting.ScopeEvidence.EvidenceID {
			continue
		}
		if _, duplicate := observed[evidence.ActorID]; duplicate {
			return nil, "", nil
		}
		if !containsMeetingActor(meeting.Participants, evidence.ActorID) {
			return nil, "", nil
		}
		observed[evidence.ActorID] = evidence.Present
	}
	if len(observed) != len(meeting.Participants) {
		return nil, "", nil
	}
	present := 0
	for _, actorID := range meeting.Participants {
		if inVenue, exists := observed[actorID]; !exists {
			return nil, "", nil
		} else if inVenue {
			present++
		}
	}
	observedAt := meeting.ScopeEvidence.ObservedAt.UTC()
	return &present, meeting.ScopeEvidence.EvidenceID, &observedAt
}

func taskMeetingConcernCounts(meeting domainmeeting.Meeting) (deliveredReports, reportedConcerns, answeredConcerns, unresolvedConcerns int) {
	concernIDs := make(map[domainmeeting.ConcernID]struct{})
	for _, report := range meeting.Reports {
		if report.Utterance.DeliveryEvidence == nil || !report.Utterance.DeliveryEvidence.Delivered {
			continue
		}
		deliveredReports++
		for index := range report.Concerns {
			concernIDs[domainmeeting.ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: uint32(index + 1)}] = struct{}{}
		}
	}
	answered := make(map[domainmeeting.ConcernID]struct{})
	for _, answer := range meeting.Answers {
		if answer.Utterance.DeliveryEvidence == nil || !answer.Utterance.DeliveryEvidence.Delivered || answer.Utterance.ActorID == answer.ReporterID || answer.Kind == "" {
			continue
		}
		if _, exists := concernIDs[answer.ConcernID]; exists {
			answered[answer.ConcernID] = struct{}{}
		}
	}
	reportedConcerns = len(concernIDs)
	answeredConcerns = len(answered)
	if answeredConcerns > reportedConcerns {
		answeredConcerns = reportedConcerns
	}
	unresolvedConcerns = reportedConcerns - answeredConcerns
	return deliveredReports, reportedConcerns, answeredConcerns, unresolvedConcerns
}

func cloneModuleTaskID(value *modulecore.TaskID) *modulecore.TaskID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneModuleRunID(value *modulecore.RunID) *modulecore.RunID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func deliveredMeetingMessageCount(meeting domainmeeting.Meeting) int {
	count := 0
	for _, report := range meeting.Reports {
		if report.Utterance.DeliveryEvidence != nil && report.Utterance.DeliveryEvidence.Delivered {
			count++
		}
	}
	for _, answer := range meeting.Answers {
		if answer.Utterance.DeliveryEvidence != nil && answer.Utterance.DeliveryEvidence.Delivered {
			count++
		}
	}
	return count
}

func findTaskMeetingOccurrenceWorkstream(ctx context.Context, db *sql.DB, ownerUserID string, key domainmeeting.OccurrenceKey) (modulecore.WorkstreamID, error) {
	if db == nil || ctx == nil || ownerUserID == "" || ownerUserID != strings.TrimSpace(ownerUserID) || key.Validate() != nil {
		return "", domainworkstream.ErrTaskPlanInvalid
	}
	rows, err := db.QueryContext(ctx, `SELECT a.artifact_id,a.workstream_id,a.payload FROM artifact a
		JOIN workstream w ON w.workstream_id=a.workstream_id
		WHERE CASE WHEN json_valid(w.payload) THEN json_type(w.payload,'$.task_center_scope') END='object'
		AND CASE WHEN json_valid(w.payload) THEN json_extract(w.payload,'$.task_center_scope.data_scope') END=?
		AND CASE WHEN json_valid(w.payload) THEN json_extract(w.payload,'$.task_center_scope.user_id') END=?
		AND CASE WHEN json_valid(a.payload) THEN json_extract(a.payload,'$.artifact_type') END=?
		AND CASE WHEN json_valid(a.payload) THEN json_extract(a.payload,'$.payload.access.owner_user_id') END=?
		AND CASE WHEN json_valid(a.payload) THEN json_extract(a.payload,'$.payload.key.schedule_id') END=?
		AND CASE WHEN json_valid(a.payload) THEN json_extract(a.payload,'$.payload.key.local_date') END=?
		AND CASE WHEN json_valid(a.payload) THEN json_extract(a.payload,'$.payload.key.slot') END=?
		AND CASE WHEN json_valid(a.payload) THEN json_extract(a.payload,'$.payload.key.timezone') END=?
		ORDER BY a.created_at,a.artifact_id LIMIT 2`, domainworkstream.TaskPlanDataScopeUser, ownerUserID, taskMeetingOccurrenceArtifactType,
		ownerUserID, string(key.ScheduleID), key.LocalDate, key.Slot, key.Timezone)
	if err != nil {
		return "", classifyTaskPlanStorageError(err)
	}
	defer rows.Close()
	var found modulecore.WorkstreamID
	for rows.Next() {
		var artifactID, workstreamID, raw string
		if err := rows.Scan(&artifactID, &workstreamID, &raw); err != nil {
			return "", classifyTaskPlanStorageError(err)
		}
		var artifact domainworkstream.Artifact
		var envelope meetingOccurrenceEnvelope
		if json.Unmarshal([]byte(raw), &artifact) != nil || json.Unmarshal(artifact.Payload, &envelope) != nil || artifact.ArtifactID != artifactID ||
			artifact.WorkstreamID != workstreamID || artifact.Type != taskMeetingOccurrenceArtifactType || envelope.Access.OwnerUserID != ownerUserID || envelope.Key != key ||
			envelope.Meeting.Key != key || envelope.Meeting.Validate() != nil || modulecore.WorkstreamID(workstreamID).Validate() != nil {
			return "", domainworkstream.ErrTaskPlanUnavailable
		}
		if found != "" {
			return "", domainworkstream.ErrTaskPlanConflict
		}
		found = modulecore.WorkstreamID(workstreamID)
	}
	if err := rows.Err(); err != nil {
		return "", classifyTaskPlanStorageError(err)
	}
	if found == "" {
		return "", domainworkstream.ErrTaskPlanNotFound
	}
	return found, nil
}

func findTaskMeetingMinutesReference(ctx context.Context, db *sql.DB, workstreamID modulecore.WorkstreamID, summary TaskMeetingOccurrenceSummary, meeting domainmeeting.Meeting) (TaskMeetingMinutesReference, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT artifact_id,created_at,payload FROM artifact WHERE workstream_id=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.artifact_type') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.transcript.occurrence_key.schedule_id') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.transcript.occurrence_key.local_date') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.transcript.occurrence_key.slot') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.transcript.occurrence_key.timezone') END=?`, string(workstreamID), taskMeetingMinutesArtifactType, string(summary.Key.ScheduleID), summary.Key.LocalDate, summary.Key.Slot, summary.Key.Timezone)
	if err != nil {
		return TaskMeetingMinutesReference{}, false, classifyTaskPlanStorageError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return TaskMeetingMinutesReference{}, false, err
		}
		return TaskMeetingMinutesReference{}, false, nil
	}
	var id, storedAt, raw string
	if err := rows.Scan(&id, &storedAt, &raw); err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	var artifact domainworkstream.Artifact
	var minutes TaskMeetingMinutesArtifact
	if json.Unmarshal([]byte(raw), &artifact) != nil || json.Unmarshal(artifact.Payload, &minutes) != nil || artifact.ArtifactID != id || artifact.Type != taskMeetingMinutesArtifactType || artifact.WorkstreamID != string(workstreamID) ||
		validateTaskMeetingMinutesArtifact(minutes) != nil || minutes.Transcript.OccurrenceKey != summary.Key || minutes.Transcript.OwnerUserID != summary.OwnerUserID || minutes.Transcript.ConfigRevision != summary.ScheduleRevision || minutes.Transcript.RegistrationEvidenceID != summary.RegistrationEvidenceID {
		return TaskMeetingMinutesReference{}, false, domainworkstream.ErrTaskPlanUnavailable
	}
	createdAt, err := parseTaskMeetingArtifactTimestamp(storedAt)
	if err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	ref := taskMeetingMinutesReference(artifact, createdAt)
	if err := validateTaskMeetingMinutesReference(ref); err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	if err := validateTaskMeetingMinutesOccurrenceProof(meeting, minutes, ref); err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	if rows.Next() {
		return TaskMeetingMinutesReference{}, false, domainworkstream.ErrTaskPlanConflict
	}
	if err := rows.Err(); err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	return ref, true, nil
}

func validateTaskMeetingMinutesOccurrenceProof(meeting domainmeeting.Meeting, artifact TaskMeetingMinutesArtifact, ref TaskMeetingMinutesReference) error {
	transcript := artifact.Transcript
	if transcript.OccurrenceKey != meeting.Key || transcript.RegistrationEvidenceID != meeting.RegistrationEvidenceID || transcript.ConfigRevision != meeting.ScheduleRevision ||
		transcript.Guard.Generation != meeting.Generation || transcript.Guard.Epoch != meeting.Epoch {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	switch meeting.Status {
	case domainmeeting.StatusFinishing:
		if transcript.Outcome != domainmeeting.StatusCompleted || transcript.Guard != meeting.Guard() || transcript.TranscriptRequestID != meeting.TranscriptRequestID || !ref.CreatedAt.Before(meeting.DeadlineAt) {
			return domainworkstream.ErrTaskPlanUnavailable
		}
	case domainmeeting.StatusCompleted:
		evidence := meeting.FinishEvidence
		if transcript.Outcome != domainmeeting.StatusCompleted || transcript.TranscriptRequestID != meeting.TranscriptRequestID || evidence == nil || evidence.TranscriptArtifactID != ref.ArtifactID ||
			evidence.Key != meeting.Key || evidence.Generation != meeting.Generation || evidence.Epoch != meeting.Epoch ||
			transcript.Guard.ProgressionRevision != evidence.ProgressionRevision || evidence.ProgressionRevision+1 != meeting.ProgressionRevision ||
			!evidence.PersistedAt.Equal(ref.CreatedAt) || !ref.CreatedAt.Before(meeting.DeadlineAt) {
			return domainworkstream.ErrTaskPlanUnavailable
		}
	case domainmeeting.StatusPartial, domainmeeting.StatusMissed:
		evidence := meeting.PartialMinutes
		if evidence == nil {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		proof := evidence.Reference
		if transcript.Outcome != meeting.Status || transcript.TranscriptRequestID != meeting.TranscriptRequestID || proof.ArtifactID != ref.ArtifactID || proof.WorkstreamID != ref.WorkstreamID ||
			proof.OwnerUserID != ref.OwnerUserID || proof.CoordinatorActorID != ref.CoordinatorActorID || proof.Key.String() != ref.OccurrenceKey ||
			proof.RequestID != transcript.TranscriptRequestID || proof.RegistrationEvidenceID != ref.RegistrationEvidenceID || proof.ConfigRevision != ref.ConfigRevision ||
			proof.ConfigSHA256 != ref.ConfigSHA256 || proof.AudienceSHA256 != ref.AudienceSHA256 || !equalMeetingStringSlices(proof.Audience, ref.Audience) ||
			proof.Guard != transcript.Guard || proof.TranscriptSHA256 != ref.TranscriptSHA256 || !proof.CreatedAt.Equal(ref.CreatedAt) {
			return domainworkstream.ErrTaskPlanUnavailable
		}
	default:
		return domainworkstream.ErrTaskPlanUnavailable
	}
	return nil
}

func parseTaskMeetingArtifactTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05.000Z"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, domainworkstream.ErrTaskPlanUnavailable
}

type storedTaskMeetingMinutes struct {
	Artifact       domainworkstream.Artifact
	TranscriptJSON []byte
	InputSHA256    string
	CreatedAt      time.Time
}

func findTaskMeetingMinutesTx(ctx context.Context, tx *sql.Tx, key domainmeeting.OccurrenceKey, workstreamID modulecore.WorkstreamID) (storedTaskMeetingMinutes, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artifact_id,created_at,payload FROM artifact WHERE workstream_id=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.artifact_type') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.transcript.occurrence_key.schedule_id') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.transcript.occurrence_key.local_date') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.transcript.occurrence_key.slot') END=? AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.payload.transcript.occurrence_key.timezone') END=? ORDER BY created_at`, workstreamID, taskMeetingMinutesArtifactType, string(key.ScheduleID), key.LocalDate, key.Slot, key.Timezone)
	if err != nil {
		return storedTaskMeetingMinutes{}, false, classifyTaskPlanStorageError(err)
	}
	defer rows.Close()
	var stored storedTaskMeetingMinutes
	count := 0
	for rows.Next() {
		count++
		var id, created, raw string
		if err := rows.Scan(&id, &created, &raw); err != nil {
			return storedTaskMeetingMinutes{}, false, err
		}
		var row domainworkstream.Artifact
		var artifact TaskMeetingMinutesArtifact
		if json.Unmarshal([]byte(raw), &row) != nil || json.Unmarshal(row.Payload, &artifact) != nil || row.ArtifactID != id || row.Type != taskMeetingMinutesArtifactType || validateTaskMeetingMinutesArtifact(artifact) != nil {
			return storedTaskMeetingMinutes{}, false, domainworkstream.ErrTaskPlanUnavailable
		}
		encoded, err := json.Marshal(artifact.Transcript)
		if err != nil || sha256Hex(encoded) != artifact.InputSHA256 {
			return storedTaskMeetingMinutes{}, false, domainworkstream.ErrTaskPlanUnavailable
		}
		parsed, err := time.Parse(timeFormatRFC3339Nano, created)
		if err != nil {
			return storedTaskMeetingMinutes{}, false, domainworkstream.ErrTaskPlanUnavailable
		}
		stored = storedTaskMeetingMinutes{Artifact: row, TranscriptJSON: encoded, InputSHA256: artifact.InputSHA256, CreatedAt: parsed}
	}
	if err := rows.Err(); err != nil {
		return storedTaskMeetingMinutes{}, false, err
	}
	if count > 1 {
		return storedTaskMeetingMinutes{}, false, domainworkstream.ErrTaskPlanConflict
	}
	return stored, count == 1, nil
}

func validateTaskMeetingMinutesOccurrenceTx(ctx context.Context, tx *sql.Tx, request TaskMeetingMinutesRequest) error {
	stored, raw, found, err := findMeetingOccurrence(ctx, tx, request.Meeting.Key, string(request.WorkstreamID))
	if err != nil {
		return err
	}
	if !found || stored.Access.OwnerUserID != request.OwnerUserID || stored.Access.CoordinatorActorID != request.CoordinatorActorID || !sameActorAudience(stored.Access.Audience, request.Meeting.Participants) ||
		stored.Meeting.Key != request.Meeting.Key || stored.Meeting.ScheduleRevision != request.Meeting.ScheduleRevision || stored.Meeting.RegistrationEvidenceID != request.RegistrationEvidenceID ||
		stored.Meeting.Guard() != request.Meeting.Guard() ||
		stored.Meeting.TranscriptRequestID != request.Meeting.TranscriptRequestID || !sameTaskMeetingSnapshotJSON(stored.Meeting, request.Meeting) ||
		stored.Meeting.TaskManagerEvidenceID != request.Meeting.TaskManagerEvidenceID || !sameNativeReference(stored.Meeting.TaskRef, request.Meeting.TaskRef) || !sameNativeReference(stored.Meeting.RunRef, request.Meeting.RunRef) {
		return domainworkstream.ErrTaskPlanConflict
	}
	artifactID, artifactWorkstreamID, _, err := findMeetingArtifactID(raw)
	if err != nil || artifactID == "" || artifactWorkstreamID != string(request.WorkstreamID) {
		return domainworkstream.ErrTaskPlanConflict
	}
	if err := requireManagedWorkstream(ctx, tx, string(request.WorkstreamID), domainworkstream.TaskPlanDataScopeUser, request.OwnerUserID); err != nil {
		return domainworkstream.ErrTaskPlanConflict
	}
	return nil
}

func sameTaskMeetingSnapshotJSON(left, right domainmeeting.Meeting) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func validateTaskMeetingMinutesRequest(request TaskMeetingMinutesRequest) error {
	meeting := request.Meeting
	if request.WorkstreamID != "" && request.WorkstreamID.Validate() != nil || request.OwnerUserID == "" || request.OwnerUserID != strings.TrimSpace(request.OwnerUserID) || request.CoordinatorActorID == "" ||
		request.RegistrationEvidenceID.Validate() != nil || !validTaskCenterSHA256(request.ConfigSHA256) || meeting.Validate() != nil ||
		meeting.RegistrationEvidenceID != request.RegistrationEvidenceID || meeting.ScheduleRevision == 0 || meeting.Key.Validate() != nil || meeting.TaskRef == nil || meeting.RunRef == nil ||
		meeting.TaskRef.Validate() != nil || meeting.RunRef.Validate() != nil || meeting.TaskManagerEvidenceID.Validate() != nil || len(meeting.Participants) != domaintask.TaskMeetingAudienceSize {
		return domainworkstream.ErrTaskPlanInvalid
	}
	if meeting.Status != domainmeeting.StatusFinishing && meeting.Status != domainmeeting.StatusCompleted && meeting.Status != domainmeeting.StatusPartial && meeting.Status != domainmeeting.StatusMissed ||
		meeting.Status == domainmeeting.StatusCompleted && meeting.TranscriptRequestID == "" {
		return domainworkstream.ErrTaskPlanInvalid
	}
	audienceHash, err := domaintask.TaskMeetingAudienceSHA256(meeting.Participants)
	if err != nil || len(request.Messages) != len(meeting.Reports)+len(meeting.Answers) ||
		meeting.Status == domainmeeting.StatusCompleted && len(request.Messages) == 0 || meeting.Status == domainmeeting.StatusMissed && len(request.Messages) != 0 {
		return domainworkstream.ErrTaskPlanConflict
	}
	ordered, err := taskMeetingMinutesOrderedUtterances(meeting)
	if err != nil || len(ordered) != len(request.Messages) {
		return domainworkstream.ErrTaskPlanConflict
	}
	proofs := make(map[modulecore.MessageID]TaskMeetingMinutesMessageProof, len(request.Messages))
	for _, proof := range request.Messages {
		if proof.MessageID.Validate() != nil {
			return domainworkstream.ErrTaskPlanConflict
		}
		if _, exists := proofs[proof.MessageID]; exists {
			return domainworkstream.ErrTaskPlanConflict
		}
		proofs[proof.MessageID] = proof
	}
	for _, utterance := range ordered {
		proof, found := proofs[utterance.MessageID]
		if !found || validateTaskMeetingMinutesMessage(request, utterance, audienceHash, proof) != nil {
			return domainworkstream.ErrTaskPlanConflict
		}
	}
	if (meeting.Status == domainmeeting.StatusFinishing || meeting.Status == domainmeeting.StatusCompleted) && (!taskMeetingMinutesAllDelivered(meeting) || !taskMeetingMinutesAllConcernsAnswered(meeting)) {
		return domainworkstream.ErrTaskPlanConflict
	}
	if meeting.Status == domainmeeting.StatusPartial && meeting.PartialEvidence == nil || meeting.Status == domainmeeting.StatusMissed && (meeting.PartialEvidence == nil || len(meeting.Reports) != 0 || len(meeting.Answers) != 0) {
		return domainworkstream.ErrTaskPlanConflict
	}
	return nil
}

func validateTaskMeetingMinutesMessage(request TaskMeetingMinutesRequest, utterance domainmeeting.Utterance, audienceHash string, proof TaskMeetingMinutesMessageProof) error {
	ref := proof.Reference
	if utterance.SourceTaskRef == nil || utterance.SourceRunRef == nil || proof.MessageID != utterance.MessageID || proof.DeliveryState != utterance.DeliveryState ||
		proof.SenderTaskID != *utterance.SourceTaskRef || proof.SenderRunID != *utterance.SourceRunRef || proof.SenderActorID != utterance.ActorID {
		return domainworkstream.ErrTaskPlanConflict
	}
	if utterance.DeliveryState != domainmeeting.DeliveryDelivered {
		if request.Meeting.Status == domainmeeting.StatusCompleted || utterance.DeliveryState != domainmeeting.DeliveryUnsent && utterance.DeliveryState != domainmeeting.DeliverySending &&
			utterance.DeliveryState != domainmeeting.DeliveryUnknown && utterance.DeliveryState != domainmeeting.DeliveryUndelivered || len(proof.ConsumptionProofs) != 0 ||
			!zeroTaskMeetingMessageReference(ref) {
			return domainworkstream.ErrTaskPlanConflict
		}
		if utterance.DeliveryEvidence != nil && (utterance.DeliveryEvidence.Delivered || utterance.DeliveryEvidence.MessageID != utterance.MessageID || utterance.DeliveryEvidence.Key != request.Meeting.Key) {
			return domainworkstream.ErrTaskPlanConflict
		}
		return nil
	}
	if utterance.DeliveryEvidence == nil || !utterance.DeliveryEvidence.Delivered || ref.Validate() != nil || ref.OccurrenceKey != request.Meeting.Key.String() ||
		ref.MessageID != utterance.MessageID || ref.BodySHA256 != taskMeetingMinutesBodySHA256(utterance.Body) || ref.AudienceSHA256 != audienceHash ||
		ref.ConfigRevision != request.Meeting.ScheduleRevision || ref.ConfigSHA256 != request.ConfigSHA256 || ref.AttemptGeneration != utterance.DeliveryGeneration ||
		ref.AttemptEpoch != utterance.DeliveryEpoch || ref.ScopeEvidenceID != utterance.DeliveryScopeEvidenceID || proof.Delivery.EvidenceID.Validate() != nil ||
		proof.Delivery.Key != request.Meeting.Key || proof.Delivery.MessageID != ref.MessageID || proof.Delivery.ThreadID != ref.ThreadID || proof.Delivery.Generation != ref.AttemptGeneration ||
		proof.Delivery.Epoch != ref.AttemptEpoch || proof.Delivery.ScopeEvidenceID != ref.ScopeEvidenceID || !proof.Delivery.Delivered || proof.Delivery.ObservedAt.IsZero() ||
		!equalMeetingStringSlices(proof.Delivery.Audience, request.Meeting.Participants) || !sameMeetingDeliveryEvidence(*utterance.DeliveryEvidence, proof.Delivery) || len(proof.ConsumptionProofs) != domaintask.TaskMeetingAudienceSize {
		return domainworkstream.ErrTaskPlanConflict
	}
	var latest time.Time
	for i, actorID := range request.Meeting.Participants {
		consumption := proof.ConsumptionProofs[i]
		if consumption.Reference != ref || consumption.ActorID != actorID || consumption.ConsumerTaskID.Validate() != nil || consumption.ConsumerRunID.Validate() != nil || consumption.EvidenceID.Validate() != nil || consumption.AcceptedAt.IsZero() || !validTaskCenterSHA256(consumption.ContextSHA256) {
			return domainworkstream.ErrTaskPlanConflict
		}
		if consumption.AcceptedAt.After(latest) {
			latest = consumption.AcceptedAt.UTC()
		}
	}
	if !latest.Equal(proof.Delivery.ObservedAt.UTC()) {
		return domainworkstream.ErrTaskPlanConflict
	}
	return nil
}

func projectTaskMeetingMinutes(request TaskMeetingMinutesRequest) (TaskMeetingMinutesTranscript, error) {
	if err := validateTaskMeetingMinutesRequest(request); err != nil {
		return TaskMeetingMinutesTranscript{}, err
	}
	meeting := request.Meeting
	ordered, err := taskMeetingMinutesOrderedUtterances(meeting)
	if err != nil {
		return TaskMeetingMinutesTranscript{}, err
	}
	messageOrder := make([]modulecore.MessageID, 0, len(ordered))
	for _, utterance := range ordered {
		messageOrder = append(messageOrder, utterance.MessageID)
	}
	transcriptOutcome := meeting.Status
	if transcriptOutcome == domainmeeting.StatusFinishing {
		transcriptOutcome = domainmeeting.StatusCompleted
	}
	transcript := TaskMeetingMinutesTranscript{WorkstreamID: request.WorkstreamID, OwnerUserID: request.OwnerUserID, CoordinatorActorID: request.CoordinatorActorID,
		OccurrenceKey: meeting.Key, TranscriptRequestID: meeting.TranscriptRequestID, RegistrationEvidenceID: request.RegistrationEvidenceID, ConfigRevision: meeting.ScheduleRevision, ConfigSHA256: request.ConfigSHA256,
		Audience: append([]string(nil), meeting.Participants...), Guard: meeting.Guard(), ScheduledAt: meeting.ScheduledAt, Outcome: transcriptOutcome,
		MessageOrder: messageOrder,
		Reports:      make([]TaskMeetingMinutesReport, 0, len(meeting.Reports)), Answers: make([]TaskMeetingMinutesAnswer, 0, len(meeting.Answers)),
		Messages: append([]TaskMeetingMinutesMessageProof(nil), request.Messages...)}
	if meeting.PartialEvidence != nil {
		transcript.ReasonCode = meeting.PartialEvidence.ReasonCode
		transcript.Unresolved = append([]string(nil), meeting.PartialEvidence.Unresolved...)
	}
	for _, report := range meeting.Reports {
		evidenceHash, ok := taskMeetingSpeakerEvidenceDigest(meeting, report.SpeakerEvidenceID)
		if !ok || report.Utterance.SourceTaskRef == nil || report.Utterance.SourceRunRef == nil {
			return TaskMeetingMinutesTranscript{}, domainworkstream.ErrTaskPlanConflict
		}
		transcript.Reports = append(transcript.Reports, TaskMeetingMinutesReport{MessageID: report.Utterance.MessageID, ActorID: report.Utterance.ActorID, Body: report.Utterance.Body,
			Current: report.Current, Next: report.Next, ConcernDeclaration: report.ConcernDeclaration, Concerns: append([]domainmeeting.Concern(nil), report.Concerns...),
			SpeakerEvidenceID: report.SpeakerEvidenceID, SpeakerEvidenceSHA: evidenceHash, SourceTaskID: *report.Utterance.SourceTaskRef, SourceRunID: *report.Utterance.SourceRunRef})
	}
	for _, answer := range meeting.Answers {
		evidenceHash, ok := taskMeetingSpeakerEvidenceDigest(meeting, answer.SpeakerEvidenceID)
		if !ok || answer.Utterance.SourceTaskRef == nil || answer.Utterance.SourceRunRef == nil {
			return TaskMeetingMinutesTranscript{}, domainworkstream.ErrTaskPlanConflict
		}
		transcript.Answers = append(transcript.Answers, TaskMeetingMinutesAnswer{MessageID: answer.Utterance.MessageID, ActorID: answer.Utterance.ActorID, Body: answer.Utterance.Body,
			ConcernID: answer.ConcernID, ReporterID: answer.ReporterID, Kind: answer.Kind, SpeakerEvidenceID: answer.SpeakerEvidenceID,
			SpeakerEvidenceSHA: evidenceHash, SourceTaskID: *answer.Utterance.SourceTaskRef, SourceRunID: *answer.Utterance.SourceRunRef})
	}
	return transcript, nil
}

func validateTaskMeetingMinutesTranscript(transcript TaskMeetingMinutesTranscript) error {
	if transcript.WorkstreamID.Validate() != nil || transcript.OwnerUserID == "" || transcript.CoordinatorActorID == "" || transcript.OccurrenceKey.Validate() != nil || transcript.TranscriptRequestID == "" || transcript.TranscriptRequestID != strings.TrimSpace(transcript.TranscriptRequestID) ||
		transcript.RegistrationEvidenceID.Validate() != nil || transcript.ConfigRevision == 0 || !validTaskCenterSHA256(transcript.ConfigSHA256) || transcript.Guard.Generation == 0 || transcript.Guard.Epoch == 0 || transcript.Guard.ProgressionRevision == 0 || transcript.ScheduledAt.IsZero() || len(transcript.Audience) != domaintask.TaskMeetingAudienceSize {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	audienceHash, err := domaintask.TaskMeetingAudienceSHA256(transcript.Audience)
	if err != nil || len(transcript.Messages) != len(transcript.Reports)+len(transcript.Answers) || len(transcript.MessageOrder) != len(transcript.Messages) ||
		transcript.Outcome != domainmeeting.StatusCompleted && transcript.Outcome != domainmeeting.StatusPartial && transcript.Outcome != domainmeeting.StatusMissed ||
		transcript.Outcome == domainmeeting.StatusCompleted && (transcript.ReasonCode != "" || len(transcript.Unresolved) != 0 || len(transcript.Messages) == 0) ||
		(transcript.Outcome == domainmeeting.StatusPartial || transcript.Outcome == domainmeeting.StatusMissed) && (strings.TrimSpace(transcript.ReasonCode) == "" || transcript.ReasonCode != strings.TrimSpace(transcript.ReasonCode)) ||
		transcript.Outcome == domainmeeting.StatusMissed && (transcript.ReasonCode != "missed" || len(transcript.Reports) != 0 || len(transcript.Answers) != 0 || len(transcript.Messages) != 0) {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	rows := make(map[modulecore.MessageID]struct {
		actor, body  string
		task         modulecore.TaskID
		run          modulecore.RunID
		evidenceID   modulecore.EvidenceID
		evidenceHash string
	}, len(transcript.Reports)+len(transcript.Answers))
	for _, report := range transcript.Reports {
		rows[report.MessageID] = struct {
			actor, body  string
			task         modulecore.TaskID
			run          modulecore.RunID
			evidenceID   modulecore.EvidenceID
			evidenceHash string
		}{report.ActorID, report.Body, report.SourceTaskID, report.SourceRunID, report.SpeakerEvidenceID, report.SpeakerEvidenceSHA}
	}
	for _, answer := range transcript.Answers {
		if _, exists := rows[answer.MessageID]; exists {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		rows[answer.MessageID] = struct {
			actor, body  string
			task         modulecore.TaskID
			run          modulecore.RunID
			evidenceID   modulecore.EvidenceID
			evidenceHash string
		}{answer.ActorID, answer.Body, answer.SourceTaskID, answer.SourceRunID, answer.SpeakerEvidenceID, answer.SpeakerEvidenceSHA}
	}
	proofs := make(map[modulecore.MessageID]TaskMeetingMinutesMessageProof, len(transcript.Messages))
	for _, message := range transcript.Messages {
		if message.MessageID.Validate() != nil {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		if _, exists := proofs[message.MessageID]; exists {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		proofs[message.MessageID] = message
	}
	for index, messageID := range transcript.MessageOrder {
		if messageID.Validate() != nil {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		message, exists := proofs[messageID]
		row, rowExists := rows[messageID]
		if !exists || !rowExists || message.SenderActorID != row.actor || message.SenderTaskID != row.task || message.SenderRunID != row.run ||
			row.task.Validate() != nil || row.run.Validate() != nil || row.evidenceID.Validate() != nil || !validTaskCenterSHA256(row.evidenceHash) ||
			!containsMeetingActor(transcript.Audience, row.actor) || taskMeetingMinutesBodySHA256(row.body) == "" {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		if index > 0 && transcript.MessageOrder[index-1] == messageID {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		ref := message.Reference
		if message.DeliveryState == domainmeeting.DeliveryDelivered {
			if ref.Validate() != nil || ref.MessageID != messageID || ref.OccurrenceKey != transcript.OccurrenceKey.String() || ref.AudienceSHA256 != audienceHash ||
				ref.ConfigRevision != transcript.ConfigRevision || ref.ConfigSHA256 != transcript.ConfigSHA256 || taskMeetingMinutesBodySHA256(row.body) != ref.BodySHA256 ||
				message.Delivery.EvidenceID.Validate() != nil || message.Delivery.Key != transcript.OccurrenceKey || message.Delivery.MessageID != ref.MessageID ||
				message.Delivery.ThreadID != ref.ThreadID || message.Delivery.Generation != ref.AttemptGeneration || message.Delivery.Epoch != ref.AttemptEpoch ||
				message.Delivery.ScopeEvidenceID != ref.ScopeEvidenceID || !message.Delivery.Delivered || message.Delivery.ObservedAt.IsZero() ||
				!equalMeetingStringSlices(message.Delivery.Audience, transcript.Audience) || len(message.ConsumptionProofs) != domaintask.TaskMeetingAudienceSize {
				return domainworkstream.ErrTaskPlanUnavailable
			}
			var latest time.Time
			for proofIndex, proof := range message.ConsumptionProofs {
				if proof.Reference != ref || proof.ActorID != transcript.Audience[proofIndex] || proof.ConsumerTaskID.Validate() != nil || proof.ConsumerRunID.Validate() != nil || proof.EvidenceID.Validate() != nil || proof.AcceptedAt.IsZero() || !validTaskCenterSHA256(proof.ContextSHA256) {
					return domainworkstream.ErrTaskPlanUnavailable
				}
				if proof.AcceptedAt.After(latest) {
					latest = proof.AcceptedAt.UTC()
				}
			}
			if !latest.Equal(message.Delivery.ObservedAt.UTC()) {
				return domainworkstream.ErrTaskPlanUnavailable
			}
		} else if message.DeliveryState != domainmeeting.DeliveryUnsent && message.DeliveryState != domainmeeting.DeliverySending && message.DeliveryState != domainmeeting.DeliveryUnknown && message.DeliveryState != domainmeeting.DeliveryUndelivered ||
			!zeroTaskMeetingMessageReference(ref) || len(message.ConsumptionProofs) != 0 {
			return domainworkstream.ErrTaskPlanUnavailable
		}
	}
	if transcript.Outcome == domainmeeting.StatusCompleted {
		for _, message := range transcript.Messages {
			if message.DeliveryState != domainmeeting.DeliveryDelivered {
				return domainworkstream.ErrTaskPlanUnavailable
			}
		}
	}
	return nil
}

func taskMeetingMinutesOrderedUtterances(meeting domainmeeting.Meeting) ([]domainmeeting.Utterance, error) {
	byEvidence := make(map[modulecore.EvidenceID]domainmeeting.Utterance, len(meeting.Reports)+len(meeting.Answers))
	for _, report := range meeting.Reports {
		if report.SpeakerEvidenceID.Validate() != nil {
			return nil, domainworkstream.ErrTaskPlanConflict
		}
		byEvidence[report.SpeakerEvidenceID] = report.Utterance
	}
	for _, answer := range meeting.Answers {
		if answer.SpeakerEvidenceID.Validate() != nil {
			return nil, domainworkstream.ErrTaskPlanConflict
		}
		if _, exists := byEvidence[answer.SpeakerEvidenceID]; exists {
			return nil, domainworkstream.ErrTaskPlanConflict
		}
		byEvidence[answer.SpeakerEvidenceID] = answer.Utterance
	}
	ordered := make([]domainmeeting.Utterance, 0, len(byEvidence))
	seen := make(map[modulecore.MessageID]struct{}, len(byEvidence))
	for _, claim := range meeting.EvidenceClaims {
		if claim.Kind != "speaker_utterance" {
			continue
		}
		utterance, found := byEvidence[claim.EvidenceID]
		if !found {
			continue
		}
		if _, exists := seen[utterance.MessageID]; exists {
			return nil, domainworkstream.ErrTaskPlanConflict
		}
		seen[utterance.MessageID] = struct{}{}
		ordered = append(ordered, utterance)
	}
	if len(ordered) != len(byEvidence) {
		return nil, domainworkstream.ErrTaskPlanConflict
	}
	return ordered, nil
}

func zeroTaskMeetingMessageReference(ref domaintask.TaskMeetingMessageReference) bool {
	return ref == (domaintask.TaskMeetingMessageReference{})
}

func taskMeetingMinutesAllDelivered(meeting domainmeeting.Meeting) bool {
	for _, report := range meeting.Reports {
		if report.Utterance.DeliveryState != domainmeeting.DeliveryDelivered {
			return false
		}
	}
	for _, answer := range meeting.Answers {
		if answer.Utterance.DeliveryState != domainmeeting.DeliveryDelivered {
			return false
		}
	}
	return true
}

func taskMeetingMinutesAllConcernsAnswered(meeting domainmeeting.Meeting) bool {
	for _, report := range meeting.Reports {
		for index := range report.Concerns {
			id := domainmeeting.ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: uint32(index + 1)}
			answered := false
			for _, answer := range meeting.Answers {
				if answer.ConcernID == id && answer.Utterance.DeliveryState == domainmeeting.DeliveryDelivered {
					answered = true
					break
				}
			}
			if !answered {
				return false
			}
		}
	}
	return true
}

func validateTaskMeetingMinutesArtifact(artifact TaskMeetingMinutesArtifact) error {
	if artifact.SchemaVersion != taskMeetingMinutesSchemaVersion || validateTaskMeetingMinutesTranscript(artifact.Transcript) != nil {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	encoded, err := json.Marshal(artifact.Transcript)
	if err != nil || sha256Hex(encoded) != artifact.InputSHA256 {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	return nil
}

func validateTaskMeetingMinutesReference(ref TaskMeetingMinutesReference) error {
	if ref.ArtifactID.Validate() != nil || ref.WorkstreamID.Validate() != nil || ref.OwnerUserID == "" || ref.CoordinatorActorID == "" || ref.OccurrenceKey == "" ||
		ref.RegistrationEvidenceID.Validate() != nil || ref.ConfigRevision == 0 || !validTaskCenterSHA256(ref.ConfigSHA256) || ref.Guard.Generation == 0 || ref.Guard.Epoch == 0 || ref.Guard.ProgressionRevision == 0 ||
		!validTaskCenterSHA256(ref.AudienceSHA256) || !validTaskCenterSHA256(ref.TranscriptSHA256) || ref.CreatedAt.IsZero() {
		return domainworkstream.ErrTaskPlanInvalid
	}
	digest, err := domaintask.TaskMeetingAudienceSHA256(ref.Audience)
	if err != nil || digest != ref.AudienceSHA256 {
		return domainworkstream.ErrTaskPlanInvalid
	}
	return nil
}

func taskMeetingSpeakerEvidenceDigest(meeting domainmeeting.Meeting, id modulecore.EvidenceID) (string, bool) {
	for _, claim := range meeting.EvidenceClaims {
		if claim.EvidenceID == id && claim.Kind == "speaker_utterance" && validTaskCenterSHA256(claim.SHA256) {
			return claim.SHA256, true
		}
	}
	return "", false
}

func taskMeetingMinutesReference(artifact domainworkstream.Artifact, createdAt time.Time) TaskMeetingMinutesReference {
	var stored TaskMeetingMinutesArtifact
	if json.Unmarshal(artifact.Payload, &stored) != nil {
		return TaskMeetingMinutesReference{}
	}
	t := stored.Transcript
	audienceHash, _ := domaintask.TaskMeetingAudienceSHA256(t.Audience)
	return TaskMeetingMinutesReference{ArtifactID: modulecore.ArtifactID(artifact.ArtifactID), WorkstreamID: modulecore.WorkstreamID(artifact.WorkstreamID),
		OwnerUserID: t.OwnerUserID, CoordinatorActorID: t.CoordinatorActorID, OccurrenceKey: t.OccurrenceKey.String(), RegistrationEvidenceID: t.RegistrationEvidenceID,
		ConfigRevision: t.ConfigRevision, ConfigSHA256: t.ConfigSHA256, Audience: append([]string(nil), t.Audience...), AudienceSHA256: audienceHash,
		Guard: t.Guard, TranscriptSHA256: sha256Hex(mustMarshal(t)), CreatedAt: createdAt}
}

func sameTaskMeetingMinutesReference(left, right TaskMeetingMinutesReference) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func cloneTaskMeetingMinutesRequest(request TaskMeetingMinutesRequest) TaskMeetingMinutesRequest {
	encoded, err := json.Marshal(request)
	if err != nil {
		return request
	}
	var cloned TaskMeetingMinutesRequest
	if json.Unmarshal(encoded, &cloned) != nil {
		return request
	}
	return cloned
}

func equalMeetingStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func containsMeetingActor(audience []string, actorID string) bool {
	for _, actor := range audience {
		if actor == actorID {
			return true
		}
	}
	return false
}

func sameMeetingDeliveryEvidence(left, right domainmeeting.DeliveryEvidence) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func taskMeetingMinutesBodySHA256(body string) string { return sha256Hex([]byte(body)) }
func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
func mustMarshal(value any) []byte { encoded, _ := json.Marshal(value); return encoded }
