package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	persistbrowsertrace "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	GroupBrowserTraceToAPI       = "browser_trace_to_api"
	browserTraceToAPIMaxPayload  = 16 << 20
	browserTraceToAPIDefaultList = 50
	browserTraceToAPIMaxList     = 100
	browserTraceToAPIMaxPage     = 1000

	browserTraceToAPIListTraceRuns               = "list_trace_runs"
	browserTraceToAPIListCandidates              = "list_api_candidates"
	browserTraceToAPIListSchemas                 = "list_api_candidate_schemas"
	browserTraceToAPIListValidations             = "list_api_candidate_validation_results"
	browserTraceToAPIListCoverage                = "list_api_coverage_reports"
	browserTraceToAPIListArtifacts               = "list_api_artifacts"
	browserTraceToAPISaveTraceRun                = persistbrowsertrace.BrowserTraceSaveTraceRunStorageHostOperation
	browserTraceToAPISaveCandidate               = persistbrowsertrace.BrowserTraceSaveAPICandidateStorageHostOperation
	browserTraceToAPISaveSchema                  = persistbrowsertrace.BrowserTraceSaveAPICandidateSchemaStorageHostOperation
	browserTraceToAPISaveValidation              = persistbrowsertrace.BrowserTraceSaveAPIValidationStorageHostOperation
	browserTraceToAPISaveCoverage                = persistbrowsertrace.BrowserTraceSaveAPICoverageStorageHostOperation
	browserTraceToAPISaveArtifact                = persistbrowsertrace.BrowserTraceSaveAPIArtifactStorageHostOperation
	browserTraceToAPIFindCandidateByID           = "find_api_candidate_by_id"
	browserTraceToAPIFindValidationByID          = "find_api_candidate_validation_result_by_id"
	browserTraceToAPICreateArtifactWithIntent    = persistbrowsertrace.BrowserTraceCreateAPIArtifactWithPublicationIntentOperation
	browserTraceToAPISupersedeArtifactWithIntent = persistbrowsertrace.BrowserTraceSupersedeAPIArtifactWithPublicationIntentOperation
	browserTraceToAPIListPublicationIntents      = "list_api_artifact_publication_intents"
	browserTraceToAPIListSupersessionFacts       = "list_api_artifact_supersession_facts"
)

// BrowserTraceToAPIGroupOwner is the closed read/write surface consumed by the
// Viewer and runtime review_candidate path. Discovery and publication execution
// remain in CORE; this owner exposes durable DTOs and its bounded outbox reads.
type BrowserTraceToAPIGroupOwner interface {
	ListTraceRuns(context.Context, int) ([]domaintrace.TraceRun, error)
	ListAPICandidates(context.Context, int) ([]domaintrace.APICandidate, error)
	ListAPICandidateSchemas(context.Context, int) ([]domaintrace.APICandidateSchema, error)
	ListAPICandidateValidationResults(context.Context, int) ([]domaintrace.APICandidateValidationResult, error)
	ListAPICoverageReports(context.Context, int) ([]domaintrace.APICoverageReport, error)
	ListAPIArtifacts(context.Context, int) ([]domaintrace.APIArtifact, error)
	SaveTraceRun(context.Context, domaintrace.TraceRun) error
	SaveAPICandidate(context.Context, domaintrace.APICandidate) error
	SaveAPICandidateSchema(context.Context, domaintrace.APICandidateSchema) error
	SaveAPICandidateValidationResult(context.Context, domaintrace.APICandidateValidationResult) error
	SaveAPICoverageReport(context.Context, domaintrace.APICoverageReport) error
	SaveAPIArtifact(context.Context, domaintrace.APIArtifact) error
	FindAPICandidateByID(context.Context, string) (domaintrace.APICandidate, bool, error)
	FindAPICandidateValidationResultByID(context.Context, string) (domaintrace.APICandidateValidationResult, bool, error)
	CreateAPIArtifactWithPublicationIntent(context.Context, domaintrace.APIArtifact, modulecore.EventEnvelope) error
	SupersedeAPIArtifactWithPublicationIntent(context.Context, modulecore.ArtifactID, modulecore.ArtifactID, modulecore.EventEnvelope) error
	ListAPIArtifactPublicationIntents(context.Context, modulecore.ArtifactID, int) ([]modulecore.EventEnvelope, error)
	ListAPIArtifactSupersessionFacts(context.Context, modulecore.ArtifactID, int) ([]modulecore.EventEnvelope, error)
}

