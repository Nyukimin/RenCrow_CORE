package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func privateWorkTestContext(t *testing.T, input conversation.TurnInput, actorID string) context.Context {
	t.Helper()
	ctx, err := execution.WithIdentity(context.Background(), input.RootTaskID(), modulecore.NewRunID(), input.TraceID())
	if err != nil {
		t.Fatalf("bind execution identity: %v", err)
	}
	scope, err := tool.NewToolExecutionScope(
		"private-work-test",
		tool.ActorKindAgent,
		actorID,
		"",
		[]string{tool.DataScopePublic},
		tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("create tool execution scope: %v", err)
	}
	scope.AgentRole = actorID
	scope.Purpose = "private_work"
	return tool.WithToolExecutionScope(ctx, scope)
}

func privateWorkTestInput(t *testing.T, message string) conversation.TurnInput {
	t.Helper()
	return newAgentTurnInput(t, message, "viewer", "private-work-chat")
}

func TestPrivateWorkEntryRejectsMissingOrMismatchedExecutionIdentity(t *testing.T) {
	input := privateWorkTestInput(t, "private request")
	t.Run("missing identity", func(t *testing.T) {
		ctx := privateWorkTestContextWithoutIdentity(t, "shiro")
		providerCalls := 0
		shiro := NewShiroAgent(&mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
			providerCalls++
			return llm.GenerateResponse{Content: "unexpected"}, nil
		}}, nil, nil, "shiro", nil)
		if _, err := shiro.ExecutePrivateWork(ctx, input, nil); err == nil {
			t.Fatal("expected missing execution identity to be rejected")
		}
		if providerCalls != 0 {
			t.Fatalf("provider calls=%d, want 0", providerCalls)
		}
	})

	for _, actor := range []string{"shiro", "kuro", "midori"} {
		t.Run(actor+" rejects another Task", func(t *testing.T) {
			ctx := privateWorkTestContext(t, input, actor)
			ctx, err := execution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), input.TraceID())
			if err != nil {
				t.Fatal(err)
			}
			ctx = withPrivateWorkActorScope(t, ctx, actor)
			if _, err := executePrivateWorkForActor(t, actor, ctx, input, nil); err == nil {
				t.Fatal("expected mismatched Task identity to be rejected")
			}
		})

		t.Run(actor+" rejects another Actor", func(t *testing.T) {
			ctx := privateWorkTestContext(t, input, "not-"+actor)
			if _, err := executePrivateWorkForActor(t, actor, ctx, input, nil); err == nil {
				t.Fatal("expected mismatched actor scope to be rejected")
			}
		})
	}
}

func TestShiroExecutePrivateWorkWithNoCapabilitiesUsesTextOnlyWithoutPublicTurnOrExternalEffects(t *testing.T) {
	input := privateWorkTestInput(t, "昔話を生成して")
	ctx := privateWorkTestContext(t, input, "shiro")
	beginCalls, commitCalls := 0, 0
	engine := &mockConversationEngine{
		beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
			beginCalls++
			return nil, nil
		},
		commitTurnFunc: func(context.Context, conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
			commitCalls++
			return conversation.ConversationTurnResult{}, nil
		},
	}
	toolCalls, advisorCalls, subagentCalls, nativeCalls, providerCalls := 0, 0, 0, 0, 0
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "codex.run"}}, nil
		},
		executeV2Func: func(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
			toolCalls++
			return tool.NewSuccess("unexpected tool result"), nil
		},
	}
	advisor := &privateWorkAdvisor{calls: &advisorCalls}
	manager := &mockSubagentManager{runSyncFunc: func(context.Context, SubagentTask) (SubagentResult, error) {
		subagentCalls++
		return SubagentResult{Output: "unexpected subagent result"}, nil
	}}
	provider := &mockLLMProvider{generateFunc: func(gotCtx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
		providerCalls++
		if _, err := execution.IdentityFromContext(gotCtx); err != nil {
			t.Errorf("Task-owned execution identity was lost: %v", err)
		}
		return llm.GenerateResponse{Content: "text only result"}, nil
	}}
	shiro := NewShiroAgent(provider, runner, &mockMCPClient{}, "shiro", manager).
		WithConversationEngine(engine).
		WithAdvisorService(advisor).
		WithNativeCodingDelegate(privateWorkNativeDelegate{calls: &nativeCalls})

	got, err := shiro.ExecutePrivateWork(ctx, input, nil)
	if err != nil {
		t.Fatalf("ExecutePrivateWork failed: %v", err)
	}
	if got != "text only result" || providerCalls != 1 {
		t.Fatalf("result=%q provider calls=%d, want text result and one provider call", got, providerCalls)
	}
	if beginCalls != 0 || commitCalls != 0 {
		t.Fatalf("legacy conversation calls: begin=%d commit=%d, want 0", beginCalls, commitCalls)
	}
	if toolCalls != 0 || advisorCalls != 0 || subagentCalls != 0 || nativeCalls != 0 {
		t.Fatalf("external effects without capabilities: tool=%d advisor=%d subagent=%d native=%d", toolCalls, advisorCalls, subagentCalls, nativeCalls)
	}
}

