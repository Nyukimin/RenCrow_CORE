package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	persistadvisor "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/advisor"
)

const (
	GroupAdvisor      = "advisor"
	advisorMaxPayload = 16 << 20
	advisorMaxList    = 100000
)

const (
	advisorOpSaveAdviceRun  = "save_advice_run"
	advisorOpListAdviceRuns = "list_advice_runs"
	advisorOpFindAdviceRun  = "find_advice_run"
	advisorOpSaveAdoption   = "save_advisor_adoption"
	advisorOpListAdoptions  = "list_advisor_adoptions"
	advisorOpFindAdoption   = "find_advisor_adoption"
	advisorOpSaveScore      = "save_advisor_score_snapshot"
	advisorOpListScores     = "list_advisor_score_snapshots"
	advisorOpSavePolicy     = "save_agent_policy_decision"
	advisorOpListPolicies   = "list_agent_policy_decisions"
)

type AdvisorGroupOwner interface {
	SaveAdviceRun(context.Context, domainadvisor.AdviceRunRecord) error
	ListAdviceRuns(context.Context, int) ([]domainadvisor.AdviceRunRecord, error)
	FindAdviceRunByID(context.Context, string) (domainadvisor.AdviceRunRecord, bool, error)
	SaveAdvisorAdoption(context.Context, domainadvisor.AdvisorAdoptionRecord) error
	ListAdvisorAdoptions(context.Context, int) ([]domainadvisor.AdvisorAdoptionRecord, error)
	FindAdvisorAdoptionByID(context.Context, string) (domainadvisor.AdvisorAdoptionRecord, bool, error)
	SaveAdvisorScoreSnapshot(context.Context, domainadvisor.AdvisorScoreSnapshot) error
	ListAdvisorScoreSnapshots(context.Context, int) ([]domainadvisor.AdvisorScoreSnapshot, error)
	SaveAgentPolicyDecision(context.Context, domainagentprofile.PolicyDecision) error
	ListAgentPolicyDecisions(context.Context, int) ([]domainagentprofile.PolicyDecision, error)
}

type advisorRecoveryOwner interface {
	SaveForStorageHostOperation(context.Context, persistadvisor.StorageHostOperationIdentity, persistadvisor.StorageHostSave) (persistadvisor.StorageHostSaveResult, error)
	LookupStorageHostOperationReceipt(context.Context, persistadvisor.StorageHostOperationIdentity, persistadvisor.StorageHostSave) (persistadvisor.StorageHostSaveResult, bool, error)
}

type advisorItemPayload[T any] struct {
	Item T `json:"item"`
}
type advisorListPayload struct {
	Limit int `json:"limit"`
}
type advisorListResult[T any] struct {
	Items []T `json:"items"`
}
type advisorFindPayload struct {
	ID string `json:"id"`
}
type advisorFindResult[T any] struct {
	Found bool `json:"found"`
	Item  *T   `json:"item,omitempty"`
}

