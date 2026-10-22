package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type readbackGeneratorTestProvider struct {
	name       string
	response   llm.GenerateResponse
	err        error
	calls      int
	scope      conversation.ConversationScope
	request    llm.GenerateRequest
	onGenerate func(context.Context)
}

func (p *readbackGeneratorTestProvider) GenerateConversation(ctx context.Context, scope conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.scope = scope
	p.request = request
	if p.onGenerate != nil {
		p.onGenerate(ctx)
	}
	return p.response, p.err
}

func (p *readbackGeneratorTestProvider) Name() string { return p.name }

func readbackGeneratorTestScope(purpose conversation.ConversationPurpose) conversation.ConversationScope {
	return conversation.ConversationScope{
		TurnID:        core.NewTurnID(),
		SessionID:     core.NewSessionID(),
		TraceID:       core.NewTraceID(),
		ActorID:       "user-1",
		InputID:       core.NewMessageID(),
		GenerationID:  "readback-generation-1",
		Purpose:       purpose,
		PolicyVersion: "policy-v1",
	}
}

func readbackGeneratorTestHandoff(assignee string) conversation.HandoffEnvelope {
	return conversation.HandoffEnvelope{
		DelegationID:         core.NewRequestID(),
		Revision:             3,
		OriginTurnID:         core.NewTurnID(),
		OriginUserMessageID:  core.NewMessageID(),
		SessionID:            core.NewSessionID(),
		SourceTraceID:        core.NewTraceID(),
		FromActor:            "mio",
		ToActor:              assignee,
		InstructionMessageID: core.NewMessageID(),
		ContextSnapshotHash:  strings.Repeat("a", 64),
		AllowedCapabilities:  []string{"sentinel_capability"},
		ReplyTo:              core.NewMessageID(),
		Goal:                 "Inspect the save wait",
		Instruction:          "Measure the save path and preserve durability",
		Constraints: []conversation.AgentConstraint{
			{Key: "keep_model", Text: "Keep the selected model."},
			{Key: "keep_durability", Text: "Keep durable writes."},
		},
		ExpectedOutput: "A report with evidence",
	}
}

func readbackGeneratorTestJSON(status string, keys ...string) string {
	body, _ := json.Marshal(struct {
		SchemaVersion              string   `json:"schema_version"`
		Status                     string   `json:"status"`
		UnderstoodGoal             string   `json:"understood_goal"`
		AcknowledgedConstraintKeys []string `json:"acknowledged_constraint_keys"`
		ExpectedOutput             string   `json:"expected_output"`
		Text                       string   `json:"text"`
	}{
		SchemaVersion:              "agent.readback.v1",
		Status:                     status,
		UnderstoodGoal:             "Inspect the save wait",
		AcknowledgedConstraintKeys: keys,
		ExpectedOutput:             "A report with evidence",
		Text:                       "I will preserve the model and durability while checking the save path.",
	})
	return string(body)
}

