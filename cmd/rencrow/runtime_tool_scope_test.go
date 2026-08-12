package main

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestWithTrustedAgentPublicToolScopeDoesNotTrustViewerUserMetadata(t *testing.T) {
	ctx, err := withTrustedAgentPublicToolScope(context.Background(), "req-viewer-1", "mio")
	if err != nil {
		t.Fatalf("withTrustedAgentPublicToolScope() error = %v", err)
	}
	scope, ok := tool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		t.Fatal("trusted scope missing")
	}
	if scope.ActorKind != tool.ActorKindAgent || scope.ActorID != "mio" || scope.AuthenticatedUserID != "" {
		t.Fatalf("unexpected trusted scope: %#v", scope)
	}
	if !scope.Allows(tool.DataScopePublic) || scope.Allows(tool.DataScopeUser) {
		t.Fatalf("viewer bridge must grant public-only scope: %#v", scope)
	}
}
