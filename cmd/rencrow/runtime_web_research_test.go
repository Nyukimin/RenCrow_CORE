package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type runtimeWebResearchProvider struct {
	chatCalls     int
	final         llm.GenerateRequest
	chatMaxTokens []int
}

func (p *runtimeWebResearchProvider) Name() string { return "selected-agent" }

func (p *runtimeWebResearchProvider) Generate(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.final = request
	return llm.GenerateResponse{Content: "answer", FinishReason: "stop"}, nil
}

func (p *runtimeWebResearchProvider) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	p.chatCalls++
	p.chatMaxTokens = append(p.chatMaxTokens, request.MaxTokens)
	if p.chatCalls%2 == 1 {
		if len(request.Tools) == 0 {
			return llm.ChatResponse{}, fmt.Errorf("missing executable query schema")
		}
		return llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "actual-fetch", Function: llm.ToolCallFunction{Name: "web_gather.fetch", Arguments: map[string]any{"url": "https://example.org"}}}}}}, nil
	}
	return llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: "done"}}, nil
}

type runtimeWebQueryRunner struct {
	calls int
	actor string
}

func (r *runtimeWebQueryRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{{ToolID: "web_gather.fetch", Category: "query", Parameters: map[string]any{"type": "object"}}}, nil
}

func (r *runtimeWebQueryRunner) ExecuteV2(ctx context.Context, _ string, _ map[string]any) (*tool.ToolResponse, error) {
	r.calls++
	scope, _ := tool.ToolExecutionScopeFromContext(ctx)
	r.actor = scope.ActorID
	return tool.NewSuccess("verified body https://example.org"), nil
}

func TestRuntimeWebResearchWiringUsesSelectedProvidersAndActualQueryRunner(t *testing.T) {
	for _, name := range []string{"mio", "shiro-chat", "shiro", "midori", "aka", "ao", "kin", "gin"} {
		t.Run(name, func(t *testing.T) {
			provider := &runtimeWebResearchProvider{}
			role := "coder"
			switch name {
			case "mio":
				role = "chat"
			case "shiro", "shiro-chat":
				role = "chatworker"
			case "midori":
				role = "wild"
			}
			// Exercise the real production middleware rather than only a bare
			// Gateway stub: every layer must retain the Chat Tool contract.
			wrapped := wrapPrimaryLLMProvider(&config.Config{}, role, enforceThinkingPolicyForRole(role, provider), nil)
			runner := &runtimeWebQueryRunner{}
			var agents agentRuntime
			var coder *coderAdapter
			switch name {
			case "mio":
				agents.Mio = agent.NewMioAgent(wrapped, nil, nil, nil, nil, nil)
			case "shiro-chat":
				agents.ShiroChat = agent.NewMioAgent(wrapped, nil, nil, nil, nil, nil)
			case "shiro":
				agents.Shiro = agent.NewShiroAgent(wrapped, nil, nil, "shiro", nil)
			case "midori":
				agents.Wild = agent.NewWildAgent(wrapped, "midori")
			default:
				coder = &coderAdapter{domainCoder: agent.NewCoderAgent(wrapped, nil, nil, "coder")}
			}
			applyRuntimeAgentWebResearch(agents, toolRuntime{WorkerRuntimeRunnerV2: runner}, coder)
			scope, err := tool.NewToolExecutionScope("request", tool.ActorKindAgent, name, "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
			if err != nil {
				t.Fatal(err)
			}
			ctx := tool.WithToolExecutionScope(context.Background(), scope)
			input := runtimeClassifierTestTurn(t, "https://example.org を取得して")
			switch name {
			case "mio":
				_, err = agents.Mio.Chat(ctx, input)
			case "shiro-chat":
				_, err = agents.ShiroChat.Chat(ctx, input)
			case "shiro":
				_, err = agents.Shiro.Execute(ctx, input)
			case "midori":
				_, err = agents.Wild.Generate(ctx, input)
			default:
				_, err = coder.GenerateWithContext(ctx, []llm.Message{{Role: "user", Content: input.MessageText()}})
			}
			if err != nil || runner.calls != 1 || runner.actor != name || provider.chatCalls != 2 {
				t.Fatalf("wrong runtime path: calls=%d actor=%s model calls=%d err=%v", runner.calls, runner.actor, provider.chatCalls, err)
			}
			wantResearchTokens := 2048
			switch name {
			case "midori":
				wantResearchTokens = 32768
				if provider.final.MaxTokens != 32768 {
					t.Fatalf("Midori final budget = %d, want 32768", provider.final.MaxTokens)
				}
			case "shiro":
				wantResearchTokens = 4096
			case "aka", "ao", "kin", "gin":
				wantResearchTokens = 8192
			}
			for _, got := range provider.chatMaxTokens {
				if got != wantResearchTokens {
					t.Fatalf("%s research budget = %d, want %d", name, got, wantResearchTokens)
				}
			}
			found := false
			for _, message := range provider.final.Messages {
				found = found || strings.Contains(message.Content, "verified body")
			}
			if !found {
				t.Fatal("runtime query results were not consumed by the final model")
			}
		})
	}
}

func TestRuntimeWebResearchKeepsKuroNativeToolRuntime(t *testing.T) {
	provider := &runtimeWebResearchProvider{}
	kuro := agent.NewHeavyAgent(provider, "kuro")
	runner := &runtimeWebQueryRunner{}
	applyRuntimeAgentWebResearch(agentRuntime{Heavy: kuro}, toolRuntime{WorkerRuntimeRunnerV2: runner})
	if _, err := kuro.Generate(context.Background(), runtimeClassifierTestTurn(t, "検索して")); err != nil {
		t.Fatal(err)
	}
	if provider.chatCalls != 0 || runner.calls != 0 || len(provider.final.Messages) == 0 {
		t.Fatal("CORE duplicated or replaced Kuro's native tool execution")
	}
}