func TestReadbackGeneratorUsesAssigneeProviderPersonaAndStableContext(t *testing.T) {
	assignees := map[string]ReadbackAssigneeConfig{}
	providers := map[string]*readbackGeneratorTestProvider{}
	for _, actor := range []string{"shiro", "kuro", "midori"} {
		provider := &readbackGeneratorTestProvider{
			name:     actor + "-conversation",
			response: llm.GenerateResponse{Content: readbackGeneratorTestJSON("accepted", "keep_model", "keep_durability"), TokensUsed: 17, RequestID: core.NewRequestID(), ResponseID: core.NewResponseID()},
		}
		providers[actor] = provider
		assignees[actor] = ReadbackAssigneeConfig{
			Provider:             provider,
			SystemPrompt:         actor + " authored system prompt",
			StableRuntimeContext: actor + " trusted stable runtime context",
		}
	}
	generator := NewReadbackGenerator(assignees)

	for _, actor := range []string{"shiro", "kuro", "midori"} {
		t.Run(actor, func(t *testing.T) {
			scope := readbackGeneratorTestScope(conversation.PurposeReadback)
			handoff := readbackGeneratorTestHandoff(actor)
			callsBefore := make(map[string]int, len(providers))
			for name, provider := range providers {
				callsBefore[name] = provider.calls
			}
			readback, receipt, err := generator.GenerateReadback(context.Background(), scope, handoff, "authorized original context")
			if err != nil {
				t.Fatalf("GenerateReadback() error = %v", err)
			}
			provider := providers[actor]
			if provider.calls != 1 || provider.scope != scope {
				t.Fatalf("selected provider calls=%d scope=%#v, want one call with %#v", provider.calls, provider.scope, scope)
			}
			if readback.Status != conversation.ReadbackStatusAccepted || readback.DelegationID != handoff.DelegationID || readback.Revision != handoff.Revision || readback.Sender != actor {
				t.Fatalf("readback identity/status = %#v", readback)
			}
			if receipt.ProviderLabel != provider.name || receipt.TokensUsed != 17 || receipt.Status != "completed" {
				t.Fatalf("invocation receipt = %#v", receipt)
			}
			if provider.request.ResponseFormat != llm.ResponseFormatJSONObject || provider.request.OnToken != nil || provider.request.MaxTokens != 512 {
				t.Fatalf("request format/stream/token limit = %q/%t/%d", provider.request.ResponseFormat, provider.request.OnToken != nil, provider.request.MaxTokens)
			}
			if got := promptMessageContent(provider.request.Messages); !strings.Contains(got, actor+" authored system prompt") || !strings.Contains(got, actor+" trusted stable runtime context") {
				t.Fatalf("request omitted %s persona or stable context: %s", actor, got)
			}
			for other, otherProvider := range providers {
				wantCalls := callsBefore[other]
				if other == actor {
					wantCalls++
				}
				if otherProvider.calls != wantCalls {
					t.Fatalf("provider for %s was called while generating %s readback", other, actor)
				}
			}
			prompt := promptMessageContent(provider.request.Messages)
			for _, required := range []string{
				"Use exactly this JSON shape",
				`"acknowledged_constraint_keys":["exact constraint key"]`,
				"Keep understood_goal and expected_output concise; text must be one short sentence in the same language as the request.",
				"Do not copy the full instruction or authorized context into the readback.",
			} {
				if !strings.Contains(prompt, required) {
					t.Fatalf("readback prompt omitted compact output instruction %q", required)
				}
			}
			for _, other := range []string{"shiro", "kuro", "midori"} {
				if other != actor && strings.Contains(prompt, other+" authored system prompt") {
					t.Fatalf("request for %s included %s persona", actor, other)
				}
			}
			for _, forbidden := range []string{"sentinel_capability", "allowed_capabilities", "task_id", "run_id", "action_id"} {
				if strings.Contains(prompt, forbidden) {
					t.Fatalf("readback prompt exposed forbidden field %q: %s", forbidden, prompt)
				}
			}
			if !strings.Contains(prompt, "keep_model") || !strings.Contains(prompt, "keep_durability") || !strings.Contains(prompt, "authorized original context") {
				t.Fatalf("readback prompt omitted constraint keys or authorized context: %s", prompt)
			}
		})
	}
}

func TestReadbackGeneratorParsesEachReadbackStatus(t *testing.T) {
	tests := []struct {
		status string
		keys   []string
	}{
		{status: "accepted", keys: []string{"keep_model", "keep_durability"}},
		{status: "needs_clarification", keys: []string{"keep_model"}},
		{status: "rejected", keys: []string{}},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			provider := &readbackGeneratorTestProvider{response: llm.GenerateResponse{Content: readbackGeneratorTestJSON(test.status, test.keys...)}}
			generator := NewReadbackGenerator(map[string]ReadbackAssigneeConfig{
				"shiro": {Provider: provider, SystemPrompt: "Shiro", StableRuntimeContext: "Shiro stable"},
			})
			readback, _, err := generator.GenerateReadback(context.Background(), readbackGeneratorTestScope(conversation.PurposeReadback), readbackGeneratorTestHandoff("shiro"), "")
			if err != nil {
				t.Fatalf("GenerateReadback() error = %v", err)
			}
			if string(readback.Status) != test.status || len(readback.AcknowledgedConstraintKeys) != len(test.keys) {
				t.Fatalf("parsed readback = %#v", readback)
			}
		})
	}
}

func TestReadbackGeneratorRejectsInvalidOutputWithoutRetry(t *testing.T) {
	for _, content := range []string{
		`{"schema_version":"agent.readback.v1","status":"accepted"}`,
		readbackGeneratorTestJSON("accepted", "keep_model"),
		strings.TrimSuffix(readbackGeneratorTestJSON("accepted", "keep_model", "keep_durability"), "}") + `,"allowed_capabilities":["sentinel_capability"]}`,
	} {
		provider := &readbackGeneratorTestProvider{response: llm.GenerateResponse{Content: content}}
		generator := NewReadbackGenerator(map[string]ReadbackAssigneeConfig{
			"shiro": {Provider: provider, SystemPrompt: "Shiro", StableRuntimeContext: "Shiro stable"},
		})
		_, receipt, err := generator.GenerateReadback(context.Background(), readbackGeneratorTestScope(conversation.PurposeReadback), readbackGeneratorTestHandoff("shiro"), "")
		if err == nil || receipt.Status != "invalid_output" {
			t.Fatalf("GenerateReadback() error=%v receipt=%#v, want invalid output", err, receipt)
		}
		if provider.calls != 1 {
			t.Fatalf("provider calls = %d, want one attempt without retry", provider.calls)
		}
	}
}

