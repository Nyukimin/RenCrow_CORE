package eventstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskMeetingWorldGatherIntentOperation  = "taskmeeting.world_gather_intent"
	taskMeetingWorldGatherOutcomeOperation = "taskmeeting.world_gather_outcome"
	taskMeetingWorldGatherIntentEvent      = "taskmeeting.world_gather_intent_prepared"
	taskMeetingWorldGatherOutcomeEvent     = "taskmeeting.world_gather_outcome_recorded"
)

type taskMeetingWorldGatherStoredRecord struct {
	Intent   domainmeeting.WorldGatherCommandIntent   `json:"intent"`
	Outcome  *domainmeeting.WorldGatherCommandOutcome `json:"outcome,omitempty"`
	EventID  modulecore.EventID                       `json:"event_id"`
	EventSeq uint64                                   `json:"event_seq"`
}

func (s *SQLiteStore) FindTaskMeetingWorldGather(
	ctx context.Context,
	intent domainmeeting.WorldGatherCommandIntent,
	reader domaintask.TaskMeetingInvocationReader,
	authorizeRead domaintask.TaskMeetingReadAuthorizationCheck,
) (domainmeeting.WorldGatherCommandIntent, domainmeeting.WorldGatherCommandOutcome, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
	}
	if ctx == nil || reader == nil || authorizeRead == nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, ErrTaskMeetingUnavailable
	}
	if err := validateTaskMeetingWorldGatherIntent(intent); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
	}
	task, run, _, err := reader.GetTaskMeetingInvocationState(ctx, intent.Scope.TaskID, intent.Scope.RunID, authorizeRead)
	if err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
	}
	if err := validateTaskMeetingWorldRootBinding(intent.Scope, task, run); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
	}
	defer tx.Rollback()
	registrationScope := intent.Scope
	registrationScope.Snapshot = domainmeeting.WorldSnapshotObservation{}
	if err := validateTaskMeetingWorldRegistrationTx(ctx, tx, registrationScope); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, ErrTaskMeetingForbidden
	}
	if err := validateTaskMeetingWorldGatherSessionTx(ctx, tx, intent.Scope); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
	}
	stored, found, err := readTaskMeetingWorldGatherIntentTx(ctx, tx, intent)
	if err != nil || !found {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, found, err
	}
	outcome, hasOutcome, err := readTaskMeetingWorldGatherOutcomeTx(ctx, tx, intent, stored)
	if err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
	}
	if intent.Phase == domainmeeting.WorldGatherPhaseFinish {
		if err := validateTaskMeetingWorldGatherStartTx(ctx, tx, intent, task, run); err != nil {
			return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, domainmeeting.WorldGatherCommandOutcome{}, false, err
	}
	if !hasOutcome {
		return stored.Intent, domainmeeting.WorldGatherCommandOutcome{}, true, nil
	}
	return stored.Intent, outcome, true, nil
}

