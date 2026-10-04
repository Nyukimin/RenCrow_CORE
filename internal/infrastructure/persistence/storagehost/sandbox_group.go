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

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	persistsandbox "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/sandbox"
)

const (
	GroupSandbox      = "sandbox"
	sandboxMaxPayload = 4 << 20
	sandboxMaxList    = 100
)
const (
	sandboxOpSave           = "save_sandbox"
	sandboxOpList           = "list_sandboxes"
	sandboxOpFind           = "find_sandbox"
	sandboxOpSaveArtifact   = "save_sandbox_artifact"
	sandboxOpListArtifacts  = "list_sandbox_artifacts"
	sandboxOpFindArtifact   = "find_sandbox_artifact"
	sandboxOpSavePromotion  = "save_promotion_request"
	sandboxOpListPromotions = "list_promotion_requests"
	sandboxOpFindPromotion  = "find_promotion_request"
	sandboxOpSaveGate       = "save_promotion_gate_log"
	sandboxOpListGates      = "list_promotion_gate_logs"
	sandboxOpFindGate       = "find_promotion_gate_log"
)

type SandboxGroupOwner interface {
	SaveSandbox(context.Context, domainsandbox.SandboxRecord) error
	ListSandboxes(context.Context, int) ([]domainsandbox.SandboxRecord, error)
	FindSandboxByID(context.Context, string) (domainsandbox.SandboxRecord, bool, error)
	SaveSandboxArtifact(context.Context, domainsandbox.SandboxArtifact) error
	ListSandboxArtifacts(context.Context, int) ([]domainsandbox.SandboxArtifact, error)
	FindSandboxArtifactByID(context.Context, string) (domainsandbox.SandboxArtifact, bool, error)
	SavePromotionRequest(context.Context, domainsandbox.PromotionRequest) error
	ListPromotionRequests(context.Context, int) ([]domainsandbox.PromotionRequest, error)
	FindPromotionRequestByID(context.Context, string) (domainsandbox.PromotionRequest, bool, error)
	SavePromotionGateLog(context.Context, domainsandbox.PromotionGateLog) error
	ListPromotionGateLogs(context.Context, int) ([]domainsandbox.PromotionGateLog, error)
	FindPromotionGateLogByID(context.Context, string) (domainsandbox.PromotionGateLog, bool, error)
}
type sandboxRecoveryOwner interface {
	SaveForStorageHostOperation(context.Context, persistsandbox.StorageHostOperationIdentity, persistsandbox.StorageHostSave) error
	LookupStorageHostOperationReceipt(context.Context, persistsandbox.StorageHostOperationIdentity, persistsandbox.StorageHostSave) (bool, error)
}
type sandboxItemPayload[T any] struct {
	Item T `json:"item"`
}
type sandboxListPayload struct {
	Limit int `json:"limit"`
}
type sandboxListResult[T any] struct {
	Items []T `json:"items"`
}
type sandboxFindPayload struct {
	ID string `json:"id"`
}
type sandboxFindResult[T any] struct {
	Found bool `json:"found"`
	Item  *T   `json:"item,omitempty"`
}

