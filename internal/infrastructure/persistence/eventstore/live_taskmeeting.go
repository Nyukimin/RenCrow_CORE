package eventstore

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
	"unicode/utf8"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskMeetingRuntimeVersion   = "taskmeeting.v1"
	taskMeetingComponent        = "taskmeeting"
	taskMeetingOpenOperation    = "taskmeeting.ensure_group_session"
	taskMeetingOpenEvent        = "taskmeeting.group_thread_opened"
	taskMeetingMessageOperation = "taskmeeting.append_group_message"
	taskMeetingMessageEvent     = "taskmeeting.group_message"
	taskMeetingWorldOperation   = "taskmeeting.observe_world"
	taskMeetingWorldEvent       = "taskmeeting.world_observation"
	taskMeetingConsumeOperation = "taskmeeting.record_group_consumption"
	taskMeetingConsumeEvent     = "taskmeeting.group_message_consumed"
	maxTaskMeetingMessageBytes  = 32 << 10
)

var (
	ErrTaskMeetingUnavailable = errors.New("TaskMeeting EventStore owner is unavailable")
	ErrTaskMeetingConflict    = errors.New("TaskMeeting EventStore receipt conflicts with its frozen input")
	ErrTaskMeetingForbidden   = errors.New("TaskMeeting EventStore owner authorization failed")
	ErrTaskMeetingReceiptBad  = errors.New("TaskMeeting EventStore receipt is corrupt")
)

// TaskMeetingGroupSessionRequest is the immutable owner input for one
// occurrence's shared conversation. Its key deliberately omits config
// revision: a later edit cannot create a second session for the same slot.
type TaskMeetingGroupSessionRequest struct {
	OccurrenceKey          string                `json:"occurrence_key"`
	TaskID                 modulecore.TaskID     `json:"task_id"`
	RunID                  modulecore.RunID      `json:"run_id"`
	OwnerUserID            string                `json:"owner_user_id"`
	CoordinatorActorID     string                `json:"coordinator_actor_id"`
	RegistrationEvidenceID modulecore.EvidenceID `json:"registration_evidence_id"`
	ConfigRevision         uint64                `json:"config_revision"`
	ConfigSHA256           string                `json:"config_sha256"`
	InputSHA256            string                `json:"input_sha256"`
	RequestID              string                `json:"request_id"`
	Audience               []string              `json:"audience"`
}

type TaskMeetingGroupSessionReceipt struct {
	OccurrenceKey        string                `json:"occurrence_key"`
	OwnerUserID          string                `json:"owner_user_id"`
	CoordinatorActorID   string                `json:"coordinator_actor_id"`
	RegistrationEvidence modulecore.EvidenceID `json:"registration_evidence_id"`
	AdmissionEvidence    modulecore.EvidenceID `json:"admission_evidence_id"`
	ConfigRevision       uint64                `json:"config_revision"`
	ConfigSHA256         string                `json:"config_sha256"`
	InputSHA256          string                `json:"input_sha256"`
	RequestID            string                `json:"request_id"`
	Audience             []string              `json:"audience"`
	AudienceSHA256       string                `json:"audience_sha256"`
	TaskID               modulecore.TaskID     `json:"task_id"`
	RunID                modulecore.RunID      `json:"run_id"`
	SessionID            modulecore.SessionID  `json:"session_id"`
	ThreadID             modulecore.ThreadID   `json:"thread_id"`
	OpenedEventID        modulecore.EventID    `json:"opened_event_id"`
	EvidenceID           modulecore.EvidenceID `json:"evidence_id"`
	OpenedAt             time.Time             `json:"opened_at"`
}

// TaskMeetingGroupMessageRequest supplies one exact shared utterance. The
// body is stored once, in the canonical EventEnvelope; receipts and outbox
// rows carry references and digests only.
type TaskMeetingGroupMessageRequest struct {
	Session           TaskMeetingGroupSessionReceipt                  `json:"session"`
	TaskID            modulecore.TaskID                               `json:"task_id"`
	RunID             modulecore.RunID                                `json:"run_id"`
	ActorID           string                                          `json:"actor_id"`
	MessageID         modulecore.MessageID                            `json:"message_id"`
	Body              string                                          `json:"body"`
	GenerationResult  domaintask.TaskMeetingGenerationResultReference `json:"generation_result"`
	AttemptGeneration uint64                                          `json:"attempt_generation"`
	AttemptEpoch      uint64                                          `json:"attempt_epoch"`
	ScopeEvidenceID   modulecore.EvidenceID                           `json:"scope_evidence_id"`
}

type TaskMeetingGroupMessageReceipt struct {
	Reference        domaintask.TaskMeetingMessageReference          `json:"reference"`
	GenerationResult domaintask.TaskMeetingGenerationResultReference `json:"generation_result"`
	SenderTaskID     modulecore.TaskID                               `json:"sender_task_id"`
	SenderRunID      modulecore.RunID                                `json:"sender_run_id"`
	Audience         []string                                        `json:"audience"`
	WorkIDs          []string                                        `json:"work_ids"`
	EvidenceID       modulecore.EvidenceID                           `json:"evidence_id"`
	CreatedAt        time.Time                                       `json:"created_at"`
}

type TaskMeetingGroupConsumptionReceipt struct {
	Proof      domaintask.TaskMeetingConsumedMessage `json:"proof"`
	WorkID     string                                `json:"work_id"`
	EventID    modulecore.EventID                    `json:"event_id"`
	EvidenceID modulecore.EvidenceID                 `json:"evidence_id"`
	RecordedAt time.Time                             `json:"recorded_at"`
}

type TaskMeetingRecipientAuthorizationCheck func(context.Context, string, domaintask.TaskMeetingMessageReference) error

type TaskMeetingGroupMessageAuthorizationCheck func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.TaskMeetingGenerationResultReference, json.RawMessage, string) error
type TaskMeetingGroupMessageReadAuthorizationCheck func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.TaskMeetingGenerationResultReference, json.RawMessage, string, *domaintask.TaskMeetingMessageReference) error

type LeasedTaskMeetingDelivery struct {
	Work      LeasedWorkItem                         `json:"work"`
	Reference domaintask.TaskMeetingMessageReference `json:"reference"`
}

type TaskMeetingWorldObservationAdmissionCheck func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domainmeeting.WorldObservationRequest) error

func (s *SQLiteStore) taskMeetingNowUTC() time.Time {
	if s != nil && s.taskMeetingClock != nil {
		return s.taskMeetingClock().UTC()
	}
	return time.Now().UTC()
}

// RecordTaskMeetingWorldObservation appends a CORE-owned receipt for one exact
// authenticated WORLD snapshot. The EventStore never claims that WORLD issued
// the EvidenceID and never performs movement or state mutation.
func (s *SQLiteStore) RecordTaskMeetingWorldObservation(
	ctx context.Context,
	request domainmeeting.WorldObservationRequest,
	reader domaintask.TaskMeetingInvocationReader,
	authorizeRead domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeObserve TaskMeetingWorldObservationAdmissionCheck,
) (domainmeeting.WorldObservationReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	if ctx == nil || reader == nil || authorizeRead == nil {
		return domainmeeting.WorldObservationReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := validateTaskMeetingWorldObservationRequest(request); err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	task, run, shared, err := reader.GetTaskMeetingInvocationState(ctx, request.TaskID, request.RunID, authorizeRead)
	if err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	if err := validateTaskMeetingWorldRootBinding(request, task, run); err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	inputHash, err := hashJSON(request)
	if err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	if prior, found, err := s.findTaskMeetingWorldObservation(ctx, request, task, run, inputHash); err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	} else if found {
		return prior, nil
	}
	if authorizeObserve == nil {
		return domainmeeting.WorldObservationReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := ctx.Err(); err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	if run.Status != domaintask.RunStatusRunning || authorizeObserve(ctx, task, run, shared, request) != nil {
		return domainmeeting.WorldObservationReceipt{}, ErrTaskMeetingForbidden
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(request)
		if err != nil {
			return domainmeeting.WorldObservationReceipt{}, err
		}
		var receipt domainmeeting.WorldObservationReceipt
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var inner error
			receipt, inner = s.recordTaskMeetingWorldObservationInline(commandCtx, request, inputHash, task, run)
			return inner
		})
		return receipt, err
	}
	return s.recordTaskMeetingWorldObservationInline(ctx, request, inputHash, task, run)
}

func validateTaskMeetingWorldObservationRequest(request domainmeeting.WorldObservationRequest) error {
	if request.Key.Validate() != nil ||
		request.TaskID.Validate() != nil || request.RunID.Validate() != nil || request.RegistrationEvidenceID.Validate() != nil ||
		request.Session.Validate() != nil || request.Thread.Validate() != nil || request.Generation == 0 || request.Epoch == 0 ||
		request.ConfigRevision == 0 || !validTaskMeetingDigest(request.ConfigSHA256) || strings.TrimSpace(request.OwnerUserID) == "" || request.CoordinatorActorID == "" ||
		request.RequestID == "" || request.ObservedAt.IsZero() || request.ValidUntil.IsZero() || !request.ValidUntil.After(request.ObservedAt) {
		return errors.New("TaskMeeting World observation request is incomplete")
	}
	if err := request.Snapshot.Validate(); err != nil {
		return err
	}
	if err := request.Rooms.Validate(); err != nil || request.Rooms.Living == "" || request.Rooms.Dining == "" {
		return errors.New("TaskMeeting World observation requires two owner-configured venue references")
	}
	audienceHash, err := domaintask.TaskMeetingAudienceSHA256(request.Audience)
	if err != nil || len(request.ActorBindings) != domaintask.TaskMeetingAudienceSize {
		return errors.New("TaskMeeting World observation audience or binding is invalid")
	}
	seenCore := make(map[string]struct{}, len(request.ActorBindings))
	seenWorld := make(map[string]struct{}, len(request.ActorBindings))
	for i, binding := range request.ActorBindings {
		if binding.CoreActorID != request.Audience[i] || binding.CoreActorID == "" || binding.WorldActorID == "" {
			return errors.New("TaskMeeting actor bindings must follow the exact audience order")
		}
		if _, exists := seenCore[binding.CoreActorID]; exists {
			return errors.New("TaskMeeting actor binding duplicates a CORE actor")
		}
		if _, exists := seenWorld[binding.WorldActorID]; exists {
			return errors.New("TaskMeeting actor binding duplicates a WORLD actor")
		}
		seenCore[binding.CoreActorID], seenWorld[binding.WorldActorID] = struct{}{}, struct{}{}
	}
	if audienceHash == "" || len(request.Snapshot.Actors) != len(request.ActorBindings) {
		return errors.New("TaskMeeting World snapshot does not contain the exact configured actor allowlist")
	}
	for i, binding := range request.ActorBindings {
		if request.Snapshot.Actors[i].WorldActorID != binding.WorldActorID {
			return errors.New("TaskMeeting World snapshot actor order differs from frozen binding")
		}
	}
	return nil
}

func validateTaskMeetingWorldRootBinding(request domainmeeting.WorldObservationRequest, task domaintask.Task, run domaintask.Run) error {
	invocation := task.TaskMeetingInvocation
	audienceHash, _ := domaintask.TaskMeetingAudienceSHA256(request.Audience)
	if invocation == nil || task.TaskID != request.TaskID || run.TaskID != task.TaskID || run.RunID != request.RunID ||
		invocation.Purpose != domaintask.TaskMeetingPurposeRoot || invocation.OccurrenceKey != request.Key.String() ||
		invocation.CoordinatorActorID != request.CoordinatorActorID ||
		invocation.ConfigRevision != request.ConfigRevision || invocation.ConfigSHA256 != request.ConfigSHA256 ||
		invocation.ConfiguredOwnerUserID != request.OwnerUserID || invocation.AudienceSHA256 != audienceHash ||
		!equalStrings(invocation.Audience, request.Audience) || task.Assignee != invocation.CoordinatorActorID ||
		task.TaskMeetingInvocation.RegistrationEvidenceID != request.RegistrationEvidenceID || task.TaskMeetingInvocation.AdmissionEvidenceID.Validate() != nil {
		return ErrTaskMeetingForbidden
	}
	return nil
}