func (s *SQLiteStore) PrepareTaskMeetingWorldGather(
	ctx context.Context,
	intent domainmeeting.WorldGatherCommandIntent,
	reader domaintask.TaskMeetingInvocationReader,
	authorizeRead domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeNewEffect domainmeeting.TaskMeetingWorldGatherAuthorizationCheck,
) (domainmeeting.WorldGatherCommandIntent, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	if ctx == nil || reader == nil || authorizeRead == nil || authorizeNewEffect == nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, ErrTaskMeetingUnavailable
	}
	if err := validateTaskMeetingWorldGatherIntent(intent); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(intent)
		if err != nil {
			return domainmeeting.WorldGatherCommandIntent{}, false, err
		}
		var prepared domainmeeting.WorldGatherCommandIntent
		var created bool
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var inner error
			prepared, created, inner = s.PrepareTaskMeetingWorldGather(commandCtx, intent, reader, authorizeRead, authorizeNewEffect)
			return inner
		})
		return prepared, created, err
	}
	task, run, shared, err := reader.GetTaskMeetingInvocationState(ctx, intent.Scope.TaskID, intent.Scope.RunID, authorizeRead)
	if err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	if err := validateTaskMeetingWorldRootBinding(intent.Scope, task, run); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	if run.Status != domaintask.RunStatusRunning {
		return domainmeeting.WorldGatherCommandIntent{}, false, ErrTaskMeetingForbidden
	}
	if err := authorizeNewEffect(ctx, task, run, shared, intent); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, ErrTaskMeetingForbidden
	}
	inputHash, err := taskMeetingWorldGatherIntentHash(intent)
	if err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	defer unlock()
	defer tx.Rollback()
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingWorldGatherIntentOperationID(intent.StableKey))
	if err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	if found {
		if prior.operationKind != taskMeetingWorldGatherIntentOperation || prior.inputSHA256 != inputHash {
			return domainmeeting.WorldGatherCommandIntent{}, false, ErrTaskMeetingConflict
		}
		stored, err := decodeTaskMeetingWorldGatherRecord(prior.resultJSON)
		storedHash, hashErr := taskMeetingWorldGatherIntentHash(stored.Intent)
		if err != nil || hashErr != nil || stored.Intent.StableKey != intent.StableKey || stored.Intent.Validate() != nil || storedHash != inputHash || stored.Outcome != nil || validateStoredTaskMeetingWorldGatherEvent(ctx, tx, stored, stored.Intent, task, run, taskMeetingWorldGatherIntentEvent) != nil {
			return domainmeeting.WorldGatherCommandIntent{}, false, ErrTaskMeetingReceiptBad
		}
		registrationScope := intent.Scope
		registrationScope.Snapshot = domainmeeting.WorldSnapshotObservation{}
		if err := validateTaskMeetingWorldRegistrationTx(ctx, tx, registrationScope); err != nil {
			return domainmeeting.WorldGatherCommandIntent{}, false, ErrTaskMeetingForbidden
		}
		if err := validateTaskMeetingWorldGatherSessionTx(ctx, tx, intent.Scope); err != nil {
			return domainmeeting.WorldGatherCommandIntent{}, false, err
		}
		if intent.Phase == domainmeeting.WorldGatherPhaseFinish {
			if err := validateTaskMeetingWorldGatherStartTx(ctx, tx, intent, task, run); err != nil {
				return domainmeeting.WorldGatherCommandIntent{}, false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return domainmeeting.WorldGatherCommandIntent{}, false, err
		}
		return stored.Intent, false, nil
	}
	if intent.Phase == domainmeeting.WorldGatherPhaseFinish {
		if err := validateTaskMeetingWorldGatherStartTx(ctx, tx, intent, task, run); err != nil {
			return domainmeeting.WorldGatherCommandIntent{}, false, err
		}
	}
	registrationScope := intent.Scope
	registrationScope.Snapshot = domainmeeting.WorldSnapshotObservation{}
	if err := validateTaskMeetingWorldRegistrationTx(ctx, tx, registrationScope); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, ErrTaskMeetingForbidden
	}
	if err := validateTaskMeetingWorldGatherSessionTx(ctx, tx, intent.Scope); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	stored := taskMeetingWorldGatherStoredRecord{Intent: intent}
	if err := s.insertTaskMeetingWorldGatherEvent(ctx, tx, &stored, task, run, intent, taskMeetingWorldGatherIntentEvent, map[string]any{"intent": intent}); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	if err := insertOperationReceipt(ctx, tx, taskMeetingWorldGatherIntentOperationID(intent.StableKey), taskMeetingWorldGatherIntentOperation, inputHash, stored); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, false, err
	}
	return stored.Intent, true, nil
}

