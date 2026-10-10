package storagehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
	persistcomplexity "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/complexity"
)

const (
	GroupComplexityHotspot = "complexity_hotspot"
	complexityListDefault  = 50
	complexityListMaximum  = 500
)

const (
	complexityOpSaveScanEvent          = persistcomplexity.ComplexitySaveScanEventStorageHostOperation
	complexityOpSaveHotspot            = persistcomplexity.ComplexitySaveHotspotStorageHostOperation
	complexityOpSaveHotspotEvidence    = persistcomplexity.ComplexitySaveHotspotEvidenceStorageHostOperation
	complexityOpSaveReportArtifact     = persistcomplexity.ComplexitySaveReportArtifactStorageHostOperation
	complexityOpListScanEvents         = "list_scan_events"
	complexityOpListHotspots           = "list_hotspots"
	complexityOpFindHotspotByID        = "find_hotspot_by_id"
	complexityOpListHotspotEvidence    = "list_hotspot_evidence"
	complexityOpListReportArtifacts    = "list_report_artifacts"
	complexityOpFindReportArtifactByID = "find_report_artifact_by_id"
)

// ComplexityHotspotGroupOwner is the existing, typed Complexity owner surface.
// The storage host receives no SQL, path, callback, or generic CRUD operation.
type ComplexityHotspotGroupOwner interface {
	SaveScanEventForStorageHostOperation(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, domaincomplexity.ScanEvent) error
	LookupScanEventStorageHostReceipt(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, domaincomplexity.ScanEvent) (bool, error)
	SaveHotspotForStorageHostOperation(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, domaincomplexity.Hotspot) error
	LookupHotspotStorageHostReceipt(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, domaincomplexity.Hotspot) (bool, error)
	SaveHotspotEvidenceForStorageHostOperation(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, domaincomplexity.HotspotEvidence) error
	LookupHotspotEvidenceStorageHostReceipt(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, domaincomplexity.HotspotEvidence) (bool, error)
	SaveReportArtifactForStorageHostOperation(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, domaincomplexity.ReportArtifact) error
	LookupReportArtifactStorageHostReceipt(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, domaincomplexity.ReportArtifact) (bool, error)
	ListScanEvents(context.Context, int) ([]domaincomplexity.ScanEvent, error)
	ListHotspots(context.Context, int) ([]domaincomplexity.Hotspot, error)
	FindHotspotByID(context.Context, string) (domaincomplexity.Hotspot, bool, error)
	ListHotspotEvidence(context.Context, int) ([]domaincomplexity.HotspotEvidence, error)
	ListReportArtifacts(context.Context, int) ([]domaincomplexity.ReportArtifact, error)
	FindReportArtifactByID(context.Context, string) (domaincomplexity.ReportArtifact, bool, error)
}

type complexityRecordPayload[T any] struct {
	Record T `json:"record"`
}

type complexityListPayload struct {
	Limit int `json:"limit"`
}

type complexityFindHotspotPayload struct {
	HotspotID string `json:"hotspot_id"`
}

type complexityFindReportPayload struct {
	ArtifactID string `json:"artifact_id"`
}

type complexityHotspotResult struct {
	Hotspot domaincomplexity.Hotspot `json:"hotspot"`
	Found   bool                     `json:"found"`
}

type complexityReportResult struct {
	Artifact domaincomplexity.ReportArtifact `json:"artifact"`
	Found    bool                            `json:"found"`
}

func RegisterComplexityHotspotGroup(handler *Handler, owner ComplexityHotspotGroupOwner) error {
	if handler == nil || nilComplexityGroupOwner(owner) {
		return errors.New("storagehost: complexity hotspot group needs a handler and owner")
	}
	registrations := []func() error{
		func() error {
			return registerComplexitySave(handler, owner, complexityOpSaveScanEvent, domaincomplexity.ValidateScanEvent, owner.SaveScanEventForStorageHostOperation, owner.LookupScanEventStorageHostReceipt)
		},
		func() error {
			return registerComplexitySave(handler, owner, complexityOpSaveHotspot, domaincomplexity.ValidateHotspot, owner.SaveHotspotForStorageHostOperation, owner.LookupHotspotStorageHostReceipt)
		},
		func() error {
			return registerComplexitySave(handler, owner, complexityOpSaveHotspotEvidence, domaincomplexity.ValidateHotspotEvidence, owner.SaveHotspotEvidenceForStorageHostOperation, owner.LookupHotspotEvidenceStorageHostReceipt)
		},
		func() error {
			return registerComplexitySave(handler, owner, complexityOpSaveReportArtifact, domaincomplexity.ValidateReportArtifact, owner.SaveReportArtifactForStorageHostOperation, owner.LookupReportArtifactStorageHostReceipt)
		},
		func() error { return registerComplexityList(handler, complexityOpListScanEvents, owner.ListScanEvents) },
		func() error { return registerComplexityList(handler, complexityOpListHotspots, owner.ListHotspots) },
		func() error {
			return registerComplexityList(handler, complexityOpListHotspotEvidence, owner.ListHotspotEvidence)
		},
		func() error {
			return registerComplexityList(handler, complexityOpListReportArtifacts, owner.ListReportArtifacts)
		},
		func() error { return registerComplexityFindHotspot(handler, owner) },
		func() error { return registerComplexityFindReport(handler, owner) },
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func registerComplexitySave[T any](handler *Handler, owner any, operation string, validate func(T) error, save func(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, T) error, lookup func(context.Context, persistcomplexity.ComplexityStorageHostOperationIdentity, T) (bool, error)) error {
	return handler.RegisterRecoverable(GroupComplexityHotspot, operation, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeComplexityPayload[complexityRecordPayload[T]](mutation.Payload)
		if err != nil || validate(payload.Record) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "complexity hotspot mutation request rejected"))
		}
		identity := complexityIdentity(mutation, operation)
		if err := save(ctx, identity, payload.Record); err != nil {
			if errors.Is(err, persistcomplexity.ErrComplexityStorageHostConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "complexity hotspot operation identity conflicts with its owner receipt"))
			}
			return nil, NewError(ErrorCodeStoreUnavailable, "complexity hotspot owner mutation failed")
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeComplexityPayload[complexityRecordPayload[T]](mutation.Payload)
		if err != nil || validate(payload.Record) != nil {
			return UnknownOutcome(), nil
		}
		found, err := lookup(ctx, complexityIdentity(mutation, operation), payload.Record)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(nil), nil
	})
}

