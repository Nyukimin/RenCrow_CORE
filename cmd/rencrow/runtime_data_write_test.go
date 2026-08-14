package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteRegistryDispatchesExactRegistrationAndReturnsReceipt(t *testing.T) {
	registry := newRuntimeDataWriteRegistry()
	ctx := runtimeDataWriteContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic}, "worker", "ops")
	var calls int
	var gotContext context.Context
	var gotRequest toolsinfra.DataWriteRequest
	callback := func(callbackContext context.Context, request toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		calls++
		gotContext = callbackContext
		gotRequest = request
		return validRuntimeDataWriteOwnerResult(), nil
	}

	if err := registry.Register(" conversation_l1 ", " append ", dataRecallAccessPublic, callback); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.Register("conversation_l1", "append", dataRecallAccessPublic, callback); err == nil {
		t.Fatal("duplicate normalized store+operation registration must be rejected")
	}

	value, err := registry.Write(ctx, toolsinfra.DataWriteRequest{
		Store:     "  conversation_l1 ",
		Operation: " append ",
		Payload:   map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	receipt, ok := value.(runtimeDataWriteReceipt)
	if !ok {
		t.Fatalf("Write() result type = %T, want runtimeDataWriteReceipt", value)
	}
	if calls != 1 || gotContext != ctx {
		t.Fatalf("callback calls/context = %d/%v, want 1/original context", calls, gotContext)
	}
	if gotRequest.Store != "conversation_l1" || gotRequest.Operation != "append" || !reflect.DeepEqual(gotRequest.Payload, map[string]any{"message": "hello"}) {
		t.Fatalf("callback request = %#v, want normalized request", gotRequest)
	}
	if receipt.RequestID != "runtime-data-write-test" || receipt.ActorID != "mio" || receipt.AgentRole != "worker" || receipt.Purpose != "ops" {
		t.Fatalf("receipt identity = %#v", receipt)
	}
	if receipt.DataScope != string(dataRecallAccessPublic) || receipt.Owner != "conversation_l1" || receipt.OwnerRoute != "conversation_l1/append" || receipt.Status != "completed" {
		t.Fatalf("receipt route/status = %#v", receipt)
	}
	if receipt.SchemaVersion != "owner-schema-v1" || receipt.MigrationState != "ready" || receipt.ValidationState != "validated" || receipt.AuditRef != "audit-1" || receipt.IdempotencyKey != "write-key-1" || !receipt.IdempotentReplay || receipt.PolicyRevision != "policy-1" {
		t.Fatalf("receipt owner fields = %#v", receipt)
	}
	if _, err := time.Parse(time.RFC3339Nano, receipt.CompletedAt); err != nil {
		t.Fatalf("completed_at = %q: %v", receipt.CompletedAt, err)
	}
}

func TestRuntimeDataWriteRegistryRejectsInvalidRegistrationAndScope(t *testing.T) {
	registry := newRuntimeDataWriteRegistry()
	callback := func(context.Context, toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return validRuntimeDataWriteOwnerResult(), nil
	}
	for _, tt := range []struct {
		name      string
		store     string
		operation string
		access    dataRecallAccess
		callback  dataWriteCallback
	}{
		{name: "empty store", store: " ", operation: "append", access: dataRecallAccessPublic, callback: callback},
		{name: "empty operation", store: "store", operation: " ", access: dataRecallAccessPublic, callback: callback},
		{name: "invalid access", store: "store", operation: "append", access: dataRecallAccess("unknown"), callback: callback},
		{name: "nil callback", store: "store", operation: "append", access: dataRecallAccessPublic},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := registry.Register(tt.store, tt.operation, tt.access, tt.callback); err == nil {
				t.Fatal("Register() must reject invalid registration")
			}
		})
	}

	var calls int
	if err := registry.Register("store", "append", dataRecallAccessPublic, func(context.Context, toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		calls++
		return validRuntimeDataWriteOwnerResult(), nil
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	for _, ctx := range []context.Context{
		context.Background(),
		domaintool.WithToolExecutionScope(context.Background(), domaintool.ToolExecutionScope{RequestID: "invalid", ActorID: "mio"}),
		runtimeDataWriteContext(t, domaintool.ActorKindUser, "user-1", "user-1", []string{domaintool.DataScopeUser}, "worker", "ops"),
		runtimeDataWriteContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic}, "chat", "ops"),
		runtimeDataWriteContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic}, "worker", "chat"),
	} {
		if _, err := registry.Write(ctx, validDataWriteRequest()); err == nil {
			t.Fatal("Write() must reject missing, invalid, non-agent, or wrong worker/ops scope")
		}
	}
	if calls != 0 {
		t.Fatalf("callback calls = %d, want 0 for rejected scopes", calls)
	}
}