type browserTraceToAPIRecoveryOwner interface {
	SaveTraceRunForStorageHostOperation(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, domaintrace.TraceRun) error
	SaveAPICandidateForStorageHostOperation(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, domaintrace.APICandidate) error
	SaveAPICandidateSchemaForStorageHostOperation(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, domaintrace.APICandidateSchema) error
	SaveAPICandidateValidationResultForStorageHostOperation(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, domaintrace.APICandidateValidationResult) error
	SaveAPICoverageReportForStorageHostOperation(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, domaintrace.APICoverageReport) error
	SaveAPIArtifactForStorageHostOperation(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, domaintrace.APIArtifact) error
	CreateAPIArtifactWithPublicationIntentForStorageHostOperation(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, domaintrace.APIArtifact, modulecore.EventEnvelope) error
	SupersedeAPIArtifactWithPublicationIntentForStorageHostOperation(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, modulecore.ArtifactID, modulecore.ArtifactID, modulecore.EventEnvelope) error
	LookupBrowserTraceStorageHostOperationReceipt(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, persistbrowsertrace.BrowserTraceStorageHostEffect) (json.RawMessage, bool, error)
}

type browserTraceToAPIListPayload struct {
	Limit int `json:"limit"`
}

type browserTraceToAPIItemPayload[T any] struct {
	Item T `json:"item"`
}

type browserTraceToAPICandidateIDPayload struct {
	CandidateID string `json:"candidate_id"`
}

type browserTraceToAPIValidationIDPayload struct {
	ValidationID string `json:"validation_id"`
}

type browserTraceToAPIArtifactPagePayload struct {
	After modulecore.ArtifactID `json:"after,omitempty"`
	Limit int                   `json:"limit"`
}

type browserTraceToAPICreatePayload struct {
	Item   domaintrace.APIArtifact  `json:"item"`
	Intent modulecore.EventEnvelope `json:"intent"`
}

type browserTraceToAPISupersedePayload struct {
	PredecessorID modulecore.ArtifactID    `json:"predecessor_id"`
	SuccessorID   modulecore.ArtifactID    `json:"successor_id"`
	Fact          modulecore.EventEnvelope `json:"fact"`
}

type browserTraceToAPIItemsResult[T any] struct {
	Items []T `json:"items"`
}

type browserTraceToAPIItemResult[T any] struct {
	Found bool `json:"found"`
	Item  *T   `json:"item,omitempty"`
}

func RegisterBrowserTraceToAPIGroup(handler *Handler, owner BrowserTraceToAPIGroupOwner) error {
	if handler == nil || nilBrowserTraceToAPIValue(owner) {
		return errors.New("storagehost: browser trace to api group needs a handler and owner")
	}
	recovery, ok := owner.(browserTraceToAPIRecoveryOwner)
	if !ok || nilBrowserTraceToAPIValue(recovery) {
		return errors.New("storagehost: browser trace to api owner lacks durable operation receipts")
	}
	if err := registerBrowserTraceToAPIReads(handler, owner); err != nil {
		return err
	}
	return registerBrowserTraceToAPIMutations(handler, owner, recovery)
}

