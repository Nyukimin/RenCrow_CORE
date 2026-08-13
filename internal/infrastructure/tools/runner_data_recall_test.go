package tools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type dataRecallProviderStub struct {
	calls   int
	ctx     context.Context
	request DataRecallRequest
	result  any
	err     error
}

func (s *dataRecallProviderStub) Recall(ctx context.Context, request DataRecallRequest) (any, error) {
	s.calls++
	s.ctx = ctx
	s.request = request
	return s.result, s.err
}

func newDataRecallTestContext(t *testing.T) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope(
		"req-data-recall",
		tool.ActorKindAgent,
		"mio",
		"",
		[]string{tool.DataScopePublic},
		tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope)
}

func validDataRecallArgs() map[string]any {
	return map[string]any{
		"store":     "conversation_l1",
		"operation": "search",
		"query":     "important decision",
	}
}

func findDataRecallMetadata(t *testing.T, runner *ToolRunner) tool.ToolMetadata {
	t.Helper()
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, item := range metadata {
		if item.ToolID == dataRecallToolName {
			return item
		}
	}
	t.Fatalf("%s metadata was not registered", dataRecallToolName)
	return tool.ToolMetadata{}
}

func assertDataRecallValidationError(t *testing.T, response *tool.ToolResponse, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("ExecuteV2() error = %v", err)
	}
	if response == nil || response.Error == nil {
		t.Fatalf("expected structured validation error, response = %#v", response)
	}
	if response.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("error code = %s, want %s", response.Error.Code, tool.ErrValidationFailed)
	}
}

func TestDataRecallToolRegistrationAndModelSchema(t *testing.T) {
	withoutProvider := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	for _, metadata := range mustListToolMetadata(t, withoutProvider) {
		if metadata.ToolID == dataRecallToolName {
			t.Fatal("data.recall must not be registered without a provider")
		}
	}
	response, err := withoutProvider.ExecuteV2(context.Background(), dataRecallToolName, validDataRecallArgs())
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("ExecuteV2() without provider error = %v, want unknown tool", err)
	}
	if response != nil {
		t.Fatalf("unknown tool response = %#v, want nil", response)
	}

	provider := &dataRecallProviderStub{result: map[string]any{"ok": true}}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataRecall: provider,
		DisableToolHarness:    true,
	})
	metadata := findDataRecallMetadata(t, runner)
	if metadata.ToolID != dataRecallToolName || metadata.Category != "query" || !metadata.DryRun {
		t.Fatalf("metadata identity = %#v, want query/read-only data.recall", metadata)
	}
	if metadata.Parameters["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false", metadata.Parameters["additionalProperties"])
	}
	properties, ok := metadata.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", metadata.Parameters["properties"])
	}
	wantFields := map[string]bool{"store": true, "operation": true, "query": true, "limit": true}
	if len(properties) != len(wantFields) {
		t.Fatalf("schema properties = %#v, want exactly store/operation/query/limit", properties)
	}
	for field := range properties {
		if !wantFields[field] {
			t.Fatalf("model-visible schema exposes unexpected field %q", field)
		}
	}
	if required, ok := metadata.Parameters["required"].([]any); !ok || !reflect.DeepEqual(required, []any{"store", "operation", "query"}) {
		t.Fatalf("required = %#v, want store/operation/query", metadata.Parameters["required"])
	}
	for _, forbidden := range []string{"user", "user_id", "auth", "authenticated_user_id", "scope", "data_scope", "request_id"} {
		if _, exposed := properties[forbidden]; exposed {
			t.Fatalf("model-visible schema exposes forbidden field %q", forbidden)
		}
	}
}

func mustListToolMetadata(t *testing.T, runner *ToolRunner) []tool.ToolMetadata {
	t.Helper()
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	return metadata
}

