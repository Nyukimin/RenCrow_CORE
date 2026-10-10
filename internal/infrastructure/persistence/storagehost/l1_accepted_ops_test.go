package storagehost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const acceptedOPSStorageHostToken = "accepted-ops-storage-host-test-token"

type acceptedOPSCountingTransport struct {
	base  http.RoundTripper
	calls atomic.Int64
}

func (transport *acceptedOPSCountingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return transport.base.RoundTrip(request)
}

type acceptedOPSStorageHostFixture struct {
	store      *l1sqlite.L1SQLiteStore
	dbPath     string
	journalDir string
	handler    *Handler
	server     *httptest.Server
	client     *Client
	remote     *L1StoreClient
	transport  *acceptedOPSCountingTransport
}

func newAcceptedOPSStorageHostFixture(t *testing.T, configuredPrincipal string) *acceptedOPSStorageHostFixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "conversation-l1.sqlite")
	store, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open accepted OPS L1 owner: %v", err)
	}
	journalDir := filepath.Join(root, "journal")
	handler, err := NewHandler(HandlerConfig{Token: acceptedOPSStorageHostToken, JournalDir: journalDir})
	if err != nil {
		_ = store.Close()
		t.Fatalf("create storage host handler: %v", err)
	}
	if err := RegisterL1Group(handler, store, configuredPrincipal); err != nil {
		_ = handler.Close()
		_ = store.Close()
		t.Fatalf("register L1 group: %v", err)
	}
	server := httptest.NewServer(handler)
	transport := &acceptedOPSCountingTransport{base: server.Client().Transport}
	client, err := NewClient(ClientConfig{
		Endpoint: server.URL, Token: acceptedOPSStorageHostToken,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		server.Close()
		_ = handler.Close()
		_ = store.Close()
		t.Fatalf("create storage host client: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = handler.Close()
		_ = store.Close()
		t.Fatalf("storage host handshake: %v", err)
	}
	transport.calls.Store(0)
	fixture := &acceptedOPSStorageHostFixture{
		store: store, dbPath: dbPath, journalDir: journalDir, handler: handler, server: server,
		client: client, remote: NewL1StoreClient(client), transport: transport,
	}
	t.Cleanup(func() {
		server.Close()
		_ = handler.Close()
		_ = store.Close()
	})
	return fixture
}

func acceptedOPSRequestForTest(requestID, ownerID, raw string) domconv.AcceptedOPSInputRequest {
	return domconv.AcceptedOPSInputRequest{
		RequestID: requestID, OwnerID: ownerID, ActorID: ownerID,
		SessionID: modulecore.NewSessionID(), FirstThreadID: modulecore.NewThreadID(),
		TaskID: modulecore.NewTaskID(), TurnID: modulecore.NewTurnID(), TraceID: modulecore.NewTraceID(),
		UserMessageID: modulecore.NewMessageID(), AgentMessageID: modulecore.NewMessageID(),
		RawMessage: raw, DeclaredOrigin: domconv.AcceptedOPSInputOriginAutomation,
	}
}

func acceptedOPSUserContext(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		requestID, domaintool.ActorKindUser, ownerID, ownerID,
		[]string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("create accepted OPS user scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func acceptedOPSAgentContext(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		requestID, domaintool.ActorKindAgent, "shiro", ownerID,
		[]string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("create accepted OPS agent scope: %v", err)
	}
	scope.AgentRole = "worker"
	scope.Purpose = "ops"
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func TestL1AcceptedOPSOwnerPortRoundTripsExactRawAndReplays(t *testing.T) {
	fixture := newAcceptedOPSStorageHostFixture(t, "accepted-ops-user")
	owner, ok := any(fixture.remote).(domconv.AcceptedOPSInputStore)
	if !ok {
		t.Fatal("remote L1 owner does not implement AcceptedOPSInputStore")
	}

	request := acceptedOPSRequestForTest("accepted-ops-request-1", "accepted-ops-user", "  東京 🐦\n\tmessage  \n")
	ctx := acceptedOPSUserContext(t, request.RequestID, request.OwnerID)
	first, err := owner.AcceptOPSInput(ctx, request)
	if err != nil {
		t.Fatalf("remote AcceptOPSInput: %v", err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("remote acceptance receipt is invalid: %v", err)
	}
	if first.IdempotentReplay {
		t.Fatal("first accepted input unexpectedly reported an idempotent replay")
	}
	if first.RequestID != request.RequestID || first.OwnerID != request.OwnerID || first.ActorID != request.ActorID ||
		first.SessionID != request.SessionID || first.ThreadID != request.FirstThreadID || first.TaskID != request.TaskID ||
		first.TurnID != request.TurnID || first.TraceID != request.TraceID || first.UserMessageID != request.UserMessageID ||
		first.AgentMessageID != request.AgentMessageID || first.DeclaredOrigin != domconv.AcceptedOPSInputOriginAutomation {
		t.Fatalf("remote acceptance receipt lost request identity: receipt=%+v request=%+v", first, request)
	}

	regenerated := acceptedOPSRequestForTest(request.RequestID, request.OwnerID, request.RawMessage)
	if regenerated.SessionID == request.SessionID || regenerated.FirstThreadID == request.FirstThreadID ||
		regenerated.TaskID == request.TaskID || regenerated.TurnID == request.TurnID || regenerated.TraceID == request.TraceID ||
		regenerated.UserMessageID == request.UserMessageID || regenerated.AgentMessageID == request.AgentMessageID {
		t.Fatal("replay fixture did not allocate fresh session/thread/task/turn/trace/message IDs")
	}
	replay, err := owner.AcceptOPSInput(ctx, regenerated)
	wantReplay := first
	wantReplay.IdempotentReplay = true
	if err != nil || !reflect.DeepEqual(replay, wantReplay) {
		t.Fatalf("same owner/request/payload with regenerated IDs did not replay the stored receipt: replay=%+v err=%v want=%+v incoming=%+v", replay, err, wantReplay, regenerated)
	}

	read, err := owner.ReadAcceptedOPSInput(ctx, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID})
	if err != nil {
		t.Fatalf("remote ReadAcceptedOPSInput: %v", err)
	}
	if read.RawMessage != request.RawMessage || read.Receipt.PayloadSHA256 != first.PayloadSHA256 ||
		read.Receipt.RawSHA256 != first.RawSHA256 || read.Receipt.ManifestSHA256 != first.ManifestSHA256 ||
		read.Receipt.SessionID != first.SessionID || read.Receipt.ThreadID != first.ThreadID ||
		read.Receipt.TaskID != first.TaskID || read.Receipt.TurnID != first.TurnID || read.Receipt.TraceID != first.TraceID ||
		read.Receipt.UserMessageID != first.UserMessageID || read.Receipt.AgentMessageID != first.AgentMessageID {
		t.Fatalf("remote exact read did not preserve original canonical raw bytes, hashes, and stored IDs: got=%+v want=%+v", read, first)
	}
	changed := request
	changed.RawMessage += "different"
	if _, err := owner.AcceptOPSInput(ctx, changed); !errors.Is(err, domconv.ErrAcceptedOPSInputConflict) {
		t.Fatalf("changed payload for the same owner/request error=%v, want typed conflict", err)
	}

	if err := fixture.store.Close(); err != nil {
		t.Fatalf("close storage-host L1 owner before reopen: %v", err)
	}
	reopened, err := l1sqlite.NewL1SQLiteStore(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen storage-host L1 owner: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.ReadAcceptedOPSInput(ctx, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID})
	if err != nil || stored.RawMessage != request.RawMessage || stored.Receipt.AcceptanceSequence != first.AcceptanceSequence {
		t.Fatalf("accepted OPS input did not survive owner reopen: stored=%+v err=%v", stored, err)
	}
}

func TestL1AcceptedOPSHumanOriginRoundTripsOnlyThroughUserParentScope(t *testing.T) {
	fixture := newAcceptedOPSStorageHostFixture(t, "accepted-ops-user")
	owner, ok := any(fixture.remote).(domconv.AcceptedOPSInputStore)
	if !ok {
		t.Fatal("remote L1 owner does not implement AcceptedOPSInputStore")
	}
	request := acceptedOPSRequestForTest("accepted-ops-human-origin", "accepted-ops-user", "  人の原文 🐦\n\t")
	request.DeclaredOrigin = domconv.AcceptedOPSInputOriginHuman
	userContext := acceptedOPSUserContext(t, request.RequestID, request.OwnerID)
	receipt, err := owner.AcceptOPSInput(userContext, request)
	if err != nil {
		t.Fatalf("remote Human AcceptOPSInput: %v", err)
	}
	if receipt.DeclaredOrigin != domconv.AcceptedOPSInputOriginHuman {
		t.Fatalf("remote receipt origin=%q want human", receipt.DeclaredOrigin)
	}
	read, err := owner.ReadAcceptedOPSInput(userContext, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID})
	if err != nil || read.Receipt.DeclaredOrigin != domconv.AcceptedOPSInputOriginHuman || read.RawMessage != request.RawMessage {
		t.Fatalf("remote Human read=%+v err=%v", read, err)
	}
	transportCalls := fixture.transport.calls.Load()
	if _, err := owner.ReadAcceptedOPSInput(acceptedOPSAgentContext(t, request.RequestID, request.OwnerID), domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID}); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("Shiro read error=%v want forbidden", err)
	}
	if callsAfter := fixture.transport.calls.Load(); callsAfter != transportCalls {
		t.Fatalf("relabelled Shiro read reached storagehost: calls=%d want=%d", callsAfter, transportCalls)
	}
	changedOrigin := request
	changedOrigin.DeclaredOrigin = domconv.AcceptedOPSInputOriginAutomation
	if _, err := owner.AcceptOPSInput(userContext, changedOrigin); !errors.Is(err, domconv.ErrAcceptedOPSInputConflict) {
		t.Fatalf("remote origin conflict=%v want conflict", err)
	}
}

