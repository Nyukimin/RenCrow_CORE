package main

import (
	"context"
	infratransport "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestReconstructLocalAgentInputPreservesCanonicalProjection(t *testing.T) {
	address, err := conversation.NewChannelAddress("line", "U-local-agent")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "local worker input", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	input = input.
		WithSessionID(string(modulecore.NewSessionID())).
		WithViewerRecipient("shiro").
		WithForcedRoute(routing.RouteOPS).
		WithRoute(routing.RouteOPS)
	taskID := modulecore.NewTaskID()
	message, err := domaintransport.NewTurnInputMessage("mio", "shiro", taskID, input)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}

	got, err := reconstructLocalAgentInput(message)
	if err != nil {
		t.Fatalf("reconstructLocalAgentInput() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("reconstructed input invalid: %v", err)
	}
	if got.RootTaskID() != input.RootTaskID() || got.TurnID() != input.TurnID() || got.TraceID() != input.TraceID() || got.UserMessageID() != input.UserMessageID() || got.AgentMessageID() != input.AgentMessageID() {
		t.Fatalf("canonical identities changed: got=%#v want=%#v", got, input)
	}
	if got.MessageText() != input.MessageText() || got.SessionID() != input.SessionID() || got.ChannelAddress() != input.ChannelAddress() {
		t.Fatalf("outer input fields changed: got=%#v want=%#v", got, input)
	}
	if got.ViewerRecipient() != input.ViewerRecipient() || got.ForcedRoute() != routing.RouteOPS || got.Route() != routing.RouteOPS {
		t.Fatalf("route projection changed: recipient=%q forced=%q route=%q", got.ViewerRecipient(), got.ForcedRoute(), got.Route())
	}
	for _, identity := range []string{
		string(got.RootTaskID()), string(got.TurnID()), string(got.TraceID()), string(got.UserMessageID()), string(got.AgentMessageID()),
	} {
		if identity == taskID.String() {
			t.Fatalf("conversation identity reused transport TaskID=%q", taskID)
		}
	}
}

func TestReconstructLocalAgentInputRejectsMissingOrMalformedProjection(t *testing.T) {
	address, err := conversation.NewChannelAddress("viewer", "local-agent")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "local input", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	input = input.WithSessionID(string(modulecore.NewSessionID())).WithRoute(routing.RouteOPS)
	message, err := domaintransport.NewTurnInputMessage("mio", "shiro", modulecore.NewTaskID(), input)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}

	missing := message
	missing.TurnInput = nil
	if _, err := reconstructLocalAgentInput(missing); err == nil {
		t.Fatal("missing turn_input projection must fail closed")
	}

	cases := []struct {
		name   string
		mutate func(*domaintransport.TurnInputContext)
	}{
		{name: "trace id", mutate: func(projection *domaintransport.TurnInputContext) {
			projection.TraceID = modulecore.TraceID("not-a-trace-id")
		}},
		{name: "address", mutate: func(projection *domaintransport.TurnInputContext) {
			projection.ChannelType = "VIEWER"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			malformed := message
			projection := *message.TurnInput
			malformed.TurnInput = &projection
			tc.mutate(malformed.TurnInput)
			if _, err := reconstructLocalAgentInput(malformed); err == nil {
				t.Fatalf("%s corruption must fail closed", tc.name)
			}
		})
	}
}

