package eventstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskMeetingDeliveryOperation = "taskmeeting.confirm_group_delivery"
	taskMeetingDeliveryEvent     = "taskmeeting.group_delivered"
)

// TaskMeetingGroupDeliveryReceipt pins the aggregate's canonical delivery
// evidence to the eight durable recipient-context consumption proofs. It
// stores no message body or derived transcript text.
type TaskMeetingGroupDeliveryReceipt struct {
	Reference         domaintask.TaskMeetingMessageReference  `json:"reference"`
	Evidence          domainmeeting.DeliveryEvidence          `json:"evidence"`
	ConsumptionProofs []domaintask.TaskMeetingConsumedMessage `json:"consumption_proofs"`
	EventID           modulecore.EventID                      `json:"event_id"`
	PersistedAt       time.Time                               `json:"persisted_at"`
}

// ResolveTaskMeetingGroupDelivery reads or writes the owner receipt for a
// fully consumed group message. Authorization and TaskManager receipt reads
// run outside EventStore transactions. The canonical delivery evidence is
// minted only once after all eight exact persisted proofs verify.
func (s *SQLiteStore) ResolveTaskMeetingGroupDelivery(
	ctx context.Context,
	reference domaintask.TaskMeetingMessageReference,
	reader domaintask.TaskMeetingMessageConsumptionReader,
	authorize domaintask.TaskMeetingMessageReadAuthorizationCheck,
) (domainmeeting.DeliveryEvidence, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return domainmeeting.DeliveryEvidence{}, false, err
	}
	if ctx == nil || reader == nil || authorize == nil || reference.Validate() != nil {
		return domainmeeting.DeliveryEvidence{}, false, ErrTaskMeetingUnavailable
	}
	messageReceipt, found, proofs, err := s.readTaskMeetingDeliveryInputs(ctx, reference)
	if err != nil || !found {
		return domainmeeting.DeliveryEvidence{}, found, err
	}
	for _, storedProof := range proofs {
		actual, err := reader.FindTaskMeetingConsumedMessage(ctx, storedProof.ConsumerTaskID, storedProof.ConsumerRunID, reference, authorize)
		if err != nil {
			if errors.Is(err, domaintask.ErrTaskMeetingNotFound) {
				return domainmeeting.DeliveryEvidence{}, false, nil
			}
			return domainmeeting.DeliveryEvidence{}, false, err
		}
		if !sameTaskMeetingConsumedMessage(actual, storedProof) {
			return domainmeeting.DeliveryEvidence{}, false, ErrTaskMeetingConflict
		}
	}
	inputHash, err := hashJSON(struct {
		Reference domaintask.TaskMeetingMessageReference  `json:"reference"`
		Proofs    []domaintask.TaskMeetingConsumedMessage `json:"proofs"`
	}{reference, proofs})
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, false, err
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(proofs)
		if err != nil {
			return domainmeeting.DeliveryEvidence{}, false, err
		}
		var evidence domainmeeting.DeliveryEvidence
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var inner error
			evidence, inner = s.persistTaskMeetingGroupDeliveryInline(commandCtx, messageReceipt, reference, proofs, inputHash)
			return inner
		})
		return evidence, err == nil, err
	}
	evidence, err := s.persistTaskMeetingGroupDeliveryInline(ctx, messageReceipt, reference, proofs, inputHash)
	return evidence, err == nil, err
}

// ResolveTaskMeetingGroupDeliveryReceipt is the typed transcript-owner variant
// of ResolveTaskMeetingGroupDelivery. It returns the exact persisted message
// and eight-recipient proof references after the same owner verification.
func (s *SQLiteStore) ResolveTaskMeetingGroupDeliveryReceipt(
	ctx context.Context,
	reference domaintask.TaskMeetingMessageReference,
	reader domaintask.TaskMeetingMessageConsumptionReader,
	authorize domaintask.TaskMeetingMessageReadAuthorizationCheck,
) (TaskMeetingGroupDeliveryReceipt, bool, error) {
	evidence, found, err := s.ResolveTaskMeetingGroupDelivery(ctx, reference, reader, authorize)
	if err != nil || !found {
		return TaskMeetingGroupDeliveryReceipt{}, found, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingGroupDeliveryReceipt{}, false, err
	}
	defer tx.Rollback()
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingDeliveryOperationID(reference))
	if err != nil || !found || prior.operationKind != taskMeetingDeliveryOperation {
		return TaskMeetingGroupDeliveryReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	receipt, err := decodeTaskMeetingGroupDeliveryReceipt(prior)
	if err != nil || receipt.Reference != reference || !sameTaskMeetingDeliveryEvidence(receipt.Evidence, evidence) || validateTaskMeetingGroupDeliveryEventTx(ctx, tx, receipt) != nil {
		return TaskMeetingGroupDeliveryReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupDeliveryReceipt{}, false, err
	}
	return receipt, true, nil
}

