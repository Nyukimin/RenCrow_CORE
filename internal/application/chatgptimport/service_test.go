package chatgptimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type serviceStoreCall struct {
	kind      string
	apply     bool
	requestID string
	ownerID   string
	actorID   string
	batch     domainmemory.ChatGPTRawImportBatch
	intake    domainmemory.CommonRawIntakeRequest
	input     domainmemory.ChatGPTImportEventInput
}

type serviceStoreFake struct {
	calls  []serviceStoreCall
	events []domainmemory.ChatGPTImportEvent

	preflightErr             error
	intakeErr                error
	applyErr                 error
	failIntakeAt             int
	failApplyAt              int
	corruptPreflightManifest bool
	corruptApplyReceipt      bool

	completedReplay   bool
	cancelOnPreflight bool
	cancel            context.CancelFunc
	terminalContextOK bool
}

func TestClassifyImportFailureUsesCanonicalChatGPTTaxonomy(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		state domainmemory.ChatGPTImportState
		code  domainmemory.ChatGPTImportErrorCode
	}{
		{name: "bounds", err: ErrBounds, state: domainmemory.ChatGPTImportStateRejected, code: domainmemory.ChatGPTImportErrorTooLarge},
		{name: "manifest", err: ErrInvalidManifest, state: domainmemory.ChatGPTImportStateRejected, code: domainmemory.ChatGPTImportErrorInvalid},
		{name: "artifact", err: ErrInvalidBundle, state: domainmemory.ChatGPTImportStateRejected, code: domainmemory.ChatGPTImportErrorArtifactInvalid},
		{name: "source changed", err: errChatGPTImportSourceChanged, state: domainmemory.ChatGPTImportStateRejected, code: domainmemory.ChatGPTImportErrorSourceChanged},
		{name: "raw root", err: domainmemory.ErrCommonRawRoot, state: domainmemory.ChatGPTImportStateBlocked, code: domainmemory.ChatGPTImportErrorUnavailable},
		{name: "raw unavailable", err: domainmemory.ErrCommonRawUnavailable, state: domainmemory.ChatGPTImportStateBlocked, code: domainmemory.ChatGPTImportErrorUnavailable},
		{name: "wrapped raw unavailable", err: fmt.Errorf("raw store: %w", domainmemory.ErrCommonRawUnavailable), state: domainmemory.ChatGPTImportStateBlocked, code: domainmemory.ChatGPTImportErrorUnavailable},
		{name: "chatgpt unavailable", err: domainmemory.ErrChatGPTImportUnavailable, state: domainmemory.ChatGPTImportStateBlocked, code: domainmemory.ChatGPTImportErrorUnavailable},
		{name: "wrapped chatgpt unavailable", err: fmt.Errorf("import store: %w", domainmemory.ErrChatGPTImportUnavailable), state: domainmemory.ChatGPTImportStateBlocked, code: domainmemory.ChatGPTImportErrorUnavailable},
		{name: "canceled", err: context.Canceled, state: domainmemory.ChatGPTImportStateBlocked, code: domainmemory.ChatGPTImportErrorUnavailable},
		{name: "unknown", err: errors.New("store transaction failed"), state: domainmemory.ChatGPTImportStateBlocked, code: domainmemory.ChatGPTImportErrorInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, code, _ := classifyImportFailure(tt.err)
			if state != tt.state || code != tt.code {
				t.Fatalf("classifyImportFailure() = state=%q code=%q, want state=%q code=%q", state, code, tt.state, tt.code)
			}
		})
	}
}

func TestServiceNilServiceAndStoreAreUnavailableBeforeFailureClassification(t *testing.T) {
	var nilService *Service
	result, err := nilService.Import(context.Background(), ImportRequest{})
	if domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorUnavailable || result.View.State != "" {
		t.Fatalf("nil Service result=%+v err=%v, want unavailable without ledger view", result, err)
	}

	service := NewService(nil, serviceTestOptions())
	result, err = service.Import(context.Background(), ImportRequest{})
	if domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorUnavailable || result.View.State != "" {
		t.Fatalf("nil store result=%+v err=%v, want unavailable without ledger view", result, err)
	}
}

