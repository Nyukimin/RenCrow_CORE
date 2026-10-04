package persona

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

const (
	PersonaSaveDiscomfortLogStorageHostOperation        = "save_discomfort_log"
	PersonaSaveTriggerLogStorageHostOperation           = "save_trigger_log"
	PersonaSaveCanonicalResponseLogStorageHostOperation = "save_canonical_response_log"
	PersonaSaveObservationLogStorageHostOperation       = "save_observation_log"
	PersonaSaveMetaProfileUpdateStorageHostOperation    = "save_meta_profile_update"
	PersonaSaveInterfaceSessionStorageHostOperation     = "save_interface_session"
	personaStorageHostReceiptTable                      = "persona_storagehost_operation_receipt"
)

var personaStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type PersonaStorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

type PersonaObservationStorageHostBinding struct {
	ActorObserverID           string
	AuthenticatedTargetUserID string
}

func (identity PersonaStorageHostOperationIdentity) validate() error {
	if !personaStorageHostOpIDPattern.MatchString(identity.OpID) || len(identity.PayloadSHA256) != sha256.Size*2 || identity.WriterGeneration <= 0 {
		return errors.New("persona storage-host operation identity is incomplete")
	}
	if !personaStorageHostOperationAllowed(identity.Operation) {
		return errors.New("persona storage-host operation is unsupported")
	}
	for _, char := range identity.PayloadSHA256 {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return errors.New("persona storage-host payload hash is malformed")
		}
	}
	return nil
}

func personaStorageHostOperationAllowed(operation string) bool {
	switch operation {
	case PersonaSaveDiscomfortLogStorageHostOperation,
		PersonaSaveTriggerLogStorageHostOperation,
		PersonaSaveCanonicalResponseLogStorageHostOperation,
		PersonaSaveObservationLogStorageHostOperation,
		PersonaSaveMetaProfileUpdateStorageHostOperation,
		PersonaSaveInterfaceSessionStorageHostOperation:
		return true
	default:
		return false
	}
}

func personaStorageHostOperationForEffect(table, idColumn string) (string, bool) {
	switch table + "." + idColumn {
	case "persona_discomfort_log.event_id":
		return PersonaSaveDiscomfortLogStorageHostOperation, true
	case "persona_trigger_log.event_id":
		return PersonaSaveTriggerLogStorageHostOperation, true
	case "canonical_response_log.event_id":
		return PersonaSaveCanonicalResponseLogStorageHostOperation, true
	case "observation_log.event_id":
		return PersonaSaveObservationLogStorageHostOperation, true
	case "meta_profile_update.update_id":
		return PersonaSaveMetaProfileUpdateStorageHostOperation, true
	case "persona_interface_session.session_id":
		return PersonaSaveInterfaceSessionStorageHostOperation, true
	default:
		return "", false
	}
}

func savePersonaStorageHostRecord[T any](
	s *SQLiteStore,
	ctx context.Context,
	identity PersonaStorageHostOperationIdentity,
	table, idColumn, effectID, createdAt, query string,
	args []any,
	item T,
) (T, error) {
	var zero T
	encoded, err := s.savePersonaStorageHostMutation(ctx, identity, table, idColumn, effectID, query, args, item)
	if err != nil {
		return zero, err
	}
	var stored T
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return zero, errors.New("persona storage-host owner result is malformed")
	}
	return stored, nil
}

func (s *SQLiteStore) SaveDiscomfortLogForStorageHostOperation(ctx context.Context, identity PersonaStorageHostOperationIdentity, item domainpersona.DiscomfortLog) (domainpersona.DiscomfortLog, error) {
	if err := domainpersona.ValidateDiscomfortLog(item); err != nil {
		return domainpersona.DiscomfortLog{}, err
	}
	return savePersonaStorageHostRecord(s, ctx, identity, "persona_discomfort_log", "event_id", item.EventID, item.CreatedAt.Format(timeFormatRFC3339Nano),
		`INSERT OR REPLACE INTO persona_discomfort_log (event_id, character_id, status, created_at, payload) VALUES (?, ?, ?, ?, ?)`,
		[]any{item.EventID, item.CharacterID, item.Status, item.CreatedAt.Format(timeFormatRFC3339Nano), item}, item)
}

