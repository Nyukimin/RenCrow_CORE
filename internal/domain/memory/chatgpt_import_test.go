package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestChatGPTImportErrorTaxonomySentinelsAreBounded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code ChatGPTImportErrorCode
	}{
		{name: "too large", err: ErrChatGPTImportTooLarge, code: ChatGPTImportErrorTooLarge},
		{name: "artifact invalid", err: ErrChatGPTImportArtifactInvalid, code: ChatGPTImportErrorArtifactInvalid},
		{name: "internal", err: ErrChatGPTImportInternal, code: ChatGPTImportErrorInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ChatGPTImportErrorCodeOf(tt.err); got != tt.code {
				t.Fatalf("ChatGPTImportErrorCodeOf() = %q, want %q", got, tt.code)
			}
			wrapped := errors.Join(errors.New("wrapped"), tt.err)
			if !errors.Is(wrapped, tt.err) {
				t.Fatalf("errors.Is() did not preserve %s sentinel", tt.code)
			}
		})
	}
}

func TestChatGPTImportBindingAndIDsAreDeterministic(t *testing.T) {
	binding := testChatGPTImportBinding()
	first, err := binding.SHA256()
	if err != nil {
		t.Fatalf("binding SHA256: %v", err)
	}
	second, err := binding.SHA256()
	if err != nil {
		t.Fatalf("binding SHA256 second call: %v", err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("binding hash is not deterministic: %q vs %q", first, second)
	}
	if got := DeterministicChatGPTImportID("user-a", first); got != DeterministicChatGPTImportID("user-a", first) {
		t.Fatalf("import ID is not deterministic")
	}
	if got := DeterministicChatGPTImportID("user-a", first); got == DeterministicChatGPTImportID("user-b", first) {
		t.Fatalf("import ID is not owner-bound: %q", got)
	}
	ownerBoundA, err := DeterministicChatGPTImportBindingSHA256("user-a", binding)
	if err != nil {
		t.Fatalf("owner-bound binding hash: %v", err)
	}
	ownerBoundB, err := DeterministicChatGPTImportBindingSHA256("user-b", binding)
	if err != nil {
		t.Fatalf("second owner-bound binding hash: %v", err)
	}
	if ownerBoundA == ownerBoundB || len(ownerBoundA) != 64 {
		t.Fatalf("binding hash is not owner-bound: %q vs %q", ownerBoundA, ownerBoundB)
	}
	if got := DeterministicChatGPTImportEventID("import-a", "request-a", ChatGPTImportStateValidating); got != DeterministicChatGPTImportEventID("import-a", "request-a", ChatGPTImportStateValidating) {
		t.Fatalf("event ID is not deterministic")
	}
	if got := DeterministicChatGPTImportEventID("import-a", "request-a", ChatGPTImportStateValidating); got == DeterministicChatGPTImportEventID("import-a", "request-a", ChatGPTImportStateCommitting) {
		t.Fatalf("event ID does not include state")
	}
}

func TestChatGPTImportValidationRejectsHumanWaitAndUnsafeDiagnostics(t *testing.T) {
	input := testChatGPTImportEventInput("request-a", "user-a", ChatGPTImportStateValidating)
	input.State = ChatGPTImportState("pending")
	if err := input.Validate(); err == nil {
		t.Fatal("pending state unexpectedly accepted")
	}

	input = testChatGPTImportEventInput("request-a", "user-a", ChatGPTImportStateRejected)
	input.ErrorCode = "artifact_invalid"
	input.FailureReason = "source/path leaked"
	if err := input.Validate(); err == nil {
		t.Fatal("path-bearing failure reason unexpectedly accepted")
	}
	input.FailureReason = "single-line sanitized reason"
	input.Warnings = []string{"warning\nwith newline"}
	if err := input.Validate(); err == nil {
		t.Fatal("multi-line warning unexpectedly accepted")
	}

	input.Warnings = make([]string, ChatGPTImportMaxWarnings+1)
	for i := range input.Warnings {
		input.Warnings[i] = "bounded warning"
	}
	if err := input.Validate(); err == nil {
		t.Fatal("too many warnings unexpectedly accepted")
	}
}

func TestChatGPTImportViewDoesNotExposeInternalOrRawFields(t *testing.T) {
	view := ChatGPTImportView{
		EventID:        "event-1",
		ImportID:       "import-1",
		RequestID:      "request-1",
		ExportID:       "export-1",
		BindingSHA256:  strings.Repeat("a", 64),
		State:          ChatGPTImportStateCompleted,
		Counts:         ChatGPTImportCounts{RawCount: 1, ProjectionCount: 1},
		Warnings:       []string{"bounded"},
		AuditReference: "audit-1",
	}
	payload, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	encoded := string(payload)
	for _, forbidden := range []string{"owner_id", "actor_id", "raw_record_id", "raw_ids", "content", "path"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("view exposes forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestChatGPTRawImportContractsValidateAndCarryCommonRawReceipt(t *testing.T) {
	record := ChatGPTL3ImportRecord{
		Format:         ChatGPTL3ArtifactFormat,
		ExportID:       "export-1",
		EvidenceID:     "chatgpt_export:conversation-1:message-1",
		ConversationID: "conversation-1",
		MessageID:      "message-1",
		Role:           "user",
		Text:           "Renは映画が好き",
	}
	if err := ValidateChatGPTL3ImportRecord(record); err != nil {
		t.Fatalf("valid ChatGPT L3 record rejected: %v", err)
	}

	receipt := CommonRawIntakeReceipt{
		RequestID:      "request-1",
		ManifestID:     "raw-manifest-1",
		Status:         CommonRawStateCompleted,
		ManifestSHA256: strings.Repeat("a", 64),
		SourceCount:    1,
		Checkpoint:     "completed",
	}
	result := ChatGPTRawImportResult{RawReceipt: receipt}
	if got := result.RawReceipt.ManifestID; got != receipt.ManifestID {
		t.Fatalf("raw receipt was not carried by ChatGPT Raw result: got %q want %q", got, receipt.ManifestID)
	}
}

func TestMarshalChatGPTRawPayloadIsCanonicalAndIndexed(t *testing.T) {
	batch := ChatGPTRawImportBatch{
		ManifestSHA256:   strings.Repeat("a", 64),
		ArtifactSHA256:   strings.Repeat("b", 64),
		SourceCount:      3,
		SchemaVersion:    ChatGPTL3ArtifactFormat,
		ConverterVersion: ChatGPTImportConverterVersion,
		BatchIndex:       1,
		BatchCount:       2,
		StartLine:        11,
		Records: []ChatGPTL3ImportRecord{{
			Format:                ChatGPTL3ArtifactFormat,
			ExportID:              "export-1",
			EvidenceID:            "chatgpt_export:conversation-1:message-1",
			ConversationID:        "conversation-1",
			ConversationTitle:     "title",
			ConversationCreatedAt: time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC),
			ConversationUpdatedAt: time.Date(2026, 8, 16, 2, 3, 4, 0, time.UTC),
			NodeID:                "node-1",
			ParentNodeID:          "parent-1",
			ChildNodeIDs:          []string{"child-1", "child-2"},
			OnCurrentBranch:       true,
			MessageID:             "message-1",
			MessageCreatedAt:      time.Date(2026, 8, 16, 3, 4, 5, 0, time.UTC),
			Role:                  "user",
			ContentType:           "text",
			Text:                  "hello",
			Content:               json.RawMessage(`{"parts":["hello"]}`),
			Metadata:              json.RawMessage(`{"source":"test"}`),
		}},
	}

	got, err := MarshalChatGPTRawPayload(batch, 0)
	if err != nil {
		t.Fatalf("marshal ChatGPT Raw payload: %v", err)
	}
	want := `{"format":"rencrow.chatgpt_l3.v1","export_id":"export-1","evidence_id":"chatgpt_export:conversation-1:message-1","conversation_id":"conversation-1","conversation_title":"title","conversation_created_at":"2026-08-16T01:02:03Z","conversation_updated_at":"2026-08-16T02:03:04Z","node_id":"node-1","parent_node_id":"parent-1","child_node_ids":["child-1","child-2"],"on_current_branch":true,"message_id":"message-1","message_created_at":"2026-08-16T03:04:05Z","role":"user","content_type":"text","text":"hello","content":{"parts":["hello"]},"metadata":{"source":"test"},"artifact_line":11,"manifest_sha256":"` + strings.Repeat("a", 64) + `","artifact_sha256":"` + strings.Repeat("b", 64) + `","source_count":3,"schema_version":"rencrow.chatgpt_l3.v1","converter_version":"chatgpt-export-memory-go/v2","batch_index":1,"batch_count":2,"start_line":11}`
	if string(got) != want {
		t.Fatalf("canonical payload changed:\n got: %s\nwant: %s", got, want)
	}

	for _, index := range []int{-1, 1} {
		if _, err := MarshalChatGPTRawPayload(batch, index); err == nil {
			t.Fatalf("out-of-range record index %d was accepted", index)
		}
	}
}

func TestChatGPTImportConfirmInputValidationIsBoundedAndResultIsSafe(t *testing.T) {
	valid := ChatGPTImportConfirmInput{
		RequestID: "confirm-request",
		OwnerID:   "owner-a",
		ActorID:   "owner-a",
		ExportID:  "export-a",
		Reason:    "Ren confirmed this import",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid confirmation rejected: %v", err)
	}
	for _, invalid := range []ChatGPTImportConfirmInput{
		{RequestID: "", OwnerID: "owner-a", ActorID: "owner-a", ExportID: "export-a", Reason: "reason"},
		{RequestID: "confirm/request", OwnerID: "owner-a", ActorID: "owner-a", ExportID: "export-a", Reason: "reason"},
		{RequestID: "confirm-request", OwnerID: "owner-a", ActorID: "owner-a", ExportID: "export-a", Reason: ""},
		{RequestID: "confirm-request", OwnerID: "owner-a", ActorID: "owner-a", ExportID: "export-a", Reason: strings.Repeat("x", ChatGPTImportConfirmMaxReasonByte+1)},
	} {
		if err := invalid.Validate(); ChatGPTImportErrorCodeOf(err) != ChatGPTImportErrorInvalid {
			t.Fatalf("invalid confirmation did not return typed invalid error: %v", err)
		}
	}
	encoded, err := json.Marshal(ChatGPTImportConfirmResult{RequestID: "confirm-request", ExportID: "export-a", AuditReference: "opaque-audit"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"owner_id", "actor_id", "raw_record", "candidate", "statement", "content", "path"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("safe confirmation result exposes %q: %s", forbidden, encoded)
		}
	}
}

func TestChatGPTImportMachineContractsRejectPathAndExposeNoPrivateFields(t *testing.T) {
	if err := (ChatGPTImportRetryInput{RequestID: "req", OwnerID: "ren", ActorID: "ren", ExportID: "export"}).Validate(); err != nil {
		t.Fatalf("valid retry rejected: %v", err)
	}
	if err := (ChatGPTImportFinalizeInput{RequestID: "req", OwnerID: "ren", ActorID: "ren", ExportID: "export", Apply: true}).Validate(); err != nil {
		t.Fatalf("valid finalize rejected: %v", err)
	}
	for _, err := range []error{
		(ChatGPTImportRetryInput{RequestID: "req", OwnerID: "ren", ActorID: "ren", ExportID: "../export"}).Validate(),
		(ChatGPTImportFinalizeInput{RequestID: "req", OwnerID: "ren", ActorID: "ren", ExportID: "export/secret", Apply: true}).Validate(),
	} {
		if ChatGPTImportErrorCodeOf(err) != ChatGPTImportErrorInvalid {
			t.Fatalf("unsafe machine request error=%v code=%q", err, ChatGPTImportErrorCodeOf(err))
		}
	}
	encoded, err := json.Marshal(ChatGPTImportFinalizeResult{RequestID: "req", ExportID: "export", Status: ChatGPTImportFinalizeStatusCompleted, ReceiptID: "opaque", AuditReference: "opaque"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"owner_id", "actor_id", "raw_record_id", "candidate", "statement", "content", "path"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("machine receipt exposes %q: %s", forbidden, encoded)
		}
	}
}

func testChatGPTImportBinding() ChatGPTImportBinding {
	return ChatGPTImportBinding{
		ExportID:          "export-1",
		ManifestSHA256:    strings.Repeat("a", 64),
		ArtifactSHA256:    strings.Repeat("b", 64),
		ArtifactBytes:     128,
		Format:            ChatGPTImportBundleFormat,
		SchemaVersion:     ChatGPTImportRecordSchema,
		ConverterVersion:  ChatGPTImportConverterVersion,
		SourceFileCount:   3,
		SourceChunkCount:  4,
		SourceObjectCount: 2,
		MessageCount:      5,
	}
}

func testChatGPTImportEventInput(requestID, ownerID string, state ChatGPTImportState) ChatGPTImportEventInput {
	return ChatGPTImportEventInput{
		RequestID: requestID,
		OwnerID:   ownerID,
		ActorID:   ownerID,
		Binding:   testChatGPTImportBinding(),
		Apply:     false,
		State:     state,
		Counts: ChatGPTImportCounts{
			SourceCount:     3,
			FileCount:       3,
			ChunkCount:      4,
			ObjectCount:     2,
			MessageCount:    5,
			BatchCount:      1,
			RawCount:        5,
			ProjectionCount: 0,
			JobCount:        0,
		},
		Warnings:       []string{},
		ErrorCode:      "",
		FailureReason:  "",
		AuditReference: "audit-1",
	}
}