func TestL1AcceptedOPSCrashAfterOwnerCommitBeforeJournalDoneStaysUnknown(t *testing.T) {
	fixture := newAcceptedOPSStorageHostFixture(t, "accepted-ops-user")
	request := acceptedOPSRequestForTest("accepted-ops-request-crash-window", "accepted-ops-user", "durable before journal")
	ctx := acceptedOPSUserContext(t, request.RequestID, request.OwnerID)
	const opID = "accepted-ops-crash-window"
	fixture.client.opID = opID
	fixture.handler.crashAfterCommitFor = opID
	if _, err := fixture.remote.AcceptOPSInput(ctx, request); errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("crash after owner commit returned %v, want fail-closed outcome_unknown", err)
	}

	fixture.server.Close()
	if err := fixture.handler.Close(); err != nil {
		t.Fatalf("close first storage host after simulated crash: %v", err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatalf("close L1 owner before reopen: %v", err)
	}

	reopened, err := l1sqlite.NewL1SQLiteStore(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen committed L1 owner: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	secondHandler, err := NewHandler(HandlerConfig{Token: acceptedOPSStorageHostToken, JournalDir: fixture.journalDir})
	if err != nil {
		t.Fatalf("reopen storage host journal: %v", err)
	}
	if err := RegisterL1Group(secondHandler, reopened, "accepted-ops-user"); err != nil {
		_ = secondHandler.Close()
		t.Fatalf("register reopened L1 owner: %v", err)
	}
	secondServer := httptest.NewServer(secondHandler)
	t.Cleanup(func() {
		secondServer.Close()
		_ = secondHandler.Close()
	})
	secondClient, err := NewClient(ClientConfig{
		Endpoint: secondServer.URL, Token: acceptedOPSStorageHostToken, HTTPClient: secondServer.Client(),
	})
	if err != nil {
		t.Fatalf("create restarted storage host client: %v", err)
	}
	if err := secondClient.Handshake(context.Background()); err != nil {
		t.Fatalf("handshake restarted storage host client: %v", err)
	}
	secondClient.opID = opID
	if _, err := NewL1StoreClient(secondClient).AcceptOPSInput(ctx, request); errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("same pending storage-host operation after restart returned %v, want outcome_unknown", err)
	}
	stored, err := reopened.ReadAcceptedOPSInput(ctx, domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: request.OwnerID,
	})
	if err != nil || stored.RawMessage != request.RawMessage || stored.Receipt.AcceptanceSequence != 1 ||
		stored.Receipt.SessionID != request.SessionID || stored.Receipt.ThreadID != request.FirstThreadID {
		t.Fatalf("owner commit was not durable after journal uncertainty: stored=%+v err=%v", stored, err)
	}
}