func (s *SQLiteStore) RecordTaskMeetingWorldGatherOutcome(
	ctx context.Context,
	intent domainmeeting.WorldGatherCommandIntent,
	outcome domainmeeting.WorldGatherCommandOutcome,
	reader domaintask.TaskMeetingInvocationReader,
	authorizeRead domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeWrite domainmeeting.TaskMeetingWorldGatherAuthorizationCheck,
) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil || reader == nil || authorizeRead == nil || authorizeWrite == nil {
		return ErrTaskMeetingUnavailable
	}
	if err := validateTaskMeetingWorldGatherIntent(intent); err != nil {
		return err
	}
	if err := outcome.ValidateFor(intent); err != nil {
		if errors.Is(err, domainmeeting.ErrWorldGatherCommandConflict) {
			return ErrTaskMeetingConflict
		}
		return err
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(struct {
			Intent  domainmeeting.WorldGatherCommandIntent  `json:"intent"`
			Outcome domainmeeting.WorldGatherCommandOutcome `json:"outcome"`
		}{Intent: intent, Outcome: outcome})
		if err != nil {
			return err
		}
		return s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			return s.RecordTaskMeetingWorldGatherOutcome(commandCtx, intent, outcome, reader, authorizeRead, authorizeWrite)
		})
	}
	task, run, shared, err := reader.GetTaskMeetingInvocationState(ctx, intent.Scope.TaskID, intent.Scope.RunID, authorizeRead)
	if err != nil {
		return err
	}
	if err := validateTaskMeetingWorldRootBinding(intent.Scope, task, run); err != nil {
		return err
	}
	if run.Status != domaintask.RunStatusRunning {
		return ErrTaskMeetingForbidden
	}
	if err := authorizeWrite(ctx, task, run, shared, intent); err != nil {
		return ErrTaskMeetingForbidden
	}
	inputHash, err := taskMeetingWorldGatherOutcomeHash(intent, outcome)
	if err != nil {
		return err
	}
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer tx.Rollback()
	storedIntent, found, err := readTaskMeetingWorldGatherIntentTx(ctx, tx, intent)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return ErrTaskMeetingReceiptBad
	}
	priorOutcome, outcomeFound, err := readOperationReceipt(ctx, tx, taskMeetingWorldGatherOutcomeOperationID(intent.StableKey))
	if err != nil {
		return err
	}
	if outcomeFound {
		if priorOutcome.operationKind != taskMeetingWorldGatherOutcomeOperation {
			return ErrTaskMeetingReceiptBad
		}
		var completed taskMeetingWorldGatherStoredRecord
		if json.Unmarshal([]byte(priorOutcome.resultJSON), &completed) != nil || completed.Outcome == nil {
			return ErrTaskMeetingReceiptBad
		}
		priorHash, hashErr := taskMeetingWorldGatherOutcomeHash(intent, *completed.Outcome)
		if hashErr != nil || priorOutcome.inputSHA256 != priorHash {
			return ErrTaskMeetingReceiptBad
		}
		if priorHash != inputHash {
			return ErrTaskMeetingConflict
		}
		completedIntentHash, completedIntentErr := taskMeetingWorldGatherIntentHash(completed.Intent)
		expectedIntentHash, expectedIntentErr := taskMeetingWorldGatherIntentHash(intent)
		if completedIntentErr != nil || expectedIntentErr != nil || completed.Intent.Validate() != nil || completed.Intent.StableKey != intent.StableKey || completedIntentHash != expectedIntentHash {
			return ErrTaskMeetingReceiptBad
		}
		if intent.Phase == domainmeeting.WorldGatherPhaseFinish {
			if err := validateTaskMeetingWorldGatherStartTx(ctx, tx, intent, task, run); err != nil {
				return err
			}
		}
		registrationScope := intent.Scope
		registrationScope.Snapshot = domainmeeting.WorldSnapshotObservation{}
		if err := validateTaskMeetingWorldRegistrationTx(ctx, tx, registrationScope); err != nil {
			return ErrTaskMeetingForbidden
		}
		if err := validateTaskMeetingWorldGatherSessionTx(ctx, tx, intent.Scope); err != nil {
			return err
		}
		if err := validateStoredTaskMeetingWorldGatherEvent(ctx, tx, completed, completed.Intent, task, run, taskMeetingWorldGatherOutcomeEvent); err != nil {
			return ErrTaskMeetingReceiptBad
		}
		return tx.Commit()
	}
	if intent.Phase == domainmeeting.WorldGatherPhaseFinish {
		if err := validateTaskMeetingWorldGatherStartTx(ctx, tx, intent, task, run); err != nil {
			return err
		}
	}
	registrationScope := intent.Scope
	registrationScope.Snapshot = domainmeeting.WorldSnapshotObservation{}
	if err := validateTaskMeetingWorldRegistrationTx(ctx, tx, registrationScope); err != nil {
		return ErrTaskMeetingForbidden
	}
	if err := validateTaskMeetingWorldGatherSessionTx(ctx, tx, intent.Scope); err != nil {
		return err
	}
	stored := taskMeetingWorldGatherStoredRecord{Intent: storedIntent.Intent, Outcome: &outcome}
	if err := s.insertTaskMeetingWorldGatherEvent(ctx, tx, &stored, task, run, storedIntent.Intent, taskMeetingWorldGatherOutcomeEvent, map[string]any{"intent": storedIntent.Intent, "outcome": outcome}); err != nil {
		return err
	}
	if err := insertOperationReceipt(ctx, tx, taskMeetingWorldGatherOutcomeOperationID(intent.StableKey), taskMeetingWorldGatherOutcomeOperation, inputHash, stored); err != nil {
		return err
	}
	return tx.Commit()
}