func (s *SQLiteStore) recordTaskMeetingWorldObservationInline(ctx context.Context, request domainmeeting.WorldObservationRequest, inputHash string, task domaintask.Task, run domaintask.Run) (domainmeeting.WorldObservationReceipt, error) {
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	defer unlock()
	defer tx.Rollback()
	opID := taskMeetingWorldOperationID(request.Key, request.RequestID)
	prior, found, err := readOperationReceipt(ctx, tx, opID)
	if err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	if found {
		if prior.operationKind != taskMeetingWorldOperation || prior.inputSHA256 != inputHash {
			return domainmeeting.WorldObservationReceipt{}, ErrTaskMeetingConflict
		}
		var receipt domainmeeting.WorldObservationReceipt
		if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || validateTaskMeetingWorldReceiptTx(ctx, tx, receipt, request, task, run) != nil {
			return domainmeeting.WorldObservationReceipt{}, ErrTaskMeetingReceiptBad
		}
		if err := tx.Commit(); err != nil {
			return domainmeeting.WorldObservationReceipt{}, err
		}
		return receipt, nil
	}
	if !taskMeetingRootRunCanCreate(run.Status) {
		return domainmeeting.WorldObservationReceipt{}, ErrTaskMeetingForbidden
	}
	session, err := readTaskMeetingSessionReceiptTx(ctx, tx, request.Key.String(), request.Session)
	if err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	if session.ThreadID != request.Thread || session.ConfigRevision != request.ConfigRevision || session.ConfigSHA256 != request.ConfigSHA256 ||
		session.OwnerUserID != request.OwnerUserID || !equalStrings(session.Audience, request.Audience) {
		return domainmeeting.WorldObservationReceipt{}, ErrTaskMeetingConflict
	}
	if err := validateTaskMeetingWorldRegistrationTx(ctx, tx, request); err != nil {
		return domainmeeting.WorldObservationReceipt{}, ErrTaskMeetingForbidden
	}
	ownerProofID := modulecore.NewEvidenceID()
	scopeEvidenceID := modulecore.NewEvidenceID()
	traceID := modulecore.NewTraceID()
	eventID := modulecore.NewEventID()
	seq, err := s.nextSequence(ctx)
	if err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	snapshot := domainmeeting.WorldSnapshotEvidence{
		WorldID: request.Snapshot.WorldID, TimelineID: request.Snapshot.TimelineID,
		TimelineEpoch: request.Snapshot.TimelineEpoch, StateSeq: request.Snapshot.StateSeq,
		StateHash: request.Snapshot.StateHash, OwnerProofID: ownerProofID,
	}
	scope := domainmeeting.WorldScopeEvidence{
		EvidenceID: scopeEvidenceID, Key: request.Key, Generation: request.Generation, Epoch: request.Epoch,
		Snapshot: snapshot, ThreadID: request.Thread, LivingRoomRef: request.Rooms.Living,
		DiningRoomRef: request.Rooms.Dining, Audience: append([]string(nil), request.Audience...),
		ObservedAt: request.ObservedAt.UTC(), ValidUntil: request.ValidUntil.UTC(),
	}
	presence := make([]domainmeeting.PresenceEvidence, 0, len(request.ActorBindings))
	for index, binding := range request.ActorBindings {
		actor := request.Snapshot.Actors[index]
		atVenue := actor.Present && (actor.RoomID == request.Rooms.Living || actor.RoomID == request.Rooms.Dining)
		presence = append(presence, domainmeeting.PresenceEvidence{
			EvidenceID: modulecore.NewEvidenceID(), Key: request.Key, Generation: request.Generation, Epoch: request.Epoch,
			ScopeEvidenceID: scopeEvidenceID, Snapshot: snapshot, ActorID: binding.CoreActorID,
			WorldActorID: binding.WorldActorID, Present: atVenue,
			RoomRef: func() string {
				if atVenue {
					return actor.RoomID
				}
				return ""
			}(),
			ObservedAt: request.ObservedAt.UTC(), ValidUntil: request.ValidUntil.UTC(),
		})
	}
	receipt := domainmeeting.WorldObservationReceipt{EvidenceID: ownerProofID, Scope: scope, Presence: presence, ObservedAt: request.ObservedAt.UTC()}
	event := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: eventID, EventSeq: seq, TraceID: traceID,
		EventType: taskMeetingWorldEvent, ComponentID: taskMeetingComponent, OccurredAt: request.ObservedAt.UTC(),
		SessionID: request.Session, ThreadID: request.Thread, TaskID: task.TaskID, RunID: run.RunID,
		ActorKind: "agent", ActorID: task.TaskMeetingInvocation.CoordinatorActorID, EvidenceID: ownerProofID,
		Payload: map[string]any{
			"occurrence_key": request.Key.String(), "request_id": request.RequestID,
			"registration_evidence_id": string(request.RegistrationEvidenceID), "admission_evidence_id": string(task.TaskMeetingInvocation.AdmissionEvidenceID), "config_revision": request.ConfigRevision,
			"config_sha256": request.ConfigSHA256, "venue_basis": "owner_configured_venue_declaration",
			"world_snapshot": request.Snapshot, "actor_bindings": request.ActorBindings,
			"rooms": request.Rooms, "observation": receipt,
		},
	}
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	if err := insertOperationReceipt(ctx, tx, opID, taskMeetingWorldOperation, inputHash, receipt); err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return domainmeeting.WorldObservationReceipt{}, err
	}
	return receipt, nil
}

func (s *SQLiteStore) FindTaskMeetingWorldObservation(ctx context.Context, request domainmeeting.WorldObservationRequest, reader domaintask.TaskMeetingInvocationReader, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domainmeeting.WorldObservationReceipt, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return domainmeeting.WorldObservationReceipt{}, false, err
	}
	if ctx == nil || reader == nil || authorize == nil {
		return domainmeeting.WorldObservationReceipt{}, false, ErrTaskMeetingUnavailable
	}
	if request.Key.Validate() != nil || request.TaskID.Validate() != nil || request.RunID.Validate() != nil || request.Session.Validate() != nil || request.RequestID == "" {
		return domainmeeting.WorldObservationReceipt{}, false, errors.New("TaskMeeting World observation receipt lookup identity is invalid")
	}
	task, run, _, err := reader.GetTaskMeetingInvocationState(ctx, request.TaskID, request.RunID, authorize)
	if err != nil {
		return domainmeeting.WorldObservationReceipt{}, false, err
	}
	if err := validateTaskMeetingWorldRootBinding(request, task, run); err != nil {
		return domainmeeting.WorldObservationReceipt{}, false, err
	}
	return s.findTaskMeetingWorldObservation(ctx, request, task, run, "")
}

func (s *SQLiteStore) findTaskMeetingWorldObservation(ctx context.Context, request domainmeeting.WorldObservationRequest, task domaintask.Task, run domaintask.Run, expectedInputHash string) (domainmeeting.WorldObservationReceipt, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domainmeeting.WorldObservationReceipt{}, false, err
	}
	defer tx.Rollback()
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingWorldOperationID(request.Key, request.RequestID))
	if err != nil || !found {
		return domainmeeting.WorldObservationReceipt{}, found, err
	}
	if prior.operationKind != taskMeetingWorldOperation {
		return domainmeeting.WorldObservationReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if expectedInputHash != "" && prior.inputSHA256 != expectedInputHash {
		return domainmeeting.WorldObservationReceipt{}, false, ErrTaskMeetingConflict
	}
	var receipt domainmeeting.WorldObservationReceipt
	if err := json.Unmarshal([]byte(prior.resultJSON), &receipt); err != nil {
		return domainmeeting.WorldObservationReceipt{}, false, fmt.Errorf("%w: decode World receipt: %v", ErrTaskMeetingReceiptBad, err)
	}
	if err := validateTaskMeetingWorldReceiptTx(ctx, tx, receipt, request, task, run); err != nil {
		return domainmeeting.WorldObservationReceipt{}, false, fmt.Errorf("%w: validate World receipt: %v", ErrTaskMeetingReceiptBad, err)
	}
	if err := tx.Commit(); err != nil {
		return domainmeeting.WorldObservationReceipt{}, false, err
	}
	return receipt, true, nil
}