func (s *SQLiteStore) LookupPersonaDiscomfortLogStorageHostReceipt(ctx context.Context, identity PersonaStorageHostOperationIdentity, expected domainpersona.DiscomfortLog) (json.RawMessage, bool, error) {
	if err := domainpersona.ValidateDiscomfortLog(expected); err != nil {
		return nil, false, err
	}
	return s.lookupPersonaStorageHostMutation(ctx, identity, "persona_discomfort_log", "event_id", expected.EventID, expected)
}

func (s *SQLiteStore) SaveTriggerLogForStorageHostOperation(ctx context.Context, identity PersonaStorageHostOperationIdentity, item domainpersona.TriggerLog) (domainpersona.TriggerLog, error) {
	if err := domainpersona.ValidateTriggerLog(item); err != nil {
		return domainpersona.TriggerLog{}, err
	}
	return savePersonaStorageHostRecord(s, ctx, identity, "persona_trigger_log", "event_id", item.EventID, item.CreatedAt.Format(timeFormatRFC3339Nano),
		`INSERT OR REPLACE INTO persona_trigger_log (event_id, character_id, trigger_id, trigger_category, created_at, payload) VALUES (?, ?, ?, ?, ?, ?)`,
		[]any{item.EventID, item.CharacterID, item.TriggerID, item.TriggerCategory, item.CreatedAt.Format(timeFormatRFC3339Nano), item}, item)
}

func (s *SQLiteStore) LookupPersonaTriggerLogStorageHostReceipt(ctx context.Context, identity PersonaStorageHostOperationIdentity, expected domainpersona.TriggerLog) (json.RawMessage, bool, error) {
	if err := domainpersona.ValidateTriggerLog(expected); err != nil {
		return nil, false, err
	}
	return s.lookupPersonaStorageHostMutation(ctx, identity, "persona_trigger_log", "event_id", expected.EventID, expected)
}

func (s *SQLiteStore) SaveCanonicalResponseLogForStorageHostOperation(ctx context.Context, identity PersonaStorageHostOperationIdentity, item domainpersona.CanonicalResponseLog) (domainpersona.CanonicalResponseLog, error) {
	if err := domainpersona.ValidateCanonicalResponseLog(item); err != nil {
		return domainpersona.CanonicalResponseLog{}, err
	}
	return savePersonaStorageHostRecord(s, ctx, identity, "canonical_response_log", "event_id", item.EventID, item.CreatedAt.Format(timeFormatRFC3339Nano),
		`INSERT OR REPLACE INTO canonical_response_log (event_id, character_id, response_key, message_id, created_at, payload) VALUES (?, ?, ?, ?, ?, ?)`,
		[]any{item.EventID, item.CharacterID, item.ResponseKey, item.MessageID, item.CreatedAt.Format(timeFormatRFC3339Nano), item}, item)
}

func (s *SQLiteStore) LookupPersonaCanonicalResponseLogStorageHostReceipt(ctx context.Context, identity PersonaStorageHostOperationIdentity, expected domainpersona.CanonicalResponseLog) (json.RawMessage, bool, error) {
	if err := domainpersona.ValidateCanonicalResponseLog(expected); err != nil {
		return nil, false, err
	}
	return s.lookupPersonaStorageHostMutation(ctx, identity, "canonical_response_log", "event_id", expected.EventID, expected)
}

func validatePersonaObservationBinding(binding PersonaObservationStorageHostBinding, item domainpersona.ObservationLog) error {
	if strings.TrimSpace(binding.ActorObserverID) == "" || binding.ActorObserverID != item.ObserverID {
		return errors.New("persona observation actor does not match observer identity")
	}
	if strings.TrimSpace(binding.AuthenticatedTargetUserID) == "" || binding.AuthenticatedTargetUserID != item.TargetID {
		return errors.New("persona observation target does not match authenticated user")
	}
	return nil
}