func TestShiroExecutePrivateWorkUsesGrantedNativeSelection(t *testing.T) {
	input := privateWorkTestInput(t, "この場面を描画して")
	input, err := input.WithBackendSelection(conversation.BackendShiroNativeCodingV1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := privateWorkTestContext(t, input, "shiro")
	nativeCalls, providerCalls := 0, 0
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		providerCalls++
		return llm.GenerateResponse{Content: "unexpected provider result"}, nil
	}}
	shiro := NewShiroAgent(provider, nil, nil, "shiro", nil).
		WithNativeCodingDelegate(privateWorkNativeDelegate{calls: &nativeCalls})

	got, err := shiro.ExecutePrivateWork(ctx, input, []string{privateCapabilityNativeCoding})
	if err != nil || got != "native result" {
		t.Fatalf("ExecutePrivateWork() = %q, %v; want native delegate result", got, err)
	}
	if nativeCalls != 1 || providerCalls != 0 {
		t.Fatalf("native calls=%d provider calls=%d, want one native call and no provider call", nativeCalls, providerCalls)
	}
}

func TestShiroExecutePrivateWorkRejectsNativeSelectionWithoutExactCapability(t *testing.T) {
	input := privateWorkTestInput(t, "この場面を描画して")
	input, err := input.WithBackendSelection(conversation.BackendShiroNativeCodingV1)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name         string
		capabilities []string
	}{
		{name: "no capabilities"},
		{name: "unrelated codex.run capability", capabilities: []string{privateCapabilityCodexRun}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := privateWorkTestContext(t, input, "shiro")
			beginCalls, commitCalls := 0, 0
			engine := &mockConversationEngine{
				beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
					beginCalls++
					return nil, nil
				},
				commitTurnFunc: func(context.Context, conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
					commitCalls++
					return conversation.ConversationTurnResult{}, nil
				},
			}
			toolListCalls, toolCalls, advisorCalls, subagentCalls, nativeCalls, providerCalls := 0, 0, 0, 0, 0, 0
			runner := &mockToolRunner{
				listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
					toolListCalls++
					return []tool.ToolMetadata{{ToolID: "codex.run"}}, nil
				},
				executeV2Func: func(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
					toolCalls++
					return tool.NewSuccess("unexpected tool result"), nil
				},
			}
			manager := &mockSubagentManager{runSyncFunc: func(context.Context, SubagentTask) (SubagentResult, error) {
				subagentCalls++
				return SubagentResult{Output: "unexpected subagent result"}, nil
			}}
			provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
				providerCalls++
				return llm.GenerateResponse{Content: "unexpected provider result"}, nil
			}}
			shiro := NewShiroAgent(provider, runner, &mockMCPClient{}, "shiro", manager).
				WithConversationEngine(engine).
				WithAdvisorService(&privateWorkAdvisor{calls: &advisorCalls}).
				WithNativeCodingDelegate(privateWorkNativeDelegate{calls: &nativeCalls})

			got, err := shiro.ExecutePrivateWork(ctx, input, tc.capabilities)
			if got != "" || !errors.Is(err, ErrPrivateWorkCapabilityDenied) || !errors.Is(err, ErrNativeCodingRejected) {
				t.Fatalf("ExecutePrivateWork() = %q, %v; want empty output wrapping both private-capability and native-rejection errors", got, err)
			}
			if beginCalls != 0 || commitCalls != 0 || toolListCalls != 0 || toolCalls != 0 || advisorCalls != 0 || subagentCalls != 0 || nativeCalls != 0 || providerCalls != 0 {
				t.Fatalf("rejected selection reached collaborators: conversation begin=%d commit=%d tool list=%d tool=%d advisor=%d subagent=%d native=%d provider=%d",
					beginCalls, commitCalls, toolListCalls, toolCalls, advisorCalls, subagentCalls, nativeCalls, providerCalls)
			}
		})
	}
}

