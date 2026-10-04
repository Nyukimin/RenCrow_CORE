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

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	persistdci "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	GroupDCI      = "dci"
	dciMaxPayload = 16 << 20
	dciMaxList    = 100
)

const (
	dciOpSaveTrace        = "save_search_trace"
	dciOpSaveResult       = "save_search_result"
	dciOpFindTraceAction  = "find_search_trace_by_action_id"
	dciOpFindResultAction = "find_search_result_by_action_id"
	dciOpFindTraceDedupe  = "find_search_trace_by_idempotency_key"
	dciOpFindResultDedupe = "find_search_result_by_idempotency_key"
	dciOpListRecent       = "list_recent"
)

type DCIGroupOwner interface {
	SaveSearchTrace(context.Context, domaindci.SearchTrace) error
	SaveSearchResult(context.Context, domaindci.SearchResult) error
	FindSearchTraceByActionID(context.Context, modulecore.ActionID) (domaindci.SearchTrace, bool, error)
	FindSearchResultByActionID(context.Context, modulecore.ActionID) (domaindci.SearchResult, bool, error)
	FindSearchTraceByIdempotencyKey(context.Context, string) (domaindci.SearchTrace, bool, error)
	FindSearchResultByIdempotencyKey(context.Context, string) (domaindci.SearchResult, bool, error)
	ListRecent(int) ([]domaindci.SearchTrace, error)
}

type dciRecoveryOwner interface {
	SaveForStorageHostOperation(context.Context, persistdci.StorageHostOperationIdentity, persistdci.StorageHostSave) error
	LookupStorageHostOperationReceipt(context.Context, persistdci.StorageHostOperationIdentity, persistdci.StorageHostSave) (bool, error)
}

type dciTraceWire struct {
	Trace          domaindci.SearchTrace `json:"trace"`
	IdempotencyKey string                `json:"idempotency_key,omitempty"`
}

type dciResultWire struct {
	Result         domaindci.SearchResult `json:"result"`
	IdempotencyKey string                 `json:"idempotency_key,omitempty"`
}

type dciActionPayload struct {
	ActionID modulecore.ActionID `json:"action_id"`
}

type dciKeyPayload struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type dciListPayload struct {
	Limit int `json:"limit"`
}

type dciFindTraceResult struct {
	Found bool          `json:"found"`
	Item  *dciTraceWire `json:"item,omitempty"`
}

type dciFindSearchResult struct {
	Found bool           `json:"found"`
	Item  *dciResultWire `json:"item,omitempty"`
}

type dciListResult struct {
	Items []dciTraceWire `json:"items"`
}