func TestL1AcceptedOPSClientRejectsNonUserParentScopesBeforeSend(t *testing.T) {
	fixture := newAcceptedOPSStorageHostFixture(t, "accepted-ops-user")
	owner, ok := any(fixture.remote).(domconv.AcceptedOPSInputStore)
	if !ok {
		t.Fatal("remote L1 owner does not implement AcceptedOPSInputStore")
	}
	request := acceptedOPSRequestForTest("accepted-ops-request-scope", "accepted-ops-user", "private input")
	wrongRequest := acceptedOPSUserContext(t, "another-request", request.OwnerID)
	wrongOwner := acceptedOPSUserContext(t, request.RequestID, "another-user")
	publicScope, err := domaintool.NewToolExecutionScope(
		request.RequestID, domaintool.ActorKindUser, request.OwnerID, request.OwnerID,
		[]string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("create public-only scope: %v", err)
	}
	public := domaintool.WithToolExecutionScope(context.Background(), publicScope)
	agent := acceptedOPSAgentContext(t, request.RequestID, request.OwnerID)
	invalid := request
	invalid.RawMessage = ""

	cases := []struct {
		name string
		ctx  context.Context
		req  domconv.AcceptedOPSInputRequest
	}{
		{name: "missing scope", req: request},
		{name: "request mismatch", ctx: wrongRequest, req: request},
		{name: "owner mismatch", ctx: wrongOwner, req: request},
		{name: "public only", ctx: public, req: request},
		{name: "agent input", ctx: agent, req: request},
		{name: "invalid request", ctx: acceptedOPSUserContext(t, request.RequestID, request.OwnerID), req: invalid},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := fixture.transport.calls.Load()
			_, err := owner.AcceptOPSInput(test.ctx, test.req)
			if test.name == "invalid request" {
				if !errors.Is(err, domconv.ErrAcceptedOPSInputInvalid) {
					t.Fatalf("invalid request error=%v, want invalid", err)
				}
			} else if !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
				t.Fatalf("unauthorized scope error=%v, want forbidden", err)
			}
			if after := fixture.transport.calls.Load(); after != before {
				t.Fatalf("client sent %d HTTP requests before rejecting scope, want none", after-before)
			}
		})
	}

	before := fixture.transport.calls.Load()
	_, err = owner.ReadAcceptedOPSInput(agent, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID})
	if !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("remote Shiro read error=%v, want unsupported/forbidden", err)
	}
	if after := fixture.transport.calls.Load(); after != before {
		t.Fatalf("remote Shiro read sent %d HTTP requests, want none", after-before)
	}
}

