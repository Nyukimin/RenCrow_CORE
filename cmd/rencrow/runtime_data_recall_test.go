package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataRecallRegistryDispatchesExactRegistrationAndNormalizesRequest(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	ctx := runtimeDataRecallContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic})
	var calls int
	var gotContext context.Context
	var gotRequest toolsinfra.DataRecallRequest
	callback := func(callbackContext context.Context, request toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		calls++
		gotContext = callbackContext
		gotRequest = request
		return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{}), nil
	}

	if err := registry.Register("conversation_l1", "search", dataRecallAccessPublic, callback); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.Register(" conversation_l1 ", "search", dataRecallAccessPublic, callback); err == nil {
		t.Fatal("duplicate store+operation registration must be rejected")
	}

	result, err := registry.Recall(ctx, toolsinfra.DataRecallRequest{
		Store:     "  conversation_l1 ",
		Operation: " search ",
		Query:     "  important decision  ",
		Limit:     7,
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	gotResult, ok := result.(runtimeDataRecallResult)
	if !ok || gotResult.Store != "conversation_l1" || gotResult.Operation != "search" {
		t.Fatalf("Recall() result = %#v", result)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
	if gotRequest != (toolsinfra.DataRecallRequest{
		Store:     "conversation_l1",
		Operation: "search",
		Query:     "important decision",
		Limit:     7,
	}) {
		t.Fatalf("callback request = %#v, want normalized request", gotRequest)
	}
	if gotContext != ctx {
		t.Fatal("callback context must preserve the trusted execution context")
	}

	for _, request := range []toolsinfra.DataRecallRequest{
		{Store: "unknown", Operation: "search", Query: "q", Limit: 1},
		{Store: "conversation_l1", Operation: "unknown", Query: "q", Limit: 1},
	} {
		if _, err := registry.Recall(ctx, request); err == nil {
			t.Fatalf("Recall(%#v) must reject unknown store/operation", request)
		}
	}
	if calls != 1 {
		t.Fatalf("callback calls after unknown dispatch = %d, want 1", calls)
	}
}

func TestRuntimeDataRecallRegistrySnapshotIsSortedAndSafe(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	callback := func(_ context.Context, request toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{}), nil
	}
	if err := registry.Register("store_b", "read_b", dataRecallAccessInternal, callback); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("store_a", "read_a", dataRecallAccessUser, callback); err != nil {
		t.Fatal(err)
	}
	routes := registry.Snapshot()
	if len(routes) != 2 || routes[0] != (runtimeDataRecallRoute{Store: "store_a", Operation: "read_a", Access: dataRecallAccessUser}) || routes[1] != (runtimeDataRecallRoute{Store: "store_b", Operation: "read_b", Access: dataRecallAccessInternal}) {
		t.Fatalf("routes=%#v", routes)
	}
	routes[0].Store = "mutated"
	if got := registry.Snapshot()[0].Store; got != "store_a" {
		t.Fatalf("snapshot leaked mutable entry: %q", got)
	}
}

func TestRuntimeDataRecallRegistryRejectsInvalidRegistration(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	callback := func(context.Context, toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return newRuntimeDataRecallResult("store", "search", []map[string]any{}), nil
	}
	cases := []struct {
		name      string
		store     string
		operation string
		access    dataRecallAccess
		callback  dataRecallCallback
	}{
		{name: "empty store", store: " ", operation: "search", access: dataRecallAccessPublic, callback: callback},
		{name: "empty operation", store: "store", operation: " ", access: dataRecallAccessPublic, callback: callback},
		{name: "invalid access", store: "store", operation: "search", access: dataRecallAccess("unknown"), callback: callback},
		{name: "nil callback", store: "store", operation: "search", access: dataRecallAccessPublic},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := registry.Register(tt.store, tt.operation, tt.access, tt.callback); err == nil {
				t.Fatal("Register() must reject invalid registration")
			}
		})
	}
}