func RegisterDCIGroup(handler *Handler, owner DCIGroupOwner) error {
	if handler == nil || nilDCIValue(owner) {
		return errors.New("storagehost: dci group needs a handler and owner")
	}
	recovery, ok := owner.(dciRecoveryOwner)
	if !ok || nilDCIValue(recovery) {
		return errors.New("storagehost: dci owner lacks durable recovery")
	}
	regs := []func() error{
		func() error { return registerDCISaveTrace(handler, recovery) },
		func() error { return registerDCISaveResult(handler, recovery) },
		func() error { return registerDCIFindTraceAction(handler, owner) },
		func() error { return registerDCIFindResultAction(handler, owner) },
		func() error { return registerDCIFindTraceKey(handler, owner) },
		func() error { return registerDCIFindResultKey(handler, owner) },
		func() error { return registerDCIList(handler, owner) },
	}
	for _, register := range regs {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func registerDCISaveTrace(h *Handler, owner dciRecoveryOwner) error {
	return h.RegisterRecoverable(GroupDCI, dciOpSaveTrace, func(ctx context.Context, m MutationMetadata) (any, error) {
		var p dciTraceWire
		if decodeDCIPayload(m.Payload, &p) != nil || !validDCIKey(p.IdempotencyKey) {
			return nil, ownerRolledBack(dciSchemaError(dciOpSaveTrace))
		}
		p.Trace.IdempotencyKey = p.IdempotencyKey
		if domaindci.ValidateSearchTrace(p.Trace) != nil || !boundedDCIValue(p) {
			return nil, ownerRolledBack(dciSchemaError(dciOpSaveTrace))
		}
		mutation := persistdci.StorageHostSave{Kind: persistdci.StorageHostSaveTrace, Trace: &p.Trace, IdempotencyKey: p.IdempotencyKey}
		if err := owner.SaveForStorageHostOperation(ctx, dciIdentity(m, dciOpSaveTrace), mutation); err != nil {
			return nil, dciMutationError(err)
		}
		return nil, nil
	}, func(ctx context.Context, m MutationMetadata) (ReconcileDecision, error) {
		var p dciTraceWire
		if decodeDCIPayload(m.Payload, &p) != nil || !validDCIKey(p.IdempotencyKey) {
			return UnknownOutcome(), nil
		}
		p.Trace.IdempotencyKey = p.IdempotencyKey
		if domaindci.ValidateSearchTrace(p.Trace) != nil || !boundedDCIValue(p) {
			return UnknownOutcome(), nil
		}
		mutation := persistdci.StorageHostSave{Kind: persistdci.StorageHostSaveTrace, Trace: &p.Trace, IdempotencyKey: p.IdempotencyKey}
		found, err := owner.LookupStorageHostOperationReceipt(ctx, dciIdentity(m, dciOpSaveTrace), mutation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(nil), nil
	})
}

func registerDCISaveResult(h *Handler, owner dciRecoveryOwner) error {
	return h.RegisterRecoverable(GroupDCI, dciOpSaveResult, func(ctx context.Context, m MutationMetadata) (any, error) {
		var p dciResultWire
		if decodeDCIPayload(m.Payload, &p) != nil || !validDCIKey(p.IdempotencyKey) {
			return nil, ownerRolledBack(dciSchemaError(dciOpSaveResult))
		}
		p.Result.Trace.IdempotencyKey = p.IdempotencyKey
		if domaindci.ValidateSearchResult(p.Result) != nil || !boundedDCIValue(p) {
			return nil, ownerRolledBack(dciSchemaError(dciOpSaveResult))
		}
		mutation := persistdci.StorageHostSave{Kind: persistdci.StorageHostSaveResult, Result: &p.Result, IdempotencyKey: p.IdempotencyKey}
		if err := owner.SaveForStorageHostOperation(ctx, dciIdentity(m, dciOpSaveResult), mutation); err != nil {
			return nil, dciMutationError(err)
		}
		return nil, nil
	}, func(ctx context.Context, m MutationMetadata) (ReconcileDecision, error) {
		var p dciResultWire
		if decodeDCIPayload(m.Payload, &p) != nil || !validDCIKey(p.IdempotencyKey) {
			return UnknownOutcome(), nil
		}
		p.Result.Trace.IdempotencyKey = p.IdempotencyKey
		if domaindci.ValidateSearchResult(p.Result) != nil || !boundedDCIValue(p) {
			return UnknownOutcome(), nil
		}
		mutation := persistdci.StorageHostSave{Kind: persistdci.StorageHostSaveResult, Result: &p.Result, IdempotencyKey: p.IdempotencyKey}
		found, err := owner.LookupStorageHostOperationReceipt(ctx, dciIdentity(m, dciOpSaveResult), mutation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(nil), nil
	})
}

func registerDCIFindTraceAction(h *Handler, owner DCIGroupOwner) error {
	return h.Register(GroupDCI, dciOpFindTraceAction, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p dciActionPayload
		if decodeDCIPayload(raw, &p) != nil || p.ActionID.Validate() != nil {
			return nil, dciSchemaError(dciOpFindTraceAction)
		}
		item, found, err := owner.FindSearchTraceByActionID(ctx, p.ActionID)
		return checkedDCITraceFind(item, found, err, func(v domaindci.SearchTrace) bool { return v.ActionID == p.ActionID }, dciOpFindTraceAction)
	})
}

func registerDCIFindResultAction(h *Handler, owner DCIGroupOwner) error {
	return h.Register(GroupDCI, dciOpFindResultAction, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p dciActionPayload
		if decodeDCIPayload(raw, &p) != nil || p.ActionID.Validate() != nil {
			return nil, dciSchemaError(dciOpFindResultAction)
		}
		item, found, err := owner.FindSearchResultByActionID(ctx, p.ActionID)
		return checkedDCIResultFind(item, found, err, func(v domaindci.SearchResult) bool { return v.Trace.ActionID == p.ActionID }, dciOpFindResultAction)
	})
}

func registerDCIFindTraceKey(h *Handler, owner DCIGroupOwner) error {
	return h.Register(GroupDCI, dciOpFindTraceDedupe, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p dciKeyPayload
		if decodeDCIPayload(raw, &p) != nil || !validDCIKey(p.IdempotencyKey) {
			return nil, dciSchemaError(dciOpFindTraceDedupe)
		}
		item, found, err := owner.FindSearchTraceByIdempotencyKey(ctx, p.IdempotencyKey)
		return checkedDCITraceFind(item, found, err, func(v domaindci.SearchTrace) bool { return v.IdempotencyKey == p.IdempotencyKey }, dciOpFindTraceDedupe)
	})
}

func registerDCIFindResultKey(h *Handler, owner DCIGroupOwner) error {
	return h.Register(GroupDCI, dciOpFindResultDedupe, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p dciKeyPayload
		if decodeDCIPayload(raw, &p) != nil || !validDCIKey(p.IdempotencyKey) {
			return nil, dciSchemaError(dciOpFindResultDedupe)
		}
		item, found, err := owner.FindSearchResultByIdempotencyKey(ctx, p.IdempotencyKey)
		return checkedDCIResultFind(item, found, err, func(v domaindci.SearchResult) bool { return v.Trace.IdempotencyKey == p.IdempotencyKey }, dciOpFindResultDedupe)
	})
}