func validateTaskMeetingWorldReceiptTx(ctx context.Context, tx *sql.Tx, receipt domainmeeting.WorldObservationReceipt, request domainmeeting.WorldObservationRequest, task domaintask.Task, run domaintask.Run) error {
	if receipt.EvidenceID.Validate() != nil || receipt.Scope.EvidenceID.Validate() != nil || receipt.Scope.Snapshot.OwnerProofID != receipt.EvidenceID ||
		receipt.Scope.Key != request.Key || receipt.Scope.Generation != request.Generation || receipt.Scope.Epoch != request.Epoch ||
		receipt.Scope.ThreadID != request.Thread || !equalStrings(receipt.Scope.Audience, request.Audience) || len(receipt.Presence) != len(request.ActorBindings) {
		return fmt.Errorf("%w: World receipt fields", ErrTaskMeetingReceiptBad)
	}
	if err := validateTaskMeetingWorldRegistrationTx(ctx, tx, request); err != nil {
		return fmt.Errorf("%w: World registration: %v", ErrTaskMeetingReceiptBad, err)
	}
	for i, presence := range receipt.Presence {
		if presence.EvidenceID.Validate() != nil || presence.ScopeEvidenceID != receipt.Scope.EvidenceID || presence.ActorID != request.ActorBindings[i].CoreActorID || presence.WorldActorID != request.ActorBindings[i].WorldActorID ||
			presence.Snapshot != receipt.Scope.Snapshot || presence.Key != request.Key || presence.Generation != request.Generation || presence.Epoch != request.Epoch {
			return fmt.Errorf("%w: World presence %d", ErrTaskMeetingReceiptBad, i)
		}
	}
	var eventID, traceID, schema, eventType, component, occurred, envelope string
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE json_extract(envelope_json,'$.evidence_id')=? AND component_id=? AND event_type=?`, string(receipt.EvidenceID), taskMeetingComponent, taskMeetingWorldEvent).Scan(&eventID, &sequence, &traceID, &schema, &eventType, &component, &occurred, &envelope)
	if err != nil {
		return fmt.Errorf("%w: World event query: %v", ErrTaskMeetingReceiptBad, err)
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, component, occurred, envelope)
	if err != nil || event.SessionID != request.Session || event.ThreadID != request.Thread || event.TaskID != task.TaskID || event.RunID != run.RunID || event.EvidenceID != receipt.EvidenceID {
		return fmt.Errorf("%w: World event envelope err=%v session=%q/%q thread=%q/%q task=%q/%q run=%q/%q evidence=%q/%q", ErrTaskMeetingReceiptBad, err, event.SessionID, request.Session, event.ThreadID, request.Thread, event.TaskID, task.TaskID, event.RunID, run.RunID, event.EvidenceID, receipt.EvidenceID)
	}
	encoded, _ := json.Marshal(receipt)
	stored, _ := json.Marshal(event.Payload["observation"])
	var eventReceipt domainmeeting.WorldObservationReceipt
	if json.Unmarshal(stored, &eventReceipt) != nil {
		return fmt.Errorf("%w: decode World event receipt", ErrTaskMeetingReceiptBad)
	}
	canonicalEventReceipt, _ := json.Marshal(eventReceipt)
	if string(encoded) != string(canonicalEventReceipt) {
		return fmt.Errorf("%w: World receipt/event mismatch", ErrTaskMeetingReceiptBad)
	}
	return nil
}

func readTaskMeetingSessionReceiptTx(ctx context.Context, tx *sql.Tx, occurrenceKey string, expected modulecore.SessionID) (TaskMeetingGroupSessionReceipt, error) {
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingSessionOperationID(occurrenceKey))
	if err != nil || !found || prior.operationKind != taskMeetingOpenOperation {
		return TaskMeetingGroupSessionReceipt{}, ErrTaskMeetingReceiptBad
	}
	var receipt TaskMeetingGroupSessionReceipt
	if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || receipt.SessionID != expected || validateTaskMeetingOpenEvent(ctx, tx, receipt) != nil {
		return TaskMeetingGroupSessionReceipt{}, ErrTaskMeetingReceiptBad
	}
	return receipt, nil
}

func taskMeetingWorldOperationID(key domainmeeting.OccurrenceKey, requestID string) string {
	return "taskmeeting:world:" + key.String() + ":" + requestID
}

func (s *SQLiteStore) ReadTaskMeetingGroupMessage(
	ctx context.Context,
	reference domaintask.TaskMeetingMessageReference,
	actorID string,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	reader domaintask.TaskMeetingInvocationReader,
	authorize domaintask.TaskMeetingMessageReadAuthorizationCheck,
) (string, error) {
	if err := s.ensureOpen(); err != nil {
		return "", err
	}
	if ctx == nil || reader == nil || authorize == nil || reference.Validate() != nil || taskID.Validate() != nil || runID.Validate() != nil || actorID == "" {
		return "", ErrTaskMeetingUnavailable
	}
	check := func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
		if task.TaskID != taskID || run.TaskID != taskID || run.RunID != runID || task.Assignee != actorID ||
			task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext ||
			task.TaskMeetingInvocation.ActorID != actorID || task.TaskMeetingInvocation.OccurrenceKey != reference.OccurrenceKey ||
			task.TaskMeetingInvocation.ConfigRevision != reference.ConfigRevision || task.TaskMeetingInvocation.ConfigSHA256 != reference.ConfigSHA256 ||
			task.TaskMeetingInvocation.AudienceSHA256 != reference.AudienceSHA256 {
			return ErrTaskMeetingForbidden
		}
		return authorize(checkCtx, task, run, shared, reference)
	}
	task, run, _, err := reader.GetTaskMeetingInvocationState(ctx, taskID, runID, check)
	if err != nil {
		return "", err
	}
	if err := validateTaskMeetingRecipient(task, run, reference, actorID); err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	var body string
	err = readTaskMeetingMessageBodyTx(ctx, tx, reference, actorID, &body)
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	// Re-read and reauthorize after the body fetch. A receipt or ID from the
	// caller never substitutes for the current native actor scope.
	taskAfter, runAfter, _, err := reader.GetTaskMeetingInvocationState(ctx, taskID, runID, check)
	if err != nil {
		return "", err
	}
	if err := validateTaskMeetingRecipient(taskAfter, runAfter, reference, actorID); err != nil || taskAfter.TaskID != task.TaskID || runAfter.RunID != run.RunID || !sameTaskMeetingInvocation(taskAfter.TaskMeetingInvocation, task.TaskMeetingInvocation) {
		return "", ErrTaskMeetingForbidden
	}
	return body, nil
}

func validateTaskMeetingRecipient(task domaintask.Task, run domaintask.Run, reference domaintask.TaskMeetingMessageReference, actorID string) error {
	invocation := task.TaskMeetingInvocation
	if invocation == nil || !domaintask.IsTaskMeetingTask(task) || invocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext ||
		task.TaskID == "" || task.Assignee != actorID || invocation.ActorID != actorID || run.TaskID != task.TaskID || run.RunID == "" ||
		invocation.OccurrenceKey != reference.OccurrenceKey || invocation.ConfigRevision != reference.ConfigRevision ||
		invocation.ConfigSHA256 != reference.ConfigSHA256 || invocation.AudienceSHA256 != reference.AudienceSHA256 {
		return ErrTaskMeetingForbidden
	}
	return nil
}

func sameTaskMeetingInvocation(left, right *domaintask.TaskMeetingInvocation) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func readTaskMeetingMessageBodyTx(ctx context.Context, tx *sql.Tx, reference domaintask.TaskMeetingMessageReference, actorID string, body *string) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	var eventID, traceID, schema, eventType, component, occurred, envelope string
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE event_id=?`, string(reference.EventID)).Scan(&eventID, &sequence, &traceID, &schema, &eventType, &component, &occurred, &envelope)
	if err != nil {
		return ErrTaskMeetingReceiptBad
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, component, occurred, envelope)
	if err != nil || event.ComponentID != taskMeetingComponent || event.EventType != taskMeetingMessageEvent || event.EventID != reference.EventID ||
		event.SessionID != reference.SessionID || event.ThreadID != reference.ThreadID || event.MessageID != reference.MessageID {
		return ErrTaskMeetingReceiptBad
	}
	var storedBody string
	if err := json.Unmarshal(mustJSON(event.Payload["body"]), &storedBody); err != nil || taskMeetingBodySHA256(storedBody) != reference.BodySHA256 ||
		payloadString(event.Payload, "occurrence_key") != reference.OccurrenceKey || payloadString(event.Payload, "audience_sha256") != reference.AudienceSHA256 ||
		payloadString(event.Payload, "config_sha256") != reference.ConfigSHA256 || eventPayloadUint(event.Payload["config_revision"]) != reference.ConfigRevision ||
		eventPayloadUint(event.Payload["attempt_generation"]) != reference.AttemptGeneration || eventPayloadUint(event.Payload["attempt_epoch"]) != reference.AttemptEpoch ||
		payloadString(event.Payload, "scope_evidence_id") != string(reference.ScopeEvidenceID) {
		return ErrTaskMeetingReceiptBad
	}
	audience, ok := event.Payload["audience"].([]any)
	if !ok || !payloadAudienceContains(audience, actorID) {
		return ErrTaskMeetingForbidden
	}
	if err := validateTaskMeetingMessageReferenceReceiptTx(ctx, tx, event, reference, storedBody); err != nil {
		return err
	}
	*body = storedBody
	return nil
}

func validateTaskMeetingMessageReferenceReceiptTx(ctx context.Context, tx *sql.Tx, event modulecore.EventEnvelope, reference domaintask.TaskMeetingMessageReference, body string) error {
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingMessageOperationID(reference.OccurrenceKey, reference.MessageID))
	if err != nil || !found || prior.operationKind != taskMeetingMessageOperation {
		return ErrTaskMeetingReceiptBad
	}
	var receipt TaskMeetingGroupMessageReceipt
	if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || receipt.Reference != reference || receipt.EvidenceID != event.EvidenceID ||
		receipt.Reference.BodySHA256 != taskMeetingBodySHA256(body) || len(receipt.Audience) != domaintask.TaskMeetingAudienceSize || len(receipt.WorkIDs) != len(receipt.Audience) ||
		!payloadAudienceEqual(event.Payload["audience"], receipt.Audience) {
		return ErrTaskMeetingReceiptBad
	}
	session, err := readTaskMeetingSessionReceiptTx(ctx, tx, reference.OccurrenceKey, reference.SessionID)
	if err != nil || session.ThreadID != reference.ThreadID || session.ConfigSHA256 != reference.ConfigSHA256 ||
		session.ConfigRevision != reference.ConfigRevision || session.AudienceSHA256 != reference.AudienceSHA256 || !equalStrings(session.Audience, receipt.Audience) {
		return ErrTaskMeetingReceiptBad
	}
	for i, actor := range receipt.Audience {
		work, _, err := readWorkForUpdate(ctx, tx, receipt.WorkIDs[i])
		if err != nil || work.Kind != taskMeetingDeliveryWorkKind(actor) || work.SourceEventID != reference.EventID || work.SessionID != reference.SessionID ||
			work.DedupeKey != taskMeetingDeliveryDedupeKey(reference.OccurrenceKey, reference.MessageID, actor) {
			return ErrTaskMeetingReceiptBad
		}
	}
	return nil
}

func mustJSON(value any) []byte { encoded, _ := json.Marshal(value); return encoded }

func eventPayloadUint(value any) uint64 {
	switch typed := value.(type) {
	case float64:
		if typed >= 0 && typed <= float64(^uint64(0)) {
			return uint64(typed)
		}
	case json.Number:
		parsed, _ := typed.Int64()
		if parsed >= 0 {
			return uint64(parsed)
		}
	case uint64:
		return typed
	case int64:
		if typed >= 0 {
			return uint64(typed)
		}
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	}
	return 0
}

func payloadAudienceContains(audience []any, actorID string) bool {
	for _, item := range audience {
		if value, ok := item.(string); ok && value == actorID {
			return true
		}
	}
	return false
}

func payloadAudienceEqual(value any, expected []string) bool {
	audience, ok := value.([]any)
	if !ok || len(audience) != len(expected) {
		return false
	}
	for i, item := range audience {
		actor, ok := item.(string)
		if !ok || actor != expected[i] {
			return false
		}
	}
	return true
}

func (s *SQLiteStore) AppendTaskMeetingGroupConsumption(
	ctx context.Context,
	proof domaintask.TaskMeetingConsumedMessage,
	stateReader domaintask.TaskMeetingInvocationReader,
	consumptionReader domaintask.TaskMeetingMessageConsumptionReader,
	authorize domaintask.TaskMeetingMessageReadAuthorizationCheck,
) (TaskMeetingGroupConsumptionReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	if ctx == nil || stateReader == nil || consumptionReader == nil || authorize == nil || proof.Reference.Validate() != nil {
		return TaskMeetingGroupConsumptionReceipt{}, ErrTaskMeetingUnavailable
	}
	check := func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
		return authorize(checkCtx, task, run, shared, proof.Reference)
	}
	task, run, _, err := stateReader.GetTaskMeetingInvocationState(ctx, proof.ConsumerTaskID, proof.ConsumerRunID, func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
		if task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext || task.Assignee != proof.ActorID ||
			task.TaskMeetingInvocation.ActorID != proof.ActorID || task.TaskMeetingInvocation.OccurrenceKey != proof.Reference.OccurrenceKey ||
			task.TaskMeetingInvocation.ConfigRevision != proof.Reference.ConfigRevision || task.TaskMeetingInvocation.ConfigSHA256 != proof.Reference.ConfigSHA256 ||
			task.TaskMeetingInvocation.AudienceSHA256 != proof.Reference.AudienceSHA256 {
			return ErrTaskMeetingForbidden
		}
		return check(checkCtx, task, run, shared)
	})
	if err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	if err := proof.Validate(task, run); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, ErrTaskMeetingForbidden
	}
	storedProof, err := consumptionReader.FindTaskMeetingConsumedMessage(ctx, task.TaskID, run.RunID, proof.Reference, func(checkCtx context.Context, checkTask domaintask.Task, checkRun domaintask.Run, checkShared domaintask.SharedRoleContext, reference domaintask.TaskMeetingMessageReference) error {
		return authorize(checkCtx, checkTask, checkRun, checkShared, reference)
	})
	if err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	want, _ := hashJSON(proof)
	got, _ := hashJSON(storedProof)
	if want == "" || want != got || storedProof.Validate(task, run) != nil {
		return TaskMeetingGroupConsumptionReceipt{}, ErrTaskMeetingConflict
	}
	inputHash := want
	if receipt, found, err := s.findTaskMeetingGroupConsumption(ctx, proof.Reference, proof.ActorID, inputHash); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	} else if found {
		return receipt, nil
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(proof)
		if err != nil {
			return TaskMeetingGroupConsumptionReceipt{}, err
		}
		var receipt TaskMeetingGroupConsumptionReceipt
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var inner error
			receipt, inner = s.appendTaskMeetingGroupConsumptionInline(commandCtx, proof, task, run, inputHash)
			return inner
		})
		return receipt, err
	}
	return s.appendTaskMeetingGroupConsumptionInline(ctx, proof, task, run, inputHash)
}