func registerBrowserTraceToAPIReads(handler *Handler, owner BrowserTraceToAPIGroupOwner) error {
	reads := []struct {
		op string
		fn OperationFunc
	}{
		{browserTraceToAPIListTraceRuns, browserTraceToAPIList(owner.ListTraceRuns, domaintrace.ValidateTraceRun)},
		{browserTraceToAPIListCandidates, browserTraceToAPIList(owner.ListAPICandidates, domaintrace.ValidateAPICandidate)},
		{browserTraceToAPIListSchemas, browserTraceToAPIList(owner.ListAPICandidateSchemas, domaintrace.ValidateAPICandidateSchema)},
		{browserTraceToAPIListValidations, browserTraceToAPIList(owner.ListAPICandidateValidationResults, domaintrace.ValidateAPICandidateValidationResult)},
		{browserTraceToAPIListCoverage, browserTraceToAPIList(owner.ListAPICoverageReports, domaintrace.ValidateAPICoverageReport)},
		{browserTraceToAPIListArtifacts, browserTraceToAPIList(owner.ListAPIArtifacts, domaintrace.ValidateAPIArtifact)},
	}
	for _, read := range reads {
		if err := handler.Register(GroupBrowserTraceToAPI, read.op, false, read.fn); err != nil {
			return err
		}
	}
	if err := handler.Register(GroupBrowserTraceToAPI, browserTraceToAPIFindCandidateByID, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload browserTraceToAPICandidateIDPayload
		if decodeBrowserTraceToAPIPayload(raw, &payload) != nil || !validBrowserTraceToAPIID(payload.CandidateID) {
			return nil, browserTraceToAPISchemaError(browserTraceToAPIFindCandidateByID)
		}
		item, found, err := owner.FindAPICandidateByID(ctx, payload.CandidateID)
		if err != nil || (found && (item.CandidateID != payload.CandidateID || domaintrace.ValidateAPICandidate(item) != nil)) {
			return nil, browserTraceToAPIStoreError(browserTraceToAPIFindCandidateByID)
		}
		return browserTraceToAPIItemResult[domaintrace.APICandidate]{Found: found, Item: browserTraceToAPIPtr(item, found)}, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupBrowserTraceToAPI, browserTraceToAPIFindValidationByID, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload browserTraceToAPIValidationIDPayload
		if decodeBrowserTraceToAPIPayload(raw, &payload) != nil || !validBrowserTraceToAPIID(payload.ValidationID) {
			return nil, browserTraceToAPISchemaError(browserTraceToAPIFindValidationByID)
		}
		item, found, err := owner.FindAPICandidateValidationResultByID(ctx, payload.ValidationID)
		if err != nil || (found && (item.ValidationID != payload.ValidationID || domaintrace.ValidateAPICandidateValidationResult(item) != nil)) {
			return nil, browserTraceToAPIStoreError(browserTraceToAPIFindValidationByID)
		}
		return browserTraceToAPIItemResult[domaintrace.APICandidateValidationResult]{Found: found, Item: browserTraceToAPIPtr(item, found)}, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupBrowserTraceToAPI, browserTraceToAPIListPublicationIntents, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload browserTraceToAPIArtifactPagePayload
		if decodeBrowserTraceToAPIPayload(raw, &payload) != nil || !validBrowserTraceToAPIPage(payload.After, payload.Limit) {
			return nil, browserTraceToAPISchemaError(browserTraceToAPIListPublicationIntents)
		}
		items, err := owner.ListAPIArtifactPublicationIntents(ctx, payload.After, payload.Limit)
		if err != nil || len(items) > browserTraceToAPIEffectivePageLimit(payload.Limit) {
			return nil, browserTraceToAPIStoreError(browserTraceToAPIListPublicationIntents)
		}
		if !browserTraceToAPIEventCursorAdvances(items, payload.After) {
			return nil, browserTraceToAPIStoreError(browserTraceToAPIListPublicationIntents)
		}
		for _, item := range items {
			if domaintrace.ValidatePersistedAPIArtifactPublicationIntent(item) != nil {
				return nil, browserTraceToAPIStoreError(browserTraceToAPIListPublicationIntents)
			}
		}
		return browserTraceToAPIItemsResult[modulecore.EventEnvelope]{Items: items}, nil
	}); err != nil {
		return err
	}
	return handler.Register(GroupBrowserTraceToAPI, browserTraceToAPIListSupersessionFacts, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload browserTraceToAPIArtifactPagePayload
		if decodeBrowserTraceToAPIPayload(raw, &payload) != nil || !validBrowserTraceToAPIPage(payload.After, payload.Limit) {
			return nil, browserTraceToAPISchemaError(browserTraceToAPIListSupersessionFacts)
		}
		items, err := owner.ListAPIArtifactSupersessionFacts(ctx, payload.After, payload.Limit)
		if err != nil || len(items) > browserTraceToAPIEffectivePageLimit(payload.Limit) {
			return nil, browserTraceToAPIStoreError(browserTraceToAPIListSupersessionFacts)
		}
		if !browserTraceToAPIEventCursorAdvances(items, payload.After) {
			return nil, browserTraceToAPIStoreError(browserTraceToAPIListSupersessionFacts)
		}
		for _, item := range items {
			if domaintrace.ValidatePersistedAPIArtifactSupersessionFact(item) != nil {
				return nil, browserTraceToAPIStoreError(browserTraceToAPIListSupersessionFacts)
			}
		}
		return browserTraceToAPIItemsResult[modulecore.EventEnvelope]{Items: items}, nil
	})
}

func browserTraceToAPIList[T any](list func(context.Context, int) ([]T, error), validate func(T) error) OperationFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload browserTraceToAPIListPayload
		if decodeBrowserTraceToAPIPayload(raw, &payload) != nil || !validBrowserTraceToAPIListLimit(payload.Limit) {
			return nil, browserTraceToAPISchemaError("list")
		}
		items, err := list(ctx, payload.Limit)
		if err != nil || len(items) > browserTraceToAPIEffectiveListLimit(payload.Limit) {
			return nil, browserTraceToAPIStoreError("list")
		}
		for _, item := range items {
			if validate(item) != nil {
				return nil, browserTraceToAPIStoreError("list")
			}
		}
		return browserTraceToAPIItemsResult[T]{Items: items}, nil
	}
}

