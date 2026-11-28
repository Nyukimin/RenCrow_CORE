package tools

import (
	"context"
	"testing"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTrustedToolActionIdempotencyKeySeparatesActionsAndSurvivesAttemptRetry(t *testing.T) {
	ctx, proof := trustedActionIdempotencyTestContext(t)
	actionID := modulecore.NewActionID()
	firstAttempt := modulecore.NewAttemptID()
	firstCtx, err := domainexecution.WithBoundActionAttempt(ctx, actionID, firstAttempt)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := trustedToolActionIdempotencyKey(firstCtx, "core-test-", proof)
	if !ok {
		t.Fatal("first action key was not derived")
	}

	retryCtx, err := domainexecution.WithChildBoundActionAttempt(ctx, actionID, modulecore.NewAttemptID())
	if err != nil {
		t.Fatal(err)
	}
	retry, ok := trustedToolActionIdempotencyKey(retryCtx, "core-test-", proof)
	if !ok || retry != first {
		t.Fatalf("retry key = (%q,%t), want same key %q", retry, ok, first)
	}

	secondCtx, err := domainexecution.WithBoundActionAttempt(ctx, modulecore.NewActionID(), modulecore.NewAttemptID())
	if err != nil {
		t.Fatal(err)
	}
	second, ok := trustedToolActionIdempotencyKey(secondCtx, "core-test-", proof)
	if !ok || second == first {
		t.Fatalf("second action key = (%q,%t), want distinct key from %q", second, ok, first)
	}
}

func TestTrustedToolActionIdempotencyKeyRequiresBoundActionAndMatchingUserScope(t *testing.T) {
	ctx, proof := trustedActionIdempotencyTestContext(t)
	if _, ok := trustedToolActionIdempotencyKey(ctx, "core-test-", proof); ok {
		t.Fatal("key derived without a persisted bound action")
	}
	wrongUser := proof
	wrongUser.AuthenticatedUserID = "other"
	withAction, err := domainexecution.WithBoundActionAttempt(ctx, modulecore.NewActionID(), modulecore.NewAttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := trustedToolActionIdempotencyKey(withAction, "core-test-", wrongUser); ok {
		t.Fatal("key derived for a proof that does not match the delegated user scope")
	}
}

func trustedActionIdempotencyTestContext(t *testing.T) (context.Context, tool.ToolExecutionScope) {
	t.Helper()
	taskID, runID, traceID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, traceID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := tool.NewToolExecutionScope("trace_request", tool.ActorKindAgent, "mio", "ren", []string{tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	ctx = tool.WithToolExecutionScope(ctx, scope)
	proof, err := tool.NewToolExecutionScope("http_request", tool.ActorKindUser, "ren", "ren", []string{tool.DataScopeUser}, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, proof
}