func registerComplexityList[T any](handler *Handler, operation string, list func(context.Context, int) ([]T, error)) error {
	return handler.Register(GroupComplexityHotspot, operation, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeComplexityPayload[complexityListPayload](raw)
		if err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "complexity hotspot list request rejected")
		}
		limit := complexityEffectiveListLimit(payload.Limit)
		items, err := list(ctx, limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "complexity hotspot list unavailable")
		}
		return complexityNonNilList(items), nil
	})
}

func registerComplexityFindHotspot(handler *Handler, owner ComplexityHotspotGroupOwner) error {
	return handler.Register(GroupComplexityHotspot, complexityOpFindHotspotByID, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeComplexityPayload[complexityFindHotspotPayload](raw)
		if err != nil || payload.HotspotID == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "complexity hotspot lookup request rejected")
		}
		item, found, err := owner.FindHotspotByID(ctx, payload.HotspotID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "complexity hotspot lookup unavailable")
		}
		return complexityHotspotResult{Hotspot: item, Found: found}, nil
	})
}

func registerComplexityFindReport(handler *Handler, owner ComplexityHotspotGroupOwner) error {
	return handler.Register(GroupComplexityHotspot, complexityOpFindReportArtifactByID, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeComplexityPayload[complexityFindReportPayload](raw)
		if err != nil || payload.ArtifactID == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "complexity report lookup request rejected")
		}
		item, found, err := owner.FindReportArtifactByID(ctx, payload.ArtifactID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "complexity report lookup unavailable")
		}
		return complexityReportResult{Artifact: item, Found: found}, nil
	})
}

func complexityIdentity(mutation MutationMetadata, operation string) persistcomplexity.ComplexityStorageHostOperationIdentity {
	hash := sha256.Sum256(mutation.Payload)
	return persistcomplexity.ComplexityStorageHostOperationIdentity{
		OpID: mutation.OpID, Operation: operation,
		PayloadSHA256: hex.EncodeToString(hash[:]), WriterGeneration: mutation.JournalGeneration,
	}
}

func complexityEffectiveListLimit(limit int) int {
	if limit <= 0 {
		return complexityListDefault
	}
	return limit
}

func complexityNonNilList[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func decodeComplexityPayload[T any](raw []byte) (T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, err
	}
	return value, nil
}