func taskMeetingWorldGatherIntentHash(intent domainmeeting.WorldGatherCommandIntent) (string, error) {
	if err := validateTaskMeetingWorldGatherIntent(intent); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(intent.StableKey))
	return hex.EncodeToString(digest[:]), nil
}

func validateTaskMeetingWorldGatherIntent(intent domainmeeting.WorldGatherCommandIntent) error {
	if err := intent.Validate(); err != nil {
		if errors.Is(err, domainmeeting.ErrWorldGatherCommandConflict) {
			return ErrTaskMeetingConflict
		}
		return err
	}
	return nil
}

func taskMeetingWorldGatherOutcomeHash(intent domainmeeting.WorldGatherCommandIntent, outcome domainmeeting.WorldGatherCommandOutcome) (string, error) {
	intentHash, err := taskMeetingWorldGatherIntentHash(intent)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		IntentHash string                                  `json:"intent_hash"`
		Outcome    domainmeeting.WorldGatherCommandOutcome `json:"outcome"`
	}{IntentHash: intentHash, Outcome: outcome})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func taskMeetingWorldGatherIntentOperationID(stableKey string) string {
	return "taskmeeting:world_gather:intent:" + stableKey
}

func taskMeetingWorldGatherOutcomeOperationID(stableKey string) string {
	return "taskmeeting:world_gather:outcome:" + stableKey
}

func readTaskMeetingWorldGatherIntentTx(ctx context.Context, tx *sql.Tx, request domainmeeting.WorldGatherCommandIntent) (taskMeetingWorldGatherStoredRecord, bool, error) {
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingWorldGatherIntentOperationID(request.StableKey))
	if err != nil || !found {
		return taskMeetingWorldGatherStoredRecord{}, found, err
	}
	hash, err := taskMeetingWorldGatherIntentHash(request)
	if err != nil {
		return taskMeetingWorldGatherStoredRecord{}, false, err
	}
	if prior.operationKind != taskMeetingWorldGatherIntentOperation || prior.inputSHA256 != hash {
		return taskMeetingWorldGatherStoredRecord{}, false, ErrTaskMeetingConflict
	}
	stored, err := decodeTaskMeetingWorldGatherRecord(prior.resultJSON)
	if err != nil {
		return taskMeetingWorldGatherStoredRecord{}, false, ErrTaskMeetingReceiptBad
	}
	storedHash, err := taskMeetingWorldGatherIntentHash(stored.Intent)
	if err != nil || stored.Intent.Validate() != nil || stored.Intent.StableKey != request.StableKey || storedHash != hash {
		return taskMeetingWorldGatherStoredRecord{}, false, ErrTaskMeetingReceiptBad
	}
	task, run, err := readTaskMeetingWorldGatherEnvelopeIdentity(ctx, tx, stored)
	if err != nil || validateStoredTaskMeetingWorldGatherEvent(ctx, tx, stored, stored.Intent, task, run, taskMeetingWorldGatherIntentEvent) != nil {
		return taskMeetingWorldGatherStoredRecord{}, false, ErrTaskMeetingReceiptBad
	}
	return stored, true, nil
}