func (s *SQLiteStore) appendTaskMeetingGroupConsumptionInline(ctx context.Context, proof domaintask.TaskMeetingConsumedMessage, task domaintask.Task, run domaintask.Run, inputHash string) (TaskMeetingGroupConsumptionReceipt, error) {
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	defer unlock()
	defer tx.Rollback()
	if prior, found, err := readOperationReceipt(ctx, tx, taskMeetingConsumptionOperationID(proof.Reference, proof.ActorID)); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	} else if found {
		if prior.operationKind != taskMeetingConsumeOperation || prior.inputSHA256 != inputHash {
			return TaskMeetingGroupConsumptionReceipt{}, ErrTaskMeetingConflict
		}
		var receipt TaskMeetingGroupConsumptionReceipt
		if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || validateTaskMeetingGroupConsumptionReceiptTx(ctx, tx, receipt, proof, task, run) != nil {
			return TaskMeetingGroupConsumptionReceipt{}, ErrTaskMeetingReceiptBad
		}
		if err := tx.Commit(); err != nil {
			return TaskMeetingGroupConsumptionReceipt{}, err
		}
		return receipt, nil
	}
	if err := validateTaskMeetingReferenceEventTx(ctx, tx, proof.Reference, proof.ActorID); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	workID := taskMeetingDeliveryWorkID(proof.Reference.OccurrenceKey, proof.Reference.MessageID, proof.ActorID)
	evidenceID, eventID, traceID := modulecore.NewEvidenceID(), modulecore.NewEventID(), modulecore.NewTraceID()
	seq, err := s.nextSequence(ctx)
	if err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	now := s.taskMeetingNowUTC()
	event := modulecore.EventEnvelope{SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: eventID, EventSeq: seq, TraceID: traceID,
		EventType: taskMeetingConsumeEvent, ComponentID: taskMeetingComponent, OccurredAt: now, SessionID: proof.Reference.SessionID,
		ThreadID: proof.Reference.ThreadID, TaskID: task.TaskID, RunID: run.RunID, MessageID: proof.Reference.MessageID,
		ActorKind: "agent", ActorID: proof.ActorID, EvidenceID: evidenceID,
		Payload: map[string]any{"proof": proof, "work_id": workID},
	}
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	receipt := TaskMeetingGroupConsumptionReceipt{Proof: proof, WorkID: workID, EventID: eventID, EvidenceID: evidenceID, RecordedAt: now}
	if err := insertOperationReceipt(ctx, tx, taskMeetingConsumptionOperationID(proof.Reference, proof.ActorID), taskMeetingConsumeOperation, inputHash, receipt); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, err
	}
	return receipt, nil
}

func (s *SQLiteStore) findTaskMeetingGroupConsumption(ctx context.Context, ref domaintask.TaskMeetingMessageReference, actorID, expectedHash string) (TaskMeetingGroupConsumptionReceipt, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, false, err
	}
	defer tx.Rollback()
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingConsumptionOperationID(ref, actorID))
	if err != nil || !found {
		return TaskMeetingGroupConsumptionReceipt{}, found, err
	}
	if prior.operationKind != taskMeetingConsumeOperation || expectedHash != "" && prior.inputSHA256 != expectedHash {
		return TaskMeetingGroupConsumptionReceipt{}, false, ErrTaskMeetingConflict
	}
	var receipt TaskMeetingGroupConsumptionReceipt
	if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || receipt.Proof.Reference != ref || receipt.Proof.ActorID != actorID {
		return TaskMeetingGroupConsumptionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := validateTaskMeetingReferenceEventTx(ctx, tx, ref, actorID); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, false, err
	}
	var eventCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_envelope WHERE event_id=? AND component_id=? AND event_type=? AND json_extract(envelope_json,'$.evidence_id')=?`, string(receipt.EventID), taskMeetingComponent, taskMeetingConsumeEvent, string(receipt.EvidenceID)).Scan(&eventCount); err != nil || eventCount != 1 {
		return TaskMeetingGroupConsumptionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupConsumptionReceipt{}, false, err
	}
	return receipt, true, nil
}

func validateTaskMeetingGroupConsumptionReceiptTx(ctx context.Context, tx *sql.Tx, receipt TaskMeetingGroupConsumptionReceipt, proof domaintask.TaskMeetingConsumedMessage, task domaintask.Task, run domaintask.Run) error {
	if receipt.Proof != proof || receipt.WorkID != taskMeetingDeliveryWorkID(proof.Reference.OccurrenceKey, proof.Reference.MessageID, proof.ActorID) || receipt.EvidenceID.Validate() != nil || receipt.EventID.Validate() != nil || receipt.Proof.Validate(task, run) != nil {
		return ErrTaskMeetingReceiptBad
	}
	return validateTaskMeetingReferenceEventTx(ctx, tx, proof.Reference, proof.ActorID)
}

func validateTaskMeetingReferenceEventTx(ctx context.Context, tx *sql.Tx, ref domaintask.TaskMeetingMessageReference, actorID string) error {
	var body string
	if err := readTaskMeetingMessageBodyTx(ctx, tx, ref, actorID, &body); err != nil {
		return err
	}
	var sessionOwner, runtimeVersion string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id,runtime_version FROM live_conversation_session WHERE session_id=?`, string(ref.SessionID)).Scan(&sessionOwner, &runtimeVersion); err != nil || runtimeVersion != taskMeetingRuntimeVersion || sessionOwner == "" {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func taskMeetingConsumptionOperationID(ref domaintask.TaskMeetingMessageReference, actor string) string {
	hash, _ := hashJSON(struct {
		Occurrence string               `json:"occurrence"`
		Message    modulecore.MessageID `json:"message"`
		Actor      string               `json:"actor"`
	}{ref.OccurrenceKey, ref.MessageID, actor})
	return "taskmeeting:consumed:" + hash
}

func taskMeetingWorkHasConsumptionReceipt(ctx context.Context, tx *sql.Tx, work LeasedWorkItem) bool {
	actorID := strings.TrimPrefix(work.Kind, "taskmeeting.group_delivery:")
	if actorID == "" || work.Kind != taskMeetingDeliveryWorkKind(actorID) {
		return false
	}
	var eventID, traceID, schema, eventType, component, occurred, envelope string
	var sequence int64
	err := tx.QueryRowContext(ctx, `
		SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json
		FROM event_envelope
		WHERE component_id=? AND event_type=? AND json_extract(envelope_json,'$.payload.work_id')=?
		ORDER BY event_seq DESC LIMIT 1`, taskMeetingComponent, taskMeetingConsumeEvent, work.WorkID,
	).Scan(&eventID, &sequence, &traceID, &schema, &eventType, &component, &occurred, &envelope)
	if err != nil {
		return false
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, component, occurred, envelope)
	if err != nil || event.EventType != taskMeetingConsumeEvent || event.ComponentID != taskMeetingComponent || event.ActorID != actorID ||
		event.SessionID != work.SessionID || event.MessageID == "" || event.EvidenceID == "" {
		return false
	}
	encoded := mustJSON(event.Payload["proof"])
	var proof domaintask.TaskMeetingConsumedMessage
	if json.Unmarshal(encoded, &proof) != nil || proof.ActorID != actorID || proof.Reference.EventID != work.SourceEventID ||
		proof.Reference.SessionID != work.SessionID || proof.Reference.MessageID != event.MessageID || proof.Reference.ThreadID != event.ThreadID ||
		proof.Reference.Validate() != nil || proof.ConsumerTaskID.Validate() != nil || proof.ConsumerRunID.Validate() != nil ||
		!validTaskMeetingDigest(proof.ContextSHA256) || proof.EvidenceID.Validate() != nil || proof.AcceptedAt.IsZero() {
		return false
	}
	var receiptEvent TaskMeetingGroupConsumptionReceipt
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingConsumptionOperationID(proof.Reference, actorID))
	if err != nil || !found || prior.operationKind != taskMeetingConsumeOperation || json.Unmarshal([]byte(prior.resultJSON), &receiptEvent) != nil {
		return false
	}
	if receiptEvent.Proof != proof || receiptEvent.EventID != event.EventID || receiptEvent.EvidenceID != event.EvidenceID || receiptEvent.WorkID != work.WorkID {
		return false
	}
	return validateTaskMeetingReferenceEventTx(ctx, tx, proof.Reference, actorID) == nil
}