func TestL1AcceptedOPSHostBindsConfiguredOwnerAndRejectsReadClaims(t *testing.T) {
	fixture := newAcceptedOPSStorageHostFixture(t, "configured-ops-user")
	owner, ok := any(fixture.remote).(domconv.AcceptedOPSInputStore)
	if !ok {
		t.Fatal("remote L1 owner does not implement AcceptedOPSInputStore")
	}
	request := acceptedOPSRequestForTest("accepted-ops-request-owner-mismatch", "other-ops-user", "must not be stored")
	ctx := acceptedOPSUserContext(t, request.RequestID, request.OwnerID)
	if _, err := owner.AcceptOPSInput(ctx, request); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("configured-owner mismatch error=%v, want forbidden", err)
	}
	configuredContext := acceptedOPSUserContext(t, request.RequestID, "configured-ops-user")
	if _, err := fixture.store.ReadAcceptedOPSInput(configuredContext, domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: "configured-ops-user",
	}); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
		t.Fatalf("configured owner read after mismatched assertion=%v, want absent/unavailable", err)
	}
	if _, err := owner.ReadAcceptedOPSInput(configuredContext, domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: "configured-ops-user",
	}); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
		t.Fatalf("remote absent owner read=%v, want typed unavailable", err)
	}

	var invalidResult map[string]any
	err := fixture.client.Call(context.Background(), GroupL1, l1OpAcceptOPSInput, map[string]any{
		"declared_origin": "human",
	}, &invalidResult)
	var invalidHostErr *Error
	if !errors.As(err, &invalidHostErr) || invalidHostErr.Code != ErrorCodeSchemaRejected {
		t.Fatalf("host accepted unsupported origin error=%v, want schema rejection", err)
	}

	readWithClaims := map[string]any{
		"request_id": request.RequestID, "owner_id": "configured-ops-user", "actor_id": "configured-ops-user",
		"read_as": "shiro", "role": "worker", "auth_source": "agent_orchestrator",
	}
	var result map[string]any
	err = fixture.client.Call(context.Background(), GroupL1, "read_accepted_ops_input", readWithClaims, &result)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeSchemaRejected {
		t.Fatalf("read with caller-selected actor claims error=%v, want schema rejection", err)
	}
}

