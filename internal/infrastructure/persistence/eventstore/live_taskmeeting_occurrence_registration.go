package eventstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskMeetingOccurrenceRegistrationOperation = "taskmeeting.pin_occurrence_registration"
	taskMeetingOccurrenceRegistrationEvent     = "taskmeeting.occurrence_registration_pinned"
)

// TaskMeetingOccurrenceRegistrationReceipt freezes which immutable schedule
// registration revision owns one occurrence. It stores references and hashes,
// not another editable copy of the declaration.
type TaskMeetingOccurrenceRegistrationReceipt struct {
	OccurrenceKey          string                `json:"occurrence_key"`
	ScheduleID             modulecore.ScheduleID `json:"schedule_id"`
	OwnerUserID            string                `json:"owner_user_id"`
	ConfigRevision         uint64                `json:"config_revision"`
	ConfigSHA256           string                `json:"config_sha256"`
	RegistrationEvidenceID modulecore.EvidenceID `json:"registration_evidence_id"`
	RegistrationEventID    modulecore.EventID    `json:"registration_event_id"`
	PinEventID             modulecore.EventID    `json:"pin_event_id"`
	PinnedAt               time.Time             `json:"pinned_at"`
}

type TaskMeetingOccurrenceRegistrationAuthorizationCheck func(context.Context, TaskMeetingScheduleRegistrationReceipt, domainmeeting.OccurrenceKey) error

type TaskMeetingOccurrenceRegistrationReadAuthorizationCheck func(context.Context, string, string) error

// PinTaskMeetingOccurrenceRegistration records the exact current owner
// registration revision for an occurrence. The operation key is the
// ScheduleID/date/slot/timezone tuple, so later config edits cannot create a
// second occurrence or rewrite its source registration.
func (s *SQLiteStore) PinTaskMeetingOccurrenceRegistration(
	ctx context.Context,
	registration TaskMeetingScheduleRegistrationReceipt,
	occurrence domainmeeting.OccurrenceKey,
	authorizeCurrent TaskMeetingOccurrenceRegistrationAuthorizationCheck,
) (TaskMeetingOccurrenceRegistrationReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	if ctx == nil || authorizeCurrent == nil || occurrence.Validate() != nil || registration.ScheduleID != occurrence.ScheduleID {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := authorizeCurrent(ctx, registration, occurrence); err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingForbidden
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(struct {
			Registration TaskMeetingScheduleRegistrationReceipt `json:"registration"`
			Occurrence   domainmeeting.OccurrenceKey            `json:"occurrence"`
		}{registration, occurrence})
		if err != nil {
			return TaskMeetingOccurrenceRegistrationReceipt{}, err
		}
		var receipt TaskMeetingOccurrenceRegistrationReceipt
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var inner error
			receipt, inner = s.pinTaskMeetingOccurrenceRegistrationInline(commandCtx, registration, occurrence)
			return inner
		})
		return receipt, err
	}
	return s.pinTaskMeetingOccurrenceRegistrationInline(ctx, registration, occurrence)
}

// FindTaskMeetingOccurrenceRegistration returns the exact historical pin for
// one known occurrence. It does not require the current schedule revision or
// reactivate a disabled schedule.
func (s *SQLiteStore) FindTaskMeetingOccurrenceRegistration(
	ctx context.Context,
	occurrence domainmeeting.OccurrenceKey,
	ownerUserID string,
	authorizeRead TaskMeetingOccurrenceRegistrationReadAuthorizationCheck,
) (TaskMeetingOccurrenceRegistrationReceipt, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, false, err
	}
	if ctx == nil || authorizeRead == nil || occurrence.Validate() != nil || strings.TrimSpace(ownerUserID) != ownerUserID || ownerUserID == "" {
		return TaskMeetingOccurrenceRegistrationReceipt{}, false, ErrTaskMeetingUnavailable
	}
	if err := authorizeRead(ctx, ownerUserID, occurrence.String()); err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, false, ErrTaskMeetingForbidden
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, false, err
	}
	defer tx.Rollback()
	receipt, found, err := readTaskMeetingOccurrenceRegistrationReceiptTx(ctx, tx, occurrence)
	if err != nil || !found {
		return TaskMeetingOccurrenceRegistrationReceipt{}, found, err
	}
	if receipt.OwnerUserID != ownerUserID {
		return TaskMeetingOccurrenceRegistrationReceipt{}, false, ErrTaskMeetingForbidden
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, false, err
	}
	return receipt, true, nil
}