func TestLocalCoderContextRejectsUnboundOrMismatchedRequests(t *testing.T) {
	owner, ownerCtx := runtimeToolOwnerFixture(t, "", "shiro")
	ownerIdentity, err := domainexecution.IdentityFromContext(ownerCtx)
	if err != nil {
		t.Fatalf("owner identity: %v", err)
	}
	ownerScope, ok := domaintool.ToolExecutionScopeFromContext(ownerCtx)
	if !ok {
		t.Fatal("owner scope missing")
	}
	address, err := conversation.NewChannelAddress("line", "U-local-coder")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(ownerIdentity.TaskID, "local coder input", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	input = input.WithSessionID(string(modulecore.NewSessionID())).WithRoute(routing.RouteOPS)
	message, err := domaintransport.NewTurnInputMessage("shiro", "coder3", ownerIdentity.TaskID, input)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}
	bind := func(taskID modulecore.TaskID, traceID modulecore.TraceID) context.Context {
		bound, bindErr := domainexecution.WithIdentity(context.Background(), taskID, ownerIdentity.RunID, traceID)
		if bindErr != nil {
			t.Fatalf("bind execution identity: %v", bindErr)
		}
		return domaintool.WithToolExecutionScope(bound, ownerScope)
	}
	validCtx := bind(ownerIdentity.TaskID, input.TraceID())
	canceledCtx, cancel := context.WithCancel(validCtx)
	cancel()
	wrongDestination := message
	wrongDestination.To = "coder2"
	cases := []struct {
		name        string
		ctx         context.Context
		msg         domaintransport.Message
		wantCalls   int
		wantSuccess bool
		finishTask  bool
	}{
		{name: "valid", ctx: validCtx, msg: message, wantCalls: 1, wantSuccess: true},
		{name: "missing context", ctx: nil, msg: message, wantCalls: 0},
		{name: "canceled before entry", ctx: canceledCtx, msg: message, wantCalls: 0},
		{name: "mismatched task", ctx: bind(modulecore.NewTaskID(), input.TraceID()), msg: message, wantCalls: 0},
		{name: "mismatched destination", ctx: validCtx, msg: wrongDestination, wantCalls: 0},
		{name: "mismatched trace", ctx: bind(ownerIdentity.TaskID, modulecore.NewTraceID()), msg: message, wantCalls: 0},
		{name: "task finished during generation", ctx: validCtx, msg: message, wantCalls: 1, finishTask: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &localCoderContextProvider{}
			if tc.finishTask {
				provider.cancel = func() {
					if _, err := owner.Succeed(tc.ctx, ownerIdentity.TaskID, "finished concurrently"); err != nil {
						t.Fatal(err)
					}
				}
			}
			coder := &coderAdapter{domainCoder: agent.NewCoderAgent(provider, nil, nil, "")}
			deps := &Dependencies{taskManager: owner}
			response := deps.handleLocalCoderMessage(tc.ctx, "coder3", tc.msg, coder)
			if provider.calls != tc.wantCalls {
				t.Fatalf("provider calls=%d, want %d; response=%#v", provider.calls, tc.wantCalls, response)
			}
			if tc.wantSuccess {
				if response.Type != domaintransport.MessageTypeResult || response.Proposal == nil {
					t.Fatalf("valid response=%#v, want successful proposal", response)
				}
				seenIdentity, identityErr := domainexecution.IdentityFromContext(provider.ctx)
				if identityErr != nil {
					t.Fatalf("provider context identity: %v", identityErr)
				}
				if seenIdentity != (domainexecution.Identity{TaskID: ownerIdentity.TaskID, RunID: ownerIdentity.RunID, TraceID: input.TraceID()}) {
					t.Fatalf("provider identity=%#v, want task=%s run=%s trace=%s", seenIdentity, ownerIdentity.TaskID, ownerIdentity.RunID, input.TraceID())
				}
				seenScope, scopeFound := domaintool.ToolExecutionScopeFromContext(provider.ctx)
				if !scopeFound || seenScope.ActorKind != ownerScope.ActorKind || seenScope.ActorID != ownerScope.ActorID || seenScope.RequestID != ownerScope.RequestID || len(seenScope.AllowedDataScopes) != 1 || seenScope.AllowedDataScopes[0] != domaintool.DataScopePublic {
					t.Fatalf("provider scope=%#v found=%t, want preserved Agent public scope %#v", seenScope, scopeFound, ownerScope)
				}
				return
			}
			if response.Type != domaintransport.MessageTypeError || response.Proposal != nil {
				t.Fatalf("rejected response=%#v, want error without proposal", response)
			}
		})
	}
}

func TestLocalCoderContextCancellationDuringCallRejectsValidResponse(t *testing.T) {
	owner, ownerCtx := runtimeToolOwnerFixture(t, "", "shiro")
	ownerIdentity, err := domainexecution.IdentityFromContext(ownerCtx)
	if err != nil {
		t.Fatalf("owner identity: %v", err)
	}
	ownerScope, ok := domaintool.ToolExecutionScopeFromContext(ownerCtx)
	if !ok {
		t.Fatal("owner scope missing")
	}
	address, err := conversation.NewChannelAddress("line", "U-local-coder-cancel")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(ownerIdentity.TaskID, "local coder input", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	input = input.WithSessionID(string(modulecore.NewSessionID())).WithRoute(routing.RouteOPS)
	message, err := domaintransport.NewTurnInputMessage("shiro", "coder3", ownerIdentity.TaskID, input)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}
	bound, err := domainexecution.WithIdentity(context.Background(), ownerIdentity.TaskID, ownerIdentity.RunID, input.TraceID())
	if err != nil {
		t.Fatalf("bind execution identity: %v", err)
	}
	bound = domaintool.WithToolExecutionScope(bound, ownerScope)
	callCtx, cancel := context.WithCancel(bound)
	provider := &localCoderContextProvider{cancel: cancel}
	coder := &coderAdapter{domainCoder: agent.NewCoderAgent(provider, nil, nil, "")}
	response := (&Dependencies{taskManager: owner}).handleLocalCoderMessage(callCtx, "coder3", message, coder)
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d, want 1", provider.calls)
	}
	if response.Type != domaintransport.MessageTypeError || response.Proposal != nil {
		t.Fatalf("canceled response=%#v, want error without accepted proposal", response)
	}
	if err := callCtx.Err(); err != context.Canceled {
		t.Fatalf("call context error=%v, want context.Canceled", err)
	}
}