func sameTaskMeetingDeliveryEvidence(left, right domainmeeting.DeliveryEvidence) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func (s *SQLiteStore) readTaskMeetingDeliveryInputs(ctx context.Context, reference domaintask.TaskMeetingMessageReference) (TaskMeetingGroupMessageReceipt, bool, []domaintask.TaskMeetingConsumedMessage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, nil, err
	}
	defer tx.Rollback()
	messageOp, found, err := readOperationReceipt(ctx, tx, taskMeetingMessageOperationID(reference.OccurrenceKey, reference.MessageID))
	if err != nil || !found {
		return TaskMeetingGroupMessageReceipt{}, found, nil, err
	}
	if messageOp.operationKind != taskMeetingMessageOperation {
		return TaskMeetingGroupMessageReceipt{}, false, nil, ErrTaskMeetingReceiptBad
	}
	var message TaskMeetingGroupMessageReceipt
	if json.Unmarshal([]byte(messageOp.resultJSON), &message) != nil || message.Reference != reference || message.Reference.Validate() != nil ||
		message.EvidenceID.Validate() != nil || message.CreatedAt.IsZero() || len(message.Audience) != domaintask.TaskMeetingAudienceSize || len(message.WorkIDs) != len(message.Audience) {
		return TaskMeetingGroupMessageReceipt{}, false, nil, ErrTaskMeetingReceiptBad
	}
	proofs := make([]domaintask.TaskMeetingConsumedMessage, 0, len(message.Audience))
	for index, actorID := range message.Audience {
		prior, hasProof, err := readOperationReceipt(ctx, tx, taskMeetingConsumptionOperationID(reference, actorID))
		if err != nil {
			return TaskMeetingGroupMessageReceipt{}, false, nil, err
		}
		if !hasProof {
			return message, false, nil, nil
		}
		if prior.operationKind != taskMeetingConsumeOperation {
			return TaskMeetingGroupMessageReceipt{}, false, nil, ErrTaskMeetingReceiptBad
		}
		var consumption TaskMeetingGroupConsumptionReceipt
		if json.Unmarshal([]byte(prior.resultJSON), &consumption) != nil || consumption.Proof.Reference != reference || consumption.Proof.ActorID != actorID ||
			consumption.WorkID != message.WorkIDs[index] || consumption.EventID.Validate() != nil || consumption.EvidenceID.Validate() != nil || consumption.RecordedAt.IsZero() || !validTaskMeetingConsumedProofShape(consumption.Proof, actorID, reference) {
			return TaskMeetingGroupMessageReceipt{}, false, nil, ErrTaskMeetingReceiptBad
		}
		proofs = append(proofs, consumption.Proof)
	}
	if !equalStrings(message.Audience, func() []string {
		values, _, err := canonicalTaskMeetingAudience(message.Audience)
		if err != nil {
			return nil
		}
		return values
	}()) {
		return TaskMeetingGroupMessageReceipt{}, false, nil, ErrTaskMeetingReceiptBad
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupMessageReceipt{}, false, nil, err
	}
	return message, true, proofs, nil
}