func (s *SQLiteStore) SaveObservationLogForStorageHostOperation(ctx context.Context, identity PersonaStorageHostOperationIdentity, binding PersonaObservationStorageHostBinding, item domainpersona.ObservationLog) (domainpersona.ObservationLog, error) {
	if err := domainpersona.ValidateObservationLog(item); err != nil {
		return domainpersona.ObservationLog{}, err
	}
	if err := validatePersonaObservationBinding(binding, item); err != nil {
		return domainpersona.ObservationLog{}, err
	}
	return savePersonaStorageHostRecord(s, ctx, identity, "observation_log", "event_id", item.EventID, item.CreatedAt.Format(timeFormatRFC3339Nano),
		`INSERT OR REPLACE INTO observation_log (event_id, observer_id, target_id, observation_type, sensitivity, review_status, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		[]any{item.EventID, item.ObserverID, item.TargetID, item.ObservationType, item.Sensitivity, item.ReviewStatus, item.CreatedAt.Format(timeFormatRFC3339Nano), item}, item)
}

func (s *SQLiteStore) LookupPersonaObservationLogStorageHostReceipt(ctx context.Context, identity PersonaStorageHostOperationIdentity, binding PersonaObservationStorageHostBinding, expected domainpersona.ObservationLog) (json.RawMessage, bool, error) {
	if err := domainpersona.ValidateObservationLog(expected); err != nil {
		return nil, false, err
	}
	if err := validatePersonaObservationBinding(binding, expected); err != nil {
		return nil, false, err
	}
	return s.lookupPersonaStorageHostMutation(ctx, identity, "observation_log", "event_id", expected.EventID, expected)
}

func (s *SQLiteStore) SaveMetaProfileUpdateForStorageHostOperation(ctx context.Context, identity PersonaStorageHostOperationIdentity, item domainpersona.MetaProfileUpdate) (domainpersona.MetaProfileUpdate, error) {
	if err := domainpersona.ValidateMetaProfileUpdate(item); err != nil {
		return domainpersona.MetaProfileUpdate{}, err
	}
	return savePersonaStorageHostRecord(s, ctx, identity, "meta_profile_update", "update_id", item.UpdateID, item.CreatedAt.Format(timeFormatRFC3339Nano),
		`INSERT OR REPLACE INTO meta_profile_update (update_id, observer_id, target_id, section, sensitivity, review_status, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		[]any{item.UpdateID, item.ObserverID, item.TargetID, item.Section, item.Sensitivity, item.ReviewStatus, item.CreatedAt.Format(timeFormatRFC3339Nano), item}, item)
}

func (s *SQLiteStore) LookupPersonaMetaProfileUpdateStorageHostReceipt(ctx context.Context, identity PersonaStorageHostOperationIdentity, expected domainpersona.MetaProfileUpdate) (json.RawMessage, bool, error) {
	if err := domainpersona.ValidateMetaProfileUpdate(expected); err != nil {
		return nil, false, err
	}
	return s.lookupPersonaStorageHostMutation(ctx, identity, "meta_profile_update", "update_id", expected.UpdateID, expected)
}

func (s *SQLiteStore) SaveInterfaceSessionForStorageHostOperation(ctx context.Context, identity PersonaStorageHostOperationIdentity, item domainpersona.InterfaceSession) (domainpersona.InterfaceSession, error) {
	if err := domainpersona.ValidateInterfaceSession(item); err != nil {
		return domainpersona.InterfaceSession{}, err
	}
	return savePersonaStorageHostRecord(s, ctx, identity, "persona_interface_session", "session_id", item.SessionID, item.CreatedAt.Format(timeFormatRFC3339Nano),
		`INSERT OR REPLACE INTO persona_interface_session (session_id, character_id, interface_type, session_key, workstream_id, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		[]any{item.SessionID, item.CharacterID, item.InterfaceType, item.SessionKey, item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item}, item)
}

func (s *SQLiteStore) LookupPersonaInterfaceSessionStorageHostReceipt(ctx context.Context, identity PersonaStorageHostOperationIdentity, expected domainpersona.InterfaceSession) (json.RawMessage, bool, error) {
	if err := domainpersona.ValidateInterfaceSession(expected); err != nil {
		return nil, false, err
	}
	return s.lookupPersonaStorageHostMutation(ctx, identity, "persona_interface_session", "session_id", expected.SessionID, expected)
}

func (s *SQLiteStore) savePersonaStorageHostMutation(ctx context.Context, identity PersonaStorageHostOperationIdentity, table, idColumn, effectID, query string, args []any, item any) (json.RawMessage, error) {
	if err := identity.validate(); err != nil {
		return nil, err
	}
	if operation, ok := personaStorageHostOperationForEffect(table, idColumn); !ok || identity.Operation != operation {
		return nil, errors.New("persona storage-host operation/effect binding is invalid")
	}
	if s == nil || s.db == nil {
		return nil, errors.New("persona sqlite store is closed")
	}
	if len(args) == 0 {
		return nil, errors.New("persona storage-host save requires canonical row arguments")
	}
	resultJSON, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	resultHash := fmt.Sprintf("%x", sha256.Sum256(resultJSON))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if prior, found, err := readPersonaStorageHostReceipt(ctx, tx, identity, table, idColumn, effectID, resultJSON); err != nil {
		return nil, err
	} else if found {
		_ = tx.Rollback()
		return prior, nil
	}
	queryArgs := append([]any(nil), args...)
	queryArgs[len(queryArgs)-1] = string(resultJSON)
	if _, err := tx.ExecContext(ctx, query, queryArgs...); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+personaStorageHostReceiptTable+` (op_id, operation, payload_sha256, writer_generation, effect_table, effect_id, result_json, result_sha256) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, table, effectID, string(resultJSON), resultHash); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return resultJSON, nil
}

type personaStorageHostQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readPersonaStorageHostReceipt(ctx context.Context, queryer personaStorageHostQueryer, identity PersonaStorageHostOperationIdentity, table, idColumn, effectID string, expectedJSON []byte) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	if operation, ok := personaStorageHostOperationForEffect(table, idColumn); !ok || identity.Operation != operation {
		return nil, false, errors.New("persona storage-host operation/effect binding is invalid")
	}
	var operation, payloadHash, effectTable, storedEffectID, resultJSON, resultHash string
	var generation int64
	err := queryer.QueryRowContext(ctx, `SELECT operation, payload_sha256, writer_generation, effect_table, effect_id, result_json, result_sha256 FROM `+personaStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID).
		Scan(&operation, &payloadHash, &generation, &effectTable, &storedEffectID, &resultJSON, &resultHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if operation != identity.Operation || payloadHash != identity.PayloadSHA256 || generation != identity.WriterGeneration || effectTable != table || storedEffectID != effectID {
		return nil, false, errors.New("persona storage-host receipt binding mismatch")
	}
	encoded := []byte(resultJSON)
	if !json.Valid(encoded) || fmt.Sprintf("%x", sha256.Sum256(encoded)) != resultHash || !bytes.Equal(encoded, expectedJSON) {
		return nil, false, errors.New("persona storage-host receipt result proof mismatch")
	}
	return json.RawMessage(append([]byte(nil), encoded...)), true, nil
}

func (s *SQLiteStore) lookupPersonaStorageHostMutation(ctx context.Context, identity PersonaStorageHostOperationIdentity, table, idColumn, effectID string, expected any) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	if operation, ok := personaStorageHostOperationForEffect(table, idColumn); !ok || identity.Operation != operation {
		return nil, false, errors.New("persona storage-host operation/effect binding is invalid")
	}
	if s == nil || s.db == nil {
		return nil, false, errors.New("persona sqlite store is closed")
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return nil, false, err
	}
	result, found, err := readPersonaStorageHostReceipt(ctx, s.db, identity, table, idColumn, effectID, expectedJSON)
	if err != nil || found {
		return result, found, err
	}
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE `+idColumn+` = ?`, effectID).Scan(&exists)
	if err == nil {
		return nil, false, errors.New("persona canonical effect exists without its operation receipt")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	return nil, false, nil
}