func RegisterAdvisorGroup(handler *Handler, owner AdvisorGroupOwner) error {
	if handler == nil || nilAdvisorValue(owner) {
		return errors.New("storagehost: advisor group needs a handler and owner")
	}
	recovery, ok := owner.(advisorRecoveryOwner)
	if !ok || nilAdvisorValue(recovery) {
		return errors.New("storagehost: advisor owner lacks durable recovery")
	}
	registrations := []func() error{
		func() error {
			return registerAdvisorSave(handler, recovery, advisorOpSaveAdviceRun, func(v domainadvisor.AdviceRunRecord) error { return v.Validate() }, func(v domainadvisor.AdviceRunRecord) persistadvisor.StorageHostSave {
				return persistadvisor.StorageHostSave{Kind: persistadvisor.StorageHostSaveAdviceRun, AdviceRun: &v}
			})
		},
		func() error {
			return registerAdvisorList(handler, advisorOpListAdviceRuns, func(v domainadvisor.AdviceRunRecord) error { return v.Validate() }, owner.ListAdviceRuns)
		},
		func() error {
			return registerAdvisorFind(handler, advisorOpFindAdviceRun, func(v domainadvisor.AdviceRunRecord) error { return v.Validate() }, func(v domainadvisor.AdviceRunRecord) string { return v.RunID }, owner.FindAdviceRunByID)
		},
		func() error {
			return registerAdvisorSave(handler, recovery, advisorOpSaveAdoption, func(v domainadvisor.AdvisorAdoptionRecord) error { return v.Validate() }, func(v domainadvisor.AdvisorAdoptionRecord) persistadvisor.StorageHostSave {
				return persistadvisor.StorageHostSave{Kind: persistadvisor.StorageHostSaveAdoption, Adoption: &v}
			})
		},
		func() error {
			return registerAdvisorList(handler, advisorOpListAdoptions, func(v domainadvisor.AdvisorAdoptionRecord) error { return v.Validate() }, owner.ListAdvisorAdoptions)
		},
		func() error {
			return registerAdvisorFind(handler, advisorOpFindAdoption, func(v domainadvisor.AdvisorAdoptionRecord) error { return v.Validate() }, func(v domainadvisor.AdvisorAdoptionRecord) string { return v.AdoptionID }, owner.FindAdvisorAdoptionByID)
		},
		func() error {
			return registerAdvisorSave(handler, recovery, advisorOpSaveScore, func(v domainadvisor.AdvisorScoreSnapshot) error { return v.Validate() }, func(v domainadvisor.AdvisorScoreSnapshot) persistadvisor.StorageHostSave {
				return persistadvisor.StorageHostSave{Kind: persistadvisor.StorageHostSaveScoreSnapshot, ScoreSnapshot: &v}
			})
		},
		func() error {
			return registerAdvisorList(handler, advisorOpListScores, func(v domainadvisor.AdvisorScoreSnapshot) error { return v.Validate() }, owner.ListAdvisorScoreSnapshots)
		},
		func() error {
			return registerAdvisorSave(handler, recovery, advisorOpSavePolicy, func(v domainagentprofile.PolicyDecision) error { return v.Validate() }, func(v domainagentprofile.PolicyDecision) persistadvisor.StorageHostSave {
				return persistadvisor.StorageHostSave{Kind: persistadvisor.StorageHostSavePolicyDecision, PolicyDecision: &v}
			})
		},
		func() error {
			return registerAdvisorList(handler, advisorOpListPolicies, func(v domainagentprofile.PolicyDecision) error { return v.Validate() }, owner.ListAgentPolicyDecisions)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func registerAdvisorSave[T any](handler *Handler, owner advisorRecoveryOwner, operation string, validate func(T) error, wrap func(T) persistadvisor.StorageHostSave) error {
	return handler.RegisterRecoverable(GroupAdvisor, operation, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload advisorItemPayload[T]
		if decodeAdvisorPayload(mutation.Payload, &payload) != nil || validate(payload.Item) != nil || !boundedAdvisorValue(payload.Item) {
			return nil, ownerRolledBack(advisorSchemaError(operation))
		}
		result, err := owner.SaveForStorageHostOperation(ctx, advisorOperationIdentity(mutation, operation), wrap(payload.Item))
		if errors.Is(err, persistadvisor.ErrAdvisorStorageHostConflict) {
			return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "advisor operation conflicts with owner state"))
		}
		if err != nil {
			return nil, advisorStoreError(operation)
		}
		return result, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload advisorItemPayload[T]
		if decodeAdvisorPayload(mutation.Payload, &payload) != nil || validate(payload.Item) != nil || !boundedAdvisorValue(payload.Item) {
			return UnknownOutcome(), nil
		}
		result, found, err := owner.LookupStorageHostOperationReceipt(ctx, advisorOperationIdentity(mutation, operation), wrap(payload.Item))
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(result), nil
	})
}

func registerAdvisorList[T any](handler *Handler, operation string, validate func(T) error, list func(context.Context, int) ([]T, error)) error {
	return handler.Register(GroupAdvisor, operation, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload advisorListPayload
		if decodeAdvisorPayload(raw, &payload) != nil || payload.Limit < 0 || payload.Limit > advisorMaxList {
			return nil, advisorSchemaError(operation)
		}
		limit := payload.Limit
		if limit == 0 {
			limit = 50
		}
		items, err := list(ctx, limit)
		if err != nil || len(items) > limit || !validAdvisorItems(items, validate) {
			return nil, advisorStoreError(operation)
		}
		return advisorListResult[T]{Items: items}, nil
	})
}