func (s *serviceStoreFake) AppendChatGPTImportEvent(ctx context.Context, input domainmemory.ChatGPTImportEventInput) (domainmemory.ChatGPTImportEvent, error) {
	s.calls = append(s.calls, serviceStoreCall{kind: "ledger", input: input})
	if input.State != domainmemory.ChatGPTImportStateValidating && ctx.Err() != nil {
		s.terminalContextOK = false
	} else if input.State != domainmemory.ChatGPTImportStateValidating {
		s.terminalContextOK = true
	}
	if s.completedReplay && input.State == domainmemory.ChatGPTImportStateValidating {
		return serviceFakeEvent(input, domainmemory.ChatGPTImportStateCompleted), nil
	}
	if input.State == domainmemory.ChatGPTImportStateValidating {
		s.events = append(s.events, serviceFakeEvent(input, input.State))
		return s.events[len(s.events)-1], nil
	}
	s.events = append(s.events, serviceFakeEvent(input, input.State))
	return s.events[len(s.events)-1], nil
}

func (s *serviceStoreFake) IntakeCommonRaw(_ context.Context, requestID, ownerID, actorID string, input domainmemory.CommonRawIntakeRequest) (domainmemory.CommonRawIntakeReceipt, error) {
	s.calls = append(s.calls, serviceStoreCall{kind: "source", requestID: requestID, ownerID: ownerID, actorID: actorID, intake: input})
	if s.failIntakeAt > 0 && countServiceCalls(s.calls, "source") == s.failIntakeAt {
		return domainmemory.CommonRawIntakeReceipt{}, s.intakeErr
	}
	if s.intakeErr != nil && s.failIntakeAt == 0 {
		return domainmemory.CommonRawIntakeReceipt{}, s.intakeErr
	}
	receipt := domainmemory.CommonRawIntakeReceipt{
		RequestID: requestID, ManifestID: domainmemory.DeterministicCommonRawManifestID(ownerID, "user:"+ownerID, input.Manifest.SourceType, input.Manifest.SourceIdentity, input.Manifest.ManifestSHA256), Status: domainmemory.CommonRawStateCompleted,
		ManifestSHA256: input.Manifest.ManifestSHA256, SourceCount: len(input.Records), AssetCount: 0,
		Checkpoint: "completed", Records: make([]domainmemory.CommonRawRecordReceipt, 0, len(input.Records)), CreatedAt: time.Unix(1, 0).UTC(),
	}
	for _, record := range input.Records {
		storageKind, objectRef := commonRawReceiptStorage(record.Content)
		receipt.Records = append(receipt.Records, domainmemory.CommonRawRecordReceipt{
			RawRecordID: domainmemory.DeterministicCommonRawRecordID(ownerID, "user:"+ownerID, input.Manifest.SourceType, input.Manifest.SourceIdentity, record.SourceRecordID, record.ContentSHA256), SourceRecordID: record.SourceRecordID,
			ContentSHA256: record.ContentSHA256, ContentSize: int64(len(record.Content)), StorageKind: storageKind, ObjectRef: objectRef,
		})
	}
	_ = ownerID
	_ = actorID
	return receipt, nil
}

