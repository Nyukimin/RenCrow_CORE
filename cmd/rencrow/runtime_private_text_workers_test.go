package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildPrimaryLLMProvidersBuildsPrivateTextBindingsWhileBoardIsDisabled(t *testing.T) {
	requests := make(map[string]map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Gateway request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		alias, _ := payload["model"].(string)
		requests[alias] = payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"private text"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	providers := buildPrimaryLLMProviders(&config.Config{
		LLMGateway:         config.LLMGatewayConfig{Enabled: true, BaseURL: server.URL, TimeoutSec: 5},
		BoardCommunication: config.BoardCommunicationConfig{Enabled: false},
	}, nil)
	privateProviders := providers.PrivateTextProviders
	if privateProviders == nil {
		t.Fatal("private text Gateway bindings are nil")
	}
	wantActors := []string{"kuro", "aka", "ao", "kin", "gin"}
	if len(privateProviders) != len(wantActors) {
		t.Fatalf("private text Gateway provider count = %d, want %d", len(privateProviders), len(wantActors))
	}

	for _, actorID := range wantActors {
		t.Run(actorID, func(t *testing.T) {
			provider := privateProviders[actorID]
			if provider == nil {
				t.Fatalf("private text Gateway provider for %s is nil", actorID)
			}
			if _, ok := provider.(llm.ToolCallingProvider); ok {
				t.Fatalf("private text Gateway provider for %s exposes tool calling", actorID)
			}
			if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
				Messages: []llm.Message{{Role: "user", Content: "private test input"}},
			}); err != nil {
				t.Fatalf("Generate(%s private text): %v", actorID, err)
			}

			alias := actorID + "-board-chat"
			payload := requests[alias]
			if payload == nil {
				t.Fatalf("Gateway request for exact private text alias %q is missing", alias)
			}
			metadata, _ := payload["rencrow"].(map[string]any)
			for key, want := range map[string]string{
				"agent_id": actorID, "execution_role": "board-chat", "execution_alias": alias,
			} {
				if metadata[key] != want {
					t.Errorf("private text Gateway metadata[%q] = %#v, want %q; all=%#v", key, metadata[key], want, metadata)
				}
			}
		})
	}
}

func TestNewPrivateTextWorkersUsesOnlyExactActorsWithAuthoredPromptsAndProviders(t *testing.T) {
	providers := map[string]llm.LLMProvider{
		"kuro": &privateTextWorkerProviderSpy{response: "kuro"},
		"aka":  &privateTextWorkerProviderSpy{response: "aka"},
		"ao":   &privateTextWorkerProviderSpy{response: "ao"},
		"kin":  &privateTextWorkerProviderSpy{response: "kin"},
		"gin":  &privateTextWorkerProviderSpy{response: "gin"},
		"mio":  &privateTextWorkerProviderSpy{response: "mio"},
	}
	prompts := map[string]string{
		"kuro": " authored Kuro prompt ",
		"aka":  " ",
		"ao":   " ",
		"kin":  "authored Kin prompt",
		"gin":  "authored Gin prompt",
		"mio":  "authored Mio prompt",
	}

	workers := newPrivateTextWorkers(providers, prompts)
	if len(workers) != 3 {
		t.Fatalf("private text worker count = %d, want only Kuro, Kin, and Gin with complete bindings", len(workers))
	}
	for actorID, wantPrompt := range map[string]string{
		"kuro": "authored Kuro prompt", "kin": "authored Kin prompt", "gin": "authored Gin prompt",
	} {
		worker, ok := workers[actorID].(*privateActorTextWorker)
		if !ok || worker.actorID != actorID || worker.systemPrompt != wantPrompt {
			t.Errorf("worker[%s] = %#v, want exact actor and authored prompt", actorID, workers[actorID])
		}
	}
	for _, actorID := range []string{"aka", "ao", "mio"} {
		if workers[actorID] != nil {
			t.Errorf("worker[%s] = %#v, want unavailable when prompt or actor allowlist is missing", actorID, workers[actorID])
		}
	}

	runner := newPrivateTaskRunQueueRunner(nil, "ren", nil, nil, nil, workers)
	for actorID, worker := range workers {
		if runner.actors[actorID] != worker {
			t.Errorf("runner actor[%s] = %#v, want exact configured worker", actorID, runner.actors[actorID])
		}
	}
}

