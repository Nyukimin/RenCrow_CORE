package tools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type dataWriteProviderStub struct {
	calls   int
	ctx     context.Context
	request DataWriteRequest
	result  any
	err     error
}

func (s *dataWriteProviderStub) Write(ctx context.Context, request DataWriteRequest) (any, error) {
	s.calls++
	s.ctx = ctx
	s.request = request
	return s.result, s.err
}

func newDataWriteTestContext(t *testing.T) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope(
		"req-data-write",
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

func validDataWriteArgs() map[string]any {
	return map[string]any{
		"store":     "conversation_l1",
		"operation": "append",
		"payload":   map[string]any{"message": "hello"},
	}
}

func TestDataWriteToolRegistrationAndModelSchema(t *testing.T) {
	withoutProvider := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	for _, metadata := range mustListToolMetadata(t, withoutProvider) {
		if metadata.ToolID == dataWriteToolName {
			t.Fatal("data.write must not be registered without a provider")
		}
	}
	response, err := withoutProvider.ExecuteV2(context.Background(), dataWriteToolName, validDataWriteArgs())
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("ExecuteV2() without provider error = %v, want unknown tool", err)
	}
	if response != nil {
		t.Fatalf("unknown tool response = %#v, want nil", response)
	}

	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataWrite: &dataWriteProviderStub{result: map[string]any{"ok": true}},
		DisableToolHarness:   true,
	})
	metadata := findDataWriteMetadata(t, runner)
	if metadata.ToolID != dataWriteToolName || metadata.Category != "mutation" || metadata.DryRun {
		t.Fatalf("metadata identity = %#v, want mutation/non-dry-run data.write", metadata)
	}
	const recallInstruction = "follow-up data.recall queryにはaudit_refだけを使い、request_id/idempotency_keyは内部相関用でモデルから参照しない"
	if !strings.Contains(metadata.Description, recallInstruction) {
		t.Fatalf("description = %q, want receipt recall instruction %q", metadata.Description, recallInstruction)
	}
	foundRecallInstruction := false
	for _, invariant := range metadata.Invariants {
		if invariant == recallInstruction {
			foundRecallInstruction = true
			break
		}
	}
	if !foundRecallInstruction {
		t.Fatalf("invariants = %#v, want exact receipt recall instruction %q", metadata.Invariants, recallInstruction)
	}
	if metadata.Parameters["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false", metadata.Parameters["additionalProperties"])
	}
	properties, ok := metadata.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", metadata.Parameters["properties"])
	}
	wantFields := map[string]bool{"store": true, "operation": true, "payload": true}
	if len(properties) != len(wantFields) {
		t.Fatalf("schema properties = %#v, want exactly store/operation/payload", properties)
	}
	for field := range properties {
		if !wantFields[field] {
			t.Fatalf("model-visible schema exposes unexpected field %q", field)
		}
	}
	if required, ok := metadata.Parameters["required"].([]any); !ok || !reflect.DeepEqual(required, []any{"store", "operation", "payload"}) {
		t.Fatalf("required = %#v, want store/operation/payload", metadata.Parameters["required"])
	}
	payloadSchema, ok := properties["payload"].(map[string]any)
	if !ok || payloadSchema["type"] != "object" {
		t.Fatalf("payload schema = %#v, want object", properties["payload"])
	}
	for _, forbidden := range []string{"actor", "user", "scope", "request_id", "path", "SQL"} {
		if _, exposed := properties[forbidden]; exposed {
			t.Fatalf("model-visible schema exposes forbidden field %q", forbidden)
		}
	}
}

func findDataWriteMetadata(t *testing.T, runner *ToolRunner) tool.ToolMetadata {
	t.Helper()
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, item := range metadata {
		if item.ToolID == dataWriteToolName {
			return item
		}
	}
	t.Fatalf("%s metadata was not registered", dataWriteToolName)
	return tool.ToolMetadata{}
}

