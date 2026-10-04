package storagehost

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestCommonRawChatGPTClosedOperationContractAndTypedReads(t *testing.T) {
	root := t.TempDir()
	owner, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.db"))
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
	rpc := newCommonRawTestClient(t, server)
	client := NewCommonRawIntakeStoreClient(rpc)

	info, err := rpc.Contract(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		commonRawIntakeOp: true, commonRawChatGPTImportOp: true, commonRawChatGPTEventOp: true,
		commonRawChatGPTStatusOp: false, commonRawChatGPTProgressOp: false, commonRawChatGPTRetryOp: true,
		commonRawChatGPTFinalizeOp: true, commonRawChatGPTReconcileOp: true,
	}
	got := map[string]bool{}
	for _, spec := range info.Operations {
		if spec.Group == GroupCommonRaw {
			got[spec.Op] = spec.Mutating
		}
	}
	if len(got) != len(want) {
		t.Fatalf("common_raw operations=%v, want %v", got, want)
	}
	for op, mutating := range want {
		if got[op] != mutating {
			t.Fatalf("common_raw/%s mutating=%v, want %v", op, got[op], mutating)
		}
	}
	if reconciled, err := client.ReconcileActiveChatGPTImports(context.Background()); err != nil || reconciled != 0 {
		t.Fatalf("empty startup reconciliation=(%d,%v), want (0,nil)", reconciled, err)
	}

	input := commonRawChatGPTTestEventInput("chatgpt-typed-event", "typed-export")
	ctx := commonRawChatGPTTestContext(t, input.RequestID, input.OwnerID)
	event, err := client.AppendChatGPTImportEvent(ctx, input)
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	status, err := client.GetChatGPTImportStatus(commonRawChatGPTTestContext(t, "chatgpt-status", input.OwnerID), "chatgpt-status", input.OwnerID, input.ActorID, input.Binding.ExportID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.EventID != event.EventID || status.RequestID != event.RequestID || status.ExportID != input.Binding.ExportID || status.State != input.State {
		t.Fatalf("status=%+v event=%+v", status, event)
	}
	progress, err := client.GetChatGPTImportProgress(commonRawChatGPTTestContext(t, "chatgpt-progress", input.OwnerID), "chatgpt-progress", input.OwnerID, input.ActorID, input.Binding.ExportID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if progress.RequestID != "chatgpt-progress" || progress.ExportID != input.Binding.ExportID || progress.ImportID != event.ImportID || progress.State != input.State {
		t.Fatalf("progress=%+v event=%+v", progress, event)
	}
	_, err = client.GetChatGPTImportStatus(context.Background(), "chatgpt-status", input.OwnerID, input.ActorID, input.Binding.ExportID)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeUnauthorized {
		t.Fatalf("missing authenticated user scope error=%v, want unauthorized", err)
	}
}

func TestCommonRawChatGPTLostResponseRecoversExactEventWithoutDuplicate(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "l1.db")
	journalPath := filepath.Join(root, "journal")
	input := commonRawChatGPTTestEventInput("chatgpt-lost-event", "lost-export")
	opID := "chatgpt-lost-event-op"

	owner1, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: journalPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterCommonRawGroup(h1, owner1, "ren"); err != nil {
		t.Fatal(err)
	}
	srv1 := newStorageHostTestServer(t, h1)
	rpc1 := newCommonRawTestClient(t, srv1)
	rpc1.opID = opID
	h1.crashAfterCommitFor = opID
	client1 := NewCommonRawIntakeStoreClient(rpc1)
	if _, err := client1.AppendChatGPTImportEvent(commonRawChatGPTTestContext(t, input.RequestID, input.OwnerID), input); err == nil {
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
	h2, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: journalPath})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if err := RegisterCommonRawGroup(h2, owner2, "ren"); err != nil {
		t.Fatal(err)
	}
	srv2 := newStorageHostTestServer(t, h2)
	rpc2 := newCommonRawTestClient(t, srv2)
	rpc2.opID = opID
	client2 := NewCommonRawIntakeStoreClient(rpc2)
	recovered, err := client2.AppendChatGPTImportEvent(commonRawChatGPTTestContext(t, input.RequestID, input.OwnerID), input)
	if err != nil {
		t.Fatalf("recover event: %v", err)
	}
	if recovered.RequestID != input.RequestID || recovered.OwnerID != input.OwnerID || recovered.State != input.State || recovered.EventID == "" {
		t.Fatalf("recovered event=%+v", recovered)
	}
	events := commonRawChatGPTEventCount(t, dbPath, input.RequestID)
	if events != 1 {
		t.Fatalf("lost-response retry duplicated event: %d", events)
	}

	changed := input
	changed.Warnings = []string{"changed payload"}
	rpc3 := newCommonRawTestClient(t, srv2)
	rpc3.opID = opID
	_, err = NewCommonRawIntakeStoreClient(rpc3).AppendChatGPTImportEvent(commonRawChatGPTTestContext(t, changed.RequestID, changed.OwnerID), changed)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeDuplicateConflict {
		t.Fatalf("same op changed payload error=%v, want duplicate conflict", err)
	}
}