func TestRuntimeDataRecallRegistryEnforcesTrustedScopePolicy(t *testing.T) {
	cases := []struct {
		name       string
		access     dataRecallAccess
		actor      domaintool.ActorKind
		actorID    string
		userID     string
		dataScopes []string
		wantCall   bool
	}{
		{name: "public scope", access: dataRecallAccessPublic, actor: domaintool.ActorKindAgent, actorID: "mio", dataScopes: []string{domaintool.DataScopePublic}, wantCall: true},
		{name: "public denied to user-only scope", access: dataRecallAccessPublic, actor: domaintool.ActorKindAgent, actorID: "mio", userID: "user-1", dataScopes: []string{domaintool.DataScopeUser}},
		{name: "user scope with authenticated user", access: dataRecallAccessUser, actor: domaintool.ActorKindAgent, actorID: "mio", userID: "user-1", dataScopes: []string{domaintool.DataScopeUser}, wantCall: true},
		{name: "user denied to public-only scope", access: dataRecallAccessUser, actor: domaintool.ActorKindAgent, actorID: "mio", dataScopes: []string{domaintool.DataScopePublic}},
		{name: "internal agent", access: dataRecallAccessInternal, actor: domaintool.ActorKindAgent, actorID: "mio", dataScopes: []string{domaintool.DataScopeInternal}, wantCall: true},
		{name: "internal public-only agent denied", access: dataRecallAccessInternal, actor: domaintool.ActorKindAgent, actorID: "mio", dataScopes: []string{domaintool.DataScopePublic}},
		{name: "internal user rejected", access: dataRecallAccessInternal, actor: domaintool.ActorKindUser, actorID: "user-1", userID: "user-1", dataScopes: []string{domaintool.DataScopeUser}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			registry := newRuntimeDataRecallRegistry()
			calls := 0
			if err := registry.Register("store", "search", tt.access, func(context.Context, toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
				calls++
				return newRuntimeDataRecallResult("store", "search", []map[string]any{}), nil
			}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			ctx := runtimeDataRecallContext(t, tt.actor, tt.actorID, tt.userID, tt.dataScopes)
			result, err := registry.Recall(ctx, toolsinfra.DataRecallRequest{Store: "store", Operation: "search", Query: "q", Limit: 1})
			if tt.wantCall {
				gotResult, ok := result.(runtimeDataRecallResult)
				if err != nil || !ok || gotResult.Store != "store" {
					t.Fatalf("Recall() result=%#v err=%v, want callback result", result, err)
				}
				if calls != 1 {
					t.Fatalf("callback calls = %d, want 1", calls)
				}
				return
			}
			if err == nil {
				t.Fatal("Recall() must reject scope without the registered access")
			}
			if calls != 0 {
				t.Fatalf("callback calls = %d, want 0 for denied scope", calls)
			}
		})
	}
}

func TestRuntimeDataRecallRegistryRequiresWorkerOpsScopeForEveryRoute(t *testing.T) {
	cases := []struct {
		name      string
		agentRole string
		purpose   string
		wantCall  bool
	}{
		{name: "worker ops", agentRole: "worker", purpose: "ops", wantCall: true},
		{name: "wrong role", agentRole: "chat", purpose: "ops"},
		{name: "wrong purpose", agentRole: "worker", purpose: "chat"},
		{name: "missing role", purpose: "ops"},
		{name: "missing purpose", agentRole: "worker"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			registry := newRuntimeDataRecallRegistry()
			calls := 0
			if err := registry.Register("store", "search", dataRecallAccessPublic, func(context.Context, toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
				calls++
				return newRuntimeDataRecallResult("store", "search", []map[string]any{}), nil
			}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			scope := domaintool.ToolExecutionScope{
				RequestID:            "req-role-purpose",
				ActorKind:            domaintool.ActorKindAgent,
				ActorID:              "shiro",
				AllowedDataScopes:    []string{domaintool.DataScopePublic},
				AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
				AgentRole:            tt.agentRole,
				Purpose:              tt.purpose,
			}
			ctx := domaintool.WithToolExecutionScope(context.Background(), scope)
			result, err := registry.Recall(ctx, toolsinfra.DataRecallRequest{Store: "store", Operation: "search", Query: "q", Limit: 1})
			if tt.wantCall {
				gotResult, ok := result.(runtimeDataRecallResult)
				if err != nil || !ok || gotResult.Store != "store" || calls != 1 {
					t.Fatalf("Recall() result=%#v err=%v calls=%d", result, err, calls)
				}
				return
			}
			if err == nil || calls != 0 {
				t.Fatalf("Recall() result=%#v err=%v calls=%d, want fail closed", result, err, calls)
			}
		})
	}
}

func TestBuildToolRuntimeRegistersDataRecallOnlyForWorkerAndRetainsRegistry(t *testing.T) {
	disabled := false
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		ToolHarness:  config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled},
	}
	runtime := buildToolRuntime(cfg, nil, nil, nil)
	if runtime.DataRecallRegistry == nil {
		t.Fatal("toolRuntime must retain the data recall registry")
	}

	workerMetadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Worker ListTools() error = %v", err)
	}
	if !hasToolMetadata(workerMetadata, "data.recall") {
		t.Fatalf("Worker metadata missing data.recall: %#v", workerMetadata)
	}
	chatMetadata, err := runtime.ChatRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Chat ListTools() error = %v", err)
	}
	if hasToolMetadata(chatMetadata, "data.recall") {
		t.Fatalf("Chat metadata must not expose data.recall: %#v", chatMetadata)
	}

	ctx := runtimeDataRecallContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic})
	response, err := runtime.WorkerRunnerV2.ExecuteV2(ctx, "data.recall", map[string]any{
		"store": "unknown_store", "operation": "search", "query": "q",
	})
	if err != nil {
		t.Fatalf("Worker data.recall ExecuteV2() error = %v", err)
	}
	if response == nil || response.Error == nil || response.Error.Code != "UNAVAILABLE" {
		t.Fatalf("unknown empty-registry response = %#v, want UNAVAILABLE", response)
	}
	if _, err := runtime.ChatRunnerV2.ExecuteV2(ctx, "data.recall", map[string]any{
		"store": "unknown_store", "operation": "search", "query": "q",
	}); !errors.Is(err, toolsinfra.ErrUnknownTool) {
		t.Fatalf("Chat data.recall error = %v, want ErrUnknownTool", err)
	}
}

