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
	"io"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskMeetingRegistrationOperationKind = "taskmeeting.register_schedule"
	taskMeetingRegistrationEventType     = "taskmeeting.schedule_registered"
)

// TaskMeetingRegistrationRequest contains the exact canonical declaration
// bytes shared by runtime, TaskManager, and this owner. Issued IDs are omitted.
type TaskMeetingRegistrationRequest struct {
	Declaration       domainmeeting.TaskMeetingDeclaration `json:"declaration"`
	FrozenDeclaration []byte                               `json:"frozen_declaration"`
}

// TaskMeetingScheduleRegistrationReceipt is the immutable EventStore proof
// for one owner declaration revision.
type TaskMeetingScheduleRegistrationReceipt struct {
	RegistrationKey        string                `json:"registration_key"`
	OwnerUserID            string                `json:"owner_user_id"`
	ScheduleID             modulecore.ScheduleID `json:"schedule_id"`
	RegistrationEvidenceID modulecore.EvidenceID `json:"registration_evidence_id"`
	Revision               uint64                `json:"revision"`
	FrozenDeclaration      json.RawMessage       `json:"frozen_declaration"`
	ConfigSHA256           string                `json:"config_sha256"`
	EventID                modulecore.EventID    `json:"event_id"`
	AcceptedAt             time.Time             `json:"accepted_at"`
}

type TaskMeetingRegistrationAuthorizationCheck func(context.Context, domainmeeting.TaskMeetingDeclaration) error

type TaskMeetingRegistrationReadAuthorizationCheck func(context.Context, string, string, uint64) error

// EnsureTaskMeetingScheduleRegistration commits an owner-managed declaration
// revision and its receipt atomically. Schedule identity is stable across
// revisions; callers cannot supply either issued identifier.
func (s *SQLiteStore) EnsureTaskMeetingScheduleRegistration(
	ctx context.Context,
	request TaskMeetingRegistrationRequest,
	authorizeCurrent TaskMeetingRegistrationAuthorizationCheck,
) (TaskMeetingScheduleRegistrationReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if ctx == nil || authorizeCurrent == nil {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingUnavailable
	}
	canonical, digest, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(request.Declaration)
	if err != nil || !bytes.Equal(canonical, request.FrozenDeclaration) {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingConflict
	}
	decoded, decodedDigest, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(request.FrozenDeclaration)
	if err != nil || digest != decodedDigest || !sameTaskMeetingDeclaration(decoded, request.Declaration) {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingConflict
	}
	if err := authorizeCurrent(ctx, decoded); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingForbidden
	}
	if err := ctx.Err(); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(request)
		if err != nil {
			return TaskMeetingScheduleRegistrationReceipt{}, err
		}
		var receipt TaskMeetingScheduleRegistrationReceipt
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var inner error
			receipt, inner = s.ensureTaskMeetingScheduleRegistrationInline(commandCtx, decoded, canonical, digest)
			return inner
		})
		return receipt, err
	}
	return s.ensureTaskMeetingScheduleRegistrationInline(ctx, decoded, canonical, digest)
}