func registerBrowserTraceToAPIMutations(handler *Handler, owner BrowserTraceToAPIGroupOwner, recovery browserTraceToAPIRecoveryOwner) error {
	if err := registerBrowserTraceToAPISave(handler, browserTraceToAPISaveTraceRun, recovery, recovery.SaveTraceRunForStorageHostOperation, domaintrace.ValidateTraceRun, func(v domaintrace.TraceRun) persistbrowsertrace.BrowserTraceStorageHostEffect {
		return persistbrowsertrace.BrowserTraceStorageHostEffect{Kind: persistbrowsertrace.BrowserTraceStorageHostTraceRunEffect, ID: string(v.RunID)}
	}); err != nil {
		return err
	}
	if err := registerBrowserTraceToAPISave(handler, browserTraceToAPISaveCandidate, recovery, recovery.SaveAPICandidateForStorageHostOperation, domaintrace.ValidateAPICandidate, func(v domaintrace.APICandidate) persistbrowsertrace.BrowserTraceStorageHostEffect {
		return persistbrowsertrace.BrowserTraceStorageHostEffect{Kind: persistbrowsertrace.BrowserTraceStorageHostCandidateEffect, ID: v.CandidateID}
	}); err != nil {
		return err
	}
	if err := registerBrowserTraceToAPISave(handler, browserTraceToAPISaveSchema, recovery, recovery.SaveAPICandidateSchemaForStorageHostOperation, domaintrace.ValidateAPICandidateSchema, func(v domaintrace.APICandidateSchema) persistbrowsertrace.BrowserTraceStorageHostEffect {
		return persistbrowsertrace.BrowserTraceStorageHostEffect{Kind: persistbrowsertrace.BrowserTraceStorageHostSchemaEffect, ID: v.SchemaID}
	}); err != nil {
		return err
	}
	if err := registerBrowserTraceToAPISave(handler, browserTraceToAPISaveValidation, recovery, recovery.SaveAPICandidateValidationResultForStorageHostOperation, domaintrace.ValidateAPICandidateValidationResult, func(v domaintrace.APICandidateValidationResult) persistbrowsertrace.BrowserTraceStorageHostEffect {
		return persistbrowsertrace.BrowserTraceStorageHostEffect{Kind: persistbrowsertrace.BrowserTraceStorageHostValidationEffect, ID: v.ValidationID}
	}); err != nil {
		return err
	}
	if err := registerBrowserTraceToAPISave(handler, browserTraceToAPISaveCoverage, recovery, recovery.SaveAPICoverageReportForStorageHostOperation, domaintrace.ValidateAPICoverageReport, func(v domaintrace.APICoverageReport) persistbrowsertrace.BrowserTraceStorageHostEffect {
		return persistbrowsertrace.BrowserTraceStorageHostEffect{Kind: persistbrowsertrace.BrowserTraceStorageHostCoverageEffect, ID: string(v.ArtifactID)}
	}); err != nil {
		return err
	}
	if err := registerBrowserTraceToAPISave(handler, browserTraceToAPISaveArtifact, recovery, recovery.SaveAPIArtifactForStorageHostOperation, domaintrace.ValidateAPIArtifact, func(v domaintrace.APIArtifact) persistbrowsertrace.BrowserTraceStorageHostEffect {
		return persistbrowsertrace.BrowserTraceStorageHostEffect{Kind: persistbrowsertrace.BrowserTraceStorageHostArtifactEffect, ID: string(v.ArtifactID)}
	}); err != nil {
		return err
	}
	if err := registerBrowserTraceToAPICreateArtifact(handler, recovery); err != nil {
		return err
	}
	return registerBrowserTraceToAPISupersedeArtifact(handler, recovery)
}

func registerBrowserTraceToAPISave[T any](handler *Handler, operation string, recovery browserTraceToAPIRecoveryOwner, save func(context.Context, persistbrowsertrace.BrowserTraceStorageHostOperationIdentity, T) error, validate func(T) error, effectFor func(T) persistbrowsertrace.BrowserTraceStorageHostEffect) error {
	return handler.RegisterRecoverable(GroupBrowserTraceToAPI, operation, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload browserTraceToAPIItemPayload[T]
		if decodeBrowserTraceToAPIPayload(mutation.Payload, &payload) != nil || validate(payload.Item) != nil {
			return nil, ownerRolledBack(browserTraceToAPISchemaError(operation))
		}
		identity := browserTraceToAPIOperationIdentity(mutation, operation)
		if err := save(ctx, identity, payload.Item); err != nil {
			return nil, browserTraceToAPIOwnerMutationError(operation, err)
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload browserTraceToAPIItemPayload[T]
		if decodeBrowserTraceToAPIPayload(mutation.Payload, &payload) != nil || validate(payload.Item) != nil {
			return UnknownOutcome(), nil
		}
		identity := browserTraceToAPIOperationIdentity(mutation, operation)
		result, found, err := recovery.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effectFor(payload.Item))
		return browserTraceToAPIReceiptDecision(result, found, err)
	})
}