func TestL1AcceptedOPSDisabledPrincipalLeavesExistingL1Available(t *testing.T) {
	fixture := newAcceptedOPSStorageHostFixture(t, "")
	if _, _, _, _, err := fixture.remote.LatestConversationThreadReference(context.Background(), string(modulecore.NewSessionID())); err != nil {
		t.Fatalf("existing L1 operation unavailable without accepted OPS principal: %v", err)
	}
	var result map[string]any
	err := fixture.client.Call(context.Background(), GroupL1, "accept_ops_input", map[string]any{}, &result)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeOperationUnsupported {
		t.Fatalf("accepted OPS operation without configured principal error=%v, want unsupported", err)
	}
}

func TestL1AcceptedOPSInvalidBearerDoesNotReachOwner(t *testing.T) {
	fixture := newAcceptedOPSStorageHostFixture(t, "accepted-ops-user")
	client, err := NewClient(ClientConfig{
		Endpoint: fixture.server.URL, Token: "invalid-storage-host-token", HTTPClient: fixture.server.Client(),
	})
	if err != nil {
		t.Fatalf("create invalid-token client: %v", err)
	}
	request := acceptedOPSRequestForTest("accepted-ops-request-invalid-token", "accepted-ops-user", "must not be stored")
	payload := map[string]any{
		"request_id": request.RequestID, "owner_id": request.OwnerID, "actor_id": request.ActorID,
		"session_id": request.SessionID, "first_thread_id": request.FirstThreadID, "task_id": request.TaskID,
		"turn_id": request.TurnID, "trace_id": request.TraceID, "user_message_id": request.UserMessageID,
		"agent_message_id": request.AgentMessageID, "declared_origin": request.DeclaredOrigin,
		"raw_message": request.RawMessage,
	}
	var result map[string]any
	err = client.Call(context.Background(), GroupL1, "accept_ops_input", payload, &result)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeUnauthorized {
		t.Fatalf("invalid bearer operation error=%v, want unauthorized", err)
	}
	ctx := acceptedOPSUserContext(t, request.RequestID, request.OwnerID)
	if _, err := fixture.store.ReadAcceptedOPSInput(ctx, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID}); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
		t.Fatalf("owner read after invalid bearer=%v, want absent/unavailable", err)
	}
}

func TestL1AcceptedOPSLostResponseResolvesSameOwnerRequestReceipt(t *testing.T) {
	fixture := newAcceptedOPSStorageHostFixture(t, "accepted-ops-user")
	owner, ok := any(fixture.remote).(domconv.AcceptedOPSInputStore)
	if !ok {
		t.Fatal("remote L1 owner does not implement AcceptedOPSInputStore")
	}
	request := acceptedOPSRequestForTest("accepted-ops-request-lost-response", "accepted-ops-user", strings.Repeat("雪 ", 50)+"\n")
	ctx := acceptedOPSUserContext(t, request.RequestID, request.OwnerID)
	fixture.client.sendHook = func() error {
		fixture.client.sendHook = nil
		return errConnectionResetAfterSend
	}
	first, err := owner.AcceptOPSInput(ctx, request)
	if err != nil {
		t.Fatalf("accepted OPS result was not resolved after response loss: %v", err)
	}
	retry, err := owner.AcceptOPSInput(ctx, request)
	if err != nil || !retry.IdempotentReplay || retry.AcceptanceSequence != first.AcceptanceSequence || retry.PayloadSHA256 != first.PayloadSHA256 {
		t.Fatalf("same owner/request/payload retry did not preserve the receipt: first=%+v retry=%+v err=%v", first, retry, err)
	}
}
