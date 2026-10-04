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

	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	persistskill "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/skillgovernance"
)

const (
	GroupSkillGovernance      = "skill_governance"
	skillGovernanceMaxPayload = 16 << 20
	skillGovernanceMaxList    = 1000
)

const (
	skillOpSaveManifest      = "save_skill_manifest"
	skillOpListManifests     = "list_skill_manifests"
	skillOpSaveTrigger       = "save_skill_trigger_log"
	skillOpListTriggers      = "list_skill_trigger_logs"
	skillOpSaveChange        = "save_skill_change_log"
	skillOpListChanges       = "list_skill_change_logs"
	skillOpSaveContribution  = "save_contribution_gate_log"
	skillOpListContributions = "list_contribution_gate_logs"
	skillOpFindContribution  = "find_contribution_gate_by_id"
	skillOpSaveExternalPR    = "save_external_pr_submit_record"
	skillOpListExternalPRs   = "list_external_pr_submit_records"
	skillOpSaveTranscript    = "save_coder_transcript_entry"
	skillOpListTranscripts   = "list_coder_transcript_entries"
)

type SkillGovernanceGroupOwner interface {
	SaveSkillManifest(context.Context, domainskill.SkillManifest) error
	ListSkillManifests(context.Context, int) ([]domainskill.SkillManifest, error)
	SaveSkillTriggerLog(context.Context, domainskill.SkillTriggerLog) error
	ListSkillTriggerLogs(context.Context, int) ([]domainskill.SkillTriggerLog, error)
	SaveSkillChangeLog(context.Context, domainskill.SkillChangeLog) error
	ListSkillChangeLogs(context.Context, int) ([]domainskill.SkillChangeLog, error)
	SaveContributionGateLog(context.Context, domainskill.ContributionGateLog) error
	ListContributionGateLogs(context.Context, int) ([]domainskill.ContributionGateLog, error)
	FindContributionGateByID(context.Context, string) (domainskill.ContributionGateLog, bool, error)
	SaveExternalPRSubmitRecord(context.Context, domainskill.ExternalPRSubmitRecord) error
	ListExternalPRSubmitRecords(context.Context, int) ([]domainskill.ExternalPRSubmitRecord, error)
	SaveCoderTranscriptEntry(context.Context, domainskill.CoderTranscriptEntry) error
	ListCoderTranscriptEntries(context.Context, int) ([]domainskill.CoderTranscriptEntry, error)
}

type skillGovernanceRecoveryOwner interface {
	SaveForStorageHostOperation(context.Context, persistskill.StorageHostOperationIdentity, persistskill.StorageHostSave) error
	LookupStorageHostOperationReceipt(context.Context, persistskill.StorageHostOperationIdentity, persistskill.StorageHostSave) (bool, error)
}

type skillGovernanceItemPayload[T any] struct {
	Item T `json:"item"`
}
type skillGovernanceListPayload struct {
	Limit int `json:"limit"`
}
type skillGovernanceListResult[T any] struct {
	Items []T `json:"items"`
}
type skillGovernanceFindPayload struct {
	ID string `json:"id"`
}
type skillGovernanceFindResult[T any] struct {
	Found bool `json:"found"`
	Item  *T   `json:"item,omitempty"`
}