func registerBrowserTraceToAPICreateArtifact(handler *Handler, recovery browserTraceToAPIRecoveryOwner) error {
	return handler.RegisterRecoverable(GroupBrowserTraceToAPI, browserTraceToAPICreateArtifactWithIntent, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload browserTraceToAPICreatePayload
		if decodeBrowserTraceToAPIPayload(mutation.Payload, &payload) != nil || domaintrace.ValidateAPIArtifact(payload.Item) != nil || domaintrace.ValidateAPIArtifactPublicationIntent(payload.Item, payload.Intent) != nil {
			return nil, ownerRolledBack(browserTraceToAPISchemaError(browserTraceToAPICreateArtifactWithIntent))
		}
		identity := browserTraceToAPIOperationIdentity(mutation, browserTraceToAPICreateArtifactWithIntent)
		if err := recovery.CreateAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, payload.Item, payload.Intent); err != nil {
			return nil, browserTraceToAPIOwnerMutationError(browserTraceToAPICreateArtifactWithIntent, err)
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload browserTraceToAPICreatePayload
		if decodeBrowserTraceToAPIPayload(mutation.Payload, &payload) != nil || domaintrace.ValidateAPIArtifact(payload.Item) != nil || domaintrace.ValidateAPIArtifactPublicationIntent(payload.Item, payload.Intent) != nil {
			return UnknownOutcome(), nil
		}
		identity := browserTraceToAPIOperationIdentity(mutation, browserTraceToAPICreateArtifactWithIntent)
		effect := persistbrowsertrace.BrowserTraceStorageHostEffect{Kind: persistbrowsertrace.BrowserTraceStorageHostArtifactCreationEffect, ID: string(payload.Item.ArtifactID)}
		result, found, err := recovery.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect)
		return browserTraceToAPIReceiptDecision(result, found, err)
	})
}

func registerBrowserTraceToAPISupersedeArtifact(handler *Handler, recovery browserTraceToAPIRecoveryOwner) error {
	return handler.RegisterRecoverable(GroupBrowserTraceToAPI, browserTraceToAPISupersedeArtifactWithIntent, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload browserTraceToAPISupersedePayload
		if decodeBrowserTraceToAPIPayload(mutation.Payload, &payload) != nil || modulecore.ValidateArtifactSupersession(payload.PredecessorID, payload.SuccessorID) != nil || domaintrace.ValidatePersistedAPIArtifactSupersessionFact(payload.Fact) != nil || payload.Fact.ArtifactID != payload.PredecessorID {
			return nil, ownerRolledBack(browserTraceToAPISchemaError(browserTraceToAPISupersedeArtifactWithIntent))
		}
		identity := browserTraceToAPIOperationIdentity(mutation, browserTraceToAPISupersedeArtifactWithIntent)
		if err := recovery.SupersedeAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, payload.PredecessorID, payload.SuccessorID, payload.Fact); err != nil {
			return nil, browserTraceToAPIOwnerMutationError(browserTraceToAPISupersedeArtifactWithIntent, err)
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload browserTraceToAPISupersedePayload
		if decodeBrowserTraceToAPIPayload(mutation.Payload, &payload) != nil || modulecore.ValidateArtifactSupersession(payload.PredecessorID, payload.SuccessorID) != nil || domaintrace.ValidatePersistedAPIArtifactSupersessionFact(payload.Fact) != nil || payload.Fact.ArtifactID != payload.PredecessorID {
			return UnknownOutcome(), nil
		}
		identity := browserTraceToAPIOperationIdentity(mutation, browserTraceToAPISupersedeArtifactWithIntent)
		effect := persistbrowsertrace.BrowserTraceStorageHostEffect{Kind: persistbrowsertrace.BrowserTraceStorageHostArtifactSupersessionEffect, ID: string(payload.PredecessorID)}
		result, found, err := recovery.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect)
		return browserTraceToAPIReceiptDecision(result, found, err)
	})
}

func browserTraceToAPIOperationIdentity(mutation MutationMetadata, operation string) persistbrowsertrace.BrowserTraceStorageHostOperationIdentity {
	return persistbrowsertrace.BrowserTraceStorageHostOperationIdentity{
		OpID: mutation.OpID, Operation: operation, PayloadSHA256: payloadHash(GroupBrowserTraceToAPI, operation, mutation.Payload),
		WriterGeneration: mutation.JournalGeneration,
	}
}