func (s *SQLiteStore) AckTaskMeetingGroupDelivery(ctx context.Context, input WorkLeaseInput) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("TaskMeeting delivery acknowledgment context is required")
	}
	if input.WorkID == "" || input.LeaseToken == "" || input.Version < 1 {
		return ErrWorkConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	work, state, err := readWorkForUpdate(ctx, tx, input.WorkID)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(work.Kind, "taskmeeting.group_delivery:") {
		return ErrWorkConflict
	}
	if state == "completed" {
		if work.LeaseToken == input.LeaseToken && work.Version == input.Version && taskMeetingWorkHasConsumptionReceipt(ctx, tx, work) {
			return tx.Commit()
		}
		return ErrWorkConflict
	}
	if _, err := validateWorkLease(work, state, input, normalizedNow(input.Now)); err != nil {
		return err
	}
	if !taskMeetingWorkHasConsumptionReceipt(ctx, tx, work) {
		return ErrWorkResultNotPersisted
	}
	if err := completeLeasedWork(ctx, tx, input); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) ClaimTaskMeetingGroupDelivery(ctx context.Context, actorID string, input WorkClaimInput, authorize TaskMeetingRecipientAuthorizationCheck) ([]LeasedTaskMeetingDelivery, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if ctx == nil || authorize == nil || actorID == "" || strings.TrimSpace(actorID) != actorID || strings.ToLower(actorID) != actorID {
		return nil, ErrTaskMeetingUnavailable
	}
	if input.Limit < 1 || input.Limit > maxWorkClaimBatch || input.LeaseDuration <= 0 || input.LeaseDuration > maxWorkLeaseDuration ||
		(input.Assignee != "" && input.Assignee != actorID) || (input.Kind != "" && input.Kind != taskMeetingDeliveryWorkKind(actorID)) {
		return nil, errors.New("invalid TaskMeeting recipient work claim")
	}
	kind := taskMeetingDeliveryWorkKind(actorID)
	now := normalizedNow(input.Now)
	leaseExpiry := now.Add(input.LeaseDuration)
	readTx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	rows, err := readTx.QueryContext(ctx, `
		SELECT work_id,dedupe_key,source_event_id,session_id,priority,state,attempt,not_before,COALESCE(lease_token,''),COALESCE(lease_expires_at,''),version
		FROM live_work_outbox WHERE kind=? AND ((state='pending' AND (not_before='' OR julianday(not_before)<=julianday(?))) OR
		(state='leased' AND lease_expires_at IS NOT NULL AND julianday(lease_expires_at)<=julianday(?)))
		ORDER BY priority ASC,work_id ASC LIMIT ?`, kind, formatWorkTime(now), formatWorkTime(now), input.Limit)
	if err != nil {
		_ = readTx.Rollback()
		return nil, err
	}
	candidates := make([]taskMeetingDeliveryCandidate, 0, input.Limit)
	for rows.Next() {
		var item taskMeetingDeliveryCandidate
		var eventID, sessionID string
		var notBefore string
		if err := rows.Scan(&item.work.WorkID, &item.work.DedupeKey, &eventID, &sessionID, &item.work.Priority, &item.state, &item.work.Attempt, &notBefore, &item.work.LeaseToken, &item.expires, &item.work.Version); err != nil {
			rows.Close()
			_ = readTx.Rollback()
			return nil, err
		}
		item.work.Kind, item.work.SourceEventID, item.work.SessionID = kind, modulecore.EventID(eventID), modulecore.SessionID(sessionID)
		item.work.NotBefore, err = parseWorkTime(notBefore)
		if err != nil {
			rows.Close()
			_ = readTx.Rollback()
			return nil, err
		}
		item.work.LeaseExpiresAt, err = parseWorkTime(item.expires)
		if err != nil {
			rows.Close()
			_ = readTx.Rollback()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		_ = readTx.Rollback()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		_ = readTx.Rollback()
		return nil, err
	}
	for index := range candidates {
		ref, err := taskMeetingMessageReferenceForWork(ctx, readTx, candidates[index].work)
		if err != nil {
			_ = readTx.Rollback()
			return nil, err
		}
		candidates[index].reference = ref
	}
	if err := readTx.Commit(); err != nil {
		return nil, err
	}
	// Authorization may read TaskManager's separate SQLite database. Keep it
	// outside the EventStore transaction to preserve the cross-owner lock order.
	authorized := make([]taskMeetingDeliveryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if err := authorize(ctx, actorID, candidate.reference); err != nil {
			return nil, ErrTaskMeetingForbidden
		}
		authorized = append(authorized, candidate)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	claimed := make([]LeasedTaskMeetingDelivery, 0, len(authorized))
	for _, candidate := range authorized {
		current, state, err := readWorkForUpdate(ctx, tx, candidate.work.WorkID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		if current.Version != candidate.work.Version || current.Kind != kind || current.SourceEventID != candidate.work.SourceEventID || current.SessionID != candidate.work.SessionID || current.DedupeKey != candidate.work.DedupeKey ||
			!taskMeetingWorkClaimable(current, state, now) {
			continue
		}
		freshRef, err := taskMeetingMessageReferenceForWork(ctx, tx, current)
		if err != nil || freshRef != candidate.reference {
			return nil, ErrTaskMeetingConflict
		}
		// The body and any existing consumption event are consulted only after
		// the recipient owner authorized this exact immutable reference.
		if err := validateTaskMeetingReferenceEventTx(ctx, tx, freshRef, actorID); err != nil {
			return nil, err
		}
		if taskMeetingWorkHasConsumptionReceipt(ctx, tx, current) {
			result, err := tx.ExecContext(ctx, `UPDATE live_work_outbox SET state='completed',lease_token=NULL,lease_expires_at=NULL WHERE work_id=? AND version=? AND state IN ('pending','leased')`, current.WorkID, current.Version)
			if err != nil {
				return nil, err
			}
			if updated, _ := result.RowsAffected(); updated != 1 {
				continue
			}
			continue
		}
		token := string(modulecore.NewRequestID())
		result, err := tx.ExecContext(ctx, `UPDATE live_work_outbox SET state='leased',attempt=attempt+1,lease_token=?,lease_expires_at=?,version=version+1 WHERE work_id=? AND version=? AND ((state='pending' AND (not_before='' OR julianday(not_before)<=julianday(?))) OR (state='leased' AND lease_expires_at IS NOT NULL AND julianday(lease_expires_at)<=julianday(?)))`, token, formatWorkTime(leaseExpiry), current.WorkID, current.Version, formatWorkTime(now), formatWorkTime(now))
		if err != nil {
			return nil, err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			continue
		}
		current.Attempt++
		current.LeaseToken, current.LeaseExpiresAt, current.Version, current.Assignee = token, leaseExpiry, current.Version+1, actorID
		claimed = append(claimed, LeasedTaskMeetingDelivery{Work: current, Reference: freshRef})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

type taskMeetingDeliveryCandidate struct {
	work      LeasedWorkItem
	state     string
	expires   string
	reference domaintask.TaskMeetingMessageReference
}

func taskMeetingWorkClaimable(work LeasedWorkItem, state string, now time.Time) bool {
	switch state {
	case "pending":
		return work.NotBefore.IsZero() || !work.NotBefore.After(now)
	case "leased":
		return !work.LeaseExpiresAt.IsZero() && !work.LeaseExpiresAt.After(now)
	default:
		return false
	}
}

func taskMeetingMessageReferenceForWork(ctx context.Context, tx *sql.Tx, work LeasedWorkItem) (domaintask.TaskMeetingMessageReference, error) {
	if !strings.HasPrefix(work.Kind, "taskmeeting.group_delivery:") {
		return domaintask.TaskMeetingMessageReference{}, ErrTaskMeetingForbidden
	}
	occurrenceKey, messageID, actorID, ok := taskMeetingDeliveryKeyParts(work.DedupeKey)
	if !ok || actorID != strings.TrimPrefix(work.Kind, "taskmeeting.group_delivery:") {
		return domaintask.TaskMeetingMessageReference{}, ErrTaskMeetingReceiptBad
	}
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingMessageOperationID(occurrenceKey, modulecore.MessageID(messageID)))
	if err != nil || !found || prior.operationKind != taskMeetingMessageOperation {
		return domaintask.TaskMeetingMessageReference{}, ErrTaskMeetingReceiptBad
	}
	var receipt TaskMeetingGroupMessageReceipt
	if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || prior.inputSHA256 == "" || receipt.Reference.Validate() != nil ||
		receipt.Reference.OccurrenceKey != occurrenceKey || string(receipt.Reference.MessageID) != messageID || receipt.Reference.EventID != work.SourceEventID ||
		receipt.Reference.SessionID != work.SessionID || !containsString(receipt.Audience, actorID) || len(receipt.WorkIDs) != len(receipt.Audience) {
		return domaintask.TaskMeetingMessageReference{}, ErrTaskMeetingReceiptBad
	}
	actorIndex := -1
	for index, audienceActor := range receipt.Audience {
		if audienceActor == actorID {
			actorIndex = index
			break
		}
	}
	if actorIndex < 0 || receipt.WorkIDs[actorIndex] != work.WorkID || taskMeetingDeliveryWorkKind(actorID) != work.Kind ||
		work.DedupeKey != taskMeetingDeliveryDedupeKey(occurrenceKey, modulecore.MessageID(messageID), actorID) {
		return domaintask.TaskMeetingMessageReference{}, ErrTaskMeetingReceiptBad
	}
	return receipt.Reference, nil
}

func taskMeetingDeliveryKeyParts(dedupeKey string) (occurrenceKey, messageID, actorID string, ok bool) {
	const prefix = "taskmeeting:delivery:"
	if !strings.HasPrefix(dedupeKey, prefix) {
		return "", "", "", false
	}
	value := strings.TrimPrefix(dedupeKey, prefix)
	actorSeparator := strings.LastIndexByte(value, ':')
	if actorSeparator <= 0 || actorSeparator == len(value)-1 {
		return "", "", "", false
	}
	actorID = value[actorSeparator+1:]
	value = value[:actorSeparator]
	messageSeparator := strings.LastIndexByte(value, ':')
	if messageSeparator <= 0 || messageSeparator == len(value)-1 {
		return "", "", "", false
	}
	occurrenceKey, messageID = value[:messageSeparator], value[messageSeparator+1:]
	return occurrenceKey, messageID, actorID, occurrenceKey != "" && messageID != "" && actorID != ""
}

func payloadAudienceContainsAny(value any, actor string) bool {
	audience, ok := value.([]any)
	return ok && payloadAudienceContains(audience, actor)
}

// EnsureTaskMeetingGroupSession creates the real EventStore session and
// thread mapping, canonical open event, and idempotency receipt atomically.
// The TaskManager reader verifies the durable root Task/Run and current owner
// authorization before EventStore assigns identities.
func (s *SQLiteStore) EnsureTaskMeetingGroupSession(
	ctx context.Context,
	request TaskMeetingGroupSessionRequest,
	reader domaintask.TaskMeetingInvocationReader,
	authorizeRead domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeCreate domaintask.TaskMeetingStateAuthorizationCheck,
) (TaskMeetingGroupSessionReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	if ctx == nil || reader == nil || authorizeRead == nil {
		return TaskMeetingGroupSessionReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := validateTaskMeetingGroupSessionRequest(request); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	task, run, shared, err := reader.GetTaskMeetingInvocationState(ctx, request.TaskID, request.RunID, authorizeRead)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, fmt.Errorf("authorize TaskMeeting root invocation: %w", err)
	}
	if err := validateTaskMeetingRootBinding(request, task, run, false); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	inputHash, err := hashJSON(request)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	prior, found, err := s.findTaskMeetingGroupSession(ctx, request, task, run, inputHash)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	if found {
		return prior, nil
	}
	if authorizeCreate == nil {
		return TaskMeetingGroupSessionReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := ctx.Err(); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	if !taskMeetingRootRunCanCreate(run.Status) || authorizeCreate(ctx, task, run, shared) != nil {
		return TaskMeetingGroupSessionReceipt{}, ErrTaskMeetingForbidden
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(request)
		if err != nil {
			return TaskMeetingGroupSessionReceipt{}, err
		}
		var receipt TaskMeetingGroupSessionReceipt
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var inner error
			receipt, inner = s.ensureTaskMeetingGroupSessionInline(commandCtx, request, inputHash, task, run)
			return inner
		})
		return receipt, err
	}
	return s.ensureTaskMeetingGroupSessionInline(ctx, request, inputHash, task, run)
}

// FindTaskMeetingGroupSession recovers the exact frozen session mapping. It
// uses the TaskManager read authorization path, so an existing receipt can be
// inspected under historical read authority without granting a new session.
func (s *SQLiteStore) FindTaskMeetingGroupSession(
	ctx context.Context,
	request TaskMeetingGroupSessionRequest,
	reader domaintask.TaskMeetingInvocationReader,
	authorize domaintask.TaskMeetingReadAuthorizationCheck,
) (TaskMeetingGroupSessionReceipt, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	if ctx == nil || reader == nil || authorize == nil {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingUnavailable
	}
	if err := validateTaskMeetingGroupSessionRequest(request); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	task, run, _, err := reader.GetTaskMeetingInvocationState(ctx, request.TaskID, request.RunID, authorize)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	if err := validateTaskMeetingRootBinding(request, task, run, false); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	inputHash, err := hashJSON(request)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	receipt, found, err := s.findTaskMeetingGroupSession(ctx, request, task, run, inputHash)
	return receipt, found, err
}

func (s *SQLiteStore) findTaskMeetingGroupSession(
	ctx context.Context,
	request TaskMeetingGroupSessionRequest,
	task domaintask.Task,
	run domaintask.Run,
	inputHash string,
) (TaskMeetingGroupSessionReceipt, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	defer tx.Rollback()
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingSessionOperationID(request.OccurrenceKey))
	if err != nil || !found {
		return TaskMeetingGroupSessionReceipt{}, found, err
	}
	if prior.operationKind != taskMeetingOpenOperation || prior.inputSHA256 != inputHash {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingConflict
	}
	var receipt TaskMeetingGroupSessionReceipt
	if err := json.Unmarshal([]byte(prior.resultJSON), &receipt); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := validateTaskMeetingGroupSessionReceipt(receipt, request, task, run); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	if err := validateTaskMeetingOpenEvent(ctx, tx, receipt); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	var ownerID, runtimeVersion string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id,runtime_version FROM live_conversation_session WHERE session_id=?`, string(receipt.SessionID)).Scan(&ownerID, &runtimeVersion); err != nil || ownerID != request.OwnerUserID || runtimeVersion != taskMeetingRuntimeVersion {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	return receipt, true, nil
}

func (s *SQLiteStore) ensureTaskMeetingGroupSessionInline(
	ctx context.Context,
	request TaskMeetingGroupSessionRequest,
	inputHash string,
	task domaintask.Task,
	run domaintask.Run,
) (TaskMeetingGroupSessionReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	defer unlock()
	defer tx.Rollback()

	operationID := taskMeetingSessionOperationID(request.OccurrenceKey)
	prior, found, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	if found {
		if prior.operationKind != taskMeetingOpenOperation || prior.inputSHA256 != inputHash {
			return TaskMeetingGroupSessionReceipt{}, ErrTaskMeetingConflict
		}
		var receipt TaskMeetingGroupSessionReceipt
		if err := json.Unmarshal([]byte(prior.resultJSON), &receipt); err != nil {
			return TaskMeetingGroupSessionReceipt{}, ErrTaskMeetingReceiptBad
		}
		if err := validateTaskMeetingGroupSessionReceipt(receipt, request, task, run); err != nil {
			return TaskMeetingGroupSessionReceipt{}, fmt.Errorf("validate TaskMeeting session receipt: %w", err)
		}
		if err := validateTaskMeetingOpenEvent(ctx, tx, receipt); err != nil {
			return TaskMeetingGroupSessionReceipt{}, err
		}
		if err := tx.Commit(); err != nil {
			return TaskMeetingGroupSessionReceipt{}, err
		}
		return receipt, nil
	}

	sessionID := modulecore.NewSessionID()
	threadID := modulecore.NewThreadID()
	evidenceID := modulecore.NewEvidenceID()
	openedEventID := modulecore.NewEventID()
	traceID := modulecore.NewTraceID()
	openedAt := s.taskMeetingNowUTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_conversation_session
			(session_id,owner_id,runtime_version,cutover_seq,admission_seq,projection_watermark,epoch)
		VALUES (?,?,?,0,0,0,1)`, string(sessionID), request.OwnerUserID, taskMeetingRuntimeVersion); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	sequence, err := s.nextSequence(ctx)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	_, audienceHash, err := canonicalTaskMeetingAudience(request.Audience)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	event := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       openedEventID, EventSeq: sequence, TraceID: traceID,
		EventType: taskMeetingOpenEvent, ComponentID: taskMeetingComponent, OccurredAt: openedAt,
		SessionID: sessionID, ThreadID: threadID, TaskID: task.TaskID, RunID: run.RunID,
		ActorKind: "agent", ActorID: request.CoordinatorActorID, EvidenceID: evidenceID,
		Payload: map[string]any{
			"occurrence_key":           request.OccurrenceKey,
			"configured_owner_user_id": request.OwnerUserID,
			"coordinator_actor_id":     request.CoordinatorActorID,
			"registration_evidence_id": string(request.RegistrationEvidenceID),
			"admission_evidence_id":    string(task.TaskMeetingInvocation.AdmissionEvidenceID),
			"venue_basis":              "owner_configured_venue_declaration",
			"config_revision":          request.ConfigRevision,
			"config_sha256":            request.ConfigSHA256,
			"input_sha256":             request.InputSHA256,
			"request_id":               request.RequestID,
			"audience":                 request.Audience,
			"audience_sha256":          audienceHash,
		},
	}
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	receipt := TaskMeetingGroupSessionReceipt{
		OccurrenceKey: request.OccurrenceKey, OwnerUserID: request.OwnerUserID,
		CoordinatorActorID: request.CoordinatorActorID, RegistrationEvidence: request.RegistrationEvidenceID,
		AdmissionEvidence: task.TaskMeetingInvocation.AdmissionEvidenceID,
		ConfigRevision:    request.ConfigRevision, ConfigSHA256: request.ConfigSHA256, InputSHA256: request.InputSHA256, RequestID: request.RequestID,
		Audience: append([]string(nil), request.Audience...), AudienceSHA256: audienceHash,
		TaskID: task.TaskID, RunID: run.RunID, SessionID: sessionID, ThreadID: threadID,
		OpenedEventID: openedEventID, EvidenceID: evidenceID, OpenedAt: openedAt,
	}
	if err := insertOperationReceipt(ctx, tx, operationID, taskMeetingOpenOperation, inputHash, receipt); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupSessionReceipt{}, err
	}
	return receipt, nil
}