func TestShiroExecutePrivateWorkKeepsGrantedToolInsideTaskContext(t *testing.T) {
	input := privateWorkTestInput(t, "昔話を生成して")
	ctx := privateWorkTestContext(t, input, "shiro")
	toolCalls := 0
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "codex.run"}, {ToolID: "shell.exec"}}, nil
		},
		executeV2Func: func(gotCtx context.Context, toolName string, _ map[string]any) (*tool.ToolResponse, error) {
			toolCalls++
			if toolName != "codex.run" {
				t.Fatalf("toolName=%q, want codex.run", toolName)
			}
			if _, err := execution.IdentityFromContext(gotCtx); err != nil {
				t.Fatalf("Task-owned execution identity was lost: %v", err)
			}
			if scope, ok := tool.ToolExecutionScopeFromContext(gotCtx); !ok || scope.ActorID != "shiro" {
				t.Fatalf("Task-owned actor scope was lost: %#v, present=%v", scope, ok)
			}
			return tool.NewSuccess("task scoped codex result"), nil
		},
	}
	shiro := NewShiroAgent(&mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		t.Fatal("LLM provider should not be used after the granted Codex route handles the request")
		return llm.GenerateResponse{}, nil
	}}, runner, nil, "shiro", nil)
	got, err := shiro.ExecutePrivateWork(ctx, input, []string{"codex.run"})
	if err != nil {
		t.Fatalf("ExecutePrivateWork failed: %v", err)
	}
	if got != "task scoped codex result" || toolCalls != 1 {
		t.Fatalf("result=%q tool calls=%d", got, toolCalls)
	}
}

func TestKuroExecutePrivateWorkDeniesFullAccessProviderWithoutExplicitCapabilityAndDoesNotPersist(t *testing.T) {
	input := privateWorkTestInput(t, "/analyze 原因を確認して")
	ctx := privateWorkTestContext(t, input, "kuro")
	providerCalls, beginCalls, commitCalls := 0, 0, 0
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		providerCalls++
		return llm.GenerateResponse{Content: "private analysis"}, nil
	}}
	engine := &mockConversationEngine{
		beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
			beginCalls++
			return nil, nil
		},
		commitTurnFunc: func(context.Context, conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
			commitCalls++
			return conversation.ConversationTurnResult{}, nil
		},
	}
	kuro := NewHeavyAgent(provider, "kuro").WithConversationEngine(engine)
	if _, err := kuro.ExecutePrivateWork(ctx, input, nil); !errors.Is(err, ErrPrivateWorkCapabilityDenied) {
		t.Fatalf("empty capabilities error=%v, want ErrPrivateWorkCapabilityDenied", err)
	}
	if providerCalls != 0 {
		t.Fatalf("provider calls without capability=%d, want 0", providerCalls)
	}

	got, err := kuro.ExecutePrivateWork(ctx, input, []string{privateCapabilityKuroCodexFullAccess})
	if err != nil {
		t.Fatalf("explicitly scoped private generation failed: %v", err)
	}
	if got != "private analysis" || providerCalls != 1 {
		t.Fatalf("result=%q provider calls=%d", got, providerCalls)
	}
	if beginCalls != 0 || commitCalls != 0 {
		t.Fatalf("legacy conversation calls: begin=%d commit=%d, want 0", beginCalls, commitCalls)
	}
}