func browserTraceToAPIReceiptDecision(result json.RawMessage, found bool, err error) (ReconcileDecision, error) {
	if err != nil {
		return UnknownOutcome(), nil
	}
	if !found {
		return ConfirmedNotCommitted(), nil
	}
	if !bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
		return UnknownOutcome(), nil
	}
	return Committed(nil), nil
}

func browserTraceToAPIOwnerMutationError(operation string, err error) error {
	storeErr := browserTraceToAPIStoreError(operation)
	if persistbrowsertrace.ArtifactTransactionRollbackConfirmed(err) {
		return ownerRolledBack(storeErr)
	}
	return storeErr
}

func decodeBrowserTraceToAPIPayload(raw []byte, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > browserTraceToAPIMaxPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("browser trace to api payload rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("browser trace to api payload contains trailing value")
		}
		return err
	}
	return nil
}

func validBrowserTraceToAPIListLimit(limit int) bool {
	return limit >= 0 && limit <= browserTraceToAPIMaxList
}

func validBrowserTraceToAPIPage(after modulecore.ArtifactID, limit int) bool {
	return limit >= 0 && limit <= browserTraceToAPIMaxPage && (after == "" || after.Validate() == nil)
}

func browserTraceToAPIEffectiveListLimit(requested int) int {
	if requested == 0 {
		return browserTraceToAPIDefaultList
	}
	return requested
}

func browserTraceToAPIEffectivePageLimit(requested int) int {
	if requested == 0 {
		return browserTraceToAPIDefaultList
	}
	return requested
}

func browserTraceToAPIEventCursorAdvances(items []modulecore.EventEnvelope, after modulecore.ArtifactID) bool {
	previous := after
	for _, item := range items {
		if item.ArtifactID <= previous {
			return false
		}
		previous = item.ArtifactID
	}
	return true
}

func validBrowserTraceToAPIID(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value) && value == string(bytes.TrimSpace([]byte(value))) && !bytes.ContainsRune([]byte(value), '\x00')
}

func browserTraceToAPISchemaError(operation string) *Error {
	return NewError(ErrorCodeSchemaRejected, "browser trace to api "+operation+" request rejected")
}

func browserTraceToAPIStoreError(operation string) *Error {
	return NewError(ErrorCodeStoreUnavailable, "browser trace to api "+operation+" failed")
}

func browserTraceToAPIPtr[T any](item T, found bool) *T {
	if !found {
		return nil
	}
	return &item
}

func nilBrowserTraceToAPIValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// BrowserTraceToAPIClient implements the Viewer-facing typed store remotely.
// Client.Call retains an unresolved exact group/op/payload identity, so a
// repeated call after a lost response resolves the same owner operation.
type BrowserTraceToAPIClient struct{ client *Client }

func NewBrowserTraceToAPIClient(client *Client) *BrowserTraceToAPIClient {
	return &BrowserTraceToAPIClient{client: client}
}

func (client *BrowserTraceToAPIClient) Close() error { return nil }

func (client *BrowserTraceToAPIClient) call(ctx context.Context, operation string, payload, result any) error {
	if client == nil || client.client == nil {
		return NewError(ErrorCodeUnreachable, "browser trace to api storage host is unavailable")
	}
	return client.client.Call(ctx, GroupBrowserTraceToAPI, operation, payload, result)
}

func (client *BrowserTraceToAPIClient) ListTraceRuns(ctx context.Context, limit int) ([]domaintrace.TraceRun, error) {
	return browserTraceToAPIClientList[domaintrace.TraceRun](ctx, client, browserTraceToAPIListTraceRuns, limit, domaintrace.ValidateTraceRun)
}

func (client *BrowserTraceToAPIClient) ListAPICandidates(ctx context.Context, limit int) ([]domaintrace.APICandidate, error) {
	return browserTraceToAPIClientList[domaintrace.APICandidate](ctx, client, browserTraceToAPIListCandidates, limit, domaintrace.ValidateAPICandidate)
}

func (client *BrowserTraceToAPIClient) ListAPICandidateSchemas(ctx context.Context, limit int) ([]domaintrace.APICandidateSchema, error) {
	return browserTraceToAPIClientList[domaintrace.APICandidateSchema](ctx, client, browserTraceToAPIListSchemas, limit, domaintrace.ValidateAPICandidateSchema)
}

func (client *BrowserTraceToAPIClient) ListAPICandidateValidationResults(ctx context.Context, limit int) ([]domaintrace.APICandidateValidationResult, error) {
	return browserTraceToAPIClientList[domaintrace.APICandidateValidationResult](ctx, client, browserTraceToAPIListValidations, limit, domaintrace.ValidateAPICandidateValidationResult)
}

