package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// trustedToolActionIdempotencyKey identifies one owner-minted Worker action.
// It deliberately excludes AttemptID so retry attempts for the same action
// reuse the durable owner key while separate Tool actions in one HTTP request
// receive distinct keys.
func trustedToolActionIdempotencyKey(ctx context.Context, prefix string, proof tool.ToolExecutionScope) (string, bool) {
	if len(prefix) == 0 || len(prefix) > 120 || proof.RequestID == "" || len(proof.RequestID) > 200 || strings.TrimSpace(proof.RequestID) != proof.RequestID {
		return "", false
	}
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || !scope.Allows(tool.DataScopeUser) || scope.AuthenticatedUserID != proof.AuthenticatedUserID || len(scope.RequestID) > 256 {
		return "", false
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return "", false
	}
	actionID, _, found := domainexecution.BoundActionAttemptFromContext(ctx)
	if !found {
		return "", false
	}
	material := proof.RequestID + "\x00" + scope.RequestID + "\x00" + proof.AuthenticatedUserID + "\x00" + string(identity.TaskID) + "\x00" + string(actionID)
	digest := sha256.Sum256([]byte(material))
	key := prefix + hex.EncodeToString(digest[:])
	if len(key) > 200 {
		return "", false
	}
	return key, true
}