func (s *serviceStoreFake) ImportChatGPTRawBatch(_ context.Context, requestID, ownerID, actorID string, batch domainmemory.ChatGPTRawImportBatch, apply bool) (domainmemory.ChatGPTRawImportResult, error) {
	s.calls = append(s.calls, serviceStoreCall{kind: "message", apply: apply, requestID: requestID, ownerID: ownerID, actorID: actorID, batch: batch})
	if !apply && s.cancelOnPreflight {
		if s.cancel != nil {
			s.cancel()
		}
		return domainmemory.ChatGPTRawImportResult{}, context.Canceled
	}
	if !apply && s.preflightErr != nil {
		return domainmemory.ChatGPTRawImportResult{}, s.preflightErr
	}
	if apply && s.failApplyAt > 0 && countAppliedServiceCalls(s.calls) == s.failApplyAt {
		return domainmemory.ChatGPTRawImportResult{Validated: len(batch.Records)}, s.applyErr
	}
	if apply && s.applyErr != nil && s.failApplyAt == 0 {
		return domainmemory.ChatGPTRawImportResult{}, s.applyErr
	}
	result := domainmemory.ChatGPTRawImportResult{
		Validated: len(batch.Records), ExternalManifestSHA256: batch.ManifestSHA256, ArtifactSHA256: batch.ArtifactSHA256,
		SourceCount: batch.SourceCount, SchemaVersion: batch.SchemaVersion,
		ConverterVersion: batch.ConverterVersion, BatchIndex: batch.BatchIndex, BatchCount: batch.BatchCount,
		StartLine: batch.StartLine, RawImported: len(batch.Records), Projected: len(batch.Records),
		RawRecordIDs: make([]string, len(batch.Records)),
	}
	expected, expectedErr := expectedChatGPTMessageRaw(ownerID, batch)
	if expectedErr != nil {
		return domainmemory.ChatGPTRawImportResult{}, expectedErr
	}
	result.ManifestID = expected.manifestID
	result.InternalManifestSHA256 = expected.manifestSHA
	result.RawRecordIDs = append([]string(nil), expected.rawRecordIDs...)
	result.RawReceipt = domainmemory.CommonRawIntakeReceipt{RequestID: requestID, ManifestID: expected.manifestID, Status: domainmemory.CommonRawStateCompleted, ManifestSHA256: expected.manifestSHA, SourceCount: len(batch.Records), AssetCount: 0, Checkpoint: "completed", CreatedAt: time.Unix(1, 0).UTC(), Records: make([]domainmemory.CommonRawRecordReceipt, 0, len(batch.Records))}
	for _, record := range batch.Records {
		expectedRecord := expected.records[record.EvidenceID]
		receiptRecord := domainmemory.CommonRawRecordReceipt{RawRecordID: expectedRecord.rawRecordID, SourceRecordID: record.EvidenceID, ContentSHA256: expectedRecord.contentSHA, ContentSize: expectedRecord.contentSize, StorageKind: expectedRecord.storageKind}
		receiptRecord.ObjectRef = expectedRecord.objectRef
		result.RawReceipt.Records = append(result.RawReceipt.Records, receiptRecord)
	}
	if !apply {
		result.RawImported = 0
		result.RawReplayed = 0
		result.Projected = 0
		result.Existing = 0
		result.Queued = 0
		result.RawReceipt = domainmemory.CommonRawIntakeReceipt{}
		if s.corruptPreflightManifest {
			result.ManifestID = "wrong-manifest"
		}
	} else if s.corruptApplyReceipt {
		result.RawReceipt.ManifestSHA256 = "wrong-manifest-hash"
	}
	_ = requestID
	_ = ownerID
	_ = actorID
	return result, nil
}

func serviceFakeEvent(input domainmemory.ChatGPTImportEventInput, state domainmemory.ChatGPTImportState) domainmemory.ChatGPTImportEvent {
	bindingSHA, _ := domainmemory.DeterministicChatGPTImportBindingSHA256(input.OwnerID, input.Binding)
	importID := domainmemory.DeterministicChatGPTImportID(input.OwnerID, bindingSHA)
	return domainmemory.ChatGPTImportEvent{
		EventID: domainmemory.DeterministicChatGPTImportEventID(importID, input.RequestID, state), ImportID: importID,
		RequestID: input.RequestID, OwnerID: input.OwnerID, ActorID: input.ActorID, Binding: input.Binding,
		BindingSHA256: bindingSHA, Apply: input.Apply, State: state, Counts: input.Counts, Warnings: []string{},
		ErrorCode: input.ErrorCode, FailureReason: input.FailureReason, AuditReference: input.AuditReference,
		CreatedAt: time.Unix(1, 0).UTC(),
	}
}