func assertDataWriteValidationError(t *testing.T, response *tool.ToolResponse, err error) {
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

func TestDataWriteToolRejectsInvalidArgsAndDoesNotCallProvider(t *testing.T) {
	provider := &dataWriteProviderStub{result: map[string]any{"ok": true}}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataWrite: provider,
		DisableToolHarness:   true,
	})
	cases := []map[string]any{
		{"operation": "append", "payload": map[string]any{}},
		{"store": "store", "payload": map[string]any{}},
		{"store": "store", "operation": "append"},
		{"store": " ", "operation": "append", "payload": map[string]any{}},
		{"store": "store", "operation": " ", "payload": map[string]any{}},
		{"store": 42, "operation": "append", "payload": map[string]any{}},
		{"store": "store", "operation": 42, "payload": map[string]any{}},
		{"store": "store", "operation": "append", "payload": nil},
		{"store": "store", "operation": "append", "payload": []any{}},
		{"store": "store", "operation": "append", "payload": "not-object"},
		{"store": "store", "operation": "append", "payload": map[string]any{}, "actor": "mio"},
		{"store": "store", "operation": "append", "payload": map[string]any{}, "user": "user-1"},
		{"store": "store", "operation": "append", "payload": map[string]any{}, "scope": "internal"},
		{"store": "store", "operation": "append", "payload": map[string]any{}, "request_id": "req"},
		{"store": "store", "operation": "append", "payload": map[string]any{}, "path": "/tmp/db"},
		{"store": "store", "operation": "append", "payload": map[string]any{}, "SQL": "INSERT"},
		{"store": "store", "operation": "append", "payload": map[string]any{"blob": strings.Repeat("x", dataWriteMaxPayloadBytes)}},
	}
	for _, key := range []string{
		"actor", "actor_id", "agent", "agent_id", "user", "user_id", "authenticated_user_id", "role", "purpose", "scope", "data_scope", "request_id", "path", "db", "database", "sql", "table", "column", "file_path", "Actor_ID",
	} {
		cases = append(cases,
			map[string]any{"store": "store", "operation": "append", "payload": map[string]any{key: "secret"}},
			map[string]any{"store": "store", "operation": "append", "payload": map[string]any{"nested": map[string]any{key: "secret"}}},
			map[string]any{"store": "store", "operation": "append", "payload": map[string]any{"items": []any{map[string]any{key: "secret"}}}},
		)
	}
	for _, args := range cases {
		response, err := runner.ExecuteV2(newDataWriteTestContext(t), dataWriteToolName, args)
		assertDataWriteValidationError(t, response, err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for invalid arguments", provider.calls)
	}
}

func TestDataWriteToolRequiresTrustedAgentScopeBeforeProvider(t *testing.T) {
	provider := &dataWriteProviderStub{result: map[string]any{"ok": true}}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataWrite: provider,
		DisableToolHarness:   true,
	})
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
	for _, ctx := range []context.Context{
		context.Background(),
		tool.WithToolExecutionScope(context.Background(), tool.ToolExecutionScope{RequestID: "req-invalid", ActorID: "mio"}),
		tool.WithToolExecutionScope(context.Background(), userScope),
	} {
		response, err := runner.ExecuteV2(ctx, dataWriteToolName, validDataWriteArgs())
		assertDataWriteValidationError(t, response, err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for missing, invalid, or non-agent scope", provider.calls)
	}
}

func TestDataWriteToolPassesNormalizedRequestAndContext(t *testing.T) {
	payload := map[string]any{"message": "hello", "count": 2}
	provider := &dataWriteProviderStub{result: map[string]any{"receipt": "owner"}}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataWrite: provider,
		DisableToolHarness:   true,
	})
	response, err := runner.ExecuteV2(newDataWriteTestContext(t), dataWriteToolName, map[string]any{
		"store":     "  conversation_l1  ",
		"operation": "  append  ",
		"payload":   payload,
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
	if provider.request.Store != "conversation_l1" || provider.request.Operation != "append" || !reflect.DeepEqual(provider.request.Payload, payload) {
		t.Fatalf("provider request = %#v, want normalized request", provider.request)
	}
	gotScope, ok := tool.ToolExecutionScopeFromContext(provider.ctx)
	if !ok || gotScope.ActorKind != tool.ActorKindAgent || gotScope.ActorID != "mio" || gotScope.RequestID != "req-data-write" {
		t.Fatalf("provider context scope = %#v, found = %v", gotScope, ok)
	}
}

func TestDataWriteToolMapsProviderFailureToUnavailableWithoutLeak(t *testing.T) {
	providerError := errors.New("secret db password=do-not-leak")
	provider := &dataWriteProviderStub{err: providerError}
	runner := NewToolRunner(ToolRunnerConfig{
		OperationalDataWrite: provider,
		DisableToolHarness:   true,
	})
	response, err := runner.ExecuteV2(newDataWriteTestContext(t), dataWriteToolName, validDataWriteArgs())
	if err != nil {
		t.Fatalf("ExecuteV2() error = %v", err)
	}
	if response == nil || response.Error == nil {
		t.Fatalf("response = %#v, want structured provider error", response)
	}
	if response.Error.Code != dataWriteUnavailableErrorCode {
		t.Fatalf("error code = %s, want %s", response.Error.Code, dataWriteUnavailableErrorCode)
	}
	serialized := response.String()
	if strings.Contains(serialized, "secret db password") || strings.Contains(serialized, "do-not-leak") {
		t.Fatalf("provider error leaked in response: %q", serialized)
	}
}