func (s *SQLiteStore) persistTaskMeetingGroupDeliveryInline(
	ctx context.Context,
	message TaskMeetingGroupMessageReceipt,
	reference domaintask.TaskMeetingMessageReference,
	proofs []domaintask.TaskMeetingConsumedMessage,
	inputHash string,
) (domainmeeting.DeliveryEvidence, error) {
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	defer unlock()
	defer tx.Rollback()
	operationID := taskMeetingDeliveryOperationID(reference)
	currentMessage, found, err := readTaskMeetingGroupMessageReceiptTx(ctx, tx, reference)
	if err != nil || !found || currentMessage.Reference != message.Reference {
		return domainmeeting.DeliveryEvidence{}, ErrTaskMeetingConflict
	}
	currentProofs, err := readTaskMeetingConsumptionProofsTx(ctx, tx, currentMessage, reference)
	if err != nil || !sameTaskMeetingConsumedMessageSlices(currentProofs, proofs) {
		return domainmeeting.DeliveryEvidence{}, ErrTaskMeetingConflict
	}
	prior, found, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	if found {
		if prior.operationKind != taskMeetingDeliveryOperation || prior.inputSHA256 != inputHash {
			return domainmeeting.DeliveryEvidence{}, ErrTaskMeetingConflict
		}
		receipt, err := decodeTaskMeetingGroupDeliveryReceipt(prior)
		if err != nil || validateTaskMeetingGroupDeliveryReceipt(receipt, currentMessage, proofs) != nil || validateTaskMeetingGroupDeliveryEventTx(ctx, tx, receipt) != nil {
			return domainmeeting.DeliveryEvidence{}, ErrTaskMeetingReceiptBad
		}
		if err := tx.Commit(); err != nil {
			return domainmeeting.DeliveryEvidence{}, err
		}
		return receipt.Evidence, nil
	}
	if err := validateTaskMeetingReferenceEventTx(ctx, tx, reference, message.Audience[0]); err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	key, err := parseTaskMeetingOccurrenceKey(reference.OccurrenceKey)
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, ErrTaskMeetingReceiptBad
	}
	observedAt := proofs[0].AcceptedAt.UTC()
	for _, proof := range proofs[1:] {
		if proof.AcceptedAt.After(observedAt) {
			observedAt = proof.AcceptedAt.UTC()
		}
	}
	persistedAt := time.Now().UTC()
	evidence := domainmeeting.DeliveryEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: reference.AttemptGeneration, Epoch: reference.AttemptEpoch,
		ScopeEvidenceID: reference.ScopeEvidenceID, ThreadID: reference.ThreadID, MessageID: reference.MessageID,
		Delivered: true, Audience: append([]string(nil), message.Audience...), ObservedAt: observedAt,
	}
	event := modulecore.NewRootEventEnvelope(taskMeetingComponent, taskMeetingDeliveryEvent, persistedAt, map[string]any{
		"reference": reference, "delivery": evidence, "consumption_proofs": proofs,
		"audience_sha256": reference.AudienceSHA256, "persisted_at": persistedAt,
	})
	event.SessionID, event.ThreadID, event.TaskID, event.RunID, event.MessageID = reference.SessionID, reference.ThreadID, message.SenderTaskID, message.SenderRunID, reference.MessageID
	event.ActorKind, event.ActorID, event.EvidenceID = "system", "taskmeeting.owner", evidence.EvidenceID
	sequence, err := s.nextSequence(ctx)
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	event.EventSeq = sequence
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	receipt := TaskMeetingGroupDeliveryReceipt{Reference: reference, Evidence: evidence, ConsumptionProofs: proofs, EventID: event.EventID, PersistedAt: persistedAt}
	if err := insertOperationReceipt(ctx, tx, operationID, taskMeetingDeliveryOperation, inputHash, receipt); err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	if err := tx.Commit(); err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	return evidence, nil
}