func countServiceCalls(calls []serviceStoreCall, kind string) int {
	count := 0
	for _, call := range calls {
		if call.kind == kind {
			count++
		}
	}
	return count
}

func countAppliedServiceCalls(calls []serviceStoreCall) int {
	count := 0
	for _, call := range calls {
		if call.kind == "message" && call.apply {
			count++
		}
	}
	return count
}

func serviceFixtureRequest(fixture bundleFixture) ImportRequest {
	return ImportRequest{
		RequestID: "request-1", OwnerID: "ren", ActorID: "ren",
		StageRoot: fixture.root, ManifestPath: fixture.manifestPath, ArtifactPath: fixture.artifactPath,
	}
}

func serviceTestOptions() ServiceOptions {
	return ServiceOptions{BundleOptions: Options{ChunkBytes: 64}, MaxSourceBatchRecords: 2, MaxMessageBatchRecords: 1}
}

func TestServiceTamperedBundleMakesNoStoreCall(t *testing.T) {
	fixture := newBundleFixture(t)
	rewriteFixtureArtifact(t, &fixture, fixture.entries, []byte("tampered"))
	store := &serviceStoreFake{}
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(context.Background(), serviceFixtureRequest(fixture))
	if err == nil || result.View.State != "" {
		t.Fatalf("Import() result=%+v err=%v, want safe verification failure", result, err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("store calls = %d, want zero", len(store.calls))
	}
	if strings.Contains(err.Error(), fixture.root) || strings.Contains(err.Error(), fixture.manifestPath) || strings.Contains(err.Error(), fixture.artifactPath) {
		t.Fatalf("error exposed staging/input path: %v", err)
	}
}

func TestServiceDryRunStateSequenceAndNoRawWrites(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{}
	request := serviceFixtureRequest(fixture)
	service := NewService(store, serviceTestOptions())
	request.Apply = false
	result, err := service.Import(context.Background(), request)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.View.State != domainmemory.ChatGPTImportStateCompleted || result.View.Counts.RawCount != 0 || result.View.Counts.ProjectionCount != 0 || result.View.Counts.JobCount != 0 {
		t.Fatalf("dry-run view = %+v", result.View)
	}
	if got := serviceEventStates(store.events); !equalServiceStates(got, []domainmemory.ChatGPTImportState{domainmemory.ChatGPTImportStateValidating, domainmemory.ChatGPTImportStateCommitting, domainmemory.ChatGPTImportStateCompleted}) {
		t.Fatalf("state sequence = %v", got)
	}
	for _, call := range store.calls {
		if call.kind == "source" || (call.kind == "message" && call.apply) {
			t.Fatalf("dry-run performed Raw/apply call: %+v", call)
		}
	}
}

func TestServiceApplyPreflightsMessagesBeforeSourceAndKeepsStableSplit(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{}
	request := serviceFixtureRequest(fixture)
	request.Apply = true
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(context.Background(), request)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.View.Counts.SourceCount != fixture.manifest.SourceChunkCount || result.View.Counts.FileCount != 2 || result.View.Counts.ChunkCount != fixture.manifest.SourceChunkCount || result.View.Counts.ObjectCount != fixture.manifest.SourceObjectCount || result.View.Counts.MessageCount != 2 || result.View.Counts.RawCount != fixture.manifest.SourceChunkCount+2 || result.View.Counts.ProjectionCount != 2 || result.View.Counts.JobCount != 1 {
		t.Fatalf("apply counts = %+v", result.View.Counts)
	}
	firstSource := -1
	firstAppliedMessage := -1
	lastPreflight := -1
	for index, call := range store.calls {
		if call.kind == "source" && firstSource < 0 {
			firstSource = index
		}
		if call.kind == "message" && !call.apply {
			lastPreflight = index
		}
		if call.kind == "message" && call.apply && firstAppliedMessage < 0 {
			firstAppliedMessage = index
		}
	}
	if firstSource < 0 || lastPreflight < 0 || firstSource <= lastPreflight || firstAppliedMessage <= firstSource {
		t.Fatalf("call order source=%d lastPreflight=%d firstApplied=%d calls=%+v", firstSource, lastPreflight, firstAppliedMessage, store.calls)
	}
	if result.View.State != domainmemory.ChatGPTImportStateCompleted {
		t.Fatalf("state = %s", result.View.State)
	}
}

func TestServicePreflightConflictRejectsBeforeRaw(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{preflightErr: domainmemory.ErrCommonRawConflict}
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(context.Background(), serviceFixtureRequest(fixture))
	if err == nil || result.View.State != domainmemory.ChatGPTImportStateRejected || result.View.ErrorCode != string(domainmemory.ChatGPTImportErrorConflict) {
		t.Fatalf("result=%+v err=%v, want rejected conflict", result, err)
	}
	if countServiceCalls(store.calls, "source") != 0 || countAppliedServiceCalls(store.calls) != 0 {
		t.Fatalf("Raw calls occurred before conflict: %+v", store.calls)
	}
	if got := serviceEventStates(store.events); !equalServiceStates(got, []domainmemory.ChatGPTImportState{domainmemory.ChatGPTImportStateValidating, domainmemory.ChatGPTImportStateRejected}) {
		t.Fatalf("states = %v", got)
	}
}

func TestServiceRejectsIncompleteMessagePreflightResult(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{corruptPreflightManifest: true}
	request := serviceFixtureRequest(fixture)
	request.Apply = false
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(context.Background(), request)
	if err == nil || result.View.State != domainmemory.ChatGPTImportStateBlocked || result.View.ErrorCode != string(domainmemory.ChatGPTImportErrorUnavailable) {
		t.Fatalf("result=%+v err=%v, want blocked unavailable preflight result", result, err)
	}
	if countServiceCalls(store.calls, "source") != 0 || countAppliedServiceCalls(store.calls) != 0 {
		t.Fatalf("Raw calls occurred after incomplete preflight result: %+v", store.calls)
	}
}

func TestServiceRejectsMismatchedMessageRawReceipt(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{corruptApplyReceipt: true}
	request := serviceFixtureRequest(fixture)
	request.Apply = true
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(context.Background(), request)
	if err == nil || result.View.State != domainmemory.ChatGPTImportStateBlocked || result.View.ErrorCode != string(domainmemory.ChatGPTImportErrorUnavailable) {
		t.Fatalf("result=%+v err=%v, want blocked unavailable mismatched Raw receipt", result, err)
	}
	if result.View.Counts.RawCount != fixture.manifest.SourceChunkCount || result.View.Counts.ProjectionCount != 0 {
		t.Fatalf("partial counts = %+v, want source-only progress", result.View.Counts)
	}
}

func TestServiceRejectsNonCanonicalObjectRawReceipt(t *testing.T) {
	content := json.RawMessage(`{"value":"` + strings.Repeat("x", domainmemory.CommonRawMaxInlinePayloadSize+1) + `"}`)
	batch := domainmemory.ChatGPTRawImportBatch{
		ExportID: strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: strings.Repeat("c", 64),
		SourceCount: 1, SchemaVersion: domainmemory.ChatGPTL3ArtifactFormat, ConverterVersion: domainmemory.ChatGPTImportConverterVersion,
		BatchIndex: 0, BatchCount: 1, StartLine: 1, Records: []domainmemory.ChatGPTL3ImportRecord{{
			Format: domainmemory.ChatGPTL3ArtifactFormat, ExportID: strings.Repeat("a", 64), EvidenceID: "chatgpt_export:conv:m1",
			ConversationID: "conv", ConversationTitle: "title", ConversationCreatedAt: time.Unix(1, 0).UTC(),
			ConversationUpdatedAt: time.Unix(2, 0).UTC(), NodeID: "node", OnCurrentBranch: true,
			MessageID: "m1", MessageCreatedAt: time.Unix(3, 0).UTC(), Role: "user", ContentType: "text",
			Text: "large", Content: content, Metadata: json.RawMessage(`{"source":"test"}`),
		}},
	}
	expected, err := expectedChatGPTMessageRaw("owner", batch)
	if err != nil {
		t.Fatal(err)
	}
	record := expected.records[batch.Records[0].EvidenceID]
	result := domainmemory.ChatGPTRawImportResult{
		Validated: 1, RawImported: 1, Projected: 1, ManifestID: expected.manifestID, RawRecordIDs: append([]string(nil), expected.rawRecordIDs...),
		InternalManifestSHA256: expected.manifestSHA, ExternalManifestSHA256: batch.ManifestSHA256, ArtifactSHA256: batch.ArtifactSHA256,
		SourceCount: batch.SourceCount, SchemaVersion: batch.SchemaVersion, ConverterVersion: batch.ConverterVersion,
		BatchIndex: batch.BatchIndex, BatchCount: batch.BatchCount, StartLine: batch.StartLine,
		RawReceipt: domainmemory.CommonRawIntakeReceipt{
			RequestID: "req", ManifestID: expected.manifestID, Status: domainmemory.CommonRawStateCompleted, ManifestSHA256: expected.manifestSHA,
			SourceCount: 1, Checkpoint: "completed", CreatedAt: time.Unix(4, 0).UTC(), Records: []domainmemory.CommonRawRecordReceipt{{
				RawRecordID: record.rawRecordID, SourceRecordID: batch.Records[0].EvidenceID, ContentSHA256: record.contentSHA,
				ContentSize: record.contentSize, StorageKind: record.storageKind, ObjectRef: "objects/sha256/" + record.contentSHA[:2] + "/wrong-object-ref",
			}},
		},
	}
	if err := verifyMessageResult(result, "req", "owner", batch, 1); err == nil {
		t.Fatal("verifyMessageResult accepted a non-canonical object receipt reference")
	}
}

func TestServiceRejectsNonCanonicalSourceObjectReceipt(t *testing.T) {
	content := []byte(strings.Repeat("y", domainmemory.CommonRawMaxInlinePayloadSize+1))
	contentHash := domainmemory.SHA256Hex(content)
	input := domainmemory.CommonRawIntakeRequest{
		Manifest: domainmemory.CommonRawManifest{
			ContractVersion: domainmemory.CommonRawContractVersion, SourceType: chatGPTImportSourceType, SourceIdentity: "export",
			ManifestSHA256: strings.Repeat("a", 64), SourceCount: 1, SchemaVersion: domainmemory.ChatGPTImportRecordSchema,
			ConverterVersion: domainmemory.ChatGPTImportConverterVersion, Sensitivity: domainmemory.CommonRawPrivateSensitivity,
			Rights: "owner", License: "private", Provenance: "test",
		},
		Records: []domainmemory.CommonRawRecord{{SourceRecordID: "source-1", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "source", ContentType: "application/octet-stream", OccurredAt: time.Unix(0, 0).UTC(), Content: content, ContentSHA256: contentHash, Provenance: "test", Rights: "owner", License: "private"}},
	}
	receipt := domainmemory.CommonRawIntakeReceipt{
		RequestID: "req", ManifestID: domainmemory.DeterministicCommonRawManifestID("owner", "user:owner", input.Manifest.SourceType, input.Manifest.SourceIdentity, input.Manifest.ManifestSHA256),
		Status: domainmemory.CommonRawStateCompleted, ManifestSHA256: input.Manifest.ManifestSHA256, SourceCount: 1, Checkpoint: "completed", CreatedAt: time.Unix(1, 0).UTC(),
		Records: []domainmemory.CommonRawRecordReceipt{{
			RawRecordID:    domainmemory.DeterministicCommonRawRecordID("owner", "user:owner", input.Manifest.SourceType, input.Manifest.SourceIdentity, "source-1", contentHash),
			SourceRecordID: "source-1", ContentSHA256: contentHash, ContentSize: int64(len(content)), StorageKind: domainmemory.CommonRawStorageObject,
			ObjectRef: "objects/sha256/" + contentHash[:2] + "/wrong-object-ref",
		}},
	}
	if err := verifySourceReceipt(receipt, "req", "owner", input); err == nil {
		t.Fatal("verifySourceReceipt accepted a non-canonical object receipt reference")
	}
}

func TestServicePartialSourceFailureBlocksWithPartialCounts(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{failIntakeAt: 2, intakeErr: domainmemory.ErrCommonRawUnavailable}
	request := serviceFixtureRequest(fixture)
	request.Apply = true
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(context.Background(), request)
	if err == nil || result.View.State != domainmemory.ChatGPTImportStateBlocked || result.View.Counts.RawCount != 2 {
		t.Fatalf("result=%+v err=%v, want blocked after one source batch", result, err)
	}
	if result.View.Counts.ProjectionCount != 0 || countAppliedServiceCalls(store.calls) != 0 {
		t.Fatalf("partial source failure counts/calls invalid: %+v calls=%+v", result.View.Counts, store.calls)
	}
}

func TestServiceApplyMessageFailureBlocksWithPartialCounts(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{failApplyAt: 1, applyErr: domainmemory.ErrCommonRawUnavailable}
	request := serviceFixtureRequest(fixture)
	request.Apply = true
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(context.Background(), request)
	if err == nil || result.View.State != domainmemory.ChatGPTImportStateBlocked || result.View.Counts.RawCount != fixture.manifest.SourceChunkCount || result.View.Counts.ProjectionCount != 0 {
		t.Fatalf("result=%+v err=%v, want blocked after source-only progress", result, err)
	}
	if countAppliedServiceCalls(store.calls) != 1 {
		t.Fatalf("applied message calls = %d, want one failed batch", countAppliedServiceCalls(store.calls))
	}
}

func TestServiceCompletedReplayDoesNoWork(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{completedReplay: true}
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(context.Background(), serviceFixtureRequest(fixture))
	if err != nil || !result.IdempotentReplay || result.View.State != domainmemory.ChatGPTImportStateCompleted {
		t.Fatalf("result=%+v err=%v, want completed replay", result, err)
	}
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want validating replay only", len(store.calls))
	}
}