func (client *BrowserTraceToAPIClient) ListAPICoverageReports(ctx context.Context, limit int) ([]domaintrace.APICoverageReport, error) {
	return browserTraceToAPIClientList[domaintrace.APICoverageReport](ctx, client, browserTraceToAPIListCoverage, limit, domaintrace.ValidateAPICoverageReport)
}

func (client *BrowserTraceToAPIClient) ListAPIArtifacts(ctx context.Context, limit int) ([]domaintrace.APIArtifact, error) {
	return browserTraceToAPIClientList[domaintrace.APIArtifact](ctx, client, browserTraceToAPIListArtifacts, limit, domaintrace.ValidateAPIArtifact)
}

func browserTraceToAPIClientList[T any](ctx context.Context, client *BrowserTraceToAPIClient, operation string, limit int, validate func(T) error) ([]T, error) {
	if !validBrowserTraceToAPIListLimit(limit) {
		return nil, browserTraceToAPISchemaError(operation)
	}
	var result browserTraceToAPIItemsResult[T]
	if err := client.call(ctx, operation, browserTraceToAPIListPayload{Limit: limit}, &result); err != nil {
		return nil, err
	}
	if len(result.Items) > browserTraceToAPIEffectiveListLimit(limit) {
		return nil, browserTraceToAPIStoreError(operation)
	}
	for _, item := range result.Items {
		if validate(item) != nil {
			return nil, browserTraceToAPIStoreError(operation)
		}
	}
	if result.Items == nil {
		result.Items = []T{}
	}
	return result.Items, nil
}

func (client *BrowserTraceToAPIClient) SaveTraceRun(ctx context.Context, item domaintrace.TraceRun) error {
	return browserTraceToAPIClientSave(ctx, client, browserTraceToAPISaveTraceRun, item, domaintrace.ValidateTraceRun)
}

func (client *BrowserTraceToAPIClient) SaveAPICandidate(ctx context.Context, item domaintrace.APICandidate) error {
	return browserTraceToAPIClientSave(ctx, client, browserTraceToAPISaveCandidate, item, domaintrace.ValidateAPICandidate)
}

func (client *BrowserTraceToAPIClient) SaveAPICandidateSchema(ctx context.Context, item domaintrace.APICandidateSchema) error {
	return browserTraceToAPIClientSave(ctx, client, browserTraceToAPISaveSchema, item, domaintrace.ValidateAPICandidateSchema)
}

func (client *BrowserTraceToAPIClient) SaveAPICandidateValidationResult(ctx context.Context, item domaintrace.APICandidateValidationResult) error {
	return browserTraceToAPIClientSave(ctx, client, browserTraceToAPISaveValidation, item, domaintrace.ValidateAPICandidateValidationResult)
}

func (client *BrowserTraceToAPIClient) SaveAPICoverageReport(ctx context.Context, item domaintrace.APICoverageReport) error {
	return browserTraceToAPIClientSave(ctx, client, browserTraceToAPISaveCoverage, item, domaintrace.ValidateAPICoverageReport)
}

func (client *BrowserTraceToAPIClient) SaveAPIArtifact(ctx context.Context, item domaintrace.APIArtifact) error {
	return browserTraceToAPIClientSave(ctx, client, browserTraceToAPISaveArtifact, item, domaintrace.ValidateAPIArtifact)
}

func browserTraceToAPIClientSave[T any](ctx context.Context, client *BrowserTraceToAPIClient, operation string, item T, validate func(T) error) error {
	if err := validate(item); err != nil {
		return browserTraceToAPISchemaError(operation)
	}
	return client.call(ctx, operation, browserTraceToAPIItemPayload[T]{Item: item}, nil)
}

func (client *BrowserTraceToAPIClient) FindAPICandidateByID(ctx context.Context, candidateID string) (domaintrace.APICandidate, bool, error) {
	var result browserTraceToAPIItemResult[domaintrace.APICandidate]
	if !validBrowserTraceToAPIID(candidateID) {
		return domaintrace.APICandidate{}, false, browserTraceToAPISchemaError(browserTraceToAPIFindCandidateByID)
	}
	if err := client.call(ctx, browserTraceToAPIFindCandidateByID, browserTraceToAPICandidateIDPayload{CandidateID: candidateID}, &result); err != nil {
		return domaintrace.APICandidate{}, false, err
	}
	if result.Found != (result.Item != nil) || (result.Found && (result.Item.CandidateID != candidateID || domaintrace.ValidateAPICandidate(*result.Item) != nil)) {
		return domaintrace.APICandidate{}, false, browserTraceToAPIStoreError(browserTraceToAPIFindCandidateByID)
	}
	if !result.Found {
		return domaintrace.APICandidate{}, false, nil
	}
	return *result.Item, true, nil
}