func runtimeDataRecallContext(t *testing.T, actor domaintool.ActorKind, actorID, userID string, dataScopes []string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID:            "runtime-data-recall-test",
		ActorKind:            actor,
		ActorID:              actorID,
		AuthenticatedUserID:  userID,
		AllowedDataScopes:    dataScopes,
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole:            "worker",
		Purpose:              "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("ToolExecutionScope.Validate() error = %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func TestRuntimeDataRecallRegistryDoesNotLeakCallbackError(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	if err := registry.Register("store", "search", dataRecallAccessPublic, func(context.Context, toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeDataRecallResult{}, errors.New("secret database token")
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx := runtimeDataRecallContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic})
	_, err := registry.Recall(ctx, toolsinfra.DataRecallRequest{Store: "store", Operation: "search", Query: "q", Limit: 1})
	if err == nil || strings.Contains(err.Error(), "secret database token") {
		t.Fatalf("Recall() error = %v, must fail without callback error details", err)
	}
}

func TestRuntimeDataRecallRegistryAddsOwnerEvidenceToCallbackResult(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	callback := func(context.Context, toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return newRuntimeDataRecallResult("store", "search", []map[string]any{{"id": "1"}}), nil
	}
	if err := registry.Register("store", "search", dataRecallAccessUser, callback); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx := runtimeDataRecallContext(t, domaintool.ActorKindAgent, "shiro", "user-1", []string{domaintool.DataScopeUser})
	value, err := registry.Recall(ctx, toolsinfra.DataRecallRequest{Store: "store", Operation: "search", Query: "q", Limit: 7})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	result, ok := value.(runtimeDataRecallResult)
	if !ok {
		t.Fatalf("Recall() result type = %T", value)
	}
	if result.Evidence.RequestID != "runtime-data-recall-test" || result.Evidence.ActorID != "shiro" || result.Evidence.AgentRole != "worker" || result.Evidence.Purpose != "ops" {
		t.Fatalf("identity evidence = %#v", result.Evidence)
	}
	if result.Evidence.DataScope != string(dataRecallAccessUser) || result.Evidence.Owner != "store" || result.Evidence.OwnerRoute != "store/search" {
		t.Fatalf("owner evidence = %#v", result.Evidence)
	}
	if result.Evidence.FreshnessState != "observed_at_read" || result.Evidence.ValidationState != "owner_route_succeeded" || result.Evidence.BudgetLimit != 7 || result.Evidence.ReturnedCount != 1 {
		t.Fatalf("result evidence = %#v", result.Evidence)
	}
	if _, err := time.Parse(time.RFC3339Nano, result.Evidence.RetrievedAt); err != nil {
		t.Fatalf("retrieved_at = %q: %v", result.Evidence.RetrievedAt, err)
	}
}

func TestRuntimeDataRecallRegistryRejectsCallbackRouteMismatch(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	callback := func(context.Context, toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return newRuntimeDataRecallResult("other", "search", []map[string]any{}), nil
	}
	if err := registry.Register("store", "search", dataRecallAccessPublic, callback); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx := runtimeDataRecallContext(t, domaintool.ActorKindAgent, "shiro", "", []string{domaintool.DataScopePublic})
	_, err := registry.Recall(ctx, toolsinfra.DataRecallRequest{Store: "store", Operation: "search", Query: "q", Limit: 1})
	if !errors.Is(err, errDataRecallRegistryCallbackFailed) {
		t.Fatalf("Recall() error = %v, want callback route failure", err)
	}
}

func TestRuntimeDataRecallRegistryRejectsNilRecords(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	callback := func(context.Context, toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return runtimeDataRecallResult{Store: "store", Operation: "search"}, nil
	}
	if err := registry.Register("store", "search", dataRecallAccessPublic, callback); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx := runtimeDataRecallContext(t, domaintool.ActorKindAgent, "shiro", "", []string{domaintool.DataScopePublic})
	_, err := registry.Recall(ctx, toolsinfra.DataRecallRequest{Store: "store", Operation: "search", Query: "q", Limit: 1})
	if !errors.Is(err, errDataRecallRegistryCallbackFailed) {
		t.Fatalf("Recall() error = %v, want nil-record failure", err)
	}
}