func TestRuntimeDataWriteRegistryRejectsOwnerFailureWithoutLeak(t *testing.T) {
	ctx := runtimeDataWriteContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic}, "worker", "ops")
	t.Run("callback error", func(t *testing.T) {
		registry := newRuntimeDataWriteRegistry()
		if err := registry.Register("store", "append", dataRecallAccessPublic, func(context.Context, toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
			return runtimeDataWriteOwnerResult{}, errors.New("secret database token")
		}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		_, err := registry.Write(ctx, validDataWriteRequest())
		if !errors.Is(err, errDataWriteRegistryCallbackFailed) || strings.Contains(err.Error(), "secret database token") {
			t.Fatalf("Write() error = %v, want generic callback failure", err)
		}
	})

	for _, field := range []string{"schema_version", "migration_state", "validation_state", "audit_ref", "idempotency_key"} {
		t.Run("missing "+field, func(t *testing.T) {
			registry := newRuntimeDataWriteRegistry()
			owner := validRuntimeDataWriteOwnerResult()
			switch field {
			case "schema_version":
				owner.SchemaVersion = " "
			case "migration_state":
				owner.MigrationState = " "
			case "validation_state":
				owner.ValidationState = " "
			case "audit_ref":
				owner.AuditRef = " "
			case "idempotency_key":
				owner.IdempotencyKey = " "
			}
			if err := registry.Register("store", "append", dataRecallAccessPublic, func(context.Context, toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
				return owner, nil
			}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			_, err := registry.Write(ctx, validDataWriteRequest())
			if !errors.Is(err, errDataWriteRegistryCallbackFailed) {
				t.Fatalf("Write() error = %v, want generic callback failure", err)
			}
		})
	}
}

func TestRuntimeDataWriteRegistryRejectsForbiddenPayloadKeysBeforeOwner(t *testing.T) {
	registry := newRuntimeDataWriteRegistry()
	calls := 0
	if err := registry.Register("store", "append", dataRecallAccessPublic, func(context.Context, toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		calls++
		return validRuntimeDataWriteOwnerResult(), nil
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx := runtimeDataWriteContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic}, "worker", "ops")
	for _, key := range []string{
		"actor", "actor_id", "agent", "agent_id", "user", "user_id", "authenticated_user_id", "role", "purpose", "scope", "data_scope", "request_id", "path", "db", "database", "sql", "table", "column", "file_path", "Actor_ID",
	} {
		for _, payload := range []map[string]any{
			{key: "secret"},
			{"nested": map[string]any{key: "secret"}},
			{"items": []any{map[string]any{key: "secret"}}},
		} {
			if _, err := registry.Write(ctx, toolsinfra.DataWriteRequest{Store: "store", Operation: "append", Payload: payload}); !errors.Is(err, errDataWriteRegistryInvalidRequest) {
				t.Fatalf("Write() payload key %q error = %v, want invalid request", key, err)
			}
		}
	}
	if calls != 0 {
		t.Fatalf("callback calls = %d, want 0 for forbidden payload keys", calls)
	}
}

func TestBuildToolRuntimeRegistersDataWriteOnlyForWorkerAndRetainsRegistry(t *testing.T) {
	disabled := false
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		ToolHarness:  config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled},
	}
	runtime := buildToolRuntime(cfg, nil, nil, nil)
	if runtime.DataWriteRegistry == nil {
		t.Fatal("toolRuntime must retain the data write registry")
	}
	workerMetadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Worker ListTools() error = %v", err)
	}
	if !hasToolMetadata(workerMetadata, "data.write") {
		t.Fatalf("Worker metadata missing data.write: %#v", workerMetadata)
	}
	chatMetadata, err := runtime.ChatRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Chat ListTools() error = %v", err)
	}
	if hasToolMetadata(chatMetadata, "data.write") {
		t.Fatalf("Chat metadata must not expose data.write: %#v", chatMetadata)
	}

	response, err := runtime.WorkerRunnerV2.ExecuteV2(runtimeDataWriteContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic}, "worker", "ops"), "data.write", validDataWriteArgsForRuntime())
	if err != nil {
		t.Fatalf("Worker data.write ExecuteV2() error = %v", err)
	}
	if response == nil || response.Error == nil || response.Error.Code != "UNAVAILABLE" {
		t.Fatalf("unknown empty-registry response = %#v, want UNAVAILABLE", response)
	}
	if _, err := runtime.ChatRunnerV2.ExecuteV2(context.Background(), "data.write", validDataWriteArgsForRuntime()); !errors.Is(err, toolsinfra.ErrUnknownTool) {
		t.Fatalf("Chat data.write error = %v, want ErrUnknownTool", err)
	}
}

func validRuntimeDataWriteOwnerResult() runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "owner-schema-v1",
		MigrationState:   "ready",
		ValidationState:  "validated",
		AuditRef:         "audit-1",
		IdempotencyKey:   "write-key-1",
		IdempotentReplay: true,
		PolicyRevision:   "policy-1",
	}
}

func validDataWriteRequest() toolsinfra.DataWriteRequest {
	return toolsinfra.DataWriteRequest{Store: "store", Operation: "append", Payload: map[string]any{"message": "hello"}}
}

func validDataWriteArgsForRuntime() map[string]any {
	return map[string]any{"store": "store", "operation": "append", "payload": map[string]any{"message": "hello"}}
}

func runtimeDataWriteContext(t *testing.T, actor domaintool.ActorKind, actorID, userID string, dataScopes []string, role, purpose string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID:            "runtime-data-write-test",
		ActorKind:            actor,
		ActorID:              actorID,
		AuthenticatedUserID:  userID,
		AllowedDataScopes:    dataScopes,
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole:            role,
		Purpose:              purpose,
	}
	if actor == domaintool.ActorKindUser {
		scope.AuthenticationSource = domaintool.AuthenticationSourceHTTP
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("ToolExecutionScope.Validate() error = %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