func readTaskMeetingConsumptionProofsTx(ctx context.Context, tx *sql.Tx, message TaskMeetingGroupMessageReceipt, reference domaintask.TaskMeetingMessageReference) ([]domaintask.TaskMeetingConsumedMessage, error) {
	proofs := make([]domaintask.TaskMeetingConsumedMessage, 0, len(message.Audience))
	for index, actorID := range message.Audience {
		prior, found, err := readOperationReceipt(ctx, tx, taskMeetingConsumptionOperationID(reference, actorID))
		if err != nil || !found || prior.operationKind != taskMeetingConsumeOperation {
			return nil, ErrTaskMeetingConflict
		}
		var receipt TaskMeetingGroupConsumptionReceipt
		if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || receipt.Proof.Reference != reference || receipt.Proof.ActorID != actorID || receipt.WorkID != message.WorkIDs[index] {
			return nil, ErrTaskMeetingReceiptBad
		}
		if !validTaskMeetingConsumedProofShape(receipt.Proof, actorID, reference) {
			return nil, ErrTaskMeetingReceiptBad
		}
		work := LeasedWorkItem{WorkItem: WorkItem{WorkID: receipt.WorkID, Kind: taskMeetingDeliveryWorkKind(actorID), SourceEventID: reference.EventID, SessionID: reference.SessionID}}
		if !taskMeetingWorkHasConsumptionReceipt(ctx, tx, work) {
			return nil, ErrTaskMeetingReceiptBad
		}
		proofs = append(proofs, receipt.Proof)
	}
	return proofs, nil
}