func registerAdvisorFind[T any](handler *Handler, operation string, validate func(T) error, id func(T) string, find func(context.Context, string) (T, bool, error)) error {
	return handler.Register(GroupAdvisor, operation, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload advisorFindPayload
		if decodeAdvisorPayload(raw, &payload) != nil || !validAdvisorID(payload.ID) {
			return nil, advisorSchemaError(operation)
		}
		item, found, err := find(ctx, payload.ID)
		if err != nil {
			return nil, advisorStoreError(operation)
		}
		if !found {
			return advisorFindResult[T]{Found: false}, nil
		}
		if validate(item) != nil || id(item) != payload.ID || !boundedAdvisorValue(item) {
			return nil, advisorStoreError(operation)
		}
		return advisorFindResult[T]{Found: true, Item: &item}, nil
	})
}

func advisorOperationIdentity(mutation MutationMetadata, operation string) persistadvisor.StorageHostOperationIdentity {
	return persistadvisor.StorageHostOperationIdentity{OpID: mutation.OpID, PayloadSHA256: payloadHash(GroupAdvisor, operation, mutation.Payload), WriterGeneration: mutation.JournalGeneration}
}

func decodeAdvisorPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > advisorMaxPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("advisor payload rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("advisor payload rejected")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("advisor payload rejected")
	}
	return nil
}

func validAdvisorID(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "" && len(value) <= 512 && strings.IndexByte(value, 0) < 0
}
func boundedAdvisorValue(value any) bool {
	raw, err := json.Marshal(value)
	return err == nil && len(raw) <= advisorMaxPayload
}
func validAdvisorItems[T any](items []T, validate func(T) error) bool {
	if len(items) > advisorMaxList || !boundedAdvisorValue(items) {
		return false
	}
	for _, item := range items {
		if validate(item) != nil {
			return false
		}
	}
	return true
}
func advisorSchemaError(op string) *Error {
	return NewError(ErrorCodeSchemaRejected, "advisor "+op+" rejected")
}
func advisorStoreError(op string) *Error {
	return NewError(ErrorCodeStoreUnavailable, "advisor "+op+" failed")
}
func nilAdvisorValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

type AdvisorClient struct{ client *Client }

func NewAdvisorClient(client *Client) *AdvisorClient { return &AdvisorClient{client: client} }