func RegisterSkillGovernanceGroup(handler *Handler, owner SkillGovernanceGroupOwner) error {
	if handler == nil || nilSkillGovernanceValue(owner) {
		return errors.New("storagehost: skill governance group needs a handler and owner")
	}
	recovery, ok := owner.(skillGovernanceRecoveryOwner)
	if !ok || nilSkillGovernanceValue(recovery) {
		return errors.New("storagehost: skill governance owner lacks durable recovery")
	}
	regs := []func() error{
		func() error {
			return registerSkillSave(handler, recovery, skillOpSaveManifest, domainskill.ValidateSkillManifest, func(v domainskill.SkillManifest) persistskill.StorageHostSave {
				return persistskill.StorageHostSave{Kind: persistskill.StorageHostSaveManifest, Manifest: &v}
			})
		},
		func() error {
			return registerSkillList(handler, skillOpListManifests, domainskill.ValidateSkillManifest, owner.ListSkillManifests)
		},
		func() error {
			return registerSkillSave(handler, recovery, skillOpSaveTrigger, domainskill.ValidateSkillTriggerLog, func(v domainskill.SkillTriggerLog) persistskill.StorageHostSave {
				return persistskill.StorageHostSave{Kind: persistskill.StorageHostSaveTrigger, Trigger: &v}
			})
		},
		func() error {
			return registerSkillList(handler, skillOpListTriggers, domainskill.ValidateSkillTriggerLog, owner.ListSkillTriggerLogs)
		},
		func() error {
			return registerSkillSave(handler, recovery, skillOpSaveChange, domainskill.ValidateSkillChangeLog, func(v domainskill.SkillChangeLog) persistskill.StorageHostSave {
				return persistskill.StorageHostSave{Kind: persistskill.StorageHostSaveChange, Change: &v}
			})
		},
		func() error {
			return registerSkillList(handler, skillOpListChanges, domainskill.ValidateSkillChangeLog, owner.ListSkillChangeLogs)
		},
		func() error {
			return registerSkillSave(handler, recovery, skillOpSaveContribution, domainskill.ValidateContributionGateLog, func(v domainskill.ContributionGateLog) persistskill.StorageHostSave {
				return persistskill.StorageHostSave{Kind: persistskill.StorageHostSaveContribution, Contribution: &v}
			})
		},
		func() error {
			return registerSkillList(handler, skillOpListContributions, domainskill.ValidateContributionGateLog, owner.ListContributionGateLogs)
		},
		func() error { return registerSkillContributionFind(handler, owner) },
		func() error {
			return registerSkillSave(handler, recovery, skillOpSaveExternalPR, domainskill.ValidateExternalPRSubmitRecord, func(v domainskill.ExternalPRSubmitRecord) persistskill.StorageHostSave {
				return persistskill.StorageHostSave{Kind: persistskill.StorageHostSaveExternalPR, ExternalPR: &v}
			})
		},
		func() error {
			return registerSkillList(handler, skillOpListExternalPRs, domainskill.ValidateExternalPRSubmitRecord, owner.ListExternalPRSubmitRecords)
		},
		func() error {
			return registerSkillSave(handler, recovery, skillOpSaveTranscript, domainskill.ValidateCoderTranscriptEntry, func(v domainskill.CoderTranscriptEntry) persistskill.StorageHostSave {
				return persistskill.StorageHostSave{Kind: persistskill.StorageHostSaveTranscript, Transcript: &v}
			})
		},
		func() error {
			return registerSkillList(handler, skillOpListTranscripts, domainskill.ValidateCoderTranscriptEntry, owner.ListCoderTranscriptEntries)
		},
	}
	for _, register := range regs {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func registerSkillSave[T any](h *Handler, owner skillGovernanceRecoveryOwner, op string, validate func(T) error, wrap func(T) persistskill.StorageHostSave) error {
	return h.RegisterRecoverable(GroupSkillGovernance, op, func(ctx context.Context, m MutationMetadata) (any, error) {
		var p skillGovernanceItemPayload[T]
		if decodeSkillGovernancePayload(m.Payload, &p) != nil || validate(p.Item) != nil || !boundedSkillGovernanceValue(p.Item) {
			return nil, ownerRolledBack(skillGovernanceSchemaError(op))
		}
		if err := owner.SaveForStorageHostOperation(ctx, skillGovernanceIdentity(m, op), wrap(p.Item)); err != nil {
			if errors.Is(err, persistskill.ErrSkillStorageHostConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "skill governance operation conflicts with owner state"))
			}
			return nil, skillGovernanceStoreError(op)
		}
		return nil, nil
	}, func(ctx context.Context, m MutationMetadata) (ReconcileDecision, error) {
		var p skillGovernanceItemPayload[T]
		if decodeSkillGovernancePayload(m.Payload, &p) != nil || validate(p.Item) != nil || !boundedSkillGovernanceValue(p.Item) {
			return UnknownOutcome(), nil
		}
		found, err := owner.LookupStorageHostOperationReceipt(ctx, skillGovernanceIdentity(m, op), wrap(p.Item))
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(nil), nil
	})
}

func registerSkillList[T any](h *Handler, op string, validate func(T) error, list func(context.Context, int) ([]T, error)) error {
	return h.Register(GroupSkillGovernance, op, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p skillGovernanceListPayload
		if decodeSkillGovernancePayload(raw, &p) != nil || p.Limit < 0 || p.Limit > skillGovernanceMaxList {
			return nil, skillGovernanceSchemaError(op)
		}
		limit := p.Limit
		if limit == 0 {
			limit = 50
		}
		items, err := list(ctx, limit)
		if err != nil || len(items) > limit || !validSkillGovernanceItems(items, validate) {
			return nil, skillGovernanceStoreError(op)
		}
		return skillGovernanceListResult[T]{Items: items}, nil
	})
}