func (s *SQLiteStore) pinTaskMeetingOccurrenceRegistrationInline(ctx context.Context, registration TaskMeetingScheduleRegistrationReceipt, occurrence domainmeeting.OccurrenceKey) (TaskMeetingOccurrenceRegistrationReceipt, error) {
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	defer unlock()
	defer tx.Rollback()
	storedRegistration, found, err := readTaskMeetingRegistrationReceiptTx(ctx, tx, registration.OwnerUserID, registration.RegistrationKey, registration.Revision)
	if err != nil || !found {
		return TaskMeetingOccurrenceRegistrationReceipt{}, domaintask.ErrTaskMeetingNotFound
	}
	if !sameTaskMeetingScheduleRegistrationReceipt(storedRegistration, registration) || registration.ScheduleID != occurrence.ScheduleID || registration.OwnerUserID == "" {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingConflict
	}
	declaration, declarationHash, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(storedRegistration.FrozenDeclaration)
	if err != nil || declarationHash != registration.ConfigSHA256 || declaration.OwnerUserID != registration.OwnerUserID || declaration.Revision != registration.Revision || declaration.RegistrationKey != registration.RegistrationKey ||
		registration.RegistrationKey != domainmeeting.TaskMeetingRegistrationKey || registration.ScheduleID != occurrence.ScheduleID {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingConflict
	}
	if declaration.Timing.Recurrence == nil || !containsString(declaration.Timing.Recurrence.Times, occurrence.Slot) {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingConflict
	}
	inputHash, err := hashJSON(struct {
		OccurrenceKey          string                `json:"occurrence_key"`
		ScheduleID             modulecore.ScheduleID `json:"schedule_id"`
		OwnerUserID            string                `json:"owner_user_id"`
		ConfigRevision         uint64                `json:"config_revision"`
		ConfigSHA256           string                `json:"config_sha256"`
		RegistrationEvidenceID modulecore.EvidenceID `json:"registration_evidence_id"`
		RegistrationEventID    modulecore.EventID    `json:"registration_event_id"`
	}{occurrence.String(), registration.ScheduleID, registration.OwnerUserID, registration.Revision, registration.ConfigSHA256, registration.RegistrationEvidenceID, registration.EventID})
	if err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	operationID := taskMeetingOccurrenceRegistrationOperationID(occurrence)
	prior, found, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	if found {
		if prior.operationKind != taskMeetingOccurrenceRegistrationOperation || prior.inputSHA256 != inputHash {
			return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingConflict
		}
		receipt, err := decodeTaskMeetingOccurrenceRegistrationReceipt(prior)
		if err != nil || validateTaskMeetingOccurrenceRegistrationEventTx(ctx, tx, receipt) != nil {
			return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingReceiptBad
		}
		if err := tx.Commit(); err != nil {
			return TaskMeetingOccurrenceRegistrationReceipt{}, err
		}
		return receipt, nil
	}
	pinnedAt := time.Now().UTC()
	event := modulecore.NewRootEventEnvelope(taskMeetingComponent, taskMeetingOccurrenceRegistrationEvent, pinnedAt, map[string]any{
		"occurrence_key": occurrence.String(), "schedule_id": string(registration.ScheduleID),
		"owner_user_id": registration.OwnerUserID, "config_revision": registration.Revision,
		"config_sha256": registration.ConfigSHA256, "registration_evidence_id": string(registration.RegistrationEvidenceID),
		"registration_event_id": string(registration.EventID), "venue_basis": "owner_configured_venue_declaration",
	})
	sequence, err := s.nextSequence(ctx)
	if err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	event.EventSeq, event.ScheduleID = sequence, occurrence.ScheduleID
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	receipt := TaskMeetingOccurrenceRegistrationReceipt{
		OccurrenceKey: occurrence.String(), ScheduleID: registration.ScheduleID, OwnerUserID: registration.OwnerUserID,
		ConfigRevision: registration.Revision, ConfigSHA256: registration.ConfigSHA256,
		RegistrationEvidenceID: registration.RegistrationEvidenceID, RegistrationEventID: registration.EventID,
		PinEventID: event.EventID, PinnedAt: pinnedAt,
	}
	if err := insertOperationReceipt(ctx, tx, operationID, taskMeetingOccurrenceRegistrationOperation, inputHash, receipt); err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	return receipt, nil
}

