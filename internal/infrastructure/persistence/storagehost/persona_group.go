package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	persistpersona "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/persona"
)

const GroupPersonaArchitecture = "persona_architecture"

const (
	personaListDefaultLimit = 50
	personaListMaxLimit     = 100
)

const (
	personaOpListDiscomfortLogs        = "list_discomfort_logs"
	personaOpListTriggerLogs           = "list_trigger_logs"
	personaOpListCanonicalResponseLogs = "list_canonical_response_logs"
	personaOpListObservationLogs       = "list_observation_logs"
	personaOpListMetaProfileUpdates    = "list_meta_profile_updates"
	personaOpListInterfaceSessions     = "list_interface_sessions"
	personaOpFindObservationLog        = "find_observation_log_by_id"
	personaOpSaveDiscomfortLog         = persistpersona.PersonaSaveDiscomfortLogStorageHostOperation
	personaOpSaveTriggerLog            = persistpersona.PersonaSaveTriggerLogStorageHostOperation
	personaOpSaveCanonicalResponseLog  = persistpersona.PersonaSaveCanonicalResponseLogStorageHostOperation
	personaOpSaveObservationLog        = persistpersona.PersonaSaveObservationLogStorageHostOperation
	personaOpSaveMetaProfileUpdate     = persistpersona.PersonaSaveMetaProfileUpdateStorageHostOperation
	personaOpSaveInterfaceSession      = persistpersona.PersonaSaveInterfaceSessionStorageHostOperation
)

type PersonaGroupOwner interface {
	SaveDiscomfortLogForStorageHostOperation(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.DiscomfortLog) (domainpersona.DiscomfortLog, error)
	LookupPersonaDiscomfortLogStorageHostReceipt(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.DiscomfortLog) (json.RawMessage, bool, error)
	SaveTriggerLogForStorageHostOperation(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.TriggerLog) (domainpersona.TriggerLog, error)
	LookupPersonaTriggerLogStorageHostReceipt(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.TriggerLog) (json.RawMessage, bool, error)
	SaveCanonicalResponseLogForStorageHostOperation(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.CanonicalResponseLog) (domainpersona.CanonicalResponseLog, error)
	LookupPersonaCanonicalResponseLogStorageHostReceipt(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.CanonicalResponseLog) (json.RawMessage, bool, error)
	SaveObservationLogForStorageHostOperation(context.Context, persistpersona.PersonaStorageHostOperationIdentity, persistpersona.PersonaObservationStorageHostBinding, domainpersona.ObservationLog) (domainpersona.ObservationLog, error)
	LookupPersonaObservationLogStorageHostReceipt(context.Context, persistpersona.PersonaStorageHostOperationIdentity, persistpersona.PersonaObservationStorageHostBinding, domainpersona.ObservationLog) (json.RawMessage, bool, error)
	SaveMetaProfileUpdateForStorageHostOperation(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.MetaProfileUpdate) (domainpersona.MetaProfileUpdate, error)
	LookupPersonaMetaProfileUpdateStorageHostReceipt(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.MetaProfileUpdate) (json.RawMessage, bool, error)
	SaveInterfaceSessionForStorageHostOperation(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.InterfaceSession) (domainpersona.InterfaceSession, error)
	LookupPersonaInterfaceSessionStorageHostReceipt(context.Context, persistpersona.PersonaStorageHostOperationIdentity, domainpersona.InterfaceSession) (json.RawMessage, bool, error)
	ListDiscomfortLogs(context.Context, int) ([]domainpersona.DiscomfortLog, error)
	ListTriggerLogs(context.Context, int) ([]domainpersona.TriggerLog, error)
	ListCanonicalResponseLogs(context.Context, int) ([]domainpersona.CanonicalResponseLog, error)
	ListObservationLogs(context.Context, int) ([]domainpersona.ObservationLog, error)
	ListMetaProfileUpdates(context.Context, int) ([]domainpersona.MetaProfileUpdate, error)
	ListInterfaceSessions(context.Context, int) ([]domainpersona.InterfaceSession, error)
	FindObservationLogByID(context.Context, string) (domainpersona.ObservationLog, bool, error)
}