const localCoderContextValidProposal = `## Plan
Update the requested file.

## Patch
[
  {"type":"file_edit","action":"update","target":"internal/app.go","content":"package main"}
]

## Risk
low

## CostHint
small`

type localCoderContextProvider struct {
	calls  int
	ctx    context.Context
	cancel context.CancelFunc
}

func (p *localCoderContextProvider) Name() string {
	return "local-coder-context-test"
}

func (p *localCoderContextProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.ctx = ctx
	if p.cancel != nil {
		p.cancel()
	}
	return llm.GenerateResponse{Content: localCoderContextValidProposal, FinishReason: "stop"}, nil
}

func TestLocalCoderReplyPreservesCallerThroughQueue(t *testing.T) {
	for _, from := range []string{"mio", "shiro"} {
		for _, reject := range []bool{false, true} {
			name := from + "/success"
			if reject {
				name = from + "/error"
			}
			t.Run(name, func(t *testing.T) {
				owner, ownerCtx := runtimeToolOwnerFixture(t, "", "shiro")
				identity, err := domainexecution.IdentityFromContext(ownerCtx)
				if err != nil {
					t.Fatal(err)
				}
				scope, _ := domaintool.ToolExecutionScopeFromContext(ownerCtx)
				address, err := conversation.NewChannelAddress("viewer", "coder-reply")
				if err != nil {
					t.Fatal(err)
				}
				input, err := conversation.NewTurnInput(identity.TaskID, "generate patch", address)
				if err != nil {
					t.Fatal(err)
				}
				input = input.WithSessionID(string(modulecore.NewSessionID())).WithRoute(routing.RouteCODE3)
				request, err := domaintransport.NewTurnInputMessage(from, "coder3", identity.TaskID, input)
				if err != nil {
					t.Fatal(err)
				}
				bound, err := domainexecution.WithIdentity(context.Background(), identity.TaskID, identity.RunID, input.TraceID())
				if err != nil {
					t.Fatal(err)
				}
				bound = domaintool.WithToolExecutionScope(bound, scope)
				ctx, cancel := context.WithTimeout(bound, time.Second)
				defer cancel()
				queue := infratransport.NewLocalTransport()
				defer queue.Close()
				replyQueue := infratransport.NewLocalTransport()
				defer replyQueue.Close()
				router := infratransport.NewMessageRouter()
				defer router.Stop()
				router.RegisterAgent(from, replyQueue)
				if err := queue.PutInboundExecution(ctx, request); err != nil {
					t.Fatal(err)
				}
				delivery, err := queue.ReceiveDelivery(ctx)
				if err != nil {
					t.Fatal(err)
				}
				provider := &localCoderContextProvider{}
				coder := &coderAdapter{domainCoder: agent.NewCoderAgent(provider, nil, nil, "")}
				if reject {
					coder = nil
				}
				deps := &Dependencies{taskManager: owner, router: router}
				response := deps.handleLocalCoderMessage(delivery.Context, "coder3", delivery.Message, coder)
				if response.From != request.To || response.To != request.From || response.TaskID != request.TaskID || response.SessionID != request.SessionID {
					t.Fatalf("reply correlation changed: request=%#v response=%#v", request, response)
				}
				deps.deliverLocalAgentResponse(response)
				got, err := replyQueue.Receive(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if reject {
					if got.Type != domaintransport.MessageTypeError || got.Proposal != nil || provider.calls != 0 {
						t.Fatalf("invalid error reply: %#v", got)
					}
				} else {
					if got.Type != domaintransport.MessageTypeResult || got.Proposal == nil || provider.calls != 1 {
						t.Fatalf("invalid successful reply: %#v", got)
					}
					seen, err := domainexecution.IdentityFromContext(provider.ctx)
					if err != nil || seen.TaskID != identity.TaskID || seen.RunID != identity.RunID || seen.TraceID != input.TraceID() {
						t.Fatalf("identity changed: %#v %v", seen, err)
					}
					seenScope, ok := domaintool.ToolExecutionScopeFromContext(provider.ctx)
					if !ok || seenScope.ActorID != "shiro" || seenScope.RequestID != scope.RequestID {
						t.Fatalf("mailbox changed Actor scope: %#v", seenScope)
					}
				}
			})
		}
	}
}