func (s *SQLiteStore) EnsureTaskMeetingGroupMessage(
	ctx context.Context,
	request TaskMeetingGroupMessageRequest,
	reader domaintask.TaskMeetingInvocationReader,
	outputReader domaintask.TaskMeetingGenerationOutputStore,
	authorizeRead domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeSend TaskMeetingGroupMessageAuthorizationCheck,
) (TaskMeetingGroupMessageReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if ctx == nil || reader == nil || outputReader == nil || authorizeRead == nil {
		return TaskMeetingGroupMessageReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := validateTaskMeetingGroupMessageRequest(request); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	task, run, shared, err := reader.GetTaskMeetingInvocationState(ctx, request.TaskID, request.RunID, authorizeRead)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if err := validateTaskMeetingMessageSender(request, task, run); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	output, outputReference, err := outputReader.GetTaskMeetingGenerationOutput(ctx, task.TaskID, run.RunID, authorizeRead)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if err := validateTaskMeetingGenerationOutput(request, task, run, outputReference, output); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if err := validateTaskMeetingMessageParentContext(ctx, request, task, reader, authorizeRead); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	inputHash, err := hashJSON(request)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if prior, found, err := s.findTaskMeetingGroupMessage(ctx, request, task, run, inputHash); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	} else if found {
		return prior, nil
	}
	if authorizeSend == nil {
		return TaskMeetingGroupMessageReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := ctx.Err(); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if authorizeSend(ctx, task, run, shared, outputReference, output, request.Body) != nil {
		return TaskMeetingGroupMessageReceipt{}, ErrTaskMeetingForbidden
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(request)
		if err != nil {
			return TaskMeetingGroupMessageReceipt{}, err
		}
		var receipt TaskMeetingGroupMessageReceipt
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			// Queue latency can cross the occurrence deadline or a partial
			// transition. Reauthorize the exact send at execution time, before
			// taking the EventStore transaction/sequence lock. Historical receipt
			// reads above remain available independently of this new-effect gate.
			freshTask, freshRun, freshShared, freshOutput, freshOutputRef, innerErr := s.authorizeTaskMeetingGroupMessageSend(
				commandCtx, request, reader, outputReader, authorizeRead, authorizeSend)
			if innerErr != nil {
				return innerErr
			}
			var inner error
			_ = freshShared
			_ = freshOutput
			_ = freshOutputRef
			receipt, inner = s.ensureTaskMeetingGroupMessageInline(commandCtx, request, inputHash, freshTask, freshRun)
			return inner
		})
		return receipt, err
	}
	return s.ensureTaskMeetingGroupMessageInline(ctx, request, inputHash, task, run)
}

func (s *SQLiteStore) authorizeTaskMeetingGroupMessageSend(
	ctx context.Context,
	request TaskMeetingGroupMessageRequest,
	reader domaintask.TaskMeetingInvocationReader,
	outputReader domaintask.TaskMeetingGenerationOutputStore,
	authorizeRead domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeSend TaskMeetingGroupMessageAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, json.RawMessage, domaintask.TaskMeetingGenerationResultReference, error) {
	if ctx == nil || reader == nil || outputReader == nil || authorizeRead == nil || authorizeSend == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, ErrTaskMeetingUnavailable
	}
	task, run, shared, err := reader.GetTaskMeetingInvocationState(ctx, request.TaskID, request.RunID, authorizeRead)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if err := validateTaskMeetingMessageSender(request, task, run); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	output, outputRef, err := outputReader.GetTaskMeetingGenerationOutput(ctx, task.TaskID, run.RunID, authorizeRead)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if err := validateTaskMeetingGenerationOutput(request, task, run, outputRef, output); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if err := validateTaskMeetingMessageParentContext(ctx, request, task, reader, authorizeRead); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	rootRequest := TaskMeetingGroupSessionRequest{
		OccurrenceKey: request.Session.OccurrenceKey, OwnerUserID: request.Session.OwnerUserID,
		CoordinatorActorID: request.Session.CoordinatorActorID, RegistrationEvidenceID: request.Session.RegistrationEvidence,
		ConfigRevision: request.Session.ConfigRevision, ConfigSHA256: request.Session.ConfigSHA256,
		InputSHA256: request.Session.InputSHA256, RequestID: request.Session.RequestID,
		Audience: append([]string(nil), request.Session.Audience...), TaskID: request.Session.TaskID, RunID: request.Session.RunID,
	}
	rootTask, rootRun, _, err := reader.GetTaskMeetingInvocationState(ctx, rootRequest.TaskID, rootRequest.RunID, authorizeRead)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if err := validateTaskMeetingRootBinding(rootRequest, rootTask, rootRun, true); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if err := authorizeSend(ctx, task, run, shared, outputRef, output, request.Body); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, nil, domaintask.TaskMeetingGenerationResultReference{}, ErrTaskMeetingForbidden
	}
	return task, run, shared, output, outputRef, nil
}

// FindTaskMeetingGroupMessage reads only an exact existing message receipt.
// It requires the frozen request, current TaskManager state/output readers, and
// current message-scope authorization; it never creates a Session, Event, or
// delivery work item.
func (s *SQLiteStore) FindTaskMeetingGroupMessage(
	ctx context.Context,
	request TaskMeetingGroupMessageRequest,
	reader domaintask.TaskMeetingInvocationReader,
	outputReader domaintask.TaskMeetingGenerationOutputStore,
	authorizeRead domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeMessageRead TaskMeetingGroupMessageReadAuthorizationCheck,
) (TaskMeetingGroupMessageReceipt, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	if ctx == nil || reader == nil || outputReader == nil || authorizeRead == nil || authorizeMessageRead == nil {
		return TaskMeetingGroupMessageReceipt{}, false, ErrTaskMeetingUnavailable
	}
	if err := validateTaskMeetingGroupMessageRequest(request); err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	task, run, shared, err := reader.GetTaskMeetingInvocationState(ctx, request.TaskID, request.RunID, authorizeRead)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	if err := validateTaskMeetingMessageSender(request, task, run); err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	output, outputReference, err := outputReader.GetTaskMeetingGenerationOutput(ctx, task.TaskID, run.RunID, authorizeRead)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	if err := validateTaskMeetingGenerationOutput(request, task, run, outputReference, output); err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	if err := validateTaskMeetingMessageParentContext(ctx, request, task, reader, authorizeRead); err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	if authorizeMessageRead(ctx, task, run, shared, outputReference, output, request.Body, nil) != nil {
		return TaskMeetingGroupMessageReceipt{}, false, ErrTaskMeetingForbidden
	}
	inputHash, err := hashJSON(request)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	receipt, found, err := s.findTaskMeetingGroupMessage(ctx, request, task, run, inputHash)
	if err != nil || !found {
		return TaskMeetingGroupMessageReceipt{}, found, err
	}
	if authorizeMessageRead(ctx, task, run, shared, outputReference, output, request.Body, &receipt.Reference) != nil {
		return TaskMeetingGroupMessageReceipt{}, false, ErrTaskMeetingForbidden
	}
	return receipt, true, nil
}

