package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	persistverification "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/verification"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	GroupVerificationReport = "verification_report"

	verificationReportOpSave        = "save"
	verificationReportOpListRecent  = "list_recent"
	verificationReportOpGetByTaskID = "get_by_task_id"
	verificationReportOpSummary     = "summary"

	verificationReportMaxPayload = 2 << 20
	verificationReportMaxList    = 100
)

// VerificationReportGroupOwner is exactly the canonical report store surface.
// Its recovery capability is intentionally separate from this consumer port.
type VerificationReportGroupOwner interface {
	Save(context.Context, domainverification.VerificationReport) error
	ListRecent(context.Context, int) ([]domainverification.VerificationReport, error)
	GetByTaskID(context.Context, modulecore.TaskID) (domainverification.VerificationReport, error)
	Summary(context.Context) (map[string]map[string]int, error)
}

type verificationReportRecoveryOwner interface {
	SaveForStorageHostOperation(context.Context, persistverification.StorageHostOperationIdentity, domainverification.VerificationReport) error
	LookupStorageHostOperationReceipt(context.Context, persistverification.StorageHostOperationIdentity) (json.RawMessage, bool, error)
}

type verificationReportSavePayload struct {
	Report domainverification.VerificationReport `json:"report"`
}

type verificationReportListPayload struct {
	Limit int `json:"limit"`
}

type verificationReportTaskPayload struct {
	TaskID modulecore.TaskID `json:"task_id"`
}

type verificationReportListResult struct {
	Reports []domainverification.VerificationReport `json:"reports"`
}

type verificationReportGetResult struct {
	Found  bool                                   `json:"found"`
	Report *domainverification.VerificationReport `json:"report,omitempty"`
}

type verificationReportSummaryResult struct {
	Summary map[string]map[string]int `json:"summary"`
}

func RegisterVerificationReportGroup(handler *Handler, owner VerificationReportGroupOwner) error {
	if handler == nil || nilVerificationReportValue(owner) {
		return errors.New("storagehost: verification report group needs a handler and owner")
	}
	recoveryOwner, ok := owner.(verificationReportRecoveryOwner)
	if !ok || nilVerificationReportValue(recoveryOwner) {
		return errors.New("storagehost: verification report owner lacks durable recovery")
	}
	if err := handler.RegisterRecoverable(GroupVerificationReport, verificationReportOpSave, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload verificationReportSavePayload
		if decodeVerificationReportPayload(mutation.Payload, &payload) != nil || !validVerificationReport(payload.Report) {
			return nil, ownerRolledBack(verificationReportSchemaError("save"))
		}
		identity := verificationReportOperationIdentity(mutation)
		if err := recoveryOwner.SaveForStorageHostOperation(ctx, identity, payload.Report); err != nil {
			if errors.Is(err, persistverification.ErrVerificationReportOperationConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "verification report operation conflicts with owner state"))
			}
			return nil, NewError(ErrorCodeStoreUnavailable, "verification report owner write failed")
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload verificationReportSavePayload
		if decodeVerificationReportPayload(mutation.Payload, &payload) != nil || !validVerificationReport(payload.Report) {
			return UnknownOutcome(), nil
		}
		raw, found, err := recoveryOwner.LookupStorageHostOperationReceipt(ctx, verificationReportOperationIdentity(mutation))
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return UnknownOutcome(), nil
		}
		return Committed(nil), nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupVerificationReport, verificationReportOpListRecent, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload verificationReportListPayload
		if decodeVerificationReportPayload(raw, &payload) != nil || payload.Limit > verificationReportMaxList {
			return nil, verificationReportSchemaError("list")
		}
		if payload.Limit <= 0 {
			payload.Limit = 20
		}
		reports, err := owner.ListRecent(ctx, payload.Limit)
		if err != nil || len(reports) > payload.Limit || !validVerificationReports(reports) {
			return nil, verificationReportReadError("list")
		}
		return verificationReportListResult{Reports: reports}, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupVerificationReport, verificationReportOpGetByTaskID, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload verificationReportTaskPayload
		if decodeVerificationReportPayload(raw, &payload) != nil || payload.TaskID.Validate() != nil {
			return nil, verificationReportSchemaError("task lookup")
		}
		report, err := owner.GetByTaskID(ctx, payload.TaskID)
		if errors.Is(err, persistverification.ErrVerificationReportNotFound) {
			return verificationReportGetResult{Found: false}, nil
		}
		if err != nil || !validVerificationReport(report) || report.TaskID != payload.TaskID {
			return nil, verificationReportReadError("task lookup")
		}
		return verificationReportGetResult{Found: true, Report: &report}, nil
	}); err != nil {
		return err
	}
	return handler.Register(GroupVerificationReport, verificationReportOpSummary, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload struct{}
		if decodeVerificationReportPayload(raw, &payload) != nil {
			return nil, verificationReportSchemaError("summary")
		}
		summary, err := owner.Summary(ctx)
		if err != nil || !validVerificationReportSummary(summary) {
			return nil, verificationReportReadError("summary")
		}
		return verificationReportSummaryResult{Summary: summary}, nil
	})
}