func registerSkillContributionFind(h *Handler, owner SkillGovernanceGroupOwner) error {
	return h.Register(GroupSkillGovernance, skillOpFindContribution, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p skillGovernanceFindPayload
		if decodeSkillGovernancePayload(raw, &p) != nil || !validSkillGovernanceID(p.ID) {
			return nil, skillGovernanceSchemaError(skillOpFindContribution)
		}
		item, found, err := owner.FindContributionGateByID(ctx, p.ID)
		if err != nil {
			return nil, skillGovernanceStoreError(skillOpFindContribution)
		}
		if !found {
			return skillGovernanceFindResult[domainskill.ContributionGateLog]{}, nil
		}
		if item.EventID != p.ID || domainskill.ValidateContributionGateLog(item) != nil || !boundedSkillGovernanceValue(item) {
			return nil, skillGovernanceStoreError(skillOpFindContribution)
		}
		return skillGovernanceFindResult[domainskill.ContributionGateLog]{Found: true, Item: &item}, nil
	})
}

func skillGovernanceIdentity(m MutationMetadata, op string) persistskill.StorageHostOperationIdentity {
	return persistskill.StorageHostOperationIdentity{OpID: m.OpID, PayloadSHA256: payloadHash(GroupSkillGovernance, op, m.Payload), WriterGeneration: m.JournalGeneration}
}

func decodeSkillGovernancePayload(raw json.RawMessage, dst any) error {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || len(b) > skillGovernanceMaxPayload || !utf8.Valid(b) || b[0] != '{' {
		return errors.New("skill governance payload rejected")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return errors.New("skill governance payload rejected")
	}
	var trailing any
	if !errors.Is(d.Decode(&trailing), io.EOF) {
		return errors.New("skill governance payload rejected")
	}
	return nil
}

func validSkillGovernanceID(v string) bool {
	return v != "" && len(v) <= 512 && utf8.ValidString(v) && strings.TrimSpace(v) == v && strings.IndexByte(v, 0) < 0
}