func TestMidoriExecutePrivateWorkWithNoCapabilitiesKeepsTextGenerationAndBlocksImageEffect(t *testing.T) {
	input := privateWorkTestInput(t, "/wild Mioの画像を生成して")
	ctx := privateWorkTestContext(t, input, "midori")
	beginCalls, commitCalls, providerCalls := 0, 0, 0
	engine := &mockConversationEngine{
		beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
			beginCalls++
			return nil, nil
		},
		commitTurnFunc: func(context.Context, conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
			commitCalls++
			return conversation.ConversationTurnResult{}, nil
		},
	}
	imageGenerator := &mockWildImageGenerator{}
	provider := &mockLLMProvider{generateFunc: func(gotCtx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		providerCalls++
		if _, err := execution.IdentityFromContext(gotCtx); err != nil {
			t.Errorf("Task-owned execution identity was lost: %v", err)
		}
		return llm.GenerateResponse{Content: "image prompt only"}, nil
	}}
	wild := NewWildAgent(provider, "midori").WithConversationEngine(engine).WithImageGenerator(imageGenerator)
	got, err := wild.ExecutePrivateWork(ctx, input, nil)
	if err != nil {
		t.Fatalf("ExecutePrivateWork failed: %v", err)
	}
	if got != "image prompt only" || providerCalls != 1 {
		t.Fatalf("result=%q provider calls=%d, want text generation", got, providerCalls)
	}
	if imageGenerator.called {
		t.Fatal("image generator was called without its capability")
	}
	if beginCalls != 0 || commitCalls != 0 {
		t.Fatalf("legacy conversation calls: begin=%d commit=%d, want 0", beginCalls, commitCalls)
	}
}

func TestMidoriExecutePrivateWorkUsesImageGeneratorOnlyWhenGranted(t *testing.T) {
	input := privateWorkTestInput(t, "/wild Mioの画像を生成して")
	ctx := privateWorkTestContext(t, input, "midori")
	imageGenerator := &mockWildImageGenerator{result: ImageGenerationResult{PromptID: "private-image", ImageURL: "https://example.test/private.png"}}
	wild := NewWildAgent(&mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		t.Fatal("LLM provider should not run when the explicitly granted image route handles the request")
		return llm.GenerateResponse{}, nil
	}}, "midori").WithImageGenerator(imageGenerator)
	got, err := wild.ExecutePrivateWork(ctx, input, []string{privateCapabilityImageGenerate})
	if err != nil {
		t.Fatalf("ExecutePrivateWork failed: %v", err)
	}
	if !imageGenerator.called || !strings.Contains(got, "private-image") {
		t.Fatalf("image generator called=%v response=%q", imageGenerator.called, got)
	}
}

type privateWorkAdvisor struct{ calls *int }

func (a *privateWorkAdvisor) RequestAdvice(context.Context, advisor.AdviceRequest) (advisor.AdviceResult, error) {
	*a.calls++
	return advisor.AdviceResult{}, nil
}

type privateWorkNativeDelegate struct{ calls *int }

func (d privateWorkNativeDelegate) DelegateNativeCoding(context.Context, NativeCodingRequest) (NativeCodingResult, error) {
	*d.calls++
	return NativeCodingResult{Status: NativeRunCompleted, Verification: NativeVerificationPassed, FinalText: "native result"}, nil
}

func privateWorkTestContextWithoutIdentity(t *testing.T, actorID string) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope("private-work-test", tool.ActorKindAgent, actorID, "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	scope.AgentRole = actorID
	scope.Purpose = "private_work"
	return tool.WithToolExecutionScope(context.Background(), scope)
}

func withPrivateWorkActorScope(t *testing.T, ctx context.Context, actorID string) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope("private-work-test", tool.ActorKindAgent, actorID, "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	scope.AgentRole = actorID
	scope.Purpose = "private_work"
	return tool.WithToolExecutionScope(ctx, scope)
}

func executePrivateWorkForActor(t *testing.T, actorID string, ctx context.Context, input conversation.TurnInput, capabilities []string) (string, error) {
	t.Helper()
	switch actorID {
	case "shiro":
		return NewShiroAgent(&mockLLMProvider{}, nil, nil, "shiro", nil).ExecutePrivateWork(ctx, input, capabilities)
	case "kuro":
		return NewHeavyAgent(&mockLLMProvider{}, "kuro").ExecutePrivateWork(ctx, input, capabilities)
	case "midori":
		return NewWildAgent(&mockLLMProvider{}, "midori").ExecutePrivateWork(ctx, input, capabilities)
	default:
		t.Fatalf("unknown actor %q", actorID)
		return "", nil
	}
}