func readTaskMeetingWorldGatherOutcomeTx(ctx context.Context, tx *sql.Tx, request domainmeeting.WorldGatherCommandIntent, storedIntent taskMeetingWorldGatherStoredRecord) (domainmeeting.WorldGatherCommandOutcome, bool, error) {
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingWorldGatherOutcomeOperationID(request.StableKey))
	if err != nil || !found {
		return domainmeeting.WorldGatherCommandOutcome{}, found, err
	}
	if prior.operationKind != taskMeetingWorldGatherOutcomeOperation {
		return domainmeeting.WorldGatherCommandOutcome{}, false, ErrTaskMeetingReceiptBad
	}
	var stored taskMeetingWorldGatherStoredRecord
	if json.Unmarshal([]byte(prior.resultJSON), &stored) != nil || stored.Outcome == nil || stored.Intent.StableKey != storedIntent.Intent.StableKey || stored.EventID.Validate() != nil || stored.EventSeq == 0 {
		return domainmeeting.WorldGatherCommandOutcome{}, false, ErrTaskMeetingReceiptBad
	}
	storedIntentHash, err := taskMeetingWorldGatherIntentHash(stored.Intent)
	expectedIntentHash, expectedErr := taskMeetingWorldGatherIntentHash(storedIntent.Intent)
	if err != nil || expectedErr != nil || stored.Intent.Validate() != nil || storedIntentHash != expectedIntentHash {
		return domainmeeting.WorldGatherCommandOutcome{}, false, ErrTaskMeetingReceiptBad
	}
	hash, err := taskMeetingWorldGatherOutcomeHash(request, *stored.Outcome)
	if err != nil || prior.inputSHA256 != hash {
		return domainmeeting.WorldGatherCommandOutcome{}, false, ErrTaskMeetingReceiptBad
	}
	task, run, err := readTaskMeetingWorldGatherEnvelopeIdentity(ctx, tx, stored)
	if err != nil || validateStoredTaskMeetingWorldGatherEvent(ctx, tx, stored, stored.Intent, task, run, taskMeetingWorldGatherOutcomeEvent) != nil || stored.Outcome.ValidateFor(stored.Intent) != nil {
		return domainmeeting.WorldGatherCommandOutcome{}, false, ErrTaskMeetingReceiptBad
	}
	return *stored.Outcome, true, nil
}

func decodeTaskMeetingWorldGatherRecord(result string) (taskMeetingWorldGatherStoredRecord, error) {
	var record taskMeetingWorldGatherStoredRecord
	if err := json.Unmarshal([]byte(result), &record); err != nil || record.Intent.StableKey == "" || record.EventID.Validate() != nil || record.EventSeq == 0 || validateTaskMeetingWorldGatherIntent(record.Intent) != nil {
		return taskMeetingWorldGatherStoredRecord{}, ErrTaskMeetingReceiptBad
	}
	return record, nil
}