type PersonaClient struct{ client *Client }

func NewPersonaClient(client *Client) *PersonaClient { return &PersonaClient{client: client} }

type personaListPayload struct {
	Limit int `json:"limit"`
}

type personaRecordPayload[T any] struct {
	Record T `json:"record"`
}

type personaObservationPayload struct {
	Binding persistpersona.PersonaObservationStorageHostBinding `json:"binding"`
	Record  domainpersona.ObservationLog                        `json:"record"`
}

type personaFindObservationPayload struct {
	EventID string `json:"event_id"`
}

type personaFindObservationResult struct {
	Observation domainpersona.ObservationLog `json:"observation"`
	Found       bool                         `json:"found"`
}

func RegisterPersonaGroup(handler *Handler, owner PersonaGroupOwner) error {
	if handler == nil || owner == nil {
		return errors.New("storagehost: Persona owner is required")
	}
	listRegistrations := []func() error{
		func() error {
			return registerPersonaList(handler, personaOpListDiscomfortLogs, owner.ListDiscomfortLogs)
		},
		func() error { return registerPersonaList(handler, personaOpListTriggerLogs, owner.ListTriggerLogs) },
		func() error {
			return registerPersonaList(handler, personaOpListCanonicalResponseLogs, owner.ListCanonicalResponseLogs)
		},
		func() error {
			return registerPersonaList(handler, personaOpListObservationLogs, owner.ListObservationLogs)
		},
		func() error {
			return registerPersonaList(handler, personaOpListMetaProfileUpdates, owner.ListMetaProfileUpdates)
		},
		func() error {
			return registerPersonaList(handler, personaOpListInterfaceSessions, owner.ListInterfaceSessions)
		},
	}
	for _, register := range listRegistrations {
		if err := register(); err != nil {
			return err
		}
	}
	if err := handler.Register(GroupPersonaArchitecture, personaOpFindObservationLog, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodePersonaPayload[personaFindObservationPayload](raw)
		if err != nil || strings.TrimSpace(payload.EventID) == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "Persona observation lookup request rejected")
		}
		item, found, err := owner.FindObservationLogByID(ctx, payload.EventID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "Persona observation lookup failed")
		}
		return personaFindObservationResult{Observation: item, Found: found}, nil
	}); err != nil {
		return err
	}
	saveRegistrations := []func() error{
		func() error {
			return registerPersonaSaveRecord(handler, personaOpSaveDiscomfortLog, domainpersona.ValidateDiscomfortLog,
				owner.SaveDiscomfortLogForStorageHostOperation, owner.LookupPersonaDiscomfortLogStorageHostReceipt)
		},
		func() error {
			return registerPersonaSaveRecord(handler, personaOpSaveTriggerLog, domainpersona.ValidateTriggerLog,
				owner.SaveTriggerLogForStorageHostOperation, owner.LookupPersonaTriggerLogStorageHostReceipt)
		},
		func() error {
			return registerPersonaSaveRecord(handler, personaOpSaveCanonicalResponseLog, domainpersona.ValidateCanonicalResponseLog,
				owner.SaveCanonicalResponseLogForStorageHostOperation, owner.LookupPersonaCanonicalResponseLogStorageHostReceipt)
		},
		func() error { return registerPersonaObservationSave(handler, owner) },
		func() error {
			return registerPersonaSaveRecord(handler, personaOpSaveMetaProfileUpdate, domainpersona.ValidateMetaProfileUpdate,
				owner.SaveMetaProfileUpdateForStorageHostOperation, owner.LookupPersonaMetaProfileUpdateStorageHostReceipt)
		},
		func() error {
			return registerPersonaSaveRecord(handler, personaOpSaveInterfaceSession, domainpersona.ValidateInterfaceSession,
				owner.SaveInterfaceSessionForStorageHostOperation, owner.LookupPersonaInterfaceSessionStorageHostReceipt)
		},
	}
	for _, register := range saveRegistrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func registerPersonaList[T any](handler *Handler, operation string, list func(context.Context, int) ([]T, error)) error {
	return handler.Register(GroupPersonaArchitecture, operation, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodePersonaPayload[personaListPayload](raw)
		if err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Persona list request rejected")
		}
		limit, err := normalizePersonaListLimit(payload.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Persona list request rejected")
		}
		items, err := list(ctx, limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "Persona list failed")
		}
		if len(items) > limit {
			return nil, NewError(ErrorCodeStoreUnavailable, "Persona owner exceeded the requested list bound")
		}
		return items, nil
	})
}