func (s *SQLiteStore) ensureTaskMeetingGroupMessageInline(ctx context.Context, request TaskMeetingGroupMessageRequest, inputHash string, task domaintask.Task, run domaintask.Run) (TaskMeetingGroupMessageReceipt, error) {
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	defer unlock()
	defer tx.Rollback()
	opID := taskMeetingMessageOperationID(request.Session.OccurrenceKey, request.MessageID)
	prior, found, err := readOperationReceipt(ctx, tx, opID)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if found {
		if prior.operationKind != taskMeetingMessageOperation || prior.inputSHA256 != inputHash {
			return TaskMeetingGroupMessageReceipt{}, ErrTaskMeetingConflict
		}
		var receipt TaskMeetingGroupMessageReceipt
		if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || validateTaskMeetingGroupMessageReceiptTx(ctx, tx, receipt, request, task, run) != nil {
			return TaskMeetingGroupMessageReceipt{}, ErrTaskMeetingReceiptBad
		}
		if err := tx.Commit(); err != nil {
			return TaskMeetingGroupMessageReceipt{}, err
		}
		return receipt, nil
	}
	if err := validateStoredTaskMeetingSessionTx(ctx, tx, request.Session); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if err := validateTaskMeetingMessageSender(request, task, run); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	sequence, err := s.nextSequence(ctx)
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	createdAt := s.taskMeetingNowUTC()
	bodyHash := taskMeetingBodySHA256(request.Body)
	evidenceID := modulecore.NewEvidenceID()
	messageEventID := modulecore.NewEventID()
	traceID := modulecore.NewTraceID()
	ref := domaintask.TaskMeetingMessageReference{
		OccurrenceKey: request.Session.OccurrenceKey, SessionID: request.Session.SessionID,
		ThreadID: request.Session.ThreadID, MessageID: request.MessageID, EventID: messageEventID,
		BodySHA256: bodyHash, AudienceSHA256: request.Session.AudienceSHA256,
		ConfigRevision: request.Session.ConfigRevision, ConfigSHA256: request.Session.ConfigSHA256,
		AttemptGeneration: request.AttemptGeneration, AttemptEpoch: request.AttemptEpoch,
		ScopeEvidenceID: request.ScopeEvidenceID,
	}
	if err := ref.Validate(); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	event := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: messageEventID, EventSeq: sequence,
		TraceID: traceID, EventType: taskMeetingMessageEvent, ComponentID: taskMeetingComponent,
		OccurredAt: createdAt, SessionID: request.Session.SessionID, ThreadID: request.Session.ThreadID,
		TaskID: task.TaskID, RunID: run.RunID, MessageID: request.MessageID,
		ActorKind: "agent", ActorID: request.ActorID, EvidenceID: evidenceID,
		Payload: map[string]any{
			"occurrence_key": ref.OccurrenceKey, "body": request.Body, "body_sha256": bodyHash,
			"generation_result": request.GenerationResult,
			"audience":          request.Session.Audience, "audience_sha256": ref.AudienceSHA256,
			"config_revision": ref.ConfigRevision, "config_sha256": ref.ConfigSHA256,
			"attempt_generation": ref.AttemptGeneration, "attempt_epoch": ref.AttemptEpoch,
			"scope_evidence_id": string(ref.ScopeEvidenceID), "sender_actor_id": request.ActorID,
		},
	}
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	workIDs := make([]string, 0, len(request.Session.Audience))
	for _, actor := range request.Session.Audience {
		workID := taskMeetingDeliveryWorkID(request.Session.OccurrenceKey, request.MessageID, actor)
		dedupe := taskMeetingDeliveryDedupeKey(request.Session.OccurrenceKey, request.MessageID, actor)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO live_work_outbox (work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
			VALUES (?,?,?, ?,?,5,'pending',0,'',1)`, workID, dedupe, taskMeetingDeliveryWorkKind(actor), string(messageEventID), string(request.Session.SessionID)); err != nil {
			return TaskMeetingGroupMessageReceipt{}, err
		}
		workIDs = append(workIDs, workID)
	}
	receipt := TaskMeetingGroupMessageReceipt{
		Reference: ref, GenerationResult: request.GenerationResult, SenderTaskID: task.TaskID, SenderRunID: run.RunID,
		Audience: append([]string(nil), request.Session.Audience...), WorkIDs: workIDs,
		EvidenceID: evidenceID, CreatedAt: createdAt,
	}
	if err := insertOperationReceipt(ctx, tx, opID, taskMeetingMessageOperation, inputHash, receipt); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupMessageReceipt{}, err
	}
	return receipt, nil
}

func validateTaskMeetingGroupMessageRequest(request TaskMeetingGroupMessageRequest) error {
	if err := validateTaskMeetingGroupSessionReceiptShape(request.Session); err != nil {
		return err
	}
	if request.TaskID.Validate() != nil || request.RunID.Validate() != nil || request.MessageID.Validate() != nil || request.ScopeEvidenceID.Validate() != nil || request.GenerationResult.Validate() != nil ||
		request.AttemptGeneration == 0 || request.AttemptEpoch == 0 || strings.TrimSpace(request.ActorID) == "" || request.ActorID != strings.TrimSpace(request.ActorID) ||
		request.Body == "" || len(request.Body) > maxTaskMeetingMessageBytes || !utf8.ValidString(request.Body) {
		return errors.New("TaskMeeting group message request is incomplete or oversized")
	}
	if !containsString(request.Session.Audience, request.ActorID) {
		return ErrTaskMeetingForbidden
	}
	return nil
}

func validateTaskMeetingMessageSender(request TaskMeetingGroupMessageRequest, task domaintask.Task, run domaintask.Run) error {
	invocation := task.TaskMeetingInvocation
	if task.TaskID != request.TaskID || run.TaskID != task.TaskID || run.RunID != request.RunID || run.Status != domaintask.RunStatusSucceeded || task.Status != domaintask.StatusSucceeded ||
		invocation == nil || (invocation.Purpose != domaintask.TaskMeetingPurposeReport && invocation.Purpose != domaintask.TaskMeetingPurposeAnswer) ||
		task.Assignee != request.ActorID || task.TaskMeetingInvocation.ActorID != request.ActorID ||
		invocation.OccurrenceKey != request.Session.OccurrenceKey || invocation.ConfiguredOwnerUserID != request.Session.OwnerUserID ||
		invocation.ConfigRevision != request.Session.ConfigRevision || invocation.ConfigSHA256 != request.Session.ConfigSHA256 ||
		invocation.AudienceSHA256 != request.Session.AudienceSHA256 || invocation.RegistrationEvidenceID != request.Session.RegistrationEvidence ||
		invocation.AdmissionEvidenceID.Validate() != nil || !equalStrings(invocation.Audience, request.Session.Audience) ||
		invocation.ParentTaskID.Validate() != nil || invocation.ParentRunID.Validate() != nil ||
		invocation.GenerationScope.ScopeEvidenceID != request.ScopeEvidenceID || invocation.GenerationScope.Generation != request.AttemptGeneration || invocation.GenerationScope.Epoch != request.AttemptEpoch ||
		task.TaskMeetingResult == nil || *task.TaskMeetingResult != request.GenerationResult || request.GenerationResult.ValidateTask(task) != nil || request.GenerationResult.ValidateRun(task, run) != nil {
		return ErrTaskMeetingForbidden
	}
	return nil
}

func validateTaskMeetingGenerationOutput(request TaskMeetingGroupMessageRequest, task domaintask.Task, run domaintask.Run, reference domaintask.TaskMeetingGenerationResultReference, output json.RawMessage) error {
	var canonical bytes.Buffer
	if json.Compact(&canonical, output) != nil || !bytes.Equal(canonical.Bytes(), output) || reference != request.GenerationResult || reference.ValidateTask(task) != nil || reference.ValidateRun(task, run) != nil ||
		reference.ResultSHA256 != taskMeetingBodySHA256(string(output)) || reference.OccurrenceKey != request.Session.OccurrenceKey || reference.ConfigRevision != request.Session.ConfigRevision || reference.ConfigSHA256 != request.Session.ConfigSHA256 ||
		reference.GenerationScope.ScopeEvidenceID != request.ScopeEvidenceID || reference.GenerationScope.Generation != request.AttemptGeneration || reference.GenerationScope.Epoch != request.AttemptEpoch {
		return ErrTaskMeetingConflict
	}
	return nil
}

func validateTaskMeetingMessageParentContext(
	ctx context.Context,
	request TaskMeetingGroupMessageRequest,
	task domaintask.Task,
	reader domaintask.TaskMeetingInvocationReader,
	authorize domaintask.TaskMeetingReadAuthorizationCheck,
) error {
	invocation := task.TaskMeetingInvocation
	if invocation == nil {
		return ErrTaskMeetingForbidden
	}
	parent, parentRun, shared, err := reader.GetTaskMeetingInvocationState(ctx, invocation.ParentTaskID, invocation.ParentRunID, authorize)
	if err != nil {
		return err
	}
	parentInvocation := parent.TaskMeetingInvocation
	if parentInvocation == nil || parentInvocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext ||
		parent.TaskID != invocation.ParentTaskID || parentRun.RunID != invocation.ParentRunID || parentRun.TaskID != parent.TaskID ||
		parentRun.Status != domaintask.RunStatusRunning || parent.Status != domaintask.StatusRunning ||
		parentInvocation.ActorID != invocation.ActorID || parent.Assignee != invocation.ActorID ||
		parentInvocation.OccurrenceKey != request.Session.OccurrenceKey || parentInvocation.ConfiguredOwnerUserID != request.Session.OwnerUserID ||
		parentInvocation.ConfigRevision != request.Session.ConfigRevision || parentInvocation.ConfigSHA256 != request.Session.ConfigSHA256 ||
		parentInvocation.RegistrationEvidenceID != request.Session.RegistrationEvidence || parentInvocation.AudienceSHA256 != request.Session.AudienceSHA256 ||
		!equalStrings(parentInvocation.Audience, request.Session.Audience) || parentInvocation.ParentTaskID != request.Session.TaskID || parentInvocation.ParentRunID != request.Session.RunID {
		return ErrTaskMeetingForbidden
	}
	if err := domaintask.ValidateTaskMeetingConsumedMessages(parent, parentRun, shared); err != nil {
		return ErrTaskMeetingForbidden
	}
	frozen := invocation.FrozenSourceMessages
	if len(frozen) > len(shared.TaskMeetingConsumedMessages) {
		return ErrTaskMeetingForbidden
	}
	for index, proof := range frozen {
		if !sameTaskMeetingConsumedMessage(proof, shared.TaskMeetingConsumedMessages[index]) {
			return ErrTaskMeetingForbidden
		}
	}
	digest, err := domaintask.TaskMeetingConsumedMessagesSHA256(frozen)
	if err != nil || digest != invocation.SourceContextSHA256 || request.AttemptGeneration == 0 || request.AttemptEpoch == 0 {
		return ErrTaskMeetingForbidden
	}
	return nil
}

func sameTaskMeetingConsumedMessage(left, right domaintask.TaskMeetingConsumedMessage) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func validateTaskMeetingGroupSessionReceiptShape(receipt TaskMeetingGroupSessionReceipt) error {
	if receipt.OccurrenceKey == "" || receipt.SessionID.Validate() != nil || receipt.ThreadID.Validate() != nil ||
		receipt.OpenedEventID.Validate() != nil || receipt.EvidenceID.Validate() != nil || !validTaskMeetingDigest(receipt.ConfigSHA256) ||
		!validTaskMeetingDigest(receipt.InputSHA256) || !validTaskMeetingDigest(receipt.AudienceSHA256) {
		return ErrTaskMeetingReceiptBad
	}
	_, digest, err := canonicalTaskMeetingAudience(receipt.Audience)
	if err != nil || digest != receipt.AudienceSHA256 {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func validateStoredTaskMeetingSessionTx(ctx context.Context, tx *sql.Tx, receipt TaskMeetingGroupSessionReceipt) error {
	if err := validateTaskMeetingGroupSessionReceiptShape(receipt); err != nil {
		return err
	}
	stored, found, err := readOperationReceipt(ctx, tx, taskMeetingSessionOperationID(receipt.OccurrenceKey))
	if err != nil || !found || stored.operationKind != taskMeetingOpenOperation {
		return ErrTaskMeetingReceiptBad
	}
	var actual TaskMeetingGroupSessionReceipt
	if json.Unmarshal([]byte(stored.resultJSON), &actual) != nil {
		return ErrTaskMeetingReceiptBad
	}
	wantJSON, _ := json.Marshal(receipt)
	gotJSON, _ := json.Marshal(actual)
	if string(wantJSON) != string(gotJSON) {
		return ErrTaskMeetingConflict
	}
	if err := validateTaskMeetingOpenEvent(ctx, tx, actual); err != nil {
		return err
	}
	var ownerID, runtimeVersion string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id,runtime_version FROM live_conversation_session WHERE session_id=?`, string(receipt.SessionID)).Scan(&ownerID, &runtimeVersion); err != nil || ownerID != receipt.OwnerUserID || runtimeVersion != taskMeetingRuntimeVersion {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func validateTaskMeetingGroupMessageReceiptTx(ctx context.Context, tx *sql.Tx, receipt TaskMeetingGroupMessageReceipt, request TaskMeetingGroupMessageRequest, task domaintask.Task, run domaintask.Run) error {
	if receipt.Reference.Validate() != nil || receipt.Reference.OccurrenceKey != request.Session.OccurrenceKey ||
		receipt.Reference.SessionID != request.Session.SessionID || receipt.Reference.ThreadID != request.Session.ThreadID ||
		receipt.Reference.MessageID != request.MessageID || receipt.Reference.BodySHA256 != taskMeetingBodySHA256(request.Body) || receipt.GenerationResult != request.GenerationResult ||
		receipt.SenderTaskID != task.TaskID || receipt.SenderRunID != run.RunID || receipt.EvidenceID.Validate() != nil ||
		!equalStrings(receipt.Audience, request.Session.Audience) || len(receipt.WorkIDs) != len(receipt.Audience) {
		return ErrTaskMeetingReceiptBad
	}
	if err := validateStoredTaskMeetingSessionTx(ctx, tx, request.Session); err != nil {
		return err
	}
	var eventID, traceID, schema, eventType, component, occurred, envelope string
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE event_id=?`, string(receipt.Reference.EventID)).Scan(&eventID, &sequence, &traceID, &schema, &eventType, &component, &occurred, &envelope); err != nil {
		return ErrTaskMeetingReceiptBad
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, component, occurred, envelope)
	if err != nil || event.ComponentID != taskMeetingComponent || event.EventType != taskMeetingMessageEvent ||
		event.SessionID != receipt.Reference.SessionID || event.ThreadID != receipt.Reference.ThreadID ||
		event.MessageID != receipt.Reference.MessageID || event.TaskID != task.TaskID || event.RunID != run.RunID || event.EvidenceID != receipt.EvidenceID {
		return ErrTaskMeetingReceiptBad
	}
	body, ok := event.Payload["body"].(string)
	if !ok || body != request.Body || taskMeetingBodySHA256(body) != receipt.Reference.BodySHA256 ||
		!sameTaskMeetingGenerationResultPayload(event.Payload["generation_result"], request.GenerationResult) ||
		payloadString(event.Payload, "audience_sha256") != receipt.Reference.AudienceSHA256 ||
		payloadString(event.Payload, "config_sha256") != receipt.Reference.ConfigSHA256 {
		return ErrTaskMeetingReceiptBad
	}
	for i, actor := range receipt.Audience {
		work, _, err := readWorkForUpdate(ctx, tx, receipt.WorkIDs[i])
		if err != nil || work.Kind != taskMeetingDeliveryWorkKind(actor) || work.SourceEventID != receipt.Reference.EventID || work.SessionID != receipt.Reference.SessionID ||
			work.DedupeKey != taskMeetingDeliveryDedupeKey(request.Session.OccurrenceKey, request.MessageID, actor) {
			return ErrTaskMeetingReceiptBad
		}
	}
	return nil
}

func sameTaskMeetingGenerationResultPayload(value any, expected domaintask.TaskMeetingGenerationResultReference) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var actual domaintask.TaskMeetingGenerationResultReference
	return json.Unmarshal(encoded, &actual) == nil && actual == expected
}

func (s *SQLiteStore) findTaskMeetingGroupMessage(ctx context.Context, request TaskMeetingGroupMessageRequest, task domaintask.Task, run domaintask.Run, inputHash string) (TaskMeetingGroupMessageReceipt, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	defer tx.Rollback()
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingMessageOperationID(request.Session.OccurrenceKey, request.MessageID))
	if err != nil || !found {
		return TaskMeetingGroupMessageReceipt{}, found, err
	}
	if prior.operationKind != taskMeetingMessageOperation || prior.inputSHA256 != inputHash {
		return TaskMeetingGroupMessageReceipt{}, false, ErrTaskMeetingConflict
	}
	var receipt TaskMeetingGroupMessageReceipt
	if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || validateTaskMeetingGroupMessageReceiptTx(ctx, tx, receipt, request, task, run) != nil {
		return TaskMeetingGroupMessageReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, err
	}
	return receipt, true, nil
}

func taskMeetingMessageOperationID(key string, messageID modulecore.MessageID) string {
	return "taskmeeting:message:" + key + ":" + string(messageID)
}
func taskMeetingDeliveryWorkKind(actor string) string { return "taskmeeting.group_delivery:" + actor }
func taskMeetingDeliveryWorkID(key string, messageID modulecore.MessageID, actor string) string {
	return "tm-delivery:" + key + ":" + string(messageID) + ":" + actor
}
func taskMeetingDeliveryDedupeKey(key string, messageID modulecore.MessageID, actor string) string {
	return "taskmeeting:delivery:" + key + ":" + string(messageID) + ":" + actor
}
func taskMeetingBodySHA256(body string) string {
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateTaskMeetingGroupSessionRequest(request TaskMeetingGroupSessionRequest) error {
	if request.OccurrenceKey == "" || strings.TrimSpace(request.OccurrenceKey) != request.OccurrenceKey || len(request.OccurrenceKey) > 128 {
		return errors.New("TaskMeeting occurrence key is invalid")
	}
	if request.TaskID.Validate() != nil || request.RunID.Validate() != nil || request.RegistrationEvidenceID.Validate() != nil {
		return errors.New("TaskMeeting root identity or registration evidence is invalid")
	}
	if strings.TrimSpace(request.OwnerUserID) == "" || request.OwnerUserID != strings.TrimSpace(request.OwnerUserID) ||
		strings.TrimSpace(request.CoordinatorActorID) == "" || request.CoordinatorActorID != strings.TrimSpace(request.CoordinatorActorID) ||
		request.ConfigRevision == 0 || request.RequestID == "" {
		return errors.New("TaskMeeting root delegation input is incomplete")
	}
	if !validTaskMeetingDigest(request.ConfigSHA256) || !validTaskMeetingDigest(request.InputSHA256) {
		return errors.New("TaskMeeting configuration digest is invalid")
	}
	_, _, err := canonicalTaskMeetingAudience(request.Audience)
	return err
}

func validateTaskMeetingRootBinding(request TaskMeetingGroupSessionRequest, task domaintask.Task, run domaintask.Run, requireRunning bool) error {
	if task.TaskID != request.TaskID || run.TaskID != task.TaskID || run.RunID != request.RunID ||
		(requireRunning && run.Status != domaintask.RunStatusRunning) ||
		task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeRoot ||
		task.Assignee != request.CoordinatorActorID || task.TaskMeetingInvocation.ActorID != request.CoordinatorActorID ||
		task.TaskMeetingInvocation.OccurrenceKey != request.OccurrenceKey ||
		task.TaskMeetingInvocation.ConfiguredOwnerUserID != request.OwnerUserID ||
		task.TaskMeetingInvocation.ConfigRevision != request.ConfigRevision ||
		task.TaskMeetingInvocation.ConfigSHA256 != request.ConfigSHA256 ||
		task.TaskMeetingInvocation.InputSHA256 != request.InputSHA256 {
		return ErrTaskMeetingForbidden
	}
	if task.TaskMeetingInvocation.RegistrationEvidenceID != request.RegistrationEvidenceID || task.TaskMeetingInvocation.AdmissionEvidenceID.Validate() != nil {
		return ErrTaskMeetingForbidden
	}
	_, audienceHash, err := canonicalTaskMeetingAudience(request.Audience)
	if err != nil || task.TaskMeetingInvocation.AudienceSHA256 != audienceHash || !equalStrings(task.TaskMeetingInvocation.Audience, request.Audience) {
		return ErrTaskMeetingForbidden
	}
	return nil
}

func taskMeetingRootRunCanCreate(status domaintask.RunStatus) bool {
	return status == domaintask.RunStatusRunning
}

func validateTaskMeetingGroupSessionReceipt(receipt TaskMeetingGroupSessionReceipt, request TaskMeetingGroupSessionRequest, task domaintask.Task, run domaintask.Run) error {
	if receipt.OccurrenceKey != request.OccurrenceKey || receipt.OwnerUserID != request.OwnerUserID ||
		receipt.CoordinatorActorID != request.CoordinatorActorID || receipt.RegistrationEvidence != request.RegistrationEvidenceID ||
		receipt.AdmissionEvidence != task.TaskMeetingInvocation.AdmissionEvidenceID ||
		receipt.ConfigRevision != request.ConfigRevision || receipt.ConfigSHA256 != request.ConfigSHA256 || receipt.InputSHA256 != request.InputSHA256 || receipt.RequestID != request.RequestID ||
		receipt.TaskID != task.TaskID || receipt.RunID != run.RunID || receipt.SessionID.Validate() != nil ||
		receipt.ThreadID.Validate() != nil || receipt.OpenedEventID.Validate() != nil || receipt.EvidenceID.Validate() != nil || receipt.OpenedAt.IsZero() {
		return ErrTaskMeetingReceiptBad
	}
	_, audienceHash, err := canonicalTaskMeetingAudience(request.Audience)
	if err != nil || receipt.AudienceSHA256 != audienceHash || !equalStrings(receipt.Audience, request.Audience) {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func validateTaskMeetingOpenEvent(ctx context.Context, tx *sql.Tx, receipt TaskMeetingGroupSessionReceipt) error {
	var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelope string
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE event_id=?`, string(receipt.OpenedEventID)).Scan(
		&eventID, &sequence, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &envelope)
	if err != nil {
		return ErrTaskMeetingReceiptBad
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schemaVersion, eventType, componentID, occurredAt, envelope)
	if err != nil || event.ComponentID != taskMeetingComponent || event.EventType != taskMeetingOpenEvent || event.SessionID != receipt.SessionID ||
		event.ThreadID != receipt.ThreadID || event.TaskID != receipt.TaskID || event.RunID != receipt.RunID || event.EvidenceID != receipt.EvidenceID ||
		payloadString(event.Payload, "occurrence_key") != receipt.OccurrenceKey || payloadString(event.Payload, "configured_owner_user_id") != receipt.OwnerUserID ||
		payloadString(event.Payload, "coordinator_actor_id") != receipt.CoordinatorActorID || payloadString(event.Payload, "registration_evidence_id") != string(receipt.RegistrationEvidence) ||
		payloadString(event.Payload, "admission_evidence_id") != string(receipt.AdmissionEvidence) || payloadString(event.Payload, "config_sha256") != receipt.ConfigSHA256 ||
		payloadString(event.Payload, "input_sha256") != receipt.InputSHA256 || payloadString(event.Payload, "audience_sha256") != receipt.AudienceSHA256 ||
		eventPayloadUint(event.Payload["config_revision"]) != receipt.ConfigRevision || !payloadAudienceEqual(event.Payload["audience"], receipt.Audience) {
		return ErrTaskMeetingReceiptBad
	}
	if err := validateTaskMeetingRegistrationBindingTx(ctx, tx, receipt.OccurrenceKey, receipt.OwnerUserID, receipt.CoordinatorActorID, receipt.Audience, receipt.ConfigRevision, receipt.RegistrationEvidence, receipt.ConfigSHA256); err != nil {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func taskMeetingSessionOperationID(key string) string { return "taskmeeting:session:" + key }

func validateTaskMeetingRegistrationBindingTx(ctx context.Context, tx *sql.Tx, occurrenceKey, ownerUserID, coordinatorActorID string, audience []string, revision uint64, evidenceID modulecore.EvidenceID, configSHA256 string) error {
	parts := strings.Split(occurrenceKey, "|")
	if len(parts) != 4 || evidenceID.Validate() != nil || revision == 0 || !validTaskMeetingDigest(configSHA256) {
		return ErrTaskMeetingReceiptBad
	}
	key := domainmeeting.OccurrenceKey{ScheduleID: modulecore.ScheduleID(parts[0]), LocalDate: parts[1], Slot: parts[2], Timezone: parts[3]}
	if key.Validate() != nil {
		return ErrTaskMeetingReceiptBad
	}
	registration, found, err := readTaskMeetingRegistrationReceiptTx(ctx, tx, ownerUserID, domainmeeting.TaskMeetingRegistrationKey, revision)
	if err != nil || !found || registration.ScheduleID != key.ScheduleID || registration.RegistrationEvidenceID != evidenceID || registration.ConfigSHA256 != configSHA256 {
		return ErrTaskMeetingReceiptBad
	}
	declaration, declarationHash, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(registration.FrozenDeclaration)
	if err != nil || declarationHash != configSHA256 || declaration.OwnerUserID != ownerUserID || declaration.CoordinatorActorID != coordinatorActorID || !equalStrings(declaration.Audience, audience) {
		return ErrTaskMeetingReceiptBad
	}
	pin, pinFound, err := readTaskMeetingOccurrenceRegistrationReceiptTx(ctx, tx, key)
	if err != nil || !pinFound || pin.OwnerUserID != ownerUserID || pin.ConfigRevision != revision || pin.ConfigSHA256 != configSHA256 || pin.RegistrationEvidenceID != evidenceID || pin.ScheduleID != key.ScheduleID {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func validateTaskMeetingWorldRegistrationTx(ctx context.Context, tx *sql.Tx, request domainmeeting.WorldObservationRequest) error {
	if err := validateTaskMeetingRegistrationBindingTx(ctx, tx, request.Key.String(), request.OwnerUserID, request.CoordinatorActorID, request.Audience, request.ConfigRevision, request.RegistrationEvidenceID, request.ConfigSHA256); err != nil {
		return err
	}
	receipt, found, err := readTaskMeetingRegistrationReceiptTx(ctx, tx, request.OwnerUserID, domainmeeting.TaskMeetingRegistrationKey, request.ConfigRevision)
	if err != nil || !found {
		return ErrTaskMeetingReceiptBad
	}
	declaration, digest, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(receipt.FrozenDeclaration)
	if err != nil || digest != request.ConfigSHA256 || (request.Snapshot.WorldID != "" && declaration.WorldID != request.Snapshot.WorldID) ||
		(request.Snapshot.TimelineID != "" && declaration.TimelineID != request.Snapshot.TimelineID) ||
		(request.Snapshot.TimelineEpoch != "" && declaration.TimelineEpoch != request.Snapshot.TimelineEpoch) || declaration.Rooms != request.Rooms || !equalStrings(declaration.Audience, request.Audience) ||
		!sameWorldActorBindings(declaration.WorldActorBindings, request.ActorBindings) {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func sameWorldActorBindings(left, right []domainmeeting.WorldActorBinding) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func canonicalTaskMeetingAudience(input []string) ([]string, string, error) {
	if len(input) != domaintask.TaskMeetingAudienceSize {
		return nil, "", errors.New("TaskMeeting group requires exactly eight audience actors")
	}
	audience := append([]string(nil), input...)
	for _, actor := range audience {
		if actor == "" || strings.TrimSpace(actor) != actor || strings.ToLower(actor) != actor || strings.ContainsAny(actor, "\r\n\x00") {
			return nil, "", errors.New("TaskMeeting audience actor is invalid")
		}
	}
	digest, err := domaintask.TaskMeetingAudienceSHA256(audience)
	return audience, digest, err
}

func validTaskMeetingDigest(value string) bool {
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

func equalStrings(left, right []string) bool {
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
