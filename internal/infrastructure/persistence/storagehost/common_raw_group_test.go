package storagehost

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

const commonRawGroupTestToken = "common-raw-storage-host-test-token"

func TestCommonRawLostResponseReturnsOriginalReceipt(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "conversation.db")
	objectRoot := filepath.Join(root, "raw-objects")
	journalRoot := filepath.Join(root, "journal")
	if err := os.MkdirAll(objectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	input := commonRawGroupTestInput()
	requestID := "raw-intake-lost-response"
	opID := "raw-intake-operation"

	owner1, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner1.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	h1, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: journalRoot})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterCommonRawGroup(h1, owner1, "ren"); err != nil {
		t.Fatal(err)
	}
	srv1 := newStorageHostTestServer(t, h1)
	c1 := newCommonRawTestClient(t, srv1)
	c1.opID = opID
	h1.crashAfterCommitFor = opID
	payload := commonRawIntakePayloadFromDomain(requestID, input)
	var first commonRawIntakeResult
	if err := c1.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, payload, &first); err == nil {
		t.Fatal("first call must lose its response after owner commit")
	}
	srv1.Close()
	if err := h1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner1.Close(); err != nil {
		t.Fatal(err)
	}

	owner2, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner2.Close()
	if err := owner2.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	h2, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: journalRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if err := RegisterCommonRawGroup(h2, owner2, "ren"); err != nil {
		t.Fatal(err)
	}
	srv2 := newStorageHostTestServer(t, h2)
	defer srv2.Close()
	c2 := newCommonRawTestClient(t, srv2)
	c2.opID = opID
	var recovered commonRawIntakeResult
	if err := c2.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, payload, &recovered); err != nil {
		t.Fatalf("recovery call: %v", err)
	}
	if recovered.Receipt.IdempotentReplay {
		t.Fatal("recovery fabricated a semantic replay instead of returning the original false result")
	}
	if !validCommonRawReceipt(recovered.Receipt, requestID) || recovered.Receipt.ManifestSHA256 != input.Manifest.ManifestSHA256 || recovered.Receipt.SourceCount != 1 {
		t.Fatalf("recovered receipt does not match the original intake: %+v", recovered.Receipt)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var manifests, records, events int
	if err := db.QueryRow(`SELECT count(*) FROM l1_raw_source_manifest`).Scan(&manifests); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM l1_raw_record`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM l1_raw_state_event WHERE event_type = 'ingested'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if manifests != 1 || records != 1 || events != 1 {
		t.Fatalf("retry duplicated raw effects: manifests=%d records=%d events=%d", manifests, records, events)
	}
	if recovered.Receipt.Records[0].ObjectRef != "objects/sha256/"+recovered.Receipt.Records[0].ContentSHA256[:2]+"/"+recovered.Receipt.Records[0].ContentSHA256 {
		t.Fatalf("object ref is not relative/content-addressed: %+v", recovered.Receipt.Records[0])
	}
}

func TestCommonRawSubstitutedOwnerResultStaysUnknown(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "conversation.db")
	objectRoot := filepath.Join(root, "raw-objects")
	journalRoot := filepath.Join(root, "journal")
	if err := os.MkdirAll(objectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	input := commonRawGroupTestInput()
	requestID := "raw-intake-proof-tamper"
	opID := "raw-intake-proof-op"
	payload := commonRawIntakePayloadFromDomain(requestID, input)
	owner1, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner1.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	h1, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: journalRoot})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterCommonRawGroup(h1, owner1, "ren"); err != nil {
		t.Fatal(err)
	}
	srv1 := newStorageHostTestServer(t, h1)
	c1 := newCommonRawTestClient(t, srv1)
	c1.opID = opID
	h1.crashAfterCommitFor = opID
	var result commonRawIntakeResult
	if err := c1.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, payload, &result); err == nil {
		t.Fatal("first call must lose its response after owner commit")
	}
	srv1.Close()
	if err := h1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner1.Close(); err != nil {
		t.Fatal(err)
	}
	tamper, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tamper.Exec(`UPDATE l1_common_raw_storagehost_operation_receipt SET result_json = '{}' WHERE op_id = ?`, opID); err != nil {
		tamper.Close()
		t.Fatal(err)
	}
	if err := tamper.Close(); err != nil {
		t.Fatal(err)
	}

	owner2, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner2.Close()
	if err := owner2.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	h2, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: journalRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if err := RegisterCommonRawGroup(h2, owner2, "ren"); err != nil {
		t.Fatal(err)
	}
	srv2 := newStorageHostTestServer(t, h2)
	defer srv2.Close()
	c2 := newCommonRawTestClient(t, srv2)
	c2.opID = opID
	var recovered commonRawIntakeResult
	err = c2.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, payload, &recovered)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("substituted exact-result proof must fail closed: result=%+v err=%v", recovered, err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var manifests, records int
	if err := db.QueryRow(`SELECT count(*) FROM l1_raw_source_manifest`).Scan(&manifests); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM l1_raw_record`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if manifests != 1 || records != 1 {
		t.Fatalf("unknown recovery re-ran or lost the raw effect: manifests=%d records=%d", manifests, records)
	}
}