// FindTaskMeetingScheduleRegistration reads one exact historical registration
// revision. It does not reactivate that revision as the current definition.
func (s *SQLiteStore) FindTaskMeetingScheduleRegistration(
	ctx context.Context,
	registrationKey, ownerUserID string,
	revision uint64,
	authorizeRead TaskMeetingRegistrationReadAuthorizationCheck,
) (TaskMeetingScheduleRegistrationReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if ctx == nil || authorizeRead == nil || registrationKey != domainmeeting.TaskMeetingRegistrationKey || strings.TrimSpace(ownerUserID) != ownerUserID || ownerUserID == "" || revision == 0 {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := authorizeRead(ctx, ownerUserID, registrationKey, revision); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingForbidden
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	defer tx.Rollback()
	receipt, found, err := readTaskMeetingRegistrationReceiptTx(ctx, tx, ownerUserID, registrationKey, revision)
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if !found {
		return TaskMeetingScheduleRegistrationReceipt{}, domaintask.ErrTaskMeetingNotFound
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	return receipt, nil
}

// FindLatestTaskMeetingScheduleRegistration reads the latest owner receipt by
// its stable registration identity. It is used to recover the known scheduler
// ID while configuration is disabled; it never activates or updates it.
// The read authorizer receives revision 0 to distinguish this exact-key
// lookup from an exact historical-revision read.
func (s *SQLiteStore) FindLatestTaskMeetingScheduleRegistration(
	ctx context.Context,
	registrationKey, ownerUserID string,
	authorizeRead TaskMeetingRegistrationReadAuthorizationCheck,
) (TaskMeetingScheduleRegistrationReceipt, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if ctx == nil || authorizeRead == nil || registrationKey != domainmeeting.TaskMeetingRegistrationKey || strings.TrimSpace(ownerUserID) != ownerUserID || ownerUserID == "" {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingUnavailable
	}
	if err := authorizeRead(ctx, ownerUserID, registrationKey, 0); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingForbidden
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	defer tx.Rollback()
	receipt, found, err := latestTaskMeetingRegistrationReceiptTx(ctx, tx, ownerUserID, registrationKey)
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if !found {
		return TaskMeetingScheduleRegistrationReceipt{}, domaintask.ErrTaskMeetingNotFound
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	return receipt, nil
}

func (s *SQLiteStore) ensureTaskMeetingScheduleRegistrationInline(
	ctx context.Context,
	declaration domainmeeting.TaskMeetingDeclaration,
	canonical []byte,
	digest string,
) (TaskMeetingScheduleRegistrationReceipt, error) {
	tx, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	defer unlock()
	defer tx.Rollback()

	operationID := taskMeetingRegistrationOperationID(declaration.OwnerUserID, declaration.RegistrationKey, declaration.Revision)
	if prior, found, err := readOperationReceipt(ctx, tx, operationID); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	} else if found {
		receipt, err := decodeTaskMeetingRegistrationReceipt(prior)
		if err != nil {
			return TaskMeetingScheduleRegistrationReceipt{}, err
		}
		if prior.inputSHA256 != digest || !bytes.Equal(receipt.FrozenDeclaration, canonical) {
			return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingConflict
		}
		if err := validateTaskMeetingRegistrationReceiptEvent(ctx, tx, receipt); err != nil {
			return TaskMeetingScheduleRegistrationReceipt{}, err
		}
		if err := tx.Commit(); err != nil {
			return TaskMeetingScheduleRegistrationReceipt{}, err
		}
		return receipt, nil
	}

	latest, found, err := latestTaskMeetingRegistrationReceiptTx(ctx, tx, declaration.OwnerUserID, declaration.RegistrationKey)
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	var scheduleID modulecore.ScheduleID
	if found {
		if declaration.Revision <= latest.Revision {
			return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingConflict
		}
		scheduleID = latest.ScheduleID
	} else {
		scheduleID = modulecore.NewScheduleID()
	}
	acceptedAt := time.Now().UTC()
	registrationEvidenceID := modulecore.NewEvidenceID()
	event := modulecore.NewRootEventEnvelope(taskMeetingComponent, taskMeetingRegistrationEventType, acceptedAt, map[string]any{
		"registration_key":         declaration.RegistrationKey,
		"owner_user_id":            declaration.OwnerUserID,
		"schedule_id":              string(scheduleID),
		"registration_evidence_id": string(registrationEvidenceID),
		"revision":                 declaration.Revision,
		"config_sha256":            digest,
		"venue_basis":              "owner_configured_venue_declaration",
		"ordered_audience_sha256":  hashOrderedStrings(declaration.Audience),
		"world_scope_sha256":       hashJSONMust(struct{ WorldID, TimelineID, TimelineEpoch string }{declaration.WorldID, declaration.TimelineID, declaration.TimelineEpoch}),
	})
	event.ScheduleID = scheduleID
	event.EvidenceID = registrationEvidenceID
	sequence, err := s.nextSequence(ctx)
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	event.EventSeq = sequence
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	receipt := TaskMeetingScheduleRegistrationReceipt{
		RegistrationKey:        declaration.RegistrationKey,
		OwnerUserID:            declaration.OwnerUserID,
		ScheduleID:             scheduleID,
		RegistrationEvidenceID: registrationEvidenceID,
		Revision:               declaration.Revision,
		FrozenDeclaration:      append(json.RawMessage(nil), canonical...),
		ConfigSHA256:           digest,
		EventID:                event.EventID,
		AcceptedAt:             acceptedAt,
	}
	if err := insertOperationReceipt(ctx, tx, operationID, taskMeetingRegistrationOperationKind, digest, receipt); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, err
	}
	return receipt, nil
}

func latestTaskMeetingRegistrationReceiptTx(ctx context.Context, tx *sql.Tx, ownerUserID, registrationKey string) (TaskMeetingScheduleRegistrationReceipt, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT operation_kind, input_sha256, result_json
		FROM live_operation_receipt
		WHERE operation_id LIKE ? AND operation_kind = ?`,
		taskMeetingRegistrationOperationPrefix(ownerUserID, registrationKey)+"%", taskMeetingRegistrationOperationKind)
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, false, err
	}
	stored := make([]operationReceipt, 0)
	for rows.Next() {
		var operationKind, inputHash, resultJSON string
		if err := rows.Scan(&operationKind, &inputHash, &resultJSON); err != nil {
			_ = rows.Close()
			return TaskMeetingScheduleRegistrationReceipt{}, false, err
		}
		stored = append(stored, operationReceipt{operationKind: operationKind, inputSHA256: inputHash, resultJSON: resultJSON})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return TaskMeetingScheduleRegistrationReceipt{}, false, err
	}
	if err := rows.Close(); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, false, err
	}
	var latest TaskMeetingScheduleRegistrationReceipt
	found := false
	for _, prior := range stored {
		receipt, err := decodeTaskMeetingRegistrationReceipt(prior)
		if err != nil {
			return TaskMeetingScheduleRegistrationReceipt{}, false, err
		}
		if receipt.OwnerUserID != ownerUserID || receipt.RegistrationKey != registrationKey {
			continue
		}
		if err := validateTaskMeetingRegistrationReceiptEvent(ctx, tx, receipt); err != nil {
			return TaskMeetingScheduleRegistrationReceipt{}, false, err
		}
		if !found || receipt.Revision > latest.Revision {
			latest, found = receipt, true
		} else if receipt.Revision == latest.Revision && receipt.ConfigSHA256 != latest.ConfigSHA256 {
			return TaskMeetingScheduleRegistrationReceipt{}, false, ErrTaskMeetingReceiptBad
		}
	}
	return latest, found, nil
}

func readTaskMeetingRegistrationReceiptTx(ctx context.Context, tx *sql.Tx, ownerUserID, registrationKey string, revision uint64) (TaskMeetingScheduleRegistrationReceipt, bool, error) {
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingRegistrationOperationID(ownerUserID, registrationKey, revision))
	if err != nil || !found {
		return TaskMeetingScheduleRegistrationReceipt{}, found, err
	}
	receipt, err := decodeTaskMeetingRegistrationReceipt(prior)
	if err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, false, err
	}
	if receipt.OwnerUserID != ownerUserID || receipt.RegistrationKey != registrationKey || receipt.Revision != revision {
		return TaskMeetingScheduleRegistrationReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := validateTaskMeetingRegistrationReceiptEvent(ctx, tx, receipt); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, false, err
	}
	return receipt, true, nil
}

func decodeTaskMeetingRegistrationReceipt(prior operationReceipt) (TaskMeetingScheduleRegistrationReceipt, error) {
	if prior.operationKind != taskMeetingRegistrationOperationKind {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	var receipt TaskMeetingScheduleRegistrationReceipt
	decoder := json.NewDecoder(strings.NewReader(prior.resultJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	declaration, digest, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(receipt.FrozenDeclaration)
	if err != nil || receipt.RegistrationKey != declaration.RegistrationKey || receipt.OwnerUserID != declaration.OwnerUserID || receipt.Revision != declaration.Revision || receipt.ConfigSHA256 != digest || prior.inputSHA256 != digest || receipt.ScheduleID.Validate() != nil || receipt.RegistrationEvidenceID.Validate() != nil || receipt.EventID.Validate() != nil || receipt.AcceptedAt.IsZero() {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(encoded, []byte(prior.resultJSON)) {
		return TaskMeetingScheduleRegistrationReceipt{}, ErrTaskMeetingReceiptBad
	}
	return receipt, nil
}

func validateTaskMeetingRegistrationReceiptEvent(ctx context.Context, tx *sql.Tx, receipt TaskMeetingScheduleRegistrationReceipt) error {
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT envelope_json FROM event_envelope WHERE event_id = ?`, string(receipt.EventID)).Scan(&encoded); err != nil {
		return ErrTaskMeetingReceiptBad
	}
	var event modulecore.EventEnvelope
	if json.Unmarshal([]byte(encoded), &event) != nil || modulecore.ValidateEventEnvelope(event) != nil || event.ComponentID != taskMeetingComponent || event.EventType != taskMeetingRegistrationEventType || event.ScheduleID != receipt.ScheduleID || event.EvidenceID != receipt.RegistrationEvidenceID || event.OccurredAt.UTC().UnixNano() != receipt.AcceptedAt.UTC().UnixNano() ||
		payloadString(event.Payload, "registration_key") != receipt.RegistrationKey || payloadString(event.Payload, "owner_user_id") != receipt.OwnerUserID || payloadString(event.Payload, "schedule_id") != string(receipt.ScheduleID) || payloadString(event.Payload, "registration_evidence_id") != string(receipt.RegistrationEvidenceID) || payloadString(event.Payload, "config_sha256") != receipt.ConfigSHA256 || payloadUint64Field(event.Payload, "revision") != receipt.Revision {
		return ErrTaskMeetingReceiptBad
	}
	return nil
}

func sameTaskMeetingDeclaration(left, right domainmeeting.TaskMeetingDeclaration) bool {
	leftBytes, leftHash, leftErr := domainmeeting.CanonicalTaskMeetingDeclarationJSON(left)
	rightBytes, rightHash, rightErr := domainmeeting.CanonicalTaskMeetingDeclarationJSON(right)
	return leftErr == nil && rightErr == nil && leftHash == rightHash && bytes.Equal(leftBytes, rightBytes)
}

func taskMeetingRegistrationOperationPrefix(ownerUserID, registrationKey string) string {
	material := ownerUserID + "\x00" + registrationKey
	digest := sha256.Sum256([]byte(material))
	return "taskmeeting.registration." + hex.EncodeToString(digest[:]) + ".r."
}

func taskMeetingRegistrationOperationID(ownerUserID, registrationKey string, revision uint64) string {
	return fmt.Sprintf("%s%020d", taskMeetingRegistrationOperationPrefix(ownerUserID, registrationKey), revision)
}

func hashOrderedStrings(values []string) string {
	encoded, _ := json.Marshal(values)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func hashJSONMust(value any) string {
	digest, err := hashJSON(value)
	if err != nil {
		return ""
	}
	return digest
}