func readTaskMeetingWorldGatherEnvelopeIdentity(ctx context.Context, tx *sql.Tx, stored taskMeetingWorldGatherStoredRecord) (domaintask.Task, domaintask.Run, error) {
	var envelopeJSON string
	var eventID, traceID, schema, eventType, component, occurred string
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE event_id=?`, string(stored.EventID)).
		Scan(&eventID, &sequence, &traceID, &schema, &eventType, &component, &occurred, &envelopeJSON)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, component, occurred, envelopeJSON)
	if err != nil || event.TaskID != stored.Intent.Scope.TaskID || event.RunID != stored.Intent.Scope.RunID {
		return domaintask.Task{}, domaintask.Run{}, ErrTaskMeetingReceiptBad
	}
	// Task and Run are reloaded through the caller's authorized reader; this
	// helper only validates that the canonical event owns the expected identity.
	return domaintask.Task{TaskID: event.TaskID}, domaintask.Run{TaskID: event.TaskID, RunID: event.RunID}, nil
}

func validateStoredTaskMeetingWorldGatherEvent(ctx context.Context, tx *sql.Tx, stored taskMeetingWorldGatherStoredRecord, request domainmeeting.WorldGatherCommandIntent, task domaintask.Task, run domaintask.Run, expectedType string) error {
	var eventID, traceID, schema, eventType, component, occurred, envelopeJSON string
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE event_id=?`, string(stored.EventID)).
		Scan(&eventID, &sequence, &traceID, &schema, &eventType, &component, &occurred, &envelopeJSON)
	if err != nil {
		return err
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, component, occurred, envelopeJSON)
	if err != nil || event.EventSeq == 0 || uint64(event.EventSeq) != stored.EventSeq || event.EventType != expectedType || event.ComponentID != taskMeetingComponent ||
		event.TaskID != task.TaskID || event.RunID != run.RunID || event.SessionID != request.Scope.Session || event.ThreadID != request.Scope.Thread || event.ActorID != request.ActorID {
		return ErrTaskMeetingReceiptBad
	}
	var recorded domainmeeting.WorldGatherCommandIntent
	encoded, err := json.Marshal(event.Payload["intent"])
	if err != nil || json.Unmarshal(encoded, &recorded) != nil {
		return ErrTaskMeetingReceiptBad
	}
	var commandPayload domainmeeting.WorldGatherCommandPayload
	decoder := json.NewDecoder(bytes.NewReader(recorded.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&commandPayload) != nil {
		return ErrTaskMeetingReceiptBad
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrTaskMeetingReceiptBad
	}
	recorded.Payload, err = json.Marshal(commandPayload)
	if err != nil {
		return ErrTaskMeetingReceiptBad
	}
	left, err := taskMeetingWorldGatherIntentHash(request)
	if err != nil {
		return err
	}
	right, err := taskMeetingWorldGatherIntentHash(recorded)
	if err != nil || left != right {
		return ErrTaskMeetingReceiptBad
	}
	if expectedType == taskMeetingWorldGatherOutcomeEvent {
		var recordedOutcome domainmeeting.WorldGatherCommandOutcome
		encoded, err = json.Marshal(event.Payload["outcome"])
		if err != nil || json.Unmarshal(encoded, &recordedOutcome) != nil || stored.Outcome == nil {
			return ErrTaskMeetingReceiptBad
		}
		storedJSON, _ := json.Marshal(stored.Outcome)
		eventJSON, _ := json.Marshal(recordedOutcome)
		if string(storedJSON) != string(eventJSON) {
			return ErrTaskMeetingReceiptBad
		}
	}
	return nil
}

func (s *SQLiteStore) insertTaskMeetingWorldGatherEvent(ctx context.Context, tx *sql.Tx, stored *taskMeetingWorldGatherStoredRecord, task domaintask.Task, run domaintask.Run, intent domainmeeting.WorldGatherCommandIntent, eventType string, payload map[string]any) error {
	traceID := modulecore.NewTraceID()
	eventID := modulecore.NewEventID()
	seq, err := s.nextSequence(ctx)
	if err != nil {
		return err
	}
	event := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: eventID, EventSeq: seq, TraceID: traceID,
		EventType: eventType, ComponentID: taskMeetingComponent, OccurredAt: s.taskMeetingNowUTC(),
		SessionID: intent.Scope.Session, ThreadID: intent.Scope.Thread, TaskID: task.TaskID, RunID: run.RunID,
		ActorKind: "agent", ActorID: intent.ActorID, Payload: payload,
	}
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return err
	}
	stored.EventID, stored.EventSeq = eventID, uint64(seq)
	return nil
}