func TestCommonRawCanonicalMaximumBatchCrossesAuthenticatedRPC(t *testing.T) {
	root := t.TempDir()
	owner, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "conversation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	objectRoot := filepath.Join(root, "raw-objects")
	if err := os.MkdirAll(objectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	if err := RegisterCommonRawGroup(handler, owner, "ren"); err != nil {
		t.Fatal(err)
	}
	server := newStorageHostTestServer(t, handler)
	client := newCommonRawTestClient(t, server)
	record := domainmemory.ChatGPTL3ImportRecord{
		Format: domainmemory.ChatGPTL3ArtifactFormat, ExportID: "maximum-export", EvidenceID: "chatgpt_export:maximum-conversation:maximum-message",
		ConversationID: "maximum-conversation", ConversationTitle: "maximum", ConversationCreatedAt: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), ConversationUpdatedAt: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		NodeID: "maximum-node", OnCurrentBranch: true, MessageID: "maximum-message", MessageCreatedAt: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), Role: "user", ContentType: "text", Content: json.RawMessage(`""`), Metadata: json.RawMessage(`{}`),
	}
	batch := domainmemory.ChatGPTRawImportBatch{ExportID: record.ExportID, ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), SourceCount: 1, SchemaVersion: domainmemory.ChatGPTL3ArtifactFormat, ConverterVersion: "maximum-test", BatchIndex: 0, BatchCount: 1, StartLine: 1, Records: []domainmemory.ChatGPTL3ImportRecord{record}}
	base, err := domainmemory.MarshalChatGPTRawPayload(batch, 0)
	if err != nil {
		t.Fatal(err)
	}
	batch.Records[0].Text = strings.Repeat("x", domainmemory.CommonRawMaxBatchPayloadSize-len(base))
	canonical, err := domainmemory.MarshalChatGPTRawPayload(batch, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != domainmemory.CommonRawMaxBatchPayloadSize {
		t.Fatalf("canonical payload size=%d, want %d", len(canonical), domainmemory.CommonRawMaxBatchPayloadSize)
	}
	payload := commonRawChatGPTBatchPayload{RequestID: "raw-maximum-request", Batch: batch, Apply: false}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelopeJSON, err := json.Marshal(Request{Contract: ContractVersion, OpID: "raw-maximum-operation", Group: GroupCommonRaw, Op: commonRawChatGPTImportOp, Generation: client.Generation(), Payload: payloadJSON})
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopeJSON) <= maxRequestBytes || len(envelopeJSON) > maxCommonRawRequestBytes {
		t.Fatalf("canonical maximum envelope size=%d, want (%d,%d]", len(envelopeJSON), maxRequestBytes, maxCommonRawRequestBytes)
	}
	result, err := NewCommonRawIntakeStoreClient(client).ImportChatGPTRawBatch(commonRawChatGPTTestContext(t, payload.RequestID, "ren"), payload.RequestID, "ren", "ren", batch, false)
	if err != nil {
		t.Fatalf("canonical %d-byte batch: %v", domainmemory.CommonRawMaxBatchPayloadSize, err)
	}
	if result.Validated != 1 || result.BatchIndex != 0 || result.BatchCount != 1 {
		t.Fatalf("maximum batch result=%+v", result)
	}
}

func TestCommonRawLargeEnvelopeAllowanceIsLimitedToChatGPTBatchImport(t *testing.T) {
	root := t.TempDir()
	owner, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "conversation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	handler, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	if err := RegisterCommonRawGroup(handler, owner, "ren"); err != nil {
		t.Fatal(err)
	}
	server := newStorageHostTestServer(t, handler)
	client := newCommonRawTestClient(t, server)
	oversized := map[string]string{"request_id": strings.Repeat("x", maxRequestBytes), "export_id": "export-1"}
	for _, op := range []string{commonRawChatGPTStatusOp, "unsupported"} {
		var result domainmemory.ChatGPTImportView
		err := client.Call(context.Background(), GroupCommonRaw, op, oversized, &result)
		var hostErr *Error
		if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeSchemaRejected {
			t.Fatalf("common_raw/%s oversized envelope error=%v, want schema_rejected", op, err)
		}
	}
}