func TestServicePropagatesRequestOwnerActorExactly(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{}
	request := serviceFixtureRequest(fixture)
	request.RequestID, request.OwnerID, request.ActorID = "req-exact", "owner-exact", "actor-exact"
	request.Apply = true
	service := NewService(store, serviceTestOptions())
	if _, err := service.Import(context.Background(), request); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	for _, call := range store.calls {
		if call.kind == "ledger" {
			if call.input.RequestID != request.RequestID || call.input.OwnerID != request.OwnerID || call.input.ActorID != request.ActorID {
				t.Fatalf("ledger scope = %+v", call.input)
			}
		} else if call.requestID != request.RequestID || call.ownerID != request.OwnerID || call.actorID != request.ActorID {
			t.Fatalf("Raw scope = %+v", call)
		}
	}
}

func TestServiceCancellationTerminalizesWithSafeContext(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{cancelOnPreflight: true}
	ctx, cancel := context.WithCancel(context.Background())
	store.cancel = cancel
	request := serviceFixtureRequest(fixture)
	service := NewService(store, serviceTestOptions())
	result, err := service.Import(ctx, request)
	if err == nil || result.View.State != domainmemory.ChatGPTImportStateBlocked || !store.terminalContextOK {
		t.Fatalf("result=%+v err=%v terminalContextOK=%v", result, err, store.terminalContextOK)
	}
	if strings.Contains(err.Error(), fixture.root) {
		t.Fatalf("cancellation error exposed stage path: %v", err)
	}
}