func nilComplexityGroupOwner(owner ComplexityHotspotGroupOwner) bool {
	if owner == nil {
		return true
	}
	value := reflect.ValueOf(owner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// ComplexityHotspotClient implements the same narrow ten-operation surface on
// the CORE side. Its reads enforce the requested item-count bound after decode.
type ComplexityHotspotClient struct{ client *Client }

func NewComplexityHotspotClient(client *Client) *ComplexityHotspotClient {
	return &ComplexityHotspotClient{client: client}
}

func (c *ComplexityHotspotClient) Close() error { return nil }

func (c *ComplexityHotspotClient) SaveScanEvent(ctx context.Context, item domaincomplexity.ScanEvent) error {
	return c.save(ctx, complexityOpSaveScanEvent, complexityRecordPayload[domaincomplexity.ScanEvent]{Record: item})
}

func (c *ComplexityHotspotClient) SaveHotspot(ctx context.Context, item domaincomplexity.Hotspot) error {
	return c.save(ctx, complexityOpSaveHotspot, complexityRecordPayload[domaincomplexity.Hotspot]{Record: item})
}

func (c *ComplexityHotspotClient) SaveHotspotEvidence(ctx context.Context, item domaincomplexity.HotspotEvidence) error {
	return c.save(ctx, complexityOpSaveHotspotEvidence, complexityRecordPayload[domaincomplexity.HotspotEvidence]{Record: item})
}

func (c *ComplexityHotspotClient) SaveReportArtifact(ctx context.Context, item domaincomplexity.ReportArtifact) error {
	return c.save(ctx, complexityOpSaveReportArtifact, complexityRecordPayload[domaincomplexity.ReportArtifact]{Record: item})
}

func (c *ComplexityHotspotClient) save(ctx context.Context, operation string, payload any) error {
	if c == nil || c.client == nil {
		return NewError(ErrorCodeStoreUnavailable, "complexity hotspot storage host client unavailable")
	}
	var result json.RawMessage
	if err := c.client.Call(ctx, GroupComplexityHotspot, operation, payload, &result); err != nil {
		return err
	}
	if string(result) != "null" {
		return NewError(ErrorCodeOutcomeUnknown, "complexity hotspot mutation result could not be confirmed")
	}
	return nil
}

func (c *ComplexityHotspotClient) ListScanEvents(ctx context.Context, limit int) ([]domaincomplexity.ScanEvent, error) {
	return complexityClientList[domaincomplexity.ScanEvent](ctx, c, complexityOpListScanEvents, limit)
}

func (c *ComplexityHotspotClient) ListHotspots(ctx context.Context, limit int) ([]domaincomplexity.Hotspot, error) {
	return complexityClientList[domaincomplexity.Hotspot](ctx, c, complexityOpListHotspots, limit)
}

func (c *ComplexityHotspotClient) ListHotspotEvidence(ctx context.Context, limit int) ([]domaincomplexity.HotspotEvidence, error) {
	return complexityClientList[domaincomplexity.HotspotEvidence](ctx, c, complexityOpListHotspotEvidence, limit)
}

func (c *ComplexityHotspotClient) ListReportArtifacts(ctx context.Context, limit int) ([]domaincomplexity.ReportArtifact, error) {
	return complexityClientList[domaincomplexity.ReportArtifact](ctx, c, complexityOpListReportArtifacts, limit)
}

func complexityClientList[T any](ctx context.Context, c *ComplexityHotspotClient, operation string, requestedLimit int) ([]T, error) {
	var zero []T
	if c == nil || c.client == nil {
		return zero, NewError(ErrorCodeStoreUnavailable, "complexity hotspot storage host client unavailable")
	}
	limit := complexityEffectiveListLimit(requestedLimit)
	var result json.RawMessage
	if err := c.client.Call(ctx, GroupComplexityHotspot, operation, complexityListPayload{Limit: limit}, &result); err != nil {
		return zero, err
	}
	var items []T
	if err := json.Unmarshal(result, &items); err != nil {
		return zero, NewError(ErrorCodeStoreUnavailable, "complexity hotspot list response rejected")
	}
	return complexityNonNilList(items), nil
}

func (c *ComplexityHotspotClient) FindHotspotByID(ctx context.Context, hotspotID string) (domaincomplexity.Hotspot, bool, error) {
	if c == nil || c.client == nil {
		return domaincomplexity.Hotspot{}, false, NewError(ErrorCodeStoreUnavailable, "complexity hotspot storage host client unavailable")
	}
	var raw json.RawMessage
	if err := c.client.Call(ctx, GroupComplexityHotspot, complexityOpFindHotspotByID, complexityFindHotspotPayload{HotspotID: hotspotID}, &raw); err != nil {
		return domaincomplexity.Hotspot{}, false, err
	}
	var result complexityHotspotResult
	if err := json.Unmarshal(raw, &result); err != nil || (!result.Found && !reflect.DeepEqual(result.Hotspot, domaincomplexity.Hotspot{})) || (result.Found && result.Hotspot.HotspotID != hotspotID) {
		return domaincomplexity.Hotspot{}, false, NewError(ErrorCodeStoreUnavailable, "complexity hotspot lookup response rejected")
	}
	return result.Hotspot, result.Found, nil
}

func (c *ComplexityHotspotClient) FindReportArtifactByID(ctx context.Context, artifactID string) (domaincomplexity.ReportArtifact, bool, error) {
	if c == nil || c.client == nil {
		return domaincomplexity.ReportArtifact{}, false, NewError(ErrorCodeStoreUnavailable, "complexity hotspot storage host client unavailable")
	}
	var raw json.RawMessage
	if err := c.client.Call(ctx, GroupComplexityHotspot, complexityOpFindReportArtifactByID, complexityFindReportPayload{ArtifactID: artifactID}, &raw); err != nil {
		return domaincomplexity.ReportArtifact{}, false, err
	}
	var result complexityReportResult
	if err := json.Unmarshal(raw, &result); err != nil || (!result.Found && !reflect.DeepEqual(result.Artifact, domaincomplexity.ReportArtifact{})) || (result.Found && result.Artifact.ArtifactID != artifactID) {
		return domaincomplexity.ReportArtifact{}, false, NewError(ErrorCodeStoreUnavailable, "complexity report lookup response rejected")
	}
	return result.Artifact, result.Found, nil
}

var _ ComplexityHotspotGroupOwner = (*persistcomplexity.SQLiteStore)(nil)
var _ = fmt.Sprintf