func TestCommonRawChatGPTMissingOwnerProofAfterLostResponseStaysUnknown(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "l1.db")
	journalPath := filepath.Join(root, "journal")
	input := commonRawChatGPTTestEventInput("chatgpt-missing-proof", "missing-proof-export")
	opID := "chatgpt-missing-proof-op"
	owner, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: journalPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterCommonRawGroup(handler, owner, "ren"); err != nil {
		t.Fatal(err)
	}
	server := newStorageHostTestServer(t, handler)
	rpc := newCommonRawTestClient(t, server)
	rpc.opID = opID
	handler.crashAfterCommitFor = opID
	if _, err := NewCommonRawIntakeStoreClient(rpc).AppendChatGPTImportEvent(commonRawChatGPTTestContext(t, input.RequestID, input.OwnerID), input); err == nil {
		t.Fatal("first call must lose response")
	}
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	tamper, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tamper.Exec(`DELETE FROM l1_chatgpt_storagehost_operation_receipt WHERE op_id=?`, opID); err != nil {
		t.Fatal(err)
	}
	if err := tamper.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	h2, err := NewHandler(HandlerConfig{Token: commonRawGroupTestToken, JournalDir: journalPath})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if err := RegisterCommonRawGroup(h2, reopened, "ren"); err != nil {
		t.Fatal(err)
	}
	srv2 := newStorageHostTestServer(t, h2)
	rpc2 := newCommonRawTestClient(t, srv2)
	rpc2.opID = opID
	_, err = NewCommonRawIntakeStoreClient(rpc2).AppendChatGPTImportEvent(commonRawChatGPTTestContext(t, input.RequestID, input.OwnerID), input)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("missing proof error=%v, want outcome_unknown", err)
	}
	events := commonRawChatGPTEventCount(t, dbPath, input.RequestID)
	if events != 1 {
		t.Fatalf("missing-proof recovery reran effect: %d", events)
	}
}

func commonRawChatGPTTestEventInput(requestID, exportID string) domainmemory.ChatGPTImportEventInput {
	return domainmemory.ChatGPTImportEventInput{
		RequestID: requestID, OwnerID: "ren", ActorID: "ren", Apply: true, State: domainmemory.ChatGPTImportStateValidating,
		Binding: domainmemory.ChatGPTImportBinding{ExportID: exportID, ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ArtifactSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ArtifactBytes: 1, Format: domainmemory.ChatGPTImportBundleFormat, SchemaVersion: domainmemory.ChatGPTImportRecordSchema, ConverterVersion: domainmemory.ChatGPTImportConverterVersion, SourceFileCount: 1, SourceChunkCount: 1, MessageCount: 1},
		Counts:  domainmemory.ChatGPTImportCounts{SourceCount: 1, FileCount: 1, ChunkCount: 1, MessageCount: 1, BatchCount: 1}, AuditReference: "chatgpt-test-audit",
	}
}

func commonRawChatGPTTestContext(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, ownerID, ownerID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func commonRawChatGPTEventCount(t *testing.T, dbPath, requestID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event WHERE request_id=?`, requestID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
