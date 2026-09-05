package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
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
	tk := newOrchestratorTestTurnInput(t, "運用データを確認して", "line", "user-a").WithSessionID("session-1")

	response, err := dispatcher.ExecuteDirect(parentCtx, tk, routing.RouteOPS, taskID, "")
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
	if got.RequestID != taskID.String() || got.ActorKind != domaintool.ActorKindAgent || got.ActorID != "shiro" {
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
	tk := newOrchestratorTestTurnInput(t, "運用データを確認して", "viewer", "viewer-user").WithSessionID("session-1")

	if _, err := dispatcher.ExecuteDirect(domaintool.WithToolExecutionScope(context.Background(), invalidParent), tk, routing.RouteOPS, taskID, ""); err == nil {
		t.Fatal("invalid parent scope must stop OPS before Shiro")
	}
	if shiro.calls != 0 {
		t.Fatalf("Shiro calls=%d, want 0 for invalid parent scope", shiro.calls)
	}
}