func validateTaskMeetingWorldGatherSessionTx(ctx context.Context, tx *sql.Tx, scope domainmeeting.WorldObservationRequest) error {
	session, err := readTaskMeetingSessionReceiptTx(ctx, tx, scope.Key.String(), scope.Session)
	if err != nil {
		return err
	}
	if session.ThreadID != scope.Thread || session.ConfigRevision != scope.ConfigRevision || session.ConfigSHA256 != scope.ConfigSHA256 ||
		session.OwnerUserID != scope.OwnerUserID || !equalStrings(session.Audience, scope.Audience) || session.RegistrationEvidence != scope.RegistrationEvidenceID {
		return ErrTaskMeetingConflict
	}
	return nil
}

func validateTaskMeetingWorldGatherStartTx(ctx context.Context, tx *sql.Tx, intent domainmeeting.WorldGatherCommandIntent, task domaintask.Task, run domaintask.Run) error {
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingWorldGatherOutcomeOperationID(intent.StartStableKey))
	if err != nil || !found || prior.operationKind != taskMeetingWorldGatherOutcomeOperation {
		return ErrTaskMeetingReceiptBad
	}
	var started taskMeetingWorldGatherStoredRecord
	if json.Unmarshal([]byte(prior.resultJSON), &started) != nil || started.Outcome == nil || started.Intent.Phase != domainmeeting.WorldGatherPhaseStart ||
		started.Intent.StableKey != intent.StartStableKey || started.Intent.Validate() != nil || !sameTaskMeetingWorldGatherScope(started.Intent.Scope, intent.Scope) ||
		started.Intent.ActorID != intent.ActorID || started.Intent.WorldActorID != intent.WorldActorID || started.Intent.TargetRoomID != intent.TargetRoomID ||
		started.Outcome.Receipt.ActivityID != intent.ActivityID || started.Outcome.Receipt.RouteID != intent.RouteID || started.Outcome.Receipt.SceneSHA256 != intent.SceneSHA256 ||
		!started.Outcome.Receipt.DueAt.Equal(intent.DueAt) || started.Outcome.ValidateFor(started.Intent) != nil || started.EventID.Validate() != nil || started.EventSeq == 0 {
		return ErrTaskMeetingConflict
	}
	_, err = taskMeetingWorldGatherIntentHash(started.Intent)
	startOutcomeHash, outcomeErr := taskMeetingWorldGatherOutcomeHash(started.Intent, *started.Outcome)
	if err != nil || outcomeErr != nil || prior.inputSHA256 != startOutcomeHash {
		return ErrTaskMeetingReceiptBad
	}
	startIntent, startFound, err := readTaskMeetingWorldGatherIntentTx(ctx, tx, started.Intent)
	if err != nil || !startFound || startIntent.Intent.StableKey != intent.StartStableKey {
		return ErrTaskMeetingReceiptBad
	}
	if err := validateStoredTaskMeetingWorldGatherEvent(ctx, tx, started, started.Intent, task, run, taskMeetingWorldGatherOutcomeEvent); err != nil {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func sameTaskMeetingWorldGatherScope(left, right domainmeeting.WorldObservationRequest) bool {
	return left.Key == right.Key && left.TaskID == right.TaskID && left.RunID == right.RunID && left.ConfigRevision == right.ConfigRevision &&
		left.ConfigSHA256 == right.ConfigSHA256 && left.OwnerUserID == right.OwnerUserID && left.RegistrationEvidenceID == right.RegistrationEvidenceID &&
		left.CoordinatorActorID == right.CoordinatorActorID && left.Session == right.Session && left.Thread == right.Thread &&
		equalStrings(left.Audience, right.Audience) &&
		sameWorldActorBindings(left.ActorBindings, right.ActorBindings) && left.Rooms == right.Rooms
}