func normalizePersonaListLimit(limit int) (int, error) {
	if limit > personaListMaxLimit {
		return 0, errors.New("Persona list limit exceeds the supported maximum")
	}
	if limit <= 0 {
		return personaListDefaultLimit, nil
	}
	return limit, nil
}

func registerPersonaSaveRecord[T any](
	handler *Handler,
	operation string,
	validate func(T) error,
	save func(context.Context, persistpersona.PersonaStorageHostOperationIdentity, T) (T, error),
	lookup func(context.Context, persistpersona.PersonaStorageHostOperationIdentity, T) (json.RawMessage, bool, error),
) error {
	return handler.RegisterRecoverable(GroupPersonaArchitecture, operation, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodePersonaPayload[personaRecordPayload[T]](mutation.Payload)
		if err != nil || validate(payload.Record) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "Persona record request rejected"))
		}
		identity, err := personaOperationIdentity(mutation, operation)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "Persona operation identity rejected"))
		}
		stored, err := save(ctx, identity, payload.Record)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(stored, payload.Record) {
			return nil, errors.New("Persona owner returned a substituted record")
		}
		return stored, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodePersonaPayload[personaRecordPayload[T]](mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		identity, err := personaOperationIdentity(mutation, operation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		result, found, err := lookup(ctx, identity, payload.Record)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		var stored T
		if err := json.Unmarshal(result, &stored); err != nil || !reflect.DeepEqual(stored, payload.Record) {
			return UnknownOutcome(), nil
		}
		return Committed(stored), nil
	})
}

func registerPersonaObservationSave(handler *Handler, owner PersonaGroupOwner) error {
	return handler.RegisterRecoverable(GroupPersonaArchitecture, personaOpSaveObservationLog, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodePersonaPayload[personaObservationPayload](mutation.Payload)
		if err != nil || domainpersona.ValidateObservationLog(payload.Record) != nil || validatePersonaObservationRequestBinding(payload.Binding, payload.Record) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "Persona observation request rejected"))
		}
		identity, err := personaOperationIdentity(mutation, personaOpSaveObservationLog)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "Persona operation identity rejected"))
		}
		stored, err := owner.SaveObservationLogForStorageHostOperation(ctx, identity, payload.Binding, payload.Record)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(stored, payload.Record) {
			return nil, errors.New("Persona owner returned a substituted observation")
		}
		return stored, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodePersonaPayload[personaObservationPayload](mutation.Payload)
		if err != nil || validatePersonaObservationRequestBinding(payload.Binding, payload.Record) != nil {
			return UnknownOutcome(), nil
		}
		identity, err := personaOperationIdentity(mutation, personaOpSaveObservationLog)
		if err != nil {
			return UnknownOutcome(), nil
		}
		result, found, err := owner.LookupPersonaObservationLogStorageHostReceipt(ctx, identity, payload.Binding, payload.Record)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		var stored domainpersona.ObservationLog
		if err := json.Unmarshal(result, &stored); err != nil || !reflect.DeepEqual(stored, payload.Record) {
			return UnknownOutcome(), nil
		}
		return Committed(stored), nil
	})
}

func validatePersonaObservationRequestBinding(binding persistpersona.PersonaObservationStorageHostBinding, item domainpersona.ObservationLog) error {
	if strings.TrimSpace(binding.ActorObserverID) == "" || binding.ActorObserverID != item.ObserverID {
		return errors.New("Persona observation actor binding rejected")
	}
	if strings.TrimSpace(binding.AuthenticatedTargetUserID) == "" || binding.AuthenticatedTargetUserID != item.TargetID {
		return errors.New("Persona observation authenticated target binding rejected")
	}
	return nil
}