func TestCommonRawOversizedReceiptIsRecoveredExactly(t *testing.T) {
	const recordCount = 2200
	root := t.TempDir()
	dbPath := filepath.Join(root, "conversation.db")
	objectRoot := filepath.Join(root, "raw-objects")
	if err := os.MkdirAll(objectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	if err := RegisterCommonRawGroup(handler, owner, "ren"); err != nil {
		t.Fatal(err)
	}
	server := newStorageHostTestServer(t, handler)
	client := newCommonRawTestClient(t, server)
	requestID, opID := "oversized-receipt-request", "oversized-receipt-operation"
	client.opID = opID

	content := []byte("record")
	assetContent := []byte("asset")
	asset := domainmemory.CommonRawAsset{
		SourceAssetID: "shared-asset", MediaType: strings.Repeat("m", domainmemory.CommonRawMaxMetadataSize),
		Content: assetContent, ContentSHA256: domainmemory.SHA256Hex(assetContent), Provenance: "export", Rights: "owner", License: "private",
	}
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	records := make([]domainmemory.CommonRawRecord, recordCount)
	for i := range records {
		records[i] = domainmemory.CommonRawRecord{
			SourceRecordID: fmt.Sprintf("record-%05d", i), Sensitivity: domainmemory.CommonRawPrivateSensitivity,
			Role: "user", ContentType: "text/plain", OccurredAt: now, Content: content,
			ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "export", Rights: "owner", License: "private",
			AssetRefs: []string{asset.SourceAssetID},
		}
	}
	manifest := domainmemory.CommonRawManifest{
		ContractVersion: domainmemory.CommonRawContractVersion, SourceType: "test", SourceIdentity: "oversized-receipt",
		SourceCount: recordCount, AssetCount: 1, SchemaVersion: "schema-1", ConverterVersion: "converter-1",
		Sensitivity: domainmemory.CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "export",
	}
	manifest.ManifestSHA256, err = domainmemory.CommonRawInputHash(manifest, records, []domainmemory.CommonRawAsset{asset})
	if err != nil {
		t.Fatal(err)
	}
	input := domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: records, Assets: []domainmemory.CommonRawAsset{asset}}
	payload := commonRawIntakePayloadFromDomain(requestID, input)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(Request{Contract: ContractVersion, OpID: opID, Group: GroupCommonRaw, Op: commonRawIntakeOp, Generation: client.Generation(), Payload: payloadJSON})
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope) > maxRequestBytes {
		t.Fatalf("valid intake request size=%d exceeds the ordinary 32 MiB request bound", len(envelope))
	}

	client.sendHook = func() error {
		server.Close()
		return errConnectionResetAfterSend
	}
	var first commonRawIntakeResult
	err = client.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, payload, &first)
	if !isOutcomeUnknown(err) {
		t.Fatalf("first response was deliberately lost after journal completion; got %v", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	owner2, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner2.Close()
	if err := owner2.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	handler2, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	defer handler2.Close()
	if err := RegisterCommonRawGroup(handler2, owner2, "ren"); err != nil {
		t.Fatal(err)
	}
	server2 := newStorageHostTestServer(t, handler2)
	client2 := newCommonRawTestClient(t, server2)
	client2.opID = opID
	var result commonRawIntakeResult
	if err := client2.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, payload, &result); err != nil {
		t.Fatalf("recover large result after restart: %v", err)
	}
	var replay commonRawIntakeResult
	if err := client2.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, payload, &replay); err != nil {
		t.Fatalf("recover large result from completed response: %v", err)
	}
	if !sameCommonRawIntakeReceipt(result.Receipt, replay.Receipt) {
		t.Fatal("paged replay changed the complete Common Raw receipt")
	}
	db, dbErr := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	defer db.Close()
	var manifests, storedRecords, storedResultBytes int
	var storedResultJSON []byte
	if dbErr := db.QueryRow(`SELECT count(*) FROM l1_raw_source_manifest`).Scan(&manifests); dbErr != nil {
		t.Fatal(dbErr)
	}
	if dbErr := db.QueryRow(`SELECT count(*) FROM l1_raw_record`).Scan(&storedRecords); dbErr != nil {
		t.Fatal(dbErr)
	}
	if dbErr := db.QueryRow(`SELECT length(result_json), result_json FROM l1_common_raw_storagehost_operation_receipt WHERE op_id = ?`, opID).Scan(&storedResultBytes, &storedResultJSON); dbErr != nil {
		t.Fatal(dbErr)
	}
	if manifests != 1 || storedRecords != recordCount || storedResultBytes <= maxRequestBytes || len(result.Receipt.Records) != recordCount {
		t.Fatalf("bounded client must recover the exact oversized committed receipt: manifests=%d records=%d durable_result_bytes=%d decoded_records=%d; request_bytes=%d", manifests, storedRecords, storedResultBytes, len(result.Receipt.Records), len(envelope))
	}
	var durableReceipt domainmemory.CommonRawIntakeReceipt
	if err := json.Unmarshal(storedResultJSON, &durableReceipt); err != nil || !sameCommonRawIntakeReceipt(result.Receipt, durableReceipt) {
		t.Fatalf("restart recovery differed from the complete durable owner receipt: decode_err=%v", err)
	}
	journalEntries, err := client2.journalEntries(context.Background())
	if err != nil {
		t.Fatalf("read journal with bounded oversized result reference: %v", err)
	}
	var journalResultFound bool
	for _, entry := range journalEntries {
		if entry.OpID != opID {
			continue
		}
		journalResultFound = true
		var journalResult commonRawIntakeResult
		if len(entry.Result) <= maxRequestBytes || json.Unmarshal(entry.Result, &journalResult) != nil || !sameCommonRawIntakeReceipt(journalResult.Receipt, durableReceipt) {
			t.Fatal("journalEntries did not hydrate the complete oversized Common Raw result")
		}
	}
	if !journalResultFound {
		t.Fatalf("journalEntries omitted completed operation %s", opID)
	}
	for i, receipt := range result.Receipt.Records {
		if len(receipt.AssetRefs) != 1 || receipt.AssetRefs[0].SourceAssetID != asset.SourceAssetID || receipt.AssetRefs[0].MediaType != asset.MediaType {
			t.Fatalf("receipt asset reference %d lost required identity/metadata", i)
		}
	}
}