func RegisterSandboxGroup(handler *Handler, owner SandboxGroupOwner) error {
	if handler == nil || nilSandboxValue(owner) {
		return errors.New("storagehost: sandbox group needs a handler and owner")
	}
	recovery, ok := owner.(sandboxRecoveryOwner)
	if !ok || nilSandboxValue(recovery) {
		return errors.New("storagehost: sandbox owner lacks durable recovery")
	}
	regs := []func() error{
		func() error {
			return registerSandboxSave(handler, recovery, sandboxOpSave, domainsandbox.ValidateSandboxRecord, func(v domainsandbox.SandboxRecord) persistsandbox.StorageHostSave {
				return persistsandbox.StorageHostSave{Kind: persistsandbox.StorageHostSaveSandbox, Sandbox: &v}
			})
		},
		func() error {
			return registerSandboxList(handler, sandboxOpList, domainsandbox.ValidateSandboxRecord, owner.ListSandboxes)
		},
		func() error {
			return registerSandboxFind(handler, sandboxOpFind, domainsandbox.ValidateSandboxRecord, func(v domainsandbox.SandboxRecord) string { return v.SandboxID }, owner.FindSandboxByID)
		},
		func() error {
			return registerSandboxSave(handler, recovery, sandboxOpSaveArtifact, domainsandbox.ValidateSandboxArtifact, func(v domainsandbox.SandboxArtifact) persistsandbox.StorageHostSave {
				return persistsandbox.StorageHostSave{Kind: persistsandbox.StorageHostSaveArtifact, Artifact: &v}
			})
		},
		func() error {
			return registerSandboxList(handler, sandboxOpListArtifacts, domainsandbox.ValidateSandboxArtifact, owner.ListSandboxArtifacts)
		},
		func() error {
			return registerSandboxFind(handler, sandboxOpFindArtifact, domainsandbox.ValidateSandboxArtifact, func(v domainsandbox.SandboxArtifact) string { return v.ArtifactID }, owner.FindSandboxArtifactByID)
		},
		func() error {
			return registerSandboxSave(handler, recovery, sandboxOpSavePromotion, domainsandbox.ValidatePromotionRequest, func(v domainsandbox.PromotionRequest) persistsandbox.StorageHostSave {
				return persistsandbox.StorageHostSave{Kind: persistsandbox.StorageHostSavePromotion, Promotion: &v}
			})
		},
		func() error {
			return registerSandboxList(handler, sandboxOpListPromotions, domainsandbox.ValidatePromotionRequest, owner.ListPromotionRequests)
		},
		func() error {
			return registerSandboxFind(handler, sandboxOpFindPromotion, domainsandbox.ValidatePromotionRequest, func(v domainsandbox.PromotionRequest) string { return v.PromotionID }, owner.FindPromotionRequestByID)
		},
		func() error {
			return registerSandboxSave(handler, recovery, sandboxOpSaveGate, domainsandbox.ValidatePromotionGateLog, func(v domainsandbox.PromotionGateLog) persistsandbox.StorageHostSave {
				return persistsandbox.StorageHostSave{Kind: persistsandbox.StorageHostSaveGateLog, GateLog: &v}
			})
		},
		func() error {
			return registerSandboxList(handler, sandboxOpListGates, domainsandbox.ValidatePromotionGateLog, owner.ListPromotionGateLogs)
		},
		func() error {
			return registerSandboxFind(handler, sandboxOpFindGate, domainsandbox.ValidatePromotionGateLog, func(v domainsandbox.PromotionGateLog) string { return v.EventID }, owner.FindPromotionGateLogByID)
		},
	}
	for _, register := range regs {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
func registerSandboxSave[T any](h *Handler, owner sandboxRecoveryOwner, op string, validate func(T) error, wrap func(T) persistsandbox.StorageHostSave) error {
	return h.RegisterRecoverable(GroupSandbox, op, func(ctx context.Context, m MutationMetadata) (any, error) {
		var p sandboxItemPayload[T]
		if decodeSandboxPayload(m.Payload, &p) != nil || validate(p.Item) != nil || !boundedSandboxValue(p.Item) {
			return nil, ownerRolledBack(sandboxSchemaError(op))
		}
		if err := owner.SaveForStorageHostOperation(ctx, sandboxIdentity(m, op), wrap(p.Item)); err != nil {
			if errors.Is(err, persistsandbox.ErrSandboxStorageHostConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "sandbox operation conflicts with owner state"))
			}
			return nil, sandboxStoreError(op)
		}
		return nil, nil
	}, func(ctx context.Context, m MutationMetadata) (ReconcileDecision, error) {
		var p sandboxItemPayload[T]
		if decodeSandboxPayload(m.Payload, &p) != nil || validate(p.Item) != nil || !boundedSandboxValue(p.Item) {
			return UnknownOutcome(), nil
		}
		found, err := owner.LookupStorageHostOperationReceipt(ctx, sandboxIdentity(m, op), wrap(p.Item))
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(nil), nil
	})
}
func registerSandboxList[T any](h *Handler, op string, validate func(T) error, list func(context.Context, int) ([]T, error)) error {
	return h.Register(GroupSandbox, op, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p sandboxListPayload
		if decodeSandboxPayload(raw, &p) != nil || p.Limit < 0 || p.Limit > sandboxMaxList {
			return nil, sandboxSchemaError(op)
		}
		limit := p.Limit
		if limit == 0 {
			limit = 50
		}
		items, err := list(ctx, limit)
		if err != nil || len(items) > limit || !validSandboxItems(items, validate) {
			return nil, sandboxStoreError(op)
		}
		return sandboxListResult[T]{Items: items}, nil
	})
}
func registerSandboxFind[T any](h *Handler, op string, validate func(T) error, itemID func(T) string, find func(context.Context, string) (T, bool, error)) error {
	return h.Register(GroupSandbox, op, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p sandboxFindPayload
		if decodeSandboxPayload(raw, &p) != nil || !validSandboxID(p.ID) {
			return nil, sandboxSchemaError(op)
		}
		item, found, err := find(ctx, p.ID)
		if err != nil {
			return nil, sandboxStoreError(op)
		}
		if !found {
			return sandboxFindResult[T]{}, nil
		}
		if validate(item) != nil || itemID(item) != p.ID || !boundedSandboxValue(item) {
			return nil, sandboxStoreError(op)
		}
		return sandboxFindResult[T]{Found: true, Item: &item}, nil
	})
}
func sandboxIdentity(m MutationMetadata, op string) persistsandbox.StorageHostOperationIdentity {
	return persistsandbox.StorageHostOperationIdentity{OpID: m.OpID, PayloadSHA256: payloadHash(GroupSandbox, op, m.Payload), WriterGeneration: m.JournalGeneration}
}
func decodeSandboxPayload(raw json.RawMessage, dst any) error {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || len(b) > sandboxMaxPayload || !utf8.Valid(b) || b[0] != '{' {
		return errors.New("sandbox payload rejected")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return errors.New("sandbox payload rejected")
	}
	var trailing any
	if !errors.Is(d.Decode(&trailing), io.EOF) {
		return errors.New("sandbox payload rejected")
	}
	return nil
}
func boundedSandboxValue(v any) bool {
	b, e := json.Marshal(v)
	return e == nil && len(b) <= sandboxMaxPayload
}
func validSandboxItems[T any](v []T, validate func(T) error) bool {
	if len(v) > sandboxMaxList || !boundedSandboxValue(v) {
		return false
	}
	for _, x := range v {
		if validate(x) != nil {
			return false
		}
	}
	return true
}
func validSandboxID(v string) bool {
	return v != "" && len(v) <= 512 && utf8.ValidString(v) && strings.TrimSpace(v) == v && strings.IndexByte(v, 0) < 0
}
func sandboxSchemaError(op string) *Error {
	return NewError(ErrorCodeSchemaRejected, "sandbox "+op+" rejected")
}
func sandboxStoreError(op string) *Error {
	return NewError(ErrorCodeStoreUnavailable, "sandbox "+op+" failed")
}
func nilSandboxValue(v any) bool {
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

type SandboxClient struct{ client *Client }

func NewSandboxClient(c *Client) *SandboxClient { return &SandboxClient{client: c} }
func (s *SandboxClient) SaveSandbox(ctx context.Context, v domainsandbox.SandboxRecord) error {
	return callSandboxSave(s, ctx, sandboxOpSave, v, domainsandbox.ValidateSandboxRecord)
}
func (s *SandboxClient) ListSandboxes(ctx context.Context, n int) ([]domainsandbox.SandboxRecord, error) {
	return callSandboxList(s, ctx, sandboxOpList, n, domainsandbox.ValidateSandboxRecord)
}
func (s *SandboxClient) FindSandboxByID(ctx context.Context, id string) (domainsandbox.SandboxRecord, bool, error) {
	return callSandboxFind(s, ctx, sandboxOpFind, id, domainsandbox.ValidateSandboxRecord, func(v domainsandbox.SandboxRecord) string { return v.SandboxID })
}
func (s *SandboxClient) SaveSandboxArtifact(ctx context.Context, v domainsandbox.SandboxArtifact) error {
	return callSandboxSave(s, ctx, sandboxOpSaveArtifact, v, domainsandbox.ValidateSandboxArtifact)
}
func (s *SandboxClient) ListSandboxArtifacts(ctx context.Context, n int) ([]domainsandbox.SandboxArtifact, error) {
	return callSandboxList(s, ctx, sandboxOpListArtifacts, n, domainsandbox.ValidateSandboxArtifact)
}
func (s *SandboxClient) FindSandboxArtifactByID(ctx context.Context, id string) (domainsandbox.SandboxArtifact, bool, error) {
	return callSandboxFind(s, ctx, sandboxOpFindArtifact, id, domainsandbox.ValidateSandboxArtifact, func(v domainsandbox.SandboxArtifact) string { return v.ArtifactID })
}
func (s *SandboxClient) SavePromotionRequest(ctx context.Context, v domainsandbox.PromotionRequest) error {
	return callSandboxSave(s, ctx, sandboxOpSavePromotion, v, domainsandbox.ValidatePromotionRequest)
}
func (s *SandboxClient) ListPromotionRequests(ctx context.Context, n int) ([]domainsandbox.PromotionRequest, error) {
	return callSandboxList(s, ctx, sandboxOpListPromotions, n, domainsandbox.ValidatePromotionRequest)
}
func (s *SandboxClient) FindPromotionRequestByID(ctx context.Context, id string) (domainsandbox.PromotionRequest, bool, error) {
	return callSandboxFind(s, ctx, sandboxOpFindPromotion, id, domainsandbox.ValidatePromotionRequest, func(v domainsandbox.PromotionRequest) string { return v.PromotionID })
}
func (s *SandboxClient) SavePromotionGateLog(ctx context.Context, v domainsandbox.PromotionGateLog) error {
	return callSandboxSave(s, ctx, sandboxOpSaveGate, v, domainsandbox.ValidatePromotionGateLog)
}
func (s *SandboxClient) ListPromotionGateLogs(ctx context.Context, n int) ([]domainsandbox.PromotionGateLog, error) {
	return callSandboxList(s, ctx, sandboxOpListGates, n, domainsandbox.ValidatePromotionGateLog)
}
func (s *SandboxClient) FindPromotionGateLogByID(ctx context.Context, id string) (domainsandbox.PromotionGateLog, bool, error) {
	return callSandboxFind(s, ctx, sandboxOpFindGate, id, domainsandbox.ValidatePromotionGateLog, func(v domainsandbox.PromotionGateLog) string { return v.EventID })
}
func callSandboxSave[T any](s *SandboxClient, ctx context.Context, op string, v T, validate func(T) error) error {
	if validate(v) != nil || !boundedSandboxValue(v) {
		return sandboxSchemaError(op)
	}
	err := s.client.Call(ctx, GroupSandbox, op, sandboxItemPayload[T]{Item: v}, nil)
	var wire *Error
	if errors.As(err, &wire) && wire.Code == ErrorCodeDuplicateConflict {
		return persistsandbox.ErrSandboxStorageHostConflict
	}
	return err
}
func callSandboxList[T any](s *SandboxClient, ctx context.Context, op string, n int, validate func(T) error) ([]T, error) {
	if n < 0 || n > sandboxMaxList {
		return nil, sandboxSchemaError(op)
	}
	var r sandboxListResult[T]
	if err := s.client.Call(ctx, GroupSandbox, op, sandboxListPayload{Limit: n}, &r); err != nil {
		return nil, err
	}
	if !validSandboxItems(r.Items, validate) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "sandbox list response malformed")
	}
	return r.Items, nil
}
func callSandboxFind[T any](s *SandboxClient, ctx context.Context, op, id string, validate func(T) error, itemID func(T) string) (T, bool, error) {
	var z T
	if !validSandboxID(id) {
		return z, false, sandboxSchemaError(op)
	}
	var r sandboxFindResult[T]
	if err := s.client.Call(ctx, GroupSandbox, op, sandboxFindPayload{ID: id}, &r); err != nil {
		return z, false, err
	}
	if !r.Found {
		if r.Item != nil {
			return z, false, NewError(ErrorCodeOutcomeUnknown, "sandbox find response malformed")
		}
		return z, false, nil
	}
	if r.Item == nil || validate(*r.Item) != nil || itemID(*r.Item) != id || !boundedSandboxValue(*r.Item) {
		return z, false, NewError(ErrorCodeOutcomeUnknown, "sandbox find response malformed")
	}
	return *r.Item, true, nil
}

var _ SandboxGroupOwner = (*SandboxClient)(nil)