func personaOperationIdentity(mutation MutationMetadata, operation string) (persistpersona.PersonaStorageHostOperationIdentity, error) {
	if mutation.OpID == "" || len(mutation.Payload) == 0 || mutation.JournalGeneration <= 0 || mutation.RequestGeneration <= 0 {
		return persistpersona.PersonaStorageHostOperationIdentity{}, errors.New("Persona mutation identity is incomplete")
	}
	return persistpersona.PersonaStorageHostOperationIdentity{
		OpID: mutation.OpID, Operation: operation,
		PayloadSHA256: payloadHash(GroupPersonaArchitecture, operation, mutation.Payload), WriterGeneration: mutation.JournalGeneration,
	}, nil
}

func decodePersonaPayload[T any](raw json.RawMessage) (T, error) {
	var result T
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return result, errors.New("Persona payload must be one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return result, errors.New("Persona payload contains trailing JSON")
		}
		return result, err
	}
	return result, nil
}

func (client *PersonaClient) newSaveRecordOperation(requestID, operation string, item any) *Operation {
	call := client.client.NewOperation(GroupPersonaArchitecture, operation, personaRecordPayload[any]{Record: item})
	if requestID != "" {
		call.OpID = requestID
	}
	return call
}

func savePersonaRecordOperation[T any](ctx context.Context, client *Client, operation *Operation) (T, error) {
	var result T
	if err := client.Do(ctx, operation, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (client *PersonaClient) NewSaveDiscomfortLogOperation(requestID string, item domainpersona.DiscomfortLog) *Operation {
	return client.newSaveRecordOperation(requestID, personaOpSaveDiscomfortLog, item)
}

func (client *PersonaClient) SaveDiscomfortLog(ctx context.Context, requestID string, item domainpersona.DiscomfortLog) (domainpersona.DiscomfortLog, error) {
	return savePersonaRecordOperation[domainpersona.DiscomfortLog](ctx, client.client, client.NewSaveDiscomfortLogOperation(requestID, item))
}

func (client *PersonaClient) NewSaveTriggerLogOperation(requestID string, item domainpersona.TriggerLog) *Operation {
	return client.newSaveRecordOperation(requestID, personaOpSaveTriggerLog, item)
}

func (client *PersonaClient) SaveTriggerLog(ctx context.Context, requestID string, item domainpersona.TriggerLog) (domainpersona.TriggerLog, error) {
	return savePersonaRecordOperation[domainpersona.TriggerLog](ctx, client.client, client.NewSaveTriggerLogOperation(requestID, item))
}

func (client *PersonaClient) NewSaveCanonicalResponseLogOperation(requestID string, item domainpersona.CanonicalResponseLog) *Operation {
	return client.newSaveRecordOperation(requestID, personaOpSaveCanonicalResponseLog, item)
}

func (client *PersonaClient) SaveCanonicalResponseLog(ctx context.Context, requestID string, item domainpersona.CanonicalResponseLog) (domainpersona.CanonicalResponseLog, error) {
	return savePersonaRecordOperation[domainpersona.CanonicalResponseLog](ctx, client.client, client.NewSaveCanonicalResponseLogOperation(requestID, item))
}

func (client *PersonaClient) NewSaveObservationLogOperation(requestID string, binding persistpersona.PersonaObservationStorageHostBinding, item domainpersona.ObservationLog) *Operation {
	call := client.client.NewOperation(GroupPersonaArchitecture, personaOpSaveObservationLog, personaObservationPayload{Binding: binding, Record: item})
	if requestID != "" {
		call.OpID = requestID
	}
	return call
}

func (client *PersonaClient) SaveObservationLog(ctx context.Context, requestID string, binding persistpersona.PersonaObservationStorageHostBinding, item domainpersona.ObservationLog) (domainpersona.ObservationLog, error) {
	return savePersonaRecordOperation[domainpersona.ObservationLog](ctx, client.client, client.NewSaveObservationLogOperation(requestID, binding, item))
}

func (client *PersonaClient) NewSaveMetaProfileUpdateOperation(requestID string, item domainpersona.MetaProfileUpdate) *Operation {
	return client.newSaveRecordOperation(requestID, personaOpSaveMetaProfileUpdate, item)
}

func (client *PersonaClient) SaveMetaProfileUpdate(ctx context.Context, requestID string, item domainpersona.MetaProfileUpdate) (domainpersona.MetaProfileUpdate, error) {
	return savePersonaRecordOperation[domainpersona.MetaProfileUpdate](ctx, client.client, client.NewSaveMetaProfileUpdateOperation(requestID, item))
}

func (client *PersonaClient) NewSaveInterfaceSessionOperation(requestID string, item domainpersona.InterfaceSession) *Operation {
	return client.newSaveRecordOperation(requestID, personaOpSaveInterfaceSession, item)
}

func (client *PersonaClient) SaveInterfaceSession(ctx context.Context, requestID string, item domainpersona.InterfaceSession) (domainpersona.InterfaceSession, error) {
	return savePersonaRecordOperation[domainpersona.InterfaceSession](ctx, client.client, client.NewSaveInterfaceSessionOperation(requestID, item))
}

func (client *PersonaClient) FindObservationLogByID(ctx context.Context, eventID string) (domainpersona.ObservationLog, bool, error) {
	var result personaFindObservationResult
	if err := client.client.Call(ctx, GroupPersonaArchitecture, personaOpFindObservationLog, personaFindObservationPayload{EventID: eventID}, &result); err != nil {
		return domainpersona.ObservationLog{}, false, err
	}
	return result.Observation, result.Found, nil
}

func (client *PersonaClient) ListDiscomfortLogs(ctx context.Context, limit int) ([]domainpersona.DiscomfortLog, error) {
	return personaListCall[domainpersona.DiscomfortLog](ctx, client.client, personaOpListDiscomfortLogs, limit)
}

func (client *PersonaClient) ListTriggerLogs(ctx context.Context, limit int) ([]domainpersona.TriggerLog, error) {
	return personaListCall[domainpersona.TriggerLog](ctx, client.client, personaOpListTriggerLogs, limit)
}

func (client *PersonaClient) ListCanonicalResponseLogs(ctx context.Context, limit int) ([]domainpersona.CanonicalResponseLog, error) {
	return personaListCall[domainpersona.CanonicalResponseLog](ctx, client.client, personaOpListCanonicalResponseLogs, limit)
}

func (client *PersonaClient) ListObservationLogs(ctx context.Context, limit int) ([]domainpersona.ObservationLog, error) {
	return personaListCall[domainpersona.ObservationLog](ctx, client.client, personaOpListObservationLogs, limit)
}

func (client *PersonaClient) ListMetaProfileUpdates(ctx context.Context, limit int) ([]domainpersona.MetaProfileUpdate, error) {
	return personaListCall[domainpersona.MetaProfileUpdate](ctx, client.client, personaOpListMetaProfileUpdates, limit)
}

func (client *PersonaClient) ListInterfaceSessions(ctx context.Context, limit int) ([]domainpersona.InterfaceSession, error) {
	return personaListCall[domainpersona.InterfaceSession](ctx, client.client, personaOpListInterfaceSessions, limit)
}

func personaListCall[T any](ctx context.Context, client *Client, operation string, limit int) ([]T, error) {
	effectiveLimit, err := normalizePersonaListLimit(limit)
	if err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "Persona list limit rejected")
	}
	var items []T
	if err := client.Call(ctx, GroupPersonaArchitecture, operation, personaListPayload{Limit: effectiveLimit}, &items); err != nil {
		return nil, err
	}
	if len(items) > effectiveLimit {
		return nil, NewError(ErrorCodeStoreUnavailable, "Persona storage host exceeded the requested list bound")
	}
	return items, nil
}