func TestReadbackGeneratorReturnsProviderFailureWithoutRetry(t *testing.T) {
	wantErr := errors.New("gateway unavailable")
	provider := &readbackGeneratorTestProvider{err: wantErr}
	generator := NewReadbackGenerator(map[string]ReadbackAssigneeConfig{
		"shiro": {Provider: provider, SystemPrompt: "Shiro", StableRuntimeContext: "Shiro stable"},
	})
	_, receipt, err := generator.GenerateReadback(context.Background(), readbackGeneratorTestScope(conversation.PurposeReadback), readbackGeneratorTestHandoff("shiro"), "")
	if !errors.Is(err, wantErr) || receipt.Status != "generation_failed" {
		t.Fatalf("GenerateReadback() error=%v receipt=%#v, want provider error", err, receipt)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want one attempt without retry", provider.calls)
	}
}

func TestReadbackGeneratorFailsClosedOnCancellation(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		provider := &readbackGeneratorTestProvider{response: llm.GenerateResponse{Content: readbackGeneratorTestJSON("accepted", "keep_model", "keep_durability")}}
		generator := NewReadbackGenerator(map[string]ReadbackAssigneeConfig{"shiro": {Provider: provider, SystemPrompt: "Shiro"}})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := generator.GenerateReadback(ctx, readbackGeneratorTestScope(conversation.PurposeReadback), readbackGeneratorTestHandoff("shiro"), ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("GenerateReadback() error = %v, want context.Canceled", err)
		}
		if provider.calls != 0 {
			t.Fatalf("provider calls = %d, want 0 for canceled context", provider.calls)
		}
	})

	t.Run("canceled during generation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		provider := &readbackGeneratorTestProvider{
			response:   llm.GenerateResponse{Content: readbackGeneratorTestJSON("accepted", "keep_model", "keep_durability")},
			onGenerate: func(context.Context) { cancel() },
		}
		generator := NewReadbackGenerator(map[string]ReadbackAssigneeConfig{"shiro": {Provider: provider, SystemPrompt: "Shiro"}})
		if _, _, err := generator.GenerateReadback(ctx, readbackGeneratorTestScope(conversation.PurposeReadback), readbackGeneratorTestHandoff("shiro"), ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("GenerateReadback() error = %v, want context.Canceled", err)
		}
		if provider.calls != 1 {
			t.Fatalf("provider calls = %d, want one canceled attempt", provider.calls)
		}
	})
}

func TestReadbackGeneratorRejectsWrongScopeAssigneeAndOversizedContext(t *testing.T) {
	provider := &readbackGeneratorTestProvider{response: llm.GenerateResponse{Content: readbackGeneratorTestJSON("accepted", "keep_model", "keep_durability")}}
	generator := NewReadbackGenerator(map[string]ReadbackAssigneeConfig{
		"shiro": {Provider: provider, SystemPrompt: "Shiro"},
	})
	cases := []struct {
		name    string
		scope   conversation.ConversationScope
		handoff conversation.HandoffEnvelope
		context string
	}{
		{name: "wrong purpose", scope: readbackGeneratorTestScope(conversation.PurposeMioTurn), handoff: readbackGeneratorTestHandoff("shiro")},
		{name: "unconfigured assignee", scope: readbackGeneratorTestScope(conversation.PurposeReadback), handoff: readbackGeneratorTestHandoff("kuro")},
		{name: "unsupported assignee", scope: readbackGeneratorTestScope(conversation.PurposeReadback), handoff: readbackGeneratorTestHandoff("coder")},
		{name: "oversized authorized context", scope: readbackGeneratorTestScope(conversation.PurposeReadback), handoff: readbackGeneratorTestHandoff("shiro"), context: strings.Repeat("x", maxReadbackAuthorizedContextBytes+1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := generator.GenerateReadback(context.Background(), test.scope, test.handoff, test.context); err == nil {
				t.Fatal("GenerateReadback() error = nil, want fail-closed rejection")
			}
		})
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for invalid scope/assignee/context", provider.calls)
	}
}

func promptMessageContent(messages []llm.Message) string {
	var content strings.Builder
	for _, message := range messages {
		content.WriteString(message.Content)
		content.WriteByte('\n')
	}
	return content.String()
}
