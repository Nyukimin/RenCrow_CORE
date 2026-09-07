package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type opsScopeCaptureShiro struct {
	calls int
	ctx   context.Context
}

func (s *opsScopeCaptureShiro) Execute(ctx context.Context, _ conversation.TurnInput) (string, error) {
	s.calls++
	s.ctx = ctx
	return "ops complete", nil
}

func TestMessageRouteDispatcherOPSBuildsShiroWorkerScope(t *testing.T) {
	parent, err := domaintool.NewToolExecutionScope(
		"req-parent",
		domaintool.ActorKindUser,
		"user-a",
		"user-a",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	parentCtx := domaintool.WithToolExecutionScope(context.Background(), parent)
	shiro := &opsScopeCaptureShiro{}
	dispatcher := newMessageRouteDispatcher(
		&mockMioAgent{},
		shiro,
		nil,
		func(string, string, string, string, string, string, string, string, string) {},
		nil,
		func(context.Context, string, routing.Route, string, string) {},
	)
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	tk := newOrchestratorTestTurnInput(t, "運用データを確認して", "line", "user-a").WithSessionID("session-1")

	response, err := dispatcher.ExecuteDirect(parentCtx, tk, routing.RouteOPS, taskID, runID, "")
	if err != nil {
		t.Fatalf("ExecuteDirect() error = %v", err)
	}
	if response != "ops complete" || shiro.calls != 1 {
		t.Fatalf("response=%q shiro calls=%d", response, shiro.calls)
	}
	got, ok := domaintool.ToolExecutionScopeFromContext(shiro.ctx)
	if !ok {
		t.Fatal("Shiro did not receive a trusted execution scope")
	}
	if got.RequestID != "req-parent" || got.ActorKind != domaintool.ActorKindAgent || got.ActorID != "shiro" {
		t.Fatalf("Shiro scope identity = %#v", got)
	}
	if got.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator || got.AgentRole != "worker" || got.Purpose != "ops" {
		t.Fatalf("Shiro scope metadata = %#v", got)
	}
	if got.AuthenticatedUserID != "user-a" || !got.Allows(domaintool.DataScopePublic) || !got.Allows(domaintool.DataScopeUser) || !got.Allows(domaintool.DataScopeInternal) {
		t.Fatalf("Shiro delegated scope = %#v", got)
	}
}

func TestMessageRouteDispatcherOPSRejectsInvalidParentBeforeShiro(t *testing.T) {
	invalidParent := domaintool.ToolExecutionScope{
		RequestID:            "req-parent",
		ActorKind:            domaintool.ActorKindAgent,
		ActorID:              "mio",
		AllowedDataScopes:    []string{domaintool.DataScopeInternal},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
	}
	shiro := &opsScopeCaptureShiro{}
	dispatcher := newMessageRouteDispatcher(
		&mockMioAgent{},
		shiro,
		nil,
		func(string, string, string, string, string, string, string, string, string) {},
		nil,
		func(context.Context, string, routing.Route, string, string) {},
	)
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	tk := newOrchestratorTestTurnInput(t, "運用データを確認して", "viewer", "viewer-user").WithSessionID("session-1")

	if _, err := dispatcher.ExecuteDirect(domaintool.WithToolExecutionScope(context.Background(), invalidParent), tk, routing.RouteOPS, taskID, runID, ""); err == nil {
		t.Fatal("invalid parent scope must stop OPS before Shiro")
	}
	if shiro.calls != 0 {
		t.Fatalf("Shiro calls=%d, want 0 for invalid parent scope", shiro.calls)
	}
}

func TestBindRouteExecutionContextUsesCanonicalRouteActors(t *testing.T) {
	cases := []struct {
		name        string
		route       routing.Route
		recipient   string
		actor       string
		role        string
		purpose     string
		allowInside bool
	}{
		{name: "chat_mio", route: routing.RouteCHAT, recipient: "mio", actor: "mio", role: "agent", purpose: "chat"},
		{name: "chat_shiro", route: routing.RouteCHAT, recipient: "shiro", actor: "shiro", role: "agent", purpose: "chat"},
		{name: "chat_midori", route: routing.RouteCHAT, recipient: "midori", actor: "midori", role: "agent", purpose: "chat"},
		{name: "chat_kuro", route: routing.RouteCHAT, recipient: "kuro", actor: "kuro", role: "agent", purpose: "chat"},
		{name: "plan", route: routing.RoutePLAN, actor: "mio", role: "agent", purpose: "plan"},
		{name: "research", route: routing.RouteRESEARCH, actor: "mio", role: "agent", purpose: "research"},
		{name: "ops", route: routing.RouteOPS, actor: "shiro", role: "worker", purpose: "ops", allowInside: true},
		{name: "code", route: routing.RouteCODE, actor: "shiro", role: "agent", purpose: "code"},
		{name: "code1", route: routing.RouteCODE1, actor: "shiro", role: "agent", purpose: "code1"},
		{name: "code2", route: routing.RouteCODE2, actor: "shiro", role: "agent", purpose: "code2"},
		{name: "code3", route: routing.RouteCODE3, actor: "shiro", role: "agent", purpose: "code3"},
		{name: "code4", route: routing.RouteCODE4, actor: "shiro", role: "agent", purpose: "code4"},
		{name: "wild", route: routing.RouteWILD, actor: "midori", role: "agent", purpose: "wild"},
		{name: "analyze", route: routing.RouteANALYZE, actor: "kuro", role: "agent", purpose: "analyze"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := newOrchestratorTestTurnInput(t, "route binding", "viewer", "viewer-user").WithViewerRecipient(test.recipient)
			taskID := modulecore.NewTaskID()
			runID := modulecore.NewRunID()
			ctx, err := bindRouteExecutionContext(context.Background(), input, test.route, taskID, runID)
			if err != nil {
				t.Fatalf("bindRouteExecutionContext: %v", err)
			}

			identity, err := domainexecution.IdentityFromContext(ctx)
			if err != nil {
				t.Fatalf("IdentityFromContext: %v", err)
			}
			if identity.TaskID != taskID || identity.RunID != runID || identity.TraceID != input.TraceID() {
				t.Fatalf("bound identity = %#v, want task=%s run=%s trace=%s", identity, taskID, runID, input.TraceID())
			}
			scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
			if !ok {
				t.Fatal("bound context has no ToolExecutionScope")
			}
			if err := scope.Validate(); err != nil {
				t.Fatalf("bound scope invalid: %v", err)
			}
			if scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != test.actor || scope.AgentRole != test.role || scope.Purpose != test.purpose {
				t.Fatalf("bound scope = %#v", scope)
			}
			if scope.RequestID == taskID.String() || modulecore.RequestID(scope.RequestID).Validate() != nil {
				t.Fatalf("bound request ID = %q, want fresh canonical RequestID", scope.RequestID)
			}
			if !scope.Allows(domaintool.DataScopePublic) || scope.Allows(domaintool.DataScopeUser) != false || scope.Allows(domaintool.DataScopeInternal) != test.allowInside {
				t.Fatalf("bound data scopes = %#v", scope.AllowedDataScopes)
			}
		})
	}
}