func (client *BrowserTraceToAPIClient) FindAPICandidateValidationResultByID(ctx context.Context, validationID string) (domaintrace.APICandidateValidationResult, bool, error) {
	var result browserTraceToAPIItemResult[domaintrace.APICandidateValidationResult]
	if !validBrowserTraceToAPIID(validationID) {
		return domaintrace.APICandidateValidationResult{}, false, browserTraceToAPISchemaError(browserTraceToAPIFindValidationByID)
	}
	if err := client.call(ctx, browserTraceToAPIFindValidationByID, browserTraceToAPIValidationIDPayload{ValidationID: validationID}, &result); err != nil {
		return domaintrace.APICandidateValidationResult{}, false, err
	}
	if result.Found != (result.Item != nil) || (result.Found && (result.Item.ValidationID != validationID || domaintrace.ValidateAPICandidateValidationResult(*result.Item) != nil)) {
		return domaintrace.APICandidateValidationResult{}, false, browserTraceToAPIStoreError(browserTraceToAPIFindValidationByID)
	}
	if !result.Found {
		return domaintrace.APICandidateValidationResult{}, false, nil
	}
	return *result.Item, true, nil
}

func (client *BrowserTraceToAPIClient) CreateAPIArtifactWithPublicationIntent(ctx context.Context, item domaintrace.APIArtifact, intent modulecore.EventEnvelope) error {
	if domaintrace.ValidateAPIArtifact(item) != nil || domaintrace.ValidateAPIArtifactPublicationIntent(item, intent) != nil {
		return browserTraceToAPISchemaError(browserTraceToAPICreateArtifactWithIntent)
	}
	return client.call(ctx, browserTraceToAPICreateArtifactWithIntent, browserTraceToAPICreatePayload{Item: item, Intent: intent}, nil)
}

func (client *BrowserTraceToAPIClient) SupersedeAPIArtifactWithPublicationIntent(ctx context.Context, predecessorID, successorID modulecore.ArtifactID, fact modulecore.EventEnvelope) error {
	if modulecore.ValidateArtifactSupersession(predecessorID, successorID) != nil || domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact) != nil || fact.ArtifactID != predecessorID {
		return browserTraceToAPISchemaError(browserTraceToAPISupersedeArtifactWithIntent)
	}
	return client.call(ctx, browserTraceToAPISupersedeArtifactWithIntent, browserTraceToAPISupersedePayload{PredecessorID: predecessorID, SuccessorID: successorID, Fact: fact}, nil)
}

func (client *BrowserTraceToAPIClient) ListAPIArtifactPublicationIntents(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error) {
	return client.browserTraceToAPIListEventPage(ctx, browserTraceToAPIListPublicationIntents, after, limit, domaintrace.ValidatePersistedAPIArtifactPublicationIntent)
}

func (client *BrowserTraceToAPIClient) ListAPIArtifactSupersessionFacts(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error) {
	return client.browserTraceToAPIListEventPage(ctx, browserTraceToAPIListSupersessionFacts, after, limit, domaintrace.ValidatePersistedAPIArtifactSupersessionFact)
}

func (client *BrowserTraceToAPIClient) browserTraceToAPIListEventPage(ctx context.Context, operation string, after modulecore.ArtifactID, limit int, validate func(modulecore.EventEnvelope) error) ([]modulecore.EventEnvelope, error) {
	if !validBrowserTraceToAPIPage(after, limit) {
		return nil, browserTraceToAPISchemaError(operation)
	}
	var result browserTraceToAPIItemsResult[modulecore.EventEnvelope]
	if err := client.call(ctx, operation, browserTraceToAPIArtifactPagePayload{After: after, Limit: limit}, &result); err != nil {
		return nil, err
	}
	if len(result.Items) > browserTraceToAPIEffectivePageLimit(limit) {
		return nil, browserTraceToAPIStoreError(operation)
	}
	if !browserTraceToAPIEventCursorAdvances(result.Items, after) {
		return nil, browserTraceToAPIStoreError(operation)
	}
	for _, item := range result.Items {
		if validate(item) != nil {
			return nil, browserTraceToAPIStoreError(operation)
		}
	}
	if result.Items == nil {
		result.Items = []modulecore.EventEnvelope{}
	}
	return result.Items, nil
}

var _ BrowserTraceToAPIGroupOwner = (*persistbrowsertrace.SQLiteStore)(nil)
var _ BrowserTraceToAPIGroupOwner = (*BrowserTraceToAPIClient)(nil)