func verificationReportOperationIdentity(mutation MutationMetadata) persistverification.StorageHostOperationIdentity {
	return persistverification.StorageHostOperationIdentity{
		OpID: mutation.OpID, PayloadSHA256: payloadHash(GroupVerificationReport, verificationReportOpSave, mutation.Payload),
		WriterGeneration: mutation.JournalGeneration,
	}
}

func decodeVerificationReportPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > verificationReportMaxPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("verification report payload rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("verification report payload rejected")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("verification report payload rejected")
	}
	return nil
}

func validVerificationReport(report domainverification.VerificationReport) bool {
	if report.Validate() != nil {
		return false
	}
	encoded, err := json.Marshal(report)
	return err == nil && len(encoded) <= verificationReportMaxPayload
}

func validVerificationReports(reports []domainverification.VerificationReport) bool {
	if len(reports) > verificationReportMaxList {
		return false
	}
	for _, report := range reports {
		if !validVerificationReport(report) {
			return false
		}
	}
	encoded, err := json.Marshal(reports)
	return err == nil && len(encoded) <= verificationReportMaxPayload
}

func validVerificationReportSummary(summary map[string]map[string]int) bool {
	want := map[string][]string{
		"status":        {string(domainverification.StatusVerified), string(domainverification.StatusWeaklySupported), string(domainverification.StatusUnsupported), string(domainverification.StatusConflict), string(domainverification.StatusNotChecked)},
		"trigger_level": {string(domainverification.TriggerLow), string(domainverification.TriggerMedium), string(domainverification.TriggerHigh)},
	}
	if len(summary) != len(want) {
		return false
	}
	for section, keys := range want {
		values, ok := summary[section]
		if !ok || len(values) != len(keys) {
			return false
		}
		for _, key := range keys {
			count, exists := values[key]
			if !exists || count < 0 {
				return false
			}
		}
	}
	return true
}

func verificationReportSchemaError(operation string) *Error {
	return NewError(ErrorCodeSchemaRejected, "verification report "+operation+" rejected")
}

func verificationReportReadError(operation string) *Error {
	return NewError(ErrorCodeStoreUnavailable, "verification report "+operation+" failed")
}

func nilVerificationReportValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// VerificationReportClient implements the four-method report store port over
// closed storage-host operations.
type VerificationReportClient struct{ client *Client }

func NewVerificationReportClient(client *Client) *VerificationReportClient {
	return &VerificationReportClient{client: client}
}

func (store *VerificationReportClient) Save(ctx context.Context, report domainverification.VerificationReport) error {
	if !validVerificationReport(report) {
		return verificationReportSchemaError("save")
	}
	return store.client.Call(ctx, GroupVerificationReport, verificationReportOpSave, verificationReportSavePayload{Report: report}, nil)
}

func (store *VerificationReportClient) ListRecent(ctx context.Context, limit int) ([]domainverification.VerificationReport, error) {
	if limit > verificationReportMaxList {
		return nil, verificationReportSchemaError("list")
	}
	var result verificationReportListResult
	if err := store.client.Call(ctx, GroupVerificationReport, verificationReportOpListRecent, verificationReportListPayload{Limit: limit}, &result); err != nil {
		return nil, err
	}
	if len(result.Reports) > verificationReportMaxList || !validVerificationReports(result.Reports) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "verification report list response malformed")
	}
	return result.Reports, nil
}

func (store *VerificationReportClient) GetByTaskID(ctx context.Context, taskID modulecore.TaskID) (domainverification.VerificationReport, error) {
	if taskID.Validate() != nil {
		return domainverification.VerificationReport{}, verificationReportSchemaError("task lookup")
	}
	var result verificationReportGetResult
	if err := store.client.Call(ctx, GroupVerificationReport, verificationReportOpGetByTaskID, verificationReportTaskPayload{TaskID: taskID}, &result); err != nil {
		return domainverification.VerificationReport{}, err
	}
	if !result.Found {
		if result.Report != nil {
			return domainverification.VerificationReport{}, NewError(ErrorCodeOutcomeUnknown, "verification report task response malformed")
		}
		return domainverification.VerificationReport{}, persistverification.ErrVerificationReportNotFound
	}
	if result.Report == nil || !validVerificationReport(*result.Report) || result.Report.TaskID != taskID {
		return domainverification.VerificationReport{}, NewError(ErrorCodeOutcomeUnknown, "verification report task response malformed")
	}
	return *result.Report, nil
}

func (store *VerificationReportClient) Summary(ctx context.Context) (map[string]map[string]int, error) {
	var result verificationReportSummaryResult
	if err := store.client.Call(ctx, GroupVerificationReport, verificationReportOpSummary, struct{}{}, &result); err != nil {
		return nil, err
	}
	if !validVerificationReportSummary(result.Summary) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "verification report summary response malformed")
	}
	return result.Summary, nil
}

var _ VerificationReportGroupOwner = (*VerificationReportClient)(nil)