func TestServiceApplyCreatesOneSyntheticRawRecordForZeroByteSource(t *testing.T) {
	fixture := newBundleFixture(t)
	makeZeroByteSourceFixture(t, &fixture)
	store := &serviceStoreFake{}
	request := serviceFixtureRequest(fixture)
	request.Apply = true
	service := NewService(store, serviceTestOptions())
	if _, err := service.Import(context.Background(), request); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	found := false
	for _, call := range store.calls {
		if call.kind != "source" {
			continue
		}
		for _, record := range call.intake.Records {
			if len(record.Content) == 0 && strings.Contains(record.Provenance, "asset.bin") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("zero-byte source did not produce an empty synthetic source record")
	}
}

func TestServiceRejectsOptionsAboveProductionCaps(t *testing.T) {
	fixture := newBundleFixture(t)
	store := &serviceStoreFake{}
	service := NewService(store, ServiceOptions{MaxSourceBatchRecords: 101})
	result, err := service.Import(context.Background(), serviceFixtureRequest(fixture))
	if err == nil || result.View.State != "" || !errors.Is(err, domainmemory.ErrChatGPTImportInvalid) {
		t.Fatalf("result=%+v err=%v, want bounded option rejection", result, err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("store calls = %d, want zero", len(store.calls))
	}
}

func makeZeroByteSourceFixture(t *testing.T, fixture *bundleFixture) {
	t.Helper()
	entries := cloneTestEntries(fixture.entries)
	index := findTestEntry(t, entries, "source-files.jsonl")
	lines := strings.Split(strings.TrimSuffix(string(entries[index].data), "\n"), "\n")
	var source testSourceIndex
	if err := json.Unmarshal([]byte(lines[0]), &source); err != nil {
		t.Fatal(err)
	}
	oldHash := source.Chunks[0].SHA256
	emptyHash := domainmemory.SHA256Hex(nil)
	source.Bytes = 0
	source.SHA256 = emptyHash
	source.Chunks = nil
	encodedSource, _ := json.Marshal(source)
	lines[0] = string(encodedSource)
	entries[index].data = append([]byte(strings.Join(lines, "\n")), '\n')
	for entryIndex, entry := range entries {
		if strings.Contains(entry.header.Name, oldHash) {
			entries = append(entries[:entryIndex], entries[entryIndex+1:]...)
			break
		}
	}
	fixture.manifest.Files[0].Bytes = 0
	fixture.manifest.Files[0].SHA256 = emptyHash
	fixture.manifest.ExportID = testSourceExportID(fixture.manifest.Files)
	fixture.manifest.SourceChunkCount--
	fixture.manifest.SourceObjectCount--
	recordsIndex := findTestEntry(t, entries, "records.jsonl")
	recordLines := strings.Split(strings.TrimSuffix(string(entries[recordsIndex].data), "\n"), "\n")
	for lineIndex := range recordLines {
		var record testArtifactRecord
		if err := json.Unmarshal([]byte(recordLines[lineIndex]), &record); err != nil {
			t.Fatal(err)
		}
		record.ExportID = fixture.manifest.ExportID
		encodedRecord, _ := json.Marshal(record)
		recordLines[lineIndex] = string(encodedRecord)
	}
	entries[recordsIndex].data = append([]byte(strings.Join(recordLines, "\n")), '\n')
	rewriteFixtureArtifact(t, fixture, entries, nil)
}

func serviceEventStates(events []domainmemory.ChatGPTImportEvent) []domainmemory.ChatGPTImportState {
	states := make([]domainmemory.ChatGPTImportState, 0, len(events))
	for _, event := range events {
		states = append(states, event.State)
	}
	return states
}

func equalServiceStates(left, right []domainmemory.ChatGPTImportState) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var _ Store = (*serviceStoreFake)(nil)