func TestBindRouteExecutionContextPreservesParentRequestAndUserScopes(t *testing.T) {
	parent, err := domaintool.NewToolExecutionScope(
		"req-parent",
		domaintool.ActorKindUser,
		"user-a",
		"user-a",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	input := newOrchestratorTestTurnInput(t, "delegated scope", "viewer", "viewer-user")
	ctx, err := bindRouteExecutionContext(domaintool.WithToolExecutionScope(context.Background(), parent), input, routing.RouteCODE, modulecore.NewTaskID(), modulecore.NewRunID())
	if err != nil {
		t.Fatalf("bindRouteExecutionContext: %v", err)
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		t.Fatal("bound context has no scope")
	}
	if scope.RequestID != parent.RequestID || scope.AuthenticatedUserID != parent.AuthenticatedUserID || !scope.Allows(domaintool.DataScopePublic) || !scope.Allows(domaintool.DataScopeUser) || scope.Allows(domaintool.DataScopeInternal) {
		t.Fatalf("parent privileges were not preserved safely: %#v", scope)
	}
}

func TestBindRouteExecutionContextDoesNotInheritInternalScopeOnNonOPS(t *testing.T) {
	parent := domaintool.ToolExecutionScope{
		RequestID:            "req-parent-agent",
		ActorKind:            domaintool.ActorKindAgent,
		ActorID:              "mio",
		AllowedDataScopes:    []string{domaintool.DataScopePublic, domaintool.DataScopeInternal},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole:            "worker",
		Purpose:              "ops",
	}
	input := newOrchestratorTestTurnInput(t, "delegated internal scope", "viewer", "viewer-user")
	ctx, err := bindRouteExecutionContext(domaintool.WithToolExecutionScope(context.Background(), parent), input, routing.RouteCODE, modulecore.NewTaskID(), modulecore.NewRunID())
	if err != nil {
		t.Fatalf("bindRouteExecutionContext: %v", err)
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Allows(domaintool.DataScopeInternal) {
		t.Fatalf("non-OPS route inherited internal scope: %#v", scope)
	}
}

func TestBindRouteExecutionContextRejectsMalformedParentAndIdentityConflict(t *testing.T) {
	input := newOrchestratorTestTurnInput(t, "invalid binding", "viewer", "viewer-user")
	invalidParent := domaintool.ToolExecutionScope{RequestID: "req-parent", ActorKind: domaintool.ActorKindAgent, ActorID: "mio", AllowedDataScopes: []string{domaintool.DataScopeInternal}, AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator}
	if _, err := bindRouteExecutionContext(domaintool.WithToolExecutionScope(context.Background(), invalidParent), input, routing.RouteOPS, modulecore.NewTaskID(), modulecore.NewRunID()); err == nil {
		t.Fatal("malformed parent scope was accepted")
	}

	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	conflictingTrace := modulecore.NewTraceID()
	if conflictingTrace == input.TraceID() {
		conflictingTrace = modulecore.NewTraceID()
	}
	identityCtx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, conflictingTrace)
	if err != nil {
		t.Fatalf("bind conflicting identity fixture: %v", err)
	}
	if _, err := bindRouteExecutionContext(identityCtx, input, routing.RoutePLAN, taskID, runID); err == nil {
		t.Fatal("existing conflicting identity was accepted")
	}
}
