package tool

import (
	"context"
	"fmt"
	"strings"
)

// ActorKind identifies the CORE actor on whose behalf a Tool is executed.
// Model, provider, runtime and adapter names are deliberately not actor kinds.
type ActorKind string

const (
	ActorKindUser  ActorKind = "user"
	ActorKindAgent ActorKind = "agent"
)

// AuthenticationSource identifies a trusted boundary that authenticated the
// request. Values supplied by a model, Tool argument, Skill or MCP payload are
// never accepted as an authentication source.
type AuthenticationSource string

const (
	AuthenticationSourceHTTP              AuthenticationSource = "http"
	AuthenticationSourceAgentOrchestrator AuthenticationSource = "agent_orchestrator"
)

const (
	DataScopePublic = "public"
	DataScopeUser   = "user"
)

// ToolExecutionScope is the immutable, typed authorization context for a
// Tool call. AuthenticatedUserID is empty for a public-only actor. The scope is
// created at a trusted ingress and transported in context.Context; it is not
// part of a model-visible Tool argument schema.
type ToolExecutionScope struct {
	RequestID            string
	ActorKind            ActorKind
	ActorID              string
	AuthenticatedUserID  string
	AllowedDataScopes    []string
	AuthenticationSource AuthenticationSource
}

// NewToolExecutionScope constructs a trusted scope after normalizing and
// validating its fixed fields. The returned value owns its scope slice.
func NewToolExecutionScope(
	requestID string,
	actorKind ActorKind,
	actorID string,
	authenticatedUserID string,
	allowedDataScopes []string,
	authenticationSource AuthenticationSource,
) (ToolExecutionScope, error) {
	scope := ToolExecutionScope{
		RequestID:            strings.TrimSpace(requestID),
		ActorKind:            ActorKind(strings.TrimSpace(string(actorKind))),
		ActorID:              strings.TrimSpace(actorID),
		AuthenticatedUserID:  strings.TrimSpace(authenticatedUserID),
		AllowedDataScopes:    cleanExecutionScopes(allowedDataScopes),
		AuthenticationSource: AuthenticationSource(strings.TrimSpace(string(authenticationSource))),
	}
	if err := scope.Validate(); err != nil {
		return ToolExecutionScope{}, err
	}
	return scope, nil
}

// Validate enforces the fail-closed execution scope contract.
func (s ToolExecutionScope) Validate() error {
	if s.RequestID == "" {
		return fmt.Errorf("tool execution scope request_id is required")
	}
	if s.ActorID == "" {
		return fmt.Errorf("tool execution scope actor_id is required")
	}
	switch s.ActorKind {
	case ActorKindUser:
		if s.AuthenticatedUserID == "" {
			return fmt.Errorf("user actor requires authenticated_user_id")
		}
		if s.ActorID != s.AuthenticatedUserID {
			return fmt.Errorf("user actor and authenticated_user_id conflict")
		}
	case ActorKindAgent:
	default:
		return fmt.Errorf("unsupported tool execution actor_kind %q", s.ActorKind)
	}
	switch s.AuthenticationSource {
	case AuthenticationSourceHTTP, AuthenticationSourceAgentOrchestrator:
	default:
		return fmt.Errorf("unsupported or untrusted authentication_source %q", s.AuthenticationSource)
	}
	if len(s.AllowedDataScopes) == 0 {
		return fmt.Errorf("tool execution scope allowed_data_scopes is required")
	}
	seen := make(map[string]struct{}, len(s.AllowedDataScopes))
	for _, value := range s.AllowedDataScopes {
		switch value {
		case DataScopePublic:
		case DataScopeUser:
			if s.AuthenticatedUserID == "" {
				return fmt.Errorf("user data scope requires authenticated_user_id")
			}
		default:
			return fmt.Errorf("unsupported data scope %q", value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate data scope %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// Allows reports whether a fixed semantic data scope was granted to this
// execution. It does not grant a scope or perform any identity conversion.
func (s ToolExecutionScope) Allows(dataScope string) bool {
	dataScope = strings.TrimSpace(dataScope)
	for _, allowed := range s.AllowedDataScopes {
		if allowed == dataScope {
			return true
		}
	}
	return false
}

// SearchScope selects the narrowest indexed Knowledge scope available to this
// execution. User scope is preferred when delegated authenticated user access
// is present; public-only scope never falls back to another user's data.
func (s ToolExecutionScope) SearchScope() (string, string, error) {
	if err := s.Validate(); err != nil {
		return "", "", err
	}
	if s.Allows(DataScopeUser) {
		return DataScopeUser, s.AuthenticatedUserID, nil
	}
	if s.Allows(DataScopePublic) {
		return DataScopePublic, "", nil
	}
	return "", "", fmt.Errorf("tool execution scope has no searchable data scope")
}

func cleanExecutionScopes(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		cleaned = append(cleaned, strings.TrimSpace(value))
	}
	return cleaned
}

type toolExecutionScopeContextKey struct{}

// WithToolExecutionScope carries a scope through Chat, Worker, delegate and
// ToolLoop wrappers without exposing it to model-controlled arguments.
func WithToolExecutionScope(ctx context.Context, scope ToolExecutionScope) context.Context {
	return context.WithValue(ctx, toolExecutionScopeContextKey{}, cloneToolExecutionScope(scope))
}

// ToolExecutionScopeFromContext returns only the typed scope installed by a
// trusted caller. Callers must still call Validate before using it because a
// test or internal adapter can place an invalid value in a context.
func ToolExecutionScopeFromContext(ctx context.Context) (ToolExecutionScope, bool) {
	if ctx == nil {
		return ToolExecutionScope{}, false
	}
	scope, ok := ctx.Value(toolExecutionScopeContextKey{}).(ToolExecutionScope)
	if !ok {
		return ToolExecutionScope{}, false
	}
	return cloneToolExecutionScope(scope), true
}

func cloneToolExecutionScope(scope ToolExecutionScope) ToolExecutionScope {
	scope.AllowedDataScopes = append([]string(nil), scope.AllowedDataScopes...)
	return scope
}