func TestPrivateActorTextWorkerSendsOnlyAuthoredPromptAndPrivateUserText(t *testing.T) {
	provider := &privateTextWorkerProviderSpy{response: "generated private text"}
	worker := newPrivateTextWorkers(map[string]llm.LLMProvider{"aka": provider}, map[string]string{"aka": "authored Aka character prompt"})["aka"]
	if worker == nil {
		t.Fatal("Aka private text worker was not created from its exact provider and authored prompt")
	}
	ctx, input := privateTextWorkerTestContext(t, "aka", "private user request")
	got, err := worker.ExecutePrivateWork(ctx, input, nil)
	if err != nil || got != provider.response {
		t.Fatalf("ExecutePrivateWork() = %q, %v; want one private text response", got, err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want exactly one", provider.calls)
	}
	if len(provider.request.Messages) != 2 || provider.request.Messages[0].Role != "system" ||
		provider.request.Messages[0].Content != "authored Aka character prompt" || provider.request.Messages[0].Type != llm.PromptContextCharacter ||
		provider.request.Messages[1].Role != "user" || provider.request.Messages[1].Content != "private user request" ||
		provider.request.Messages[1].Type != llm.PromptContextUser {
		t.Fatalf("private text request messages = %#v, want only the exact authored prompt and user text", provider.request.Messages)
	}
	if provider.request.ResponseFormat != llm.ResponseFormatText || provider.request.OnToken != nil {
		t.Fatalf("private text request = %+v, want non-streaming plain text", provider.request)
	}
	observation, ok := llm.ExecutionObservationFromContext(provider.ctx)
	if !ok || observation.TaskID != input.RootTaskID() || observation.TraceID != string(input.TraceID()) || observation.Initiator != "aka" {
		t.Fatalf("provider observation = %+v found=%t, want exact Task/Trace and Aka initiator", observation, ok)
	}
}

func TestPrivateActorTextWorkerRejectsCapabilitiesAndMismatchedIdentityBeforeProvider(t *testing.T) {
	for _, test := range []struct {
		name         string
		actorID      string
		capabilities []string
		changeInput  func(conversation.TurnInput) conversation.TurnInput
	}{
		{name: "tool capability", actorID: "aka", capabilities: []string{"codex.run"}},
		{name: "recipient mismatch", actorID: "aka", changeInput: func(input conversation.TurnInput) conversation.TurnInput { return input.WithViewerRecipient("ao") }},
		{name: "unsupported actor", actorID: "lumina"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &privateTextWorkerProviderSpy{response: "must not be generated"}
			worker := &privateActorTextWorker{actorID: test.actorID, systemPrompt: "authored test prompt", provider: provider}
			ctx, input := privateTextWorkerTestContext(t, "aka", "private input")
			if test.changeInput != nil {
				input = test.changeInput(input)
			}
			if _, err := worker.ExecutePrivateWork(ctx, input, test.capabilities); err == nil {
				t.Fatal("ExecutePrivateWork() succeeded, want fail-closed refusal")
			}
			if provider.calls != 0 {
				t.Fatalf("provider calls = %d, want no request for invalid capability, actor, or identity", provider.calls)
			}
		})
	}
}

type privateTextWorkerProviderSpy struct {
	calls    int
	ctx      context.Context
	request  llm.GenerateRequest
	response string
	err      error
}

func (p *privateTextWorkerProviderSpy) Generate(ctx context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.ctx = ctx
	p.request = request
	return llm.GenerateResponse{Content: p.response}, p.err
}

func (*privateTextWorkerProviderSpy) Name() string { return "private-text-worker-test" }

func privateTextWorkerTestContext(t *testing.T, actorID, message string) (context.Context, conversation.TurnInput) {
	t.Helper()
	taskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	address, err := conversation.NewChannelAddress("superagent", taskID.String())
	if err != nil {
		t.Fatal(err)
	}
	input, err := conversation.ReconstructTurnInput(
		taskID, modulecore.NewTurnID(), traceID, modulecore.NewMessageID(), modulecore.NewMessageID(), message, address,
	)
	if err != nil {
		t.Fatal(err)
	}
	input = input.WithViewerRecipient(actorID).WithRoute(domainrouting.RouteCHAT)
	ctx, err := execution.WithIdentity(context.Background(), taskID, modulecore.NewRunID(), traceID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope(
		string(traceID), domaintool.ActorKindAgent, actorID, "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatal(err)
	}
	scope.AgentRole, scope.Purpose = "worker", "private_task_execution"
	ctx = domaintool.WithToolExecutionScope(ctx, scope)
	return ctx, input
}