func TestDataRecallToolRejectsInvalidArgsAndDoesNotCallProvider(t *testing.T) {
	provider := &dataRecallProviderStub{result: map[string]any{"ok": true}}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataRecall: provider,
		DisableToolHarness:    true,
	})
	cases := []map[string]any{
		{"operation": "search", "query": "q"},
		{"store": "store", "query": "q"},
		{"store": "store", "operation": "search"},
		{"store": " ", "operation": "search", "query": "q"},
		{"store": "store", "operation": " ", "query": "q"},
		{"store": "store", "operation": "search", "query": " "},
		{"store": 42, "operation": "search", "query": "q"},
		{"store": "store", "operation": 42, "query": "q"},
		{"store": "store", "operation": "search", "query": 42},
		{"store": "store", "operation": "search", "query": "q", "user_id": "u-1"},
		{"store": "store", "operation": "search", "query": "q", "limit": 0},
		{"store": "store", "operation": "search", "query": "q", "limit": 51},
		{"store": "store", "operation": "search", "query": "q", "limit": -1},
		{"store": "store", "operation": "search", "query": "q", "limit": 1.5},
		{"store": "store", "operation": "search", "query": "q", "limit": "10"},
	}
	for _, args := range cases {
		response, err := runner.ExecuteV2(newDataRecallTestContext(t), dataRecallToolName, args)
		assertDataRecallValidationError(t, response, err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for invalid arguments", provider.calls)
	}
}

func TestDataRecallToolRequiresTrustedAgentScopeBeforeProvider(t *testing.T) {
	provider := &dataRecallProviderStub{result: map[string]any{"ok": true}}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataRecall: provider,
		DisableToolHarness:    true,
	})
	validArgs := validDataRecallArgs()

	userScope, err := tool.NewToolExecutionScope(
		"req-user",
		tool.ActorKindUser,
		"user-1",
		"user-1",
		[]string{tool.DataScopeUser},
		tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(user) error = %v", err)
	}
	cases := []context.Context{
		context.Background(),
		tool.WithToolExecutionScope(context.Background(), tool.ToolExecutionScope{RequestID: "req-invalid", ActorID: "mio"}),
		tool.WithToolExecutionScope(context.Background(), userScope),
	}
	for _, ctx := range cases {
		response, err := runner.ExecuteV2(ctx, dataRecallToolName, validArgs)
		assertDataRecallValidationError(t, response, err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for missing, invalid, or non-agent scope", provider.calls)
	}
}

func TestDataRecallToolPassesNormalizedRequestAndContext(t *testing.T) {
	provider := &dataRecallProviderStub{result: map[string]any{"records": []any{"one"}}}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataRecall: provider,
		DisableToolHarness:    true,
	})
	ctx := newDataRecallTestContext(t)
	response, err := runner.ExecuteV2(ctx, dataRecallToolName, map[string]any{
		"store":     "  conversation_l1  ",
		"operation": "  search  ",
		"query":     "  important decision  ",
	})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("response = %#v, err = %v", response, err)
	}
	if !reflect.DeepEqual(response.Result, provider.result) {
		t.Fatalf("response result = %#v, want %#v", response.Result, provider.result)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if provider.request != (DataRecallRequest{
		Store:     "conversation_l1",
		Operation: "search",
		Query:     "important decision",
		Limit:     10,
	}) {
		t.Fatalf("provider request = %#v, want normalized request", provider.request)
	}
	gotScope, ok := tool.ToolExecutionScopeFromContext(provider.ctx)
	if !ok || gotScope.ActorKind != tool.ActorKindAgent || gotScope.ActorID != "mio" || gotScope.RequestID != "req-data-recall" {
		t.Fatalf("provider context scope = %#v, found = %v", gotScope, ok)
	}
}

func TestDataRecallToolMapsProviderFailureToUnavailableWithoutLeak(t *testing.T) {
	providerError := errors.New("secret db password=do-not-leak")
	provider := &dataRecallProviderStub{err: providerError}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataRecall: provider,
		DisableToolHarness:    true,
	})
	response, err := runner.ExecuteV2(newDataRecallTestContext(t), dataRecallToolName, validDataRecallArgs())
	if err != nil {
		t.Fatalf("ExecuteV2() error = %v", err)
	}
	if response == nil || response.Error == nil {
		t.Fatalf("response = %#v, want structured provider error", response)
	}
	if response.Error.Code != dataRecallUnavailableErrorCode {
		t.Fatalf("error code = %s, want %s", response.Error.Code, dataRecallUnavailableErrorCode)
	}
	serialized := response.String()
	if strings.Contains(serialized, "secret db password") || strings.Contains(serialized, "do-not-leak") {
		t.Fatalf("provider error leaked in response: %q", serialized)
	}
}