func readTaskMeetingOccurrenceRegistrationReceiptTx(ctx context.Context, tx *sql.Tx, occurrence domainmeeting.OccurrenceKey) (TaskMeetingOccurrenceRegistrationReceipt, bool, error) {
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingOccurrenceRegistrationOperationID(occurrence))
	if err != nil || !found {
		return TaskMeetingOccurrenceRegistrationReceipt{}, found, err
	}
	if prior.operationKind != taskMeetingOccurrenceRegistrationOperation {
		return TaskMeetingOccurrenceRegistrationReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	receipt, err := decodeTaskMeetingOccurrenceRegistrationReceipt(prior)
	if err != nil || receipt.OccurrenceKey != occurrence.String() || receipt.ScheduleID != occurrence.ScheduleID || validateTaskMeetingOccurrenceRegistrationEventTx(ctx, tx, receipt) != nil {
		return TaskMeetingOccurrenceRegistrationReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	return receipt, true, nil
}

func decodeTaskMeetingOccurrenceRegistrationReceipt(prior operationReceipt) (TaskMeetingOccurrenceRegistrationReceipt, error) {
	if prior.operationKind != taskMeetingOccurrenceRegistrationOperation {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	var receipt TaskMeetingOccurrenceRegistrationReceipt
	if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || receipt.ScheduleID.Validate() != nil || receipt.RegistrationEvidenceID.Validate() != nil ||
		receipt.RegistrationEventID.Validate() != nil || receipt.PinEventID.Validate() != nil || receipt.PinnedAt.IsZero() || !validTaskMeetingDigest(receipt.ConfigSHA256) {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	if receipt.OwnerUserID == "" || strings.TrimSpace(receipt.OwnerUserID) != receipt.OwnerUserID || receipt.ConfigRevision == 0 {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(encoded, []byte(prior.resultJSON)) {
		return TaskMeetingOccurrenceRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	return receipt, nil
}

func validateTaskMeetingOccurrenceRegistrationEventTx(ctx context.Context, tx *sql.Tx, receipt TaskMeetingOccurrenceRegistrationReceipt) error {
	registration, found, err := readTaskMeetingRegistrationReceiptTx(ctx, tx, receipt.OwnerUserID, domainmeeting.TaskMeetingRegistrationKey, receipt.ConfigRevision)
	if err != nil || !found || registration.ScheduleID != receipt.ScheduleID || registration.ConfigSHA256 != receipt.ConfigSHA256 || registration.RegistrationEvidenceID != receipt.RegistrationEvidenceID || registration.EventID != receipt.RegistrationEventID {
		return ErrTaskMeetingReceiptBad
	}
	var eventID, traceID, schema, eventType, component, occurred, envelope string
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE event_id=?`, string(receipt.PinEventID)).Scan(&eventID, &sequence, &traceID, &schema, &eventType, &component, &occurred, &envelope); err != nil {
		return ErrTaskMeetingReceiptBad
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, component, occurred, envelope)
	if err != nil || event.ComponentID != taskMeetingComponent || event.EventType != taskMeetingOccurrenceRegistrationEvent || event.ScheduleID != receipt.ScheduleID || !event.OccurredAt.Equal(receipt.PinnedAt) ||
		payloadString(event.Payload, "occurrence_key") != receipt.OccurrenceKey || payloadString(event.Payload, "owner_user_id") != receipt.OwnerUserID ||
		payloadString(event.Payload, "config_sha256") != receipt.ConfigSHA256 || payloadString(event.Payload, "registration_evidence_id") != string(receipt.RegistrationEvidenceID) ||
		payloadString(event.Payload, "registration_event_id") != string(receipt.RegistrationEventID) || eventPayloadUint(event.Payload["config_revision"]) != receipt.ConfigRevision {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func taskMeetingOccurrenceRegistrationOperationID(occurrence domainmeeting.OccurrenceKey) string {
	digest, _ := hashJSON(occurrence)
	return "taskmeeting:occurrence-registration:" + digest
}

func sameTaskMeetingScheduleRegistrationReceipt(left, right TaskMeetingScheduleRegistrationReceipt) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