func (s *AdvisorClient) SaveAdviceRun(ctx context.Context, item domainadvisor.AdviceRunRecord) error {
	_, err := callAdvisorSave(s, ctx, advisorOpSaveAdviceRun, item, func(v domainadvisor.AdviceRunRecord) error { return v.Validate() })
	return err
}
func (s *AdvisorClient) ListAdviceRuns(ctx context.Context, limit int) ([]domainadvisor.AdviceRunRecord, error) {
	return callAdvisorList(s, ctx, advisorOpListAdviceRuns, limit, func(v domainadvisor.AdviceRunRecord) error { return v.Validate() })
}
func (s *AdvisorClient) FindAdviceRunByID(ctx context.Context, id string) (domainadvisor.AdviceRunRecord, bool, error) {
	return callAdvisorFind(s, ctx, advisorOpFindAdviceRun, id, func(v domainadvisor.AdviceRunRecord) error { return v.Validate() }, func(v domainadvisor.AdviceRunRecord) string { return v.RunID })
}
func (s *AdvisorClient) SaveAdvisorAdoption(ctx context.Context, item domainadvisor.AdvisorAdoptionRecord) error {
	_, err := s.SaveAdvisorAdoptionWithReceipt(ctx, item)
	return err
}
func (s *AdvisorClient) SaveAdvisorAdoptionWithReceipt(ctx context.Context, item domainadvisor.AdvisorAdoptionRecord) (bool, error) {
	return callAdvisorSave(s, ctx, advisorOpSaveAdoption, item, func(v domainadvisor.AdvisorAdoptionRecord) error { return v.Validate() })
}
func (s *AdvisorClient) ListAdvisorAdoptions(ctx context.Context, limit int) ([]domainadvisor.AdvisorAdoptionRecord, error) {
	return callAdvisorList(s, ctx, advisorOpListAdoptions, limit, func(v domainadvisor.AdvisorAdoptionRecord) error { return v.Validate() })
}
func (s *AdvisorClient) FindAdvisorAdoptionByID(ctx context.Context, id string) (domainadvisor.AdvisorAdoptionRecord, bool, error) {
	return callAdvisorFind(s, ctx, advisorOpFindAdoption, id, func(v domainadvisor.AdvisorAdoptionRecord) error { return v.Validate() }, func(v domainadvisor.AdvisorAdoptionRecord) string { return v.AdoptionID })
}
func (s *AdvisorClient) SaveAdvisorScoreSnapshot(ctx context.Context, item domainadvisor.AdvisorScoreSnapshot) error {
	_, err := callAdvisorSave(s, ctx, advisorOpSaveScore, item, func(v domainadvisor.AdvisorScoreSnapshot) error { return v.Validate() })
	return err
}
func (s *AdvisorClient) ListAdvisorScoreSnapshots(ctx context.Context, limit int) ([]domainadvisor.AdvisorScoreSnapshot, error) {
	return callAdvisorList(s, ctx, advisorOpListScores, limit, func(v domainadvisor.AdvisorScoreSnapshot) error { return v.Validate() })
}
func (s *AdvisorClient) SaveAgentPolicyDecision(ctx context.Context, item domainagentprofile.PolicyDecision) error {
	_, err := callAdvisorSave(s, ctx, advisorOpSavePolicy, item, func(v domainagentprofile.PolicyDecision) error { return v.Validate() })
	return err
}
func (s *AdvisorClient) ListAgentPolicyDecisions(ctx context.Context, limit int) ([]domainagentprofile.PolicyDecision, error) {
	return callAdvisorList(s, ctx, advisorOpListPolicies, limit, func(v domainagentprofile.PolicyDecision) error { return v.Validate() })
}

func callAdvisorSave[T any](s *AdvisorClient, ctx context.Context, operation string, item T, validate func(T) error) (bool, error) {
	if validate(item) != nil || !boundedAdvisorValue(item) {
		return false, advisorSchemaError(operation)
	}
	var result persistadvisor.StorageHostSaveResult
	if err := s.client.Call(ctx, GroupAdvisor, operation, advisorItemPayload[T]{Item: item}, &result); err != nil {
		var wire *Error
		if errors.As(err, &wire) && wire.Code == ErrorCodeDuplicateConflict {
			return false, persistadvisor.ErrAdvisorStorageHostConflict
		}
		return false, err
	}
	return result.Replayed, nil
}
func callAdvisorList[T any](s *AdvisorClient, ctx context.Context, operation string, limit int, validate func(T) error) ([]T, error) {
	if limit < 0 || limit > advisorMaxList {
		return nil, advisorSchemaError(operation)
	}
	var result advisorListResult[T]
	if err := s.client.Call(ctx, GroupAdvisor, operation, advisorListPayload{Limit: limit}, &result); err != nil {
		return nil, err
	}
	if !validAdvisorItems(result.Items, validate) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "advisor list response malformed")
	}
	return result.Items, nil
}
func callAdvisorFind[T any](s *AdvisorClient, ctx context.Context, operation, idValue string, validate func(T) error, id func(T) string) (T, bool, error) {
	var zero T
	if !validAdvisorID(idValue) {
		return zero, false, advisorSchemaError(operation)
	}
	var result advisorFindResult[T]
	if err := s.client.Call(ctx, GroupAdvisor, operation, advisorFindPayload{ID: idValue}, &result); err != nil {
		return zero, false, err
	}
	if !result.Found {
		if result.Item != nil {
			return zero, false, NewError(ErrorCodeOutcomeUnknown, "advisor find response malformed")
		}
		return zero, false, nil
	}
	if result.Item == nil || validate(*result.Item) != nil || id(*result.Item) != idValue || !boundedAdvisorValue(*result.Item) {
		return zero, false, NewError(ErrorCodeOutcomeUnknown, "advisor find response malformed")
	}
	return *result.Item, true, nil
}

var _ AdvisorGroupOwner = (*AdvisorClient)(nil)
