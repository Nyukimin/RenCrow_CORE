package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

var ErrPrivateWorkCapabilityDenied = errors.New("private work capability denied")

const (
	privateCapabilityCodexRun            = "codex.run"
	privateCapabilityAdvisorRequest      = "advisor.request"
	privateCapabilityNativeCoding        = "shiro_native_coding_v1"
	privateCapabilityImageGenerate       = "image.generate"
	privateCapabilityKuroCodexFullAccess = "kuro.codex.full_access"
)

// validatePrivateWorkEntry requires the Task owner’s current execution context
// and the trusted Agent scope before any private-work provider or Tool is used.
// allowedCapabilities must come from the Task owner; this function validates
// its shape but never derives a grant from request text.
func validatePrivateWorkEntry(
	ctx context.Context,
	input conversation.TurnInput,
	agentID string,
	allowedCapabilities []string,
) (map[string]struct{}, error) {
	if err := input.Validate(); err != nil {
		return nil, fmt.Errorf("private work turn input is invalid: %w", err)
	}
	identity, err := execution.IdentityFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("private work execution identity is invalid: %w", err)
	}
	if identity.TaskID != input.RootTaskID() {
		return nil, fmt.Errorf("private work Task identity does not match TurnInput")
	}
	if identity.TraceID == "" || identity.TraceID != input.TraceID() {
		return nil, fmt.Errorf("private work Trace identity does not match TurnInput")
	}
	scope, ok := tool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("private work requires a trusted Agent ToolExecutionScope")
	}
	if err := scope.Validate(); err != nil {
		return nil, fmt.Errorf("private work ToolExecutionScope is invalid: %w", err)
	}
	if scope.ActorKind != tool.ActorKindAgent || scope.AuthenticationSource != tool.AuthenticationSourceAgentOrchestrator || scope.ActorID != agentID {
		return nil, fmt.Errorf("private work ToolExecutionScope does not authorize Agent %q", agentID)
	}
	if len(allowedCapabilities) > conversation.MaxHandoffCapabilities {
		return nil, fmt.Errorf("%w: too many capabilities", ErrPrivateWorkCapabilityDenied)
	}
	capabilities := make(map[string]struct{}, len(allowedCapabilities))
	for _, capability := range allowedCapabilities {
		if !validPrivateWorkCapability(capability) {
			return nil, fmt.Errorf("%w: invalid capability", ErrPrivateWorkCapabilityDenied)
		}
		if _, exists := capabilities[capability]; exists {
			return nil, fmt.Errorf("%w: duplicate capability %q", ErrPrivateWorkCapabilityDenied, capability)
		}
		capabilities[capability] = struct{}{}
	}
	return capabilities, nil
}

func validPrivateWorkCapability(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

func hasPrivateCapability(capabilities map[string]struct{}, capability string) bool {
	_, ok := capabilities[capability]
	return ok
}

// capabilityScopedToolRunner preserves the Task-owned Tool implementation and
// context while limiting private work to the exact Tool IDs granted by the
// Task owner.
type capabilityScopedToolRunner struct {
	inner   ToolRunner
	allowed map[string]struct{}
}

func (r capabilityScopedToolRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	if r.inner == nil {
		return nil, nil
	}
	metas, err := r.inner.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]tool.ToolMetadata, 0, len(metas))
	for _, meta := range metas {
		if _, ok := r.allowed[meta.ToolID]; ok {
			filtered = append(filtered, meta)
		}
	}
	return filtered, nil
}

func (r capabilityScopedToolRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if r.inner == nil {
		return nil, fmt.Errorf("private work Tool runner is unavailable")
	}
	if _, ok := r.allowed[toolName]; !ok {
		return tool.NewError(tool.ErrPermissionDenied, "private work capability denied for Tool "+toolName, nil), nil
	}
	return r.inner.ExecuteV2(ctx, toolName, args)
}
