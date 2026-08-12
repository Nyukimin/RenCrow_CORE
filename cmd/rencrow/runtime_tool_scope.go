package main

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// withTrustedAgentPublicToolScope is used only after CORE has selected an
// actual Agent recipient. Viewer-provided user metadata is intentionally not
// converted into a private owner scope here.
func withTrustedAgentPublicToolScope(ctx context.Context, requestID, actorID string) (context.Context, error) {
	scope, err := tool.NewToolExecutionScope(
		requestID,
		tool.ActorKindAgent,
		actorID,
		"",
		[]string{tool.DataScopePublic},
		tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		return nil, err
	}
	return tool.WithToolExecutionScope(ctx, scope), nil
}