func registerDCIList(h *Handler, owner DCIGroupOwner) error {
	return h.Register(GroupDCI, dciOpListRecent, false, func(_ context.Context, raw json.RawMessage) (any, error) {
		var p dciListPayload
		if decodeDCIPayload(raw, &p) != nil || p.Limit < 0 || p.Limit > dciMaxList {
			return nil, dciSchemaError(dciOpListRecent)
		}
		limit := p.Limit
		if limit == 0 {
			limit = 50
		}
		items, err := owner.ListRecent(limit)
		if err != nil || len(items) > limit {
			return nil, dciStoreError(dciOpListRecent)
		}
		result := dciListResult{Items: make([]dciTraceWire, len(items))}
		for i := range items {
			if domaindci.ValidateStoredSearchTrace(items[i]) != nil || !validDCIKey(items[i].IdempotencyKey) {
				return nil, dciStoreError(dciOpListRecent)
			}
			result.Items[i] = dciTraceWire{Trace: items[i], IdempotencyKey: items[i].IdempotencyKey}
		}
		if !boundedDCIValue(result) {
			return nil, dciStoreError(dciOpListRecent)
		}
		return result, nil
	})
}

func checkedDCITraceFind(item domaindci.SearchTrace, found bool, err error, matches func(domaindci.SearchTrace) bool, op string) (any, error) {
	if err != nil {
		return nil, dciStoreError(op)
	}
	if !found {
		return dciFindTraceResult{}, nil
	}
	if domaindci.ValidateStoredSearchTrace(item) != nil || !validDCIKey(item.IdempotencyKey) || !matches(item) {
		return nil, dciStoreError(op)
	}
	w := dciTraceWire{Trace: item, IdempotencyKey: item.IdempotencyKey}
	if !boundedDCIValue(w) {
		return nil, dciStoreError(op)
	}
	return dciFindTraceResult{Found: true, Item: &w}, nil
}

func checkedDCIResultFind(item domaindci.SearchResult, found bool, err error, matches func(domaindci.SearchResult) bool, op string) (any, error) {
	if err != nil {
		return nil, dciStoreError(op)
	}
	if !found {
		return dciFindSearchResult{}, nil
	}
	if domaindci.ValidateStoredSearchResult(item) != nil || !validDCIKey(item.Trace.IdempotencyKey) || !matches(item) {
		return nil, dciStoreError(op)
	}
	w := dciResultWire{Result: item, IdempotencyKey: item.Trace.IdempotencyKey}
	if !boundedDCIValue(w) {
		return nil, dciStoreError(op)
	}
	return dciFindSearchResult{Found: true, Item: &w}, nil
}

func dciIdentity(m MutationMetadata, op string) persistdci.StorageHostOperationIdentity {
	return persistdci.StorageHostOperationIdentity{OpID: m.OpID, PayloadSHA256: payloadHash(GroupDCI, op, m.Payload), WriterGeneration: m.JournalGeneration}
}