func boundedSkillGovernanceValue(v any) bool {
	b, err := json.Marshal(v)
	return err == nil && len(b) <= skillGovernanceMaxPayload
}
func validSkillGovernanceItems[T any](items []T, validate func(T) error) bool {
	if len(items) > skillGovernanceMaxList || !boundedSkillGovernanceValue(items) {
		return false
	}
	for _, item := range items {
		if validate(item) != nil {
			return false
		}
	}
	return true
}
func skillGovernanceSchemaError(op string) *Error {
	return NewError(ErrorCodeSchemaRejected, "skill governance "+op+" rejected")
}
func skillGovernanceStoreError(op string) *Error {
	return NewError(ErrorCodeStoreUnavailable, "skill governance "+op+" failed")
}
func nilSkillGovernanceValue(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

type SkillGovernanceClient struct{ client *Client }

func NewSkillGovernanceClient(c *Client) *SkillGovernanceClient {
	return &SkillGovernanceClient{client: c}
}

func (c *SkillGovernanceClient) SaveSkillManifest(ctx context.Context, v domainskill.SkillManifest) error {
	return callSkillGovernanceSave(c, ctx, skillOpSaveManifest, v, domainskill.ValidateSkillManifest)
}
func (c *SkillGovernanceClient) ListSkillManifests(ctx context.Context, n int) ([]domainskill.SkillManifest, error) {
	return callSkillGovernanceList(c, ctx, skillOpListManifests, n, domainskill.ValidateSkillManifest)
}
func (c *SkillGovernanceClient) SaveSkillTriggerLog(ctx context.Context, v domainskill.SkillTriggerLog) error {
	return callSkillGovernanceSave(c, ctx, skillOpSaveTrigger, v, domainskill.ValidateSkillTriggerLog)
}
func (c *SkillGovernanceClient) ListSkillTriggerLogs(ctx context.Context, n int) ([]domainskill.SkillTriggerLog, error) {
	return callSkillGovernanceList(c, ctx, skillOpListTriggers, n, domainskill.ValidateSkillTriggerLog)
}
func (c *SkillGovernanceClient) SaveSkillChangeLog(ctx context.Context, v domainskill.SkillChangeLog) error {
	return callSkillGovernanceSave(c, ctx, skillOpSaveChange, v, domainskill.ValidateSkillChangeLog)
}
func (c *SkillGovernanceClient) ListSkillChangeLogs(ctx context.Context, n int) ([]domainskill.SkillChangeLog, error) {
	return callSkillGovernanceList(c, ctx, skillOpListChanges, n, domainskill.ValidateSkillChangeLog)
}
func (c *SkillGovernanceClient) SaveContributionGateLog(ctx context.Context, v domainskill.ContributionGateLog) error {
	return callSkillGovernanceSave(c, ctx, skillOpSaveContribution, v, domainskill.ValidateContributionGateLog)
}
func (c *SkillGovernanceClient) ListContributionGateLogs(ctx context.Context, n int) ([]domainskill.ContributionGateLog, error) {
	return callSkillGovernanceList(c, ctx, skillOpListContributions, n, domainskill.ValidateContributionGateLog)
}
func (c *SkillGovernanceClient) SaveExternalPRSubmitRecord(ctx context.Context, v domainskill.ExternalPRSubmitRecord) error {
	return callSkillGovernanceSave(c, ctx, skillOpSaveExternalPR, v, domainskill.ValidateExternalPRSubmitRecord)
}
func (c *SkillGovernanceClient) ListExternalPRSubmitRecords(ctx context.Context, n int) ([]domainskill.ExternalPRSubmitRecord, error) {
	return callSkillGovernanceList(c, ctx, skillOpListExternalPRs, n, domainskill.ValidateExternalPRSubmitRecord)
}
func (c *SkillGovernanceClient) SaveCoderTranscriptEntry(ctx context.Context, v domainskill.CoderTranscriptEntry) error {
	return callSkillGovernanceSave(c, ctx, skillOpSaveTranscript, v, domainskill.ValidateCoderTranscriptEntry)
}
func (c *SkillGovernanceClient) ListCoderTranscriptEntries(ctx context.Context, n int) ([]domainskill.CoderTranscriptEntry, error) {
	return callSkillGovernanceList(c, ctx, skillOpListTranscripts, n, domainskill.ValidateCoderTranscriptEntry)
}

func (c *SkillGovernanceClient) FindContributionGateByID(ctx context.Context, id string) (domainskill.ContributionGateLog, bool, error) {
	if !validSkillGovernanceID(id) {
		return domainskill.ContributionGateLog{}, false, skillGovernanceSchemaError(skillOpFindContribution)
	}
	var r skillGovernanceFindResult[domainskill.ContributionGateLog]
	if err := c.client.Call(ctx, GroupSkillGovernance, skillOpFindContribution, skillGovernanceFindPayload{ID: id}, &r); err != nil {
		return domainskill.ContributionGateLog{}, false, err
	}
	if !r.Found {
		if r.Item != nil {
			return domainskill.ContributionGateLog{}, false, NewError(ErrorCodeOutcomeUnknown, "skill governance find response malformed")
		}
		return domainskill.ContributionGateLog{}, false, nil
	}
	if r.Item == nil || r.Item.EventID != id || domainskill.ValidateContributionGateLog(*r.Item) != nil || !boundedSkillGovernanceValue(*r.Item) {
		return domainskill.ContributionGateLog{}, false, NewError(ErrorCodeOutcomeUnknown, "skill governance find response malformed")
	}
	return *r.Item, true, nil
}

func callSkillGovernanceSave[T any](c *SkillGovernanceClient, ctx context.Context, op string, v T, validate func(T) error) error {
	if validate(v) != nil || !boundedSkillGovernanceValue(v) {
		return skillGovernanceSchemaError(op)
	}
	err := c.client.Call(ctx, GroupSkillGovernance, op, skillGovernanceItemPayload[T]{Item: v}, nil)
	var wire *Error
	if errors.As(err, &wire) && wire.Code == ErrorCodeDuplicateConflict {
		return persistskill.ErrSkillStorageHostConflict
	}
	return err
}

func callSkillGovernanceList[T any](c *SkillGovernanceClient, ctx context.Context, op string, n int, validate func(T) error) ([]T, error) {
	if n < 0 || n > skillGovernanceMaxList {
		return nil, skillGovernanceSchemaError(op)
	}
	var r skillGovernanceListResult[T]
	if err := c.client.Call(ctx, GroupSkillGovernance, op, skillGovernanceListPayload{Limit: n}, &r); err != nil {
		return nil, err
	}
	limit := n
	if limit == 0 {
		limit = 50
	}
	if len(r.Items) > limit || !validSkillGovernanceItems(r.Items, validate) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "skill governance list response malformed")
	}
	return r.Items, nil
}

var _ SkillGovernanceGroupOwner = (*SkillGovernanceClient)(nil)