func readTaskMeetingGroupMessageReceiptTx(ctx context.Context, tx *sql.Tx, reference domaintask.TaskMeetingMessageReference) (TaskMeetingGroupMessageReceipt, bool, error) {
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingMessageOperationID(reference.OccurrenceKey, reference.MessageID))
	if err != nil || !found {
		return TaskMeetingGroupMessageReceipt{}, found, err
	}
	if prior.operationKind != taskMeetingMessageOperation {
		return TaskMeetingGroupMessageReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	var receipt TaskMeetingGroupMessageReceipt
	if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || receipt.Reference != reference || receipt.Reference.Validate() != nil ||
		len(receipt.Audience) != domaintask.TaskMeetingAudienceSize || len(receipt.WorkIDs) != len(receipt.Audience) {
		return TaskMeetingGroupMessageReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	return receipt, true, nil
}

func decodeTaskMeetingGroupDeliveryReceipt(prior operationReceipt) (TaskMeetingGroupDeliveryReceipt, error) {
	if prior.operationKind != taskMeetingDeliveryOperation {
		return TaskMeetingGroupDeliveryReceipt{}, ErrTaskMeetingReceiptBad
	}
	var receipt TaskMeetingGroupDeliveryReceipt
	if json.Unmarshal([]byte(prior.resultJSON), &receipt) != nil || receipt.Reference.Validate() != nil || receipt.Evidence.EvidenceID.Validate() != nil ||
		receipt.EventID.Validate() != nil || receipt.PersistedAt.IsZero() || len(receipt.ConsumptionProofs) != domaintask.TaskMeetingAudienceSize {
		return TaskMeetingGroupDeliveryReceipt{}, ErrTaskMeetingReceiptBad
	}
	return receipt, nil
}

func validTaskMeetingConsumedProofShape(proof domaintask.TaskMeetingConsumedMessage, actorID string, reference domaintask.TaskMeetingMessageReference) bool {
	return proof.Reference == reference && proof.ActorID == actorID && proof.ConsumerTaskID.Validate() == nil && proof.ConsumerRunID.Validate() == nil &&
		validTaskMeetingDigest(proof.ContextSHA256) && !proof.AcceptedAt.IsZero() && proof.EvidenceID.Validate() == nil
}

func validateTaskMeetingGroupDeliveryReceipt(receipt TaskMeetingGroupDeliveryReceipt, message TaskMeetingGroupMessageReceipt, proofs []domaintask.TaskMeetingConsumedMessage) error {
	key, err := parseTaskMeetingOccurrenceKey(receipt.Reference.OccurrenceKey)
	if err != nil || receipt.Reference != message.Reference || !sameTaskMeetingConsumedMessageSlices(receipt.ConsumptionProofs, proofs) || len(proofs) != len(message.Audience) ||
		receipt.Evidence.EvidenceID.Validate() != nil || receipt.Evidence.Key != key || receipt.Evidence.Generation != receipt.Reference.AttemptGeneration ||
		receipt.Evidence.Epoch != receipt.Reference.AttemptEpoch || receipt.Evidence.ScopeEvidenceID != receipt.Reference.ScopeEvidenceID ||
		receipt.Evidence.ThreadID != receipt.Reference.ThreadID || receipt.Evidence.MessageID != receipt.Reference.MessageID || !receipt.Evidence.Delivered ||
		!equalStrings(receipt.Evidence.Audience, message.Audience) || receipt.PersistedAt.IsZero() {
		return ErrTaskMeetingReceiptBad
	}
	observedAt := proofs[0].AcceptedAt.UTC()
	for index, proof := range proofs {
		if proof.ActorID != message.Audience[index] || !validTaskMeetingConsumedProofShape(proof, message.Audience[index], receipt.Reference) {
			return ErrTaskMeetingReceiptBad
		}
		if proof.AcceptedAt.After(observedAt) {
			observedAt = proof.AcceptedAt.UTC()
		}
	}
	if !receipt.Evidence.ObservedAt.Equal(observedAt) {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func validateTaskMeetingGroupDeliveryEventTx(ctx context.Context, tx *sql.Tx, receipt TaskMeetingGroupDeliveryReceipt) error {
	var eventID, traceID, schema, eventType, component, occurred, envelope string
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE event_id=?`, string(receipt.EventID)).Scan(&eventID, &sequence, &traceID, &schema, &eventType, &component, &occurred, &envelope); err != nil {
		return ErrTaskMeetingReceiptBad
	}
	event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, component, occurred, envelope)
	if err != nil || event.ComponentID != taskMeetingComponent || event.EventType != taskMeetingDeliveryEvent || event.EvidenceID != receipt.Evidence.EvidenceID ||
		event.SessionID != receipt.Reference.SessionID || event.ThreadID != receipt.Reference.ThreadID || event.MessageID != receipt.Reference.MessageID ||
		payloadString(event.Payload, "audience_sha256") != receipt.Reference.AudienceSHA256 || !event.OccurredAt.Equal(receipt.PersistedAt) || payloadString(event.Payload, "persisted_at") != receipt.PersistedAt.Format(time.RFC3339Nano) {
		return ErrTaskMeetingReceiptBad
	}
	if !sameTaskMeetingDeliveryEventPayload(event.Payload, receipt) {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func sameTaskMeetingDeliveryEventPayload(payload map[string]any, receipt TaskMeetingGroupDeliveryReceipt) bool {
	var evidence domainmeeting.DeliveryEvidence
	var reference domaintask.TaskMeetingMessageReference
	var proofs []domaintask.TaskMeetingConsumedMessage
	if json.Unmarshal(mustJSON(payload["delivery"]), &evidence) != nil || json.Unmarshal(mustJSON(payload["reference"]), &reference) != nil || json.Unmarshal(mustJSON(payload["consumption_proofs"]), &proofs) != nil {
		return false
	}
	evidenceJSON, evidenceErr := json.Marshal(evidence)
	receiptEvidenceJSON, receiptEvidenceErr := json.Marshal(receipt.Evidence)
	return evidenceErr == nil && receiptEvidenceErr == nil && bytes.Equal(evidenceJSON, receiptEvidenceJSON) &&
		reference == receipt.Reference && sameTaskMeetingConsumedMessageSlices(proofs, receipt.ConsumptionProofs)
}

func sameTaskMeetingConsumedMessageSlices(left, right []domaintask.TaskMeetingConsumedMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameTaskMeetingConsumedMessage(left[index], right[index]) {
			return false
		}
	}
	return true
}

func taskMeetingDeliveryOperationID(reference domaintask.TaskMeetingMessageReference) string {
	digest, _ := hashJSON(reference)
	return "taskmeeting:delivery:" + digest
}

func parseTaskMeetingOccurrenceKey(value string) (domainmeeting.OccurrenceKey, error) {
	parts := strings.Split(value, "|")
	if len(parts) != 4 {
		return domainmeeting.OccurrenceKey{}, fmt.Errorf("invalid TaskMeeting occurrence key")
	}
	key := domainmeeting.OccurrenceKey{ScheduleID: modulecore.ScheduleID(parts[0]), LocalDate: parts[1], Slot: parts[2], Timezone: parts[3]}
	return key, key.Validate()
}