func dciMutationError(err error) error {
	if errors.Is(err, persistdci.ErrDCIStorageHostConflict) {
		return ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "dci operation conflicts with owner state"))
	}
	return dciStoreError("save")
}

func decodeDCIPayload(raw json.RawMessage, dst any) error {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || len(b) > dciMaxPayload || !utf8.Valid(b) || b[0] != '{' {
		return errors.New("dci payload rejected")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return errors.New("dci payload rejected")
	}
	var trailing any
	if !errors.Is(d.Decode(&trailing), io.EOF) {
		return errors.New("dci payload rejected")
	}
	return nil
}

func validDCIKey(v string) bool {
	return v != "" && len(v) <= 512 && utf8.ValidString(v) && strings.TrimSpace(v) == v && strings.IndexByte(v, 0) < 0
}

func boundedDCIValue(v any) bool {
	b, err := json.Marshal(v)
	return err == nil && len(b) <= dciMaxPayload
}

func dciSchemaError(op string) *Error {
	return NewError(ErrorCodeSchemaRejected, "dci "+op+" rejected")
}
func dciStoreError(op string) *Error { return NewError(ErrorCodeStoreUnavailable, "dci "+op+" failed") }

func nilDCIValue(v any) bool {
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

type DCIClient struct{ client *Client }

func NewDCIClient(c *Client) *DCIClient { return &DCIClient{client: c} }

func (c *DCIClient) SaveSearchTrace(ctx context.Context, v domaindci.SearchTrace) error {
	if domaindci.ValidateSearchTrace(v) != nil || !validDCIKey(v.IdempotencyKey) {
		return dciSchemaError(dciOpSaveTrace)
	}
	err := c.client.Call(ctx, GroupDCI, dciOpSaveTrace, dciTraceWire{Trace: v, IdempotencyKey: v.IdempotencyKey}, nil)
	return mapDCIClientError(err)
}

func (c *DCIClient) SaveSearchResult(ctx context.Context, v domaindci.SearchResult) error {
	if domaindci.ValidateSearchResult(v) != nil || !validDCIKey(v.Trace.IdempotencyKey) {
		return dciSchemaError(dciOpSaveResult)
	}
	err := c.client.Call(ctx, GroupDCI, dciOpSaveResult, dciResultWire{Result: v, IdempotencyKey: v.Trace.IdempotencyKey}, nil)
	return mapDCIClientError(err)
}

func (c *DCIClient) FindSearchTraceByActionID(ctx context.Context, id modulecore.ActionID) (domaindci.SearchTrace, bool, error) {
	if id.Validate() != nil {
		return domaindci.SearchTrace{}, false, dciSchemaError(dciOpFindTraceAction)
	}
	var r dciFindTraceResult
	if err := c.client.Call(ctx, GroupDCI, dciOpFindTraceAction, dciActionPayload{ActionID: id}, &r); err != nil {
		return domaindci.SearchTrace{}, false, err
	}
	return validateDCITraceResponse(r, func(v domaindci.SearchTrace) bool { return v.ActionID == id })
}

func (c *DCIClient) FindSearchResultByActionID(ctx context.Context, id modulecore.ActionID) (domaindci.SearchResult, bool, error) {
	if id.Validate() != nil {
		return domaindci.SearchResult{}, false, dciSchemaError(dciOpFindResultAction)
	}
	var r dciFindSearchResult
	if err := c.client.Call(ctx, GroupDCI, dciOpFindResultAction, dciActionPayload{ActionID: id}, &r); err != nil {
		return domaindci.SearchResult{}, false, err
	}
	return validateDCIResultResponse(r, func(v domaindci.SearchResult) bool { return v.Trace.ActionID == id })
}

func (c *DCIClient) FindSearchTraceByIdempotencyKey(ctx context.Context, key string) (domaindci.SearchTrace, bool, error) {
	if !validDCIKey(key) {
		return domaindci.SearchTrace{}, false, dciSchemaError(dciOpFindTraceDedupe)
	}
	var r dciFindTraceResult
	if err := c.client.Call(ctx, GroupDCI, dciOpFindTraceDedupe, dciKeyPayload{IdempotencyKey: key}, &r); err != nil {
		return domaindci.SearchTrace{}, false, err
	}
	return validateDCITraceResponse(r, func(v domaindci.SearchTrace) bool { return v.IdempotencyKey == key })
}

func (c *DCIClient) FindSearchResultByIdempotencyKey(ctx context.Context, key string) (domaindci.SearchResult, bool, error) {
	if !validDCIKey(key) {
		return domaindci.SearchResult{}, false, dciSchemaError(dciOpFindResultDedupe)
	}
	var r dciFindSearchResult
	if err := c.client.Call(ctx, GroupDCI, dciOpFindResultDedupe, dciKeyPayload{IdempotencyKey: key}, &r); err != nil {
		return domaindci.SearchResult{}, false, err
	}
	return validateDCIResultResponse(r, func(v domaindci.SearchResult) bool { return v.Trace.IdempotencyKey == key })
}

func (c *DCIClient) ListRecent(limit int) ([]domaindci.SearchTrace, error) {
	if limit < 0 || limit > dciMaxList {
		return nil, dciSchemaError(dciOpListRecent)
	}
	var r dciListResult
	if err := c.client.Call(context.Background(), GroupDCI, dciOpListRecent, dciListPayload{Limit: limit}, &r); err != nil {
		return nil, err
	}
	effectiveLimit := limit
	if effectiveLimit == 0 {
		effectiveLimit = 50
	}
	if len(r.Items) > effectiveLimit {
		return nil, NewError(ErrorCodeOutcomeUnknown, "dci list response malformed")
	}
	items := make([]domaindci.SearchTrace, len(r.Items))
	for i := range r.Items {
		items[i] = r.Items[i].Trace
		items[i].IdempotencyKey = r.Items[i].IdempotencyKey
		if !validDCIKey(items[i].IdempotencyKey) || domaindci.ValidateStoredSearchTrace(items[i]) != nil {
			return nil, NewError(ErrorCodeOutcomeUnknown, "dci list response malformed")
		}
	}
	return items, nil
}

func validateDCITraceResponse(r dciFindTraceResult, matches func(domaindci.SearchTrace) bool) (domaindci.SearchTrace, bool, error) {
	if !r.Found {
		if r.Item != nil {
			return domaindci.SearchTrace{}, false, NewError(ErrorCodeOutcomeUnknown, "dci find response malformed")
		}
		return domaindci.SearchTrace{}, false, nil
	}
	if r.Item == nil {
		return domaindci.SearchTrace{}, false, NewError(ErrorCodeOutcomeUnknown, "dci find response malformed")
	}
	v := r.Item.Trace
	v.IdempotencyKey = r.Item.IdempotencyKey
	if !validDCIKey(v.IdempotencyKey) || domaindci.ValidateStoredSearchTrace(v) != nil || !matches(v) {
		return domaindci.SearchTrace{}, false, NewError(ErrorCodeOutcomeUnknown, "dci find response malformed")
	}
	return v, true, nil
}

func validateDCIResultResponse(r dciFindSearchResult, matches func(domaindci.SearchResult) bool) (domaindci.SearchResult, bool, error) {
	if !r.Found {
		if r.Item != nil {
			return domaindci.SearchResult{}, false, NewError(ErrorCodeOutcomeUnknown, "dci find response malformed")
		}
		return domaindci.SearchResult{}, false, nil
	}
	if r.Item == nil {
		return domaindci.SearchResult{}, false, NewError(ErrorCodeOutcomeUnknown, "dci find response malformed")
	}
	v := r.Item.Result
	v.Trace.IdempotencyKey = r.Item.IdempotencyKey
	if !validDCIKey(v.Trace.IdempotencyKey) || domaindci.ValidateStoredSearchResult(v) != nil || !matches(v) {
		return domaindci.SearchResult{}, false, NewError(ErrorCodeOutcomeUnknown, "dci find response malformed")
	}
	return v, true, nil
}

func mapDCIClientError(err error) error {
	var wire *Error
	if errors.As(err, &wire) && wire.Code == ErrorCodeDuplicateConflict {
		return persistdci.ErrDCIStorageHostConflict
	}
	return err
}

var _ DCIGroupOwner = (*DCIClient)(nil)