func TestCommonRawInvalidUTF8DoesNotReachOwner(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "conversation.db")
	objectRoot := filepath.Join(root, "raw-objects")
	if err := os.MkdirAll(objectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	if err := RegisterCommonRawGroup(handler, owner, "ren"); err != nil {
		t.Fatal(err)
	}
	server := newStorageHostTestServer(t, handler)
	client := newCommonRawTestClient(t, server)

	input := commonRawGroupTestInput()
	input.Records[0].SourceRecordID = "record-\uFFFD-id"
	input.Manifest.ManifestSHA256, err = domainmemory.CommonRawInputHash(input.Manifest, input.Records, input.Assets)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(commonRawIntakePayloadFromDomain("invalid-utf8-request", input))
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(Request{Contract: ContractVersion, OpID: "invalid-utf8-op", Group: GroupCommonRaw, Op: commonRawIntakeOp, Generation: client.Generation(), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	validSource := []byte("record-\uFFFD-id")
	malformedSource := []byte{'r', 'e', 'c', 'o', 'r', 'd', '-', 0xff, '-', 'i', 'd'}
	request = bytes.Replace(request, validSource, malformedSource, 1)
	if bytes.Contains(request, validSource) || !bytes.Contains(request, malformedSource) {
		t.Fatal("test request did not contain the intended raw invalid UTF-8 source ID")
	}
	var response Response
	postErr := client.postRaw(context.Background(), request, &response)
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var manifests, records int
	if err := db.QueryRow(`SELECT count(*) FROM l1_raw_source_manifest`).Scan(&manifests); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM l1_raw_record`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if postErr != nil || response.Error == nil || response.Error.Code != ErrorCodeSchemaRejected || manifests != 0 || records != 0 {
		t.Fatalf("malformed raw UTF-8 must be schema_rejected before owner intake: transport_err=%v response_error=%+v manifests=%d records=%d", postErr, response.Error, manifests, records)
	}
}

func commonRawGroupTestInput() domainmemory.CommonRawIntakeRequest {
	content := []byte(strings.Repeat("immutable raw source ", (domainmemory.CommonRawMaxInlinePayloadSize/20)+10))
	record := domainmemory.CommonRawRecord{SourceRecordID: "record-1", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "text/plain", OccurredAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), Content: content, ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "export", Rights: "owner", License: "private"}
	manifest := domainmemory.CommonRawManifest{ContractVersion: domainmemory.CommonRawContractVersion, SourceType: "test", SourceIdentity: "source-1", SourceCount: 1, SchemaVersion: "schema-1", ConverterVersion: "converter-1", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "export"}
	manifest.ManifestSHA256, _ = domainmemory.CommonRawInputHash(manifest, []domainmemory.CommonRawRecord{record}, nil)
	return domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: []domainmemory.CommonRawRecord{record}}
}

func newStorageHostTestServer(t *testing.T, handler *Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func newCommonRawTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: commonRawGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	return client
}
